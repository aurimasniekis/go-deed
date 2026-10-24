package app

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/alerts"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/findings"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/live"
	"github.com/aurimasniekis/go-deed/internal/ops"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/tfimport"
	"github.com/aurimasniekis/go-deed/internal/views"
)

// initExtensions wires ingest, sources, graph, live updates, telemetry, findings, alerts and backups.
func (a *App) initExtensions(ctx context.Context) error {
	cfg := a.Cfg
	a.Ingest = &ingest.Service{Store: a.Store, Catalog: a.Catalog, Audit: a.Audit, Clock: a.Clock, Metrics: a.Metrics, Log: a.Log.With("component", "ingest"),
		Limits: ingest.Limits{MaxItems: cfg.Limits.IngestMaxItems, MaxSnapshotItems: 200_000, SnapshotTimeout: cfg.Sources.SnapshotTimeout.D(),
			MaxRemovalPercent: cfg.Limits.MaxRemovalPercent}}
	a.Registry = sources.NewRegistry()
	tf := registerProviders(a.Registry)
	tf.MaxBytes = cfg.Limits.StateMaxBytes
	a.TerraformKinds = tf.Catalog.KindTypes
	a.Runner = &sources.Runner{Store: a.Store, Catalog: a.Catalog, Ingest: a.Ingest, Audit: a.Audit, Registry: a.Registry, Guard: a.Guard, Clock: a.Clock,
		Metrics: a.Metrics, Log: a.Log.With("component", "sources"), MaxItems: 200_000, Timeout: 10 * time.Minute}
	srcAPI := &sources.API{Runner: a.Runner, Ingest: a.Ingest, Rate: httpx.NewLimiter(a.Clock, cfg.Limits.IngestPerSourceRPM, max(cfg.Limits.IngestPerSourceRPM/6, 10))}
	a.apiExt = append(a.apiExt, srcAPI.Register, (&tfimport.PushAPI{Provider: tf, Ingest: a.Ingest, Runner: a.Runner}).Register)
	httpapi.KindSupport = func(kind string) catalog.Support { return a.Registry.Support(kind, a.terraformKinds) }
	a.Graph = &graph.Engine{Store: a.Store, Catalog: a.Catalog, Clock: a.Clock, MaxDepth: cfg.Limits.GraphMaxDepth, MaxNodes: cfg.Limits.GraphMaxNodes,
		ClusterAbove: cfg.Limits.GraphClusterAbove, DegreeCollapse: 50}
	a.apiExt = append(a.apiExt, a.Graph.Register, (&views.Service{Store: a.Store}).Register)
	a.Live = live.NewHub(a.Catalog, a.Metrics, a.Clock, 200)
	if err := a.Live.Start(ctx); err != nil {
		return err
	}
	a.Catalog.Notify = a.Live.Notify
	a.rawExt = append(a.rawExt, a.Live.Register)
	a.Findings = &findings.Engine{Store: a.Store, Catalog: a.Catalog, Clock: a.Clock, Metrics: a.Metrics, Log: a.Log.With("component", "findings")}
	if cfg.Backup.Enabled {
		a.Findings.Config.BackupInterval = cfg.Backup.Interval.D()
	}
	a.Alerts = &alerts.Engine{Store: a.Store, Audit: a.Audit, Clock: a.Clock, Metrics: a.Metrics}
	if err := a.Alerts.EnsureBuiltins(ctx); err != nil {
		return err
	}
	a.Findings.OnChange = a.Alerts.Evaluate
	a.Authn.OnBreakGlass = func(ctx context.Context, tx *store.Tx, userID, ip string) error {
		return findings.RaiseTx(ctx, tx, findings.Finding{DedupKey: findings.TypeBreakGlass + "|" + userID + "|" + tx.Now.Format("20060102T1504"),
			Type: findings.TypeBreakGlass, Severity: findings.Critical, Class: findings.ClassConfirmed, Title: "Break-glass sign-in used",
			Message: "a single-use recovery link signed in without the second factor", Evidence: map[string]any{"user_id": userID, "source_ip": ip}}, 24*time.Hour)
	}
	a.OpsAPI = &ops.API{Store: a.Store, Findings: a.Findings, Alerts: a.Alerts, Clock: a.Clock}
	a.apiExt = append(a.apiExt, a.OpsAPI.Register)
	a.every("findings", 2*time.Minute, a.Findings.Evaluate)
	a.every("findings_prune", 24*time.Hour, func(ctx context.Context) error { return a.Findings.Prune(ctx, cfg.Retention.ClosedFindings.D()) })
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
func registerProviders(r *sources.Registry) *tfimport.Provider {
	tf := &tfimport.Provider{Catalog: tfimport.MustBuiltin()}
	r.Register(tf)
	return tf
}

// APIExtensionsForSpec returns every API extension so `deed openapi` documents the complete API without services.
func APIExtensionsForSpec() []func(huma.API, *httpapi.Deps) {
	reg := sources.NewRegistry()
	tf := registerProviders(reg)
	return []func(huma.API, *httpapi.Deps){
		(&sources.API{Runner: &sources.Runner{Registry: reg}}).Register,
		(&tfimport.PushAPI{Provider: tf}).Register,
		(&graph.Engine{}).Register,
		(&views.Service{}).Register,
		(&ops.API{}).Register,
	}
}
