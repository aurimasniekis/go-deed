package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
)

// ProjectView adds the caller's role to a project.
type ProjectView struct {
	*catalog.Project
	Role string `json:"role" doc:"Your effective role in the project: viewer, editor or admin."`
}

type projectsOutput struct {
	Body []ProjectView
}

type projectOutput struct {
	ETag string `header:"ETag"`
	Body ProjectView
}

type ProjectPath struct {
	Project string `path:"project" maxLength:"64" doc:"Project ID or name."`
}

type createProjectInput struct {
	Body catalog.ProjectInput
}

type updateProjectInput struct {
	ProjectPath
	IfMatch
	Body catalog.ProjectInput
}

type deleteProjectInput struct {
	ProjectPath
	Force bool `query:"force" doc:"Delete even if the project still has entities."`
}

type membersOutput struct {
	Body []authn.ProjectMembership
}

type memberInput struct {
	Project string `path:"project" maxLength:"64" doc:"Project ID or name."`
	User    string `path:"user" maxLength:"64"`
	Body    struct {
		Role string `json:"role" enum:"viewer,editor,admin"`
	}
}

type MemberPath struct {
	Project string `path:"project" maxLength:"64" doc:"Project ID or name."`
	User    string `path:"user" maxLength:"64"`
}

type bridgesOutput struct {
	Body []catalog.Bridge
}

type bridgeOutput struct {
	Body *catalog.Bridge
}

type createBridgeInput struct {
	Body struct {
		FromProject  string   `json:"from_project" maxLength:"64"`
		ToProject    string   `json:"to_project" maxLength:"64"`
		AllowedTypes []string `json:"allowed_types,omitempty" maxItems:"50" doc:"Relationship types allowed across the bridge (empty = any)."`
		Description  string   `json:"description,omitempty" maxLength:"2000"`
	}
}

func registerProjects(api huma.API, d *Deps) {
	view := func(p *authn.Principal, pr *catalog.Project) ProjectView {
		return ProjectView{Project: pr, Role: authz.ProjectLevel(p, pr.ID).String()}
	}
	huma.Register(api, huma.Operation{OperationID: "list-projects", Method: http.MethodGet, Path: "/projects", Tags: []string{"projects"},
		Summary: "Projects you can read", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*projectsOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-projects", err)
			}
			all, err := d.Catalog.Projects(ctx)
			if err != nil {
				return nil, d.Fail("list-projects", err)
			}
			out := []ProjectView{}
			for _, pr := range all {
				if authz.Can(p, pr.ID, authz.Viewer) {
					out = append(out, view(p, pr))
				}
			}
			return &projectsOutput{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-project", Method: http.MethodPost, Path: "/projects", Tags: []string{"projects"},
		Summary: "Create a project (installation owner or admin)", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createProjectInput) (*projectOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("create-project", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("create-project", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			creator := ""
			if p.TokenID == "" {
				creator = p.UserID
			}
			pr, err := d.Catalog.CreateProject(ctx, p.Actor(), in.Body, creator)
			if err != nil {
				return nil, d.Fail("create-project", err)
			}
			return &projectOutput{ETag: ETag(pr.ResourceVersion), Body: view(p, pr)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-project", Method: http.MethodGet, Path: "/projects/{project}", Tags: []string{"projects"},
		Summary: "Get a project", Security: Secured},
		func(ctx context.Context, in *ProjectPath) (*projectOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
			if err != nil {
				return nil, d.Fail("get-project", err)
			}
			return &projectOutput{ETag: ETag(pr.ResourceVersion), Body: view(PrincipalFrom(ctx), pr)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-project", Method: http.MethodPatch, Path: "/projects/{project}", Tags: []string{"projects"},
		Summary: "Update a project (project admin)", Security: Secured},
		func(ctx context.Context, in *updateProjectInput) (*projectOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("update-project", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			pr, err = d.Catalog.UpdateProject(ctx, Actor(ctx), pr.ID, in.Body, v)
			if err != nil {
				return nil, d.Fail("update-project", err)
			}
			return &projectOutput{ETag: ETag(pr.ResourceVersion), Body: view(PrincipalFrom(ctx), pr)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-project", Method: http.MethodDelete, Path: "/projects/{project}", Tags: []string{"projects"},
		Summary: "Delete a project (installation owner or admin, recent sign-in)", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *deleteProjectInput) (*noContent, error) {
			p := PrincipalFrom(ctx)
			if !authz.CanAdminister(p) {
				return nil, d.Fail("delete-project", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			pr, err := d.Catalog.Project(ctx, in.Project)
			if err != nil {
				return nil, d.Fail("delete-project", err)
			}
			return nil, d.Fail("delete-project", d.Catalog.DeleteProject(ctx, Actor(ctx), pr.ID, in.Force))
		})
	huma.Register(api, huma.Operation{OperationID: "list-project-members", Method: http.MethodGet, Path: "/projects/{project}/members",
		Tags: []string{"projects"}, Summary: "Members of a project", Security: Secured},
		func(ctx context.Context, in *ProjectPath) (*membersOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
			if err != nil {
				return nil, d.Fail("list-project-members", err)
			}
			m, err := d.Authn.ListMembers(ctx, pr.ID)
			return &membersOutput{Body: m}, d.Fail("list-project-members", err)
		})
	huma.Register(api, huma.Operation{OperationID: "set-project-member", Method: http.MethodPut, Path: "/projects/{project}/members/{user}",
		Tags: []string{"projects"}, Summary: "Grant a project role (project admin)", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *memberInput) (*noContent, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("set-project-member", err)
			}
			return nil, d.Fail("set-project-member", d.Authn.SetMember(ctx, Actor(ctx), pr.ID, in.User, in.Body.Role))
		})
	huma.Register(api, huma.Operation{OperationID: "remove-project-member", Method: http.MethodDelete, Path: "/projects/{project}/members/{user}",
		Tags: []string{"projects"}, Summary: "Remove a project role (project admin)", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *MemberPath) (*noContent, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("remove-project-member", err)
			}
			return nil, d.Fail("remove-project-member", d.Authn.SetMember(ctx, Actor(ctx), pr.ID, in.User, ""))
		})
	huma.Register(api, huma.Operation{OperationID: "list-bridges", Method: http.MethodGet, Path: "/bridges", Tags: []string{"projects"},
		Summary: "Bridges touching projects you can read", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*bridgesOutput, error) {
			p := PrincipalFrom(ctx)
			all, err := d.Catalog.Bridges(ctx)
			if err != nil {
				return nil, d.Fail("list-bridges", err)
			}
			out := []catalog.Bridge{}
			for _, b := range all {
				if authz.Can(p, b.FromProject, authz.Viewer) || authz.Can(p, b.ToProject, authz.Viewer) {
					out = append(out, b)
				}
			}
			return &bridgesOutput{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-bridge", Method: http.MethodPost, Path: "/bridges", Tags: []string{"projects"},
		Summary: "Declare a bridge between two projects (admin of both)", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createBridgeInput) (*bridgeOutput, error) {
			from, err := d.ResolveProject(ctx, in.Body.FromProject, authz.Admin)
			if err != nil {
				return nil, d.Fail("create-bridge", err)
			}
			to, err := d.ResolveProject(ctx, in.Body.ToProject, authz.Admin)
			if err != nil {
				return nil, d.Fail("create-bridge", err)
			}
			b, err := d.Catalog.CreateBridge(ctx, Actor(ctx), from.ID, to.ID, in.Body.AllowedTypes, in.Body.Description)
			return &bridgeOutput{Body: b}, d.Fail("create-bridge", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-bridge", Method: http.MethodDelete, Path: "/bridges/{id}", Tags: []string{"projects"},
		Summary: "Remove a bridge (admin of both projects)", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *idPath) (*noContent, error) {
			all, err := d.Catalog.Bridges(ctx)
			if err != nil {
				return nil, d.Fail("delete-bridge", err)
			}
			p := PrincipalFrom(ctx)
			for _, b := range all {
				if b.ID == in.ID {
					if !authz.Can(p, b.FromProject, authz.Admin) || !authz.Can(p, b.ToProject, authz.Admin) {
						return nil, d.Fail("delete-bridge", apperr.New(apperr.CodeForbidden, "admin of both projects required"))
					}
					return nil, d.Fail("delete-bridge", d.Catalog.DeleteBridge(ctx, Actor(ctx), in.ID))
				}
			}
			return nil, d.Fail("delete-bridge", apperr.New(apperr.CodeNotFound, "bridge not found"))
		})
}
