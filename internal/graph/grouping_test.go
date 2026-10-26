package graph_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/graph"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestOverviewByGroupingKind(t *testing.T) {
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
	add("region", "eu")
	add("location", "dc1")
	add("location", "rack1")
	for _, s := range []string{"s1", "s2", "s3"} {
		add("server", s)
	}
	add("vm", "vm1")
	add("vm", "vm2")
	add("label", "critical")
	rel("located_in", "dc1", "eu")
	rel("located_in", "rack1", "dc1")
	rel("located_in", "s1", "rack1")
	rel("located_in", "s2", "rack1")
	rel("located_in", "s3", "dc1")
	rel("runs_on", "vm1", "s1")
	rel("runs_on", "vm2", "s3")
	rel("labeled", "vm1", "critical")
	rel("labeled", "vm2", "critical")
	e := &graph.Engine{Store: env.Store, Catalog: env.Catalog, Clock: clock.System{}, MaxDepth: 10, MaxNodes: 500, ClusterAbove: 500, DegreeCollapse: 50}
	ctx := context.Background()

	groups := func(groupBy string) map[string]int {
		t.Helper()
		res, err := e.Overview(ctx, graph.Scope{}, nil, groupBy, false)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		nodes := map[string]bool{}
		for _, n := range res.Nodes {
			nodes[n.ID] = true
			if n.Cluster != nil {
				out[n.Name] = n.Cluster.Count
			} else {
				out["="+n.Name] = 1
			}
		}
		for _, ed := range res.Edges {
			if !nodes[ed.From] || !nodes[ed.To] {
				t.Errorf("%s: edge %s → %s has an endpoint that is not a node", groupBy, ed.From, ed.To)
			}
		}
		return out
	}
	// By location the nearest location wins: the rack holds itself, two servers and the VM on s1; the site itself, s3 and
	// the VM on it.
	if got := groups("location"); !mapsEqual(got, map[string]int{"rack1": 4, "dc1": 3, "(none)": 2}) {
		t.Errorf("by location: %v", got)
	}
	// By region, everything placed is in eu through the chain; the only unplaced entity is shown as itself.
	if got := groups("region"); !mapsEqual(got, map[string]int{"eu": 8, "=critical": 1}) {
		t.Errorf("by region: %v", got)
	}
	// Labels: the label and the two VMs that carry it.
	if got := groups("label"); got["critical"] != 3 {
		t.Errorf("by label: %v", got)
	}
	if _, err := e.Overview(ctx, graph.Scope{}, nil, "server", false); err == nil {
		t.Error("a kind without a grouping was accepted")
	}

	// Expanding a group lists its members.
	g, err := e.Expand(ctx, graph.Scope{}, "location|"+ids["rack1"], 100)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range g.Nodes {
		names = append(names, n.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"rack1", "s1", "s2", "vm1"}) {
		t.Errorf("expand rack1: %v", names)
	}
	// The members stay connected to the groups still collapsed: vm1 is labeled critical, which is in no location.
	var toNone bool
	for _, ed := range g.Edges {
		if ed.From == ids["vm1"] && ed.To == "group:location:" && ed.Type == "labeled" {
			toNone = true
		}
	}
	if !toNone {
		t.Errorf("expand rack1 edges: %+v", g.Edges)
	}
	none, err := e.Expand(ctx, graph.Scope{}, "location|", 100)
	if err != nil || len(none.Nodes) != 2 {
		t.Errorf("expand the unplaced: %v %d", err, len(none.Nodes))
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
