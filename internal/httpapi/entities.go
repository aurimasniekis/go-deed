package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Declaration is one declaring source's facet of an entity: what that source asserted, never other sources' data.
// The Terraform provider stores exactly this, so runtime observations never cause plan diffs.
type Declaration struct {
	EntityID        string          `json:"entity_id"`
	Project         string          `json:"project"`
	ProjectID       string          `json:"project_id"`
	Source          string          `json:"source"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Metadata        map[string]any  `json:"metadata"`
	Spec            map[string]any  `json:"spec"`
	ResourceVersion int64           `json:"resource_version" doc:"Version of this declaration (ETag)."`
	Entity          *catalog.Entity `json:"entity" doc:"The effective entity (all sources merged, read-only status)."`
}

type declarationOutput struct {
	ETag string `header:"ETag"`
	Body *Declaration
}

type entityOutput struct {
	ETag string `header:"ETag"`
	Body *catalog.Entity
}

type EntityPath struct {
	ID string `path:"id" maxLength:"64"`
}

type DeclPath struct {
	ID     string `path:"id" maxLength:"64"`
	Source string `path:"source" maxLength:"63" doc:"Declaring source name in the entity's project (api, terraform, ui, overrides or a custom manual source)."`
}

// DeclarationBody is the writable part of a declaration.
type DeclarationBody struct {
	Metadata map[string]any `json:"metadata,omitempty" doc:"displayName, description, environment, owner, lifecycle, icon, labels, annotations, links, tags, version."`
	Spec     map[string]any `json:"spec,omitempty" doc:"Validated against the kind's storage schema version."`
}

type createEntityInput struct {
	Body struct {
		Project string `json:"project" maxLength:"64" doc:"Project ID or name."`
		Source  string `json:"source,omitempty" maxLength:"63" default:"api" doc:"Declaring source (default api; the Terraform provider uses terraform)."`
		Kind    string `json:"kind" maxLength:"128"`
		Name    string `json:"name" maxLength:"253"`
		DeclarationBody
	}
}

type putDeclarationInput struct {
	DeclPath
	IfMatch
	Body DeclarationBody
}

type deleteDeclarationInput struct {
	DeclPath
	IfMatch
}

type listEntitiesInput struct {
	Pagination
	Project           []string `query:"project,explode" doc:"Project IDs or names (repeat or comma-separate)."`
	Kind              []string `query:"kind,explode"`
	Q                 string   `query:"q" maxLength:"256" doc:"Full-text search (names, descriptions, labels, spec values, external IDs)."`
	Environment       []string `query:"environment,explode"`
	Owner             string   `query:"owner" maxLength:"64"`
	Health            []string `query:"health,explode" enum:"ok,warning,failed,unknown"`
	Freshness         []string `query:"freshness,explode" enum:"fresh,aging,stale,unknown"`
	Lifecycle         []string `query:"lifecycle,explode"`
	Label             []string `query:"label,explode" doc:"key=value; repeat for AND."`
	Source            string   `query:"source" maxLength:"64" doc:"Source ID: entities with a live facet from it."`
	Declared          string   `query:"declared" enum:"true,false," doc:"Only declared or undeclared (observed-only) entities."`
	Valid             string   `query:"valid" enum:"true,false,"`
	IncludeTombstoned bool     `query:"include_tombstoned"`
	Sort              string   `query:"sort" maxLength:"32" doc:"name, kind, updated_at, created_at, health, freshness, environment, display_name, last_seen_at; prefix - for descending."`
	IDs               []string `query:"id,explode" maxItems:"500"`
}

type entityPageOutput struct {
	Body *catalog.EntityPage
}

type lookupInput struct {
	Project string `query:"project" required:"true" maxLength:"64"`
	Kind    string `query:"kind" required:"true" maxLength:"128"`
	Name    string `query:"name" required:"true" maxLength:"253"`
}

type facetsOutput struct {
	Body []catalog.Facet
}

type provenanceOutput struct {
	Body map[string]catalog.Provenance
}

type conflictsOutput struct {
	Body []catalog.ConflictRow
}

type historyInput struct {
	ID     string `path:"id" maxLength:"64"`
	Limit  int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
	Before int64  `query:"before" doc:"Return changes with a sequence number below this."`
}

type changesOutput struct {
	Body []catalog.ChangeEvent
}

type renameInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Name string `json:"name" maxLength:"253"`
	}
}

type mergeInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		From string `json:"from" maxLength:"64" doc:"Entity to merge into this one; it is tombstoned and its name becomes an alias."`
	}
}

type bulkInput struct {
	Body struct {
		IDs          []string          `json:"ids" minItems:"1" maxItems:"500"`
		SetLabels    map[string]string `json:"set_labels,omitempty"`
		RemoveLabels []string          `json:"remove_labels,omitempty" maxItems:"64"`
		Owner        *string           `json:"owner,omitempty" maxLength:"64" doc:"Group entity ID; empty string clears the UI owner annotation."`
		Environment  *string           `json:"environment,omitempty" maxLength:"63"`
	}
}

// BulkResult reports a bulk annotation.
type BulkResult struct {
	Updated int                 `json:"updated"`
	Errors  []apperr.FieldError `json:"errors"`
}

type bulkOutput struct {
	Body BulkResult
}

type entityAuditInput struct {
	ID    string `path:"id" maxLength:"64"`
	Limit int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
}

type auditListOutput struct {
	Body []audit.Record
}

// declaringSource resolves a writable declaration source and checks the caller's role for its layer.
func (d *Deps) declaringSource(ctx context.Context, projectID, name string) (*catalog.Source, error) {
	if name == "" {
		name = "api"
	}
	src, err := d.Catalog.SourceByName(ctx, projectID, name)
	if err != nil {
		return nil, apperr.New(apperr.CodeNotFound, "source %q not found in this project", name)
	}
	if src.Provider != "manual" {
		return nil, apperr.New(apperr.CodeInvalid, "source %q is fed by %s; write to it through the ingest API", name, src.Provider)
	}
	need := authz.Editor
	if src.Layer == catalog.LayerOverride {
		need = authz.Admin
	}
	if err := authz.Require(PrincipalFrom(ctx), projectID, need); err != nil {
		return nil, err
	}
	return src, nil
}

func (d *Deps) declaration(ctx context.Context, entityID, source string) (*Declaration, error) {
	e, err := d.Catalog.GetEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	fs, err := d.Catalog.Facets(ctx, entityID, source)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if !f.Live {
			continue
		}
		// An annotation or override of an entity no source describes any more is dormant, not current: reads say it is
		// gone (Terraform plans to create it again, which fails clearly until the entity is back).
		if e.Status.TombstonedAt != nil && f.Layer.Overlay() {
			return nil, apperr.New(apperr.CodeNotFound, "entity %s is gone (no source describes it); its %s are dormant", e.Metadata.ID, source)
		}
		md, _ := f.Fields["metadata"].(map[string]any)
		spec, _ := f.Fields["spec"].(map[string]any)
		if md == nil {
			md = map[string]any{}
		}
		if spec == nil {
			spec = map[string]any{}
		}
		return &Declaration{EntityID: e.Metadata.ID, Project: e.Metadata.Project, ProjectID: e.Metadata.ProjectID, Source: source, Kind: e.Kind,
			Name: e.Metadata.Name, Metadata: md, Spec: spec, ResourceVersion: f.ResourceVersion, Entity: e}, nil
	}
	return nil, apperr.New(apperr.CodeNotFound, "source %q has no declaration for this entity", source)
}

// readableEntity loads an entity and checks the caller may read it (unreadable → not found).
func (d *Deps) readableEntity(ctx context.Context, id string, need authz.Level) (*catalog.Entity, error) {
	e, err := d.Catalog.GetEntity(ctx, id)
	if err != nil {
		return nil, err
	}
	if !authz.Can(PrincipalFrom(ctx), e.Metadata.ProjectID, need) {
		if authz.Can(PrincipalFrom(ctx), e.Metadata.ProjectID, authz.Viewer) {
			return nil, apperr.New(apperr.CodeForbidden, "project role %s required", need)
		}
		return nil, catalog.ErrEntityNotFound
	}
	return e, nil
}

func (d *Deps) writeDeclaration(ctx context.Context, src *catalog.Source, it *catalog.EntityItem, o catalog.ApplyOpts, action string) (*catalog.ApplyResult, error) {
	o.Actor, o.Strict = Actor(ctx), true
	var res *catalog.ApplyResult
	err := d.Store.W().TxNamed(ctx, "api."+action, func(tx *store.Tx) error {
		var err error
		if res, err = d.Catalog.ApplyEntity(ctx, tx, src, it, o); err != nil {
			return err
		}
		if res.Action == catalog.ActionNotFound || res.Action == catalog.ActionUnchanged {
			return nil
		}
		_, err = d.Audit.Append(ctx, tx, audit.Event{Actor: o.Actor, ProjectID: src.ProjectID, Action: "entity." + action,
			Target: audit.Target{Type: "entity", ID: res.EntityID}, Details: map[string]any{"source": src.Name, "result": res.Action}})
		return err
	})
	return res, err
}

func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func registerEntities(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-entities", Method: http.MethodGet, Path: "/entities", Tags: []string{"entities"},
		Summary: "Search and filter entities", Security: Secured},
		func(ctx context.Context, in *listEntitiesInput) (*entityPageOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("list-entities", err)
			}
			readable, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-entities", err)
			}
			f := catalog.EntityFilter{Kinds: splitList(in.Kind), Query: in.Q, Environment: splitList(in.Environment), Owner: in.Owner,
				Health: in.Health, Freshness: in.Freshness, Lifecycle: splitList(in.Lifecycle), SourceID: in.Source, IncludeTombstoned: in.IncludeTombstoned,
				Sort: in.Sort, Limit: in.Limit, Cursor: in.Cursor, IDs: splitList(in.IDs), Labels: map[string]string{}}
			for _, l := range in.Label {
				k, v, ok := strings.Cut(l, "=")
				if !ok {
					return nil, d.Fail("list-entities", apperr.New(apperr.CodeInvalid, "label filters are key=value"))
				}
				f.Labels[k] = v
			}
			if in.Declared != "" {
				b := in.Declared == "true"
				f.Declared = &b
			}
			if in.Valid != "" {
				b := in.Valid == "true"
				f.Valid = &b
			}
			f.ProjectIDs = readable
			if ps := splitList(in.Project); len(ps) > 0 {
				var ids []string
				for _, ref := range ps {
					pr, err := d.ResolveProject(ctx, ref, authz.Viewer)
					if err != nil {
						return nil, d.Fail("list-entities", err)
					}
					ids = append(ids, pr.ID)
				}
				f.ProjectIDs = ids
			}
			page, err := d.Catalog.ListEntities(ctx, f)
			return &entityPageOutput{Body: page}, d.Fail("list-entities", err)
		})
	huma.Register(api, huma.Operation{OperationID: "lookup-entity", Method: http.MethodGet, Path: "/entities/lookup", Tags: []string{"entities"},
		Summary: "Find an entity by project, kind and name (aliases included)", Security: Secured},
		func(ctx context.Context, in *lookupInput) (*entityOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
			if err != nil {
				return nil, d.Fail("lookup-entity", err)
			}
			id, err := d.lookupID(ctx, pr.ID, in.Kind, in.Name)
			if err != nil {
				return nil, d.Fail("lookup-entity", err)
			}
			e, err := d.Catalog.GetEntity(ctx, id)
			if err != nil {
				return nil, d.Fail("lookup-entity", err)
			}
			return &entityOutput{ETag: ETag(e.Metadata.ResourceVersion), Body: e}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-entity", Method: http.MethodPost, Path: "/entities", Tags: []string{"entities"},
		Summary: "Declare a new entity", Description: "Creates the entity (or adopts an existing one with the same project, kind and name that this " +
			"source has not declared yet) and stores the source's declaration. Fails with 409 already_exists when the source already declares it.",
		Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createEntityInput) (*declarationOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Body.Project, authz.Editor)
			if err != nil {
				return nil, d.Fail("create-entity", err)
			}
			src, err := d.declaringSource(ctx, pr.ID, in.Body.Source)
			if err != nil {
				return nil, d.Fail("create-entity", err)
			}
			it := &catalog.EntityItem{Kind: in.Body.Kind, Name: in.Body.Name, Metadata: in.Body.Metadata, Spec: in.Body.Spec}
			res, err := d.writeDeclaration(ctx, src, it, catalog.ApplyOpts{CreateOnly: true}, "declared")
			if err != nil {
				return nil, d.Fail("create-entity", err)
			}
			decl, err := d.declaration(ctx, res.EntityID, src.Name)
			if err != nil {
				return nil, d.Fail("create-entity", err)
			}
			return &declarationOutput{ETag: ETag(decl.ResourceVersion), Body: decl}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-entity", Method: http.MethodGet, Path: "/entities/{id}", Tags: []string{"entities"},
		Summary: "Get an entity (effective view)", Security: Secured},
		func(ctx context.Context, in *EntityPath) (*entityOutput, error) {
			e, err := d.readableEntity(ctx, in.ID, authz.Viewer)
			if err != nil {
				return nil, d.Fail("get-entity", err)
			}
			return &entityOutput{ETag: ETag(e.Metadata.ResourceVersion), Body: e}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-declaration", Method: http.MethodGet, Path: "/entities/{id}/declarations/{source}",
		Tags: []string{"entities"}, Summary: "Read one source's declaration of an entity", Security: Secured},
		func(ctx context.Context, in *DeclPath) (*declarationOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("get-declaration", err)
			}
			decl, err := d.declaration(ctx, in.ID, in.Source)
			if err != nil {
				return nil, d.Fail("get-declaration", err)
			}
			return &declarationOutput{ETag: ETag(decl.ResourceVersion), Body: decl}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "put-declaration", Method: http.MethodPut, Path: "/entities/{id}/declarations/{source}",
		Tags: []string{"entities"}, Summary: "Replace (or add) one source's declaration of an entity", Security: Secured},
		func(ctx context.Context, in *putDeclarationInput) (*declarationOutput, error) {
			e, err := d.readableEntity(ctx, in.ID, authz.Editor)
			if err != nil {
				return nil, d.Fail("put-declaration", err)
			}
			src, err := d.declaringSource(ctx, e.Metadata.ProjectID, in.Source)
			if err != nil {
				return nil, d.Fail("put-declaration", err)
			}
			if e.Status.TombstonedAt != nil && src.Layer.Overlay() {
				return nil, d.Fail("put-declaration", apperr.New(apperr.CodeConflict,
					"entity %s/%s is gone (no source describes it); annotations and overrides apply only to live entities", e.Kind, e.Metadata.Name))
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			it := &catalog.EntityItem{Ref: catalog.Ref{ID: in.ID}, Metadata: in.Body.Metadata, Spec: in.Body.Spec}
			if _, err := d.writeDeclaration(ctx, src, it, catalog.ApplyOpts{IfMatch: v}, "declared"); err != nil {
				return nil, d.Fail("put-declaration", err)
			}
			decl, err := d.declaration(ctx, in.ID, src.Name)
			if err != nil {
				return nil, d.Fail("put-declaration", err)
			}
			return &declarationOutput{ETag: ETag(decl.ResourceVersion), Body: decl}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-declaration", Method: http.MethodDelete, Path: "/entities/{id}/declarations/{source}",
		Tags: []string{"entities"}, Summary: "Remove one source's declaration", Description: "Only this source's facet is removed. The entity " +
			"stays while other sources (imports, runtime observations, annotations) still contribute; otherwise it is tombstoned.",
		Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *deleteDeclarationInput) (*noContent, error) {
			e, err := d.readableEntity(ctx, in.ID, authz.Editor)
			if err != nil {
				return nil, d.Fail("delete-declaration", err)
			}
			src, err := d.declaringSource(ctx, e.Metadata.ProjectID, in.Source)
			if err != nil {
				return nil, d.Fail("delete-declaration", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			res, err := d.writeDeclaration(ctx, src, &catalog.EntityItem{Ref: catalog.Ref{ID: in.ID}, Delete: true}, catalog.ApplyOpts{IfMatch: v}, "declaration_removed")
			if err != nil {
				return nil, d.Fail("delete-declaration", err)
			}
			if res.Action == catalog.ActionNotFound {
				return nil, d.Fail("delete-declaration", apperr.New(apperr.CodeNotFound, "source %q has no declaration for this entity", src.Name))
			}
			return nil, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-entity-facets", Method: http.MethodGet, Path: "/entities/{id}/facets", Tags: []string{"entities"},
		Summary: "Every source's contribution (declared, imported, discovered, observations, annotations)", Security: Secured},
		func(ctx context.Context, in *EntityPath) (*facetsOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("list-entity-facets", err)
			}
			fs, err := d.Catalog.Facets(ctx, in.ID, "")
			return &facetsOutput{Body: fs}, d.Fail("list-entity-facets", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-entity-provenance", Method: http.MethodGet, Path: "/entities/{id}/provenance",
		Tags: []string{"entities"}, Summary: "Why does each value exist: winning source, layer, revision, time and losing candidates", Security: Secured},
		func(ctx context.Context, in *EntityPath) (*provenanceOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("get-entity-provenance", err)
			}
			p, err := d.Catalog.Provenance(ctx, in.ID)
			return &provenanceOutput{Body: p}, d.Fail("get-entity-provenance", err)
		})
	huma.Register(api, huma.Operation{OperationID: "list-entity-conflicts", Method: http.MethodGet, Path: "/entities/{id}/conflicts",
		Tags: []string{"entities"}, Summary: "Same-layer disagreements between sources", Security: Secured},
		func(ctx context.Context, in *EntityPath) (*conflictsOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("list-entity-conflicts", err)
			}
			c, err := d.Catalog.Conflicts(ctx, in.ID)
			return &conflictsOutput{Body: c}, d.Fail("list-entity-conflicts", err)
		})
	huma.Register(api, huma.Operation{OperationID: "list-entity-history", Method: http.MethodGet, Path: "/entities/{id}/history",
		Tags: []string{"entities"}, Summary: "Change history (newest first)", Security: Secured},
		func(ctx context.Context, in *historyInput) (*changesOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("list-entity-history", err)
			}
			c, err := d.Catalog.Changes(ctx, catalog.ChangeFilter{ObjectID: in.ID, Limit: in.Limit, BeforeSeq: in.Before})
			return &changesOutput{Body: c}, d.Fail("list-entity-history", err)
		})
	huma.Register(api, huma.Operation{OperationID: "list-entity-audit", Method: http.MethodGet, Path: "/entities/{id}/audit",
		Tags: []string{"entities"}, Summary: "Audit records about this entity (project admin or auditor)", Security: Secured},
		func(ctx context.Context, in *entityAuditInput) (*auditListOutput, error) {
			e, err := d.readableEntity(ctx, in.ID, authz.Viewer)
			if err != nil {
				return nil, d.Fail("list-entity-audit", err)
			}
			p := PrincipalFrom(ctx)
			if !authz.CanReadAllAudit(p) && !authz.Can(p, e.Metadata.ProjectID, authz.Admin) {
				return nil, d.Fail("list-entity-audit", apperr.New(apperr.CodeForbidden, "project admin or auditor required"))
			}
			recs, err := audit.List(ctx, d.Store.R(), audit.Filter{TargetType: "entity", TargetID: in.ID, Limit: in.Limit})
			return &auditListOutput{Body: recs}, d.Fail("list-entity-audit", err)
		})
	huma.Register(api, huma.Operation{OperationID: "rename-entity", Method: http.MethodPost, Path: "/entities/{id}/rename", Tags: []string{"entities"},
		Summary: "Rename an entity (the old name becomes an alias)", Security: Secured},
		func(ctx context.Context, in *renameInput) (*entityOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Editor); err != nil {
				return nil, d.Fail("rename-entity", err)
			}
			if err := d.Catalog.Rename(ctx, Actor(ctx), in.ID, in.Body.Name); err != nil {
				return nil, d.Fail("rename-entity", err)
			}
			e, err := d.Catalog.GetEntity(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("rename-entity", err)
			}
			return &entityOutput{ETag: ETag(e.Metadata.ResourceVersion), Body: e}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "merge-entities", Method: http.MethodPost, Path: "/entities/{id}/merge", Tags: []string{"entities"},
		Summary: "Merge a duplicate into this entity (project admin)", Security: Secured},
		func(ctx context.Context, in *mergeInput) (*entityOutput, error) {
			if _, err := d.readableEntity(ctx, in.ID, authz.Admin); err != nil {
				return nil, d.Fail("merge-entities", err)
			}
			if _, err := d.readableEntity(ctx, in.Body.From, authz.Admin); err != nil {
				return nil, d.Fail("merge-entities", err)
			}
			if err := d.Catalog.MergeEntities(ctx, Actor(ctx), in.ID, in.Body.From); err != nil {
				return nil, d.Fail("merge-entities", err)
			}
			e, err := d.Catalog.GetEntity(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("merge-entities", err)
			}
			return &entityOutput{ETag: ETag(e.Metadata.ResourceVersion), Body: e}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "bulk-annotate-entities", Method: http.MethodPost, Path: "/entities/bulk", Tags: []string{"entities"},
		Summary: "Set labels, owner or environment on many entities", Description: "Writes the UI annotation facet of each entity (editor on each " +
			"project). Declarations and imports are untouched; annotations rank below declarations (ADR-003).", Security: Secured},
		func(ctx context.Context, in *bulkInput) (*bulkOutput, error) {
			res := BulkResult{Errors: []apperr.FieldError{}}
			p := PrincipalFrom(ctx)
			err := d.Store.W().TxNamed(ctx, "api.bulk_annotate", func(tx *store.Tx) error {
				res = BulkResult{Errors: []apperr.FieldError{}}
				for _, id := range in.Body.IDs {
					e, err := d.Catalog.GetEntity(ctx, id)
					if err != nil || !authz.Can(p, e.Metadata.ProjectID, authz.Editor) {
						res.Errors = append(res.Errors, apperr.FieldError{Field: id, Message: "not found or not editable"})
						continue
					}
					src, err := d.Catalog.SourceByName(ctx, e.Metadata.ProjectID, "ui")
					if err != nil {
						return err
					}
					fs, err := d.Catalog.Facets(ctx, id, "ui")
					if err != nil {
						return err
					}
					md := map[string]any{}
					for _, f := range fs {
						if f.Live {
							if m, ok := f.Fields["metadata"].(map[string]any); ok {
								md = m
							}
						}
					}
					labels, _ := md["labels"].(map[string]any)
					if labels == nil {
						labels = map[string]any{}
					}
					for k, v := range in.Body.SetLabels {
						labels[k] = v
					}
					for _, k := range in.Body.RemoveLabels {
						delete(labels, k)
					}
					md["labels"] = labels
					if len(labels) == 0 {
						delete(md, "labels")
					}
					if in.Body.Owner != nil {
						if *in.Body.Owner == "" {
							delete(md, "owner")
						} else {
							md["owner"] = *in.Body.Owner
						}
					}
					if in.Body.Environment != nil {
						if *in.Body.Environment == "" {
							delete(md, "environment")
						} else {
							md["environment"] = *in.Body.Environment
						}
					}
					it := &catalog.EntityItem{Ref: catalog.Ref{ID: id}, Metadata: md}
					if len(md) == 0 {
						it.Delete = true
					}
					if _, err := d.Catalog.ApplyEntity(ctx, tx, src, it, catalog.ApplyOpts{Actor: p.Actor()}); err != nil {
						if apperr.CodeOf(err) == apperr.CodeInvalid {
							res.Errors = append(res.Errors, apperr.FieldError{Field: id, Message: err.Error()})
							continue
						}
						return err
					}
					res.Updated++
				}
				_, err := d.Audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "entity.bulk_annotated",
					Details: map[string]any{"count": res.Updated, "labels_set": len(in.Body.SetLabels), "labels_removed": len(in.Body.RemoveLabels)}})
				return err
			})
			return &bulkOutput{Body: res}, d.Fail("bulk-annotate-entities", err)
		})
}

// lookupID resolves a natural key or alias.
func (d *Deps) lookupID(ctx context.Context, projectID, kind, name string) (string, error) {
	var id string
	err := d.Store.R().QueryRowContext(ctx, `SELECT id FROM entities WHERE project_id = ? AND kind = ? AND name = ?
		UNION ALL SELECT entity_id FROM entity_aliases WHERE project_id = ? AND kind = ? AND name = ? LIMIT 1`,
		projectID, kind, name, projectID, kind, name).Scan(&id)
	if err != nil {
		return "", catalog.ErrEntityNotFound
	}
	return id, nil
}
