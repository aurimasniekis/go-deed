package connectors

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// fixture serves testdata/<name>.json: keys are path?query (per_page dropped, query sorted); values are a body, a bare
// status code, or {"body": …, "link": next-path} for GitHub Link pagination. Unknown keys answer fallback.
type fixture struct {
	*httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	failed map[string]int // key → status to force
}

func serveFixture(t *testing.T, name, prefix, token string, fallback any) *fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var routes map[string]json.RawMessage
	if err := json.Unmarshal(b, &routes); err != nil {
		t.Fatal(err)
	}
	f := &fixture{hits: map[string]int{}, failed: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		q.Del("per_page")
		key := strings.TrimPrefix(r.URL.Path, prefix)
		if len(q) > 0 {
			key += "?" + q.Encode()
		}
		f.mu.Lock()
		f.hits[key]++
		forced := f.failed[key]
		f.mu.Unlock()
		if forced != 0 {
			w.WriteHeader(forced)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		raw, ok := routes[key]
		if !ok {
			if fallback == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(fallback)
			return
		}
		var status int
		if json.Unmarshal(raw, &status) == nil {
			w.WriteHeader(status)
			return
		}
		var wrapped struct {
			Body json.RawMessage `json:"body"`
			Link string          `json:"link"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && wrapped.Body != nil {
			if wrapped.Link != "" {
				w.Header().Set("Link", "<"+f.URL+prefix+wrapped.Link+`>; rel="next", <`+f.URL+prefix+`/x?page=9>; rel="last"`)
			}
			raw = wrapped.Body
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fixture) fail(key string, status int) {
	f.mu.Lock()
	f.failed[key] = status
	f.mu.Unlock()
}

func runner(env *testenv.Env) *sources.Runner {
	reg := sources.NewRegistry()
	reg.Register(GitHub{})
	reg.Register(HallPass{})
	reg.Register(Porch{})
	guard, _ := safehttp.New(nil)
	ing := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: 1000, MaxSnapshotItems: 100000, SnapshotTimeout: time.Hour, MaxRemovalPercent: 25}}
	return &sources.Runner{Store: env.Store, Catalog: env.Catalog, Ingest: ing, Audit: env.Audit, Registry: reg, Guard: guard, Clock: env.Clock,
		Log: slog.New(slog.DiscardHandler), MaxItems: 100000}
}

func newSource(t *testing.T, env *testenv.Env, projectID, provider string, cfg map[string]any, token string) *catalog.Source {
	t.Helper()
	cfg["insecure_http"] = true
	cfg["allow_cidrs"] = []string{"127.0.0.1/32"}
	raw, _ := json.Marshal(cfg)
	p, _ := runner(env).Registry.Get(provider)
	if err := p.Validate(raw, nil); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var pol *catalog.Policy
	if d := p.Describe(); d.DefaultPolicy != nil {
		pol = d.DefaultPolicy
	}
	src, err := env.Catalog.CreateSource(context.Background(), audit.System, catalog.SourceInput{ProjectID: projectID, Name: provider, Provider: provider,
		Layer: catalog.LayerImported, Config: raw, Policy: pol}, map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func entities(t *testing.T, env *testenv.Env) map[string]*catalog.Entity {
	t.Helper()
	page, err := env.Catalog.ListEntities(context.Background(), catalog.EntityFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*catalog.Entity{}
	for _, e := range page.Items {
		out[e.Kind+"/"+e.Metadata.Name] = e
	}
	return out
}

func relations(t *testing.T, env *testenv.Env) map[string]*catalog.Relation {
	t.Helper()
	rels, err := env.Catalog.ListRelations(context.Background(), catalog.RelationFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*catalog.Relation{}
	for _, r := range rels.Items {
		out[r.Type+" "+r.From.Name+" -> "+r.To.Name] = r
	}
	return out
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func syncOK(t *testing.T, r *sources.Runner, src *catalog.Source) *sources.Run {
	t.Helper()
	run, res, err := r.Sync(context.Background(), src, "manual", false, audit.System)
	if err != nil {
		t.Fatalf("sync: %v %+v", err, run)
	}
	for _, it := range res.Results {
		if it.Error != nil {
			t.Errorf("item %d (%s): %s", it.Index, it.Type, it.Error.Message)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return run
}

func TestDeedRefs(t *testing.T) {
	got := DeedRefs("see deed:service/gateway, deed:ent_01JB9W2V3X4Y5Z6A7B8C9D0E1F and (deed:dns_record/api.example.com).",
		"xdeed:service/no", "deed:service/gateway", "deed:BAD/x")
	want := []string{"service/gateway", "ent_01JB9W2V3X4Y5Z6A7B8C9D0E1F", "dns_record/api.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, r := range got {
		if r.String() != want[i] {
			t.Errorf("ref %d = %s, want %s", i, r.String(), want[i])
		}
	}
}

func TestGitHubSync(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	gh := serveFixture(t, "github", "", "ghp_test", []any{})
	p := env.Project(t, "software")
	api := env.Source(t, p.ID, "api", "")
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "service", Name: "gateway"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	src := newSource(t, env, p.ID, GitHubType, map[string]any{"api_url": gh.URL, "owners": []string{"acme", "octo"},
		"repositories": []string{"acme/infra"}, "topics": []string{"infra"}, "include_archived": true}, "ghp_test")
	r := runner(env)
	if diags, err := r.Test(ctx, src); err != nil || diags[len(diags)-1].Code != "ok" {
		t.Fatalf("test: %+v %v", diags, err)
	}
	run := syncOK(t, r, src)
	if run.Result != "ok" {
		t.Fatalf("run %s: %+v", run.Result, run.Diagnostics)
	}
	ents := entities(t, env)
	for _, want := range []string{"repository/acme-api", "repository/acme-web", "repository/acme-infra", "repository/octo-dotfiles",
		"release/acme-api-v1.2.0", "deployment/acme-api-production", "deployment/acme-api-staging",
		"pull_request/acme-api-pr-12", "pull_request/acme-api-pr-11", "issue/acme-api-issue-20", "issue/acme-api-issue-15"} {
		if ents[want] == nil {
			t.Errorf("missing %s (have %v)", want, keys(ents))
		}
	}
	for _, unwanted := range []string{"repository/acme-upstream-fork", "repository/acme-notes", "release/acme-api-v1.3.0",
		"pull_request/acme-api-pr-5", "issue/acme-api-issue-12"} {
		if ents[unwanted] != nil {
			t.Errorf("unexpected %s (fork/topic filter, draft, retention or PR-as-issue)", unwanted)
		}
	}
	if pr := ents["pull_request/acme-api-pr-11"]; pr != nil && pr.Spec["state"] != "merged" {
		t.Errorf("pr 11 state = %v", pr.Spec["state"])
	}
	if d := ents["deployment/acme-api-production"]; d != nil && (d.Spec["sha"] != "a1b2c3d" || d.Spec["status"] != "success" || d.Metadata.Environment != "production") {
		t.Errorf("production deployment = %+v %+v", d.Spec, d.Metadata)
	}
	if d := ents["deployment/acme-api-staging"]; d != nil && d.Status.Health["state"] != "failed" {
		t.Errorf("staging health = %+v", d.Status.Health)
	}
	if repo := ents["repository/acme-infra"]; repo != nil && repo.Metadata.Lifecycle != "deprecated" {
		t.Errorf("archived repo lifecycle = %q", repo.Metadata.Lifecycle)
	}
	rels := relations(t, env)
	for _, want := range []string{"contains acme-api -> acme-api-v1.2.0", "deployed_from acme-api-production -> acme-api",
		"deployed_from acme-api-production -> acme-api-v1.2.0", "references acme-api-pr-12 -> acme-api",
		"tracks acme-api-pr-12 -> gateway", "tracks acme-api-issue-20 -> gateway"} {
		if rels[want] == nil {
			t.Errorf("missing relation %s (have %v)", want, keys(rels))
		}
	}
	if rels["deployed_from acme-api-staging -> acme-api-v1.2.0"] != nil {
		t.Error("staging deployed from a release it does not reference")
	}
	un, err := env.Catalog.Unresolved(ctx, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	unresolved := map[string]bool{}
	for _, u := range un {
		unresolved[u.To] = true
	}
	if !unresolved["server/missing-box"] || !unresolved["dns_record/api.example.com"] {
		t.Errorf("unresolved refs = %+v", un)
	}

	// A failing endpoint makes the run partial: nothing the run did not see is removed.
	gh.fail("/repos/acme/api/pulls?direction=desc&sort=updated&state=open", http.StatusBadGateway)
	run = syncOK(t, r, src)
	if run.Result != "partial" {
		t.Fatalf("run = %s %+v", run.Result, run.Diagnostics)
	}
	if ents := entities(t, env); ents["pull_request/acme-api-pr-12"] == nil {
		t.Fatal("partial run tombstoned a pull request it could not read")
	}
}

func TestGitHubRateBudget(t *testing.T) {
	env := testenv.New(t)
	gh := serveFixture(t, "github", "", "ghp_test", []any{})
	p := env.Project(t, "software")
	src := newSource(t, env, p.ID, GitHubType, map[string]any{"api_url": gh.URL, "repositories": []string{"acme/infra"},
		"include_archived": true, "min_rate_remaining": 4500}, "ghp_test")
	run := syncOK(t, runner(env), src)
	if run.Result != "partial" {
		t.Fatalf("run = %s", run.Result)
	}
	if entities(t, env)["repository/acme-infra"] == nil {
		t.Fatal("repository missing")
	}
	for k := range gh.hits {
		if strings.Contains(k, "/releases") || strings.Contains(k, "/pulls") {
			t.Fatalf("requested %s after the budget ran out", k)
		}
	}
}

func TestGitHubRejectsForeignPaginationLink(t *testing.T) {
	gh := serveFixture(t, "github", "", "ghp_test", nil)
	guard, _ := safehttp.New(nil)
	e := sources.Env{HTTP: guard.Client}
	c, err := newClient(e, "GitHub", gh.URL+"/api", []string{"127.0.0.1/32"}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if _, err := c.getURL(context.Background(), "https://evil.example.net/repos", &out); err == nil {
		t.Fatal("followed a pagination link to another origin")
	}
}

func TestHallPassSync(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	hp := serveFixture(t, "hallpass", "/api/v1", "hallpass_at_test", nil)
	p := env.Project(t, "access")
	api := env.Source(t, p.ID, "api", "")
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "server", Name: "web-1", Spec: map[string]any{"ipv4": "192.0.2.10"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "server", Name: "builder", Spec: map[string]any{"hostname": "builder-1"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	src := newSource(t, env, p.ID, HallPassType, map[string]any{"url": hp.URL}, "hallpass_at_test")
	r := runner(env)
	if diags, err := r.Test(ctx, src); err != nil || diags[0].Code != "ok" || len(diags) != 2 || diags[1].Code != "address_redacted" {
		t.Fatalf("test: %+v %v", diags, err)
	}
	run := syncOK(t, r, src)
	if run.Result != "ok" {
		t.Fatalf("run %s: %+v", run.Result, run.Diagnostics)
	}
	ents := entities(t, env)
	for _, want := range []string{"service/hallpass", "hallpass_agent/ci-runner", "hallpass_agent/claude-box", "agent_box/builder-1-ci-runner",
		"hallpass_server/web-1", "hallpass_server/git-host", "hallpass_role/web-1-deploy", "hallpass_grant/grant-gr_01", "hallpass_grant/grant-gr_02",
		"agent_session/session-ses_01"} {
		if ents[want] == nil {
			t.Errorf("missing %s (have %v)", want, keys(ents))
		}
	}
	for _, unwanted := range []string{"hallpass_agent/retired", "hallpass_server/gone", "hallpass_grant/grant-gr_03"} {
		if ents[unwanted] != nil {
			t.Errorf("unexpected %s", unwanted)
		}
	}
	if a := ents["hallpass_agent/ci-runner"]; a != nil {
		if _, leaked := a.Metadata.Labels["api_token"]; leaked || a.Metadata.Labels["team"] != "platform" {
			t.Errorf("agent labels = %v", a.Metadata.Labels)
		}
	}
	if s := ents["hallpass_server/git-host"]; s != nil && s.Spec["address"] != nil {
		t.Errorf("redacted address stored: %v", s.Spec["address"])
	}
	rels := relations(t, env)
	for _, want := range []string{"has_access_to ci-runner -> web-1", "has_access_to claude-box -> git-host", "protected_by web-1 -> hallpass",
		"has_access_to grant-gr_01 -> web-1-deploy", "has_access_to grant-gr_02 -> git-host", "contains web-1 -> web-1-deploy",
		"runs_on ci-runner -> builder-1-ci-runner", "runs_on builder-1-ci-runner -> builder", "connects_to session-ses_01 -> web-1"} {
		if rels[want] == nil {
			t.Errorf("missing relation %s (have %v)", want, keys(rels))
		}
	}
	// The catalog server matched by address is protected by the HallPass server (inferred, with evidence).
	found := false
	for k, rel := range rels {
		if k == "protected_by web-1 -> web-1" && rel.Inferred {
			found = true
		}
	}
	if !found {
		t.Errorf("missing inferred protected_by from the catalog server (have %v)", keys(rels))
	}
	if hp.hits["/grants?cursor=c2&limit=200&state=active"] != 1 {
		t.Errorf("grant pagination not followed: %v", hp.hits)
	}
}

func TestPorchSync(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	pc := serveFixture(t, "porch", "/api/v1", "porch_at_test", nil)
	p := env.Project(t, "identity")
	api := env.Source(t, p.ID, "api", "")
	for name, fqdn := range map[string]string{"nas": "nas.example.com", "grafana": "grafana.example.com"} {
		if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "dns_record", Name: name, Spec: map[string]any{"fqdn": fqdn, "type": "A", "content": "192.0.2.20"}}, catalog.ApplyOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	src := newSource(t, env, p.ID, PorchType, map[string]any{"url": pc.URL}, "porch_at_test")
	r := runner(env)
	if diags, err := r.Test(ctx, src); err != nil || diags[0].Code != "ok" {
		t.Fatalf("test: %+v %v", diags, err)
	}
	run := syncOK(t, r, src)
	if run.Result != "ok" {
		t.Fatalf("run %s: %+v", run.Result, run.Diagnostics)
	}
	ents := entities(t, env)
	for _, want := range []string{"service/porch", "porch_app/home-grafana", "porch_app/home-nas", "porch_service_account/home-deed"} {
		if ents[want] == nil {
			t.Errorf("missing %s (have %v)", want, keys(ents))
		}
	}
	if ents["porch_app/home-old"] != nil {
		t.Error("disabled app imported")
	}
	if app := ents["porch_app/home-grafana"]; app != nil && app.Spec["client_id"] != "grafana-client" {
		t.Errorf("grafana spec = %+v", app.Spec)
	}
	for _, e := range ents {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "x9z") || strings.Contains(string(b), "secret_hint") {
			t.Fatalf("secret hint stored on %s", e.Metadata.Name)
		}
	}
	rels := relations(t, env)
	for _, want := range []string{"authenticates_with home-grafana -> porch", "authenticates_with home-deed -> porch",
		"protected_by nas -> home-nas", "authenticates_with grafana -> home-grafana"} {
		if rels[want] == nil {
			t.Errorf("missing relation %s (have %v)", want, keys(rels))
		}
	}
	if pc.hits["/tenants/lab/applications"] != 0 {
		t.Error("disabled tenant read")
	}
}
