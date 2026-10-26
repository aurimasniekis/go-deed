// Package tfimport reads Terraform state snapshots read-only and turns them into catalog facts (ADR-006 A).
//
// Terraform state is treated as highly sensitive: it is parsed in memory with a size cap, never stored, never logged
// and never echoed in errors. Only attribute paths allowlisted by a mapping are read; paths Terraform marks as
// sensitive are dropped even when allowlisted, and values that look like key material are refused. Resource types
// without a mapping are counted in diagnostics and nothing about them is persisted.
package tfimport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// Resource is one managed resource instance extracted from a state document.
type Resource struct {
	Address string // module.x.type.name[index] (data sources: module.x.data.type.name[index])
	Module  string // module.x (empty for root)
	// Type is the resource type; data sources are prefixed "data." and only imported when a mapping names them.
	Type         string
	Name         string
	Index        string // "" or the index key rendered as a string
	Provider     string // registry.terraform.io/cloudflare/cloudflare
	Attributes   map[string]any
	Sensitive    [][]string // attribute paths (get_attr / index steps rendered as strings)
	Dependencies []string   // addresses without index
}

// State is a parsed state document (no outputs: outputs are never read).
type State struct {
	Format           string // "state-v4" or "show-json"
	TerraformVersion string
	Serial           int64
	Lineage          string
	Resources        []Resource
}

// ErrUnsupported means the document is not a supported state format.
var ErrUnsupported = apperr.New(apperr.CodeInvalid, "not a supported Terraform state: expected state version 4 or `terraform show -json` format_version 1.x")

type rawV4 struct {
	Version          int    `json:"version"`
	TerraformVersion string `json:"terraform_version"`
	Serial           int64  `json:"serial"`
	Lineage          string `json:"lineage"`
	Resources        []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []struct {
			IndexKey            any             `json:"index_key"`
			Attributes          map[string]any  `json:"attributes"`
			SensitiveAttributes json.RawMessage `json:"sensitive_attributes"`
			Dependencies        []string        `json:"dependencies"`
		} `json:"instances"`
	} `json:"resources"`
	FormatVersion string          `json:"format_version"`
	Values        json.RawMessage `json:"values"`
}

type showModule struct {
	Address   string `json:"address"`
	Resources []struct {
		Address         string         `json:"address"`
		Mode            string         `json:"mode"`
		Type            string         `json:"type"`
		Name            string         `json:"name"`
		Index           any            `json:"index"`
		ProviderName    string         `json:"provider_name"`
		Values          map[string]any `json:"values"`
		SensitiveValues map[string]any `json:"sensitive_values"`
		DependsOn       []string       `json:"depends_on"`
	} `json:"resources"`
	ChildModules []showModule `json:"child_modules"`
}

// Parse reads a state document (raw state v4 or `terraform show -json`) of at most maxBytes. Errors never contain
// state content.
func Parse(r io.Reader, maxBytes int64) (*State, error) {
	lr := &io.LimitedReader{R: r, N: maxBytes + 1}
	dec := json.NewDecoder(lr)
	dec.UseNumber()
	var raw rawV4
	if err := dec.Decode(&raw); err != nil {
		if lr.N <= 0 {
			return nil, apperr.New(apperr.CodeInvalid, "state exceeds %d bytes", maxBytes)
		}
		return nil, apperr.New(apperr.CodeInvalid, "state is not valid JSON")
	}
	if lr.N <= 0 {
		return nil, apperr.New(apperr.CodeInvalid, "state exceeds %d bytes", maxBytes)
	}
	switch {
	case raw.Version == 4:
		return parseV4(&raw), nil
	case strings.HasPrefix(raw.FormatVersion, "1."):
		return parseShow(&raw)
	}
	return nil, ErrUnsupported
}

func indexString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strconv.Quote(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func providerName(p string) string {
	// v4: provider["registry.terraform.io/cloudflare/cloudflare"] or provider["…"].alias
	if i := strings.Index(p, `["`); i >= 0 {
		if j := strings.Index(p[i+2:], `"]`); j >= 0 {
			return p[i+2 : i+2+j]
		}
	}
	return p
}

func parseV4(raw *rawV4) *State {
	st := &State{Format: "state-v4", TerraformVersion: raw.TerraformVersion, Serial: raw.Serial, Lineage: raw.Lineage}
	for _, r := range raw.Resources {
		typ := r.Type
		switch r.Mode {
		case "managed":
		case "data":
			// Data sources describe lookups, not owned infrastructure: kept only for mappings that name them explicitly
			// (a zone or host that is only looked up, never managed, in this state).
			typ = "data." + r.Type
		default:
			continue
		}
		for _, in := range r.Instances {
			idx := indexString(in.IndexKey)
			addr := typ + "." + r.Name
			if r.Module != "" {
				addr = r.Module + "." + addr
			}
			if idx != "" {
				addr += "[" + idx + "]"
			}
			res := Resource{Address: addr, Module: r.Module, Type: typ, Name: r.Name, Index: idx, Provider: providerName(r.Provider),
				Attributes: in.Attributes, Dependencies: in.Dependencies}
			res.Sensitive = parseSensitiveV4(in.SensitiveAttributes)
			st.Resources = append(st.Resources, res)
		}
	}
	sortResources(st)
	return st
}

// parseSensitiveV4 reads [[{"type":"get_attr","value":"password"},{"type":"index","value":{"value":0,"type":"number"}}], …].
func parseSensitiveV4(b json.RawMessage) [][]string {
	if len(b) == 0 {
		return nil
	}
	var paths [][]struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(b, &paths); err != nil {
		return [][]string{{"*"}} // unknown shape: treat everything as sensitive
	}
	var out [][]string
	for _, p := range paths {
		var steps []string
		for _, s := range p {
			switch s.Type {
			case "get_attr":
				steps = append(steps, fmt.Sprint(s.Value))
			case "index":
				if m, ok := s.Value.(map[string]any); ok {
					steps = append(steps, fmt.Sprint(m["value"]))
				} else {
					steps = append(steps, fmt.Sprint(s.Value))
				}
			}
		}
		if len(steps) > 0 {
			out = append(out, steps)
		}
	}
	return out
}

func parseShow(raw *rawV4) (*State, error) {
	var v struct {
		RootModule showModule `json:"root_module"`
	}
	if len(raw.Values) == 0 {
		return &State{Format: "show-json"}, nil
	}
	if err := json.Unmarshal(raw.Values, &v); err != nil {
		return nil, ErrUnsupported
	}
	st := &State{Format: "show-json"}
	var walk func(m showModule)
	walk = func(m showModule) {
		for _, r := range m.Resources {
			typ := r.Type
			switch r.Mode {
			case "managed":
			case "data":
				typ = "data." + r.Type
			default:
				continue
			}
			idx := indexString(r.Index)
			deps := make([]string, 0, len(r.DependsOn))
			deps = append(deps, r.DependsOn...)
			res := Resource{Address: r.Address, Module: m.Address, Type: typ, Name: r.Name, Index: idx, Provider: r.ProviderName,
				Attributes: r.Values, Dependencies: deps}
			res.Sensitive = sensitiveFromValues(r.SensitiveValues, nil)
			st.Resources = append(st.Resources, res)
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(v.RootModule)
	sortResources(st)
	return st, nil
}

// sensitiveFromValues converts show-json sensitive_values ({"password": true, "list": [false, true]}) into paths.
func sensitiveFromValues(v any, prefix []string) [][]string {
	var out [][]string
	switch t := v.(type) {
	case bool:
		if t {
			out = append(out, append([]string(nil), prefix...))
		}
	case map[string]any:
		for k, x := range t {
			out = append(out, sensitiveFromValues(x, append(prefix, k))...)
		}
	case []any:
		for i, x := range t {
			out = append(out, sensitiveFromValues(x, append(prefix, strconv.Itoa(i)))...)
		}
	}
	return out
}

func sortResources(st *State) {
	sort.SliceStable(st.Resources, func(i, j int) bool { return st.Resources[i].Address < st.Resources[j].Address })
}
