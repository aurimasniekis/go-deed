package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func server(name, ip string, extra map[string]any) catalog.EntityItem {
	spec := map[string]any{"hostname": name + ".example.com", "ipv4": ip}
	for k, v := range extra {
		spec[k] = v
	}
	return catalog.EntityItem{Kind: "server", Name: name, Spec: spec}
}

func TestDeclareAndMergeLayers(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	tf := env.Source(t, p.ID, "terraform", "")
	imp := env.Source(t, p.ID, "tfstate", catalog.LayerImported)
	disc := env.Source(t, p.ID, "agent", catalog.LayerDiscovered)

	it := server("web-1", "198.51.100.10", nil)
	it.Metadata = map[string]any{"displayName": "Web 1", "labels": map[string]any{"tier": "web"}}
	res, err := env.Apply(t, tf, it, catalog.ApplyOpts{Strict: true, CreateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != catalog.ActionCreated {
		t.Fatalf("action %s", res.Action)
	}
	// The import disagrees on ipv4 and adds os; the declaration wins ipv4, the import contributes os.
	imported := server("web-1", "198.51.100.99", map[string]any{"os": "debian-13"})
	if _, err := env.Apply(t, imp, imported, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	e, err := env.Catalog.GetEntity(ctx, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Spec["ipv4"] != "198.51.100.10" || e.Spec["os"] != "debian-13" {
		t.Fatalf("effective spec %v", e.Spec)
	}
	if e.Metadata.DisplayName != "Web 1" || e.Metadata.Labels["tier"] != "web" {
		t.Fatalf("metadata %+v", e.Metadata)
	}
	prov, err := env.Catalog.Provenance(ctx, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if prov["/spec/ipv4"].Source != "terraform" || len(prov["/spec/ipv4"].Candidates) != 1 || prov["/spec/os"].Source != "tfstate" {
		t.Fatalf("provenance %+v", prov)
	}
	// Different layers never produce a conflict row; the losing value is a candidate.
	cs, _ := env.Catalog.Conflicts(ctx, res.EntityID)
	if len(cs) != 0 {
		t.Fatalf("unexpected conflicts %+v", cs)
	}
	// A discovered observation reports health without touching spec.
	obs := catalog.EntityItem{Ref: catalog.Ref{ID: res.EntityID}, Health: &catalog.HealthReport{State: "warning", Reason: "disk 91%"},
		Observed: map[string]any{"version": "1.4.1"}}
	if _, err := env.Apply(t, disc, obs, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	e, _ = env.Catalog.GetEntity(ctx, res.EntityID)
	if e.Status.Health["state"] != "warning" || e.Metadata.Version.Observed != "1.4.1" || e.Status.Freshness.State != "fresh" {
		t.Fatalf("status %+v version %+v", e.Status, e.Metadata.Version)
	}
}

func TestSameLayerConflictIsDeterministic(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	a := env.Source(t, p.ID, "import-a", catalog.LayerImported)
	env.Clock.Advance(time.Second)
	b := env.Source(t, p.ID, "import-b", catalog.LayerImported)
	r1, err := env.Apply(t, a, server("db", "198.51.100.1", nil), catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	env.Clock.Advance(time.Second)
	if _, err := env.Apply(t, b, server("db", "198.51.100.2", nil), catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	e, _ := env.Catalog.GetEntity(ctx, r1.EntityID)
	if e.Spec["ipv4"] != "198.51.100.1" {
		t.Fatalf("earliest claim must win, got %v", e.Spec["ipv4"])
	}
	cs, _ := env.Catalog.Conflicts(ctx, r1.EntityID)
	if len(cs) != 1 || cs[0].Pointer != "/spec/ipv4" || cs[0].Winner != "import-a" || cs[0].Loser != "import-b" {
		t.Fatalf("conflicts %+v", cs)
	}
	// Agreement clears the conflict.
	if _, err := env.Apply(t, b, server("db", "198.51.100.1", nil), catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	cs, _ = env.Catalog.Conflicts(ctx, r1.EntityID)
	if len(cs) != 0 {
		t.Fatalf("conflict should clear: %+v", cs)
	}
}

func TestStaleRevisionsNeverResurrect(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	src := env.Source(t, p.ID, "agent", catalog.LayerDiscovered)
	it := server("app", "198.51.100.5", nil)
	it.Revision = 10
	r, err := env.Apply(t, src, it, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	del := catalog.EntityItem{Kind: "server", Name: "app", Delete: true, Revision: 12}
	if res, err := env.Apply(t, src, del, catalog.ApplyOpts{}); err != nil || res.Action != catalog.ActionDeleted {
		t.Fatalf("delete %+v %v", res, err)
	}
	e, _ := env.Catalog.GetEntity(ctx, r.EntityID)
	if e.Status.TombstonedAt == nil {
		t.Fatal("entity with no facets must be tombstoned")
	}
	late := server("app", "198.51.100.6", nil)
	late.Revision = 11 // delayed message from before the delete
	res, err := env.Apply(t, src, late, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != catalog.ActionStale {
		t.Fatalf("late upsert must be stale, got %s", res.Action)
	}
	e, _ = env.Catalog.GetEntity(ctx, r.EntityID)
	if e.Status.TombstonedAt == nil {
		t.Fatal("stale upsert resurrected a deleted entity")
	}
	// A newer revision legitimately revives it with the same ID.
	newer := server("app", "198.51.100.7", nil)
	newer.Revision = 13
	if res, err = env.Apply(t, src, newer, catalog.ApplyOpts{}); err != nil || res.EntityID != r.EntityID {
		t.Fatalf("revive %+v %v", res, err)
	}
	e, _ = env.Catalog.GetEntity(ctx, r.EntityID)
	if e.Status.TombstonedAt != nil || e.Spec["ipv4"] != "198.51.100.7" {
		t.Fatalf("revived entity %+v", e.Spec)
	}
}

func TestDeletingDeclarationKeepsObservations(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	tf := env.Source(t, p.ID, "terraform", "")
	disc := env.Source(t, p.ID, "agent", catalog.LayerDiscovered)
	r, err := env.Apply(t, tf, server("edge", "198.51.100.20", nil), catalog.ApplyOpts{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, disc, server("edge", "198.51.100.20", map[string]any{"os": "alpine"}), catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, tf, catalog.EntityItem{Ref: catalog.Ref{ID: r.EntityID}, Delete: true}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	e, err := env.Catalog.GetEntity(ctx, r.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status.TombstonedAt != nil || e.Status.Declared || e.Spec["os"] != "alpine" {
		t.Fatalf("observations must survive declaration removal: %+v %+v", e.Status, e.Spec)
	}
}

func TestStrictDeclarationValidatesSchema(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	_, err := env.Apply(t, api, catalog.EntityItem{Kind: "server", Name: "bad", Spec: map[string]any{"ipv4": "not-an-ip"}}, catalog.ApplyOpts{Strict: true})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeInvalid || len(ae.Fields) == 0 || ae.Fields[0].Field != "spec/ipv4" {
		t.Fatalf("expected spec validation error, got %#v", err)
	}
	// Non-strict sources are never rejected for schema problems: the entity is flagged instead.
	imp := env.Source(t, p.ID, "tfstate", catalog.LayerImported)
	res, err := env.Apply(t, imp, catalog.EntityItem{Kind: "server", Name: "bad", Spec: map[string]any{"ipv4": "not-an-ip"}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := env.Catalog.GetEntity(context.Background(), res.EntityID)
	if e.Status.Valid {
		t.Fatal("entity should be marked invalid")
	}
}

func TestSecretsAndLinksAreRejected(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	cases := []catalog.EntityItem{
		{Kind: "service", Name: "a", Spec: map[string]any{"db_password": "hunter2"}},
		{Kind: "service", Name: "b", Spec: map[string]any{"key": "-----BEGIN RSA PRIVATE KEY-----\nabc"}},
		{Kind: "service", Name: "c", Metadata: map[string]any{"links": []any{map[string]any{"type": "docs", "url": "javascript:alert(1)"}}}},
		{Kind: "service", Name: "d", Metadata: map[string]any{"links": []any{map[string]any{"type": "docs", "url": "http://example.com"}}}},
	}
	for i, c := range cases {
		if _, err := env.Apply(t, api, c, catalog.ApplyOpts{Strict: true}); apperr.CodeOf(err) != apperr.CodeInvalid {
			t.Errorf("case %d: expected validation error, got %v", i, err)
		}
	}
	ok := catalog.EntityItem{Kind: "service", Name: "e", Spec: map[string]any{"db_password": map[string]any{"secretRef": map[string]any{"store": "vault", "key": "kv/db"}}}}
	if _, err := env.Apply(t, api, ok, catalog.ApplyOpts{Strict: true}); err != nil {
		t.Fatalf("secret references are allowed: %v", err)
	}
}

func TestIdentityKeysConverge(t *testing.T) {
	env := testenv.New(t)
	p := env.Project(t, "home")
	imp := env.Source(t, p.ID, "tfstate", catalog.LayerImported)
	disc := env.Source(t, p.ID, "agent", catalog.LayerDiscovered)
	r1, err := env.Apply(t, imp, catalog.EntityItem{Kind: "repository", Name: "go-deed", ExternalID: "github_repository.deed",
		Spec: map[string]any{"url": "https://github.com/example/go-deed"}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// A different source names it differently but the identity key (url) matches: same entity.
	r2, err := env.Apply(t, disc, catalog.EntityItem{Kind: "repository", Name: "example-go-deed", ExternalID: "1234",
		Spec: map[string]any{"url": "https://github.com/example/go-deed", "default_branch": "main"}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.EntityID != r2.EntityID {
		t.Fatalf("identity keys should converge: %s vs %s", r1.EntityID, r2.EntityID)
	}
}

func TestRelationsEvidenceAndEndpoints(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	tail := env.Source(t, p.ID, "pulse-tail", catalog.LayerDiscovered)
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "service", Name: "porch"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, api, catalog.EntityItem{Kind: "nats_subject", Name: "evt.porch.tenancy.tenant.created",
		Spec: map[string]any{"subject": "evt.porch.tenancy.tenant.created"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	pub := catalog.RelationItem{Type: "publishes", From: catalog.Ref{Kind: "service", Name: "porch"},
		To: catalog.Ref{Kind: "nats_subject", Name: "evt.porch.tenancy.tenant.created"}, Evidence: map[string]any{"messages": 3}}
	res, err := env.Relate(t, tail, pub)
	if err != nil || res.Action != catalog.ActionCreated {
		t.Fatalf("%+v %v", res, err)
	}
	r, err := env.Catalog.GetRelation(ctx, res.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if r.EvidenceClass != catalog.EvidenceObserved || !r.Live || len(r.Assertions) != 1 {
		t.Fatalf("relation %+v", r)
	}
	// A declaration of the same edge upgrades the evidence class.
	if _, err := env.Relate(t, api, pub); err != nil {
		t.Fatal(err)
	}
	r, _ = env.Catalog.GetRelation(ctx, res.RelationID)
	if r.EvidenceClass != catalog.EvidenceDeclared || len(r.Assertions) != 2 {
		t.Fatalf("relation %+v", r)
	}
	// Endpoint constraints: publishes must target a subject or contract.
	bad := catalog.RelationItem{Type: "publishes", From: catalog.Ref{Kind: "nats_subject", Name: "evt.porch.tenancy.tenant.created"},
		To: catalog.Ref{Kind: "service", Name: "porch"}}
	if _, err := env.Relate(t, api, bad); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("endpoint constraint not enforced: %v", err)
	}
	// Missing targets are recorded, not fatal.
	miss := catalog.RelationItem{Type: "depends_on", From: catalog.Ref{Kind: "service", Name: "porch"}, To: catalog.Ref{Kind: "database", Name: "nope"}}
	if res, err := env.Relate(t, api, miss); err != nil || res.Action != catalog.ActionUnresolved {
		t.Fatalf("%+v %v", res, err)
	}
	un, _ := env.Catalog.Unresolved(ctx, nil, 10)
	if len(un) != 1 {
		t.Fatalf("unresolved %+v", un)
	}
	// Inferred relations must carry evidence.
	inf := catalog.RelationItem{Type: "depends_on", From: catalog.Ref{Kind: "service", Name: "porch"},
		To: catalog.Ref{Kind: "nats_subject", Name: "evt.porch.tenancy.tenant.created"}, Inferred: true}
	if _, err := env.Relate(t, api, inf); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("inferred without evidence accepted: %v", err)
	}
}

func TestCrossProjectNeedsBridge(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	home := env.Project(t, "home")
	parents := env.Project(t, "parents")
	a := env.Source(t, home.ID, "api", "")
	b := env.Source(t, parents.ID, "api", "")
	if _, err := env.Apply(t, a, catalog.EntityItem{Kind: "service", Name: "vpn"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, b, catalog.EntityItem{Kind: "server", Name: "nas"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	it := catalog.RelationItem{Type: "connects_to", From: catalog.Ref{Kind: "service", Name: "vpn"}, To: catalog.Ref{Project: "parents", Kind: "server", Name: "nas"}}
	if _, err := env.Relate(t, a, it); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("cross-project relation without bridge accepted: %v", err)
	}
	if _, err := env.Catalog.CreateBridge(ctx, audit.System, home.ID, parents.ID, []string{"connects_to"}, "site-to-site VPN"); err != nil {
		t.Fatal(err)
	}
	if res, err := env.Relate(t, a, it); err != nil || res.Action != catalog.ActionCreated {
		t.Fatalf("%+v %v", res, err)
	}
}
