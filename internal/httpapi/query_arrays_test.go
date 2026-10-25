package httpapi_test

import (
	"fmt"
	"sort"
	"testing"
)

// TestRepeatedAndCommaQueryArrays: multi-value query parameters accept both repeated keys (what the web client and the
// Terraform provider send) and one comma-separated value. Repeated keys used to keep only the first value, so multi-kind
// filters silently filtered on the first kind (and the map's overview expansion showed one member; see map.spec.ts).
func TestRepeatedAndCommaQueryArrays(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	ids := map[string]string{}
	for _, e := range []struct{ kind, name string }{{"server", "srv-1"}, {"server", "srv-2"}, {"database", "db-1"}, {"service", "api"}} {
		out := owner.must("POST", "/entities", map[string]any{"project": "home", "kind": e.kind, "name": e.name}, 201)
		ids[e.name] = out["entity_id"].(string)
	}
	owner.must("POST", "/relations", map[string]any{"type": "runs_on", "from": map[string]any{"id": ids["api"]}, "to": map[string]any{"id": ids["srv-2"]}}, 201)
	owner.must("POST", "/relations", map[string]any{"type": "depends_on", "from": map[string]any{"id": ids["api"]}, "to": map[string]any{"id": ids["db-1"]}}, 201)

	names := func(path string) []string {
		t.Helper()
		out := owner.must("GET", path, nil, 200)
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["metadata"].(map[string]any)["name"].(string))
		}
		sort.Strings(got)
		return got
	}
	for name, path := range map[string]string{
		"repeated": "/entities?kind=server&kind=database",
		"comma":    "/entities?kind=server,database",
	} {
		if got := fmt.Sprint(names(path)); got != "[db-1 srv-1 srv-2]" {
			t.Errorf("entities by kind (%s): %s", name, got)
		}
	}
	rel := owner.must("GET", "/relations?project=home&type=runs_on&type=depends_on", nil, 200)
	if n := len(rel["items"].([]any)); n != 2 {
		t.Errorf("relations by repeated type: %d, want 2", n)
	}
}
