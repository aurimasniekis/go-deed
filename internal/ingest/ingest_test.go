package ingest_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func setup(t *testing.T) (*testenv.Env, *ingest.Service, *catalog.Source) {
	env := testenv.New(t)
	p := env.Project(t, "home")
	src := env.Source(t, p.ID, "inventory", catalog.LayerImported)
	svc := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 100000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	return env, svc, src
}

func servers(n int, prefix string) ingest.Batch {
	var b ingest.Batch
	for i := range n {
		b.Entities = append(b.Entities, catalog.EntityItem{Kind: "server", Name: fmt.Sprintf("%s-%d", prefix, i), ExternalID: fmt.Sprintf("srv-%s-%d", prefix, i),
			Spec: map[string]any{"hostname": fmt.Sprintf("%s-%d.example.com", prefix, i)}})
	}
	return b
}

func live(t *testing.T, env *testenv.Env) int {
	t.Helper()
	c, err := env.Catalog.CountEntities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Entities
}

func refresh(t *testing.T, env *testenv.Env, src *catalog.Source) *catalog.Source {
	t.Helper()
	s, err := env.Catalog.Source(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCompleteSnapshotTombstonesAbsent(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	res, err := svc.RunSnapshot(ctx, src, servers(10, "a"), true, ingest.CommitOptions{}, audit.System)
	if err != nil || res.Summary.Created != 10 {
		t.Fatalf("%+v %v", res, err)
	}
	src = refresh(t, env, src)
	b := servers(10, "a")
	b.Entities = b.Entities[:9] // one disappeared upstream
	res, err = svc.RunSnapshot(ctx, src, b, true, ingest.CommitOptions{}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Removed != 1 || res.Summary.Unchanged != 9 || live(t, env) != 9 {
		t.Fatalf("summary %+v live %d", res.Summary, live(t, env))
	}
}

func TestPartialSnapshotNeverTombstones(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	if _, err := svc.RunSnapshot(ctx, src, servers(10, "a"), true, ingest.CommitOptions{}, audit.System); err != nil {
		t.Fatal(err)
	}
	src = refresh(t, env, src)
	res, err := svc.RunSnapshot(ctx, src, servers(2, "a"), false, ingest.CommitOptions{}, audit.System)
	if err != nil || res.Summary.Removed != 0 || live(t, env) != 10 {
		t.Fatalf("partial snapshot removed facts: %+v %v live=%d", res, err, live(t, env))
	}
}

func TestMassRemovalGuard(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	if _, err := svc.RunSnapshot(ctx, src, servers(20, "a"), true, ingest.CommitOptions{}, audit.System); err != nil {
		t.Fatal(err)
	}
	src = refresh(t, env, src)
	// An upstream glitch returns almost nothing: refuse.
	_, err := svc.RunSnapshot(ctx, src, servers(2, "a"), true, ingest.CommitOptions{}, audit.System)
	if apperr.CodeOf(err) != "mass_removal_refused" || live(t, env) != 20 {
		t.Fatalf("guard did not hold: %v live=%d", err, live(t, env))
	}
	src = refresh(t, env, src)
	res, err := svc.RunSnapshot(ctx, src, servers(2, "a"), true, ingest.CommitOptions{AllowMassRemoval: true}, audit.System)
	if err != nil || res.Summary.Removed != 18 || live(t, env) != 2 {
		t.Fatalf("override failed: %+v %v", res, err)
	}
}

func TestInterruptedSnapshotLeavesCatalogIntact(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	if _, err := svc.RunSnapshot(ctx, src, servers(5, "a"), true, ingest.CommitOptions{}, audit.System); err != nil {
		t.Fatal(err)
	}
	src = refresh(t, env, src)
	sn, err := svc.Begin(ctx, src, 0, true, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutBatch(ctx, src, sn.ID, 1, servers(1, "a")); err != nil {
		t.Fatal(err)
	}
	// The producer dies before commit; the snapshot times out.
	env.Clock.Advance(2 * time.Hour)
	if err := svc.ExpireSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, src, sn.ID, ingest.CommitOptions{}, audit.System); err == nil {
		t.Fatal("expired snapshot committed")
	}
	if live(t, env) != 5 {
		t.Fatalf("interrupted snapshot changed the catalog: %d", live(t, env))
	}
	// Aborts behave the same.
	sn, err = svc.Begin(ctx, src, 0, true, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Abort(ctx, src, sn.ID, audit.System); err != nil || live(t, env) != 5 {
		t.Fatalf("abort: %v live=%d", err, live(t, env))
	}
}

func TestBatchReplayAndGenerations(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	sn, err := svc.Begin(ctx, src, 7, true, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	b := servers(3, "a")
	r1, err := svc.PutBatch(ctx, src, sn.ID, 1, b)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.PutBatch(ctx, src, sn.ID, 1, b)
	if err != nil || !r2.Replayed || r1.Replayed {
		t.Fatalf("replay not idempotent: %+v %v", r2, err)
	}
	if _, err := svc.PutBatch(ctx, src, sn.ID, 1, servers(4, "a")); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("different content under same seq accepted: %v", err)
	}
	res, err := svc.Commit(ctx, src, sn.ID, ingest.CommitOptions{}, audit.System)
	if err != nil || res.Summary.Created != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	src = refresh(t, env, src)
	if _, err := svc.Begin(ctx, src, 7, true, audit.System); apperr.CodeOf(err) != "stale_generation" {
		t.Fatalf("stale generation accepted: %v", err)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	res, err := svc.RunSnapshot(ctx, src, servers(4, "a"), true, ingest.CommitOptions{DryRun: true}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Summary.Created != 4 || len(res.Changes) != 4 || live(t, env) != 0 {
		t.Fatalf("dry run %+v live=%d", res, live(t, env))
	}
	src = refresh(t, env, src)
	if src.LastGeneration != 0 {
		t.Fatalf("dry run advanced the generation to %d", src.LastGeneration)
	}
}

func TestIncrementalItemsAndAtomic(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	b := servers(2, "a")
	b.Entities = append(b.Entities, catalog.EntityItem{Kind: "nope", Name: "x"})
	res, err := svc.Items(ctx, src, b, true, audit.System)
	if err != nil || res.Applied || res.Summary.Errors != 1 || live(t, env) != 0 {
		t.Fatalf("atomic batch partially applied: %+v %v", res, err)
	}
	res, err = svc.Items(ctx, src, b, false, audit.System)
	if err != nil || !res.Applied || res.Summary.Created != 2 || res.Summary.Errors != 1 || live(t, env) != 2 {
		t.Fatalf("non-atomic: %+v %v", res, err)
	}
	if res.Results[2].Error == nil || res.Results[2].Error.Code != string(apperr.CodeInvalid) {
		t.Fatalf("structured item error missing: %+v", res.Results[2])
	}
}

func TestOutOfOrderAcrossSnapshotsAndIncrementals(t *testing.T) {
	env, svc, src := setup(t)
	ctx := context.Background()
	it := catalog.EntityItem{Kind: "server", Name: "x", ExternalID: "x", Revision: 5, Spec: map[string]any{"os": "v5"}}
	if _, err := svc.Items(ctx, src, ingest.Batch{Entities: []catalog.EntityItem{it}}, false, audit.System); err != nil {
		t.Fatal(err)
	}
	old := it
	old.Revision, old.Spec = 4, map[string]any{"os": "v4"}
	res, err := svc.Items(ctx, src, ingest.Batch{Entities: []catalog.EntityItem{old}}, false, audit.System)
	if err != nil || res.Summary.Stale != 1 {
		t.Fatalf("stale not detected: %+v %v", res, err)
	}
	e, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Limit: 5})
	if e.Items[0].Spec["os"] != "v5" {
		t.Fatalf("stale item applied: %v", e.Items[0].Spec)
	}
	var _ clock.Clock = env.Clock
}
