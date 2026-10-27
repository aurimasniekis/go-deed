package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Label rules over HTTP, and overrides of an imported entity: the override wins, and when the importer lets go of the
// entity the override neither keeps it alive nor reads as current.
func TestLabelRulesAndOverrides(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	src := owner.must("POST", "/sources", map[string]any{"project": "home", "name": "github", "provider": "push"}, 201)
	sid := src["id"].(string)
	tok := owner.must("POST", "/sources/"+sid+"/tokens", map[string]any{"name": "agent"}, 201)
	agent := h.client()
	agent.bearer = tok["token"].(string)
	repo := map[string]any{"kind": "repository", "name": "example-app", "external_id": "repo-1", "metadata": map[string]any{"displayName": "example/app"}}
	agent.must("POST", "/sources/"+sid+"/items", map[string]any{"entities": []any{repo}}, 200)
	ent := owner.must("GET", "/entities/lookup?project=home&kind=repository&name=example-app", nil, 200)
	id := ent["metadata"].(map[string]any)["id"].(string)

	// Label rules: create, list, update with If-Match, delete; viewers read only.
	rule := owner.must("POST", "/projects/home/label-rules", map[string]any{"name": "github", "match": map[string]any{"sources": []string{"github"}},
		"labels": map[string]any{"group": "github"}}, 201)
	labels := func() map[string]any {
		return owner.must("GET", "/entities/"+id, nil, 200)["metadata"].(map[string]any)["labels"].(map[string]any)
	}
	if labels()["group"] != "github" {
		t.Fatalf("rule label: %v", labels())
	}
	code, _, _ := owner.do("GET", "/projects/home/label-rules", nil, nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	_, raw, _ := owner.do("GET", "/entities/label-keys?project=home", nil, nil)
	var keys []map[string]any
	if err := json.Unmarshal([]byte(raw["_raw"].(string)), &keys); err != nil || len(keys) == 0 || keys[0]["key"] != "group" ||
		keys[0]["entities"].(float64) != 1 {
		t.Fatalf("label keys: %v %v", raw, err)
	}
	viewer := owner.token(map[string]any{"name": "ro", "project_role": "viewer", "projects": []string{"home"}})
	viewer.must("GET", "/projects/home/label-rules/github", nil, 200)
	if code, _, _ := viewer.do("POST", "/projects/home/label-rules", map[string]any{"name": "x", "labels": map[string]any{"a": "b"}}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer created a rule: %d", code)
	}
	if code, _, _ := owner.do("PATCH", "/projects/home/label-rules/github", map[string]any{"labels": map[string]any{"group": "code"}},
		map[string]string{"If-Match": `"99"`}); code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", code)
	}
	owner.must("PATCH", "/projects/home/label-rules/"+rule["id"].(string), map[string]any{"labels": map[string]any{"group": "code"}}, 200)
	if labels()["group"] != "code" {
		t.Fatalf("updated rule label: %v", labels())
	}

	// Override: beats the importer's display name, merges labels per key, reads back only what it set.
	owner.must("PUT", "/entities/"+id+"/declarations/overrides", map[string]any{"metadata": map[string]any{"displayName": "App",
		"labels": map[string]any{"team": "platform"}}}, 200)
	e := owner.must("GET", "/entities/"+id, nil, 200)["metadata"].(map[string]any)
	if e["displayName"] != "App" || labels()["team"] != "platform" || labels()["group"] != "code" {
		t.Fatalf("override: %v %v", e["displayName"], labels())
	}
	decl := owner.must("GET", "/entities/"+id+"/declarations/overrides", nil, 200)
	if md := decl["metadata"].(map[string]any); md["displayName"] != "App" || len(md) != 2 {
		t.Fatalf("override declaration: %v", md)
	}

	// The importer lets go: the override does not keep the entity alive and reads as gone; re-creating it fails clearly.
	agent.must("POST", "/sources/"+sid+"/items", map[string]any{"entities": []any{map[string]any{"kind": "repository", "name": "example-app",
		"external_id": "repo-1", "delete": true}}}, 200)
	if st := owner.must("GET", "/entities/"+id, nil, 200)["status"].(map[string]any); st["tombstonedAt"] == nil {
		t.Fatalf("override kept the entity alive: %v", st)
	}
	if code, _, _ := owner.do("GET", "/entities/"+id+"/declarations/overrides", nil, nil); code != http.StatusNotFound {
		t.Fatalf("dormant override read: %d", code)
	}
	if code, out, _ := owner.do("PUT", "/entities/"+id+"/declarations/overrides", map[string]any{"metadata": map[string]any{"displayName": "App"}}, nil); code != http.StatusConflict {
		t.Fatalf("override of a gone entity: %d %v", code, out)
	}
	// The importer describes it again: the dormant override applies again.
	agent.must("POST", "/sources/"+sid+"/items", map[string]any{"entities": []any{repo}}, 200)
	if e := owner.must("GET", "/entities/"+id, nil, 200)["metadata"].(map[string]any); e["displayName"] != "App" {
		t.Fatalf("revived without its override: %v", e["displayName"])
	}
	owner.must("DELETE", "/projects/home/label-rules/code", nil, 404)
	owner.must("DELETE", "/projects/home/label-rules/github", nil, 204)
}
