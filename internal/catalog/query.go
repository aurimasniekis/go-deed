package catalog

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Entity is the API envelope of an entity (ADR-002).
type Entity struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   EntityMetadata `json:"metadata"`
	Spec       map[string]any `json:"spec"`
	Status     EntityStatus   `json:"status"`
}

// EntityMetadata is the envelope metadata.
type EntityMetadata struct {
	ID              string            `json:"id"`
	Project         string            `json:"project"`
	ProjectID       string            `json:"projectId"`
	Name            string            `json:"name"`
	DisplayName     string            `json:"displayName,omitempty"`
	Description     string            `json:"description,omitempty"`
	Environment     string            `json:"environment,omitempty"`
	Owner           string            `json:"owner,omitempty"`
	Lifecycle       string            `json:"lifecycle,omitempty"`
	Icon            string            `json:"icon,omitempty"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	Links           []Link            `json:"links"`
	Tags            []string          `json:"tags"`
	Version         VersionInfo       `json:"version"`
	Aliases         []string          `json:"aliases,omitempty"`
	ExternalIDs     []ExternalID      `json:"externalIds,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	ResourceVersion int64             `json:"resourceVersion"`
}

// VersionInfo tracks declared, observed and available versions separately (§13).
type VersionInfo struct {
	Declared  string `json:"declared,omitempty"`
	Observed  string `json:"observed,omitempty"`
	Available string `json:"available,omitempty"`
	Policy    string `json:"policy,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Product   string `json:"product,omitempty"`
}

// ExternalID is a source-scoped identity.
type ExternalID struct {
	SourceID string `json:"sourceId"`
	Source   string `json:"source"`
	ID       string `json:"id"`
}

// EntityStatus holds computed dimensions; declarations never write it.
type EntityStatus struct {
	Health        map[string]any      `json:"health"`
	Freshness     Freshness           `json:"freshness"`
	Observed      map[string]any      `json:"observed"`
	Valid         bool                `json:"valid"`
	Validation    []apperr.FieldError `json:"validation"`
	SchemaVersion string              `json:"schemaVersion"`
	Declared      bool                `json:"declared"`
	Facets        int                 `json:"facets"`
	Conflicts     int                 `json:"conflicts"`
	Findings      map[string]int      `json:"findings"`
	TombstonedAt  *time.Time          `json:"tombstonedAt,omitempty"`
}

// Freshness is the runtime freshness of an entity.
type Freshness struct {
	State      string     `json:"state"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

const entityCols = `e.id, e.project_id, p.name, e.kind, e.name, e.effective_json, e.observed_json, e.health, e.health_json, e.freshness, e.last_seen_at,
	e.valid, e.validation_json, e.schema_version, e.declared, e.facet_count, e.version_declared, e.version_observed, e.version_available,
	e.created_at, e.updated_at, e.tombstoned_at, e.version,
	(SELECT COUNT(*) FROM conflicts c WHERE c.entity_id = e.id)`

func scanEntity(r interface{ Scan(...any) error }) (*Entity, error) {
	var e Entity
	var eff, obs, health, healthJSON, fresh, validation string
	var lastSeen, tomb sql.NullInt64
	var c, u int64
	var vd, vo, va string
	md := &e.Metadata
	if err := r.Scan(&md.ID, &md.ProjectID, &md.Project, &e.Kind, &md.Name, &eff, &obs, &health, &healthJSON, &fresh, &lastSeen, &e.Status.Valid,
		&validation, &e.Status.SchemaVersion, &e.Status.Declared, &e.Status.Facets, &vd, &vo, &va, &c, &u, &tomb, &md.ResourceVersion,
		&e.Status.Conflicts); err != nil {
		return nil, err
	}
	e.APIVersion = APIVersion
	effective := decodeObject(eff)
	m, _ := effective["metadata"].(map[string]any)
	str := func(k string) string {
		v, _ := m[k].(string)
		return v
	}
	md.DisplayName, md.Description, md.Environment, md.Owner, md.Lifecycle, md.Icon = str("displayName"), str("description"), str("environment"),
		str("owner"), str("lifecycle"), str("icon")
	md.Labels, md.Annotations = stringMap(m["labels"]), stringMap(m["annotations"])
	md.Links = []Link{}
	if links, ok := m["links"].([]any); ok {
		for _, l := range links {
			if o, ok := l.(map[string]any); ok {
				t, _ := o["type"].(string)
				ti, _ := o["title"].(string)
				ur, _ := o["url"].(string)
				md.Links = append(md.Links, Link{Type: t, Title: ti, URL: ur})
			}
		}
	}
	md.Tags = []string{}
	if tags, ok := m["tags"].([]any); ok {
		for _, t := range tags {
			md.Tags = append(md.Tags, fmt.Sprint(t))
		}
	}
	md.Version = VersionInfo{Declared: vd, Observed: vo, Available: va}
	if ver, ok := m["version"].(map[string]any); ok {
		md.Version.Policy, _ = ver["policy"].(string)
		md.Version.Channel, _ = ver["channel"].(string)
		md.Version.Product, _ = ver["product"].(string)
	}
	md.CreatedAt, md.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	e.Spec, _ = effective["spec"].(map[string]any)
	if e.Spec == nil {
		e.Spec = map[string]any{}
	}
	e.Status.Observed = decodeObject(obs)
	e.Status.Health = decodeObject(healthJSON)
	e.Status.Health["state"] = health
	e.Status.Freshness = Freshness{State: fresh, LastSeenAt: nullTime(lastSeen)}
	_ = json.Unmarshal([]byte(validation), &e.Status.Validation)
	if e.Status.Validation == nil {
		e.Status.Validation = []apperr.FieldError{}
	}
	e.Status.TombstonedAt = nullTime(tomb)
	e.Status.Findings = map[string]int{}
	return &e, nil
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, x := range m {
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// ErrEntityNotFound is returned for unknown entities.
var ErrEntityNotFound = apperr.New(apperr.CodeNotFound, "entity not found")

// GetEntity returns one entity (including tombstoned ones) with aliases, external IDs and finding counts.
func (s *Service) GetEntity(ctx context.Context, id string) (*Entity, error) {
	d := s.Store.R()
	e, err := scanEntity(d.QueryRowContext(ctx, `SELECT `+entityCols+` FROM entities e JOIN projects p ON p.id = e.project_id WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT name FROM entity_aliases WHERE entity_id = ? ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return nil, err
		}
		e.Metadata.Aliases = append(e.Metadata.Aliases, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = d.QueryContext(ctx, `SELECT x.source_id, s.name, x.external_id FROM entity_external_ids x JOIN sources s ON s.id = x.source_id
		WHERE x.entity_id = ? ORDER BY s.name, x.external_id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x ExternalID
		if err := rows.Scan(&x.SourceID, &x.Source, &x.ID); err != nil {
			rows.Close()
			return nil, err
		}
		e.Metadata.ExternalIDs = append(e.Metadata.ExternalIDs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = d.QueryContext(ctx, `SELECT severity, COUNT(*) FROM findings WHERE entity_id = ? AND state = 'open' GROUP BY severity`, id)
	if err == nil {
		for rows.Next() {
			var sev string
			var n int
			if rows.Scan(&sev, &n) == nil {
				e.Status.Findings[sev] = n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// EntityFilter selects entities.
type EntityFilter struct {
	ProjectIDs        []string // nil = all readable (caller restricts); empty = none
	Kinds             []string
	Environment       []string
	Owner             string
	Health            []string
	Freshness         []string
	Lifecycle         []string
	Query             string            // full-text search
	Labels            map[string]string // exact label matches
	SourceID          string            // entities with a live facet from this source
	Declared          *bool
	Valid             *bool
	IncludeTombstoned bool
	IDs               []string
	Sort              string // name, kind, updated_at, health, freshness, environment (prefix - for descending)
	Limit             int
	Cursor            string
}

// EntityPage is a page of entities.
type EntityPage struct {
	Items      []*Entity `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	Total      int       `json:"total"`
}

var sortCols = map[string]string{"name": "e.name", "kind": "e.kind", "updated_at": "e.updated_at", "created_at": "e.created_at", "health": "e.health",
	"freshness": "e.freshness", "environment": "e.environment", "display_name": "e.display_name", "last_seen_at": "COALESCE(e.last_seen_at, 0)"}

var ftsToken = regexp.MustCompile(`[\p{L}\p{N}_.:/-]+`)

// FTSQuery converts user input into a safe FTS5 prefix query (no operators are passed through).
func FTSQuery(q string) string {
	toks := ftsToken.FindAllString(strings.ToLower(q), 8)
	var parts []string
	for _, t := range toks {
		t = strings.Trim(t, "-./:")
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " AND ")
}

type cursor struct {
	Offset int `json:"o"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Offset < 0 || c.Offset > 1_000_000 {
		return c, apperr.New(apperr.CodeInvalid, "invalid cursor")
	}
	return c, nil
}

func inClause(col string, vals []string, args *[]any) string {
	ph := strings.Repeat(",?", len(vals))[1:]
	for _, v := range vals {
		*args = append(*args, v)
	}
	return col + " IN (" + ph + ")"
}

// ListEntities returns a filtered page of entities.
func (s *Service) ListEntities(ctx context.Context, f EntityFilter) (*EntityPage, error) {
	if f.ProjectIDs != nil && len(f.ProjectIDs) == 0 {
		return &EntityPage{Items: []*Entity{}}, nil
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	var conds []string
	var args []any
	from := `entities e JOIN projects p ON p.id = e.project_id`
	if q := FTSQuery(f.Query); q != "" {
		from += ` JOIN entity_search fs ON fs.rowid = e.rid`
		conds = append(conds, `entity_search MATCH ?`)
		args = append(args, q)
	} else if strings.TrimSpace(f.Query) != "" {
		return &EntityPage{Items: []*Entity{}}, nil
	}
	if !f.IncludeTombstoned {
		conds = append(conds, "e.tombstoned_at IS NULL")
	}
	if f.ProjectIDs != nil {
		conds = append(conds, inClause("e.project_id", f.ProjectIDs, &args))
	}
	for col, vals := range map[string][]string{"e.kind": f.Kinds, "e.environment": f.Environment, "e.health": f.Health, "e.freshness": f.Freshness,
		"e.lifecycle": f.Lifecycle, "e.id": f.IDs} {
		if len(vals) > 0 {
			conds = append(conds, inClause(col, vals, &args))
		}
	}
	if f.Owner != "" {
		conds = append(conds, "e.owner = ?")
		args = append(args, f.Owner)
	}
	keys := make([]string, 0, len(f.Labels))
	for k := range f.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		conds = append(conds, "json_extract(e.labels_json, ?) = ?")
		args = append(args, `$."`+strings.ReplaceAll(k, `"`, ``)+`"`, f.Labels[k])
	}
	if f.SourceID != "" {
		conds = append(conds, "e.id IN (SELECT fc.entity_id FROM facets fc WHERE fc.source_id = ? AND fc.tombstoned_at IS NULL)")
		args = append(args, f.SourceID)
	}
	if f.Declared != nil {
		conds = append(conds, "e.declared = ?")
		args = append(args, *f.Declared)
	}
	if f.Valid != nil {
		conds = append(conds, "e.valid = ?")
		args = append(args, *f.Valid)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	d := s.Store.R()
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	order := "e.kind, e.name, e.id"
	if f.Sort != "" {
		desc := strings.HasPrefix(f.Sort, "-")
		col, ok := sortCols[strings.TrimPrefix(f.Sort, "-")]
		if !ok {
			return nil, apperr.New(apperr.CodeInvalid, "unknown sort field %q", strings.TrimPrefix(f.Sort, "-"))
		}
		if desc {
			col += " DESC"
		}
		order = col + ", e.id"
	} else if f.Query != "" {
		order = "bm25(entity_search, 10.0, 8.0, 2.0, 1.0, 2.0, 1.0), e.id"
	}
	rows, err := d.QueryContext(ctx, `SELECT `+entityCols+` FROM `+from+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(args, f.Limit+1, cur.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &EntityPage{Items: []*Entity{}, Total: total}
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = encodeCursor(cursor{Offset: cur.Offset + f.Limit})
	}
	return page, nil
}

// Facet is one source's contribution to an entity.
type Facet struct {
	ID              string         `json:"id"`
	SourceID        string         `json:"source_id"`
	Source          string         `json:"source"`
	Provider        string         `json:"provider"`
	Layer           Layer          `json:"layer"`
	Generation      int64          `json:"generation"`
	Revision        int64          `json:"revision"`
	Fields          map[string]any `json:"fields"`
	ExternalID      string         `json:"external_id,omitempty"`
	ObservedAt      time.Time      `json:"observed_at"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	TombstonedAt    *time.Time     `json:"tombstoned_at,omitempty"`
	FirstClaimedAt  time.Time      `json:"first_claimed_at"`
	Live            bool           `json:"live"`
	Absent          bool           `json:"absent_from_last_snapshot"`
	ResourceVersion int64          `json:"resource_version"`
}

// Facets lists an entity's facets (sourceName filters to one source).
func (s *Service) Facets(ctx context.Context, entityID, sourceName string) ([]Facet, error) {
	q := `SELECT f.id, f.source_id, s.name, s.provider, f.layer, f.generation, f.revision, f.fields_json, f.external_id, f.observed_at, f.expires_at,
		f.tombstoned_at, f.first_claimed_at, f.version, s.last_generation FROM facets f JOIN sources s ON s.id = f.source_id WHERE f.entity_id = ?`
	args := []any{entityID}
	if sourceName != "" {
		q += ` AND s.name = ?`
		args = append(args, sourceName)
	}
	rows, err := s.Store.R().QueryContext(ctx, q+` ORDER BY f.layer, s.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Clock.Now().UnixMicro()
	out := []Facet{}
	for rows.Next() {
		var f Facet
		var layer, fields string
		var obs, first, lastGen int64
		var exp, tomb sql.NullInt64
		if err := rows.Scan(&f.ID, &f.SourceID, &f.Source, &f.Provider, &layer, &f.Generation, &f.Revision, &fields, &f.ExternalID, &obs, &exp, &tomb,
			&first, &f.ResourceVersion, &lastGen); err != nil {
			return nil, err
		}
		f.Layer = Layer(layer)
		f.Fields = decodeObject(fields)
		f.ObservedAt, f.FirstClaimedAt = clock.FromMicros(obs), clock.FromMicros(first)
		f.ExpiresAt, f.TombstonedAt = nullTime(exp), nullTime(tomb)
		f.Live = !tomb.Valid && (!exp.Valid || exp.Int64 > now)
		f.Absent = f.Live && f.Generation > 0 && f.Generation < lastGen
		out = append(out, f)
	}
	return out, rows.Err()
}

// Provenance returns the per-pointer provenance of an entity's effective view ("why does this value exist?").
func (s *Service) Provenance(ctx context.Context, entityID string) (map[string]Provenance, error) {
	var pj string
	err := s.Store.R().QueryRowContext(ctx, `SELECT provenance_json FROM entities WHERE id = ?`, entityID).Scan(&pj)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}
	out := map[string]Provenance{}
	_ = json.Unmarshal([]byte(pj), &out)
	return out, nil
}

// ConflictRow is a stored same-layer disagreement.
type ConflictRow struct {
	Pointer      string    `json:"pointer"`
	Layer        Layer     `json:"layer"`
	WinnerSource string    `json:"winner_source_id"`
	Winner       string    `json:"winner_source"`
	WinnerValue  any       `json:"winner_value"`
	LoserSource  string    `json:"loser_source_id"`
	Loser        string    `json:"loser_source"`
	LoserValue   any       `json:"loser_value"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// Conflicts lists an entity's conflicts.
func (s *Service) Conflicts(ctx context.Context, entityID string) ([]ConflictRow, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT c.pointer, c.layer, c.winner_source, COALESCE(ws.name, '?'), c.winner_value, c.loser_source,
		COALESCE(ls.name, '?'), c.loser_value, c.first_seen_at, c.last_seen_at FROM conflicts c
		LEFT JOIN sources ws ON ws.id = c.winner_source LEFT JOIN sources ls ON ls.id = c.loser_source WHERE c.entity_id = ? ORDER BY c.pointer`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConflictRow{}
	for rows.Next() {
		var c ConflictRow
		var layer, wv, lv string
		var f, l int64
		if err := rows.Scan(&c.Pointer, &layer, &c.WinnerSource, &c.Winner, &wv, &c.LoserSource, &c.Loser, &lv, &f, &l); err != nil {
			return nil, err
		}
		c.Layer = Layer(layer)
		_ = json.Unmarshal([]byte(wv), &c.WinnerValue)
		_ = json.Unmarshal([]byte(lv), &c.LoserValue)
		c.FirstSeenAt, c.LastSeenAt = clock.FromMicros(f), clock.FromMicros(l)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChangeEvent is a change-log row.
type ChangeEvent struct {
	Seq        int64          `json:"seq"`
	At         time.Time      `json:"at"`
	ProjectID  string         `json:"project_id"`
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Action     string         `json:"action"`
	ActorType  string         `json:"actor_type,omitempty"`
	ActorID    string         `json:"actor_id,omitempty"`
	SourceID   string         `json:"source_id,omitempty"`
	Source     string         `json:"source,omitempty"`
	Summary    map[string]any `json:"summary"`
}

// ChangeFilter selects change-log rows.
type ChangeFilter struct {
	ObjectID   string
	ProjectIDs []string // nil = all
	AfterSeq   int64
	BeforeSeq  int64
	Limit      int
	Ascending  bool
}

// Changes lists change-log rows.
func (s *Service) Changes(ctx context.Context, f ChangeFilter) ([]ChangeEvent, error) {
	if f.ProjectIDs != nil && len(f.ProjectIDs) == 0 {
		return []ChangeEvent{}, nil
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 1000)
	var conds []string
	var args []any
	if f.ObjectID != "" {
		conds = append(conds, "c.object_id = ?")
		args = append(args, f.ObjectID)
	}
	if f.ProjectIDs != nil {
		conds = append(conds, inClause("c.project_id", f.ProjectIDs, &args))
	}
	if f.AfterSeq > 0 {
		conds = append(conds, "c.seq > ?")
		args = append(args, f.AfterSeq)
	}
	if f.BeforeSeq > 0 {
		conds = append(conds, "c.seq < ?")
		args = append(args, f.BeforeSeq)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	order := "DESC"
	if f.Ascending {
		order = "ASC"
	}
	rows, err := s.Store.R().QueryContext(ctx, `SELECT c.seq, c.at, c.project_id, c.object_type, c.object_id, c.action, c.actor_type, c.actor_id, c.source_id,
		COALESCE(s.name, ''), c.summary_json FROM change_log c LEFT JOIN sources s ON s.id = c.source_id`+where+` ORDER BY c.seq `+order+` LIMIT ?`,
		append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeEvent{}
	for rows.Next() {
		var c ChangeEvent
		var at int64
		var sum string
		if err := rows.Scan(&c.Seq, &at, &c.ProjectID, &c.ObjectType, &c.ObjectID, &c.Action, &c.ActorType, &c.ActorID, &c.SourceID, &c.Source, &sum); err != nil {
			return nil, err
		}
		c.At = clock.FromMicros(at)
		c.Summary = decodeObject(sum)
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestSeq returns the newest change-log sequence number.
func (s *Service) LatestSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.Store.R().QueryRowContext(ctx, `SELECT MAX(seq) FROM change_log`).Scan(&seq)
	return seq.Int64, err
}

// Relation is the API view of a relationship.
type Relation struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	ProjectID     string         `json:"project_id"`
	From          EntityRef      `json:"from"`
	To            EntityRef      `json:"to"`
	Attributes    map[string]any `json:"attributes"`
	EvidenceClass string         `json:"evidence_class"`
	Inferred      bool           `json:"inferred"`
	CrossProject  bool           `json:"cross_project"`
	BridgeID      string         `json:"bridge_id,omitempty"`
	Live          bool           `json:"live"`
	LastSeenAt    *time.Time     `json:"last_seen_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Assertions    []Assertion    `json:"assertions,omitempty"`
}

// EntityRef is a compact entity reference in relation views.
type EntityRef struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Health      string `json:"health,omitempty"`
	Tombstoned  bool   `json:"tombstoned,omitempty"`
	Redacted    bool   `json:"redacted,omitempty"`
}

// Assertion is one source's claim of a relationship.
type Assertion struct {
	ID            string         `json:"id"`
	SourceID      string         `json:"source_id"`
	Source        string         `json:"source"`
	Provider      string         `json:"provider"`
	Layer         Layer          `json:"layer"`
	EvidenceClass string         `json:"evidence_class"`
	Inferred      bool           `json:"inferred"`
	Evidence      map[string]any `json:"evidence"`
	Confidence    float64        `json:"confidence"`
	Attributes    map[string]any `json:"attributes"`
	Revision      int64          `json:"revision"`
	ExternalID    string         `json:"external_id,omitempty"`
	FirstSeenAt   time.Time      `json:"first_seen_at"`
	LastSeenAt    time.Time      `json:"last_seen_at"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	Live          bool           `json:"live"`
}

const relCols = `r.id, r.type, r.project_id, r.from_id, fe.project_id, fe.kind, fe.name, fe.display_name, fe.health, fe.tombstoned_at IS NOT NULL,
	r.to_id, te.project_id, te.kind, te.name, te.display_name, te.health, te.tombstoned_at IS NOT NULL,
	r.attributes_json, r.evidence_class, r.inferred, COALESCE(r.bridge_id, ''), r.live, r.last_seen_at, r.created_at, r.updated_at`

const relFrom = ` FROM relations r JOIN entities fe ON fe.id = r.from_id JOIN entities te ON te.id = r.to_id`

func scanRelation(r interface{ Scan(...any) error }) (*Relation, error) {
	var x Relation
	var attrs string
	var last sql.NullInt64
	var c, u int64
	if err := r.Scan(&x.ID, &x.Type, &x.ProjectID, &x.From.ID, &x.From.ProjectID, &x.From.Kind, &x.From.Name, &x.From.DisplayName, &x.From.Health,
		&x.From.Tombstoned, &x.To.ID, &x.To.ProjectID, &x.To.Kind, &x.To.Name, &x.To.DisplayName, &x.To.Health, &x.To.Tombstoned,
		&attrs, &x.EvidenceClass, &x.Inferred, &x.BridgeID, &x.Live, &last, &c, &u); err != nil {
		return nil, err
	}
	x.Attributes = decodeObject(attrs)
	x.CrossProject = x.From.ProjectID != x.To.ProjectID
	x.LastSeenAt = nullTime(last)
	x.CreatedAt, x.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &x, nil
}

// ErrRelationNotFound is returned for unknown relations.
var ErrRelationNotFound = apperr.New(apperr.CodeNotFound, "relationship not found")

// GetRelation returns a relation with its assertions.
func (s *Service) GetRelation(ctx context.Context, id string) (*Relation, error) {
	r, err := scanRelation(s.Store.R().QueryRowContext(ctx, `SELECT `+relCols+relFrom+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRelationNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Assertions, err = s.assertions(ctx, id)
	return r, err
}

func (s *Service) assertions(ctx context.Context, relID string) ([]Assertion, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT a.id, a.source_id, s.name, s.provider, a.layer, a.evidence_class, a.inferred, a.evidence_json,
		a.confidence, a.attributes_json, a.revision, a.external_id, a.first_seen_at, a.last_seen_at, a.expires_at, a.tombstoned_at
		FROM relation_assertions a JOIN sources s ON s.id = a.source_id WHERE a.relation_id = ? ORDER BY a.layer, s.name`, relID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Clock.Now().UnixMicro()
	out := []Assertion{}
	for rows.Next() {
		var a Assertion
		var layer, ev, attrs string
		var f, l int64
		var exp, tomb sql.NullInt64
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Source, &a.Provider, &layer, &a.EvidenceClass, &a.Inferred, &ev, &a.Confidence, &attrs, &a.Revision,
			&a.ExternalID, &f, &l, &exp, &tomb); err != nil {
			return nil, err
		}
		a.Layer = Layer(layer)
		a.Evidence, a.Attributes = decodeObject(ev), decodeObject(attrs)
		a.FirstSeenAt, a.LastSeenAt, a.ExpiresAt = clock.FromMicros(f), clock.FromMicros(l), nullTime(exp)
		a.Live = !tomb.Valid && (!exp.Valid || exp.Int64 > now)
		out = append(out, a)
	}
	return out, rows.Err()
}

// RelationFilter selects relations.
type RelationFilter struct {
	ProjectIDs  []string // readable projects (nil = all)
	EntityID    string
	Direction   string // out, in, both (with EntityID)
	Types       []string
	SourceID    string
	Evidence    []string
	IncludeDead bool
	Limit       int
	Cursor      string
}

// RelationPage is a page of relations.
type RelationPage struct {
	Items      []*Relation `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	Total      int         `json:"total"`
}

// ListRelations returns a page of relations. Endpoints in projects outside ProjectIDs are redacted by the caller.
func (s *Service) ListRelations(ctx context.Context, f RelationFilter) (*RelationPage, error) {
	if f.ProjectIDs != nil && len(f.ProjectIDs) == 0 {
		return &RelationPage{Items: []*Relation{}}, nil
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 1000)
	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	var conds []string
	var args []any
	if !f.IncludeDead {
		conds = append(conds, "r.live = 1 AND fe.tombstoned_at IS NULL AND te.tombstoned_at IS NULL")
	}
	if f.ProjectIDs != nil {
		// A relation is visible when either endpoint's project is readable; the other endpoint may be redacted.
		var a1, a2 []any
		c1 := inClause("fe.project_id", f.ProjectIDs, &a1)
		c2 := inClause("te.project_id", f.ProjectIDs, &a2)
		conds = append(conds, "("+c1+" OR "+c2+")")
		args = append(append(args, a1...), a2...)
	}
	if f.EntityID != "" {
		switch f.Direction {
		case "out":
			conds = append(conds, "r.from_id = ?")
			args = append(args, f.EntityID)
		case "in":
			conds = append(conds, "r.to_id = ?")
			args = append(args, f.EntityID)
		default:
			conds = append(conds, "(r.from_id = ? OR r.to_id = ?)")
			args = append(args, f.EntityID, f.EntityID)
		}
	}
	if len(f.Types) > 0 {
		conds = append(conds, inClause("r.type", f.Types, &args))
	}
	if len(f.Evidence) > 0 {
		conds = append(conds, inClause("r.evidence_class", f.Evidence, &args))
	}
	if f.SourceID != "" {
		conds = append(conds, "r.id IN (SELECT a.relation_id FROM relation_assertions a WHERE a.source_id = ? AND a.tombstoned_at IS NULL)")
		args = append(args, f.SourceID)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	d := s.Store.R()
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*)`+relFrom+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT `+relCols+relFrom+where+` ORDER BY r.type, fe.name, te.name, r.id LIMIT ? OFFSET ?`,
		append(args, f.Limit+1, cur.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RelationPage{Items: []*Relation{}, Total: total}
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = encodeCursor(cursor{Offset: cur.Offset + f.Limit})
	}
	return page, nil
}

// UnresolvedRef is a relation endpoint a source referenced that does not exist.
type UnresolvedRef struct {
	ID          string    `json:"id"`
	SourceID    string    `json:"source_id"`
	ProjectID   string    `json:"project_id"`
	Type        string    `json:"type"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	FromEntity  string    `json:"from_entity,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Unresolved lists unresolved relation endpoints.
func (s *Service) Unresolved(ctx context.Context, projectIDs []string, limit int) ([]UnresolvedRef, error) {
	if projectIDs != nil && len(projectIDs) == 0 {
		return []UnresolvedRef{}, nil
	}
	var args []any
	where := ""
	if projectIDs != nil {
		where = " WHERE " + inClause("project_id", projectIDs, &args)
	}
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 1000)
	rows, err := s.Store.R().QueryContext(ctx, `SELECT id, source_id, project_id, type, from_ref, to_ref, COALESCE(from_entity, ''), first_seen_at, last_seen_at
		FROM unresolved_refs`+where+` ORDER BY last_seen_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnresolvedRef{}
	for rows.Next() {
		var u UnresolvedRef
		var f, l int64
		if err := rows.Scan(&u.ID, &u.SourceID, &u.ProjectID, &u.Type, &u.From, &u.To, &u.FromEntity, &f, &l); err != nil {
			return nil, err
		}
		u.FirstSeenAt, u.LastSeenAt = clock.FromMicros(f), clock.FromMicros(l)
		out = append(out, u)
	}
	return out, rows.Err()
}

// Counts summarizes the catalog for dashboards and metrics.
type Counts struct {
	Entities      int            `json:"entities"`
	Relations     int            `json:"relations"`
	ByKind        map[string]int `json:"by_kind"`
	ByEnvironment map[string]int `json:"by_environment"`
	ByOwner       map[string]int `json:"by_owner"`
	ByHealth      map[string]int `json:"by_health"`
	ByFreshness   map[string]int `json:"by_freshness"`
	Invalid       int            `json:"invalid"`
	Undeclared    int            `json:"undeclared"`
	Custom        int            `json:"custom"`
}

// CountEntities aggregates live entities in the given projects (nil = all).
func (s *Service) CountEntities(ctx context.Context, projectIDs []string) (*Counts, error) {
	c := &Counts{ByKind: map[string]int{}, ByEnvironment: map[string]int{}, ByOwner: map[string]int{}, ByHealth: map[string]int{}, ByFreshness: map[string]int{}}
	if projectIDs != nil && len(projectIDs) == 0 {
		return c, nil
	}
	var args []any
	where := " WHERE e.tombstoned_at IS NULL"
	if projectIDs != nil {
		where += " AND " + inClause("e.project_id", projectIDs, &args)
	}
	d := s.Store.R()
	for col, m := range map[string]map[string]int{"e.kind": c.ByKind, "e.environment": c.ByEnvironment, "e.owner": c.ByOwner, "e.health": c.ByHealth,
		"e.freshness": c.ByFreshness} {
		rows, err := d.QueryContext(ctx, `SELECT `+col+`, COUNT(*) FROM entities e`+where+` GROUP BY `+col, args...) //nolint:gosec // col comes from a fixed map; values are bound
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return nil, err
			}
			m[k] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, n := range c.ByKind {
		c.Entities += n
	}
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(SUM(valid = 0), 0), COALESCE(SUM(declared = 0), 0) FROM entities e`+where, args...).Scan(&c.Invalid, &c.Undeclared); err != nil {
		return nil, err
	}
	for k, n := range c.ByKind {
		if kd := s.reg.kind(k); kd != nil && !kd.Builtin {
			c.Custom += n
		}
	}
	rargs := []any{}
	rwhere := " WHERE r.live = 1"
	if projectIDs != nil {
		rwhere += " AND " + inClause("r.project_id", projectIDs, &rargs)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM relations r`+rwhere, rargs...).Scan(&c.Relations); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseIntDefault parses s or returns def.
func ParseIntDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// LatestSeqTx returns the newest change-log sequence number inside a transaction.
func (s *Service) LatestSeqTx(ctx context.Context, tx *store.Tx) (int64, error) {
	var seq sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT MAX(seq) FROM change_log`).Scan(&seq)
	return seq.Int64, err
}

// FindByPointer finds a single live entity in a project whose effective value at pointer equals value (case
// insensitive), among kinds. It prefers identity keys and falls back to a bounded JSON scan.
func (s *Service) FindByPointer(ctx context.Context, projectID string, kinds []string, pointer, value string) (string, bool) {
	if len(kinds) == 0 || value == "" || !strings.HasPrefix(pointer, "/") {
		return "", false
	}
	d := s.Store.R()
	var args []any
	args = append(args, projectID, pointer+"="+strings.ToLower(value))
	kin := inClause("kind", kinds, &args)
	rows, err := d.QueryContext(ctx, `SELECT DISTINCT entity_id FROM entity_identity_keys WHERE project_id = ? AND key_value = ? AND `+kin+` LIMIT 2`, args...) //nolint:gosec // inClause emits placeholders only
	if err == nil {
		var found []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				found = append(found, id)
			}
		}
		rows.Close()
		if rows.Err() != nil {
			return "", false
		}
		if len(found) == 1 {
			return found[0], true
		}
		if len(found) > 1 {
			return "", false
		}
	}
	path := "$." + strings.ReplaceAll(strings.TrimPrefix(pointer, "/"), "/", ".")
	args = []any{projectID, path, strings.ToLower(value)}
	kin = inClause("kind", kinds, &args)
	rows, err = d.QueryContext(ctx, `SELECT id FROM entities WHERE project_id = ? AND tombstoned_at IS NULL AND lower(json_extract(effective_json, ?)) = ? AND `+kin+` LIMIT 2`, args...) //nolint:gosec // inClause emits placeholders only
	if err != nil {
		return "", false
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			found = append(found, id)
		}
	}
	if rows.Err() != nil {
		return "", false
	}
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}
