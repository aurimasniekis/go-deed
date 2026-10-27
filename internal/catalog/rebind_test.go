package catalog_test

import (
	"testing"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// An importer whose mapping changes a resource's kind (googleworkspace_domain: domain → service) must not stay bound
// to the old entity: its facet there is withdrawn and the item lands on an entity of the new kind.
func TestKindChangeRebindsExternalID(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "infra")
	hub := env.Source(t, p.ID, "hub", catalog.LayerDeclared)
	imp := env.Source(t, p.ID, "tf-google", catalog.LayerImported)
	first, err := env.Apply(t, imp, catalog.EntityItem{Kind: "domain", Name: "example.com", ExternalID: "googleworkspace_domain.primary",
		Spec: map[string]any{"fqdn": "example.com", "verified": true}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// The hand-declared hub domain adopts the same entity.
	declared, err := env.Apply(t, hub, catalog.EntityItem{Kind: "domain", Name: "example.com", Spec: map[string]any{"fqdn": "example.com"}}, catalog.ApplyOpts{})
	if err != nil || declared.EntityID != first.EntityID {
		t.Fatalf("hub: %+v %v", declared, err)
	}
	second, err := env.Apply(t, imp, catalog.EntityItem{Kind: "service", Name: "google-workspace-example.com", ExternalID: "googleworkspace_domain.primary",
		Spec: map[string]any{"type": "google_workspace", "domain": "example.com"}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatalf("kind change: %v", err)
	}
	if second.EntityID == first.EntityID || second.Action != catalog.ActionCreated {
		t.Fatalf("not rebound: %+v", second)
	}
	// The domain keeps the hub's declaration, without the importer's facet; the relationship between them now works.
	var facets, importer int
	if err := env.Store.R().QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE source_id = ?) FROM facets WHERE entity_id = ? AND tombstoned_at IS NULL`,
		imp.ID, first.EntityID).Scan(&facets, &importer); err != nil {
		t.Fatal(err)
	}
	if facets != 1 || importer != 0 {
		t.Fatalf("domain facets: %d live, %d from the importer", facets, importer)
	}
	if _, err := env.Relate(t, imp, catalog.RelationItem{Type: "contains", From: catalog.Ref{ID: first.EntityID},
		To: catalog.Ref{ExternalID: "googleworkspace_domain.primary"}}); err != nil {
		t.Fatalf("domain contains service: %v", err)
	}
	// Re-applying the new kind is a no-op on the new entity.
	again, err := env.Apply(t, imp, catalog.EntityItem{Kind: "service", Name: "google-workspace-example.com", ExternalID: "googleworkspace_domain.primary",
		Spec: map[string]any{"type": "google_workspace", "domain": "example.com"}}, catalog.ApplyOpts{})
	if err != nil || again.EntityID != second.EntityID || again.Action == catalog.ActionCreated {
		t.Fatalf("re-apply: %+v %v", again, err)
	}
}
