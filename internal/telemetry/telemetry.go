// Package telemetry queries operational data from external systems (ADR-008). Admins configure integrations
// (an adapter type, a base URL and sealed credentials) and named bindings (query templates with typed parameters bound
// to entity fields); users can only execute bindings against entities they can read. Every result is labelled
// measured, inferred, stale or unavailable with the time it was measured. Deed caches bounded summaries in memory and
// never persists raw telemetry.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
)

// Status of a telemetry result.
const (
	StatusMeasured    = "measured"    // fresh value read from the system of record
	StatusInferred    = "inferred"    // derived (e.g. a rate computed from two counter samples)
	StatusStale       = "stale"       // last good value; the latest query failed or the data is older than its freshness
	StatusUnavailable = "unavailable" // no value: adapter down, query failed, no data
)

// Shapes a binding can return.
const (
	ShapeScalar     = "scalar"
	ShapeTimeseries = "timeseries"
	ShapeTable      = "table"
	ShapeLink       = "link"
)

// Budgets applied to every adapter response.
const (
	MaxSeries = 50
	MaxPoints = 720
	MaxRows   = 200
	MaxLinks  = 50
)

// AdapterInfo documents an adapter for the integrations UI.
type AdapterInfo struct {
	Type         string          `json:"type"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	ConfigSchema json.RawMessage `json:"config_schema"`
	SecretFields []string        `json:"secret_fields"`
	Shapes       []string        `json:"shapes"`
	QueryHelp    string          `json:"query_help" doc:"How binding queries are written for this adapter."`
	// Local adapters read Deed's own data (no URL, no credentials).
	Local    bool   `json:"local"`
	Verified string `json:"verified"`
}

// Conn is what an adapter gets for one call.
type Conn struct {
	Name    string
	Config  json.RawMessage
	Secrets map[string]string
	// HTTP returns an SSRF-safe client for the integration's allowlist.
	HTTP func(p safehttp.Policy) (*http.Client, error)
}

// Query is one rendered binding query.
type Query struct {
	Text  string
	Shape string
	Start time.Time
	End   time.Time
	Step  time.Duration
}

// Point is one sample.
type TelemetryPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// Series is one labelled time series.
type TelemetrySeries struct {
	Labels map[string]string `json:"labels"`
	Points []TelemetryPoint  `json:"points"`
}

// Link is a navigable reference (Grafana dashboard, explore view).
type TelemetryLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Kind  string `json:"kind,omitempty"`
}

// Data is an adapter's answer.
type TelemetryData struct {
	Value  *float64            `json:"value,omitempty"`
	Series []TelemetrySeries   `json:"series,omitempty"`
	Rows   []map[string]string `json:"rows,omitempty"`
	Links  []TelemetryLink     `json:"links,omitempty"`
	// Inferred marks derived values (counter deltas, sampled rollups).
	Inferred bool `json:"-"`
	// Note qualifies the value (e.g. "sampled, 5-min window").
	Note      string `json:"note,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (d *TelemetryData) empty() bool {
	return d.Value == nil && len(d.Series) == 0 && len(d.Rows) == 0 && len(d.Links) == 0
}

// Adapter queries one kind of external system.
type Adapter interface {
	Info() AdapterInfo
	// Validate checks an integration config beyond its JSON Schema.
	Validate(cfg json.RawMessage, secrets map[string]string) error
	// ValidateQuery checks a binding's query template for this adapter and shape (static checks, no I/O).
	ValidateQuery(text, shape string) error
	// Escape renders a parameter value safely inside the adapter's query language.
	Escape(v string) string
	// Test performs a safe connectivity check.
	Test(ctx context.Context, c Conn) (string, error)
	// Query runs one rendered query.
	Query(ctx context.Context, c Conn, q Query) (*TelemetryData, error)
}

// Registry holds adapters by type.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	schemas  map[string]*jsonschema.Schema
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}, schemas: map[string]*jsonschema.Schema{}}
}

// Register adds an adapter; its config schema must compile.
func (r *Registry) Register(a Adapter) {
	info := a.Info()
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(info.ConfigSchema))
	if err != nil {
		panic(fmt.Sprintf("adapter %s: config schema: %v", info.Type, err))
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	loc := "urn:deed:adapter:" + info.Type
	if err := c.AddResource(loc, raw); err != nil {
		panic(err)
	}
	sch, err := c.Compile(loc)
	if err != nil {
		panic(fmt.Sprintf("adapter %s: config schema: %v", info.Type, err))
	}
	r.mu.Lock()
	r.adapters[info.Type] = a
	r.schemas[info.Type] = sch
	r.mu.Unlock()
}

// ValidateConfig checks an integration config against the adapter's JSON Schema.
func (r *Registry) ValidateConfig(typ string, cfg json.RawMessage) error {
	r.mu.RLock()
	sch, ok := r.schemas[typ]
	r.mu.RUnlock()
	if !ok {
		return apperr.New(apperr.CodeInvalid, "unknown adapter %q", typ)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(cfg))
	if err != nil {
		return apperr.New(apperr.CodeInvalid, "config is not valid JSON")
	}
	if err := sch.Validate(v); err != nil {
		var ve *jsonschema.ValidationError
		val := &apperr.Validation{}
		if errors.As(err, &ve) {
			var walk func(u jsonschema.OutputUnit)
			walk = func(u jsonschema.OutputUnit) {
				if u.Error != nil && len(u.Errors) == 0 {
					val.Add("config"+u.InstanceLocation, "%s", u.Error.String())
				}
				for _, e := range u.Errors {
					walk(e)
				}
			}
			walk(*ve.BasicOutput())
		}
		if val.Err() == nil {
			val.Add("config", "does not match the adapter schema")
		}
		return val.Err()
	}
	return nil
}

// Get returns an adapter.
func (r *Registry) Get(typ string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[typ]
	return a, ok
}

// Infos lists adapters.
func (r *Registry) Infos() []AdapterInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AdapterInfo, 0, len(r.adapters))
	for _, a := range r.adapters {
		i := a.Info()
		if i.SecretFields == nil {
			i.SecretFields = []string{}
		}
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// truncateSeries applies the series and point budgets.
func truncateSeries(d *TelemetryData) {
	if len(d.Series) > MaxSeries {
		d.Series, d.Truncated = d.Series[:MaxSeries], true
	}
	for i := range d.Series {
		if n := len(d.Series[i].Points); n > MaxPoints {
			d.Series[i].Points, d.Truncated = d.Series[i].Points[n-MaxPoints:], true
		}
	}
	if len(d.Rows) > MaxRows {
		d.Rows, d.Truncated = d.Rows[:MaxRows], true
	}
	if len(d.Links) > MaxLinks {
		d.Links, d.Truncated = d.Links[:MaxLinks], true
	}
}
