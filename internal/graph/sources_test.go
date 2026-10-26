package graph_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func sourceNames(e graph.Edge) []string {
	var out []string
	for _, s := range e.Sources {
		out = append(out, s.Name)
	}
	return out
}

func edgeByType(t *testing.T, res *graph.Result, from, typ string) graph.Edge {
	t.Helper()
	for _, e := range res.Edges {
		if e.From == from && e.Type == typ {
			return e
		}
	}
	t.Fatalf("no %s edge from %s in %+v", typ, from, res.Edges)
	return graph.Edge{}
}

// Edges list their asserting sources by ADR-003 precedence: layer priority (or the source policy's priority), then the
// oldest first claim. Retracted and expired assertions are not listed.
func TestEdgeSourcesAndPrimary(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	tf := env.Source(t, p.ID, "terraform", "")
	netbox := env.Source(t, p.ID, "netbox", catalog.LayerImported)
	prio := 90
	cmdb, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "cmdb", DisplayName: "Company CMDB",
		Provider: "push", Layer: catalog.LayerImported, Policy: &catalog.Policy{Priority: &prio}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	flows := env.Source(t, p.ID, "flows", catalog.LayerObservation)
	ids := map[string]string{}
	for _, n := range []struct{ kind, name string }{{"service", "app"}, {"vm", "vm1"}, {"database", "db"}, {"storage", "q"}, {"service", "cache"}} {
		r, err := env.Apply(t, api, catalog.EntityItem{Kind: n.kind, Name: n.name}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		ids[n.name] = r.EntityID
	}
	rel := func(src *catalog.Source, typ, from, to string, mod func(*catalog.RelationItem)) {
		t.Helper()
		it := catalog.RelationItem{Type: typ, From: catalog.Ref{ID: ids[from]}, To: catalog.Ref{ID: ids[to]}}
		if mod != nil {
			mod(&it)
		}
		if _, err := env.Relate(t, src, it); err != nil {
			t.Fatal(err)
		}
		env.Clock.Advance(time.Minute)
	}
	// runs_on: netbox (imported, 60) claims first, api (declared, 80) later: the declared layer wins.
	rel(netbox, "runs_on", "app", "vm1", nil)
	rel(api, "runs_on", "app", "vm1", nil)
	// depends_on: terraform then api, both declared: the oldest first claim wins.
	rel(tf, "depends_on", "app", "db", nil)
	rel(api, "depends_on", "app", "db", nil)
	// stored_in: api (80) then cmdb (imported, policy priority 90): the policy priority wins; netbox's claim is retracted.
	rel(api, "stored_in", "app", "q", nil)
	rel(netbox, "stored_in", "app", "q", nil)
	rel(cmdb, "stored_in", "app", "q", nil)
	rel(netbox, "stored_in", "app", "q", func(it *catalog.RelationItem) { it.Delete = true })
	// connects_to: flows' observation expires, api's declaration stays.
	exp := env.Clock.Now().Add(time.Hour)
	rel(flows, "connects_to", "app", "cache", func(it *catalog.RelationItem) { it.ExpiresAt = &exp })
	rel(api, "connects_to", "app", "cache", nil)

	e := &graph.Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock, MaxDepth: 10, MaxNodes: 500, ClusterAbove: 500, DegreeCollapse: 50}
	res, err := e.Neighborhood(ctx, graph.Scope{}, graph.Query{Roots: []string{ids["app"]}, Depth: 1, IncludeStale: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ     string
		want    []string
		primary string
	}{
		{"runs_on", []string{"api", "netbox"}, api.ID},
		{"depends_on", []string{"terraform", "api"}, tf.ID},
		{"stored_in", []string{"cmdb", "api"}, cmdb.ID},
		{"connects_to", []string{"api", "flows"}, api.ID},
	} {
		ed := edgeByType(t, res, ids["app"], tc.typ)
		if got := sourceNames(ed); !equal(got, tc.want) || ed.PrimarySource != tc.primary {
			t.Errorf("%s: sources %v primary %s, want %v primary %s", tc.typ, got, ed.PrimarySource, tc.want, tc.primary)
		}
	}
	st := edgeByType(t, res, ids["app"], "stored_in")
	if st.Sources[0].DisplayName != "Company CMDB" || st.Sources[0].Layer != "imported" || st.Sources[0].ProjectID != p.ID {
		t.Fatalf("source details: %+v", st.Sources[0])
	}

	// Once flows' observation expires, only the declaration is listed.
	env.Clock.Advance(2 * time.Hour)
	res, err = e.Neighborhood(ctx, graph.Scope{}, graph.Query{Roots: []string{ids["app"]}, Depth: 1, IncludeStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceNames(edgeByType(t, res, ids["app"], "connects_to")); !equal(got, []string{"api"}) {
		t.Fatalf("expired assertion listed: %v", got)
	}
	// Path and impact answers carry sources too.
	path, err := e.Path(ctx, graph.Scope{}, ids["app"], ids["db"], 3, false, nil)
	if err != nil || len(path.Edges) != 1 || path.Edges[0].PrimarySource != tf.ID {
		t.Fatalf("path sources: %+v %v", path.Edges, err)
	}
	imp, err := e.Impact(ctx, graph.Scope{}, ids["db"], "downstream", 2)
	if err != nil || len(imp.Edges) == 0 || len(imp.Edges[0].Sources) == 0 {
		t.Fatalf("impact sources: %+v %v", imp.Edges, err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A relationship into a project the caller cannot read ends at a redacted stub; the edge lists only the sources of
// projects the caller can read, never the other project's source names.
func TestEdgeSourcesRespectProjectScope(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	home := env.Project(t, "home")
	parents := env.Project(t, "parents")
	a := env.Source(t, home.ID, "api", "")
	scanner := env.Source(t, parents.ID, "lan-scanner", catalog.LayerDiscovered)
	vpn, err := env.Apply(t, a, catalog.EntityItem{Kind: "service", Name: "vpn"}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, env.Source(t, parents.ID, "api", ""), catalog.EntityItem{Kind: "server", Name: "nas"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Catalog.CreateBridge(ctx, audit.System, home.ID, parents.ID, []string{"connects_to"}, "site-to-site VPN"); err != nil {
		t.Fatal(err)
	}
	it := catalog.RelationItem{Type: "connects_to", From: catalog.Ref{Project: "home", Kind: "service", Name: "vpn"},
		To: catalog.Ref{Project: "parents", Kind: "server", Name: "nas"}}
	for _, src := range []*catalog.Source{scanner, a} {
		if r, err := env.Relate(t, src, it); err != nil || (r.Action != catalog.ActionCreated && r.Action != catalog.ActionUpdated) {
			t.Fatalf("%s: %+v %v", src.Name, r, err)
		}
	}
	e := &graph.Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock, MaxDepth: 10, MaxNodes: 500, ClusterAbove: 500, DegreeCollapse: 50}
	q := graph.Query{Roots: []string{vpn.EntityID}, Depth: 1}
	all, err := e.Neighborhood(ctx, graph.Scope{}, q)
	if err != nil || len(all.Edges) != 1 {
		t.Fatalf("%+v %v", all, err)
	}
	if got := sourceNames(all.Edges[0]); !equal(got, []string{"api", "lan-scanner"}) {
		t.Fatalf("full scope sources %v", got)
	}
	homeOnly, err := e.Neighborhood(ctx, graph.Scope{Readable: []string{home.ID}}, q)
	if err != nil || len(homeOnly.Edges) != 1 {
		t.Fatalf("%+v %v", homeOnly, err)
	}
	// The other end is a redacted stub (never the real entity).
	ed := homeOnly.Edges[0]
	if !ed.CrossProject || !strings.HasPrefix(ed.To, "redacted:") || len(homeOnly.Nodes) != 2 || !homeOnly.Nodes[1].Redacted {
		t.Fatalf("not a redacted cross-project edge: %+v %+v", ed, homeOnly.Nodes)
	}
	if got := sourceNames(ed); !equal(got, []string{"api"}) || ed.PrimarySource != a.ID {
		t.Fatalf("redacted edge leaks sources: %v primary %s", got, ed.PrimarySource)
	}
	for _, s := range ed.Sources {
		if s.ProjectID != home.ID {
			t.Fatalf("source of an unreadable project: %+v", s)
		}
	}
	// The overview's bundles follow the same rule.
	over, err := e.Overview(ctx, graph.Scope{Readable: []string{home.ID}}, nil, "kind", true)
	if err != nil || len(over.Edges) != 1 {
		t.Fatalf("overview %+v %v", over, err)
	}
	if got := sourceNames(over.Edges[0]); !equal(got, []string{"api"}) || over.Edges[0].Sources[0].ProjectID != home.ID {
		t.Fatalf("overview leaks sources: %+v", over.Edges[0].Sources)
	}

	// A stub reached twice is listed once, and every edge to it uses the stub's ID.
	router, err := env.Apply(t, a, catalog.EntityItem{Kind: "network", Name: "router"}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []catalog.RelationItem{
		{Type: "connects_to", From: catalog.Ref{ID: vpn.EntityID}, To: catalog.Ref{ID: router.EntityID}},
		{Type: "connects_to", From: catalog.Ref{ID: router.EntityID}, To: catalog.Ref{Project: "parents", Kind: "server", Name: "nas"}},
	} {
		if _, err := env.Relate(t, a, it); err != nil {
			t.Fatal(err)
		}
	}
	deep, err := e.Neighborhood(ctx, graph.Scope{Readable: []string{home.ID}}, graph.Query{Roots: []string{vpn.EntityID}, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	stubs := 0
	for _, n := range deep.Nodes {
		if n.Redacted {
			stubs++
		}
	}
	if len(deep.Nodes) != 3 || stubs != 1 || len(deep.Edges) != 3 {
		t.Fatalf("stub reached twice: %+v %+v", deep.Nodes, deep.Edges)
	}
	for _, ed := range deep.Edges {
		for _, end := range []string{ed.From, ed.To} {
			if end != vpn.EntityID && end != router.EntityID && !strings.HasPrefix(end, "redacted:") {
				t.Fatalf("edge leaks an unreadable entity ID: %+v", ed)
			}
		}
	}
}

// Collapsed fans and overview bundles list the union of their relationships' sources with counts, and name a primary
// only when it is the primary of every bundled relationship.
func TestBundleSources(t *testing.T) {
	env, e, ids := fixture(t)
	e.Clock = env.Clock
	ctx := context.Background()
	res, err := e.Neighborhood(ctx, graph.Scope{}, graph.Query{Roots: []string{ids["example-com"]}, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edges) != 1 || res.Edges[0].Count != 60 {
		t.Fatalf("bundle: %+v", res.Edges)
	}
	b := res.Edges[0]
	if len(b.Sources) != 1 || b.Sources[0].Name != "api" || b.Sources[0].Relations != 60 || b.PrimarySource != b.Sources[0].ID {
		t.Fatalf("bundle sources: %+v primary %q", b.Sources, b.PrimarySource)
	}
	// A second, stronger source asserting one of the records makes the bundle's primary mixed.
	p, err := env.Catalog.Project(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	ov := env.Source(t, p.ID, "overrides", "")
	if _, err := env.Relate(t, ov, catalog.RelationItem{Type: "contains", From: catalog.Ref{ID: ids["example-com"]}, To: catalog.Ref{ID: ids["rec7"]}}); err != nil {
		t.Fatal(err)
	}
	res, err = e.Neighborhood(ctx, graph.Scope{}, graph.Query{Roots: []string{ids["example-com"]}, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	b = res.Edges[0]
	if got := sourceNames(b); !equal(got, []string{"overrides", "api"}) || b.PrimarySource != "" || b.Sources[0].Relations != 1 {
		t.Fatalf("mixed bundle: %+v primary %q", b.Sources, b.PrimarySource)
	}
	over, err := e.Overview(ctx, graph.Scope{}, nil, "kind", true)
	if err != nil {
		t.Fatal(err)
	}
	// The only VM is shown as itself, not as a group of one.
	vm := ""
	for _, n := range over.Nodes {
		if n.Kind == "vm" {
			vm = n.ID
		}
	}
	var contains, runsOn *graph.Edge
	for i, ed := range over.Edges {
		switch {
		case ed.Type == "contains":
			contains = &over.Edges[i]
		case ed.Type == "runs_on" && ed.From == vm:
			runsOn = &over.Edges[i]
		}
	}
	if contains == nil || runsOn == nil {
		t.Fatalf("overview edges %+v", over.Edges)
	}
	// overrides outranks api but asserts only one of the 60 records: no single primary.
	if got := sourceNames(*contains); !equal(got, []string{"overrides", "api"}) || contains.PrimarySource != "" || contains.Sources[1].Relations != 60 {
		t.Fatalf("overview contains: %+v primary %q", contains.Sources, contains.PrimarySource)
	}
	if len(runsOn.Sources) != 1 || runsOn.PrimarySource != runsOn.Sources[0].ID {
		t.Fatalf("overview runs_on: %+v primary %q", runsOn.Sources, runsOn.PrimarySource)
	}
}
