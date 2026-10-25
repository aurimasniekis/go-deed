package pulseint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// TailType is the provider name of the tail aggregator.
const TailType = "pulse_tail"

// TailConfig configures a tail source.
type TailConfig struct {
	Filter        string `json:"filter,omitempty"`
	WindowSeconds int    `json:"window_seconds,omitempty"`
	MaxKeys       int    `json:"max_keys,omitempty"`
}

const tailSchema = `{
  "type": "object", "additionalProperties": false,
  "properties": {
    "filter": {"type": "string", "default": "evt.>", "description": "Subject filter of the read-only subscription."},
    "window_seconds": {"type": "integer", "minimum": 15, "maximum": 3600, "default": 60, "description": "Aggregation window."},
    "max_keys": {"type": "integer", "minimum": 10, "maximum": 50000, "default": 5000, "description": "Distinct (publisher, subject template, type) keys per window; the rest are counted as overflow."}
  }
}`

// TailProvider describes pulse_tail sources; the Tail runs them continuously.
type TailProvider struct{}

// Describe implements sources.Provider.
func (TailProvider) Describe() sources.Descriptor {
	return sources.Descriptor{Type: TailType, Title: "Pulse tail", Description: "A read-only subscription to evt.> that counts messages per " +
		"(ce-source, subject template, ce-type) each window. Publishers become observed `publishes` relationships with rates; event types become " +
		"contracts carried on subjects. Raw messages are never stored; observations expire when traffic stops.", DefaultLayer: catalog.LayerDiscovered,
		ConfigSchema: json.RawMessage(tailSchema), Capabilities: sources.Capabilities{Push: false, Pull: false, Incremental: true},
		Kinds: []sources.KindSupport{{Kind: "service", Level: sources.SupportRuntime, Note: "publishers by ce-source"},
			{Kind: "nats_subject", Level: sources.SupportRuntime}, {Kind: "event_contract", Level: sources.SupportRuntime}},
		RelTypes: []string{"publishes", "carried_on"}, Docs: "docs/PULSE.md",
		Verified: "integration test against a local nats-server with JetStream; validated against pulsed built from source with Pulse client credentials (scripts/validate/pulse.sh)"}
}

// Validate implements sources.Provider.
func (TailProvider) Validate(json.RawMessage, map[string]string) error { return nil }

// Test implements sources.Provider.
func (TailProvider) Test(context.Context, sources.Env) []sources.Diagnostic {
	return []sources.Diagnostic{{Level: "info", Code: "continuous", Message: "the tail runs while Pulse is enabled (pulse.enabled); see /metrics deed_pulse_tail_messages_total"}}
}

// Sync implements sources.Provider.
func (TailProvider) Sync(context.Context, sources.Env, sources.Emitter) error {
	return apperr.New(apperr.CodeInvalid, "pulse_tail sources run continuously; they are not synced")
}

func parseTail(raw json.RawMessage) TailConfig {
	var c TailConfig
	_ = json.Unmarshal(raw, &c)
	if c.Filter == "" {
		c.Filter = "evt.>"
	}
	if c.WindowSeconds == 0 {
		c.WindowSeconds = 60
	}
	if c.MaxKeys == 0 {
		c.MaxKeys = 5000
	}
	return c
}

type tailKey struct{ publisher, subject, typ string }

type tailCount struct {
	msgs, bytes int64
	last        time.Time
}

// Aggregator counts one window of messages.
type Aggregator struct {
	mu       sync.Mutex
	counts   map[tailKey]*tailCount
	overflow int64
	maxKeys  int
	start    time.Time
}

// NewAggregator returns an empty aggregator.
func NewAggregator(maxKeys int, start time.Time) *Aggregator {
	return &Aggregator{counts: map[tailKey]*tailCount{}, maxKeys: maxKeys, start: start}
}

// Add counts one message.
func (a *Aggregator) Add(source, subject, typ string, size int, at time.Time) bool {
	if source == "" {
		source = "unknown"
	}
	k := tailKey{publisher: source, subject: Template(subject), typ: typ}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.counts[k]
	if !ok {
		if len(a.counts) >= a.maxKeys {
			a.overflow++
			return false
		}
		c = &tailCount{}
		a.counts[k] = c
	}
	c.msgs++
	c.bytes += int64(size)
	if at.After(c.last) {
		c.last = at
	}
	return true
}

// Swap returns the current window and starts a new one.
func (a *Aggregator) Swap(now time.Time) (map[tailKey]*tailCount, int64, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	counts, overflow, start := a.counts, a.overflow, a.start
	a.counts, a.overflow, a.start = map[tailKey]*tailCount{}, 0, now
	return counts, overflow, start
}

// Tail runs pulse_tail sources.
type Tail struct {
	Store   *store.Store
	Catalog *catalog.Service
	Ingest  *ingest.Service
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
}

// Run keeps one subscription per enabled pulse_tail source until ctx ends.
func (t *Tail) Run(ctx context.Context, client *pulse.Client) {
	running := map[string]context.CancelFunc{}
	defer func() {
		for _, c := range running {
			c()
		}
	}()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		srcs, err := t.Catalog.Sources(ctx, nil)
		if err == nil {
			want := map[string]*catalog.Source{}
			for _, s := range srcs {
				if s.Provider == TailType && s.Enabled {
					want[s.ID] = s
				}
			}
			for id, c := range running {
				if _, ok := want[id]; !ok {
					c()
					delete(running, id)
				}
			}
			for id, s := range want {
				if _, ok := running[id]; ok {
					continue
				}
				sctx, cancel := context.WithCancel(ctx)
				running[id] = cancel
				go t.runOne(sctx, client, s)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (t *Tail) runOne(ctx context.Context, client *pulse.Client, src *catalog.Source) {
	c := parseTail(src.Config)
	window := time.Duration(c.WindowSeconds) * time.Second
	agg := NewAggregator(c.MaxKeys, t.Clock.Now())
	sub, err := client.Subscribe(ctx, c.Filter, func(_ context.Context, e pulse.Event) error {
		at := e.Time
		if at.IsZero() {
			at = e.StreamTime
		}
		if agg.Add(e.Source, e.Subject, e.Type, len(e.Data), at) {
			t.Metrics.PulseTail("counted", 1)
		} else {
			t.Metrics.PulseTail("overflow", 1)
		}
		return nil
	}, pulse.OnlyNew(), pulse.OnError(func(_ pulse.Event, err error) {
		t.Log.Debug("tail delivery error", "error.message", err.Error())
	}))
	if err != nil {
		t.Log.Warn("pulse tail subscription failed", "event.action", "pulse.tail", "event.outcome", "failure", "source", src.Name, "error.message", err.Error())
		return
	}
	defer sub.Stop()
	t.Log.Info("pulse tail running", "event.action", "pulse.tail", "source", src.Name, "filter", c.Filter)
	tick := time.NewTicker(window)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			counts, overflow, start := agg.Swap(t.Clock.Now())
			if err := t.Flush(context.WithoutCancel(ctx), src, counts, overflow, start, window); err != nil {
				t.Log.Warn("pulse tail flush failed", "event.action", "pulse.tail_flush", "event.outcome", "failure", "source", src.Name, "error.message", err.Error())
			}
		}
	}
}

// Flush records one window: rollups for rate charts and observed facts with an expiry of three windows (min 10 min).
func (t *Tail) Flush(ctx context.Context, src *catalog.Source, counts map[tailKey]*tailCount, overflow int64, start time.Time, window time.Duration) error {
	now := t.Clock.Now()
	keys := make([]tailKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].publisher != keys[j].publisher {
			return keys[i].publisher < keys[j].publisher
		}
		if keys[i].subject != keys[j].subject {
			return keys[i].subject < keys[j].subject
		}
		return keys[i].typ < keys[j].typ
	})
	bucket := start.Truncate(time.Minute).UnixMicro()
	err := t.Store.W().TxNamed(ctx, "pulse.tail_rollup", func(tx *store.Tx) error {
		for _, k := range keys {
			c := counts[k]
			if _, err := tx.Exec(ctx, `INSERT INTO pulse_rollups (source_id, granularity, bucket, publisher, subject, ce_type, messages, bytes, sampled)
				VALUES (?, 'minute', ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (source_id, granularity, bucket, publisher, subject, ce_type)
				DO UPDATE SET messages = messages + excluded.messages, bytes = bytes + excluded.bytes`,
				src.ID, bucket, k.publisher, k.subject, k.typ, c.msgs, c.bytes, overflow > 0); err != nil {
				return err
			}
			hb := start.Truncate(time.Hour).UnixMicro()
			if _, err := tx.Exec(ctx, `INSERT INTO pulse_rollups (source_id, granularity, bucket, publisher, subject, ce_type, messages, bytes, sampled)
				VALUES (?, 'hour', ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (source_id, granularity, bucket, publisher, subject, ce_type)
				DO UPDATE SET messages = messages + excluded.messages, bytes = bytes + excluded.bytes`,
				src.ID, hb, k.publisher, k.subject, k.typ, c.msgs, c.bytes, overflow > 0); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE sources SET last_heartbeat_at = ? WHERE id = ?`, tx.NowMicros(), src.ID)
		return err
	})
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	ttl := max(3*window, 10*time.Minute)
	exp := now.Add(ttl)
	var b ingest.Batch
	pubs := map[string]bool{}
	subs := map[string]map[string]any{}
	contracts := map[string]bool{}
	per := window.Minutes()
	for _, k := range keys {
		c := counts[k]
		if !pubs[k.publisher] {
			pubs[k.publisher] = true
			b.Entities = append(b.Entities, catalog.EntityItem{Kind: "service", Name: EntityName(k.publisher), ExternalID: "publisher:" + k.publisher,
				Spec: map[string]any{"pulse_source": k.publisher}, ExpiresAt: &exp, ObservedAt: &now})
		}
		if subs[k.subject] == nil {
			subs[k.subject] = map[string]any{"messages_per_minute": 0.0, "last_seen": "", "window_seconds": int(window.Seconds())}
		}
		o := subs[k.subject]
		o["messages_per_minute"] = o["messages_per_minute"].(float64) + float64(c.msgs)/per
		if ls := c.last.UTC().Format(time.RFC3339); ls > o["last_seen"].(string) {
			o["last_seen"] = ls
		}
		if k.typ != "" && !contracts[k.typ] {
			contracts[k.typ] = true
			b.Entities = append(b.Entities, catalog.EntityItem{Kind: "event_contract", Name: EntityName(k.typ), ExternalID: "contract:" + k.typ,
				Spec: map[string]any{"type": k.typ}, ExpiresAt: &exp, ObservedAt: &now})
		}
	}
	subjects := make([]string, 0, len(subs))
	for s := range subs {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)
	for _, s := range subjects {
		b.Entities = append(b.Entities, catalog.EntityItem{Kind: "nats_subject", Name: EntityName(s), ExternalID: "subject:" + s,
			Spec: map[string]any{"subject": s, "pattern": IsPattern(s)}, Observed: subs[s], ExpiresAt: &exp, ObservedAt: &now})
	}
	for _, k := range keys {
		c := counts[k]
		rate := float64(c.msgs) / per
		ev := map[string]any{"basis": "observed_message", "window_seconds": int(window.Seconds()), "messages": c.msgs, "bytes": c.bytes,
			"messages_per_minute": rate, "sampled": overflow > 0, "last_seen": c.last.UTC().Format(time.RFC3339), "ce_type": k.typ}
		b.Relations = append(b.Relations, catalog.RelationItem{Type: "publishes", From: catalog.Ref{ExternalID: "publisher:" + k.publisher},
			To: catalog.Ref{ExternalID: "subject:" + k.subject}, EvidenceClass: catalog.EvidenceObserved, Evidence: ev,
			Attributes: map[string]any{"messages_per_minute": rate, "window_seconds": int(window.Seconds())},
			ExternalID: fmt.Sprintf("publishes:%s->%s", k.publisher, k.subject), ExpiresAt: &exp, ObservedAt: &now})
		if k.typ != "" {
			b.Relations = append(b.Relations, catalog.RelationItem{Type: "carried_on", From: catalog.Ref{ExternalID: "contract:" + k.typ},
				To: catalog.Ref{ExternalID: "subject:" + k.subject}, EvidenceClass: catalog.EvidenceObserved,
				Evidence:   map[string]any{"basis": "observed_message", "window_seconds": int(window.Seconds())},
				ExternalID: fmt.Sprintf("carried_on:%s->%s", k.typ, k.subject), ExpiresAt: &exp, ObservedAt: &now})
		}
	}
	for start := 0; start < b.Len(); start += t.Ingest.Limits.MaxItems {
		end := min(start+t.Ingest.Limits.MaxItems, b.Len())
		var part ingest.Batch
		ne := len(b.Entities)
		if start < ne {
			part.Entities = b.Entities[start:min(end, ne)]
		}
		if end > ne {
			part.Relations = b.Relations[max(start-ne, 0) : end-ne]
		}
		res, err := t.Ingest.Items(ctx, src, part, false, audit.Actor{Type: audit.ActorSource, ID: src.ID})
		if err != nil {
			return err
		}
		if res.Summary.Errors > 0 {
			t.Log.Warn("pulse tail items rejected", "source", src.Name, "errors", res.Summary.Errors)
		}
	}
	return nil
}

// PruneRollups deletes rollups beyond retention.
func PruneRollups(ctx context.Context, s *store.Store, minute, hour time.Duration) error {
	return s.W().TxNamed(ctx, "pulse.rollup_prune", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM pulse_rollups WHERE granularity = 'minute' AND bucket < ?`, tx.Now.Add(-minute).UnixMicro()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pulse_rollups WHERE granularity = 'hour' AND bucket < ?`, tx.Now.Add(-hour).UnixMicro())
		return err
	})
}
