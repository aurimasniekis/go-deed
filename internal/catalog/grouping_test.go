package catalog_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestKindGrouping(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	c := env.Catalog
	// Built-in groupings, and every relationship type they follow exists.
	for name, want := range map[string][]string{"location": {"located_in", "runs_on", "hosted_on"}, "region": {"located_in", "runs_on", "hosted_on"}, "label": {"labeled"},
		"group": {"owned_by", "member_of"}} {
		k := c.Kind(name)
		if k == nil || k.Grouping == nil || !slices.Equal(k.Grouping.RelationshipTypes, want) {
			t.Fatalf("built-in %s grouping: %+v", name, k)
		}
	}
	for _, k := range c.Kinds() {
		if k.Grouping == nil {
			continue
		}
		for _, rt := range k.Grouping.RelationshipTypes {
			if c.RelType(rt) == nil {
				t.Errorf("kind %s groups by unknown relationship type %s", k.Name, rt)
			}
		}
	}
	if !slices.Contains(c.RelType("located_in").ToKinds, "region") {
		t.Error("located_in does not allow regions")
	}

	// Custom kinds: validated, kept when omitted on update, removed with an empty list.
	if _, err := c.CreateKind(ctx, audit.System, catalog.KindInput{Name: "example.com/rack_row", Title: "Rack row",
		Grouping: &catalog.KindGrouping{RelationshipTypes: []string{"nope"}}}, nil); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("unknown relationship type accepted: %v", err)
	}
	k, err := c.CreateKind(ctx, audit.System, catalog.KindInput{Name: "example.com/rack_row", Title: "Rack row",
		Grouping: &catalog.KindGrouping{RelationshipTypes: []string{"located_in", "member_of", "located_in"}}}, nil)
	if err != nil || k.Grouping == nil || !slices.Equal(k.Grouping.RelationshipTypes, []string{"located_in", "member_of"}) {
		t.Fatalf("create with grouping: %+v %v", k, err)
	}
	k, err = c.UpdateKind(ctx, audit.System, k.Name, catalog.KindInput{Title: "Row"}, 0)
	if err != nil || k.Grouping == nil {
		t.Fatalf("update without grouping must keep it: %+v %v", k, err)
	}
	k, err = c.UpdateKind(ctx, audit.System, k.Name, catalog.KindInput{Title: "Row", Grouping: &catalog.KindGrouping{}}, 0)
	if err != nil || k.Grouping != nil {
		t.Fatalf("update with an empty grouping must remove it: %+v %v", k, err)
	}
}
