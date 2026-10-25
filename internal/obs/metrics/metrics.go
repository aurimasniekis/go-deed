// Adapted from Porch internal/obs/metrics/metrics.go (Apache-2.0).

// Package metrics defines Deed's Prometheus metrics. Every method is safe on a nil *Metrics (metrics disabled).
// Label values are bounded enums, route patterns, provider types, adapter kinds or operator-chosen source names
// (bounded by configuration); entity IDs, raw URLs, raw subjects and user identifiers are never labels.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TxResult classifies a write transaction outcome.
type TxResult string

// Transaction results.
const (
	TxOK         TxResult = "ok"
	TxCanceled   TxResult = "canceled"
	TxBusy       TxResult = "busy"
	TxFull       TxResult = "full"
	TxIO         TxResult = "io"
	TxCorrupt    TxResult = "corrupt"
	TxConstraint TxResult = "constraint"
	TxConflict   TxResult = "conflict"
	TxNotFound   TxResult = "not_found"
	TxError      TxResult = "error"
)

// Metrics holds every Deed collector in its own registry.
type Metrics struct {
	reg *prometheus.Registry

	storeTx       *prometheus.HistogramVec
	storeBusy     prometheus.Counter
	storeWait     prometheus.Histogram
	storeQueue    prometheus.Gauge
	storeDBBytes  prometheus.Gauge
	storeWALBytes prometheus.Gauge

	httpReqs *prometheus.CounterVec
	httpDur  *prometheus.HistogramVec

	logins      *prometheus.CounterVec
	auditEvents *prometheus.CounterVec
	authzDenied *prometheus.CounterVec

	reconcile       *prometheus.CounterVec
	reconcileDur    *prometheus.HistogramVec
	ingestItems     *prometheus.CounterVec
	ingestRejected  *prometheus.CounterVec
	sourceLastOK    *prometheus.GaugeVec
	sourceFreshness *prometheus.GaugeVec
	sourceRuns      *prometheus.CounterVec

	jobRuns *prometheus.CounterVec

	outboxQueue  *prometheus.GaugeVec
	outboxPub    *prometheus.CounterVec
	outboxOldest prometheus.Gauge
	pulseConsume *prometheus.CounterVec
	pulseTail    *prometheus.CounterVec
	pulseConn    *prometheus.GaugeVec

	telemetryQueries *prometheus.CounterVec
	telemetryDur     *prometheus.HistogramVec
	telemetryUp      *prometheus.GaugeVec
	cache            *prometheus.CounterVec

	liveClients prometheus.Gauge
	liveDropped *prometheus.CounterVec
	liveEvents  prometheus.Counter

	findingsOpen *prometheus.GaugeVec
	alertsFiring *prometheus.GaugeVec
	alertTrans   *prometheus.CounterVec

	backupLast     prometheus.Gauge
	backupLastRun  *prometheus.GaugeVec
	backupRuns     *prometheus.CounterVec
	backupDuration prometheus.Histogram
	backupBytes    prometheus.Gauge

	catalogEntities  *prometheus.GaugeVec
	catalogRelations prometheus.Gauge

	ready     prometheus.Gauge
	buildInfo *prometheus.GaugeVec
}

const ns = "deed"

// New registers every collector in a fresh registry (plus Go runtime and process collectors).
func New() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	c := func(sub, name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Subsystem: sub, Name: name, Help: help}, labels)
	}
	g := func(sub, name, help string, labels ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Subsystem: sub, Name: name, Help: help}, labels)
	}
	h := func(sub, name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Subsystem: sub, Name: name, Help: help, Buckets: buckets}, labels)
	}
	fast := []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}
	slow := []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}

	m.storeTx = h("store", "tx_duration_seconds", "Write transaction duration by name and result.", fast, "tx", "result")
	m.storeBusy = prometheus.NewCounter(prometheus.CounterOpts{Namespace: ns, Subsystem: "store", Name: "tx_busy_retries_total",
		Help: "Write transaction attempts retried after SQLITE_BUSY."})
	m.storeWait = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Subsystem: "store", Name: "writer_wait_seconds",
		Help: "Time waiting for the single writer connection and its write lock.", Buckets: prometheus.DefBuckets})
	m.storeQueue = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "writer_queue",
		Help: "Write transactions waiting for the writer."})
	m.storeDBBytes = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "db_bytes", Help: "Database file size."})
	m.storeWALBytes = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "store", Name: "wal_bytes", Help: "WAL file size."})

	m.httpReqs = c("http", "requests_total", "HTTP requests by route group, method and status code.", "route", "method", "code")
	m.httpDur = h("http", "request_duration_seconds", "HTTP request duration by route group.", prometheus.DefBuckets, "route")

	m.logins = c("auth", "logins_total", "Login attempts by method and outcome.", "method", "outcome")
	m.auditEvents = c("audit", "events_total", "Audit events appended by outcome.", "outcome")
	m.authzDenied = c("authz", "denied_total", "Authorization denials by reason.", "reason")

	m.reconcile = c("reconcile", "operations_total", "Reconciliation operations by kind (item, snapshot_commit, dry_run) and outcome.", "op", "outcome")
	m.reconcileDur = h("reconcile", "duration_seconds", "Reconciliation duration by operation.", slow, "op")
	m.ingestItems = c("ingest", "items_total", "Ingested items by provider type and action.", "provider", "action")
	m.ingestRejected = c("ingest", "rejected_total", "Rejected ingest items by provider type and reason.", "provider", "reason")
	m.sourceLastOK = g("source", "last_success_timestamp_seconds", "Unix time of the last successful sync by source name.", "source", "provider")
	m.sourceFreshness = g("source", "freshness_state", "1 for the current freshness state of each source (fresh, aging, stale, unknown).", "source", "state")
	m.sourceRuns = c("source", "runs_total", "Source sync runs by provider type and result.", "provider", "result")

	m.jobRuns = c("jobs", "runs_total", "Background job runs by job and result.", "job", "result")

	m.outboxQueue = g("outbox", "events", "Outbox events by state (pending, dead).", "state")
	m.outboxPub = c("outbox", "publish_total", "Outbox publish attempts by result.", "result")
	m.outboxOldest = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "outbox", Name: "oldest_pending_age_seconds",
		Help: "Age of the oldest unpublished outbox event."})
	m.pulseConsume = c("pulse", "consumed_total", "Pulse ingest messages by result (ok, terminal, retry).", "result")
	m.pulseTail = c("pulse", "tail_messages_total", "Messages observed by the read-only evt.> tail, by outcome (counted, overflow: beyond the per-window key budget).", "outcome")
	m.pulseConn = g("pulse", "connected", "1 when the named Pulse connection is up.", "conn")

	m.telemetryQueries = c("telemetry", "queries_total", "Telemetry adapter queries by adapter kind and result.", "adapter", "result")
	m.telemetryDur = h("telemetry", "query_duration_seconds", "Telemetry query duration by adapter kind.", slow, "adapter")
	m.telemetryUp = g("telemetry", "integration_up", "1 when the last connectivity test of an integration succeeded.", "adapter", "integration")
	m.cache = c("cache", "requests_total", "Cache lookups by cache and result (hit, miss, shared).", "cache", "result")

	m.liveClients = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "live", Name: "clients", Help: "Connected SSE clients."})
	m.liveDropped = c("live", "dropped_total", "SSE clients forced to resync, by reason (overflow, gap).", "reason")
	m.liveEvents = prometheus.NewCounter(prometheus.CounterOpts{Namespace: ns, Subsystem: "live", Name: "events_sent_total", Help: "SSE events sent."})

	m.findingsOpen = g("findings", "open", "Open findings by type and severity.", "type", "severity")
	m.alertsFiring = g("alerts", "firing", "Firing alerts by severity.", "severity")
	m.alertTrans = c("alerts", "transitions_total", "Alert state transitions.", "to")

	m.backupLast = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "backup", Name: "last_success_timestamp_seconds",
		Help: "Unix time of the last successful backup."})
	m.backupLastRun = g("backup", "last_run_timestamp_seconds", "Unix time of the last backup run by result.", "result")
	m.backupRuns = c("backup", "runs_total", "Backup runs by result.", "result")
	m.backupDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Subsystem: "backup", Name: "duration_seconds",
		Help: "Backup duration.", Buckets: slow})
	m.backupBytes = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "backup", Name: "size_bytes",
		Help: "Ciphertext size of the last successful backup."})

	m.catalogEntities = g("catalog", "entities", "Live entities by kind group (builtin, custom).", "group")
	m.catalogRelations = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Subsystem: "catalog", Name: "relations", Help: "Live relations."})

	m.ready = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "ready", Help: "1 when the instance reports ready."})
	m.buildInfo = g("", "build_info", "Build information.", "version", "go_version")

	m.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.storeTx, m.storeBusy, m.storeWait, m.storeQueue, m.storeDBBytes, m.storeWALBytes, m.httpReqs, m.httpDur,
		m.logins, m.auditEvents, m.authzDenied, m.reconcile, m.reconcileDur, m.ingestItems, m.ingestRejected,
		m.sourceLastOK, m.sourceFreshness, m.sourceRuns, m.jobRuns, m.outboxQueue, m.outboxPub, m.outboxOldest,
		m.pulseConsume, m.pulseTail, m.pulseConn, m.telemetryQueries, m.telemetryDur, m.telemetryUp, m.cache,
		m.liveClients, m.liveDropped, m.liveEvents, m.findingsOpen, m.alertsFiring, m.alertTrans,
		m.backupLast, m.backupLastRun, m.backupRuns, m.backupDuration, m.backupBytes,
		m.catalogEntities, m.catalogRelations, m.ready, m.buildInfo)
	return m
}

// Registry returns the underlying registry.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// StoreTx records a finished write transaction.
func (m *Metrics) StoreTx(name string, d time.Duration, r TxResult) {
	if m == nil {
		return
	}
	if name == "" {
		name = "unnamed"
	}
	m.storeTx.WithLabelValues(name, string(r)).Observe(d.Seconds())
}

// StoreTxBusyRetry counts a busy retry.
func (m *Metrics) StoreTxBusyRetry() {
	if m != nil {
		m.storeBusy.Inc()
	}
}

// StoreWriterWait records writer wait time.
func (m *Metrics) StoreWriterWait(d time.Duration) {
	if m != nil {
		m.storeWait.Observe(d.Seconds())
	}
}

// StoreWriterQueue adjusts the writer queue gauge.
func (m *Metrics) StoreWriterQueue(delta float64) {
	if m != nil {
		m.storeQueue.Add(delta)
	}
}

// SetStoreSizes exports database and WAL sizes.
func (m *Metrics) SetStoreSizes(dbBytes, walBytes int64) {
	if m != nil {
		m.storeDBBytes.Set(float64(dbBytes))
		m.storeWALBytes.Set(float64(walBytes))
	}
}

// HTTPRequest records a served request.
func (m *Metrics) HTTPRequest(route, method string, status int, d time.Duration) {
	if m == nil {
		return
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		method = "other"
	}
	m.httpReqs.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDur.WithLabelValues(route).Observe(d.Seconds())
}

// Login counts a login attempt.
func (m *Metrics) Login(method, outcome string) {
	if m != nil {
		m.logins.WithLabelValues(method, outcome).Inc()
	}
}

// AuditEvent counts an appended audit event.
func (m *Metrics) AuditEvent(outcome string) {
	if m != nil {
		m.auditEvents.WithLabelValues(outcome).Inc()
	}
}

// AuthzDenied counts a denial.
func (m *Metrics) AuthzDenied(reason string) {
	if m != nil {
		m.authzDenied.WithLabelValues(reason).Inc()
	}
}

// Reconcile records a reconciliation operation.
func (m *Metrics) Reconcile(op, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.reconcile.WithLabelValues(op, outcome).Inc()
	m.reconcileDur.WithLabelValues(op).Observe(d.Seconds())
}

// IngestItems counts accepted items.
func (m *Metrics) IngestItems(provider, action string, n int) {
	if m != nil && n > 0 {
		m.ingestItems.WithLabelValues(provider, action).Add(float64(n))
	}
}

// IngestRejected counts rejected items.
func (m *Metrics) IngestRejected(provider, reason string, n int) {
	if m != nil && n > 0 {
		m.ingestRejected.WithLabelValues(provider, reason).Add(float64(n))
	}
}

// SourceRun records a sync run.
func (m *Metrics) SourceRun(source, provider, result string, at time.Time) {
	if m == nil {
		return
	}
	m.sourceRuns.WithLabelValues(provider, result).Inc()
	if result == "ok" {
		m.sourceLastOK.WithLabelValues(source, provider).Set(float64(at.Unix()))
	}
}

// SourceFreshness sets the freshness state of a source.
func (m *Metrics) SourceFreshness(source, state string) {
	if m == nil {
		return
	}
	for _, s := range []string{"fresh", "aging", "stale", "unknown"} {
		v := 0.0
		if s == state {
			v = 1
		}
		m.sourceFreshness.WithLabelValues(source, s).Set(v)
	}
}

// ForgetSource removes per-source series after a source is deleted.
func (m *Metrics) ForgetSource(source string) {
	if m == nil {
		return
	}
	m.sourceLastOK.DeletePartialMatch(prometheus.Labels{"source": source})
	m.sourceFreshness.DeletePartialMatch(prometheus.Labels{"source": source})
}

// JobRun counts a background job run.
func (m *Metrics) JobRun(job, result string) {
	if m != nil {
		m.jobRuns.WithLabelValues(job, result).Inc()
	}
}

// OutboxPublish counts a publish attempt.
func (m *Metrics) OutboxPublish(result string) {
	if m != nil {
		m.outboxPub.WithLabelValues(result).Inc()
	}
}

// SetOutbox exports the outbox backlog.
func (m *Metrics) SetOutbox(pending, dead int64, oldest time.Duration) {
	if m == nil {
		return
	}
	m.outboxQueue.WithLabelValues("pending").Set(float64(pending))
	m.outboxQueue.WithLabelValues("dead").Set(float64(dead))
	m.outboxOldest.Set(oldest.Seconds())
}

// PulseConsumed counts an ingest message.
func (m *Metrics) PulseConsumed(result string) {
	if m != nil {
		m.pulseConsume.WithLabelValues(result).Inc()
	}
}

// PulseTail counts tail messages.
func (m *Metrics) PulseTail(outcome string, n int) {
	if m != nil && n > 0 {
		m.pulseTail.WithLabelValues(outcome).Add(float64(n))
	}
}

// PulseConnected sets a connection state.
func (m *Metrics) PulseConnected(conn string, up bool) {
	if m != nil {
		m.pulseConn.WithLabelValues(conn).Set(b2f(up))
	}
}

// TelemetryQuery records an adapter query.
func (m *Metrics) TelemetryQuery(adapter, result string, d time.Duration) {
	if m == nil {
		return
	}
	m.telemetryQueries.WithLabelValues(adapter, result).Inc()
	m.telemetryDur.WithLabelValues(adapter).Observe(d.Seconds())
}

// TelemetryUp sets an integration's health.
func (m *Metrics) TelemetryUp(adapter, integration string, up bool) {
	if m != nil {
		m.telemetryUp.WithLabelValues(adapter, integration).Set(b2f(up))
	}
}

// Cache counts a cache lookup.
func (m *Metrics) Cache(cache, result string) {
	if m != nil {
		m.cache.WithLabelValues(cache, result).Inc()
	}
}

// LiveClients adjusts the SSE client gauge.
func (m *Metrics) LiveClients(delta float64) {
	if m != nil {
		m.liveClients.Add(delta)
	}
}

// LiveDropped counts a forced resync.
func (m *Metrics) LiveDropped(reason string) {
	if m != nil {
		m.liveDropped.WithLabelValues(reason).Inc()
	}
}

// LiveEventSent counts a sent SSE event.
func (m *Metrics) LiveEventSent() {
	if m != nil {
		m.liveEvents.Inc()
	}
}

// SetFindings replaces the open findings gauge.
func (m *Metrics) SetFindings(counts map[[2]string]int) {
	if m == nil {
		return
	}
	m.findingsOpen.Reset()
	for k, v := range counts {
		m.findingsOpen.WithLabelValues(k[0], k[1]).Set(float64(v))
	}
}

// SetAlertsFiring replaces the firing alerts gauge.
func (m *Metrics) SetAlertsFiring(bySeverity map[string]int) {
	if m == nil {
		return
	}
	m.alertsFiring.Reset()
	for k, v := range bySeverity {
		m.alertsFiring.WithLabelValues(k).Set(float64(v))
	}
}

// AlertTransition counts an alert state change.
func (m *Metrics) AlertTransition(to string) {
	if m != nil {
		m.alertTrans.WithLabelValues(to).Inc()
	}
}

// BackupRun records a backup run.
func (m *Metrics) BackupRun(result string, at time.Time, d time.Duration, size int64) {
	if m == nil {
		return
	}
	m.backupRuns.WithLabelValues(result).Inc()
	m.backupLastRun.WithLabelValues(result).Set(float64(at.Unix()))
	if result == "ok" {
		m.backupLast.Set(float64(at.Unix()))
		m.backupDuration.Observe(d.Seconds())
		m.backupBytes.Set(float64(size))
	}
}

// SetCatalog exports catalog sizes.
func (m *Metrics) SetCatalog(builtin, custom, relations int64) {
	if m == nil {
		return
	}
	m.catalogEntities.WithLabelValues("builtin").Set(float64(builtin))
	m.catalogEntities.WithLabelValues("custom").Set(float64(custom))
	m.catalogRelations.Set(float64(relations))
}

// SetReady exports readiness.
func (m *Metrics) SetReady(ok bool) {
	if m != nil {
		m.ready.Set(b2f(ok))
	}
}

// SetBuildInfo exports build information.
func (m *Metrics) SetBuildInfo(version, goVersion string) {
	if m != nil {
		m.buildInfo.WithLabelValues(version, goVersion).Set(1)
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
