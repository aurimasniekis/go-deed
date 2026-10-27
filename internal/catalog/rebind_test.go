package catalog_test

import (
	"testing"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
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

// Resources of different states that collided on one name (three origin certificates all named after their resource,
// "origin") come apart when the mapping names them after their hosts; the last state renames the old entity instead
// of leaving it empty. A user rename (the importer's name is now an alias) is left alone.
func TestNameChangeRebindsExternalID(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "infra")
	hosts := []string{"porch.example.com", "hallpass.example.com", "events.example.com"}
	srcs := make([]*catalog.Source, len(hosts))
	var merged string
	for i := range hosts {
		srcs[i] = env.Source(t, p.ID, "tf-host-"+string(rune('a'+i)), catalog.LayerImported)
		r, err := env.Apply(t, srcs[i], catalog.EntityItem{Kind: "certificate", Name: "origin-origin", ExternalID: "cloudflare_origin_ca_certificate.origin",
			Spec: map[string]any{"common_name": hosts[i]}}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && r.EntityID != merged {
			t.Fatalf("setup: not merged")
		}
		merged = r.EntityID
	}
	seen := map[string]bool{}
	for i, h := range hosts {
		r, err := env.Apply(t, srcs[i], catalog.EntityItem{Kind: "certificate", Name: "origin-" + h, ExternalID: "cloudflare_origin_ca_certificate.origin",
			Spec: map[string]any{"common_name": h}}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		seen[r.EntityID] = true
		last := i == len(hosts)-1
		if last != (r.EntityID == merged) {
			t.Fatalf("%s: entity %s (merged %s, last %v)", h, r.EntityID, merged, last)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("certificates: %d entities", len(seen))
	}
	var name string
	var facets int
	if err := env.Store.R().QueryRow(`SELECT name, (SELECT COUNT(*) FROM facets WHERE entity_id = entities.id AND tombstoned_at IS NULL)
		FROM entities WHERE id = ?`, merged).Scan(&name, &facets); err != nil {
		t.Fatal(err)
	}
	if name != "origin-events.example.com" || facets != 1 {
		t.Fatalf("old entity: %s with %d facets", name, facets)
	}
	// A user renames the entity: the importer's name is now an alias and is not "changed".
	if err := env.Catalog.Rename(t.Context(), audit.System, merged, "events-origin"); err != nil {
		t.Fatal(err)
	}
	r, err := env.Apply(t, srcs[2], catalog.EntityItem{Kind: "certificate", Name: "origin-events.example.com", ExternalID: "cloudflare_origin_ca_certificate.origin",
		Spec: map[string]any{"common_name": "events.example.com"}}, catalog.ApplyOpts{})
	if err != nil || r.EntityID != merged {
		t.Fatalf("after user rename: %+v %v", r, err)
	}
	if err := env.Store.R().QueryRow(`SELECT name FROM entities WHERE id = ?`, merged).Scan(&name); err != nil || name != "events-origin" {
		t.Fatalf("user rename undone: %s %v", name, err)
	}
}

// Conflicts recorded under older merge rules (here: two states' Terraform addresses) disappear once at start-up,
// without waiting for each entity's facts to change; the second run is a no-op.
func TestRemergeClearsConflictsOfOldRules(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "infra")
	a := env.Source(t, p.ID, "tf-events", catalog.LayerImported)
	b := env.Source(t, p.ID, "tf-dns", catalog.LayerImported)
	var id string
	for _, s := range []*catalog.Source{a, b} {
		r, err := env.Apply(t, s, catalog.EntityItem{Kind: "dns_zone", Name: "example.com", Metadata: map[string]any{
			"annotations": map[string]any{"terraform/address": s.Name + ".zone"}}, Spec: map[string]any{"name": "example.com"}}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		id = r.EntityID
	}
	if err := env.Store.W().Tx(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO conflicts (entity_id, pointer, layer, winner_source, loser_source, winner_value, loser_value,
			first_seen_at, last_seen_at) VALUES (?, '/metadata/annotations/terraform~1address', 'imported', ?, ?, '"a"', '"b"', 1, 1)`, id, a.ID, b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n, err := env.Catalog.RemergeIfRulesChanged(t.Context())
	if err != nil || n == 0 {
		t.Fatalf("remerge: %d %v", n, err)
	}
	var left int
	if err := env.Store.R().QueryRow(`SELECT COUNT(*) FROM conflicts WHERE entity_id = ?`, id).Scan(&left); err != nil || left != 0 {
		t.Fatalf("conflicts left: %d %v", left, err)
	}
	if n, err := env.Catalog.RemergeIfRulesChanged(t.Context()); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
}
