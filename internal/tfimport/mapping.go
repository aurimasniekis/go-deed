package tfimport

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/publicsuffix"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

//go:embed mappings/*.yaml
var mappingFS embed.FS

// Mapping maps one Terraform resource type (or glob) to a kind with allowlisted attributes.
type Mapping struct {
	Resource string `yaml:"resource" json:"resource"`
	Kind     string `yaml:"kind" json:"kind"`
	Icon     string `yaml:"icon,omitempty" json:"icon,omitempty"`
	// Name is a template over {name, index, module, address, attr} rendered into a slug; default "{{.name}}".
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// DisplayName is an optional template for metadata.displayName.
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	// Fields maps target JSON pointers to attribute paths; "a|b" falls back left to right; "json:attr.key" reads a
	// key from a JSON-encoded string attribute; "first:list" takes the first list element; "lower:attr" lowercases;
	// "int:attr" takes a whole number only (a number or a numeric string, never an object); "time:attr" reads a
	// timestamp (RFC 3339, Go's time.String form "2041-10-21 13:26:00 +0000 UTC", or a date) as RFC 3339 in UTC.
	Fields map[string]string `yaml:"fields" json:"fields"`
	// Join lists extra attribute paths kept in memory (never stored) for relationship joins.
	Join []string `yaml:"join,omitempty" json:"join,omitempty"`
	// Relations infer relationships from attribute joins.
	Relations []RelRule `yaml:"relations,omitempty" json:"relations,omitempty"`
	// Dependencies turn Terraform dependencies on given resource types into typed relationships.
	Dependencies []DepRule `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	// Derive declares synthetic resources built from this resource's allowlisted attributes, mapped by their own
	// mapping (for example a DNS zone derived from its records when the state only passes the zone ID around).
	Derive []Derive `yaml:"derive,omitempty" json:"derive,omitempty"`
	// DeriveOnly maps a resource only for its Derive rules (a zone setting that names its zone): no entity of its own.
	DeriveOnly bool `yaml:"derive_only,omitempty" json:"derive_only,omitempty"`
	// Environment and Labels set static metadata.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// RelRule infers a relationship when an attribute of this resource equals a value of a target.
type RelRule struct {
	Type string `yaml:"type" json:"type"`
	// Direction "out" (default): this → target; "in": target → this.
	Direction string `yaml:"direction,omitempty" json:"direction,omitempty"`
	// Attribute is this resource's attribute path (list values match element-wise).
	Attribute string `yaml:"attribute" json:"attribute"`
	// TargetKinds restricts targets.
	TargetKinds []string `yaml:"target_kinds" json:"target_kinds"`
	// TargetAttribute matches a raw attribute of a resource in the same state (e.g. zone_id → id).
	TargetAttribute string `yaml:"target_attribute,omitempty" json:"target_attribute,omitempty"`
	// TargetPointer matches an effective pointer of a mapped target (same state) or an existing entity (catalog).
	TargetPointer string `yaml:"target_pointer,omitempty" json:"target_pointer,omitempty"`
	// Confidence of the inference (default 0.9 for pointer joins, 1.0 for id joins).
	Confidence float64 `yaml:"confidence,omitempty" json:"confidence,omitempty"`
}

// Derive is a synthetic resource ("derived.<type>") built from the attributes of the resources that declare it: one per
// distinct Key (a template), with Attributes rendered from templates and "id" set to the key. It is skipped when a
// resource of one of UnlessTypes in the same state has that id.
type Derive struct {
	Resource    string            `yaml:"resource" json:"resource"`
	Key         string            `yaml:"key" json:"key"`
	Attributes  map[string]string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	UnlessTypes []string          `yaml:"unless_types,omitempty" json:"unless_types,omitempty"`
}

// DepRule types a Terraform dependency.
type DepRule struct {
	Resource  string `yaml:"resource" json:"resource"`
	Type      string `yaml:"type" json:"type"`
	Direction string `yaml:"direction,omitempty" json:"direction,omitempty"`
}

// Catalog is a set of mappings.
type Catalog struct {
	byType []Mapping
}

// Builtin returns the embedded mapping catalog.
func Builtin() (*Catalog, error) {
	entries, err := mappingFS.ReadDir("mappings")
	if err != nil {
		return nil, err
	}
	c := &Catalog{}
	for _, e := range entries {
		b, err := mappingFS.ReadFile("mappings/" + e.Name())
		if err != nil {
			return nil, err
		}
		var doc struct {
			Mappings []Mapping `yaml:"mappings"`
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mappings/%s: %w", e.Name(), err)
		}
		for _, m := range doc.Mappings {
			if err := m.validate(); err != nil {
				return nil, fmt.Errorf("mappings/%s %s: %w", e.Name(), m.Resource, err)
			}
			c.byType = append(c.byType, m)
		}
	}
	return c, nil
}

// MustBuiltin panics on an invalid embedded catalog (caught by tests).
func MustBuiltin() *Catalog {
	c, err := Builtin()
	if err != nil {
		panic(err)
	}
	return c
}

// With returns a catalog where custom mappings take precedence over c.
func (c *Catalog) With(custom []Mapping) (*Catalog, error) {
	out := &Catalog{}
	for _, m := range custom {
		if err := m.validate(); err != nil {
			return nil, apperr.New(apperr.CodeInvalid, "mapping %s: %s", m.Resource, err.Error())
		}
		out.byType = append(out.byType, m)
	}
	out.byType = append(out.byType, c.byType...)
	return out, nil
}

// resourceRE: a resource type or glob, optionally "data." for a data source (mapped only when named explicitly) or
// "derived." for a synthetic resource (Mapping.Derive).
var indexKeyRE = regexp.MustCompile(`\["((?:[^"\\]|\\.)*)"\]`)

var resourceRE = regexp.MustCompile(`^((data|derived)\.)?[a-z0-9_*?]+$`)

func (m *Mapping) validate() error {
	if !resourceRE.MatchString(m.Resource) {
		return fmt.Errorf("resource must be a type name or glob")
	}
	if m.DeriveOnly {
		if m.Kind != "" || len(m.Fields) > 0 || len(m.Relations) > 0 || len(m.Derive) == 0 {
			return fmt.Errorf("a derive_only mapping has derive rules and no kind, fields or relations")
		}
	} else if m.Kind == "" {
		return fmt.Errorf("kind is required")
	}
	for ptr := range m.Fields {
		if !strings.HasPrefix(ptr, "/spec/") && !strings.HasPrefix(ptr, "/metadata/") {
			return fmt.Errorf("field %q must target /spec/ or /metadata/", ptr)
		}
	}
	for _, t := range []string{m.Name, m.DisplayName} {
		if t == "" {
			continue
		}
		if len(t) > 512 {
			return fmt.Errorf("template too long")
		}
		if _, err := template.New("t").Funcs(funcs).Parse(t); err != nil {
			return fmt.Errorf("template: %w", err)
		}
	}
	for _, r := range m.Relations {
		if r.Type == "" || r.Attribute == "" || (r.TargetAttribute == "" && r.TargetPointer == "") {
			return fmt.Errorf("relation rules need type, attribute and target_attribute or target_pointer")
		}
	}
	for _, dv := range m.Derive {
		if !strings.HasPrefix(dv.Resource, "derived.") || !resourceRE.MatchString(dv.Resource) || strings.ContainsAny(dv.Resource, "*?") {
			return fmt.Errorf("derive resource must be derived.<type>")
		}
		templates := []string{dv.Key}
		for _, t := range dv.Attributes {
			templates = append(templates, t)
		}
		for _, t := range templates {
			if t == "" || len(t) > 512 {
				return fmt.Errorf("derive templates must be 1–512 characters")
			}
			if _, err := template.New("t").Funcs(funcs).Parse(t); err != nil {
				return fmt.Errorf("derive template: %w", err)
			}
		}
	}
	return nil
}

// Lookup returns the first mapping for a resource type.
func (c *Catalog) Lookup(typ string) (*Mapping, bool) {
	for i := range c.byType {
		if ok, _ := path.Match(c.byType[i].Resource, typ); ok {
			return &c.byType[i], true
		}
	}
	return nil, false
}

// KindTypes lists resource types (patterns) mapped to kind (support matrix).
func (c *Catalog) KindTypes(kind string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range c.byType {
		if m.Kind == kind && !seen[m.Resource] {
			seen[m.Resource] = true
			out = append(out, m.Resource)
		}
	}
	sort.Strings(out)
	return out
}

// All returns every mapping (documentation and UI).
func (c *Catalog) All() []Mapping { return append([]Mapping(nil), c.byType...) }

var slugRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug renders s as an entity name.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

var funcs = template.FuncMap{
	"slug": Slug,
	// first is the first element of a list attribute as a string, or "".
	"first": func(v any) string {
		if l, ok := v.([]any); ok && len(l) > 0 && l[0] != nil {
			return fmt.Sprint(l[0])
		}
		return ""
	},
	"lower":      strings.ToLower,
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	// indexKey is the last string index key in a module path or address (zone["example.com"] → example.com), or "".
	"indexKey": func(v any) string {
		m := indexKeyRE.FindAllStringSubmatch(fmt.Sprint(v), -1)
		if len(m) == 0 {
			return ""
		}
		k, err := strconv.Unquote(`"` + m[len(m)-1][1] + `"`)
		if err != nil {
			return ""
		}
		return k
	},
	// registrable is the registrable domain of a host name (public-suffix rules: app.example.co.uk → example.co.uk), or "".
	"registrable": func(v any) string {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fmt.Sprint(v))), ".")
		if host == "" || host == "<nil>" {
			return ""
		}
		d, err := publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			return ""
		}
		return d
	},
	"default": func(def string, v any) string {
		if s := fmt.Sprint(v); v != nil && s != "" && s != "<no value>" {
			return s
		}
		return def
	},
}

// getPath reads an attribute path: dotted keys and numeric indexes ("network_data.0.ip_address").
func getPath(attrs map[string]any, p string) (any, bool) {
	var cur any = attrs
	for _, part := range strings.Split(p, ".") {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// resolve evaluates a field expression against attributes. The bool reports whether a value was found.
func resolve(attrs map[string]any, expr string) (any, bool) {
	for _, alt := range strings.Split(expr, "|") {
		alt = strings.TrimSpace(alt)
		op, arg, hasOp := strings.Cut(alt, ":")
		if !hasOp {
			op, arg = "", alt
		}
		var v any
		var ok bool
		switch op {
		case "json":
			attr, key, _ := strings.Cut(arg, ".")
			raw, found := getPath(attrs, attr)
			s, isStr := raw.(string)
			if !found || !isStr || len(s) > 1<<20 {
				continue
			}
			var doc map[string]any
			if json.Unmarshal([]byte(s), &doc) != nil {
				continue
			}
			v, ok = getPath(doc, key)
		case "first":
			raw, found := getPath(attrs, arg)
			if l, isList := raw.([]any); found && isList && len(l) > 0 {
				v, ok = l[0], true
			}
		case "lower":
			raw, found := getPath(attrs, arg)
			if s, isStr := raw.(string); found && isStr {
				v, ok = strings.ToLower(s), true
			}
		case "int":
			raw, found := getPath(attrs, arg)
			if n, isInt := wholeNumber(raw); found && isInt {
				v, ok = n, true
			}
		case "time":
			raw, found := getPath(attrs, arg)
			if s, isStr := raw.(string); found && isStr {
				if t, parsed := parseTime(s); parsed {
					v, ok = t.UTC().Format(time.RFC3339), true
				}
			}
		case "const":
			v, ok = arg, true
		default:
			v, ok = getPath(attrs, alt)
		}
		if ok && v != nil && v != "" {
			return v, true
		}
	}
	return nil, false
}

// wholeNumber reads an integer from a number (scrubbed attributes hold int64) or a numeric string.
func wholeNumber(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t), true
		}
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// timeLayouts are the timestamp forms providers store: RFC 3339, Go's time.String (Cloudflare's origin CA expires_on),
// and a bare date.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05 -0700", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// pathsOf lists the attribute paths an expression reads (for sensitivity checks).
func pathsOf(expr string) []string {
	var out []string
	for _, alt := range strings.Split(expr, "|") {
		alt = strings.TrimSpace(alt)
		if op, arg, ok := strings.Cut(alt, ":"); ok {
			switch op {
			case "json":
				attr, _, _ := strings.Cut(arg, ".")
				out = append(out, attr)
			case "const":
			default:
				out = append(out, arg)
			}
			continue
		}
		out = append(out, alt)
	}
	return out
}

func render(tpl string, data map[string]any) (string, error) {
	t, err := template.New("t").Funcs(funcs).Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&limitWriter{w: &b, n: 1024}, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

type limitWriter struct {
	w *strings.Builder
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	l.w.Write(p)
	return len(p), nil
}
