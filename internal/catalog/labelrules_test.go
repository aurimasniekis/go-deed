package catalog_test

import (
	"testing"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestLabelRules(t *testing.T) {
	env := testenv.New(t)
	ctx := t.Context()
	p := env.Project(t, "infra")
	dns := env.Source(t, p.ID, "tf-cloudflare-dns", catalog.LayerImported)
	gh := env.Source(t, p.ID, "github", catalog.LayerDiscovered)
	apply := func(src *catalog.Source, kind, name string, labels map[string]any) string {
		t.Helper()
		md := map[string]any{}
		if labels != nil {
			md["labels"] = labels
		}
		r, err := env.Apply(t, src, catalog.EntityItem{Kind: kind, Name: name, ExternalID: kind + "." + name, Metadata: md}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		return r.EntityID
	}
	record := apply(dns, "dns_record", "www.example.com", map[string]any{"terraform/type": "cloudflare_dns_record"})
	ruleset := apply(dns, "firewall_rule", "example.com-waf", map[string]any{"terraform/type": "cloudflare_ruleset"})
	repo := apply(gh, "repository", "example-org-app", nil)
	// A source's own label wins over a rule's.
	pinned := apply(gh, "release", "example-org-app-v1", map[string]any{"group": "releases"})
	labels := func(id string) map[string]string {
		t.Helper()
		e, err := env.Catalog.GetEntity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return e.Metadata.Labels
	}
	mk := func(name string, m catalog.LabelRuleMatch, l map[string]string) *catalog.LabelRule {
		t.Helper()
		r, err := env.Catalog.CreateLabelRule(ctx, audit.System, p.ID, catalog.LabelRuleInput{Name: name, Match: &m, Labels: l})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mk("dns", catalog.LabelRuleMatch{Kinds: []string{"dns_*"}}, map[string]string{"group": "dns"})
	cf := mk("cloudflare-rulesets", catalog.LabelRuleMatch{Sources: []string{"tf-cloudflare-*"}, Labels: map[string]string{"terraform/type": "cloudflare_ruleset"}},
		map[string]string{"group": "cloudflare"})
	mk("github", catalog.LabelRuleMatch{Sources: []string{"github"}}, map[string]string{"group": "github"})
	for id, want := range map[string]string{record: "dns", ruleset: "cloudflare", repo: "github", pinned: "releases"} {
		if got := labels(id)["group"]; got != want {
			t.Errorf("%s: group %q, want %q", id, got, want)
		}
	}
	if labels(record)["terraform/type"] != "cloudflare_dns_record" {
		t.Errorf("source labels lost: %v", labels(record))
	}
	prov, err := env.Catalog.Provenance(ctx, ruleset)
	if err != nil || prov["/metadata/labels/group"].SourceID != cf.ID || prov["/metadata/labels/group"].Layer != catalog.LayerRule {
		t.Fatalf("provenance: %+v %v", prov["/metadata/labels/group"], err)
	}
	// Changing a rule re-applies it; deleting it removes its labels.
	if _, err := env.Catalog.UpdateLabelRule(ctx, audit.System, p.ID, "cloudflare-rulesets", catalog.LabelRuleInput{Labels: map[string]string{"group": "edge"}}, cf.ResourceVersion); err != nil {
		t.Fatal(err)
	}
	if got := labels(ruleset)["group"]; got != "edge" {
		t.Fatalf("after update: %q", got)
	}
	if _, err := env.Catalog.UpdateLabelRule(ctx, audit.System, p.ID, "cloudflare-rulesets", catalog.LabelRuleInput{}, cf.ResourceVersion); err == nil {
		t.Fatal("stale version accepted")
	}
	if err := env.Catalog.DeleteLabelRule(ctx, audit.System, p.ID, "cloudflare-rulesets"); err != nil {
		t.Fatal(err)
	}
	if _, has := labels(ruleset)["group"]; has {
		t.Fatalf("label of a deleted rule kept: %v", labels(ruleset))
	}
	// A rule never keeps an entity alive: when its only source lets go, the entity is tombstoned.
	if _, err := env.Apply(t, gh, catalog.EntityItem{Kind: "repository", Name: "example-org-app", ExternalID: "repository.example-org-app", Delete: true},
		catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if e, err := env.Catalog.GetEntity(ctx, repo); err == nil && e.Status.TombstonedAt == nil {
		t.Fatalf("entity kept alive by a rule: %+v", e.Status)
	}
	// Validation.
	for _, bad := range []catalog.LabelRuleInput{
		{Name: "no-labels"},
		{Name: "bad-glob", Match: &catalog.LabelRuleMatch{Kinds: []string{"["}}, Labels: map[string]string{"group": "x"}},
		{Name: "Bad Name", Labels: map[string]string{"group": "x"}},
		{Name: "secret", Labels: map[string]string{"api_token": "x"}},
	} {
		if _, err := env.Catalog.CreateLabelRule(ctx, audit.System, p.ID, bad); err == nil {
			t.Errorf("%s accepted", bad.Name)
		}
	}
	if _, err := env.Catalog.CreateLabelRule(ctx, audit.System, p.ID, catalog.LabelRuleInput{Name: "dns", Labels: map[string]string{"group": "x"}}); err == nil {
		t.Error("duplicate name accepted")
	}
}
