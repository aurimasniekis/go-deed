package deedclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRetriesReuseIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer deed_st_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "summary": map[string]any{"created": 1, "not_found": 2}})
	}))
	defer srv.Close()
	c, err := New(srv.URL, "deed_st_x", "src_1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Items(context.Background(), Batch{Entities: []Entity{{Kind: "server", Name: "a"}}}, false)
	if err != nil || !res.Applied || res.Summary.Created != 1 || res.Summary.NotFound != 2 {
		t.Fatalf("items: %+v %v", res, err)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("idempotency keys across retries: %v", keys)
	}
}

func TestErrorsAndRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sources/src_1/heartbeat" {
			http.Redirect(w, r, "https://evil.example/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"not_found","detail":"source not found"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "deed_st_x", "src_1", WithRetries(0))
	if _, err := c.Begin(context.Background(), 0, true); !IsNotFound(err) {
		t.Fatalf("not found: %v", err)
	}
	if err := c.Heartbeat(context.Background()); err == nil {
		t.Fatal("redirect followed or accepted")
	}
	if _, err := New("ftp://x", "deed_st_x", "s"); err == nil {
		t.Fatal("bad endpoint accepted")
	}
	if _, err := New(srv.URL, "ghp_x", "s"); err == nil {
		t.Fatal("non-Deed token accepted")
	}
}
