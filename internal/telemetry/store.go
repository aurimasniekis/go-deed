package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Integration is a configured connection to a telemetry system (installation-wide, admin-managed).
type Integration struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Adapter         string          `json:"adapter"`
	Config          json.RawMessage `json:"config"`
	HasSecrets      bool            `json:"has_secrets"`
	Enabled         bool            `json:"enabled"`
	LastTestAt      *time.Time      `json:"last_test_at,omitempty"`
	LastTestOK      *bool           `json:"last_test_ok,omitempty"`
	LastError       string          `json:"last_error,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ResourceVersion int64           `json:"resource_version"`
	secretsEnc      []byte
}

// IntegrationInput creates or updates an integration. Secrets: nil keeps the stored ones, an empty map clears them.
type IntegrationInput struct {
	Name    string            `json:"name,omitempty" maxLength:"63"`
	Adapter string            `json:"adapter,omitempty" maxLength:"32"`
	Config  json.RawMessage   `json:"config,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty" doc:"Write-only credentials (keys from the adapter's secret_fields). Omit to keep, {} to clear."`
}

// Param is a typed binding parameter. Values come from the entity (From, a JSON pointer into the entity envelope such
// as /spec/hostname or /metadata/labels/app; edge-rate bindings prefix from: or to:) or from Default.
type BindingParam struct {
	Name     string `json:"name" pattern:"^[a-z][a-z0-9_]{0,31}$"`
	Type     string `json:"type" enum:"label,string,int,duration,subject"`
	From     string `json:"from,omitempty" maxLength:"256"`
	Default  string `json:"default,omitempty" maxLength:"256"`
	Required bool   `json:"required,omitempty"`
	Pattern  string `json:"pattern,omitempty" maxLength:"200" doc:"Optional extra RE2 the value must match."`
}

// Thresholds turn a scalar into a telemetry health state (role health).
type BindingThresholds struct {
	Warning   *float64 `json:"warning,omitempty"`
	Critical  *float64 `json:"critical,omitempty"`
	Direction string   `json:"direction,omitempty" enum:"above,below," doc:"above (default): values above a threshold are bad."`
}

// Binding is a named, admin-authored query template.
type Binding struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Title            string            `json:"title"`
	IntegrationID    string            `json:"integration_id"`
	Kinds            []string          `json:"kinds" doc:"Entity kinds the binding applies to (empty = every kind)."`
	Query            string            `json:"query,omitempty" doc:"Template with {{param}} placeholders and the built-ins {{window}} and {{step}}. Returned to admins only."`
	Params           []BindingParam    `json:"params"`
	Shape            string            `json:"shape"`
	Unit             string            `json:"unit"`
	Thresholds       BindingThresholds `json:"thresholds"`
	FreshnessSeconds int               `json:"freshness_seconds"`
	Role             string            `json:"role"`
	RelationshipType string            `json:"relationship_type,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	ResourceVersion  int64             `json:"resource_version"`
}

// BindingInput creates or updates a binding.
type BindingInput struct {
	Name             string             `json:"name,omitempty" maxLength:"63"`
	Title            string             `json:"title,omitempty" maxLength:"128"`
	Integration      string             `json:"integration,omitempty" maxLength:"64" doc:"Integration ID or name."`
	Kinds            []string           `json:"kinds,omitempty" maxItems:"50"`
	Query            string             `json:"query,omitempty" maxLength:"8192"`
	Params           []BindingParam     `json:"params,omitempty" maxItems:"16"`
	Shape            string             `json:"shape,omitempty" enum:"scalar,timeseries,table,link,"`
	Unit             *string            `json:"unit,omitempty" maxLength:"32"`
	Thresholds       *BindingThresholds `json:"thresholds,omitempty"`
	FreshnessSeconds int                `json:"freshness_seconds,omitempty" minimum:"0" maximum:"86400"`
	Role             string             `json:"role,omitempty" enum:"chart,health,edge_rate,link,"`
	RelationshipType *string            `json:"relationship_type,omitempty" maxLength:"64"`
}

// Store persists integrations and bindings.
type Store struct {
	Store    *store.Store
	Keyring  *keyring.Keyring
	Audit    *audit.Writer
	Registry *Registry
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrIntegrationNotFound / ErrBindingNotFound are returned for unknown references.
var (
	ErrIntegrationNotFound = apperr.New(apperr.CodeNotFound, "integration not found")
	ErrBindingNotFound     = apperr.New(apperr.CodeNotFound, "binding not found")
)

func integrationAAD(id string) []byte { return keyring.FieldAAD("integrations", "secrets_enc", id) }

const integrationCols = `id, name, adapter, config_json, secrets_enc, enabled, last_test_at, last_test_ok, COALESCE(last_error, ''), created_at, updated_at, version`

func scanIntegration(r interface{ Scan(...any) error }) (*Integration, error) {
	var it Integration
	var cfg string
	var lt, lok sql.NullInt64
	var c, u int64
	if err := r.Scan(&it.ID, &it.Name, &it.Adapter, &cfg, &it.secretsEnc, &it.Enabled, &lt, &lok, &it.LastError, &c, &u, &it.ResourceVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIntegrationNotFound
		}
		return nil, err
	}
	it.Config = json.RawMessage(cfg)
	it.HasSecrets = len(it.secretsEnc) > 0
	if lt.Valid {
		t := clock.FromMicros(lt.Int64)
		it.LastTestAt = &t
	}
	if lok.Valid {
		ok := lok.Int64 == 1
		it.LastTestOK = &ok
	}
	it.CreatedAt, it.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &it, nil
}

// Integrations lists integrations.
func (s *Store) Integrations(ctx context.Context) ([]*Integration, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT `+integrationCols+` FROM integrations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Integration{}
	for rows.Next() {
		it, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Integration returns an integration by ID or name.
func (s *Store) Integration(ctx context.Context, idOrName string) (*Integration, error) {
	return scanIntegration(s.Store.R().QueryRowContext(ctx, `SELECT `+integrationCols+` FROM integrations WHERE id = ? OR name = ?`, idOrName, idOrName))
}

// Secrets opens an integration's credentials.
func (s *Store) Secrets(it *Integration) (map[string]string, error) {
	out := map[string]string{}
	if len(it.secretsEnc) == 0 {
		return out, nil
	}
	pt, err := s.Keyring.Open(keyring.PurposeField, integrationAAD(it.ID), it.secretsEnc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(pt, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) validateIntegration(in *IntegrationInput, secrets map[string]string) error {
	v := &apperr.Validation{}
	if !nameRE.MatchString(in.Name) {
		v.Add("name", "must be a lowercase slug")
	}
	a, ok := s.Registry.Get(in.Adapter)
	if !ok {
		v.Add("adapter", "unknown adapter %q", in.Adapter)
		return v.Err()
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	if err := s.Registry.ValidateConfig(in.Adapter, in.Config); err != nil {
		return err
	}
	allowed := a.Info().SecretFields
	for k, val := range secrets {
		if !slices.Contains(allowed, k) {
			v.Add("secrets."+k, "unknown secret field (allowed: %s)", strings.Join(allowed, ", "))
		}
		if len(val) > 8192 {
			v.Add("secrets."+k, "too long")
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	return a.Validate(in.Config, secrets)
}

func (s *Store) seal(id string, secrets map[string]string) ([]byte, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	b, _ := json.Marshal(secrets)
	return s.Keyring.Seal(keyring.PurposeField, integrationAAD(id), b)
}

// CreateIntegration stores a new integration.
func (s *Store) CreateIntegration(ctx context.Context, actor audit.Actor, in IntegrationInput) (*Integration, error) {
	if err := s.validateIntegration(&in, in.Secrets); err != nil {
		return nil, err
	}
	id := ids.New(ids.Integration)
	enc, err := s.seal(id, in.Secrets)
	if err != nil {
		return nil, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	err = s.Store.W().TxNamed(ctx, "telemetry.integration_create", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO integrations (id, name, adapter, config_json, secrets_enc, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.Name, in.Adapter, compact(in.Config), enc, enabled, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "an integration named %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "integration.created", Target: audit.Target{Type: "integration", ID: id},
			Details: map[string]any{"name": in.Name, "adapter": in.Adapter, "with_secrets": enc != nil}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Integration(ctx, id)
}

// UpdateIntegration changes an integration (optimistic concurrency with version > 0).
func (s *Store) UpdateIntegration(ctx context.Context, actor audit.Actor, idOrName string, version int64, in IntegrationInput) (*Integration, error) {
	cur, err := s.Integration(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	if version > 0 && version != cur.ResourceVersion {
		return nil, apperr.New(apperr.CodePrecondition, "the integration changed since it was read")
	}
	if in.Adapter != "" && in.Adapter != cur.Adapter {
		return nil, apperr.New(apperr.CodeInvalid, "the adapter of an integration cannot change")
	}
	in.Adapter = cur.Adapter
	if in.Name == "" {
		in.Name = cur.Name
	}
	if len(in.Config) == 0 {
		in.Config = cur.Config
	}
	secrets := in.Secrets
	if secrets == nil {
		if secrets, err = s.Secrets(cur); err != nil {
			return nil, err
		}
	}
	if err := s.validateIntegration(&in, secrets); err != nil {
		return nil, err
	}
	enc, err := s.seal(cur.ID, secrets)
	if err != nil {
		return nil, err
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	err = s.Store.W().TxNamed(ctx, "telemetry.integration_update", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE integrations SET name = ?, config_json = ?, secrets_enc = ?, enabled = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ?`, in.Name, compact(in.Config), enc, enabled, tx.NowMicros(), cur.ID, cur.ResourceVersion)
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "an integration named %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "the integration changed since it was read")
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "integration.updated", Target: audit.Target{Type: "integration", ID: cur.ID},
			Details: map[string]any{"name": in.Name, "secrets_changed": in.Secrets != nil, "enabled": enabled}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Integration(ctx, cur.ID)
}

// DeleteIntegration removes an integration and its bindings.
func (s *Store) DeleteIntegration(ctx context.Context, actor audit.Actor, idOrName string) error {
	cur, err := s.Integration(ctx, idOrName)
	if err != nil {
		return err
	}
	return s.Store.W().TxNamed(ctx, "telemetry.integration_delete", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM integrations WHERE id = ?`, cur.ID); err != nil {
			return err
		}
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "integration.deleted", Target: audit.Target{Type: "integration", ID: cur.ID},
			Details: map[string]any{"name": cur.Name}})
		return err
	})
}

// RecordTest stores a connectivity test outcome.
func (s *Store) RecordTest(ctx context.Context, id string, ok bool, msg string) error {
	return s.Store.W().TxNamed(ctx, "telemetry.integration_test", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE integrations SET last_test_at = ?, last_test_ok = ?, last_error = ? WHERE id = ?`, tx.NowMicros(), ok, msg, id)
		return err
	})
}

const bindingCols = `id, name, title, integration_id, kinds_json, query, params_json, shape, unit, thresholds_json, freshness_seconds, role,
	relationship_type, created_at, updated_at, version`

func scanBinding(r interface{ Scan(...any) error }) (*Binding, error) {
	var b Binding
	var kinds, params, th string
	var c, u int64
	if err := r.Scan(&b.ID, &b.Name, &b.Title, &b.IntegrationID, &kinds, &b.Query, &params, &b.Shape, &b.Unit, &th, &b.FreshnessSeconds, &b.Role,
		&b.RelationshipType, &c, &u, &b.ResourceVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBindingNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(kinds), &b.Kinds)
	_ = json.Unmarshal([]byte(params), &b.Params)
	_ = json.Unmarshal([]byte(th), &b.Thresholds)
	if b.Kinds == nil {
		b.Kinds = []string{}
	}
	if b.Params == nil {
		b.Params = []BindingParam{}
	}
	b.CreatedAt, b.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &b, nil
}

// Bindings lists bindings.
func (s *Store) Bindings(ctx context.Context) ([]*Binding, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT `+bindingCols+` FROM bindings ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Binding returns a binding by ID or name.
func (s *Store) Binding(ctx context.Context, idOrName string) (*Binding, error) {
	return scanBinding(s.Store.R().QueryRowContext(ctx, `SELECT `+bindingCols+` FROM bindings WHERE id = ? OR name = ?`, idOrName, idOrName))
}

// validateBinding normalizes and checks a binding against its integration's adapter.
func (s *Store) validateBinding(ctx context.Context, b *Binding) error {
	v := &apperr.Validation{}
	if !nameRE.MatchString(b.Name) {
		v.Add("name", "must be a lowercase slug")
	}
	if b.Title == "" {
		b.Title = b.Name
	}
	it, err := s.Integration(ctx, b.IntegrationID)
	if err != nil {
		v.Add("integration", "unknown integration")
		return v.Err()
	}
	b.IntegrationID = it.ID
	a, ok := s.Registry.Get(it.Adapter)
	if !ok {
		v.Add("integration", "adapter %q is not available", it.Adapter)
		return v.Err()
	}
	if b.Shape == "" {
		b.Shape = ShapeScalar
	}
	if !slices.Contains(a.Info().Shapes, b.Shape) {
		v.Add("shape", "adapter %s supports %s", it.Adapter, strings.Join(a.Info().Shapes, ", "))
	}
	if b.Role == "" {
		b.Role = "chart"
		if b.Shape == ShapeLink {
			b.Role = "link"
		}
	}
	switch b.Role {
	case "chart", "link":
	case "health":
		if b.Shape != ShapeScalar {
			v.Add("shape", "health bindings must return a scalar")
		}
		if b.Thresholds.Warning == nil && b.Thresholds.Critical == nil {
			v.Add("thresholds", "health bindings need a warning or critical threshold")
		}
	case "edge_rate":
		if b.Shape != ShapeScalar {
			v.Add("shape", "edge-rate bindings must return a scalar (a rate)")
		}
		if b.RelationshipType == "" {
			v.Add("relationship_type", "edge-rate bindings name the relationship type they measure")
		}
	default:
		v.Add("role", "must be chart, health, edge_rate or link")
	}
	if b.Role == "link" && b.Shape != ShapeLink {
		v.Add("shape", "link bindings must use the link shape")
	}
	if b.Thresholds.Direction == "" {
		b.Thresholds.Direction = "above"
	}
	if b.FreshnessSeconds == 0 {
		b.FreshnessSeconds = 300
	}
	if b.FreshnessSeconds < 15 {
		v.Add("freshness_seconds", "must be at least 15")
	}
	if len(b.Kinds) > 50 {
		v.Add("kinds", "at most 50 kinds")
	}
	seen := map[string]bool{}
	for i, p := range b.Params {
		f := fmt.Sprintf("params[%d]", i)
		if !paramNameRE.MatchString(p.Name) || builtins[p.Name] {
			v.Add(f+".name", "must be a lowercase identifier and not window or step")
		}
		if seen[p.Name] {
			v.Add(f+".name", "duplicate parameter")
		}
		seen[p.Name] = true
		if _, ok := paramTypes[p.Type]; !ok {
			v.Add(f+".type", "must be label, string, int, duration or subject")
		}
		if p.From == "" && p.Default == "" {
			v.Add(f, "needs from (an entity pointer) or a default")
		}
		if p.From != "" {
			ptr := p.From
			if b.Role == "edge_rate" {
				end, rest, ok := strings.Cut(ptr, ":")
				if !ok || (end != "from" && end != "to") {
					v.Add(f+".from", "edge-rate parameters read from:/pointer or to:/pointer")
				}
				ptr = rest
			}
			if !strings.HasPrefix(ptr, "/") || len(ptr) > 256 {
				v.Add(f+".from", "must be a JSON pointer into the entity (e.g. /spec/hostname)")
			}
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				v.Add(f+".pattern", "invalid RE2 expression")
			}
		}
		if p.Default != "" {
			if err := checkParam(p, p.Default); err != nil {
				v.Add(f+".default", "%s", err.Error())
			}
		}
	}
	if b.Query == "" {
		v.Add("query", "required")
	}
	for _, ph := range placeholders(b.Query) {
		if !seen[ph] && !builtins[ph] {
			v.Add("query", "unknown placeholder {{%s}}", ph)
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	// Static adapter checks on a sample rendering (every parameter filled with a harmless value).
	sample, _ := render(b.Query, func(name string) (string, bool) { return a.Escape(sampleValue(b.Params, name)), true })
	if err := a.ValidateQuery(sample, b.Shape); err != nil {
		return apperr.Wrap(apperr.CodeInvalid, err, "query: %s", err.Error())
	}
	return nil
}

func (b *Binding) apply(in BindingInput) {
	if in.Name != "" {
		b.Name = in.Name
	}
	if in.Title != "" {
		b.Title = in.Title
	}
	if in.Integration != "" {
		b.IntegrationID = in.Integration
	}
	if in.Kinds != nil {
		b.Kinds = in.Kinds
	}
	if in.Query != "" {
		b.Query = in.Query
	}
	if in.Params != nil {
		b.Params = in.Params
	}
	if in.Shape != "" {
		b.Shape = in.Shape
	}
	if in.Unit != nil {
		b.Unit = *in.Unit
	}
	if in.Thresholds != nil {
		b.Thresholds = *in.Thresholds
	}
	if in.FreshnessSeconds != 0 {
		b.FreshnessSeconds = in.FreshnessSeconds
	}
	if in.Role != "" {
		b.Role = in.Role
	}
	if in.RelationshipType != nil {
		b.RelationshipType = *in.RelationshipType
	}
}

// CreateBinding stores a new binding.
func (s *Store) CreateBinding(ctx context.Context, actor audit.Actor, in BindingInput) (*Binding, error) {
	b := &Binding{Kinds: []string{}, Params: []BindingParam{}}
	b.apply(in)
	if err := s.validateBinding(ctx, b); err != nil {
		return nil, err
	}
	b.ID = ids.New(ids.Binding)
	err := s.Store.W().TxNamed(ctx, "telemetry.binding_create", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO bindings (id, name, title, integration_id, kinds_json, query, params_json, shape, unit, thresholds_json,
			freshness_seconds, role, relationship_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.Name, b.Title, b.IntegrationID, js(b.Kinds), b.Query, js(b.Params), b.Shape, b.Unit, js(b.Thresholds), b.FreshnessSeconds, b.Role,
			b.RelationshipType, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "a binding named %q already exists", b.Name)
		}
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "binding.created", Target: audit.Target{Type: "binding", ID: b.ID},
			Details: map[string]any{"name": b.Name, "integration_id": b.IntegrationID, "role": b.Role}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Binding(ctx, b.ID)
}

// UpdateBinding changes a binding.
func (s *Store) UpdateBinding(ctx context.Context, actor audit.Actor, idOrName string, version int64, in BindingInput) (*Binding, error) {
	b, err := s.Binding(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	if version > 0 && version != b.ResourceVersion {
		return nil, apperr.New(apperr.CodePrecondition, "the binding changed since it was read")
	}
	cur := b.ResourceVersion
	b.apply(in)
	if err := s.validateBinding(ctx, b); err != nil {
		return nil, err
	}
	err = s.Store.W().TxNamed(ctx, "telemetry.binding_update", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE bindings SET name = ?, title = ?, integration_id = ?, kinds_json = ?, query = ?, params_json = ?, shape = ?, unit = ?,
			thresholds_json = ?, freshness_seconds = ?, role = ?, relationship_type = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
			b.Name, b.Title, b.IntegrationID, js(b.Kinds), b.Query, js(b.Params), b.Shape, b.Unit, js(b.Thresholds), b.FreshnessSeconds, b.Role,
			b.RelationshipType, tx.NowMicros(), b.ID, cur)
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "a binding named %q already exists", b.Name)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "the binding changed since it was read")
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "binding.updated", Target: audit.Target{Type: "binding", ID: b.ID},
			Details: map[string]any{"name": b.Name}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Binding(ctx, b.ID)
}

// DeleteBinding removes a binding.
func (s *Store) DeleteBinding(ctx context.Context, actor audit.Actor, idOrName string) error {
	b, err := s.Binding(ctx, idOrName)
	if err != nil {
		return err
	}
	return s.Store.W().TxNamed(ctx, "telemetry.binding_delete", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM bindings WHERE id = ?`, b.ID); err != nil {
			return err
		}
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "binding.deleted", Target: audit.Target{Type: "binding", ID: b.ID},
			Details: map[string]any{"name": b.Name}})
		return err
	})
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "{}"
	}
	return js(v)
}
