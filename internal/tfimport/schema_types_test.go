package tfimport

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Every built-in mapping that fills a date-time or integer property of its kind reads it through "time:" or "int:",
// so provider formats ("2041-10-21 13:26:00 +0000 UTC", a VLAN object) never fail the kind's schema.
func TestMappingFieldTypesMatchKindSchemas(t *testing.T) {
	raw, err := os.ReadFile("../catalog/builtin/kinds.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Kinds []struct {
			Name     string `yaml:"name"`
			Versions []struct {
				Schema struct {
					Properties map[string]struct {
						Type   any    `yaml:"type"`
						Format string `yaml:"format"`
					} `yaml:"properties"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"kinds"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{} // kind/property → required op
	for _, k := range doc.Kinds {
		for _, v := range k.Versions {
			for name, p := range v.Schema.Properties {
				switch {
				case p.Format == "date-time":
					want[k.Name+"/"+name] = "time:"
				case p.Type == "integer":
					want[k.Name+"/"+name] = "int:"
				}
			}
		}
	}
	for _, m := range MustBuiltin().All() {
		for ptr, expr := range m.Fields {
			prop, ok := strings.CutPrefix(ptr, "/spec/")
			if !ok || strings.Contains(prop, "/") {
				continue
			}
			op := want[m.Kind+"/"+prop]
			if op == "" {
				continue
			}
			for _, alt := range strings.Split(expr, "|") {
				if alt = strings.TrimSpace(alt); !strings.HasPrefix(alt, op) && !strings.HasPrefix(alt, "const:") {
					t.Errorf("%s %s: %q should read through %q", m.Resource, ptr, alt, op)
				}
			}
		}
	}
}
