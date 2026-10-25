package catalog_test

import (
	"context"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// Relationships to entities that do not exist yet are kept and applied, with their attributes, when the entity
// appears — whichever source creates it.
func TestUnresolvedRelationsResolveWhenTargetsAppear(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	agent, err := env.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: p.ID, Name: "agent", Provider: "push", Layer: catalog.LayerDiscovered}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := env.Apply(t, api, catalog.EntityItem{Kind: "service", Name: "app"}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.Relate(t, api, catalog.RelationItem{Type: "depends_on", From: catalog.Ref{ID: app.EntityID}, To: catalog.Ref{Kind: "database", Name: "pg"},
		Attributes: map[string]any{"port": 5432}})
	if err != nil || res.Action != catalog.ActionUnresolved {
		t.Fatalf("first relation: %+v %v", res, err)
	}
	res, err = env.Relate(t, agent, catalog.RelationItem{Type: "runs_on", From: catalog.Ref{ExternalID: "c1"}, To: catalog.Ref{Kind: "server", Name: "host"}})
	if err != nil || res.Action != catalog.ActionUnresolved {
		t.Fatalf("second relation: %+v %v", res, err)
	}
	if un, _ := env.Catalog.Unresolved(ctx, nil, 10); len(un) != 2 {
		t.Fatalf("unresolved: %+v", un)
	}
	// Another source creates the database: the API source's relationship applies with its attributes.
	if _, err := env.Apply(t, agent, catalog.EntityItem{Kind: "database", Name: "pg"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	rels, _ := env.Catalog.ListRelations(ctx, catalog.RelationFilter{Types: []string{"depends_on"}, Limit: 10})
	if rels.Total != 1 || rels.Items[0].Attributes["port"] != float64(5432) {
		t.Fatalf("depends_on after the target appeared: %+v", rels.Items)
	}
	// The agent's own external ID resolves when it reports the container, the server still missing keeps it pending.
	if _, err := env.Apply(t, agent, catalog.EntityItem{Kind: "container", Name: "c1", ExternalID: "c1"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if un, _ := env.Catalog.Unresolved(ctx, nil, 10); len(un) != 1 {
		t.Fatalf("still unresolved: %+v", un)
	}
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "server", Name: "host"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if un, _ := env.Catalog.Unresolved(ctx, nil, 10); len(un) != 0 {
		t.Fatalf("unresolved after both endpoints exist: %+v", un)
	}
	rels, _ = env.Catalog.ListRelations(ctx, catalog.RelationFilter{Types: []string{"runs_on"}, Limit: 10})
	if rels.Total != 1 || rels.Items[0].From.Name != "c1" || rels.Items[0].To.Name != "host" {
		t.Fatalf("runs_on: %+v", rels.Items)
	}
}

func TestCardinalityOne(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	if _, err := env.Catalog.CreateRelType(ctx, audit.System, catalog.RelTypeInput{Name: "example.com/plugged_into", Title: "Plugged into", Cardinality: "one"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, n := range []string{"nas", "ups-a", "ups-b"} {
		r, err := env.Apply(t, api, catalog.EntityItem{Kind: "server", Name: n}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = r.EntityID
	}
	rel := func(to string) error {
		_, err := env.Relate(t, api, catalog.RelationItem{Type: "example.com/plugged_into", From: catalog.Ref{ID: ids["nas"]}, To: catalog.Ref{ID: ids[to]}})
		return err
	}
	if err := rel("ups-a"); err != nil {
		t.Fatal(err)
	}
	if err := rel("ups-a"); err != nil {
		t.Fatalf("re-asserting the same target: %v", err)
	}
	if err := rel("ups-b"); err == nil {
		t.Fatal("a second target was accepted for a cardinality-one type")
	}
}
