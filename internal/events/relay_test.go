package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/events"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/store/storetest"
)

type published struct {
	subject, typ string
	data         json.RawMessage
	id           string
}

// fakePub records publishes and fails while err is set.
type fakePub struct {
	mu    sync.Mutex
	calls []published
	err   error
}

func (f *fakePub) Publish(_ context.Context, subject, typ string, data any, opts ...pulse.PublishOption) (*pulse.PubAck, error) {
	id := msgID(opts)
	raw, _ := data.(json.RawMessage)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, published{subject: subject, typ: typ, data: raw, id: id})
	if f.err != nil {
		return nil, f.err
	}
	return &pulse.PubAck{ID: id, Stream: "EVENTS", Sequence: uint64(len(f.calls))}, nil
}

func (f *fakePub) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// msgID evaluates pulse publish options against a fresh (unexported) options struct and reads back the event ID the
// real client sends as ce-id / Nats-Msg-Id. go-pulse offers no exported accessor, so this uses reflection.
func msgID(opts []pulse.PublishOption) string {
	if len(opts) == 0 {
		return ""
	}
	p := reflect.New(reflect.TypeOf(opts[0]).In(0).Elem())
	for _, o := range opts {
		reflect.ValueOf(o).Call([]reflect.Value{p})
	}
	return p.Elem().FieldByName("id").String()
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*store.Store, *clock.Fake, *fakePub, *events.Relay) {
	t.Helper()
	c := clock.NewFake(t0)
	s := storetest.Open(t, storetest.WithClock(c))
	pub := &fakePub{}
	r := &events.Relay{Store: s, Pub: pub, Clock: c, Log: slog.New(slog.DiscardHandler)}
	return s, c, pub, r
}

func enqueue(t *testing.T, s *store.Store, name string) string {
	t.Helper()
	var id string
	err := s.W().Tx(context.Background(), func(tx *store.Tx) error {
		var err error
		id, err = outbox.Enqueue(context.Background(), tx, outbox.Event{Area: "catalog", Entity: "entity", Action: "created", ProjectID: "prj_1",
			Payload: map[string]any{"name": name}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type rowState struct {
	attempts          int64
	next              int64
	lastError         sql.NullString
	published, deadAt sql.NullInt64
}

func state(t *testing.T, s *store.Store, id string) rowState {
	t.Helper()
	var r rowState
	if err := s.R().QueryRowContext(context.Background(), `SELECT attempts, next_attempt_at, last_error, published_at, dead_at FROM outbox WHERE id = ?`, id).
		Scan(&r.attempts, &r.next, &r.lastError, &r.published, &r.deadAt); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRelayPublishesWithOutboxID(t *testing.T) {
	s, c, pub, r := setup(t)
	ctx := context.Background()
	id1 := enqueue(t, s, "web-1")
	c.Advance(time.Second)
	id2 := enqueue(t, s, "web-2")

	n, failed, err := r.Once(ctx)
	if err != nil || n != 2 || failed != 0 {
		t.Fatalf("Once = %d, %d, %v", n, failed, err)
	}
	if pub.n() != 2 {
		t.Fatalf("publishes %d", pub.n())
	}
	for i, want := range []string{id1, id2} {
		p := pub.calls[i]
		if p.id != want {
			t.Fatalf("publish %d: message ID %q, want outbox ID %q", i, p.id, want)
		}
		if p.subject != "evt.deed.catalog.entity.created" || p.typ != "deed.catalog.entity_created.v1" {
			t.Fatalf("publish %d: %s %s", i, p.subject, p.typ)
		}
		var payload map[string]any
		if err := json.Unmarshal(p.data, &payload); err != nil || payload["project_id"] != "prj_1" {
			t.Fatalf("publish %d payload %s (%v)", i, p.data, err)
		}
		st := state(t, s, want)
		if !st.published.Valid || st.published.Int64 != c.Now().UnixMicro() || st.attempts != 1 || st.deadAt.Valid {
			t.Fatalf("row %s after publish: %+v", want, st)
		}
	}
	// Published rows are not sent again.
	if n, failed, err := r.Once(ctx); err != nil || n != 0 || failed != 0 || pub.n() != 2 {
		t.Fatalf("second Once = %d, %d, %v (publishes %d)", n, failed, err, pub.n())
	}
}

func TestRelayRetriesWithBackoff(t *testing.T) {
	s, c, pub, r := setup(t)
	ctx := context.Background()
	id1 := enqueue(t, s, "web-1")
	id2 := enqueue(t, s, "web-2")
	pub.err = errors.New("nats: no responders available for request")

	n, failed, err := r.Once(ctx)
	if err != nil || n != 0 || failed != 1 {
		t.Fatalf("Once = %d, %d, %v", n, failed, err)
	}
	// The round stops at the first failure instead of timing out on every row.
	if pub.n() != 1 {
		t.Fatalf("publishes %d, want 1", pub.n())
	}
	first := pub.calls[0].id
	st := state(t, s, first)
	now := c.Now()
	if st.attempts != 1 || st.published.Valid || st.deadAt.Valid || !strings.Contains(st.lastError.String, "no responders") {
		t.Fatalf("failed row %+v", st)
	}
	// First retry after 1 s ± 50 %.
	lo, hi := now.Add(500*time.Millisecond).UnixMicro(), now.Add(1500*time.Millisecond).UnixMicro()
	if st.next < lo || st.next > hi {
		t.Fatalf("next attempt %v, want within [0.5s, 1.5s]", clock.FromMicros(st.next).Sub(now))
	}
	other := id1
	if first == id1 {
		other = id2
	}
	if st := state(t, s, other); st.attempts != 0 || st.lastError.Valid {
		t.Fatalf("untouched row changed %+v", st)
	}

	// Still failing: the backed-off row waits; the other row is tried and backs off too.
	if _, failed, _ := r.Once(ctx); failed != 1 || pub.n() != 2 || pub.calls[1].id != other {
		t.Fatalf("second round: failed %d, publishes %d", failed, pub.n())
	}
	if _, failed, _ := r.Once(ctx); failed != 0 || pub.n() != 2 {
		t.Fatalf("not-yet-due rows retried early: failed %d publishes %d", failed, pub.n())
	}

	// Pulse recovers; after the backoff both rows publish with the same IDs (consumers deduplicate on them).
	pub.err = nil
	c.Advance(2 * time.Second)
	n, failed, err = r.Once(ctx)
	if err != nil || n != 2 || failed != 0 {
		t.Fatalf("recovery Once = %d, %d, %v", n, failed, err)
	}
	ids := map[string]int{}
	for _, p := range pub.calls {
		ids[p.id]++
	}
	if ids[id1] != 2 || ids[id2] != 2 {
		t.Fatalf("publish IDs %v", ids)
	}
	if st := state(t, s, first); st.attempts != 2 || !st.published.Valid {
		t.Fatalf("recovered row %+v", st)
	}
}

func TestBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempts int64
		jitter   float64
		want     time.Duration
	}{
		{1, 0, 500 * time.Millisecond},
		{1, 0.5, time.Second},
		{2, 0.5, 2 * time.Second},
		{3, 0, 2 * time.Second},
		{8, 0.5, 128 * time.Second},
		{9, 0.5, 256 * time.Second},
		{10, 0.5, events.MaxBackoff},
		{events.MaxAttempts, 0.999, events.MaxBackoff},
		{1 << 40, 0.999, events.MaxBackoff},
	} {
		if got := events.Backoff(tc.attempts, tc.jitter); got != tc.want {
			t.Errorf("Backoff(%d, %v) = %s, want %s", tc.attempts, tc.jitter, got, tc.want)
		}
	}
	prev := time.Duration(0)
	for a := int64(1); a <= events.MaxAttempts; a++ {
		d := events.Backoff(a, 0.5)
		if d < prev || d > events.MaxBackoff {
			t.Fatalf("Backoff(%d) = %s after %s", a, d, prev)
		}
		prev = d
	}
}

func TestRelayDeadLettersAfterMaxAttempts(t *testing.T) {
	s, _, pub, r := setup(t)
	ctx := context.Background()
	id := enqueue(t, s, "web-1")
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE outbox SET attempts = ? WHERE id = ?`, events.MaxAttempts-2, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pub.err = errors.New("pulse down")
	if _, failed, err := r.Once(ctx); err != nil || failed != 1 {
		t.Fatalf("Once: failed %d, %v", failed, err)
	}
	if st := state(t, s, id); st.deadAt.Valid || st.attempts != events.MaxAttempts-1 {
		t.Fatalf("dead-lettered too early: %+v", st)
	}
	if err := s.W().Tx(ctx, func(tx *store.Tx) error { // make it due again without waiting out the backoff
		_, err := tx.Exec(ctx, `UPDATE outbox SET next_attempt_at = 0 WHERE id = ?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, failed, err := r.Once(ctx); err != nil || failed != 1 {
		t.Fatalf("Once: failed %d, %v", failed, err)
	}
	st := state(t, s, id)
	if !st.deadAt.Valid || st.attempts != events.MaxAttempts || st.published.Valid || st.lastError.String != "pulse down" {
		t.Fatalf("row after final attempt %+v", st)
	}
	// Dead rows are never retried, even once Pulse is back.
	pub.err = nil
	calls := pub.n()
	if n, failed, err := r.Once(ctx); err != nil || n != 0 || failed != 0 || pub.n() != calls {
		t.Fatalf("dead row retried: %d %d %v", n, failed, err)
	}
	stat, err := events.GetStatus(ctx, s.R(), t0)
	if err != nil || stat.Dead != 1 || stat.Pending != 0 || stat.LastError != "pulse down" {
		t.Fatalf("status %+v (%v)", stat, err)
	}
}

func TestRelayDeadLettersAfterMaxAge(t *testing.T) {
	s, c, pub, r := setup(t)
	ctx := context.Background()
	id := enqueue(t, s, "web-1")
	c.Advance(events.MaxAge)
	pub.err = errors.New("pulse down")
	if _, failed, err := r.Once(ctx); err != nil || failed != 1 {
		t.Fatalf("Once: failed %d, %v", failed, err)
	}
	if st := state(t, s, id); !st.deadAt.Valid || st.attempts != 1 {
		t.Fatalf("expired row not dead-lettered on first failure: %+v", st)
	}
}

func TestRelayMaintainBoundsBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts MaxBacklog rows")
	}
	if raceEnabled {
		// Maintain scans the whole outbox (~65 ms at MaxBacklog rows natively); the race detector slows the transpiled
		// SQLite far beyond the transaction guard's race allowance.
		t.Skip("MaxBacklog-sized outbox is too slow under the race detector")
	}
	s, _, _, r := setup(t)
	ctx := context.Background()
	const extra = 3
	// Bulk-insert MaxBacklog+extra pending rows, oldest first (an unnamed fixture transaction).
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
			INSERT INTO outbox (id, type, subject, payload_json, created_at, next_attempt_at)
			SELECT printf('evt_%010d', i), 'deed.test.row_created.v1', 'evt.deed.test.row.created', '{}', ? + i, ? + i FROM n`,
			events.MaxBacklog+extra, t0.UnixMicro()-1_000_000, t0.UnixMicro()-1_000_000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.R().QueryContext(ctx, `SELECT id, last_error FROM outbox WHERE dead_at IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var dead []string
	for rows.Next() {
		var id, msg string
		if err := rows.Scan(&id, &msg); err != nil {
			t.Fatal(err)
		}
		if msg != "backlog limit exceeded" {
			t.Fatalf("last_error %q", msg)
		}
		dead = append(dead, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(dead, ",") != "evt_0000000001,evt_0000000002,evt_0000000003" {
		t.Fatalf("dead-lettered %v, want the %d oldest", dead, extra)
	}
	stat, err := events.GetStatus(ctx, s.R(), t0)
	if err != nil || stat.Pending != events.MaxBacklog || stat.Dead != extra {
		t.Fatalf("status %+v (%v)", stat, err)
	}
	// At the limit nothing more is dropped.
	if err := r.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if stat, _ := events.GetStatus(ctx, s.R(), t0); stat.Dead != extra {
		t.Fatalf("second Maintain dead %d", stat.Dead)
	}
}

func TestRelayMaintainRetention(t *testing.T) {
	s, c, pub, r := setup(t)
	ctx := context.Background()
	r.Retention = time.Hour

	oldPub := enqueue(t, s, "old-published")
	oldDead := enqueue(t, s, "old-dead")
	oldPending := enqueue(t, s, "old-pending")
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = ? WHERE id = ?`, t0.UnixMicro(), oldPub); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET dead_at = ? WHERE id = ?`, t0.UnixMicro(), oldDead); err != nil {
			return err
		}
		// Keep the pending row from being published below.
		_, err := tx.Exec(ctx, `UPDATE outbox SET next_attempt_at = ? WHERE id = ?`, t0.Add(48*time.Hour).UnixMicro(), oldPending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c.Advance(2 * time.Hour)
	fresh := enqueue(t, s, "fresh")
	if n, _, err := r.Once(ctx); err != nil || n != 1 || pub.calls[0].id != fresh {
		t.Fatalf("Once = %d, %v", n, err)
	}
	if err := r.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	rows, err := s.R().QueryContext(ctx, `SELECT id FROM outbox`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if remaining[oldPub] || remaining[oldDead] || !remaining[oldPending] || !remaining[fresh] || len(remaining) != 2 {
		t.Fatalf("remaining %v (old published %s, old dead %s, pending %s, fresh %s)", remaining, oldPub, oldDead, oldPending, fresh)
	}
	stat, err := events.GetStatus(ctx, s.R(), c.Now())
	if err != nil || stat.Pending != 1 || stat.Dead != 0 || stat.Published24h != 1 || stat.OldestPending == nil || !stat.OldestPending.Equal(t0) {
		t.Fatalf("status %+v (%v)", stat, err)
	}
}

func TestRelayRunStopsOnCancel(t *testing.T) {
	s, _, pub, r := setup(t)
	enqueue(t, s, "web-1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for pub.n() == 0 {
		select {
		case <-deadline:
			t.Fatal("Run did not publish")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
