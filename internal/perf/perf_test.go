// Package perf holds the opt-in performance harness behind docs/PERFORMANCE.md:
//
//	DEED_PERF=1 DEED_TEST_TX_MAX=10m go test -run TestPerformance -v -timeout 30m ./internal/perf/
//
// It builds a 10k-entity / 50k-relation catalog in a real SQLite file and reports latency percentiles for the
// operations the UI and providers hit. DEED_PERF_ENTITIES / DEED_PERF_RELATIONS change the size.
package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

type stats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

func measure(n int, fn func(i int) error) (stats, error) {
	d := make([]float64, 0, n)
	for i := range n {
		t0 := time.Now()
		if err := fn(i); err != nil {
			return stats{}, err
		}
		d = append(d, float64(time.Since(t0).Microseconds())/1000)
	}
	slices.Sort(d)
	q := func(p float64) float64 { return d[min(len(d)-1, int(p*float64(len(d))))] }
	return stats{N: n, P50: q(0.50), P95: q(0.95), P99: q(0.99), Max: d[len(d)-1]}, nil
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func TestPerformance(t *testing.T) {
	if os.Getenv("DEED_PERF") == "" {
		t.Skip("set DEED_PERF=1 (and DEED_TEST_TX_MAX=10m) to run the performance harness")
	}
	nEnt, nRel := envInt("DEED_PERF_ENTITIES", 10000), envInt("DEED_PERF_RELATIONS", 50000)
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "perf")
	src := env.Source(t, p.ID, "api", "")
	rng := rand.New(rand.NewPCG(1, 2))
	report := map[string]any{"entities": nEnt, "relations_target": nRel, "go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"cpus": runtime.NumCPU()}

	// Dataset: hosts in racks, four workloads per host, workloads depending on other workloads.
	hosts := nEnt / 5
	ids := make([]string, 0, nEnt)
	kinds := make([]string, 0, nEnt)
	t0 := time.Now()
	const chunk = 250
	var items []catalog.EntityItem
	for h := range hosts {
		items = append(items, catalog.EntityItem{Kind: "server", Name: fmt.Sprintf("host-%05d", h), Metadata: map[string]any{"environment": []string{"prod", "staging", "dev"}[h%3],
			"labels": map[string]any{"rack": fmt.Sprintf("r%02d", h%40)}}, Spec: map[string]any{"hostname": fmt.Sprintf("host-%05d.example.com", h)}})
		for w := range 4 {
			items = append(items, catalog.EntityItem{Kind: "container", Name: fmt.Sprintf("host-%05d-app-%d", h, w),
				Spec: map[string]any{"image": fmt.Sprintf("example/app-%d", w%7), "state": "running"}})
		}
	}
	for i := 0; i < len(items); i += chunk {
		part := items[i:min(i+chunk, len(items))]
		if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
			for j := range part {
				r, err := env.Catalog.ApplyEntity(ctx, tx, src, &part[j], catalog.ApplyOpts{})
				if err != nil {
					return err
				}
				ids = append(ids, r.EntityID)
				kinds = append(kinds, part[j].Kind)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	report["build_entities_s"] = time.Since(t0).Seconds()
	t0 = time.Now()
	var rels []catalog.RelationItem
	for h := range hosts {
		host := ids[h*5]
		for w := 1; w <= 4; w++ {
			rels = append(rels, catalog.RelationItem{Type: "runs_on", From: catalog.Ref{ID: ids[h*5+w]}, To: catalog.Ref{ID: host}})
		}
	}
	seen := map[[2]int]bool{}
	for len(rels) < nRel {
		a, b := rng.IntN(len(ids)), rng.IntN(len(ids))
		if a == b || kinds[a] != "container" || kinds[b] != "container" || seen[[2]int{a, b}] {
			continue
		}
		seen[[2]int{a, b}] = true
		rels = append(rels, catalog.RelationItem{Type: "depends_on", From: catalog.Ref{ID: ids[a]}, To: catalog.Ref{ID: ids[b]}})
	}
	for i := 0; i < len(rels); i += chunk {
		part := rels[i:min(i+chunk, len(rels))]
		if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
			for j := range part {
				if _, err := env.Catalog.ApplyRelation(ctx, tx, src, &part[j], catalog.ApplyOpts{}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	report["build_relations_s"] = time.Since(t0).Seconds()
	report["relations"] = len(rels)

	eng := &graph.Engine{Store: env.Store, Catalog: env.Catalog, Clock: clock.System{}, MaxDepth: 10, MaxNodes: 2000, ClusterAbove: 500, DegreeCollapse: 50}
	scope := graph.Scope{}
	root := func(i int) string { return ids[(i*7919)%len(ids)] }
	res := map[string]stats{}
	run := func(name string, n int, fn func(i int) error) {
		s, err := measure(n, fn)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res[name] = s
	}
	var nodes []int
	run("graph_neighborhood_depth2_max500", 200, func(i int) error {
		r, err := eng.Neighborhood(ctx, scope, graph.Query{Roots: []string{root(i)}, Depth: 2, Direction: "both", MaxNodes: 500})
		if err == nil {
			nodes = append(nodes, len(r.Nodes))
		}
		return err
	})
	slices.Sort(nodes)
	report["neighborhood_depth2_nodes_median"] = nodes[len(nodes)/2]
	run("graph_neighborhood_depth3_max2000", 50, func(i int) error {
		_, err := eng.Neighborhood(ctx, scope, graph.Query{Roots: []string{root(i)}, Depth: 3, Direction: "both", MaxNodes: 2000})
		return err
	})
	run("graph_impact_downstream_depth4", 100, func(i int) error {
		_, err := eng.Impact(ctx, scope, ids[(i%hosts)*5], "downstream", 4)
		return err
	})
	run("graph_path_depth6", 50, func(i int) error {
		_, err := eng.Path(ctx, scope, root(i), root(i+1), 6, false, nil)
		return err
	})
	run("graph_overview_by_kind", 20, func(int) error {
		_, err := eng.Overview(ctx, scope, nil, "kind")
		return err
	})
	run("entities_search_fts_page50", 100, func(i int) error {
		_, err := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Query: fmt.Sprintf("app-%d", i%7), Limit: 50})
		return err
	})
	run("entities_filter_kind_env_page50", 100, func(i int) error {
		_, err := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Kinds: []string{"server"}, Environment: []string{"prod"}, Limit: 50})
		return err
	})
	run("entity_get", 200, func(i int) error {
		_, err := env.Catalog.GetEntity(ctx, root(i))
		return err
	})
	run("reconcile_update_one_entity_tx", 200, func(i int) error {
		h := (i * 31) % hosts
		return env.Store.W().TxNamed(ctx, "perf.update", func(tx *store.Tx) error {
			_, err := env.Catalog.ApplyEntity(ctx, tx, src, &catalog.EntityItem{Kind: "server", Name: fmt.Sprintf("host-%05d", h),
				Spec: map[string]any{"hostname": fmt.Sprintf("host-%05d.example.com", h), "os": fmt.Sprintf("Debian %d", 12+i%2)}}, catalog.ApplyOpts{})
			return err
		})
	})
	// Ingest: incremental batches and snapshot commits through the same service the API uses.
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 200000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 100}}
	push, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "agent", Provider: "push", Layer: catalog.LayerDiscovered}, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := func(n, gen int) ingest.Batch {
		var b ingest.Batch
		for i := range n {
			b.Entities = append(b.Entities, catalog.EntityItem{Kind: "container", Name: fmt.Sprintf("agent-%05d", i), ExternalID: fmt.Sprintf("c%d", i),
				Spec: map[string]any{"image": "example/agent", "generation": gen}})
		}
		return b
	}
	run("ingest_items_batch100", 20, func(i int) error {
		_, err := ing.Items(ctx, push, batch(100, i), false, audit.System)
		return err
	})
	for _, n := range []int{1000, 10000} {
		snap := batch(n, 99)
		t0 := time.Now()
		if _, err := ing.RunSnapshot(ctx, push, snap, true, ingest.CommitOptions{}, audit.System); err != nil {
			t.Fatal(err)
		}
		report[fmt.Sprintf("snapshot_%d_items_total_s", n)] = time.Since(t0).Seconds()
	}
	if fi, err := os.Stat(filepath.Join(env.Store.Dir(), store.DBFile)); err == nil {
		report["db_bytes"] = fi.Size()
	}
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	report["go_heap_alloc_mb_after_gc"] = ms.HeapAlloc >> 20 // SQLite page cache lives outside the Go heap
	report["latency"] = res
	b, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\n%s", b)
	if out := os.Getenv("DEED_PERF_OUT"); out != "" {
		_ = os.WriteFile(out, b, 0o600)
	}
}
