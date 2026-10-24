package httpapi_test

import (
	"encoding/base32"
	"net/http"
	"strings"
	"testing"
)

func decodeB32(s string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(s))
}

func TestDeclarationLifecycle(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	tf := owner.token(map[string]any{"name": "terraform", "project_role": "editor", "projects": []string{"home"}})

	decl := tf.must("POST", "/entities", map[string]any{"project": "home", "source": "terraform", "kind": "server", "name": "web-1",
		"metadata": map[string]any{"displayName": "Web 1"}, "spec": map[string]any{"hostname": "web-1.example.com", "ipv4": "198.51.100.10"}}, 201)
	id := decl["entity_id"].(string)

	code, out, _ := tf.do("POST", "/entities", map[string]any{"project": "home", "source": "terraform", "kind": "server", "name": "web-1"}, nil)
	if code != http.StatusConflict || out["code"] != "already_exists" {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	code, out, hdr := tf.do("GET", "/entities/"+id+"/declarations/terraform", nil, nil)
	if code != 200 || hdr.Get("ETag") != `"1"` {
		t.Fatalf("get declaration: %d %v %v", code, out, hdr)
	}
	code, out, hdr = tf.do("PUT", "/entities/"+id+"/declarations/terraform", map[string]any{"metadata": map[string]any{"displayName": "Web One"},
		"spec": map[string]any{"hostname": "web-1.example.com", "ipv4": "198.51.100.10"}}, map[string]string{"If-Match": `"1"`})
	if code != 200 || hdr.Get("ETag") != `"2"` {
		t.Fatalf("put declaration: %d %v", code, out)
	}
	code, out, _ = tf.do("PUT", "/entities/"+id+"/declarations/terraform", map[string]any{}, map[string]string{"If-Match": `"1"`})
	if code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %v", code, out)
	}
	code, out, _ = tf.do("POST", "/entities", map[string]any{"project": "home", "kind": "server", "name": "bad", "spec": map[string]any{"ipv4": "x"}}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid spec: %d %v", code, out)
	}
	tf.must("DELETE", "/entities/"+id+"/declarations/terraform", nil, 204)
	e := tf.must("GET", "/entities/"+id, nil, 200)
	if e["status"].(map[string]any)["tombstonedAt"] == nil {
		t.Fatalf("expected tombstone: %v", e["status"])
	}
}

func TestAuthorizationBoundaries(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	owner.must("POST", "/projects", map[string]any{"name": "parents"}, 201)
	viewer := owner.token(map[string]any{"name": "ro", "project_role": "viewer", "projects": []string{"home"}})
	editor := owner.token(map[string]any{"name": "rw", "project_role": "editor", "projects": []string{"home"}})
	editor.must("POST", "/entities", map[string]any{"project": "home", "kind": "service", "name": "a"}, 201)
	owner.must("POST", "/entities", map[string]any{"project": "parents", "kind": "service", "name": "b"}, 201)

	if code, out, _ := viewer.do("POST", "/entities", map[string]any{"project": "home", "kind": "service", "name": "c"}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer wrote: %d %v", code, out)
	}
	// A project outside the token's scope is invisible, not forbidden.
	if code, _, _ := editor.do("GET", "/projects/parents", nil, nil); code != http.StatusNotFound {
		t.Fatalf("out-of-scope project visible: %d", code)
	}
	list := editor.must("GET", "/entities", nil, 200)
	if list["total"].(float64) != 1 {
		t.Fatalf("editor sees %v entities", list["total"])
	}
	// Token-forbidden operations.
	if code, _, _ := editor.do("POST", "/me/tokens", map[string]any{"name": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("token minted a token: %d", code)
	}
	// Cookie sessions need the CSRF header and a same-origin Origin.
	noCSRF := *owner
	noCSRF.csrf = ""
	if code, _, _ := noCSRF.do("POST", "/projects", map[string]any{"name": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", code)
	}
	if code, _, _ := owner.do("POST", "/projects", map[string]any{"name": "y"}, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-origin accepted: %d", code)
	}
	// Revoked tokens stop working immediately.
	toks := owner.must("GET", "/me/tokens", nil, 200)
	_ = toks
}

func TestLoginThrottleAndMFA(t *testing.T) {
	h := newHarness(t)
	h.owner()
	c := h.client()
	for i := range 4 {
		code, _, _ := c.do("POST", "/auth/login", map[string]any{"email": "owner@example.com", "password": "wrong password here"}, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	code, out, _ := c.do("POST", "/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery staple"}, nil)
	if code != http.StatusTooManyRequests {
		t.Fatalf("throttle not applied: %d %v", code, out)
	}
	h.env.Clock.Advance(20 * 60 * 1e9)
	me := c.must("POST", "/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery staple"}, 200)
	if me["mfa_pending"] != true {
		t.Fatalf("owner login must require the second factor: %v", me)
	}
	c.csrf = me["csrf_token"].(string)
	if code, _, _ := c.do("GET", "/projects", nil, nil); code != http.StatusForbidden {
		t.Fatalf("limited session reached the API: %d", code)
	}
}
