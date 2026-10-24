package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// PorchType is the provider name of the Porch connector.
const PorchType = "porch"

// PorchConfig configures the Porch connector.
type PorchConfig struct {
	URL             string   `json:"url"`
	Tenants         []string `json:"tenants,omitempty"`
	InstanceName    string   `json:"instance_name,omitempty"`
	ServiceAccounts *bool    `json:"service_accounts,omitempty"`
	MatchHosts      *bool    `json:"match_hosts,omitempty"`
	IncludeDisabled bool     `json:"include_disabled,omitempty"`
	AllowCIDRs      []string `json:"allow_cidrs,omitempty"`
	InsecureHTTP    bool     `json:"insecure_http,omitempty"`
}

const porchSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {
    "url": {"type": "string", "format": "uri", "description": "Porch base URL (the API lives under /api/v1)."},
    "tenants": {"type": "array", "maxItems": 100, "items": {"type": "string", "pattern": "^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$"}, "description": "Tenant slugs to import (empty = every tenant the token can read)."},
    "instance_name": {"type": "string", "pattern": "^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$", "default": "porch", "description": "Catalog name of the Porch identity-provider service entity."},
    "service_accounts": {"type": "boolean", "default": true},
    "match_hosts": {"type": "boolean", "default": true, "description": "Link catalog DNS records/domains whose FQDN equals an application's protected host, redirect or launch host (inferred)."},
    "include_disabled": {"type": "boolean"},
    "allow_cidrs": {"type": "array", "items": {"type": "string"}, "description": "Private CIDRs the Porch URL resolves to."},
    "insecure_http": {"type": "boolean"}
  }
}`

// Porch is the porch provider: applications (OIDC clients and forward-auth apps) and service accounts per tenant,
// read with a read-only porch_at_ automation token.
type Porch struct{}

// Describe implements sources.Provider.
func (Porch) Describe() sources.Descriptor {
	return sources.Descriptor{Type: PorchType, Title: "Porch", Description: "Reads applications and service accounts of Porch tenants with " +
		"a read-only porch_at_ token. Applications authenticate_with the Porch instance; hosts protected by forward-auth apps and hosts in " +
		"OIDC redirect URIs are linked to matching DNS records or domains (inferred, with evidence). Client secrets are never requested.",
		DefaultLayer: catalog.LayerImported, ConfigSchema: json.RawMessage(porchSchema), SecretFields: []string{"token"},
		Capabilities: sources.Capabilities{Pull: true, Snapshot: true},
		Kinds: []sources.KindSupport{{Kind: "service", Level: sources.SupportNative, Note: "the Porch instance"}, {Kind: "porch_app", Level: sources.SupportNative},
			{Kind: "porch_service_account", Level: sources.SupportNative}},
		RelTypes: []string{"authenticates_with", "protected_by"}, DefaultSchedule: 900, Docs: "docs/CONNECTORS.md#porch",
		Verified: "contract tests against fixtures shaped by Porch api/openapi.json; real Porch validation pending"}
}

func parsePorch(raw json.RawMessage) (*PorchConfig, error) {
	c, err := decode[PorchConfig](raw, "porch")
	if err != nil {
		return nil, err
	}
	t := true
	if c.InstanceName == "" {
		c.InstanceName = "porch"
	}
	if c.ServiceAccounts == nil {
		c.ServiceAccounts = &t
	}
	if c.MatchHosts == nil {
		c.MatchHosts = &t
	}
	return c, nil
}

// Validate implements sources.Provider.
func (Porch) Validate(raw json.RawMessage, _ map[string]string) error {
	c, err := parsePorch(raw)
	if err != nil {
		return err
	}
	return validateBaseURL("url", c.URL, c.InsecureHTTP)
}

func porchClient(env sources.Env, c *PorchConfig) (*apiClient, error) {
	u, _ := url.JoinPath(c.URL, "/api/v1")
	return newClient(env, "Porch", u, c.AllowCIDRs, c.InsecureHTTP, map[string]string{"Authorization": "Bearer " + env.Secrets["token"]})
}

// Porch API DTOs (subset of api/openapi.json).
type porchTenant struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type porchApp struct {
	ID          string   `json:"id"`
	TenantID    string   `json:"tenant_id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	LaunchURL   string   `json:"launch_url"`
	Enabled     bool     `json:"enabled"`
	AccessMode  string   `json:"access_mode"`
	Hosts       []string `json:"hosts"`
	Group       string   `json:"group"`
	ManagedBy   string   `json:"managed_by"`
	Client      *struct {
		ClientID        string   `json:"client_id"`
		ClientType      string   `json:"client_type"`
		ApplicationType string   `json:"application_type"`
		RedirectURIs    []string `json:"redirect_uris"`
		GrantTypes      []string `json:"grant_types"`
		Audiences       []string `json:"audiences"`
		RequirePKCE     bool     `json:"require_pkce"`
		SigningAlg      string   `json:"signing_alg"`
		Issuer          string   `json:"issuer"`
	} `json:"client"`
}

type porchServiceAccount struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	Audiences   []string   `json:"audiences"`
	TTL         int64      `json:"access_token_ttl_seconds"`
	ManagedBy   string     `json:"managed_by"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

func porchTenants(ctx context.Context, pc *apiClient, c *PorchConfig) ([]porchTenant, error) {
	if len(c.Tenants) > 0 {
		out := make([]porchTenant, 0, len(c.Tenants))
		for _, slug := range c.Tenants {
			var t porchTenant
			if _, err := pc.get(ctx, "/tenants/"+url.PathEscape(slug), nil, &t); err != nil {
				if err == errNotFound {
					return nil, fmt.Errorf("tenant %s: %w", slug, err)
				}
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	}
	ts, truncated, err := paged[porchTenant](ctx, pc, "/tenants", nil, 100, 1000)
	if truncated {
		return nil, fmt.Errorf("more than 1000 tenants visible; list them in tenants")
	}
	return ts, err
}

// Test implements sources.Provider.
func (Porch) Test(ctx context.Context, env sources.Env) []sources.Diagnostic {
	c, err := parsePorch(env.Config)
	if err != nil {
		return diagErr("config", err)
	}
	pc, err := porchClient(env, c)
	if err != nil {
		return diagErr("config", err)
	}
	tenants, err := porchTenants(ctx, pc, c)
	if err != nil {
		return diagErr("tenants", err)
	}
	apps := 0
	for _, t := range tenants {
		if t.Status == "disabled" && !c.IncludeDisabled {
			continue
		}
		var list []porchApp
		if _, err := pc.get(ctx, "/tenants/"+url.PathEscape(t.Slug)+"/applications", nil, &list); err != nil {
			return []sources.Diagnostic{{Level: "error", Code: "applications", Message: t.Slug + ": " + message(err)}}
		}
		apps += len(list)
	}
	if len(tenants) == 0 {
		return []sources.Diagnostic{{Level: "warning", Code: "tenants", Message: "the token can read no tenants"}}
	}
	return []sources.Diagnostic{{Level: "info", Code: "ok", Message: fmt.Sprintf("%d tenants, %d applications visible", len(tenants), apps)}}
}

// Sync implements sources.Provider.
func (Porch) Sync(ctx context.Context, env sources.Env, em sources.Emitter) error {
	c, err := parsePorch(env.Config)
	if err != nil {
		return err
	}
	pc, err := porchClient(env, c)
	if err != nil {
		return err
	}
	tenants, err := porchTenants(ctx, pc, c)
	if err != nil {
		return err
	}
	inst := catalog.Ref{ExternalID: "instance"}
	if err := em.Entity(catalog.EntityItem{Kind: "service", Name: c.InstanceName, ExternalID: inst.ExternalID,
		Metadata: map[string]any{"displayName": "Porch", "icon": "tabler:key", "description": "Identity provider (OIDC, forward auth)"},
		Spec:     map[string]any{"url": c.URL, "protocol": "https"}}); err != nil {
		return err
	}
	rel := func(typ string, from, to catalog.Ref, ev map[string]any, inferred bool) error {
		return em.Relation(catalog.RelationItem{Type: typ, From: from, To: to, Evidence: ev, Inferred: inferred,
			ExternalID: typ + ":" + from.String() + "->" + to.String()})
	}
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].Slug < tenants[j].Slug })
	for _, t := range tenants {
		if t.Status == "disabled" && !c.IncludeDisabled {
			continue
		}
		var apps []porchApp
		if _, err := pc.get(ctx, "/tenants/"+url.PathEscape(t.Slug)+"/applications", nil, &apps); err != nil {
			return fmt.Errorf("tenant %s applications: %w", t.Slug, err)
		}
		for _, a := range apps {
			if !a.Enabled && !c.IncludeDisabled {
				continue
			}
			ref := catalog.Ref{ExternalID: "app:" + a.ID}
			md := map[string]any{"displayName": a.Name, "icon": "tabler:key"}
			if a.Description != "" {
				md["description"] = a.Description
			}
			spec := map[string]any{"app_id": a.ID, "tenant": t.Slug, "app_type": a.Kind, "slug": a.Slug, "enabled": a.Enabled,
				"access_mode": a.AccessMode, "hosts": strs(a.Hosts)}
			if a.LaunchURL != "" {
				spec["launch_url"] = a.LaunchURL
			}
			hosts := map[string]string{} // host → basis
			for _, h := range a.Hosts {
				hosts[normHost(h)] = "forward_auth_host"
			}
			if a.Client != nil {
				spec["client_id"] = a.Client.ClientID
				spec["client_type"] = a.Client.ClientType
				spec["redirect_uris"] = strs(a.Client.RedirectURIs)
				spec["grant_types"] = strs(a.Client.GrantTypes)
				spec["require_pkce"] = a.Client.RequirePKCE
				spec["signing_alg"] = a.Client.SigningAlg
				for _, r := range a.Client.RedirectURIs {
					if h := hostOf(r); h != "" && h != "localhost" && h != "127.0.0.1" {
						if _, ok := hosts[h]; !ok {
							hosts[h] = "oidc_redirect_uri"
						}
					}
				}
			}
			if h := hostOf(a.LaunchURL); h != "" {
				if _, ok := hosts[h]; !ok {
					hosts[h] = "launch_url"
				}
			}
			if err := em.Entity(catalog.EntityItem{Kind: "porch_app", Name: slug(t.Slug + "-" + a.Slug), ExternalID: ref.ExternalID,
				Metadata: md, Spec: withoutNil(spec)}); err != nil {
				return err
			}
			if err := rel("authenticates_with", ref, inst, map[string]any{"basis": "porch_application", "tenant": t.Slug, "kind": a.Kind}, false); err != nil {
				return err
			}
			if !*c.MatchHosts || env.Lookup == nil {
				continue
			}
			names := make([]string, 0, len(hosts))
			for h := range hosts {
				names = append(names, h)
			}
			sort.Strings(names)
			for _, h := range names {
				id, ok := env.Lookup(ctx, []string{"dns_record", "domain"}, "/spec/fqdn", h)
				if !ok {
					continue
				}
				typ := "authenticates_with"
				if a.Kind == "forward_auth" && hosts[h] == "forward_auth_host" {
					typ = "protected_by"
				}
				if err := rel(typ, catalog.Ref{ID: id}, ref, map[string]any{"rule": "porch_" + hosts[h], "matched": h}, true); err != nil {
					return err
				}
			}
		}
		if !*c.ServiceAccounts {
			continue
		}
		var sas []porchServiceAccount
		if _, err := pc.get(ctx, "/tenants/"+url.PathEscape(t.Slug)+"/service-accounts", nil, &sas); err != nil {
			em.Partial("Porch service accounts unavailable")
			em.Diag(sources.Diagnostic{Level: "warning", Code: "service_accounts", Message: t.Slug + ": " + message(err)})
			continue
		}
		for _, s := range sas {
			if !s.Enabled && !c.IncludeDisabled {
				continue
			}
			ref := catalog.Ref{ExternalID: "sa:" + s.ID}
			status := "enabled"
			if !s.Enabled {
				status = "disabled"
			}
			md := map[string]any{"displayName": s.Name, "icon": "tabler:robot"}
			if s.Description != "" {
				md["description"] = s.Description
			}
			if err := em.Entity(catalog.EntityItem{Kind: "porch_service_account", Name: slug(t.Slug + "-" + s.Slug), ExternalID: ref.ExternalID,
				Metadata: md, Spec: map[string]any{"service_account_id": s.ID, "tenant": t.Slug, "status": status, "audiences": strs(s.Audiences),
					"access_token_ttl_seconds": s.TTL},
				Observed: withoutNil(map[string]any{"last_used_at": ts(s.LastUsedAt)})}); err != nil {
				return err
			}
			if err := rel("authenticates_with", ref, inst, map[string]any{"basis": "porch_service_account", "tenant": t.Slug}, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func normHost(h string) string {
	if u := hostOf("https://" + h); u != "" {
		return u
	}
	return h
}
