package versions

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.10", -1}, {"v1.10.0", "1.9.9", 1}, {"1.2.0-rc1", "1.2.0", -1}, {"1.2", "1.2.0", -1}, {"2.0", "2.0", 0},
		{"22.04", "22.04.3", -1}, {"1.2.3+build5", "1.2.3", 0}, {"0010", "9", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	cycles := []Cycle{{Cycle: "15"}, {Cycle: "16"}, {Cycle: "1.2"}, {Cycle: "1.25"}, {Cycle: "22.04"}}
	for v, want := range map[string]string{"15.4": "15", "16": "16", "1.25.3": "1.25", "1.2.9": "1.2", "22.04.3": "22.04", "17.1": ""} {
		c, ok := MatchCycle(cycles, v)
		if (want == "") == ok || c.Cycle != want {
			t.Errorf("MatchCycle(%s) = %q %v, want %q", v, c.Cycle, ok, want)
		}
	}
}

const postgres = `[
 {"cycle":"17","releaseDate":"2024-09-26","eol":"2029-11-08","latest":"17.2","latestReleaseDate":"2024-11-21","lts":false},
 {"cycle":"16","releaseDate":"2023-09-14","eol":"2028-11-09","latest":"16.6","latestReleaseDate":"2024-11-21","lts":false},
 {"cycle":"12","releaseDate":"2019-10-03","eol":"2024-11-21","latest":"12.22","latestReleaseDate":"2024-11-21","lts":false}
]`

const nginx = `[{"cycle":"1.27","releaseDate":"2024-05-29","eol":false,"latest":"1.27.3","lts":false},{"cycle":"1.20","eol":true,"latest":"1.20.2"}]`

func TestRun(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/api/postgresql.json":
			_, _ = io.WriteString(w, postgres)
		case "/api/nginx.json":
			_, _ = io.WriteString(w, nginx)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	env := testenv.New(t)
	guard, _ := safehttp.New([]string{"127.0.0.1/32"})
	if err := guard.AddRootCAs(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})); err != nil {
		t.Fatal(err)
	}
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	ent := func(kind, name string, md map[string]any, spec map[string]any) string {
		res, err := env.Apply(t, api, catalog.EntityItem{Kind: kind, Name: name, Metadata: md, Spec: spec}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		return res.EntityID
	}
	ver := func(v, product string) map[string]any {
		return map[string]any{"version": map[string]any{"declared": v, "product": product}}
	}
	pg16 := ent("database", "pg16", ver("16.2", "postgresql"), nil)
	pg17 := ent("database", "pg17", ver("17.2", "postgresql"), nil)
	pg12 := ent("database", "pg12", ver("12.20", "postgresql"), nil)
	ng := ent("service", "proxy", ver("1.20.1", "nginx"), nil)
	unknown := ent("service", "odd", ver("3.1", "no-such-product"), nil)
	// Catalog source: a service deployed from a repository whose newest release is v2.4.0.
	app := ent("application", "api", ver("v2.3.1", ""), nil)
	repo := ent("repository", "acme-api", nil, map[string]any{"full_name": "acme/api"})
	r1 := ent("release", "acme-api-v2.4.0", nil, map[string]any{"tag": "v2.4.0"})
	r2 := ent("release", "acme-api-v3.0.0-rc1", nil, map[string]any{"tag": "v3.0.0-rc1", "prerelease": true})
	for _, rel := range []catalog.RelationItem{{Type: "contains", From: catalog.Ref{ID: repo}, To: catalog.Ref{ID: r1}},
		{Type: "contains", From: catalog.Ref{ID: repo}, To: catalog.Ref{ID: r2}}, {Type: "deployed_from", From: catalog.Ref{ID: app}, To: catalog.Ref{ID: repo}}} {
		if _, err := env.Relate(t, api, rel); err != nil {
			t.Fatal(err)
		}
	}
	c := &Checker{Store: env.Store, Guard: guard, Clock: env.Clock, EOLURL: srv.URL}
	n, err := c.Run(context.Background())
	if err != nil || n != 6 {
		t.Fatalf("run: %d %v", n, err)
	}
	status := func(id string) (string, string, string) {
		var s, latest, eol string
		if err := env.Store.R().QueryRow(`SELECT status, latest, eol_date FROM version_checks WHERE entity_id = ?`, id).Scan(&s, &latest, &eol); err != nil {
			t.Fatal(err)
		}
		return s, latest, eol
	}
	for id, want := range map[string][2]string{pg16: {StatusUpdateAvailable, "16.6"}, pg17: {StatusCurrent, "17.2"}, pg12: {StatusEOL, "12.22"},
		ng: {StatusEOL, "1.20.2"}, unknown: {StatusUnknown, ""}, app: {StatusUpdateAvailable, "v2.4.0"}} {
		if s, latest, _ := status(id); s != want[0] || latest != want[1] {
			t.Errorf("%s: %s %s, want %v", id, s, latest, want)
		}
	}
	e, _ := env.Catalog.GetEntity(context.Background(), pg16)
	if e.Metadata.Version.Available != "16.6" {
		t.Fatalf("available = %q", e.Metadata.Version.Available)
	}
	// Products are fetched once per run and cached.
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits["/api/postgresql.json"] != 1 {
		t.Fatalf("postgresql fetched %d times", hits["/api/postgresql.json"])
	}
	// Without the EOL source only catalog releases count.
	c2 := &Checker{Store: env.Store, Guard: guard, Clock: env.Clock}
	if _, err := c2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := status(pg12); s != StatusUnknown {
		t.Fatalf("catalog-only pg12 = %s", s)
	}
}

// TestLiveEndOfLife runs the checker against the real endoflife.date API (or another compatible URL):
// DEED_TEST_EOL_URL=https://endoflife.date go test -run TestLiveEndOfLife ./internal/versions/
func TestLiveEndOfLife(t *testing.T) {
	base := os.Getenv("DEED_TEST_EOL_URL")
	if base == "" {
		t.Skip("set DEED_TEST_EOL_URL=https://endoflife.date to query the live API")
	}
	env := testenv.New(t)
	guard, _ := safehttp.New(nil)
	p := env.Project(t, "home")
	api := env.Source(t, p.ID, "api", "")
	ent := func(name, v, product string) string {
		res, err := env.Apply(t, api, catalog.EntityItem{Kind: "database", Name: name,
			Metadata: map[string]any{"version": map[string]any{"declared": v, "product": product}}}, catalog.ApplyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		return res.EntityID
	}
	// Cycles whose end of life lies before the test clock (2026-10-01), one still supported, and an unknown product.
	pg12, pg17, mysql57 := ent("pg12", "12.1", "postgresql"), ent("pg17", "17.0", "postgresql"), ent("mysql57", "5.7.30", "mysql")
	unknown := ent("odd", "1.0", "no-such-product-deed-validation")
	c := &Checker{Store: env.Store, Guard: guard, Clock: env.Clock, EOLURL: base}
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := func(id string) (status, cycle, latest, source string) {
		if err := env.Store.R().QueryRow(`SELECT status, cycle, latest, latest_source FROM version_checks WHERE entity_id = ?`, id).
			Scan(&status, &cycle, &latest, &source); err != nil {
			t.Fatal(err)
		}
		return
	}
	for id, name := range map[string]string{pg12: "postgresql 12", mysql57: "mysql 5.7"} {
		if s, cyc, latest, src := row(id); s != StatusEOL || cyc == "" || latest == "" || src != "eol_api" {
			t.Errorf("%s: status %s cycle %q latest %q source %q, want eol from the API", name, s, cyc, latest, src)
		}
	}
	if s, cyc, latest, _ := row(pg17); (s != StatusUpdateAvailable && s != StatusCurrent) || cyc != "17" || latest == "" {
		t.Errorf("postgresql 17: status %s cycle %q latest %q", s, cyc, latest)
	}
	if s, _, _, _ := row(unknown); s != StatusUnknown {
		t.Errorf("unknown product: %s", s)
	}
	for name, id := range map[string]string{"postgresql 12": pg12, "postgresql 17": pg17, "mysql 5.7": mysql57} {
		s, cyc, latest, _ := row(id)
		t.Logf("%s: %s (cycle %s, latest %s)", name, s, cyc, latest)
	}
}
