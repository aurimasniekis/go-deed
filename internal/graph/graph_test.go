package graph_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func fixture(t *testing.T) (*testenv.Env, *graph.Engine, map[string]string) {
	env := testenv.New(t)
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	ids := map[string]string{}
	add := func(kind, name string) {
		r, err := env.Apply(t, api, catalog.EntityItem{Kind: kind, Name: name}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = r.EntityID
	}
	rel := func(typ, from, to string) {
		if _, err := env.Relate(t, api, catalog.RelationItem{Type: typ, From: catalog.Ref{ID: ids[from]}, To: catalog.Ref{ID: ids[to]}}); err != nil {
			t.Fatal(err)
		}
	}
	add("hypervisor", "esx1")
	add("vm", "vm1")
	add("container", "app")
	add("service", "api")
	add("database", "db")
	rel("runs_on", "vm1", "esx1")
	rel("runs_on", "app", "vm1")
	rel("depends_on", "api", "db")
	rel("runs_on", "api", "vm1")
	// A cycle: db depends on api (pathological but must not loop).
	rel("depends_on", "db", "api")
	add("dns_zone", "example-com")
	for i := range 60 {
		n := fmt.Sprintf("rec%d", i)
		add("dns_record", n)
		rel("contains", "example-com", n)
	}
	e := &graph.Engine{Store: env.Store, Catalog: env.Catalog, Clock: clock.System{}, MaxDepth: 10, MaxNodes: 500, ClusterAbove: 500, DegreeCollapse: 50}
	return env, e, ids
}

func TestNeighborhoodCollapsesFans(t *testing.T) {
	_, e, ids := fixture(t)
	res, err := e.Neighborhood(context.Background(), graph.Scope{}, graph.Query{Roots: []string{ids["example-com"]}, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 2 || res.Nodes[1].Cluster == nil || res.Nodes[1].Cluster.Count != 60 {
		t.Fatalf("fan not collapsed: %+v", res.Nodes)
	}
	exp, err := e.Expand(context.Background(), graph.Scope{}, res.Nodes[1].Cluster.Key, 500)
	if err != nil || len(exp.Nodes) != 61 {
		t.Fatalf("expand: %d %v", len(exp.Nodes), err)
	}
}

func TestImpactHandlesCyclesAndSemantics(t *testing.T) {
	_, e, ids := fixture(t)
	ctx := context.Background()
	res, err := e.Impact(ctx, graph.Scope{}, ids["esx1"], "downstream", 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range res.Nodes {
		got[n.Name] = true
	}
	// esx1 failing affects vm1 (runs_on), app and api (run on vm1) and db (api depends... no: db is a dependency of api, not dependent).
	for _, want := range []string{"vm1", "app", "api"} {
		if !got[want] {
			t.Fatalf("missing %s in impact %v", want, got)
		}
	}
	up, err := e.Impact(ctx, graph.Scope{}, ids["api"], "upstream", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !up.Cycles {
		t.Fatalf("cycle api→db→api not reported: %+v", up)
	}
}

func TestPathAndRedaction(t *testing.T) {
	env, e, ids := fixture(t)
	ctx := context.Background()
	res, err := e.Path(ctx, graph.Scope{}, ids["app"], ids["db"], 6, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 4 { // app → vm1 → api → db
		t.Fatalf("path %+v", res.Nodes)
	}
	// A caller who can read nothing gets not-found, not data.
	if _, err := e.Neighborhood(ctx, graph.Scope{Readable: []string{}}, graph.Query{Roots: []string{ids["app"]}}); err == nil {
		t.Fatal("unreadable root returned data")
	}
	other := env.Project(t, "other")
	_ = other
}

func TestOverview(t *testing.T) {
	_, e, _ := fixture(t)
	res, err := e.Overview(context.Background(), graph.Scope{}, nil, "kind", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 7 || len(res.Edges) == 0 {
		t.Fatalf("overview %+v", res)
	}
	// A group of one entity is that entity (nothing to expand), and bundles point at it.
	ids := map[string]bool{}
	singles := 0
	for _, n := range res.Nodes {
		ids[n.ID] = true
		if n.Cluster != nil && n.Cluster.Count < 2 {
			t.Errorf("single-member cluster %+v", n)
		}
		if n.Cluster == nil {
			singles++
			if !strings.HasPrefix(n.ID, "ent_") || n.Kind == "cluster" {
				t.Errorf("single group not shown as its entity: %+v", n)
			}
		}
	}
	if singles == 0 {
		t.Fatal("fixture has no single-entity kind")
	}
	for _, ed := range res.Edges {
		if !ids[ed.From] || !ids[ed.To] {
			t.Errorf("bundle %s %s → %s has an endpoint that is not a node", ed.ID, ed.From, ed.To)
		}
	}
	// Overview group keys expand into their members and the relationships among them.
	var key string
	for _, n := range res.Nodes {
		if n.Cluster != nil && n.Cluster.Value == "dns_record" {
			key = n.Cluster.Key
		}
	}
	g, err := e.Expand(context.Background(), graph.Scope{}, key, 500)
	if err != nil || len(g.Nodes) != 60 {
		t.Fatalf("expand %q: %v %d nodes", key, err, len(g.Nodes))
	}
	if _, err := e.Expand(context.Background(), graph.Scope{}, "color|red", 10); err == nil {
		t.Fatal("unknown group accepted")
	}
}

// Overview by a label key: one group per value (entities without the key in ""), bundles between groups, expansion.
func TestOverviewByLabel(t *testing.T) {
	env, e, _ := fixture(t)
	ctx := context.Background()
	pr, err := env.Catalog.Project(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	projectID := pr.ID
	if _, err := env.Catalog.CreateLabelRule(ctx, audit.System, projectID, catalog.LabelRuleInput{Name: "dns",
		Match: &catalog.LabelRuleMatch{Kinds: []string{"dns_*"}}, Labels: map[string]string{"group": "dns"}}); err != nil {
		t.Fatal(err)
	}
	res, err := e.Overview(ctx, graph.Scope{}, nil, "label:group", false)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for _, n := range res.Nodes {
		if n.Cluster != nil && n.Cluster.Value == "dns" {
			key = n.Cluster.Key
		}
	}
	if key != "label:group|dns" {
		t.Fatalf("label groups: %+v", res.Nodes)
	}
	ex, err := e.Expand(ctx, graph.Scope{}, key, 100)
	if err != nil || len(ex.Nodes) == 0 {
		t.Fatalf("expand: %+v %v", ex, err)
	}
	for _, n := range ex.Nodes {
		if !strings.HasPrefix(n.Kind, "dns_") {
			t.Errorf("non-dns member %s %s", n.Kind, n.Name)
		}
	}
	if _, err := e.Overview(ctx, graph.Scope{}, nil, `label:bad"key`, false); err == nil {
		t.Fatal("invalid label key accepted")
	}
}
