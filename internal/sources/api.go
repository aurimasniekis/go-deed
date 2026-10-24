package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// API registers the sources and ingest endpoints.
type API struct {
	Runner *Runner
	Ingest *ingest.Service
	// Rate limits ingest per source.
	Rate *httpx.Limiter
}

// SourceView is a source with descriptor information and token count (never secrets).
type SourceView struct {
	*catalog.Source
	SecretFieldsSet []string `json:"secret_fields_set" doc:"Secret fields that have a stored value (values are never returned)."`
	ProviderTitle   string   `json:"provider_title"`
	Pull            bool     `json:"pull"`
	Push            bool     `json:"push"`
	Running         bool     `json:"running"`
}

type sourcesOutput struct {
	Body []SourceView
}

type sourceOutput struct {
	ETag string `header:"ETag"`
	Body SourceView
}

// SourcePath is the source in the URL.
type SourcePath struct {
	ID string `path:"id" maxLength:"64"`
}

type listSourcesInput struct {
	Project string `query:"project" maxLength:"64"`
}

// SourceBody creates or updates a source.
type SourceBody struct {
	Project                 string            `json:"project,omitempty" maxLength:"64"`
	Name                    string            `json:"name,omitempty" maxLength:"63"`
	DisplayName             string            `json:"display_name,omitempty" maxLength:"128"`
	Provider                string            `json:"provider,omitempty" maxLength:"64"`
	Layer                   catalog.Layer     `json:"layer,omitempty" enum:"declared,imported,discovered,observation,"`
	Enabled                 *bool             `json:"enabled,omitempty"`
	Config                  json.RawMessage   `json:"config,omitempty"`
	Secrets                 map[string]string `json:"secrets,omitempty" doc:"Write-only credentials (keys from the provider's secret_fields). Omit to keep, {} to clear."`
	Policy                  *catalog.Policy   `json:"policy,omitempty"`
	ScheduleSeconds         *int              `json:"schedule_seconds,omitempty" doc:"0 = push only."`
	ExpectedIntervalSeconds int               `json:"expected_interval_seconds,omitempty"`
}

type createSourceInput struct {
	Body SourceBody
}

type updateSourceInput struct {
	ID string `path:"id" maxLength:"64"`
	httpapi.IfMatch
	Body SourceBody
}

type providersOutput struct {
	Body []Descriptor
}

type diagnosticsOutput struct {
	Body struct {
		OK          bool         `json:"ok"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
}

type runOutput struct {
	Body struct {
		Run    *Run                 `json:"run"`
		Result *ingest.CommitResult `json:"result,omitempty"`
	}
}

type runsOutput struct {
	Body []Run
}

type tokensOutput struct {
	Body []*authn.Token
}

type createTokenInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Name          string `json:"name" maxLength:"100"`
		ExpiresInDays int    `json:"expires_in_days,omitempty" minimum:"1" maximum:"366" default:"90"`
	}
}

type createdTokenOutput struct {
	Body struct {
		*authn.Token
		Secret string `json:"token" doc:"The source token (deed_st_…). Shown once."`
	}
}

type tokenPath struct {
	ID    string `path:"id" maxLength:"64"`
	Token string `path:"token" maxLength:"64"`
}

type itemsInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		ingest.Batch
		Atomic bool `json:"atomic,omitempty" doc:"Reject the whole batch if any item fails."`
	}
}

type itemsOutput struct {
	Body *ingest.ItemsResult
}

type beginInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Generation int64 `json:"generation,omitempty" doc:"Monotonic per source; 0 = last committed + 1."`
		Complete   bool  `json:"complete" doc:"A complete snapshot tombstones facts it no longer contains (per the source's on_absent policy); a partial one never does."`
	}
}

type snapshotOutput struct {
	Body *ingest.Snapshot
}

type snapshotPath struct {
	ID       string `path:"id" maxLength:"64"`
	Snapshot string `path:"snapshot" maxLength:"64"`
}

type batchInput struct {
	ID       string `path:"id" maxLength:"64"`
	Snapshot string `path:"snapshot" maxLength:"64"`
	Seq      int    `path:"seq" minimum:"1" maximum:"1000000"`
	Body     ingest.Batch
}

type batchOutput struct {
	Body *ingest.BatchResult
}

type commitInput struct {
	ID       string `path:"id" maxLength:"64"`
	Snapshot string `path:"snapshot" maxLength:"64"`
	Body     ingest.CommitOptions
}

type commitOutput struct {
	Body *ingest.CommitResult
}

type observationsInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Observations []catalog.EntityItem `json:"observations"`
	}
}

type runsInput struct {
	ID    string `path:"id" maxLength:"64"`
	Limit int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type previewInput struct {
	ID string `path:"id" maxLength:"64"`
}

func (a *API) view(src *catalog.Source) SourceView {
	v := SourceView{Source: src, SecretFieldsSet: []string{}}
	if p, ok := a.Runner.Registry.Get(src.Provider); ok {
		d := p.Describe()
		v.ProviderTitle, v.Pull, v.Push = d.Title, d.Capabilities.Pull, d.Capabilities.Push
		if src.HasSecrets {
			if sec, err := a.Runner.Catalog.SourceSecrets(src); err == nil {
				for k := range sec {
					v.SecretFieldsSet = append(v.SecretFieldsSet, k)
				}
			}
		}
	}
	_, v.Running = a.Runner.running.Load(src.ID)
	return v
}

// source loads a source and checks the caller's project level.
func (a *API) source(ctx context.Context, d *httpapi.Deps, id string, need authz.Level) (*catalog.Source, error) {
	src, err := d.Catalog.Source(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authz.Require(httpapi.PrincipalFrom(ctx), src.ProjectID, need); err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return nil, catalog.ErrSourceNotFound
		}
		return nil, err
	}
	return src, nil
}

// ingestSource authorizes an ingest call: the source's own token, or a project editor for push/manual sources.
func (a *API) ingestSource(ctx context.Context, d *httpapi.Deps, id string) (*catalog.Source, error) {
	p := httpapi.PrincipalFrom(ctx)
	src, err := d.Catalog.Source(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.SourceID != "" {
		if p.SourceID != src.ID {
			return nil, catalog.ErrSourceNotFound
		}
	} else if err := authz.Require(p, src.ProjectID, authz.Editor); err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return nil, catalog.ErrSourceNotFound
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, apperr.New(apperr.CodeForbidden, "source is disabled")
	}
	if a.Rate != nil && !a.Rate.Allow(src.ID) {
		return nil, httpapi.NewProblem(http.StatusTooManyRequests, apperr.CodeRateLimited, "ingest rate limit for this source exceeded; retry after a few seconds")
	}
	return src, nil
}

// Register adds the endpoints.
func (a *API) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	huma.Register(api, huma.Operation{OperationID: "list-providers", Method: http.MethodGet, Path: "/providers", Tags: []string{"sources"},
		Summary: "Source provider types with config schemas, capabilities and supported kinds", Security: sec},
		func(ctx context.Context, _ *struct{}) (*providersOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-providers", err)
			}
			return &providersOutput{Body: a.Runner.Registry.Descriptors()}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-sources", Method: http.MethodGet, Path: "/sources", Tags: []string{"sources"},
		Summary: "Sources of readable projects with freshness", Security: sec},
		func(ctx context.Context, in *listSourcesInput) (*sourcesOutput, error) {
			if _, err := httpapi.RequireUser(ctx); err != nil {
				return nil, d.Fail("list-sources", err)
			}
			readable, err := d.Readable(ctx)
			if err != nil {
				return nil, d.Fail("list-sources", err)
			}
			if in.Project != "" {
				pr, err := d.ResolveProject(ctx, in.Project, authz.Viewer)
				if err != nil {
					return nil, d.Fail("list-sources", err)
				}
				readable = []string{pr.ID}
			}
			srcs, err := d.Catalog.Sources(ctx, readable)
			if err != nil {
				return nil, d.Fail("list-sources", err)
			}
			out := make([]SourceView, 0, len(srcs))
			for _, s := range srcs {
				out = append(out, a.view(s))
			}
			return &sourcesOutput{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-source", Method: http.MethodPost, Path: "/sources", Tags: []string{"sources"},
		Summary: "Create a source (project admin)", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createSourceInput) (*sourceOutput, error) {
			pr, err := d.ResolveProject(ctx, in.Body.Project, authz.Admin)
			if err != nil {
				return nil, d.Fail("create-source", err)
			}
			p, ok := a.Runner.Registry.Get(in.Body.Provider)
			if !ok || in.Body.Provider == "manual" {
				return nil, d.Fail("create-source", apperr.New(apperr.CodeInvalid, "unknown provider %q (see /providers)", in.Body.Provider))
			}
			desc := p.Describe()
			if in.Body.Layer == "" {
				in.Body.Layer = desc.DefaultLayer
			}
			if err := a.Runner.Registry.ValidateConfig(in.Body.Provider, in.Body.Config, in.Body.Secrets); err != nil {
				return nil, d.Fail("create-source", err)
			}
			sched := desc.DefaultSchedule
			if in.Body.ScheduleSeconds != nil {
				sched = *in.Body.ScheduleSeconds
			}
			if !desc.Capabilities.Pull {
				sched = 0
			}
			src, err := d.Catalog.CreateSource(ctx, httpapi.Actor(ctx), catalog.SourceInput{ProjectID: pr.ID, Name: in.Body.Name, DisplayName: in.Body.DisplayName,
				Provider: in.Body.Provider, Layer: in.Body.Layer, Enabled: in.Body.Enabled, Config: in.Body.Config, Policy: in.Body.Policy,
				ScheduleSeconds: sched, ExpectedIntervalSeconds: in.Body.ExpectedIntervalSeconds}, in.Body.Secrets)
			if err != nil {
				return nil, d.Fail("create-source", err)
			}
			return &sourceOutput{ETag: httpapi.ETag(src.ResourceVersion), Body: a.view(src)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-source", Method: http.MethodGet, Path: "/sources/{id}", Tags: []string{"sources"},
		Summary: "Get a source", Security: sec},
		func(ctx context.Context, in *SourcePath) (*sourceOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Viewer)
			if err != nil {
				return nil, d.Fail("get-source", err)
			}
			return &sourceOutput{ETag: httpapi.ETag(src.ResourceVersion), Body: a.view(src)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-source", Method: http.MethodPatch, Path: "/sources/{id}", Tags: []string{"sources"},
		Summary: "Update a source's config, credentials, policy or schedule (project admin)", Security: sec},
		func(ctx context.Context, in *updateSourceInput) (*sourceOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Admin)
			if err != nil {
				return nil, d.Fail("update-source", err)
			}
			if in.Body.Secrets != nil {
				if err := d.RequireRecent(ctx); err != nil {
					return nil, err
				}
			}
			cfg := src.Config
			if len(in.Body.Config) > 0 {
				cfg = in.Body.Config
			}
			if !src.System {
				if err := a.Runner.Registry.ValidateConfig(src.Provider, cfg, in.Body.Secrets); err != nil {
					return nil, d.Fail("update-source", err)
				}
			}
			v, err := in.Version()
			if err != nil {
				return nil, err
			}
			sched := src.ScheduleSeconds
			if in.Body.ScheduleSeconds != nil {
				sched = *in.Body.ScheduleSeconds
			}
			src, err = d.Catalog.UpdateSource(ctx, httpapi.Actor(ctx), in.ID, catalog.SourceInput{DisplayName: in.Body.DisplayName, Enabled: in.Body.Enabled,
				Config: in.Body.Config, Policy: in.Body.Policy, ScheduleSeconds: sched, ExpectedIntervalSeconds: in.Body.ExpectedIntervalSeconds}, in.Body.Secrets, v)
			if err != nil {
				return nil, d.Fail("update-source", err)
			}
			return &sourceOutput{ETag: httpapi.ETag(src.ResourceVersion), Body: a.view(src)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-source", Method: http.MethodDelete, Path: "/sources/{id}", Tags: []string{"sources"},
		Summary: "Delete a source and every fact it owns (project admin, recent sign-in)", Description: "Entities keep the facets of other " +
			"sources; entities left without facets are tombstoned. The source's tokens are revoked.", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *SourcePath) (*struct{}, error) {
			if _, err := a.source(ctx, d, in.ID, authz.Admin); err != nil {
				return nil, d.Fail("delete-source", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			return nil, d.Fail("delete-source", d.Catalog.DeleteSource(ctx, httpapi.Actor(ctx), in.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "test-source", Method: http.MethodPost, Path: "/sources/{id}/test", Tags: []string{"sources"},
		Summary: "Safe connectivity diagnostics (writes nothing)", Security: sec},
		func(ctx context.Context, in *SourcePath) (*diagnosticsOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Admin)
			if err != nil {
				return nil, d.Fail("test-source", err)
			}
			diags, err := a.Runner.Test(ctx, src)
			if err != nil {
				return nil, d.Fail("test-source", err)
			}
			out := &diagnosticsOutput{}
			out.Body.Diagnostics, out.Body.OK = diags, true
			for _, x := range diags {
				if x.Level == "error" {
					out.Body.OK = false
				}
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "sync-source", Method: http.MethodPost, Path: "/sources/{id}/sync", Tags: []string{"sources"},
		Summary: "Run a pull source now", Security: sec},
		func(ctx context.Context, in *SourcePath) (*runOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Editor)
			if err != nil {
				return nil, d.Fail("sync-source", err)
			}
			run, res, err := a.Runner.Sync(ctx, src, "manual", false, httpapi.Actor(ctx))
			out := &runOutput{}
			out.Body.Run, out.Body.Result = run, res
			if err != nil && run == nil {
				return nil, d.Fail("sync-source", err)
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "preview-source", Method: http.MethodPost, Path: "/sources/{id}/preview", Tags: []string{"sources"},
		Summary: "Dry-run a pull source: what a sync would create, update and remove", Security: sec},
		func(ctx context.Context, in *previewInput) (*runOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Editor)
			if err != nil {
				return nil, d.Fail("preview-source", err)
			}
			run, res, err := a.Runner.Sync(ctx, src, "preview", true, httpapi.Actor(ctx))
			out := &runOutput{}
			out.Body.Run, out.Body.Result = run, res
			if err != nil && run == nil {
				return nil, d.Fail("preview-source", err)
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-source-runs", Method: http.MethodGet, Path: "/sources/{id}/runs", Tags: []string{"sources"},
		Summary: "Sync history with diagnostics and mapping errors", Security: sec},
		func(ctx context.Context, in *runsInput) (*runsOutput, error) {
			if _, err := a.source(ctx, d, in.ID, authz.Viewer); err != nil {
				return nil, d.Fail("list-source-runs", err)
			}
			runs, err := a.Runner.Runs(ctx, in.ID, in.Limit)
			return &runsOutput{Body: runs}, d.Fail("list-source-runs", err)
		})
	huma.Register(api, huma.Operation{OperationID: "list-source-tokens", Method: http.MethodGet, Path: "/sources/{id}/tokens", Tags: []string{"sources"},
		Summary: "Source tokens (never their secrets)", Security: sec},
		func(ctx context.Context, in *SourcePath) (*tokensOutput, error) {
			if _, err := a.source(ctx, d, in.ID, authz.Admin); err != nil {
				return nil, d.Fail("list-source-tokens", err)
			}
			t, err := d.Authn.ListTokens(ctx, "", in.ID)
			return &tokensOutput{Body: t}, d.Fail("list-source-tokens", err)
		})
	huma.Register(api, huma.Operation{OperationID: "create-source-token", Method: http.MethodPost, Path: "/sources/{id}/tokens", Tags: []string{"sources"},
		Summary: "Issue a source token (shown once; project admin, recent sign-in)", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *createTokenInput) (*createdTokenOutput, error) {
			src, err := a.source(ctx, d, in.ID, authz.Admin)
			if err != nil {
				return nil, d.Fail("create-source-token", err)
			}
			if err := d.RequireRecent(ctx); err != nil {
				return nil, err
			}
			out := &createdTokenOutput{}
			err = d.Store.W().TxNamed(ctx, "sources.token_create", func(tx *store.Tx) error {
				t, raw, err := d.Authn.CreateSourceTokenTx(ctx, tx, httpapi.Actor(ctx), src.ID, in.Body.Name, time.Duration(in.Body.ExpiresInDays)*24*time.Hour)
				out.Body.Token, out.Body.Secret = t, raw
				return err
			})
			return out, d.Fail("create-source-token", err)
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-source-token", Method: http.MethodDelete, Path: "/sources/{id}/tokens/{token}", Tags: []string{"sources"},
		Summary: "Revoke a source token", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *tokenPath) (*struct{}, error) {
			if _, err := a.source(ctx, d, in.ID, authz.Admin); err != nil {
				return nil, d.Fail("revoke-source-token", err)
			}
			t, err := d.Authn.GetToken(ctx, in.Token)
			if err != nil || t.SourceID != in.ID {
				return nil, d.Fail("revoke-source-token", apperr.New(apperr.CodeNotFound, "token not found"))
			}
			return nil, d.Fail("revoke-source-token", d.Authn.RevokeToken(ctx, httpapi.Actor(ctx), in.Token))
		})

	// Source protocol (ADR-006 B).
	huma.Register(api, huma.Operation{OperationID: "ingest-items", Method: http.MethodPost, Path: "/sources/{id}/items", Tags: []string{"ingest"},
		Summary: "Incremental upserts and deletes", Description: "Each item carries an optional per-source monotonic revision; items not newer " +
			"than the stored revision (or the deletion's) are reported as stale and ignored, so delayed messages never resurrect deleted facts. " +
			"Results are per item; atomic=true rejects the batch on any error.", Security: sec},
		func(ctx context.Context, in *itemsInput) (*itemsOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("ingest-items", err)
			}
			res, err := a.Ingest.Items(ctx, src, in.Body.Batch, in.Body.Atomic, httpapi.Actor(ctx))
			return &itemsOutput{Body: res}, d.Fail("ingest-items", err)
		})
	huma.Register(api, huma.Operation{OperationID: "begin-snapshot", Method: http.MethodPost, Path: "/sources/{id}/snapshots", Tags: []string{"ingest"},
		Summary: "Begin a snapshot", Security: sec, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *beginInput) (*snapshotOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("begin-snapshot", err)
			}
			sn, err := a.Ingest.Begin(ctx, src, in.Body.Generation, in.Body.Complete, httpapi.Actor(ctx))
			return &snapshotOutput{Body: sn}, d.Fail("begin-snapshot", err)
		})
	huma.Register(api, huma.Operation{OperationID: "get-snapshot", Method: http.MethodGet, Path: "/sources/{id}/snapshots/{snapshot}", Tags: []string{"ingest"},
		Summary: "Snapshot state", Security: sec},
		func(ctx context.Context, in *snapshotPath) (*snapshotOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("get-snapshot", err)
			}
			sn, err := a.Ingest.GetSnapshot(ctx, src, in.Snapshot)
			return &snapshotOutput{Body: sn}, d.Fail("get-snapshot", err)
		})
	huma.Register(api, huma.Operation{OperationID: "put-snapshot-batch", Method: http.MethodPut, Path: "/sources/{id}/snapshots/{snapshot}/batches/{seq}",
		Tags: []string{"ingest"}, Summary: "Stage a batch (idempotent per seq)", Security: sec},
		func(ctx context.Context, in *batchInput) (*batchOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("put-snapshot-batch", err)
			}
			res, err := a.Ingest.PutBatch(ctx, src, in.Snapshot, in.Seq, in.Body)
			return &batchOutput{Body: res}, d.Fail("put-snapshot-batch", err)
		})
	huma.Register(api, huma.Operation{OperationID: "commit-snapshot", Method: http.MethodPost, Path: "/sources/{id}/snapshots/{snapshot}/commit",
		Tags: []string{"ingest"}, Summary: "Commit (or dry-run) a snapshot atomically", Description: "Applies every staged item in one transaction. " +
			"Complete snapshots then handle absent facts per the source's on_absent policy behind the mass-removal guard " +
			"(409 mass_removal_refused unless allow_mass_removal). A refused or failed commit changes nothing.", Security: sec},
		func(ctx context.Context, in *commitInput) (*commitOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("commit-snapshot", err)
			}
			res, err := a.Ingest.Commit(ctx, src, in.Snapshot, in.Body, httpapi.Actor(ctx))
			if apperr.CodeOf(err) == "mass_removal_refused" || apperr.CodeOf(err) == "stale_generation" {
				return nil, httpapi.NewProblem(http.StatusConflict, apperr.CodeOf(err), err.Error())
			}
			return &commitOutput{Body: res}, d.Fail("commit-snapshot", err)
		})
	huma.Register(api, huma.Operation{OperationID: "abort-snapshot", Method: http.MethodPost, Path: "/sources/{id}/snapshots/{snapshot}/abort",
		Tags: []string{"ingest"}, Summary: "Abort a snapshot; the previous generation stays intact", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *snapshotPath) (*struct{}, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("abort-snapshot", err)
			}
			return nil, d.Fail("abort-snapshot", a.Ingest.Abort(ctx, src, in.Snapshot, httpapi.Actor(ctx)))
		})
	huma.Register(api, huma.Operation{OperationID: "source-heartbeat", Method: http.MethodPost, Path: "/sources/{id}/heartbeat", Tags: []string{"ingest"},
		Summary: "Report liveness (freshness without data)", Security: sec, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *SourcePath) (*struct{}, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("source-heartbeat", err)
			}
			return nil, d.Fail("source-heartbeat", d.Catalog.Heartbeat(ctx, src.ID))
		})
	huma.Register(api, huma.Operation{OperationID: "ingest-observations", Method: http.MethodPost, Path: "/sources/{id}/observations", Tags: []string{"ingest"},
		Summary: "Time-bounded observations and health (observation-layer sources)", Description: "Each item references an existing entity and " +
			"sets observed values and/or health with an expires_at; observations never create entities or touch spec.", Security: sec},
		func(ctx context.Context, in *observationsInput) (*itemsOutput, error) {
			src, err := a.ingestSource(ctx, d, in.ID)
			if err != nil {
				return nil, d.Fail("ingest-observations", err)
			}
			res, err := a.Ingest.Observations(ctx, src, in.Body.Observations, httpapi.Actor(ctx))
			return &itemsOutput{Body: res}, d.Fail("ingest-observations", err)
		})
}

// RetryAfter is the advice sent with 429 answers.
const RetryAfter = 5

var _ = strconv.Itoa
