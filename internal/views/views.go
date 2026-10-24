// Package views stores saved inventory filters, map views and layouts server-side (ADR-011). A view is private to
// its owner unless shared with a project, in which case every project member can open it and project editors can
// update it.
package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// View is a saved view.
type View struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind" enum:"inventory,map,dashboard"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ProjectID       string          `json:"project_id,omitempty"`
	OwnerID         string          `json:"owner_id"`
	Shared          bool            `json:"shared"`
	State           json.RawMessage `json:"state" doc:"Filters, grouping, columns, edge styles and options (client-defined, ≤ 256 KiB)."`
	Layout          json.RawMessage `json:"layout" doc:"Node positions keyed by node ID (map views)."`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ResourceVersion int64           `json:"resource_version"`
	Editable        bool            `json:"editable"`
}

// Service stores views.
type Service struct {
	Store *store.Store
}

const cols = `id, kind, name, description, COALESCE(project_id, ''), owner_id, shared, state_json, layout_json, created_at, updated_at, version`

func scan(r interface{ Scan(...any) error }) (*View, error) {
	var v View
	var st, lay string
	var c, u int64
	if err := r.Scan(&v.ID, &v.Kind, &v.Name, &v.Description, &v.ProjectID, &v.OwnerID, &v.Shared, &st, &lay, &c, &u, &v.ResourceVersion); err != nil {
		return nil, err
	}
	v.State, v.Layout = json.RawMessage(st), json.RawMessage(lay)
	v.CreatedAt, v.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &v, nil
}

func visible(p *authn.Principal, v *View) bool {
	return v.OwnerID == p.UserID || (v.Shared && v.ProjectID != "" && authz.Can(p, v.ProjectID, authz.Viewer))
}

func editable(p *authn.Principal, v *View) bool {
	return v.OwnerID == p.UserID || (v.Shared && v.ProjectID != "" && authz.Can(p, v.ProjectID, authz.Editor))
}

// Input creates or updates a view.
type Input struct {
	Kind        string          `json:"kind,omitempty" enum:"inventory,map,dashboard,"`
	Name        string          `json:"name,omitempty" maxLength:"100"`
	Description string          `json:"description,omitempty" maxLength:"2000"`
	Project     string          `json:"project,omitempty" maxLength:"64"`
	Shared      bool            `json:"shared,omitempty"`
	State       json.RawMessage `json:"state,omitempty"`
	Layout      json.RawMessage `json:"layout,omitempty"`
}

type listInput struct {
	Kind    string `query:"kind" enum:"inventory,map,dashboard,"`
	Project string `query:"project" maxLength:"64"`
}

type listOutput struct {
	Body []*View
}

type viewOutput struct {
	ETag string `header:"ETag"`
	Body *View
}

// ViewPath is a view in the URL.
type ViewPath struct {
	ID string `path:"id" maxLength:"64"`
}

type createInput struct {
	Body Input
}

type updateInput struct {
	ID string `path:"id" maxLength:"64"`
	httpapi.IfMatch
	Body Input
}

func checkJSON(name string, b json.RawMessage, max int) (string, error) {
	if len(b) == 0 {
		return "{}", nil
	}
	if len(b) > max {
		return "", apperr.New(apperr.CodeInvalid, "%s exceeds %d bytes", name, max)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", apperr.New(apperr.CodeInvalid, "%s must be a JSON object", name)
	}
	out, _ := json.Marshal(v)
	return string(out), nil
}

func (s *Service) get(ctx context.Context, id string) (*View, error) {
	v, err := scan(s.Store.R().QueryRowContext(ctx, `SELECT `+cols+` FROM saved_views WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.New(apperr.CodeNotFound, "view not found")
	}
	return v, err
}

// Register adds the endpoints.
func (s *Service) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	resolve := func(ctx context.Context, in *Input) (string, error) {
		if in.Project == "" {
			if in.Shared {
				return "", apperr.New(apperr.CodeInvalid, "shared views belong to a project")
			}
			return "", nil
		}
		pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
		if err != nil {
			return "", err
		}
		if in.Shared && !authz.Can(httpapi.PrincipalFrom(ctx), pr.ID, authz.Editor) {
			return "", apperr.New(apperr.CodeForbidden, "sharing a view needs the editor role in the project")
		}
		return pr.ID, nil
	}
	huma.Register(api, huma.Operation{OperationID: "list-views", Method: http.MethodGet, Path: "/views", Tags: []string{"views"},
		Summary: "Your views and views shared with your projects", Security: sec},
		func(ctx context.Context, in *listInput) (*listOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-views", err)
			}
			q := `SELECT ` + cols + ` FROM saved_views WHERE (owner_id = ? OR shared = 1)`
			args := []any{p.UserID}
			if in.Kind != "" {
				q += ` AND kind = ?`
				args = append(args, in.Kind)
			}
			rows, err := s.Store.R().QueryContext(ctx, q+` ORDER BY name`, args...)
			if err != nil {
				return nil, d.Fail("list-views", err)
			}
			defer rows.Close()
			out := []*View{}
			for rows.Next() {
				v, err := scan(rows)
				if err != nil {
					return nil, d.Fail("list-views", err)
				}
				if !visible(p, v) {
					continue
				}
				if in.Project != "" && !strings.EqualFold(in.Project, v.ProjectID) {
					if pr, err := d.Catalog.Project(ctx, in.Project); err != nil || pr.ID != v.ProjectID {
						continue
					}
				}
				v.Editable = editable(p, v)
				out = append(out, v)
			}
			return &listOutput{Body: out}, d.Fail("list-views", rows.Err())
		})
	huma.Register(api, huma.Operation{OperationID: "create-view", Method: http.MethodPost, Path: "/views", Tags: []string{"views"},
		Summary: "Save a view", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createInput) (*viewOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			if in.Body.Kind == "" || strings.TrimSpace(in.Body.Name) == "" {
				return nil, d.Fail("create-view", apperr.New(apperr.CodeInvalid, "kind and name are required"))
			}
			pid, err := resolve(ctx, &in.Body)
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			st, err := checkJSON("state", in.Body.State, 256<<10)
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			lay, err := checkJSON("layout", in.Body.Layout, 2<<20)
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			id := ids.New(ids.SavedView)
			err = s.Store.W().TxNamed(ctx, "views.create", func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO saved_views (id, kind, name, description, project_id, owner_id, shared, state_json, layout_json, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Body.Kind, strings.TrimSpace(in.Body.Name), in.Body.Description,
					sql.NullString{String: pid, Valid: pid != ""}, p.UserID, in.Body.Shared, st, lay, tx.NowMicros(), tx.NowMicros())
				return err
			})
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			v, err := s.get(ctx, id)
			if err != nil {
				return nil, d.Fail("create-view", err)
			}
			v.Editable = true
			return &viewOutput{ETag: httpapi.ETag(v.ResourceVersion), Body: v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-view", Method: http.MethodGet, Path: "/views/{id}", Tags: []string{"views"},
		Summary: "Open a view (shareable link target)", Security: sec},
		func(ctx context.Context, in *ViewPath) (*viewOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("get-view", err)
			}
			v, err := s.get(ctx, in.ID)
			if err != nil || !visible(p, v) {
				return nil, d.Fail("get-view", apperr.New(apperr.CodeNotFound, "view not found"))
			}
			v.Editable = editable(p, v)
			return &viewOutput{ETag: httpapi.ETag(v.ResourceVersion), Body: v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-view", Method: http.MethodPut, Path: "/views/{id}", Tags: []string{"views"},
		Summary: "Update a view", Security: sec},
		func(ctx context.Context, in *updateInput) (*viewOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			v, err := s.get(ctx, in.ID)
			if err != nil || !visible(p, v) {
				return nil, d.Fail("update-view", apperr.New(apperr.CodeNotFound, "view not found"))
			}
			if !editable(p, v) {
				return nil, d.Fail("update-view", apperr.New(apperr.CodeForbidden, "you cannot edit this view"))
			}
			ver, err := in.Version()
			if err != nil {
				return nil, err
			}
			if in.Body.Name == "" {
				in.Body.Name = v.Name
			}
			pid, err := resolve(ctx, &in.Body)
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			st, err := checkJSON("state", in.Body.State, 256<<10)
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			lay, err := checkJSON("layout", in.Body.Layout, 2<<20)
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			if len(in.Body.Layout) == 0 {
				lay = string(v.Layout)
			}
			err = s.Store.W().TxNamed(ctx, "views.update", func(tx *store.Tx) error {
				n, err := tx.ExecN(ctx, `UPDATE saved_views SET name = ?, description = ?, project_id = ?, shared = ?, state_json = ?, layout_json = ?,
					updated_at = ?, version = version + 1 WHERE id = ? AND (? = 0 OR version = ?)`, in.Body.Name, in.Body.Description,
					sql.NullString{String: pid, Valid: pid != ""}, in.Body.Shared, st, lay, tx.NowMicros(), in.ID, ver, ver)
				if err != nil {
					return err
				}
				if n == 0 {
					return apperr.New(apperr.CodePrecondition, "view was modified concurrently; reload it")
				}
				return nil
			})
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			v, err = s.get(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("update-view", err)
			}
			v.Editable = true
			return &viewOutput{ETag: httpapi.ETag(v.ResourceVersion), Body: v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-view", Method: http.MethodDelete, Path: "/views/{id}", Tags: []string{"views"},
		Summary: "Delete a view (owner)", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *ViewPath) (*struct{}, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("delete-view", err)
			}
			v, err := s.get(ctx, in.ID)
			if err != nil || v.OwnerID != p.UserID {
				return nil, d.Fail("delete-view", apperr.New(apperr.CodeNotFound, "view not found"))
			}
			return nil, d.Fail("delete-view", s.Store.W().TxNamed(ctx, "views.delete", func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `DELETE FROM saved_views WHERE id = ?`, in.ID)
				return err
			}))
		})
}
