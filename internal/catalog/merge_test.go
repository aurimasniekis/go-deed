package catalog

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

func randomFacets(r *rand.Rand, n int) []MergeFacet {
	layers := []Layer{LayerDeclared, LayerImported, LayerDiscovered, LayerAnnotation, LayerOverride, LayerObservation}
	keys := []string{"ipv4", "hostname", "os", "region", "nested"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []MergeFacet
	for i := range n {
		l := layers[r.IntN(len(layers))]
		spec := map[string]any{}
		for _, k := range keys {
			if r.IntN(2) == 0 {
				continue
			}
			if k == "nested" && r.IntN(2) == 0 {
				spec[k] = map[string]any{"a": float64(r.IntN(3)), "b": "x"}
			} else {
				spec[k] = fmt.Sprintf("v%d", r.IntN(3))
			}
		}
		f := MergeFacet{FacetID: fmt.Sprintf("f%d", i), SourceID: fmt.Sprintf("src_%02d", i), SourceName: fmt.Sprintf("s%d", i), Layer: l,
			Priority: l.DefaultPriority(), FirstClaimedAt: base.Add(time.Duration(r.IntN(5)) * time.Minute),
			ObservedAt: base.Add(time.Duration(r.IntN(100)) * time.Second), Fields: map[string]any{}}
		if l == LayerObservation {
			f.Fields["observed"] = spec
			if r.IntN(2) == 0 {
				f.Fields["health"] = map[string]any{"state": []string{"ok", "warning", "failed"}[r.IntN(3)]}
			}
		} else {
			f.Fields["spec"] = spec
			f.Fields["metadata"] = map[string]any{"labels": map[string]any{fmt.Sprintf("k%d", r.IntN(3)): "v"},
				"links": []any{map[string]any{"type": "docs", "url": fmt.Sprintf("https://example.com/%d", r.IntN(3))}}}
		}
		out = append(out, f)
	}
	return out
}

func canonical(m MergeResult) string {
	b, _ := json.Marshal(struct {
		E, O, H any
		C       []Conflict
	}{m.Effective, m.Observed, m.Health, m.Conflicts})
	return string(b)
}

// Invariant 6 (ADR-003): the effective view is a pure function of the live facets — order never matters.
func TestMergeIsOrderIndependent(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for round := range 500 {
		facets := randomFacets(r, 1+r.IntN(6))
		want := canonical(Merge(facets))
		for range 5 {
			shuffled := append([]MergeFacet(nil), facets...)
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			if got := canonical(Merge(shuffled)); got != want {
				t.Fatalf("round %d: merge depends on order\nwant %s\ngot  %s", round, want, got)
			}
		}
	}
}

// Invariant 7: removing one source's facet never changes values owned by another source that won before.
func TestRemovingFacetPreservesOtherOwners(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for round := range 500 {
		facets := randomFacets(r, 2+r.IntN(5))
		full := Merge(facets)
		victim := r.IntN(len(facets))
		rest := append(append([]MergeFacet(nil), facets[:victim]...), facets[victim+1:]...)
		less := Merge(rest)
		for ptr, prov := range full.Provenance {
			if prov.SourceID == facets[victim].SourceID || strings.HasPrefix(ptr, "/status/") || strings.HasPrefix(ptr, "/metadata/links") {
				continue
			}
			// Structural clashes can let a previously shadowed value surface at an ancestor/descendant path; skip those.
			if clashedWith(full, ptr, facets[victim].SourceID) {
				continue
			}
			got, ok := less.Provenance[ptr]
			if !ok || got.SourceID != prov.SourceID {
				t.Fatalf("round %d: %s owner changed from %s to %+v after removing %s", round, ptr, prov.SourceID, got, facets[victim].SourceID)
			}
			a, _ := GetPointer(full.Effective, ptr)
			b, _ := GetPointer(less.Effective, ptr)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("round %d: %s value changed", round, ptr)
			}
		}
	}
}

func clashedWith(m MergeResult, ptr, src string) bool {
	for p, prov := range m.Provenance {
		if prov.SourceID == src && (strings.HasPrefix(p, ptr+"/") || strings.HasPrefix(ptr, p+"/")) {
			return true
		}
	}
	return false
}

func TestMergePrecedence(t *testing.T) {
	now := time.Now()
	facets := []MergeFacet{
		{SourceID: "a", SourceName: "tf", Layer: LayerDeclared, Priority: 80, FirstClaimedAt: now, Fields: map[string]any{"spec": map[string]any{"x": "declared"}}},
		{SourceID: "b", SourceName: "imp", Layer: LayerImported, Priority: 60, FirstClaimedAt: now, Fields: map[string]any{"spec": map[string]any{"x": "imported", "y": 1.0}}},
		{SourceID: "c", SourceName: "ovr", Layer: LayerOverride, Priority: 100, FirstClaimedAt: now, Fields: map[string]any{"spec": map[string]any{"y": 2.0}}},
		{SourceID: "d", SourceName: "imp2", Layer: LayerImported, Priority: 60, FirstClaimedAt: now, FieldPriority: map[string]int{"/spec/z": 90},
			Fields: map[string]any{"spec": map[string]any{"z": "boosted"}}},
		{SourceID: "e", SourceName: "tf2", Layer: LayerDeclared, Priority: 80, FirstClaimedAt: now, Fields: map[string]any{"spec": map[string]any{"z": "declared"}}},
	}
	m := Merge(facets)
	spec := m.Effective["spec"].(map[string]any)
	if spec["x"] != "declared" || spec["y"] != 2.0 || spec["z"] != "boosted" {
		t.Fatalf("precedence wrong: %v", spec)
	}
}

func TestCompatibility(t *testing.T) {
	parse := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := parse(`{"type":"object","properties":{"a":{"type":"string","maxLength":10},"e":{"enum":["x","y"]}},"required":["a"],"additionalProperties":false}`)
	ok := parse(`{"type":"object","properties":{"a":{"type":"string","maxLength":20},"e":{"enum":["x","y","z"]},"b":{"type":"integer"}},"required":["a"],"additionalProperties":false}`)
	if p := CompatibilityProblems(old, ok); len(p) != 0 {
		t.Fatalf("additive change flagged: %v", p)
	}
	for name, s := range map[string]string{
		"new required":  `{"type":"object","properties":{"a":{"type":"string"},"e":{"enum":["x","y"]},"b":{"type":"string"}},"required":["a","b"],"additionalProperties":false}`,
		"narrower":      `{"type":"object","properties":{"a":{"type":"string","maxLength":5},"e":{"enum":["x","y"]}},"required":["a"],"additionalProperties":false}`,
		"enum removed":  `{"type":"object","properties":{"a":{"type":"string","maxLength":10},"e":{"enum":["x"]}},"required":["a"],"additionalProperties":false}`,
		"type changed":  `{"type":"object","properties":{"a":{"type":"integer"},"e":{"enum":["x","y"]}},"required":["a"],"additionalProperties":false}`,
		"pattern added": `{"type":"object","properties":{"a":{"type":"string","maxLength":10,"pattern":"^x"},"e":{"enum":["x","y"]}},"required":["a"],"additionalProperties":false}`,
		"prop removed":  `{"type":"object","properties":{"e":{"enum":["x","y"]}},"additionalProperties":false}`,
	} {
		if p := CompatibilityProblems(old, parse(s)); len(p) == 0 {
			t.Errorf("%s: not flagged as breaking", name)
		}
	}
}

func TestSchemaLimits(t *testing.T) {
	for name, s := range map[string]string{
		"remote ref": `{"type":"object","properties":{"a":{"$ref":"https://example.com/s.json"}}}`,
		"id":         `{"$id":"https://example.com/x","type":"object"}`,
		"format":     `{"type":"object","properties":{"a":{"type":"string","format":"credit-card"}}}`,
		"not object": `{"type":"string"}`,
		"big":        `{"type":"object","description":"` + strings.Repeat("x", MaxSchemaBytes) + `"}`,
	} {
		if _, err := ParseSchema([]byte(s)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	deep := `{"type":"object","properties":{"a":` + strings.Repeat(`{"type":"object","properties":{"a":`, 20) + `{}` + strings.Repeat(`}}`, 20) + `}}`
	if _, err := ParseSchema([]byte(deep)); err == nil {
		t.Error("deep schema accepted")
	}
	if _, err := ParseSchema([]byte(`{"type":"object","properties":{"a":{"type":"string","pattern":"^(a+)+$"}}}`)); err != nil {
		t.Errorf("RE2 pattern rejected: %v", err)
	}
}

// Two imported states that describe one zone differ in their Terraform address and type and may differ in icon:
// expected, not conflicts. A real disagreement is one, unless a source policy ranks the field lower.
func TestMergeConflictsIgnoreExpectedDifferences(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	facet := func(id string, prio map[string]int, desc string) MergeFacet {
		return MergeFacet{FacetID: "f_" + id, SourceID: id, SourceName: id, Layer: LayerImported, Priority: LayerImported.DefaultPriority(),
			FieldPriority: prio, FirstClaimedAt: base, ObservedAt: base, Fields: map[string]any{"metadata": map[string]any{
				"icon":        "tabler:" + id,
				"description": desc,
				"labels":      map[string]any{"terraform/type": id, "team": "net"},
				"annotations": map[string]any{"terraform/address": id + ".zone"},
			}}}
	}
	res := Merge([]MergeFacet{facet("a", nil, "same"), facet("b", nil, "same")})
	if len(res.Conflicts) != 0 {
		t.Fatalf("expected differences reported: %+v", res.Conflicts)
	}
	res = Merge([]MergeFacet{facet("a", nil, "one"), facet("b", nil, "two")})
	if len(res.Conflicts) != 1 || res.Conflicts[0].Pointer != "/metadata/description" {
		t.Fatalf("description disagreement: %+v", res.Conflicts)
	}
	res = Merge([]MergeFacet{facet("a", nil, "one"), facet("b", map[string]int{"/metadata/description": 10}, "two")})
	if len(res.Conflicts) != 0 || res.Effective["metadata"].(map[string]any)["description"] != "one" {
		t.Fatalf("policy-ranked field: %+v %v", res.Conflicts, res.Effective)
	}
}
