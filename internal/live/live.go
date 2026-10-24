// Package live streams catalog changes to browsers over Server-Sent Events (ADR-009).
//
// Every event's id is the change-log sequence number. Subscribers do not hold queues: when the catalog commits a
// change the hub wakes every subscriber, which reads the change log after its last sequence (filtered to its
// readable projects) in bounded pages. A reconnect with Last-Event-ID resumes from the change log when the gap is
// within the replay window (10,000 events and 24 hours); otherwise the client receives `resync` and refetches. A
// subscriber that falls behind by more than the window is also told to resync, so memory stays bounded whatever
// the change rate. Credentials are re-validated every minute; a revoked session or token closes the stream.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
)

// Replay window (ADR-009).
const (
	ReplayEvents = 10_000
	ReplayAge    = 24 * time.Hour
	pageSize     = 500
	heartbeat    = 20 * time.Second
	revalidate   = time.Minute
)

// Hub fans change notifications out to subscribers.
type Hub struct {
	Catalog *catalog.Service
	Metrics *metrics.Metrics
	Clock   clock.Clock

	latest  atomic.Int64
	mu      sync.Mutex
	wake    chan struct{}
	maxConn int
	conns   atomic.Int64
}

// NewHub returns a hub; maxConn bounds concurrent streams (0 = 200).
func NewHub(c *catalog.Service, m *metrics.Metrics, clk clock.Clock, maxConn int) *Hub {
	if maxConn <= 0 {
		maxConn = 200
	}
	return &Hub{Catalog: c, Metrics: m, Clock: clk, wake: make(chan struct{}), maxConn: maxConn}
}

// Notify records a committed change-log sequence and wakes subscribers (non-blocking, coalescing).
func (h *Hub) Notify(seq int64) {
	for {
		cur := h.latest.Load()
		if seq <= cur || h.latest.CompareAndSwap(cur, seq) {
			break
		}
	}
	h.mu.Lock()
	close(h.wake)
	h.wake = make(chan struct{})
	h.mu.Unlock()
}

func (h *Hub) waitCh() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wake
}

// Event is the payload of a `change` event.
type Event struct {
	Seq        int64          `json:"seq"`
	At         time.Time      `json:"at"`
	ProjectID  string         `json:"project_id"`
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Action     string         `json:"action"`
	SourceID   string         `json:"source_id,omitempty"`
	Summary    map[string]any `json:"summary"`
}

type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s *sse) send(event string, id int64, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if id > 0 {
		if _, err := fmt.Fprintf(s.w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sse) comment(c string) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", c); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Register mounts GET /api/v1/stream.
func (h *Hub) Register(mux *http.ServeMux, d *httpapi.Deps) {
	mux.HandleFunc("GET /api/v1/stream", func(w http.ResponseWriter, r *http.Request) { h.serve(w, r, d) })
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": msg, "code": code})
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request, d *httpapi.Deps) {
	p, err := d.Authenticate(r)
	if err != nil || p.SourceID != "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if h.conns.Load() >= int64(h.maxConn) {
		w.Header().Set("Retry-After", "10")
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "too many live connections")
		return
	}
	ctx := r.Context()
	readable, err := h.readable(ctx, p, r.URL.Query()["project"])
	if err != nil {
		code := http.StatusInternalServerError
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			code = http.StatusNotFound
		}
		writeErr(w, code, string(apperr.CodeOf(err)), err.Error())
		return
	}
	h.conns.Add(1)
	h.Metrics.LiveClients(1)
	defer func() {
		h.conns.Add(-1)
		h.Metrics.LiveClients(-1)
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	latest, err := h.Catalog.LatestSeq(ctx)
	if err != nil {
		return
	}
	cursor := latest
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		var last int64
		if _, err := fmt.Sscan(v, &last); err == nil && last >= 0 && last <= latest {
			if latest-last <= ReplayEvents && h.withinAge(ctx, last) {
				cursor = last
			} else {
				h.Metrics.LiveDropped("gap")
				if s.send("resync", latest, map[string]any{"reason": "gap", "seq": latest}) != nil {
					return
				}
			}
		}
	}
	if err := s.send("hello", 0, map[string]any{"seq": latest, "replay_from": cursor}); err != nil {
		return
	}
	hb := time.NewTicker(heartbeat)
	defer hb.Stop()
	rv := time.NewTicker(revalidate)
	defer rv.Stop()
	for {
		wake := h.waitCh()
		var err error
		cursor, err = h.drain(ctx, s, readable, cursor)
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-hb.C:
			if s.comment("keepalive") != nil {
				return
			}
		case <-rv.C:
			if !h.stillValid(r, d) {
				_ = s.send("closed", 0, map[string]any{"reason": "credentials revoked or expired"})
				return
			}
		}
	}
}

// drain sends every change after cursor; a backlog beyond the replay window becomes a resync.
func (h *Hub) drain(ctx context.Context, s *sse, readable []string, cursor int64) (int64, error) {
	latest := h.latest.Load()
	if latest-cursor > ReplayEvents {
		h.Metrics.LiveDropped("overflow")
		if err := s.send("resync", latest, map[string]any{"reason": "overflow", "seq": latest}); err != nil {
			return cursor, err
		}
		return latest, nil
	}
	for {
		evs, err := h.Catalog.Changes(ctx, catalog.ChangeFilter{ProjectIDs: readable, AfterSeq: cursor, Limit: pageSize, Ascending: true})
		if err != nil {
			return cursor, err
		}
		for _, c := range evs {
			if err := s.send("change", c.Seq, Event{Seq: c.Seq, At: c.At, ProjectID: c.ProjectID, ObjectType: c.ObjectType, ObjectID: c.ObjectID,
				Action: c.Action, SourceID: c.SourceID, Summary: c.Summary}); err != nil {
				return cursor, err
			}
			h.Metrics.LiveEventSent()
			cursor = c.Seq
		}
		if len(evs) < pageSize {
			// Events of unreadable projects are skipped silently; advance to the latest known sequence.
			if l := h.latest.Load(); l > cursor && len(evs) == 0 {
				cursor = max(cursor, l)
			}
			return cursor, nil
		}
	}
}

func (h *Hub) withinAge(ctx context.Context, last int64) bool {
	evs, err := h.Catalog.Changes(ctx, catalog.ChangeFilter{AfterSeq: last, Limit: 1, Ascending: true})
	if err != nil {
		return false
	}
	if len(evs) == 0 {
		return true
	}
	// The next event must still exist (not pruned) and be recent.
	return evs[0].Seq == last+1 && h.Clock.Now().Sub(evs[0].At) <= ReplayAge
}

func (h *Hub) readable(ctx context.Context, p *authn.Principal, want []string) ([]string, error) {
	known, err := h.Catalog.ProjectIDs(ctx)
	if err != nil {
		return nil, err
	}
	all, ids := authz.Readable(p, known)
	if len(want) == 0 {
		if all {
			return known, nil
		}
		return ids, nil
	}
	var out []string
	for _, ref := range want {
		pr, err := h.Catalog.Project(ctx, ref)
		if err != nil || (!all && !slices.Contains(ids, pr.ID)) {
			return nil, apperr.New(apperr.CodeNotFound, "project not found")
		}
		out = append(out, pr.ID)
	}
	return out, nil
}

func (h *Hub) stillValid(r *http.Request, d *httpapi.Deps) bool {
	if bearer, ok := httpapi.BearerToken(r); ok {
		_, err := d.Authn.AuthenticateToken(r.Context(), bearer, "")
		return err == nil
	}
	_, err := d.Authn.Validate(r.Context(), authn.TokenFromRequest(r))
	return err == nil
}

// Start primes the latest sequence.
func (h *Hub) Start(ctx context.Context) error {
	seq, err := h.Catalog.LatestSeq(ctx)
	if err != nil {
		return err
	}
	h.latest.Store(seq)
	return nil
}
