// Adapted from Porch internal/httpx/headers.go (Apache-2.0).

package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

type nonceKey struct{}

// Nonce returns the request's CSP nonce ("" when SecurityHeaders did not run).
func Nonce(ctx context.Context) string {
	n, _ := ctx.Value(nonceKey{}).(string)
	return n
}

// CSP builds the Content-Security-Policy for a nonce: scripts and stylesheet elements need the nonce,
// inline style *attributes* are allowed because Mantine sets them; nothing may frame Deed.
func CSP(nonce string) string {
	return "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src-elem 'self' 'nonce-" + nonce + "'; " +
		"style-src-attr 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"
}

// NoStorePrefixes are path prefixes whose responses must never be cached.
var NoStorePrefixes = []string{"/api/", "/login", "/logout", "/setup", "/auth/", "/account"}

// SecurityHeaders sets security headers on every response and a fresh CSP nonce per request. hsts enables
// Strict-Transport-Security (only when the public URL is https).
func SecurityHeaders(hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [18]byte
		_, _ = rand.Read(b[:])
		nonce := base64.RawStdEncoding.EncodeToString(b[:])
		h := w.Header()
		h.Set("Content-Security-Policy", CSP(nonce))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		for _, p := range NoStorePrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				h.Set("Cache-Control", "no-store")
				h.Set("Pragma", "no-cache")
				break
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
	})
}

// LimitBody caps request bodies at n bytes.
func LimitBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}

// IsSafeMethod reports whether m never changes state.
func IsSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// SameOrigin reports whether an unsafe request carries proof that it was sent by a page of origin: a matching Origin
// header, or (when a browser omits Origin) Sec-Fetch-Site: same-origin. Requests with neither are rejected; this is
// the first CSRF layer in front of the HMAC token.
func SameOrigin(r *http.Request, origin string) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, origin)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}
