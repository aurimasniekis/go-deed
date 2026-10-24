package app

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/views"
)

// initExtensions wires ingest, sources, graph, live updates, telemetry, findings, alerts and backups.
func (a *App) initExtensions(_ context.Context) error {
	cfg := a.Cfg
	a.Ingest = &ingest.Service{Store: a.Store, Catalog: a.Catalog, Audit: a.Audit, Clock: a.Clock, Metrics: a.Metrics, Log: a.Log.With("component", "ingest"),
		Limits: ingest.Limits{MaxItems: cfg.Limits.IngestMaxItems, MaxSnapshotItems: 200_000, SnapshotTimeout: cfg.Sources.SnapshotTimeout.D(),
			MaxRemovalPercent: cfg.Limits.MaxRemovalPercent}}
	a.Registry = sources.NewRegistry()
	registerProviders(a.Registry)
	a.Runner = &sources.Runner{Store: a.Store, Catalog: a.Catalog, Ingest: a.Ingest, Audit: a.Audit, Registry: a.Registry, Guard: a.Guard, Clock: a.Clock,
		Metrics: a.Metrics, Log: a.Log.With("component", "sources"), MaxItems: 200_000, Timeout: 10 * time.Minute}
	srcAPI := &sources.API{Runner: a.Runner, Ingest: a.Ingest, Rate: httpx.NewLimiter(a.Clock, cfg.Limits.IngestPerSourceRPM, max(cfg.Limits.IngestPerSourceRPM/6, 10))}
	a.apiExt = append(a.apiExt, srcAPI.Register)
	httpapi.KindSupport = func(kind string) catalog.Support { return a.Registry.Support(kind, a.terraformKinds) }
	a.Graph = &graph.Engine{Store: a.Store, Catalog: a.Catalog, Clock: a.Clock, MaxDepth: cfg.Limits.GraphMaxDepth, MaxNodes: cfg.Limits.GraphMaxNodes,
		ClusterAbove: cfg.Limits.GraphClusterAbove, DegreeCollapse: 50}
	a.apiExt = append(a.apiExt, a.Graph.Register, (&views.Service{Store: a.Store}).Register)
	a.every("snapshot_expiry", time.Minute, a.Ingest.ExpireSnapshots)
	if cfg.Sources.SchedulerEnabled {
		a.addLoop("scheduler", func(ctx context.Context) { a.Runner.Schedule(ctx, cfg.Sources.Concurrency) })
	}
	return nil
}

// terraformKinds reports Terraform resource types mapped to a kind (set by the Terraform import module).
func (a *App) terraformKinds(kind string) []string {
	if a.TerraformKinds == nil {
		return nil
	}
	return a.TerraformKinds(kind)
}

// registerProviders adds the built-in source providers.
func registerProviders(r *sources.Registry) {}

// APIExtensionsForSpec returns every API extension so `deed openapi` documents the complete API without services.
func APIExtensionsForSpec() []func(huma.API, *httpapi.Deps) {
	reg := sources.NewRegistry()
	registerProviders(reg)
	return []func(huma.API, *httpapi.Deps){
		(&sources.API{Runner: &sources.Runner{Registry: reg}}).Register,
		(&graph.Engine{}).Register,
		(&views.Service{}).Register,
	}
}
