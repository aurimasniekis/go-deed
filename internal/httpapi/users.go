package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
)

type usersOutput struct {
	Body []*authn.User
}

type userOutput struct {
	Body *authn.User
}

type createUserInput struct {
	Body struct {
		Email            string `json:"email" maxLength:"254"`
		DisplayName      string `json:"display_name" maxLength:"128"`
		Password         string `json:"password,omitempty" maxLength:"4096" doc:"Optional; prefer an invite link."`
		InstallationRole string `json:"installation_role,omitempty" enum:"owner,admin,auditor,"`
	}
}

type updateUserInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		DisplayName      *string `json:"display_name,omitempty" maxLength:"128"`
		Status           *string `json:"status,omitempty" enum:"active,disabled"`
		InstallationRole *string `json:"installation_role,omitempty" enum:"owner,admin,auditor," doc:"Empty string removes the installation role."`
	}
}

type inviteInput struct {
	Body struct {
		Email            string `json:"email" maxLength:"254"`
		InstallationRole string `json:"installation_role,omitempty" enum:"owner,admin,auditor,"`
		ExpiresInHours   int    `json:"expires_in_hours,omitempty" minimum:"1" maximum:"168" default:"72"`
	}
}

// LinkBody returns a single-use link once.
type LinkBody struct {
	SetupLink string    `json:"setup_link" doc:"Single-use link. Shown once; share it over a trusted channel."`
	ExpiresAt time.Time `json:"expires_at"`
}

type linkOutput struct {
	Body LinkBody
}

type membershipsOutput struct {
	Body []authn.ProjectMembership
}

func registerUsers(api huma.API, d *Deps) {
	admin := func(ctx context.Context, owner bool) (*authn.Principal, error) {
		p, err := requireUser(ctx)
		if err != nil {
			return nil, err
		}
		roles := []string{authn.RoleOwner, authn.RoleAdmin}
		if owner {
			roles = []string{authn.RoleOwner}
		}
		if err := authz.RequireInstallation(p, roles...); err != nil {
			return nil, err
		}
		return p, nil
	}
	huma.Register(api, huma.Operation{OperationID: "list-users", Method: http.MethodGet, Path: "/users", Tags: []string{"users"},
		Summary: "List users (installation owner, admin or auditor)", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*usersOutput, error) {
			if !authz.CanReadAllAudit(PrincipalFrom(ctx)) {
				return nil, d.Fail("list-users", apperr.New(apperr.CodeForbidden, "installation role required"))
			}
			u, err := d.Authn.ListUsers(ctx)
			return &usersOutput{Body: u}, d.Fail("list-users", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-user", Method: http.MethodPost, Path: "/users", Tags: []string{"users"},
		Summary: "Create a local user", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createUserInput) (*userOutput, error) {
			p, err := admin(ctx, in.Body.InstallationRole == authn.RoleOwner)
			if err != nil {
				return nil, d.Fail("create-user", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			u, err := d.Authn.CreateUser(ctx, p.Actor(), authn.CreateUserInput{Email: in.Body.Email, DisplayName: in.Body.DisplayName,
				Password: in.Body.Password, InstallationRole: in.Body.InstallationRole})
			return &userOutput{Body: u}, d.Fail("create-user", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-user", Method: http.MethodGet, Path: "/users/{id}", Tags: []string{"users"},
		Summary: "Get a user", Security: Secured},
		func(ctx context.Context, in *idPath) (*userOutput, error) {
			p := PrincipalFrom(ctx)
			if !authz.CanReadAllAudit(p) && p.UserID != in.ID {
				return nil, d.Fail("get-user", apperr.New(apperr.CodeNotFound, "user not found"))
			}
			u, err := d.Authn.GetUser(ctx, in.ID)
			return &userOutput{Body: u}, d.Fail("get-user", err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-user", Method: http.MethodPatch, Path: "/users/{id}", Tags: []string{"users"},
		Summary: "Change a user's name, status or installation role", Security: Secured},
		func(ctx context.Context, in *updateUserInput) (*userOutput, error) {
			touchesOwner := in.Body.InstallationRole != nil && *in.Body.InstallationRole == authn.RoleOwner
			if cur, err := d.Authn.GetUser(ctx, in.ID); err == nil && cur.InstallationRole == authn.RoleOwner {
				touchesOwner = true
			}
			p, err := admin(ctx, touchesOwner)
			if err != nil {
				return nil, d.Fail("update-user", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			u, err := d.Authn.UpdateUser(ctx, p.Actor(), in.ID, authn.UpdateUserInput{DisplayName: in.Body.DisplayName, Status: in.Body.Status,
				InstallationRole: in.Body.InstallationRole})
			return &userOutput{Body: u}, d.Fail("update-user", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-user", Method: http.MethodDelete, Path: "/users/{id}", Tags: []string{"users"},
		Summary: "Delete a user", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *idPath) (*noContent, error) {
			owner := false
			if cur, err := d.Authn.GetUser(ctx, in.ID); err == nil && cur.InstallationRole == authn.RoleOwner {
				owner = true
			}
			p, err := admin(ctx, owner)
			if err != nil {
				return nil, d.Fail("delete-user", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			if p.UserID == in.ID {
				return nil, d.Fail("delete-user", apperr.New(apperr.CodeConflict, "you cannot delete yourself"))
			}
			return nil, d.Fail("delete-user", d.Authn.DeleteUser(ctx, p.Actor(), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "invite-user", Method: http.MethodPost, Path: "/users/invites", Tags: []string{"users"},
		Summary: "Issue a single-use invite link", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *inviteInput) (*linkOutput, error) {
			p, err := admin(ctx, in.Body.InstallationRole == authn.RoleOwner)
			if err != nil {
				return nil, d.Fail("invite-user", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			l, err := d.Authn.IssueInvite(ctx, p.Actor(), in.Body.Email, in.Body.InstallationRole, time.Duration(in.Body.ExpiresInHours)*time.Hour)
			if err != nil {
				return nil, d.Fail("invite-user", err)
			}
			return &linkOutput{Body: LinkBody{SetupLink: l.URL(d.Origin), ExpiresAt: l.ExpiresAt}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "issue-reset-link", Method: http.MethodPost, Path: "/users/{id}/reset-link", Tags: []string{"users"},
		Summary: "Issue a single-use password reset link", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *idPath) (*linkOutput, error) {
			p, err := admin(ctx, false)
			if err != nil {
				return nil, d.Fail("issue-reset-link", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			l, err := d.Authn.IssueReset(ctx, p.Actor(), in.ID, time.Hour)
			if err != nil {
				return nil, d.Fail("issue-reset-link", err)
			}
			return &linkOutput{Body: LinkBody{SetupLink: l.URL(d.Origin), ExpiresAt: l.ExpiresAt}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-user-memberships", Method: http.MethodGet, Path: "/users/{id}/memberships", Tags: []string{"users"},
		Summary: "Projects a user belongs to", Security: Secured},
		func(ctx context.Context, in *idPath) (*membershipsOutput, error) {
			p := PrincipalFrom(ctx)
			if !authz.CanReadAllAudit(p) && p.UserID != in.ID {
				return nil, d.Fail("list-user-memberships", apperr.New(apperr.CodeNotFound, "user not found"))
			}
			m, err := d.Authn.UserMemberships(ctx, in.ID)
			return &membershipsOutput{Body: m}, d.Fail("list-user-memberships", err)
		})
}

var _ = audit.System
