// Package sources runs source providers (ADR-013): a registry of in-process providers described by JSON Schema
// configs, a runner that turns a provider's emitted items into a staged snapshot through the ingest service (so
// built-in and external sources share one reconciliation path), run history with diagnostics, and a scheduler.
package sources

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Support levels a provider declares per kind (Q-06).
const (
	SupportRuntime = "runtime" // runtime ingestion (push protocol, Pulse, agents)
	SupportNative  = "native"  // native in-process integration
)

// KindSupport declares what a provider emits for a kind.
type KindSupport struct {
	Kind  string `json:"kind"`
	Level string `json:"level"`
	Note  string `json:"note,omitempty"`
}

// Capabilities of a provider.
type Capabilities struct {
	Pull        bool `json:"pull" doc:"Deed fetches data on a schedule."`
	Push        bool `json:"push" doc:"An external program pushes data with a source token."`
	Snapshot    bool `json:"snapshot"`
	Incremental bool `json:"incremental"`
}

// Descriptor documents a provider for the generic Sources UI.
type Descriptor struct {
	Type            string          `json:"type"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	DefaultLayer    catalog.Layer   `json:"default_layer"`
	ConfigSchema    json.RawMessage `json:"config_schema"`
	SecretFields    []string        `json:"secret_fields"`
	Capabilities    Capabilities    `json:"capabilities"`
	Kinds           []KindSupport   `json:"kinds"`
	RelTypes        []string        `json:"relationship_types"`
	DefaultSchedule int             `json:"default_schedule_seconds"`
	Docs            string          `json:"docs,omitempty"`
	Verified        string          `json:"verified" doc:"How this integration was validated (contract fixtures, local service, real service)."`
	// DefaultPolicy is applied when a source is created without a policy (e.g. high churn of pull requests).
	DefaultPolicy *catalog.Policy `json:"default_policy,omitempty"`
}

// Diagnostic is a safe, non-sensitive message from a provider run or connectivity test.
type Diagnostic struct {
	Level   string `json:"level"` // info, warning, error
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

// Env is what a provider gets for one call.
type Env struct {
	Source  *catalog.Source
	Config  json.RawMessage
	Secrets map[string]string
	// HTTP returns an SSRF-safe client for the provider's allowlist.
	HTTP  func(p safehttp.Policy) (*http.Client, error)
	Log   *slog.Logger
	Clock clock.Clock
	// Lookup finds a single existing entity of the source's project by an effective value (inference joins).
	Lookup func(ctx context.Context, kinds []string, pointer, value string) (string, bool)
}

// Emitter receives a provider's facts.
type Emitter interface {
	Entity(it catalog.EntityItem) error
	Relation(it catalog.RelationItem) error
	Diag(d Diagnostic)
	// Partial marks the run incomplete: absent facts are never tombstoned.
	Partial(reason string)
}

// Provider is a source provider.
type Provider interface {
	Describe() Descriptor
	// Validate checks provider-specific rules beyond the JSON Schema.
	Validate(cfg json.RawMessage, secrets map[string]string) error
	// Test performs safe connectivity diagnostics; it writes nothing.
	Test(ctx context.Context, env Env) []Diagnostic
	// Sync emits the complete current state (or calls Partial).
	Sync(ctx context.Context, env Env, em Emitter) error
}

// Registry holds providers by type.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	schemas   map[string]*jsonschema.Schema
}

// NewRegistry returns a registry with the manual and push descriptors.
func NewRegistry() *Registry {
	r := &Registry{providers: map[string]Provider{}, schemas: map[string]*jsonschema.Schema{}}
	r.Register(manual{})
	r.Register(push{})
	return r
}

// Register adds a provider; its config schema must compile.
func (r *Registry) Register(p Provider) {
	d := p.Describe()
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(d.ConfigSchema))
	if err != nil {
		panic(fmt.Sprintf("provider %s: config schema: %v", d.Type, err))
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	loc := "urn:deed:provider:" + d.Type
	if err := c.AddResource(loc, raw); err != nil {
		panic(err)
	}
	sch, err := c.Compile(loc)
	if err != nil {
		panic(fmt.Sprintf("provider %s: config schema: %v", d.Type, err))
	}
	r.mu.Lock()
	r.providers[d.Type] = p
	r.schemas[d.Type] = sch
	r.mu.Unlock()
}

// Get returns a provider.
func (r *Registry) Get(typ string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[typ]
	return p, ok
}

// Descriptors lists every provider.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.providers))
	for _, p := range r.providers {
		d := p.Describe()
		if d.SecretFields == nil {
			d.SecretFields = []string{}
		}
		if d.Kinds == nil {
			d.Kinds = []KindSupport{}
		}
		if d.RelTypes == nil {
			d.RelTypes = []string{}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Support computes the support levels of a kind across providers (and Terraform mappings via tf).
func (r *Registry) Support(kind string, terraform func(string) []string) catalog.Support {
	s := catalog.Support{Modeling: true, Terraform: []string{}, Runtime: []string{}, Native: []string{}}
	if terraform != nil {
		s.Terraform = append(s.Terraform, terraform(kind)...)
	}
	for _, d := range r.Descriptors() {
		for _, k := range d.Kinds {
			if k.Kind != kind {
				continue
			}
			switch k.Level {
			case SupportNative:
				s.Native = append(s.Native, d.Type)
			case SupportRuntime:
				s.Runtime = append(s.Runtime, d.Type)
			}
		}
	}
	return s
}

// ValidateConfig validates a config against the provider's schema and rules.
func (r *Registry) ValidateConfig(typ string, cfg json.RawMessage, secrets map[string]string) error {
	p, ok := r.Get(typ)
	if !ok {
		return apperr.New(apperr.CodeInvalid, "unknown provider %q", typ)
	}
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(cfg))
	if err != nil {
		return apperr.New(apperr.CodeInvalid, "config is not valid JSON")
	}
	r.mu.RLock()
	sch := r.schemas[typ]
	r.mu.RUnlock()
	if err := sch.Validate(v); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			var fields []apperr.FieldError
			var walk func(u jsonschema.OutputUnit)
			walk = func(u jsonschema.OutputUnit) {
				if u.Error != nil && len(u.Errors) == 0 {
					fields = append(fields, apperr.FieldError{Field: "config" + u.InstanceLocation, Message: u.Error.String()})
				}
				for _, e := range u.Errors {
					walk(e)
				}
			}
			walk(*ve.BasicOutput())
			return &apperr.Error{Code: apperr.CodeInvalid, Message: "config does not match the provider schema", Fields: fields}
		}
		return apperr.New(apperr.CodeInvalid, "config does not match the provider schema")
	}
	allowed := map[string]bool{}
	for _, f := range p.Describe().SecretFields {
		allowed[f] = true
	}
	for k := range secrets {
		if !allowed[k] {
			return apperr.New(apperr.CodeInvalid, "provider %s has no secret field %q", typ, k)
		}
	}
	return p.Validate(cfg, secrets)
}

// manual is the provider of system declaration sources (API, Terraform, UI, overrides).
type manual struct{}

func (manual) Describe() Descriptor {
	return Descriptor{Type: "manual", Title: "Declarations", Description: "Declarations written through the API, the Terraform provider or the UI.",
		DefaultLayer: catalog.LayerDeclared, ConfigSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Capabilities: Capabilities{Push: true, Incremental: true}, Verified: "API integration tests"}
}
func (manual) Validate(json.RawMessage, map[string]string) error { return nil }
func (manual) Test(context.Context, Env) []Diagnostic {
	return []Diagnostic{{Level: "info", Code: "no_connectivity", Message: "declaration sources have nothing to connect to"}}
}
func (manual) Sync(context.Context, Env, Emitter) error {
	return apperr.New(apperr.CodeInvalid, "declaration sources are written through the API")
}

// push is a generic external source (agents, CI jobs, services) using the source protocol with a source token.
type push struct{}

func (push) Describe() Descriptor {
	return Descriptor{Type: "push", Title: "External push source", Description: "Any program that speaks the source protocol " +
		"(items, snapshots, heartbeats, observations) with a source token: deed-agent, CI jobs, services, the Go SDK pkg/deedclient.",
		DefaultLayer: catalog.LayerDiscovered, ConfigSchema: json.RawMessage(`{"type":"object","properties":{"notes":{"type":"string","maxLength":2000},` +
			`"pulse_source":{"type":"string","maxLength":128,"pattern":"^[a-z0-9_-]+$","description":"Accept deed.ingest.*.v1 events from Pulse ` +
			`published with this ce-source on the subject token after the ingest filter prefix (evt.deed.ingest.<pulse_source>.items)."}},"additionalProperties":false}`),
		Capabilities: Capabilities{Push: true, Snapshot: true, Incremental: true},
		Kinds: []KindSupport{{Kind: "container", Level: SupportRuntime, Note: "deed-agent docker collector"}, {Kind: "docker_host", Level: SupportRuntime},
			{Kind: "service", Level: SupportRuntime}},
		Docs: "docs/INGEST_API.md", Verified: "API integration tests; deed-agent against a local Docker engine"}
}
func (push) Validate(json.RawMessage, map[string]string) error { return nil }
func (push) Test(context.Context, Env) []Diagnostic {
	return []Diagnostic{{Level: "info", Code: "push_source", Message: "push sources are tested by sending a heartbeat with their token"}}
}
func (push) Sync(context.Context, Env, Emitter) error {
	return apperr.New(apperr.CodeInvalid, "push sources are written by external programs, not synced")
}

// Runner executes provider syncs.
type Runner struct {
	Store    *store.Store
	Catalog  *catalog.Service
	Ingest   *ingest.Service
	Audit    *audit.Writer
	Registry *Registry
	Guard    *safehttp.Guard
	Clock    clock.Clock
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	// MaxItems bounds one sync's emitted items.
	MaxItems int
	// Timeout bounds one sync.
	Timeout time.Duration

	running sync.Map // source ID → struct{}
}

// Run is a sync history entry.
type Run struct {
	ID          string          `json:"id"`
	SourceID    string          `json:"source_id"`
	Trigger     string          `json:"trigger"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	Result      string          `json:"result"`
	Stats       json.RawMessage `json:"stats"`
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Error       string          `json:"error,omitempty"`
}

type collector struct {
	b       ingest.Batch
	diags   map[string]*Diagnostic
	order   []string
	partial string
	max     int
}

func (c *collector) Entity(it catalog.EntityItem) error {
	if c.b.Len() >= c.max {
		return fmt.Errorf("provider emitted more than %d items", c.max)
	}
	c.b.Entities = append(c.b.Entities, it)
	return nil
}

func (c *collector) Relation(it catalog.RelationItem) error {
	if c.b.Len() >= c.max {
		return fmt.Errorf("provider emitted more than %d items", c.max)
	}
	c.b.Relations = append(c.b.Relations, it)
	return nil
}

func (c *collector) Diag(d Diagnostic) {
	key := d.Level + "|" + d.Code + "|" + d.Message
	if x, ok := c.diags[key]; ok {
		x.Count += max(d.Count, 1)
		return
	}
	if d.Count == 0 {
		d.Count = 1
	}
	if len(c.order) >= 200 {
		return
	}
	c.diags[key] = &d
	c.order = append(c.order, key)
}

func (c *collector) Partial(reason string) { c.partial = reason }

func (c *collector) diagnostics() []Diagnostic {
	out := make([]Diagnostic, 0, len(c.order))
	for _, k := range c.order {
		out = append(out, *c.diags[k])
	}
	return out
}

// Env builds a provider environment for src.
func (r *Runner) Env(src *catalog.Source) (Env, error) {
	secrets, err := r.Catalog.SourceSecrets(src)
	if err != nil {
		return Env{}, err
	}
	if r.Log == nil {
		r.Log = slog.New(slog.DiscardHandler)
	}
	return Env{Source: src, Config: src.Config, Secrets: secrets, Log: r.Log.With("source", src.Name, "provider", src.Provider), Clock: r.Clock,
		HTTP: func(p safehttp.Policy) (*http.Client, error) { return r.Guard.Client(p) },
		Lookup: func(ctx context.Context, kinds []string, pointer, value string) (string, bool) {
			return r.Catalog.FindByPointer(ctx, src.ProjectID, kinds, pointer, value)
		}}, nil
}

// ErrBusy means a sync of the source is already running.
var ErrBusy = apperr.New(apperr.CodeConflict, "a sync of this source is already running")

// Sync runs a provider for src. With dryRun the result is a preview and nothing is written.
func (r *Runner) Sync(ctx context.Context, src *catalog.Source, trigger string, dryRun bool, actor audit.Actor) (*Run, *ingest.CommitResult, error) {
	p, ok := r.Registry.Get(src.Provider)
	if !ok {
		return nil, nil, apperr.New(apperr.CodeInvalid, "unknown provider %q", src.Provider)
	}
	if !p.Describe().Capabilities.Pull {
		return nil, nil, apperr.New(apperr.CodeInvalid, "provider %s is push-only", src.Provider)
	}
	if _, loaded := r.running.LoadOrStore(src.ID, struct{}{}); loaded {
		return nil, nil, ErrBusy
	}
	defer r.running.Delete(src.ID)
	env, err := r.Env(src)
	if err != nil {
		return nil, nil, err
	}
	if dryRun {
		trigger = "preview"
	}
	run := &Run{ID: ids.New(ids.SourceRun), SourceID: src.ID, Trigger: trigger, StartedAt: r.Clock.Now(), Result: "running"}
	if !dryRun {
		if err := r.Store.W().TxNamed(ctx, "sources.run_start", func(tx *store.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO source_runs (id, source_id, trigger, started_at) VALUES (?, ?, ?, ?)`, run.ID, src.ID, trigger, tx.NowMicros())
			return err
		}); err != nil {
			return nil, nil, err
		}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	col := &collector{diags: map[string]*Diagnostic{}, max: max(r.MaxItems, 1)}
	syncErr := p.Sync(sctx, env, col)
	var res *ingest.CommitResult
	if syncErr == nil {
		if col.partial != "" {
			col.Diag(Diagnostic{Level: "warning", Code: "partial", Message: col.partial + " — absent facts are kept"})
		}
		res, syncErr = r.Ingest.RunSnapshot(ctx, src, col.b, col.partial == "", ingest.CommitOptions{DryRun: dryRun}, actor)
	}
	run.Diagnostics = col.diagnostics()
	now := r.Clock.Now()
	run.FinishedAt = &now
	switch {
	case syncErr != nil:
		run.Result, run.Error = "error", safeError(syncErr)
	case dryRun:
		run.Result = "dry_run"
	case res.Summary.Errors > 0 || col.partial != "":
		run.Result = "partial"
	default:
		run.Result = "ok"
	}
	if res != nil {
		run.Stats, _ = json.Marshal(res.Summary)
	} else {
		run.Stats = json.RawMessage("{}")
	}
	if dryRun {
		return run, res, syncErr
	}
	r.Metrics.SourceRun(src.Name, src.Provider, run.Result, now)
	dj, _ := json.Marshal(run.Diagnostics)
	err = r.Store.W().TxNamed(context.WithoutCancel(ctx), "sources.run_finish", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE source_runs SET finished_at = ?, result = ?, stats_json = ?, diagnostics_json = ?, error = ? WHERE id = ?`,
			tx.NowMicros(), run.Result, string(run.Stats), string(dj), run.Error, run.ID); err != nil {
			return err
		}
		if syncErr != nil {
			if err := catalog.RecordRun(ctx, tx, src.ID, false, run.Error, 0); err != nil {
				return err
			}
			_, err := r.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: src.ProjectID, Action: "source.sync", Outcome: audit.OutcomeFailure,
				ReasonCode: "sync_failed", Target: audit.Target{Type: "source", ID: src.ID}, Details: map[string]any{"run_id": run.ID, "trigger": trigger}})
			return err
		}
		return nil
	})
	if err != nil {
		return run, res, err
	}
	return run, res, syncErr
}

// safeError renders a provider error without leaking response bodies or credentials (providers wrap their errors
// with stable messages; outbound failures are classified).
func safeError(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	if c := safehttp.Classify(err); c != "connection" {
		return "outbound request failed: " + c
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// Test runs a provider's connectivity diagnostics.
func (r *Runner) Test(ctx context.Context, src *catalog.Source) ([]Diagnostic, error) {
	p, ok := r.Registry.Get(src.Provider)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalid, "unknown provider %q", src.Provider)
	}
	env, err := r.Env(src)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := p.Test(cctx, env)
	if d == nil {
		d = []Diagnostic{}
	}
	return d, nil
}

// Runs lists a source's run history.
func (r *Runner) Runs(ctx context.Context, sourceID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.Store.R().QueryContext(ctx, `SELECT id, source_id, trigger, started_at, finished_at, result, stats_json, diagnostics_json, error
		FROM source_runs WHERE source_id = ? ORDER BY started_at DESC LIMIT ?`, sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var x Run
		var s int64
		var f sql.NullInt64
		var stats, diags string
		if err := rows.Scan(&x.ID, &x.SourceID, &x.Trigger, &s, &f, &x.Result, &stats, &diags, &x.Error); err != nil {
			return nil, err
		}
		x.StartedAt = clock.FromMicros(s)
		if f.Valid {
			t := clock.FromMicros(f.Int64)
			x.FinishedAt = &t
		}
		x.Stats = json.RawMessage(stats)
		_ = json.Unmarshal([]byte(diags), &x.Diagnostics)
		if x.Diagnostics == nil {
			x.Diagnostics = []Diagnostic{}
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Schedule runs due pull sources until ctx ends.
func (r *Runner) Schedule(ctx context.Context, concurrency int) {
	if concurrency <= 0 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		due, err := r.due(ctx)
		if err != nil && ctx.Err() == nil {
			r.Log.Warn("scheduler", "error.message", err.Error())
		}
		for _, src := range due {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(src *catalog.Source) {
				defer func() { <-sem }()
				if _, _, err := r.Sync(ctx, src, "schedule", false, audit.System); err != nil && !errors.Is(err, ErrBusy) && ctx.Err() == nil {
					r.Log.Warn("scheduled sync failed", "event.action", "source.sync", "event.outcome", "failure", "source", src.Name,
						"provider", src.Provider, "error.message", safeError(err))
				}
			}(src)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// due claims sources whose next run is due by moving next_run_at forward (with ±10 % jitter) before running them.
func (r *Runner) due(ctx context.Context) ([]*catalog.Source, error) {
	var ids []string
	err := r.Store.W().TxNamed(ctx, "sources.claim_due", func(tx *store.Tx) error {
		ids = nil
		rows, err := tx.Query(ctx, `SELECT id, schedule_seconds FROM sources WHERE enabled = 1 AND schedule_seconds > 0 AND next_run_at <= ? LIMIT 32`, tx.NowMicros())
		if err != nil {
			return err
		}
		type due struct {
			id    string
			every int64
		}
		var ds []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.every); err != nil {
				rows.Close()
				return err
			}
			ds = append(ds, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range ds {
			if p, ok := r.Registry.Get(sourceProvider(ctx, tx, d.id)); !ok || !p.Describe().Capabilities.Pull {
				if _, err := tx.Exec(ctx, `UPDATE sources SET next_run_at = NULL WHERE id = ?`, d.id); err != nil {
					return err
				}
				continue
			}
			next := tx.Now.Add(jittered(time.Duration(d.every) * time.Second))
			if _, err := tx.Exec(ctx, `UPDATE sources SET next_run_at = ? WHERE id = ?`, next.UnixMicro(), d.id); err != nil {
				return err
			}
			ids = append(ids, d.id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*catalog.Source, 0, len(ids))
	for _, id := range ids {
		if s, err := r.Catalog.Source(ctx, id); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func sourceProvider(ctx context.Context, tx *store.Tx, id string) string {
	var p string
	_ = tx.QueryRow(ctx, `SELECT provider FROM sources WHERE id = ?`, id).Scan(&p)
	return p
}

func jittered(d time.Duration) time.Duration {
	var b [8]byte
	_, _ = rand.Read(b[:])
	f := float64(binary.BigEndian.Uint64(b[:])>>11)/(1<<53)*0.2 - 0.1
	return time.Duration(float64(d) * (1 + f))
}

// RecordPush records a run for data pushed to a source (Terraform state uploads).
func (r *Runner) RecordPush(ctx context.Context, src *catalog.Source, trigger string, res *ingest.CommitResult, diags []Diagnostic, runErr error) *Run {
	now := r.Clock.Now()
	run := &Run{ID: ids.New(ids.SourceRun), SourceID: src.ID, Trigger: trigger, StartedAt: now, FinishedAt: &now, Diagnostics: diags, Stats: json.RawMessage("{}")}
	switch {
	case runErr != nil:
		run.Result, run.Error = "error", safeError(runErr)
	case res != nil && res.DryRun:
		run.Result = "dry_run"
	case res != nil && res.Summary.Errors > 0:
		run.Result = "partial"
	default:
		run.Result = "ok"
	}
	if res != nil {
		run.Stats, _ = json.Marshal(res.Summary)
	}
	if run.Diagnostics == nil {
		run.Diagnostics = []Diagnostic{}
	}
	if run.Result == "dry_run" {
		return run
	}
	r.Metrics.SourceRun(src.Name, src.Provider, run.Result, now)
	dj, _ := json.Marshal(run.Diagnostics)
	_ = r.Store.W().TxNamed(context.WithoutCancel(ctx), "sources.push_run", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO source_runs (id, source_id, trigger, started_at, finished_at, result, stats_json, diagnostics_json, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, src.ID, trigger, tx.NowMicros(), tx.NowMicros(), run.Result, string(run.Stats), string(dj), run.Error); err != nil {
			return err
		}
		if runErr != nil {
			return catalog.RecordRun(ctx, tx, src.ID, false, run.Error, 0)
		}
		return nil
	})
	return run
}
