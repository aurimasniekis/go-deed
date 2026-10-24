package demo

import (
	"context"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestSeed(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	res, err := Seed(ctx, env.Catalog, env.Store, env.Clock.Now(), 5)
	if err != nil {
		t.Fatal(err)
	}
	page, err := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != res.Entities {
		t.Fatalf("entities: %d listed, %d seeded", len(page.Items), res.Entities)
	}
	for _, e := range page.Items {
		if e.Metadata.Labels["deed.dev/synthetic"] != "true" || !e.Status.Valid {
			t.Fatalf("%s/%s: labels %v valid %v %v", e.Kind, e.Metadata.Name, e.Metadata.Labels, e.Status.Valid, e.Status.Validation)
		}
	}
	un, _ := env.Catalog.Unresolved(ctx, nil, 10)
	if len(un) != 0 {
		t.Fatalf("unresolved relations: %+v", un)
	}
	rels, _ := env.Catalog.ListRelations(ctx, catalog.RelationFilter{Limit: 500})
	if len(rels.Items) != res.Relations {
		t.Fatalf("relations: %d listed, %d seeded", len(rels.Items), res.Relations)
	}
	p, _ := env.Catalog.Project(ctx, Project)
	if !p.Synthetic {
		t.Fatal("project not flagged synthetic")
	}
	// Idempotent.
	if _, err := Seed(ctx, env.Catalog, env.Store, env.Clock.Now(), 5); err != nil {
		t.Fatal(err)
	}
	page2, _ := env.Catalog.ListEntities(ctx, catalog.EntityFilter{Limit: 500})
	if len(page2.Items) != len(page.Items) {
		t.Fatal("re-seed changed the entity count")
	}
}
