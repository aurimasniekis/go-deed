package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// Prometheus queries a Prometheus-compatible HTTP API (Prometheus, Mimir, Thanos, VictoriaMetrics, or a Grafana
// data-source proxy URL).
type Prometheus struct{}

const promSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {` + httpSchemaProps + `,
    "tenant": {"type": "string", "maxLength": 128, "description": "Mimir/Cortex tenant (X-Scope-OrgID)."}
  }
}`

// Info implements Adapter.
func (Prometheus) Info() AdapterInfo {
	return AdapterInfo{Type: "prometheus", Title: "Prometheus / Mimir", Description: "PromQL against a Prometheus-compatible API. For Mimir use " +
		"the query-frontend URL including the /prometheus prefix and set tenant. Through Grafana, use " +
		"https://GRAFANA/api/datasources/proxy/uid/<uid> with a Grafana service-account token.",
		ConfigSchema: json.RawMessage(promSchema), SecretFields: []string{"token", "username", "password"},
		Shapes: []string{ShapeScalar, ShapeTimeseries, ShapeTable},
		QueryHelp: "PromQL. Scalars need a single-series result (aggregate with sum/max). Use {{window}} for range selectors, e.g. " +
			`sum(rate(http_requests_total{job="{{job}}"}[{{window}}])).`,
		Verified: "contract tests against Prometheus HTTP API v1 fixtures; docker-compose Prometheus validation in scripts/e2e.sh; " +
			"multi-tenant Grafana Mimir validation in scripts/validate/telemetry.sh"}
}

// Validate implements Adapter.
func (Prometheus) Validate(cfg json.RawMessage, _ map[string]string) error { return validateHTTP(cfg) }

// ValidateQuery implements Adapter.
func (Prometheus) ValidateQuery(text, _ string) error {
	if strings.TrimSpace(text) == "" || len(text) > 8192 {
		return fmt.Errorf("PromQL query required (≤ 8 KiB)")
	}
	return balanced(text)
}

// balanced is a cheap static check for brackets and quotes; the server parses the real grammar.
func balanced(s string) error {
	var stack []rune
	var quote rune
	esc := false
	for _, r := range s {
		if quote != 0 {
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == quote:
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'', '`':
			quote = r
		case '(', '[', '{':
			stack = append(stack, r)
		case ')', ']', '}':
			want := map[rune]rune{')': '(', ']': '[', '}': '{'}[r]
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return fmt.Errorf("unbalanced %q", r)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if quote != 0 || len(stack) > 0 {
		return fmt.Errorf("unterminated quote or bracket")
	}
	return nil
}

// Escape implements Adapter: values land inside double-quoted PromQL/LogQL strings.
func (Prometheus) Escape(v string) string { return promEscape(v) }

func promEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "`", "").Replace(v)
}

// promResponse is the Prometheus (and Loki metric query) API envelope.
type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
	Values [][2]any          `json:"values"`
}

func sampleValue2(v [2]any) (TelemetryPoint, bool) {
	ts, ok := v[0].(float64)
	s, ok2 := v[1].(string)
	if !ok || !ok2 {
		return TelemetryPoint{}, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return TelemetryPoint{}, false
	}
	sec, frac := math.Modf(ts)
	return TelemetryPoint{T: time.Unix(int64(sec), int64(frac*1e9)).UTC(), V: f}, true
}

// decodeProm turns a vector/matrix/scalar result into Data for the requested shape.
func decodeProm(r *promResponse, shape string) (*TelemetryData, error) {
	if r.Status != "success" {
		return nil, apperr.New(apperr.CodeInvalid, "query failed upstream (%s)", r.ErrorType)
	}
	d := &TelemetryData{}
	switch r.Data.ResultType {
	case "scalar":
		var v [2]any
		if err := json.Unmarshal(r.Data.Result, &v); err != nil {
			return nil, apperr.New(apperr.CodeUnavailable, "unexpected scalar result")
		}
		if p, ok := sampleValue2(v); ok {
			d.Value = &p.V
		}
		return d, nil
	case "vector", "matrix":
	case "streams":
		return nil, apperr.New(apperr.CodeInvalid, "log stream results are not supported; use a metric query")
	default:
		return nil, apperr.New(apperr.CodeUnavailable, "unsupported result type %q", r.Data.ResultType)
	}
	var samples []promSample
	if err := json.Unmarshal(r.Data.Result, &samples); err != nil {
		return nil, apperr.New(apperr.CodeUnavailable, "unexpected result")
	}
	sort.Slice(samples, func(i, j int) bool { return labelKey(samples[i].Metric) < labelKey(samples[j].Metric) })
	switch shape {
	case ShapeScalar:
		if len(samples) > 1 {
			return nil, apperr.New(apperr.CodeInvalid, "query returned %d series; aggregate it to one for a scalar binding", len(samples))
		}
		if len(samples) == 1 {
			v := samples[0].Value
			if r.Data.ResultType == "matrix" && len(samples[0].Values) > 0 {
				v = samples[0].Values[len(samples[0].Values)-1]
			}
			if p, ok := sampleValue2(v); ok {
				d.Value = &p.V
			}
		}
	case ShapeTable:
		for _, s := range samples {
			row := map[string]string{}
			for k, v := range s.Metric {
				row[k] = v
			}
			if p, ok := sampleValue2(s.Value); ok {
				row["value"] = strconv.FormatFloat(p.V, 'g', 6, 64)
			}
			d.Rows = append(d.Rows, row)
		}
	case ShapeTimeseries:
		for _, s := range samples {
			ser := TelemetrySeries{Labels: s.Metric, Points: []TelemetryPoint{}}
			if ser.Labels == nil {
				ser.Labels = map[string]string{}
			}
			for _, v := range s.Values {
				if p, ok := sampleValue2(v); ok {
					ser.Points = append(ser.Points, p)
				}
			}
			d.Series = append(d.Series, ser)
		}
	}
	return d, nil
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + ",")
	}
	return b.String()
}

// tenantHint explains a 401 from an integration without a tenant: multi-tenant Mimir, Cortex and Loki answer
// "no org id" (401) when X-Scope-OrgID is missing, which otherwise reads like bad credentials.
func tenantHint(err error, cfg *httpConfig) error {
	if cfg.Tenant == "" && upstreamStatus(err) == http.StatusUnauthorized {
		return apperr.New(apperr.CodeForbidden, "upstream refused the request (401); a multi-tenant Mimir, Cortex or Loki needs tenant (X-Scope-OrgID)")
	}
	return err
}

func unixParam(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', 3, 64)
}

// Test implements Adapter.
func (Prometheus) Test(ctx context.Context, c Conn) (string, error) {
	cl, cfg, err := newHTTPClient(c, 1<<20)
	if err != nil {
		return "", err
	}
	var r promResponse
	if err := cl.do(ctx, http.MethodGet, "/api/v1/query", url.Values{"query": {"vector(1)"}}, nil, &r); err != nil {
		return "", tenantHint(err, cfg)
	}
	if r.Status != "success" {
		return "", apperr.New(apperr.CodeUnavailable, "the endpoint did not answer like a Prometheus API")
	}
	var bi struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if cl.do(ctx, http.MethodGet, "/api/v1/status/buildinfo", nil, nil, &bi) == nil && bi.Data.Version != "" {
		return "Prometheus API reachable (version " + bi.Data.Version + ")", nil
	}
	return "Prometheus API reachable", nil
}

// Query implements Adapter.
func (Prometheus) Query(ctx context.Context, c Conn, q Query) (*TelemetryData, error) {
	cl, cfg, err := newHTTPClient(c, 4<<20)
	if err != nil {
		return nil, err
	}
	var r promResponse
	if q.Shape == ShapeTimeseries {
		err = cl.do(ctx, http.MethodGet, "/api/v1/query_range", url.Values{"query": {q.Text}, "start": {unixParam(q.Start)}, "end": {unixParam(q.End)},
			"step": {strconv.Itoa(int(q.Step.Seconds()))}}, nil, &r)
	} else {
		err = cl.do(ctx, http.MethodGet, "/api/v1/query", url.Values{"query": {q.Text}, "time": {unixParam(q.End)}}, nil, &r)
	}
	if err != nil {
		return nil, tenantHint(err, cfg)
	}
	return decodeProm(&r, q.Shape)
}

// Loki runs LogQL metric queries. Log line queries are rejected: Deed summarizes, it does not ship logs.
type Loki struct{}

const lokiSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {` + httpSchemaProps + `,
    "tenant": {"type": "string", "maxLength": 128, "description": "Loki tenant (X-Scope-OrgID)."}
  }
}`

// Info implements Adapter.
func (Loki) Info() AdapterInfo {
	return AdapterInfo{Type: "loki", Title: "Loki", Description: "LogQL metric queries (count_over_time, rate, sum by …) against Loki. " +
		"Log lines are never fetched or stored.", ConfigSchema: json.RawMessage(lokiSchema), SecretFields: []string{"token", "username", "password"},
		Shapes:    []string{ShapeScalar, ShapeTimeseries, ShapeTable},
		QueryHelp: `LogQL metric query, e.g. sum(count_over_time({app="{{app}}"} |= "error" [{{window}}])).`,
		Verified:  "contract tests against Loki HTTP API fixtures; docker-compose Loki validation in scripts/e2e.sh"}
}

// Validate implements Adapter.
func (Loki) Validate(cfg json.RawMessage, _ map[string]string) error { return validateHTTP(cfg) }

// ValidateQuery implements Adapter.
func (Loki) ValidateQuery(text, _ string) error {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 8192 {
		return fmt.Errorf("LogQL query required (≤ 8 KiB)")
	}
	if strings.HasPrefix(t, "{") {
		return fmt.Errorf("log queries are not allowed; wrap the selector in a metric function such as count_over_time(...[{{window}}])")
	}
	return balanced(t)
}

// Escape implements Adapter.
func (Loki) Escape(v string) string { return promEscape(v) }

// Test implements Adapter.
func (Loki) Test(ctx context.Context, c Conn) (string, error) {
	cl, cfg, err := newHTTPClient(c, 1<<20)
	if err != nil {
		return "", err
	}
	var bi struct {
		Version string `json:"version"`
	}
	if err := cl.do(ctx, http.MethodGet, "/loki/api/v1/status/buildinfo", nil, nil, &bi); err != nil {
		return "", err
	}
	var labels struct {
		Status string `json:"status"`
	}
	if err := cl.do(ctx, http.MethodGet, "/loki/api/v1/labels", nil, nil, &labels); err != nil {
		return "", tenantHint(err, cfg)
	}
	return "Loki reachable (version " + bi.Version + ")", nil
}

// Query implements Adapter.
func (Loki) Query(ctx context.Context, c Conn, q Query) (*TelemetryData, error) {
	cl, cfg, err := newHTTPClient(c, 4<<20)
	if err != nil {
		return nil, err
	}
	var r promResponse
	ns := func(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }
	if q.Shape == ShapeTimeseries {
		err = cl.do(ctx, http.MethodGet, "/loki/api/v1/query_range", url.Values{"query": {q.Text}, "start": {ns(q.Start)}, "end": {ns(q.End)},
			"step": {strconv.Itoa(int(q.Step.Seconds()))}, "limit": {"1"}}, nil, &r)
	} else {
		err = cl.do(ctx, http.MethodGet, "/loki/api/v1/query", url.Values{"query": {q.Text}, "time": {ns(q.End)}, "limit": {"1"}}, nil, &r)
	}
	if err != nil {
		return nil, tenantHint(err, cfg)
	}
	return decodeProm(&r, q.Shape)
}
