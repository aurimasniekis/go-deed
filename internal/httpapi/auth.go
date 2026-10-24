package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/buildinfo"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/oidc"
)

// SystemInfo describes the server for clients (the Terraform provider checks api_version and version).
type SystemInfo struct {
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	Commit        string              `json:"commit,omitempty"`
	APIVersion    string              `json:"api_version" doc:"Major API version served under /api/v1."`
	Features      []string            `json:"features"`
	LocalAuth     bool                `json:"local_auth"`
	Providers     []oidc.ProviderInfo `json:"sso_providers"`
	SetupRequired bool                `json:"setup_required" doc:"True until the first owner exists."`
}

type systemInfoOutput struct {
	Body SystemInfo
}

// Features advertised by this server (additive).
var Features = []string{"entities", "relations", "custom_kinds", "facets", "provenance", "snapshots", "terraform_state", "pulse",
	"telemetry", "findings", "alerts", "backups", "sse", "graph", "saved_views", "icons"}

func registerSystem(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "get-system-info", Method: http.MethodGet, Path: "/system/info", Tags: []string{"system"},
		Summary: "Server version, API version and sign-in options (public)"},
		func(ctx context.Context, _ *struct{}) (*systemInfoOutput, error) {
			bi := buildinfo.Get()
			info := SystemInfo{Name: "deed", Version: bi.Version, Commit: bi.Commit, APIVersion: "v1", Features: Features, LocalAuth: d.LocalAuth,
				Providers: []oidc.ProviderInfo{}}
			if d.OIDC != nil {
				info.Providers = d.OIDC.Providers()
			}
			if d.Authn != nil {
				has, err := d.Authn.HasOwner(ctx)
				if err != nil {
					return nil, d.Fail("get-system-info", err)
				}
				info.SetupRequired = !has
			}
			return &systemInfoOutput{Body: info}, nil
		})
}

// Me is the signed-in principal.
type Me struct {
	UserID           string            `json:"user_id,omitempty"`
	Email            string            `json:"email,omitempty"`
	DisplayName      string            `json:"display_name"`
	InstallationRole string            `json:"installation_role,omitempty"`
	ProjectRoles     map[string]string `json:"project_roles" doc:"Project ID → role (explicit memberships)."`
	Method           string            `json:"method"`
	Provider         string            `json:"provider,omitempty"`
	SessionID        string            `json:"session_id,omitempty"`
	AuthAt           time.Time         `json:"auth_at"`
	ExpiresAt        time.Time         `json:"expires_at"`
	MFAPending       bool              `json:"mfa_pending"`
	MustEnroll       bool              `json:"must_enroll"`
	MFAEnrolled      bool              `json:"mfa_enrolled"`
	HasPassword      bool              `json:"has_password"`
	CSRFToken        string            `json:"csrf_token,omitempty" doc:"Send in X-Deed-CSRF on state-changing requests (sessions only)."`
	TokenID          string            `json:"token_id,omitempty"`
	SourceID         string            `json:"source_id,omitempty"`
}

func (d *Deps) me(ctx context.Context, p *authn.Principal) (*Me, error) {
	m := &Me{UserID: p.UserID, Email: p.Email, DisplayName: p.DisplayName, InstallationRole: p.InstallationRole, ProjectRoles: p.ProjectRoles,
		Method: p.Method, Provider: p.Provider, SessionID: p.SessionID, AuthAt: p.AuthAt, ExpiresAt: p.ExpiresAt, MFAPending: p.Limited && !p.NeedsEnroll,
		MustEnroll: p.NeedsEnroll, CSRFToken: p.CSRFToken(), TokenID: p.TokenID, SourceID: p.SourceID}
	if m.ProjectRoles == nil {
		m.ProjectRoles = map[string]string{}
	}
	if p.UserID != "" {
		u, err := d.Authn.GetUser(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		m.MFAEnrolled, m.HasPassword = u.MFAEnrolled, u.HasPassword
	}
	return m, nil
}

type meOutput struct {
	Body *Me
}

type sessionOutput struct {
	SetCookie *http.Cookie `header:"Set-Cookie"`
	Body      *Me
}

type loginInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Email    string `json:"email" maxLength:"254"`
		Password string `json:"password" maxLength:"4096"`
	}
}

type codeInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Code string `json:"code" maxLength:"32" doc:"6-digit TOTP code or a recovery code."`
	}
}

type reauthInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Password string `json:"password" maxLength:"4096"`
		Code     string `json:"code,omitempty" maxLength:"32"`
	}
}

type setupInspectInput struct {
	Body struct {
		Token string `json:"token" maxLength:"128"`
	}
}

type setupInspectOutput struct {
	Body *authn.LinkInfo
}

type setupCompleteInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Token       string `json:"token" maxLength:"128"`
		Email       string `json:"email,omitempty" maxLength:"254" doc:"Bootstrap only."`
		DisplayName string `json:"display_name,omitempty" maxLength:"128"`
		Password    string `json:"password,omitempty" maxLength:"4096"`
	}
}

type noContent struct{}

func meta(ctx context.Context, ua string) authn.SessionMeta {
	if len(ua) > 256 {
		ua = ua[:256]
	}
	return authn.SessionMeta{IP: httpx.ClientIP(ctx).String(), UserAgent: ua}
}

func (d *Deps) sessionCookie(token string) *http.Cookie {
	return authn.SessionCookie(d.Secure, token, 0)
}

func (d *Deps) clearCookie() *http.Cookie {
	return authn.SessionCookie(d.Secure, "", -1)
}

func (d *Deps) withSession(ctx context.Context, token string) (*sessionOutput, error) {
	p, err := d.Authn.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	m, err := d.me(ctx, p)
	if err != nil {
		return nil, err
	}
	return &sessionOutput{SetCookie: d.sessionCookie(token), Body: m}, nil
}

func registerAuth(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/auth/login", Tags: []string{"auth"},
		Summary: "Sign in with email and password", Description: "Starts a session. When a second factor is enrolled or required, the session " +
			"is limited until /auth/mfa (or TOTP enrollment) completes."},
		func(ctx context.Context, in *loginInput) (*sessionOutput, error) {
			if d.LoginRate != nil && !d.LoginRate.Allow(httpx.SourceKey(httpx.ClientIP(ctx))) {
				return nil, problem(http.StatusTooManyRequests, apperr.CodeRateLimited, "too many attempts; try again later", nil)
			}
			res, err := d.Authn.Login(ctx, in.Body.Email, in.Body.Password, meta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.Fail("login", err)
			}
			out, err := d.withSession(ctx, res.Token)
			return out, d.Fail("login", err)
		})
	huma.Register(api, huma.Operation{OperationID: "verify-mfa", Method: http.MethodPost, Path: "/auth/mfa", Tags: []string{"auth"},
		Summary: "Complete sign-in with a TOTP or recovery code", Security: Secured},
		func(ctx context.Context, in *codeInput) (*sessionOutput, error) {
			tok, err := d.Authn.VerifyMFA(ctx, PrincipalFrom(ctx), in.Body.Code, meta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.Fail("verify-mfa", err)
			}
			out, err := d.withSession(ctx, tok)
			return out, d.Fail("verify-mfa", err)
		})
	huma.Register(api, huma.Operation{OperationID: "reauth", Method: http.MethodPost, Path: "/auth/reauth", Tags: []string{"auth"},
		Summary: "Re-authenticate for sensitive operations (local accounts)", Security: Secured},
		func(ctx context.Context, in *reauthInput) (*sessionOutput, error) {
			tok, err := d.Authn.Reauth(ctx, PrincipalFrom(ctx), in.Body.Password, in.Body.Code, meta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.Fail("reauth", err)
			}
			out, err := d.withSession(ctx, tok)
			return out, d.Fail("reauth", err)
		})
	huma.Register(api, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", Tags: []string{"auth"},
		Summary: "End the current session", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*struct {
			SetCookie *http.Cookie `header:"Set-Cookie"`
		}, error) {
			if err := d.Authn.Logout(ctx, PrincipalFrom(ctx)); err != nil {
				return nil, d.Fail("logout", err)
			}
			return &struct {
				SetCookie *http.Cookie `header:"Set-Cookie"`
			}{SetCookie: d.clearCookie()}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "inspect-setup-link", Method: http.MethodPost, Path: "/auth/setup/inspect", Tags: []string{"auth"},
		Summary: "Validate a setup, invite, reset or recovery link without consuming it"},
		func(ctx context.Context, in *setupInspectInput) (*setupInspectOutput, error) {
			info, err := d.Authn.InspectLink(ctx, in.Body.Token)
			if err != nil {
				return nil, d.Fail("inspect-setup-link", err)
			}
			return &setupInspectOutput{Body: info}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "complete-setup-link", Method: http.MethodPost, Path: "/auth/setup/complete", Tags: []string{"auth"},
		Summary: "Consume a single-use link", Description: "bootstrap: creates the first owner (email, display_name, password); invite: creates the " +
			"invited user (display_name, password); reset: sets a new password (then sign in normally); recovery: break-glass sign-in."},
		func(ctx context.Context, in *setupCompleteInput) (*sessionOutput, error) {
			if d.LoginRate != nil && !d.LoginRate.Allow(httpx.SourceKey(httpx.ClientIP(ctx))) {
				return nil, problem(http.StatusTooManyRequests, apperr.CodeRateLimited, "too many attempts; try again later", nil)
			}
			info, err := d.Authn.InspectLink(ctx, in.Body.Token)
			if err != nil {
				return nil, d.Fail("complete-setup-link", err)
			}
			m := meta(ctx, in.UserAgent)
			var res *authn.LoginResult
			switch info.Purpose {
			case authn.LinkBootstrap:
				res, err = d.Authn.ConsumeBootstrap(ctx, in.Body.Token, in.Body.Email, in.Body.DisplayName, in.Body.Password, m)
			case authn.LinkInvite:
				res, err = d.Authn.ConsumeInvite(ctx, in.Body.Token, in.Body.DisplayName, in.Body.Password, m)
			case authn.LinkRecovery:
				res, err = d.Authn.ConsumeRecovery(ctx, in.Body.Token, m)
			case authn.LinkReset:
				if err = d.Authn.ConsumeReset(ctx, in.Body.Token, in.Body.Password, m); err != nil {
					return nil, d.Fail("complete-setup-link", err)
				}
				return &sessionOutput{Body: &Me{ProjectRoles: map[string]string{}, Method: "reset"}}, nil
			}
			if err != nil {
				return nil, d.Fail("complete-setup-link", err)
			}
			out, err := d.withSession(ctx, res.Token)
			return out, d.Fail("complete-setup-link", err)
		})
}

// registerOIDCRedirects serves the browser redirects of the OIDC flow (not JSON operations).
func registerOIDCRedirects(mux *http.ServeMux, d *Deps) {
	if d.OIDC == nil {
		return
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		code := string(apperr.CodeOf(err))
		if code == "" {
			code = "error"
			d.log().Warn("oidc flow failed", "error.message", err.Error())
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
	}
	mux.HandleFunc("GET /auth/oidc/{slug}/start", func(w http.ResponseWriter, r *http.Request) {
		purpose := r.URL.Query().Get("purpose")
		if purpose == "" {
			purpose = oidc.PurposeLogin
		}
		var cur *authn.Principal
		if purpose != oidc.PurposeLogin {
			p, err := d.Authn.Validate(r.Context(), authn.TokenFromRequest(r))
			if err != nil {
				fail(w, r, err)
				return
			}
			cur = p
		}
		res, err := d.OIDC.Start(r.Context(), r.PathValue("slug"), purpose, r.URL.Query().Get("return_to"), cur)
		if err != nil {
			fail(w, r, err)
			return
		}
		http.SetCookie(w, res.Cookie)
		http.Redirect(w, r, res.RedirectURL, http.StatusFound) //nolint:gosec // provider authorization endpoint from discovery
	})
	mux.HandleFunc("GET /auth/oidc/{slug}/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var cur *authn.Principal
		if p, err := d.Authn.Validate(r.Context(), authn.TokenFromRequest(r)); err == nil {
			cur = p
		}
		ua := r.UserAgent()
		res, err := d.OIDC.Callback(r.Context(), oidc.CallbackInput{Slug: r.PathValue("slug"), Code: q.Get("code"), State: q.Get("state"),
			Iss: q.Get("iss"), Error: q.Get("error"), Binding: oidc.CookieValue(r), Current: cur, Meta: meta(r.Context(), ua)})
		// The binding cookie is single use either way.
		name := oidc.DevStateCookie
		if d.Secure {
			name = oidc.StateCookie
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: d.Secure, SameSite: http.SameSiteLaxMode})
		if err != nil {
			fail(w, r, err)
			return
		}
		if res.SessionToken != "" {
			http.SetCookie(w, d.sessionCookie(res.SessionToken))
		}
		http.Redirect(w, r, httpx.SafeReturnTo(res.ReturnTo, "/"), http.StatusFound)
	})
}

var errNoUser = errors.New("no user")
