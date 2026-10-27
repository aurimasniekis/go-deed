package telemetry

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
)

// MapMetric is one on-map binding's value for an entity: what a node card shows (CPU 23 %, 5xx 0.1 req/s).
type MapMetric struct {
	Binding    string           `json:"binding"`
	Title      string           `json:"title"`
	Value      *float64         `json:"value,omitempty"`
	Unit       string           `json:"unit,omitempty"`
	Status     string           `json:"status" enum:"measured,inferred,stale,unavailable"`
	Health     *TelemetryHealth `json:"health,omitempty"`
	MeasuredAt *time.Time       `json:"measured_at,omitempty"`
}

// Limits of one map-metrics request.
const (
	MaxMapEntities = 200
	// MaxMapBindings is how many on-map bindings one entity shows.
	MaxMapBindings = 6
)

// MapMetrics runs the on-map bindings (scalar chart or health bindings with on_map) that apply to each entity,
// through the same cache, concurrency limits and timeouts as the entity page. Entities without any applicable binding,
// and results that are not applicable, are left out; the caller has already checked read access.
func (s *Service) MapMetrics(ctx context.Context, ents []*catalog.Entity, window string) (map[string][]MapMetric, error) {
	if _, ok := Windows[window]; !ok {
		window = "15m"
	}
	if len(ents) > MaxMapEntities {
		ents = ents[:MaxMapEntities]
	}
	all, err := s.Store.Bindings(ctx)
	if err != nil {
		return nil, err
	}
	var onMap []*Binding
	for _, b := range all {
		if b.OnMap && b.Shape == ShapeScalar && (b.Role == "chart" || b.Role == "health") {
			onMap = append(onMap, b)
		}
	}
	out := map[string][]MapMetric{}
	if len(onMap) == 0 {
		return out, nil
	}
	type job struct {
		e    *catalog.Entity
		b    *Binding
		slot int
	}
	var jobs []job
	slots := map[string]int{}
	for _, e := range ents {
		for _, b := range onMap {
			if len(b.Kinds) > 0 && !slices.Contains(b.Kinds, e.Kind) {
				continue
			}
			if slots[e.Metadata.ID] == MaxMapBindings {
				break
			}
			jobs = append(jobs, job{e: e, b: b, slot: slots[e.Metadata.ID]})
			slots[e.Metadata.ID]++
		}
	}
	results := make(map[string][]*MapMetric, len(slots))
	for id, n := range slots {
		results[id] = make([]*MapMetric, n)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for _, j := range jobs {
		wg.Add(1)
		limit <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			r := s.Run(ctx, j.b, Subject{Entity: j.e}, window)
			if r.Status == StatusUnavailable && (r.Data == nil || r.Data.Value == nil) {
				return // not applicable or no data: nothing to draw
			}
			m := &MapMetric{Binding: j.b.Name, Title: j.b.Title, Unit: j.b.Unit, Status: r.Status, Health: r.Health, MeasuredAt: r.MeasuredAt}
			if r.Data != nil {
				m.Value = r.Data.Value
			}
			mu.Lock()
			results[j.e.Metadata.ID][j.slot] = m
			mu.Unlock()
		}()
	}
	wg.Wait()
	for id, list := range results {
		for _, m := range list {
			if m != nil && m.Value != nil {
				out[id] = append(out[id], *m)
			}
		}
	}
	return out, nil
}
