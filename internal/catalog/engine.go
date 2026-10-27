package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// MaxFacetBytes bounds one facet's fields document.
const MaxFacetBytes = 256 << 10

// Item outcomes.
const (
	ActionCreated    = "created"
	ActionUpdated    = "updated"
	ActionUnchanged  = "unchanged"
	ActionStale      = "stale"
	ActionDeleted    = "deleted"
	ActionNotFound   = "not_found"
	ActionUnresolved = "unresolved"
)

// ApplyOpts tune one write.
type ApplyOpts struct {
	Actor audit.Actor
	// Strict rejects writes whose effective spec fails the kind schema (API/provider declarations). Source imports are
	// never rejected for that; the entity is marked invalid instead (ADR-003).
	Strict bool
	// CreateOnly fails when the source already has a live facet for the entity (POST semantics).
	CreateOnly bool
	// IfMatch requires the facet's current version (0 = unconditional).
	IfMatch int64
	// Generation stamps facets written by a snapshot.
	Generation int64
	// NoCreate refuses to create entities (enrichment-only writes).
	NoCreate bool
}

// ApplyResult is the outcome of one item.
type ApplyResult struct {
	EntityID     string `json:"entity_id,omitempty"`
	Action       string `json:"action"`
	FacetVersion int64  `json:"facet_version,omitempty"`
}

// EntityChange describes an effective-view change for hooks and live updates.
type EntityChange struct {
	EntityID   string
	ProjectID  string
	Kind       string
	Name       string
	Action     string // created, updated, observed, tombstoned, revived
	Changed    []string
	Effective  map[string]any
	Observed   map[string]any
	Valid      bool
	Validation []apperr.FieldError
	Conflicts  []Conflict
	Tombstoned bool
	SourceID   string
}

type entityRow struct {
	rid                             int64
	id, project, kind, name         string
	effective, observed, healthJSON string
	health, freshness               string
	valid, declared                 bool
	tombstoned                      sql.NullInt64
	lastSeen                        sql.NullInt64
	validation                      string
	version                         int64
}

func loadEntityRow(ctx context.Context, tx *store.Tx, id string) (*entityRow, error) {
	var r entityRow
	err := tx.QueryRow(ctx, `SELECT rid, id, project_id, kind, name, effective_json, observed_json, health_json, health, freshness, valid, declared,
		tombstoned_at, last_seen_at, validation_json, version FROM entities WHERE id = ?`, id).Scan(&r.rid, &r.id, &r.project, &r.kind, &r.name,
		&r.effective, &r.observed, &r.healthJSON, &r.health, &r.freshness, &r.valid, &r.declared, &r.tombstoned, &r.lastSeen, &r.validation, &r.version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.New(apperr.CodeNotFound, "entity not found")
	}
	return &r, err
}

// ResolveRef finds an entity for a source without creating it. Refs may name another project (bridges).
func (s *Service) ResolveRef(ctx context.Context, tx *store.Tx, src *Source, ref Ref) (string, error) {
	switch {
	case ref.ID != "":
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM entities WHERE id = ?`, ref.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return id, err
	case ref.ExternalID != "":
		var id string
		err := tx.QueryRow(ctx, `SELECT entity_id FROM entity_external_ids WHERE source_id = ? AND external_id = ?`, src.ID, ref.ExternalID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return id, err
	case ref.Name != "" && ref.Kind != "":
		project := src.ProjectID
		if ref.Project != "" {
			p, err := projectTx(ctx, tx, ref.Project)
			if err != nil {
				return "", nil //nolint:nilerr // an unknown project leaves the reference unresolved (recorded by the caller)
			}
			project = p.ID
		}
		return byNaturalKey(ctx, tx, project, ref.Kind, ref.Name)
	}
	return "", apperr.New(apperr.CodeInvalid, "reference needs id, external_id or kind+name")
}

func byNaturalKey(ctx context.Context, tx *store.Tx, project, kind, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM entities WHERE project_id = ? AND kind = ? AND name = ?`, project, kind, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT entity_id FROM entity_aliases WHERE project_id = ? AND kind = ? AND name = ?`, project, kind, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// identityValues computes the identity key strings of an effective-like object for a kind.
func identityValues(k *Kind, obj map[string]any) []string {
	var out []string
	for _, key := range k.IdentityKeys {
		var parts []string
		ok := true
		for _, p := range key {
			v, found := GetPointer(obj, p)
			if !found || v == nil || v == "" {
				ok = false
				break
			}
			parts = append(parts, p+"="+strings.ToLower(fmt.Sprint(v)))
		}
		if ok {
			out = append(out, strings.Join(parts, "&"))
		}
	}
	return out
}

// ApplyEntity upserts (or, with Delete, removes) the source's facet for an entity, then recomputes the entity.
func (s *Service) ApplyEntity(ctx context.Context, tx *store.Tx, src *Source, it *EntityItem, o ApplyOpts) (*ApplyResult, error) {
	start := time.Now()
	res, err := s.applyEntity(ctx, tx, src, it, o)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	} else if res.Action == ActionStale {
		outcome = "stale"
	}
	s.Metrics.Reconcile("item", outcome, time.Since(start))
	return res, err
}

func (s *Service) applyEntity(ctx context.Context, tx *store.Tx, src *Source, it *EntityItem, o ApplyOpts) (*ApplyResult, error) {
	if !src.Enabled {
		return nil, apperr.New(apperr.CodeForbidden, "source is disabled")
	}
	ref := it.Ref
	if ref.ExternalID == "" {
		ref.ExternalID = it.ExternalID
	}
	if ref.Kind == "" {
		ref.Kind = it.Kind
	}
	if ref.Name == "" {
		ref.Name = it.Name
	}
	if ref.Project != "" {
		return nil, apperr.New(apperr.CodeInvalid, "entity items are always written in the source's project")
	}
	entityID, err := s.resolveForWrite(ctx, tx, src, ref)
	if err != nil {
		return nil, err
	}
	if entityID != "" && it.Ref.ID == "" && ref.ExternalID != "" && it.Kind != "" && !it.Delete {
		if entityID, err = s.rebindChangedIdentity(ctx, tx, src, entityID, ref, o); err != nil {
			return nil, err
		}
	}
	if it.Delete {
		if entityID == "" {
			return &ApplyResult{Action: ActionNotFound}, nil
		}
		return s.deleteFacet(ctx, tx, src, entityID, it.Revision, o)
	}
	v := &apperr.Validation{}
	kindName := it.Kind
	var row *entityRow
	if entityID != "" {
		if row, err = loadEntityRow(ctx, tx, entityID); err != nil {
			return nil, err
		}
		if row.project != src.ProjectID {
			return nil, apperr.New(apperr.CodeForbidden, "a source can only write entities in its own project")
		}
		if kindName != "" && kindName != row.kind {
			v.Add("kind", "entity %s is a %s, not a %s", entityID, row.kind, kindName)
		}
		kindName = row.kind
	}
	k := s.reg.kind(kindName)
	if k == nil {
		v.Add("kind", "unknown kind %q", kindName)
		return nil, v.Err()
	}
	if len(src.Policy.AllowedKinds) > 0 && !slices.Contains(src.Policy.AllowedKinds, kindName) {
		v.Add("kind", "source policy does not allow kind %q", kindName)
	}
	if src.Layer == LayerObservation && (len(it.Spec) > 0 || len(it.Metadata) > 0) {
		v.Add("spec", "observation sources write observed values and health only")
	}
	if it.Health != nil && !slices.Contains([]string{"ok", "warning", "failed", "unknown"}, it.Health.State) {
		v.Add("health.state", "must be ok, warning, failed or unknown")
	}
	allowHTTP := false
	if p, err := projectTx(ctx, tx, src.ProjectID); err == nil {
		allowHTTP = p.AllowHTTPLinks
	}
	ValidateMetadata(it.Metadata, allowHTTP, v)
	CheckSecretValues("spec", it.Spec, v)
	CheckSecretValues("observed", it.Observed, v)
	if owner, ok := it.Metadata["owner"].(string); ok && owner != "" && !ids.Valid(ids.Entity, owner) && !ValidName(owner) {
		v.Add("metadata.owner", "must be a group entity ID or group name")
	}
	fields := map[string]any{}
	if len(it.Metadata) > 0 {
		fields["metadata"] = it.Metadata
	}
	if len(it.Spec) > 0 {
		fields["spec"] = it.Spec
	}
	if len(it.Observed) > 0 {
		fields["observed"] = it.Observed
	}
	if it.Health != nil {
		fields["health"] = map[string]any{"state": it.Health.State, "reason": it.Health.Reason}
	}
	fj := rawJSON(fields)
	if len(fj) > MaxFacetBytes {
		v.Add("item", "facet exceeds %d bytes", MaxFacetBytes)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	observedAt := tx.Now
	if it.ObservedAt != nil {
		observedAt = it.ObservedAt.UTC()
		if observedAt.After(tx.Now.Add(5 * time.Minute)) {
			return nil, apperr.New(apperr.CodeInvalid, "observed_at is in the future")
		}
	}
	var expires sql.NullInt64
	switch {
	case it.ExpiresAt != nil:
		if !it.ExpiresAt.After(tx.Now) {
			return nil, apperr.New(apperr.CodeInvalid, "expires_at is in the past")
		}
		expires = sql.NullInt64{Int64: it.ExpiresAt.UnixMicro(), Valid: true}
	case src.Layer == LayerObservation:
		ttl := time.Duration(src.ExpectedIntervalSeconds) * 3 * time.Second
		if ttl <= 0 {
			ttl = time.Hour
		}
		expires = sql.NullInt64{Int64: tx.Now.Add(ttl).UnixMicro(), Valid: true}
	case src.Policy.DefaultTTLSeconds > 0 && src.Layer == LayerDiscovered:
		expires = sql.NullInt64{Int64: tx.Now.Add(time.Duration(src.Policy.DefaultTTLSeconds) * time.Second).UnixMicro(), Valid: true}
	}
	created := false
	if entityID == "" {
		if o.NoCreate {
			return &ApplyResult{Action: ActionNotFound}, nil
		}
		// Converge on an existing entity through the kind's identity keys (unique match only, Q-09).
		if id, err := s.matchIdentity(ctx, tx, src.ProjectID, k, fields); err != nil {
			return nil, err
		} else if id != "" {
			entityID = id
		}
	}
	if entityID == "" {
		if !ValidName(it.Name) {
			return nil, apperr.New(apperr.CodeInvalid, "name must be a lowercase slug (a-z, 0-9, '.', '_', '-') to create an entity")
		}
		if err := s.checkCapacity(ctx, tx); err != nil {
			return nil, err
		}
		entityID = ids.New(ids.Entity)
		if _, err := tx.Exec(ctx, `INSERT INTO entities (id, project_id, kind, name, schema_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			entityID, src.ProjectID, k.Name, it.Name, k.StorageVersion, tx.NowMicros(), tx.NowMicros()); err != nil {
			if store.IsConstraint(err) {
				return nil, apperr.New(apperr.CodeConflict, "entity %s/%s was created concurrently; retry", k.Name, it.Name)
			}
			return nil, err
		}
		created = true
	}
	// Facet staleness and concurrency checks (ADR-003).
	var (
		facetID                     string
		curRev, tombRev, curVersion int64
		curObserved                 int64
		curFields                   string
		curExpires                  sql.NullInt64
		tomb                        sql.NullInt64
	)
	err = tx.QueryRow(ctx, `SELECT id, revision, tombstone_revision, version, observed_at, fields_json, expires_at, tombstoned_at FROM facets
		WHERE entity_id = ? AND source_id = ?`, entityID, src.ID).Scan(&facetID, &curRev, &tombRev, &curVersion, &curObserved, &curFields, &curExpires, &tomb)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists {
		if o.CreateOnly && !tomb.Valid {
			return nil, apperr.New(apperr.CodeAlreadyExists, "entity %s is already declared by source %q; import it instead of creating it", entityID, src.Name)
		}
		if o.IfMatch > 0 && o.IfMatch != curVersion {
			return nil, apperr.New(apperr.CodePrecondition, "declaration was modified concurrently (version %d, expected %d)", curVersion, o.IfMatch)
		}
		stale := (it.Revision > 0 && (it.Revision <= curRev || it.Revision <= tombRev)) ||
			(it.Revision == 0 && it.ObservedAt != nil && observedAt.UnixMicro() < curObserved)
		if stale {
			// A stale item still proves the facet is present in this snapshot generation.
			if o.Generation > 0 && !tomb.Valid {
				if _, err := tx.Exec(ctx, `UPDATE facets SET generation = MAX(generation, ?) WHERE id = ?`, o.Generation, facetID); err != nil {
					return nil, err
				}
			}
			return &ApplyResult{EntityID: entityID, Action: ActionStale, FacetVersion: curVersion}, nil
		}
	} else if o.IfMatch > 0 {
		return nil, apperr.New(apperr.CodePrecondition, "no declaration exists for this source")
	}
	rev := it.Revision
	if rev == 0 {
		rev = max(curRev, tombRev) + 1
	}
	if exists && !tomb.Valid && curFields == fj && curExpires == expires {
		// Same facts again: refresh liveness only.
		if _, err := tx.Exec(ctx, `UPDATE facets SET observed_at = ?, revision = ?, generation = MAX(generation, ?), updated_at = ? WHERE id = ?`,
			observedAt.UnixMicro(), rev, o.Generation, tx.NowMicros(), facetID); err != nil {
			return nil, err
		}
		if err := s.mapExternalID(ctx, tx, src, entityID, ref.ExternalID); err != nil {
			return nil, err
		}
		if src.Layer != LayerDeclared && src.Layer != LayerAnnotation && src.Layer != LayerOverride {
			if err := s.touchFreshness(ctx, tx, entityID); err != nil {
				return nil, err
			}
		}
		return &ApplyResult{EntityID: entityID, Action: ActionUnchanged, FacetVersion: curVersion}, nil
	}
	if exists {
		if _, err := tx.Exec(ctx, `UPDATE facets SET layer = ?, generation = MAX(generation, ?), revision = ?, fields_json = ?, observed_at = ?, expires_at = ?,
			tombstoned_at = NULL, external_id = ?, updated_at = ?, version = version + 1,
			first_claimed_at = CASE WHEN tombstoned_at IS NOT NULL THEN ? ELSE first_claimed_at END WHERE id = ?`,
			string(src.Layer), o.Generation, rev, fj, observedAt.UnixMicro(), expires, ref.ExternalID, tx.NowMicros(), tx.NowMicros(), facetID); err != nil {
			return nil, err
		}
		curVersion++
	} else {
		facetID = ids.New(ids.Facet)
		curVersion = 1
		if _, err := tx.Exec(ctx, `INSERT INTO facets (id, entity_id, source_id, layer, generation, revision, fields_json, observed_at, expires_at,
			first_claimed_at, external_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			facetID, entityID, src.ID, string(src.Layer), o.Generation, rev, fj, observedAt.UnixMicro(), expires, tx.NowMicros(), ref.ExternalID,
			tx.NowMicros(), tx.NowMicros()); err != nil {
			return nil, err
		}
	}
	if err := s.mapExternalID(ctx, tx, src, entityID, ref.ExternalID); err != nil {
		return nil, err
	}
	ch, err := s.recomputeTx(ctx, tx, entityID, recomputeOpts{actor: o.Actor, sourceID: src.ID, created: created})
	if err != nil {
		return nil, err
	}
	if o.Strict && ch != nil && !ch.Valid {
		return nil, &apperr.Error{Code: apperr.CodeInvalid, Message: fmt.Sprintf("spec does not validate against kind %s", k.Name), Fields: ch.Validation}
	}
	action := ActionUpdated
	if created {
		action = ActionCreated
		if err := s.resolvePendingTx(ctx, tx, entityID, src, it.ExternalID); err != nil {
			return nil, err
		}
	}
	return &ApplyResult{EntityID: entityID, Action: action, FacetVersion: curVersion}, nil
}

func (s *Service) resolveForWrite(ctx context.Context, tx *store.Tx, src *Source, ref Ref) (string, error) {
	if ref.ID != "" {
		id, err := s.ResolveRef(ctx, tx, src, Ref{ID: ref.ID})
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", apperr.New(apperr.CodeNotFound, "entity %s not found", ref.ID)
		}
		return id, nil
	}
	if ref.ExternalID != "" {
		if len(ref.ExternalID) > 512 {
			return "", apperr.New(apperr.CodeInvalid, "external_id must be at most 512 characters")
		}
		id, err := s.ResolveRef(ctx, tx, src, Ref{ExternalID: ref.ExternalID})
		if err != nil || id != "" {
			return id, err
		}
	}
	if ref.Kind != "" && ref.Name != "" {
		return byNaturalKey(ctx, tx, src.ProjectID, ref.Kind, ref.Name)
	}
	if ref.ExternalID == "" {
		return "", apperr.New(apperr.CodeInvalid, "item needs ref.id, external_id or kind+name")
	}
	return "", nil
}

func (s *Service) matchIdentity(ctx context.Context, tx *store.Tx, project string, k *Kind, fields map[string]any) (string, error) {
	vals := identityValues(k, fields)
	for _, v := range vals {
		rows, err := tx.Query(ctx, `SELECT DISTINCT entity_id FROM entity_identity_keys WHERE project_id = ? AND kind = ? AND key_value = ?`, project, k.Name, v)
		if err != nil {
			return "", err
		}
		var found []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", err
			}
			found = append(found, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		if len(found) == 1 {
			return found[0], nil
		}
	}
	return "", nil
}

func (s *Service) checkCapacity(ctx context.Context, tx *store.Tx) error {
	if s.MaxEntities <= 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM entities`).Scan(&n); err != nil {
		return err
	}
	if n >= s.MaxEntities {
		return apperr.New(apperr.CodeUnavailable, "the catalog reached its configured limit of %d entities", s.MaxEntities)
	}
	return nil
}

// rebindChangedIdentity handles a source whose mapping changed an item's kind or name (an importer that now maps a
// resource to a service instead of a domain, or names a certificate after its host instead of its resource name) while
// the source's own external ID is still bound to the old entity:
//   - another kind: the source withdraws its facet from that entity and the item resolves by its new kind and name (or
//     creates a new entity, which rebinds the external ID);
//   - another name (not one of the entity's aliases, which a user rename leaves behind): the same, when another
//     source also describes the entity (resources of different states that collided on one name come apart); when
//     this source is its only describing source the entity is renamed instead, keeping its ID, annotations and the old
//     name as an alias.
//
// Other sources' facets on the old entity are untouched.
func (s *Service) rebindChangedIdentity(ctx context.Context, tx *store.Tx, src *Source, entityID string, ref Ref, o ApplyOpts) (string, error) {
	var kind, name, bound string
	if err := tx.QueryRow(ctx, `SELECT kind, name FROM entities WHERE id = ?`, entityID).Scan(&kind, &name); err != nil {
		return entityID, nil //nolint:nilerr // a missing row is reported by the caller's own load
	}
	renamed := kind == ref.Kind && ref.Name != "" && ref.Name != name
	if kind == ref.Kind && !renamed {
		return entityID, nil
	}
	err := tx.QueryRow(ctx, `SELECT entity_id FROM entity_external_ids WHERE source_id = ? AND external_id = ?`, src.ID, ref.ExternalID).Scan(&bound)
	if err != nil || bound != entityID {
		return entityID, nil //nolint:nilerr // not bound through this external ID: the kind check reports the mismatch
	}
	if renamed {
		var alias, others int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM entity_aliases WHERE entity_id = ? AND name = ?`, entityID, ref.Name).Scan(&alias); err != nil {
			return "", err
		}
		if alias > 0 {
			return entityID, nil
		}
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM facets WHERE entity_id = ? AND source_id != ? AND tombstoned_at IS NULL
			AND layer IN ('declared', 'imported', 'discovered')`, entityID, src.ID).Scan(&others); err != nil {
			return "", err
		}
		if others == 0 {
			taken, err := byNaturalKey(ctx, tx, src.ProjectID, ref.Kind, ref.Name)
			if err != nil {
				return "", err
			}
			if taken == "" {
				row, err := loadEntityRow(ctx, tx, entityID)
				if err != nil {
					return "", err
				}
				actor := o.Actor
				if actor.Type == "" {
					actor = audit.Actor{Type: audit.ActorSource, ID: src.ID}
				}
				return entityID, s.renameTx(ctx, tx, actor, row, ref.Name)
			}
		}
	}
	if _, err := s.deleteFacet(ctx, tx, src, entityID, 0, ApplyOpts{Actor: o.Actor}); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entity_external_ids WHERE source_id = ? AND external_id = ?`, src.ID, ref.ExternalID); err != nil {
		return "", err
	}
	if ref.Name == "" {
		return "", nil
	}
	return byNaturalKey(ctx, tx, src.ProjectID, ref.Kind, ref.Name)
}

func (s *Service) mapExternalID(ctx context.Context, tx *store.Tx, src *Source, entityID, ext string) error {
	if ext == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO entity_external_ids (source_id, external_id, entity_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (source_id, external_id) DO UPDATE SET entity_id = excluded.entity_id`, src.ID, ext, entityID, tx.NowMicros())
	return err
}

func (s *Service) deleteFacet(ctx context.Context, tx *store.Tx, src *Source, entityID string, revision int64, o ApplyOpts) (*ApplyResult, error) {
	var facetID string
	var curRev, tombRev, version int64
	var tomb sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT id, revision, tombstone_revision, version, tombstoned_at FROM facets WHERE entity_id = ? AND source_id = ?`,
		entityID, src.ID).Scan(&facetID, &curRev, &tombRev, &version, &tomb)
	if errors.Is(err, sql.ErrNoRows) {
		return &ApplyResult{EntityID: entityID, Action: ActionNotFound}, nil
	}
	if err != nil {
		return nil, err
	}
	if o.IfMatch > 0 && o.IfMatch != version {
		return nil, apperr.New(apperr.CodePrecondition, "declaration was modified concurrently (version %d, expected %d)", version, o.IfMatch)
	}
	if revision > 0 && (revision <= curRev || revision <= tombRev) {
		return &ApplyResult{EntityID: entityID, Action: ActionStale, FacetVersion: version}, nil
	}
	if tomb.Valid {
		return &ApplyResult{EntityID: entityID, Action: ActionNotFound}, nil
	}
	tr := revision
	if tr == 0 {
		tr = curRev + 1
	}
	if _, err := tx.Exec(ctx, `UPDATE facets SET tombstoned_at = ?, tombstone_revision = ?, revision = ?, fields_json = '{}', updated_at = ?, version = version + 1
		WHERE id = ?`, tx.NowMicros(), tr, tr, tx.NowMicros(), facetID); err != nil {
		return nil, err
	}
	if _, err := s.recomputeTx(ctx, tx, entityID, recomputeOpts{actor: o.Actor, sourceID: src.ID}); err != nil {
		return nil, err
	}
	return &ApplyResult{EntityID: entityID, Action: ActionDeleted, FacetVersion: version + 1}, nil
}

// TombstoneFacetsTx tombstones the given facets of a source (snapshot absence) and recomputes their entities.
func (s *Service) TombstoneFacetsTx(ctx context.Context, tx *store.Tx, src *Source, facetIDs []string, actor audit.Actor) error {
	affected := map[string]bool{}
	for _, fid := range facetIDs {
		var eid string
		var rev int64
		if err := tx.QueryRow(ctx, `SELECT entity_id, revision FROM facets WHERE id = ? AND source_id = ?`, fid, src.ID).Scan(&eid, &rev); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE facets SET tombstoned_at = ?, tombstone_revision = ?, fields_json = '{}', updated_at = ?, version = version + 1 WHERE id = ?`,
			tx.NowMicros(), rev, tx.NowMicros(), fid); err != nil {
			return err
		}
		affected[eid] = true
	}
	keys := make([]string, 0, len(affected))
	for k := range affected {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, eid := range keys {
		if _, err := s.recomputeTx(ctx, tx, eid, recomputeOpts{actor: actor, sourceID: src.ID}); err != nil {
			return err
		}
	}
	return nil
}

type recomputeOpts struct {
	actor    audit.Actor
	sourceID string
	created  bool
}

type facetRow struct {
	MergeFacet
	expires    sql.NullInt64
	tombstoned bool
	expected   int
}

func (s *Service) loadFacets(ctx context.Context, tx *store.Tx, entityID string) ([]facetRow, error) {
	rows, err := tx.Query(ctx, `SELECT f.id, f.source_id, s.name, f.layer, s.policy_json, f.revision, f.observed_at, f.first_claimed_at, f.fields_json,
		f.expires_at, f.tombstoned_at IS NOT NULL, s.expected_interval_seconds, s.layer FROM facets f JOIN sources s ON s.id = f.source_id
		WHERE f.entity_id = ? ORDER BY f.id`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []facetRow
	for rows.Next() {
		var f facetRow
		var layer, pol, fields, srcLayer string
		var obs, first int64
		if err := rows.Scan(&f.FacetID, &f.SourceID, &f.SourceName, &layer, &pol, &f.Revision, &obs, &first, &fields, &f.expires, &f.tombstoned,
			&f.expected, &srcLayer); err != nil {
			return nil, err
		}
		f.Layer = Layer(layer)
		var p Policy
		_ = json.Unmarshal([]byte(pol), &p)
		f.Priority = f.Layer.DefaultPriority()
		if p.Priority != nil {
			f.Priority = *p.Priority
		}
		f.FieldPriority = p.FieldPriority
		f.ObservedAt, f.FirstClaimedAt = clock.FromMicros(obs), clock.FromMicros(first)
		f.Fields = decodeObject(fields)
		out = append(out, f)
	}
	return out, rows.Err()
}

// facetFreshness classifies a facet by age against its source's expected interval.
func facetFreshness(age time.Duration, expectedSeconds int) string {
	interval := time.Duration(expectedSeconds) * time.Second
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	switch {
	case age <= interval:
		return "fresh"
	case age <= 3*interval:
		return "aging"
	}
	return "stale"
}

var freshRank = map[string]int{"unknown": 0, "stale": 1, "aging": 2, "fresh": 3}

func runtimeLayer(l Layer) bool {
	return l == LayerImported || l == LayerDiscovered || l == LayerObservation
}

// touchFreshness recomputes last_seen_at and freshness after liveness-only refreshes.
func (s *Service) touchFreshness(ctx context.Context, tx *store.Tx, entityID string) error {
	facets, err := s.loadFacets(ctx, tx, entityID)
	if err != nil {
		return err
	}
	last, fresh := freshnessOf(facets, tx.Now)
	_, err = tx.Exec(ctx, `UPDATE entities SET last_seen_at = ?, freshness = ? WHERE id = ?`, last, fresh, entityID)
	return err
}

func freshnessOf(facets []facetRow, now time.Time) (sql.NullInt64, string) {
	var last sql.NullInt64
	fresh := "unknown"
	for _, f := range facets {
		if f.tombstoned || !runtimeLayer(f.Layer) || (f.expires.Valid && f.expires.Int64 <= now.UnixMicro()) {
			continue
		}
		if !last.Valid || f.ObservedAt.UnixMicro() > last.Int64 {
			last = sql.NullInt64{Int64: f.ObservedAt.UnixMicro(), Valid: true}
		}
		if fr := facetFreshness(now.Sub(f.ObservedAt), f.expected); freshRank[fr] > freshRank[fresh] {
			fresh = fr
		}
	}
	return last, fresh
}

// RecomputeEntity recomputes one entity's effective view in its own transaction (maintenance, expiry).
func (s *Service) RecomputeEntity(ctx context.Context, entityID string, actor audit.Actor) error {
	return s.Store.W().TxNamed(ctx, "catalog.recompute", func(tx *store.Tx) error {
		_, err := s.recomputeTx(ctx, tx, entityID, recomputeOpts{actor: actor})
		return err
	})
}

// recomputeTx rebuilds an entity's effective view from its live facets (ADR-003) and records the change.
func (s *Service) recomputeTx(ctx context.Context, tx *store.Tx, entityID string, o recomputeOpts) (*EntityChange, error) {
	row, err := loadEntityRow(ctx, tx, entityID)
	if err != nil {
		return nil, err
	}
	facets, err := s.loadFacets(ctx, tx, entityID)
	if err != nil {
		return nil, err
	}
	nowUS := tx.Now.UnixMicro()
	var live []MergeFacet
	declared := false
	for _, f := range facets {
		if f.tombstoned || (f.expires.Valid && f.expires.Int64 <= nowUS) {
			continue
		}
		live = append(live, f.MergeFacet)
		if f.Layer == LayerDeclared {
			declared = true
		}
	}
	ch := &EntityChange{EntityID: entityID, ProjectID: row.project, Kind: row.kind, Name: row.name, SourceID: o.sourceID, Valid: true}
	if len(live) == 0 {
		if row.tombstoned.Valid {
			return nil, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET tombstoned_at = ?, declared = 0, facet_count = 0, updated_at = ?, version = version + 1 WHERE id = ?`,
			nowUS, nowUS, entityID); err != nil {
			return nil, err
		}
		for _, q := range []string{`DELETE FROM entity_identity_keys WHERE entity_id = ?`, `DELETE FROM conflicts WHERE entity_id = ?`} {
			if _, err := tx.Exec(ctx, q, entityID); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM entity_search WHERE rowid = ?`, row.rid); err != nil {
			return nil, err
		}
		ch.Action, ch.Tombstoned = "tombstoned", true
		return ch, s.emitEntityChange(ctx, tx, ch, o)
	}
	m := Merge(live)
	k := s.reg.kind(row.kind)
	var fe []apperr.FieldError
	schemaVersion := ""
	if k != nil {
		spec, _ := m.Effective["spec"].(map[string]any)
		if spec == nil {
			spec = map[string]any{}
		}
		fe = k.Schema("").Validate(spec)
		schemaVersion = k.StorageVersion
	}
	ch.Valid, ch.Validation, ch.Conflicts = len(fe) == 0, fe, m.Conflicts
	if fe == nil {
		fe = []apperr.FieldError{}
	}
	md, _ := m.Effective["metadata"].(map[string]any)
	str := func(key string) string {
		v, _ := md[key].(string)
		return v
	}
	labels, _ := md["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	verDeclared, verObserved := "", ""
	if ver, ok := md["version"].(map[string]any); ok {
		verDeclared, _ = ver["declared"].(string)
		verObserved, _ = ver["observed"].(string)
	}
	if v, ok := m.Observed["version"].(string); ok && v != "" {
		verObserved = v
	}
	health := "unknown"
	if st, ok := m.Health["state"].(string); ok {
		health = st
	}
	lastSeen, fresh := freshnessOf(facets, tx.Now)
	effJSON, obsJSON, healthJSON := rawJSON(m.Effective), rawJSON(m.Observed), rawJSON(m.Health)
	if m.Health == nil {
		healthJSON = "{}"
	}
	semantic := effJSON != row.effective || ch.Valid != row.valid || row.tombstoned.Valid || o.created
	observational := obsJSON != row.observed || healthJSON != row.healthJSON || health != row.health
	switch {
	case o.created:
		ch.Action = "created"
	case row.tombstoned.Valid:
		ch.Action = "revived"
	case semantic:
		ch.Action = "updated"
	case observational:
		ch.Action = "observed"
	}
	if ch.Action == "" {
		// Nothing visible changed (e.g. a second source asserting the same values): keep provenance and freshness current.
		_, err := tx.Exec(ctx, `UPDATE entities SET provenance_json = ?, last_seen_at = ?, freshness = ?, declared = ?, facet_count = ? WHERE id = ?`,
			rawJSON(m.Provenance), lastSeen, fresh, declared, len(live), entityID)
		if err != nil {
			return nil, err
		}
		return nil, s.syncConflicts(ctx, tx, entityID, m.Conflicts)
	}
	ch.Changed = diffPointers(decodeObject(row.effective), m.Effective)
	ch.Changed = append(ch.Changed, diffPointers(prefixed("status", "observed", decodeObject(row.observed)), prefixed("status", "observed", m.Observed))...)
	if health != row.health {
		ch.Changed = append(ch.Changed, "/status/health")
	}
	ch.Effective, ch.Observed = m.Effective, m.Observed
	if _, err := tx.Exec(ctx, `UPDATE entities SET display_name = ?, description = ?, environment = ?, owner = ?, lifecycle = ?, icon = ?, labels_json = ?,
		effective_json = ?, provenance_json = ?, observed_json = ?, health = ?, health_json = ?, freshness = ?, last_seen_at = ?, schema_version = ?,
		valid = ?, validation_json = ?, declared = ?, facet_count = ?, version_declared = ?, version_observed = ?, updated_at = ?, tombstoned_at = NULL,
		version = version + 1 WHERE id = ?`, str("displayName"), str("description"), str("environment"), str("owner"), str("lifecycle"), str("icon"),
		rawJSON(labels), effJSON, rawJSON(m.Provenance), obsJSON, health, healthJSON, fresh, lastSeen, schemaVersion, ch.Valid, rawJSON(fe), declared,
		len(live), verDeclared, verObserved, nowUS, entityID); err != nil {
		return nil, err
	}
	if semantic {
		if err := s.syncIdentity(ctx, tx, row, k, m.Effective); err != nil {
			return nil, err
		}
		if err := s.syncSearch(ctx, tx, row, m.Effective); err != nil {
			return nil, err
		}
	}
	if err := s.syncConflicts(ctx, tx, entityID, m.Conflicts); err != nil {
		return nil, err
	}
	return ch, s.emitEntityChange(ctx, tx, ch, o)
}

func prefixed(a, b string, m map[string]any) map[string]any {
	return map[string]any{a: map[string]any{b: m}}
}

// diffPointers lists leaf pointers whose values differ between two objects (capped at 100).
func diffPointers(old, cur map[string]any) []string {
	om, nm := map[string]string{}, map[string]string{}
	flatten("", old, func(p string, v any) { om[p] = compact(v) })
	flatten("", cur, func(p string, v any) { nm[p] = compact(v) })
	var out []string
	for p, v := range nm {
		if om[p] != v {
			out = append(out, p)
		}
	}
	for p := range om {
		if _, ok := nm[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) > 100 {
		out = append(out[:100], "…")
	}
	return out
}

func (s *Service) emitEntityChange(ctx context.Context, tx *store.Tx, ch *EntityChange, o recomputeOpts) error {
	summary := map[string]any{"kind": ch.Kind, "name": ch.Name, "changed": ch.Changed}
	if !ch.Valid {
		summary["invalid"] = true
	}
	if err := s.changeTx(ctx, tx, change{project: ch.ProjectID, typ: "entity", id: ch.EntityID, action: ch.Action, actor: o.actor, sourceID: o.sourceID,
		summary: summary}); err != nil {
		return err
	}
	if ch.Action != "observed" {
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "entity", Action: ch.Action, ProjectID: ch.ProjectID,
			Payload: map[string]any{"entity_id": ch.EntityID, "kind": ch.Kind, "name": ch.Name, "changed": nonNil(ch.Changed), "source_id": o.sourceID}}); err != nil {
			return err
		}
	}
	for _, h := range s.EntityHooks {
		if err := h(ctx, tx, *ch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncIdentity(ctx context.Context, tx *store.Tx, row *entityRow, k *Kind, eff map[string]any) error {
	if _, err := tx.Exec(ctx, `DELETE FROM entity_identity_keys WHERE entity_id = ?`, row.id); err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	for _, v := range identityValues(k, eff) {
		if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO entity_identity_keys (project_id, kind, key_value, entity_id) VALUES (?, ?, ?, ?)`,
			row.project, row.kind, v, row.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncSearch(ctx context.Context, tx *store.Tx, row *entityRow, eff map[string]any) error {
	md, _ := eff["metadata"].(map[string]any)
	spec, _ := eff["spec"].(map[string]any)
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	var labels []string
	if l, ok := md["labels"].(map[string]any); ok {
		for k, v := range l {
			labels = append(labels, fmt.Sprintf("%s=%v %s %v", k, v, k, v))
		}
	}
	sort.Strings(labels)
	var extra []string
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := spec[k].(type) {
		case string:
			extra = append(extra, v)
		case float64, bool:
			extra = append(extra, fmt.Sprint(v))
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					extra = append(extra, s)
				}
			}
		}
	}
	if tags, ok := md["tags"].([]any); ok {
		for _, t := range tags {
			extra = append(extra, fmt.Sprint(t))
		}
	}
	al, err := stringsQuery(ctx, tx, `SELECT name FROM entity_aliases WHERE entity_id = ?`, row.id)
	if err != nil {
		return err
	}
	ext, err := stringsQuery(ctx, tx, `SELECT external_id FROM entity_external_ids WHERE entity_id = ?`, row.id)
	if err != nil {
		return err
	}
	extra = append(append(extra, al...), ext...)
	x := strings.Join(extra, " ")
	if len(x) > 4096 {
		x = x[:4096]
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entity_search WHERE rowid = ?`, row.rid); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO entity_search (rowid, name, display_name, description, kind, labels, extra) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.rid, row.name, str(md, "displayName"), str(md, "description"), row.kind, strings.Join(labels, " "), x)
	return err
}

func (s *Service) syncConflicts(ctx context.Context, tx *store.Tx, entityID string, cs []Conflict) error {
	keep := map[string]bool{}
	for _, c := range cs {
		keep[c.Pointer+"|"+c.LoserSource] = true
		if _, err := tx.Exec(ctx, `INSERT INTO conflicts (entity_id, pointer, layer, winner_source, loser_source, winner_value, loser_value, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (entity_id, pointer, loser_source) DO UPDATE SET winner_source = excluded.winner_source,
			winner_value = excluded.winner_value, loser_value = excluded.loser_value, layer = excluded.layer, last_seen_at = excluded.last_seen_at`,
			entityID, c.Pointer, string(c.Layer), c.WinnerSource, c.LoserSource, truncate(c.WinnerValue, 2048), truncate(c.LoserValue, 2048),
			tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT pointer, loser_source FROM conflicts WHERE entity_id = ?`, entityID)
	if err != nil {
		return err
	}
	var stale [][2]string
	for rows.Next() {
		var p, l string
		if err := rows.Scan(&p, &l); err != nil {
			rows.Close()
			return err
		}
		if !keep[p+"|"+l] {
			stale = append(stale, [2]string{p, l})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range stale {
		if _, err := tx.Exec(ctx, `DELETE FROM conflicts WHERE entity_id = ? AND pointer = ? AND loser_source = ?`, entityID, x[0], x[1]); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Rename changes an entity's name, keeping the old name as an alias (Q-10).
func (s *Service) Rename(ctx context.Context, actor audit.Actor, entityID, newName string) error {
	if !ValidName(newName) {
		return apperr.New(apperr.CodeInvalid, "name must be a lowercase slug")
	}
	return s.Store.W().TxNamed(ctx, "catalog.entity_rename", func(tx *store.Tx) error {
		row, err := loadEntityRow(ctx, tx, entityID)
		if err != nil {
			return err
		}
		if row.name == newName {
			return nil
		}
		if id, err := byNaturalKey(ctx, tx, row.project, row.kind, newName); err != nil {
			return err
		} else if id != "" && id != entityID {
			return apperr.New(apperr.CodeAlreadyExists, "%s/%s already exists", row.kind, newName)
		}
		return s.renameTx(ctx, tx, actor, row, newName)
	})
}

// renameTx renames an entity whose new name is free (or one of its own aliases), keeping the old name as an alias.
func (s *Service) renameTx(ctx context.Context, tx *store.Tx, actor audit.Actor, row *entityRow, newName string) error {
	entityID := row.id
	if _, err := tx.Exec(ctx, `DELETE FROM entity_aliases WHERE project_id = ? AND kind = ? AND name = ?`, row.project, row.kind, newName); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT OR REPLACE INTO entity_aliases (project_id, kind, name, entity_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		row.project, row.kind, row.name, entityID, tx.NowMicros()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE entities SET name = ?, updated_at = ?, version = version + 1 WHERE id = ?`, newName, tx.NowMicros(), entityID); err != nil {
		return err
	}
	row.name = newName
	if err := s.syncSearch(ctx, tx, row, decodeObject(row.effective)); err != nil {
		return err
	}
	if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: row.project, Action: "entity.renamed", Target: audit.Target{Type: "entity", ID: entityID},
		Details: map[string]any{"to": newName}}); err != nil {
		return err
	}
	return s.changeTx(ctx, tx, change{project: row.project, typ: "entity", id: entityID, action: "renamed", actor: actor,
		summary: map[string]any{"kind": row.kind, "name": newName}})
}

// Merge moves every facet, relation, alias and external ID of `from` onto `into` and tombstones `from` (Q-11).
func (s *Service) MergeEntities(ctx context.Context, actor audit.Actor, into, from string) error {
	if into == from {
		return apperr.New(apperr.CodeInvalid, "cannot merge an entity into itself")
	}
	return s.Store.W().TxNamed(ctx, "catalog.entity_merge", func(tx *store.Tx) error {
		a, err := loadEntityRow(ctx, tx, into)
		if err != nil {
			return err
		}
		b, err := loadEntityRow(ctx, tx, from)
		if err != nil {
			return err
		}
		if a.project != b.project || a.kind != b.kind {
			return apperr.New(apperr.CodeInvalid, "only entities of the same kind and project can be merged")
		}
		// Facets of a source present on both: the surviving entity's facet wins; the other is dropped.
		if _, err := tx.Exec(ctx, `DELETE FROM facets WHERE entity_id = ? AND source_id IN (SELECT source_id FROM facets WHERE entity_id = ?)`, from, into); err != nil {
			return err
		}
		stmts := []string{
			`UPDATE facets SET entity_id = ? WHERE entity_id = ?`,
			`UPDATE entity_external_ids SET entity_id = ? WHERE entity_id = ?`,
			`UPDATE entity_aliases SET entity_id = ? WHERE entity_id = ?`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q, into, from); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT OR REPLACE INTO entity_aliases (project_id, kind, name, entity_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			b.project, b.kind, b.name, into, tx.NowMicros()); err != nil {
			return err
		}
		if err := s.moveRelations(ctx, tx, from, into, actor); err != nil {
			return err
		}
		if _, err := s.recomputeTx(ctx, tx, into, recomputeOpts{actor: actor}); err != nil {
			return err
		}
		if _, err := s.recomputeTx(ctx, tx, from, recomputeOpts{actor: actor}); err != nil {
			return err
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: a.project, Action: "entity.merged", Target: audit.Target{Type: "entity", ID: into},
			Details: map[string]any{"merged": from}}); err != nil {
			return err
		}
		return s.changeTx(ctx, tx, change{project: a.project, typ: "entity", id: into, action: "merged", actor: actor, summary: map[string]any{"merged": from}})
	})
}
