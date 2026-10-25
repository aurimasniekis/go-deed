package graph_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// TestQueryPlans guards the indexes behind the hot read paths: each query below has the shape the code builds
// (graph.edgesOf, catalog.ListEntities and ListRelations filters, outbox retention, the SSE change-log resume) and must not scan a
// whole table. The plans are taken on a fresh database without statistics, as on a new installation.
func TestQueryPlans(t *testing.T) {
	env := testenv.New(t)
	const edge = `SELECT r.id, r.type, r.from_id, r.to_id, r.evidence_class, r.inferred, r.last_seen_at, fe.project_id, te.project_id
		FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id
		WHERE %s AND r.live = 1 AND fe.tombstoned_at IS NULL AND te.tombstoned_at IS NULL AND r.type IN (?)
		AND (r.evidence_class != 'observed' OR r.last_seen_at IS NULL OR r.last_seen_at >= ?) ORDER BY r.type, r.id`
	for _, tc := range []struct {
		name, sql string
		want      []string
	}{
		{"out edges", strings.Replace(edge, "%s", "r.from_id IN (?, ?)", 1), []string{"ix_relations_from"}},
		{"in edges", strings.Replace(edge, "%s", "r.to_id IN (?, ?)", 1), []string{"ix_relations_to"}},
		{"entities by project and kind", `SELECT e.id FROM entities e JOIN projects p ON p.id = e.project_id
			WHERE e.tombstoned_at IS NULL AND e.project_id IN (?) AND e.kind IN (?) ORDER BY e.name LIMIT 50`, []string{"(project_id=? AND kind=?)"}},
		{"entity search", `SELECT e.id FROM entities e JOIN projects p ON p.id = e.project_id JOIN entity_search fs ON fs.rowid = e.rid
			WHERE entity_search MATCH ? AND e.tombstoned_at IS NULL LIMIT 50`, []string{"VIRTUAL TABLE INDEX", "INTEGER PRIMARY KEY"}},
		{"entities by source", `SELECT e.id FROM entities e WHERE e.tombstoned_at IS NULL
			AND e.id IN (SELECT fc.entity_id FROM facets fc WHERE fc.source_id = ? AND fc.tombstoned_at IS NULL)`, []string{"ix_facets_source"}},
		{"relations by source", `SELECT r.id FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id
			WHERE r.id IN (SELECT a.relation_id FROM relation_assertions a WHERE a.source_id = ? AND a.tombstoned_at IS NULL)`, []string{"ix_assertions_source"}},
		{"assertions of a relation", `SELECT a.id FROM relation_assertions a JOIN sources s ON s.id = a.source_id WHERE a.relation_id = ?
			ORDER BY a.layer, s.name`, []string{"(relation_id=?)"}},
		{"outbox retention (published)", `DELETE FROM outbox WHERE published_at IS NOT NULL AND published_at < ?`, []string{"ix_outbox_published"}},
		{"outbox retention (dead)", `DELETE FROM outbox WHERE dead_at IS NOT NULL AND dead_at < ?`, []string{"ix_outbox_dead"}},
		{"change-log resume", `SELECT seq FROM change_log WHERE seq > ? ORDER BY seq LIMIT 500`, []string{"INTEGER PRIMARY KEY (rowid>?)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := strings.Count(tc.sql, "?")
			args := make([]any, n)
			for i := range args {
				args[i] = "x"
			}
			rows, err := env.Store.R().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+tc.sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			var steps []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				steps = append(steps, detail)
			}
			rows.Close()
			plan := strings.Join(steps, " | ")
			for _, s := range steps {
				// A covering scan of a lookup table (projects, sources) is fine; a scan of a catalog table is not.
				for _, big := range []string{"SCAN e", "SCAN r", "SCAN fe", "SCAN te", "SCAN a", "SCAN fc", "SCAN change_log", "SCAN outbox"} {
					if s == big || strings.HasPrefix(s, big+" ") {
						t.Fatalf("full scan: %s", plan)
					}
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(plan, w) {
					t.Fatalf("plan does not use %s: %s", w, plan)
				}
			}
		})
	}
}
