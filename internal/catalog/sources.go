package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Policy is a source's reconciliation policy (ADR-003).
type Policy struct {
	// Priority overrides the layer's default precedence for every field the source writes.
	Priority *int `json:"priority,omitempty"`
	// FieldPriority overrides precedence per JSON pointer (exact, or a prefix ending in "/").
	FieldPriority map[string]int `json:"field_priority,omitempty"`
	// OnAbsent decides what a complete snapshot does with this source's facets it no longer contains:
	// tombstone (default), orphan (keep, flag as absent) or keep.
	OnAbsent string `json:"on_absent,omitempty"`
	// MaxRemovalPercent is the mass-removal guard (default: installation limit).
	MaxRemovalPercent float64 `json:"max_removal_percent,omitempty"`
	// AllowedKinds restricts what the source may write (empty = any).
	AllowedKinds []string `json:"allowed_kinds,omitempty"`
	// DefaultTTLSeconds sets expires_at on discovered facets that do not carry one (0 = no expiry).
	DefaultTTLSeconds int `json:"default_ttl_seconds,omitempty"`
}

// Validate checks the policy.
func (p *Policy) Validate() error {
	v := &apperr.Validation{}
	switch p.OnAbsent {
	case "", "tombstone", "orphan", "keep":
	default:
		v.Add("policy.on_absent", "must be tombstone, orphan or keep")
	}
	if p.MaxRemovalPercent < 0 || p.MaxRemovalPercent > 100 {
		v.Add("policy.max_removal_percent", "must be within 0..100")
	}
	if p.Priority != nil && (*p.Priority < 0 || *p.Priority > 1000) {
		v.Add("policy.priority", "must be within 0..1000")
	}
	for k, pr := range p.FieldPriority {
		if !strings.HasPrefix(k, "/metadata/") && !strings.HasPrefix(k, "/spec/") {
			v.Add("policy.field_priority", "pointer %q must start with /metadata/ or /spec/", k)
		}
		if pr < 0 || pr > 1000 {
			v.Add("policy.field_priority", "priority for %q must be within 0..1000", k)
		}
	}
	if p.DefaultTTLSeconds < 0 || p.DefaultTTLSeconds > 366*86400 {
		v.Add("policy.default_ttl_seconds", "must be within 0..31622400")
	}
	return v.Err()
}

// Source is a writer of facts (ADR-013).
type Source struct {
	ID                      string          `json:"id"`
	ProjectID               string          `json:"project_id"`
	Name                    string          `json:"name"`
	DisplayName             string          `json:"display_name"`
	Provider                string          `json:"provider"`
	Layer                   Layer           `json:"layer"`
	System                  bool            `json:"system"`
	Enabled                 bool            `json:"enabled"`
	Config                  json.RawMessage `json:"config"`
	HasSecrets              bool            `json:"has_secrets"`
	Policy                  Policy          `json:"policy"`
	ScheduleSeconds         int             `json:"schedule_seconds"`
	ExpectedIntervalSeconds int             `json:"expected_interval_seconds"`
	LastGeneration          int64           `json:"last_generation"`
	LastRunAt               *time.Time      `json:"last_run_at,omitempty"`
	LastSuccessAt           *time.Time      `json:"last_success_at,omitempty"`
	LastHeartbeatAt         *time.Time      `json:"last_heartbeat_at,omitempty"`
	LastError               string          `json:"last_error,omitempty"`
	NextRunAt               *time.Time      `json:"next_run_at,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	ResourceVersion         int64           `json:"resource_version"`
	Freshness               string          `json:"freshness"`

	secretsEnc []byte
}

// Priority returns the effective precedence of the source.
func (s *Source) Priority() int {
	if s.Policy.Priority != nil {
		return *s.Policy.Priority
	}
	return s.Layer.DefaultPriority()
}

const sourceCols = `id, project_id, name, display_name, provider, layer, system, enabled, config_json, secrets_enc, policy_json, schedule_seconds,
	expected_interval_seconds, last_generation, last_run_at, last_success_at, last_heartbeat_at, COALESCE(last_error, ''), next_run_at,
	created_at, updated_at, version`

func nullTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := clock.FromMicros(n.Int64)
	return &t
}

func scanSource(r interface{ Scan(...any) error }) (*Source, error) {
	var s Source
	var cfg, pol, layer string
	var lr, ls, lh, nr sql.NullInt64
	var c, u int64
	if err := r.Scan(&s.ID, &s.ProjectID, &s.Name, &s.DisplayName, &s.Provider, &layer, &s.System, &s.Enabled, &cfg, &s.secretsEnc, &pol,
		&s.ScheduleSeconds, &s.ExpectedIntervalSeconds, &s.LastGeneration, &lr, &ls, &lh, &s.LastError, &nr, &c, &u, &s.ResourceVersion); err != nil {
		return nil, err
	}
	s.Layer = Layer(layer)
	s.Config = json.RawMessage(cfg)
	s.HasSecrets = len(s.secretsEnc) > 0
	_ = json.Unmarshal([]byte(pol), &s.Policy)
	s.LastRunAt, s.LastSuccessAt, s.LastHeartbeatAt, s.NextRunAt = nullTime(lr), nullTime(ls), nullTime(lh), nullTime(nr)
	s.CreatedAt, s.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &s, nil
}

// SourceFreshness classifies a source by its last success (or heartbeat) against its expected interval.
func SourceFreshness(s *Source, now time.Time) string {
	last := s.LastSuccessAt
	if s.LastHeartbeatAt != nil && (last == nil || s.LastHeartbeatAt.After(*last)) {
		last = s.LastHeartbeatAt
	}
	if s.Provider == "manual" {
		return "fresh" // declarations do not age
	}
	if last == nil {
		return "unknown"
	}
	interval := time.Duration(s.ExpectedIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	age := now.Sub(*last)
	switch {
	case age <= interval:
		return "fresh"
	case age <= 3*interval:
		return "aging"
	}
	return "stale"
}

// ErrSourceNotFound is returned for unknown sources.
var ErrSourceNotFound = apperr.New(apperr.CodeNotFound, "source not found")

// Source returns a source by ID.
func (s *Service) Source(ctx context.Context, id string) (*Source, error) {
	src, err := scanSource(s.Store.R().QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err == nil {
		src.Freshness = SourceFreshness(src, s.Clock.Now())
	}
	return src, err
}

// SourceTx returns a source inside a transaction.
func SourceTx(ctx context.Context, tx *store.Tx, id string) (*Source, error) {
	src, err := scanSource(tx.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	return src, err
}

// SourceByName returns a source by project and name.
func (s *Service) SourceByName(ctx context.Context, projectID, name string) (*Source, error) {
	src, err := scanSource(s.Store.R().QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE project_id = ? AND name = ?`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err == nil {
		src.Freshness = SourceFreshness(src, s.Clock.Now())
	}
	return src, err
}

// Sources lists sources of the given projects (nil = all).
func (s *Service) Sources(ctx context.Context, projectIDs []string) ([]*Source, error) {
	q := `SELECT ` + sourceCols + ` FROM sources`
	var args []any
	if projectIDs != nil {
		if len(projectIDs) == 0 {
			return []*Source{}, nil
		}
		q += ` WHERE project_id IN (` + strings.Repeat(",?", len(projectIDs))[1:] + `)`
		for _, p := range projectIDs {
			args = append(args, p)
		}
	}
	rows, err := s.Store.R().QueryContext(ctx, q+` ORDER BY project_id, system DESC, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Source{}
	now := s.Clock.Now()
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		src.Freshness = SourceFreshness(src, now)
		out = append(out, src)
	}
	return out, rows.Err()
}

// SourceInput creates or updates a source.
type SourceInput struct {
	ProjectID               string          `json:"project_id"`
	Name                    string          `json:"name"`
	DisplayName             string          `json:"display_name"`
	Provider                string          `json:"provider"`
	Layer                   Layer           `json:"layer"`
	Enabled                 *bool           `json:"enabled,omitempty"`
	Config                  json.RawMessage `json:"config,omitempty"`
	Policy                  *Policy         `json:"policy,omitempty"`
	ScheduleSeconds         int             `json:"schedule_seconds,omitempty"`
	ExpectedIntervalSeconds int             `json:"expected_interval_seconds,omitempty"`
}

func secretsAAD(sourceID string) []byte { return keyring.FieldAAD("sources", "secrets_enc", sourceID) }

// CreateSource creates a non-system source. secrets (provider credentials) are sealed with the keyring.
func (s *Service) CreateSource(ctx context.Context, actor audit.Actor, in SourceInput, secrets map[string]string) (*Source, error) {
	v := &apperr.Validation{}
	if !ValidName(in.Name) || len(in.Name) > 63 {
		v.Add("name", "must be a lowercase slug")
	}
	for _, ss := range systemSources {
		if ss.name == in.Name {
			v.Add("name", "%q is reserved for a system source", in.Name)
		}
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Name
	}
	checkString(in.DisplayName, 128, false, "display_name", v)
	if !in.Layer.Valid() || in.Layer == LayerOverride || in.Layer == LayerAnnotation {
		v.Add("layer", "must be declared, imported, discovered or observation")
	}
	if in.ScheduleSeconds != 0 && (in.ScheduleSeconds < 60 || in.ScheduleSeconds > 7*86400) {
		v.Add("schedule_seconds", "must be 0 (push only) or within 60..604800")
	}
	if in.ExpectedIntervalSeconds < 0 || in.ExpectedIntervalSeconds > 30*86400 {
		v.Add("expected_interval_seconds", "must be within 0..2592000")
	}
	pol := Policy{}
	if in.Policy != nil {
		pol = *in.Policy
	}
	if err := pol.Validate(); err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			for _, f := range ae.Fields {
				v.Add(f.Field, "%s", f.Message)
			}
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	if in.ExpectedIntervalSeconds == 0 && in.ScheduleSeconds > 0 {
		in.ExpectedIntervalSeconds = in.ScheduleSeconds
	}
	enabled := in.Enabled == nil || *in.Enabled
	id := ids.New(ids.Source)
	var enc []byte
	if len(secrets) > 0 {
		b, _ := json.Marshal(secrets)
		var err error
		if enc, err = s.Keyring.Seal(keyring.PurposeField, secretsAAD(id), b); err != nil {
			return nil, err
		}
	}
	err := s.Store.W().TxNamed(ctx, "catalog.source_create", func(tx *store.Tx) error {
		if _, err := projectTx(ctx, tx, in.ProjectID); err != nil {
			return err
		}
		var next sql.NullInt64
		if in.ScheduleSeconds > 0 && enabled {
			next = sql.NullInt64{Int64: tx.Now.Add(30 * time.Second).UnixMicro(), Valid: true}
		}
		_, err := tx.Exec(ctx, `INSERT INTO sources (id, project_id, name, display_name, provider, layer, enabled, config_json, secrets_enc, policy_json,
			schedule_seconds, expected_interval_seconds, next_run_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.ProjectID, in.Name, in.DisplayName, in.Provider, string(in.Layer), enabled, compactJSON(in.Config), enc, rawJSON(pol),
			in.ScheduleSeconds, in.ExpectedIntervalSeconds, next, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "a source named %q already exists in this project", in.Name)
		}
		if err != nil {
			return err
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: in.ProjectID, Action: "source.created", Target: audit.Target{Type: "source", ID: id},
			Details: map[string]any{"name": in.Name, "provider": in.Provider, "layer": string(in.Layer), "with_secrets": enc != nil}}); err != nil {
			return err
		}
		return s.changeTx(ctx, tx, change{project: in.ProjectID, typ: "source", id: id, action: "created", actor: actor, summary: map[string]any{"name": in.Name}})
	})
	if err != nil {
		return nil, err
	}
	return s.Source(ctx, id)
}

// UpdateSource changes a source. secrets: nil keeps the stored credentials, an empty map clears them.
func (s *Service) UpdateSource(ctx context.Context, actor audit.Actor, id string, in SourceInput, secrets map[string]string, ifMatch int64) (*Source, error) {
	err := s.Store.W().TxNamed(ctx, "catalog.source_update", func(tx *store.Tx) error {
		cur, err := SourceTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.System && (in.Provider != "" && in.Provider != cur.Provider || len(in.Config) > 0 && string(in.Config) != "{}" || in.ScheduleSeconds != 0) {
			return apperr.New(apperr.CodeForbidden, "system sources only accept policy changes")
		}
		if in.DisplayName == "" {
			in.DisplayName = cur.DisplayName
		}
		cfg := cur.Config
		if len(in.Config) > 0 {
			cfg = in.Config
		}
		pol := cur.Policy
		if in.Policy != nil {
			pol = *in.Policy
			if err := pol.Validate(); err != nil {
				return err
			}
		}
		enabled := cur.Enabled
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		sched, exp := cur.ScheduleSeconds, cur.ExpectedIntervalSeconds
		if !cur.System {
			sched = in.ScheduleSeconds
			if in.ExpectedIntervalSeconds != 0 {
				exp = in.ExpectedIntervalSeconds
			}
		}
		if sched != 0 && (sched < 60 || sched > 7*86400) {
			return apperr.New(apperr.CodeInvalid, "schedule_seconds must be 0 or within 60..604800")
		}
		enc := cur.secretsEnc
		if secrets != nil {
			enc = nil
			if len(secrets) > 0 {
				b, _ := json.Marshal(secrets)
				if enc, err = s.Keyring.Seal(keyring.PurposeField, secretsAAD(id), b); err != nil {
					return err
				}
			}
		}
		var next sql.NullInt64
		if sched > 0 && enabled {
			next = sql.NullInt64{Int64: tx.Now.Add(10 * time.Second).UnixMicro(), Valid: true}
		}
		n, err := tx.ExecN(ctx, `UPDATE sources SET display_name = ?, enabled = ?, config_json = ?, secrets_enc = ?, policy_json = ?, schedule_seconds = ?,
			expected_interval_seconds = ?, next_run_at = ?, updated_at = ?, version = version + 1 WHERE id = ? AND (? = 0 OR version = ?)`,
			in.DisplayName, enabled, compactJSON(cfg), enc, rawJSON(pol), sched, exp, next, tx.NowMicros(), id, ifMatch, ifMatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "source was modified concurrently; re-read it")
		}
		details := map[string]any{"enabled": enabled, "secrets_changed": secrets != nil}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: cur.ProjectID, Action: "source.updated", Target: audit.Target{Type: "source", ID: id},
			Details: details}); err != nil {
			return err
		}
		// Precedence changes affect every entity the source contributes to.
		if in.Policy != nil && rawJSON(pol) != rawJSON(cur.Policy) {
			return s.recomputeSourceEntitiesTx(ctx, tx, id, actor)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Source(ctx, id)
}

// SourceSecrets decrypts a source's credentials (providers only; never returned by the API).
func (s *Service) SourceSecrets(src *Source) (map[string]string, error) {
	out := map[string]string{}
	if len(src.secretsEnc) == 0 {
		return out, nil
	}
	pt, err := s.Keyring.Open(keyring.PurposeField, secretsAAD(src.ID), src.secretsEnc)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(pt, &out)
	return out, err
}

// DeleteSource deletes a non-system source and every fact it owns. Entities left without facets are tombstoned;
// entities with facets from other sources keep them (ADR-003 ownership scoping).
func (s *Service) DeleteSource(ctx context.Context, actor audit.Actor, id string) error {
	return s.Store.W().TxNamed(ctx, "catalog.source_delete", func(tx *store.Tx) error {
		cur, err := SourceTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.System {
			return apperr.New(apperr.CodeForbidden, "system sources cannot be deleted")
		}
		entities, err := stringsQuery(ctx, tx, `SELECT DISTINCT entity_id FROM facets WHERE source_id = ?`, id)
		if err != nil {
			return err
		}
		relations, err := stringsQuery(ctx, tx, `SELECT DISTINCT relation_id FROM relation_assertions WHERE source_id = ?`, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE source_id = ? AND revoked_at IS NULL`, tx.NowMicros(), id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sources WHERE id = ?`, id); err != nil {
			return err
		}
		for _, eid := range entities {
			if _, err := s.recomputeTx(ctx, tx, eid, recomputeOpts{actor: actor, sourceID: id}); err != nil {
				return err
			}
		}
		for _, rid := range relations {
			if err := s.recomputeRelationTx(ctx, tx, rid, actor, id); err != nil {
				return err
			}
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: cur.ProjectID, Action: "source.deleted", Target: audit.Target{Type: "source", ID: id},
			Details: map[string]any{"name": cur.Name, "entities_affected": len(entities), "relations_affected": len(relations)}}); err != nil {
			return err
		}
		if err := s.changeTx(ctx, tx, change{project: cur.ProjectID, typ: "source", id: id, action: "deleted", actor: actor, summary: map[string]any{"name": cur.Name}}); err != nil {
			return err
		}
		_, err = outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "source", Action: "deleted", ProjectID: cur.ProjectID, Payload: map[string]any{"source_id": id, "name": cur.Name}})
		return err
	})
}

func (s *Service) recomputeSourceEntitiesTx(ctx context.Context, tx *store.Tx, sourceID string, actor audit.Actor) error {
	entities, err := stringsQuery(ctx, tx, `SELECT entity_id FROM facets WHERE source_id = ? AND tombstoned_at IS NULL`, sourceID)
	if err != nil {
		return err
	}
	for _, eid := range entities {
		if _, err := s.recomputeTx(ctx, tx, eid, recomputeOpts{actor: actor, sourceID: sourceID}); err != nil {
			return err
		}
	}
	return nil
}

// RecordRun updates a source's run bookkeeping.
func RecordRun(ctx context.Context, tx *store.Tx, sourceID string, ok bool, errMsg string, generation int64) error {
	if len(errMsg) > 1000 {
		errMsg = errMsg[:1000]
	}
	if ok {
		_, err := tx.Exec(ctx, `UPDATE sources SET last_run_at = ?, last_success_at = ?, last_error = NULL,
			last_generation = MAX(last_generation, ?) WHERE id = ?`, tx.NowMicros(), tx.NowMicros(), generation, sourceID)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE sources SET last_run_at = ?, last_error = ? WHERE id = ?`, tx.NowMicros(), errMsg, sourceID)
	return err
}

// Heartbeat records liveness of a push source.
func (s *Service) Heartbeat(ctx context.Context, sourceID string) error {
	return s.Store.W().TxNamed(ctx, "catalog.source_heartbeat", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sources SET last_heartbeat_at = ? WHERE id = ?`, tx.NowMicros(), sourceID)
		return err
	})
}

func stringsQuery(ctx context.Context, tx *store.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SourceLabel renders "project/name" for diagnostics.
func SourceLabel(s *Source) string { return fmt.Sprintf("%s/%s", s.ProjectID, s.Name) }
