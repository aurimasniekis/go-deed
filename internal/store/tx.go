// Adapted from Porch (Apache-2.0), which adapted HallPass internal/store/tx.go.

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
)

// Retry policy for SQLITE_BUSY on the writer.
const (
	txMaxAttempts = 5
	txBaseBackoff = 10 * time.Millisecond
	txMaxBackoff  = time.Second
)

// TxObserver receives the duration and final error of each write transaction.
type TxObserver func(name string, d time.Duration, err error)

// Writer is the single write connection. All mutations go through Tx.
type Writer struct {
	db       *sql.DB
	clock    clock.Clock
	health   *Health
	observer TxObserver
	metrics  *metrics.Metrics
}

// Tx is a write transaction. Its callback must only touch the database: no network, SSH, file fsync,
// channel operations or waits.
type Tx struct {
	// SQL is the raw transaction.
	SQL *sql.Tx
	// Now is the transaction's timestamp, taken after BEGIN IMMEDIATE acquired the write lock.
	Now time.Time

	afterCommit []func()
}

// AfterCommit registers fn to run after a successful commit (e.g. publishing events). Hooks from failed or
// retried attempts never run.
func (t *Tx) AfterCommit(fn func()) { t.afterCommit = append(t.afterCommit, fn) }

// NowMicros is Now as persisted Unix microseconds.
func (t *Tx) NowMicros() int64 { return t.Now.UnixMicro() }

// Exec runs a statement inside the transaction.
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.SQL.ExecContext(ctx, query, args...)
}

// ExecN runs a statement and returns the number of affected rows.
func (t *Tx) ExecN(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := t.SQL.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueryRow runs a single-row query inside the transaction.
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return t.SQL.QueryRowContext(ctx, query, args...)
}

// Query runs a query inside the transaction.
func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.SQL.QueryContext(ctx, query, args...)
}

// Tx runs fn in a BEGIN IMMEDIATE transaction, retrying the whole callback on SQLITE_BUSY with jittered backoff.
// fn must be safe to re-run. Returned errors are classified (ErrBusy, ErrFull, ErrIO, ErrCorrupt, ErrConstraint).
func (w *Writer) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	return w.TxNamed(ctx, "", fn)
}

// TxNamed is Tx with a name for observability.
func (w *Writer) TxNamed(ctx context.Context, name string, fn func(tx *Tx) error) error {
	start := time.Now()
	var err error
	for attempt := 1; ; attempt++ {
		var hooks []func()
		var held time.Duration
		hooks, held, err = w.attempt(ctx, fn)
		if err == nil {
			w.health.Observe(nil)
			// The short-transaction rule is about how long the write lock is held, not how long callers queued for it.
			w.observe(name, held, nil)
			for _, h := range hooks {
				h()
			}
			return nil
		}
		w.health.Observe(err)
		if !errors.Is(err, ErrBusy) || attempt >= txMaxAttempts || ctx.Err() != nil {
			break
		}
		w.metrics.StoreTxBusyRetry()
		if serr := sleepCtx(ctx, backoff(attempt)); serr != nil {
			err = errors.Join(err, serr)
			break
		}
	}
	w.observe(name, time.Since(start), err)
	return err
}

// observe reports a finished transaction to the observer and metrics.
func (w *Writer) observe(name string, d time.Duration, err error) {
	if w.observer != nil {
		w.observer(name, d, err)
	}
	w.metrics.StoreTx(name, d, TxResult(err))
}

// TxResult classifies a transaction error for metrics.
func TxResult(err error) metrics.TxResult {
	switch {
	case err == nil:
		return metrics.TxOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return metrics.TxCanceled
	case errors.Is(err, ErrBusy):
		return metrics.TxBusy
	case errors.Is(err, ErrFull):
		return metrics.TxFull
	case errors.Is(err, ErrIO):
		return metrics.TxIO
	case errors.Is(err, ErrCorrupt):
		return metrics.TxCorrupt
	case errors.Is(err, ErrConstraint):
		return metrics.TxConstraint
	case errors.Is(err, ErrConflict):
		return metrics.TxConflict
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return metrics.TxNotFound
	default:
		return metrics.TxError
	}
}

func (w *Writer) attempt(ctx context.Context, fn func(tx *Tx) error) (hooks []func(), held time.Duration, err error) {
	waitStart := time.Now()
	w.metrics.StoreWriterQueue(1)
	sqlTx, err := w.db.BeginTx(ctx, nil)
	w.metrics.StoreWriterQueue(-1)
	w.metrics.StoreWriterWait(time.Since(waitStart)) // pool wait + BEGIN IMMEDIATE lock acquisition
	if err != nil {
		return nil, 0, classify(fmt.Errorf("begin: %w", err))
	}
	locked := time.Now()
	tx := &Tx{SQL: sqlTx, Now: w.clock.Now().UTC()}
	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return nil, time.Since(locked), classify(err)
	}
	if err := sqlTx.Commit(); err != nil {
		_ = sqlTx.Rollback()
		return nil, time.Since(locked), classify(fmt.Errorf("commit: %w", err))
	}
	return tx.afterCommit, time.Since(locked), nil
}

// spaceProbe commits a transaction that allocates 64 KiB of new pages and frees them again: it succeeds only when
// the database can grow (disk-full recovery).
func (w *Writer) spaceProbe(ctx context.Context) error {
	return w.TxNamed(ctx, "store.space_probe", func(tx *Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO store_space_probe (id, data) VALUES (1, zeroblob(65536))`); err != nil {
			return err
		}
		_, err := tx.SQL.ExecContext(ctx, `DELETE FROM store_space_probe`)
		return err
	})
}

// Exec runs a statement outside an explicit transaction on the writer (maintenance: VACUUM, checkpoints).
func (w *Writer) Exec(ctx context.Context, query string, args ...any) error {
	_, err := w.db.ExecContext(ctx, query, args...)
	err = classify(err)
	w.health.Observe(err)
	return err
}

func backoff(attempt int) time.Duration {
	ceiling := min(txBaseBackoff<<(attempt-1), txMaxBackoff)
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(ceiling)) //nolint:gosec // ceiling > 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
