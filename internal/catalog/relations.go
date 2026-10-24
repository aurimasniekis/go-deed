package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Evidence classes, strongest first.
const (
	EvidenceDeclared   = "declared"
	EvidenceStructural = "structural"
	EvidenceObserved   = "observed"
	EvidenceInferred   = "inferred"
)

var evidenceRank = map[string]int{EvidenceDeclared: 4, EvidenceStructural: 3, EvidenceObserved: 2, EvidenceInferred: 1}

// RelationResult is the outcome of one relation item.
type RelationResult struct {
	RelationID string `json:"relation_id,omitempty"`
	Action     string `json:"action"`
	Detail     string `json:"detail,omitempty"`
}

func defaultEvidence(l Layer, inferred bool) string {
	switch {
	case inferred:
		return EvidenceInferred
	case l == LayerDeclared || l == LayerOverride || l == LayerAnnotation:
		return EvidenceDeclared
	case l == LayerImported:
		return EvidenceStructural
	}
	return EvidenceObserved
}

type endpoint struct {
	id, project, kind string
	tombstoned        bool
}

func loadEndpoint(ctx context.Context, tx *store.Tx, id string) (*endpoint, error) {
	var e endpoint
	var t sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT id, project_id, kind, tombstoned_at FROM entities WHERE id = ?`, id).Scan(&e.id, &e.project, &e.kind, &t)
	if err != nil {
		return nil, err
	}
	e.tombstoned = t.Valid
	return &e, nil
}

// ApplyRelation upserts (or, with Delete, retracts) the source's assertion of a relationship.
func (s *Service) ApplyRelation(ctx context.Context, tx *store.Tx, src *Source, it *RelationItem, o ApplyOpts) (*RelationResult, error) {
	start := time.Now()
	res, err := s.applyRelation(ctx, tx, src, it, o)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	} else if res.Action == ActionStale || res.Action == ActionUnresolved {
		outcome = res.Action
	}
	s.Metrics.Reconcile("relation", outcome, time.Since(start))
	return res, err
}

func (s *Service) applyRelation(ctx context.Context, tx *store.Tx, src *Source, it *RelationItem, o ApplyOpts) (*RelationResult, error) {
	if !src.Enabled {
		return nil, apperr.New(apperr.CodeForbidden, "source is disabled")
	}
	// Retraction by the source's external ID needs no endpoints.
	if it.Delete && it.ExternalID != "" && it.From.Empty() {
		var rid string
		err := tx.QueryRow(ctx, `SELECT relation_id FROM relation_assertions WHERE source_id = ? AND external_id = ? AND tombstoned_at IS NULL`,
			src.ID, it.ExternalID).Scan(&rid)
		if errors.Is(err, sql.ErrNoRows) {
			return &RelationResult{Action: ActionNotFound}, nil
		}
		if err != nil {
			return nil, err
		}
		return s.retract(ctx, tx, src, rid, it.Revision, o)
	}
	rt := s.reg.rel(it.Type)
	if rt == nil {
		return nil, apperr.New(apperr.CodeInvalid, "unknown relationship type %q", it.Type)
	}
	fromID, err := s.ResolveRef(ctx, tx, src, it.From)
	if err != nil {
		return nil, err
	}
	toID, err := s.ResolveRef(ctx, tx, src, it.To)
	if err != nil {
		return nil, err
	}
	if fromID == "" || toID == "" {
		if it.Delete {
			return &RelationResult{Action: ActionNotFound}, nil
		}
		return s.recordUnresolved(ctx, tx, src, it, fromID)
	}
	if fromID == toID {
		return nil, apperr.New(apperr.CodeInvalid, "a relationship needs two different entities")
	}
	from, err := loadEndpoint(ctx, tx, fromID)
	if err != nil {
		return nil, err
	}
	to, err := loadEndpoint(ctx, tx, toID)
	if err != nil {
		return nil, err
	}
	var relID string
	err = tx.QueryRow(ctx, `SELECT id FROM relations WHERE type = ? AND from_id = ? AND to_id = ?`, rt.Name, fromID, toID).Scan(&relID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if it.Delete {
		if relID == "" {
			return &RelationResult{Action: ActionNotFound}, nil
		}
		return s.retract(ctx, tx, src, relID, it.Revision, o)
	}
	v := &apperr.Validation{}
	if from.project != src.ProjectID && to.project != src.ProjectID {
		v.Add("from", "a source can only assert relationships touching its own project")
	}
	if len(rt.FromKinds) > 0 && !slices.Contains(rt.FromKinds, from.kind) {
		v.Add("from", "%s cannot start a %s relationship (allowed: %v)", from.kind, rt.Name, rt.FromKinds)
	}
	if len(rt.ToKinds) > 0 && !slices.Contains(rt.ToKinds, to.kind) {
		v.Add("to", "%s cannot end a %s relationship (allowed: %v)", to.kind, rt.Name, rt.ToKinds)
	}
	var bridgeID sql.NullString
	if from.project != to.project {
		bid, ok, err := s.bridgeFor(ctx, tx, from, to, rt.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			v.Add("to", "cross-project relationships need a bridge entity endpoint or a declared bridge between the projects (ADR-004)")
		}
		bridgeID = sql.NullString{String: bid, Valid: bid != ""}
	}
	if it.Inferred && len(it.Evidence) == 0 {
		v.Add("evidence", "inferred relationships must explain their evidence")
	}
	if rt.attrs != nil && len(it.Attributes) > 0 {
		for _, fe := range rt.attrs.Validate(it.Attributes) {
			v.Add("attributes"+fe.Field[len("spec"):], "%s", fe.Message)
		}
	}
	CheckSecretValues("attributes", it.Attributes, v)
	ev := it.EvidenceClass
	if ev == "" {
		ev = defaultEvidence(src.Layer, it.Inferred)
	}
	if _, ok := evidenceRank[ev]; !ok {
		v.Add("evidence_class", "must be declared, structural, observed or inferred")
	}
	conf := 1.0
	if it.Confidence != nil {
		conf = *it.Confidence
		if conf < 0 || conf > 1 {
			v.Add("confidence", "must be within 0..1")
		}
	}
	if len(rawJSON(it.Evidence)) > 16<<10 || len(rawJSON(it.Attributes)) > 64<<10 {
		v.Add("item", "evidence (16 KiB) or attributes (64 KiB) too large")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	created := false
	if relID == "" {
		relID = ids.New(ids.Relation)
		created = true
		if _, err := tx.Exec(ctx, `INSERT INTO relations (id, project_id, type, from_id, to_id, to_project_id, bridge_id, created_at, updated_at, live)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, relID, from.project, rt.Name, fromID, toID, to.project, bridgeID, tx.NowMicros(), tx.NowMicros()); err != nil {
			return nil, err
		}
	}
	observed := tx.Now
	if it.ObservedAt != nil {
		observed = it.ObservedAt.UTC()
	}
	var expires sql.NullInt64
	if it.ExpiresAt != nil {
		expires = sql.NullInt64{Int64: it.ExpiresAt.UnixMicro(), Valid: true}
	} else if src.Layer == LayerObservation || (src.Layer == LayerDiscovered && src.Policy.DefaultTTLSeconds > 0) {
		ttl := time.Duration(src.Policy.DefaultTTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = time.Duration(max(src.ExpectedIntervalSeconds, 1200)) * 3 * time.Second
		}
		expires = sql.NullInt64{Int64: tx.Now.Add(ttl).UnixMicro(), Valid: true}
	}
	var aid string
	var curRev, tombRev int64
	var tomb sql.NullInt64
	var curEvidence, curAttrs string
	err = tx.QueryRow(ctx, `SELECT id, revision, tombstone_revision, tombstoned_at, evidence_json, attributes_json FROM relation_assertions
		WHERE relation_id = ? AND source_id = ?`, relID, src.ID).Scan(&aid, &curRev, &tombRev, &tomb, &curEvidence, &curAttrs)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists {
		if o.CreateOnly && !tomb.Valid {
			return nil, apperr.New(apperr.CodeAlreadyExists, "relationship %s is already declared by source %q", relID, src.Name)
		}
		if it.Revision > 0 && (it.Revision <= curRev || it.Revision <= tombRev) {
			return &RelationResult{RelationID: relID, Action: ActionStale}, nil
		}
	}
	rev := it.Revision
	if rev == 0 {
		rev = max(curRev, tombRev) + 1
	}
	evJSON, attrJSON := rawJSON(it.Evidence), rawJSON(it.Attributes)
	if exists {
		if _, err := tx.Exec(ctx, `UPDATE relation_assertions SET layer = ?, evidence_class = ?, inferred = ?, evidence_json = ?, confidence = ?, attributes_json = ?,
			generation = MAX(generation, ?), revision = ?, external_id = ?, last_seen_at = ?, expires_at = ?, tombstoned_at = NULL,
			first_seen_at = CASE WHEN tombstoned_at IS NOT NULL THEN ? ELSE first_seen_at END WHERE id = ?`,
			string(src.Layer), ev, it.Inferred, evJSON, conf, attrJSON, o.Generation, rev, it.ExternalID, observed.UnixMicro(), expires,
			observed.UnixMicro(), aid); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(ctx, `INSERT INTO relation_assertions (id, relation_id, source_id, layer, evidence_class, inferred, evidence_json, confidence,
			attributes_json, generation, revision, external_id, first_seen_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ids.New(ids.Assertion), relID, src.ID, string(src.Layer), ev, it.Inferred, evJSON, conf, attrJSON, o.Generation, rev, it.ExternalID,
			observed.UnixMicro(), observed.UnixMicro(), expires); err != nil {
			return nil, err
		}
	}
	// The target exists now: forget an earlier unresolved record.
	if _, err := tx.Exec(ctx, `DELETE FROM unresolved_refs WHERE source_id = ? AND type = ? AND from_ref = ? AND to_ref = ?`,
		src.ID, it.Type, it.From.String(), it.To.String()); err != nil {
		return nil, err
	}
	unchanged := exists && !tomb.Valid && curEvidence == evJSON && curAttrs == attrJSON
	if unchanged {
		return &RelationResult{RelationID: relID, Action: ActionUnchanged}, nil
	}
	if err := s.recomputeRelationTx(ctx, tx, relID, o.Actor, src.ID); err != nil {
		return nil, err
	}
	action := ActionUpdated
	if created {
		action = ActionCreated
	}
	return &RelationResult{RelationID: relID, Action: action}, nil
}

func (s *Service) bridgeFor(ctx context.Context, tx *store.Tx, from, to *endpoint, typ string) (string, bool, error) {
	if from.kind == "bridge" || to.kind == "bridge" {
		return "", true, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, allowed_types FROM bridges WHERE (from_project = ? AND to_project = ?) OR (from_project = ? AND to_project = ?)`,
		from.project, to.project, to.project, from.project)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return "", false, err
		}
		var types []string
		_ = json.Unmarshal([]byte(at), &types)
		if len(types) == 0 || slices.Contains(types, typ) {
			return id, true, nil
		}
	}
	return "", false, rows.Err()
}

func (s *Service) recordUnresolved(ctx context.Context, tx *store.Tx, src *Source, it *RelationItem, fromID string) (*RelationResult, error) {
	if s.reg.rel(it.Type) == nil {
		return nil, apperr.New(apperr.CodeInvalid, "unknown relationship type %q", it.Type)
	}
	var from sql.NullString
	if fromID != "" {
		from = sql.NullString{String: fromID, Valid: true}
	}
	_, err := tx.Exec(ctx, `INSERT INTO unresolved_refs (id, source_id, project_id, type, from_ref, to_ref, from_entity, external_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (source_id, type, from_ref, to_ref) DO UPDATE SET last_seen_at = excluded.last_seen_at,
		from_entity = excluded.from_entity`, ids.New(ids.Relation), src.ID, src.ProjectID, it.Type, it.From.String(), it.To.String(), from, it.ExternalID,
		tx.NowMicros(), tx.NowMicros())
	if err != nil {
		return nil, err
	}
	missing := it.To.String()
	if fromID == "" {
		missing = it.From.String()
	}
	return &RelationResult{Action: ActionUnresolved, Detail: "endpoint not found: " + missing}, nil
}

func (s *Service) retract(ctx context.Context, tx *store.Tx, src *Source, relID string, revision int64, o ApplyOpts) (*RelationResult, error) {
	var aid string
	var curRev, tombRev int64
	var tomb sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT id, revision, tombstone_revision, tombstoned_at FROM relation_assertions WHERE relation_id = ? AND source_id = ?`,
		relID, src.ID).Scan(&aid, &curRev, &tombRev, &tomb)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && tomb.Valid) {
		return &RelationResult{RelationID: relID, Action: ActionNotFound}, nil
	}
	if err != nil {
		return nil, err
	}
	if revision > 0 && (revision <= curRev || revision <= tombRev) {
		return &RelationResult{RelationID: relID, Action: ActionStale}, nil
	}
	tr := revision
	if tr == 0 {
		tr = curRev + 1
	}
	if _, err := tx.Exec(ctx, `UPDATE relation_assertions SET tombstoned_at = ?, tombstone_revision = ?, revision = ? WHERE id = ?`,
		tx.NowMicros(), tr, tr, aid); err != nil {
		return nil, err
	}
	if err := s.recomputeRelationTx(ctx, tx, relID, o.Actor, src.ID); err != nil {
		return nil, err
	}
	return &RelationResult{RelationID: relID, Action: ActionDeleted}, nil
}

// TombstoneAssertionsTx retracts the given assertions (snapshot absence).
func (s *Service) TombstoneAssertionsTx(ctx context.Context, tx *store.Tx, src *Source, assertionIDs []string, actor audit.Actor) error {
	rels := map[string]bool{}
	for _, aid := range assertionIDs {
		var rid string
		if err := tx.QueryRow(ctx, `SELECT relation_id FROM relation_assertions WHERE id = ? AND source_id = ?`, aid, src.ID).Scan(&rid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE relation_assertions SET tombstoned_at = ?, tombstone_revision = revision WHERE id = ?`, tx.NowMicros(), aid); err != nil {
			return err
		}
		rels[rid] = true
	}
	keys := make([]string, 0, len(rels))
	for k := range rels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rid := range keys {
		if err := s.recomputeRelationTx(ctx, tx, rid, actor, src.ID); err != nil {
			return err
		}
	}
	return nil
}

// recomputeRelationTx derives a relation's liveness, evidence class and attributes from its live assertions.
func (s *Service) recomputeRelationTx(ctx context.Context, tx *store.Tx, relID string, actor audit.Actor, sourceID string) error {
	var project, typ, fromID, toID, curAttrs, curEv string
	var live, inferred int64
	err := tx.QueryRow(ctx, `SELECT project_id, type, from_id, to_id, live, attributes_json, evidence_class, inferred FROM relations WHERE id = ?`, relID).
		Scan(&project, &typ, &fromID, &toID, &live, &curAttrs, &curEv, &inferred)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT a.evidence_class, a.inferred, a.attributes_json, a.last_seen_at, a.layer, s.policy_json, a.source_id
		FROM relation_assertions a JOIN sources s ON s.id = a.source_id
		WHERE a.relation_id = ? AND a.tombstoned_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > ?)`, relID, tx.NowMicros())
	if err != nil {
		return err
	}
	type asr struct {
		ev       string
		inferred bool
		attrs    map[string]any
		seen     int64
		prio     int
		src      string
	}
	var as []asr
	for rows.Next() {
		var a asr
		var attrs, layer, pol string
		if err := rows.Scan(&a.ev, &a.inferred, &attrs, &a.seen, &layer, &pol, &a.src); err != nil {
			rows.Close()
			return err
		}
		a.attrs = decodeObject(attrs)
		var p Policy
		_ = json.Unmarshal([]byte(pol), &p)
		a.prio = Layer(layer).DefaultPriority()
		if p.Priority != nil {
			a.prio = *p.Priority
		}
		as = append(as, a)
	}
	rows.Close()
	newLive := len(as) > 0
	best, allInferred := EvidenceInferred, true
	attrs := map[string]any{}
	var lastSeen sql.NullInt64
	sort.Slice(as, func(i, j int) bool {
		if as[i].prio != as[j].prio {
			return as[i].prio < as[j].prio // lower first so higher precedence overwrites
		}
		return as[i].src > as[j].src
	})
	for _, a := range as {
		if evidenceRank[a.ev] > evidenceRank[best] {
			best = a.ev
		}
		if !a.inferred {
			allInferred = false
		}
		for k, v := range a.attrs {
			attrs[k] = v
		}
		if !lastSeen.Valid || a.seen > lastSeen.Int64 {
			lastSeen = sql.NullInt64{Int64: a.seen, Valid: true}
		}
	}
	if !newLive {
		best, allInferred = curEv, inferred == 1
	}
	attrJSON := rawJSON(attrs)
	if !newLive && live == 0 {
		return nil
	}
	var tomb sql.NullInt64
	if !newLive {
		tomb = sql.NullInt64{Int64: tx.NowMicros(), Valid: true}
	}
	if _, err := tx.Exec(ctx, `UPDATE relations SET live = ?, evidence_class = ?, inferred = ?, attributes_json = ?, last_seen_at = COALESCE(?, last_seen_at),
		tombstoned_at = ?, updated_at = ? WHERE id = ?`, newLive, best, allInferred && newLive, attrJSON, lastSeen, tomb, tx.NowMicros(), relID); err != nil {
		return err
	}
	action := ""
	switch {
	case newLive && live == 0:
		action = "created"
	case !newLive && live == 1:
		action = "removed"
	case attrJSON != curAttrs || best != curEv || (allInferred && newLive) != (inferred == 1):
		action = "updated"
	}
	if action == "" {
		return nil
	}
	summary := map[string]any{"type": typ, "from": fromID, "to": toID, "evidence": best}
	if err := s.changeTx(ctx, tx, change{project: project, typ: "relation", id: relID, action: action, actor: actor, sourceID: sourceID, summary: summary}); err != nil {
		return err
	}
	if action != "updated" {
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "relation", Action: action, ProjectID: project,
			Payload: map[string]any{"relation_id": relID, "type": typ, "from_id": fromID, "to_id": toID, "evidence": best}}); err != nil {
			return err
		}
	}
	return nil
}

// moveRelations re-points relations of `from` to `into` (entity merge), merging duplicates.
func (s *Service) moveRelations(ctx context.Context, tx *store.Tx, from, into string, actor audit.Actor) error {
	for _, col := range []string{"from_id", "to_id"} {
		rels, err := stringsQuery(ctx, tx, `SELECT id FROM relations WHERE `+col+` = ?`, from)
		if err != nil {
			return err
		}
		for _, rid := range rels {
			var typ, other string
			otherCol := "to_id"
			if col == "to_id" {
				otherCol = "from_id"
			}
			if err := tx.QueryRow(ctx, `SELECT type, `+otherCol+` FROM relations WHERE id = ?`, rid).Scan(&typ, &other); err != nil {
				return err
			}
			if other == into {
				if _, err := tx.Exec(ctx, `DELETE FROM relations WHERE id = ?`, rid); err != nil { // would become a self-loop
					return err
				}
				continue
			}
			var dup string
			q := `SELECT id FROM relations WHERE type = ? AND from_id = ? AND to_id = ?`
			args := []any{typ, into, other}
			if col == "to_id" {
				args = []any{typ, other, into}
			}
			err := tx.QueryRow(ctx, q, args...).Scan(&dup)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := tx.Exec(ctx, `UPDATE relations SET `+col+` = ? WHERE id = ?`, into, rid); err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if _, err := tx.Exec(ctx, `UPDATE OR IGNORE relation_assertions SET relation_id = ? WHERE relation_id = ?`, dup, rid); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM relations WHERE id = ?`, rid); err != nil {
					return err
				}
				if err := s.recomputeRelationTx(ctx, tx, dup, actor, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ExpireTx recomputes entities and relations whose facets or assertions expired since the last sweep.
func (s *Service) ExpireTx(ctx context.Context, tx *store.Tx, since time.Time, limit int) (int, error) {
	ents, err := stringsQuery(ctx, tx, `SELECT DISTINCT entity_id FROM facets WHERE tombstoned_at IS NULL AND expires_at IS NOT NULL
		AND expires_at > ? AND expires_at <= ? LIMIT ?`, since.UnixMicro(), tx.NowMicros(), limit)
	if err != nil {
		return 0, err
	}
	for _, id := range ents {
		if _, err := s.recomputeTx(ctx, tx, id, recomputeOpts{actor: audit.System}); err != nil {
			return 0, err
		}
	}
	rels, err := stringsQuery(ctx, tx, `SELECT DISTINCT relation_id FROM relation_assertions WHERE tombstoned_at IS NULL AND expires_at IS NOT NULL
		AND expires_at > ? AND expires_at <= ? LIMIT ?`, since.UnixMicro(), tx.NowMicros(), limit)
	if err != nil {
		return 0, err
	}
	for _, id := range rels {
		if err := s.recomputeRelationTx(ctx, tx, id, audit.System, ""); err != nil {
			return 0, err
		}
	}
	return len(ents) + len(rels), nil
}

// Compact permanently deletes entities and relations tombstoned before cutoff, and tombstoned facets/assertions.
func (s *Service) Compact(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	err := s.Store.W().TxNamed(ctx, "catalog.compact", func(tx *store.Tx) error {
		total = 0
		c := cutoff.UnixMicro()
		for _, q := range []string{
			`DELETE FROM relations WHERE live = 0 AND tombstoned_at IS NOT NULL AND tombstoned_at < ?`,
			`DELETE FROM relation_assertions WHERE tombstoned_at IS NOT NULL AND tombstoned_at < ?`,
			`DELETE FROM entities WHERE tombstoned_at IS NOT NULL AND tombstoned_at < ?`,
			`DELETE FROM facets WHERE tombstoned_at IS NOT NULL AND tombstoned_at < ?`,
			`DELETE FROM unresolved_refs WHERE last_seen_at < ?`,
		} {
			n, err := tx.ExecN(ctx, q, c)
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	})
	return total, err
}

// PruneChangeLog deletes change-log rows older than cutoff.
func (s *Service) PruneChangeLog(ctx context.Context, cutoff time.Time) error {
	return s.Store.W().TxNamed(ctx, "catalog.prune_changes", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM change_log WHERE at < ?`, cutoff.UnixMicro())
		return err
	})
}

// RefreshFreshness recomputes the freshness column for entities with runtime facets (freshness ages without writes).
func (s *Service) RefreshFreshness(ctx context.Context) error {
	ids, err := func() ([]string, error) {
		rows, err := s.Store.R().QueryContext(ctx, `SELECT id FROM entities WHERE tombstoned_at IS NULL AND last_seen_at IS NOT NULL`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}()
	if err != nil {
		return err
	}
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		if err := s.Store.W().TxNamed(ctx, "catalog.freshness", func(tx *store.Tx) error {
			for _, id := range ids[start:end] {
				if err := s.touchFreshness(ctx, tx, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("refresh freshness: %w", err)
		}
	}
	return nil
}
