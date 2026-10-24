package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/outbox"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Service is the catalog.
type Service struct {
	Store   *store.Store
	Audit   *audit.Writer
	Keyring *keyring.Keyring
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Notify is called after a commit that appended change-log rows (live updates).
	Notify func(lastSeq int64)
	// EntityHooks run inside the transaction after an entity's effective view changed (findings).
	EntityHooks []func(ctx context.Context, tx *store.Tx, ch EntityChange) error
	// MaxEntities bounds the catalog size.
	MaxEntities int
	// DefaultRemovalPercent is the snapshot mass-removal guard default.
	DefaultRemovalPercent float64

	reg registry
}

// New builds the catalog service. Call SeedBuiltins before use.
func New(s *store.Store, aw *audit.Writer, kr *keyring.Keyring, c clock.Clock, m *metrics.Metrics, log *slog.Logger) *Service {
	if c == nil {
		c = clock.System{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{Store: s, Audit: aw, Keyring: kr, Clock: c, Metrics: m, Log: log, MaxEntities: 200000, DefaultRemovalPercent: 25,
		reg: registry{kinds: map[string]*Kind{}, rels: map[string]*RelType{}}}
}

// Project is an organizational and security scope (ADR-004).
type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	DisplayName     string    `json:"display_name"`
	Description     string    `json:"description"`
	AllowHTTPLinks  bool      `json:"allow_http_links"`
	Synthetic       bool      `json:"synthetic"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

const projectCols = `id, name, display_name, description, allow_http_links, synthetic, created_at, updated_at, version`

func scanProject(r interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var c, u int64
	if err := r.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Description, &p.AllowHTTPLinks, &p.Synthetic, &c, &u, &p.ResourceVersion); err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = clock.FromMicros(c), clock.FromMicros(u)
	return &p, nil
}

// ErrProjectNotFound is returned for unknown projects.
var ErrProjectNotFound = apperr.New(apperr.CodeNotFound, "project not found")

// Projects lists every project.
func (s *Service) Projects(ctx context.Context) ([]*Project, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT `+projectCols+` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectIDs lists every project ID.
func (s *Service) ProjectIDs(ctx context.Context) ([]string, error) {
	ps, err := s.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out, nil
}

// Project returns a project by ID or name.
func (s *Service) Project(ctx context.Context, idOrName string) (*Project, error) {
	p, err := scanProject(s.Store.R().QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ? OR name = ?`, idOrName, idOrName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

func projectTx(ctx context.Context, tx *store.Tx, idOrName string) (*Project, error) {
	p, err := scanProject(tx.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ? OR name = ?`, idOrName, idOrName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

// ProjectInput creates or updates a project.
type ProjectInput struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description,omitempty"`
	AllowHTTPLinks bool   `json:"allow_http_links,omitempty"`
	Synthetic      bool   `json:"synthetic,omitempty"`
}

// System sources created with every project (ADR-003): declarations through the API, the Terraform provider, UI
// annotations and authorized overrides.
var systemSources = []struct {
	name, display string
	layer         Layer
}{
	{"api", "API declarations", LayerDeclared},
	{"terraform", "Terraform provider", LayerDeclared},
	{"ui", "UI annotations", LayerAnnotation},
	{"overrides", "Authorized overrides", LayerOverride},
}

// CreateProject creates a project with its system sources. creator (optional) becomes its admin.
func (s *Service) CreateProject(ctx context.Context, actor audit.Actor, in ProjectInput, creatorUserID string) (*Project, error) {
	v := &apperr.Validation{}
	if !ValidProjectName(in.Name) {
		v.Add("name", "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Name
	}
	checkString(in.DisplayName, 128, false, "display_name", v)
	checkString(in.Description, 4096, true, "description", v)
	if err := v.Err(); err != nil {
		return nil, err
	}
	id := ids.New(ids.Project)
	err := s.Store.W().TxNamed(ctx, "catalog.project_create", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO projects (id, name, display_name, description, allow_http_links, synthetic, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, in.DisplayName, in.Description, in.AllowHTTPLinks, in.Synthetic, tx.NowMicros(), tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeAlreadyExists, "project %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		for _, ss := range systemSources {
			if _, err := tx.Exec(ctx, `INSERT INTO sources (id, project_id, name, display_name, provider, layer, system, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'manual', ?, 1, ?, ?)`, ids.New(ids.Source), id, ss.name, ss.display, string(ss.layer), tx.NowMicros(), tx.NowMicros()); err != nil {
				return err
			}
		}
		if creatorUserID != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'admin', ?)`,
				id, creatorUserID, tx.NowMicros()); err != nil {
				return err
			}
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: id, Action: "project.created", Target: audit.Target{Type: "project", ID: id},
			Details: map[string]any{"name": in.Name}}); err != nil {
			return err
		}
		if err := s.changeTx(ctx, tx, change{project: id, typ: "project", id: id, action: "created", actor: actor, summary: map[string]any{"name": in.Name}}); err != nil {
			return err
		}
		_, err = outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "project", Action: "created", ProjectID: id, Payload: map[string]any{"name": in.Name}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, id)
}

// UpdateProject changes display fields and link policy.
func (s *Service) UpdateProject(ctx context.Context, actor audit.Actor, id string, in ProjectInput, ifMatch int64) (*Project, error) {
	v := &apperr.Validation{}
	checkString(in.DisplayName, 128, false, "display_name", v)
	checkString(in.Description, 4096, true, "description", v)
	if err := v.Err(); err != nil {
		return nil, err
	}
	err := s.Store.W().TxNamed(ctx, "catalog.project_update", func(tx *store.Tx) error {
		p, err := projectTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.DisplayName == "" {
			in.DisplayName = p.DisplayName
		}
		n, err := tx.ExecN(ctx, `UPDATE projects SET display_name = ?, description = ?, allow_http_links = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND (? = 0 OR version = ?)`, in.DisplayName, in.Description, in.AllowHTTPLinks, tx.NowMicros(), p.ID, ifMatch, ifMatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodePrecondition, "project was modified concurrently; re-read it")
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: p.ID, Action: "project.updated", Target: audit.Target{Type: "project", ID: p.ID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, id)
}

// DeleteProject deletes a project and everything in it. It refuses while non-system sources or live entities exist
// unless force is set (callers require installation admin and recent authentication).
func (s *Service) DeleteProject(ctx context.Context, actor audit.Actor, id string, force bool) error {
	return s.Store.W().TxNamed(ctx, "catalog.project_delete", func(tx *store.Tx) error {
		p, err := projectTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !force {
			var n int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM entities WHERE project_id = ? AND tombstoned_at IS NULL`, p.ID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return apperr.New(apperr.CodeConflict, "project %q still has %d entities; delete them or use force", p.Name, n)
			}
		}
		// Cross-project relations pointing into this project are removed with their entities (FK cascade).
		if _, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = ?`, p.ID); err != nil {
			return err
		}
		if _, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: p.ID, Action: "project.deleted", Target: audit.Target{Type: "project", ID: p.ID},
			Details: map[string]any{"name": p.Name, "force": force}}); err != nil {
			return err
		}
		_, err = outbox.Enqueue(ctx, tx, outbox.Event{Area: "catalog", Entity: "project", Action: "deleted", ProjectID: p.ID, Payload: map[string]any{"name": p.Name}})
		return err
	})
}

// Bridge permits relations between two projects (ADR-004).
type Bridge struct {
	ID           string    `json:"id"`
	FromProject  string    `json:"from_project"`
	ToProject    string    `json:"to_project"`
	AllowedTypes []string  `json:"allowed_types"`
	Description  string    `json:"description"`
	CreatedAt    time.Time `json:"created_at"`
}

// Bridges lists bridges touching any of the given projects (nil = all).
func (s *Service) Bridges(ctx context.Context) ([]Bridge, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT id, from_project, to_project, allowed_types, description, created_at FROM bridges ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bridge{}
	for rows.Next() {
		var b Bridge
		var at string
		var c int64
		if err := rows.Scan(&b.ID, &b.FromProject, &b.ToProject, &at, &b.Description, &c); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(at), &b.AllowedTypes)
		if b.AllowedTypes == nil {
			b.AllowedTypes = []string{}
		}
		b.CreatedAt = clock.FromMicros(c)
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBridge declares a bridge (admin of both projects; checked by the caller).
func (s *Service) CreateBridge(ctx context.Context, actor audit.Actor, from, to string, types []string, desc string) (*Bridge, error) {
	if from == to {
		return nil, apperr.New(apperr.CodeInvalid, "a bridge connects two different projects")
	}
	for _, t := range types {
		if s.reg.rel(t) == nil {
			return nil, apperr.New(apperr.CodeInvalid, "unknown relationship type %q", t)
		}
	}
	b := &Bridge{ID: ids.New(ids.Bridge), FromProject: from, ToProject: to, AllowedTypes: nonNil(types), Description: desc}
	err := s.Store.W().TxNamed(ctx, "catalog.bridge_create", func(tx *store.Tx) error {
		tj, _ := json.Marshal(b.AllowedTypes)
		b.CreatedAt = tx.Now
		_, err := tx.Exec(ctx, `INSERT INTO bridges (id, from_project, to_project, allowed_types, description, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			b.ID, from, to, string(tj), desc, tx.NowMicros())
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeConflict, "a bridge between these projects already exists, or a project does not exist")
		}
		if err != nil {
			return err
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: from, Action: "bridge.created", Target: audit.Target{Type: "bridge", ID: b.ID},
			Details: map[string]any{"to_project": to, "types": b.AllowedTypes}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// DeleteBridge removes a bridge; relations that relied on it stay but are flagged by the relation query (bridge_id
// no longer resolves) and raise findings.
func (s *Service) DeleteBridge(ctx context.Context, actor audit.Actor, id string) error {
	return s.Store.W().TxNamed(ctx, "catalog.bridge_delete", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `DELETE FROM bridges WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "bridge not found")
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "bridge.deleted", Target: audit.Target{Type: "bridge", ID: id}})
		return err
	})
}

// change is one change-log row.
type change struct {
	project, typ, id, action, sourceID string
	actor                              audit.Actor
	summary                            map[string]any
}

// changeTx appends a change-log row and schedules a live notification.
func (s *Service) changeTx(ctx context.Context, tx *store.Tx, c change) error {
	res, err := tx.Exec(ctx, `INSERT INTO change_log (at, project_id, object_type, object_id, action, actor_type, actor_id, source_id, summary_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, tx.NowMicros(), c.project, c.typ, c.id, c.action, c.actor.Type, c.actor.ID, c.sourceID, rawJSON(c.summary))
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if s.Notify != nil {
		tx.AfterCommit(func() { s.Notify(seq) })
	}
	return nil
}
