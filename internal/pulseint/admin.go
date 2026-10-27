package pulseint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// AdminType is the provider name of the Pulse admin adapter.
const AdminType = "pulse_admin"

// AdminConfig configures the admin adapter.
type AdminConfig struct {
	AdminURL        string   `json:"admin_url"`
	Stream          string   `json:"stream,omitempty"`
	SubjectPrefix   string   `json:"subject_prefix,omitempty"`
	MaxSubjects     int      `json:"max_subjects,omitempty"`
	AllowCIDRs      []string `json:"allow_cidrs,omitempty"`
	InsecureHTTP    bool     `json:"insecure_http,omitempty"`
	IncludeInternal bool     `json:"include_internal_consumers,omitempty"`
}

const adminSchema = `{
  "type": "object", "additionalProperties": false, "required": ["admin_url"],
  "properties": {
    "admin_url": {"type": "string", "format": "uri", "description": "Base URL of the Pulse admin listener (e.g. http://127.0.0.1:8080 through an SSH tunnel). PATs work only there."},
    "stream": {"type": "string", "default": "EVENTS", "pattern": "^[A-Za-z0-9_-]{1,64}$"},
    "subject_prefix": {"type": "string", "default": "evt", "maxLength": 256},
    "max_subjects": {"type": "integer", "minimum": 10, "maximum": 20000, "default": 2000},
    "allow_cidrs": {"type": "array", "items": {"type": "string"}, "description": "Private CIDRs the admin URL resolves to."},
    "insecure_http": {"type": "boolean", "description": "Allow http:// (loopback tunnels)."},
    "include_internal_consumers": {"type": "boolean", "description": "Also import pulsed's own consumers."}
  }
}`

// Admin is the pulse_admin provider.
type Admin struct{}

// Describe implements sources.Provider.
func (Admin) Describe() sources.Descriptor {
	return sources.Descriptor{Type: AdminType, Title: "Pulse admin", Description: "Reads the Pulse admin API (viewer PAT): clients and their " +
		"publish/subscribe grants, streams, durable consumers and subject activity. Grants and consumer filters become declared relationships; " +
		"Pulse does not record which client published a message, so publishers come from the tail adapter.", DefaultLayer: catalog.LayerImported,
		ConfigSchema: json.RawMessage(adminSchema), SecretFields: []string{"token"}, Capabilities: sources.Capabilities{Pull: true, Snapshot: true},
		Kinds: []sources.KindSupport{{Kind: "nats_system", Level: sources.SupportNative}, {Kind: "nats_stream", Level: sources.SupportNative},
			{Kind: "nats_consumer", Level: sources.SupportNative}, {Kind: "nats_subject", Level: sources.SupportNative},
			{Kind: "service", Level: sources.SupportNative, Note: "one per Pulse client"}, {Kind: "event_contract", Level: sources.SupportNative, Note: "last ce-type per subject"}},
		RelTypes: []string{"publishes", "subscribes_to", "contains", "carried_on"}, DefaultSchedule: 300, Docs: "docs/PULSE.md",
		Verified: "contract tests against fixtures recorded from a real pulsed; validated against pulsed built from source (scripts/validate/pulse.sh)"}
}

func parseAdmin(raw json.RawMessage) (*AdminConfig, error) {
	var c AdminConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "invalid pulse_admin config")
	}
	if c.Stream == "" {
		c.Stream = "EVENTS"
	}
	if c.SubjectPrefix == "" {
		c.SubjectPrefix = "evt"
	}
	if c.MaxSubjects == 0 {
		c.MaxSubjects = 2000
	}
	return &c, nil
}

// Validate implements sources.Provider.
func (Admin) Validate(raw json.RawMessage, _ map[string]string) error {
	c, err := parseAdmin(raw)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.AdminURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return apperr.New(apperr.CodeInvalid, "admin_url must be an http(s) URL")
	}
	if u.Scheme == "http" && !c.InsecureHTTP {
		return apperr.New(apperr.CodeInvalid, "admin_url uses http; set insecure_http for a loopback tunnel")
	}
	return nil
}

type adminClient struct {
	base  string
	token string
	http  *http.Client
}

func newAdminClient(env sources.Env, c *AdminConfig) (*adminClient, error) {
	u, err := url.Parse(c.AdminURL)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "invalid admin_url")
	}
	hc, err := env.HTTP(safehttp.Policy{Allow: []string{u.Scheme + "://" + u.Host}, AllowCIDRs: c.AllowCIDRs, InsecureHTTP: c.InsecureHTTP,
		Timeout: 30 * time.Second, MaxBodyBytes: 32 << 20, MaxRedirects: -1})
	if err != nil {
		return nil, err
	}
	return &adminClient{base: strings.TrimSuffix(c.AdminURL, "/") + "/api/admin/v1", token: env.Secrets["token"], http: hc}, nil
}

func (a *adminClient) get(ctx context.Context, p string, q url.Values, out any) error {
	u := a.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return apperr.New(apperr.CodeForbidden, "Pulse refused the token (%d); use a viewer PAT on the admin listener", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		return apperr.New(apperr.CodeUnavailable, "Pulse admin API answered %d for %s", resp.StatusCode, p)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.New(apperr.CodeUnavailable, "Pulse admin API returned an unexpected body for %s", p)
	}
	return nil
}

// Pulse admin DTOs (subset of api/admin.openapi.json).
type clientDTO struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Tags        []string   `json:"tags"`
	Status      string     `json:"status"`
	Connections int64      `json:"connections"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	Grants      []struct {
		Action  string `json:"action"`
		Subject string `json:"subject"`
		Stream  string `json:"stream"`
		Name    string `json:"name"`
	} `json:"grants"`
}

type streamDTO struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Subjects    []string `json:"subjects"`
	Storage     string   `json:"storage"`
	Retention   string   `json:"retention"`
	MaxBytes    int64    `json:"max_bytes"`
	State       struct {
		Msgs      int64     `json:"msgs"`
		Bytes     int64     `json:"bytes"`
		Consumers int64     `json:"consumers"`
		LastTime  time.Time `json:"last_time"`
	} `json:"state"`
}

type consumerDTO struct {
	Name           string     `json:"name"`
	Durable        bool       `json:"durable"`
	Internal       bool       `json:"internal"`
	FilterSubjects []string   `json:"filter_subjects"`
	DeliverPolicy  string     `json:"deliver_policy"`
	AckPolicy      string     `json:"ack_policy"`
	NumPending     int64      `json:"num_pending"`
	NumAckPending  int64      `json:"num_ack_pending"`
	NumRedelivered int64      `json:"num_redelivered"`
	LastActive     *time.Time `json:"last_active"`
}

type treeNode struct {
	Token    string    `json:"token"`
	Path     string    `json:"path"`
	Leaf     bool      `json:"leaf"`
	Children int64     `json:"children"`
	Msgs     int64     `json:"msgs"`
	Bytes    int64     `json:"bytes"`
	LastSeen time.Time `json:"last_seen"`
	LastType string    `json:"last_type"`
}

type systemInfo struct {
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
	NATS    struct {
		Connected  bool    `json:"connected"`
		ServerName string  `json:"server_name"`
		Version    string  `json:"version"`
		RTTMs      float64 `json:"rtt_ms"`
	} `json:"nats"`
}

// Test implements sources.Provider.
func (Admin) Test(ctx context.Context, env sources.Env) []sources.Diagnostic {
	c, err := parseAdmin(env.Config)
	if err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "config", Message: err.Error()}}
	}
	ac, err := newAdminClient(env, c)
	if err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "config", Message: err.Error()}}
	}
	var info systemInfo
	if err := ac.get(ctx, "/system/info", nil, &info); err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "connect", Message: message(err)}}
	}
	var clients []clientDTO
	if err := ac.get(ctx, "/clients", nil, &clients); err != nil {
		return []sources.Diagnostic{{Level: "error", Code: "clients", Message: message(err)}}
	}
	return []sources.Diagnostic{{Level: "info", Code: "ok", Message: fmt.Sprintf("pulsed %s (NATS %s, ready=%v): %d clients visible",
		info.Version, info.NATS.Version, info.Ready, len(clients))}}
}

func message(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "request failed: " + safehttp.Classify(err)
}

// Sync implements sources.Provider.
func (Admin) Sync(ctx context.Context, env sources.Env, em sources.Emitter) error {
	c, err := parseAdmin(env.Config)
	if err != nil {
		return err
	}
	ac, err := newAdminClient(env, c)
	if err != nil {
		return err
	}
	var info systemInfo
	if err := ac.get(ctx, "/system/info", nil, &info); err != nil {
		return err
	}
	sysName := EntityName(info.NATS.ServerName)
	if sysName == "" {
		sysName = "pulse"
	}
	health := &catalog.HealthReport{State: "ok"}
	if !info.Ready || !info.NATS.Connected {
		health = &catalog.HealthReport{State: "failed", Reason: "pulsed reports not ready or NATS disconnected"}
	}
	sysRef := catalog.Ref{ExternalID: "system"}
	if err := em.Entity(catalog.EntityItem{Kind: "nats_system", Name: sysName, ExternalID: "system", Health: health,
		Metadata: map[string]any{"displayName": "Pulse (" + info.NATS.ServerName + ")", "icon": "simple-icons:natsdotio"},
		Spec:     map[string]any{"server_version": info.NATS.Version, "jetstream": true},
		Observed: map[string]any{"pulsed_version": info.Version, "rtt_ms": info.NATS.RTTMs}}); err != nil {
		return err
	}
	subjects := map[string]bool{}
	subject := func(s string) (catalog.Ref, error) {
		ext := "subject:" + s
		if !subjects[s] {
			subjects[s] = true
			if err := em.Entity(catalog.EntityItem{Kind: "nats_subject", Name: EntityName(s), ExternalID: ext,
				Spec: map[string]any{"subject": s, "pattern": IsPattern(s)}}); err != nil {
				return catalog.Ref{}, err
			}
		}
		return catalog.Ref{ExternalID: ext}, nil
	}
	rel := func(typ string, from, to catalog.Ref, ev map[string]any) error {
		return em.Relation(catalog.RelationItem{Type: typ, From: from, To: to, EvidenceClass: catalog.EvidenceDeclared, Evidence: ev,
			ExternalID: typ + ":" + from.String() + "->" + to.String()})
	}
	var clients []clientDTO
	if err := ac.get(ctx, "/clients", nil, &clients); err != nil {
		return err
	}
	byClient := map[string]catalog.Ref{}
	for _, cl := range clients {
		if cl.RevokedAt != nil {
			continue
		}
		ref := catalog.Ref{ExternalID: "client:" + cl.ID}
		byClient[cl.ID] = ref
		// The client's name and description describe its Pulse credentials ("Porch domain events, …"), not the service,
		// which its own connector or declaration usually names: they go into annotations, so they never compete with
		// the service's displayName and description.
		labels := map[string]any{"pulse/client": "true"}
		annotations := map[string]any{"pulse/client-name": cl.Name}
		if cl.Description != "" {
			annotations["pulse/client-description"] = cl.Description
		}
		md := map[string]any{"labels": labels, "annotations": annotations}
		if len(cl.Tags) > 0 {
			tags := []any{}
			for _, t := range cl.Tags {
				tags = append(tags, t)
			}
			md["tags"] = tags
		}
		st := &catalog.HealthReport{State: "ok"}
		if cl.Status != "" && cl.Status != "active" {
			st = &catalog.HealthReport{State: "warning", Reason: "Pulse client status " + cl.Status}
		}
		if err := em.Entity(catalog.EntityItem{Kind: "service", Name: EntityName(cl.Name), ExternalID: ref.ExternalID, Metadata: md,
			Spec: map[string]any{"pulse_client_id": cl.ID}, Observed: map[string]any{"pulse_connections": cl.Connections, "pulse_status": cl.Status},
			Health: st}); err != nil {
			return err
		}
		for _, g := range cl.Grants {
			if g.Subject == "" || !strings.HasPrefix(g.Subject, c.SubjectPrefix+".") && g.Subject != c.SubjectPrefix+".>" {
				continue
			}
			typ := ""
			switch g.Action {
			case "publish", "request":
				typ = "publishes"
			case "subscribe", "reply":
				typ = "subscribes_to"
			default:
				continue
			}
			sref, err := subject(g.Subject)
			if err != nil {
				return err
			}
			if err := rel(typ, ref, sref, map[string]any{"basis": "pulse_grant", "action": g.Action, "client_id": cl.ID}); err != nil {
				return err
			}
		}
	}
	var streams []streamDTO
	if err := ac.get(ctx, "/streams", nil, &streams); err != nil {
		return err
	}
	for _, s := range streams {
		if s.Kind != "" && s.Kind != "stream" {
			continue
		}
		sref := catalog.Ref{ExternalID: "stream:" + s.Name}
		subs := []any{}
		for _, x := range s.Subjects {
			subs = append(subs, x)
		}
		if err := em.Entity(catalog.EntityItem{Kind: "nats_stream", Name: EntityName(s.Name), ExternalID: sref.ExternalID,
			Metadata: map[string]any{"displayName": s.Name},
			Spec:     map[string]any{"stream": s.Name, "subjects": subs, "storage": s.Storage, "retention": s.Retention, "max_bytes": s.MaxBytes},
			Observed: map[string]any{"messages": s.State.Msgs, "bytes": s.State.Bytes, "consumers": s.State.Consumers,
				"last_message_at": s.State.LastTime.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		if err := rel("contains", sysRef, sref, map[string]any{"basis": "pulse_stream"}); err != nil {
			return err
		}
		var consumers []consumerDTO
		if err := ac.get(ctx, "/streams/"+url.PathEscape(s.Name)+"/consumers", nil, &consumers); err != nil {
			em.Diag(sources.Diagnostic{Level: "warning", Code: "consumers", Message: message(err)})
			continue
		}
		for _, co := range consumers {
			if !co.Durable || (co.Internal && !c.IncludeInternal) {
				continue
			}
			cref := catalog.Ref{ExternalID: "consumer:" + s.Name + "/" + co.Name}
			fs := []any{}
			for _, f := range co.FilterSubjects {
				fs = append(fs, f)
			}
			obs := map[string]any{"pending": co.NumPending, "ack_pending": co.NumAckPending, "redelivered": co.NumRedelivered}
			if co.LastActive != nil {
				obs["last_active"] = co.LastActive.UTC().Format(time.RFC3339)
			}
			if err := em.Entity(catalog.EntityItem{Kind: "nats_consumer", Name: EntityName(s.Name + "-" + co.Name), ExternalID: cref.ExternalID,
				Metadata: map[string]any{"displayName": co.Name},
				Spec:     map[string]any{"durable": co.Name, "stream": s.Name, "filter_subjects": fs, "deliver_policy": co.DeliverPolicy, "ack_policy": co.AckPolicy},
				Observed: obs}); err != nil {
				return err
			}
			if err := rel("contains", sref, cref, map[string]any{"basis": "consumer_config"}); err != nil {
				return err
			}
			owner, hasOwner := consumerOwner(co.Name, byClient)
			for _, f := range co.FilterSubjects {
				fref, err := subject(f)
				if err != nil {
					return err
				}
				if err := rel("subscribes_to", cref, fref, map[string]any{"basis": "consumer_config", "consumer": co.Name}); err != nil {
					return err
				}
				if hasOwner {
					if err := rel("subscribes_to", owner, fref, map[string]any{"basis": "consumer_config", "consumer": co.Name}); err != nil {
						return err
					}
				}
			}
		}
	}
	templates, err := walkSubjects(ctx, ac, c, em, subject)
	if err != nil {
		return err
	}
	// Wildcard grants and consumer filters contain the concrete subject templates they match.
	var patterns []string
	for s := range subjects {
		if IsPattern(s) {
			patterns = append(patterns, s)
		}
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		for _, t := range templates {
			if t != p && Matches(p, strings.ReplaceAll(t, "*", "x")) {
				if err := em.Relation(catalog.RelationItem{Type: "contains", From: catalog.Ref{ExternalID: "subject:" + p}, To: catalog.Ref{ExternalID: "subject:" + t},
					EvidenceClass: catalog.EvidenceStructural, Evidence: map[string]any{"basis": "wildcard_match", "pattern": p},
					ExternalID: "contains:" + p + "->" + t}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// consumerOwner maps a durable named "<clientID>_<name>" to its client.
func consumerOwner(name string, byClient map[string]catalog.Ref) (catalog.Ref, bool) {
	for id, ref := range byClient {
		if strings.HasPrefix(name, id+"_") {
			return ref, true
		}
	}
	return catalog.Ref{}, false
}

// walkSubjects reads the subject index breadth-first and records activity per subject template.
func walkSubjects(ctx context.Context, ac *adminClient, c *AdminConfig, em sources.Emitter, subject func(string) (catalog.Ref, error)) ([]string, error) {
	type agg struct {
		msgs, bytes int64
		last        time.Time
		lastType    string
		concrete    int
	}
	templates := map[string]*agg{}
	queue := []string{c.SubjectPrefix}
	visited := 0
	for len(queue) > 0 && visited < c.MaxSubjects {
		prefix := queue[0]
		queue = queue[1:]
		var tree struct {
			Nodes []treeNode `json:"nodes"`
		}
		if err := ac.get(ctx, "/subjects/tree", url.Values{"stream": {c.Stream}, "prefix": {prefix}}, &tree); err != nil {
			em.Diag(sources.Diagnostic{Level: "warning", Code: "subjects", Message: message(err)})
			em.Partial("the subject index could not be read completely")
			break
		}
		for _, n := range tree.Nodes {
			visited++
			if n.Leaf {
				t := Template(n.Path)
				a := templates[t]
				if a == nil {
					a = &agg{}
					templates[t] = a
				}
				a.msgs += n.Msgs
				a.bytes += n.Bytes
				a.concrete++
				if n.LastSeen.After(a.last) {
					a.last, a.lastType = n.LastSeen, n.LastType
				}
			}
			if n.Children > 0 && len(strings.Split(n.Path, ".")) < 12 {
				queue = append(queue, n.Path)
			}
		}
	}
	if visited >= c.MaxSubjects {
		em.Diag(sources.Diagnostic{Level: "warning", Code: "subject_cap", Message: fmt.Sprintf("stopped after %d subject index nodes (max_subjects)", c.MaxSubjects)})
		em.Partial("subject index truncated by max_subjects")
	}
	keys := make([]string, 0, len(templates))
	for k := range templates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, t := range keys {
		a := templates[t]
		if _, err := subject(t); err != nil {
			return nil, err
		}
		if err := em.Entity(catalog.EntityItem{Ref: catalog.Ref{ExternalID: "subject:" + t}, Kind: "nats_subject", Name: EntityName(t), ExternalID: "subject:" + t,
			Spec: map[string]any{"subject": t, "pattern": IsPattern(t)},
			Observed: map[string]any{"messages_retained": a.msgs, "bytes_retained": a.bytes, "last_seen": a.last.UTC().Format(time.RFC3339),
				"last_type": a.lastType, "concrete_subjects": a.concrete}}); err != nil {
			return nil, err
		}
		if a.lastType != "" {
			cref := catalog.Ref{ExternalID: "contract:" + a.lastType}
			if err := em.Entity(catalog.EntityItem{Kind: "event_contract", Name: EntityName(a.lastType), ExternalID: cref.ExternalID,
				Spec: map[string]any{"type": a.lastType}}); err != nil {
				return nil, err
			}
			if err := em.Relation(catalog.RelationItem{Type: "carried_on", From: cref, To: catalog.Ref{ExternalID: "subject:" + t},
				EvidenceClass: catalog.EvidenceObserved, Evidence: map[string]any{"basis": "subject_index_last_type", "last_seen": a.last.UTC().Format(time.RFC3339)},
				ExternalID: "carried_on:" + a.lastType + "->" + t}); err != nil {
				return nil, err
			}
		}
	}
	return keys, nil
}
