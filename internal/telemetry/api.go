package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
)

// API registers the integrations, bindings, entity telemetry and edge-rate endpoints plus the telemetry stream.
type API struct {
	Service *Service
	// MaxStreams bounds concurrent telemetry streams (default 100).
	MaxStreams int
	streams    atomic.Int64
}

type adaptersOutput struct{ Body []AdapterInfo }

type integrationsOutput struct{ Body []*Integration }

type integrationOutput struct {
	ETag string `header:"ETag"`
	Body *Integration
}

// IntegrationPath is an integration in the URL.
type IntegrationPath struct {
	ID string `path:"id" maxLength:"64" doc:"Integration ID or name."`
}

type createIntegrationInput struct{ Body IntegrationInput }

type updateIntegrationInput struct {
	IntegrationPath
	httpapi.IfMatch
	Body IntegrationInput
}

type testOutput struct {
	Body struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
}

type bindingsOutput struct{ Body []*Binding }

type bindingOutput struct {
	ETag string `header:"ETag"`
	Body *Binding
}

// BindingPath is a binding in the URL.
type BindingPath struct {
	ID string `path:"id" maxLength:"64" doc:"Binding ID or name."`
}

type createBindingInput struct{ Body BindingInput }

type updateBindingInput struct {
	BindingPath
	httpapi.IfMatch
	Body BindingInput
}

type previewBindingInput struct {
	BindingPath
	Body struct {
		Entity string `json:"entity" maxLength:"64" doc:"Entity ID to bind parameters from (edge-rate bindings: the relation ID)."`
		Window string `json:"window,omitempty" enum:"15m,1h,6h,24h,7d,"`
	}
}

type resultOutput struct{ Body *TelemetryResult }

type entityTelemetryInput struct {
	ID      string `path:"id" maxLength:"64"`
	Window  string `query:"window" enum:"15m,1h,6h,24h,7d," doc:"Default 1h."`
	Binding string `query:"binding" maxLength:"64" doc:"Run only this binding."`
}

type entityTelemetryOutput struct {
	Body struct {
		EntityID string             `json:"entity_id"`
		Window   string             `json:"window"`
		Results  []*TelemetryResult `json:"results"`
	}
}

type edgeRatesInput struct {
	Body struct {
		RelationIDs []string `json:"relation_ids" maxItems:"200"`
		Window      string   `json:"window,omitempty" enum:"15m,1h,6h,24h,7d," doc:"Default 15m."`
	}
}

type edgeRatesOutput struct {
	Body struct {
		Window string     `json:"window"`
		Rates  []EdgeRate `json:"rates"`
	}
}

type noContent struct{}

func requireAdmin(ctx context.Context) (*authn.Principal, error) {
	p, err := httpapi.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	if !authz.CanAdminister(p) {
		return nil, apperr.New(apperr.CodeForbidden, "installation owner or admin required")
	}
	return p, nil
}

func canReadConfig(p *authn.Principal) bool {
	return authz.IsInstallation(p, authn.RoleOwner, authn.RoleAdmin, authn.RoleAuditor)
}

// readableEntity loads an entity the caller may view (unknown and unreadable look the same).
func (a *API) readableEntity(ctx context.Context, id string) (*catalog.Entity, error) {
	e, err := a.Service.Catalog.GetEntity(ctx, id)
	if err != nil || !authz.Can(httpapi.PrincipalFrom(ctx), e.Metadata.ProjectID, authz.Viewer) {
		return nil, apperr.New(apperr.CodeNotFound, "entity not found")
	}
	return e, nil
}

// readableRelation loads a relation whose both endpoints the caller may view.
func (a *API) readableRelation(ctx context.Context, id string) (*catalog.Relation, bool) {
	r, err := a.Service.Catalog.GetRelation(ctx, id)
	if err != nil {
		return nil, false
	}
	p := httpapi.PrincipalFrom(ctx)
	if !authz.Can(p, r.ProjectID, authz.Viewer) || (r.To.ProjectID != "" && !authz.Can(p, r.To.ProjectID, authz.Viewer)) ||
		(r.From.ProjectID != "" && !authz.Can(p, r.From.ProjectID, authz.Viewer)) {
		return nil, false
	}
	return r, true
}

// Register adds the endpoints.
func (a *API) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	st := a.Service.Store
	huma.Register(api, huma.Operation{OperationID: "list-telemetry-adapters", Method: http.MethodGet, Path: "/telemetry/adapters", Tags: []string{"telemetry"},
		Summary: "Telemetry adapter types with config schemas and query languages", Security: sec},
		func(ctx context.Context, _ *struct{}) (*adaptersOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-telemetry-adapters", err)
			}
			return &adaptersOutput{Body: a.Service.Registry.Infos()}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-integrations", Method: http.MethodGet, Path: "/integrations", Tags: []string{"telemetry"},
		Summary: "Telemetry integrations (owner, admin, auditor; credentials are never returned)", Security: sec},
		func(ctx context.Context, _ *struct{}) (*integrationsOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-integrations", err)
			}
			if !canReadConfig(p) {
				return nil, d.Fail("list-integrations", apperr.New(apperr.CodeForbidden, "installation role required"))
			}
			list, err := st.Integrations(ctx)
			return &integrationsOutput{Body: list}, d.Fail("list-integrations", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-integration", Method: http.MethodGet, Path: "/integrations/{id}", Tags: []string{"telemetry"},
		Summary: "Get an integration", Security: sec},
		func(ctx context.Context, in *IntegrationPath) (*integrationOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("get-integration", err)
			}
			if !canReadConfig(p) {
				return nil, d.Fail("get-integration", ErrIntegrationNotFound)
			}
			it, err := st.Integration(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("get-integration", err)
			}
			return &integrationOutput{ETag: httpapi.ETag(it.ResourceVersion), Body: it}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-integration", Method: http.MethodPost, Path: "/integrations", Tags: []string{"telemetry"},
		Summary: "Create an integration (owner or admin)", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createIntegrationInput) (*integrationOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("create-integration", err)
			}
			it, err := st.CreateIntegration(ctx, httpapi.Actor(ctx), in.Body)
			if err != nil {
				return nil, d.Fail("create-integration", err)
			}
			return &integrationOutput{ETag: httpapi.ETag(it.ResourceVersion), Body: it}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-integration", Method: http.MethodPatch, Path: "/integrations/{id}", Tags: []string{"telemetry"},
		Summary: "Update an integration (owner or admin)", Security: sec},
		func(ctx context.Context, in *updateIntegrationInput) (*integrationOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("update-integration", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, d.Fail("update-integration", err)
			}
			it, err := st.UpdateIntegration(ctx, httpapi.Actor(ctx), in.ID, v, in.Body)
			if err != nil {
				return nil, d.Fail("update-integration", err)
			}
			return &integrationOutput{ETag: httpapi.ETag(it.ResourceVersion), Body: it}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-integration", Method: http.MethodDelete, Path: "/integrations/{id}", Tags: []string{"telemetry"},
		Summary: "Delete an integration and its bindings (owner or admin)", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *IntegrationPath) (*noContent, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("delete-integration", err)
			}
			return nil, d.Fail("delete-integration", st.DeleteIntegration(ctx, httpapi.Actor(ctx), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "test-integration", Method: http.MethodPost, Path: "/integrations/{id}/test", Tags: []string{"telemetry"},
		Summary: "Check connectivity of an integration (owner or admin)", Security: sec},
		func(ctx context.Context, in *IntegrationPath) (*testOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("test-integration", err)
			}
			it, err := st.Integration(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("test-integration", err)
			}
			out := &testOutput{}
			msg, err := a.Service.Test(ctx, it)
			out.Body.OK, out.Body.Message = err == nil, msg
			if err != nil {
				out.Body.Message = safeMessage(err)
			}
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "list-bindings", Method: http.MethodGet, Path: "/bindings", Tags: []string{"telemetry"},
		Summary: "Telemetry bindings (query text is shown to installation roles only)", Security: sec},
		func(ctx context.Context, _ *struct{}) (*bindingsOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-bindings", err)
			}
			list, err := st.Bindings(ctx)
			if err != nil {
				return nil, d.Fail("list-bindings", err)
			}
			if !canReadConfig(p) {
				for _, b := range list {
					b.Query = ""
				}
			}
			return &bindingsOutput{Body: list}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-binding", Method: http.MethodPost, Path: "/bindings", Tags: []string{"telemetry"},
		Summary: "Create a named binding (owner or admin)", Description: "Users can run bindings against entities they can read; only admins author queries.",
		Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createBindingInput) (*bindingOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("create-binding", err)
			}
			b, err := st.CreateBinding(ctx, httpapi.Actor(ctx), in.Body)
			if err != nil {
				return nil, d.Fail("create-binding", err)
			}
			return &bindingOutput{ETag: httpapi.ETag(b.ResourceVersion), Body: b}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-binding", Method: http.MethodPatch, Path: "/bindings/{id}", Tags: []string{"telemetry"},
		Summary: "Update a binding (owner or admin)", Security: sec},
		func(ctx context.Context, in *updateBindingInput) (*bindingOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("update-binding", err)
			}
			v, err := in.Version()
			if err != nil {
				return nil, d.Fail("update-binding", err)
			}
			b, err := st.UpdateBinding(ctx, httpapi.Actor(ctx), in.ID, v, in.Body)
			if err != nil {
				return nil, d.Fail("update-binding", err)
			}
			return &bindingOutput{ETag: httpapi.ETag(b.ResourceVersion), Body: b}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-binding", Method: http.MethodDelete, Path: "/bindings/{id}", Tags: []string{"telemetry"},
		Summary: "Delete a binding (owner or admin)", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *BindingPath) (*noContent, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("delete-binding", err)
			}
			return nil, d.Fail("delete-binding", st.DeleteBinding(ctx, httpapi.Actor(ctx), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "preview-binding", Method: http.MethodPost, Path: "/bindings/{id}/preview", Tags: []string{"telemetry"},
		Summary: "Run a binding against one entity or relation (owner or admin)", Security: sec},
		func(ctx context.Context, in *previewBindingInput) (*resultOutput, error) {
			if _, err := requireAdmin(ctx); err != nil {
				return nil, d.Fail("preview-binding", err)
			}
			b, err := st.Binding(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("preview-binding", err)
			}
			subj := Subject{}
			if b.Role == "edge_rate" {
				r, ok := a.readableRelation(ctx, in.Body.Entity)
				if !ok {
					return nil, d.Fail("preview-binding", apperr.New(apperr.CodeNotFound, "relation not found"))
				}
				if subj.From, err = a.readableEntity(ctx, r.From.ID); err != nil {
					return nil, d.Fail("preview-binding", err)
				}
				if subj.To, err = a.readableEntity(ctx, r.To.ID); err != nil {
					return nil, d.Fail("preview-binding", err)
				}
			} else if subj.Entity, err = a.readableEntity(ctx, in.Body.Entity); err != nil {
				return nil, d.Fail("preview-binding", err)
			}
			return &resultOutput{Body: a.Service.Run(ctx, b, subj, in.Body.Window)}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-entity-telemetry", Method: http.MethodGet, Path: "/entities/{id}/telemetry", Tags: []string{"telemetry"},
		Summary: "Run the entity's telemetry bindings", Description: "Each result is labelled measured, inferred, stale or unavailable with its " +
			"measurement time. Results are cached per binding freshness; upstream failures serve the last good value as stale.", Security: sec},
		func(ctx context.Context, in *entityTelemetryInput) (*entityTelemetryOutput, error) {
			e, err := a.readableEntity(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("get-entity-telemetry", err)
			}
			window := in.Window
			if window == "" {
				window = "1h"
			}
			res, err := a.Service.ForEntity(ctx, e, window, in.Binding)
			if err != nil {
				return nil, d.Fail("get-entity-telemetry", err)
			}
			out := &entityTelemetryOutput{}
			out.Body.EntityID, out.Body.Window, out.Body.Results = e.Metadata.ID, window, res
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-edge-rates", Method: http.MethodPost, Path: "/telemetry/edge-rates", Tags: []string{"telemetry"},
		Summary: "Measured activity of relationships for traffic overlays", Description: "Relations whose endpoints you cannot read are omitted. " +
			"Rates come from the Pulse tail (publish edges) or edge_rate bindings; anything else is unavailable.", Security: sec},
		func(ctx context.Context, in *edgeRatesInput) (*edgeRatesOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("get-edge-rates", err)
			}
			rels := make([]*catalog.Relation, 0, len(in.Body.RelationIDs))
			for _, id := range in.Body.RelationIDs {
				if r, ok := a.readableRelation(ctx, id); ok {
					rels = append(rels, r)
				}
			}
			window := in.Body.Window
			if window == "" {
				window = "15m"
			}
			rates, err := a.Service.EdgeRates(ctx, rels, window)
			if err != nil {
				return nil, d.Fail("get-edge-rates", err)
			}
			out := &edgeRatesOutput{}
			out.Body.Window, out.Body.Rates = window, rates
			return out, nil
		})
}

// RegisterStream mounts GET /api/v1/stream/telemetry: periodic telemetry and edge-rate snapshots for one entity and/or
// a set of relations, separate from the topology stream so metric ticks never trigger graph re-layout.
func (a *API) RegisterStream(mux *http.ServeMux, d *httpapi.Deps) {
	mux.HandleFunc("GET /api/v1/stream/telemetry", func(w http.ResponseWriter, r *http.Request) { a.stream(w, r, d) })
}

func writeProblem(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": msg, "code": code})
}

func (a *API) stream(w http.ResponseWriter, r *http.Request, d *httpapi.Deps) {
	p, err := d.Authenticate(r)
	if err != nil || p.SourceID != "" {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	max := a.MaxStreams
	if max <= 0 {
		max = 100
	}
	if a.streams.Load() >= int64(max) {
		w.Header().Set("Retry-After", "10")
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "too many telemetry streams")
		return
	}
	q := r.URL.Query()
	interval := 30 * time.Second
	if v := q.Get("interval"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 15 || n > 300 {
			writeProblem(w, http.StatusBadRequest, "validation_failed", "interval must be 15..300 seconds")
			return
		}
		interval = time.Duration(n) * time.Second
	}
	window := q.Get("window")
	if _, ok := Windows[window]; !ok && window != "" {
		writeProblem(w, http.StatusBadRequest, "validation_failed", "window must be 15m, 1h, 6h, 24h or 7d")
		return
	}
	relIDs := q["relation"]
	if len(relIDs) > MaxEdgeRates {
		writeProblem(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("at most %d relations", MaxEdgeRates))
		return
	}
	ctx := httpapi.WithPrincipal(r.Context(), p)
	entityID := q.Get("entity")
	if entityID == "" && len(relIDs) == 0 {
		writeProblem(w, http.StatusBadRequest, "validation_failed", "entity or relation required")
		return
	}
	if entityID != "" {
		if _, err := a.readableEntity(ctx, entityID); err != nil {
			writeProblem(w, http.StatusNotFound, "not_found", "entity not found")
			return
		}
	}
	a.streams.Add(1)
	defer a.streams.Add(-1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(event string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		return rc.Flush()
	}
	tick := func() error { //nolint:contextcheck // every tick uses the stream request's own context
		// Authorization is re-checked every tick: revoked sessions, tokens and project access end the stream.
		cur, err := d.Revalidate(r) //nolint:contextcheck // revalidates with the stream request's own context
		if err != nil {
			_ = send("closed", map[string]any{"reason": "credentials revoked or expired"})
			return err
		}
		ctx := httpapi.WithPrincipal(r.Context(), cur)
		if entityID != "" {
			e, err := a.readableEntity(ctx, entityID)
			if err != nil {
				_ = send("closed", map[string]any{"reason": "entity no longer readable"})
				return err
			}
			w := window
			if w == "" {
				w = "1h"
			}
			res, err := a.Service.ForEntity(ctx, e, w, "")
			if err != nil {
				return err
			}
			if err := send("telemetry", map[string]any{"entity_id": e.Metadata.ID, "window": w, "results": res, "at": a.Service.Clock.Now()}); err != nil {
				return err
			}
		}
		if len(relIDs) > 0 {
			rels := make([]*catalog.Relation, 0, len(relIDs))
			for _, id := range relIDs {
				if rel, ok := a.readableRelation(ctx, id); ok {
					rels = append(rels, rel)
				}
			}
			w := window
			if w == "" {
				w = "15m"
			}
			rates, err := a.Service.EdgeRates(ctx, rels, w)
			if err != nil {
				return err
			}
			if err := send("edge_rates", map[string]any{"window": w, "rates": rates, "at": a.Service.Clock.Now()}); err != nil {
				return err
			}
		}
		return nil
	}
	if tick() != nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Keep-alive comments keep idle streams open through proxies with read timeouts (Cloudflare: 100 s).
	hb := time.NewTicker(20 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-t.C:
			if tick() != nil {
				return
			}
		}
	}
}
