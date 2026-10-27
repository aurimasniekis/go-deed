package catalog

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// Layer is a fact layer (ADR-003).
type Layer string

// Layers.
const (
	LayerDeclared    Layer = "declared"
	LayerImported    Layer = "imported"
	LayerDiscovered  Layer = "discovered"
	LayerObservation Layer = "observation"
	LayerAnnotation  Layer = "annotation"
	LayerOverride    Layer = "override"
)

// Layers lists every layer.
var Layers = []Layer{LayerDeclared, LayerImported, LayerDiscovered, LayerObservation, LayerAnnotation, LayerOverride}

// Valid reports whether l is a known layer.
func (l Layer) Valid() bool {
	return slices.Contains(Layers, l)
}

// Overlay reports whether a layer only changes entities other sources describe (annotations, overrides, label rules):
// an entity whose live facets are all overlays is tombstoned.
func (l Layer) Overlay() bool {
	return l == LayerAnnotation || l == LayerOverride || l == LayerRule
}

// DefaultPriority is the precedence of a layer: higher wins. Sources may override it (and per field) in their policy.
func (l Layer) DefaultPriority() int {
	switch l {
	case LayerOverride:
		return 100
	case LayerDeclared:
		return 80
	case LayerAnnotation:
		return 70
	case LayerImported:
		return 60
	case LayerDiscovered:
		return 40
	case LayerRule:
		return 5
	}
	return 0
}

// MergeFacet is the input of the pure merge: one live facet with its precedence data.
type MergeFacet struct {
	FacetID        string
	SourceID       string
	SourceName     string
	Layer          Layer
	Priority       int            // source priority (layer default unless the policy overrides it)
	FieldPriority  map[string]int // per-pointer overrides from the source policy (exact pointer or prefix ending in /)
	Revision       int64
	ObservedAt     time.Time
	FirstClaimedAt time.Time
	Fields         map[string]any // {"metadata": {...}, "spec": {...}} or, for observations, {"observed": {...}, "health": {...}}
}

// Provenance explains one effective value.
type Provenance struct {
	SourceID   string      `json:"source_id"`
	Source     string      `json:"source"`
	Layer      Layer       `json:"layer"`
	Revision   int64       `json:"revision"`
	ObservedAt time.Time   `json:"observed_at"`
	FacetID    string      `json:"facet_id"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Candidate is a losing value for a pointer.
type Candidate struct {
	SourceID string `json:"source_id"`
	Source   string `json:"source"`
	Layer    Layer  `json:"layer"`
	Value    any    `json:"value"`
}

// Conflict is a same-layer disagreement.
type Conflict struct {
	Pointer      string
	Layer        Layer
	WinnerSource string
	LoserSource  string
	WinnerValue  string
	LoserValue   string
}

// MergeResult is the effective view.
type MergeResult struct {
	Effective  map[string]any        // {"metadata": {...}, "spec": {...}}
	Observed   map[string]any        // status.observed merged from observation facets
	Health     map[string]any        // worst health reported by observation facets
	Provenance map[string]Provenance // pointer → provenance (effective and observed, prefixed /status/observed)
	Conflicts  []Conflict
}

// unionPointers are arrays merged as a union across facets instead of competing as single values.
var unionPointers = map[string]string{
	"/metadata/links": "url",
	"/metadata/tags":  "",
}

// quietPointer reports whether disagreement at ptr is expected rather than a conflict: every Terraform state that
// describes an entity has its own address and resource type, and icons are presentation.
func quietPointer(ptr string) bool {
	return ptr == "/metadata/icon" || strings.HasPrefix(ptr, "/metadata/labels/terraform~1") ||
		strings.HasPrefix(ptr, "/metadata/annotations/terraform~1")
}

type leaf struct {
	ptr   string
	value any
	f     *MergeFacet
	prio  int
}

// Merge computes the effective view of a set of live facets. It is a pure, deterministic function of its input:
// the order of facets does not matter.
func Merge(facets []MergeFacet) MergeResult {
	res := MergeResult{Effective: map[string]any{}, Observed: map[string]any{}, Provenance: map[string]Provenance{}}
	byPtr := map[string][]leaf{}
	unions := map[string][]leaf{}
	var obs []leaf
	var health []leaf
	for i := range facets {
		f := &facets[i]
		// Any layer may report observations and health; only non-observation layers contribute metadata and spec.
		if o, ok := f.Fields["observed"].(map[string]any); ok {
			flatten("/status/observed", o, func(p string, v any) { obs = append(obs, leaf{ptr: p, value: v, f: f, prio: 0}) })
		}
		if h, ok := f.Fields["health"].(map[string]any); ok {
			health = append(health, leaf{ptr: "/status/health", value: h, f: f})
		}
		if f.Layer == LayerObservation {
			continue
		}
		for _, top := range []string{"metadata", "spec"} {
			m, ok := f.Fields[top].(map[string]any)
			if !ok {
				continue
			}
			flatten("/"+top, m, func(p string, v any) {
				l := leaf{ptr: p, value: v, f: f, prio: priorityFor(f, p)}
				if _, u := unionPointers[p]; u {
					unions[p] = append(unions[p], l)
					return
				}
				byPtr[p] = append(byPtr[p], l)
			})
		}
	}
	// Pick a winner per pointer.
	type win struct {
		leaf
		cands []leaf
	}
	var winners []win
	ptrs := make([]string, 0, len(byPtr))
	for p := range byPtr {
		ptrs = append(ptrs, p)
	}
	sort.Strings(ptrs)
	for _, p := range ptrs {
		ls := byPtr[p]
		sort.SliceStable(ls, func(i, j int) bool { return better(ls[i], ls[j]) })
		winners = append(winners, win{leaf: ls[0], cands: ls[1:]})
		for _, c := range ls[1:] {
			// A conflict is a disagreement that only claim order settles: same layer, same priority (a source policy that
			// ranks a field lower has decided it), and not an expected per-source difference.
			if c.f.Layer == ls[0].f.Layer && c.f.Layer != LayerRule && c.prio == ls[0].prio && c.f.SourceID != ls[0].f.SourceID && !quietPointer(p) &&
				!jsonEqual(c.value, ls[0].value) {
				res.Conflicts = append(res.Conflicts, Conflict{Pointer: p, Layer: c.f.Layer, WinnerSource: ls[0].f.SourceID, LoserSource: c.f.SourceID,
					WinnerValue: compact(ls[0].value), LoserValue: compact(c.value)})
			}
		}
	}
	// Insert winners in precedence order so that a stronger scalar beats a weaker object at an ancestor path (and
	// vice versa); losers of such structural clashes are recorded as candidates only.
	sort.SliceStable(winners, func(i, j int) bool {
		if better(winners[i].leaf, winners[j].leaf) != better(winners[j].leaf, winners[i].leaf) {
			return better(winners[i].leaf, winners[j].leaf)
		}
		return winners[i].ptr < winners[j].ptr
	})
	taken := map[string]bool{}
	for _, w := range winners {
		if clashes(taken, w.ptr) {
			continue
		}
		taken[w.ptr] = true
		setPointer(res.Effective, w.ptr, w.value)
		pv := provenanceOf(w.leaf)
		for _, c := range w.cands {
			pv.Candidates = append(pv.Candidates, Candidate{SourceID: c.f.SourceID, Source: c.f.SourceName, Layer: c.f.Layer, Value: c.value})
		}
		res.Provenance[w.ptr] = pv
	}
	// Union arrays: entries from every facet, deduplicated by key (or by value), ordered by precedence then value.
	for p, ls := range unions {
		if clashes(taken, p) {
			continue
		}
		sort.SliceStable(ls, func(i, j int) bool { return better(ls[i], ls[j]) })
		key := unionPointers[p]
		seen := map[string]bool{}
		var out []any
		for _, l := range ls {
			arr, ok := l.value.([]any)
			if !ok {
				continue
			}
			for _, e := range arr {
				k := compact(e)
				if key != "" {
					if m, ok := e.(map[string]any); ok {
						if s, ok := m[key].(string); ok {
							k = s
						}
					}
				}
				if !seen[k] {
					seen[k] = true
					out = append(out, e)
				}
			}
		}
		if out != nil {
			setPointer(res.Effective, p, out)
			res.Provenance[p] = provenanceOf(ls[0])
		}
	}
	// Observations: the freshest value wins per pointer (they are time-bounded measurements, not claims).
	byObs := map[string][]leaf{}
	for _, l := range obs {
		byObs[l.ptr] = append(byObs[l.ptr], l)
	}
	optrs := make([]string, 0, len(byObs))
	for p := range byObs {
		optrs = append(optrs, p)
	}
	sort.Strings(optrs)
	otaken := map[string]bool{}
	for _, p := range optrs {
		ls := byObs[p]
		sort.SliceStable(ls, func(i, j int) bool {
			if !ls[i].f.ObservedAt.Equal(ls[j].f.ObservedAt) {
				return ls[i].f.ObservedAt.After(ls[j].f.ObservedAt)
			}
			return ls[i].f.SourceID < ls[j].f.SourceID
		})
		if clashes(otaken, p) {
			continue
		}
		otaken[p] = true
		setPointer(res.Observed, strings.TrimPrefix(p, "/status/observed"), ls[0].value)
		res.Provenance[p] = provenanceOf(ls[0])
	}
	// Health: the worst live report wins; ties go to the freshest.
	if len(health) > 0 {
		sort.SliceStable(health, func(i, j int) bool {
			ri, rj := healthRank(health[i].value), healthRank(health[j].value)
			if ri != rj {
				return ri > rj
			}
			if !health[i].f.ObservedAt.Equal(health[j].f.ObservedAt) {
				return health[i].f.ObservedAt.After(health[j].f.ObservedAt)
			}
			return health[i].f.SourceID < health[j].f.SourceID
		})
		h := maps.Clone(health[0].value.(map[string]any))
		h["source"] = health[0].f.SourceName
		h["observed_at"] = health[0].f.ObservedAt.UTC().Format(time.RFC3339)
		res.Health = h
		res.Provenance["/status/health"] = provenanceOf(health[0])
	}
	sort.Slice(res.Conflicts, func(i, j int) bool {
		if res.Conflicts[i].Pointer != res.Conflicts[j].Pointer {
			return res.Conflicts[i].Pointer < res.Conflicts[j].Pointer
		}
		return res.Conflicts[i].LoserSource < res.Conflicts[j].LoserSource
	})
	return res
}

func healthRank(v any) int {
	m, _ := v.(map[string]any)
	switch m["state"] {
	case "failed":
		return 3
	case "warning":
		return 2
	case "ok":
		return 1
	}
	return 0
}

func provenanceOf(l leaf) Provenance {
	return Provenance{SourceID: l.f.SourceID, Source: l.f.SourceName, Layer: l.f.Layer, Revision: l.f.Revision, ObservedAt: l.f.ObservedAt.UTC(), FacetID: l.f.FacetID}
}

// better reports whether a beats b: priority, then earliest first claim, then source ID.
func better(a, b leaf) bool {
	if a.prio != b.prio {
		return a.prio > b.prio
	}
	if !a.f.FirstClaimedAt.Equal(b.f.FirstClaimedAt) {
		return a.f.FirstClaimedAt.Before(b.f.FirstClaimedAt)
	}
	return a.f.SourceID < b.f.SourceID
}

func priorityFor(f *MergeFacet, ptr string) int {
	if p, ok := f.FieldPriority[ptr]; ok {
		return p
	}
	best, bestLen := 0, -1
	for k, v := range f.FieldPriority {
		if strings.HasSuffix(k, "/") && strings.HasPrefix(ptr, k) && len(k) > bestLen {
			best, bestLen = v, len(k)
		}
	}
	if bestLen >= 0 {
		return best
	}
	return f.Priority
}

// clashes reports whether ptr is an ancestor or descendant of a taken pointer.
func clashes(taken map[string]bool, ptr string) bool {
	for p := range taken {
		if p == ptr || strings.HasPrefix(ptr, p+"/") || strings.HasPrefix(p, ptr+"/") {
			return true
		}
	}
	return false
}

// flatten walks m and calls fn for every leaf: scalars, arrays and empty objects. Null values are skipped (a facet
// cannot assert "no value"; use an override to replace a value).
func flatten(prefix string, m map[string]any, fn func(string, any)) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		p := prefix + "/" + escapePointer(k)
		switch t := v.(type) {
		case nil:
		case map[string]any:
			if len(t) == 0 {
				fn(p, t)
			} else {
				flatten(p, t, fn)
			}
		default:
			fn(p, v)
		}
	}
}

func escapePointer(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func unescapePointer(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")
}

// SplitPointer splits a JSON pointer into unescaped tokens.
func SplitPointer(ptr string) []string {
	if ptr == "" || ptr == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i, p := range parts {
		parts[i] = unescapePointer(p)
	}
	return parts
}

func setPointer(root map[string]any, ptr string, v any) {
	parts := SplitPointer(ptr)
	if len(parts) == 0 {
		return
	}
	cur := root
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// GetPointer returns the value at ptr in root.
func GetPointer(root map[string]any, ptr string) (any, bool) {
	var cur any = root
	for _, p := range SplitPointer(ptr) {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}
