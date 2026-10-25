package alerts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/alerts"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/findings"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func outboxTypes(t *testing.T, env *testenv.Env) []string {
	rows, err := env.Store.R().QueryContext(context.Background(), `SELECT type FROM outbox WHERE type LIKE 'deed.alert.%' ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func TestCertificateFindingAlertLifecycle(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	imp := env.Source(t, p.ID, "certs", catalog.LayerImported)
	soon := env.Clock.Now().Add(3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	r, err := env.Apply(t, imp, catalog.EntityItem{Kind: "certificate", Name: "www", Spec: map[string]any{"common_name": "www.example.com", "not_after": soon}}, catalog.ApplyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	al := &alerts.Engine{Store: env.Store, Audit: env.Audit, Clock: env.Clock}
	if err := al.EnsureBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	fe := &findings.Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock, OnChange: al.Evaluate}
	if err := fe.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := al.Alerts(ctx, []string{"firing"}, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	// Both the "critical-findings" and "certificates" rules match a 3-day expiry (critical).
	if len(list) != 2 {
		t.Fatalf("expected 2 firing alerts, got %+v", list)
	}
	if got := outboxTypes(t, env); len(got) != 2 || got[0] != "deed.alert.alert_raised.v1" {
		t.Fatalf("notifications %v", got)
	}
	// Re-evaluating inside the cooldown does not re-notify (dedup).
	env.Clock.Advance(time.Minute)
	if err := fe.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, env); len(got) != 2 {
		t.Fatalf("duplicate notifications %v", got)
	}
	// Renewal resolves the finding and the alerts.
	later := env.Clock.Now().Add(300 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := env.Apply(t, imp, catalog.EntityItem{Ref: catalog.Ref{ID: r.EntityID}, Spec: map[string]any{"common_name": "www.example.com", "not_after": later}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := fe.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ = al.Alerts(ctx, []string{"resolved"}, nil, 50)
	if len(list) != 2 {
		t.Fatalf("alerts not resolved: %+v", list)
	}
	got := outboxTypes(t, env)
	if len(got) != 4 || got[3] != "deed.alert.alert_resolved.v1" {
		t.Fatalf("resolution notifications %v", got)
	}
	// Silences suppress delivery.
	if _, err := al.CreateSilence(ctx, audit.System, alerts.Matcher{Types: []string{"certificate_expiring"}}, "maintenance", env.Clock.Now(), env.Clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, imp, catalog.EntityItem{Ref: catalog.Ref{ID: r.EntityID}, Spec: map[string]any{"common_name": "www.example.com", "not_after": soon}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := fe.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	sup, _ := al.Alerts(ctx, []string{"suppressed"}, nil, 50)
	if len(sup) == 0 {
		t.Fatalf("silenced alert not suppressed")
	}
}

func TestDriftAndInvalidFindings(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	p := env.Project(t, "home")
	tf := env.Source(t, p.ID, "terraform", "")
	imp := env.Source(t, p.ID, "tfstate", catalog.LayerImported)
	if _, err := env.Apply(t, tf, catalog.EntityItem{Kind: "server", Name: "web", Spec: map[string]any{"ipv4": "198.51.100.1"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, imp, catalog.EntityItem{Kind: "server", Name: "web", Spec: map[string]any{"ipv4": "198.51.100.2"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, imp, catalog.EntityItem{Kind: "server", Name: "bad", Spec: map[string]any{"ipv4": "nope"}}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	fe := &findings.Engine{Store: env.Store, Catalog: env.Catalog, Clock: env.Clock}
	if err := fe.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	var drift, invalid int
	_ = env.Store.R().QueryRowContext(ctx, `SELECT SUM(type = 'declared_observed_drift'), SUM(type = 'invalid_schema') FROM findings WHERE state = 'open'`).Scan(&drift, &invalid)
	if drift != 1 || invalid != 1 {
		t.Fatalf("drift=%d invalid=%d", drift, invalid)
	}
}

func TestAlertsListFiltersBeforeLimit(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	busy, quiet := env.Project(t, "busy"), env.Project(t, "quiet")
	now := env.Clock.Now().UnixMicro()
	if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO alert_rules (id, name, matcher_json, created_at, updated_at) VALUES ('rul_t', 'test', '{}', ?, ?)`, now, now); err != nil {
			return err
		}
		add := func(id, project, state, silenced string, updated int64) error {
			_, err := tx.Exec(ctx, `INSERT INTO alerts (id, rule_id, finding_id, dedup_key, project_id, state, severity, title, started_at, silenced_by, updated_at)
				VALUES (?, 'rul_t', ?, ?, ?, ?, 'critical', ?, ?, NULLIF(?, ''), ?)`, id, id, id, project, state, id, now, silenced, updated)
			return err
		}
		// Newer alerts in a project the caller cannot read sort before the caller's alerts.
		for i := range 20 {
			if err := add(fmt.Sprintf("alt_busy%02d", i), busy.ID, "firing", "", now+int64(100+i)); err != nil {
				return err
			}
		}
		if err := add("alt_quiet_firing", quiet.ID, "firing", "", now+2); err != nil {
			return err
		}
		return add("alt_quiet_silenced", quiet.ID, "firing", "sil_1", now+1)
	}); err != nil {
		t.Fatal(err)
	}
	al := &alerts.Engine{Store: env.Store, Clock: env.Clock}
	ids := func(states, projects []string, limit int) []string {
		t.Helper()
		list, err := al.Alerts(ctx, states, projects, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range list {
			out = append(out, a.ID+"="+a.State)
		}
		return out
	}
	if got := ids(nil, []string{quiet.ID}, 2); fmt.Sprint(got) != "[alt_quiet_firing=firing alt_quiet_silenced=suppressed]" {
		t.Fatalf("viewer of quiet: %v", got)
	}
	if got := ids([]string{"suppressed"}, nil, 1); fmt.Sprint(got) != "[alt_quiet_silenced=suppressed]" {
		t.Fatalf("suppressed filter: %v", got)
	}
	if got := ids([]string{"firing"}, nil, 50); len(got) != 21 {
		t.Fatalf("firing (silenced excluded): %d", len(got))
	}
	if got := ids(nil, []string{}, 10); len(got) != 0 {
		t.Fatalf("no readable projects: %v", got)
	}
}
