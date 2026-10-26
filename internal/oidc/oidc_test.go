package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/config"
	"github.com/aurimasniekis/go-deed/internal/oidc"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// testOP is a minimal OpenID provider: discovery, JWKS, a token endpoint that checks PKCE, and userinfo.
type testOP struct {
	*httptest.Server
	key  *ecdsa.PrivateKey
	now  func() time.Time
	mu   sync.Mutex
	code map[string]grant // code → what the authorization request carried
	// Per-test knobs.
	claims     map[string]any // extra ID token claims
	userinfo   map[string]any // extra userinfo claims
	wrongNonce bool
	issParam   bool
	scope      string // granted scope in the token response ("" = omitted)
}

type grant struct {
	nonce, challenge, clientID string
}

func newOP(t *testing.T, now func() time.Time) *testOP {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	op := &testOP{key: k, now: now, code: map[string]grant{}, issParam: true}
	mux := http.NewServeMux()
	op.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": op.URL, "authorization_endpoint": op.URL + "/authorize", "token_endpoint": op.URL + "/token",
			"jwks_uri": op.URL + "/jwks", "userinfo_endpoint": op.URL + "/userinfo", "id_token_signing_alg_values_supported": []string{"ES256"},
			"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": op.issParam})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "ES256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		op.mu.Lock()
		g, ok := op.code[r.Form.Get("code")]
		delete(op.code, r.Form.Get("code"))
		op.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		nonce := g.nonce
		if op.wrongNonce {
			nonce = "something-else"
		}
		c := map[string]any{"iss": op.URL, "sub": "u_01", "aud": g.clientID, "exp": op.now().Add(5 * time.Minute).Unix(), "iat": op.now().Unix(),
			"auth_time": op.now().Unix(), "nonce": nonce, "email": "alice@example.com", "email_verified": true, "name": "Alice"}
		for k, v := range op.claims {
			c[k] = v
		}
		resp := map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": op.sign(t, c)}
		if op.scope != "" {
			resp["scope"] = op.scope
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		u := map[string]any{"sub": "u_01"}
		for k, v := range op.userinfo {
			u[k] = v
		}
		_ = json.NewEncoder(w).Encode(u)
	})
	t.Cleanup(op.Close)
	return op
}

func (op *testOP) sign(t *testing.T, claims map[string]any) string {
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: op.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(s).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// authorize plays the browser + OP: it reads Deed's authorization URL and returns a code bound to its nonce and
// PKCE challenge.
func (op *testOP) authorize(t *testing.T, redirect string) (code, state string) {
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
		t.Fatalf("authorization request without PKCE S256 or nonce: %s", redirect)
	}
	code = base64.RawURLEncoding.EncodeToString([]byte(q.Get("state")))[:16]
	op.mu.Lock()
	op.code[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), clientID: q.Get("client_id")}
	op.mu.Unlock()
	return code, q.Get("state")
}

type rig struct {
	env *testenv.Env
	op  *testOP
	svc *oidc.Service
}

func newRig(t *testing.T, cfg config.OIDCProvider) *rig {
	env := testenv.New(t)
	op := newOP(t, env.Clock.Now)
	params := authn.Params{Version: authn.CurrentParams.Version, Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}
	auth := authn.New(env.Store, env.Keyring, env.Audit, authn.Options{Clock: env.Clock, Params: &params, LocalEnabled: true})
	svc := &oidc.Service{Store: env.Store, Keyring: env.Keyring, Audit: env.Audit, Authn: auth, Clock: env.Clock, Log: slog.New(slog.DiscardHandler),
		PublicURL: "https://deed.example.com", HTTP: http.DefaultClient,
		Projects: func(ctx context.Context, slug string) (string, error) {
			p, err := env.Catalog.Project(ctx, slug)
			if err != nil {
				return "", err
			}
			return p.ID, nil
		}}
	cfg.Slug, cfg.Issuer, cfg.ClientID, cfg.ClientSecret = "porch", op.URL, "deed", "client-secret"
	if err := svc.Configure([]config.OIDCProvider{cfg}); err != nil {
		t.Fatal(err)
	}
	return &rig{env: env, op: op, svc: svc}
}

func (r *rig) login(t *testing.T, mutate func(*oidc.CallbackInput)) (*oidc.CallbackResult, error) {
	t.Helper()
	start, err := r.svc.Start(context.Background(), "porch", oidc.PurposeLogin, "/inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	if start.Cookie.Name != oidc.DevStateCookie || !start.Cookie.HttpOnly {
		t.Fatalf("binding cookie: %+v", start.Cookie)
	}
	code, state := r.op.authorize(t, start.RedirectURL)
	in := oidc.CallbackInput{Slug: "porch", Code: code, State: state, Iss: r.op.URL, Binding: start.Cookie.Value}
	if mutate != nil {
		mutate(&in)
	}
	return r.svc.Callback(context.Background(), in)
}

func TestLoginJITAndRoleSync(t *testing.T) {
	r := newRig(t, config.OIDCProvider{JIT: true, Mappings: []config.GroupMapping{
		{Group: "deed-admins", InstallationRole: "admin"},
		{Group: "home-editors", Project: "home", ProjectRole: "editor"},
	}})
	r.env.Project(t, "home")
	r.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"deed-admins", "home-editors"}}}
	res, err := r.login(t, nil)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.SessionToken == "" || res.ReturnTo != "/inventory" {
		t.Fatalf("result: %+v", res)
	}
	var userID, role string
	if err := r.env.Store.R().QueryRow(`SELECT id, COALESCE(installation_role, '') FROM users`).Scan(&userID, &role); err != nil {
		t.Fatal(err)
	}
	var projRole string
	_ = r.env.Store.R().QueryRow(`SELECT role FROM project_members WHERE user_id = ?`, userID).Scan(&projRole)
	if role != "admin" || projRole != "editor" {
		t.Fatalf("roles after JIT: installation %q project %q", role, projRole)
	}
	// Groups shrink on the next login: synced roles are removed, the same user is reused.
	r.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"home-editors"}}}
	if _, err := r.login(t, nil); err != nil {
		t.Fatalf("second login: %v", err)
	}
	var users int
	_ = r.env.Store.R().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	_ = r.env.Store.R().QueryRow(`SELECT COALESCE(installation_role, '') FROM users WHERE id = ?`, userID).Scan(&role)
	if users != 1 || role != "" {
		t.Fatalf("after shrinking groups: %d users, installation role %q", users, role)
	}
}

func TestCallbackRejectsTamperedFlows(t *testing.T) {
	r := newRig(t, config.OIDCProvider{JIT: true, Mappings: []config.GroupMapping{{Group: "g", InstallationRole: "auditor"}}})
	r.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"g"}}}
	cases := map[string]struct {
		mutate func(*oidc.CallbackInput)
		before func()
		want   error
	}{
		"wrong binding cookie":   {mutate: func(in *oidc.CallbackInput) { in.Binding = "x" }, want: oidc.ErrInvalidState},
		"unknown state":          {mutate: func(in *oidc.CallbackInput) { in.State = "forged" }, want: oidc.ErrInvalidState},
		"missing iss (RFC 9207)": {mutate: func(in *oidc.CallbackInput) { in.Iss = "" }, want: oidc.ErrValidation},
		"wrong iss":              {mutate: func(in *oidc.CallbackInput) { in.Iss = "https://evil.example" }, want: oidc.ErrValidation},
		"wrong PKCE verifier":    {mutate: func(in *oidc.CallbackInput) { in.Code = "not-issued" }, want: oidc.ErrValidation},
		"nonce mismatch":         {before: func() { r.op.wrongNonce = true }, want: oidc.ErrValidation},
	}
	for name, c := range cases {
		r.op.wrongNonce = false
		if c.before != nil {
			c.before()
		}
		if _, err := r.login(t, c.mutate); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	r.op.wrongNonce = false
	// A state is single use.
	start, _ := r.svc.Start(context.Background(), "porch", oidc.PurposeLogin, "/", nil)
	code, state := r.op.authorize(t, start.RedirectURL)
	in := oidc.CallbackInput{Slug: "porch", Code: code, State: state, Iss: r.op.URL, Binding: start.Cookie.Value}
	if _, err := r.svc.Callback(context.Background(), in); err != nil {
		t.Fatalf("valid callback: %v", err)
	}
	if _, err := r.svc.Callback(context.Background(), in); !errors.Is(err, oidc.ErrInvalidState) {
		t.Fatalf("replayed state: %v", err)
	}
	// The state expires.
	start, _ = r.svc.Start(context.Background(), "porch", oidc.PurposeLogin, "/", nil)
	code, state = r.op.authorize(t, start.RedirectURL)
	r.env.Clock.Advance(time.Hour)
	if _, err := r.svc.Callback(context.Background(), oidc.CallbackInput{Slug: "porch", Code: code, State: state, Iss: r.op.URL,
		Binding: start.Cookie.Value}); !errors.Is(err, oidc.ErrInvalidState) {
		t.Fatalf("expired state: %v", err)
	}
}

func TestProvisioningPolicies(t *testing.T) {
	// Without JIT an unknown identity is refused and the refusal is audited.
	r := newRig(t, config.OIDCProvider{Mappings: []config.GroupMapping{{Group: "g", InstallationRole: "auditor"}}})
	r.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"g"}}}
	if _, err := r.login(t, nil); !errors.Is(err, oidc.ErrNotProvisioned) {
		t.Fatalf("no JIT: %v", err)
	}
	var denied int
	_ = r.env.Store.R().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE outcome = 'denied' AND reason_code = 'not_provisioned'`).Scan(&denied)
	if denied == 0 {
		t.Fatal("refused sign-in not audited")
	}
	// JIT without a matching group is refused too.
	r2 := newRig(t, config.OIDCProvider{JIT: true, Mappings: []config.GroupMapping{{Group: "other", InstallationRole: "auditor"}}})
	r2.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"g"}}}
	if _, err := r2.login(t, nil); !errors.Is(err, oidc.ErrNotProvisioned) {
		t.Fatalf("JIT without matching group: %v", err)
	}
	// A required tenant rejects other tenants; groups from userinfo count when the ID token has none.
	r3 := newRig(t, config.OIDCProvider{JIT: true, TenantClaim: "porch.tenant.slug", RequiredTenant: "home",
		Mappings: []config.GroupMapping{{Group: "g", InstallationRole: "auditor"}}})
	r3.op.claims = map[string]any{"porch": map[string]any{"tenant": map[string]any{"slug": "work"}}}
	r3.op.userinfo = map[string]any{"porch": map[string]any{"groups": []string{"g"}}}
	if _, err := r3.login(t, nil); !errors.Is(err, oidc.ErrRejected) {
		t.Fatalf("wrong tenant: %v", err)
	}
	r3.op.claims = map[string]any{"porch": map[string]any{"tenant": map[string]any{"slug": "home"}}}
	if _, err := r3.login(t, nil); err != nil {
		t.Fatalf("right tenant with userinfo groups: %v", err)
	}
	// An unverified email never merges with an existing account of the same address.
	r4 := newRig(t, config.OIDCProvider{JIT: true, Mappings: []config.GroupMapping{{Group: "g", InstallationRole: "auditor"}}})
	if err := r4.env.Store.W().Exec(context.Background(), `INSERT INTO users (id, email, display_name, created_at, updated_at)
		VALUES ('usr_existing', 'alice@example.com', 'Local Alice', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	r4.op.claims = map[string]any{"email_verified": false, "porch": map[string]any{"groups": []string{"g"}}}
	if _, err := r4.login(t, nil); err != nil {
		t.Fatalf("JIT with unverified email: %v", err)
	}
	var linked string
	_ = r4.env.Store.R().QueryRow(`SELECT user_id FROM identities`).Scan(&linked)
	if linked == "usr_existing" {
		t.Fatal("an unverified email took over an existing account")
	}
}

// Porch grants only the client's allowed_scopes and silently drops the rest (groups is not allowed by default); the
// resulting "not provisioned" refusal must come with a log line naming the missing scope.
func TestUngrantedScopesAreLogged(t *testing.T) {
	r := newRig(t, config.OIDCProvider{JIT: true, Mappings: []config.GroupMapping{{Group: "g", InstallationRole: "auditor"}}})
	var buf strings.Builder
	r.svc.Log = slog.New(slog.NewTextHandler(&buf, nil))
	r.op.scope = "openid profile email"
	if _, err := r.login(t, nil); !errors.Is(err, oidc.ErrNotProvisioned) {
		t.Fatalf("login without groups: %v", err)
	}
	if !strings.Contains(buf.String(), "did not grant every requested scope") || !strings.Contains(buf.String(), "missing_scopes=[groups]") {
		t.Fatalf("log: %s", buf.String())
	}
	buf.Reset()
	r.op.scope = "openid profile email groups"
	r.op.claims = map[string]any{"porch": map[string]any{"groups": []string{"g"}}}
	if _, err := r.login(t, nil); err != nil {
		t.Fatalf("login with groups: %v", err)
	}
	if strings.Contains(buf.String(), "did not grant") {
		t.Fatalf("warning with every scope granted: %s", buf.String())
	}
}

func TestLinkIdentity(t *testing.T) {
	// No JIT: the identity can only reach an account by being linked to it.
	r := newRig(t, config.OIDCProvider{})
	ctx := context.Background()
	alice, err := r.svc.Authn.CreateUser(ctx, audit.System, authn.CreateUserInput{Email: "alice@example.com", DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := r.svc.Authn.CreateUser(ctx, audit.System, authn.CreateUserInput{Email: "bob@example.com", DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	link := func(userID string) (*oidc.CallbackResult, error) {
		t.Helper()
		cur := &authn.Principal{UserID: userID}
		start, err := r.svc.Start(ctx, "porch", oidc.PurposeLink, "/settings/account", cur)
		if err != nil {
			t.Fatal(err)
		}
		code, state := r.op.authorize(t, start.RedirectURL)
		return r.svc.Callback(ctx, oidc.CallbackInput{Slug: "porch", Code: code, State: state, Iss: r.op.URL, Binding: start.Cookie.Value, Current: cur})
	}
	if _, err := r.svc.Start(ctx, "porch", oidc.PurposeLink, "/", nil); !errors.Is(err, authn.ErrUnauthorized) {
		t.Fatalf("link without a session: %v", err)
	}
	if res, err := link(alice.ID); err != nil || res.SessionToken != "" || res.ReturnTo != "/settings/account" {
		t.Fatalf("link: %+v, %v", res, err)
	}
	ids, err := r.svc.Identities(ctx, alice.ID)
	if err != nil || len(ids) != 1 || ids[0].Provider != "porch" {
		t.Fatalf("identities after link: %+v, %v", ids, err)
	}
	// Signing in with the linked identity reaches Alice's account.
	res, err := r.login(t, nil)
	if err != nil {
		t.Fatalf("login with the linked identity: %v", err)
	}
	p, err := r.svc.Authn.Authenticate(ctx, res.SessionToken)
	if err != nil || p.UserID != alice.ID {
		t.Fatalf("session after SSO login: %+v, %v", p, err)
	}
	// The same identity cannot be linked to a second account; the failure carries where to send the signed-in user.
	res, err = link(bob.ID)
	if apperr.CodeOf(err) != apperr.CodeConflict || res == nil || res.Purpose != oidc.PurposeLink || res.ReturnTo != "/settings/account" {
		t.Fatalf("second link: %+v, %v", res, err)
	}
}
