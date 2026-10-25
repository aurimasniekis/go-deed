package graph

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/aurimasniekis/go-deed/internal/catalog"
)

// Edge sources: which sources assert a relationship (ADR-003 layered facts), for the map's colour-by-source mode and
// the edge tooltip. Assertions are loaded in one indexed query per chunk of relationships (relation_assertions by
// relation_id) and the sources they name in one primary-key query per chunk; a source is only shown to callers who
// can read its project, so a redacted cross-project stub never reveals the other project's sources.

type sourceInfo struct {
	EdgeSource
	policyPriority *int
}

// priority is the ADR-003 precedence of an assertion made in layer by this source.
func (s *sourceInfo) priority(layer string) int {
	if s.policyPriority != nil {
		return *s.policyPriority
	}
	return catalog.Layer(layer).DefaultPriority()
}

type liveAssertion struct {
	source string
	layer  string
	first  int64
}

// rankedSource is a visible asserting source with its precedence.
type rankedSource struct {
	*sourceInfo
	prio  int
	first int64
	count int
}

func rankCompare(a, b rankedSource) int {
	return cmp.Or(cmp.Compare(b.prio, a.prio), cmp.Compare(a.first, b.first), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}

// assertionsOf returns the live (not retracted, not expired) assertions of each relationship.
func (e *Engine) assertionsOf(ctx context.Context, relIDs []string) (map[string][]liveAssertion, error) {
	out := make(map[string][]liveAssertion, len(relIDs))
	now := e.Clock.Now().UnixMicro()
	for start := 0; start < len(relIDs); start += chunk {
		part := relIDs[start:min(start+chunk, len(relIDs))]
		args := make([]any, 0, len(part)+1)
		for _, id := range part {
			args = append(args, id)
		}
		args = append(args, now)
		rows, err := e.Store.R().QueryContext(ctx, `SELECT a.relation_id, a.source_id, a.layer, a.first_seen_at FROM relation_assertions a
			WHERE a.relation_id IN (`+placeholders(len(part))+`) AND a.tombstoned_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > ?)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rel string
			var a liveAssertion
			if err := rows.Scan(&rel, &a.source, &a.layer, &a.first); err != nil {
				rows.Close()
				return nil, err
			}
			out[rel] = append(out[rel], a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sourcesOf loads the named sources that the scope may see (sources of unreadable projects are left out).
func (e *Engine) sourcesOf(ctx context.Context, scope Scope, ids []string) (map[string]*sourceInfo, error) {
	out := make(map[string]*sourceInfo, len(ids))
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := e.Store.R().QueryContext(ctx, `SELECT id, project_id, name, display_name, layer, policy_json FROM sources
			WHERE id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s sourceInfo
			var pol string
			if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.DisplayName, &s.Layer, &pol); err != nil {
				rows.Close()
				return nil, err
			}
			if !scope.can(s.ProjectID) {
				continue
			}
			if s.DisplayName == s.Name {
				s.DisplayName = ""
			}
			var p catalog.Policy
			if json.Unmarshal([]byte(pol), &p) == nil {
				s.policyPriority = p.Priority
			}
			out[s.ID] = &s
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// attachSources fills Sources and PrimarySource on edges. bundles maps an aggregated edge's ID to the relationships
// it bundles; every other edge is one relationship (its ID).
func (e *Engine) attachSources(ctx context.Context, scope Scope, edges []Edge, bundles map[string][]string) error {
	if len(edges) == 0 {
		return nil
	}
	var relIDs []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			relIDs = append(relIDs, id)
		}
	}
	for _, ed := range edges {
		if members, ok := bundles[ed.ID]; ok {
			for _, id := range members {
				add(id)
			}
		} else if ed.Count == 0 {
			add(ed.ID)
		}
	}
	asserts, err := e.assertionsOf(ctx, relIDs)
	if err != nil {
		return err
	}
	var srcIDs []string
	srcSeen := map[string]bool{}
	for _, as := range asserts {
		for _, a := range as {
			if !srcSeen[a.source] {
				srcSeen[a.source] = true
				srcIDs = append(srcIDs, a.source)
			}
		}
	}
	slices.Sort(srcIDs)
	srcs, err := e.sourcesOf(ctx, scope, srcIDs)
	if err != nil {
		return err
	}
	// ranked lists a relationship's visible sources, highest precedence first (one entry per source).
	ranked := func(relID string) []rankedSource {
		var out []rankedSource
		for _, a := range asserts[relID] {
			s, ok := srcs[a.source]
			if !ok {
				continue
			}
			out = append(out, rankedSource{sourceInfo: s, prio: s.priority(a.layer), first: a.first, count: 1})
		}
		slices.SortFunc(out, rankCompare)
		return out
	}
	for i := range edges {
		ed := &edges[i]
		members, bundled := bundles[ed.ID]
		if !bundled {
			if ed.Count > 0 {
				ed.Sources = []EdgeSource{}
				continue
			}
			rs := ranked(ed.ID)
			ed.Sources = make([]EdgeSource, 0, len(rs))
			for _, r := range rs {
				ed.Sources = append(ed.Sources, r.EdgeSource)
			}
			ed.PrimarySource = ""
			if len(rs) > 0 {
				ed.PrimarySource = rs[0].ID
			}
			continue
		}
		union := map[string]*rankedSource{}
		primary, uniform := "", true
		for _, id := range members {
			rs := ranked(id)
			if len(rs) == 0 {
				uniform = false
			} else if primary == "" {
				primary = rs[0].ID
			} else if primary != rs[0].ID {
				uniform = false
			}
			for _, r := range rs {
				if u, ok := union[r.ID]; ok {
					u.count++
					u.prio = max(u.prio, r.prio)
					u.first = min(u.first, r.first)
				} else {
					c := r
					union[r.ID] = &c
				}
			}
		}
		ed.Sources = bundleSources(union)
		ed.PrimarySource = ""
		if uniform {
			ed.PrimarySource = primary
		}
	}
	return nil
}

// bundleSources orders the union of a bundle's sources: precedence, then how many relationships each asserts.
func bundleSources(union map[string]*rankedSource) []EdgeSource {
	list := make([]rankedSource, 0, len(union))
	for _, u := range union {
		list = append(list, *u)
	}
	slices.SortFunc(list, func(a, b rankedSource) int {
		return cmp.Or(cmp.Compare(b.prio, a.prio), cmp.Compare(b.count, a.count), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	out := make([]EdgeSource, 0, len(list))
	for _, r := range list {
		s := r.EdgeSource
		s.Relations = r.count
		out = append(out, s)
	}
	return out
}

// overviewSources fills the sources of the overview's bundled edges with one aggregate over the live assertions of
// the same relationships (keyed by group pair and type). A bundle has a primary source when one visible source asserts
// every bundled relationship and outranks every other source in the bundle.
func (e *Engine) overviewSources(ctx context.Context, scope Scope, col, where string, args []any, edges []Edge, keys []string) error {
	ecol := strings.ReplaceAll(col, "e.", "fe.")
	tcol := strings.ReplaceAll(col, "e.", "te.")
	ewhere := strings.ReplaceAll(where, "e.project_id", "fe.project_id")
	qargs := append(slices.Clone(args), e.Clock.Now().UnixMicro())
	rows, err := e.Store.R().QueryContext(ctx, `SELECT `+ecol+`, `+tcol+`, r.type, a.source_id, a.layer, COUNT(*)
		FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id JOIN relation_assertions a ON a.relation_id = r.id
		WHERE r.live = 1 AND fe.tombstoned_at IS NULL AND te.tombstoned_at IS NULL`+ewhere+`
		AND a.tombstoned_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > ?) GROUP BY 1, 2, 3, 4, 5`, qargs...)
	if err != nil {
		return err
	}
	type counted struct {
		source, layer string
		n             int
	}
	byKey := map[string][]counted{}
	srcSeen := map[string]bool{}
	var srcIDs []string
	for rows.Next() {
		var a, b, typ string
		var c counted
		if err := rows.Scan(&a, &b, &typ, &c.source, &c.layer, &c.n); err != nil {
			rows.Close()
			return err
		}
		if a == b {
			continue
		}
		k := a + "\x00" + b + "\x00" + typ
		byKey[k] = append(byKey[k], c)
		if !srcSeen[c.source] {
			srcSeen[c.source] = true
			srcIDs = append(srcIDs, c.source)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slices.Sort(srcIDs)
	srcs, err := e.sourcesOf(ctx, scope, srcIDs)
	if err != nil {
		return err
	}
	for i := range edges {
		ed := &edges[i]
		union := map[string]*rankedSource{}
		for _, c := range byKey[keys[i]] {
			s, ok := srcs[c.source]
			if !ok {
				continue
			}
			if u, ok := union[s.ID]; ok {
				u.count += c.n
				u.prio = max(u.prio, s.priority(c.layer))
			} else {
				union[s.ID] = &rankedSource{sourceInfo: s, prio: s.priority(c.layer), count: c.n}
			}
		}
		ed.Sources = bundleSources(union)
		ed.PrimarySource = ""
		if len(ed.Sources) > 0 {
			top := union[ed.Sources[0].ID]
			outranks := len(ed.Sources) == 1 || union[ed.Sources[1].ID].prio < top.prio
			if top.count >= ed.Count && outranks {
				ed.PrimarySource = top.ID
			}
		}
	}
	return nil
}
