package icons

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Icon is a stored custom icon (referenced as custom:<name>).
type Icon struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Ref        string    `json:"ref" doc:"Icon reference for metadata.icon (custom:<name>)."`
	URL        string    `json:"url"`
	SHA256     string    `json:"sha256"`
	Monochrome bool      `json:"monochrome" doc:"Only black/currentColor paint: the UI tints it for the theme."`
	Bytes      int       `json:"bytes"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// Service stores icons.
type Service struct {
	Store *store.Store
	Audit *audit.Writer
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ErrNotFound is returned for unknown icons.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "icon not found")

func view(id, name, sum string, mono bool, size int, by string, at int64) Icon {
	return Icon{ID: id, Name: name, Ref: "custom:" + name, URL: "/api/v1/icons/" + name + ".svg", SHA256: sum, Monochrome: mono, Bytes: size,
		CreatedBy: by, CreatedAt: clock.FromMicros(at)}
}

// List returns every icon.
func (s *Service) List(ctx context.Context) ([]Icon, error) {
	rows, err := s.Store.R().QueryContext(ctx, `SELECT id, name, sha256, monochrome, length(svg), created_by, created_at FROM custom_icons ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Icon{}
	for rows.Next() {
		var id, name, sum, by string
		var mono bool
		var n int
		var at int64
		if err := rows.Scan(&id, &name, &sum, &mono, &n, &by, &at); err != nil {
			return nil, err
		}
		out = append(out, view(id, name, sum, mono, n, by, at))
	}
	return out, rows.Err()
}

// Put sanitizes and stores (or replaces) an icon.
func (s *Service) Put(ctx context.Context, actor audit.Actor, name string, svg []byte) (*Icon, error) {
	if !nameRE.MatchString(name) {
		return nil, apperr.New(apperr.CodeInvalid, "icon names are lowercase slugs")
	}
	clean, mono, err := Sanitize(svg)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "%s", strings.TrimPrefix(err.Error(), "icons: "))
	}
	sum := sha256.Sum256(clean)
	hexSum := hex.EncodeToString(sum[:])
	id := ids.New(ids.Icon)
	err = s.Store.W().TxNamed(ctx, "icons.put", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO custom_icons (id, name, svg, sha256, monochrome, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET svg = excluded.svg, sha256 = excluded.sha256, monochrome = excluded.monochrome,
			created_by = excluded.created_by, created_at = excluded.created_at`, id, name, clean, hexSum, mono, actor.ID, tx.NowMicros()); err != nil {
			return err
		}
		_, err := s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "icon.uploaded", Target: audit.Target{Type: "icon", ID: name},
			Details: map[string]any{"sha256": hexSum, "bytes": len(clean), "dropped_bytes": len(svg) - len(clean)}})
		return err
	})
	if err != nil {
		return nil, err
	}
	var out Icon
	var at int64
	err = s.Store.R().QueryRowContext(ctx, `SELECT id, created_at FROM custom_icons WHERE name = ?`, name).Scan(&out.ID, &at)
	if err != nil {
		return nil, err
	}
	out = view(out.ID, name, hexSum, mono, len(clean), actor.ID, at)
	return &out, nil
}

// Delete removes an icon. Entities referencing it fall back to their kind's icon.
func (s *Service) Delete(ctx context.Context, actor audit.Actor, name string) error {
	return s.Store.W().TxNamed(ctx, "icons.delete", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `DELETE FROM custom_icons WHERE name = ?`, name)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		_, err = s.Audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "icon.deleted", Target: audit.Target{Type: "icon", ID: name}})
		return err
	})
}

// SVG returns an icon's sanitized bytes and hash.
func (s *Service) SVG(ctx context.Context, name string) ([]byte, string, error) {
	var b []byte
	var sum string
	err := s.Store.R().QueryRowContext(ctx, `SELECT svg, sha256 FROM custom_icons WHERE name = ?`, name).Scan(&b, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return b, sum, err
}

type listOutput struct{ Body []Icon }

type putInput struct {
	Name string `path:"name" maxLength:"128"`
	Body struct {
		SVG string `json:"svg" maxLength:"65536" doc:"SVG source (≤ 64 KiB). Scripts, styles, event handlers, foreignObject and external references are removed."`
	}
}

type iconOutput struct{ Body *Icon }

type namePath struct {
	Name string `path:"name" maxLength:"128"`
}

type noContent struct{}

// Register adds the JSON endpoints.
func (s *Service) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	huma.Register(api, huma.Operation{OperationID: "list-icons", Method: http.MethodGet, Path: "/icons", Tags: []string{"icons"},
		Summary: "Custom icons", Security: sec},
		func(ctx context.Context, _ *struct{}) (*listOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-icons", err)
			}
			list, err := s.List(ctx)
			return &listOutput{Body: list}, d.Fail("list-icons", err)
		})
	huma.Register(api, huma.Operation{OperationID: "put-icon", Method: http.MethodPut, Path: "/icons/{name}", Tags: []string{"icons"},
		Summary: "Upload or replace a custom icon (owner or admin)", Security: sec},
		func(ctx context.Context, in *putInput) (*iconOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("put-icon", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("put-icon", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			ic, err := s.Put(ctx, httpapi.Actor(ctx), in.Name, []byte(in.Body.SVG))
			return &iconOutput{Body: ic}, d.Fail("put-icon", err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-icon", Method: http.MethodDelete, Path: "/icons/{name}", Tags: []string{"icons"},
		Summary: "Delete a custom icon (owner or admin)", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *namePath) (*noContent, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("delete-icon", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("delete-icon", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			return nil, d.Fail("delete-icon", s.Delete(ctx, httpapi.Actor(ctx), in.Name))
		})
}

// RegisterRaw serves GET /api/v1/icons/{name}.svg for <img> tags: sandboxed, never sniffed, cacheable by hash.
func (s *Service) RegisterRaw(mux *http.ServeMux, d *httpapi.Deps) {
	mux.HandleFunc("GET /api/v1/icons/{file}", func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutSuffix(r.PathValue("file"), ".svg")
		if !ok || !nameRE.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if _, err := d.Authenticate(r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, sum, err := s.SVG(r.Context(), name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		etag := `"` + sum[:32] + `"`
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `inline; filename="`+name+`.svg"`)
		// Replacing an icon keeps its URL: browsers revalidate (cheap 304s by ETag) instead of caching blindly.
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(b)
	})
}
