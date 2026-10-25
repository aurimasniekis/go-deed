package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// loopbackPolicy permits plain HTTP to srv, which listens on 127.0.0.1.
func loopbackPolicy(srv *httptest.Server) Policy {
	return Policy{Allow: []string{srv.URL}, AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}
}

func get(t *testing.T, c *http.Client, rawURL string, hdr ...string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return c.Do(req)
}

func newGuard(t *testing.T, allow ...string) *Guard {
	t.Helper()
	g, err := New(allow)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNew(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		ok    bool
	}{
		{"none", nil, true},
		{"ipv4 and ipv6", []string{"10.0.0.0/8", "fd00::/8"}, true},
		{"unmasked prefix", []string{"10.1.2.3/8"}, true},
		{"bare address", []string{"10.0.0.1"}, false},
		{"garbage", []string{"nope"}, false},
		{"bad length", []string{"10.0.0.0/33"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.allow)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
	// An unmasked prefix is masked, so it covers the whole network.
	g := newGuard(t, "10.1.2.3/8")
	if !g.Allowed(netip.MustParseAddr("10.200.0.1"), nil) {
		t.Fatal("10.1.2.3/8 should be masked to 10.0.0.0/8")
	}
}

func TestAllowed(t *testing.T) {
	none := newGuard(t)
	wide := newGuard(t, "0.0.0.0/0", "::/0")
	lan := newGuard(t, "10.0.0.0/8", "fd00::/8")
	extra := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}
	cases := []struct {
		name  string
		g     *Guard
		addr  string
		extra []netip.Prefix
		want  bool
	}{
		// Public addresses are allowed by default.
		{"public v4", none, "192.0.2.10", nil, true},
		{"public v4 test-net-2", none, "198.51.100.7", nil, true},
		{"public v4 test-net-3", none, "203.0.113.9", nil, true},
		{"public v6", none, "2001:db8::1", nil, true},

		// Cloud metadata is never reachable, even under an allow-everything CIDR.
		{"metadata aws", wide, "169.254.169.254", nil, false},
		{"metadata ecs", wide, "169.254.170.2", nil, false},
		{"metadata alibaba", wide, "100.100.100.200", nil, false},
		{"metadata aws v6", wide, "fd00:ec2::254", nil, false},
		{"metadata v4-mapped", wide, "::ffff:169.254.169.254", nil, false},
		{"metadata per-policy cidr", none, "169.254.169.254", []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}, false},
		{"metadata exact per-policy cidr", none, "169.254.169.254", []netip.Prefix{netip.MustParsePrefix("169.254.169.254/32")}, false},
		{"metadata v6 under lan allow", lan, "fd00:ec2::254", nil, false},
		{"metadata v6 zoned", wide, "fd00:ec2::254%eth0", nil, false},
		{"metadata neighbour allowed by cidr", wide, "169.254.169.253", nil, true},

		// Special ranges are refused by default.
		{"loopback v4", none, "127.0.0.1", nil, false},
		{"loopback v4 other", none, "127.10.20.30", nil, false},
		{"loopback v6", none, "::1", nil, false},
		{"private 10", none, "10.0.0.1", nil, false},
		{"private 172.16", none, "172.16.5.4", nil, false},
		{"private 192.168", none, "192.168.1.1", nil, false},
		{"private v6 ula", none, "fd12:3456::1", nil, false},
		{"link-local v4", none, "169.254.1.1", nil, false},
		{"link-local v6", none, "fe80::1", nil, false},
		{"link-local v6 zoned", none, "fe80::1%eth0", nil, false},
		{"cgnat", none, "100.64.0.1", nil, false},
		{"cgnat top", none, "100.127.255.254", nil, false},
		{"multicast v4", none, "224.0.0.1", nil, false},
		{"multicast v6", none, "ff02::1", nil, false},
		{"interface-local multicast v6", none, "ff01::1", nil, false},
		{"unspecified v4", none, "0.0.0.0", nil, false},
		{"unspecified v6", none, "::", nil, false},
		{"this network", none, "0.1.2.3", nil, false},
		{"ietf protocol assignments", none, "192.0.0.8", nil, false},
		{"benchmarking", none, "198.18.0.1", nil, false},
		{"reserved class e", none, "240.0.0.1", nil, false},
		{"broadcast", none, "255.255.255.255", nil, false},
		{"ipv4-mapped loopback", none, "::ffff:127.0.0.1", nil, false},
		{"ipv4-mapped private", none, "::ffff:10.0.0.1", nil, false},
		{"ipv4-mapped public", none, "::ffff:192.0.2.1", nil, true},
		{"ipv4-translated", none, "::ffff:0:a00:1", nil, false},
		{"ipv4-compatible", none, "::a00:1", nil, false},
		{"nat64 well-known", none, "64:ff9b::a9fe:a9fe", nil, false},
		{"nat64 local-use", none, "64:ff9b:1::a00:1", nil, false},
		{"6to4", none, "2002:a00:1::1", nil, false},
		{"teredo", none, "2001:0:a00:1::1", nil, false},
		{"site-local v6", none, "fec0::1", nil, false},

		// An operator CIDR permits private destinations.
		{"lan allow v4", lan, "10.1.2.3", nil, true},
		{"lan allow v6", lan, "fd00::5", nil, true},
		{"lan allow does not cover other private", lan, "192.168.1.1", nil, false},
		{"lan allow ipv4-mapped", lan, "::ffff:10.1.2.3", nil, true},
		{"per-policy cidr", none, "192.168.1.20", extra, true},
		{"per-policy cidr outside", none, "192.168.2.20", extra, false},
		{"loopback allowed by cidr", none, "127.0.0.1", []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, true},
		{"loopback /32 does not cover neighbour", none, "127.0.0.2", []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, false},

		// IPv6 zones must not let an address slip past the prefix lists.
		{"nat64 zoned", none, "64:ff9b::a9fe:a9fe%eth0", nil, false},
		{"6to4 zoned", none, "2002:a00:1::1%eth0", nil, false},
		{"ipv4-compatible zoned", none, "::a00:1%eth0", nil, false},
		{"zoned link-local allowed by cidr", newGuard(t, "fe80::/10"), "fe80::1%eth0", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.g.Allowed(netip.MustParseAddr(c.addr), c.extra); got != c.want {
				t.Fatalf("Allowed(%s) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}

func TestValidatePolicy(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		ok   bool
	}{
		{"empty", Policy{}, true},
		{"https host", Policy{Allow: []string{"https://example.com"}}, true},
		{"https host port", Policy{Allow: []string{"https://example.com:8443"}}, true},
		{"ipv6 literal", Policy{Allow: []string{"https://[2001:db8::1]:8443"}}, true},
		{"surrounding space", Policy{Allow: []string{"  https://example.com  "}}, true},
		{"http refused", Policy{Allow: []string{"http://example.com"}}, false},
		{"http with insecure", Policy{Allow: []string{"http://example.com"}, InsecureHTTP: true}, true},
		{"no scheme", Policy{Allow: []string{"example.com"}}, false},
		{"other scheme", Policy{Allow: []string{"ftp://example.com"}}, false},
		{"no host", Policy{Allow: []string{"https://"}}, false},
		{"credentials", Policy{Allow: []string{"https://user:pass@example.com"}}, false},
		{"bad cidr", Policy{AllowCIDRs: []string{"10.0.0.0"}}, false},
		{"good cidr", Policy{AllowCIDRs: []string{"10.0.0.0/8"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePolicy(c.p)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			_, cerr := newGuard(t).Client(c.p)
			if (cerr == nil) != c.ok {
				t.Fatalf("Client err = %v, want ok=%v", cerr, c.ok)
			}
		})
	}
}

func TestMustClientPanicsOnInvalidPolicy(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustClient did not panic")
		}
	}()
	newGuard(t).MustClient(Policy{Allow: []string{"ftp://example.com"}})
}

func TestAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port := u.Port()
	other := strconv.Itoa(mustAtoi(t, port) + 1)
	g := newGuard(t)
	cases := []struct {
		name    string
		p       Policy
		target  string
		wantErr error
	}{
		{"listed", loopbackPolicy(srv), srv.URL + "/x", nil},
		{"listed uppercase host in entry", Policy{Allow: []string{"http://LOCALHOST:" + port}, AllowCIDRs: []string{"127.0.0.1/32", "::1/128"}, InsecureHTTP: true}, "http://localhost:" + port + "/", nil},
		{"listed uppercase host in request", Policy{Allow: []string{"http://localhost:" + port}, AllowCIDRs: []string{"127.0.0.1/32", "::1/128"}, InsecureHTTP: true}, "http://LocalHost:" + port + "/", nil},
		{"empty allowlist permits any host", Policy{AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}, srv.URL, nil},
		{"wrong port", loopbackPolicy(srv), "http://127.0.0.1:" + other + "/", ErrNotAllowlisted},
		{"wrong host", loopbackPolicy(srv), "http://127.0.0.2:" + port + "/", ErrNotAllowlisted},
		{"wrong scheme", Policy{Allow: []string{"https://127.0.0.1:" + port}, AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}, srv.URL, ErrNotAllowlisted},
		{"default port differs", Policy{Allow: []string{"http://127.0.0.1"}, AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}, srv.URL, ErrNotAllowlisted},
		{"http without insecure", Policy{AllowCIDRs: []string{"127.0.0.1/32"}}, srv.URL, ErrNotAllowlisted},
		{"credentials in url", loopbackPolicy(srv), "http://user:pass@127.0.0.1:" + port + "/", ErrNotAllowlisted},
		{"credentials in url without allowlist", Policy{AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}, "http://user@127.0.0.1:" + port + "/", ErrNotAllowlisted},
		{"allowlisted but loopback not permitted", Policy{Allow: []string{srv.URL}, InsecureHTTP: true}, srv.URL, ErrBlockedAddress},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := get(t, g.MustClient(c.p), c.target)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				_ = resp.Body.Close()
				return
			}
			if err == nil {
				_ = resp.Body.Close()
				t.Fatalf("request succeeded, want %v", c.wantErr)
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The dial-time check sees the resolved address: a hostname (here localhost, resolved from the hosts file) that
// resolves to a refused address is blocked even though the allowlist names it.
func TestDialTimeCheckOfResolvedHostname(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	target := "http://localhost:" + u.Port() + "/"
	allow := []string{"http://localhost:" + u.Port()}

	_, err := get(t, newGuard(t).MustClient(Policy{Allow: allow, InsecureHTTP: true}), target)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if Classify(err) != "blocked_address" {
		t.Fatalf("Classify = %q", Classify(err))
	}
	if hits.Load() != 0 {
		t.Fatal("blocked request reached the server")
	}

	// A guard-wide allow permits it.
	resp, err := get(t, newGuard(t, "127.0.0.1/32").MustClient(Policy{Allow: allow, InsecureHTTP: true}), target)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// So does a per-policy allow.
	resp, err = get(t, newGuard(t).MustClient(Policy{Allow: allow, AllowCIDRs: []string{"127.0.0.0/8"}, InsecureHTTP: true}), target)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestDialTimeCheckOfLiterals(t *testing.T) {
	// Every address below is refused before a packet is sent, so nothing leaves the host.
	targets := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://169.254.170.2/v2/credentials",
		"http://100.100.100.200/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
		"http://10.0.0.1/",
		"http://192.168.0.1/",
		"http://100.64.0.1/",
		"http://0.0.0.0/",
		"http://[::1]:1/",
		"http://[64:ff9b::a00:1]/",
		"http://[2002:a00:1::1]/",
		// A zone must not hide an address-embedding range from the prefix lists.
		"http://[2002:a00:1::1%251]/",
		"http://[64:ff9b::a00:1%251]/",
	}
	// The per-policy allow covers the metadata ranges; it must not unlock them.
	c := newGuard(t).MustClient(Policy{AllowCIDRs: []string{"169.254.0.0/16", "100.100.0.0/16", "fd00::/8"}, InsecureHTTP: true})
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			_, err := get(t, c, target)
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("err = %v, want ErrBlockedAddress", err)
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	// /hop/N redirects N more times, then answers.
	mux.HandleFunc("/hop/{n}", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n, _ := strconv.Atoi(r.PathValue("n"))
		if n == 0 {
			_, _ = io.WriteString(w, "done")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g := newGuard(t)
	cases := []struct {
		name    string
		max     int
		hops    int
		wantErr bool
	}{
		{"default allows 3", 0, 3, false},
		{"default refuses 4", 0, 4, true},
		{"one allowed", 1, 1, false},
		{"one refuses two", 1, 2, true},
		{"disabled refuses one", -1, 1, true},
		{"disabled no redirect", -1, 0, false},
		{"ten allowed", 10, 10, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := loopbackPolicy(srv)
			p.MaxRedirects = c.max
			resp, err := get(t, g.MustClient(p), fmt.Sprintf("%s/hop/%d", srv.URL, c.hops))
			if c.wantErr {
				if !errors.Is(err, ErrTooManyHops) {
					t.Fatalf("err = %v, want ErrTooManyHops", err)
				}
				if Classify(err) != "too_many_redirects" {
					t.Fatalf("Classify = %q", Classify(err))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "done" {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

func TestRedirectAllowlistAndCredentials(t *testing.T) {
	type seen struct{ auth, org string }
	record := func(ch chan<- seen) http.HandlerFunc {
		return func(_ http.ResponseWriter, r *http.Request) {
			ch <- seen{r.Header.Get("Authorization"), r.Header.Get("X-Scope-OrgID")}
		}
	}
	targetSeen := make(chan seen, 1)
	target := httptest.NewServer(record(targetSeen))
	t.Cleanup(target.Close)

	var redirectTo atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			if r.Header.Get("Authorization") == "" || r.Header.Get("X-Scope-OrgID") == "" {
				w.WriteHeader(http.StatusTeapot)
			}
			return
		}
		http.Redirect(w, r, redirectTo.Load().(string), http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	g := newGuard(t)
	creds := []string{"Authorization", "Bearer deed_at_secret", "X-Scope-OrgID", "tenant-1"}

	// Redirect to a destination outside the allowlist is refused before it is contacted.
	redirectTo.Store(target.URL + "/")
	_, err := get(t, g.MustClient(loopbackPolicy(origin)), origin.URL+"/", creds...)
	if !errors.Is(err, ErrNotAllowlisted) {
		t.Fatalf("err = %v, want ErrNotAllowlisted", err)
	}
	select {
	case <-targetSeen:
		t.Fatal("non-allowlisted redirect target was contacted")
	default:
	}

	// Redirect to another scheme is refused.
	redirectTo.Store("ftp://127.0.0.1/")
	if _, err := get(t, g.MustClient(loopbackPolicy(origin)), origin.URL+"/"); !errors.Is(err, ErrNotAllowlisted) {
		t.Fatalf("redirect to ftp: err = %v, want ErrNotAllowlisted", err)
	}

	// Same-origin redirect keeps the credentials.
	redirectTo.Store("/same")
	resp, err := get(t, g.MustClient(loopbackPolicy(origin)), origin.URL+"/", creds...)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin redirect lost credentials (status %d)", resp.StatusCode)
	}

	// Cross-origin redirect inside the allowlist drops them.
	redirectTo.Store(target.URL + "/")
	p := Policy{Allow: []string{origin.URL, target.URL}, AllowCIDRs: []string{"127.0.0.1/32"}, InsecureHTTP: true}
	resp, err = get(t, g.MustClient(p), origin.URL+"/", creds...)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if s := <-targetSeen; s.auth != "" || s.org != "" {
		t.Fatalf("credentials forwarded cross-origin: %+v", s)
	}
}

func TestResponseSizeBudget(t *testing.T) {
	const limit = 64
	mux := http.NewServeMux()
	mux.HandleFunc("/len/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		_, _ = io.WriteString(w, strings.Repeat("a", n))
	})
	mux.HandleFunc("/chunked/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		// Flushing before writing forces chunked encoding with no Content-Length.
		w.(http.Flusher).Flush()
		for range n {
			_, _ = io.WriteString(w, "a")
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := loopbackPolicy(srv)
	p.MaxBodyBytes = limit
	c := newGuard(t).MustClient(p)
	cases := []struct {
		name      string
		path      string
		doErr     bool // refused before the body is read (declared length)
		readErr   bool // refused while reading
		wantBytes int
	}{
		{"declared under", "/len/10", false, false, 10},
		{"declared at limit", fmt.Sprintf("/len/%d", limit), false, false, limit},
		{"declared over", fmt.Sprintf("/len/%d", limit+1), true, false, 0},
		{"chunked under", "/chunked/10", false, false, 10},
		{"chunked at limit", fmt.Sprintf("/chunked/%d", limit), false, false, limit},
		{"chunked over", fmt.Sprintf("/chunked/%d", limit+1), false, true, 0},
		{"chunked far over", "/chunked/10000", false, true, 0},
		{"empty", "/len/0", false, false, 0},
	}
	for _, c2 := range cases {
		t.Run(c2.name, func(t *testing.T) {
			resp, err := get(t, c, srv.URL+c2.path)
			if c2.doErr {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("err = %v, want ErrTooLarge", err)
				}
				if Classify(err) != "too_large" {
					t.Fatalf("Classify = %q", Classify(err))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if c2.readErr {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("read err = %v, want ErrTooLarge", err)
				}
				if len(body) > limit {
					t.Fatalf("read %d bytes past the %d budget", len(body), limit)
				}
				return
			}
			if err != nil || len(body) != c2.wantBytes {
				t.Fatalf("read %d bytes, err %v; want %d", len(body), err, c2.wantBytes)
			}
		})
	}
}

func TestDefaultBodyBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(8<<20+1))
	}))
	t.Cleanup(srv.Close)
	_, err := get(t, newGuard(t).MustClient(loopbackPolicy(srv)), srv.URL)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge for a body over the 8 MiB default", err)
	}
}

// The environment proxy is never consulted: with HTTP(S)_PROXY pointing at a reachable proxy, a request to a
// refused address is still refused at dial time instead of being handed to the proxy.
func TestNoEnvironmentProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyHits.Add(1) }))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	// Sanity check: the standard environment lookup would route this request through the proxy.
	probe, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://10.0.0.1/", nil)
	if u, err := http.ProxyFromEnvironment(probe); err != nil || u == nil {
		t.Fatalf("test setup: environment proxy not picked up (%v, %v)", u, err)
	}

	// 127.0.0.1 is allowed so that, were the proxy used, the request would reach it.
	c := newGuard(t, "127.0.0.1/32").MustClient(Policy{InsecureHTTP: true})
	for _, target := range []string{"http://10.0.0.1/", "https://10.0.0.1/"} {
		_, err := get(t, c, target)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("%s: err = %v, want ErrBlockedAddress", target, err)
		}
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("environment proxy received %d requests", proxyHits.Load())
	}
	base := c.Transport.(*transport).base.(*http.Transport)
	if base.Proxy != nil {
		t.Fatal("transport has a proxy function")
	}
}

func certPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

func TestTLSTrust(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // expected handshake failures
	srv.StartTLS()
	t.Cleanup(srv.Close)
	p := Policy{Allow: []string{srv.URL}, AllowCIDRs: []string{"127.0.0.1/32"}}

	// The test CA is not trusted by default.
	_, err := get(t, newGuard(t).MustClient(p), srv.URL)
	if err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if Classify(err) != "tls" {
		t.Fatalf("Classify = %q (%v), want tls", Classify(err), err)
	}

	// A per-integration CA.
	withCA := p
	withCA.CAPEM = certPEM(srv)
	resp, err := get(t, newGuard(t).MustClient(withCA), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// ...does not leak into other integrations of the same guard.
	g := newGuard(t)
	_ = g.MustClient(withCA)
	if _, err := get(t, g.MustClient(p), srv.URL); err == nil {
		t.Fatal("per-integration CA leaked into another client")
	}

	// An installation-wide CA.
	g = newGuard(t)
	if err := g.AddRootCAs(certPEM(srv)); err != nil {
		t.Fatal(err)
	}
	resp, err = get(t, g.MustClient(p), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Bad PEM is refused in both places.
	if err := newGuard(t).AddRootCAs([]byte("not pem")); err == nil {
		t.Fatal("AddRootCAs accepted garbage")
	}
	bad := p
	bad.CAPEM = []byte("not pem")
	if _, err := newGuard(t).Client(bad); err == nil {
		t.Fatal("Client accepted a garbage CAPEM")
	}

	// TLS 1.2 is the minimum.
	tc := g.MustClient(p).Transport.(*transport).base.(*http.Transport).TLSClientConfig
	if tc.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x", tc.MinVersion)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	p := loopbackPolicy(srv)
	p.Timeout = 50 * time.Millisecond
	_, err := get(t, newGuard(t).MustClient(p), srv.URL)
	if err == nil || Classify(err) != "timeout" {
		t.Fatalf("err = %v (%s), want timeout", err, Classify(err))
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{ErrBlockedAddress, "blocked_address"},
		{fmt.Errorf("dial: %w", ErrBlockedAddress), "blocked_address"},
		{&url.Error{Op: "Get", URL: "https://example.com", Err: ErrNotAllowlisted}, "not_allowlisted"},
		{ErrTooLarge, "too_large"},
		{ErrTooManyHops, "too_many_redirects"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{&net.OpError{Op: "dial", Err: timeoutErr{}}, "timeout"},
		{&net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}, "dns"},
		{&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "tls"},
		{errors.New("connection refused"), "connection"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Fatalf("Classify(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
