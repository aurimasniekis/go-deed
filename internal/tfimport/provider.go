package tfimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// ProviderType is the source provider name.
const ProviderType = "terraform_state"

// Config is the provider configuration.
type Config struct {
	Backend string    `json:"backend"`
	S3      *S3Config `json:"s3,omitempty"`
	HTTP    *struct {
		URL string `json:"url"`
	} `json:"http,omitempty"`
	Workspace         string    `json:"workspace,omitempty"`
	InferDependencies bool      `json:"infer_dependencies,omitempty"`
	Mappings          []Mapping `json:"mappings,omitempty"`
	AllowCIDRs        []string  `json:"allow_cidrs,omitempty"`
	InsecureHTTP      bool      `json:"insecure_http,omitempty"`
	MaxBytes          int64     `json:"max_bytes,omitempty"`
}

// S3Config locates a state object.
type S3Config struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Region   string `json:"region,omitempty"`
	UseSSL   *bool  `json:"use_ssl,omitempty"`
	Lookup   string `json:"lookup,omitempty"`
}

const configSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "backend": {"type": "string", "enum": ["push", "s3", "http"], "default": "push", "description": "push: CI or the CLI uploads state; s3: read an S3-compatible backend object; http: read a Terraform HTTP backend."},
    "s3": {"type": "object", "additionalProperties": false, "required": ["endpoint", "bucket", "key"], "properties": {
      "endpoint": {"type": "string", "description": "host[:port] of the S3 API (e.g. s3.eu-central-1.amazonaws.com or minio.lan:9000)"},
      "bucket": {"type": "string"}, "key": {"type": "string", "description": "Object key of the state (e.g. env:/prod/core.tfstate)"},
      "region": {"type": "string"}, "use_ssl": {"type": "boolean", "default": true}, "lookup": {"type": "string", "enum": ["auto", "path", "virtual"]}}},
    "http": {"type": "object", "additionalProperties": false, "required": ["url"], "properties": {"url": {"type": "string", "format": "uri"}}},
    "workspace": {"type": "string", "maxLength": 63, "description": "Label and external-ID prefix, keeps several states apart."},
    "infer_dependencies": {"type": "boolean", "description": "Turn untyped Terraform dependencies into inferred depends_on relationships."},
    "mappings": {"type": "array", "maxItems": 200, "description": "Custom mappings, checked before the built-in catalog.", "items": {"type": "object"}},
    "allow_cidrs": {"type": "array", "items": {"type": "string"}, "description": "Private CIDRs this source may reach (e.g. a LAN MinIO)."},
    "insecure_http": {"type": "boolean", "description": "Allow http:// (development or trusted LAN only)."},
    "max_bytes": {"type": "integer", "minimum": 1024, "maximum": 536870912}
  }
}`

// Provider reads Terraform state read-only.
type Provider struct {
	Catalog  *Catalog
	MaxBytes int64
}

// Describe implements sources.Provider.
func (p *Provider) Describe() sources.Descriptor {
	var kinds []sources.KindSupport
	seen := map[string]bool{}
	for _, m := range p.Catalog.All() {
		if !seen[m.Kind] {
			seen[m.Kind] = true
			kinds = append(kinds, sources.KindSupport{Kind: m.Kind, Level: sources.SupportNative, Note: "Terraform state mapping"})
		}
	}
	return sources.Descriptor{Type: ProviderType, Title: "Terraform state", Description: "Imports Terraform state read-only (state v4 or " +
		"`terraform show -json`) from an S3-compatible backend, a Terraform HTTP backend, or uploads from CI and `deed import terraform`. " +
		"Only allowlisted attributes are read; raw state is never stored.", DefaultLayer: catalog.LayerImported, ConfigSchema: json.RawMessage(configSchema),
		SecretFields: []string{"s3_access_key_id", "s3_secret_access_key", "http_username", "http_password", "http_token"},
		Capabilities: sources.Capabilities{Pull: true, Push: true, Snapshot: true}, Kinds: kinds, RelTypes: []string{"contains", "resolves_to", "runs_on",
			"stored_in", "member_of", "depends_on"}, DefaultSchedule: 900, Docs: "docs/TERRAFORM_IMPORT.md",
		Verified: "redacted state fixtures (v4 and show-json) with planted secrets; S3 reader against local MinIO; push via CLI"}
}

func parseConfig(raw json.RawMessage) (*Config, error) {
	var c Config
	if len(raw) > 0 {
		// Strict like the built-in YAML mappings: a misspelled mapping key is an error, not a silently ignored field.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, apperr.New(apperr.CodeInvalid, "invalid terraform_state config: %s", strings.TrimPrefix(err.Error(), "json: "))
		}
	}
	if c.Backend == "" {
		c.Backend = "push"
	}
	return &c, nil
}

// Validate implements sources.Provider.
func (p *Provider) Validate(raw json.RawMessage, secrets map[string]string) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	switch c.Backend {
	case "s3":
		if c.S3 == nil {
			return apperr.New(apperr.CodeInvalid, "backend s3 needs the s3 block")
		}
		if strings.Contains(c.S3.Endpoint, "/") {
			return apperr.New(apperr.CodeInvalid, "s3.endpoint must be host[:port]")
		}
	case "http":
		if c.HTTP == nil || c.HTTP.URL == "" {
			return apperr.New(apperr.CodeInvalid, "backend http needs http.url")
		}
	}
	if _, err := p.Catalog.With(c.Mappings); err != nil {
		return err
	}
	return nil
}

func (p *Provider) policy(c *Config) (safehttp.Policy, error) {
	pol := safehttp.Policy{AllowCIDRs: c.AllowCIDRs, InsecureHTTP: c.InsecureHTTP, MaxBodyBytes: p.maxBytes(c) + (1 << 20), Timeout: 2 * time.Minute}
	switch c.Backend {
	case "s3":
		scheme := "https"
		if c.S3.UseSSL != nil && !*c.S3.UseSSL {
			scheme = "http"
		}
		pol.Allow = []string{scheme + "://" + c.S3.Endpoint}
		if c.S3.Lookup != "path" {
			pol.Allow = append(pol.Allow, scheme+"://"+c.S3.Bucket+"."+c.S3.Endpoint)
		}
	case "http":
		u, err := url.Parse(c.HTTP.URL)
		if err != nil {
			return pol, apperr.New(apperr.CodeInvalid, "invalid http.url")
		}
		pol.Allow = []string{u.Scheme + "://" + u.Host}
	}
	return pol, nil
}

func (p *Provider) maxBytes(c *Config) int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return 64 << 20
}

// fetch downloads the state (never logged).
func (p *Provider) fetch(ctx context.Context, env sources.Env, c *Config) (io.ReadCloser, error) {
	pol, err := p.policy(c)
	if err != nil {
		return nil, err
	}
	client, err := env.HTTP(pol)
	if err != nil {
		return nil, err
	}
	switch c.Backend {
	case "s3":
		useSSL := c.S3.UseSSL == nil || *c.S3.UseSSL
		lookup := minio.BucketLookupAuto
		switch c.S3.Lookup {
		case "path":
			lookup = minio.BucketLookupPath
		case "virtual":
			lookup = minio.BucketLookupDNS
		}
		mc, err := minio.New(c.S3.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(env.Secrets["s3_access_key_id"], env.Secrets["s3_secret_access_key"], ""),
			Secure: useSSL, Region: c.S3.Region, BucketLookup: lookup, Transport: client.Transport})
		if err != nil {
			return nil, apperr.New(apperr.CodeInvalid, "s3 client configuration is invalid")
		}
		obj, err := mc.GetObject(ctx, c.S3.Bucket, c.S3.Key, minio.GetObjectOptions{})
		if err != nil {
			return nil, fmt.Errorf("s3 get object: %w", err)
		}
		if _, err := obj.Stat(); err != nil {
			_ = obj.Close()
			return nil, s3Error(err)
		}
		return obj, nil
	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HTTP.URL, nil)
		if err != nil {
			return nil, err
		}
		if t := env.Secrets["http_token"]; t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		} else if u := env.Secrets["http_username"]; u != "" {
			req.SetBasicAuth(u, env.Secrets["http_password"])
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, apperr.New(apperr.CodeUnavailable, "http backend answered %d", resp.StatusCode)
		}
		return resp.Body, nil
	}
	return nil, apperr.New(apperr.CodeInvalid, "push sources receive state from uploads; use `deed import terraform` or the API")
}

func s3Error(err error) error {
	er := minio.ToErrorResponse(err)
	switch er.Code {
	case "NoSuchKey":
		return apperr.New(apperr.CodeNotFound, "state object not found in the bucket")
	case "NoSuchBucket":
		return apperr.New(apperr.CodeNotFound, "bucket not found")
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return apperr.New(apperr.CodeForbidden, "access to the state object was denied (check the read-only credentials)")
	}
	return fmt.Errorf("s3: %w", err)
}

// Test implements sources.Provider: it checks access and parses the state header, without importing anything.
func (p *Provider) Test(ctx context.Context, env sources.Env) []sources.Diagnostic {
	c, err := parseConfig(env.Config)
	if err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "config", Message: err.Error()}}
	}
	if c.Backend == "push" {
		return []sources.Diagnostic{{Level: "info", Code: "push", Message: "this source receives uploads; create a source token and run `deed import terraform`"}}
	}
	rc, err := p.fetch(ctx, env, c)
	if err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "fetch", Message: diagMessage(err)}}
	}
	defer rc.Close()
	st, err := Parse(rc, p.maxBytes(c))
	if err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "parse", Message: diagMessage(err)}}
	}
	cat, _ := p.Catalog.With(c.Mappings)
	mapped := 0
	for _, r := range st.Resources {
		if _, ok := cat.Lookup(r.Type); ok {
			mapped++
		}
	}
	return []sources.Diagnostic{{Level: "info", Code: "ok", Message: fmt.Sprintf("read %s (Terraform %s, serial %d): %d managed resources, %d mapped",
		st.Format, st.TerraformVersion, st.Serial, len(st.Resources), mapped)}}
}

func diagMessage(err error) string {
	var ae *apperr.Error
	if errorsAs(err, &ae) {
		return ae.Message
	}
	return "request failed: " + safehttp.Classify(err)
}

// Sync implements sources.Provider.
func (p *Provider) Sync(ctx context.Context, env sources.Env, em sources.Emitter) error {
	c, err := parseConfig(env.Config)
	if err != nil {
		return err
	}
	rc, err := p.fetch(ctx, env, c)
	if err != nil {
		return err
	}
	defer rc.Close()
	st, err := Parse(rc, p.maxBytes(c))
	if err != nil {
		return err
	}
	return p.emit(ctx, env, c, st, em)
}

func (p *Provider) emit(ctx context.Context, env sources.Env, c *Config, st *State, em sources.Emitter) error {
	cat, err := p.Catalog.With(c.Mappings)
	if err != nil {
		return err
	}
	b, d := Extract(ctx, st, cat, Options{Workspace: c.Workspace, InferDependencies: c.InferDependencies, Lookup: env.Lookup})
	for _, e := range b.Entities {
		if err := em.Entity(e); err != nil {
			return err
		}
	}
	for _, r := range b.Relations {
		if err := em.Relation(r); err != nil {
			return err
		}
	}
	for _, diag := range Diags(d) {
		em.Diag(diag)
	}
	return nil
}

// Diags renders extraction diagnostics as source diagnostics (counts and types only).
func Diags(d *Diagnostics) []sources.Diagnostic {
	out := []sources.Diagnostic{{Level: "info", Code: "state", Message: fmt.Sprintf("%s from Terraform %s, serial %d, %d managed resources",
		d.Format, d.TerraformVersion, d.Serial, d.Resources)}}
	for t, n := range d.Unsupported {
		out = append(out, sources.Diagnostic{Level: "info", Code: "unsupported_resource", Message: "no mapping for " + t + " (not imported)", Count: n})
	}
	if d.DroppedSensitive > 0 {
		out = append(out, sources.Diagnostic{Level: "info", Code: "sensitive_dropped", Message: "allowlisted attributes marked sensitive were skipped",
			Count: d.DroppedSensitive})
	}
	if d.DroppedSecrets > 0 {
		out = append(out, sources.Diagnostic{Level: "warning", Code: "secret_like_dropped", Message: "values that look like key material were refused",
			Count: d.DroppedSecrets})
	}
	for _, m := range d.Messages {
		out = append(out, sources.Diagnostic{Level: "warning", Code: "mapping", Message: m})
	}
	return out
}

// PushAPI serves POST /sources/{id}/terraform/state.
type PushAPI struct {
	Provider *Provider
	Ingest   *ingest.Service
	Runner   *sources.Runner
	// Rate is the per-source ingest limiter shared with the other ingest endpoints (nil = unlimited).
	Rate *httpx.Limiter
}

type pushInput struct {
	ID               string `path:"id" maxLength:"64"`
	DryRun           bool   `query:"dry_run" doc:"Preview the import without writing."`
	Partial          bool   `query:"partial" doc:"Upsert only; never tombstone resources missing from this state."`
	AllowMassRemoval bool   `query:"allow_mass_removal"`
	RawBody          []byte `contentType:"application/json"`
}

// PushResult answers an upload.
type PushResult struct {
	Result      *ingest.CommitResult `json:"result"`
	Diagnostics []sources.Diagnostic `json:"diagnostics"`
	Run         *sources.Run         `json:"run,omitempty"`
}

type pushOutput struct {
	Body PushResult
}

// Register adds the push endpoint.
func (a *PushAPI) Register(api huma.API, d *httpapi.Deps) {
	huma.Register(api, huma.Operation{OperationID: "push-terraform-state", Method: http.MethodPost, Path: "/sources/{id}/terraform/state",
		Tags: []string{"ingest"}, Summary: "Upload a Terraform state (or `terraform show -json`) to a terraform_state source",
		Description: "The body is parsed in memory, never stored. Only allowlisted attributes of mapped resource types are imported; the upload " +
			"is a complete snapshot of the source unless partial=true. dry_run previews the result.", Security: httpapi.Secured,
		MaxBodyBytes: 512 << 20, SkipValidateBody: true},
		func(ctx context.Context, in *pushInput) (*pushOutput, error) {
			p := httpapi.PrincipalFrom(ctx)
			src, err := d.Catalog.Source(ctx, in.ID)
			if err != nil {
				return nil, d.Fail("push-terraform-state", err)
			}
			if p.SourceID != "" && p.SourceID != src.ID {
				return nil, d.Fail("push-terraform-state", catalog.ErrSourceNotFound)
			}
			if p.SourceID == "" {
				if err := authz.Require(p, src.ProjectID, authz.Editor); err != nil {
					return nil, d.Fail("push-terraform-state", err)
				}
			}
			if src.Provider != ProviderType {
				return nil, d.Fail("push-terraform-state", apperr.New(apperr.CodeInvalid, "source %s is not a terraform_state source", src.Name))
			}
			if !src.Enabled {
				return nil, d.Fail("push-terraform-state", apperr.New(apperr.CodeForbidden, "source is disabled"))
			}
			if a.Rate != nil && !a.Rate.Allow(src.ID) {
				return nil, d.Fail("push-terraform-state", httpapi.NewProblem(http.StatusTooManyRequests, apperr.CodeRateLimited,
					"ingest rate limit for this source exceeded; retry after a few seconds"))
			}
			c, err := parseConfig(src.Config)
			if err != nil {
				return nil, d.Fail("push-terraform-state", err)
			}
			st, err := Parse(bytes.NewReader(in.RawBody), a.Provider.maxBytes(c))
			in.RawBody = nil
			if err != nil {
				return nil, d.Fail("push-terraform-state", err)
			}
			env, err := a.Runner.Env(src)
			if err != nil {
				return nil, d.Fail("push-terraform-state", err)
			}
			col := &collect{}
			if err := a.Provider.emit(ctx, env, c, st, col); err != nil {
				return nil, d.Fail("push-terraform-state", err)
			}
			actor := p.Actor()
			res, err := a.Ingest.RunSnapshot(ctx, src, col.b, !in.Partial, ingest.CommitOptions{DryRun: in.DryRun, AllowMassRemoval: in.AllowMassRemoval}, actor)
			run := a.Runner.RecordPush(ctx, src, "push", res, col.diags, err)
			if err != nil {
				if apperr.CodeOf(err) == "mass_removal_refused" {
					return nil, httpapi.NewProblem(http.StatusConflict, "mass_removal_refused", err.Error())
				}
				return nil, d.Fail("push-terraform-state", err)
			}
			return &pushOutput{Body: PushResult{Result: res, Diagnostics: col.diags, Run: run}}, nil
		})
}

type collect struct {
	b     ingest.Batch
	diags []sources.Diagnostic
}

func (c *collect) Entity(it catalog.EntityItem) error {
	c.b.Entities = append(c.b.Entities, it)
	return nil
}
func (c *collect) Relation(it catalog.RelationItem) error {
	c.b.Relations = append(c.b.Relations, it)
	return nil
}
func (c *collect) Diag(d sources.Diagnostic) { c.diags = append(c.diags, d) }
func (c *collect) Partial(string)            {}

var _ = audit.System

func errorsAs(err error, target any) bool { return errors.As(err, target) }
