package versions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
)

// Check is one entity's version status.
type Check struct {
	EntityID     string         `json:"entity_id"`
	ProjectID    string         `json:"project_id"`
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	Declared     string         `json:"declared,omitempty"`
	Observed     string         `json:"observed,omitempty"`
	Product      string         `json:"product,omitempty"`
	Cycle        string         `json:"cycle,omitempty"`
	Latest       string         `json:"latest,omitempty"`
	LatestSource string         `json:"latest_source,omitempty" enum:"catalog_release,eol_api,"`
	EOLDate      string         `json:"eol_date,omitempty"`
	SupportUntil string         `json:"support_until,omitempty"`
	Status       string         `json:"status" enum:"current,update_available,eol,unknown,not_applicable"`
	Evidence     map[string]any `json:"evidence"`
	CheckedAt    time.Time      `json:"checked_at"`
}

// API registers GET /versions and POST /versions/check.
type API struct {
	Checker *Checker
	Enabled bool
}

type listInput struct {
	Status  string `query:"status" maxLength:"64" doc:"Comma-separated statuses."`
	Project string `query:"project" maxLength:"64"`
	Entity  string `query:"entity" maxLength:"64" doc:"Only this entity's check."`
	Limit   int    `query:"limit" minimum:"1" maximum:"1000" default:"200"`
}

type listOutput struct {
	Body struct {
		Enabled bool    `json:"enabled"`
		EOL     bool    `json:"eol_source"`
		Items   []Check `json:"items"`
	}
}

type checkOutput struct {
	Body struct {
		Checked int `json:"checked"`
	}
}

// Register adds the endpoints.
func (a *API) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	huma.Register(api, huma.Operation{OperationID: "list-version-checks", Method: http.MethodGet, Path: "/versions", Tags: []string{"versions"},
		Summary: "Version status of entities in readable projects", Security: sec},
		func(ctx context.Context, in *listInput) (*listOutput, error) {
			readable, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-version-checks", err)
			}
			if in.Project != "" {
				p, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
				if err != nil {
					return nil, d.Fail("list-version-checks", err)
				}
				readable = []string{p.ID}
			}
			out := &listOutput{}
			out.Body.Enabled, out.Body.EOL, out.Body.Items = a.Enabled, a.Checker != nil && a.Checker.EOLURL != "", []Check{}
			// readable == nil means every project (installation roles); an empty list means none.
			if (readable != nil && len(readable) == 0) || a.Checker == nil {
				return out, nil
			}
			q := `SELECT v.entity_id, e.project_id, e.kind, e.name, e.version_declared, e.version_observed, v.product, v.cycle, v.latest, v.latest_source,
				v.eol_date, v.support_until, v.status, v.evidence_json, v.checked_at FROM version_checks v JOIN entities e ON e.id = v.entity_id
				WHERE e.tombstoned_at IS NULL`
			args := []any{}
			if readable != nil {
				q += ` AND e.project_id IN (?` + strings.Repeat(", ?", len(readable)-1) + `)`
				for _, p := range readable {
					args = append(args, p)
				}
			}
			if in.Entity != "" {
				q += ` AND v.entity_id = ?`
				args = append(args, in.Entity)
			}
			if in.Status != "" {
				st := strings.Split(in.Status, ",")
				q += ` AND v.status IN (?` + strings.Repeat(", ?", len(st)-1) + `)`
				for _, s := range st {
					args = append(args, strings.TrimSpace(s))
				}
			}
			q += ` ORDER BY CASE v.status WHEN 'eol' THEN 0 WHEN 'update_available' THEN 1 ELSE 2 END, e.kind, e.name LIMIT ?`
			args = append(args, in.Limit)
			rows, err := a.Checker.Store.R().QueryContext(ctx, q, args...)
			if err != nil {
				return nil, d.Fail("list-version-checks", err)
			}
			defer rows.Close()
			for rows.Next() {
				var c Check
				var ev string
				var at int64
				if err := rows.Scan(&c.EntityID, &c.ProjectID, &c.Kind, &c.Name, &c.Declared, &c.Observed, &c.Product, &c.Cycle, &c.Latest, &c.LatestSource,
					&c.EOLDate, &c.SupportUntil, &c.Status, &ev, &at); err != nil {
					return nil, d.Fail("list-version-checks", err)
				}
				_ = json.Unmarshal([]byte(ev), &c.Evidence)
				c.CheckedAt = clock.FromMicros(at)
				out.Body.Items = append(out.Body.Items, c)
			}
			return out, d.Fail("list-version-checks", rows.Err())
		})
	huma.Register(api, huma.Operation{OperationID: "run-version-check", Method: http.MethodPost, Path: "/versions/check", Tags: []string{"versions"},
		Summary: "Re-check versions now (owner or admin)", Security: sec},
		func(ctx context.Context, _ *struct{}) (*checkOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("run-version-check", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("run-version-check", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			if a.Checker == nil {
				return nil, d.Fail("run-version-check", apperr.New(apperr.CodeInvalid, "version checks are disabled"))
			}
			n, err := a.Checker.Run(ctx)
			out := &checkOutput{}
			out.Body.Checked = n
			return out, d.Fail("run-version-check", err)
		})
}
