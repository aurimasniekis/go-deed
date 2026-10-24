package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
)

// SupportResolver computes the support levels of a kind from the mapping and provider registries (Q-06).
type SupportResolver func(kind string) catalog.Support

// KindView is a kind with its computed support levels.
type KindView struct {
	*catalog.Kind
	Support catalog.Support `json:"support"`
}

type kindsOutput struct {
	Body []KindView
}

type kindOutput struct {
	ETag string `header:"ETag"`
	Body KindView
}

type KindPath struct {
	Name string `path:"name" maxLength:"128"`
}

type createKindInput struct {
	Body struct {
		catalog.KindInput
		Schema json.RawMessage `json:"schema,omitempty" doc:"JSON Schema 2020-12 for spec (v1). Defaults to an open object."`
	}
}

type updateKindInput struct {
	KindPath
	IfMatch
	Body catalog.KindInput
}

type publishInput struct {
	KindPath
	IfMatch
	Body struct {
		Schema    json.RawMessage `json:"schema"`
		Breaking  bool            `json:"breaking,omitempty"`
		Migration string          `json:"migration,omitempty" enum:"revalidate,mark_invalid,"`
	}
}

type publishOutput struct {
	ETag string `header:"ETag"`
	Body *catalog.PublishResult
}

type validateSchemaInput struct {
	KindPath
	Body struct {
		Schema json.RawMessage `json:"schema"`
		Spec   map[string]any  `json:"spec,omitempty" doc:"Optional sample spec to validate."`
	}
}

// SchemaCheck reports schema validity, compatibility with the storage version and sample validation.
type SchemaCheck struct {
	Valid                 bool                `json:"valid"`
	Error                 string              `json:"error,omitempty"`
	CompatibilityProblems []string            `json:"compatibility_problems"`
	SampleErrors          []apperr.FieldError `json:"sample_errors"`
}

type schemaCheckOutput struct {
	Body SchemaCheck
}

type relTypesOutput struct {
	Body []*catalog.RelType
}

type relTypeOutput struct {
	ETag string `header:"ETag"`
	Body *catalog.RelType
}

type createRelTypeInput struct {
	Body catalog.RelTypeInput
}

type updateRelTypeInput struct {
	KindPath
	IfMatch
	Body catalog.RelTypeInput
}

// KindSupport is set by the app to compute support levels.
var KindSupport SupportResolver = func(string) catalog.Support { return catalog.Support{Modeling: true} }

func kindView(k *catalog.Kind) KindView {
	s := KindSupport(k.Name)
	s.Modeling = true
	if s.Terraform == nil {
		s.Terraform = []string{}
	}
	if s.Runtime == nil {
		s.Runtime = []string{}
	}
	if s.Native == nil {
		s.Native = []string{}
	}
	return KindView{Kind: k, Support: s}
}

func registerKinds(api huma.API, d *Deps) {
	adminOnly := func(ctx context.Context) error {
		p, err := requireUser(ctx)
		if err != nil {
			return err
		}
		if !authz.CanAdminister(p) {
			return apperr.New(apperr.CodeForbidden, "installation owner or admin required to manage kinds and relationship types")
		}
		return nil
	}
	huma.Register(api, huma.Operation{OperationID: "list-kinds", Method: http.MethodGet, Path: "/kinds", Tags: []string{"kinds"},
		Summary: "Built-in and custom kinds with support levels", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*kindsOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("list-kinds", err)
			}
			out := []KindView{}
			for _, k := range d.Catalog.Kinds() {
				out = append(out, kindView(k))
			}
			return &kindsOutput{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-kind", Method: http.MethodGet, Path: "/kinds/{name}", Tags: []string{"kinds"},
		Summary: "Get a kind", Security: Secured},
		func(ctx context.Context, in *KindPath) (*kindOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("get-kind", err)
			}
			k := d.Catalog.Kind(in.Name)
			if k == nil {
				return nil, d.Fail("get-kind", apperr.New(apperr.CodeNotFound, "kind %q not found", in.Name))
			}
			return &kindOutput{ETag: ETag(k.ResourceVersion), Body: kindView(k)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-kind", Method: http.MethodPost, Path: "/kinds", Tags: []string{"kinds"},
		Summary: "Create a custom kind", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createKindInput) (*kindOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("create-kind", err)
			}
			k, err := d.Catalog.CreateKind(ctx, Actor(ctx), in.Body.KindInput, in.Body.Schema)
			if err != nil {
				return nil, d.Fail("create-kind", err)
			}
			return &kindOutput{ETag: ETag(k.ResourceVersion), Body: kindView(k)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-kind", Method: http.MethodPatch, Path: "/kinds/{name}", Tags: []string{"kinds"},
		Summary: "Update a custom kind's display fields and identity keys", Security: Secured},
		func(ctx context.Context, in *updateKindInput) (*kindOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("update-kind", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			k, err := d.Catalog.UpdateKind(ctx, Actor(ctx), in.Name, in.Body, v)
			if err != nil {
				return nil, d.Fail("update-kind", err)
			}
			return &kindOutput{ETag: ETag(k.ResourceVersion), Body: kindView(k)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "publish-kind-version", Method: http.MethodPost, Path: "/kinds/{name}/versions", Tags: []string{"kinds"},
		Summary: "Publish a new schema version", Description: "Non-breaking versions must be backward compatible and validate every existing " +
			"entity. Breaking versions need migration revalidate (refused if any entity fails) or mark_invalid (entities are flagged, never dropped).",
		Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *publishInput) (*publishOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("publish-kind-version", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			res, err := d.Catalog.PublishVersion(ctx, Actor(ctx), in.Name, in.Body.Schema, in.Body.Breaking, in.Body.Migration, v)
			if err != nil {
				var pr *Problem
				if errors.As(d.Fail("publish-kind-version", err), &pr) && res != nil {
					for _, p := range res.CompatibilityProblems {
						pr.Fields = append(pr.Fields, apperr.FieldError{Field: "schema", Message: p})
					}
					return nil, pr
				}
				return nil, d.Fail("publish-kind-version", err)
			}
			return &publishOutput{ETag: ETag(res.Kind.ResourceVersion), Body: res}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "check-kind-schema", Method: http.MethodPost, Path: "/kinds/{name}/schema-check", Tags: []string{"kinds"},
		Summary: "Dry-run a schema: validity, compatibility with the storage version, sample validation", Security: Secured},
		func(ctx context.Context, in *validateSchemaInput) (*schemaCheckOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("check-kind-schema", err)
			}
			out := SchemaCheck{CompatibilityProblems: []string{}, SampleErrors: []apperr.FieldError{}}
			cs, err := catalog.ParseSchema(in.Body.Schema)
			if err != nil {
				out.Error = err.Error()
				return &schemaCheckOutput{Body: out}, nil //nolint:nilerr // an invalid schema is the check's answer, not a request failure
			}
			out.Valid = true
			if k := d.Catalog.Kind(in.Name); k != nil {
				if p := catalog.CompatibilityProblems(k.Schema("").Raw, cs.Raw); p != nil {
					out.CompatibilityProblems = p
				}
			}
			if in.Body.Spec != nil {
				if fe := cs.Validate(in.Body.Spec); fe != nil {
					out.SampleErrors = fe
				}
			}
			return &schemaCheckOutput{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-kind", Method: http.MethodDelete, Path: "/kinds/{name}", Tags: []string{"kinds"},
		Summary: "Delete an unused custom kind", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *KindPath) (*noContent, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("delete-kind", err)
			}
			return nil, d.Fail("delete-kind", d.Catalog.DeleteKind(ctx, Actor(ctx), in.Name))
		})
	huma.Register(api, huma.Operation{OperationID: "list-relationship-types", Method: http.MethodGet, Path: "/relationship-types",
		Tags: []string{"kinds"}, Summary: "Built-in and custom relationship types", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*relTypesOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("list-relationship-types", err)
			}
			return &relTypesOutput{Body: d.Catalog.RelTypes()}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-relationship-type", Method: http.MethodGet, Path: "/relationship-types/{name}",
		Tags: []string{"kinds"}, Summary: "Get a relationship type", Security: Secured},
		func(ctx context.Context, in *KindPath) (*relTypeOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("get-relationship-type", err)
			}
			r := d.Catalog.RelType(in.Name)
			if r == nil {
				return nil, d.Fail("get-relationship-type", apperr.New(apperr.CodeNotFound, "relationship type %q not found", in.Name))
			}
			return &relTypeOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-relationship-type", Method: http.MethodPost, Path: "/relationship-types",
		Tags: []string{"kinds"}, Summary: "Create a custom relationship type", Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createRelTypeInput) (*relTypeOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("create-relationship-type", err)
			}
			r, err := d.Catalog.CreateRelType(ctx, Actor(ctx), in.Body)
			if err != nil {
				return nil, d.Fail("create-relationship-type", err)
			}
			return &relTypeOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-relationship-type", Method: http.MethodPut, Path: "/relationship-types/{name}",
		Tags: []string{"kinds"}, Summary: "Replace a custom relationship type", Security: Secured},
		func(ctx context.Context, in *updateRelTypeInput) (*relTypeOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("update-relationship-type", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			r, err := d.Catalog.UpdateRelType(ctx, Actor(ctx), in.Name, in.Body, v)
			if err != nil {
				return nil, d.Fail("update-relationship-type", err)
			}
			return &relTypeOutput{ETag: ETag(r.ResourceVersion), Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-relationship-type", Method: http.MethodDelete, Path: "/relationship-types/{name}",
		Tags: []string{"kinds"}, Summary: "Delete an unused custom relationship type", Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *KindPath) (*noContent, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("delete-relationship-type", err)
			}
			return nil, d.Fail("delete-relationship-type", d.Catalog.DeleteRelType(ctx, Actor(ctx), in.Name))
		})
}
