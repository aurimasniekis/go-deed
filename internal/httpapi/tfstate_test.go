package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestTerraformStatePush(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "core"}, 201)
	src := owner.must("POST", "/sources", map[string]any{"project": "core", "name": "core-tf", "provider": "terraform_state",
		"config": map[string]any{"backend": "push", "workspace": "core"}}, 201)
	sid := src["id"].(string)
	tok := owner.must("POST", "/sources/"+sid+"/tokens", map[string]any{"name": "ci"}, 201)["token"].(string)
	state, err := os.ReadFile("../tfimport/testdata/state-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	push := func(query string) (int, map[string]any) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, h.srv.URL+"/api/v1/sources/"+sid+"/terraform/state"+query, bytes.NewReader(state))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if bytes.Contains(b, []byte("PLANTED-SECRET")) {
			t.Fatalf("response leaks state secrets: %s", b)
		}
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	code, out := push("?dry_run=true")
	if code != 200 || out["result"].(map[string]any)["dry_run"] != true {
		t.Fatalf("dry run: %d %v", code, out)
	}
	if list := owner.must("GET", "/entities?project=core", nil, 200); list["total"].(float64) != 0 {
		t.Fatalf("dry run wrote entities")
	}
	code, out = push("")
	if code != 200 {
		t.Fatalf("push: %d %v", code, out)
	}
	sum := out["result"].(map[string]any)["summary"].(map[string]any)
	if sum["created"].(float64) < 9 {
		t.Fatalf("summary %v", sum)
	}
	list := owner.must("GET", "/entities?project=core&kind=dns_record", nil, 200)
	if list["total"].(float64) != 2 {
		t.Fatalf("dns records %v", list["total"])
	}
	// No secret anywhere in the database.
	rows, err := h.env.Store.R().QueryContext(context.Background(), `SELECT effective_json || provenance_json FROM entities UNION ALL SELECT fields_json FROM facets
		UNION ALL SELECT details_json FROM audit_events UNION ALL SELECT payload_json FROM outbox UNION ALL SELECT diagnostics_json FROM source_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		if bytes.Contains([]byte(s), []byte("PLANTED-SECRET")) || bytes.Contains([]byte(s), []byte("PRIVATE KEY")) {
			t.Fatalf("secret persisted: %s", s)
		}
	}
}

// TestTerraformStatePushHomelab pushes the vSphere / Cloudflare / UniFi / HallPass fixture: every mapped entity and
// relationship must validate against its kind and relationship type, and nothing secret may be stored.
func TestTerraformStatePushHomelab(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "lab"}, 201)
	src := owner.must("POST", "/sources", map[string]any{"project": "lab", "name": "lab-tf", "provider": "terraform_state",
		"config": map[string]any{"backend": "push"}}, 201)
	sid := src["id"].(string)
	tok := owner.must("POST", "/sources/"+sid+"/tokens", map[string]any{"name": "ci"}, 201)["token"].(string)
	state, err := os.ReadFile("../tfimport/testdata/homelab-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, h.srv.URL+"/api/v1/sources/"+sid+"/terraform/state", bytes.NewReader(state))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Result struct {
			Summary map[string]float64 `json:"summary"`
			Errors  []any              `json:"errors"`
		} `json:"result"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusOK || len(out.Result.Errors) > 0 || out.Result.Summary["created"] < 20 {
		t.Fatalf("push: %d %s", resp.StatusCode, b)
	}
	rel := owner.must("GET", "/relations?project=lab&limit=200", nil, 200)
	if items, _ := rel["items"].([]any); len(items) < 18 {
		t.Fatalf("relationships: %d %v", len(items), rel)
	}
	rows, err := h.env.Store.R().QueryContext(context.Background(), `SELECT effective_json || provenance_json FROM entities UNION ALL SELECT fields_json FROM facets
		UNION ALL SELECT diagnostics_json FROM source_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		for _, secret := range []string{"PLANTED-SECRET", "PLANTED-CSR", "BEGIN CERTIFICATE", "PLANTED-PUBLIC-KEY"} {
			if bytes.Contains([]byte(s), []byte(secret)) {
				t.Fatalf("%s persisted: %s", secret, s)
			}
		}
	}
}
