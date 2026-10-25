// Package ingest implements the source protocol (ADR-003, ADR-006): incremental items, staged snapshots with
// generations, atomic commit with absence handling and a mass-removal guard, dry-run previews, heartbeats and
// observations. Built-in providers, the push API, Pulse ingest and Terraform imports all go through it, so every
// path has the same reconciliation semantics.
package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Limits bound ingest requests.
type Limits struct {
	MaxItems          int           // per request / batch
	MaxSnapshotItems  int           // per snapshot
	SnapshotTimeout   time.Duration // open snapshots expire after this
	MaxRemovalPercent float64       // default mass-removal guard
}

// Service runs the source protocol.
type Service struct {
	Store   *store.Store
	Catalog *catalog.Service
	Audit   *audit.Writer
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Limits  Limits
}

// ItemError is a structured per-item error.
type ItemError struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Fields  []apperr.FieldError `json:"fields,omitempty"`
}

// ItemResult is the outcome of one item.
type ItemResult struct {
	Index      int        `json:"index"`
	Type       string     `json:"type"` // entity or relation
	Action     string     `json:"action"`
	EntityID   string     `json:"entity_id,omitempty"`
	RelationID string     `json:"relation_id,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Error      *ItemError `json:"error,omitempty"`
}

// Batch is a set of items.
type Batch struct {
	Entities  []catalog.EntityItem   `json:"entities,omitempty"`
	Relations []catalog.RelationItem `json:"relations,omitempty"`
}

// Len counts items.
func (b Batch) Len() int { return len(b.Entities) + len(b.Relations) }

// Summary counts outcomes.
type Summary struct {
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Deleted    int `json:"deleted"`
	Stale      int `json:"stale"`
	NotFound   int `json:"not_found"`
	Unresolved int `json:"unresolved"`
	Errors     int `json:"errors"`
	// Removed counts facets/assertions tombstoned because a complete snapshot no longer contained them.
	Removed  int `json:"removed"`
	Orphaned int `json:"orphaned"`
}

func (s *Summary) add(r ItemResult) {
	if r.Error != nil {
		s.Errors++
		return
	}
	switch r.Action {
	case catalog.ActionCreated:
		s.Created++
	case catalog.ActionUpdated:
		s.Updated++
	case catalog.ActionUnchanged:
		s.Unchanged++
	case catalog.ActionDeleted:
		s.Deleted++
	case catalog.ActionStale:
		s.Stale++
	case catalog.ActionNotFound:
		s.NotFound++
	case catalog.ActionUnresolved:
		s.Unresolved++
	}
}

// ItemsResult answers an incremental request.
type ItemsResult struct {
	Results []ItemResult `json:"results"`
	Summary Summary      `json:"summary"`
	Applied bool         `json:"applied" doc:"False when an atomic batch was rejected."`
}

func toItemError(err error) *ItemError {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return &ItemError{Code: string(ae.Code), Message: ae.Message, Fields: ae.Fields}
	}
	return nil
}

// CheckSize enforces the per-request item limit.
func (s *Service) CheckSize(b Batch) error {
	if n := b.Len(); n == 0 || n > s.Limits.MaxItems {
		return apperr.New(apperr.CodeInvalid, "a request carries 1..%d items, got %d", s.Limits.MaxItems, n)
	}
	return nil
}

// applyBatch applies items inside tx, isolating each with a savepoint so one bad item never leaves partial writes.
func (s *Service) applyBatch(ctx context.Context, tx *store.Tx, src *catalog.Source, b Batch, o catalog.ApplyOpts, offset int) ([]ItemResult, error) {
	results := make([]ItemResult, 0, b.Len())
	run := func(idx int, typ string, fn func() (ItemResult, error)) error {
		if _, err := tx.Exec(ctx, `SAVEPOINT item`); err != nil {
			return err
		}
		r, err := fn()
		r.Index, r.Type = idx, typ
		if err != nil {
			ie := toItemError(err)
			if ie == nil {
				return err // storage failure: abort the whole transaction
			}
			if _, rerr := tx.Exec(ctx, `ROLLBACK TO item`); rerr != nil {
				return rerr
			}
			r.Error = ie
		}
		if _, err := tx.Exec(ctx, `RELEASE item`); err != nil {
			return err
		}
		results = append(results, r)
		return nil
	}
	for i := range b.Entities {
		it := &b.Entities[i]
		if err := run(offset+i, "entity", func() (ItemResult, error) {
			res, err := s.Catalog.ApplyEntity(ctx, tx, src, it, o)
			if err != nil {
				return ItemResult{}, err
			}
			return ItemResult{Action: res.Action, EntityID: res.EntityID}, nil
		}); err != nil {
			return nil, err
		}
	}
	for i := range b.Relations {
		it := &b.Relations[i]
		if err := run(offset+len(b.Entities)+i, "relation", func() (ItemResult, error) {
			res, err := s.Catalog.ApplyRelation(ctx, tx, src, it, o)
			if err != nil {
				return ItemResult{}, err
			}
			return ItemResult{Action: res.Action, RelationID: res.RelationID, Detail: res.Detail}, nil
		}); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// Items applies an incremental batch (ADR-003). With atomic, any item error rejects the whole batch.
func (s *Service) Items(ctx context.Context, src *catalog.Source, b Batch, atomic bool, actor audit.Actor) (*ItemsResult, error) {
	if err := s.CheckSize(b); err != nil {
		return nil, err
	}
	start := s.Clock.Now()
	var out ItemsResult
	errRejected := errors.New("atomic batch rejected")
	err := s.Store.W().TxNamed(ctx, "ingest.items", func(tx *store.Tx) error {
		out = ItemsResult{}
		res, err := s.applyBatch(ctx, tx, src, b, catalog.ApplyOpts{Actor: actor}, 0)
		if err != nil {
			return err
		}
		out.Results = res
		for _, r := range res {
			out.Summary.add(r)
		}
		if atomic && out.Summary.Errors > 0 {
			return errRejected
		}
		out.Applied = true
		if err := catalog.RecordRun(ctx, tx, src.ID, true, "", 0); err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: src.ProjectID, Action: "ingest.items", Target: audit.Target{Type: "source", ID: src.ID},
			Details: summaryDetails(out.Summary)})
		return err
	})
	if errors.Is(err, errRejected) {
		out.Applied = false
		err = nil
	}
	if err != nil {
		return nil, err
	}
	s.observe(src, "items", out.Summary, s.Clock.Now().Sub(start))
	return &out, nil
}

func summaryDetails(s Summary) map[string]any {
	return map[string]any{"created": s.Created, "updated": s.Updated, "unchanged": s.Unchanged, "deleted": s.Deleted, "stale": s.Stale,
		"errors": s.Errors, "unresolved": s.Unresolved, "removed": s.Removed}
}

func (s *Service) observe(src *catalog.Source, op string, sum Summary, d time.Duration) {
	s.Metrics.IngestItems(src.Provider, "created", sum.Created)
	s.Metrics.IngestItems(src.Provider, "updated", sum.Updated)
	s.Metrics.IngestItems(src.Provider, "deleted", sum.Deleted+sum.Removed)
	s.Metrics.IngestRejected(src.Provider, "stale", sum.Stale)
	s.Metrics.IngestRejected(src.Provider, "invalid", sum.Errors)
	s.Metrics.IngestRejected(src.Provider, "unresolved", sum.Unresolved)
	outcome := "ok"
	if sum.Errors > 0 {
		outcome = "partial"
	}
	s.Metrics.Reconcile(op, outcome, d)
}

// Snapshot is a staged complete or partial snapshot of a source.
type Snapshot struct {
	ID         string     `json:"id"`
	SourceID   string     `json:"source_id"`
	Generation int64      `json:"generation"`
	Complete   bool       `json:"complete"`
	State      string     `json:"state"`
	ItemCount  int        `json:"item_count"`
	ErrorCount int        `json:"error_count"`
	Stats      *Summary   `json:"stats,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func scanSnapshot(r interface{ Scan(...any) error }) (*Snapshot, error) {
	var s Snapshot
	var stats string
	var c, e int64
	var f sql.NullInt64
	if err := r.Scan(&s.ID, &s.SourceID, &s.Generation, &s.Complete, &s.State, &s.ItemCount, &s.ErrorCount, &stats, &s.Error, &c, &e, &f); err != nil {
		return nil, err
	}
	if stats != "" && stats != "{}" {
		s.Stats = &Summary{}
		_ = json.Unmarshal([]byte(stats), s.Stats)
	}
	s.CreatedAt, s.ExpiresAt = clock.FromMicros(c), clock.FromMicros(e)
	if f.Valid {
		t := clock.FromMicros(f.Int64)
		s.FinishedAt = &t
	}
	return &s, nil
}

const snapCols = `id, source_id, generation, complete, state, item_count, error_count, stats_json, error, created_at, expires_at, finished_at`

// ErrSnapshotNotFound is returned for unknown snapshots.
var ErrSnapshotNotFound = apperr.New(apperr.CodeNotFound, "snapshot not found")

// GetSnapshot returns a snapshot of src.
func (s *Service) GetSnapshot(ctx context.Context, src *catalog.Source, id string) (*Snapshot, error) {
	sn, err := scanSnapshot(s.Store.R().QueryRowContext(ctx, `SELECT `+snapCols+` FROM snapshots WHERE id = ? AND source_id = ?`, id, src.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSnapshotNotFound
	}
	return sn, err
}

// Begin opens a snapshot. The generation must increase per source; generation 0 picks last+1.
func (s *Service) Begin(ctx context.Context, src *catalog.Source, generation int64, complete bool, actor audit.Actor) (*Snapshot, error) {
	var sn *Snapshot
	err := s.Store.W().TxNamed(ctx, "ingest.snapshot_begin", func(tx *store.Tx) error {
		cur, err := catalog.SourceTx(ctx, tx, src.ID)
		if err != nil {
			return err
		}
		if err := s.expireTx(ctx, tx, src.ID); err != nil {
			return err
		}
		var open string
		if err := tx.QueryRow(ctx, `SELECT id FROM snapshots WHERE source_id = ? AND state = 'open'`, src.ID).Scan(&open); err == nil {
			return apperr.New(apperr.CodeConflict, "snapshot %s is still open for this source; commit or abort it first", open)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if generation == 0 {
			generation = cur.LastGeneration + 1
		}
		if generation <= cur.LastGeneration {
			return &apperr.Error{Code: "stale_generation", Message: fmt.Sprintf("generation %d is not newer than the last committed generation %d",
				generation, cur.LastGeneration)}
		}
		id := ids.New(ids.Snapshot)
		if _, err := tx.Exec(ctx, `INSERT INTO snapshots (id, source_id, generation, complete, state, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, 'open', ?, ?, ?)`, id, src.ID, generation, complete, actor.ID, tx.NowMicros(),
			tx.Now.Add(s.Limits.SnapshotTimeout).UnixMicro()); err != nil {
			return err
		}
		sn, err = scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapCols+` FROM snapshots WHERE id = ?`, id))
		return err
	})
	return sn, err
}

// BatchResult answers a staged batch: structural validation only (catalog effects appear at commit).
type BatchResult struct {
	Seq      int          `json:"seq"`
	Accepted int          `json:"accepted"`
	Errors   []ItemResult `json:"errors"`
	Replayed bool         `json:"replayed"`
}

// PutBatch stages a batch. Re-sending the same seq with the same content is idempotent; different content is a conflict.
func (s *Service) PutBatch(ctx context.Context, src *catalog.Source, snapshotID string, seq int, b Batch) (*BatchResult, error) {
	if seq < 1 || seq > 1_000_000 {
		return nil, apperr.New(apperr.CodeInvalid, "seq must be within 1..1000000")
	}
	if err := s.CheckSize(b); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(b)
	hash := sha256.Sum256(raw)
	errs := s.validate(src, b)
	res := &BatchResult{Seq: seq, Errors: errs}
	err := s.Store.W().TxNamed(ctx, "ingest.snapshot_batch", func(tx *store.Tx) error {
		var state string
		var count int
		var exp int64
		err := tx.QueryRow(ctx, `SELECT state, item_count, expires_at FROM snapshots WHERE id = ? AND source_id = ?`, snapshotID, src.ID).Scan(&state, &count, &exp)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSnapshotNotFound
		}
		if err != nil {
			return err
		}
		var prev []byte
		var prevErrs string
		err = tx.QueryRow(ctx, `SELECT request_hash, errors_json FROM snapshot_batches WHERE snapshot_id = ? AND seq = ?`, snapshotID, seq).Scan(&prev, &prevErrs)
		if err == nil {
			if string(prev) != string(hash[:]) {
				return apperr.New(apperr.CodeConflict, "batch %d was already received with different content", seq)
			}
			res.Replayed = true
			_ = json.Unmarshal([]byte(prevErrs), &res.Errors)
			res.Accepted = b.Len() - len(res.Errors)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state != "open" || !tx.Now.Before(clock.FromMicros(exp)) {
			return apperr.New(apperr.CodeConflict, "snapshot is %s; begin a new one", map[bool]string{true: state, false: "expired"}[state != "open"])
		}
		if count+b.Len() > s.Limits.MaxSnapshotItems {
			return apperr.New(apperr.CodeInvalid, "snapshot exceeds %d items", s.Limits.MaxSnapshotItems)
		}
		for i, it := range b.Entities {
			j, _ := json.Marshal(stagedItem{Entity: &it})
			if _, err := tx.Exec(ctx, `INSERT INTO snapshot_items (snapshot_id, seq, idx, item_json) VALUES (?, ?, ?, ?)`, snapshotID, seq, i, string(j)); err != nil {
				return err
			}
		}
		for i, it := range b.Relations {
			j, _ := json.Marshal(stagedItem{Relation: &it})
			if _, err := tx.Exec(ctx, `INSERT INTO snapshot_items (snapshot_id, seq, idx, item_json) VALUES (?, ?, ?, ?)`, snapshotID, seq, len(b.Entities)+i, string(j)); err != nil {
				return err
			}
		}
		ej, _ := json.Marshal(errs)
		if _, err := tx.Exec(ctx, `INSERT INTO snapshot_batches (snapshot_id, seq, request_hash, item_count, error_count, errors_json, received_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, snapshotID, seq, hash[:], b.Len(), len(errs), string(ej), tx.NowMicros()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE snapshots SET item_count = item_count + ?, error_count = error_count + ? WHERE id = ?`, b.Len(), len(errs), snapshotID)
		res.Accepted = b.Len() - len(errs)
		return err
	})
	if err != nil {
		return nil, err
	}
	if res.Errors == nil {
		res.Errors = []ItemResult{}
	}
	return res, nil
}

type stagedItem struct {
	Entity   *catalog.EntityItem   `json:"e,omitempty"`
	Relation *catalog.RelationItem `json:"r,omitempty"`
}

// validate performs the structural checks that need no database (kind and type exist, names, metadata shape).
func (s *Service) validate(src *catalog.Source, b Batch) []ItemResult {
	var out []ItemResult
	for i, it := range b.Entities {
		v := &apperr.Validation{}
		if it.Kind != "" && s.Catalog.Kind(it.Kind) == nil {
			v.Add("kind", "unknown kind %q", it.Kind)
		}
		if it.Ref.Empty() && it.ExternalID == "" && (it.Kind == "" || it.Name == "") {
			v.Add("ref", "item needs ref.id, external_id or kind+name")
		}
		if it.Name != "" && !catalog.ValidName(it.Name) {
			v.Add("name", "must be a lowercase slug")
		}
		catalog.ValidateMetadata(it.Metadata, true, v)
		if err := v.Err(); err != nil {
			out = append(out, ItemResult{Index: i, Type: "entity", Error: toItemError(err)})
		}
	}
	for i, it := range b.Relations {
		if it.ExternalID != "" && it.Delete {
			continue
		}
		if s.Catalog.RelType(it.Type) == nil {
			out = append(out, ItemResult{Index: len(b.Entities) + i, Type: "relation", Error: &ItemError{Code: string(apperr.CodeInvalid),
				Message: fmt.Sprintf("unknown relationship type %q", it.Type)}})
		}
	}
	return out
}

// CommitOptions tune a commit.
type CommitOptions struct {
	DryRun             bool `json:"dry_run,omitempty"`
	AllowMassRemoval   bool `json:"allow_mass_removal,omitempty"`
	AllowPartialErrors bool `json:"allow_partial_errors,omitempty" doc:"Tombstone absent facts even though some items failed."`
}

// CommitResult reports a snapshot commit or preview.
type CommitResult struct {
	Snapshot *Snapshot    `json:"snapshot"`
	Summary  Summary      `json:"summary"`
	Results  []ItemResult `json:"results" doc:"Per-item outcomes (errors, unresolved and stale items only; capped at 500)."`
	Removed  []Removal    `json:"removed" doc:"Facts no longer present (capped at 500)."`
	DryRun   bool         `json:"dry_run"`
	Changes  []Change     `json:"changes,omitempty" doc:"Preview only: what the commit would change (capped at 500)."`
}

// Removal is an absent fact.
type Removal struct {
	Type     string `json:"type"`
	EntityID string `json:"entity_id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	ID       string `json:"id"`
	Action   string `json:"action"` // tombstone, orphan, keep
}

// Change is a previewed effective change.
type Change struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Action string `json:"action"`
	Label  string `json:"label"`
}

var errDryRun = errors.New("dry run")

// ErrMassRemoval is the mass-removal guard (ADR-003).
type ErrMassRemoval struct {
	Removing, Live int
	Limit          float64
}

func (e *ErrMassRemoval) Error() string {
	return fmt.Sprintf("commit would remove %d of %d live facts (%.0f%% > %.0f%% guard); set allow_mass_removal to proceed",
		e.Removing, e.Live, 100*float64(e.Removing)/float64(max(e.Live, 1)), e.Limit)
}

// Commit applies every staged item and, for complete snapshots, handles absent facts — atomically. A failed or
// refused commit leaves the catalog untouched and the snapshot open (so it can be aborted or retried with options).
func (s *Service) Commit(ctx context.Context, src *catalog.Source, snapshotID string, o CommitOptions, actor audit.Actor) (*CommitResult, error) {
	start := s.Clock.Now()
	res := &CommitResult{DryRun: o.DryRun}
	var refusal error
	err := s.Store.W().TxNamed(ctx, "ingest.snapshot_commit", func(tx *store.Tx) error {
		*res = CommitResult{DryRun: o.DryRun}
		refusal = nil
		sn, err := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapCols+` FROM snapshots WHERE id = ? AND source_id = ?`, snapshotID, src.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSnapshotNotFound
		}
		if err != nil {
			return err
		}
		if sn.State != "open" || !tx.Now.Before(sn.ExpiresAt) {
			return apperr.New(apperr.CodeConflict, "snapshot is %s", map[bool]string{true: sn.State, false: "expired"}[sn.State != "open"])
		}
		cur, err := catalog.SourceTx(ctx, tx, src.ID)
		if err != nil {
			return err
		}
		if sn.Generation <= cur.LastGeneration {
			return &apperr.Error{Code: "stale_generation", Message: "a newer generation was committed meanwhile"}
		}
		b, err := loadStaged(ctx, tx, snapshotID)
		if err != nil {
			return err
		}
		before, err := s.Catalog.LatestSeqTx(ctx, tx)
		if err != nil {
			return err
		}
		results, err := s.applyBatch(ctx, tx, cur, b, catalog.ApplyOpts{Actor: actor, Generation: sn.Generation}, 0)
		if err != nil {
			return err
		}
		for _, r := range results {
			res.Summary.add(r)
			if (r.Error != nil || r.Action == catalog.ActionUnresolved || r.Action == catalog.ActionStale) && len(res.Results) < 500 {
				res.Results = append(res.Results, r)
			}
		}
		if sn.Complete && (res.Summary.Errors == 0 || o.AllowPartialErrors) {
			if err := s.handleAbsent(ctx, tx, cur, sn.Generation, o, res, actor); err != nil {
				var mr *ErrMassRemoval
				if errors.As(err, &mr) {
					refusal = err
					return err
				}
				return err
			}
		}
		if o.DryRun {
			res.Changes, err = s.previewChanges(ctx, tx, before)
			if err != nil {
				return err
			}
			res.Snapshot = sn
			return errDryRun
		}
		stats, _ := json.Marshal(res.Summary)
		if _, err := tx.Exec(ctx, `UPDATE snapshots SET state = 'committed', stats_json = ?, finished_at = ? WHERE id = ?`, string(stats), tx.NowMicros(), snapshotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM snapshot_items WHERE snapshot_id = ?`, snapshotID); err != nil {
			return err
		}
		if err := catalog.RecordRun(ctx, tx, src.ID, true, "", sn.Generation); err != nil {
			return err
		}
		details := summaryDetails(res.Summary)
		details["generation"], details["complete"] = sn.Generation, sn.Complete
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: src.ProjectID, Action: "ingest.snapshot_committed",
			Target: audit.Target{Type: "source", ID: src.ID}, Details: details}); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Area: "ingest", Entity: "snapshot", Action: "committed", ProjectID: src.ProjectID,
			Payload: map[string]any{"source_id": src.ID, "snapshot_id": snapshotID, "generation": sn.Generation, "created": res.Summary.Created,
				"updated": res.Summary.Updated, "removed": res.Summary.Removed, "errors": res.Summary.Errors}}); err != nil {
			return err
		}
		res.Snapshot, err = scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapCols+` FROM snapshots WHERE id = ?`, snapshotID))
		return err
	})
	if errors.Is(err, errDryRun) {
		s.Metrics.Reconcile("dry_run", "ok", s.Clock.Now().Sub(start))
		if res.Results == nil {
			res.Results = []ItemResult{}
		}
		if res.Removed == nil {
			res.Removed = []Removal{}
		}
		return res, nil
	}
	if refusal != nil {
		s.Metrics.Reconcile("snapshot_commit", "refused", s.Clock.Now().Sub(start))
		s.recordRefusal(ctx, src, snapshotID, refusal, actor)
		return nil, &apperr.Error{Code: "mass_removal_refused", Message: refusal.Error()}
	}
	if err != nil {
		s.Metrics.Reconcile("snapshot_commit", "error", s.Clock.Now().Sub(start))
		return nil, err
	}
	s.observe(src, "snapshot_commit", res.Summary, s.Clock.Now().Sub(start))
	if res.Results == nil {
		res.Results = []ItemResult{}
	}
	if res.Removed == nil {
		res.Removed = []Removal{}
	}
	return res, nil
}

func (s *Service) recordRefusal(ctx context.Context, src *catalog.Source, snapshotID string, refusal error, actor audit.Actor) {
	_ = s.Store.W().TxNamed(context.WithoutCancel(ctx), "ingest.snapshot_refused", func(tx *store.Tx) error {
		if err := catalog.RecordRun(ctx, tx, src.ID, false, refusal.Error(), 0); err != nil {
			return err
		}
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: src.ProjectID, Action: "ingest.snapshot_committed", Outcome: audit.OutcomeFailure,
			ReasonCode: "mass_removal_guard", Target: audit.Target{Type: "source", ID: src.ID}, Details: map[string]any{"snapshot_id": snapshotID}})
		return err
	})
}

func loadStaged(ctx context.Context, tx *store.Tx, snapshotID string) (Batch, error) {
	rows, err := tx.Query(ctx, `SELECT item_json FROM snapshot_items WHERE snapshot_id = ? ORDER BY seq, idx`, snapshotID)
	if err != nil {
		return Batch{}, err
	}
	defer rows.Close()
	var b Batch
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			return Batch{}, err
		}
		var it stagedItem
		if err := json.Unmarshal([]byte(j), &it); err != nil {
			return Batch{}, err
		}
		if it.Entity != nil {
			b.Entities = append(b.Entities, *it.Entity)
		}
		if it.Relation != nil {
			b.Relations = append(b.Relations, *it.Relation)
		}
	}
	return b, rows.Err()
}

// handleAbsent processes facts of src not seen in this generation according to its on_absent policy, behind the
// mass-removal guard.
func (s *Service) handleAbsent(ctx context.Context, tx *store.Tx, src *catalog.Source, gen int64, o CommitOptions, res *CommitResult, actor audit.Actor) error {
	type fact struct{ id, entity, kind, name string }
	var facets, asserts []fact
	rows, err := tx.Query(ctx, `SELECT f.id, f.entity_id, e.kind, e.name FROM facets f JOIN entities e ON e.id = f.entity_id
		WHERE f.source_id = ? AND f.tombstoned_at IS NULL AND f.generation < ?`, src.ID, gen)
	if err != nil {
		return err
	}
	for rows.Next() {
		var f fact
		if err := rows.Scan(&f.id, &f.entity, &f.kind, &f.name); err != nil {
			rows.Close()
			return err
		}
		facets = append(facets, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT id, relation_id FROM relation_assertions WHERE source_id = ? AND tombstoned_at IS NULL AND generation < ?`, src.ID, gen)
	if err != nil {
		return err
	}
	for rows.Next() {
		var f fact
		if err := rows.Scan(&f.id, &f.entity); err != nil {
			rows.Close()
			return err
		}
		asserts = append(asserts, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	absent := len(facets) + len(asserts)
	if absent == 0 {
		return nil
	}
	policy := src.Policy.OnAbsent
	if policy == "" {
		policy = "tombstone"
	}
	for _, f := range facets {
		if len(res.Removed) < 500 {
			res.Removed = append(res.Removed, Removal{Type: "entity_facet", ID: f.id, EntityID: f.entity, Kind: f.kind, Name: f.name, Action: policy})
		}
	}
	for _, f := range asserts {
		if len(res.Removed) < 500 {
			res.Removed = append(res.Removed, Removal{Type: "relation_assertion", ID: f.id, EntityID: f.entity, Action: policy})
		}
	}
	if policy != "tombstone" {
		res.Summary.Orphaned = absent
		return nil
	}
	var live int
	if err := tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM facets WHERE source_id = ? AND tombstoned_at IS NULL) +
		(SELECT COUNT(*) FROM relation_assertions WHERE source_id = ? AND tombstoned_at IS NULL)`, src.ID, src.ID).Scan(&live); err != nil {
		return err
	}
	limit := src.Policy.MaxRemovalPercent
	if limit <= 0 {
		limit = s.Limits.MaxRemovalPercent
	}
	if live > 0 && !o.AllowMassRemoval && 100*float64(absent)/float64(live) > limit && absent > 3 {
		return &ErrMassRemoval{Removing: absent, Live: live, Limit: limit}
	}
	fids := make([]string, len(facets))
	for i, f := range facets {
		fids[i] = f.id
	}
	aids := make([]string, len(asserts))
	for i, f := range asserts {
		aids[i] = f.id
	}
	sort.Strings(fids)
	sort.Strings(aids)
	if err := s.Catalog.TombstoneFacetsTx(ctx, tx, src, fids, actor); err != nil {
		return err
	}
	if err := s.Catalog.TombstoneAssertionsTx(ctx, tx, src, aids, actor); err != nil {
		return err
	}
	res.Summary.Removed = absent
	return nil
}

// previewChanges lists change-log rows appended by the (to be rolled back) commit.
func (s *Service) previewChanges(ctx context.Context, tx *store.Tx, after int64) ([]Change, error) {
	rows, err := tx.Query(ctx, `SELECT object_type, object_id, action, summary_json FROM change_log WHERE seq > ? ORDER BY seq LIMIT 500`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		var sum string
		if err := rows.Scan(&c.Type, &c.ID, &c.Action, &sum); err != nil {
			return nil, err
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(sum), &m)
		switch c.Type {
		case "entity":
			c.Label = fmt.Sprintf("%v/%v", m["kind"], m["name"])
		case "relation":
			c.Label = fmt.Sprintf("%v %v → %v", m["type"], m["from"], m["to"])
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Abort discards an open snapshot; the previous generation stays intact.
func (s *Service) Abort(ctx context.Context, src *catalog.Source, snapshotID string, actor audit.Actor) error {
	return s.Store.W().TxNamed(ctx, "ingest.snapshot_abort", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE snapshots SET state = 'aborted', finished_at = ? WHERE id = ? AND source_id = ? AND state = 'open'`,
			tx.NowMicros(), snapshotID, src.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "no open snapshot with this ID")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM snapshot_items WHERE snapshot_id = ?`, snapshotID); err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: src.ProjectID, Action: "ingest.snapshot_aborted",
			Target: audit.Target{Type: "source", ID: src.ID}, Details: map[string]any{"snapshot_id": snapshotID}})
		return err
	})
}

func (s *Service) expireTx(ctx context.Context, tx *store.Tx, sourceID string) error {
	ids, err := func() ([]string, error) {
		rows, err := tx.Query(ctx, `SELECT id FROM snapshots WHERE source_id = ? AND state = 'open' AND expires_at <= ?`, sourceID, tx.NowMicros())
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
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE snapshots SET state = 'expired', finished_at = ?, error = 'snapshot timed out before commit' WHERE id = ?`, tx.NowMicros(), id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM snapshot_items WHERE snapshot_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ExpireSnapshots expires every timed-out snapshot (maintenance).
func (s *Service) ExpireSnapshots(ctx context.Context) error {
	return s.Store.W().TxNamed(ctx, "ingest.snapshot_expire", func(tx *store.Tx) error {
		srcs, err := func() ([]string, error) {
			rows, err := tx.Query(ctx, `SELECT DISTINCT source_id FROM snapshots WHERE state = 'open' AND expires_at <= ?`, tx.NowMicros())
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
		for _, sid := range srcs {
			if err := s.expireTx(ctx, tx, sid); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM snapshots WHERE state != 'open' AND created_at < ?`, tx.Now.Add(-30*24*time.Hour).UnixMicro())
		return err
	})
}

// RunSnapshot is the in-process path for built-in providers: begin, stage everything, commit (or preview).
func (s *Service) RunSnapshot(ctx context.Context, src *catalog.Source, b Batch, complete bool, o CommitOptions, actor audit.Actor) (*CommitResult, error) {
	sn, err := s.Begin(ctx, src, 0, complete, actor)
	if err != nil {
		return nil, err
	}
	commit := func() (*CommitResult, error) {
		for start, seq := 0, 1; start < b.Len(); seq++ {
			end := min(start+s.Limits.MaxItems, b.Len())
			part := slice(b, start, end)
			if _, err := s.PutBatch(ctx, src, sn.ID, seq, part); err != nil {
				return nil, err
			}
			start = end
		}
		return s.Commit(ctx, src, sn.ID, o, actor)
	}
	res, err := commit()
	if err != nil || o.DryRun {
		_ = s.Abort(context.WithoutCancel(ctx), src, sn.ID, actor)
	}
	return res, err
}

// slice returns items [start, end) of b counting entities first.
func slice(b Batch, start, end int) Batch {
	var out Batch
	ne := len(b.Entities)
	if start < ne {
		out.Entities = b.Entities[start:min(end, ne)]
	}
	if end > ne {
		out.Relations = b.Relations[max(start-ne, 0) : end-ne]
	}
	return out
}

// Observations writes time-bounded observation facets (observation-layer sources only, enrichment only).
func (s *Service) Observations(ctx context.Context, src *catalog.Source, items []catalog.EntityItem, actor audit.Actor) (*ItemsResult, error) {
	if src.Layer != catalog.LayerObservation {
		return nil, apperr.New(apperr.CodeInvalid, "observations are written by observation-layer sources (this source is %s)", src.Layer)
	}
	if n := len(items); n == 0 || n > s.Limits.MaxItems {
		return nil, apperr.New(apperr.CodeInvalid, "a request carries 1..%d observations", s.Limits.MaxItems)
	}
	v := &apperr.Validation{}
	for i, it := range items {
		if it.Spec != nil || it.Metadata != nil {
			v.Add(fmt.Sprintf("observations[%d]", i), "observations carry only observed, health, observed_at and expires_at; declare spec and metadata as items")
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	var out ItemsResult
	err := s.Store.W().TxNamed(ctx, "ingest.observations", func(tx *store.Tx) error {
		out = ItemsResult{Applied: true}
		res, err := s.applyBatch(ctx, tx, src, Batch{Entities: items}, catalog.ApplyOpts{Actor: actor, NoCreate: true}, 0)
		if err != nil {
			return err
		}
		out.Results = res
		for _, r := range res {
			out.Summary.add(r)
		}
		return catalog.RecordRun(ctx, tx, src.ID, true, "", 0)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
