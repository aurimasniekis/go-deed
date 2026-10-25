package catalog_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/catalog"
)

func nested(depth int) string {
	return strings.Repeat(`{"type":"object","properties":{"a":`, depth) + `{"type":"string"}` + strings.Repeat(`}}`, depth)
}

func manyProps(n int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"p%d":{"type":"string"}`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func TestParseSchemaLimits(t *testing.T) {
	pad := `{"type":"object","description":"` + strings.Repeat("x", catalog.MaxSchemaBytes) + `"}`
	for _, tc := range []struct {
		name, doc string
		ok        bool
	}{
		{"minimal", `{"type":"object"}`, true},
		{"too large", pad, false},
		{"not json", `{"type":`, false},
		{"not an object", `[]`, false},
		{"non-object type", `{"type":"string"}`, false},
		{"old draft", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`, false},
		{"max depth", nested(7), true},
		{"too deep", nested(8), false},
		{"max properties", manyProps(catalog.MaxSchemaProperties), true},
		{"too many properties", manyProps(catalog.MaxSchemaProperties + 1), false},
		{"remote ref", `{"type":"object","properties":{"a":{"$ref":"https://example.com/s.json"}}}`, false},
		{"relative ref", `{"type":"object","properties":{"a":{"$ref":"other.json#/x"}}}`, false},
		{"remote dynamic ref", `{"type":"object","properties":{"a":{"$dynamicRef":"https://example.com/s.json#meta"}}}`, false},
		{"local ref", `{"type":"object","$defs":{"h":{"type":"string","format":"hostname"}},"properties":{"a":{"$ref":"#/$defs/h"}}}`, true},
		{"$id", `{"type":"object","$id":"https://example.com/kind"}`, false},
		{"nested remote metaschema", `{"type":"object","properties":{"a":{"$schema":"https://example.com/meta","type":"string"}}}`, false},
		{"long pattern", `{"type":"object","properties":{"a":{"type":"string","pattern":"` + strings.Repeat("a", catalog.MaxPatternLen+1) + `"}}}`, false},
		{"long patternProperties key", `{"type":"object","patternProperties":{"` + strings.Repeat("a", catalog.MaxPatternLen+1) + `":{}}}`, false},
		{"unsupported format", `{"type":"object","properties":{"a":{"type":"string","format":"idn-email"}}}`, false},
		// Property and definition names are not keywords; default/const values are data.
		{"property named format", `{"type":"object","properties":{"format":{"type":"string","enum":["ext4","xfs"]}}}`, true},
		{"property named $ref", `{"type":"object","properties":{"$ref":{"type":"string"}}}`, true},
		{"property named $id", `{"type":"object","properties":{"$id":{"type":"string"}}}`, true},
		{"definition named format", `{"type":"object","$defs":{"format":{"type":"string"}},"properties":{"f":{"$ref":"#/$defs/format"}}}`, true},
		{"default with format key", `{"type":"object","properties":{"disk":{"type":"object","default":{"format":"ext4"}}}}`, true},
		{"bad format under a named property", `{"type":"object","properties":{"format":{"type":"string","format":"bogus"}}}`, false},
		{"remote ref under a named property", `{"type":"object","properties":{"$ref":{"$ref":"https://example.com/x"}}}`, false},
		{"deep default", `{"type":"object","default":` + strings.Repeat(`[`, catalog.MaxSchemaDepth) + strings.Repeat(`]`, catalog.MaxSchemaDepth) + `}`, false},
	} {
		_, err := catalog.ParseSchema([]byte(tc.doc))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && apperr.CodeOf(err) != apperr.CodeInvalid {
			t.Errorf("%s: error code %q (%v)", tc.name, apperr.CodeOf(err), err)
		}
	}
}

// Mirrors of the keyword classes in schema.go, used by the fuzz oracle.
var (
	namedSchemas = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true}
	dataKeywords = map[string]bool{"const": true, "enum": true, "default": true, "examples": true}
)

type schemaFacts struct {
	maxDepth   int
	props      int
	remoteRefs []string
	ids        int
}

func (f *schemaFacts) data(v any, depth int) {
	f.maxDepth = max(f.maxDepth, depth)
	switch t := v.(type) {
	case map[string]any:
		for _, x := range t {
			f.data(x, depth+1)
		}
	case []any:
		for _, x := range t {
			f.data(x, depth+1)
		}
	}
}

func (f *schemaFacts) schema(v any, depth int) {
	f.maxDepth = max(f.maxDepth, depth)
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch k {
			case "$ref", "$dynamicRef":
				if s, _ := x.(string); !strings.HasPrefix(s, "#") {
					f.remoteRefs = append(f.remoteRefs, fmt.Sprint(x))
				}
			case "$id":
				f.ids++
			case "properties", "patternProperties":
				if m, ok := x.(map[string]any); ok {
					f.props += len(m)
				}
			}
			m, isMap := x.(map[string]any)
			switch {
			case isMap && namedSchemas[k]:
				f.maxDepth = max(f.maxDepth, depth+1)
				for _, s := range m {
					f.schema(s, depth+2)
				}
			case dataKeywords[k]:
				f.data(x, depth+1)
			default:
				f.schema(x, depth+1)
			}
		}
	case []any:
		for _, x := range t {
			f.schema(x, depth+1)
		}
	}
}

func FuzzParseSchema(f *testing.F) {
	for _, s := range []string{
		`{"type":"object"}`,
		`{"type":"object","required":["hostname"],"properties":{"hostname":{"type":"string","format":"hostname"},"ipv4":{"type":"string","format":"ipv4"},"port":{"type":"integer","minimum":1,"maximum":65535}}}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{"tags":{"type":"array","items":{"type":"string","pattern":"^[a-z]+$"},"uniqueItems":true}}}`,
		`{"type":"object","$defs":{"h":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/h"},"b":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/h"}]}}}`,
		`{"type":"object","patternProperties":{"^x-":{"type":"string"}},"propertyNames":{"maxLength":32}}`,
		`{"type":"object","properties":{"a":{"$ref":"https://example.com/s.json"}}}`,
		`{"type":"object","$id":"https://example.com/k"}`,
		`{"type":"object","properties":{"format":{"type":"string"},"disk":{"type":"object","default":{"format":"ext4"}}}}`,
		`{"type":"object","properties":{"a":{"$dynamicRef":"#meta"}},"$dynamicAnchor":"meta"}`,
		`{"type":"object","if":{"properties":{"k":{"const":"vm"}}},"then":{"required":["cpus"]},"dependentSchemas":{"x":{"required":["y"]}}}`,
		nested(8),
		manyProps(catalog.MaxSchemaProperties + 1),
	} {
		f.Add([]byte(s), []byte(`{"hostname":"web-1.example.com","ipv4":"192.0.2.10","port":443,"tags":["a"]}`))
	}
	f.Add([]byte(`{"type":"object","description":"`+strings.Repeat("x", catalog.MaxSchemaBytes)+`"}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, doc, spec []byte) {
		cs, err := catalog.ParseSchema(doc)
		if len(doc) > catalog.MaxSchemaBytes && err == nil {
			t.Fatalf("schema of %d bytes accepted", len(doc))
		}
		if err != nil {
			if apperr.CodeOf(err) != apperr.CodeInvalid {
				t.Fatalf("error without CodeInvalid: %v", err)
			}
			return
		}
		raw, derr := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
		if derr != nil {
			t.Fatalf("accepted a document that does not decode: %v", derr)
		}
		var facts schemaFacts
		facts.schema(raw, 1)
		switch {
		case facts.maxDepth > catalog.MaxSchemaDepth:
			t.Fatalf("accepted depth %d", facts.maxDepth)
		case facts.props > catalog.MaxSchemaProperties:
			t.Fatalf("accepted %d properties", facts.props)
		case len(facts.remoteRefs) > 0:
			t.Fatalf("accepted non-local references %v", facts.remoteRefs)
		case facts.ids > 0:
			t.Fatal("accepted $id")
		}
		var s map[string]any
		if json.Unmarshal(spec, &s) != nil {
			return
		}
		errs := cs.Validate(s)
		if len(errs) > 50 {
			t.Fatalf("%d field errors (cap is 50)", len(errs))
		}
		for _, fe := range errs {
			if !strings.HasPrefix(fe.Field, "spec") {
				t.Fatalf("field error outside spec: %+v", fe)
			}
		}
	})
}
