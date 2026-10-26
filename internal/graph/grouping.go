package graph

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
)

// groupingKind returns the kind behind a group_by value that is not one of the built-in columns: a kind with a map
// grouping (location, region, label, group, or a custom kind), or nil.
func (e *Engine) groupingKind(groupBy string) *catalog.Kind {
	if e.Catalog == nil {
		return nil
	}
	if k := e.Catalog.Kind(groupBy); k != nil && k.Grouping != nil && len(k.Grouping.RelationshipTypes) > 0 {
		return k
	}
	return nil
}

// anchorsOf maps every live entity of projects (nil = every project) to its group under kind k: the nearest entity of
// kind k it reaches by following k's grouping relationship types outward, at most catalog.MaxGroupingDepth hops (itself
// when it is of kind k), or "" when it reaches none. Only entities of readable projects count as groups.
func (e *Engine) anchorsOf(ctx context.Context, scope Scope, projects []string, k *catalog.Kind) (map[string]string, error) {
	where, args := projectWhere("e.project_id", projects)
	members := []string{}
	rows, err := e.Store.R().QueryContext(ctx, `SELECT e.id FROM entities e WHERE e.tombstoned_at IS NULL`+where, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	isAnchor := map[string]bool{}
	rwhere, rargs := projectWhere("e.project_id", scope.Readable)
	rows, err = e.Store.R().QueryContext(ctx, `SELECT e.id FROM entities e WHERE e.tombstoned_at IS NULL AND e.kind = ?`+rwhere,
		append([]any{k.Name}, rargs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		isAnchor[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	types := make([]any, len(k.Grouping.RelationshipTypes))
	for i, t := range k.Grouping.RelationshipTypes {
		types[i] = t
	}
	parents := map[string][]string{}
	rows, err = e.Store.R().QueryContext(ctx, `SELECT r.from_id, r.to_id FROM relations r JOIN entities te ON te.id = r.to_id
		WHERE r.live = 1 AND te.tombstoned_at IS NULL AND r.type IN (`+placeholders(len(types))+`) ORDER BY r.from_id, r.to_id`, types...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			rows.Close()
			return nil, err
		}
		parents[from] = append(parents[from], to)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(members))
	for _, id := range members {
		out[id] = nearestAnchor(id, parents, isAnchor)
	}
	return out, nil
}

// nearestAnchor is a breadth-first search up the grouping relationships; among anchors at the same distance the first
// in ID order wins, so the answer is deterministic.
func nearestAnchor(id string, parents map[string][]string, isAnchor map[string]bool) string {
	if isAnchor[id] {
		return id
	}
	seen := map[string]bool{id: true}
	level := []string{id}
	for depth := 0; depth < catalog.MaxGroupingDepth && len(level) > 0; depth++ {
		var next []string
		for _, cur := range level {
			for _, p := range parents[cur] {
				if !seen[p] {
					seen[p] = true
					next = append(next, p)
				}
			}
		}
		slices.Sort(next)
		for _, p := range next {
			if isAnchor[p] {
				return p
			}
		}
		level = next
	}
	return ""
}

func projectWhere(col string, projects []string) (string, []any) {
	if projects == nil {
		return "", nil
	}
	if len(projects) == 0 {
		return " AND 0", nil
	}
	args := make([]any, len(projects))
	for i, p := range projects {
		args[i] = p
	}
	return " AND " + col + " IN (" + placeholders(len(projects)) + ")", args
}

// overviewByKind is the overview grouped by a grouping kind: one cluster per entity of that kind (named after it), one
// for everything that reaches none, and bundled edges between them. A group of one is shown as its entity.
func (e *Engine) overviewByKind(ctx context.Context, scope Scope, projects []string, k *catalog.Kind) (*Result, error) {
	start := time.Now()
	anchors, err := e.anchorsOf(ctx, scope, projects, k)
	if err != nil {
		return nil, err
	}
	res := &Result{Nodes: []Node{}, Edges: []Edge{}, Notes: []string{}}
	if len(anchors) == 0 {
		return res, nil
	}
	groups := map[string][]string{}
	for id, a := range anchors {
		groups[a] = append(groups[a], id)
	}
	anchorIDs := make([]string, 0, len(groups))
	var singles []string
	for a, m := range groups {
		if a != "" {
			anchorIDs = append(anchorIDs, a)
		}
		if len(m) == 1 {
			singles = append(singles, m[0])
		}
	}
	named, err := e.loadNodes(ctx, scope, append(anchorIDs, singles...))
	if err != nil {
		return nil, err
	}
	health, err := e.healthOf(ctx, anchors)
	if err != nil {
		return nil, err
	}
	nodeOf := map[string]string{} // member → node ID on the map
	for a, m := range groups {
		if len(m) == 1 {
			if n, ok := named[m[0]]; ok {
				res.Nodes = append(res.Nodes, *n)
				nodeOf[m[0]] = n.ID
			}
			continue
		}
		label := "(none)"
		if n, ok := named[a]; ok {
			label = cmp.Or(n.DisplayName, n.Name)
		}
		h, f := "ok", "fresh"
		for _, id := range m {
			switch health[id][0] {
			case "failed":
				h = "failed"
			case "warning":
				if h == "ok" {
					h = "warning"
				}
			}
			if health[id][1] == "stale" {
				f = "stale"
			}
		}
		id := "group:" + k.Name + ":" + a
		res.Nodes = append(res.Nodes, Node{ID: id, Kind: "cluster", Name: label, Health: h, Freshness: f,
			Cluster: &Cluster{Key: k.Name + "|" + a, Count: len(m), GroupBy: k.Name, Value: a}})
		for _, mid := range m {
			nodeOf[mid] = id
		}
	}
	slices.SortFunc(res.Nodes, func(a, b Node) int {
		return cmp.Or(cmp.Compare(clusterCount(b), clusterCount(a)), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	where, args := projectWhere("fe.project_id", projects)
	rows, err := e.Store.R().QueryContext(ctx, `SELECT r.from_id, r.to_id, r.type, r.evidence_class = 'declared'
		FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id
		WHERE r.live = 1 AND fe.tombstoned_at IS NULL AND te.tombstoned_at IS NULL`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type bkey struct{ from, to, typ string }
	bundles := map[bkey]*Edge{}
	var order []bkey
	for rows.Next() {
		var from, to, typ string
		var declared bool
		if err := rows.Scan(&from, &to, &typ, &declared); err != nil {
			return nil, err
		}
		a, b := nodeOf[from], nodeOf[to]
		if a == "" || b == "" || a == b {
			continue
		}
		key := bkey{a, b, typ}
		ed := bundles[key]
		if ed == nil {
			ed = &Edge{ID: "bundle:" + shortHash(a+b+typ), Type: typ, From: a, To: b, EvidenceClass: "observed", Sources: []EdgeSource{}}
			bundles[key] = ed
			order = append(order, key)
		}
		ed.Count++
		if declared {
			ed.EvidenceClass = "declared"
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range order {
		res.Edges = append(res.Edges, *bundles[key])
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

func clusterCount(n Node) int {
	if n.Cluster == nil {
		return 1
	}
	return n.Cluster.Count
}

// healthOf returns [health, freshness] of the given entities.
func (e *Engine) healthOf(ctx context.Context, ids map[string]string) (map[string][2]string, error) {
	out := make(map[string][2]string, len(ids))
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	for start := 0; start < len(list); start += chunk {
		part := list[start:min(start+chunk, len(list))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := e.Store.R().QueryContext(ctx, `SELECT id, health, freshness FROM entities WHERE id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, h, f string
			if err := rows.Scan(&id, &h, &f); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = [2]string{h, f}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// expandByKind expands one group of an overview grouped by kind k: its members and the relationships among them, plus
// bundles from the members to the other groups, using the overview's node IDs (group:<kind>:<anchor>, or the entity
// itself for a group of one), so the expanded members stay connected to the groups still collapsed.
func (e *Engine) expandByKind(ctx context.Context, scope Scope, k *catalog.Kind, anchors map[string]string, value string, limit int,
	start time.Time) (*Result, error) {
	size := map[string]int{}
	var members []string
	for id, a := range anchors {
		size[a]++
		if a == value {
			members = append(members, id)
		}
	}
	slices.Sort(members)
	res, err := e.groupMembers(ctx, scope, members, limit, start)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, n := range res.Nodes {
		in[n.ID] = true
	}
	raws, err := e.edgesOf(ctx, members, "both", Query{IncludeStale: true})
	if err != nil {
		return nil, err
	}
	nodeOf := func(id string) string {
		a, ok := anchors[id]
		if !ok {
			return ""
		}
		if size[a] == 1 {
			return id
		}
		return "group:" + k.Name + ":" + a
	}
	bundles := map[string]*Edge{}
	var order []string
	for _, r := range raws {
		from, to := r.From, r.To
		switch {
		case in[from] && in[to]:
			continue // already among the members' own relationships
		case in[from]:
			to = nodeOf(to)
		case in[to]:
			from = nodeOf(from)
		default:
			continue
		}
		if from == "" || to == "" {
			continue
		}
		id := "agg:" + from + ">" + to + ":" + r.Type
		ed := bundles[id]
		if ed == nil {
			ed = &Edge{ID: id, Type: r.Type, From: from, To: to, EvidenceClass: "observed", Sources: []EdgeSource{}}
			bundles[id] = ed
			order = append(order, id)
		}
		ed.Count++
		if r.EvidenceClass == "declared" {
			ed.EvidenceClass = "declared"
		}
	}
	for _, id := range order {
		res.Edges = append(res.Edges, *bundles[id])
	}
	return res, nil
}
