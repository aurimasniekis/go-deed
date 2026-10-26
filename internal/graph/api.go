package graph

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
)

type neighborhoodInput struct {
	Root         []string `query:"root,explode" required:"true" maxItems:"50" doc:"Entity IDs to start from (repeat)."`
	Depth        int      `query:"depth" minimum:"1" maximum:"20" default:"2"`
	Direction    string   `query:"direction" enum:"out,in,both" default:"both"`
	Type         []string `query:"type,explode" doc:"Relationship types to follow."`
	Kind         []string `query:"kind,explode" doc:"Only include nodes of these kinds (roots always included)."`
	Evidence     []string `query:"evidence,explode" enum:"declared,structural,observed,inferred"`
	MaxNodes     int      `query:"max_nodes" minimum:"1" maximum:"20000" default:"500"`
	IncludeStale bool     `query:"include_stale" doc:"Keep observed relationships not seen for 7 days."`
}

type resultOutput struct {
	Body *Result
}

type expandInput struct {
	Key   string `query:"key" required:"true" maxLength:"300"`
	Limit int    `query:"limit" minimum:"1" maximum:"20000" default:"500"`
}

type pathInput struct {
	From     string   `query:"from" required:"true" maxLength:"64"`
	To       string   `query:"to" required:"true" maxLength:"64"`
	MaxDepth int      `query:"max_depth" minimum:"1" maximum:"20" default:"6"`
	Directed bool     `query:"directed"`
	Type     []string `query:"type,explode"`
}

type impactInput struct {
	Entity    string `query:"entity" required:"true" maxLength:"64"`
	Direction string `query:"direction" enum:"downstream,upstream" default:"downstream" doc:"downstream: what may be affected if the entity fails; upstream: what it depends on."`
	Depth     int    `query:"depth" minimum:"1" maximum:"20" default:"4"`
}

type impactOutput struct {
	Body *ImpactResult
}

type overviewInput struct {
	Project []string `query:"project,explode"`
	GroupBy string   `query:"group_by" maxLength:"128" default:"kind" doc:"project, kind, environment, owner, or a kind with a map grouping (location, region, label, group, custom kinds)."`
	Sources bool     `query:"sources" doc:"Also list the asserting sources of each bundled edge (one more aggregate over the catalog; off by default)."`
}

func split(vals []string) []string {
	var out []string
	for _, v := range vals {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (e *Engine) scope(ctx context.Context, d *httpapi.Deps) (Scope, error) {
	if _, err := httpapi.RequireUser(ctx); err != nil {
		return Scope{}, err
	}
	r, err := d.Readable(ctx)
	return Scope{Readable: r}, err
}

// Register adds the graph endpoints.
func (e *Engine) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	huma.Register(api, huma.Operation{OperationID: "graph-neighborhood", Method: http.MethodGet, Path: "/graph/neighborhood", Tags: []string{"graph"},
		Summary: "Bounded neighborhood of entities", Description: "Breadth-first expansion with depth and node caps. High-degree fans " +
			"(many neighbors through one relationship type) collapse into cluster nodes that /graph/expand loads on demand. Entities in projects " +
			"you cannot read appear as redacted stubs and are never expanded.", Security: sec},
		func(ctx context.Context, in *neighborhoodInput) (*resultOutput, error) {
			s, err := e.scope(ctx, d)
			if err != nil {
				return nil, d.Fail("graph-neighborhood", err)
			}
			res, err := e.Neighborhood(ctx, s, Query{Roots: split(in.Root), Depth: in.Depth, Direction: in.Direction, Types: split(in.Type),
				Kinds: split(in.Kind), Evidence: in.Evidence, MaxNodes: in.MaxNodes, IncludeStale: in.IncludeStale})
			return &resultOutput{Body: res}, d.Fail("graph-neighborhood", err)
		})
	huma.Register(api, huma.Operation{OperationID: "graph-expand", Method: http.MethodGet, Path: "/graph/expand", Tags: []string{"graph"},
		Summary: "Load the members of a collapsed cluster", Security: sec},
		func(ctx context.Context, in *expandInput) (*resultOutput, error) {
			s, err := e.scope(ctx, d)
			if err != nil {
				return nil, d.Fail("graph-expand", err)
			}
			res, err := e.Expand(ctx, s, in.Key, in.Limit)
			return &resultOutput{Body: res}, d.Fail("graph-expand", err)
		})
	huma.Register(api, huma.Operation{OperationID: "graph-path", Method: http.MethodGet, Path: "/graph/path", Tags: []string{"graph"},
		Summary: "Shortest relationship path between two entities", Security: sec},
		func(ctx context.Context, in *pathInput) (*resultOutput, error) {
			s, err := e.scope(ctx, d)
			if err != nil {
				return nil, d.Fail("graph-path", err)
			}
			res, err := e.Path(ctx, s, in.From, in.To, in.MaxDepth, in.Directed, split(in.Type))
			return &resultOutput{Body: res}, d.Fail("graph-path", err)
		})
	huma.Register(api, huma.Operation{OperationID: "graph-impact", Method: http.MethodGet, Path: "/graph/impact", Tags: []string{"graph"},
		Summary: "Evidence-based upstream/downstream impact", Description: "Follows the dependency semantics of relationship types. Results are " +
			"potential impact derived from declared and observed relationships, labelled with the weakest evidence along each path.", Security: sec},
		func(ctx context.Context, in *impactInput) (*impactOutput, error) {
			s, err := e.scope(ctx, d)
			if err != nil {
				return nil, d.Fail("graph-impact", err)
			}
			res, err := e.Impact(ctx, s, in.Entity, in.Direction, in.Depth)
			return &impactOutput{Body: res}, d.Fail("graph-impact", err)
		})
	huma.Register(api, huma.Operation{OperationID: "graph-overview", Method: http.MethodGet, Path: "/graph/overview", Tags: []string{"graph"},
		Summary: "Aggregated map: groups with counts, health and bundled relationships", Security: sec},
		func(ctx context.Context, in *overviewInput) (*resultOutput, error) {
			s, err := e.scope(ctx, d)
			if err != nil {
				return nil, d.Fail("graph-overview", err)
			}
			var projects []string
			for _, ref := range split(in.Project) {
				pr, err := d.ResolveProject(ctx, ref, authz.Viewer)
				if err != nil {
					return nil, d.Fail("graph-overview", err)
				}
				projects = append(projects, pr.ID)
			}
			res, err := e.Overview(ctx, s, projects, in.GroupBy, in.Sources)
			return &resultOutput{Body: res}, d.Fail("graph-overview", err)
		})
}
