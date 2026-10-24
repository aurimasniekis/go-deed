// Adapted from Porch internal/outbox/outbox.go (Apache-2.0).

// Package outbox records domain events in the same write transaction as the change they describe (ADR-007). The
// relay (internal/events) publishes them to Pulse later; publishing never blocks or fails a request.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/obs/redact"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Event is one domain event. Payload must contain only non-secret, non-spec summary fields (IDs, names, kinds,
// projects, change summaries); keys that look secret are rejected.
type Event struct {
	// Area, Entity and Action name the event: type deed.<area>.<entity>_<action>.v1, subject
	// evt.deed.<area>.<entity>.<action>.
	Area, Entity, Action string
	ProjectID            string
	Payload              map[string]any
}

var segment = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Type returns the CloudEvents type, e.g. deed.catalog.entity_updated.v1.
func (e Event) Type() string { return "deed." + e.Area + "." + e.Entity + "_" + e.Action + ".v1" }

// Subject returns the NATS subject, e.g. evt.deed.catalog.entity.updated.
func (e Event) Subject() string { return "evt.deed." + e.Area + "." + e.Entity + "." + e.Action }

// Enqueue stores ev inside tx and returns its stable ID (the event ID consumers deduplicate on).
func Enqueue(ctx context.Context, tx *store.Tx, ev Event) (string, error) {
	for _, s := range []string{ev.Area, ev.Entity, ev.Action} {
		if !segment.MatchString(s) {
			return "", fmt.Errorf("outbox: invalid event name segment %q", s)
		}
	}
	for k := range ev.Payload {
		if redact.IsSensitiveKey(k) {
			return "", fmt.Errorf("outbox: sensitive key %q in event payload", k)
		}
	}
	payload := ev.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	if ev.ProjectID != "" {
		payload["project_id"] = ev.ProjectID
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := ids.New(ids.Outbox)
	_, err = tx.Exec(ctx, `INSERT INTO outbox (id, type, subject, project_id, payload_json, created_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, ev.Type(), ev.Subject(), sql.NullString{String: ev.ProjectID, Valid: ev.ProjectID != ""}, string(b), tx.NowMicros(), tx.NowMicros())
	return id, err
}
