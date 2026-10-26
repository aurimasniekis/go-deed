package tfimport

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"

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
	// key from a JSON-encoded string attribute; "first:list" takes the first list element; "lower:attr" lowercases.
	Fields map[string]string `yaml:"fields" json:"fields"`
	// Join lists extra attribute paths kept in memory (never stored) for relationship joins.
	Join []string `yaml:"join,omitempty" json:"join,omitempty"`
	// Relations infer relationships from attribute joins.
	Relations []RelRule `yaml:"relations,omitempty" json:"relations,omitempty"`
	// Dependencies turn Terraform dependencies on given resource types into typed relationships.
	Dependencies []DepRule `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
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

// resourceRE: a resource type or glob, optionally "data." for a data source (mapped only when named explicitly).
var resourceRE = regexp.MustCompile(`^(data\.)?[a-z0-9_*?]+$`)

func (m *Mapping) validate() error {
	if !resourceRE.MatchString(m.Resource) {
		return fmt.Errorf("resource must be a type name or glob")
	}
	if m.Kind == "" {
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
	"slug":       Slug,
	"lower":      strings.ToLower,
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
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
