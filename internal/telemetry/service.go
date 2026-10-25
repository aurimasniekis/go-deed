package telemetry

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
)

// Windows a caller may request.
var Windows = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

// Health is a telemetry-derived health state (separate from structural health; never written into the catalog).
type TelemetryHealth struct {
	State  string `json:"state" enum:"ok,warning,critical"`
	Reason string `json:"reason"`
}

// Result is one binding's answer for one subject.
type TelemetryResult struct {
	Binding     string           `json:"binding"`
	Title       string           `json:"title"`
	Role        string           `json:"role"`
	Shape       string           `json:"shape"`
	Unit        string           `json:"unit"`
	Integration string           `json:"integration"`
	Adapter     string           `json:"adapter"`
	Status      string           `json:"status" enum:"measured,inferred,stale,unavailable"`
	MeasuredAt  *time.Time       `json:"measured_at,omitempty"`
	Window      string           `json:"window"`
	Data        *TelemetryData   `json:"data,omitempty"`
	Health      *TelemetryHealth `json:"health,omitempty"`
	Error       string           `json:"error,omitempty" doc:"Why the value is stale or unavailable (never upstream response bodies)."`
	Cached      bool             `json:"cached"`
}

// Service executes bindings with caching, request coalescing, per-integration concurrency limits and timeouts.
type Service struct {
	Store    *Store
	Catalog  *catalog.Service
	Registry *Registry
	Guard    *safehttp.Guard
	Metrics  *metrics.Metrics
	Clock    clock.Clock
	Log      *slog.Logger
	// Timeout bounds one upstream query (default 10s).
	Timeout time.Duration
	// Concurrency is the number of simultaneous queries per integration (default 4).
	Concurrency int
	// CacheEntries bounds the result cache (default 2000).
	CacheEntries int

	sf    singleflight.Group
	mu    sync.Mutex
	sems  map[string]chan struct{}
	cache *lru
}

type cached struct {
	data *TelemetryData
	at   time.Time
}

// emptyTTL bounds how long an empty answer is reused: a backend that has not ingested recent data yet (Loki indexing,
// a first scrape) should show it within seconds, not after a whole freshness period.
const emptyTTL = 15 * time.Second

func (c cached) ttl(fresh time.Duration) time.Duration {
	if c.data.empty() {
		return min(fresh, emptyTTL)
	}
	return fresh
}

// Init prepares internal state.
func (s *Service) Init() {
	if s.Timeout <= 0 {
		s.Timeout = 10 * time.Second
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 4
	}
	if s.CacheEntries <= 0 {
		s.CacheEntries = 2000
	}
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	s.sems = map[string]chan struct{}{}
	s.cache = newLRU(s.CacheEntries)
}

func (s *Service) sem(id string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.sems[id]
	if !ok {
		c = make(chan struct{}, s.Concurrency)
		s.sems[id] = c
	}
	return c
}

// Conn builds an adapter connection for an integration.
func (s *Service) Conn(it *Integration) (Conn, error) {
	sec, err := s.Store.Secrets(it)
	if err != nil {
		return Conn{}, err
	}
	return Conn{Name: it.Name, Config: it.Config, Secrets: sec, HTTP: func(p safehttp.Policy) (*http.Client, error) { return s.Guard.Client(p) }}, nil
}

// Test runs an integration's connectivity check and records the outcome.
func (s *Service) Test(ctx context.Context, it *Integration) (string, error) {
	a, ok := s.Registry.Get(it.Adapter)
	if !ok {
		return "", apperr.New(apperr.CodeInvalid, "adapter %q is not available", it.Adapter)
	}
	c, err := s.Conn(it)
	if err != nil {
		return "", err
	}
	tctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	msg, terr := a.Test(tctx, c)
	if terr != nil {
		msg = safeMessage(terr)
	}
	s.Metrics.TelemetryUp(it.Adapter, it.Name, terr == nil)
	if err := s.Store.RecordTest(context.WithoutCancel(ctx), it.ID, terr == nil, map[bool]string{true: "", false: msg}[terr == nil]); err != nil {
		s.Log.Warn("record integration test", "error.message", err.Error())
	}
	if terr != nil {
		return "", apperr.New(apperr.CodeUnavailable, "%s", msg)
	}
	return msg, nil
}

// safeMessage turns an adapter error into a message that never carries upstream bodies or credentials.
func safeMessage(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "request failed: " + safehttp.Classify(err)
}

// Subject supplies parameter values: an entity, or the two endpoints of a relation (edge-rate bindings).
type Subject struct {
	Entity *catalog.Entity
	From   *catalog.Entity
	To     *catalog.Entity
}

func docOf(e *catalog.Entity) any {
	if e == nil {
		return nil
	}
	b, _ := json.Marshal(e)
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

// Run executes a binding for a subject over a window.
func (s *Service) Run(ctx context.Context, b *Binding, subj Subject, window string) *TelemetryResult {
	w, ok := Windows[window]
	if !ok {
		window, w = "1h", time.Hour
	}
	res := &TelemetryResult{Binding: b.Name, Title: b.Title, Role: b.Role, Shape: b.Shape, Unit: b.Unit, Window: window, Status: StatusUnavailable}
	it, err := s.Store.Integration(ctx, b.IntegrationID)
	if err != nil {
		res.Error = "integration missing"
		return res
	}
	res.Integration, res.Adapter = it.Name, it.Adapter
	if !it.Enabled {
		res.Error = "integration disabled"
		return res
	}
	a, ok := s.Registry.Get(it.Adapter)
	if !ok {
		res.Error = "adapter not available in this build"
		return res
	}
	step := max(w/60, 15*time.Second).Truncate(time.Second)
	docs := map[string]any{"": docOf(subj.Entity), "from": docOf(subj.From), "to": docOf(subj.To)}
	text, err := render(b.Query, func(name string) (string, bool) {
		switch name {
		case "window":
			return promDuration(w), true
		case "step":
			return promDuration(step), true
		}
		for _, p := range b.Params {
			if p.Name != name {
				continue
			}
			v, ok := "", false
			if p.From != "" {
				end, ptr := "", p.From
				if e, rest, cut := strings.Cut(p.From, ":"); cut && (e == "from" || e == "to") {
					end, ptr = e, rest
				}
				v, ok = pointer(docs[end], ptr)
			}
			if !ok && p.Default != "" {
				v, ok = p.Default, true
			}
			if !ok || checkParam(p, v) != nil {
				return "", false
			}
			if p.Type == "int" || p.Type == "duration" {
				return v, true
			}
			return a.Escape(v), true
		}
		return "", false
	})
	if err != nil {
		res.Error = "not applicable: " + strings.TrimPrefix(err.Error(), "validation_failed: ")
		return res
	}
	now := s.Clock.Now()
	// Range queries align to the step so concurrent viewers share cache entries.
	end := now.Truncate(step)
	q := Query{Text: text, Shape: b.Shape, Start: end.Add(-w), End: end, Step: step}
	key := fmt.Sprintf("%s|%d|%s|%s|%s", b.ID, b.ResourceVersion, it.UpdatedAt.Format(time.RFC3339Nano), window, text)
	fresh := time.Duration(b.FreshnessSeconds) * time.Second
	if c, ok := s.cache.get(key); ok && now.Sub(c.at) < c.ttl(fresh) {
		s.Metrics.Cache("telemetry", "hit")
		s.fill(res, b, c.data, c.at, true)
		return res
	}
	s.Metrics.Cache("telemetry", "miss")
	v, err, _ := s.sf.Do(key, func() (any, error) {
		sem := s.sem(it.ID)
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.Timeout)
		defer cancel()
		select {
		case sem <- struct{}{}:
		case <-qctx.Done():
			return nil, apperr.New(apperr.CodeUnavailable, "integration busy")
		}
		defer func() { <-sem }()
		c, err := s.Conn(it)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		d, err := a.Query(qctx, c, q)
		result := "ok"
		if err != nil {
			result = "error"
			if errors.Is(err, context.DeadlineExceeded) {
				result = "timeout"
			}
		}
		s.Metrics.TelemetryQuery(it.Adapter, result, time.Since(start))
		if err != nil {
			return nil, err
		}
		truncateSeries(d)
		entry := cached{data: d, at: s.Clock.Now()}
		s.cache.put(key, entry)
		return entry, nil
	})
	if err != nil {
		msg := safeMessage(err)
		// Serve the last good value as stale for up to ten freshness periods.
		if c, ok := s.cache.get(key); ok && now.Sub(c.at) < 10*fresh {
			s.fill(res, b, c.data, c.at, true)
			res.Status = StatusStale
		}
		res.Error = msg
		return res
	}
	c := v.(cached)
	s.fill(res, b, c.data, c.at, false)
	return res
}

func (s *Service) fill(res *TelemetryResult, b *Binding, d *TelemetryData, at time.Time, fromCache bool) {
	res.Data, res.Cached, res.Error = d, fromCache, ""
	t := at
	res.MeasuredAt = &t
	res.Status = StatusMeasured
	if d.Inferred {
		res.Status = StatusInferred
	}
	if d.empty() {
		res.Status, res.Error = StatusUnavailable, "no data"
		return
	}
	if b.Role == "health" && d.Value != nil {
		res.Health = evaluate(b.Thresholds, *d.Value, b.Unit)
	}
}

// evaluate applies thresholds to a value.
func evaluate(t BindingThresholds, v float64, unit string) *TelemetryHealth {
	bad := func(limit float64) bool {
		if t.Direction == "below" {
			return v < limit
		}
		return v > limit
	}
	format := func(limit float64) string {
		op := ">"
		if t.Direction == "below" {
			op = "<"
		}
		// Fields collapses the gaps an empty unit leaves ("42 < 50", not "42  < 50").
		return strings.Join(strings.Fields(fmt.Sprintf("%g %s %s %g %s", v, unit, op, limit, unit)), " ")
	}
	switch {
	case t.Critical != nil && bad(*t.Critical):
		return &TelemetryHealth{State: "critical", Reason: format(*t.Critical)}
	case t.Warning != nil && bad(*t.Warning):
		return &TelemetryHealth{State: "warning", Reason: format(*t.Warning)}
	}
	return &TelemetryHealth{State: "ok", Reason: "within thresholds"}
}

// MaxEntityBindings bounds the bindings evaluated for one entity.
const MaxEntityBindings = 20

// ForEntity runs every applicable chart, health and link binding (or only the named one) for an entity.
func (s *Service) ForEntity(ctx context.Context, e *catalog.Entity, window, only string) ([]*TelemetryResult, error) {
	all, err := s.Store.Bindings(ctx)
	if err != nil {
		return nil, err
	}
	var todo []*Binding
	for _, b := range all {
		if b.Role == "edge_rate" || (only != "" && b.Name != only && b.ID != only) {
			continue
		}
		if len(b.Kinds) > 0 && !slices.Contains(b.Kinds, e.Kind) {
			continue
		}
		todo = append(todo, b)
		if len(todo) == MaxEntityBindings {
			break
		}
	}
	if only != "" && len(todo) == 0 {
		return nil, ErrBindingNotFound
	}
	out := make([]*TelemetryResult, len(todo))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, b := range todo {
		wg.Add(1)
		limit <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			out[i] = s.Run(ctx, b, Subject{Entity: e}, window)
		}()
	}
	wg.Wait()
	return out, nil
}

// lru is a small bounded cache.
type lru struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[string]*list.Element
}

type lruItem struct {
	key string
	val cached
}

func newLRU(n int) *lru { return &lru{max: n, ll: list.New(), items: map[string]*list.Element{}} }

func (l *lru) get(k string) (cached, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.items[k]; ok {
		l.ll.MoveToFront(e)
		return e.Value.(*lruItem).val, true
	}
	return cached{}, false
}

func (l *lru) put(k string, v cached) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.items[k]; ok {
		e.Value.(*lruItem).val = v
		l.ll.MoveToFront(e)
		return
	}
	l.items[k] = l.ll.PushFront(&lruItem{key: k, val: v})
	for l.ll.Len() > l.max {
		last := l.ll.Back()
		l.ll.Remove(last)
		delete(l.items, last.Value.(*lruItem).key)
	}
}
