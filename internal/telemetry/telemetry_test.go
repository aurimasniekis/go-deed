package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

type rig struct {
	env *testenv.Env
	svc *Service
	st  *Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	env := testenv.New(t)
	reg := NewRegistry()
	reg.Register(Prometheus{})
	reg.Register(Loki{})
	reg.Register(&NATSMon{})
	reg.Register(OpenSearch{})
	reg.Register(Grafana{})
	reg.Register(&PulseRollups{Store: env.Store, Clock: env.Clock})
	guard, _ := safehttp.New(nil)
	st := &Store{Store: env.Store, Keyring: env.Keyring, Audit: env.Audit, Registry: reg}
	svc := &Service{Store: st, Catalog: env.Catalog, Registry: reg, Guard: guard, Clock: env.Clock, Timeout: 2 * time.Second}
	svc.Init()
	return &rig{env: env, svc: svc, st: st}
}

func local(u string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"url": u, "insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}})
	return b
}

func (r *rig) integration(t *testing.T, name, adapter string, cfg json.RawMessage, secrets map[string]string) *Integration {
	t.Helper()
	it, err := r.st.CreateIntegration(context.Background(), audit.System, IntegrationInput{Name: name, Adapter: adapter, Config: cfg, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (r *rig) binding(t *testing.T, in BindingInput) *Binding {
	t.Helper()
	b, err := r.st.CreateBinding(context.Background(), audit.System, in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *rig) entity(t *testing.T, project, kind, name string, spec map[string]any) *catalog.Entity {
	t.Helper()
	p, err := r.env.Catalog.Project(context.Background(), project)
	if err != nil {
		p = r.env.Project(t, project)
	}
	res, err := r.env.Apply(t, r.env.Source(t, p.ID, "api", ""), catalog.EntityItem{Kind: kind, Name: name, Spec: spec}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.env.Catalog.GetEntity(context.Background(), res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// promServer answers instant and range queries and records what it received.
type promServer struct {
	*httptest.Server
	queries atomic.Int64
	fail    atomic.Bool
	last    atomic.Value // url.Values encoded
	headers atomic.Value
}

func newProm(t *testing.T) *promServer {
	p := &promServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.queries.Add(1)
		p.last.Store(r.URL.Query().Get("query"))
		p.headers.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Scope-OrgID"))
		if p.fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "secret upstream stack trace")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/query":
			q := r.URL.Query().Get("query")
			if strings.Contains(q, "by (code)") {
				_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"code":"200"},"value":[1790000000,"12"]},{"metric":{"code":"500"},"value":[1790000000,"0.5"]}]}}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1790000000,"0.97"]}]}}`)
		case "/api/v1/query_range":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"instance":"a"},"values":[[1790000000,"1"],[1790000060,"2"],[1790000120,"NaN"]]}]}}`)
		case "/api/v1/status/buildinfo":
			_, _ = io.WriteString(w, `{"status":"success","data":{"version":"3.1.0"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func TestParamsAreValidatedAndEscaped(t *testing.T) {
	r := newRig(t)
	prom := newProm(t)
	r.integration(t, "prom", "prometheus", local(prom.URL), nil)
	lbl := r.binding(t, BindingInput{Name: "up", Integration: "prom", Query: `avg(up{instance="{{host}}"})`, Params: []BindingParam{{Name: "host", Type: "label", From: "/spec/hostname"}}})
	str := r.binding(t, BindingInput{Name: "svc", Integration: "prom", Query: `avg(up{service="{{svc}}"})`, Params: []BindingParam{{Name: "svc", Type: "string", From: "/spec/os"}}})
	evil := r.entity(t, "home", "server", "evil", map[string]any{"hostname": `x"} or vector(1) #`, "os": `a"} or on() vector(1) #`})
	res := r.svc.Run(context.Background(), lbl, Subject{Entity: evil}, "1h")
	if res.Status != StatusUnavailable || !strings.Contains(res.Error, "not applicable") || prom.queries.Load() != 0 {
		t.Fatalf("label param with a quote reached the backend: %+v", res)
	}
	res = r.svc.Run(context.Background(), str, Subject{Entity: evil}, "1h")
	if res.Status != StatusMeasured {
		t.Fatalf("string param: %+v", res)
	}
	if q := prom.last.Load().(string); q != `avg(up{service="a\"} or on() vector(1) #"})` {
		t.Fatalf("string param not escaped: %s", q)
	}
}

func TestBindingValidation(t *testing.T) {
	r := newRig(t)
	prom := newProm(t)
	r.integration(t, "prom", "prometheus", local(prom.URL), nil)
	r.integration(t, "loki", "loki", local(prom.URL), nil)
	r.integration(t, "os", "opensearch", local(prom.URL), nil)
	r.integration(t, "graf", "grafana", local(prom.URL), nil)
	for name, in := range map[string]BindingInput{
		"unknown placeholder": {Integration: "prom", Query: `up{x="{{nope}}"}`},
		"unbalanced":          {Integration: "prom", Query: `sum(rate(x[5m])`},
		"log query":           {Integration: "loki", Query: `{app="x"} |= "error"`},
		"script":              {Integration: "os", Query: `logs-* {"script": {"source": "1"}}`},
		"shape":               {Integration: "graf", Query: `search x`, Shape: ShapeScalar},
		"health no threshold": {Integration: "prom", Query: `up`, Role: "health"},
		"edge without type":   {Integration: "prom", Query: `up`, Role: "edge_rate"},
		"param without from":  {Integration: "prom", Query: `up{a="{{a}}"}`, Params: []BindingParam{{Name: "a", Type: "label"}}},
		"bad default":         {Integration: "prom", Query: `up{a="{{a}}"}`, Params: []BindingParam{{Name: "a", Type: "int", Default: "x"}}},
		"builtin shadow":      {Integration: "prom", Query: `up`, Params: []BindingParam{{Name: "window", Type: "duration", Default: "5m"}}},
	} {
		in.Name = "b"
		if _, err := r.st.CreateBinding(context.Background(), audit.System, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIntegrationSecretsAreSealed(t *testing.T) {
	r := newRig(t)
	prom := newProm(t)
	cfg, _ := json.Marshal(map[string]any{"url": prom.URL, "insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}, "tenant": "team-a"})
	it := r.integration(t, "mimir", "prometheus", cfg, map[string]string{"token": "PLANTED-SECRET-TOKEN"})
	var raw []byte
	if err := r.env.Store.R().QueryRow(`SELECT secrets_enc FROM integrations WHERE id = ?`, it.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PLANTED") || strings.Contains(string(it.Config), "PLANTED") {
		t.Fatal("secret stored in clear")
	}
	if _, err := r.svc.Test(context.Background(), it); err != nil {
		t.Fatal(err)
	}
	if h := prom.headers.Load().(string); h != "Bearer PLANTED-SECRET-TOKEN|team-a" {
		t.Fatalf("headers = %q", h)
	}
	if _, err := r.st.CreateIntegration(context.Background(), audit.System, IntegrationInput{Name: "x", Adapter: "prometheus", Config: local(prom.URL),
		Secrets: map[string]string{"api_key": "k"}}); err == nil {
		t.Fatal("unknown secret field accepted")
	}
	// Updates without secrets keep them; {} clears them.
	it, err := r.st.UpdateIntegration(context.Background(), audit.System, "mimir", it.ResourceVersion, IntegrationInput{Enabled: ptr(false)})
	if err != nil || !it.HasSecrets || it.Enabled {
		t.Fatalf("update: %+v %v", it, err)
	}
	it, err = r.st.UpdateIntegration(context.Background(), audit.System, "mimir", 0, IntegrationInput{Secrets: map[string]string{}})
	if err != nil || it.HasSecrets {
		t.Fatalf("clear: %+v %v", it, err)
	}
	if _, err := r.st.UpdateIntegration(context.Background(), audit.System, "mimir", 1, IntegrationInput{}); err == nil {
		t.Fatal("stale version accepted")
	}
}

func ptr[T any](v T) *T { return &v }

func TestCacheStaleAndHealth(t *testing.T) {
	r := newRig(t)
	prom := newProm(t)
	r.integration(t, "prom", "prometheus", local(prom.URL), nil)
	warn, crit := 0.99, 0.9
	b := r.binding(t, BindingInput{Name: "availability", Integration: "prom", Query: `avg(up{instance="{{host}}"})`, Role: "health", Unit: ptr("ratio"),
		Thresholds: &BindingThresholds{Warning: &warn, Critical: &crit, Direction: "below"}, FreshnessSeconds: 60,
		Params: []BindingParam{{Name: "host", Type: "label", From: "/spec/hostname"}}})
	e := r.entity(t, "home", "server", "web-1", map[string]any{"hostname": "web-1.example.com"})
	ctx := context.Background()
	res := r.svc.Run(ctx, b, Subject{Entity: e}, "1h")
	if res.Status != StatusMeasured || res.Data.Value == nil || *res.Data.Value != 0.97 || res.Health.State != "warning" {
		t.Fatalf("first: %+v %+v", res, res.Health)
	}
	r.svc.Run(ctx, b, Subject{Entity: e}, "1h")
	if n := prom.queries.Load(); n != 1 {
		t.Fatalf("cache miss: %d upstream queries", n)
	}
	// After the freshness period the upstream fails: the last value is served as stale, without upstream text.
	r.env.Clock.Advance(2 * time.Minute)
	prom.fail.Store(true)
	res = r.svc.Run(ctx, b, Subject{Entity: e}, "1h")
	if res.Status != StatusStale || res.Data == nil || strings.Contains(res.Error, "secret") || res.Error == "" {
		t.Fatalf("stale: %+v", res)
	}
	// Much later the stale value expires.
	r.env.Clock.Advance(time.Hour)
	res = r.svc.Run(ctx, b, Subject{Entity: e}, "1h")
	if res.Status != StatusUnavailable || res.Data != nil {
		t.Fatalf("unavailable: %+v", res)
	}
	// Entities without the parameter are not applicable.
	other := r.entity(t, "home", "server", "nohost", nil)
	if res := r.svc.Run(ctx, b, Subject{Entity: other}, "1h"); !strings.Contains(res.Error, "not applicable") {
		t.Fatalf("missing param: %+v", res)
	}
}

func TestPrometheusShapes(t *testing.T) {
	r := newRig(t)
	prom := newProm(t)
	r.integration(t, "prom", "prometheus", local(prom.URL), nil)
	e := r.entity(t, "home", "service", "api", nil)
	ctx := context.Background()
	ts := r.svc.Run(ctx, r.binding(t, BindingInput{Name: "ts", Integration: "prom", Query: `rate(x[{{window}}])`, Shape: ShapeTimeseries}), Subject{Entity: e}, "6h")
	if ts.Status != StatusMeasured || len(ts.Data.Series) != 1 || len(ts.Data.Series[0].Points) != 2 {
		t.Fatalf("timeseries (NaN dropped): %+v", ts.Data)
	}
	if q := prom.last.Load().(string); q != "rate(x[6h])" {
		t.Fatalf("window placeholder: %s", q)
	}
	tbl := r.svc.Run(ctx, r.binding(t, BindingInput{Name: "tbl", Integration: "prom", Query: `sum by (code) (x)`, Shape: ShapeTable}), Subject{Entity: e}, "1h")
	if len(tbl.Data.Rows) != 2 || tbl.Data.Rows[0]["code"] != "200" {
		t.Fatalf("table: %+v", tbl.Data)
	}
	multi := r.svc.Run(ctx, r.binding(t, BindingInput{Name: "multi", Integration: "prom", Query: `sum by (code) (y)`}), Subject{Entity: e}, "1h")
	if multi.Status != StatusUnavailable || !strings.Contains(multi.Error, "aggregate") {
		t.Fatalf("multi-series scalar: %+v", multi)
	}
	all, err := r.svc.ForEntity(ctx, e, "1h", "")
	if err != nil || len(all) != 3 {
		t.Fatalf("for entity: %d %v", len(all), err)
	}
}

func TestLokiNATSOpenSearchGrafana(t *testing.T) {
	r := newRig(t)
	var osBody atomic.Value
	var varzMsgs atomic.Int64
	varzMsgs.Store(1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/loki/api/v1/query":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1790000000,"7"]}]}}`)
		case "/varz":
			_, _ = io.WriteString(w, `{"server_name":"n1","version":"2.11.0","in_msgs":`+itoa(varzMsgs.Load())+`}`)
		case "/jsz":
			if req.URL.Query().Get("streams") != "true" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"account_details":[{"stream_detail":[{"name":"OTHER","state":{"messages":1}},{"name":"EVENTS","state":{"messages":42}}]}]}`)
		case "/logs-*/_count":
			b, _ := io.ReadAll(req.Body)
			osBody.Store(string(b))
			_, _ = io.WriteString(w, `{"count":5}`)
		case "/api/search":
			_, _ = io.WriteString(w, `[{"uid":"abc","title":"Node","url":"/d/abc/node","folderTitle":"Infra"},{"uid":"x","title":"Evil","url":"https://evil.example.net/"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	for _, a := range []string{"loki", "natsmon", "opensearch", "grafana"} {
		r.integration(t, a, a, local(srv.URL), nil)
	}
	e := r.entity(t, "home", "service", "api", map[string]any{"url": "https://api.example.com"})
	ctx := context.Background()
	run := func(in BindingInput) *TelemetryResult {
		t.Helper()
		return r.svc.Run(ctx, r.binding(t, in), Subject{Entity: e}, "1h")
	}
	if res := run(BindingInput{Name: "errs", Integration: "loki", Query: `sum(count_over_time({app="api"} |= "error" [{{window}}]))`}); res.Data == nil || *res.Data.Value != 7 {
		t.Fatalf("loki: %+v", res)
	}
	if res := run(BindingInput{Name: "stream", Integration: "natsmon", Query: `jsz?streams=true account_details[0].stream_detail[name=EVENTS].state.messages`}); res.Data == nil || *res.Data.Value != 42 {
		t.Fatalf("natsmon path: %+v", res)
	}
	rate := r.binding(t, BindingInput{Name: "rate", Integration: "natsmon", Query: `rate varz in_msgs`, FreshnessSeconds: 15})
	first := r.svc.Run(ctx, rate, Subject{Entity: e}, "1h")
	if first.Status != StatusUnavailable || !strings.Contains(first.Data.Note, "two samples") {
		t.Fatalf("first rate sample: %+v", first)
	}
	time.Sleep(1100 * time.Millisecond)
	varzMsgs.Store(1100)
	r.env.Clock.Advance(time.Minute)
	second := r.svc.Run(ctx, rate, Subject{Entity: e}, "1h")
	if second.Status != StatusInferred || second.Data.Value == nil || *second.Data.Value <= 0 {
		t.Fatalf("second rate sample: %+v", second)
	}
	if res := run(BindingInput{Name: "docs", Integration: "opensearch", Query: `logs-* {"term": {"url": "{{u}}"}}`,
		Params: []BindingParam{{Name: "u", Type: "string", From: "/spec/url"}}}); res.Data == nil || *res.Data.Value != 5 {
		t.Fatalf("opensearch: %+v", res)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(osBody.Load().(string)), &body); err != nil || !strings.Contains(osBody.Load().(string), `"https://api.example.com"`) ||
		!strings.Contains(osBody.Load().(string), `"range"`) {
		t.Fatalf("opensearch body: %s", osBody.Load())
	}
	res := run(BindingInput{Name: "dash", Integration: "grafana", Query: `search node`, Shape: ShapeLink})
	if res.Data == nil || len(res.Data.Links) != 1 || res.Data.Links[0].URL != srv.URL+"/d/abc/node" {
		t.Fatalf("grafana search (foreign URL dropped): %+v", res.Data)
	}
	res = run(BindingInput{Name: "board", Integration: "grafana", Query: `dashboard abc?var-host={{h}}`, Shape: ShapeLink,
		Params: []BindingParam{{Name: "h", Type: "string", From: "/spec/url"}}})
	if res.Status != StatusInferred || res.Data.Links[0].URL != srv.URL+"/d/abc?var-host=https%3A%2F%2Fapi.example.com" {
		t.Fatalf("grafana dashboard link: %+v", res.Data)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestEdgeRatesFromPulseRollups(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	p := r.env.Project(t, "events")
	tail, err := r.env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "tail", Provider: "pulse_tail", Layer: catalog.LayerDiscovered}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := r.entity(t, "events", "service", "porch", map[string]any{"pulse_source": "porch"})
	sub := r.entity(t, "events", "service", "notifier", nil)
	subj := r.entity(t, "events", "nats_subject", "evt.porch.all", map[string]any{"subject": "evt.porch.>"})
	other := r.entity(t, "events", "service", "unmeasured", nil)
	api := r.env.Source(t, p.ID, "api", "")
	rel := func(typ string, from, to *catalog.Entity) *catalog.Relation {
		res, err := r.env.Relate(t, api, catalog.RelationItem{Type: typ, From: catalog.Ref{ID: from.Metadata.ID}, To: catalog.Ref{ID: to.Metadata.ID}})
		if err != nil {
			t.Fatal(err)
		}
		out, err := r.env.Catalog.GetRelation(ctx, res.RelationID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	pubRel, subRel, depRel := rel("publishes", pub, subj), rel("subscribes_to", sub, subj), rel("depends_on", other, pub)
	now := r.env.Clock.Now()
	err = r.env.Store.W().Tx(ctx, func(tx *store.Tx) error {
		for i := 1; i <= 15; i++ {
			b := now.Truncate(time.Minute).Add(-time.Duration(i) * time.Minute).UnixMicro()
			for _, row := range [][]any{{"porch", "evt.porch.tenancy.tenant.created", 60}, {"intruder", "evt.porch.tenancy.tenant.created", 30}, {"porch", "evt.other.x", 600}} {
				if _, err := tx.Exec(ctx, `INSERT INTO pulse_rollups (source_id, granularity, bucket, publisher, subject, ce_type, messages, bytes, sampled)
					VALUES (?, 'minute', ?, ?, ?, 't', ?, 0, 0)`, tail.ID, b, row[0], row[1], row[2]); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rates, err := r.svc.EdgeRates(ctx, []*catalog.Relation{pubRel, subRel, depRel}, "15m")
	if err != nil {
		t.Fatal(err)
	}
	if got := rates[0]; got.Status != StatusMeasured || got.Rate == nil || *got.Rate != 1 || got.Basis != "pulse_tail" {
		t.Fatalf("publish edge (60 msg/min from porch only): %+v", got)
	}
	if got := rates[1]; got.Status != StatusInferred || got.Rate == nil || *got.Rate != 1.5 || !strings.Contains(got.Note, "not measured") {
		t.Fatalf("subscribe edge: %+v", got)
	}
	if got := rates[2]; got.Status != StatusUnavailable || got.Rate != nil {
		t.Fatalf("unmeasured edge: %+v", got)
	}
}
