// Adapted from Porch internal/httpx/clientip.go (Apache-2.0).

// Package httpx holds Deed's HTTP edge plumbing: client-IP resolution behind trusted proxies, security headers with a
// per-request CSP nonce, Origin checks, body limits, request IDs and rate limiting.
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver derives the client address from the TCP peer and, only when the peer is trusted, from forwarding
// headers. It never trusts a header from an untrusted peer.
type ClientIPResolver struct {
	trusted    []netip.Prefix
	cloudflare []netip.Prefix // non-nil only when Cloudflare mode is enabled
}

// NewClientIPResolver builds a resolver. trusted are reverse-proxy CIDRs whose X-Forwarded-For entries are believed;
// cloudflare, if non-empty, enables CF-Connecting-IP for hops inside those ranges.
func NewClientIPResolver(trusted, cloudflare []string) (*ClientIPResolver, error) {
	r := &ClientIPResolver{}
	for _, s := range trusted {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	for _, s := range cloudflare {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("cloudflare range %q: %w", s, err)
		}
		r.cloudflare = append(r.cloudflare, p.Masked())
	}
	return r, nil
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve returns the client address for r.
//
// Algorithm (right-to-left walk): start at the TCP peer. While the current hop is a trusted proxy and an
// X-Forwarded-For entry remains, step one entry to the left. The first untrusted hop is the client. Then, in
// Cloudflare mode, if that hop is a Cloudflare edge address, the client is CF-Connecting-IP (which Cloudflare
// overwrites on every request). Malformed entries stop the walk at the last trustworthy hop.
func (c *ClientIPResolver) Resolve(r *http.Request) netip.Addr {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return peer
	}
	hop := peer
	var chain []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for part := range strings.SplitSeq(h, ",") {
			chain = append(chain, strings.TrimSpace(part))
		}
	}
	for i := len(chain) - 1; i >= 0 && contains(c.trusted, hop); i-- {
		a, err := netip.ParseAddr(chain[i])
		if err != nil {
			break
		}
		hop = a.Unmap()
	}
	if len(c.cloudflare) > 0 && contains(c.cloudflare, hop) {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
			return a.Unmap()
		}
	}
	return hop
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// SourceKey is the throttling key for an address: the IPv4 address itself, or the IPv6 /64 network (one subscriber
// typically controls a whole /64).
func SourceKey(a netip.Addr) string {
	if !a.IsValid() {
		return "invalid"
	}
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(64)
	return p.String()
}

type clientIPKey struct{}

// Middleware stores the resolved client address in the request context.
func (c *ClientIPResolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, c.Resolve(r))))
	})
}

// ClientIP returns the address resolved by Middleware (invalid when absent).
func ClientIP(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return a
}
