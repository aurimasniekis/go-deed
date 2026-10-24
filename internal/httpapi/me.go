package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/oidc"
)

type totpBeginOutput struct {
	Body *authn.TOTPEnrollment
}

type recoveryCodesBody struct {
	RecoveryCodes []string `json:"recovery_codes" doc:"Shown once. Store them offline."`
}

type totpConfirmOutput struct {
	SetCookie *http.Cookie `header:"Set-Cookie"`
	Body      recoveryCodesBody
}

type recoveryOutput struct {
	Body recoveryCodesBody
}

type passwordInput struct {
	Body struct {
		Current string `json:"current,omitempty" maxLength:"4096"`
		New     string `json:"new" maxLength:"4096"`
	}
}

type sessionsOutput struct {
	Body []authn.SessionInfo
}

type idPath struct {
	ID string `path:"id" maxLength:"64"`
}

type tokensOutput struct {
	Body []*authn.Token
}

type createTokenInput struct {
	Body struct {
		Name             string   `json:"name" maxLength:"100"`
		InstallationRole string   `json:"installation_role,omitempty" enum:"admin,auditor," doc:"Optional installation role (≤ yours)."`
		ProjectRole      string   `json:"project_role,omitempty" enum:"viewer,editor,admin," doc:"Caps every project role of the token."`
		Projects         []string `json:"projects,omitempty" maxItems:"100" doc:"Restrict to these project IDs or names (empty = all you can access)."`
		ExpiresInDays    int      `json:"expires_in_days,omitempty" minimum:"1" maximum:"366" default:"90"`
	}
}

// CreatedToken returns a token once.
type CreatedToken struct {
	*authn.Token
	Secret string `json:"token" doc:"The bearer token. Shown once."`
}

type createdTokenOutput struct {
	Body CreatedToken
}

type identitiesOutput struct {
	Body []oidc.Identity
}

func registerMe(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "get-me", Method: http.MethodGet, Path: "/me", Tags: []string{"account"},
		Summary: "The signed-in principal and its CSRF token", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*meOutput, error) {
			m, err := d.me(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, d.Fail("get-me", err)
			}
			return &meOutput{Body: m}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "set-password", Method: http.MethodPut, Path: "/me/password", Tags: []string{"account"},
		Summary: "Set or change your password", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *passwordInput) (*noContent, error) {
			return nil, d.Fail("set-password", d.Authn.SetPassword(ctx, PrincipalFrom(ctx), in.Body.Current, in.Body.New))
		})
	huma.Register(api, huma.Operation{OperationID: "begin-totp", Method: http.MethodPost, Path: "/me/mfa/totp", Tags: []string{"account"},
		Summary: "Start TOTP enrollment", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*totpBeginOutput, error) {
			e, err := d.Authn.BeginTOTP(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, d.Fail("begin-totp", err)
			}
			return &totpBeginOutput{Body: e}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "confirm-totp", Method: http.MethodPost, Path: "/me/mfa/totp/confirm", Tags: []string{"account"},
		Summary: "Confirm TOTP enrollment; returns recovery codes once", Security: Secured},
		func(ctx context.Context, in *codeInput) (*totpConfirmOutput, error) {
			codes, tok, err := d.Authn.ConfirmTOTP(ctx, PrincipalFrom(ctx), in.Body.Code, meta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.Fail("confirm-totp", err)
			}
			return &totpConfirmOutput{SetCookie: d.sessionCookie(tok), Body: recoveryCodesBody{RecoveryCodes: codes}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "disable-totp", Method: http.MethodDelete, Path: "/me/mfa/totp", Tags: []string{"account"},
		Summary: "Remove your second factor", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*noContent, error) {
			return nil, d.Fail("disable-totp", d.Authn.DisableTOTP(ctx, PrincipalFrom(ctx)))
		})
	huma.Register(api, huma.Operation{OperationID: "regenerate-recovery-codes", Method: http.MethodPost, Path: "/me/mfa/recovery-codes",
		Tags: []string{"account"}, Summary: "Replace your recovery codes", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*recoveryOutput, error) {
			codes, err := d.Authn.RegenerateRecoveryCodes(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, d.Fail("regenerate-recovery-codes", err)
			}
			return &recoveryOutput{Body: recoveryCodesBody{RecoveryCodes: codes}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-sessions", Method: http.MethodGet, Path: "/me/sessions", Tags: []string{"account"},
		Summary: "Your active sessions", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*sessionsOutput, error) {
			p, err := requireUser(ctx)
			if err != nil || p.TokenID != "" {
				return nil, d.Fail("list-sessions", apperr.New(apperr.CodeForbidden, "sessions are listed from an interactive sign-in"))
			}
			s, err := d.Authn.ListSessions(ctx, p)
			return &sessionsOutput{Body: s}, d.Fail("list-sessions", err)
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-session", Method: http.MethodDelete, Path: "/me/sessions/{id}", Tags: []string{"account"},
		Summary: "Sign out one of your sessions", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *idPath) (*noContent, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("revoke-session", err)
			}
			return nil, d.Fail("revoke-session", d.Authn.RevokeSession(ctx, p, in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-other-sessions", Method: http.MethodPost, Path: "/me/sessions/revoke-others",
		Tags: []string{"account"}, Summary: "Sign out everywhere else", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*noContent, error) {
			return nil, d.Fail("revoke-other-sessions", d.Authn.RevokeOtherSessions(ctx, PrincipalFrom(ctx)))
		})
	huma.Register(api, huma.Operation{OperationID: "list-api-tokens", Method: http.MethodGet, Path: "/me/tokens", Tags: []string{"account"},
		Summary: "Your API tokens", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*tokensOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-api-tokens", err)
			}
			t, err := d.Authn.ListTokens(ctx, p.UserID, "")
			return &tokensOutput{Body: t}, d.Fail("list-api-tokens", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-api-token", Method: http.MethodPost, Path: "/me/tokens", Tags: []string{"account"},
		Summary: "Create an API token (shown once)", Description: "Needs a recent sign-in. Tokens act for you with at most your roles.",
		Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createTokenInput) (*createdTokenOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("create-api-token", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			var pids []string
			for _, ref := range in.Body.Projects {
				pr, err := d.Catalog.Project(ctx, ref)
				if err != nil {
					return nil, d.Fail("create-api-token", err)
				}
				pids = append(pids, pr.ID)
			}
			t, raw, err := d.Authn.CreateAPIToken(ctx, p, authn.CreateAPITokenInput{Name: in.Body.Name, InstallationRole: in.Body.InstallationRole,
				ProjectRole: in.Body.ProjectRole, Projects: pids, ExpiresIn: time.Duration(in.Body.ExpiresInDays) * 24 * time.Hour})
			if err != nil {
				return nil, d.Fail("create-api-token", err)
			}
			return &createdTokenOutput{Body: CreatedToken{Token: t, Secret: raw}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-api-token", Method: http.MethodDelete, Path: "/me/tokens/{id}", Tags: []string{"account"},
		Summary: "Revoke one of your API tokens", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *idPath) (*noContent, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("revoke-api-token", err)
			}
			t, err := d.Authn.GetToken(ctx, in.ID)
			if err != nil || t.UserID != p.UserID {
				return nil, d.Fail("revoke-api-token", apperr.New(apperr.CodeNotFound, "token not found"))
			}
			return nil, d.Fail("revoke-api-token", d.Authn.RevokeToken(ctx, p.Actor(), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "list-identities", Method: http.MethodGet, Path: "/me/identities", Tags: []string{"account"},
		Summary: "Your linked SSO identities", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*identitiesOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-identities", err)
			}
			if d.OIDC == nil {
				return &identitiesOutput{Body: []oidc.Identity{}}, nil
			}
			ids, err := d.OIDC.Identities(ctx, p.UserID)
			return &identitiesOutput{Body: ids}, d.Fail("list-identities", err)
		})
	huma.Register(api, huma.Operation{OperationID: "unlink-identity", Method: http.MethodDelete, Path: "/me/identities/{id}", Tags: []string{"account"},
		Summary: "Unlink an SSO identity", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *idPath) (*noContent, error) {
			p, err := requireUser(ctx)
			if err != nil || d.OIDC == nil {
				return nil, d.Fail("unlink-identity", apperr.New(apperr.CodeNotFound, "identity not found"))
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			return nil, d.Fail("unlink-identity", d.OIDC.Unlink(ctx, p.Actor(), p.UserID, in.ID))
		})
}
