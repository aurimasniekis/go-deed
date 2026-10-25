package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/store/storetest"
)

func count(t *testing.T, s *store.Store, id string) int {
	t.Helper()
	var n int
	if err := s.R().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outbox WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEventNames(t *testing.T) {
	ev := outbox.Event{Area: "catalog", Entity: "entity", Action: "updated"}
	if ev.Type() != "deed.catalog.entity_updated.v1" || ev.Subject() != "evt.deed.catalog.entity.updated" {
		t.Fatalf("type %q subject %q", ev.Type(), ev.Subject())
	}
}

func TestEnqueueVisibleOnlyAfterCommit(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := storetest.Open(t, storetest.WithClock(clock.NewFake(now)))
	ctx := context.Background()
	ev := outbox.Event{Area: "catalog", Entity: "entity", Action: "created", ProjectID: "prj_1",
		Payload: map[string]any{"entity_id": "ent_1", "name": "web-1"}}

	var id string
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		var err error
		if id, err = outbox.Enqueue(ctx, tx, ev); err != nil {
			return err
		}
		// Readers see only committed state.
		if n := count(t, s, id); n != 0 {
			t.Errorf("uncommitted outbox row visible to readers (%d)", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "evt_") {
		t.Fatalf("id %q", id)
	}
	if n := count(t, s, id); n != 1 {
		t.Fatalf("committed row count %d", n)
	}
	var typ, subject, payload string
	var project sql.NullString
	var created, next, attempts int64
	var published, dead sql.NullInt64
	if err := s.R().QueryRowContext(ctx, `SELECT type, subject, project_id, payload_json, created_at, next_attempt_at, attempts, published_at, dead_at
		FROM outbox WHERE id = ?`, id).Scan(&typ, &subject, &project, &payload, &created, &next, &attempts, &published, &dead); err != nil {
		t.Fatal(err)
	}
	if typ != "deed.catalog.entity_created.v1" || subject != "evt.deed.catalog.entity.created" || project.String != "prj_1" {
		t.Fatalf("row %s %s %v", typ, subject, project)
	}
	if created != now.UnixMicro() || next != created || attempts != 0 || published.Valid || dead.Valid {
		t.Fatalf("row state created=%d next=%d attempts=%d published=%v dead=%v", created, next, attempts, published, dead)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if p["project_id"] != "prj_1" || p["entity_id"] != "ent_1" || p["name"] != "web-1" {
		t.Fatalf("payload %v", p)
	}
}

func TestEnqueueRolledBackWithTransaction(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	boom := errors.New("boom")
	var id string
	err := s.W().Tx(ctx, func(tx *store.Tx) error {
		var err error
		if id, err = outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "entity", Action: "deleted"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if id == "" || count(t, s, id) != 0 {
		t.Fatalf("rolled-back event %q persisted", id)
	}
	// An event without project or payload stores an empty JSON object.
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		id, err = outbox.Enqueue(ctx, tx, outbox.Event{Area: "ops", Entity: "backup", Action: "finished"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var payload string
	var project sql.NullString
	if err := s.R().QueryRowContext(ctx, `SELECT payload_json, project_id FROM outbox WHERE id = ?`, id).Scan(&payload, &project); err != nil {
		t.Fatal(err)
	}
	if payload != "{}" || project.Valid {
		t.Fatalf("payload %q project %v", payload, project)
	}
}

func TestEnqueueRejectsBadEvents(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for name, ev := range map[string]outbox.Event{
		"empty area":       {Area: "", Entity: "entity", Action: "created"},
		"upper case":       {Area: "Catalog", Entity: "entity", Action: "created"},
		"dot in segment":   {Area: "catalog", Entity: "entity.x", Action: "created"},
		"leading digit":    {Area: "catalog", Entity: "entity", Action: "1created"},
		"wildcard":         {Area: "catalog", Entity: "*", Action: "created"},
		"password payload": {Area: "catalog", Entity: "entity", Action: "created", Payload: map[string]any{"password": "x"}},
		"token payload":    {Area: "catalog", Entity: "entity", Action: "created", Payload: map[string]any{"api_token": "x"}},
		"reason payload":   {Area: "catalog", Entity: "entity", Action: "created", Payload: map[string]any{"reason": "x"}},
	} {
		err := s.W().Tx(ctx, func(tx *store.Tx) error {
			_, err := outbox.Enqueue(ctx, tx, ev)
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var n int
	if err := s.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("outbox rows %d (%v)", n, err)
	}
}
