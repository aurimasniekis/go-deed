// Package graph answers bounded graph queries over the catalog (ADR-011): neighborhoods with depth and node caps,
// collapsed clusters for high-degree nodes, shortest paths, evidence-based impact traversal and aggregated
// overviews. Every query is restricted to readable projects; nodes in other projects become redacted stubs that are
// never expanded (ADR-004). Cycles are handled with visited sets.
package graph

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Node is a graph node: an entity, a redacted stub or a collapsed cluster.
type Node struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	ProjectID   string            `json:"project_id"`
	Icon        string            `json:"icon,omitempty"`
	Health      string            `json:"health"`
	Freshness   string            `json:"freshness"`
	Environment string            `json:"environment,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Declared    bool              `json:"declared"`
	Valid       bool              `json:"valid"`
	Findings    int               `json:"findings"`
	Depth       int               `json:"depth"`
	Redacted    bool              `json:"redacted,omitempty"`
	Cluster     *Cluster          `json:"cluster,omitempty"`
}

// Cluster describes a collapsed group of nodes.
type Cluster struct {
	Key     string `json:"key" doc:"Pass to /graph/expand to load the members."`
	Count   int    `json:"count"`
	GroupBy string `json:"group_by"`
	Value   string `json:"value"`
}

// Edge is a relationship (or an aggregated bundle in overviews).
type Edge struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	EvidenceClass string     `json:"evidence_class"`
	Inferred      bool       `json:"inferred"`
	CrossProject  bool       `json:"cross_project"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	Count         int        `json:"count,omitempty" doc:"Aggregated edges: number of relationships bundled."`
}

// Result is a graph answer.
type Result struct {
	Nodes     []Node   `json:"nodes"`
	Edges     []Edge   `json:"edges"`
	Truncated bool     `json:"truncated"`
	Notes     []string `json:"notes"`
	ElapsedMS int64    `json:"elapsed_ms"`
}

// Engine runs graph queries.
type Engine struct {
	Store   *store.Store
	Catalog *catalog.Service
	Clock   clock.Clock
	// MaxDepth and MaxNodes are installation limits.
	MaxDepth     int
	MaxNodes     int
	ClusterAbove int
	// DegreeCollapse collapses more than this many neighbors of one (type, kind) behind one node.
	DegreeCollapse int
}

// Scope is the caller's readable projects (nil = all).
type Scope struct {
	Readable []string
}

func (s Scope) can(project string) bool {
	return s.Readable == nil || slices.Contains(s.Readable, project)
}

// Query selects a neighborhood.
type Query struct {
	Roots     []string
	Depth     int
	Direction string // out, in, both
	Types     []string
	Kinds     []string // node filter (roots are always included)
	Evidence  []string
	MaxNodes  int
	// IncludeStale keeps relations whose observed assertions are stale.
	IncludeStale bool
}

type rawEdge struct {
	Edge
	fromProject, toProject string
}

const chunk = 400

func placeholders(n int) string { return strings.Repeat(",?", n)[1:] }

// loadNodes fetches node rows for ids (redacting unreadable ones).
func (e *Engine) loadNodes(ctx context.Context, scope Scope, ids []string) (map[string]*Node, error) {
	out := map[string]*Node{}
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := e.Store.R().QueryContext(ctx, `SELECT e.id, e.kind, e.name, e.display_name, e.project_id, e.icon, e.health, e.freshness, e.environment,
			e.owner, e.labels_json, e.declared, e.valid, (SELECT COUNT(*) FROM findings f WHERE f.entity_id = e.id AND f.state = 'open')
			FROM entities e WHERE e.id IN (`+placeholders(len(part))+`) AND e.tombstoned_at IS NULL`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n Node
			var labels string
			if err := rows.Scan(&n.ID, &n.Kind, &n.Name, &n.DisplayName, &n.ProjectID, &n.Icon, &n.Health, &n.Freshness, &n.Environment, &n.Owner,
				&labels, &n.Declared, &n.Valid, &n.Findings); err != nil {
				rows.Close()
				return nil, err
			}
			if !scope.can(n.ProjectID) {
				n = redacted(n.ID)
			} else {
				_ = json.Unmarshal([]byte(labels), &n.Labels)
			}
			out[n.ID] = &n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func redacted(id string) Node {
	h := sha256.Sum256([]byte("deed/redact/v1|" + id))
	return Node{ID: "redacted:" + hex.EncodeToString(h[:8]), Kind: "external", Name: "restricted", ProjectID: "restricted", Health: "unknown",
		Freshness: "unknown", Redacted: true}
}

// edgesOf loads live relations touching ids in the given direction.
func (e *Engine) edgesOf(ctx context.Context, ids []string, dir string, q Query) ([]rawEdge, error) {
	var out []rawEdge
	staleCut := e.Clock.Now().Add(-7 * 24 * time.Hour).UnixMicro()
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		var conds []string
		var args []any
		in := placeholders(len(part))
		switch dir {
		case "out":
			conds = append(conds, "r.from_id IN ("+in+")")
			for _, id := range part {
				args = append(args, id)
			}
		case "in":
			conds = append(conds, "r.to_id IN ("+in+")")
			for _, id := range part {
				args = append(args, id)
			}
		default:
			conds = append(conds, "(r.from_id IN ("+in+") OR r.to_id IN ("+in+"))")
			for range 2 {
				for _, id := range part {
					args = append(args, id)
				}
			}
		}
		conds = append(conds, "r.live = 1", "fe.tombstoned_at IS NULL", "te.tombstoned_at IS NULL")
		if len(q.Types) > 0 {
			conds = append(conds, "r.type IN ("+placeholders(len(q.Types))+")")
			for _, t := range q.Types {
				args = append(args, t)
			}
		}
		if len(q.Evidence) > 0 {
			conds = append(conds, "r.evidence_class IN ("+placeholders(len(q.Evidence))+")")
			for _, t := range q.Evidence {
				args = append(args, t)
			}
		}
		if !q.IncludeStale {
			conds = append(conds, "(r.evidence_class != 'observed' OR r.last_seen_at IS NULL OR r.last_seen_at >= ?)")
			args = append(args, staleCut)
		}
		rows, err := e.Store.R().QueryContext(ctx, `SELECT r.id, r.type, r.from_id, r.to_id, r.evidence_class, r.inferred, r.last_seen_at, fe.project_id, te.project_id
			FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY r.type, r.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var x rawEdge
			var last sql.NullInt64
			if err := rows.Scan(&x.ID, &x.Type, &x.From, &x.To, &x.EvidenceClass, &x.Inferred, &last, &x.fromProject, &x.toProject); err != nil {
				rows.Close()
				return nil, err
			}
			if last.Valid {
				t := clock.FromMicros(last.Int64)
				x.LastSeenAt = &t
			}
			x.CrossProject = x.fromProject != x.toProject
			out = append(out, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (e *Engine) limits(q *Query) {
	maxDepth := max(e.MaxDepth, 1)
	if q.Depth <= 0 {
		q.Depth = 1
	}
	q.Depth = min(q.Depth, maxDepth)
	if q.MaxNodes <= 0 || q.MaxNodes > e.MaxNodes {
		q.MaxNodes = e.MaxNodes
	}
	if q.Direction == "" {
		q.Direction = "both"
	}
}

// Neighborhood expands from roots breadth-first within the limits.
func (e *Engine) Neighborhood(ctx context.Context, scope Scope, q Query) (*Result, error) {
	start := time.Now()
	e.limits(&q)
	if len(q.Roots) == 0 || len(q.Roots) > 50 {
		return nil, apperr.New(apperr.CodeInvalid, "give 1..50 root entities")
	}
	roots, err := e.loadNodes(ctx, scope, q.Roots)
	if err != nil {
		return nil, err
	}
	res := &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}
	nodes := map[string]*Node{}
	order := []string{}
	for _, id := range q.Roots {
		n, ok := roots[id]
		if !ok || n.Redacted {
			continue
		}
		n.Depth = 0
		nodes[id] = n
		order = append(order, id)
	}
	if len(nodes) == 0 {
		return nil, catalog.ErrEntityNotFound
	}
	edges := map[string]Edge{}
	frontier := slices.Clone(order)
	collapse := max(e.DegreeCollapse, 1)
	for depth := 1; depth <= q.Depth && len(frontier) > 0 && !res.Truncated; depth++ {
		raws, err := e.edgesOf(ctx, frontier, q.Direction, q)
		if err != nil {
			return nil, err
		}
		// Group candidate neighbors per (anchor, type, kind direction) to collapse high-degree fans.
		type fanKey struct{ anchor, typ, dir string }
		fans := map[fanKey][]rawEdge{}
		var fanOrder []fanKey
		frontierSet := map[string]bool{}
		for _, id := range frontier {
			frontierSet[id] = true
		}
		for _, r := range raws {
			anchor, dir := r.From, "out"
			if !frontierSet[r.From] || (frontierSet[r.To] && q.Direction == "in") {
				anchor, dir = r.To, "in"
			}
			k := fanKey{anchor, r.Type, dir}
			if _, ok := fans[k]; !ok {
				fanOrder = append(fanOrder, k)
			}
			fans[k] = append(fans[k], r)
		}
		var newIDs []string
		pending := map[string]bool{}
		for _, k := range fanOrder {
			group := fans[k]
			other := func(r rawEdge) string {
				if k.dir == "out" {
					return r.To
				}
				return r.From
			}
			fresh := 0
			for _, r := range group {
				if _, seen := nodes[other(r)]; !seen {
					fresh++
				}
			}
			if fresh > collapse {
				key := fmt.Sprintf("fan|%s|%s|%s", k.anchor, k.typ, k.dir)
				cid := "cluster:" + shortHash(key)
				nodes[cid] = &Node{ID: cid, Kind: "cluster", Name: fmt.Sprintf("%d × %s", fresh, k.typ), Health: "unknown", Freshness: "unknown",
					Depth: depth, Cluster: &Cluster{Key: key, Count: fresh, GroupBy: "relationship", Value: k.typ}}
				order = append(order, cid)
				from, to := k.anchor, cid
				if k.dir == "in" {
					from, to = cid, k.anchor
				}
				edges[cid+"|"+k.typ] = Edge{ID: "bundle:" + shortHash(key), Type: k.typ, From: from, To: to, EvidenceClass: group[0].EvidenceClass, Count: fresh}
				for _, r := range group {
					if _, seen := nodes[other(r)]; seen {
						edges[r.ID] = r.Edge
					}
				}
				res.Notes = append(res.Notes, fmt.Sprintf("collapsed %d %s neighbors of one node; expand the cluster to load them", fresh, k.typ))
				continue
			}
			for _, r := range group {
				o := other(r)
				edges[r.ID] = r.Edge
				if _, seen := nodes[o]; !seen && !pending[o] {
					pending[o] = true
					newIDs = append(newIDs, o)
				}
			}
		}
		loaded, err := e.loadNodes(ctx, scope, newIDs)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, id := range newIDs {
			n, ok := loaded[id]
			if !ok {
				continue
			}
			if len(nodes) >= q.MaxNodes {
				res.Truncated = true
				break
			}
			if !n.Redacted && len(q.Kinds) > 0 && !slices.Contains(q.Kinds, n.Kind) {
				continue
			}
			n.Depth = depth
			if n.Redacted {
				// Map edges to the stub ID; stubs are never expanded.
				nodes[n.ID] = n
				for k, ed := range edges {
					if ed.From == id {
						ed.From = n.ID
					}
					if ed.To == id {
						ed.To = n.ID
					}
					edges[k] = ed
				}
				order = append(order, n.ID)
				continue
			}
			nodes[id] = n
			order = append(order, id)
			frontier = append(frontier, id)
		}
	}
	if res.Truncated {
		res.Notes = append(res.Notes, fmt.Sprintf("node limit %d reached; narrow the filters or lower the depth", q.MaxNodes))
	}
	for _, id := range order {
		if n, ok := nodes[id]; ok {
			res.Nodes = append(res.Nodes, *n)
		}
	}
	for _, ed := range edges {
		if _, ok := nodes[ed.From]; !ok {
			continue
		}
		if _, ok := nodes[ed.To]; !ok {
			continue
		}
		res.Edges = append(res.Edges, ed)
	}
	sort.Slice(res.Edges, func(i, j int) bool { return res.Edges[i].ID < res.Edges[j].ID })
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

// Expand loads the members of a fan cluster (fan|…) or of an overview group (<group_by>|<value>).
func (e *Engine) Expand(ctx context.Context, scope Scope, key string, limit int) (*Result, error) {
	parts := strings.Split(key, "|")
	if len(parts) == 2 {
		return e.expandGroup(ctx, scope, parts[0], parts[1], limit)
	}
	if len(parts) != 4 || parts[0] != "fan" {
		return nil, apperr.New(apperr.CodeInvalid, "unknown cluster key")
	}
	anchor, typ, dir := parts[1], parts[2], parts[3]
	if limit <= 0 || limit > e.MaxNodes {
		limit = e.MaxNodes
	}
	start := time.Now()
	an, err := e.loadNodes(ctx, scope, []string{anchor})
	if err != nil {
		return nil, err
	}
	if n, ok := an[anchor]; !ok || n.Redacted {
		return nil, catalog.ErrEntityNotFound
	}
	raws, err := e.edgesOf(ctx, []string{anchor}, dir, Query{Types: []string{typ}, IncludeStale: true})
	if err != nil {
		return nil, err
	}
	res := &Result{Nodes: []Node{*an[anchor]}, Edges: []Edge{}, Notes: []string{}}
	var ids []string
	for _, r := range raws {
		if dir == "out" {
			ids = append(ids, r.To)
		} else {
			ids = append(ids, r.From)
		}
	}
	if len(ids) > limit {
		ids, res.Truncated = ids[:limit], true
	}
	loaded, err := e.loadNodes(ctx, scope, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range raws {
		o := r.To
		if dir == "in" {
			o = r.From
		}
		n, ok := loaded[o]
		if !ok {
			continue
		}
		n.Depth = 1
		res.Nodes = append(res.Nodes, *n)
		ed := r.Edge
		if n.Redacted {
			if dir == "out" {
				ed.To = n.ID
			} else {
				ed.From = n.ID
			}
		}
		res.Edges = append(res.Edges, ed)
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

// expandGroup loads the entities of an overview group and the relationships among them.
func (e *Engine) expandGroup(ctx context.Context, scope Scope, groupBy, value string, limit int) (*Result, error) {
	if limit <= 0 || limit > e.MaxNodes {
		limit = e.MaxNodes
	}
	start := time.Now()
	f := catalog.EntityFilter{ProjectIDs: scope.Readable, Limit: min(limit, 500), Sort: "name"}
	switch groupBy {
	case "kind":
		f.Kinds = []string{value}
	case "environment":
		f.Environment = []string{value}
	case "owner":
		f.Owner = value
	case "project":
		if scope.Readable != nil && !slices.Contains(scope.Readable, value) {
			return nil, apperr.New(apperr.CodeNotFound, "group not found")
		}
		f.ProjectIDs = []string{value}
	default:
		return nil, apperr.New(apperr.CodeInvalid, "unknown cluster key")
	}
	res := &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}
	var ids []string
	for len(ids) < limit {
		page, err := e.Catalog.ListEntities(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			ids = append(ids, it.Metadata.ID)
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		f.Cursor = page.NextCursor
	}
	if len(ids) > limit {
		ids, res.Truncated = ids[:limit], true
	}
	loaded, err := e.loadNodes(ctx, scope, ids)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, id := range ids {
		if n, ok := loaded[id]; ok {
			res.Nodes = append(res.Nodes, *n)
			in[id] = true
		}
	}
	raws, err := e.edgesOf(ctx, ids, "out", Query{})
	if err != nil {
		return nil, err
	}
	for _, r := range raws {
		if in[r.From] && in[r.To] {
			res.Edges = append(res.Edges, r.Edge)
		}
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

// Path finds a shortest path between two entities (directed or not) within maxDepth.
func (e *Engine) Path(ctx context.Context, scope Scope, from, to string, maxDepth int, directed bool, types []string) (*Result, error) {
	start := time.Now()
	if maxDepth <= 0 || maxDepth > e.MaxDepth {
		maxDepth = e.MaxDepth
	}
	ends, err := e.loadNodes(ctx, scope, []string{from, to})
	if err != nil {
		return nil, err
	}
	for _, id := range []string{from, to} {
		if n, ok := ends[id]; !ok || n.Redacted {
			return nil, catalog.ErrEntityNotFound
		}
	}
	// Bidirectional breadth-first search: expand the smaller frontier each round, so a path of length d explores
	// about two balls of radius d/2 instead of one of radius d; a visited-node budget bounds pathological graphs.
	type step struct {
		other string // the neighbour towards the search's origin
		edge  Edge
	}
	fwd := map[string]step{from: {}}
	bwd := map[string]step{to: {}}
	ff, bf := []string{from}, []string{to}
	fDir, bDir := "both", "both"
	if directed {
		fDir, bDir = "out", "in"
	}
	budget := max(e.MaxNodes*10, 5000)
	meet := ""
	if from == to {
		meet = from
	}
	exhausted := false
	expand := func(frontier []string, dir string, seen, other map[string]step, forward bool) ([]string, error) {
		raws, err := e.edgesOf(ctx, frontier, dir, Query{Types: types, IncludeStale: true})
		if err != nil {
			return nil, err
		}
		fs := map[string]bool{}
		for _, id := range frontier {
			fs[id] = true
		}
		var next []string
		for _, r := range raws {
			if !scope.can(r.fromProject) || !scope.can(r.toProject) {
				continue // paths never pass through unreadable projects
			}
			var o, cur string
			switch {
			case (forward || !directed) && fs[r.From] && (dir != "in"):
				o, cur = r.To, r.From
			case (!forward || !directed) && fs[r.To] && (dir != "out"):
				o, cur = r.From, r.To
			default:
				continue
			}
			if _, ok := seen[o]; ok {
				continue
			}
			seen[o] = step{other: cur, edge: r.Edge}
			next = append(next, o)
			if _, ok := other[o]; ok {
				meet = o
				return next, nil
			}
			if len(fwd)+len(bwd) > budget {
				exhausted = true
				return next, nil
			}
		}
		return next, nil
	}
	for depth := 0; depth < maxDepth && meet == "" && !exhausted && len(ff) > 0 && len(bf) > 0; depth++ {
		var err error
		if len(ff) <= len(bf) {
			ff, err = expand(ff, fDir, fwd, bwd, true)
		} else {
			bf, err = expand(bf, bDir, bwd, fwd, false)
		}
		if err != nil {
			return nil, err
		}
	}
	res := &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}
	if meet == "" {
		if exhausted {
			res.Notes = append(res.Notes, fmt.Sprintf("search stopped after visiting %d entities without finding a path", len(fwd)+len(bwd)))
		} else {
			res.Notes = append(res.Notes, fmt.Sprintf("no path within %d hops", maxDepth))
		}
		res.Nodes = append(res.Nodes, *ends[from], *ends[to])
		res.ElapsedMS = time.Since(start).Milliseconds()
		return res, nil
	}
	var chain []string
	for cur := meet; cur != from; cur = fwd[cur].other {
		chain = append(chain, cur)
		res.Edges = append(res.Edges, fwd[cur].edge)
	}
	chain = append(chain, from)
	slices.Reverse(chain)
	slices.Reverse(res.Edges)
	for cur := meet; cur != to; {
		st := bwd[cur]
		res.Edges = append(res.Edges, st.edge)
		chain = append(chain, st.other)
		cur = st.other
	}
	loaded, err := e.loadNodes(ctx, scope, chain)
	if err != nil {
		return nil, err
	}
	for i, id := range chain {
		if n, ok := loaded[id]; ok {
			n.Depth = i
			res.Nodes = append(res.Nodes, *n)
		}
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

// ImpactNode is a node reached by impact traversal with its evidence.
type ImpactNode struct {
	Node
	Via       []string `json:"via" doc:"Relationship types along the shortest impact path."`
	Evidence  string   `json:"evidence" doc:"Weakest evidence class along the path."`
	Potential bool     `json:"potential" doc:"Always true: impact is evidence-based graph analysis, not observed failure."`
}

// ImpactResult is an impact analysis.
type ImpactResult struct {
	Root      Node         `json:"root"`
	Direction string       `json:"direction"`
	Nodes     []ImpactNode `json:"nodes"`
	Edges     []Edge       `json:"edges"`
	Cycles    bool         `json:"cycles"`
	Truncated bool         `json:"truncated"`
	Notes     []string     `json:"notes"`
	ElapsedMS int64        `json:"elapsed_ms"`
}

var evidenceRank = map[string]int{"declared": 4, "structural": 3, "observed": 2, "inferred": 1}

// Impact walks dependency semantics of relationship types (ADR-002 impact field):
//   - downstream: what may be affected if the root fails (its dependents);
//   - upstream: what the root depends on.
func (e *Engine) Impact(ctx context.Context, scope Scope, root, direction string, depth int) (*ImpactResult, error) {
	start := time.Now()
	if depth <= 0 || depth > e.MaxDepth {
		depth = min(4, e.MaxDepth)
	}
	if direction != "upstream" {
		direction = "downstream"
	}
	rs, err := e.loadNodes(ctx, scope, []string{root})
	if err != nil {
		return nil, err
	}
	rn, ok := rs[root]
	if !ok || rn.Redacted {
		return nil, catalog.ErrEntityNotFound
	}
	impact := map[string]string{}
	for _, rt := range e.Catalog.RelTypes() {
		impact[rt.Name] = rt.Impact
	}
	res := &ImpactResult{Root: *rn, Direction: direction, Nodes: []ImpactNode{}, Edges: []Edge{}, Notes: []string{
		"impact is evidence-based graph analysis of declared and observed dependencies; it does not mean these resources are failing"}}
	type info struct {
		via []string
		ev  string
	}
	seen := map[string]info{root: {}}
	frontier := []string{root}
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		raws, err := e.edgesOf(ctx, frontier, "both", Query{IncludeStale: false})
		if err != nil {
			return nil, err
		}
		fs := map[string]bool{}
		for _, id := range frontier {
			fs[id] = true
		}
		var next []string
		for _, r := range raws {
			imp := impact[r.Type]
			// dependent(r): the endpoint that depends on the other.
			var dependent, dependency string
			switch imp {
			case "forward":
				dependent, dependency = r.From, r.To
			case "reverse":
				dependent, dependency = r.To, r.From
			case "both":
				if fs[r.From] {
					dependent, dependency = r.To, r.From
				} else {
					dependent, dependency = r.From, r.To
				}
			default:
				continue
			}
			var anchor, reached string
			if direction == "downstream" {
				anchor, reached = dependency, dependent
			} else {
				anchor, reached = dependent, dependency
			}
			if !fs[anchor] {
				continue
			}
			if !scope.can(r.fromProject) || !scope.can(r.toProject) {
				res.Notes = append(res.Notes, "some impact continues into projects you cannot read")
				continue
			}
			if _, ok := seen[reached]; ok {
				if reached == root {
					res.Cycles = true
				}
				res.Edges = append(res.Edges, r.Edge)
				continue
			}
			pi := seen[anchor]
			ev := r.EvidenceClass
			if pi.ev != "" && evidenceRank[pi.ev] < evidenceRank[ev] {
				ev = pi.ev
			}
			seen[reached] = info{via: append(slices.Clone(pi.via), r.Type), ev: ev}
			res.Edges = append(res.Edges, r.Edge)
			next = append(next, reached)
			if len(seen) > e.MaxNodes {
				res.Truncated = true
				break
			}
		}
		frontier = next
		if res.Truncated {
			break
		}
	}
	var ids []string
	for id := range seen {
		if id != root {
			ids = append(ids, id)
		}
	}
	loaded, err := e.loadNodes(ctx, scope, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		n, ok := loaded[id]
		if !ok {
			continue
		}
		n.Depth = len(seen[id].via)
		res.Nodes = append(res.Nodes, ImpactNode{Node: *n, Via: seen[id].via, Evidence: seen[id].ev, Potential: true})
	}
	sort.Slice(res.Nodes, func(i, j int) bool {
		if res.Nodes[i].Depth != res.Nodes[j].Depth {
			return res.Nodes[i].Depth < res.Nodes[j].Depth
		}
		return res.Nodes[i].Name < res.Nodes[j].Name
	})
	res.Notes = slices.Compact(res.Notes)
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

// Overview aggregates the readable catalog into cluster nodes grouped by project, kind, environment or owner, with
// bundled edges between groups — the entry view of the global map.
func (e *Engine) Overview(ctx context.Context, scope Scope, projects []string, groupBy string) (*Result, error) {
	start := time.Now()
	col := map[string]string{"project": "e.project_id", "kind": "e.kind", "environment": "e.environment", "owner": "e.owner"}[groupBy]
	if col == "" {
		return nil, apperr.New(apperr.CodeInvalid, "group_by must be project, kind, environment or owner")
	}
	if projects == nil {
		projects = scope.Readable
	}
	var where string
	var args []any
	if projects != nil {
		if len(projects) == 0 {
			return &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}, nil
		}
		where = " AND e.project_id IN (" + placeholders(len(projects)) + ")"
		for _, p := range projects {
			args = append(args, p)
		}
	}
	rows, err := e.Store.R().QueryContext(ctx, `SELECT `+col+`, COUNT(*), SUM(e.health = 'failed'), SUM(e.health = 'warning'),
		SUM(e.freshness = 'stale') FROM entities e WHERE e.tombstoned_at IS NULL`+where+` GROUP BY `+col+` ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	res := &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}
	for rows.Next() {
		var v string
		var n, failed, warn, stale int
		if err := rows.Scan(&v, &n, &failed, &warn, &stale); err != nil {
			rows.Close()
			return nil, err
		}
		health := "ok"
		switch {
		case failed > 0:
			health = "failed"
		case warn > 0:
			health = "warning"
		}
		fresh := "fresh"
		if stale > 0 {
			fresh = "stale"
		}
		label := v
		if label == "" {
			label = "(none)"
		}
		id := "group:" + groupBy + ":" + v
		res.Nodes = append(res.Nodes, Node{ID: id, Kind: "cluster", Name: label, Health: health, Freshness: fresh, ProjectID: "",
			Cluster: &Cluster{Key: groupBy + "|" + v, Count: n, GroupBy: groupBy, Value: v}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ecol := strings.ReplaceAll(col, "e.", "fe.")
	tcol := strings.ReplaceAll(col, "e.", "te.")
	ewhere := strings.ReplaceAll(where, "e.project_id", "fe.project_id")
	rows, err = e.Store.R().QueryContext(ctx, `SELECT `+ecol+`, `+tcol+`, r.type, COUNT(*), MAX(r.evidence_class = 'declared')
		FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id
		WHERE r.live = 1 AND fe.tombstoned_at IS NULL AND te.tombstoned_at IS NULL`+ewhere+` GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, typ string
		var n int
		var declared bool
		if err := rows.Scan(&a, &b, &typ, &n, &declared); err != nil {
			return nil, err
		}
		if a == b {
			continue
		}
		ev := "observed"
		if declared {
			ev = "declared"
		}
		from, to := "group:"+groupBy+":"+a, "group:"+groupBy+":"+b
		res.Edges = append(res.Edges, Edge{ID: "bundle:" + shortHash(from+to+typ), Type: typ, From: from, To: to, EvidenceClass: ev, Count: n})
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, rows.Err()
}
