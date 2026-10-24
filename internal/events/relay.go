// Adapted from Porch internal/events/relay.go (Apache-2.0).

// Package events relays outbox rows to Pulse (NATS JetStream) after their transaction committed (ADR-007). Each event
// keeps its outbox ID as the CloudEvents ID / Nats-Msg-Id, so consumers can deduplicate retries. A Pulse outage never
// blocks Deed: events wait in the outbox, retry with capped exponential backoff and are dead-lettered after 50
// attempts or 24 hours. The backlog is bounded: beyond MaxBacklog unpublished rows the oldest are dead-lettered.
package events

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Publisher is the part of the Pulse client the relay uses.
type Publisher interface {
	Publish(ctx context.Context, subject, typ string, data any, opts ...pulse.PublishOption) (*pulse.PubAck, error)
}

// Limits of the retry policy.
const (
	MaxAttempts = 50
	MaxAge      = 24 * time.Hour
	MaxBackoff  = 5 * time.Minute
	MaxBacklog  = 100_000
	batch       = 100
)

// Relay publishes due outbox rows.
type Relay struct {
	Store   *store.Store
	Pub     Publisher
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Retention is how long published and dead rows are kept.
	Retention time.Duration
}

// Backoff is the delay before attempt n+1 (n ≥ 1): 2^(n-1) s with ±50 % jitter, capped at MaxBackoff.
func Backoff(attempts int64, jitter float64) time.Duration {
	d := time.Second << min(attempts-1, 20)
	d = time.Duration(float64(d) * (0.5 + jitter))
	return min(d, MaxBackoff)
}

type row struct {
	id, typ, subject, payload string
	created, attempts         int64
}

// Once publishes up to one batch of due events and returns how many were published and how many failed.
func (r *Relay) Once(ctx context.Context) (published, failed int, err error) {
	now := r.Clock.Now()
	rows, err := r.Store.R().QueryContext(ctx, `SELECT id, type, subject, payload_json, created_at, attempts FROM outbox
		WHERE published_at IS NULL AND dead_at IS NULL AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`, now.UnixMicro(), batch)
	if err != nil {
		return 0, 0, err
	}
	var due []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.typ, &x.subject, &x.payload, &x.created, &x.attempts); err != nil {
			rows.Close()
			return 0, 0, err
		}
		due = append(due, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, x := range due {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, perr := r.Pub.Publish(pctx, x.subject, x.typ, json.RawMessage(x.payload), pulse.WithID(x.id), pulse.WithTime(clock.FromMicros(x.created)))
		cancel()
		if ctx.Err() != nil {
			return published, failed, ctx.Err()
		}
		if perr == nil {
			published++
			r.Metrics.OutboxPublish("ok")
			if err := r.mark(ctx, `UPDATE outbox SET published_at = ?, attempts = attempts + 1 WHERE id = ?`, now.UnixMicro(), x.id); err != nil {
				return published, failed, err
			}
			continue
		}
		failed++
		msg := perr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		attempts := x.attempts + 1
		dead := attempts >= MaxAttempts || now.Sub(clock.FromMicros(x.created)) >= MaxAge
		if dead {
			r.Metrics.OutboxPublish("dead")
			r.Log.Error("event dead-lettered", "event.id", x.id, "event.type", x.typ, "attempts", attempts, "error.message", msg)
			err = r.mark(ctx, `UPDATE outbox SET dead_at = ?, attempts = ?, last_error = ? WHERE id = ?`, now.UnixMicro(), attempts, msg, x.id)
		} else {
			r.Metrics.OutboxPublish("error")
			err = r.mark(ctx, `UPDATE outbox SET next_attempt_at = ?, attempts = ?, last_error = ? WHERE id = ?`,
				now.Add(Backoff(attempts, jitter())).UnixMicro(), attempts, msg, x.id)
		}
		if err != nil {
			return published, failed, err
		}
		// One failure usually means Pulse is unreachable: stop this round instead of timing out on every row.
		break
	}
	return published, failed, nil
}

// jitter returns a uniform value in [0, 1).
func jitter() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

func (r *Relay) mark(ctx context.Context, q string, args ...any) error {
	return r.Store.W().TxNamed(context.WithoutCancel(ctx), "outbox.mark", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, q, args...)
		return err
	})
}

// Maintain enforces the backlog bound and retention and exports backlog metrics.
func (r *Relay) Maintain(ctx context.Context) error {
	now := r.Clock.Now()
	err := r.Store.W().TxNamed(ctx, "outbox.maintain", func(tx *store.Tx) error {
		var pending int64
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&pending); err != nil {
			return err
		}
		if over := pending - MaxBacklog; over > 0 {
			if _, err := tx.Exec(ctx, `UPDATE outbox SET dead_at = ?, last_error = 'backlog limit exceeded' WHERE id IN (
				SELECT id FROM outbox WHERE published_at IS NULL AND dead_at IS NULL ORDER BY created_at LIMIT ?)`, tx.NowMicros(), over); err != nil {
				return err
			}
			r.Log.Error("outbox backlog limit exceeded; oldest events dead-lettered", "count", over)
		}
		ret := r.Retention
		if ret <= 0 {
			ret = 7 * 24 * time.Hour
		}
		_, err := tx.Exec(ctx, `DELETE FROM outbox WHERE (published_at IS NOT NULL AND published_at < ?) OR (dead_at IS NOT NULL AND dead_at < ?)`,
			tx.Now.Add(-ret).UnixMicro(), tx.Now.Add(-ret).UnixMicro())
		return err
	})
	if err != nil {
		return err
	}
	var pending, dead int64
	var oldest sql.NullInt64
	if err := r.Store.R().QueryRowContext(ctx, `SELECT
		COALESCE(SUM(published_at IS NULL AND dead_at IS NULL), 0), COALESCE(SUM(dead_at IS NOT NULL), 0),
		MIN(CASE WHEN published_at IS NULL AND dead_at IS NULL THEN created_at END) FROM outbox`).Scan(&pending, &dead, &oldest); err != nil {
		return err
	}
	age := time.Duration(0)
	if oldest.Valid {
		age = now.Sub(clock.FromMicros(oldest.Int64))
	}
	r.Metrics.SetOutbox(pending, dead, age)
	return nil
}

// Run publishes until ctx ends, polling every interval and draining backlogs without waiting.
func (r *Relay) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			n, failed, err := r.Once(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				r.Log.Warn("event relay", "error.message", err.Error())
			}
			if err != nil || failed > 0 || n < batch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Status summarizes the outbox for the operations API.
type Status struct {
	Pending       int64      `json:"pending"`
	Dead          int64      `json:"dead"`
	Published24h  int64      `json:"published_24h"`
	OldestPending *time.Time `json:"oldest_pending,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// GetStatus reads the outbox status.
func GetStatus(ctx context.Context, d *sql.DB, now time.Time) (Status, error) {
	var s Status
	var oldest sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT COALESCE(SUM(published_at IS NULL AND dead_at IS NULL), 0), COALESCE(SUM(dead_at IS NOT NULL), 0),
		COALESCE(SUM(published_at >= ?), 0), MIN(CASE WHEN published_at IS NULL AND dead_at IS NULL THEN created_at END) FROM outbox`,
		now.Add(-24*time.Hour).UnixMicro()).Scan(&s.Pending, &s.Dead, &s.Published24h, &oldest)
	if err != nil {
		return s, err
	}
	if oldest.Valid {
		t := clock.FromMicros(oldest.Int64)
		s.OldestPending = &t
	}
	_ = d.QueryRowContext(ctx, `SELECT COALESCE(last_error, '') FROM outbox WHERE last_error IS NOT NULL ORDER BY created_at DESC LIMIT 1`).Scan(&s.LastError)
	return s, nil
}
