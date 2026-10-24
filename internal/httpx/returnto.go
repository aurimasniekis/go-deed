// Adapted from Porch internal/httpx/returnto.go (Apache-2.0).

package httpx

import (
	"net/url"
	"strings"
)

// SafeReturnTo returns s when it is a same-origin absolute path, else fallback. Rejected: absolute URLs, scheme-
// relative ("//evil"), backslash tricks ("/\evil"), control characters and over-long values (T-B2-6).
func SafeReturnTo(s, fallback string) string {
	if s == "" || len(s) > 2048 || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") {
		return fallback
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return fallback
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return fallback
	}
	return s
}
