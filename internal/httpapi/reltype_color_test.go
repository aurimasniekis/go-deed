package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// column runs a one-column query against the store.
func (h *harness) column(t *testing.T, query string, args ...any) []string {
	t.Helper()
	rows, err := h.env.Store.R().QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// list fetches a JSON array.
func (c *client) list(path string) []map[string]any {
	c.h.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.h.srv.URL+"/api/v1"+path, nil)
	req.Header.Set("Origin", c.h.srv.URL)
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.h.t.Fatalf("GET %s: %d %v", path, resp.StatusCode, err)
	}
	return out
}

// Installation admins change the colour of a built-in relationship type (an override that keeps the definition), reset
// it to the default, and every change is audited, published and guarded by If-Match.
func TestBuiltinRelTypeColorOverride(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	viewer := owner.token(map[string]any{"name": "ro", "project_role": "viewer", "projects": []string{"home"}})

	code, before, hdr := owner.do("GET", "/relationship-types/runs_on", nil, nil)
	if code != 200 || before["builtin"] != true || before["color_overridden"] != false || before["default_color"] != before["color"] {
		t.Fatalf("built-in before: %d %v", code, before)
	}
	def := before["color"].(string)
	etag := hdr.Get("ETag")

	// Only installation admins.
	if code, _, _ := viewer.do("PUT", "/relationship-types/runs_on/color", map[string]any{"color": "#123ABC"}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer changed a colour: %d", code)
	}
	// Bad colours and stale versions are refused.
	if code, _, _ := owner.do("PUT", "/relationship-types/runs_on/color", map[string]any{"color": "red"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid colour: %d", code)
	}
	if code, _, _ := owner.do("PUT", "/relationship-types/runs_on/color", map[string]any{"color": "#123abc"}, map[string]string{"If-Match": `"999"`}); code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", code)
	}
	code, out, hdr := owner.do("PUT", "/relationship-types/runs_on/color", map[string]any{"color": "#123ABC"}, map[string]string{"If-Match": etag})
	if code != 200 || out["color"] != "#123abc" || out["default_color"] != def || out["color_overridden"] != true || hdr.Get("ETag") == etag {
		t.Fatalf("override: %d %v %s", code, out, hdr.Get("ETag"))
	}
	// The definition itself stays immutable.
	if code, _, _ := owner.do("PUT", "/relationship-types/runs_on", map[string]any{"title": "Runs on", "color": "#000000"}, nil); code != http.StatusForbidden {
		t.Fatalf("built-in definition replaced: %d", code)
	}
	for _, rt := range owner.list("/relationship-types") {
		if rt["name"] == "runs_on" && (rt["color"] != "#123abc" || rt["title"] != before["title"] || rt["impact"] != before["impact"]) {
			t.Fatalf("listed: %v", rt)
		}
	}
	// Re-seeding (an upgrade) keeps the override.
	if err := h.env.Catalog.SeedBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := owner.must("GET", "/relationship-types/runs_on", nil, 200); got["color"] != "#123abc" || got["default_color"] != def {
		t.Fatalf("override lost on seed: %v", got)
	}

	// Reset restores the default; resetting again is a no-op.
	code, _, hdr = owner.do("GET", "/relationship-types/runs_on", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := owner.do("DELETE", "/relationship-types/runs_on/color", nil, map[string]string{"If-Match": `"1"`}); code != http.StatusPreconditionFailed {
		t.Fatalf("stale reset: %d", code)
	}
	code, out, _ = owner.do("DELETE", "/relationship-types/runs_on/color", nil, map[string]string{"If-Match": hdr.Get("ETag")})
	if code != 200 || out["color"] != def || out["color_overridden"] != false {
		t.Fatalf("reset: %d %v", code, out)
	}
	owner.must("DELETE", "/relationship-types/runs_on/color", nil, 200)
	// Setting the default colour explicitly is the same as no override.
	if out := owner.must("PUT", "/relationship-types/runs_on/color", map[string]any{"color": strings.ToUpper(def)}, 200); out["color_overridden"] != false {
		t.Fatalf("default colour stored as an override: %v", out)
	}

	audit := strings.Join(h.column(t, `SELECT action || ' ' || details_json FROM audit_events WHERE target_id = ? ORDER BY seq`, "runs_on"), "\n")
	if strings.Count(audit, "reltype.color_updated") != 2 || strings.Count(audit, "reltype.color_reset") != 1 ||
		!strings.Contains(audit, `"color":"#123abc"`) || !strings.Contains(audit, `"previous_color":"`+def+`"`) {
		t.Fatalf("audit: %s", audit)
	}
	outbox := strings.Join(h.column(t, `SELECT subject || ' ' || payload_json FROM outbox WHERE subject LIKE 'evt.deed.catalog.reltype.%'`), "\n")
	if strings.Count(outbox, "evt.deed.catalog.reltype.color_updated ") != 2 || strings.Count(outbox, "evt.deed.catalog.reltype.color_reset ") != 1 ||
		!strings.Contains(outbox, `"name":"runs_on"`) {
		t.Fatalf("outbox: %s", outbox)
	}
}

// Custom types change their colour in place through the same endpoint; they have no default to reset to.
func TestCustomRelTypeColor(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/relationship-types", map[string]any{"name": "example.com/powers", "title": "Powers", "color": "#111111"}, 201)
	out := owner.must("PUT", "/relationship-types/example.com%2Fpowers/color", map[string]any{"color": "#222222"}, 200)
	if out["color"] != "#222222" || out["color_overridden"] != false || out["default_color"] != nil {
		t.Fatalf("custom colour: %v", out)
	}
	if code, _, _ := owner.do("DELETE", "/relationship-types/example.com%2Fpowers/color", nil, nil); code != http.StatusConflict {
		t.Fatalf("custom reset: %d", code)
	}
	if code, _, _ := owner.do("PUT", "/relationship-types/nope/color", map[string]any{"color": "#222222"}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown type: %d", code)
	}
}
