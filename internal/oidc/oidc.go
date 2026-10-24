// Package oidc implements OpenID Connect single sign-on as a relying party (ADR-005): authorization code flow with
// PKCE S256, nonce, server-side state bound to a __Host- cookie, RFC 9207 issuer checks, identities matched on
// (iss, sub) — never on email — and group→role mapping read from a configurable JSON path (Porch: porch.groups).
//
// Patterns adapted from HallPass internal/oidc/flow.go (Apache-2.0).
package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/config"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Flow purposes.
const (
	PurposeLogin  = "login"
	PurposeReauth = "reauth"
	PurposeLink   = "link"
)

// StateCookie binds the browser to its authorization request.
const (
	StateCookie    = "__Host-deed_oidc"
	DevStateCookie = "deed_oidc"
	stateTTL       = 10 * time.Minute
)

// Errors shown to users.
var (
	ErrInvalidState   = apperr.New(apperr.CodeInvalid, "sign-in session expired or invalid; start again")
	ErrNotProvisioned = apperr.New(apperr.CodeForbidden, "this identity is not provisioned in Deed; ask an administrator")
	ErrRejected       = apperr.New(apperr.CodeForbidden, "sign-in was rejected by policy")
	ErrValidation     = apperr.New(apperr.CodeUnauthorized, "the identity provider's response could not be validated")
	ErrUnavailable    = apperr.New(apperr.CodeUnavailable, "the identity provider is unavailable")
	ErrUnknown        = apperr.New(apperr.CodeNotFound, "unknown identity provider")
)

// ProjectResolver maps project slugs to IDs (group mappings name projects by slug).
type ProjectResolver func(ctx context.Context, slug string) (string, error)

// Service runs OIDC flows for every configured provider.
type Service struct {
	Store     *store.Store
	Keyring   *keyring.Keyring
	Audit     *audit.Writer
	Authn     *authn.Service
	Clock     clock.Clock
	Metrics   *metrics.Metrics
	Log       *slog.Logger
	PublicURL string
	Secure    bool // https deployment: __Host- cookie
	HTTP      *http.Client
	Projects  ProjectResolver

	mu        sync.Mutex
	providers map[string]*provider
	order     []string
}

type provider struct {
	cfg    config.OIDCProvider
	secret string

	mu       sync.Mutex
	op       *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	issParam bool // authorization_response_iss_parameter_supported
	fetched  time.Time
}

// Configure loads the providers.
func (s *Service) Configure(list []config.OIDCProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers = map[string]*provider{}
	s.order = nil
	for _, p := range list {
		secret, err := config.ReadSecret(p.ClientSecret, p.ClientSecretFile)
		if err != nil {
			return fmt.Errorf("oidc provider %s: %w", p.Slug, err)
		}
		if p.GroupsClaim == "" {
			p.GroupsClaim = "porch.groups"
		}
		if len(p.Scopes) == 0 {
			p.Scopes = []string{gooidc.ScopeOpenID, "profile", "email", "groups"}
		}
		if !slices.Contains(p.Scopes, gooidc.ScopeOpenID) {
			p.Scopes = append([]string{gooidc.ScopeOpenID}, p.Scopes...)
		}
		s.providers[p.Slug] = &provider{cfg: p, secret: secret}
		s.order = append(s.order, p.Slug)
	}
	return nil
}

// ProviderInfo is public metadata for the login page.
type ProviderInfo struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// Providers lists configured providers in configuration order.
func (s *Service) Providers() []ProviderInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ProviderInfo{}
	for _, slug := range s.order {
		p := s.providers[slug]
		name := p.cfg.DisplayName
		if name == "" {
			name = slug
		}
		out = append(out, ProviderInfo{Slug: slug, DisplayName: name})
	}
	return out
}

func (s *Service) get(slug string) (*provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.providers[slug]
	if !ok {
		return nil, ErrUnknown
	}
	return p, nil
}

// discover fetches provider metadata, cached for an hour and retried on failure.
func (s *Service) discover(ctx context.Context, p *provider) (*gooidc.Provider, *gooidc.IDTokenVerifier, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.op != nil && s.Clock.Now().Sub(p.fetched) < time.Hour {
		return p.op, p.verifier, p.issParam, nil
	}
	cctx := gooidc.ClientContext(ctx, s.HTTP)
	op, err := gooidc.NewProvider(cctx, p.cfg.Issuer)
	if err != nil {
		if p.op != nil {
			return p.op, p.verifier, p.issParam, nil // keep the last good metadata during an outage
		}
		s.Log.Warn("oidc discovery failed", "provider", p.cfg.Slug, "err", err)
		return nil, nil, false, ErrUnavailable
	}
	var meta struct {
		IssParam bool     `json:"authorization_response_iss_parameter_supported"`
		Methods  []string `json:"code_challenge_methods_supported"`
	}
	_ = op.Claims(&meta)
	p.op = op
	p.verifier = op.Verifier(&gooidc.Config{ClientID: p.cfg.ClientID, Now: s.Clock.Now})
	p.issParam = meta.IssParam
	p.fetched = s.Clock.Now()
	return p.op, p.verifier, p.issParam, nil
}

func (s *Service) oauthConfig(p *provider, op *gooidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: p.cfg.ClientID, ClientSecret: p.secret, Endpoint: op.Endpoint(),
		RedirectURL: strings.TrimSuffix(s.PublicURL, "/") + "/auth/oidc/" + p.cfg.Slug + "/callback", Scopes: p.cfg.Scopes}
}

type stateSecrets struct {
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Binding  string `json:"binding"`
	UserID   string `json:"user_id,omitempty"` // reauth/link: the session's user
}

func (s *Service) stateMAC(state string) ([]byte, error) {
	return s.Keyring.MAC(keyring.PurposeToken, []byte("oidc-state|"+state))
}

func randomString() string {
	return base64.RawURLEncoding.EncodeToString(ids.RandomBytes(32))
}

// StartResult is where to send the browser, and the binding cookie to set.
type StartResult struct {
	RedirectURL string
	Cookie      *http.Cookie
}

// Start begins an authorization. For reauth and link the current principal is required.
func (s *Service) Start(ctx context.Context, slug, purpose, returnTo string, current *authn.Principal) (*StartResult, error) {
	p, err := s.get(slug)
	if err != nil {
		return nil, err
	}
	switch purpose {
	case PurposeLogin:
	case PurposeReauth, PurposeLink:
		if current == nil || current.UserID == "" || current.TokenID != "" {
			return nil, authn.ErrUnauthorized
		}
	default:
		return nil, apperr.New(apperr.CodeInvalid, "unknown purpose")
	}
	op, _, _, err := s.discover(ctx, p)
	if err != nil {
		return nil, err
	}
	state, nonce, verifier, binding := randomString(), randomString(), oauth2.GenerateVerifier(), randomString()
	sec := stateSecrets{Nonce: nonce, Verifier: verifier, Binding: binding}
	if current != nil {
		sec.UserID = current.UserID
	}
	mac, err := s.stateMAC(state)
	if err != nil {
		return nil, err
	}
	pt, _ := json.Marshal(sec)
	enc, err := s.Keyring.Seal(keyring.PurposeField, keyring.FieldAAD("oidc_states", "secrets_enc", base64.RawURLEncoding.EncodeToString(mac)), pt)
	if err != nil {
		return nil, err
	}
	returnTo = httpx.SafeReturnTo(returnTo, "/")
	err = s.Store.W().TxNamed(ctx, "oidc.start", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO oidc_states (state_mac, provider_slug, secrets_enc, purpose, return_to, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, mac, slug, enc, purpose, returnTo, tx.NowMicros(), tx.Now.Add(stateTTL).UnixMicro())
		return err
	})
	if err != nil {
		return nil, err
	}
	opts := []oauth2.AuthCodeOption{gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if purpose == PurposeReauth {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	name := DevStateCookie
	if s.Secure {
		name = StateCookie
	}
	cookie := &http.Cookie{Name: name, Value: binding, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(stateTTL.Seconds())}
	return &StartResult{RedirectURL: s.oauthConfig(p, op).AuthCodeURL(state, opts...), Cookie: cookie}, nil
}

// CookieValue returns the binding cookie from r.
func CookieValue(r *http.Request) string {
	for _, n := range []string{StateCookie, DevStateCookie} {
		if c, err := r.Cookie(n); err == nil {
			return c.Value
		}
	}
	return ""
}

// CallbackInput carries the redirect parameters.
type CallbackInput struct {
	Slug, Code, State, Iss, Error, Binding string
	Meta                                   authn.SessionMeta
	// SessionToken is the current session (reauth/link must come from the same user).
	Current *authn.Principal
}

// CallbackResult tells the handler what to do next.
type CallbackResult struct {
	SessionToken string // new session cookie (login, reauth)
	ReturnTo     string
}

// Claims are the validated identity claims Deed uses.
type Claims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
	Tenant        string
	AMR           []string
	AuthTime      time.Time
}

// Callback completes the flow.
func (s *Service) Callback(ctx context.Context, in CallbackInput) (res *CallbackResult, err error) {
	defer func() {
		switch {
		case err == nil:
			s.Metrics.Login("oidc", "success")
		case errors.Is(err, ErrUnavailable):
			s.Metrics.Login("oidc", "error")
		default:
			s.Metrics.Login("oidc", "failure")
		}
	}()
	p, err := s.get(in.Slug)
	if err != nil {
		return nil, err
	}
	if in.State == "" || len(in.State) > 128 {
		return nil, ErrInvalidState
	}
	mac, err := s.stateMAC(in.State)
	if err != nil {
		return nil, err
	}
	var enc []byte
	var purpose, returnTo, slug string
	var exp int64
	err = s.Store.W().TxNamed(ctx, "oidc.take_state", func(tx *store.Tx) error {
		err := tx.QueryRow(ctx, `SELECT secrets_enc, purpose, return_to, provider_slug, expires_at FROM oidc_states WHERE state_mac = ?`, mac).
			Scan(&enc, &purpose, &returnTo, &slug, &exp)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidState
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM oidc_states WHERE state_mac = ?`, mac) // single use
		return err
	})
	if err != nil {
		return nil, err
	}
	if slug != in.Slug || !s.Clock.Now().Before(clock.FromMicros(exp)) {
		return nil, ErrInvalidState
	}
	pt, err := s.Keyring.Open(keyring.PurposeField, keyring.FieldAAD("oidc_states", "secrets_enc", base64.RawURLEncoding.EncodeToString(mac)), enc)
	if err != nil {
		return nil, ErrInvalidState
	}
	var sec stateSecrets
	if err := json.Unmarshal(pt, &sec); err != nil {
		return nil, ErrInvalidState
	}
	if in.Binding == "" || !hmac.Equal([]byte(in.Binding), []byte(sec.Binding)) {
		return nil, ErrInvalidState
	}
	if in.Error != "" {
		s.deny(ctx, p, nil, in.Meta, "provider_error")
		return nil, apperr.New(apperr.CodeForbidden, "the identity provider returned an error")
	}
	op, verifier, issParam, err := s.discover(ctx, p)
	if err != nil {
		return nil, err
	}
	// RFC 9207: when present, iss must be the configured issuer; when the provider advertises support it must be present.
	if (in.Iss != "" && in.Iss != p.cfg.Issuer) || (issParam && in.Iss == "") {
		s.deny(ctx, p, nil, in.Meta, "issuer_mismatch")
		return nil, ErrValidation
	}
	cctx := gooidc.ClientContext(ctx, s.HTTP)
	tok, err := s.oauthConfig(p, op).Exchange(cctx, in.Code, oauth2.VerifierOption(sec.Verifier))
	if err != nil {
		s.Log.Warn("oidc token exchange failed", "provider", p.cfg.Slug, "err", err)
		s.deny(ctx, p, nil, in.Meta, "token_exchange_failed")
		return nil, ErrValidation
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, ErrValidation
	}
	idt, err := verifier.Verify(cctx, rawID)
	if err != nil {
		s.Log.Warn("oidc id token rejected", "provider", p.cfg.Slug, "err", err)
		s.deny(ctx, p, nil, in.Meta, "id_token_invalid")
		return nil, ErrValidation
	}
	sum := sha256.Sum256([]byte(sec.Nonce))
	got := sha256.Sum256([]byte(idt.Nonce))
	if !hmac.Equal(sum[:], got[:]) {
		s.deny(ctx, p, nil, in.Meta, "nonce_mismatch")
		return nil, ErrValidation
	}
	var raw map[string]any
	if err := idt.Claims(&raw); err != nil {
		return nil, ErrValidation
	}
	// Userinfo may carry claims the ID token omits (groups); ID token values win when both are present.
	if ui, err := op.UserInfo(cctx, oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == idt.Subject {
		var extra map[string]any
		if ui.Claims(&extra) == nil {
			for k, v := range extra {
				if _, ok := raw[k]; !ok {
					raw[k] = v
				}
			}
		}
	}
	c := extractClaims(idt.Issuer, idt.Subject, raw, p.cfg)
	if reason := policyReject(p.cfg, c); reason != "" {
		s.deny(ctx, p, c, in.Meta, reason)
		return nil, ErrRejected
	}
	switch purpose {
	case PurposeReauth:
		if in.Current == nil || in.Current.UserID != sec.UserID {
			return nil, ErrInvalidState
		}
		if c.AuthTime.IsZero() || s.Clock.Now().Sub(c.AuthTime) > 10*time.Minute {
			s.deny(ctx, p, c, in.Meta, "stale_auth_time")
			return nil, apperr.New(apperr.CodeForbidden, "the identity provider did not confirm a fresh sign-in")
		}
		tok, err := s.reauth(ctx, p, c, in)
		if err != nil {
			return nil, err
		}
		return &CallbackResult{SessionToken: tok, ReturnTo: returnTo}, nil
	case PurposeLink:
		if in.Current == nil || in.Current.UserID != sec.UserID {
			return nil, ErrInvalidState
		}
		if err := s.link(ctx, p, c, in); err != nil {
			return nil, err
		}
		return &CallbackResult{ReturnTo: returnTo}, nil
	}
	tokn, err := s.login(ctx, p, c, in.Meta)
	if err != nil {
		return nil, err
	}
	return &CallbackResult{SessionToken: tokn, ReturnTo: returnTo}, nil
}

// extractClaims reads the claims Deed uses, including the configurable groups and tenant paths.
func extractClaims(iss, sub string, raw map[string]any, cfg config.OIDCProvider) *Claims {
	c := &Claims{Issuer: iss, Subject: sub}
	c.Email, _ = raw["email"].(string)
	c.EmailVerified, _ = raw["email_verified"].(bool)
	c.Name, _ = raw["name"].(string)
	if c.Name == "" {
		c.Name, _ = raw["preferred_username"].(string)
	}
	c.Groups = stringsAt(raw, cfg.GroupsClaim)
	if cfg.TenantClaim != "" {
		if v := lookup(raw, cfg.TenantClaim); v != nil {
			c.Tenant, _ = v.(string)
		}
	}
	c.AMR = stringsAt(raw, "amr")
	if at, ok := raw["auth_time"].(float64); ok {
		c.AuthTime = time.Unix(int64(at), 0)
	}
	return c
}

// lookup resolves a dotted JSON path ("porch.groups"). A literal key containing dots is tried first.
func lookup(m map[string]any, path string) any {
	if v, ok := m[path]; ok {
		return v
	}
	var cur any = m
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[part]
	}
	return cur
}

// stringsAt returns the strings at path: an array of strings, or a single string. Nested arrays of objects with a
// "name"/"slug" field (group objects) are flattened to those names. At most 256 groups of ≤ 256 bytes are kept.
func stringsAt(m map[string]any, path string) []string {
	v := lookup(m, path)
	var out []string
	add := func(s string) {
		if s != "" && len(s) <= 256 && len(out) < 256 && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, e := range t {
			switch g := e.(type) {
			case string:
				add(g)
			case map[string]any:
				for _, k := range []string{"slug", "name", "id"} {
					if s, ok := g[k].(string); ok {
						add(s)
						break
					}
				}
			}
		}
	}
	return out
}

func policyReject(cfg config.OIDCProvider, c *Claims) string {
	if cfg.RequiredTenant != "" && c.Tenant != cfg.RequiredTenant {
		return "tenant_mismatch"
	}
	if cfg.RequireEmailVerified && !c.EmailVerified {
		return "email_not_verified"
	}
	if len(cfg.AllowedAMR) > 0 {
		ok := false
		for _, a := range c.AMR {
			if slices.Contains(cfg.AllowedAMR, a) {
				ok = true
			}
		}
		if !ok {
			return "amr_not_allowed"
		}
	}
	return ""
}

// grants evaluates the provider's mappings for the user's groups.
type grants struct {
	installationRole string
	projects         map[string]string // project ID → role
	any              bool
}

var instRank = map[string]int{"": 0, authn.RoleAuditor: 1, authn.RoleAdmin: 2, authn.RoleOwner: 3}
var projRank = map[string]int{"": 0, authn.ProjectViewer: 1, authn.ProjectEditor: 2, authn.ProjectAdmin: 3}

func (s *Service) evaluate(ctx context.Context, cfg config.OIDCProvider, groups []string) (grants, error) {
	g := grants{projects: map[string]string{}}
	for _, m := range cfg.Mappings {
		if !slices.Contains(groups, m.Group) {
			continue
		}
		if instRank[m.InstallationRole] > instRank[g.installationRole] {
			g.installationRole = m.InstallationRole
		}
		if m.ProjectRole != "" && s.Projects != nil {
			pid, err := s.Projects(ctx, m.Project)
			if err != nil {
				s.Log.Warn("oidc mapping names an unknown project", "provider", cfg.Slug, "project", m.Project)
				continue
			}
			if projRank[m.ProjectRole] > projRank[g.projects[pid]] {
				g.projects[pid] = m.ProjectRole
			}
		}
		g.any = true
	}
	return g, nil
}

func (s *Service) login(ctx context.Context, p *provider, c *Claims, meta authn.SessionMeta) (string, error) {
	g, err := s.evaluate(ctx, p.cfg, c.Groups)
	if err != nil {
		return "", err
	}
	meta.Provider = p.cfg.Slug
	var token string
	err = s.Store.W().TxNamed(ctx, "oidc.login", func(tx *store.Tx) error {
		var userID, status string
		err := tx.QueryRow(ctx, `SELECT u.id, u.status FROM identities i JOIN users u ON u.id = i.user_id WHERE i.issuer = ? AND i.subject = ?`,
			c.Issuer, c.Subject).Scan(&userID, &status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !p.cfg.JIT || !g.any {
				return ErrNotProvisioned
			}
			userID = ids.New(ids.User)
			email := c.Email
			if email != "" {
				var n int
				if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, email).Scan(&n); err != nil {
					return err
				}
				if n > 0 || !c.EmailVerified {
					email = "" // never take over or merge with an existing account by email
				}
			}
			name := c.Name
			if name == "" {
				name = c.Email
			}
			if name == "" {
				name = p.cfg.Slug + " user"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, display_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
				userID, nullStr(email), truncate(name, 128), tx.NowMicros(), tx.NowMicros()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO identities (id, user_id, provider_slug, issuer, subject, email, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				ids.New(ids.Identity), userID, p.cfg.Slug, c.Issuer, c.Subject, nullStr(c.Email), tx.NowMicros()); err != nil {
				return err
			}
			if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "user.provisioned", Target: audit.Target{Type: "user", ID: userID},
				Details: map[string]any{"provider": p.cfg.Slug}}); err != nil {
				return err
			}
			status = "active"
		case err != nil:
			return err
		}
		if status != "active" {
			return ErrRejected
		}
		gj, _ := json.Marshal(c.Groups)
		if _, err := tx.Exec(ctx, `UPDATE identities SET last_login_at = ?, groups_json = ?, email = ? WHERE issuer = ? AND subject = ?`,
			tx.NowMicros(), string(gj), nullStr(c.Email), c.Issuer, c.Subject); err != nil {
			return err
		}
		if len(p.cfg.Mappings) > 0 {
			if err := s.syncRoles(ctx, tx, p.cfg.Slug, userID, g); err != nil {
				return err
			}
		}
		token, err = s.Authn.CreateSessionTx(ctx, tx, userID, authn.MethodOIDC, meta)
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "auth.login",
			SourceIP: meta.IP, AuthMethod: authn.MethodOIDC, Details: map[string]any{"provider": p.cfg.Slug, "groups": len(c.Groups)}})
		return err
	})
	if errors.Is(err, ErrNotProvisioned) || errors.Is(err, ErrRejected) {
		s.deny(ctx, p, c, meta, string(apperr.CodeOf(err)))
	}
	return token, err
}

// syncRoles applies mapping results. Roles granted by this provider carry role_source "oidc:<slug>" and are removed
// when the mapping no longer grants them; manual grants are never lowered. The last owner is never demoted.
func (s *Service) syncRoles(ctx context.Context, tx *store.Tx, slug, userID string, g grants) error {
	src := "oidc:" + slug
	var cur, curSrc string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(installation_role, ''), role_source FROM users WHERE id = ?`, userID).Scan(&cur, &curSrc); err != nil {
		return err
	}
	next := cur
	switch {
	case instRank[g.installationRole] > instRank[cur]:
		next = g.installationRole
	case curSrc == src && g.installationRole != cur:
		next = g.installationRole
	}
	if next != cur {
		if cur == authn.RoleOwner {
			var owners int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner' AND status = 'active'`).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				next = cur
			}
		}
	}
	if next != cur {
		if _, err := tx.Exec(ctx, `UPDATE users SET installation_role = ?, role_source = ?, updated_at = ? WHERE id = ?`, nullStr(next), src, tx.NowMicros(), userID); err != nil {
			return err
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "user.role_synced", Target: audit.Target{Type: "user", ID: userID},
			Details: map[string]any{"provider": slug, "from": cur, "to": next}}); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT project_id, role, role_source FROM project_members WHERE user_id = ?`, userID)
	if err != nil {
		return err
	}
	type mem struct{ role, src string }
	existing := map[string]mem{}
	for rows.Next() {
		var pid string
		var m mem
		if err := rows.Scan(&pid, &m.role, &m.src); err != nil {
			rows.Close()
			return err
		}
		existing[pid] = m
	}
	rows.Close()
	for pid, role := range g.projects {
		m, ok := existing[pid]
		if ok && (m.src != src && projRank[m.role] >= projRank[role] || m.role == role) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, role_source, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = excluded.role, role_source = excluded.role_source`, pid, userID, role, src, tx.NowMicros()); err != nil {
			return err
		}
	}
	for pid, m := range existing {
		if _, still := g.projects[pid]; !still && m.src == src {
			if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, pid, userID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) reauth(ctx context.Context, p *provider, c *Claims, in CallbackInput) (string, error) {
	var token string
	err := s.Store.W().TxNamed(ctx, "oidc.reauth", func(tx *store.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `SELECT user_id FROM identities WHERE issuer = ? AND subject = ?`, c.Issuer, c.Subject).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) || userID != in.Current.UserID {
			return apperr.New(apperr.CodeForbidden, "this identity belongs to a different account")
		}
		if err != nil {
			return err
		}
		meta := in.Meta
		meta.Provider = p.cfg.Slug
		token, err = s.Authn.RotateTx(ctx, tx, in.Current, meta)
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: in.Current.Actor(), Action: "auth.reauth", AuthMethod: authn.MethodOIDC, SourceIP: in.Meta.IP})
		return err
	})
	return token, err
}

func (s *Service) link(ctx context.Context, p *provider, c *Claims, in CallbackInput) error {
	return s.Store.W().TxNamed(ctx, "oidc.link", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identities (id, user_id, provider_slug, issuer, subject, email, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ids.New(ids.Identity), in.Current.UserID, p.cfg.Slug, c.Issuer, c.Subject, nullStr(c.Email), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeConflict, "this identity is already linked to an account")
		}
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: in.Current.Actor(), Action: "user.identity_linked", Target: audit.Target{Type: "user", ID: in.Current.UserID},
			Details: map[string]any{"provider": p.cfg.Slug}})
		return err
	})
}

// Identity is a linked external identity.
type Identity struct {
	ID          string     `json:"id"`
	Provider    string     `json:"provider"`
	Subject     string     `json:"subject"`
	Email       string     `json:"email,omitempty"`
	Groups      []string   `json:"groups"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Identities lists a user's linked identities.
func (s *Service) Identities(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT id, provider_slug, subject, COALESCE(email, ''), groups_json, created_at, last_login_at
		FROM identities WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		var i Identity
		var gj string
		var c int64
		var l sql.NullInt64
		if err := rows.Scan(&i.ID, &i.Provider, &i.Subject, &i.Email, &gj, &c, &l); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(gj), &i.Groups)
		if i.Groups == nil {
			i.Groups = []string{}
		}
		i.CreatedAt = clock.FromMicros(c)
		if l.Valid {
			t := clock.FromMicros(l.Int64)
			i.LastLoginAt = &t
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Unlink removes a linked identity (the user must keep another way to sign in).
func (s *Service) Unlink(ctx context.Context, actor audit.Actor, userID, identityID string) error {
	return s.Store.W().TxNamed(ctx, "oidc.unlink", func(tx *store.Tx) error {
		var others, hasPw int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM identities WHERE user_id = ? AND id != ?`, userID, identityID).Scan(&others); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM local_credentials WHERE user_id = ?`, userID).Scan(&hasPw); err != nil {
			return err
		}
		if others == 0 && hasPw == 0 {
			return apperr.New(apperr.CodeConflict, "set a password or link another identity first")
		}
		n, err := tx.ExecN(ctx, `DELETE FROM identities WHERE id = ? AND user_id = ?`, identityID, userID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "identity not found")
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.identity_unlinked", Target: audit.Target{Type: "user", ID: userID}})
		return err
	})
}

func (s *Service) deny(ctx context.Context, p *provider, c *Claims, meta authn.SessionMeta, reason string) {
	details := map[string]any{"provider": p.cfg.Slug}
	if c != nil {
		details["subject_hash"] = shortHash(c.Issuer + "|" + c.Subject)
	}
	_ = s.Store.W().TxNamed(context.WithoutCancel(ctx), "oidc.denied", func(tx *store.Tx) error {
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser}, Action: "auth.login", Outcome: audit.OutcomeDenied,
			ReasonCode: reason, SourceIP: meta.IP, AuthMethod: authn.MethodOIDC, Details: details})
		return err
	})
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:9])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
