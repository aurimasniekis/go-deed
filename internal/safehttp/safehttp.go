// Adapted from Porch internal/netguard/netguard.go (Apache-2.0).

// Package safehttp builds outbound HTTP clients for connectors, telemetry adapters, OIDC discovery and backups
// (ADR-008). Every client enforces, in order:
//
//  1. a per-integration allowlist of scheme://host:port (checked for every request, including redirects);
//  2. a dial-time check of the resolved IP (so DNS rebinding cannot bypass it): cloud metadata addresses are always
//     refused; loopback, private, link-local, CGNAT, multicast, unspecified and address-embedding IPv6 ranges are
//     refused unless an operator-configured CIDR allows them;
//  3. a redirect cap (default 3) and a response-size budget.
//
// No environment proxy is ever used.
package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Errors.
var (
	ErrBlockedAddress = errors.New("outbound connection to a disallowed address refused")
	ErrNotAllowlisted = errors.New("outbound destination is not in the integration's allowlist")
	ErrTooLarge       = errors.New("response exceeds the size budget")
	ErrTooManyHops    = errors.New("too many redirects")
)

// metadata addresses are never reachable, whatever the configuration says.
var metadata = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"), // AWS, GCP, Azure, DigitalOcean, Hetzner…
	netip.MustParsePrefix("169.254.170.2/32"),   // AWS ECS task metadata
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IPv6 IMDS
}

// blocked are special-purpose ranges not covered by netip's predicates.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

// Guard holds installation-wide outbound settings.
type Guard struct {
	allow []netip.Prefix
	roots *x509.CertPool
}

// New returns a guard allowing the given private CIDRs (outbound.allow_cidrs).
func New(allowCIDRs []string) (*Guard, error) {
	g := &Guard{}
	for _, s := range allowCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("outbound allow %q: %w", s, err)
		}
		g.allow = append(g.allow, p.Masked())
	}
	return g, nil
}

// AddRootCAs trusts extra PEM CA certificates in addition to the system roots.
func (g *Guard) AddRootCAs(pem []byte) error {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("no PEM certificates found")
	}
	g.roots = pool
	return nil
}

// Allowed reports whether address a may be dialed with the extra per-policy CIDRs.
func (g *Guard) Allowed(a netip.Addr, extra []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range metadata {
		if p.Contains(a) {
			return false
		}
	}
	for _, list := range [][]netip.Prefix{g.allow, extra} {
		for _, p := range list {
			if p.Contains(a) {
				return true
			}
		}
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() ||
		a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Policy is the per-integration outbound policy.
type Policy struct {
	// Allow lists permitted destinations as "https://host[:port]" (scheme required; port defaults to the scheme's).
	// Empty means any public destination (still subject to address checks) — used only for OIDC discovery of an
	// issuer the operator configured.
	Allow []string
	// AllowCIDRs additionally permits private addresses for this integration only.
	AllowCIDRs []string
	// MaxRedirects caps redirects (default 3; 0 keeps the default, -1 disables redirects).
	MaxRedirects int
	// MaxBodyBytes caps response bodies (default 8 MiB).
	MaxBodyBytes int64
	// Timeout bounds a whole request (default 15 s).
	Timeout time.Duration
	// InsecureHTTP permits http:// destinations in the allowlist (development or explicitly allowed LAN services).
	InsecureHTTP bool
	// CAPEM trusts an extra CA for this integration.
	CAPEM []byte
}

type dest struct {
	scheme, host, port string
}

func parseDest(s string) (dest, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return dest{}, fmt.Errorf("allowlist entry %q must be scheme://host[:port]", s)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return dest{scheme: u.Scheme, host: strings.ToLower(u.Hostname()), port: port}, nil
}

// ValidatePolicy checks the allowlist syntax.
func ValidatePolicy(p Policy) error {
	for _, a := range p.Allow {
		d, err := parseDest(a)
		if err != nil {
			return err
		}
		if d.scheme == "http" && !p.InsecureHTTP {
			return fmt.Errorf("allowlist entry %q uses http; enable insecure_http explicitly", a)
		}
	}
	for _, c := range p.AllowCIDRs {
		if _, err := netip.ParsePrefix(c); err != nil {
			return fmt.Errorf("allow_cidrs entry %q: %w", c, err)
		}
	}
	return nil
}

type transport struct {
	base     http.RoundTripper
	allow    []dest
	maxBody  int64
	insecure bool
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.User != nil {
		return nil, ErrNotAllowlisted
	}
	if r.URL.Scheme != "https" && (r.URL.Scheme != "http" || !t.insecure) {
		return nil, fmt.Errorf("%w: scheme %s", ErrNotAllowlisted, r.URL.Scheme)
	}
	if len(t.allow) > 0 {
		port := r.URL.Port()
		if port == "" {
			port = map[string]string{"https": "443", "http": "80"}[r.URL.Scheme]
		}
		ok := false
		for _, d := range t.allow {
			if d.scheme == r.URL.Scheme && d.host == strings.ToLower(r.URL.Hostname()) && d.port == port {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s://%s", ErrNotAllowlisted, r.URL.Scheme, r.URL.Host)
		}
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > t.maxBody {
		_ = resp.Body.Close()
		return nil, ErrTooLarge
	}
	resp.Body = &limitedBody{rc: resp.Body, left: t.maxBody}
	return resp, nil
}

type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// Probe for more data: an exactly-at-limit body is fine, anything beyond is an error.
		var one [1]byte
		n, err := l.rc.Read(one[:])
		if n > 0 {
			return 0, ErrTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

// Client returns an HTTP client enforcing p.
func (g *Guard) Client(p Policy) (*http.Client, error) {
	if err := ValidatePolicy(p); err != nil {
		return nil, err
	}
	var allow []dest
	for _, a := range p.Allow {
		d, _ := parseDest(a)
		allow = append(allow, d)
	}
	var extra []netip.Prefix
	for _, c := range p.AllowCIDRs {
		pf, _ := netip.ParsePrefix(c)
		extra = append(extra, pf.Masked())
	}
	if p.MaxBodyBytes <= 0 {
		p.MaxBodyBytes = 8 << 20
	}
	if p.Timeout <= 0 {
		p.Timeout = 15 * time.Second
	}
	maxHops := p.MaxRedirects
	if maxHops == 0 {
		maxHops = 3
	}
	control := func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return err
		}
		if !g.Allowed(a, extra) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, a)
		}
		return nil
	}
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: g.roots}
	if len(p.CAPEM) > 0 {
		pool := g.roots
		if pool == nil {
			if sp, err := x509.SystemCertPool(); err == nil && sp != nil {
				pool = sp
			} else {
				pool = x509.NewCertPool()
			}
		}
		pool = pool.Clone()
		if !pool.AppendCertsFromPEM(p.CAPEM) {
			return nil, errors.New("integration CA: no PEM certificates found")
		}
		tc.RootCAs = pool
	}
	base := &http.Transport{
		TLSClientConfig:       tc,
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: p.Timeout,
	}
	return &http.Client{
		Timeout:   p.Timeout,
		Transport: &transport{base: base, allow: allow, maxBody: p.MaxBodyBytes, insecure: p.InsecureHTTP},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if maxHops < 0 || len(via) > maxHops {
				return ErrTooManyHops
			}
			// Never forward credentials to another origin.
			if len(via) > 0 && (req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme) {
				req.Header.Del("Authorization")
				req.Header.Del("X-Scope-OrgID")
			}
			return nil
		},
	}, nil
}

// MustClient is Client for policies validated earlier.
func (g *Guard) MustClient(p Policy) *http.Client {
	c, err := g.Client(p)
	if err != nil {
		panic(err)
	}
	return c
}

// Classify maps an outbound error to a stable, non-sensitive reason for diagnostics and metrics.
func Classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrBlockedAddress):
		return "blocked_address"
	case errors.Is(err, ErrNotAllowlisted):
		return "not_allowlisted"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrTooManyHops):
		return "too_many_redirects"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "tls"
	}
	return "connection"
}
