// Package ops serves findings, alerts, silences, alert rules and the dashboard summary.
package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/alerts"
	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/events"
	"github.com/aurimasniekis/go-deed/internal/findings"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// API serves the operations endpoints.
type API struct {
	Store    *store.Store
	Findings *findings.Engine
	Alerts   *alerts.Engine
	Clock    clock.Clock
	// Extra contributes dashboard sections from other modules (telemetry, backups).
	Extra func(ctx context.Context, readable []string, installation bool) map[string]any
}

type findingsInput struct {
	Project  string   `query:"project" maxLength:"64"`
	Entity   string   `query:"entity" maxLength:"64"`
	Type     []string `query:"type,explode"`
	Severity []string `query:"severity,explode" enum:"critical,warning,info"`
	Class    []string `query:"class,explode" enum:"confirmed,warning,unknown,stale,not_applicable"`
	State    string   `query:"state" enum:"open,closed,all" default:"open"`
	Limit    int      `query:"limit" minimum:"1" maximum:"1000" default:"200"`
}

type findingsOutput struct {
	Body []findings.Finding
}

type findingOutput struct {
	Body *findings.Finding
}

// FindingPath is a finding in the URL.
type FindingPath struct {
	ID string `path:"id" maxLength:"64"`
}

const findingCols = `id, dedup_key, type, severity, class, COALESCE(project_id, ''), COALESCE(entity_id, ''), COALESCE(relation_id, ''), COALESCE(source_id, ''),
	title, message, evidence_json, state, acknowledged_at, COALESCE(acknowledged_by, ''), first_seen_at, last_seen_at, closed_at`

func scanFinding(r interface{ Scan(...any) error }) (*findings.Finding, error) {
	var f findings.Finding
	var ev string
	var ack, closed sql.NullInt64
	var first, last int64
	if err := r.Scan(&f.ID, &f.DedupKey, &f.Type, &f.Severity, &f.Class, &f.ProjectID, &f.EntityID, &f.RelationID, &f.SourceID, &f.Title, &f.Message, &ev,
		&f.State, &ack, &f.AcknowledgedBy, &first, &last, &closed); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(ev), &f.Evidence)
	if f.Evidence == nil {
		f.Evidence = map[string]any{}
	}
	f.FirstSeenAt, f.LastSeenAt = clock.FromMicros(first), clock.FromMicros(last)
	if ack.Valid {
		t := clock.FromMicros(ack.Int64)
		f.AcknowledgedAt = &t
	}
	if closed.Valid {
		t := clock.FromMicros(closed.Int64)
		f.ClosedAt = &t
	}
	return &f, nil
}

func visible(readable []string, project string) bool {
	return readable == nil || project == "" || slices.Contains(readable, project)
}

// List queries findings visible to the caller. Installation-level findings (no project) are shown to installation
// roles only.
func (a *API) List(ctx context.Context, readable []string, installation bool, in *findingsInput) ([]findings.Finding, error) {
	var conds []string
	var args []any
	if in.State != "all" {
		conds = append(conds, "state = ?")
		args = append(args, in.State)
	}
	if in.Entity != "" {
		conds = append(conds, "entity_id = ?")
		args = append(args, in.Entity)
	}
	add := func(col string, vals []string) {
		// Repeated parameters (explode) or one comma-separated value: both are accepted.
		var split []string
		for _, v := range vals {
			split = append(split, strings.Split(v, ",")...)
		}
		vals = split
		if len(vals) == 0 {
			return
		}
		conds = append(conds, col+" IN ("+strings.Repeat(",?", len(vals))[1:]+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	add("type", in.Type)
	add("severity", in.Severity)
	add("class", in.Class)
	// Visibility is part of the query: filtering after a LIMIT would hide visible findings behind invisible ones.
	if !installation {
		conds = append(conds, "project_id IS NOT NULL")
	}
	if readable != nil {
		if len(readable) == 0 {
			conds = append(conds, "project_id IS NULL")
		} else {
			conds = append(conds, "(project_id IS NULL OR project_id IN ("+strings.Repeat(",?", len(readable))[1:]+"))")
			for _, r := range readable {
				args = append(args, r)
			}
		}
	}
	if in.Project != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, in.Project)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := a.Store.R().QueryContext(ctx, `SELECT `+findingCols+` FROM findings`+where+`
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, last_seen_at DESC LIMIT ?`, append(args, in.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []findings.Finding{}
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

type alertsInput struct {
	State []string `query:"state,explode" enum:"pending,firing,suppressed,resolved"`
	Limit int      `query:"limit" minimum:"1" maximum:"500" default:"200"`
}

type alertsOutput struct {
	Body []alerts.Alert
}

type rulesOutput struct {
	Body []*alerts.Rule
}

type ruleOutput struct {
	Body *alerts.Rule
}

type createRuleInput struct {
	Body alerts.RuleInput
}

type updateRuleInput struct {
	ID string `path:"id" maxLength:"64"`
	httpapi.IfMatch
	Body alerts.RuleInput
}

type silencesOutput struct {
	Body []alerts.Silence
}

type silenceOutput struct {
	Body *alerts.Silence
}

type createSilenceInput struct {
	Body struct {
		Matcher alerts.Matcher `json:"matcher"`
		Comment string         `json:"comment" maxLength:"2000"`
		Hours   int            `json:"hours" minimum:"1" maximum:"2160"`
	}
}

// Dashboard is the summary behind the dashboard view.
type Dashboard struct {
	GeneratedAt    time.Time             `json:"generated_at"`
	Counts         *catalog.Counts       `json:"counts"`
	FindingCounts  map[string]int        `json:"finding_counts" doc:"Open findings by severity."`
	FindingsByType map[string]int        `json:"findings_by_type"`
	Sources        []SourceHealth        `json:"sources"`
	Expiring       []findings.Finding    `json:"expiring" doc:"Expiring or expired certificates and domains."`
	Unresolved     []findings.Finding    `json:"unresolved" doc:"Broken or unresolved relationships."`
	Versions       []findings.Finding    `json:"versions" doc:"Outdated or end-of-life versions with evidence."`
	Drift          []findings.Finding    `json:"drift"`
	Alerts         []alerts.Alert        `json:"alerts" doc:"Firing and pending alerts."`
	RecentChanges  []catalog.ChangeEvent `json:"recent_changes"`
	Outbox         *events.Status        `json:"outbox,omitempty"`
	Extra          map[string]any        `json:"extra"`
	SyntheticData  bool                  `json:"synthetic_data" doc:"True when a readable project contains synthetic demo data."`
}

// SourceHealth summarizes one source.
type SourceHealth struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id"`
	Name          string     `json:"name"`
	DisplayName   string     `json:"display_name"`
	Provider      string     `json:"provider"`
	Freshness     string     `json:"freshness"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Enabled       bool       `json:"enabled"`
}

type dashboardInput struct {
	Project []string `query:"project,explode"`
}

type dashboardOutput struct {
	Body *Dashboard
}

// Register adds the endpoints.
func (a *API) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	scope := func(ctx context.Context) ([]string, bool, error) {
		p, err := httpapi.RequireUser(ctx)
		if err != nil {
			return nil, false, err
		}
		r, err := d.Readable(ctx)
		return r, authz.CanReadAllAudit(p), err
	}
	huma.Register(api, huma.Operation{OperationID: "list-findings", Method: http.MethodGet, Path: "/findings", Tags: []string{"findings"},
		Summary: "Findings with severity and evidence class", Security: sec},
		func(ctx context.Context, in *findingsInput) (*findingsOutput, error) {
			r, inst, err := scope(ctx)
			if err != nil {
				return nil, d.Fail("list-findings", err)
			}
			if in.Project != "" {
				pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
				if err != nil {
					return nil, d.Fail("list-findings", err)
				}
				in.Project = pr.ID
			}
			out, err := a.List(ctx, r, inst, in)
			return &findingsOutput{Body: out}, d.Fail("list-findings", err)
		})
	huma.Register(api, huma.Operation{OperationID: "evaluate-findings", Method: http.MethodPost, Path: "/findings/evaluate", Tags: []string{"findings"},
		Summary: "Re-evaluate findings and alerts now (owner or admin)", Description: "Findings are otherwise evaluated every two minutes.",
		Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("evaluate-findings", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("evaluate-findings", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			return nil, d.Fail("evaluate-findings", a.Findings.Evaluate(ctx))
		})
	huma.Register(api, huma.Operation{OperationID: "get-finding", Method: http.MethodGet, Path: "/findings/{id}", Tags: []string{"findings"},
		Summary: "Get a finding", Security: sec},
		func(ctx context.Context, in *FindingPath) (*findingOutput, error) {
			r, inst, err := scope(ctx)
			if err != nil {
				return nil, d.Fail("get-finding", err)
			}
			f, err := scanFinding(a.Store.R().QueryRowContext(ctx, `SELECT `+findingCols+` FROM findings WHERE id = ?`, in.ID))
			if errors.Is(err, sql.ErrNoRows) || (err == nil && (!visible(r, f.ProjectID) || (f.ProjectID == "" && !inst))) {
				return nil, d.Fail("get-finding", apperr.New(apperr.CodeNotFound, "finding not found"))
			}
			return &findingOutput{Body: f}, d.Fail("get-finding", err)
		})
	huma.Register(api, huma.Operation{OperationID: "acknowledge-finding", Method: http.MethodPost, Path: "/findings/{id}/acknowledge", Tags: []string{"findings"},
		Summary: "Acknowledge a finding (editor)", Security: sec},
		func(ctx context.Context, in *FindingPath) (*findingOutput, error) {
			p := httpapi.PrincipalFrom(ctx)
			f, err := scanFinding(a.Store.R().QueryRowContext(ctx, `SELECT `+findingCols+` FROM findings WHERE id = ?`, in.ID))
			if err != nil {
				return nil, d.Fail("acknowledge-finding", apperr.New(apperr.CodeNotFound, "finding not found"))
			}
			if (f.ProjectID == "" && !authz.CanAdminister(p)) || (f.ProjectID != "" && !authz.Can(p, f.ProjectID, authz.Editor)) {
				return nil, d.Fail("acknowledge-finding", apperr.New(apperr.CodeForbidden, "editor role required"))
			}
			err = a.Store.W().TxNamed(ctx, "findings.ack", func(tx *store.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE findings SET acknowledged_at = ?, acknowledged_by = ? WHERE id = ?`, tx.NowMicros(), p.UserID, in.ID); err != nil {
					return err
				}
				_, err := d.Audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), ProjectID: f.ProjectID, Action: "finding.acknowledged",
					Target: audit.Target{Type: "finding", ID: in.ID}})
				return err
			})
			if err != nil {
				return nil, d.Fail("acknowledge-finding", err)
			}
			f, err = scanFinding(a.Store.R().QueryRowContext(ctx, `SELECT `+findingCols+` FROM findings WHERE id = ?`, in.ID))
			return &findingOutput{Body: f}, d.Fail("acknowledge-finding", err)
		})
	huma.Register(api, huma.Operation{OperationID: "list-alerts", Method: http.MethodGet, Path: "/alerts", Tags: []string{"alerts"},
		Summary: "Alerts with lifecycle and delivery state", Security: sec},
		func(ctx context.Context, in *alertsInput) (*alertsOutput, error) {
			r, inst, err := scope(ctx)
			if err != nil {
				return nil, d.Fail("list-alerts", err)
			}
			list, err := a.Alerts.Alerts(ctx, in.State, r, in.Limit)
			if err != nil {
				return nil, d.Fail("list-alerts", err)
			}
			out := []alerts.Alert{}
			for _, x := range list {
				if x.ProjectID == "" && !inst {
					continue
				}
				out = append(out, x)
			}
			return &alertsOutput{Body: out}, nil
		})
	adminOnly := func(ctx context.Context) error {
		if !authz.CanAdminister(httpapi.PrincipalFrom(ctx)) {
			return apperr.New(apperr.CodeForbidden, "installation owner or admin required")
		}
		return nil
	}
	huma.Register(api, huma.Operation{OperationID: "list-alert-rules", Method: http.MethodGet, Path: "/alert-rules", Tags: []string{"alerts"},
		Summary: "Alert rules", Security: sec},
		func(ctx context.Context, _ *struct{}) (*rulesOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-alert-rules", err)
			}
			r, err := a.Alerts.Rules(ctx)
			return &rulesOutput{Body: r}, d.Fail("list-alert-rules", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-alert-rule", Method: http.MethodPost, Path: "/alert-rules", Tags: []string{"alerts"},
		Summary: "Create an alert rule (installation admin)", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createRuleInput) (*ruleOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("create-alert-rule", err)
			}
			r, err := a.Alerts.SaveRule(ctx, httpapi.Actor(ctx), "", in.Body, 0)
			return &ruleOutput{Body: r}, d.Fail("create-alert-rule", err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-alert-rule", Method: http.MethodPut, Path: "/alert-rules/{id}", Tags: []string{"alerts"},
		Summary: "Update an alert rule (installation admin)", Security: sec},
		func(ctx context.Context, in *updateRuleInput) (*ruleOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("update-alert-rule", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			r, err := a.Alerts.SaveRule(ctx, httpapi.Actor(ctx), in.ID, in.Body, v)
			return &ruleOutput{Body: r}, d.Fail("update-alert-rule", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-alert-rule", Method: http.MethodDelete, Path: "/alert-rules/{id}", Tags: []string{"alerts"},
		Summary: "Delete a custom alert rule", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *FindingPath) (*struct{}, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("delete-alert-rule", err)
			}
			return nil, d.Fail("delete-alert-rule", a.Alerts.DeleteRule(ctx, httpapi.Actor(ctx), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "list-silences", Method: http.MethodGet, Path: "/silences", Tags: []string{"alerts"},
		Summary: "Silences", Security: sec},
		func(ctx context.Context, _ *struct{}) (*silencesOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-silences", err)
			}
			s, err := a.Alerts.Silences(ctx)
			return &silencesOutput{Body: s}, d.Fail("list-silences", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-silence", Method: http.MethodPost, Path: "/silences", Tags: []string{"alerts"},
		Summary: "Silence matching alerts for a while (installation admin)", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createSilenceInput) (*silenceOutput, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("create-silence", err)
			}
			now := a.Clock.Now()
			s, err := a.Alerts.CreateSilence(ctx, httpapi.Actor(ctx), in.Body.Matcher, in.Body.Comment, now, now.Add(time.Duration(in.Body.Hours)*time.Hour))
			return &silenceOutput{Body: s}, d.Fail("create-silence", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-silence", Method: http.MethodDelete, Path: "/silences/{id}", Tags: []string{"alerts"},
		Summary: "End a silence", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *FindingPath) (*struct{}, error) {
			if err := adminOnly(ctx); err != nil {
				return nil, d.Fail("delete-silence", err)
			}
			return nil, d.Fail("delete-silence", a.Alerts.DeleteSilence(ctx, httpapi.Actor(ctx), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "get-dashboard", Method: http.MethodGet, Path: "/dashboard", Tags: []string{"dashboard"},
		Summary: "Dashboard summary", Description: "Inventory by kind, environment and owner; source freshness; expiring certificates; unresolved " +
			"relationships; outdated versions with evidence; drift; alerts; recent changes; and operational summaries contributed by modules.", Security: sec},
		func(ctx context.Context, in *dashboardInput) (*dashboardOutput, error) {
			r, inst, err := scope(ctx)
			if err != nil {
				return nil, d.Fail("get-dashboard", err)
			}
			if len(in.Project) > 0 {
				var ids []string
				for _, ref := range in.Project {
					pr, err := d.ResolveProject(ctx, ref, authz.Viewer)
					if err != nil {
						return nil, d.Fail("get-dashboard", err)
					}
					ids = append(ids, pr.ID)
				}
				r = ids
			}
			out, err := a.dashboard(ctx, d, r, inst)
			return &dashboardOutput{Body: out}, d.Fail("get-dashboard", err)
		})
}

func (a *API) dashboard(ctx context.Context, d *httpapi.Deps, readable []string, inst bool) (*Dashboard, error) {
	out := &Dashboard{GeneratedAt: a.Clock.Now(), FindingCounts: map[string]int{}, FindingsByType: map[string]int{}, Sources: []SourceHealth{}, Extra: map[string]any{}}
	var err error
	if out.Counts, err = d.Catalog.CountEntities(ctx, readable); err != nil {
		return nil, err
	}
	all, err := a.List(ctx, readable, inst, &findingsInput{State: "open", Limit: 1000})
	if err != nil {
		return nil, err
	}
	// Counts come from the whole table (the list above only feeds the samples below).
	vis, vargs := "1", []any{}
	if readable != nil {
		vis = "project_id IN (''" + strings.Repeat(",?", len(readable)) + ")"
		for _, r := range readable {
			vargs = append(vargs, r)
		}
	}
	if inst {
		vis = "(" + vis + " OR project_id IS NULL)"
	} else {
		vis = "(" + vis + " AND project_id IS NOT NULL)"
	}
	crows, err := a.Store.R().QueryContext(ctx, `SELECT severity, type, COUNT(*) FROM findings WHERE state = 'open' AND `+vis+` GROUP BY severity, type`, vargs...)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var sev, typ string
		var n int
		if err := crows.Scan(&sev, &typ, &n); err != nil {
			crows.Close()
			return nil, err
		}
		out.FindingCounts[sev] += n
		out.FindingsByType[typ] += n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	for _, f := range all {
		switch f.Type {
		case findings.TypeCertExpiring, findings.TypeCertExpired, findings.TypeDomainExpiring:
			if len(out.Expiring) < 20 {
				out.Expiring = append(out.Expiring, f)
			}
		case findings.TypeMissingTarget:
			if len(out.Unresolved) < 20 {
				out.Unresolved = append(out.Unresolved, f)
			}
		case findings.TypeVersionAvailable, findings.TypeVersionEOL:
			if len(out.Versions) < 20 {
				out.Versions = append(out.Versions, f)
			}
		case findings.TypeDrift, findings.TypeConflict:
			if len(out.Drift) < 20 {
				out.Drift = append(out.Drift, f)
			}
		}
	}
	for _, l := range []*[]findings.Finding{&out.Expiring, &out.Unresolved, &out.Versions, &out.Drift} {
		if *l == nil {
			*l = []findings.Finding{}
		}
	}
	srcs, err := d.Catalog.Sources(ctx, readable)
	if err != nil {
		return nil, err
	}
	for _, s := range srcs {
		if s.System {
			continue
		}
		out.Sources = append(out.Sources, SourceHealth{ID: s.ID, ProjectID: s.ProjectID, Name: s.Name, DisplayName: s.DisplayName, Provider: s.Provider,
			Freshness: s.Freshness, LastSuccessAt: s.LastSuccessAt, LastError: s.LastError, Enabled: s.Enabled})
	}
	al, err := a.Alerts.Alerts(ctx, []string{"firing", "pending", "suppressed"}, readable, 50)
	if err != nil {
		return nil, err
	}
	out.Alerts = []alerts.Alert{}
	for _, x := range al {
		if x.ProjectID == "" && !inst {
			continue
		}
		out.Alerts = append(out.Alerts, x)
	}
	if out.RecentChanges, err = d.Catalog.Changes(ctx, catalog.ChangeFilter{ProjectIDs: readable, Limit: 25}); err != nil {
		return nil, err
	}
	if inst {
		st, err := events.GetStatus(ctx, a.Store.R(), a.Clock.Now())
		if err == nil {
			out.Outbox = &st
		}
	}
	projects, err := d.Catalog.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if p.Synthetic && (readable == nil || slices.Contains(readable, p.ID)) {
			out.SyntheticData = true
		}
	}
	if a.Extra != nil {
		out.Extra = a.Extra(ctx, readable, inst)
	}
	return out, nil
}
