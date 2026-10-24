package httpapi_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelemetryAuthorization(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1790000000,"3"]}]}}`)
	}))
	defer prom.Close()
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	owner.must("POST", "/projects", map[string]any{"name": "parents"}, 201)
	viewer := owner.token(map[string]any{"name": "ro", "project_role": "viewer", "projects": []string{"home"}})
	a := owner.must("POST", "/entities", map[string]any{"project": "home", "kind": "service", "name": "a"}, 201)["entity_id"].(string)
	b := owner.must("POST", "/entities", map[string]any{"project": "parents", "kind": "service", "name": "b"}, 201)["entity_id"].(string)
	b2 := owner.must("POST", "/entities", map[string]any{"project": "parents", "kind": "service", "name": "b2"}, 201)["entity_id"].(string)
	rel := owner.must("POST", "/relations", map[string]any{"type": "depends_on", "from": map[string]any{"id": b}, "to": map[string]any{"id": b2}}, 201)

	cfg := map[string]any{"url": prom.URL, "insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}}
	if code, _, _ := viewer.do("POST", "/integrations", map[string]any{"name": "prom", "adapter": "prometheus", "config": cfg}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer created an integration: %d", code)
	}
	owner.must("POST", "/integrations", map[string]any{"name": "prom", "adapter": "prometheus", "config": cfg, "secrets": map[string]any{"token": "PLANTED"}}, 201)
	if code, _, _ := viewer.do("GET", "/integrations", nil, nil); code != http.StatusForbidden {
		t.Fatalf("viewer listed integrations: %d", code)
	}
	got := owner.must("GET", "/integrations/prom", nil, 200)
	if got["has_secrets"] != true || got["secrets"] != nil {
		t.Fatalf("integration view: %v", got)
	}
	owner.must("POST", "/bindings", map[string]any{"name": "rps", "integration": "prom", "query": `sum(rate(http_requests_total{service="{{name}}"}[{{window}}]))`,
		"params": []any{map[string]any{"name": "name", "type": "label", "from": "/metadata/name"}}, "unit": "req/s"}, 201)
	if code, _, _ := viewer.do("POST", "/bindings", map[string]any{"name": "x", "integration": "prom", "query": "up"}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer authored a binding: %d", code)
	}
	list := viewer.must("GET", "/bindings", nil, 200)
	if raw, _ := list["_raw"].(string); !strings.Contains(raw, `"name":"rps"`) || strings.Contains(raw, "http_requests_total") {
		t.Fatal("binding query text shown to a viewer")
	}
	res := viewer.must("GET", "/entities/"+a+"/telemetry", nil, 200)
	r0 := res["results"].([]any)[0].(map[string]any)
	if r0["status"] != "measured" || r0["data"].(map[string]any)["value"].(float64) != 3 {
		t.Fatalf("entity telemetry: %v", res)
	}
	if code, _, _ := viewer.do("GET", "/entities/"+b+"/telemetry", nil, nil); code != http.StatusNotFound {
		t.Fatalf("telemetry of an unreadable entity: %d", code)
	}
	out := viewer.must("POST", "/telemetry/edge-rates", map[string]any{"relation_ids": []string{rel["id"].(string)}}, 200)
	if len(out["rates"].([]any)) != 0 {
		t.Fatalf("edge rate of an unreadable relation: %v", out)
	}

	// The telemetry stream sends a snapshot immediately and refuses unreadable entities.
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/stream/telemetry?entity="+b, nil)
	req.Header.Set("Authorization", "Bearer "+viewer.bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stream of an unreadable entity: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.srv.URL+"/api/v1/stream/telemetry?entity="+a, nil)
	req.Header.Set("Authorization", "Bearer "+viewer.bearer)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "event: ") {
			if line != "event: telemetry" {
				t.Fatalf("first event = %s", line)
			}
			break
		}
	}
}
