// Package alerts turns findings into alerts (ADR-007): rules select findings, alerts move pending → firing →
// resolved with deduplication (one alert per rule and finding), a for-duration, cooldown between re-notifications,
// silences that suppress delivery, and evidence copied from the finding. Notifications are outbox events
// (evt.deed.alert.alert.raised|resolved) delivered to Pulse with retries; Pulse rules or webhooks fan them out.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Matcher selects findings.
type Matcher struct {
	Types       []string `json:"types,omitempty"`
	MinSeverity string   `json:"min_severity,omitempty" enum:"critical,warning,info,"`
	Classes     []string `json:"classes,omitempty"`
	Projects    []string `json:"projects,omitempty"`
}

var sevRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

func (m Matcher) match(typ, sev, class, project string) bool {
	if len(m.Types) > 0 && !slices.Contains(m.Types, typ) {
		return false
	}
	if m.MinSeverity != "" && sevRank[sev] < sevRank[m.MinSeverity] {
		return false
	}
	if len(m.Classes) > 0 && !slices.Contains(m.Classes, class) {
		return false
	}
	if len(m.Projects) > 0 && !slices.Contains(m.Projects, project) {
		return false
	}
	return true
}

// Rule is an alert rule.
type Rule struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Enabled         bool      `json:"enabled"`
	Matcher         Matcher   `json:"matcher"`
	ForSeconds      int       `json:"for_seconds"`
	CooldownSeconds int       `json:"cooldown_seconds"`
	Severity        string    `json:"severity,omitempty" doc:"Overrides the finding's severity when set."`
	Builtin         bool      `json:"builtin"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

// Alert is an alert instance.
type Alert struct {
	ID             string         `json:"id"`
	RuleID         string         `json:"rule_id"`
	Rule           string         `json:"rule"`
	FindingID      string         `json:"finding_id"`
	ProjectID      string         `json:"project_id,omitempty"`
	State          string         `json:"state"`
	Severity       string         `json:"severity"`
	Title          string         `json:"title"`
	Evidence       map[string]any `json:"evidence"`
	StartedAt      time.Time      `json:"started_at"`
	FiredAt        *time.Time     `json:"fired_at,omitempty"`
	ResolvedAt     *time.Time     `json:"resolved_at,omitempty"`
	LastNotifiedAt *time.Time     `json:"last_notified_at,omitempty"`
	Notifications  int            `json:"notifications"`
	SilencedBy     string         `json:"silenced_by,omitempty"`
	Delivery       string         `json:"delivery" doc:"pending, published, retrying or dead (last notification's outbox state); empty before any notification."`
}

// Silence suppresses notifications of matching alerts until it ends.
type Silence struct {
	ID        string    `json:"id"`
	Matcher   Matcher   `json:"matcher"`
	Comment   string    `json:"comment"`
	CreatedBy string    `json:"created_by"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Engine evaluates alert rules.
type Engine struct {
	Store   *store.Store
	Audit   *audit.Writer
	Clock   clock.Clock
	Metrics *metrics.Metrics
}

// Builtins are created on first start (they can be disabled, not deleted).
var Builtins = []Rule{
	{Name: "critical-findings", Description: "Every critical finding.", Matcher: Matcher{MinSeverity: "critical"}, CooldownSeconds: 6 * 3600},
	{Name: "certificates", Description: "Expiring or expired certificates and domains.", Matcher: Matcher{Types: []string{"certificate_expiring",
		"certificate_expired", "domain_expiring"}}, CooldownSeconds: 24 * 3600},
	{Name: "ingestion", Description: "Failing or stale sources and unreachable integrations.", Matcher: Matcher{Types: []string{"ingestion_failed",
		"stale_source", "integration_unreachable"}}, ForSeconds: 600, CooldownSeconds: 6 * 3600},
	{Name: "security", Description: "Break-glass sign-ins.", Matcher: Matcher{Types: []string{"breakglass_used"}}, CooldownSeconds: 3600},
	{Name: "event-flow", Description: "Declared Pulse connections not observed.", Matcher: Matcher{Types: []string{"expected_connection_unseen"}},
		ForSeconds: 1800, CooldownSeconds: 12 * 3600},
}

// EnsureBuiltins inserts missing built-in rules.
func (e *Engine) EnsureBuiltins(ctx context.Context) error {
	return e.Store.W().TxNamed(ctx, "alerts.builtins", func(tx *store.Tx) error {
		for _, r := range Builtins {
			m, _ := json.Marshal(r.Matcher)
			if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO alert_rules (id, name, description, matcher_json, for_seconds, cooldown_seconds, builtin, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, ids.New(ids.AlertRule), r.Name, r.Description, string(m), r.ForSeconds, r.CooldownSeconds,
				tx.NowMicros(), tx.NowMicros()); err != nil {
				return err
			}
		}
		return nil
	})
}

const ruleCols = `id, name, description, enabled, matcher_json, for_seconds, cooldown_seconds, severity, builtin, created_at, updated_at, version`

func scanRule(r interface{ Scan(...any) error }) (*Rule, error) {
	var x Rule
	var m string
	var c, u int64
	if err := r.Scan(&x.ID, &x.Name, &x.Description, &x.Enabled, &m, &x.ForSeconds, &x.CooldownSeconds, &x.Severity, &x.Builtin, &c, &u, &x.ResourceVersion); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(m), &x.Matcher)
	x.CreatedAt, x.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &x, nil
}

// Rules lists rules.
func (e *Engine) Rules(ctx context.Context) ([]*Rule, error) {
	rows, err := e.Store.R().QueryContext(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func silenced(sil []Silence, now time.Time, typ, sev, class, project string) string {
	for _, s := range sil {
		if now.Before(s.StartsAt) || !now.Before(s.EndsAt) {
			continue
		}
		if s.Matcher.match(typ, sev, class, project) {
			return s.ID
		}
	}
	return ""
}

type openFinding struct {
	id, key, typ, sev, class, project, title, evidence string
}

// Evaluate advances every alert against the current findings.
func (e *Engine) Evaluate(ctx context.Context) error {
	rules, err := e.Rules(ctx)
	if err != nil {
		return err
	}
	sil, err := e.Silences(ctx)
	if err != nil {
		return err
	}
	return e.Store.W().TxNamed(ctx, "alerts.evaluate", func(tx *store.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, dedup_key, type, severity, class, COALESCE(project_id, ''), title, evidence_json FROM findings WHERE state = 'open'`)
		if err != nil {
			return err
		}
		var open []openFinding
		for rows.Next() {
			var f openFinding
			if err := rows.Scan(&f.id, &f.key, &f.typ, &f.sev, &f.class, &f.project, &f.title, &f.evidence); err != nil {
				rows.Close()
				return err
			}
			open = append(open, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		active := map[string]bool{}
		for _, r := range rules {
			if !r.Enabled {
				continue
			}
			for _, f := range open {
				if !r.Matcher.match(f.typ, f.sev, f.class, f.project) {
					continue
				}
				key := r.ID + "|" + f.key
				active[key] = true
				if err := e.advance(ctx, tx, r, f, key, sil); err != nil {
					return err
				}
			}
		}
		if err := e.resolveInactive(ctx, tx, rules, active); err != nil {
			return err
		}
		crows, err := tx.Query(ctx, `SELECT severity, COUNT(*) FROM alerts WHERE state = 'firing' AND silenced_by IS NULL GROUP BY severity`)
		if err != nil {
			return err
		}
		m := map[string]int{}
		for crows.Next() {
			var s string
			var n int
			if crows.Scan(&s, &n) == nil {
				m[s] = n
			}
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return err
		}
		e.Metrics.SetAlertsFiring(m)
		return nil
	})
}

func (e *Engine) advance(ctx context.Context, tx *store.Tx, r *Rule, f openFinding, key string, sil []Silence) error {
	now := tx.Now
	sev := f.sev
	if r.Severity != "" {
		sev = r.Severity
	}
	silence := silenced(sil, now, f.typ, sev, f.class, f.project)
	var id, state string
	var started int64
	var lastNotified sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT id, state, started_at, last_notified_at FROM alerts WHERE dedup_key = ?`, key).Scan(&id, &state, &started, &lastNotified)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, state, started = ids.New(ids.Alert), "pending", now.UnixMicro()
		if _, err := tx.Exec(ctx, `INSERT INTO alerts (id, rule_id, finding_id, dedup_key, project_id, state, severity, title, evidence_json, started_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)`, id, r.ID, f.id, key, nullStr(f.project), sev, f.title, f.evidence, started, tx.NowMicros()); err != nil {
			return err
		}
	case err != nil:
		return err
	case state == "resolved":
		state, started, lastNotified = "pending", now.UnixMicro(), sql.NullInt64{}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'pending', finding_id = ?, started_at = ?, resolved_at = NULL, fired_at = NULL, last_notified_at = NULL,
			updated_at = ? WHERE id = ?`, f.id, started, tx.NowMicros(), id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts SET severity = ?, title = ?, evidence_json = json_set(?, '$.last_event_id', json_extract(evidence_json, '$.last_event_id')),
		silenced_by = ?, updated_at = ? WHERE id = ?`, sev, f.title, f.evidence, nullStr(silence), tx.NowMicros(), id); err != nil {
		return err
	}
	if state == "pending" && now.Sub(clock.FromMicros(started)) >= time.Duration(r.ForSeconds)*time.Second {
		if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'firing', fired_at = ? WHERE id = ?`, tx.NowMicros(), id); err != nil {
			return err
		}
		state = "firing"
		e.Metrics.AlertTransition("firing")
	}
	if state == "firing" && silence == "" {
		cool := time.Duration(r.CooldownSeconds) * time.Second
		if !lastNotified.Valid || now.Sub(clock.FromMicros(lastNotified.Int64)) >= cool {
			return e.notify(ctx, tx, id, "raised", r, f.typ, sev, f.project, f.title, f.evidence)
		}
	}
	return nil
}

func (e *Engine) resolveInactive(ctx context.Context, tx *store.Tx, rules []*Rule, active map[string]bool) error {
	rows, err := tx.Query(ctx, `SELECT a.id, a.dedup_key, a.state, a.rule_id, a.severity, COALESCE(a.project_id, ''), a.title, a.evidence_json,
		COALESCE(f.type, ''), a.last_notified_at IS NOT NULL FROM alerts a LEFT JOIN findings f ON f.id = a.finding_id WHERE a.state IN ('pending', 'firing')`)
	if err != nil {
		return err
	}
	type res struct {
		id, state, rule, sev, project, title, ev, typ string
		notified                                      bool
	}
	var resolve []res
	for rows.Next() {
		var x res
		var key string
		if err := rows.Scan(&x.id, &key, &x.state, &x.rule, &x.sev, &x.project, &x.title, &x.ev, &x.typ, &x.notified); err != nil {
			rows.Close()
			return err
		}
		if !active[key] {
			resolve = append(resolve, x)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range resolve {
		if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = ?, updated_at = ? WHERE id = ?`, tx.NowMicros(), tx.NowMicros(), x.id); err != nil {
			return err
		}
		e.Metrics.AlertTransition("resolved")
		if x.state != "firing" || !x.notified {
			continue
		}
		for _, r := range rules {
			if r.ID == x.rule {
				if err := e.notify(ctx, tx, x.id, "resolved", r, x.typ, x.sev, x.project, x.title, x.ev); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (e *Engine) notify(ctx context.Context, tx *store.Tx, alertID, action string, r *Rule, typ, sev, project, title, evidence string) error {
	var ev map[string]any
	_ = json.Unmarshal([]byte(evidence), &ev)
	delete(ev, "last_event_id")
	eventID, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "alert", Entity: "alert", Action: action, ProjectID: project,
		Payload: map[string]any{"alert_id": alertID, "rule": r.Name, "finding_type": typ, "severity": sev, "title": title, "evidence": ev,
			"at": tx.Now.UTC().Format(time.RFC3339)}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE alerts SET last_notified_at = ?, notifications = notifications + 1, evidence_json = json_set(evidence_json, '$.last_event_id', ?)
		WHERE id = ?`, tx.NowMicros(), eventID, alertID)
	return err
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// Alerts lists alerts (state filter optional; projects nil = all).
func (e *Engine) Alerts(ctx context.Context, states []string, projects []string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := e.Store.R().QueryContext(ctx, `SELECT a.id, a.rule_id, r.name, a.finding_id, COALESCE(a.project_id, ''), a.state, a.severity, a.title, a.evidence_json,
		a.started_at, a.fired_at, a.resolved_at, a.last_notified_at, a.notifications, COALESCE(a.silenced_by, ''),
		COALESCE((SELECT CASE WHEN o.published_at IS NOT NULL THEN 'published' WHEN o.dead_at IS NOT NULL THEN 'dead' WHEN o.attempts > 0 THEN 'retrying' ELSE 'pending' END
			FROM outbox o WHERE o.id = json_extract(a.evidence_json, '$.last_event_id')), '')
		FROM alerts a JOIN alert_rules r ON r.id = a.rule_id ORDER BY a.updated_at DESC LIMIT ?`, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var ev string
		var s int64
		var f, rs, ln sql.NullInt64
		if err := rows.Scan(&a.ID, &a.RuleID, &a.Rule, &a.FindingID, &a.ProjectID, &a.State, &a.Severity, &a.Title, &ev, &s, &f, &rs, &ln, &a.Notifications,
			&a.SilencedBy, &a.Delivery); err != nil {
			return nil, err
		}
		if a.SilencedBy != "" && a.State == "firing" {
			a.State = "suppressed"
		}
		if len(states) > 0 && !slices.Contains(states, a.State) {
			continue
		}
		if projects != nil && a.ProjectID != "" && !slices.Contains(projects, a.ProjectID) {
			continue
		}
		_ = json.Unmarshal([]byte(ev), &a.Evidence)
		a.StartedAt = clock.FromMicros(s)
		a.FiredAt, a.ResolvedAt, a.LastNotifiedAt = nt(f), nt(rs), nt(ln)
		out = append(out, a)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func nt(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := clock.FromMicros(n.Int64)
	return &t
}

// Silences lists silences.
func (e *Engine) Silences(ctx context.Context) ([]Silence, error) {
	rows, err := e.Store.R().QueryContext(ctx, `SELECT id, matcher_json, comment, created_by, starts_at, ends_at, created_at FROM silences ORDER BY ends_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Silence{}
	for rows.Next() {
		var s Silence
		var m string
		var st, en, c int64
		if err := rows.Scan(&s.ID, &m, &s.Comment, &s.CreatedBy, &st, &en, &c); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(m), &s.Matcher)
		s.StartsAt, s.EndsAt, s.CreatedAt = clock.FromMicros(st), clock.FromMicros(en), clock.FromMicros(c)
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateSilence adds a silence.
func (e *Engine) CreateSilence(ctx context.Context, actor audit.Actor, m Matcher, comment string, start, end time.Time) (*Silence, error) {
	if !end.After(start) || end.Sub(start) > 90*24*time.Hour {
		return nil, apperr.New(apperr.CodeInvalid, "a silence lasts between 1 second and 90 days")
	}
	s := &Silence{ID: ids.New(ids.Silence), Matcher: m, Comment: comment, CreatedBy: actor.ID, StartsAt: start, EndsAt: end}
	err := e.Store.W().TxNamed(ctx, "alerts.silence", func(tx *store.Tx) error {
		mj, _ := json.Marshal(m)
		s.CreatedAt = tx.Now
		if _, err := tx.Exec(ctx, `INSERT INTO silences (id, matcher_json, comment, created_by, starts_at, ends_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.ID, string(mj), comment, actor.ID, start.UnixMicro(), end.UnixMicro(), tx.NowMicros()); err != nil {
			return err
		}
		_, err := e.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "alert.silence_created", Target: audit.Target{Type: "silence", ID: s.ID},
			Details: map[string]any{"ends_at": end.UTC().Format(time.RFC3339)}})
		return err
	})
	return s, err
}

// DeleteSilence ends a silence.
func (e *Engine) DeleteSilence(ctx context.Context, actor audit.Actor, id string) error {
	return e.Store.W().TxNamed(ctx, "alerts.unsilence", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `DELETE FROM silences WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "silence not found")
		}
		_, err = e.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "alert.silence_deleted", Target: audit.Target{Type: "silence", ID: id}})
		return err
	})
}

// RuleInput creates or updates a rule.
type RuleInput struct {
	Name            string  `json:"name,omitempty" maxLength:"100"`
	Description     string  `json:"description,omitempty" maxLength:"2000"`
	Enabled         *bool   `json:"enabled,omitempty"`
	Matcher         Matcher `json:"matcher"`
	ForSeconds      int     `json:"for_seconds,omitempty" minimum:"0" maximum:"86400"`
	CooldownSeconds int     `json:"cooldown_seconds,omitempty" minimum:"0" maximum:"604800"`
	Severity        string  `json:"severity,omitempty" enum:"critical,warning,info,"`
}

// SaveRule creates (id == "") or updates a rule.
func (e *Engine) SaveRule(ctx context.Context, actor audit.Actor, id string, in RuleInput, ifMatch int64) (*Rule, error) {
	if id == "" && in.Name == "" {
		return nil, apperr.New(apperr.CodeInvalid, "name is required")
	}
	enabled := in.Enabled == nil || *in.Enabled
	mj, _ := json.Marshal(in.Matcher)
	cool := in.CooldownSeconds
	if cool == 0 {
		cool = 3600
	}
	err := e.Store.W().TxNamed(ctx, "alerts.rule_save", func(tx *store.Tx) error {
		if id == "" {
			id = ids.New(ids.AlertRule)
			_, err := tx.Exec(ctx, `INSERT INTO alert_rules (id, name, description, enabled, matcher_json, for_seconds, cooldown_seconds, severity, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, in.Description, enabled, string(mj), in.ForSeconds, cool, in.Severity, tx.NowMicros(), tx.NowMicros())
			if store.IsConstraint(err) {
				return apperr.New(apperr.CodeAlreadyExists, "a rule named %q exists", in.Name)
			}
			if err != nil {
				return err
			}
		} else {
			n, err := tx.ExecN(ctx, `UPDATE alert_rules SET description = ?, enabled = ?, matcher_json = ?, for_seconds = ?, cooldown_seconds = ?, severity = ?,
				updated_at = ?, version = version + 1 WHERE id = ? AND (? = 0 OR version = ?)`, in.Description, enabled, string(mj), in.ForSeconds, cool, in.Severity,
				tx.NowMicros(), id, ifMatch, ifMatch)
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.New(apperr.CodePrecondition, "rule not found or modified concurrently")
			}
		}
		_, err := e.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "alert.rule_saved", Target: audit.Target{Type: "alert_rule", ID: id}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return scanRule(e.Store.R().QueryRowContext(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id = ?`, id))
}

// DeleteRule deletes a custom rule.
func (e *Engine) DeleteRule(ctx context.Context, actor audit.Actor, id string) error {
	return e.Store.W().TxNamed(ctx, "alerts.rule_delete", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `DELETE FROM alert_rules WHERE id = ? AND builtin = 0`, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "rule not found (built-in rules can be disabled, not deleted)")
		}
		_, err = e.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "alert.rule_deleted", Target: audit.Target{Type: "alert_rule", ID: id}})
		return err
	})
}
