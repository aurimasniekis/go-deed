package graph_test

import (
	"context"
	"fmt"
	"testing"

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
	res, err := e.Overview(context.Background(), graph.Scope{}, nil, "kind")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 7 || len(res.Edges) == 0 {
		t.Fatalf("overview %+v", res)
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
