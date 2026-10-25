package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
)

// httpConfig is shared by the HTTP adapters.
type httpConfig struct {
	URL          string   `json:"url"`
	Tenant       string   `json:"tenant,omitempty"`
	AllowCIDRs   []string `json:"allow_cidrs,omitempty"`
	InsecureHTTP bool     `json:"insecure_http,omitempty"`
}

// httpSchemaProps are the JSON Schema properties of httpConfig (spliced into each adapter's schema).
const httpSchemaProps = `
    "url": {"type": "string", "format": "uri", "maxLength": 2048},
    "allow_cidrs": {"type": "array", "maxItems": 32, "items": {"type": "string"}, "description": "Private CIDRs the URL may resolve to (loopback and metadata addresses stay blocked unless listed)."},
    "insecure_http": {"type": "boolean", "description": "Allow http:// (private networks only)."}`

func parseHTTP(raw json.RawMessage) (*httpConfig, error) {
	var c httpConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "invalid config")
	}
	return &c, nil
}

func validateHTTP(raw json.RawMessage) error {
	c, err := parseHTTP(raw)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return apperr.New(apperr.CodeInvalid, "url must be an http(s) URL without credentials or query")
	}
	if u.Scheme == "http" && !c.InsecureHTTP {
		return apperr.New(apperr.CodeInvalid, "url uses http; set insecure_http to allow it")
	}
	return nil
}

// client is an authenticated HTTP client for one integration.
type client struct {
	base    string
	headers map[string]string
	http    *http.Client
}

func newHTTPClient(c Conn, maxBody int64) (*client, *httpConfig, error) {
	cfg, err := parseHTTP(c.Config)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, nil, apperr.New(apperr.CodeInvalid, "invalid url")
	}
	hc, err := c.HTTP(safehttp.Policy{Allow: []string{u.Scheme + "://" + u.Host}, AllowCIDRs: cfg.AllowCIDRs, InsecureHTTP: cfg.InsecureHTTP,
		Timeout: 30 * time.Second, MaxBodyBytes: maxBody, MaxRedirects: -1})
	if err != nil {
		return nil, nil, err
	}
	h := map[string]string{"Accept": "application/json", "User-Agent": "deed-telemetry"}
	switch {
	case c.Secrets["token"] != "":
		h["Authorization"] = "Bearer " + c.Secrets["token"]
	case c.Secrets["api_key"] != "":
		h["Authorization"] = "ApiKey " + c.Secrets["api_key"]
	case c.Secrets["username"] != "":
		h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Secrets["username"]+":"+c.Secrets["password"]))
	}
	if cfg.Tenant != "" {
		h["X-Scope-OrgID"] = cfg.Tenant
	}
	return &client{base: strings.TrimSuffix(cfg.URL, "/"), headers: h, http: hc}, cfg, nil
}

// do sends a request and decodes a JSON answer. Upstream error bodies are never surfaced.
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		var e *apperr.Error
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			e = apperr.New(apperr.CodeForbidden, "upstream refused the credentials (%d)", resp.StatusCode)
		case http.StatusTooManyRequests:
			e = apperr.New(apperr.CodeRateLimited, "upstream rate limited the query")
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			e = apperr.New(apperr.CodeInvalid, "upstream rejected the query (%d); check the binding", resp.StatusCode)
		default:
			e = apperr.New(apperr.CodeUnavailable, "upstream answered %d", resp.StatusCode)
		}
		return &statusError{status: resp.StatusCode, err: e}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, safehttp.ErrTooLarge) {
			return apperr.New(apperr.CodeUnavailable, "upstream response exceeded the size budget")
		}
		return apperr.New(apperr.CodeUnavailable, "upstream returned an unexpected body")
	}
	return nil
}

// statusError is a refused upstream request: a safe message plus the HTTP status, so adapters can tell, for example,
// a missing tenant (401) from a missing permission (403). The upstream body is never kept.
type statusError struct {
	status int
	err    *apperr.Error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// upstreamStatus returns the HTTP status of a refused upstream request, or 0.
func upstreamStatus(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}
