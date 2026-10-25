package findings

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func open(t *testing.T, env *testenv.Env) map[string]Finding {
	t.Helper()
	rows, err := env.Store.R().Query(`SELECT dedup_key, type, severity, class, COALESCE(entity_id, '') FROM findings WHERE state = 'open'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]Finding{}
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.DedupKey, &f.Type, &f.Severity, &f.Class, &f.EntityID); err != nil {
			t.Fatal(err)
		}
		out[f.DedupKey] = f
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func byType(fs map[string]Finding, typ string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func TestEvaluateCertificatesOwnersDriftAndClosing(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	now := env.Clock.Now()
	apply := func(src *catalog.Source, it catalog.EntityItem) string {
		r, err := env.Apply(t, src, it, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		return r.EntityID
	}
	soon := apply(api, catalog.EntityItem{Kind: "certificate", Name: "soon", Spec: map[string]any{"not_after": now.Add(3 * 24 * time.Hour).Format(time.RFC3339)}})
	later := apply(api, catalog.EntityItem{Kind: "certificate", Name: "later", Spec: map[string]any{"not_after": now.Add(20 * 24 * time.Hour).Format(time.RFC3339)}})
	fine := apply(api, catalog.EntityItem{Kind: "certificate", Name: "fine", Spec: map[string]any{"not_after": now.Add(200 * 24 * time.Hour).Format(time.RFC3339)}})
	expired := apply(api, catalog.EntityItem{Kind: "certificate", Name: "expired", Spec: map[string]any{"not_after": now.Add(-time.Hour).Format(time.RFC3339)}})
	orphan := apply(api, catalog.EntityItem{Kind: "service", Name: "orphan"})
	apply(api, catalog.EntityItem{Kind: "group", Name: "platform"})
	owned := apply(api, catalog.EntityItem{Kind: "service", Name: "owned", Metadata: map[string]any{"owner": "platform"}})
	// Drift: the declaration says one OS, an importer reports another.
	imp, err := env.Catalog.CreateSource(ctx, auditSystem(), catalog.SourceInput{ProjectID: p.ID, Name: "importer", Provider: "push", Layer: catalog.LayerImported}, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := apply(api, catalog.EntityItem{Kind: "server", Name: "host", Metadata: map[string]any{"owner": "platform"}, Spec: map[string]any{"os": "Debian 12"}})
	apply(imp, catalog.EntityItem{Kind: "server", Name: "host", Spec: map[string]any{"os": "Debian 11"}})
	// A relationship to an entity that does not exist yet.
	if _, err := env.Relate(t, api, catalog.RelationItem{Type: "depends_on", From: catalog.Ref{ID: owned}, To: catalog.Ref{Kind: "database", Name: "missing"}}); err != nil {
		t.Fatal(err)
	}

	e := &Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock}
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	fs := open(t, env)
	sev := map[string]string{}
	for _, f := range append(byType(fs, TypeCertExpiring), byType(fs, TypeCertExpired)...) {
		sev[f.EntityID] = f.Type + "/" + f.Severity
	}
	if sev[soon] != TypeCertExpiring+"/"+Critical || sev[later] != TypeCertExpiring+"/"+Warning || sev[expired] != TypeCertExpired+"/"+Critical || sev[fine] != "" {
		t.Fatalf("certificate findings: %v", sev)
	}
	missing := map[string]bool{}
	for _, f := range byType(fs, TypeMissingOwner) {
		missing[f.EntityID] = true
	}
	if !missing[orphan] || missing[owned] || missing[host] {
		t.Fatalf("missing owner: %v", missing)
	}
	drift := byType(fs, TypeDrift)
	if len(drift) != 1 || drift[0].EntityID != host {
		t.Fatalf("drift: %+v", drift)
	}
	if len(byType(fs, TypeMissingTarget)) != 1 {
		t.Fatalf("missing target: %+v", byType(fs, TypeMissingTarget))
	}

	// Fixing the conditions closes the findings on the next evaluation.
	apply(api, catalog.EntityItem{Kind: "certificate", Name: "soon", Spec: map[string]any{"not_after": now.Add(300 * 24 * time.Hour).Format(time.RFC3339)}})
	apply(api, catalog.EntityItem{Kind: "service", Name: "orphan", Metadata: map[string]any{"owner": "platform"}})
	apply(api, catalog.EntityItem{Kind: "database", Name: "missing"})
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	fs = open(t, env)
	for _, f := range fs {
		if f.EntityID == soon || (f.Type == TypeMissingOwner && f.EntityID == orphan) || f.Type == TypeMissingTarget {
			t.Fatalf("finding not closed: %+v", f)
		}
	}
	var closed int
	_ = env.Store.R().QueryRow(`SELECT COUNT(*) FROM findings WHERE state = 'closed' AND closed_at IS NOT NULL`).Scan(&closed)
	if closed < 3 {
		t.Fatalf("closed findings: %d", closed)
	}
}

func TestRaisedFindingsLiveUntilExpiry(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
		return RaiseTx(ctx, tx, Finding{DedupKey: TypeBreakGlass + "|u1", Type: TypeBreakGlass, Severity: Critical, Class: ClassConfirmed,
			Title: "Break-glass sign-in used", Message: "test"}, time.Hour)
	}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock}
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(byType(open(t, env), TypeBreakGlass)) != 1 {
		t.Fatal("break-glass finding closed before it expired")
	}
	env.Clock.Advance(2 * time.Hour)
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(byType(open(t, env), TypeBreakGlass)) != 0 {
		t.Fatal("break-glass finding outlived its expiry")
	}
}

func auditSystem() audit.Actor { return audit.System }
