package pulseint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestTemplate(t *testing.T) {
	cases := map[string]string{
		"evt.porch.tenancy.tenant.created":                                "evt.porch.tenancy.tenant.created",
		"evt.porch.tenancy.tenant.updated.ten_01jb9w2v3x4y5z6a7b8c9d0e1f": "evt.porch.tenancy.tenant.updated.*",
		"evt.home.door.opened.42":                                         "evt.home.door.opened.*",
		"evt.ci.build.done.3f2a9c1b7d":                                    "evt.ci.build.done.*",
		"evt.app.user.signed_in.550e8400-e29b-41d4-a716-446655440000":     "evt.app.user.signed_in.*",
		"evt.app.user.signed_in.alice":                                    "evt.app.user.signed_in.alice",
	}
	for in, want := range cases {
		if got := Template(in); got != want {
			t.Errorf("Template(%s) = %s, want %s", in, got, want)
		}
	}
	if !Matches("evt.porch.>", "evt.porch.tenancy.tenant.created") || Matches("evt.porch.*", "evt.porch.a.b") || !Matches("evt.*.x", "evt.a.x") {
		t.Fatal("Matches")
	}
}

func fixtureServer(t *testing.T) *httptest.Server {
	b, err := os.ReadFile("testdata/admin.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes map[string]json.RawMessage
	if err := json.Unmarshal(b, &routes); err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pulse_pat_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/api/admin/v1")
		if r.URL.RawQuery != "" {
			q := r.URL.Query()
			key += "?prefix=" + q.Get("prefix") + "&stream=" + q.Get("stream")
		}
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func runner(env *testenv.Env) *sources.Runner {
	reg := sources.NewRegistry()
	reg.Register(Admin{})
	guard, _ := safehttp.New(nil)
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 100000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	return &sources.Runner{Store: env.Store, Catalog: env.Catalog, Ingest: ing, Audit: env.Audit, Registry: reg, Guard: guard, Clock: env.Clock,
		Log: slog.New(slog.DiscardHandler), MaxItems: 100000}
}

func TestAdminSync(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	srv := fixtureServer(t)
	defer srv.Close()
	p := env.Project(t, "events")
	cfg, _ := json.Marshal(map[string]any{"admin_url": srv.URL, "insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}})
	src, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "pulse", Provider: AdminType,
		Layer: catalog.LayerImported, Config: cfg}, map[string]string{"token": "pulse_pat_test"})
	if err != nil {
		t.Fatal(err)
	}
	r := runner(env)
	if diags, err := r.Test(ctx, src); err != nil || len(diags) != 1 || diags[0].Level != "info" {
		t.Fatalf("test: %+v %v", diags, err)
	}
	run, res, err := r.Sync(ctx, src, "manual", false, audit.System)
	if err != nil {
		t.Fatalf("sync: %v %+v", err, run)
	}
	if res.Summary.Errors != 0 {
		t.Fatalf("item errors: %+v", res.Results)
	}
	page, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Limit: 100})
	names := map[string]bool{}
	for _, e := range page.Items {
		names[e.Kind+"/"+e.Metadata.Name] = true
	}
	// Shapes follow a real pulsed (scripts/validate/pulse.sh): ULID client IDs, durables "<client-id>_<name>", outbound
	// hooks as internal durables, pulsed's own rollup reader as an ephemeral consumer.
	for _, want := range []string{"service/porch", "service/notifier", "nats_stream/events", "nats_consumer/events-01jbx2q8m5n0t1f1er6e7f8g9h_tenants",
		"nats_subject/evt.porch.all", "nats_subject/evt.porch.tenancy.tenant.created", "nats_subject/evt.porch.tenancy.tenant.updated.star",
		"event_contract/porch.tenancy.tenant_created.v1", "nats_system/pulse-1"} {
		if !names[want] {
			t.Errorf("missing %s (have %v)", want, names)
		}
	}
	if names["service/old"] || names["nats_stream/kv_config"] || names["nats_stream/obj_blobs"] ||
		names["nats_consumer/events-pulse_out_01jbx2q9a1h00k0000000000aa"] || names["nats_consumer/events-7an6u9nkqsjlqxoc8vq8te_1"] {
		t.Fatalf("revoked client, kv/object bucket, internal or ephemeral consumer imported: %v", names)
	}
	rels, _ := env.Catalog.ListRelations(ctx, catalog.RelationFilter{Limit: 200})
	got := map[string]string{}
	for _, r := range rels.Items {
		got[r.Type+" "+r.From.Name+" → "+r.To.Name] = r.EvidenceClass
	}
	for _, want := range []string{"publishes porch → evt.porch.all", "subscribes_to notifier → evt.porch.tenancy.all",
		"subscribes_to events-01jbx2q8m5n0t1f1er6e7f8g9h_tenants → evt.porch.tenancy.all",
		"contains events → events-01jbx2q8m5n0t1f1er6e7f8g9h_tenants", "contains evt.porch.all → evt.porch.tenancy.tenant.created", "carried_on porch.tenancy.tenant_created.v1 → evt.porch.tenancy.tenant.created"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing relation %s in %v", want, got)
		}
	}
}

// startNATS runs a local nats-server with JetStream (skips when the binary is absent).
func startNATS(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("DEED_TEST_NATS"); u != "" {
		return u
	}
	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("nats-server not installed; set DEED_TEST_NATS to an existing server")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir := t.TempDir()
	cmd := exec.Command(bin, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", filepath.Join(dir, "js"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	for range 50 {
		if nc, err := nats.Connect(url); err == nil {
			nc.Close()
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func pulseClient(t *testing.T, url, source string) *pulse.Client {
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	c, err := pulse.New(nc, source)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTailAndIngestConsumerAgainstNATS(t *testing.T) {
	url := startNATS(t)
	env := testenv.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	{
		nc, _ := nats.Connect(url)
		js, _ := jetstream.New(nc)
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"evt.>"}, Duplicates: 2 * time.Minute}); err != nil {
			t.Fatal(err)
		}
		nc.Close()
	}
	p := env.Project(t, "events")
	tailSrc, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "tail", Provider: TailType,
		Layer: catalog.LayerDiscovered, Config: json.RawMessage(`{"window_seconds": 1}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pushCfg := json.RawMessage(`{"pulse_source": "agent-1"}`)
	pushSrc, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "agent", Provider: "push",
		Layer: catalog.LayerDiscovered, Config: pushCfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 100000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	log := slog.New(slog.DiscardHandler)
	tail := &Tail{Store: env.Store, Catalog: env.Catalog, Ingest: ing, Clock: env.Clock, Log: log}
	go tail.runOne(ctx, pulseClient(t, url, "deed"), tailSrc)
	cons := &Consumer{Catalog: env.Catalog, Ingest: ing, Log: log}
	go func() { _ = cons.Run(ctx, pulseClient(t, url, "deed"), "evt.deed.ingest.>", "deed_ingest") }()
	time.Sleep(500 * time.Millisecond)

	porch := pulseClient(t, url, "porch")
	for i := range 5 {
		if _, err := porch.Publish(ctx, fmt.Sprintf("evt.porch.tenancy.tenant.updated.ten_01jb9w2v3x4y5z6a7b8c9d0e%02d", i), "porch.tenancy.tenant_updated.v1",
			map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	agent := pulseClient(t, url, "agent-1")
	items := Envelope{SourceID: pushSrc.ID, Entities: []catalog.EntityItem{{Kind: "container", Name: "web", ExternalID: "c1",
		Spec: map[string]any{"image": "example/web"}}}}
	if _, err := agent.Publish(ctx, "evt.deed.ingest.agent-1.items", TypeItems, items); err != nil {
		t.Fatal(err)
	}
	// A different publisher cannot write into the agent's source: neither with its own ce-source nor by claiming the
	// agent's ce-source on its own subject (Pulse authenticates subjects through grants, not ce-source).
	rogue := pulseClient(t, url, "rogue")
	bad := Envelope{SourceID: pushSrc.ID, Entities: []catalog.EntityItem{{Kind: "container", Name: "evil", ExternalID: "x"}}}
	if _, err := rogue.Publish(ctx, "evt.deed.ingest.agent-1.items", TypeItems, bad); err != nil {
		t.Fatal(err)
	}
	spoofed := Envelope{SourceID: pushSrc.ID, Entities: []catalog.EntityItem{{Kind: "container", Name: "spoofed", ExternalID: "y"}}}
	if _, err := rogue.Publish(ctx, "evt.deed.ingest.rogue.items", TypeItems, spoofed, pulse.WithEventSource("agent-1")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var rels *catalog.RelationPage
	for time.Now().Before(deadline) {
		rels, _ = env.Catalog.ListRelations(ctx, catalog.RelationFilter{Types: []string{"publishes"}, Limit: 10})
		c, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Kinds: []string{"container"}, Limit: 10})
		if rels != nil && rels.Total > 0 && c.Total > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	var edge *catalog.Relation
	for _, r := range rels.Items {
		if r.From.Name == "porch" {
			edge = r
		}
	}
	// The tail sees every evt.> message: porch's events and the ingest envelopes (porch, agent-1 and rogue on
	// agent-1's subject, "agent-1" on rogue's subject: 4 publisher/subject pairs).
	if edge == nil || rels.Total != 4 || edge.To.Name != "evt.porch.tenancy.tenant.updated.star" || edge.EvidenceClass != catalog.EvidenceObserved {
		t.Fatalf("observed publish edge missing: %+v", rels.Items)
	}
	if edge.Attributes["messages_per_minute"] == nil {
		t.Fatalf("rate missing: %+v", edge.Attributes)
	}
	time.Sleep(500 * time.Millisecond) // the rejected envelopes follow the accepted one
	cs, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Kinds: []string{"container"}, Limit: 10})
	if cs.Total != 1 || cs.Items[0].Metadata.Name != "web" {
		t.Fatalf("ingest consumer: %+v", cs.Items)
	}
	var n int
	_ = env.Store.R().QueryRowContext(ctx, `SELECT COALESCE(SUM(messages), 0) FROM pulse_rollups WHERE granularity = 'minute' AND publisher = 'porch'`).Scan(&n)
	if n != 5 {
		t.Fatalf("rollups counted %d messages", n)
	}
}

func TestSubjectPublisher(t *testing.T) {
	cases := []struct{ filter, subject, want string }{
		{"evt.deed.ingest.>", "evt.deed.ingest.billing.items", "billing"},
		{"evt.deed.ingest.>", "evt.deed.ingest", ""},
		{"evt.*.ingest.>", "evt.billing.ingest.items", "billing"},
		{"evt.deed.ingest.billing.items", "evt.deed.ingest.billing.items", ""},
	}
	for _, c := range cases {
		if got := SubjectPublisher(c.filter, c.subject); got != c.want {
			t.Errorf("SubjectPublisher(%s, %s) = %q, want %q", c.filter, c.subject, got, c.want)
		}
	}
}

// Pulse authenticates the subject (client grants) but not ce-source: an event is accepted only when both name the
// source's pulse_source.
func TestConsumerBindsSubjectToPublisher(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "events")
	src, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "billing", Provider: "push",
		Layer: catalog.LayerDiscovered, Config: json.RawMessage(`{"pulse_source": "billing"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 100000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	cons := &Consumer{Catalog: env.Catalog, Ingest: ing, Log: slog.New(slog.DiscardHandler), Self: "deed", Filter: "evt.deed.ingest.>"}
	event := func(subject, source, name string) pulse.Event {
		b, _ := json.Marshal(Envelope{SourceID: src.ID, Entities: []catalog.EntityItem{{Kind: "container", Name: name, ExternalID: name}}})
		return pulse.Event{Subject: subject, Source: source, Type: TypeItems, ID: name, Data: b}
	}
	for _, e := range []pulse.Event{
		event("evt.deed.ingest.rogue.items", "billing", "spoofed-source"),
		event("evt.deed.ingest.billing.items", "rogue", "wrong-source"),
		event("evt.deed.ingest.items", "billing", "no-publisher-token"),
	} {
		if err := cons.Handle(ctx, e); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s from %s on %s: want a terminal ErrNotAllowed, got %v", e.ID, e.Source, e.Subject, err)
		}
	}
	if err := cons.Handle(ctx, event("evt.deed.ingest.billing.items", "billing", "web")); err != nil {
		t.Fatalf("legitimate event: %v", err)
	}
	cs, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Kinds: []string{"container"}, Limit: 10})
	if cs.Total != 1 || cs.Items[0].Metadata.Name != "web" {
		t.Fatalf("containers: %+v", cs.Items)
	}
}
