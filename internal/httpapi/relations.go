package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
)

type relationOutput struct {
	Body *catalog.Relation
}

type relationPageOutput struct {
	Body *catalog.RelationPage
}

type RelationPath struct {
	ID string `path:"id" maxLength:"64"`
}

type AssertionPath struct {
	ID     string `path:"id" maxLength:"64"`
	Source string `path:"source" maxLength:"63"`
}

type createRelationInput struct {
	Body struct {
		Project    string         `json:"project,omitempty" maxLength:"64" doc:"Project of the declaring source (default: the from entity's project)."`
		Source     string         `json:"source,omitempty" maxLength:"63" default:"api"`
		Type       string         `json:"type" maxLength:"128"`
		From       catalog.Ref    `json:"from"`
		To         catalog.Ref    `json:"to"`
		Attributes map[string]any `json:"attributes,omitempty"`
	}
}

type putAssertionInput struct {
	AssertionPath
	Body struct {
		Attributes map[string]any `json:"attributes,omitempty"`
	}
}

type listRelationsInput struct {
	Pagination
	Project     []string `query:"project"`
	Entity      string   `query:"entity" maxLength:"64"`
	Direction   string   `query:"direction" enum:"out,in,both," default:"both"`
	Type        []string `query:"type"`
	Source      string   `query:"source" maxLength:"64"`
	Evidence    []string `query:"evidence" enum:"declared,structural,observed,inferred"`
	IncludeDead bool     `query:"include_removed"`
}

type unresolvedOutput struct {
	Body []catalog.UnresolvedRef
}

// RedactRef hides an endpoint in a project the caller cannot read (ADR-004).
func RedactRef(r catalog.EntityRef) catalog.EntityRef {
	h := sha256.Sum256([]byte("deed/redact/v1|" + r.ID))
	return catalog.EntityRef{ID: "redacted:" + hex.EncodeToString(h[:8]), ProjectID: "restricted", Kind: "external", Name: "restricted", Redacted: true}
}

// RedactRelation redacts unreadable endpoints and drops attributes of half-visible relations.
func RedactRelation(p *authn.Principal, r *catalog.Relation) *catalog.Relation {
	fromOK, toOK := authz.Can(p, r.From.ProjectID, authz.Viewer), authz.Can(p, r.To.ProjectID, authz.Viewer)
	if fromOK && toOK {
		return r
	}
	c := *r
	if !fromOK {
		c.From = RedactRef(r.From)
	}
	if !toOK {
		c.To = RedactRef(r.To)
	}
	c.Attributes = map[string]any{}
	c.Assertions = nil
	return &c
}

func (d *Deps) assertRelation(ctx context.Context, src *catalog.Source, it *catalog.RelationItem, o catalog.ApplyOpts, action string) (*catalog.RelationResult, error) {
	o.Actor = Actor(ctx)
	var res *catalog.RelationResult
	err := d.Store.W().TxNamed(ctx, "api."+action, func(tx *store.Tx) error {
		var err error
		if res, err = d.Catalog.ApplyRelation(ctx, tx, src, it, o); err != nil {
			return err
		}
		if res.Action == catalog.ActionUnresolved {
			return apperr.New(apperr.CodeNotFound, "%s", res.Detail)
		}
		if res.Action == catalog.ActionNotFound || res.Action == catalog.ActionUnchanged {
			return nil
		}
		_, err = d.Audit.Append(ctx, tx, audit.Event{Actor: o.Actor, ProjectID: src.ProjectID, Action: "relation." + action,
			Target: audit.Target{Type: "relation", ID: res.RelationID}, Details: map[string]any{"source": src.Name, "type": it.Type, "result": res.Action}})
		return err
	})
	return res, err
}

func (d *Deps) readableRelation(ctx context.Context, id string) (*catalog.Relation, error) {
	r, err := d.Catalog.GetRelation(ctx, id)
	if err != nil {
		return nil, err
	}
	p := PrincipalFrom(ctx)
	if !authz.Can(p, r.From.ProjectID, authz.Viewer) && !authz.Can(p, r.To.ProjectID, authz.Viewer) {
		return nil, catalog.ErrRelationNotFound
	}
	return RedactRelation(p, r), nil
}

func registerRelations(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-relations", Method: http.MethodGet, Path: "/relations", Tags: []string{"relations"},
		Summary: "Filter relationships (accessible non-graph view of the map)", Security: Secured},
		func(ctx context.Context, in *listRelationsInput) (*relationPageOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-relations", err)
			}
			readable, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-relations", err)
			}
			f := catalog.RelationFilter{ProjectIDs: readable, EntityID: in.Entity, Direction: in.Direction, Types: splitList(in.Type),
				SourceID: in.Source, Evidence: in.Evidence, IncludeDead: in.IncludeDead, Limit: in.Limit, Cursor: in.Cursor}
			if ps := splitList(in.Project); len(ps) > 0 {
				var ids []string
				for _, ref := range ps {
					pr, err := d.ResolveProject(ctx, ref, authz.Viewer)
					if err != nil {
						return nil, d.Fail("list-relations", err)
					}
					ids = append(ids, pr.ID)
				}
				f.ProjectIDs = ids
			}
			page, err := d.Catalog.ListRelations(ctx, f)
			if err != nil {
				return nil, d.Fail("list-relations", err)
			}
			for i, r := range page.Items {
				page.Items[i] = RedactRelation(p, r)
			}
			return &relationPageOutput{Body: page}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-relation", Method: http.MethodPost, Path: "/relations", Tags: []string{"relations"},
		Summary: "Declare a relationship", Description: "Records the declaring source's assertion. Fails with 409 when that source already " +
			"asserts the same relationship, 404 when an endpoint does not exist and 422 when endpoint kinds or project bridges forbid it.",
		Security: Secured, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createRelationInput) (*relationOutput, error) {
			if _, err := requireUser(ctx); err != nil {
				return nil, d.Fail("create-relation", err)
			}
			projectRef := in.Body.Project
			if projectRef == "" {
				if in.Body.From.ID == "" {
					return nil, d.Fail("create-relation", apperr.New(apperr.CodeInvalid, "project is required when from is a natural key"))
				}
				e, err := d.readableEntity(ctx, in.Body.From.ID, authz.Viewer)
				if err != nil {
					return nil, d.Fail("create-relation", err)
				}
				projectRef = e.Metadata.ProjectID
			}
			pr, err := d.ResolveProject(ctx, projectRef, authz.Editor)
			if err != nil {
				return nil, d.Fail("create-relation", err)
			}
			src, err := d.declaringSource(ctx, pr.ID, in.Body.Source)
			if err != nil {
				return nil, d.Fail("create-relation", err)
			}
			// The other endpoint may be in another project: the caller must be able to read it.
			for _, ref := range []catalog.Ref{in.Body.From, in.Body.To} {
				if ref.Project != "" {
					if _, err := d.ResolveProject(ctx, ref.Project, authz.Viewer); err != nil {
						return nil, d.Fail("create-relation", err)
					}
				}
				if ref.ID != "" {
					if _, err := d.readableEntity(ctx, ref.ID, authz.Viewer); err != nil {
						return nil, d.Fail("create-relation", err)
					}
				}
			}
			it := &catalog.RelationItem{Type: in.Body.Type, From: in.Body.From, To: in.Body.To, Attributes: in.Body.Attributes}
			res, err := d.assertRelation(ctx, src, it, catalog.ApplyOpts{CreateOnly: true}, "declared")
			if err != nil {
				return nil, d.Fail("create-relation", err)
			}
			r, err := d.readableRelation(ctx, res.RelationID)
			return &relationOutput{Body: r}, d.Fail("create-relation", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-relation", Method: http.MethodGet, Path: "/relations/{id}", Tags: []string{"relations"},
		Summary: "Get a relationship with every source's assertion and evidence", Security: Secured},
		func(ctx context.Context, in *RelationPath) (*relationOutput, error) {
			r, err := d.readableRelation(ctx, in.ID)
			return &relationOutput{Body: r}, d.Fail("get-relation", err)
		})
	huma.Register(api, huma.Operation{OperationID: "put-relation-assertion", Method: http.MethodPut, Path: "/relations/{id}/assertions/{source}",
		Tags: []string{"relations"}, Summary: "Replace (or add) one source's assertion of a relationship", Security: Secured},
		func(ctx context.Context, in *putAssertionInput) (*relationOutput, error) {
			r, err := d.Catalog.GetRelation(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("put-relation-assertion", err)
			}
			src, err := d.declaringSource(ctx, r.ProjectID, in.Source)
			if err != nil {
				return nil, d.Fail("put-relation-assertion", err)
			}
			it := &catalog.RelationItem{Type: r.Type, From: catalog.Ref{ID: r.From.ID}, To: catalog.Ref{ID: r.To.ID}, Attributes: in.Body.Attributes}
			if _, err := d.assertRelation(ctx, src, it, catalog.ApplyOpts{}, "declared"); err != nil {
				return nil, d.Fail("put-relation-assertion", err)
			}
			out, err := d.readableRelation(ctx, in.ID)
			return &relationOutput{Body: out}, d.Fail("put-relation-assertion", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-relation-assertion", Method: http.MethodDelete, Path: "/relations/{id}/assertions/{source}",
		Tags: []string{"relations"}, Summary: "Retract one source's assertion", Description: "The relationship stays while other sources assert it.",
		Security: Secured, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *AssertionPath) (*noContent, error) {
			r, err := d.Catalog.GetRelation(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("delete-relation-assertion", err)
			}
			src, err := d.declaringSource(ctx, r.ProjectID, in.Source)
			if err != nil {
				return nil, d.Fail("delete-relation-assertion", err)
			}
			it := &catalog.RelationItem{Type: r.Type, From: catalog.Ref{ID: r.From.ID}, To: catalog.Ref{ID: r.To.ID}, Delete: true}
			res, err := d.assertRelation(ctx, src, it, catalog.ApplyOpts{}, "retracted")
			if err != nil {
				return nil, d.Fail("delete-relation-assertion", err)
			}
			if res.Action == catalog.ActionNotFound {
				return nil, d.Fail("delete-relation-assertion", apperr.New(apperr.CodeNotFound, "source %q does not assert this relationship", src.Name))
			}
			return nil, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-unresolved-relations", Method: http.MethodGet, Path: "/relations/unresolved",
		Tags: []string{"relations"}, Summary: "Relationship endpoints sources referenced that do not exist (missing targets)", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*unresolvedOutput, error) {
			readable, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-unresolved-relations", err)
			}
			u, err := d.Catalog.Unresolved(ctx, readable, 500)
			return &unresolvedOutput{Body: u}, d.Fail("list-unresolved-relations", err)
		})
}

type auditInput struct {
	Project    string `query:"project" maxLength:"64"`
	TargetType string `query:"target_type" maxLength:"64"`
	TargetID   string `query:"target_id" maxLength:"128"`
	Actor      string `query:"actor" maxLength:"64"`
	Action     string `query:"action" maxLength:"64" doc:"Action prefix, e.g. auth. or entity."`
	Outcome    string `query:"outcome" enum:"success,failure,denied,"`
	Before     int64  `query:"before" doc:"Return records with seq below this."`
	Limit      int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
}

type auditVerifyOutput struct {
	Body audit.VerifyResult
}

func registerAudit(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-audit", Method: http.MethodGet, Path: "/audit", Tags: []string{"audit"},
		Summary: "Security audit records", Description: "Installation owners, admins and auditors see everything; project admins see their " +
			"projects' records.", Security: Secured},
		func(ctx context.Context, in *auditInput) (*auditListOutput, error) {
			p, err := requireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-audit", err)
			}
			f := audit.Filter{TargetType: in.TargetType, TargetID: in.TargetID, ActorID: in.Actor, Action: in.Action, Outcome: in.Outcome,
				BeforeSeq: in.Before, Limit: in.Limit}
			if in.Project != "" {
				pr, err := d.ResolveProject(ctx, in.Project, authz.Admin)
				if err != nil {
					return nil, d.Fail("list-audit", err)
				}
				f.ProjectIDs = []string{pr.ID}
			} else if !authz.CanReadAllAudit(p) {
				ids, err := d.Catalog.ProjectIDs(ctx)
				if err != nil {
					return nil, d.Fail("list-audit", err)
				}
				f.ProjectIDs = []string{}
				for _, id := range ids {
					if authz.Can(p, id, authz.Admin) {
						f.ProjectIDs = append(f.ProjectIDs, id)
					}
				}
			}
			recs, err := audit.List(ctx, d.Store.R(), f)
			return &auditListOutput{Body: recs}, d.Fail("list-audit", err)
		})
	huma.Register(api, huma.Operation{OperationID: "verify-audit", Method: http.MethodPost, Path: "/audit/verify", Tags: []string{"audit"},
		Summary: "Verify the audit hash chain (tamper-evident, not tamper-proof)", Security: Secured},
		func(ctx context.Context, _ *struct{}) (*auditVerifyOutput, error) {
			if !authz.CanReadAllAudit(PrincipalFrom(ctx)) {
				return nil, d.Fail("verify-audit", apperr.New(apperr.CodeForbidden, "installation owner, admin or auditor required"))
			}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			res, err := audit.Verify(cctx, d.Store.R())
			return &auditVerifyOutput{Body: res}, d.Fail("verify-audit", err)
		})
}
