package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/obs/redact"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// LayerRule is the layer of label rules: below every source, so a source's own label for a key always wins. It is not
// one of Layers — no source can write it.
const LayerRule Layer = "rule"

// Limits of label rules.
const (
	MaxLabelRules      = 200 // per project
	maxRuleLabels      = 32
	maxRuleMatchValues = 50
)

// LabelRuleMatch selects the entities of a project a label rule applies to. Every given criterion must match (an empty
// match selects every entity); within a list, any entry may match. Kinds, source names and label values are globs
// (path.Match: `*`, `?`, `[…]`).
type LabelRuleMatch struct {
	Kinds   []string          `json:"kinds,omitempty" maxItems:"50" doc:"Entity kinds (globs allowed); any may match."`
	Sources []string          `json:"sources,omitempty" maxItems:"50" doc:"Names of sources that describe the entity (globs allowed); any may match."`
	Labels  map[string]string `json:"labels,omitempty" doc:"Labels the entity's sources set, by key: value or glob; all must match."`
}

// LabelRule adds labels to the entities its match selects, below every source (layer "rule").
type LabelRule struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Match           LabelRuleMatch    `json:"match"`
	Labels          map[string]string `json:"labels"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ResourceVersion int64             `json:"resource_version"`
}

// LabelRuleInput creates or changes a label rule. On update, omitted fields keep their value.
type LabelRuleInput struct {
	Name        string            `json:"name,omitempty" maxLength:"63" doc:"Unique in the project (lowercase slug)."`
	Description *string           `json:"description,omitempty" maxLength:"2000"`
	Match       *LabelRuleMatch   `json:"match,omitempty"`
	Labels      map[string]string `json:"labels,omitempty" doc:"Labels to add (at most 32); a source's own label for the same key wins."`
}

func validGlobs(field string, pats []string, v *apperr.Validation) {
	if len(pats) > maxRuleMatchValues {
		v.Add(field, "at most %d entries", maxRuleMatchValues)
	}
	for _, p := range pats {
		if _, err := path.Match(p, ""); err != nil || p == "" || len(p) > 256 {
			v.Add(field, "%q is not a valid glob", p)
		}
	}
}

func validateLabelRule(r *LabelRule, v *apperr.Validation) {
	if !ValidName(r.Name) {
		v.Add("name", "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")
	}
	validGlobs("match.kinds", r.Match.Kinds, v)
	validGlobs("match.sources", r.Match.Sources, v)
	if len(r.Match.Labels) > maxRuleMatchValues {
		v.Add("match.labels", "at most %d entries", maxRuleMatchValues)
	}
	for k, p := range r.Match.Labels {
		if !labelKeyRE.MatchString(k) {
			v.Add("match.labels."+k, "invalid label key")
		}
		validGlobs("match.labels."+k, []string{p}, v)
	}
	if len(r.Labels) == 0 {
		v.Add("labels", "at least one label is required")
	}
	if len(r.Labels) > maxRuleLabels {
		v.Add("labels", "at most %d labels", maxRuleLabels)
	}
	for k, val := range r.Labels {
		switch {
		case !labelKeyRE.MatchString(k):
			v.Add("labels."+k, "invalid label key")
		case redact.IsSensitiveKey(k):
			v.Add("labels."+k, "looks like a secret; labels are not for secrets")
		case val == "" || len(val) > 256 || !utf8.ValidString(val):
			v.Add("labels."+k, "must be a string of 1 to 256 bytes")
		}
	}
}

const labelRuleCols = `id, project_id, name, description, match_json, labels_json, created_at, updated_at, version`

func scanLabelRule(sc interface{ Scan(...any) error }) (*LabelRule, error) {
	var r LabelRule
	var match, labels string
	var created, updated int64
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Description, &match, &labels, &created, &updated, &r.ResourceVersion); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(match), &r.Match)
	_ = json.Unmarshal([]byte(labels), &r.Labels)
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.CreatedAt, r.UpdatedAt = clock.FromMicros(created), clock.FromMicros(updated)
	return &r, nil
}

// LabelRules lists a project's label rules, oldest first (the order in which they apply).
func (s *Service) LabelRules(ctx context.Context, projectID string) ([]LabelRule, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT `+labelRuleCols+` FROM label_rules WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LabelRule{}
	for rows.Next() {
		r, err := scanLabelRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// LabelRule reads one rule of a project by ID or name.
func (s *Service) LabelRule(ctx context.Context, projectID, idOrName string) (*LabelRule, error) {
	r, err := scanLabelRule(s.Store.R().QueryRowContext(ctx, `SELECT `+labelRuleCols+` FROM label_rules WHERE project_id = ? AND (id = ? OR name = ?)`,
		projectID, idOrName, idOrName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.New(apperr.CodeNotFound, "label rule not found")
	}
	return r, err
}

// CreateLabelRule adds a rule and applies it to the project's entities.
func (s *Service) CreateLabelRule(ctx context.Context, actor audit.Actor, projectID string, in LabelRuleInput) (*LabelRule, error) {
	r := &LabelRule{ID: ids.New(ids.LabelRule), ProjectID: projectID, Name: in.Name, Labels: in.Labels}
	if in.Description != nil {
		r.Description = *in.Description
	}
	if in.Match != nil {
		r.Match = *in.Match
	}
	v := &apperr.Validation{}
	validateLabelRule(r, v)
	if err := v.Err(); err != nil {
		return nil, err
	}
	err := s.Store.W().TxNamed(ctx, "catalog.label_rule_create", func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM label_rules WHERE project_id = ?`, projectID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxLabelRules {
			return apperr.New(apperr.CodeInvalid, "a project has at most %d label rules", MaxLabelRules)
		}
		r.CreatedAt, r.UpdatedAt, r.ResourceVersion = tx.Now, tx.Now, 1
		_, err := tx.Exec(ctx, `INSERT INTO label_rules (id, project_id, name, description, match_json, labels_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, projectID, r.Name, r.Description, rawJSON(r.Match), rawJSON(r.Labels), tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "label rule %q already exists", r.Name)
		}
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: projectID, Action: "label_rule.created",
			Target: audit.Target{Type: "label_rule", ID: r.ID}, Details: map[string]any{"name": r.Name, "labels": r.Labels}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, s.RecomputeProject(ctx, projectID, actor)
}

// UpdateLabelRule changes a rule (version > 0: optimistic concurrency) and re-applies the project's rules.
func (s *Service) UpdateLabelRule(ctx context.Context, actor audit.Actor, projectID, idOrName string, in LabelRuleInput, version int64) (*LabelRule, error) {
	cur, err := s.LabelRule(ctx, projectID, idOrName)
	if err != nil {
		return nil, err
	}
	if version > 0 && version != cur.ResourceVersion {
		return nil, apperr.New(apperr.CodePrecondition, "label rule was modified concurrently (version %d, expected %d)", cur.ResourceVersion, version)
	}
	r := *cur
	if in.Name != "" {
		r.Name = in.Name
	}
	if in.Description != nil {
		r.Description = *in.Description
	}
	if in.Match != nil {
		r.Match = *in.Match
	}
	if in.Labels != nil {
		r.Labels = in.Labels
	}
	v := &apperr.Validation{}
	validateLabelRule(&r, v)
	if err := v.Err(); err != nil {
		return nil, err
	}
	err = s.Store.W().TxNamed(ctx, "catalog.label_rule_update", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE label_rules SET name = ?, description = ?, match_json = ?, labels_json = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ?`, r.Name, r.Description, rawJSON(r.Match), rawJSON(r.Labels), tx.NowMicros(), r.ID, cur.ResourceVersion)
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "label rule %q already exists", r.Name)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "label rule was modified concurrently")
		}
		r.UpdatedAt, r.ResourceVersion = tx.Now, cur.ResourceVersion+1
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: projectID, Action: "label_rule.updated",
			Target: audit.Target{Type: "label_rule", ID: r.ID}, Details: map[string]any{"name": r.Name, "labels": r.Labels}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &r, s.RecomputeProject(ctx, projectID, actor)
}

// DeleteLabelRule removes a rule; the labels it added go away (unless a source or another rule sets them).
func (s *Service) DeleteLabelRule(ctx context.Context, actor audit.Actor, projectID, idOrName string) error {
	cur, err := s.LabelRule(ctx, projectID, idOrName)
	if err != nil {
		return err
	}
	err = s.Store.W().TxNamed(ctx, "catalog.label_rule_delete", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM label_rules WHERE id = ?`, cur.ID); err != nil {
			return err
		}
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: projectID, Action: "label_rule.deleted",
			Target: audit.Target{Type: "label_rule", ID: cur.ID}, Details: map[string]any{"name": cur.Name}})
		return err
	})
	if err != nil {
		return err
	}
	return s.RecomputeProject(ctx, projectID, actor)
}

// RecomputeProject re-merges every live entity of a project (in batches of short transactions, so ingestion keeps
// going); unchanged entities emit nothing. Label rule changes use it.
func (s *Service) RecomputeProject(ctx context.Context, projectID string, actor audit.Actor) error {
	_, err := s.recomputeWhere(ctx, `project_id = ?`, []any{projectID}, actor)
	return err
}

// recomputeWhere re-merges the live entities matching a condition on the entities table, 200 per transaction.
func (s *Service) recomputeWhere(ctx context.Context, cond string, args []any, actor audit.Actor) (int, error) {
	n, after := 0, ""
	for {
		var batch []string
		err := s.Store.W().TxNamed(ctx, "catalog.recompute_batch", func(tx *store.Tx) error {
			var err error
			batch, err = stringsQuery(ctx, tx, `SELECT id FROM entities WHERE tombstoned_at IS NULL AND `+cond+` AND id > ? ORDER BY id LIMIT 200`,
				append(slices.Clone(args), after)...)
			if err != nil {
				return err
			}
			for _, id := range batch {
				if _, err := s.recomputeTx(ctx, tx, id, recomputeOpts{actor: actor}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		n += len(batch)
		if len(batch) < 200 {
			return n, nil
		}
		after = batch[len(batch)-1]
	}
}

func globAny(pats []string, s string) bool {
	for _, p := range pats {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

// ruleFacets are the label rules of a project that select an entity, as facets of the rule layer: kind, the names of
// the sources describing it, and the labels those sources set (before any rule) are matched.
func (s *Service) ruleFacets(ctx context.Context, tx *store.Tx, projectID, kind string, live []MergeFacet, labels map[string]any) ([]MergeFacet, error) {
	rows, err := tx.Query(ctx, `SELECT `+labelRuleCols+` FROM label_rules WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []string
	for _, f := range live {
		sources = append(sources, f.SourceName)
	}
	var out []MergeFacet
	for rows.Next() {
		r, err := scanLabelRule(rows)
		if err != nil {
			return nil, err
		}
		m := r.Match
		if len(m.Kinds) > 0 && !globAny(m.Kinds, kind) {
			continue
		}
		if len(m.Sources) > 0 && !slices.ContainsFunc(sources, func(name string) bool { return globAny(m.Sources, name) }) {
			continue
		}
		ok := true
		for k, p := range m.Labels {
			v, has := labels[k].(string)
			if matched, _ := path.Match(p, v); !has || !matched {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		add := map[string]any{}
		for k, v := range r.Labels {
			add[k] = v
		}
		out = append(out, MergeFacet{FacetID: r.ID, SourceID: r.ID, SourceName: "label rule " + r.Name, Layer: LayerRule,
			Priority: LayerRule.DefaultPriority(), Revision: r.ResourceVersion, ObservedAt: r.UpdatedAt, FirstClaimedAt: r.CreatedAt,
			Fields: map[string]any{"metadata": map[string]any{"labels": add}}})
	}
	return out, rows.Err()
}

// withRules merges an entity's live facets with the label rules that select it. Rules see the labels the sources set,
// never other rules' labels, so their order does not chain.
func (s *Service) withRules(ctx context.Context, tx *store.Tx, projectID, kind string, live []MergeFacet, m MergeResult) (MergeResult, error) {
	md, _ := m.Effective["metadata"].(map[string]any)
	labels, _ := md["labels"].(map[string]any)
	rf, err := s.ruleFacets(ctx, tx, projectID, kind, live, maps.Clone(labels))
	if err != nil || len(rf) == 0 {
		return m, err
	}
	return Merge(append(slices.Clone(live), rf...)), nil
}

// LabelKey is a label key in use, with how many live entities carry it and how many distinct values it has.
type LabelKey struct {
	Key      string `json:"key"`
	Entities int    `json:"entities"`
	Values   int    `json:"values"`
}

// LabelKeys lists the label keys of the live entities of the given projects (nil: all), most used first (at most 200).
func (s *Service) LabelKeys(ctx context.Context, projectIDs []string) ([]LabelKey, error) {
	q := `SELECT j.key, COUNT(*), COUNT(DISTINCT j.value) FROM entities e, json_each(e.labels_json) j WHERE e.tombstoned_at IS NULL`
	var args []any
	if projectIDs != nil {
		if len(projectIDs) == 0 {
			return []LabelKey{}, nil
		}
		q += ` AND e.project_id IN (?` + strings.Repeat(`, ?`, len(projectIDs)-1) + `)`
		for _, p := range projectIDs {
			args = append(args, p)
		}
	}
	rows, err := s.Store.R().QueryContext(ctx, q+` GROUP BY j.key ORDER BY COUNT(*) DESC, j.key LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LabelKey{}
	for rows.Next() {
		var k LabelKey
		if err := rows.Scan(&k.Key, &k.Entities, &k.Values); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
