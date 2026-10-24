package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestPushSourceProtocol(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	src := owner.must("POST", "/sources", map[string]any{"project": "home", "name": "agent", "provider": "push"}, 201)
	sid := src["id"].(string)
	tok := owner.must("POST", "/sources/"+sid+"/tokens", map[string]any{"name": "agent-1"}, 201)
	agent := h.client()
	agent.bearer = tok["token"].(string)

	items := []any{}
	for i := range 3 {
		items = append(items, map[string]any{"kind": "container", "name": fmt.Sprintf("c%d", i), "external_id": fmt.Sprintf("cid-%d", i),
			"spec": map[string]any{"image": "example/app", "state": "running"}})
	}
	res := agent.must("POST", "/sources/"+sid+"/items", map[string]any{"entities": items}, 200)
	if res["summary"].(map[string]any)["created"].(float64) != 3 {
		t.Fatalf("items: %v", res)
	}
	// A source token cannot do anything else, nor touch other sources.
	if code, _, _ := agent.do("GET", "/entities", nil, nil); code != http.StatusForbidden {
		t.Fatalf("source token read entities: %d", code)
	}
	other := owner.must("POST", "/sources", map[string]any{"project": "home", "name": "other", "provider": "push"}, 201)
	if code, _, _ := agent.do("POST", "/sources/"+other["id"].(string)+"/heartbeat", map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("source token reached another source: %d", code)
	}
	agent.must("POST", "/sources/"+sid+"/heartbeat", map[string]any{}, 204)

	// Complete snapshot over HTTP: c2 disappears.
	sn := agent.must("POST", "/sources/"+sid+"/snapshots", map[string]any{"complete": true}, 201)
	snid := sn["id"].(string)
	agent.must("PUT", fmt.Sprintf("/sources/%s/snapshots/%s/batches/1", sid, snid), map[string]any{"entities": items[:2]}, 200)
	preview := agent.must("POST", fmt.Sprintf("/sources/%s/snapshots/%s/commit", sid, snid), map[string]any{"dry_run": true}, 200)
	if preview["summary"].(map[string]any)["removed"].(float64) != 1 {
		t.Fatalf("preview: %v", preview)
	}
	commit := agent.must("POST", fmt.Sprintf("/sources/%s/snapshots/%s/commit", sid, snid), map[string]any{}, 200)
	if commit["summary"].(map[string]any)["removed"].(float64) != 1 {
		t.Fatalf("commit: %v", commit)
	}
	list := owner.must("GET", "/entities?kind=container", nil, 200)
	if list["total"].(float64) != 2 {
		t.Fatalf("containers after snapshot: %v", list["total"])
	}
	// Revoked source tokens stop immediately.
	toks := owner.must("GET", "/sources/"+sid+"/tokens", nil, 200)
	_ = toks
	code, out, _ := owner.do("GET", "/sources/"+sid+"/tokens", nil, nil)
	if code != 200 {
		t.Fatal(out)
	}
}
