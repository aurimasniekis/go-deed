package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
)

type serviceAccountRef struct {
	ID string `path:"id" maxLength:"64" doc:"Service account ID or name."`
}

type createServiceAccountInput struct {
	Body struct {
		Name             string `json:"name" maxLength:"63" pattern:"^[a-z0-9][a-z0-9_-]*$" doc:"Unique name (lowercase letters, digits, '-', '_')."`
		DisplayName      string `json:"display_name,omitempty" maxLength:"128" doc:"Defaults to the name."`
		Description      string `json:"description,omitempty" maxLength:"500"`
		InstallationRole string `json:"installation_role,omitempty" enum:"admin,auditor," doc:"Installation role; empty = project memberships only."`
	}
}

type updateServiceAccountInput struct {
	ID   string `path:"id" maxLength:"64" doc:"Service account ID or name."`
	Body struct {
		DisplayName      *string `json:"display_name,omitempty" maxLength:"128"`
		Description      *string `json:"description,omitempty" maxLength:"500"`
		Status           *string `json:"status,omitempty" enum:"active,disabled" doc:"Disabling revokes every token."`
		InstallationRole *string `json:"installation_role,omitempty" enum:"admin,auditor," doc:"Empty string removes the installation role."`
	}
}

type createServiceTokenInput struct {
	ID   string `path:"id" maxLength:"64" doc:"Service account ID or name."`
	Body struct {
		Name             string   `json:"name" maxLength:"100"`
		InstallationRole *string  `json:"installation_role,omitempty" enum:"admin,auditor," doc:"Omit to inherit the service account's installation role; empty for none."`
		ProjectRole      string   `json:"project_role,omitempty" enum:"viewer,editor,admin," doc:"Caps every project role of the token."`
		Projects         []string `json:"projects,omitempty" maxItems:"100" doc:"Restrict to these project IDs or names (empty = all the service account can access)."`
		ExpiresInDays    int      `json:"expires_in_days,omitempty" minimum:"1" maximum:"366" default:"90"`
	}
}

type serviceTokenPath struct {
	ID    string `path:"id" maxLength:"64" doc:"Service account ID or name."`
	Token string `path:"token" maxLength:"64"`
}

func registerServiceAccounts(api huma.API, d *Deps) {
	// Managing service accounts and their tokens needs an installation owner or admin; a token with that role works too,
	// so automation can rotate its own credentials. Reading needs any installation role.
	admin := func(ctx context.Context) (*authn.Principal, error) {
		p, err := requireUser(ctx)
		if err != nil {
			return nil, err
		}
		if err := authz.RequireInstallation(p, authn.RoleOwner, authn.RoleAdmin); err != nil {
			return nil, err
		}
		return p, d.RequireRecent(ctx)
	}
	reader := func(ctx context.Context) error {
		if !authz.CanReadAllAudit(PrincipalFrom(ctx)) {
			return apperr.New(apperr.CodeForbidden, "installation role required")
		}
		return nil
	}
	tags := []string{"service accounts"}
	huma.Register(api, huma.Operation{OperationID: "list-service-accounts", Method: http.MethodGet, Path: "/service-accounts", Tags: tags,
		Summary: "List service accounts (installation owner, admin or auditor)", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*usersOutput, error) {
			if err := reader(ctx); err != nil {
				return nil, d.Fail("list-service-accounts", err)
			}
			u, err := d.Authn.ListServiceAccounts(ctx)
			return &usersOutput{Body: u}, d.Fail("list-service-accounts", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-service-account", Method: http.MethodPost, Path: "/service-accounts", Tags: tags,
		Summary: "Create a service account", Description: "A non-human user for automation: no password, SSO or sessions, only API tokens. " +
			"Grant project roles with PUT /projects/{project}/members/{user}.", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createServiceAccountInput) (*userOutput, error) {
			p, err := admin(ctx)
			if err != nil {
				return nil, d.Fail("create-service-account", err)
			}
			u, err := d.Authn.CreateServiceAccount(ctx, p.Actor(), authn.ServiceAccountInput{Name: in.Body.Name, DisplayName: in.Body.DisplayName,
				Description: in.Body.Description, InstallationRole: in.Body.InstallationRole})
			return &userOutput{Body: u}, d.Fail("create-service-account", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-service-account", Method: http.MethodGet, Path: "/service-accounts/{id}", Tags: tags,
		Summary: "Get a service account by ID or name", Security: Secured},
		func(ctx context.Context, in *serviceAccountRef) (*userOutput, error) {
			if err := reader(ctx); err != nil {
				return nil, d.Fail("get-service-account", err)
			}
			u, err := d.Authn.ServiceAccount(ctx, in.ID)
			return &userOutput{Body: u}, d.Fail("get-service-account", err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-service-account", Method: http.MethodPatch, Path: "/service-accounts/{id}", Tags: tags,
		Summary: "Change a service account", Security: Secured},
		func(ctx context.Context, in *updateServiceAccountInput) (*userOutput, error) {
			p, err := admin(ctx)
			if err != nil {
				return nil, d.Fail("update-service-account", err)
			}
			u, err := d.Authn.UpdateServiceAccount(ctx, p.Actor(), in.ID, authn.UpdateServiceAccountInput{DisplayName: in.Body.DisplayName,
				Description: in.Body.Description, Status: in.Body.Status, InstallationRole: in.Body.InstallationRole})
			return &userOutput{Body: u}, d.Fail("update-service-account", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-service-account", Method: http.MethodDelete, Path: "/service-accounts/{id}", Tags: tags,
		Summary: "Delete a service account with its tokens and memberships", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *serviceAccountRef) (*noContent, error) {
			p, err := admin(ctx)
			if err != nil {
				return nil, d.Fail("delete-service-account", err)
			}
			if sa, err := d.Authn.ServiceAccount(ctx, in.ID); err == nil && sa.ID == p.UserID {
				return nil, d.Fail("delete-service-account", apperr.New(apperr.CodeConflict, "a service account cannot delete itself"))
			}
			return nil, d.Fail("delete-service-account", d.Authn.DeleteServiceAccount(ctx, p.Actor(), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "list-service-account-tokens", Method: http.MethodGet, Path: "/service-accounts/{id}/tokens",
		Tags: tags, Summary: "A service account's API tokens", Security: Secured},
		func(ctx context.Context, in *serviceAccountRef) (*tokensOutput, error) {
			if err := reader(ctx); err != nil {
				return nil, d.Fail("list-service-account-tokens", err)
			}
			sa, err := d.Authn.ServiceAccount(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("list-service-account-tokens", err)
			}
			t, err := d.Authn.ListTokens(ctx, sa.ID, "")
			return &tokensOutput{Body: t}, d.Fail("list-service-account-tokens", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-service-account-token", Method: http.MethodPost, Path: "/service-accounts/{id}/tokens",
		Tags: tags, Summary: "Create a service account API token (shown once)",
		Description: "The token acts as the service account, with at most its installation role and project memberships.",
		Security:    Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createServiceTokenInput) (*createdTokenOutput, error) {
			p, err := admin(ctx)
			if err != nil {
				return nil, d.Fail("create-service-account-token", err)
			}
			var pids []string
			for _, ref := range in.Body.Projects {
				pr, err := d.Catalog.Project(ctx, ref)
				if err != nil {
					return nil, d.Fail("create-service-account-token", err)
				}
				pids = append(pids, pr.ID)
			}
			t, raw, err := d.Authn.CreateServiceAccountToken(ctx, p, in.ID, authn.ServiceTokenInput{Name: in.Body.Name,
				InstallationRole: in.Body.InstallationRole, ProjectRole: in.Body.ProjectRole, Projects: pids,
				ExpiresIn: time.Duration(in.Body.ExpiresInDays) * 24 * time.Hour})
			if err != nil {
				return nil, d.Fail("create-service-account-token", err)
			}
			return &createdTokenOutput{Body: CreatedToken{Token: t, Secret: raw}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-service-account-token", Method: http.MethodDelete,
		Path: "/service-accounts/{id}/tokens/{token}", Tags: tags, Summary: "Revoke a service account API token", Security: Secured,
		DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *serviceTokenPath) (*noContent, error) {
			p, err := admin(ctx)
			if err != nil {
				return nil, d.Fail("revoke-service-account-token", err)
			}
			sa, err := d.Authn.ServiceAccount(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("revoke-service-account-token", err)
			}
			t, err := d.Authn.GetToken(ctx, in.Token)
			if err != nil || t.UserID != sa.ID {
				return nil, d.Fail("revoke-service-account-token", apperr.New(apperr.CodeNotFound, "token not found"))
			}
			return nil, d.Fail("revoke-service-account-token", d.Authn.RevokeToken(ctx, p.Actor(), in.Token))
		})
}
