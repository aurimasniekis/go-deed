package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// HallPassType is the provider name of the HallPass connector.
const HallPassType = "hallpass"

// HallPassConfig configures the HallPass connector.
type HallPassConfig struct {
	URL             string   `json:"url"`
	InstanceName    string   `json:"instance_name,omitempty"`
	Grants          *bool    `json:"grants,omitempty"`
	Sessions        *bool    `json:"sessions,omitempty"`
	AgentHostLabel  string   `json:"agent_host_label,omitempty"`
	MatchServers    *bool    `json:"match_servers,omitempty"`
	MaxGrants       int      `json:"max_grants,omitempty"`
	MaxSessions     int      `json:"max_sessions,omitempty"`
	AllowCIDRs      []string `json:"allow_cidrs,omitempty"`
	InsecureHTTP    bool     `json:"insecure_http,omitempty"`
	IncludeDisabled bool     `json:"include_disabled,omitempty"`
}

//nolint:gosec // G101: a JSON Schema, not a credential
const hallpassSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {
    "url": {"type": "string", "format": "uri", "description": "HallPass base URL (the API lives under /api/v1)."},
    "instance_name": {"type": "string", "pattern": "^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$", "default": "hallpass", "description": "Catalog name of the HallPass service entity."},
    "grants": {"type": "boolean", "default": true, "description": "Import active grants as has_access_to edges (they expire with the grant)."},
    "sessions": {"type": "boolean", "default": true, "description": "Import open access sessions."},
    "agent_host_label": {"type": "string", "default": "host", "maxLength": 63, "description": "Agent label naming the machine the agent runs on; such agents also become agent boxes."},
    "match_servers": {"type": "boolean", "default": true, "description": "Link catalog servers/VMs whose hostname or IPv4 equals a HallPass server address (inferred)."},
    "max_grants": {"type": "integer", "minimum": 1, "maximum": 20000, "default": 2000},
    "max_sessions": {"type": "integer", "minimum": 1, "maximum": 20000, "default": 1000},
    "include_disabled": {"type": "boolean", "description": "Also import disabled agents and servers."},
    "allow_cidrs": {"type": "array", "items": {"type": "string"}, "description": "Private CIDRs the HallPass URL resolves to."},
    "insecure_http": {"type": "boolean"}
  }
}`

// HallPass is the hallpass provider: agent identities, protected servers, login roles, active grants and open access
// sessions, read with an auditor (read-only) API token.
type HallPass struct{}

// Describe implements sources.Provider.
func (HallPass) Describe() sources.Descriptor {
	return sources.Descriptor{Type: HallPassType, Title: "HallPass", Description: "Reads agents, servers, roles, active grants and open " +
		"access sessions from the HallPass API with a read-only (auditor) hallpass_at_ token. Agents with active grants get has_access_to " +
		"edges to servers that expire with the grant; servers are protected_by HallPass. Addresses are visible only when the token's role " +
		"may read them.", DefaultLayer: catalog.LayerImported,
		ConfigSchema: json.RawMessage(hallpassSchema), SecretFields: []string{"token"}, Capabilities: sources.Capabilities{Pull: true, Snapshot: true},
		Kinds: []sources.KindSupport{{Kind: "service", Level: sources.SupportNative, Note: "the HallPass instance"},
			{Kind: "hallpass_agent", Level: sources.SupportNative}, {Kind: "agent_box", Level: sources.SupportNative, Note: "agents with a host label"},
			{Kind: "hallpass_server", Level: sources.SupportNative}, {Kind: "hallpass_role", Level: sources.SupportNative},
			{Kind: "hallpass_grant", Level: sources.SupportNative, Note: "active grants"}, {Kind: "agent_session", Level: sources.SupportNative, Note: "open access sessions (one per active grant)"}},
		RelTypes: []string{"has_access_to", "protected_by", "contains", "runs_on", "references", "connects_to"}, DefaultSchedule: 300, Docs: "docs/CONNECTORS.md#hallpass",
		Verified: "validated against a real local HallPass (scripts/validate/hallpass.sh); contract tests against fixtures in its real response shape",
		// Grants and sessions are short-lived: their turnover must not trip the mass-removal guard.
		DefaultPolicy: &catalog.Policy{MaxRemovalPercent: 90}}
}

func parseHallPass(raw json.RawMessage) (*HallPassConfig, error) {
	c, err := decode[HallPassConfig](raw, "hallpass")
	if err != nil {
		return nil, err
	}
	t := true
	if c.InstanceName == "" {
		c.InstanceName = "hallpass"
	}
	if c.Grants == nil {
		c.Grants = &t
	}
	if c.Sessions == nil {
		c.Sessions = &t
	}
	if c.MatchServers == nil {
		c.MatchServers = &t
	}
	if c.AgentHostLabel == "" {
		c.AgentHostLabel = "host"
	}
	if c.MaxGrants == 0 {
		c.MaxGrants = 2000
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = 1000
	}
	return c, nil
}

// Validate implements sources.Provider.
func (HallPass) Validate(raw json.RawMessage, _ map[string]string) error {
	c, err := parseHallPass(raw)
	if err != nil {
		return err
	}
	return validateBaseURL("url", c.URL, c.InsecureHTTP)
}

func hallpassClient(env sources.Env, c *HallPassConfig) (*apiClient, error) {
	u, _ := url.JoinPath(c.URL, "/api/v1")
	return newClient(env, "HallPass", u, c.AllowCIDRs, c.InsecureHTTP, map[string]string{"Authorization": "Bearer " + env.Secrets["token"]})
}

// HallPass API DTOs (subset of api/openapi.yaml).
type hpAgent struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Groups      []string          `json:"groups"`
	Labels      map[string]string `json:"labels"`
	DeletedAt   *time.Time        `json:"deleted_at"`
}

type hpServer struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Alias           string            `json:"alias"`
	DisplayName     string            `json:"display_name"`
	Description     string            `json:"description"`
	Environment     string            `json:"environment"`
	Address         string            `json:"address"`
	Port            int               `json:"port"`
	AddressRedacted bool              `json:"address_redacted"`
	Enabled         bool              `json:"enabled"`
	Groups          []string          `json:"groups"`
	Labels          map[string]string `json:"labels"`
	DeletedAt       *time.Time        `json:"deleted_at"`
}

type hpRole struct {
	ID            string     `json:"id"`
	ServerID      string     `json:"server_id"`
	Alias         string     `json:"alias"`
	DisplayName   string     `json:"display_name"`
	RemoteUser    string     `json:"remote_user"`
	Capabilities  []string   `json:"capabilities"`
	MaxDuration   int        `json:"max_duration"`
	RecordingMode string     `json:"recording_mode"`
	Enabled       bool       `json:"enabled"`
	DeletedAt     *time.Time `json:"deleted_at"`
}

type hpGrant struct {
	ID           string     `json:"id"`
	AgentID      string     `json:"agent_id"`
	ServerID     string     `json:"server_id"`
	RoleID       string     `json:"role_id"`
	RemoteUser   string     `json:"remote_user"`
	Capabilities []string   `json:"capabilities"`
	StartsAt     time.Time  `json:"starts_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	State        string     `json:"state"`
	EndedAt      *time.Time `json:"ended_at"`
}

type hpSession struct {
	ID         string     `json:"id"`
	GrantID    string     `json:"grant_id"`
	State      string     `json:"state"`
	OpenedAt   time.Time  `json:"opened_at"`
	ClosedAt   *time.Time `json:"closed_at"`
	AgentID    string     `json:"agent_id"`
	ServerID   string     `json:"server_id"`
	RoleID     string     `json:"role_id"`
	RemoteUser string     `json:"remote_user"`
	// ChannelsLive is only returned by GET /access-sessions/{id}, not by the list the connector reads.
	ChannelsLive *int `json:"channels_live"`
}

type hpStatus struct {
	Lockdown        bool   `json:"lockdown"`
	Version         string `json:"version"`
	PendingRequests int    `json:"pending_requests"`
	ActiveGrants    int    `json:"active_grants"`
	LiveChannels    int    `json:"live_channels"`
	StorageOK       bool   `json:"storage_ok"`
	BreakglassAlert bool   `json:"breakglass_alert"`
}

type hpList[T any] struct {
	Items []T `json:"items"`
}

// Test implements sources.Provider.
func (HallPass) Test(ctx context.Context, env sources.Env) []sources.Diagnostic {
	c, err := parseHallPass(env.Config)
	if err != nil {
		return diagErr("config", err)
	}
	hc, err := hallpassClient(env, c)
	if err != nil {
		return diagErr("config", err)
	}
	var st hpStatus
	if _, err := hc.get(ctx, "/system/status", nil, &st); err != nil {
		return diagErr("connect", err)
	}
	var agents hpList[hpAgent]
	if _, err := hc.get(ctx, "/agents", nil, &agents); err != nil {
		return diagErr("agents", err)
	}
	var servers hpList[hpServer]
	if _, err := hc.get(ctx, "/servers", nil, &servers); err != nil {
		return diagErr("servers", err)
	}
	out := []sources.Diagnostic{{Level: "info", Code: "ok", Message: fmt.Sprintf("HallPass %s: %d agents, %d servers, %d active grants visible",
		st.Version, len(agents.Items), len(servers.Items), st.ActiveGrants)}}
	for _, s := range servers.Items {
		if s.AddressRedacted {
			out = append(out, sources.Diagnostic{Level: "warning", Code: "address_redacted", Message: "server addresses are redacted for this token; server matching uses aliases only"})
			break
		}
	}
	return out
}

// Sync implements sources.Provider.
func (HallPass) Sync(ctx context.Context, env sources.Env, em sources.Emitter) error {
	c, err := parseHallPass(env.Config)
	if err != nil {
		return err
	}
	hc, err := hallpassClient(env, c)
	if err != nil {
		return err
	}
	var st hpStatus
	if _, err := hc.get(ctx, "/system/status", nil, &st); err != nil {
		return err
	}
	inst := catalog.Ref{ExternalID: "instance"}
	health := &catalog.HealthReport{State: "ok"}
	switch {
	case !st.StorageOK:
		health = &catalog.HealthReport{State: "failed", Reason: "HallPass reports storage not OK"}
	case st.Lockdown:
		health = &catalog.HealthReport{State: "warning", Reason: "HallPass is in lockdown"}
	case st.BreakglassAlert:
		health = &catalog.HealthReport{State: "warning", Reason: "unacknowledged HallPass break-glass recovery"}
	}
	if err := em.Entity(catalog.EntityItem{Kind: "service", Name: c.InstanceName, ExternalID: inst.ExternalID, Health: health,
		Metadata: map[string]any{"displayName": "HallPass", "icon": "tabler:lock-access"},
		Spec:     map[string]any{"url": c.URL, "protocol": "https"},
		Observed: map[string]any{"version": st.Version, "active_grants": st.ActiveGrants, "pending_requests": st.PendingRequests,
			"live_channels": st.LiveChannels, "lockdown": st.Lockdown}}); err != nil {
		return err
	}
	rel := func(typ string, from, to catalog.Ref, ev map[string]any, exp *time.Time, inferred bool) error {
		return em.Relation(catalog.RelationItem{Type: typ, From: from, To: to, Evidence: ev, ExpiresAt: exp, Inferred: inferred,
			ExternalID: typ + ":" + from.String() + "->" + to.String()})
	}

	var agents hpList[hpAgent]
	if _, err := hc.get(ctx, "/agents", nil, &agents); err != nil {
		return err
	}
	agentRef := map[string]catalog.Ref{}
	for _, a := range agents.Items {
		if a.DeletedAt != nil || (!a.Enabled && !c.IncludeDisabled) {
			continue
		}
		ref := catalog.Ref{ExternalID: "agent:" + a.ID}
		agentRef[a.ID] = ref
		md := map[string]any{"displayName": a.Name, "icon": "tabler:robot", "labels": safeLabels(a.Labels)}
		if a.Description != "" {
			md["description"] = a.Description
		}
		status := "enabled"
		if !a.Enabled {
			status = "disabled"
		}
		if err := em.Entity(catalog.EntityItem{Kind: "hallpass_agent", Name: slug(a.Name), ExternalID: ref.ExternalID, Metadata: md,
			Spec: map[string]any{"agent_id": a.ID, "name": a.Name, "status": status, "groups": strs(a.Groups)}}); err != nil {
			return err
		}
		if err := rel("protected_by", ref, inst, map[string]any{"basis": "hallpass_agent"}, nil, false); err != nil {
			return err
		}
		if host := a.Labels[c.AgentHostLabel]; host != "" && slug(host) != "" {
			box := catalog.Ref{ExternalID: "box:" + a.ID}
			if err := em.Entity(catalog.EntityItem{Kind: "agent_box", Name: slug(host + "-" + a.Name), ExternalID: box.ExternalID,
				Metadata: map[string]any{"displayName": a.Name + " on " + host, "icon": "tabler:box-model"},
				Spec:     map[string]any{"hostname": host, "agent_id": a.ID}}); err != nil {
				return err
			}
			if err := rel("runs_on", ref, box, map[string]any{"basis": "agent_label", "label": c.AgentHostLabel}, nil, false); err != nil {
				return err
			}
			if *c.MatchServers && env.Lookup != nil {
				if id, ok := env.Lookup(ctx, []string{"server", "vm"}, "/spec/hostname", host); ok {
					if err := rel("runs_on", box, catalog.Ref{ID: id}, map[string]any{"rule": "agent_host_label_equals_hostname", "matched": host}, nil, true); err != nil {
						return err
					}
				}
			}
		}
	}

	var servers hpList[hpServer]
	if _, err := hc.get(ctx, "/servers", nil, &servers); err != nil {
		return err
	}
	serverRef := map[string]catalog.Ref{}
	roleRef := map[string]catalog.Ref{}
	for _, s := range servers.Items {
		if s.DeletedAt != nil || (!s.Enabled && !c.IncludeDisabled) {
			continue
		}
		ref := catalog.Ref{ExternalID: "server:" + s.ID}
		serverRef[s.ID] = ref
		dn := s.DisplayName
		if dn == "" {
			dn = s.Alias
		}
		md := map[string]any{"displayName": dn, "icon": "tabler:server-bolt", "labels": safeLabels(s.Labels)}
		if s.Description != "" {
			md["description"] = s.Description
		}
		if e := envSlug(s.Environment); e != "" {
			md["environment"] = e
		}
		spec := map[string]any{"server_id": s.ID, "kind": s.Kind, "alias": s.Alias, "hostname": s.Alias, "groups": strs(s.Groups), "enabled": s.Enabled}
		if !s.AddressRedacted && s.Address != "" {
			spec["address"] = s.Address
			if s.Port > 0 {
				spec["port"] = s.Port
			}
		}
		if err := em.Entity(catalog.EntityItem{Kind: "hallpass_server", Name: slug(s.Alias), ExternalID: ref.ExternalID, Metadata: md, Spec: spec}); err != nil {
			return err
		}
		if err := rel("protected_by", ref, inst, map[string]any{"basis": "hallpass_server"}, nil, false); err != nil {
			return err
		}
		if *c.MatchServers && env.Lookup != nil && !s.AddressRedacted && s.Address != "" {
			ptr := "/spec/hostname"
			if ip := net.ParseIP(s.Address); ip != nil && ip.To4() != nil {
				ptr = "/spec/ipv4"
			}
			if id, ok := env.Lookup(ctx, []string{"server", "vm", "container"}, ptr, s.Address); ok {
				if err := rel("protected_by", catalog.Ref{ID: id}, ref, map[string]any{"rule": "hallpass_server_address", "pointer": ptr, "matched": s.Address}, nil, true); err != nil {
					return err
				}
			}
		}
		var roles hpList[hpRole]
		if _, err := hc.get(ctx, "/servers/"+url.PathEscape(s.ID)+"/roles", nil, &roles); err != nil {
			em.Partial("HallPass roles unavailable for a server")
			em.Diag(sources.Diagnostic{Level: "warning", Code: "roles", Message: s.Alias + ": " + message(err)})
			continue
		}
		for _, r := range roles.Items {
			if r.DeletedAt != nil || (!r.Enabled && !c.IncludeDisabled) {
				continue
			}
			rref := catalog.Ref{ExternalID: "role:" + r.ID}
			roleRef[r.ID] = rref
			dn := r.DisplayName
			if dn == "" {
				dn = r.Alias
			}
			if err := em.Entity(catalog.EntityItem{Kind: "hallpass_role", Name: slug(s.Alias + "-" + r.Alias), ExternalID: rref.ExternalID,
				Metadata: map[string]any{"displayName": s.Alias + " / " + dn, "icon": "tabler:id-badge"},
				Spec: map[string]any{"role_id": r.ID, "login": r.RemoteUser, "server_id": r.ServerID, "capabilities": strs(r.Capabilities),
					"max_duration_seconds": r.MaxDuration, "recording_mode": r.RecordingMode}}); err != nil {
				return err
			}
			if err := rel("contains", ref, rref, map[string]any{"basis": "hallpass_role"}, nil, false); err != nil {
				return err
			}
		}
	}

	if *c.Grants {
		grants, truncated, err := paged[hpGrant](ctx, hc, "/grants", url.Values{"state": {"active"}}, 200, c.MaxGrants)
		if err != nil {
			em.Partial("HallPass grants unavailable")
			em.Diag(sources.Diagnostic{Level: "warning", Code: "grants", Message: message(err)})
		}
		if truncated {
			em.Partial("max_grants reached")
			em.Diag(sources.Diagnostic{Level: "warning", Code: "max_grants", Message: "active grant cap reached; raise max_grants", Count: c.MaxGrants})
		}
		sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
		// An agent often holds several active grants on one server (one per role): the agent→server edge is emitted
		// once, lasting until the latest of them expires.
		type access struct {
			from, to    catalog.Ref
			exp         time.Time
			ids, logins []string
		}
		var accessOrder []string
		accesses := map[string]*access{}
		for _, g := range grants {
			ar, aok := agentRef[g.AgentID]
			sr, sok := serverRef[g.ServerID]
			if g.State != "active" || !aok || !sok {
				continue
			}
			exp := g.ExpiresAt
			gref := catalog.Ref{ExternalID: "grant:" + g.ID}
			if err := em.Entity(catalog.EntityItem{Kind: "hallpass_grant", Name: slug("grant-" + g.ID), ExternalID: gref.ExternalID, ExpiresAt: &exp,
				Metadata: map[string]any{"displayName": "grant " + g.RemoteUser + " until " + exp.UTC().Format(time.RFC3339), "icon": "tabler:shield-check"},
				Spec: map[string]any{"grant_id": g.ID, "subject": g.AgentID, "expires_at": exp.UTC().Format(time.RFC3339), "starts_at": g.StartsAt.UTC().Format(time.RFC3339),
					"remote_user": g.RemoteUser, "capabilities": strs(g.Capabilities), "state": g.State}}); err != nil {
				return err
			}
			key := g.AgentID + "\x00" + g.ServerID
			acc := accesses[key]
			if acc == nil {
				acc = &access{from: ar, to: sr}
				accesses[key] = acc
				accessOrder = append(accessOrder, key)
			}
			if exp.After(acc.exp) {
				acc.exp = exp
			}
			acc.ids = append(acc.ids, g.ID)
			if !slices.Contains(acc.logins, g.RemoteUser) {
				acc.logins = append(acc.logins, g.RemoteUser)
			}
			if err := rel("references", gref, ar, map[string]any{"basis": "grant_agent"}, &exp, false); err != nil {
				return err
			}
			to := sr
			if rr, ok := roleRef[g.RoleID]; ok {
				to = rr
			}
			if err := rel("has_access_to", gref, to, map[string]any{"basis": "grant_role"}, &exp, false); err != nil {
				return err
			}
		}
		for _, key := range accessOrder {
			acc := accesses[key]
			exp := acc.exp
			ev := map[string]any{"basis": "hallpass_grant", "grant_ids": acc.ids, "remote_users": acc.logins, "expires_at": exp.UTC().Format(time.RFC3339)}
			if err := rel("has_access_to", acc.from, acc.to, ev, &exp, false); err != nil {
				return err
			}
		}
	}

	if *c.Sessions {
		var sessions []hpSession
		for _, state := range []string{"open", "closing"} {
			ss, truncated, err := paged[hpSession](ctx, hc, "/access-sessions", url.Values{"state": {state}}, 200, c.MaxSessions)
			if err != nil {
				em.Partial("HallPass sessions unavailable")
				em.Diag(sources.Diagnostic{Level: "warning", Code: "sessions", Message: message(err)})
				break
			}
			if truncated {
				em.Partial("max_sessions reached")
			}
			sessions = append(sessions, ss...)
		}
		for _, s := range sessions {
			ar, aok := agentRef[s.AgentID]
			sr, sok := serverRef[s.ServerID]
			if !aok || !sok {
				continue
			}
			sref := catalog.Ref{ExternalID: "session:" + s.ID}
			opened := s.OpenedAt
			var observed map[string]any
			if s.ChannelsLive != nil {
				observed = map[string]any{"channels_live": *s.ChannelsLive}
			}
			if err := em.Entity(catalog.EntityItem{Kind: "agent_session", Name: slug("session-" + s.ID), ExternalID: sref.ExternalID,
				Metadata: map[string]any{"displayName": "session " + s.RemoteUser + " since " + opened.UTC().Format(time.RFC3339), "icon": "tabler:terminal-2"},
				Spec:     map[string]any{"session_id": s.ID, "started_at": ts(&opened), "status": s.State, "remote_user": s.RemoteUser, "grant_id": s.GrantID},
				Observed: observed}); err != nil {
				return err
			}
			if err := rel("references", sref, ar, map[string]any{"basis": "session_agent"}, nil, false); err != nil {
				return err
			}
			if err := rel("connects_to", sref, sr, map[string]any{"basis": "session_server"}, nil, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// safeLabels keeps HallPass labels that are valid Deed label keys and do not look like secrets.
func safeLabels(in map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if len(out) >= 64 || !catalog.ValidLabel(k, v) {
			continue
		}
		out[k] = v
	}
	return out
}
