// Package findings evaluates operational findings (§13): expiring and expired certificates and domains, missing
// relationship targets, stale observations and sources, declared/observed drift, conflicts, duplicate candidates,
// failed ingestion, unreachable integrations, invalid schemas, missing ownership, expected event connections not
// recently observed, outdated or end-of-life versions, stale backups and dead-lettered events.
//
// Each finding has a severity (critical, warning, info) and a class that keeps evidence honest: confirmed (the
// failure is known), warning (a risk), unknown (no data to decide), stale (data too old to trust) and
// not_applicable. Findings are recomputed periodically; ones no longer present are closed, never deleted.
package findings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Severities and classes.
const (
	Critical = "critical"
	Warning  = "warning"
	Info     = "info"

	ClassConfirmed     = "confirmed"
	ClassWarning       = "warning"
	ClassUnknown       = "unknown"
	ClassStale         = "stale"
	ClassNotApplicable = "not_applicable"
)

// Finding types.
const (
	TypeCertExpiring     = "certificate_expiring"
	TypeCertExpired      = "certificate_expired"
	TypeDomainExpiring   = "domain_expiring"
	TypeMissingTarget    = "missing_relationship_target"
	TypeStaleEntity      = "stale_observation"
	TypeStaleSource      = "stale_source"
	TypeIngestionFailed  = "ingestion_failed"
	TypeUnreachable      = "integration_unreachable"
	TypeInvalidSchema    = "invalid_schema"
	TypeMissingOwner     = "missing_owner"
	TypeDrift            = "declared_observed_drift"
	TypeConflict         = "source_conflict"
	TypeDuplicate        = "duplicate_candidate"
	TypeExpectedUnseen   = "expected_connection_unseen"
	TypeVersionAvailable = "version_update_available"
	TypeVersionEOL       = "version_end_of_life"
	TypeBackupStale      = "backup_stale"
	TypeRestored         = "backup_restored"
	TypeDeadLetters      = "events_dead_lettered"
	TypeBreakGlass       = "breakglass_used"
	TypeAbsent           = "absent_from_source"
)

// Finding is one current finding.
type Finding struct {
	ID             string         `json:"id"`
	DedupKey       string         `json:"dedup_key"`
	Type           string         `json:"type"`
	Severity       string         `json:"severity"`
	Class          string         `json:"class"`
	ProjectID      string         `json:"project_id,omitempty"`
	EntityID       string         `json:"entity_id,omitempty"`
	RelationID     string         `json:"relation_id,omitempty"`
	SourceID       string         `json:"source_id,omitempty"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	Evidence       map[string]any `json:"evidence"`
	State          string         `json:"state"`
	AcknowledgedAt *time.Time     `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string         `json:"acknowledged_by,omitempty"`
	FirstSeenAt    time.Time      `json:"first_seen_at"`
	LastSeenAt     time.Time      `json:"last_seen_at"`
	ClosedAt       *time.Time     `json:"closed_at,omitempty"`
}

// Config tunes evaluation.
type Config struct {
	CertWarn, CertCritical time.Duration
	// OwnedKinds must have an owner.
	OwnedKinds []string
	// BackupInterval is the configured backup interval (0 = backups disabled).
	BackupInterval time.Duration
	// ExpectedWindow is how recently a declared event connection must have been observed.
	ExpectedWindow time.Duration
}

// Engine evaluates findings.
type Engine struct {
	Store   *store.Store
	Catalog *catalog.Service
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Config  Config
	// OnChange runs after evaluation commits (alert engine).
	OnChange func(ctx context.Context) error
}

func (e *Engine) cfg() Config {
	c := e.Config
	if c.CertWarn == 0 {
		c.CertWarn = 30 * 24 * time.Hour
	}
	if c.CertCritical == 0 {
		c.CertCritical = 7 * 24 * time.Hour
	}
	if c.OwnedKinds == nil {
		c.OwnedKinds = []string{"service", "application", "repository", "server", "vm", "database", "domain"}
	}
	if c.ExpectedWindow == 0 {
		c.ExpectedWindow = 24 * time.Hour
	}
	return c
}

func parseTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type ent struct {
	id, project, kind, name, display, owner, health, freshness string
	valid                                                      bool
	eff, prov                                                  map[string]any
	validation                                                 string
	verDeclared, verObserved                                   string
	lastSeen                                                   sql.NullInt64
}

func label(e ent) string {
	if e.display != "" {
		return e.display
	}
	return e.kind + "/" + e.name
}

// Evaluate computes the current findings and reconciles them with the stored set.
func (e *Engine) Evaluate(ctx context.Context) error {
	start := e.Clock.Now()
	cur, err := e.compute(ctx)
	if err != nil {
		return err
	}
	if err := e.reconcile(ctx, cur); err != nil {
		return err
	}
	e.exportMetrics(ctx)
	if e.Log != nil {
		e.Log.Debug("findings evaluated", "event.action", "findings.evaluate", "count", len(cur), "event.duration_ms", e.Clock.Now().Sub(start).Milliseconds())
	}
	if e.OnChange != nil {
		return e.OnChange(ctx)
	}
	return nil
}

// RaiseTx records a transient finding inside a transaction (break-glass hook); the next evaluation keeps it open
// until it expires.
func RaiseTx(ctx context.Context, tx *store.Tx, f Finding, ttl time.Duration) error {
	ev := f.Evidence
	if ev == nil {
		ev = map[string]any{}
	}
	ev["expires_at"] = tx.Now.Add(ttl).UTC().Format(time.RFC3339)
	b, _ := json.Marshal(ev)
	_, err := tx.Exec(ctx, `INSERT INTO findings (id, dedup_key, type, severity, class, project_id, entity_id, source_id, title, message, evidence_json,
		state, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)
		ON CONFLICT (dedup_key) DO UPDATE SET state = 'open', last_seen_at = excluded.last_seen_at, evidence_json = excluded.evidence_json, closed_at = NULL`,
		ids.New(ids.Finding), f.DedupKey, f.Type, f.Severity, f.Class, nullStr(f.ProjectID), nullStr(f.EntityID), nullStr(f.SourceID), f.Title, f.Message,
		string(b), tx.NowMicros(), tx.NowMicros())
	return err
}

func (e *Engine) compute(ctx context.Context) ([]Finding, error) {
	c := e.cfg()
	now := e.Clock.Now()
	d := e.Store.R()
	var out []Finding
	add := func(f Finding) {
		if f.Evidence == nil {
			f.Evidence = map[string]any{}
		}
		out = append(out, f)
	}
	var ents []ent
	err := each(ctx, d, `SELECT id, project_id, kind, name, display_name, owner, health, freshness, valid, effective_json, provenance_json,
		validation_json, version_declared, version_observed, last_seen_at FROM entities WHERE tombstoned_at IS NULL`, func(rows *sql.Rows) error {
		var x ent
		var eff, prov string
		if err := rows.Scan(&x.id, &x.project, &x.kind, &x.name, &x.display, &x.owner, &x.health, &x.freshness, &x.valid, &eff, &prov, &x.validation,
			&x.verDeclared, &x.verObserved, &x.lastSeen); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(eff), &x.eff)
		_ = json.Unmarshal([]byte(prov), &x.prov)
		ents = append(ents, x)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ownedBy := map[string]bool{}
	if err := each(ctx, d, `SELECT DISTINCT r.from_id FROM relations r WHERE r.type = 'owned_by' AND r.live = 1`, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ownedBy[id] = true
		return nil
	}); err != nil {
		return nil, err
	}
	for _, x := range ents {
		spec, _ := x.eff["spec"].(map[string]any)
		subject := func(f Finding) Finding {
			f.ProjectID, f.EntityID = x.project, x.id
			return f
		}
		switch x.kind {
		case "certificate":
			if t, ok := parseTime(spec["not_after"]); ok {
				left := t.Sub(now)
				ev := map[string]any{"not_after": t.UTC().Format(time.RFC3339), "days_left": int(left.Hours() / 24), "pointer": "/spec/not_after"}
				switch {
				case left <= 0:
					add(subject(Finding{DedupKey: TypeCertExpired + "|" + x.id, Type: TypeCertExpired, Severity: Critical, Class: ClassConfirmed,
						Title: "Certificate expired: " + label(x), Message: fmt.Sprintf("expired %s", t.UTC().Format("2006-01-02")), Evidence: ev}))
				case left <= c.CertCritical:
					add(subject(Finding{DedupKey: TypeCertExpiring + "|" + x.id, Type: TypeCertExpiring, Severity: Critical, Class: ClassWarning,
						Title: "Certificate expires within 7 days: " + label(x), Message: fmt.Sprintf("expires %s", t.UTC().Format("2006-01-02")), Evidence: ev}))
				case left <= c.CertWarn:
					add(subject(Finding{DedupKey: TypeCertExpiring + "|" + x.id, Type: TypeCertExpiring, Severity: Warning, Class: ClassWarning,
						Title: "Certificate expires soon: " + label(x), Message: fmt.Sprintf("expires %s", t.UTC().Format("2006-01-02")), Evidence: ev}))
				}
			}
		case "domain":
			if t, ok := parseTime(spec["expires_at"]); ok && t.Sub(now) <= c.CertWarn {
				sev := Warning
				if t.Sub(now) <= c.CertCritical {
					sev = Critical
				}
				add(subject(Finding{DedupKey: TypeDomainExpiring + "|" + x.id, Type: TypeDomainExpiring, Severity: sev, Class: ClassWarning,
					Title: "Domain registration expires soon: " + label(x), Message: "expires " + t.UTC().Format("2006-01-02"),
					Evidence: map[string]any{"expires_at": t.UTC().Format(time.RFC3339), "auto_renew": spec["auto_renew"]}}))
			}
		}
		if !x.valid {
			var fe []map[string]any
			_ = json.Unmarshal([]byte(x.validation), &fe)
			add(subject(Finding{DedupKey: TypeInvalidSchema + "|" + x.id, Type: TypeInvalidSchema, Severity: Warning, Class: ClassConfirmed,
				Title: "Does not match its kind schema: " + label(x), Message: fmt.Sprintf("%d validation errors", len(fe)), Evidence: map[string]any{"errors": fe}}))
		}
		if x.freshness == "stale" {
			ev := map[string]any{}
			if x.lastSeen.Valid {
				ev["last_seen_at"] = clock.FromMicros(x.lastSeen.Int64).Format(time.RFC3339)
			}
			add(subject(Finding{DedupKey: TypeStaleEntity + "|" + x.id, Type: TypeStaleEntity, Severity: Info, Class: ClassStale,
				Title: "Observations are stale: " + label(x), Message: "no fresh runtime data from its sources", Evidence: ev}))
		}
		if slices.Contains(c.OwnedKinds, x.kind) && x.owner == "" && !ownedBy[x.id] {
			add(subject(Finding{DedupKey: TypeMissingOwner + "|" + x.id, Type: TypeMissingOwner, Severity: Info, Class: ClassConfirmed,
				Title: "No owner: " + label(x), Message: "set metadata.owner or an owned_by relationship"}))
		}
		if x.verDeclared != "" && x.verObserved != "" && strings.TrimPrefix(x.verDeclared, "v") != strings.TrimPrefix(x.verObserved, "v") {
			add(subject(Finding{DedupKey: TypeDrift + "|version|" + x.id, Type: TypeDrift, Severity: Warning, Class: ClassConfirmed,
				Title: "Declared and observed versions differ: " + label(x), Message: fmt.Sprintf("declared %s, observed %s", x.verDeclared, x.verObserved),
				Evidence: map[string]any{"declared": x.verDeclared, "observed": x.verObserved}}))
		}
		// Declared/observed drift: a declared value wins over a different imported or discovered value.
		var drift []map[string]any
		for ptr, raw := range x.prov {
			pm, _ := raw.(map[string]any)
			if pm["layer"] != string(catalog.LayerDeclared) || !strings.HasPrefix(ptr, "/spec/") {
				continue
			}
			cands, _ := pm["candidates"].([]any)
			for _, cnd := range cands {
				cm, _ := cnd.(map[string]any)
				l, _ := cm["layer"].(string)
				if l != string(catalog.LayerImported) && l != string(catalog.LayerDiscovered) {
					continue
				}
				want, _ := catalog.GetPointer(x.eff, ptr)
				if fmt.Sprint(want) != fmt.Sprint(cm["value"]) {
					drift = append(drift, map[string]any{"pointer": ptr, "declared": want, "observed": cm["value"], "source": cm["source"], "layer": l})
				}
			}
		}
		if len(drift) > 0 {
			sort.Slice(drift, func(i, j int) bool { return fmt.Sprint(drift[i]["pointer"]) < fmt.Sprint(drift[j]["pointer"]) })
			add(subject(Finding{DedupKey: TypeDrift + "|spec|" + x.id, Type: TypeDrift, Severity: Warning, Class: ClassConfirmed,
				Title: "Declaration differs from imported/observed data: " + label(x), Message: fmt.Sprintf("%d fields differ", len(drift)),
				Evidence: map[string]any{"fields": drift}}))
		}
	}
	// Conflicts.
	if err := each(ctx, d, `SELECT c.entity_id, e.project_id, e.kind, e.name, COUNT(*) FROM conflicts c JOIN entities e ON e.id = c.entity_id
		WHERE e.tombstoned_at IS NULL GROUP BY c.entity_id`, func(rows *sql.Rows) error {
		var id, project, kind, name string
		var n int
		if err := rows.Scan(&id, &project, &kind, &name, &n); err != nil {
			return err
		}
		add(Finding{DedupKey: TypeConflict + "|" + id, Type: TypeConflict, Severity: Warning, Class: ClassConfirmed, ProjectID: project, EntityID: id,
			Title: "Sources disagree: " + kind + "/" + name, Message: fmt.Sprintf("%d fields have conflicting values in the same layer", n)})
		return nil
	}); err != nil {
		return nil, err
	}
	// Duplicate candidates: identity keys shared by more than one live entity.
	drows, err := d.QueryContext(ctx, `SELECT project_id, kind, key_value, GROUP_CONCAT(entity_id) FROM entity_identity_keys
		GROUP BY project_id, kind, key_value HAVING COUNT(DISTINCT entity_id) > 1`)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var project, kind, key, members string
		if err := drows.Scan(&project, &kind, &key, &members); err != nil {
			drows.Close()
			return nil, err
		}
		list := strings.Split(members, ",")
		sort.Strings(list)
		add(Finding{DedupKey: TypeDuplicate + "|" + project + "|" + kind + "|" + key, Type: TypeDuplicate, Severity: Info, Class: ClassWarning, ProjectID: project,
			EntityID: list[0], Title: "Possible duplicate " + kind + " entities", Message: "identity key " + key + " matches several entities",
			Evidence: map[string]any{"key": key, "entities": list}})
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return nil, err
	}
	// Missing relationship targets.
	urows, err := d.QueryContext(ctx, `SELECT u.source_id, u.project_id, u.type, u.from_ref, u.to_ref, COALESCE(u.from_entity, ''), s.name
		FROM unresolved_refs u JOIN sources s ON s.id = u.source_id`)
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var src, project, typ, from, to, fromEnt, srcName string
		if err := urows.Scan(&src, &project, &typ, &from, &to, &fromEnt, &srcName); err != nil {
			urows.Close()
			return nil, err
		}
		add(Finding{DedupKey: TypeMissingTarget + "|" + src + "|" + typ + "|" + from + "|" + to, Type: TypeMissingTarget, Severity: Warning, Class: ClassConfirmed,
			ProjectID: project, EntityID: fromEnt, SourceID: src, Title: "Relationship target not found", Message: fmt.Sprintf("%s: %s %s → %s", srcName, from, typ, to),
			Evidence: map[string]any{"source": srcName, "type": typ, "from": from, "to": to}})
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return nil, err
	}
	// Sources: stale and failing.
	srcs, err := e.Catalog.Sources(ctx, nil)
	if err != nil {
		return nil, err
	}
	tailFresh := map[string]bool{}
	for _, s := range srcs {
		if s.System {
			continue
		}
		if s.Provider == "pulse_tail" && s.Freshness == "fresh" {
			tailFresh[s.ProjectID] = true
		}
		if s.Enabled && s.Freshness == "stale" {
			ev := map[string]any{"expected_interval_seconds": s.ExpectedIntervalSeconds}
			if s.LastSuccessAt != nil {
				ev["last_success_at"] = s.LastSuccessAt.Format(time.RFC3339)
			}
			add(Finding{DedupKey: TypeStaleSource + "|" + s.ID, Type: TypeStaleSource, Severity: Warning, Class: ClassStale, ProjectID: s.ProjectID, SourceID: s.ID,
				Title: "Source is stale: " + s.DisplayName, Message: "no successful sync or heartbeat within 3× its expected interval", Evidence: ev})
		}
		if s.Enabled && s.LastError != "" && (s.LastSuccessAt == nil || (s.LastRunAt != nil && s.LastRunAt.After(*s.LastSuccessAt))) {
			add(Finding{DedupKey: TypeIngestionFailed + "|" + s.ID, Type: TypeIngestionFailed, Severity: Warning, Class: ClassConfirmed, ProjectID: s.ProjectID,
				SourceID: s.ID, Title: "Ingestion failed: " + s.DisplayName, Message: s.LastError})
		}
	}
	// Facts absent from their source's latest complete snapshot (orphan policy).
	arows, err := d.QueryContext(ctx, `SELECT f.entity_id, e.project_id, e.kind, e.name, s.id, s.name FROM facets f JOIN sources s ON s.id = f.source_id
		JOIN entities e ON e.id = f.entity_id WHERE f.tombstoned_at IS NULL AND f.generation > 0 AND f.generation < s.last_generation
		AND json_extract(s.policy_json, '$.on_absent') = 'orphan' AND e.tombstoned_at IS NULL LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var id, project, kind, name, sid, sname string
		if err := arows.Scan(&id, &project, &kind, &name, &sid, &sname); err != nil {
			arows.Close()
			return nil, err
		}
		add(Finding{DedupKey: TypeAbsent + "|" + sid + "|" + id, Type: TypeAbsent, Severity: Info, Class: ClassStale, ProjectID: project, EntityID: id, SourceID: sid,
			Title: kind + "/" + name + " is no longer reported by " + sname, Message: "kept because the source's on_absent policy is orphan"})
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return nil, err
	}
	// Expected event connections not recently observed (only where a fresh Pulse tail exists to observe them).
	cut := now.Add(-c.ExpectedWindow).UnixMicro()
	erows, err := d.QueryContext(ctx, `SELECT r.id, r.project_id, r.type, fe.name, te.name,
		(SELECT MAX(a.last_seen_at) FROM relation_assertions a WHERE a.relation_id = r.id AND a.evidence_class = 'observed' AND a.tombstoned_at IS NULL)
		FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id
		WHERE r.live = 1 AND r.type IN ('publishes', 'subscribes_to') AND r.evidence_class = 'declared'`)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var id, project, typ, from, to string
		var last sql.NullInt64
		if err := erows.Scan(&id, &project, &typ, &from, &to, &last); err != nil {
			erows.Close()
			return nil, err
		}
		if !tailFresh[project] || (last.Valid && last.Int64 >= cut) {
			continue
		}
		ev := map[string]any{"window": c.ExpectedWindow.String()}
		if last.Valid {
			ev["last_observed_at"] = clock.FromMicros(last.Int64).Format(time.RFC3339)
		}
		add(Finding{DedupKey: TypeExpectedUnseen + "|" + id, Type: TypeExpectedUnseen, Severity: Warning, Class: ClassWarning, ProjectID: project, RelationID: id,
			Title: fmt.Sprintf("Declared %s not observed: %s → %s", typ, from, to), Message: "the Pulse tail has not seen this traffic within the window",
			Evidence: ev})
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}
	// Versions.
	vrows, err := d.QueryContext(ctx, `SELECT v.entity_id, e.project_id, e.kind, e.name, v.status, v.latest, v.eol_date, e.version_observed, e.version_declared, v.evidence_json
		FROM version_checks v JOIN entities e ON e.id = v.entity_id WHERE e.tombstoned_at IS NULL AND v.status IN ('update_available', 'eol')`)
	if err != nil {
		return nil, err
	}
	for vrows.Next() {
		var id, project, kind, name, status, latest, eol, obs, decl, evj string
		if err := vrows.Scan(&id, &project, &kind, &name, &status, &latest, &eol, &obs, &decl, &evj); err != nil {
			vrows.Close()
			return nil, err
		}
		var ev map[string]any
		_ = json.Unmarshal([]byte(evj), &ev)
		cur := obs
		if cur == "" {
			cur = decl
		}
		if status == "eol" {
			sev := Warning
			if t, ok := parseTime(eol); ok && t.Before(now) {
				sev = Critical
			}
			add(Finding{DedupKey: TypeVersionEOL + "|" + id, Type: TypeVersionEOL, Severity: sev, Class: ClassConfirmed, ProjectID: project, EntityID: id,
				Title: fmt.Sprintf("%s/%s runs an end-of-life release", kind, name), Message: fmt.Sprintf("version %s, end of life %s", cur, eol), Evidence: ev})
		} else {
			add(Finding{DedupKey: TypeVersionAvailable + "|" + id, Type: TypeVersionAvailable, Severity: Info, Class: ClassConfirmed, ProjectID: project, EntityID: id,
				Title: fmt.Sprintf("Update available for %s/%s", kind, name), Message: fmt.Sprintf("%s → %s", cur, latest), Evidence: ev})
		}
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}
	// Telemetry integrations.
	irows, err := d.QueryContext(ctx, `SELECT id, name, COALESCE(last_error, '') FROM integrations WHERE enabled = 1 AND last_test_ok = 0`)
	if err != nil {
		return nil, err
	}
	for irows.Next() {
		var id, name, msg string
		if err := irows.Scan(&id, &name, &msg); err != nil {
			irows.Close()
			return nil, err
		}
		add(Finding{DedupKey: TypeUnreachable + "|" + id, Type: TypeUnreachable, Severity: Warning, Class: ClassConfirmed, Title: "Integration unreachable: " + name,
			Message: msg, Evidence: map[string]any{"integration_id": id}})
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return nil, err
	}
	// Backups.
	if c.BackupInterval > 0 {
		var last sql.NullInt64
		if err := d.QueryRowContext(ctx, `SELECT MAX(finished_at) FROM backup_runs WHERE result = 'ok'`).Scan(&last); err != nil {
			return nil, err
		}
		if !last.Valid || now.Sub(clock.FromMicros(last.Int64)) > 2*c.BackupInterval {
			ev := map[string]any{"interval": c.BackupInterval.String()}
			msg := "no successful backup yet"
			if last.Valid {
				ev["last_success_at"] = clock.FromMicros(last.Int64).Format(time.RFC3339)
				msg = "last successful backup " + clock.FromMicros(last.Int64).Format(time.RFC3339)
			}
			add(Finding{DedupKey: TypeBackupStale, Type: TypeBackupStale, Severity: Critical, Class: ClassConfirmed, Title: "Backups are not current",
				Message: msg, Evidence: ev})
		}
	}
	// Dead-lettered events.
	var dead int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE dead_at IS NOT NULL AND dead_at > ?`, now.Add(-24*time.Hour).UnixMicro()).Scan(&dead); err != nil {
		return nil, err
	}
	if dead > 0 {
		add(Finding{DedupKey: TypeDeadLetters, Type: TypeDeadLetters, Severity: Warning, Class: ClassConfirmed, Title: "Events could not be delivered to Pulse",
			Message: fmt.Sprintf("%d events dead-lettered in the last 24 h", dead), Evidence: map[string]any{"count": dead}})
	}
	return out, nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// reconcile upserts current findings and closes the rest (transient findings stay open until they expire).
func (e *Engine) reconcile(ctx context.Context, cur []Finding) error {
	return e.Store.W().TxNamed(ctx, "findings.reconcile", func(tx *store.Tx) error {
		keep := map[string]bool{}
		for _, f := range cur {
			keep[f.DedupKey] = true
			ev, _ := json.Marshal(f.Evidence)
			var state string
			err := tx.QueryRow(ctx, `SELECT state FROM findings WHERE dedup_key = ?`, f.DedupKey).Scan(&state)
			switch {
			case err == sql.ErrNoRows:
				if _, err := tx.Exec(ctx, `INSERT INTO findings (id, dedup_key, type, severity, class, project_id, entity_id, relation_id, source_id, title,
					message, evidence_json, state, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
					ids.New(ids.Finding), f.DedupKey, f.Type, f.Severity, f.Class, nullStr(f.ProjectID), nullStr(f.EntityID), nullStr(f.RelationID),
					nullStr(f.SourceID), f.Title, f.Message, string(ev), tx.NowMicros(), tx.NowMicros()); err != nil {
					return err
				}
				if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "findings", Entity: "finding", Action: "opened", ProjectID: f.ProjectID,
					Payload: map[string]any{"type": f.Type, "severity": f.Severity, "class": f.Class, "entity_id": f.EntityID, "title": f.Title}}); err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if _, err := tx.Exec(ctx, `UPDATE findings SET severity = ?, class = ?, title = ?, message = ?, evidence_json = ?, last_seen_at = ?, state = 'open',
					closed_at = NULL, first_seen_at = CASE WHEN state = 'closed' THEN ? ELSE first_seen_at END WHERE dedup_key = ?`,
					f.Severity, f.Class, f.Title, f.Message, string(ev), tx.NowMicros(), tx.NowMicros(), f.DedupKey); err != nil {
					return err
				}
			}
		}
		rows, err := tx.Query(ctx, `SELECT dedup_key, type, evidence_json, COALESCE(project_id, '') FROM findings WHERE state = 'open'`)
		if err != nil {
			return err
		}
		type closing struct{ key, typ, project string }
		var toClose []closing
		for rows.Next() {
			var key, typ, evj, project string
			if err := rows.Scan(&key, &typ, &evj, &project); err != nil {
				rows.Close()
				return err
			}
			if keep[key] {
				continue
			}
			// Findings raised by events (break-glass, restore) stay open until their expiry.
			var ev map[string]any
			_ = json.Unmarshal([]byte(evj), &ev)
			if t, ok := parseTime(ev["expires_at"]); ok && tx.Now.Before(t) {
				continue
			}
			toClose = append(toClose, closing{key, typ, project})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range toClose {
			if _, err := tx.Exec(ctx, `UPDATE findings SET state = 'closed', closed_at = ? WHERE dedup_key = ?`, tx.NowMicros(), c.key); err != nil {
				return err
			}
			if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "findings", Entity: "finding", Action: "closed", ProjectID: c.project,
				Payload: map[string]any{"type": c.typ, "dedup_key": c.key}}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *Engine) exportMetrics(ctx context.Context) {
	rows, err := e.Store.R().QueryContext(ctx, `SELECT type, severity, COUNT(*) FROM findings WHERE state = 'open' GROUP BY type, severity`)
	if err != nil {
		return
	}
	defer rows.Close()
	m := map[[2]string]int{}
	for rows.Next() {
		var t, s string
		var n int
		if rows.Scan(&t, &s, &n) == nil {
			m[[2]string{t, s}] = n
		}
	}
	if rows.Err() != nil {
		return
	}
	e.Metrics.SetFindings(m)
}

// Prune deletes closed findings older than retention.
func (e *Engine) Prune(ctx context.Context, retention time.Duration) error {
	return e.Store.W().TxNamed(ctx, "findings.prune", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM findings WHERE state = 'closed' AND closed_at < ?`, tx.Now.Add(-retention).UnixMicro())
		return err
	})
}

// each runs a query and calls fn per row, closing the rows and reporting iteration errors.
func each(ctx context.Context, d *sql.DB, q string, fn func(*sql.Rows) error, args ...any) error {
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
