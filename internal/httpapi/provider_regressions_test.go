package httpapi_test

import (
	"net/http"
	"testing"
)

// Regressions reported by the Terraform provider's acceptance suite.

func TestDeleteKindAndRelTypeAfterTheirUsersAreDestroyed(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	owner.must("POST", "/kinds", map[string]any{"name": "example.com/ups", "title": "UPS"}, 201)
	owner.must("POST", "/relationship-types", map[string]any{"name": "example.com/powers", "title": "Powers"}, 201)
	ups := owner.must("POST", "/entities", map[string]any{"project": "home", "kind": "example.com/ups", "name": "ups-1"}, 201)
	srv := owner.must("POST", "/entities", map[string]any{"project": "home", "kind": "server", "name": "srv-1"}, 201)
	upsID := ups["entity_id"].(string)
	srvID := srv["entity_id"].(string)
	rel := owner.must("POST", "/relations", map[string]any{"type": "example.com/powers", "from": map[string]any{"id": upsID}, "to": map[string]any{"id": srvID}}, 201)

	if code, _, _ := owner.do("DELETE", "/kinds/example.com%2Fups", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete of a used kind = %d", code)
	}
	owner.must("DELETE", "/relations/"+rel["id"].(string)+"/assertions/api", nil, 204)
	owner.must("DELETE", "/entities/"+upsID+"/declarations/api", nil, 204)
	// Tombstones and retracted relations awaiting compaction no longer block the delete.
	owner.must("DELETE", "/relationship-types/example.com%2Fpowers", nil, 204)
	owner.must("DELETE", "/kinds/example.com%2Fups", nil, 204)
	// A kind of the same name starts clean.
	owner.must("POST", "/kinds", map[string]any{"name": "example.com/ups", "title": "UPS"}, 201)
	owner.must("POST", "/entities", map[string]any{"project": "home", "kind": "example.com/ups", "name": "ups-1"}, 201)
}

func TestCrossProjectAssertionDeclaredThroughTheToProject(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	owner.must("POST", "/projects", map[string]any{"name": "parents"}, 201)
	owner.must("POST", "/bridges", map[string]any{"from_project": "home", "to_project": "parents"}, 201)
	a := owner.must("POST", "/entities", map[string]any{"project": "home", "kind": "service", "name": "vpn"}, 201)
	b := owner.must("POST", "/entities", map[string]any{"project": "parents", "kind": "service", "name": "nas"}, 201)
	aID := a["entity_id"].(string)
	bID := b["entity_id"].(string)
	rel := owner.must("POST", "/relations", map[string]any{"project": "parents", "type": "connects_to", "from": map[string]any{"id": aID},
		"to": map[string]any{"id": bID}}, 201)
	id := rel["id"].(string)
	owner.must("PUT", "/relations/"+id+"/assertions/api", map[string]any{"attributes": map[string]any{"port": 51820}}, 200)
	got := owner.must("GET", "/relations/"+id, nil, 200)
	if n := len(got["assertions"].([]any)); n != 1 {
		t.Fatalf("PUT added an assertion through the wrong project: %d assertions", n)
	}
	owner.must("DELETE", "/relations/"+id+"/assertions/api", nil, 204)
}
