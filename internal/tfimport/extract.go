package tfimport

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/ingest"
)

// Diagnostics describe an extraction without any attribute values.
type Diagnostics struct {
	Format           string         `json:"format"`
	TerraformVersion string         `json:"terraform_version,omitempty"`
	Serial           int64          `json:"serial"`
	Resources        int            `json:"resources"`
	Mapped           map[string]int `json:"mapped" doc:"Mapped resources by type."`
	Unsupported      map[string]int `json:"unsupported" doc:"Resource types without a mapping (nothing persisted)."`
	DroppedSensitive int            `json:"dropped_sensitive" doc:"Allowlisted fields skipped because Terraform marks them sensitive."`
	DroppedSecrets   int            `json:"dropped_secret_like" doc:"Allowlisted values refused because they look like key material."`
	Relations        int            `json:"relations"`
	Inferred         int            `json:"inferred"`
	Messages         []string       `json:"messages"`
}

// TargetLookup resolves an inference target among existing catalog entities (outside this state).
type TargetLookup func(ctx context.Context, kinds []string, pointer string, value string) (id string, ok bool)

// Options tune extraction.
type Options struct {
	// Workspace labels every entity (terraform/workspace) and prefixes external IDs to keep states apart.
	Workspace string
	// InferDependencies turns untyped Terraform dependencies between mapped resources into depends_on (inferred).
	InferDependencies bool
	Lookup            TargetLookup
}

type extracted struct {
	res   Resource
	m     *Mapping
	item  catalog.EntityItem
	attrs map[string]any // allowlisted + join attributes only (memory only)
	eff   map[string]any // the item's fields as an object (for pointer joins)
}

var secretMaterial = regexp.MustCompile(`-----BEGIN [A-Z ]*(PRIVATE KEY|ENCRYPTED)|^(AKIA|ASIA)[A-Z0-9]{16}$|^gh[pousr]_[A-Za-z0-9]{30,}|^xox[abp]-`)

func sensitive(res Resource, attrPath string) bool {
	parts := strings.Split(attrPath, ".")
	for _, sp := range res.Sensitive {
		if len(sp) == 1 && sp[0] == "*" {
			return true
		}
		// A sensitive path covers itself and everything below it; reading a parent of a sensitive path is refused too.
		n := min(len(sp), len(parts))
		match := true
		for i := range n {
			if sp[i] != parts[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func scrub(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		if secretMaterial.MatchString(t) || len(t) > 4096 {
			return nil, false
		}
		return t, true
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, true
		}
		f, _ := t.Float64()
		return f, true
	case []any:
		var out []any
		for _, x := range t {
			s, ok := scrub(x)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		if len(out) > 100 {
			out = out[:100]
		}
		return out, true
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			s, ok := scrub(x)
			if !ok {
				return nil, false
			}
			out[k] = s
		}
		return out, true
	}
	return v, true
}

func setPtr(root map[string]any, ptr string, v any) {
	parts := catalog.SplitPointer(ptr)
	cur := root
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

func valuesOf(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, x := range t {
			out = append(out, valuesOf(x)...)
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{strings.ToLower(strings.TrimSuffix(t, "."))}
	}
	return []string{strings.ToLower(fmt.Sprint(v))}
}

// Extract builds catalog items from a state. It never returns attribute values outside allowlisted fields.
func Extract(ctx context.Context, st *State, cat *Catalog, o Options) (ingest.Batch, *Diagnostics) {
	d := &Diagnostics{Format: st.Format, TerraformVersion: st.TerraformVersion, Serial: st.Serial, Resources: len(st.Resources),
		Mapped: map[string]int{}, Unsupported: map[string]int{}, Messages: []string{}}
	var items []*extracted
	byAddr := map[string]*extracted{}
	// Dependencies name resources without an index; a counted or for_each resource is every one of its instances.
	byBase := map[string][]*extracted{}
	names := map[string]int{}
	extPrefix := ""
	if o.Workspace != "" {
		extPrefix = o.Workspace + ":"
	}
	for _, res := range st.Resources {
		m, ok := cat.Lookup(res.Type)
		if !ok {
			d.Unsupported[res.Type]++
			continue
		}
		d.Mapped[res.Type]++
		x := &extracted{res: res, m: m, attrs: map[string]any{}, eff: map[string]any{}}
		// Read every allowlisted attribute path individually (sensitivity check + secret scrub); nothing else is touched.
		allow := map[string]bool{}
		jsonPaths := map[string]bool{}
		for _, expr := range m.Fields {
			for _, alt := range strings.Split(expr, "|") {
				if op, arg, ok := strings.Cut(strings.TrimSpace(alt), ":"); ok && op == "json" {
					attr, _, _ := strings.Cut(arg, ".")
					jsonPaths[attr] = true
				}
			}
			for _, p := range pathsOf(expr) {
				allow[p] = true
			}
		}
		for _, j := range append(append([]string{"id"}, m.Join...), relAttrs(m)...) {
			allow[strings.TrimSpace(j)] = true
		}
		raw := map[string]any{} // json-encoded attributes, memory only, never exposed to templates
		for p := range allow {
			if sensitive(res, p) {
				if _, present := getPath(res.Attributes, p); present {
					d.DroppedSensitive++
				}
				continue
			}
			v, ok := getPath(res.Attributes, p)
			if !ok || v == nil {
				continue
			}
			if jsonPaths[p] {
				if s, isStr := v.(string); isStr && len(s) <= 1<<20 {
					raw[p] = s
				}
				continue
			}
			sv, ok := scrub(v)
			if !ok {
				d.DroppedSecrets++
				continue
			}
			x.attrs[p] = sv
		}
		tree := allowlistedTree(x.attrs)
		for k, v := range raw {
			tree[k] = v
		}
		ptrs := make([]string, 0, len(m.Fields))
		for ptr := range m.Fields {
			ptrs = append(ptrs, ptr)
		}
		sort.Strings(ptrs)
		for _, ptr := range ptrs {
			v, ok := resolve(tree, m.Fields[ptr])
			if !ok {
				continue
			}
			sv, ok := scrub(v)
			if !ok {
				d.DroppedSecrets++
				continue
			}
			setPtr(x.eff, ptr, sv)
		}
		tdata := map[string]any{"name": res.Name, "index": strings.Trim(res.Index, `"`), "module": strings.TrimPrefix(res.Module, "module."),
			"address": res.Address, "type": res.Type, "attr": allowlistedTree(x.attrs)}
		name := ""
		if m.Name != "" {
			if s, err := render(m.Name, tdata); err == nil {
				name = Slug(s)
			}
		}
		if name == "" {
			name = Slug(res.Name)
		}
		if res.Index != "" && m.Name == "" {
			name += "-" + Slug(strings.Trim(res.Index, `"`))
		}
		if res.Module != "" && m.Name == "" {
			name = Slug(strings.ReplaceAll(strings.TrimPrefix(res.Module, "module."), ".module.", "-")) + "-" + name
		}
		if name == "" || !catalog.ValidName(name) {
			name = Slug(res.Address)
		}
		key := m.Kind + "/" + name
		names[key]++
		if names[key] > 1 {
			name = fmt.Sprintf("%s-%d", name, names[key])
			d.Messages = append(d.Messages, fmt.Sprintf("%s: name collision, renamed to %s", res.Address, name))
		}
		md, _ := x.eff["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
		}
		if m.DisplayName != "" {
			if s, err := render(m.DisplayName, tdata); err == nil && strings.TrimSpace(s) != "" && len(s) <= 256 {
				md["displayName"] = strings.TrimSpace(s)
			}
		}
		if m.Icon != "" {
			md["icon"] = m.Icon
		}
		labels := map[string]any{"terraform/type": res.Type}
		if o.Workspace != "" {
			labels["terraform/workspace"] = Slug(o.Workspace)
		}
		for k, v := range m.Labels {
			labels[k] = v
		}
		md["labels"] = labels
		md["annotations"] = map[string]any{"terraform/address": res.Address}
		spec, _ := x.eff["spec"].(map[string]any)
		x.item = catalog.EntityItem{Kind: m.Kind, Name: name, ExternalID: extPrefix + res.Address, Metadata: md, Spec: spec}
		items = append(items, x)
		byBase[stripIndex(res.Address)] = append(byBase[stripIndex(res.Address)], x)
		byAddr[res.Address] = x
	}
	var b ingest.Batch
	for _, x := range items {
		b.Entities = append(b.Entities, x.item)
	}
	seenRel := map[string]bool{}
	addRel := func(it catalog.RelationItem) {
		k := it.Type + "|" + it.From.String() + "|" + it.To.String()
		if seenRel[k] {
			return
		}
		seenRel[k] = true
		b.Relations = append(b.Relations, it)
		d.Relations++
		if it.Inferred {
			d.Inferred++
		}
	}
	ref := func(x *extracted) catalog.Ref { return catalog.Ref{ExternalID: x.item.ExternalID} }
	for _, x := range items {
		for _, r := range x.m.Relations {
			var vals []string
			if v, ok := resolve(allowlistedTree(x.attrs), r.Attribute); ok {
				vals = valuesOf(v)
			}
			for _, val := range vals {
				matched := false
				for _, y := range items {
					if y == x || !contains(r.TargetKinds, y.m.Kind) {
						continue
					}
					var cand []string
					if r.TargetAttribute != "" {
						if v, ok := resolve(allowlistedTree(y.attrs), r.TargetAttribute); ok {
							cand = valuesOf(v)
						}
					} else {
						v, _ := catalog.GetPointer(y.eff, r.TargetPointer)
						cand = valuesOf(v)
					}
					if !contains(cand, val) {
						continue
					}
					matched = true
					from, to := ref(x), ref(y)
					if r.Direction == "in" {
						from, to = to, from
					}
					conf := r.Confidence
					if conf == 0 {
						conf = 0.9
					}
					ev := map[string]any{"rule": "attribute_join", "attribute": r.Attribute, "value": val, "source_resource": x.res.Address,
						"target_resource": y.res.Address}
					if r.TargetAttribute != "" {
						ev["target_attribute"] = r.TargetAttribute
					} else {
						ev["target_pointer"] = r.TargetPointer
					}
					addRel(catalog.RelationItem{Type: r.Type, From: from, To: to, Inferred: conf < 1, Confidence: &conf, Evidence: ev,
						EvidenceClass: evidenceFor(conf), ExternalID: "join:" + r.Type + ":" + x.item.ExternalID + "->" + y.item.ExternalID})
				}
				if !matched && r.TargetPointer != "" && o.Lookup != nil {
					if id, ok := o.Lookup(ctx, r.TargetKinds, r.TargetPointer, val); ok {
						from, to := ref(x), catalog.Ref{ID: id}
						if r.Direction == "in" {
							from, to = to, from
						}
						conf := r.Confidence
						if conf == 0 {
							conf = 0.8
						}
						addRel(catalog.RelationItem{Type: r.Type, From: from, To: to, Inferred: true, Confidence: &conf, EvidenceClass: catalog.EvidenceInferred,
							Evidence: map[string]any{"rule": "attribute_join_catalog", "attribute": r.Attribute, "value": val, "target_pointer": r.TargetPointer,
								"source_resource": x.res.Address}, ExternalID: "join:" + r.Type + ":" + x.item.ExternalID + "->" + id})
					}
				}
			}
		}
		var deps []*extracted
		for _, dep := range x.res.Dependencies {
			if y, ok := byAddr[dep]; ok {
				deps = append(deps, y)
			} else {
				deps = append(deps, byBase[dep]...)
			}
		}
		for _, y := range deps {
			dep := y.res.Address
			if y == x {
				continue
			}
			typed := false
			for _, dr := range x.m.Dependencies {
				if ok, _ := matchType(dr.Resource, y.res.Type); ok {
					from, to := ref(x), ref(y)
					if dr.Direction == "in" {
						from, to = to, from
					}
					one := 1.0
					addRel(catalog.RelationItem{Type: dr.Type, From: from, To: to, Confidence: &one, EvidenceClass: catalog.EvidenceStructural,
						Evidence:   map[string]any{"rule": "terraform_dependency", "dependency": dep, "source_resource": x.res.Address},
						ExternalID: "dep:" + dr.Type + ":" + x.item.ExternalID + "->" + y.item.ExternalID})
					typed = true
				}
			}
			if !typed && o.InferDependencies {
				c := 0.6
				addRel(catalog.RelationItem{Type: "depends_on", From: ref(x), To: ref(y), Inferred: true, Confidence: &c, EvidenceClass: catalog.EvidenceInferred,
					Evidence:   map[string]any{"rule": "terraform_dependency_untyped", "dependency": dep, "source_resource": x.res.Address},
					ExternalID: "dep:depends_on:" + x.item.ExternalID + "->" + y.item.ExternalID})
			}
		}
	}
	return b, d
}

func evidenceFor(conf float64) string {
	if conf >= 1 {
		return catalog.EvidenceStructural
	}
	return catalog.EvidenceInferred
}

func matchType(pattern, typ string) (bool, error) {
	if pattern == typ {
		return true, nil
	}
	if strings.ContainsAny(pattern, "*?") {
		return regexp.MatchString("^"+strings.ReplaceAll(strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*"), `\?`, ".")+"$", typ)
	}
	return false, nil
}

func relAttrs(m *Mapping) []string {
	var out []string
	for _, r := range m.Relations {
		out = append(out, strings.Split(r.Attribute, "|")...)
	}
	return out
}

// allowlistedTree exposes allowlisted attributes to name templates as nested maps ("tags.Name" → attr.tags.Name).
func allowlistedTree(attrs map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range attrs {
		if strings.ContainsAny(k, "|:") {
			continue
		}
		parts := strings.Split(k, ".")
		cur := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := cur[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[p] = next
			}
			cur = next
		}
		cur[parts[len(parts)-1]] = v
	}
	return out
}

func stripIndex(addr string) string {
	if i := strings.LastIndexByte(addr, '['); i > 0 && strings.HasSuffix(addr, "]") {
		return addr[:i]
	}
	return addr
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
