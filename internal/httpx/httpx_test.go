// Adapted from Porch internal/httpx/httpx_test.go (Apache-2.0).

package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
)

func req(remote string, hdr map[string][]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for k, vs := range hdr {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return r
}

// T-B2-1: forwarding headers are believed only from trusted peers, walking right to left.
func TestClientIPSpoofing(t *testing.T) {
	cf := []string{"173.245.48.0/20", "2400:cb00::/32"}
	res, err := NewClientIPResolver([]string{"10.0.0.0/8", "fd00::/8"}, cf)
	if err != nil {
		t.Fatal(err)
	}
	noCF, _ := NewClientIPResolver([]string{"10.0.0.0/8"}, nil)
	cases := []struct {
		name   string
		r      *ClientIPResolver
		remote string
		hdr    map[string][]string
		want   string
	}{
		{"direct, no headers", res, "198.51.100.7:5555", nil, "198.51.100.7"},
		{"untrusted peer forging XFF", res, "198.51.100.7:5555", map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}, "198.51.100.7"},
		{"untrusted peer forging CF header", res, "198.51.100.7:5555", map[string][]string{"CF-Connecting-IP": {"1.2.3.4"}}, "198.51.100.7"},
		{"trusted proxy single hop", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9"},
		{"client prepends fake entries", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"1.1.1.1, 203.0.113.9"}}, "203.0.113.9"},
		{"chained trusted proxies", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"9.9.9.9, 203.0.113.9, 10.1.1.1"}}, "203.0.113.9"},
		{"multiple XFF headers", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"9.9.9.9", "203.0.113.9"}}, "203.0.113.9"},
		{"malformed entry stops walk", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9, garbage"}}, "10.0.0.2"},
		{"trusted proxy without XFF", res, "10.0.0.2:1", nil, "10.0.0.2"},
		{"cloudflare edge direct", res, "173.245.48.10:443", map[string][]string{"CF-Connecting-IP": {"203.0.113.50"}}, "203.0.113.50"},
		{"cloudflare via local proxy", res, "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"173.245.48.10"}, "CF-Connecting-IP": {"203.0.113.50"}}, "203.0.113.50"},
		{"cloudflare disabled ignores header", noCF, "173.245.48.10:443", map[string][]string{"CF-Connecting-IP": {"203.0.113.50"}}, "173.245.48.10"},
		{"cloudflare edge bad header", res, "173.245.48.10:443", map[string][]string{"CF-Connecting-IP": {"nope"}}, "173.245.48.10"},
		{"forged cf range inside XFF from untrusted", res, "198.51.100.7:1", map[string][]string{"X-Forwarded-For": {"173.245.48.10"}, "CF-Connecting-IP": {"1.2.3.4"}}, "198.51.100.7"},
		{"ipv4-mapped ipv6 peer", res, "[::ffff:10.0.0.2]:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9"},
		{"ipv6 trusted", res, "[fd00::1]:1", map[string][]string{"X-Forwarded-For": {"2001:db8::5"}}, "2001:db8::5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.Resolve(req(c.remote, c.hdr)); got.String() != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestSourceKeyIPv6Slash64(t *testing.T) {
	a := netip.MustParseAddr("2001:db8:1:2:aaaa::1")
	b := netip.MustParseAddr("2001:db8:1:2:ffff::9")
	if SourceKey(a) != SourceKey(b) || SourceKey(a) != "2001:db8:1:2::/64" {
		t.Fatalf("%s %s", SourceKey(a), SourceKey(b))
	}
	if SourceKey(netip.MustParseAddr("192.0.2.1")) != "192.0.2.1" {
		t.Fatal("ipv4 key")
	}
}

func TestSecurityHeaders(t *testing.T) {
	var nonce string
	h := SecurityHeaders(true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nonce = Nonce(r.Context()) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if nonce == "" || !strings.Contains(csp, "'nonce-"+nonce+"'") || strings.Contains(csp, "style-src 'unsafe-inline'") ||
		!strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp %q nonce %q", csp, nonce)
	}
	for k, want := range map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff",
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q", k, got)
		}
	}
	rec2 := httptest.NewRecorder()
	var nonce2 string
	SecurityHeaders(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nonce2 = Nonce(r.Context()) })).
		ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/assets/x.js", nil))
	if nonce2 == nonce || rec2.Header().Get("Cache-Control") != "" || rec2.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("nonce reused or no-store/HSTS leaked")
	}
}

func TestSameOrigin(t *testing.T) {
	const origin = "https://auth.example.com"
	mk := func(h map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/x", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	if !SameOrigin(mk(map[string]string{"Origin": origin}), origin) {
		t.Fatal("same origin rejected")
	}
	for _, h := range []map[string]string{
		{"Origin": "https://evil.example.com"}, {"Origin": "null"}, {}, {"Sec-Fetch-Site": "cross-site"},
		{"Origin": "https://auth.example.com.evil.net"},
	} {
		if SameOrigin(mk(h), origin) {
			t.Fatalf("accepted %v", h)
		}
	}
	if !SameOrigin(mk(map[string]string{"Sec-Fetch-Site": "same-origin"}), origin) {
		t.Fatal("sec-fetch-site fallback")
	}
}

func TestLimitBody(t *testing.T) {
	h := LimitBody(10, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 100)
		if _, err := r.Body.Read(buf); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 50))))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestLimiter(t *testing.T) {
	c := clock.NewFake(time.Unix(0, 0))
	l := NewLimiter(c, 60, 3)
	for i := range 3 {
		if !l.Allow("k") {
			t.Fatalf("burst %d denied", i)
		}
	}
	if l.Allow("k") || !l.Allow("other") {
		t.Fatal("bucket not isolated")
	}
	c.Advance(time.Second)
	if !l.Allow("k") || l.Allow("k") {
		t.Fatal("refill wrong")
	}
}

func TestRequestIDIgnoresInbound(t *testing.T) {
	var got string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = GetRequestID(r.Context()) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "attacker-chosen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got == "attacker-chosen" || !strings.HasPrefix(got, "req_") || rec.Header().Get("X-Request-ID") != got {
		t.Fatalf("got %q", got)
	}
}

// T-B2-6: open redirects through return_to are refused.
func TestSafeReturnTo(t *testing.T) {
	for _, bad := range []string{"https://evil.example.com", "//evil.example.com", "/\\evil.example.com", "\\\\evil", "javascript:alert(1)",
		"/ok\r\nLocation: x", "http:/evil", "", "evil", "/%0d%0a", "///evil"} {
		got := SafeReturnTo(bad, "/")
		if got != "/" && bad != "/%0d%0a" {
			t.Errorf("%q → %q", bad, got)
		}
	}
	for _, ok := range []string{"/", "/apps", "/account?tab=security", "/a/b#frag"} {
		if got := SafeReturnTo(ok, "/"); got != ok {
			t.Errorf("%q → %q", ok, got)
		}
	}
}
