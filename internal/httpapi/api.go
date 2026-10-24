// Package httpapi serves the versioned REST API under /api/v1 with Huma v2. The OpenAPI 3.1 document is generated
// from this code (`deed openapi`), committed as api/openapi.json and drift-checked in CI (docs/VERSIONING.md).
//
// Structure adapted from Porch internal/mgmtapi (Apache-2.0).
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/oidc"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Prefix is the API mount point.
const Prefix = "/api/v1"

// CSRFHeader carries the session's CSRF token on state-changing cookie-authenticated requests.
const CSRFHeader = "X-Deed-CSRF"

// Deps are the services the API uses. Optional services may be nil; their operations then answer 503.
type Deps struct {
	Log       *slog.Logger
	Version   string
	Origin    string // public origin for Origin checks
	Secure    bool   // https deployment
	Store     *store.Store
	Keyring   *keyring.Keyring
	Audit     *audit.Writer
	Authn     *authn.Service
	OIDC      *oidc.Service
	Catalog   *catalog.Service
	Metrics   *metrics.Metrics
	Clock     clock.Clock
	LoginRate *httpx.Limiter
	SetupTTL  time.Duration
	LocalAuth bool
	// Ready reports readiness for /system/status.
	Ready func(context.Context) error
	// Extension points registered by other packages (ingest, sources, graph, telemetry, findings, backups…).
	Extensions []func(api huma.API, d *Deps)
	// Raw registers non-Huma handlers (OIDC redirects, SSE, icon files) on the mux.
	Raw []func(mux *http.ServeMux, d *Deps)
}

func (d *Deps) now() time.Time {
	if d.Clock == nil {
		return time.Now()
	}
	return d.Clock.Now()
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Log
}

// Security scheme names.
const (
	SchemeSession = "session"
	SchemeToken   = "bearer"
)

// Config returns the Huma configuration (shared with the openapi command).
func Config(version string) huma.Config {
	c := huma.DefaultConfig("Deed API", version)
	c.Servers = []*huma.Server{{URL: Prefix}}
	c.DocsPath = "" // the docs UI loads third-party scripts, which the CSP forbids
	c.OpenAPIPath = "/openapi"
	c.SchemasPath = ""
	c.CreateHooks = nil
	c.Info.Description = "Versioned API of Deed, a declarative infrastructure catalog and live relationship map. Browser clients authenticate " +
		"with the __Host-deed_session cookie and send the session's CSRF token in X-Deed-CSRF on state-changing requests. Automation uses " +
		"`Authorization: Bearer deed_at_…` (user API tokens, e.g. the Terraform provider) or `Bearer deed_st_…` (source tokens, ingest " +
		"endpoints of one source only). Changes within /api/v1 are additive; see docs/VERSIONING.md."
	c.Info.License = &huma.License{Name: "Apache-2.0", URL: "https://www.apache.org/licenses/LICENSE-2.0"}
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SchemeSession: {Type: "apiKey", In: "cookie", Name: authn.CookieName},
		SchemeToken:   {Type: "http", Scheme: "bearer", Description: "API token (deed_at_…) or source token (deed_st_…)."},
	}
	return c
}

// Secured is the security requirement of authenticated operations.
var Secured = []map[string][]string{{SchemeSession: {}}, {SchemeToken: {}}}

// New registers every operation on mux and returns the API.
func New(mux *http.ServeMux, d *Deps) huma.API {
	inner := http.NewServeMux()
	mux.Handle(Prefix+"/", d.idempotency(inner))
	api := humago.NewWithPrefix(inner, Prefix, Config(d.Version))
	api.UseMiddleware(d.authenticate(api))
	registerSystem(api, d)
	registerAuth(api, d)
	registerMe(api, d)
	registerUsers(api, d)
	registerProjects(api, d)
	registerKinds(api, d)
	registerEntities(api, d)
	registerRelations(api, d)
	registerAudit(api, d)
	for _, ext := range d.Extensions {
		ext(api, d)
	}
	registerOIDCRedirects(mux, d)
	for _, r := range d.Raw {
		r(mux, d)
	}
	return api
}

type principalKey struct{}

// PrincipalFrom returns the authenticated principal (nil for public operations).
func PrincipalFrom(ctx context.Context) *authn.Principal {
	p, _ := ctx.Value(principalKey{}).(*authn.Principal)
	return p
}

// WithPrincipal attaches a principal (raw handlers and tests).
func WithPrincipal(ctx context.Context, p *authn.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// limitedOK are the operations a session with a pending second factor may use.
var limitedOK = map[string]bool{"get-me": true, "verify-mfa": true, "begin-totp": true, "confirm-totp": true, "logout": true}

// tokenForbidden operations need an interactive sign-in (credentials, tokens, roles, account security).
var tokenForbidden = map[string]bool{"create-api-token": true, "begin-totp": true, "confirm-totp": true, "disable-totp": true,
	"regenerate-recovery-codes": true, "set-password": true, "reauth": true, "logout": true, "revoke-other-sessions": true, "update-user": true,
	"delete-user": true, "create-user": true, "invite-user": true, "issue-reset-link": true}

// sourceTokenOK are the only operations a source token may call.
var sourceTokenOK = map[string]bool{"ingest-items": true, "begin-snapshot": true, "put-snapshot-batch": true, "commit-snapshot": true,
	"abort-snapshot": true, "get-snapshot": true, "source-heartbeat": true, "ingest-observations": true, "push-terraform-state": true,
	"get-system-info": true, "get-me": true}

// BearerToken returns the bearer token of r.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// Authenticate resolves the caller of a raw (non-Huma) request: bearer token or session (with Origin + CSRF on
// unsafe methods). It is used by SSE and file handlers.
func (d *Deps) Authenticate(r *http.Request) (*authn.Principal, error) {
	if bearer, ok := BearerToken(r); ok {
		return d.Authn.AuthenticateToken(r.Context(), bearer, httpx.ClientIP(r.Context()).String())
	}
	if !httpx.IsSafeMethod(r.Method) && !httpx.SameOrigin(r, d.Origin) {
		return nil, apperr.New(apperr.CodeForbidden, "cross-origin request refused")
	}
	p, err := d.Authn.Authenticate(r.Context(), authn.TokenFromRequest(r))
	if err != nil {
		return nil, err
	}
	if p.Limited {
		return nil, apperr.New(apperr.CodeMFARequired, "complete the second factor first")
	}
	if !httpx.IsSafeMethod(r.Method) && !p.VerifyCSRF(r.Header.Get(CSRFHeader)) {
		return nil, apperr.New(apperr.CodeForbidden, "missing or invalid CSRF token")
	}
	return p, nil
}

// Revalidate re-checks the credentials of a long-lived request (SSE) without extending the session's idle timer, so
// an open stream never keeps an idle session alive.
func (d *Deps) Revalidate(r *http.Request) (*authn.Principal, error) {
	if bearer, ok := BearerToken(r); ok {
		return d.Authn.AuthenticateToken(r.Context(), bearer, httpx.ClientIP(r.Context()).String())
	}
	p, err := d.Authn.Validate(r.Context(), authn.TokenFromRequest(r))
	if err != nil {
		return nil, err
	}
	if p.Limited {
		return nil, apperr.New(apperr.CodeMFARequired, "complete the second factor first")
	}
	return p, nil
}

// authenticate resolves the principal for operations that declare security and enforces Origin + CSRF on unsafe
// methods for cookie sessions.
func (d *Deps) authenticate(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		r, _ := humago.Unwrap(ctx)
		meta := audit.Meta{SourceIP: httpx.ClientIP(r.Context()).String(), RequestID: httpx.GetRequestID(r.Context())}
		op := ctx.Operation()
		if op == nil || len(op.Security) == 0 {
			next(huma.WithContext(ctx, audit.WithMeta(ctx.Context(), meta)))
			return
		}
		deny := func(status int, code apperr.Code, reason, msg string) {
			d.Metrics.AuthzDenied(reason)
			_ = writeProblem(api, ctx, problem(status, code, msg, nil))
		}
		if bearer, ok := BearerToken(r); ok {
			p, err := d.Authn.AuthenticateToken(r.Context(), bearer, meta.SourceIP)
			if err != nil {
				deny(http.StatusUnauthorized, apperr.CodeUnauthorized, "invalid_token", "invalid or expired token")
				return
			}
			if p.SourceID != "" && !sourceTokenOK[op.OperationID] {
				deny(http.StatusForbidden, apperr.CodeForbidden, "source_token_scope", "source tokens may only use the ingest endpoints of their source")
				return
			}
			if p.SourceID == "" && tokenForbidden[op.OperationID] {
				deny(http.StatusForbidden, apperr.CodeForbidden, "interactive_only", "this operation needs an interactive sign-in")
				return
			}
			meta.AuthMethod = p.Method
			next(huma.WithContext(ctx, WithPrincipal(audit.WithMeta(ctx.Context(), meta), p)))
			return
		}
		if !httpx.IsSafeMethod(r.Method) && !httpx.SameOrigin(r, d.Origin) {
			deny(http.StatusForbidden, apperr.CodeForbidden, "cross_origin", "cross-origin request refused")
			return
		}
		p, err := d.Authn.Authenticate(r.Context(), authn.TokenFromRequest(r))
		if err != nil {
			deny(http.StatusUnauthorized, apperr.CodeUnauthorized, "no_session", "authentication required")
			return
		}
		if !httpx.IsSafeMethod(r.Method) && !p.VerifyCSRF(r.Header.Get(CSRFHeader)) {
			deny(http.StatusForbidden, apperr.CodeForbidden, "csrf", "missing or invalid CSRF token")
			return
		}
		if p.Limited && !limitedOK[op.OperationID] {
			code := apperr.CodeMFARequired
			msg := "complete the second factor first"
			if p.NeedsEnroll {
				msg = "enroll a second factor first"
			}
			deny(http.StatusForbidden, code, "mfa_pending", msg)
			return
		}
		meta.AuthMethod = p.Method
		next(huma.WithContext(ctx, WithPrincipal(audit.WithMeta(ctx.Context(), meta), p)))
	}
}

// IfMatch is embedded by inputs of conditional mutations.
type IfMatch struct {
	IfMatch string `header:"If-Match" required:"false" doc:"ETag (resource version) the change is based on; mismatches fail with 412."`
}

// Version parses the If-Match header ("" → 0 = unconditional).
func (m IfMatch) Version() (int64, error) {
	s := strings.TrimSpace(m.IfMatch)
	if s == "" || s == "*" {
		return 0, nil
	}
	s = strings.Trim(strings.TrimPrefix(s, "W/"), `"`)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, problem(http.StatusBadRequest, apperr.CodeInvalid, "If-Match must be an ETag returned by this API", nil)
	}
	return v, nil
}

// ETag renders a resource version.
func ETag(version int64) string { return fmt.Sprintf(`"%d"`, version) }

// Pagination is embedded by list inputs.
type Pagination struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"500" default:"50" doc:"Page size."`
	Cursor string `query:"cursor" maxLength:"256" doc:"Opaque cursor from next_cursor."`
}

// Fail converts an error into an RFC 9457 problem, logging unexpected errors without leaking them.
func (d *Deps) Fail(op string, err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return problem(statusFor(ae.Code), ae.Code, ae.Message, ae.Fields)
	}
	var pr *Problem
	if errors.As(err, &pr) {
		return pr
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return se
	}
	switch {
	case errors.Is(err, context.Canceled):
		return huma.NewError(499, "client closed request")
	case errors.Is(err, context.DeadlineExceeded):
		return problem(http.StatusGatewayTimeout, "timeout", "the operation timed out", nil)
	case errors.Is(err, store.ErrBusy):
		return problem(http.StatusServiceUnavailable, apperr.CodeUnavailable, "the database is busy; retry shortly", nil)
	}
	d.log().Error("api error", "event.action", op, "error.message", err.Error())
	return problem(http.StatusInternalServerError, "internal", "internal error", nil)
}

func statusFor(c apperr.Code) int {
	switch c {
	case apperr.CodeInvalid:
		return http.StatusUnprocessableEntity
	case apperr.CodeNotFound:
		return http.StatusNotFound
	case apperr.CodeAlreadyExists, apperr.CodeConflict:
		return http.StatusConflict
	case apperr.CodeForbidden, apperr.CodeReauth, apperr.CodeMFARequired:
		return http.StatusForbidden
	case apperr.CodePrecondition:
		return http.StatusPreconditionFailed
	case apperr.CodeUnavailable:
		return http.StatusServiceUnavailable
	case apperr.CodeRateLimited:
		return http.StatusTooManyRequests
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}

// requireUser returns the principal of a user credential.
func requireUser(ctx context.Context) (*authn.Principal, error) {
	p := PrincipalFrom(ctx)
	if err := authz.RequireUser(p); err != nil {
		return nil, err
	}
	return p, nil
}

// RequireUser is requireUser for extension packages.
func RequireUser(ctx context.Context) (*authn.Principal, error) { return requireUser(ctx) }

// Readable returns the project IDs the caller can read (nil = all).
func (d *Deps) Readable(ctx context.Context) ([]string, error) {
	p := PrincipalFrom(ctx)
	known, err := d.Catalog.ProjectIDs(ctx)
	if err != nil {
		return nil, err
	}
	all, ids := authz.Readable(p, known)
	if all {
		return nil, nil
	}
	return ids, nil
}

// ResolveProject finds a project by ID or name and checks the caller's level (unreadable → not found).
func (d *Deps) ResolveProject(ctx context.Context, idOrName string, need authz.Level) (*catalog.Project, error) {
	pr, err := d.Catalog.Project(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	if err := authz.Require(PrincipalFrom(ctx), pr.ID, need); err != nil {
		return nil, err
	}
	return pr, nil
}

// RequireRecent enforces the re-authentication window for sensitive operations.
func (d *Deps) RequireRecent(ctx context.Context) error {
	p := PrincipalFrom(ctx)
	if p == nil {
		return authn.ErrUnauthorized
	}
	if err := d.Authn.RequireRecentAuth(p); err != nil {
		pr := problem(http.StatusForbidden, apperr.CodeReauth, "this action needs a recent sign-in", nil)
		if p.Method == authn.MethodOIDC && p.Provider != "" {
			pr.Action = "/auth/oidc/" + p.Provider + "/start?purpose=reauth"
		} else {
			pr.Action = "reauth"
		}
		return pr
	}
	return nil
}

// Actor returns the audit actor of the request.
func Actor(ctx context.Context) audit.Actor { return PrincipalFrom(ctx).Actor() }

func oneOf(v string, allowed ...string) bool { return slices.Contains(allowed, v) }
