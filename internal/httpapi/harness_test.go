package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/oidc"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// fastParams keep Argon2id cheap in tests.
var fastParams = authn.Params{Version: authn.CurrentParams.Version, Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

type harness struct {
	t    *testing.T
	env  *testenv.Env
	srv  *httptest.Server
	auth *authn.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := testenv.New(t)
	auth := authn.New(env.Store, env.Keyring, env.Audit, authn.Options{Clock: env.Clock, Params: &fastParams, LocalEnabled: true,
		MFARequiredRoles: []string{"owner", "admin"}})
	resolver, _ := httpx.NewClientIPResolver(nil, nil)
	h := &harness{t: t, env: env, auth: auth}
	mux := http.NewServeMux()
	d := &httpapi.Deps{Version: "test", Store: env.Store, Keyring: env.Keyring, Audit: env.Audit, Authn: auth, Catalog: env.Catalog,
		Clock: env.Clock, LocalAuth: true, OIDC: &oidc.Service{}}
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 10000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	runner := &sources.Runner{Store: env.Store, Catalog: env.Catalog, Ingest: ing, Audit: env.Audit, Registry: sources.NewRegistry(), Clock: env.Clock}
	d.Extensions = append(d.Extensions, (&sources.API{Runner: runner, Ingest: ing}).Register)
	h.srv = httptest.NewServer(resolver.Middleware(httpx.RequestID(mux)))
	d.Origin = h.srv.URL
	httpapi.New(mux, d)
	t.Cleanup(h.srv.Close)
	return h
}

type client struct {
	h      *harness
	c      *http.Client
	csrf   string
	bearer string
}

func (h *harness) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (c *client) do(method, path string, body any, hdr map[string]string) (int, map[string]any, http.Header) {
	c.h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, c.h.srv.URL+"/api/v1"+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.h.srv.URL)
	if c.csrf != "" {
		req.Header.Set("X-Deed-CSRF", c.csrf)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(data) > 0 && strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		_ = json.Unmarshal(data, &out)
	} else if len(data) > 0 {
		out = map[string]any{"_raw": string(data)}
	}
	return resp.StatusCode, out, resp.Header
}

func (c *client) must(method, path string, body any, want int) map[string]any {
	c.h.t.Helper()
	code, out, _ := c.do(method, path, body, nil)
	if code != want {
		c.h.t.Fatalf("%s %s: status %d want %d: %v", method, path, code, want, out)
	}
	return out
}

// owner bootstraps the first owner with TOTP and returns a signed-in client.
func (h *harness) owner() *client {
	h.t.Helper()
	ctx := context.Background()
	l, err := h.auth.IssueBootstrap(ctx, time.Hour)
	if err != nil {
		h.t.Fatal(err)
	}
	c := h.client()
	me := c.must("POST", "/auth/setup/complete", map[string]any{"token": l.Token, "email": "owner@example.com", "display_name": "Owner",
		"password": "correct horse battery staple"}, 200)
	c.csrf = me["csrf_token"].(string)
	e := c.must("POST", "/me/mfa/totp", map[string]any{}, 200)
	secret := e["secret"].(string)
	code := totpNow(h, secret)
	c.must("POST", "/me/mfa/totp/confirm", map[string]any{"code": code}, 200)
	me = c.must("GET", "/me", nil, 200)
	c.csrf = me["csrf_token"].(string)
	return c
}

func totpNow(h *harness, secret string) string {
	h.t.Helper()
	raw, err := decodeB32(secret)
	if err != nil {
		h.t.Fatal(err)
	}
	return authn.TOTPCode(raw, authn.TOTPStep(h.env.Clock.Now()))
}

// token creates an API token for c's user.
func (c *client) token(body map[string]any) *client {
	c.h.t.Helper()
	out := c.must("POST", "/me/tokens", body, 201)
	t := c.h.client()
	t.bearer = out["token"].(string)
	return t
}
