package live_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/live"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func readEvents(t *testing.T, r *bufio.Reader, n int) []string {
	t.Helper()
	var out []string
	var cur strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for len(out) < n && time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (got %v)", err, out)
		}
		if line == "\n" {
			if s := cur.String(); s != "" && !strings.HasPrefix(s, ":") {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteString(line)
	}
	return out
}

func TestStreamResumeAndAuthorization(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	auth := authn.New(env.Store, env.Keyring, env.Audit, authn.Options{Clock: env.Clock, LocalEnabled: true,
		Params: &authn.Params{Version: authn.CurrentParams.Version, Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}})
	home := env.Project(t, "home")
	secret := env.Project(t, "secret")
	hub := live.NewHub(env.Catalog, nil, env.Clock, 10)
	if err := hub.Start(ctx); err != nil {
		t.Fatal(err)
	}
	env.Catalog.Notify = hub.Notify
	mux := http.NewServeMux()
	d := &httpapi.Deps{Authn: auth, Catalog: env.Catalog}
	hub.Register(mux, d)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// A viewer token scoped to home only.
	u, err := auth.CreateUser(ctx, env.Audit.SystemActor(), authn.CreateUserInput{Email: "v@example.com", DisplayName: "V"})
	_ = u
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetMember(ctx, env.Audit.SystemActor(), home.ID, u.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	p := &authn.Principal{UserID: u.ID, AuthAt: env.Clock.Now(), ProjectRoles: map[string]string{home.ID: "viewer"}}
	_, tok, err := auth.CreateAPIToken(ctx, p, authn.CreateAPITokenInput{Name: "t", ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateToken(ctx, tok, ""); err != nil {
		t.Fatalf("token: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b := make([]byte, 500)
		n, _ := resp.Body.Read(b)
		t.Fatalf("status %d: %s", resp.StatusCode, b[:n])
	}
	br := bufio.NewReader(resp.Body)
	if ev := readEvents(t, br, 1); !strings.Contains(ev[0], "event: hello") {
		t.Fatalf("hello: %v", ev)
	}
	hs := env.Source(t, home.ID, "api", "")
	ss := env.Source(t, secret.ID, "api", "")
	if _, err := env.Apply(t, ss, catalog.EntityItem{Kind: "service", Name: "hidden"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Apply(t, hs, catalog.EntityItem{Kind: "service", Name: "visible"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	ev := readEvents(t, br, 1)
	if !strings.Contains(ev[0], `"visible"`) || strings.Contains(strings.Join(ev, ""), "hidden") {
		t.Fatalf("authorization filter: %v", ev)
	}
	// Resume from the last id: only newer events are replayed.
	id := strings.TrimPrefix(strings.Split(ev[0], "\n")[0], "id: ")
	if _, err := env.Apply(t, hs, catalog.EntityItem{Kind: "service", Name: "later"}, catalog.ApplyOpts{}); err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	req2.Header.Set("Last-Event-ID", id)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	ev2 := readEvents(t, bufio.NewReader(resp2.Body), 2)
	if !strings.Contains(ev2[1], `"later"`) || strings.Contains(ev2[1], `"visible"`) {
		t.Fatalf("resume: %v", ev2)
	}
}
