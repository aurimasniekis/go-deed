package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// Schema limits for custom kinds (ADR-002, Q-53).
const (
	MaxSchemaBytes      = 64 << 10
	MaxSchemaDepth      = 16
	MaxSchemaProperties = 512
	MaxPatternLen       = 512
)

// allowedFormats are the only "format" values asserted; others are rejected to keep validation predictable.
var allowedFormats = []string{"date-time", "date", "time", "duration", "email", "hostname", "ipv4", "ipv6", "uri",
	"uri-reference", "uuid", "regex", "json-pointer"}

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote $ref %q is not allowed in kind schemas", url)
}

// CompiledSchema is a validated, compiled kind schema version.
type CompiledSchema struct {
	Raw    map[string]any
	schema *jsonschema.Schema
}

// ParseSchema decodes and checks a schema document against the limits, then compiles it.
func ParseSchema(doc []byte) (*CompiledSchema, error) {
	if len(doc) > MaxSchemaBytes {
		return nil, apperr.New(apperr.CodeInvalid, "schema exceeds %d bytes", MaxSchemaBytes)
	}
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "schema is not valid JSON")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalid, "schema must be a JSON object")
	}
	if t, ok := obj["type"]; ok && t != "object" {
		return nil, apperr.New(apperr.CodeInvalid, `schema "type" must be "object" (spec is always an object)`)
	}
	if s, ok := obj["$schema"].(string); ok && !strings.Contains(s, "2020-12") {
		return nil, apperr.New(apperr.CodeInvalid, "only JSON Schema draft 2020-12 is supported")
	}
	props := 0
	if err := checkLimits(obj, 1, &props); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalid, err, "schema rejected")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(denyLoader{})
	c.AssertFormat()
	c.UseRegexpEngine(func(s string) (jsonschema.Regexp, error) {
		if len(s) > MaxPatternLen {
			return nil, fmt.Errorf("pattern longer than %d characters", MaxPatternLen)
		}
		return regexp.Compile(s) // RE2: linear time, no catastrophic backtracking
	})
	const loc = "urn:deed:kind-schema"
	if err := c.AddResource(loc, obj); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalid, err, "schema rejected")
	}
	sch, err := c.Compile(loc)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "schema does not compile: %s", firstLine(err.Error()))
	}
	return &CompiledSchema{Raw: obj, schema: sch}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func checkLimits(v any, depth int, props *int) error {
	if depth > MaxSchemaDepth {
		return fmt.Errorf("schema nesting deeper than %d", MaxSchemaDepth)
	}
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch k {
			case "$ref", "$dynamicRef":
				s, _ := x.(string)
				if !strings.HasPrefix(s, "#") {
					return fmt.Errorf("%s %q: only local references (#…) are allowed", k, s)
				}
			case "$id":
				return errors.New("$id is not allowed in kind schemas")
			case "pattern":
				if s, _ := x.(string); len(s) > MaxPatternLen {
					return fmt.Errorf("pattern longer than %d characters", MaxPatternLen)
				}
			case "format":
				if s, _ := x.(string); !slices.Contains(allowedFormats, s) {
					return fmt.Errorf("format %q is not supported (allowed: %s)", s, strings.Join(allowedFormats, ", "))
				}
			case "properties", "patternProperties":
				if m, ok := x.(map[string]any); ok {
					*props += len(m)
					if *props > MaxSchemaProperties {
						return fmt.Errorf("more than %d properties", MaxSchemaProperties)
					}
					for pk := range m {
						if strings.Contains(strings.ToLower(pk), "secret") || strings.Contains(strings.ToLower(pk), "password") {
							// Secret-looking properties are allowed only as secretRef objects; validation enforces that on values.
							continue
						}
					}
				}
			}
			if err := checkLimits(x, depth+1, props); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := checkLimits(x, depth+1, props); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks a spec object and returns field errors (JSON pointers into spec).
func (c *CompiledSchema) Validate(spec map[string]any) []apperr.FieldError {
	if c == nil {
		return nil
	}
	// The validator expects JSON-decoded values (json.Number-free float64 is fine).
	norm, err := normalizeJSON(spec)
	if err != nil {
		return []apperr.FieldError{{Field: "spec", Message: "not representable as JSON"}}
	}
	err = c.schema.Validate(norm)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []apperr.FieldError{{Field: "spec", Message: firstLine(err.Error())}}
	}
	var out []apperr.FieldError
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if u.Error != nil && len(u.Errors) == 0 {
			out = append(out, apperr.FieldError{Field: "spec" + u.InstanceLocation, Message: u.Error.String()})
		}
		for _, e := range u.Errors {
			walk(e)
		}
	}
	walk(*ve.BasicOutput())
	if len(out) == 0 {
		out = []apperr.FieldError{{Field: "spec", Message: firstLine(ve.Error())}}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}

// CompatibilityProblems compares a new schema version to the storage version and lists changes that could reject
// data valid under the old version (ADR-002 evolution rules). An empty list means the new version is a backward
// compatible superset: only optional additions, relaxed constraints and widened enums/types.
func CompatibilityProblems(oldS, newS map[string]any) []string {
	var probs []string
	compat("", oldS, newS, &probs)
	return probs
}

func compat(path string, o, n map[string]any, probs *[]string) {
	at := path
	if at == "" {
		at = "/"
	}
	add := func(format string, args ...any) { *probs = append(*probs, at+": "+fmt.Sprintf(format, args...)) }
	// Keywords that are compared for exact equality because their relaxation semantics are not analyzed.
	for _, k := range []string{"pattern", "const", "format", "$ref", "allOf", "anyOf", "oneOf", "not", "if", "then", "else",
		"dependentRequired", "dependentSchemas", "propertyNames", "contains", "uniqueItems", "multipleOf", "patternProperties"} {
		ov, oOK := o[k]
		nv, nOK := n[k]
		if nOK && (!oOK || !jsonEqual(ov, nv)) {
			add("%s added or changed", k)
		}
	}
	if ot, nt := typeSet(o["type"]), typeSet(n["type"]); nt != nil {
		if ot == nil {
			add("type constraint added")
		} else {
			for t := range ot {
				if !nt[t] && !(t == "integer" && nt["number"]) {
					add("type %q no longer accepted", t)
				}
			}
		}
	}
	if ne, ok := n["enum"].([]any); ok {
		oe, ook := o["enum"].([]any)
		if !ook {
			add("enum added")
		} else {
			for _, v := range oe {
				if !slices.ContainsFunc(ne, func(x any) bool { return jsonEqual(x, v) }) {
					add("enum value %s removed", compact(v))
				}
			}
		}
	}
	lower := func(k string) { // new lower bound must not be higher
		if nv, ok := num(n[k]); ok {
			if ov, ok := num(o[k]); !ok || nv > ov {
				add("%s raised or added", k)
			}
		}
	}
	upper := func(k string) { // new upper bound must not be lower
		if nv, ok := num(n[k]); ok {
			if ov, ok := num(o[k]); !ok || nv < ov {
				add("%s lowered or added", k)
			}
		}
	}
	for _, k := range []string{"minimum", "exclusiveMinimum", "minLength", "minItems", "minProperties"} {
		lower(k)
	}
	for _, k := range []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems", "maxProperties"} {
		upper(k)
	}
	oreq, nreq := stringSet(o["required"]), stringSet(n["required"])
	for r := range nreq {
		if !oreq[r] {
			add("property %q became required", r)
		}
	}
	op, _ := o["properties"].(map[string]any)
	np, _ := n["properties"].(map[string]any)
	oAdd, oAddSet := o["additionalProperties"]
	nAdd, nAddSet := n["additionalProperties"]
	for name, ov := range op {
		nv, ok := np[name]
		if !ok {
			// A removed property is now governed by additionalProperties.
			if nAddSet && nAdd == false {
				add("property %q removed while additionalProperties is false", name)
			}
			continue
		}
		om, _ := ov.(map[string]any)
		nm, _ := nv.(map[string]any)
		if om != nil && nm != nil {
			compat(path+"/properties/"+name, om, nm, probs)
		} else if !jsonEqual(ov, nv) {
			add("property %q schema changed", name)
		}
	}
	for name, nv := range np {
		if _, ok := op[name]; ok {
			continue
		}
		// A new property is safe only if old data could not have used it with a different shape.
		if !oAddSet || oAdd == true {
			if nm, ok := nv.(map[string]any); !ok || len(nm) > 0 {
				add("property %q added with constraints while old data could already contain it (additionalProperties allowed)", name)
			}
		}
	}
	if nAddSet && !jsonEqual(oAdd, nAdd) {
		switch {
		case nAdd == true:
		case oAddSet && oAdd == false:
			// was closed; any new definition is at least as permissive for existing data
		default:
			add("additionalProperties restricted")
		}
	}
	if ni, ok := n["items"].(map[string]any); ok {
		if oi, ok := o["items"].(map[string]any); ok {
			compat(path+"/items", oi, ni, probs)
		} else {
			add("items constraint added")
		}
	}
}

func typeSet(v any) map[string]bool {
	switch t := v.(type) {
	case string:
		return map[string]bool{t: true}
	case []any:
		m := map[string]bool{}
		for _, x := range t {
			if s, ok := x.(string); ok {
				m[s] = true
			}
		}
		return m
	}
	return nil
}

func stringSet(v any) map[string]bool {
	m := map[string]bool{}
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				m[s] = true
			}
		}
	}
	return m
}

func num(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return math.NaN(), false
}
