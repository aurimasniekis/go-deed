package catalog

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// KindVersion is one published schema version of a kind.
type KindVersion struct {
	Version   string          `json:"version"`
	Schema    json.RawMessage `json:"schema"`
	Breaking  bool            `json:"breaking"`
	Migration string          `json:"migration,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Support describes what Deed can do for a kind (Q-06): it is computed from the mapping and provider registries,
// never implied by an icon or schema.
type Support struct {
	Modeling  bool     `json:"modeling"`
	Terraform []string `json:"terraform"` // Terraform resource types mapped to this kind
	Runtime   []string `json:"runtime"`   // runtime ingestion paths (push protocol, Pulse, agent providers)
	Native    []string `json:"native"`    // native in-process providers
}

// Kind is a kind definition.
type Kind struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Group           string        `json:"group"`
	Builtin         bool          `json:"builtin"`
	Title           string        `json:"title"`
	Plural          string        `json:"plural"`
	Description     string        `json:"description"`
	Icon            string        `json:"icon"`
	Color           string        `json:"color"`
	IdentityKeys    [][]string    `json:"identity_keys"`
	StorageVersion  string        `json:"storage_version"`
	Versions        []KindVersion `json:"versions"`
	Deprecated      bool          `json:"deprecated"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ResourceVersion int64         `json:"resource_version"`

	compiled map[string]*CompiledSchema
}

// Schema returns the compiled schema of version v ("" = storage version).
func (k *Kind) Schema(v string) *CompiledSchema {
	if v == "" {
		v = k.StorageVersion
	}
	return k.compiled[v]
}

// RelType is a relationship type definition.
type RelType struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	InverseName      string          `json:"inverse_name"`
	Title            string          `json:"title"`
	Description      string          `json:"description"`
	Directed         bool            `json:"directed"`
	FromKinds        []string        `json:"from_kinds"`
	ToKinds          []string        `json:"to_kinds"`
	DefaultEvidence  string          `json:"default_evidence"`
	Cardinality      string          `json:"cardinality"`
	AttributesSchema json.RawMessage `json:"attributes_schema,omitempty"`
	Impact           string          `json:"impact"`
	Color            string          `json:"color"`
	Builtin          bool            `json:"builtin"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ResourceVersion  int64           `json:"resource_version"`

	attrs *CompiledSchema
}

// registry caches kinds and relationship types; reloaded after every committed change.
type registry struct {
	mu    sync.RWMutex
	kinds map[string]*Kind
	rels  map[string]*RelType
}

func (r *registry) kind(name string) *Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.kinds[name]
}

func (r *registry) rel(name string) *RelType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rels[name]
}

// Kind returns a kind definition (nil when unknown).
func (s *Service) Kind(name string) *Kind { return s.reg.kind(name) }

// RelType returns a relationship type (nil when unknown).
func (s *Service) RelType(name string) *RelType { return s.reg.rel(name) }

// Kinds lists every kind ordered by group and name.
func (s *Service) Kinds() []*Kind {
	s.reg.mu.RLock()
	defer s.reg.mu.RUnlock()
	out := make([]*Kind, 0, len(s.reg.kinds))
	for _, k := range s.reg.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RelTypes lists every relationship type ordered by name.
func (s *Service) RelTypes() []*RelType {
	s.reg.mu.RLock()
	defer s.reg.mu.RUnlock()
	out := make([]*RelType, 0, len(s.reg.rels))
	for _, r := range s.reg.rels {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reload rebuilds the registry from the database.
func (s *Service) Reload(ctx context.Context) error {
	kinds, err := loadKinds(ctx, s.Store.R())
	if err != nil {
		return err
	}
	rels, err := loadRelTypes(ctx, s.Store.R())
	if err != nil {
		return err
	}
	s.reg.mu.Lock()
	s.reg.kinds, s.reg.rels = kinds, rels
	s.reg.mu.Unlock()
	return nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadKinds(ctx context.Context, q querier) (map[string]*Kind, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, group_name, builtin, title, plural, description, icon, color, identity_keys,
		storage_version, deprecated, created_at, updated_at, version FROM kind_definitions`)
	if err != nil {
		return nil, err
	}
	out := map[string]*Kind{}
	byID := map[string]*Kind{}
	for rows.Next() {
		var k Kind
		var ik string
		var c, u int64
		if err := rows.Scan(&k.ID, &k.Name, &k.Group, &k.Builtin, &k.Title, &k.Plural, &k.Description, &k.Icon, &k.Color, &ik,
			&k.StorageVersion, &k.Deprecated, &c, &u, &k.ResourceVersion); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(ik), &k.IdentityKeys)
		if k.IdentityKeys == nil {
			k.IdentityKeys = [][]string{}
		}
		k.CreatedAt, k.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
		k.compiled = map[string]*CompiledSchema{}
		k.Versions = []KindVersion{}
		out[k.Name] = &k
		byID[k.ID] = &k
	}
	rows.Close()
	vrows, err := q.QueryContext(ctx, `SELECT kind_id, version, schema_json, breaking, migration, created_at FROM kind_versions`)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var kid, schema string
		var v KindVersion
		var c int64
		if err := vrows.Scan(&kid, &v.Version, &schema, &v.Breaking, &v.Migration, &c); err != nil {
			return nil, err
		}
		k := byID[kid]
		if k == nil {
			continue
		}
		v.Schema = json.RawMessage(schema)
		v.CreatedAt = clock.FromMicros(c)
		cs, err := ParseSchema([]byte(schema))
		if err != nil {
			return nil, fmt.Errorf("kind %s %s: stored schema no longer compiles: %w", k.Name, v.Version, err)
		}
		k.compiled[v.Version] = cs
		k.Versions = append(k.Versions, v)
	}
	for _, k := range out {
		sort.Slice(k.Versions, func(i, j int) bool { return versionNum(k.Versions[i].Version) < versionNum(k.Versions[j].Version) })
	}
	return out, vrows.Err()
}

func loadRelTypes(ctx context.Context, q querier) (map[string]*RelType, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, inverse_name, title, description, directed, from_kinds, to_kinds, default_evidence,
		cardinality, attributes_schema, impact, color, builtin, created_at, updated_at, version FROM relationship_types`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*RelType{}
	for rows.Next() {
		var r RelType
		var fk, tk, as string
		var c, u int64
		if err := rows.Scan(&r.ID, &r.Name, &r.InverseName, &r.Title, &r.Description, &r.Directed, &fk, &tk, &r.DefaultEvidence,
			&r.Cardinality, &as, &r.Impact, &r.Color, &r.Builtin, &c, &u, &r.ResourceVersion); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(fk), &r.FromKinds)
		_ = json.Unmarshal([]byte(tk), &r.ToKinds)
		if r.FromKinds == nil {
			r.FromKinds = []string{}
		}
		if r.ToKinds == nil {
			r.ToKinds = []string{}
		}
		if as != "" {
			r.AttributesSchema = json.RawMessage(as)
			if cs, err := ParseSchema([]byte(as)); err == nil {
				r.attrs = cs
			}
		}
		r.CreatedAt, r.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
		out[r.Name] = &r
	}
	return out, rows.Err()
}

var versionRE = regexp.MustCompile(`^v([1-9][0-9]{0,3})$`)

func versionNum(v string) int {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

type builtinKinds struct {
	Kinds []struct {
		Name         string     `yaml:"name"`
		Group        string     `yaml:"group"`
		Title        string     `yaml:"title"`
		Plural       string     `yaml:"plural"`
		Icon         string     `yaml:"icon"`
		Description  string     `yaml:"description"`
		IdentityKeys [][]string `yaml:"identity_keys"`
		Versions     []struct {
			Version string         `yaml:"version"`
			Schema  map[string]any `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"kinds"`
}

type builtinRels struct {
	RelationshipTypes []struct {
		Name            string   `yaml:"name"`
		Title           string   `yaml:"title"`
		InverseName     string   `yaml:"inverse_name"`
		Description     string   `yaml:"description"`
		Impact          string   `yaml:"impact"`
		DefaultEvidence string   `yaml:"default_evidence"`
		FromKinds       []string `yaml:"from_kinds"`
		ToKinds         []string `yaml:"to_kinds"`
		Color           string   `yaml:"color"`
	} `yaml:"relationship_types"`
}

// SeedBuiltins inserts or updates built-in kinds and relationship types from the embedded catalog. Built-in
// definitions are owned by the binary: their display fields follow the catalog and new schema versions are added.
func (s *Service) SeedBuiltins(ctx context.Context) error {
	var bk builtinKinds
	var br builtinRels
	for name, dst := range map[string]any{"builtin/kinds.yaml": &bk, "builtin/reltypes.yaml": &br} {
		b, err := builtinFS.ReadFile(name)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(b, dst); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	err := s.Store.W().TxNamed(ctx, "catalog.seed_builtins", func(tx *store.Tx) error {
		for _, k := range bk.Kinds {
			ik, _ := json.Marshal(k.IdentityKeys)
			if k.IdentityKeys == nil {
				ik = []byte("[]")
			}
			last := k.Versions[len(k.Versions)-1].Version
			var id string
			err := tx.QueryRow(ctx, `SELECT id FROM kind_definitions WHERE name = ?`, k.Name).Scan(&id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				id = ids.New(ids.Kind)
				if _, err := tx.Exec(ctx, `INSERT INTO kind_definitions (id, name, group_name, builtin, title, plural, description, icon, identity_keys,
					storage_version, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`, id, k.Name, k.Group, k.Title, k.Plural,
					k.Description, k.Icon, string(ik), last, tx.NowMicros(), tx.NowMicros()); err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if _, err := tx.Exec(ctx, `UPDATE kind_definitions SET group_name = ?, title = ?, plural = ?, description = ?, icon = ?, identity_keys = ?,
					storage_version = ?, builtin = 1 WHERE id = ? AND (group_name, title, plural, description, icon, identity_keys, storage_version)
					IS NOT (?, ?, ?, ?, ?, ?, ?)`, k.Group, k.Title, k.Plural, k.Description, k.Icon, string(ik), last, id,
					k.Group, k.Title, k.Plural, k.Description, k.Icon, string(ik), last); err != nil {
					return err
				}
			}
			for _, v := range k.Versions {
				sb, _ := json.Marshal(v.Schema)
				if _, err := ParseSchema(sb); err != nil {
					return fmt.Errorf("builtin kind %s %s: %w", k.Name, v.Version, err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO kind_versions (kind_id, version, schema_json, created_at) VALUES (?, ?, ?, ?)
					ON CONFLICT (kind_id, version) DO UPDATE SET schema_json = excluded.schema_json`, id, v.Version, string(sb), tx.NowMicros()); err != nil {
					return err
				}
			}
		}
		for _, r := range br.RelationshipTypes {
			fk, _ := json.Marshal(nonNil(r.FromKinds))
			tk, _ := json.Marshal(nonNil(r.ToKinds))
			if _, err := tx.Exec(ctx, `INSERT INTO relationship_types (id, name, inverse_name, title, description, from_kinds, to_kinds,
				default_evidence, impact, color, builtin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
				ON CONFLICT (name) DO UPDATE SET inverse_name = excluded.inverse_name, title = excluded.title, description = excluded.description,
				from_kinds = excluded.from_kinds, to_kinds = excluded.to_kinds, default_evidence = excluded.default_evidence,
				impact = excluded.impact, color = excluded.color, builtin = 1`,
				ids.New(ids.RelType), r.Name, r.InverseName, r.Title, r.Description, string(fk), string(tk), r.DefaultEvidence, r.Impact, r.Color,
				tx.NowMicros(), tx.NowMicros()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.Reload(ctx)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// KindInput creates or updates a custom kind.
type KindInput struct {
	Name         string     `json:"name,omitempty"`
	Group        string     `json:"group,omitempty"`
	Title        string     `json:"title"`
	Plural       string     `json:"plural,omitempty"`
	Description  string     `json:"description,omitempty"`
	Icon         string     `json:"icon,omitempty"`
	Color        string     `json:"color,omitempty"`
	IdentityKeys [][]string `json:"identity_keys,omitempty"`
	Deprecated   bool       `json:"deprecated,omitempty"`
}

var colorRE = regexp.MustCompile(`^(#[0-9a-fA-F]{6})?$`)
var groupRE = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,62}$`)

func validateKindInput(in *KindInput, creating bool) error {
	v := &apperr.Validation{}
	if creating && !ValidKindName(in.Name) {
		v.Add("name", "must be a lowercase identifier, optionally prefixed by a domain (custom.example.com/widget)")
	}
	if in.Group == "" {
		in.Group = "custom"
	}
	if !groupRE.MatchString(in.Group) {
		v.Add("group", "must be a lowercase identifier")
	}
	checkString(in.Title, 100, false, "title", v)
	if strings.TrimSpace(in.Title) == "" {
		v.Add("title", "is required")
	}
	if in.Plural == "" {
		in.Plural = in.Title + "s"
	}
	checkString(in.Plural, 100, false, "plural", v)
	checkString(in.Description, 2000, true, "description", v)
	if in.Icon != "" && !iconRE.MatchString(in.Icon) {
		v.Add("icon", "must be pack:name")
	}
	if !colorRE.MatchString(in.Color) {
		v.Add("color", "must be #rrggbb")
	}
	if len(in.IdentityKeys) > 8 {
		v.Add("identity_keys", "at most 8 keys")
	}
	for i, key := range in.IdentityKeys {
		if len(key) == 0 || len(key) > 4 {
			v.Add(fmt.Sprintf("identity_keys[%d]", i), "must list 1–4 JSON pointers")
		}
		for _, p := range key {
			if !strings.HasPrefix(p, "/spec/") && !strings.HasPrefix(p, "/metadata/") {
				v.Add(fmt.Sprintf("identity_keys[%d]", i), "pointers must start with /spec/ or /metadata/")
			}
		}
	}
	if in.IdentityKeys == nil {
		in.IdentityKeys = [][]string{}
	}
	return v.Err()
}

// CreateKind creates a custom kind with its first schema version.
func (s *Service) CreateKind(ctx context.Context, actor audit.Actor, in KindInput, schema json.RawMessage) (*Kind, error) {
	if err := validateKindInput(&in, true); err != nil {
		return nil, err
	}
	if s.reg.kind(in.Name) != nil {
		return nil, apperr.New(apperr.CodeAlreadyExists, "kind %q already exists", in.Name)
	}
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
	if _, err := ParseSchema(schema); err != nil {
		return nil, err
	}
	id := ids.New(ids.Kind)
	ik, _ := json.Marshal(in.IdentityKeys)
	err := s.Store.W().TxNamed(ctx, "catalog.kind_create", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO kind_definitions (id, name, group_name, title, plural, description, icon, color, identity_keys,
			storage_version, deprecated, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'v1', ?, ?, ?)`, id, in.Name, in.Group, in.Title,
			in.Plural, in.Description, in.Icon, in.Color, string(ik), in.Deprecated, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "kind %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kind_versions (kind_id, version, schema_json, created_at) VALUES (?, 'v1', ?, ?)`,
			id, compactJSON(schema), tx.NowMicros()); err != nil {
			return err
		}
		return s.kindChangedTx(ctx, tx, actor, "kind.created", in.Name, map[string]any{"version": "v1"})
	})
	if err != nil {
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.reg.kind(in.Name), nil
}

// UpdateKind changes display fields and identity keys of a custom kind (If-Match on resource version).
func (s *Service) UpdateKind(ctx context.Context, actor audit.Actor, name string, in KindInput, ifMatch int64) (*Kind, error) {
	k := s.reg.kind(name)
	if k == nil {
		return nil, apperr.New(apperr.CodeNotFound, "kind %q not found", name)
	}
	if k.Builtin {
		return nil, apperr.New(apperr.CodeForbidden, "built-in kinds are managed by Deed and cannot be changed")
	}
	in.Name = name
	if err := validateKindInput(&in, false); err != nil {
		return nil, err
	}
	ik, _ := json.Marshal(in.IdentityKeys)
	err := s.Store.W().TxNamed(ctx, "catalog.kind_update", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE kind_definitions SET group_name = ?, title = ?, plural = ?, description = ?, icon = ?, color = ?,
			identity_keys = ?, deprecated = ?, updated_at = ?, version = version + 1 WHERE name = ? AND (? = 0 OR version = ?)`,
			in.Group, in.Title, in.Plural, in.Description, in.Icon, in.Color, string(ik), in.Deprecated, tx.NowMicros(), name, ifMatch, ifMatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "kind was modified concurrently; re-read it")
		}
		return s.kindChangedTx(ctx, tx, actor, "kind.updated", name, nil)
	})
	if err != nil {
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.reg.kind(name), nil
}

// PublishResult reports a new schema version.
type PublishResult struct {
	Kind                  *Kind    `json:"kind"`
	Version               string   `json:"version"`
	CompatibilityProblems []string `json:"compatibility_problems"`
	Revalidated           int      `json:"revalidated"`
	Invalid               int      `json:"invalid"`
}

// PublishVersion adds a schema version and makes it the storage version (ADR-002 evolution rules).
//   - non-breaking: must pass the structural compatibility check and every existing entity must validate;
//   - breaking + revalidate: every existing entity must validate, else the publication is refused;
//   - breaking + mark_invalid: publication proceeds; failing entities are marked invalid (findings), never dropped.
func (s *Service) PublishVersion(ctx context.Context, actor audit.Actor, name string, schema json.RawMessage, breaking bool, migration string, ifMatch int64) (*PublishResult, error) {
	k := s.reg.kind(name)
	if k == nil {
		return nil, apperr.New(apperr.CodeNotFound, "kind %q not found", name)
	}
	if k.Builtin {
		return nil, apperr.New(apperr.CodeForbidden, "built-in kinds are versioned by Deed releases")
	}
	cs, err := ParseSchema(schema)
	if err != nil {
		return nil, err
	}
	switch {
	case !breaking && migration != "":
		return nil, apperr.New(apperr.CodeInvalid, "migration applies only to breaking versions")
	case breaking && migration != "revalidate" && migration != "mark_invalid":
		return nil, apperr.New(apperr.CodeInvalid, "breaking versions need migration revalidate or mark_invalid")
	}
	old := k.Schema("")
	probs := CompatibilityProblems(old.Raw, cs.Raw)
	if probs == nil {
		probs = []string{}
	}
	if !breaking && len(probs) > 0 {
		return &PublishResult{Kind: k, CompatibilityProblems: probs}, apperr.New(apperr.CodeConflict,
			"the new schema is not backward compatible (%d problems); publish it with breaking=true and a migration mode", len(probs))
	}
	next := fmt.Sprintf("v%d", versionNum(k.StorageVersion)+1)
	res := &PublishResult{Version: next, CompatibilityProblems: probs}
	err = s.Store.W().TxNamed(ctx, "catalog.kind_publish", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE kind_definitions SET storage_version = ?, updated_at = ?, version = version + 1 WHERE id = ? AND (? = 0 OR version = ?)`,
			next, tx.NowMicros(), k.ID, ifMatch, ifMatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "kind was modified concurrently; re-read it")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kind_versions (kind_id, version, schema_json, breaking, migration, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			k.ID, next, compactJSON(schema), breaking, migration, tx.NowMicros()); err != nil {
			return err
		}
		checked, invalid, err := s.revalidateKindTx(ctx, tx, k.Name, next, cs)
		if err != nil {
			return err
		}
		res.Revalidated, res.Invalid = checked, invalid
		if invalid > 0 && migration != "mark_invalid" {
			return apperr.New(apperr.CodeConflict, "%d existing %s entities do not validate against %s; fix them or publish with breaking=true, migration=mark_invalid",
				invalid, k.Name, next)
		}
		return s.kindChangedTx(ctx, tx, actor, "kind.version_published", name, map[string]any{"version": next, "breaking": breaking,
			"migration": migration, "invalid": invalid})
	})
	if err != nil {
		if res.Invalid > 0 || len(probs) > 0 {
			return res, err
		}
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	res.Kind = s.reg.kind(name)
	return res, nil
}

// revalidateKindTx validates every live entity of a kind against a schema and records validity.
func (s *Service) revalidateKindTx(ctx context.Context, tx *store.Tx, kind, version string, cs *CompiledSchema) (checked, invalid int, err error) {
	rows, err := tx.Query(ctx, `SELECT id, effective_json FROM entities WHERE kind = ? AND tombstoned_at IS NULL`, kind)
	if err != nil {
		return 0, 0, err
	}
	type res struct {
		id    string
		errs  string
		valid bool
	}
	var out []res
	for rows.Next() {
		var id, eff string
		if err := rows.Scan(&id, &eff); err != nil {
			rows.Close()
			return 0, 0, err
		}
		spec, _ := decodeObject(eff)["spec"].(map[string]any)
		if spec == nil {
			spec = map[string]any{}
		}
		fe := cs.Validate(spec)
		out = append(out, res{id: id, errs: rawJSON(fe), valid: len(fe) == 0})
	}
	rows.Close()
	for _, r := range out {
		checked++
		if !r.valid {
			invalid++
		} else {
			r.errs = "[]"
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET valid = ?, validation_json = ?, schema_version = ? WHERE id = ?`, r.valid, r.errs, version, r.id); err != nil {
			return 0, 0, err
		}
	}
	return checked, invalid, nil
}

// DeleteKind removes a custom kind that no live entity uses.
func (s *Service) DeleteKind(ctx context.Context, actor audit.Actor, name string) error {
	k := s.reg.kind(name)
	if k == nil {
		return apperr.New(apperr.CodeNotFound, "kind %q not found", name)
	}
	if k.Builtin {
		return apperr.New(apperr.CodeForbidden, "built-in kinds cannot be deleted")
	}
	err := s.Store.W().TxNamed(ctx, "catalog.kind_delete", func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM entities WHERE kind = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.New(apperr.CodeConflict, "%d entities still use kind %q (including tombstones awaiting compaction)", n, name)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM kind_definitions WHERE id = ?`, k.ID); err != nil {
			return err
		}
		return s.kindChangedTx(ctx, tx, actor, "kind.deleted", name, nil)
	})
	if err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) kindChangedTx(ctx context.Context, tx *store.Tx, actor audit.Actor, action, name string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	details["kind"] = name
	if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: action, Target: audit.Target{Type: "kind", ID: name}, Details: details}); err != nil {
		return err
	}
	parts := strings.SplitN(action, ".", 2)
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: parts[0], Action: strings.ReplaceAll(parts[1], ".", "_"), Payload: details})
	return err
}

// RelTypeInput creates or updates a custom relationship type.
type RelTypeInput struct {
	Name             string          `json:"name,omitempty"`
	InverseName      string          `json:"inverse_name,omitempty"`
	Title            string          `json:"title"`
	Description      string          `json:"description,omitempty"`
	Directed         *bool           `json:"directed,omitempty"`
	FromKinds        []string        `json:"from_kinds,omitempty"`
	ToKinds          []string        `json:"to_kinds,omitempty"`
	DefaultEvidence  string          `json:"default_evidence,omitempty"`
	Cardinality      string          `json:"cardinality,omitempty"`
	AttributesSchema json.RawMessage `json:"attributes_schema,omitempty"`
	Impact           string          `json:"impact,omitempty"`
	Color            string          `json:"color,omitempty"`
}

func (s *Service) validateRelInput(in *RelTypeInput, creating bool) error {
	v := &apperr.Validation{}
	if creating && !relNameRE.MatchString(in.Name) {
		v.Add("name", "must be a lowercase identifier, optionally domain-prefixed")
	}
	if in.InverseName != "" && !relNameRE.MatchString(in.InverseName) {
		v.Add("inverse_name", "must be a lowercase identifier")
	}
	if strings.TrimSpace(in.Title) == "" {
		v.Add("title", "is required")
	}
	checkString(in.Title, 100, false, "title", v)
	checkString(in.Description, 2000, true, "description", v)
	for _, list := range [][]string{in.FromKinds, in.ToKinds} {
		for _, k := range list {
			if s.reg.kind(k) == nil {
				v.Add("kinds", "unknown kind %q", k)
			}
		}
	}
	if in.DefaultEvidence == "" {
		in.DefaultEvidence = "structural"
	}
	if in.DefaultEvidence != "structural" && in.DefaultEvidence != "observed" {
		v.Add("default_evidence", "must be structural or observed")
	}
	if in.Cardinality == "" {
		in.Cardinality = "many"
	}
	if in.Cardinality != "one" && in.Cardinality != "many" {
		v.Add("cardinality", "must be one or many")
	}
	if in.Impact == "" {
		in.Impact = "forward"
	}
	if !slices.Contains([]string{"forward", "reverse", "both", "none"}, in.Impact) {
		v.Add("impact", "must be forward, reverse, both or none")
	}
	if !colorRE.MatchString(in.Color) {
		v.Add("color", "must be #rrggbb")
	}
	if len(in.AttributesSchema) > 0 {
		if _, err := ParseSchema(in.AttributesSchema); err != nil {
			v.Add("attributes_schema", "%s", err.Error())
		}
	}
	return v.Err()
}

// CreateRelType creates a custom relationship type.
func (s *Service) CreateRelType(ctx context.Context, actor audit.Actor, in RelTypeInput) (*RelType, error) {
	if err := s.validateRelInput(&in, true); err != nil {
		return nil, err
	}
	directed := in.Directed == nil || *in.Directed
	fk, _ := json.Marshal(nonNil(in.FromKinds))
	tk, _ := json.Marshal(nonNil(in.ToKinds))
	err := s.Store.W().TxNamed(ctx, "catalog.reltype_create", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO relationship_types (id, name, inverse_name, title, description, directed, from_kinds, to_kinds,
			default_evidence, cardinality, attributes_schema, impact, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ids.New(ids.RelType), in.Name, in.InverseName, in.Title, in.Description, directed, string(fk), string(tk), in.DefaultEvidence,
			in.Cardinality, compactJSON(in.AttributesSchema), in.Impact, in.Color, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "relationship type %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		return s.relChangedTx(ctx, tx, actor, "created", in.Name)
	})
	if err != nil {
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.reg.rel(in.Name), nil
}

// UpdateRelType changes a custom relationship type. Endpoint constraints may only be relaxed when relations exist.
func (s *Service) UpdateRelType(ctx context.Context, actor audit.Actor, name string, in RelTypeInput, ifMatch int64) (*RelType, error) {
	cur := s.reg.rel(name)
	if cur == nil {
		return nil, apperr.New(apperr.CodeNotFound, "relationship type %q not found", name)
	}
	if cur.Builtin {
		return nil, apperr.New(apperr.CodeForbidden, "built-in relationship types are managed by Deed")
	}
	in.Name = name
	if err := s.validateRelInput(&in, false); err != nil {
		return nil, err
	}
	directed := in.Directed == nil || *in.Directed
	fk, _ := json.Marshal(nonNil(in.FromKinds))
	tk, _ := json.Marshal(nonNil(in.ToKinds))
	err := s.Store.W().TxNamed(ctx, "catalog.reltype_update", func(tx *store.Tx) error {
		// Refuse narrowing endpoint kinds when live relations would violate the new constraint.
		for _, side := range []struct {
			col   string
			kinds []string
		}{{"from_id", in.FromKinds}, {"to_id", in.ToKinds}} {
			if len(side.kinds) == 0 {
				continue
			}
			ph := strings.Repeat(",?", len(side.kinds))[1:]
			args := []any{name}
			for _, k := range side.kinds {
				args = append(args, k)
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM relations r JOIN entities e ON e.id = r.`+side.col+`
				WHERE r.type = ? AND r.live = 1 AND e.kind NOT IN (`+ph+`)`, args...).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return apperr.New(apperr.CodeConflict, "%d existing relations would violate the new endpoint constraint", n)
			}
		}
		n, err := tx.ExecN(ctx, `UPDATE relationship_types SET inverse_name = ?, title = ?, description = ?, directed = ?, from_kinds = ?, to_kinds = ?,
			default_evidence = ?, cardinality = ?, attributes_schema = ?, impact = ?, color = ?, updated_at = ?, version = version + 1
			WHERE name = ? AND (? = 0 OR version = ?)`, in.InverseName, in.Title, in.Description, directed, string(fk), string(tk), in.DefaultEvidence,
			in.Cardinality, compactJSON(in.AttributesSchema), in.Impact, in.Color, tx.NowMicros(), name, ifMatch, ifMatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "relationship type was modified concurrently; re-read it")
		}
		return s.relChangedTx(ctx, tx, actor, "updated", name)
	})
	if err != nil {
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.reg.rel(name), nil
}

// DeleteRelType removes a custom relationship type without relations.
func (s *Service) DeleteRelType(ctx context.Context, actor audit.Actor, name string) error {
	cur := s.reg.rel(name)
	if cur == nil {
		return apperr.New(apperr.CodeNotFound, "relationship type %q not found", name)
	}
	if cur.Builtin {
		return apperr.New(apperr.CodeForbidden, "built-in relationship types cannot be deleted")
	}
	err := s.Store.W().TxNamed(ctx, "catalog.reltype_delete", func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM relations WHERE type = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.New(apperr.CodeConflict, "%d relations still use %q", n, name)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM relationship_types WHERE name = ?`, name); err != nil {
			return err
		}
		return s.relChangedTx(ctx, tx, actor, "deleted", name)
	})
	if err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) relChangedTx(ctx context.Context, tx *store.Tx, actor audit.Actor, action, name string) error {
	if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "reltype." + action, Target: audit.Target{Type: "relationship_type", ID: name}}); err != nil {
		return err
	}
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "reltype", Action: action, Payload: map[string]any{"name": name}})
	return err
}

func compactJSON(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}
