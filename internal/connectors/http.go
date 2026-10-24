// Package connectors holds the built-in API source providers (ADR-013): GitHub, HallPass and Porch. Each reads a
// documented, versioned API with a read-only credential through the SSRF-safe client and emits facts through the
// shared snapshot protocol.
package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// apiClient performs authenticated JSON GETs against one origin.
type apiClient struct {
	base    string
	headers map[string]string
	http    *http.Client
	name    string
	// remaining is the last X-RateLimit-Remaining value seen (-1 = unknown).
	remaining int
}

func newClient(env sources.Env, name, base string, allowCIDRs []string, insecure bool, headers map[string]string) (*apiClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, apperr.New(apperr.CodeInvalid, "invalid %s URL", name)
	}
	hc, err := env.HTTP(safehttp.Policy{Allow: []string{u.Scheme + "://" + u.Host}, AllowCIDRs: allowCIDRs, InsecureHTTP: insecure,
		Timeout: 30 * time.Second, MaxBodyBytes: 16 << 20, MaxRedirects: -1})
	if err != nil {
		return nil, err
	}
	return &apiClient{base: strings.TrimSuffix(base, "/"), headers: headers, http: hc, name: name, remaining: -1}, nil
}

// get decodes path into out. 404 returns errNotFound.
func (c *apiClient) get(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return c.getURL(ctx, u, out)
}

// getURL fetches an absolute URL, which must stay under the client's base (pagination links).
func (c *apiClient) getURL(ctx context.Context, u string, out any) (http.Header, error) {
	if !strings.HasPrefix(u, c.base+"/") && !strings.HasPrefix(u, c.base+"?") {
		return nil, apperr.New(apperr.CodeUnavailable, "%s returned a pagination link outside its base URL", c.name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "deed-connector")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.remaining = n
		}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Header, errNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return resp.Header, apperr.New(apperr.CodeRateLimited, "%s rate limit exhausted until %s", c.name, resp.Header.Get("X-RateLimit-Reset"))
		}
		return resp.Header, apperr.New(apperr.CodeForbidden, "%s refused the credential (%d); check the token and its read-only scopes", c.name, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return resp.Header, apperr.New(apperr.CodeRateLimited, "%s rate limited the request", c.name)
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Header, apperr.New(apperr.CodeUnavailable, "%s answered %d", c.name, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.Header, apperr.New(apperr.CodeUnavailable, "%s returned an unexpected response body", c.name)
	}
	return resp.Header, nil
}

var errNotFound = apperr.New(apperr.CodeNotFound, "not found")

func message(err error) string {
	if ae, ok := err.(*apperr.Error); ok {
		return ae.Message
	}
	return "request failed: " + safehttp.Classify(err)
}

func diagErr(code string, err error) []sources.Diagnostic {
	return []sources.Diagnostic{{Level: "error", Code: code, Message: message(err)}}
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// envSlug maps a free-form environment name onto the metadata.environment slug.
func envSlug(s string) string {
	out := strings.Trim(strings.NewReplacer(".", "-", "_", "-").Replace(slug(s)), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

func strs(v []string) []any {
	out := make([]any, 0, len(v))
	for _, s := range v {
		out = append(out, s)
	}
	return out
}

func ts(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func withoutNil(m map[string]any) map[string]any {
	for k, v := range m {
		if v == nil || v == "" {
			delete(m, k)
		}
	}
	return m
}

// page is the cursor-paged list shape shared by HallPass and Porch.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// paged follows next_cursor until exhausted or max items are read; truncated reports hitting the cap.
func paged[T any](ctx context.Context, c *apiClient, path string, q url.Values, limit, max int) (items []T, truncated bool, err error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit", strconv.Itoa(limit))
	for {
		var p page[T]
		if _, err := c.get(ctx, path, q, &p); err != nil {
			return nil, false, err
		}
		items = append(items, p.Items...)
		if p.NextCursor == nil || *p.NextCursor == "" {
			return items, false, nil
		}
		if len(items) >= max {
			return items[:max], true, nil
		}
		q.Set("cursor", *p.NextCursor)
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func validateBaseURL(field, raw string, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return apperr.New(apperr.CodeInvalid, "%s must be an http(s) URL without credentials", field)
	}
	if u.Scheme == "http" && !insecure {
		return apperr.New(apperr.CodeInvalid, "%s uses http; set insecure_http to allow it", field)
	}
	return nil
}

func decode[T any](raw json.RawMessage, name string) (*T, error) {
	var c T
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&c); err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "invalid %s config", name)
	}
	return &c, nil
}
