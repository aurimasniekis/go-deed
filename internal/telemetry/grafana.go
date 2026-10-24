package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// Grafana provides links: dashboard search through the Grafana API, or dashboard URLs built from a UID. Grafana is
// not treated as a metrics store; query its data sources directly (prometheus/loki adapters) or through the
// data-source proxy URL with the prometheus adapter.
//
// Query language: `search <terms>`, `search tag:<tag>` or `dashboard <uid>[?var-name=value&…]`.
type Grafana struct{}

const grafanaSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {` + httpSchemaProps + `,
    "public_url": {"type": "string", "format": "uri", "maxLength": 2048, "description": "Base URL for links shown to users (default: url)."}
  }
}`

var uidRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// Info implements Adapter.
func (Grafana) Info() AdapterInfo {
	return AdapterInfo{Type: "grafana", Title: "Grafana", Description: "Dashboard links for entities: API search by title or tag (service-account " +
		"token with Viewer role), or a dashboard UID with template variables. For metrics, query the data source directly.",
		ConfigSchema: json.RawMessage(grafanaSchema), SecretFields: []string{"token"}, Shapes: []string{ShapeLink},
		QueryHelp: "`search {{name}}`, `search tag:{{team}}` or `dashboard abc123?var-host={{hostname}}`.",
		Verified:  "contract tests against Grafana HTTP API fixtures; docker-compose Grafana validation in scripts/e2e.sh"}
}

// Validate implements Adapter.
func (Grafana) Validate(cfg json.RawMessage, _ map[string]string) error {
	if err := validateHTTP(cfg); err != nil {
		return err
	}
	var c struct {
		PublicURL string `json:"public_url"`
	}
	_ = json.Unmarshal(cfg, &c)
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return apperr.New(apperr.CodeInvalid, "public_url must be an http(s) URL")
		}
	}
	return nil
}

func parseGrafana(text string) (verb, arg string, err error) {
	verb, arg, ok := strings.Cut(strings.TrimSpace(text), " ")
	arg = strings.TrimSpace(arg)
	switch {
	case !ok || arg == "":
		return "", "", fmt.Errorf("expected `search <terms>` or `dashboard <uid>`")
	case verb == "search":
		if len(arg) > 200 {
			return "", "", fmt.Errorf("search terms too long")
		}
	case verb == "dashboard":
		uid, q, _ := strings.Cut(arg, "?")
		if !uidRE.MatchString(uid) {
			return "", "", fmt.Errorf("invalid dashboard UID")
		}
		if _, err := url.ParseQuery(q); err != nil {
			return "", "", fmt.Errorf("invalid dashboard variables")
		}
	default:
		return "", "", fmt.Errorf("unknown verb %q", verb)
	}
	return verb, arg, nil
}

// ValidateQuery implements Adapter.
func (Grafana) ValidateQuery(text, _ string) error {
	_, _, err := parseGrafana(text)
	return err
}

// Escape implements Adapter: values are URL query components.
func (Grafana) Escape(v string) string { return url.QueryEscape(v) }

func publicBase(c Conn) string {
	var cfg struct {
		URL       string `json:"url"`
		PublicURL string `json:"public_url"`
	}
	_ = json.Unmarshal(c.Config, &cfg)
	if cfg.PublicURL != "" {
		return strings.TrimSuffix(cfg.PublicURL, "/")
	}
	return strings.TrimSuffix(cfg.URL, "/")
}

// Test implements Adapter.
func (Grafana) Test(ctx context.Context, c Conn) (string, error) {
	cl, _, err := newHTTPClient(c, 1<<20)
	if err != nil {
		return "", err
	}
	var h struct {
		Version  string `json:"version"`
		Database string `json:"database"`
	}
	if err := cl.do(ctx, http.MethodGet, "/api/health", nil, nil, &h); err != nil {
		return "", err
	}
	var hits []any
	if err := cl.do(ctx, http.MethodGet, "/api/search", url.Values{"type": {"dash-db"}, "limit": {"1"}}, nil, &hits); err != nil {
		return "", err
	}
	return fmt.Sprintf("Grafana %s reachable (database %s)", h.Version, h.Database), nil
}

// Query implements Adapter.
func (Grafana) Query(ctx context.Context, c Conn, q Query) (*TelemetryData, error) {
	verb, arg, err := parseGrafana(q.Text)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "%s", err.Error())
	}
	base := publicBase(c)
	d := &TelemetryData{}
	if verb == "dashboard" {
		uid, vars, _ := strings.Cut(arg, "?")
		u := base + "/d/" + uid
		if vars != "" {
			u += "?" + vars
		}
		d.Links = []TelemetryLink{{Title: "Dashboard " + uid, URL: u, Kind: "dashboard"}}
		d.Inferred = true
		d.Note = "link built from the binding; not checked against Grafana"
		return d, nil
	}
	cl, _, err := newHTTPClient(c, 2<<20)
	if err != nil {
		return nil, err
	}
	params := url.Values{"type": {"dash-db"}, "limit": {fmt.Sprint(MaxLinks)}}
	if tag, ok := strings.CutPrefix(arg, "tag:"); ok {
		params.Set("tag", tag)
	} else {
		params.Set("query", arg)
	}
	var hits []struct {
		UID         string `json:"uid"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		FolderTitle string `json:"folderTitle"`
	}
	if err := cl.do(ctx, http.MethodGet, "/api/search", params, nil, &hits); err != nil {
		return nil, err
	}
	for _, h := range hits {
		// Grafana returns site-relative URLs; anything else is ignored.
		if !strings.HasPrefix(h.URL, "/") || strings.HasPrefix(h.URL, "//") {
			continue
		}
		title := h.Title
		if h.FolderTitle != "" {
			title = h.FolderTitle + " / " + h.Title
		}
		d.Links = append(d.Links, TelemetryLink{Title: title, URL: base + h.URL, Kind: "dashboard"})
	}
	return d, nil
}
