package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
)

type labelRulesOutput struct {
	Body []catalog.LabelRule
}

type labelRuleOutput struct {
	ETag string `header:"ETag"`
	Body *catalog.LabelRule
}

type LabelRulePath struct {
	Project string `path:"project" maxLength:"64" doc:"Project ID or name."`
	Rule    string `path:"rule" maxLength:"64" doc:"Label rule ID or name."`
}

type createLabelRuleInput struct {
	ProjectPath
	Body catalog.LabelRuleInput
}

type updateLabelRuleInput struct {
	LabelRulePath
	IfMatch
	Body catalog.LabelRuleInput
}

// registerLabelRules: project label rules (docs/LABEL_RULES.md). Viewers read them; project admins change them, and
// every change re-applies the project's rules to its entities before the response.
type labelKeysInput struct {
	Project string `query:"project" maxLength:"64" doc:"Project ID or name (default: every project you can read)."`
}

type labelKeysOutput struct {
	Body []catalog.LabelKey
}

func registerLabelRules(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-label-keys", Method: http.MethodGet, Path: "/entities/label-keys",
		Tags: []string{"entities"}, Summary: "Label keys in use, with entity and value counts (map grouping, show / hide)", Security: Secured},
		func(ctx context.Context, in *labelKeysInput) (*labelKeysOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("list-label-keys", err)
			}
			projects, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-label-keys", err)
			}
			if in.Project != "" {
				pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
				if err != nil {
					return nil, d.Fail("list-label-keys", err)
				}
				projects = []string{pr.ID}
			}
			keys, err := d.Catalog.LabelKeys(ctx, projects)
			if err != nil {
				return nil, d.Fail("list-label-keys", err)
			}
			return &labelKeysOutput{Body: keys}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-label-rules", Method: http.MethodGet, Path: "/projects/{project}/label-rules",
		Tags: []string{"projects"}, Summary: "Label rules of a project", Security: Secured},
		func(ctx context.Context, in *ProjectPath) (*labelRulesOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
			if err != nil {
				return nil, d.Fail("list-label-rules", err)
			}
			rs, err := d.Catalog.LabelRules(ctx, pr.ID)
			if err != nil {
				return nil, d.Fail("list-label-rules", err)
			}
			return &labelRulesOutput{Body: rs}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-label-rule", Method: http.MethodGet, Path: "/projects/{project}/label-rules/{rule}",
		Tags: []string{"projects"}, Summary: "Get a label rule", Security: Secured},
		func(ctx context.Context, in *LabelRulePath) (*labelRuleOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
			if err != nil {
				return nil, d.Fail("get-label-rule", err)
			}
			r, err := d.Catalog.LabelRule(ctx, pr.ID, in.Rule)
			if err != nil {
				return nil, d.Fail("get-label-rule", err)
			}
			return &labelRuleOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-label-rule", Method: http.MethodPost, Path: "/projects/{project}/label-rules",
		Tags: []string{"projects"}, Summary: "Create a label rule (project admin)", Security: Secured, DefaultStatus: http.StatusCreated,
		Description: "Adds labels to the project's entities its match selects, below every source: a source's own label for the same key " +
			"wins, and a rule never keeps an entity alive. The project's entities are re-merged before the response."},
		func(ctx context.Context, in *createLabelRuleInput) (*labelRuleOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("create-label-rule", err)
			}
			r, err := d.Catalog.CreateLabelRule(ctx, Actor(ctx), pr.ID, in.Body)
			if err != nil {
				return nil, d.Fail("create-label-rule", err)
			}
			return &labelRuleOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-label-rule", Method: http.MethodPatch, Path: "/projects/{project}/label-rules/{rule}",
		Tags: []string{"projects"}, Summary: "Update a label rule (project admin)", Security: Secured},
		func(ctx context.Context, in *updateLabelRuleInput) (*labelRuleOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("update-label-rule", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			r, err := d.Catalog.UpdateLabelRule(ctx, Actor(ctx), pr.ID, in.Rule, in.Body, v)
			if err != nil {
				return nil, d.Fail("update-label-rule", err)
			}
			return &labelRuleOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-label-rule", Method: http.MethodDelete, Path: "/projects/{project}/label-rules/{rule}",
		Tags: []string{"projects"}, Summary: "Delete a label rule (project admin)", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *LabelRulePath) (*noContent, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("delete-label-rule", err)
			}
			return nil, d.Fail("delete-label-rule", d.Catalog.DeleteLabelRule(ctx, Actor(ctx), pr.ID, in.Rule))
		})
}
