package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// OpenSearch counts documents (scalar) or buckets them over time (timeseries) with a query-DSL filter. It never
// returns documents.
//
// Query language: `<index-pattern> <query DSL object>`, e.g.
// `logs-* {"bool": {"filter": [{"term": {"host.name": "{{host}}"}}, {"term": {"log.level": "error"}}]}}`.
type OpenSearch struct{}

const osSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {` + httpSchemaProps + `,
    "time_field": {"type": "string", "default": "@timestamp", "maxLength": 128}
  }
}`

var indexRE = regexp.MustCompile(`^[a-z0-9*][a-z0-9*._,-]{0,254}$`)

// Info implements Adapter.
func (OpenSearch) Info() AdapterInfo {
	return AdapterInfo{Type: "opensearch", Title: "OpenSearch", Description: "Document counts and date histograms with a query-DSL filter " +
		"(no documents are returned). Authenticate with username/password, an API key or a bearer token.", ConfigSchema: json.RawMessage(osSchema),
		SecretFields: []string{"username", "password", "api_key", "token"}, Shapes: []string{ShapeScalar, ShapeTimeseries},
		QueryHelp: "`<index-pattern> <query DSL object>`; the window filter on time_field is added automatically. Scripts are not allowed.",
		Verified:  "contract tests against OpenSearch _count/_search fixtures; real OpenSearch validation pending"}
}

// Validate implements Adapter.
func (OpenSearch) Validate(cfg json.RawMessage, _ map[string]string) error { return validateHTTP(cfg) }

func parseOS(text string) (string, map[string]any, error) {
	idx, rest, ok := strings.Cut(strings.TrimSpace(text), " ")
	if !ok || !indexRE.MatchString(idx) || strings.Contains(idx, "..") {
		return "", nil, fmt.Errorf("expected `<index-pattern> <query DSL>` with a lowercase index pattern")
	}
	var q map[string]any
	dec := json.NewDecoder(strings.NewReader(rest))
	dec.UseNumber()
	if err := dec.Decode(&q); err != nil {
		return "", nil, fmt.Errorf("query DSL must be a JSON object")
	}
	if dec.More() {
		return "", nil, fmt.Errorf("unexpected text after the query DSL")
	}
	if hasScript(q) {
		return "", nil, fmt.Errorf("scripts are not allowed in binding queries")
	}
	return idx, q, nil
}

func hasScript(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if strings.Contains(strings.ToLower(k), "script") || hasScript(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasScript(e) {
				return true
			}
		}
	}
	return false
}

// ValidateQuery implements Adapter.
func (OpenSearch) ValidateQuery(text, _ string) error {
	_, _, err := parseOS(text)
	return err
}

// Escape implements Adapter: values land inside JSON strings.
func (OpenSearch) Escape(v string) string {
	b, _ := json.Marshal(v)
	return string(b[1 : len(b)-1])
}

// Test implements Adapter.
func (OpenSearch) Test(ctx context.Context, c Conn) (string, error) {
	cl, _, err := newHTTPClient(c, 1<<20)
	if err != nil {
		return "", err
	}
	var info struct {
		Version struct {
			Distribution string `json:"distribution"`
			Number       string `json:"number"`
		} `json:"version"`
	}
	if err := cl.do(ctx, http.MethodGet, "/", nil, nil, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s reachable", map[bool]string{true: info.Version.Distribution, false: "OpenSearch"}[info.Version.Distribution != ""], info.Version.Number), nil
}

// Query implements Adapter.
func (OpenSearch) Query(ctx context.Context, c Conn, q Query) (*TelemetryData, error) {
	idx, dsl, err := parseOS(q.Text)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "%s", err.Error())
	}
	cl, _, err := newHTTPClient(c, 4<<20)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		TimeField string `json:"time_field"`
	}
	_ = json.Unmarshal(c.Config, &cfg)
	if cfg.TimeField == "" {
		cfg.TimeField = "@timestamp"
	}
	filtered := map[string]any{"bool": map[string]any{"filter": []any{dsl, map[string]any{"range": map[string]any{cfg.TimeField: map[string]any{
		"gte": q.Start.UTC().Format(time.RFC3339), "lte": q.End.UTC().Format(time.RFC3339)}}}}}}
	path := "/" + url.PathEscape(idx)
	d := &TelemetryData{}
	if q.Shape == ShapeScalar {
		var r struct {
			Count float64 `json:"count"`
		}
		if err := cl.do(ctx, http.MethodPost, path+"/_count", url.Values{"ignore_unavailable": {"true"}}, map[string]any{"query": filtered}, &r); err != nil {
			return nil, err
		}
		d.Value = &r.Count
		return d, nil
	}
	body := map[string]any{"size": 0, "track_total_hits": false, "query": filtered, "aggs": map[string]any{"t": map[string]any{"date_histogram": map[string]any{
		"field": cfg.TimeField, "fixed_interval": fmt.Sprintf("%ds", int(q.Step.Seconds())), "min_doc_count": 0,
		"extended_bounds": map[string]any{"min": q.Start.UnixMilli(), "max": q.End.UnixMilli()}}}}}
	var r struct {
		Aggregations struct {
			T struct {
				Buckets []struct {
					Key      int64   `json:"key"`
					DocCount float64 `json:"doc_count"`
				} `json:"buckets"`
			} `json:"t"`
		} `json:"aggregations"`
	}
	if err := cl.do(ctx, http.MethodPost, path+"/_search", url.Values{"ignore_unavailable": {"true"}}, body, &r); err != nil {
		return nil, err
	}
	s := TelemetrySeries{Labels: map[string]string{"index": idx}, Points: []TelemetryPoint{}}
	for _, b := range r.Aggregations.T.Buckets {
		s.Points = append(s.Points, TelemetryPoint{T: time.UnixMilli(b.Key).UTC(), V: b.DocCount})
	}
	d.Series = []TelemetrySeries{s}
	return d, nil
}
