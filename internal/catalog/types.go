// Package catalog owns projects, kinds, relationship types, sources, entities, facets and relations, and the
// deterministic reconciliation engine that merges per-source facets into an effective view with provenance
// (ADR-002, ADR-003, ADR-004).
package catalog

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/obs/redact"
)

// APIVersion is the resource model version.
const APIVersion = "deed.dev/v1"

// Name rules: DNS-label-like slugs, also used for kinds ("custom.example.com/widget") and relationship types.
var (
	nameRE     = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$`)
	kindNameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?/)?[a-z][a-z0-9_]{0,62}$`)
	relNameRE  = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?/)?[a-z][a-z0-9_]{0,62}$`)
	labelKeyRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/)?[a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?$`)
	iconRE     = regexp.MustCompile(`^(tabler|simple-icons|thesvg|custom):[a-z0-9][a-z0-9._-]{0,127}$`)
	envRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	projectRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	lifecycles = []string{"", "planned", "active", "deprecated", "retired"}
	linkTypes  = []string{"repository", "dashboard", "logs", "docs", "runbook", "metrics", "alerts", "ticket", "other"}
)

// ValidName reports whether s is a valid entity or source name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidLabelKey reports whether s is a valid label key (`prefix/name`, Kubernetes-style; no quotes or spaces).
func ValidLabelKey(s string) bool { return labelKeyRE.MatchString(s) }

// ValidKindName reports whether s is a valid kind name.
func ValidKindName(s string) bool { return kindNameRE.MatchString(s) }

// ValidProjectName reports whether s is a valid project slug.
func ValidProjectName(s string) bool { return projectRE.MatchString(s) }

// Ref identifies an entity: by ID, by the source's external ID, or by natural key (kind + name, optionally in
// another project for bridge relations).
type Ref struct {
	ID         string `json:"id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Project    string `json:"project,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
}

// String renders the ref for diagnostics.
func (r Ref) String() string {
	switch {
	case r.ID != "":
		return r.ID
	case r.ExternalID != "":
		return "external:" + r.ExternalID
	case r.Project != "":
		return r.Project + "/" + r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Name
}

// Empty reports whether the ref carries no identifier.
func (r Ref) Empty() bool { return r.ID == "" && r.ExternalID == "" && r.Name == "" }

// EntityItem is one entity fact written by a source (API declaration, import item, ingest item).
type EntityItem struct {
	// Ref locates an existing entity; when it does not resolve, Kind + Name create one.
	Ref        Ref            `json:"ref,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Name       string         `json:"name,omitempty"`
	ExternalID string         `json:"external_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Spec       map[string]any `json:"spec,omitempty"`
	// Observed and Health are written by observation facets (status.observed / status.health).
	Observed   map[string]any `json:"observed,omitempty"`
	Health     *HealthReport  `json:"health,omitempty"`
	Revision   int64          `json:"revision,omitempty"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	// Delete removes the source's facet instead of upserting it.
	Delete bool `json:"delete,omitempty"`
}

// HealthReport is a health observation.
type HealthReport struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// RelationItem is one relationship assertion written by a source.
type RelationItem struct {
	Type       string         `json:"type"`
	From       Ref            `json:"from"`
	To         Ref            `json:"to"`
	ExternalID string         `json:"external_id,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	// Evidence explains the assertion (required for inferred relations): e.g. {"rule": "dns_content_ipv4", "matched": "203.0.113.10"}.
	Evidence   map[string]any `json:"evidence,omitempty"`
	Inferred   bool           `json:"inferred,omitempty"`
	Confidence *float64       `json:"confidence,omitempty"`
	// EvidenceClass overrides the class derived from the source layer: declared, structural, observed, inferred.
	EvidenceClass string     `json:"evidence_class,omitempty"`
	Revision      int64      `json:"revision,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Delete        bool       `json:"delete,omitempty"`
}

// Link is a typed external link on an entity.
type Link struct {
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// metadataFields are the facet-writable metadata keys and their validators.
var metadataFields = map[string]bool{"displayName": true, "description": true, "environment": true, "owner": true, "lifecycle": true,
	"icon": true, "labels": true, "annotations": true, "links": true, "tags": true, "version": true}

// ValidateMetadata checks the metadata a source writes. allowHTTP permits http:// links.
func ValidateMetadata(m map[string]any, allowHTTP bool, v *apperr.Validation) {
	for k, val := range m {
		f := "metadata." + k
		if !metadataFields[k] {
			v.Add(f, "unknown metadata field (allowed: displayName, description, environment, owner, lifecycle, icon, labels, annotations, links, tags, version)")
			continue
		}
		switch k {
		case "displayName":
			checkString(val, 256, false, f, v)
		case "description":
			checkString(val, 8192, true, f, v)
		case "environment":
			if s, ok := val.(string); !ok || (s != "" && !envRE.MatchString(s)) {
				v.Add(f, "must be a lowercase slug")
			}
		case "owner":
			checkString(val, 300, false, f, v)
		case "lifecycle":
			s, _ := val.(string)
			if !contains(lifecycles, s) {
				v.Add(f, "must be planned, active, deprecated or retired")
			}
		case "icon":
			if s, ok := val.(string); !ok || (s != "" && !iconRE.MatchString(s)) {
				v.Add(f, "must be pack:name with pack tabler, simple-icons, thesvg or custom")
			}
		case "labels", "annotations":
			mm, ok := val.(map[string]any)
			if !ok {
				v.Add(f, "must be an object of strings")
				continue
			}
			if len(mm) > 64 {
				v.Add(f, "at most 64 entries")
			}
			for lk, lv := range mm {
				if !labelKeyRE.MatchString(lk) {
					v.Add(f+"."+lk, "invalid key")
				}
				max := 256
				if k == "annotations" {
					max = 4096
				}
				s, ok := lv.(string)
				if !ok || len(s) > max || !utf8.ValidString(s) {
					v.Add(f+"."+lk, "must be a string of at most %d bytes", max)
				}
				if redact.IsSensitiveKey(lk) {
					v.Add(f+"."+lk, "looks like a secret; store a secret reference instead")
				}
			}
		case "links":
			arr, ok := val.([]any)
			if !ok {
				v.Add(f, "must be an array of {type, title, url}")
				continue
			}
			if len(arr) > 32 {
				v.Add(f, "at most 32 links")
			}
			for i, e := range arr {
				lf := fmt.Sprintf("%s[%d]", f, i)
				o, ok := e.(map[string]any)
				if !ok {
					v.Add(lf, "must be an object")
					continue
				}
				t, _ := o["type"].(string)
				if !contains(linkTypes, t) {
					v.Add(lf+".type", "must be one of %s", strings.Join(linkTypes, ", "))
				}
				checkString(o["title"], 200, false, lf+".title", v)
				u, _ := o["url"].(string)
				if err := ValidateLinkURL(u, allowHTTP); err != nil {
					v.Add(lf+".url", "%s", err.Error())
				}
				for key := range o {
					if key != "type" && key != "title" && key != "url" {
						v.Add(lf+"."+key, "unknown field")
					}
				}
			}
		case "tags":
			arr, ok := val.([]any)
			if !ok || len(arr) > 64 {
				v.Add(f, "must be an array of at most 64 strings")
				continue
			}
			for i, e := range arr {
				if s, ok := e.(string); !ok || s == "" || len(s) > 64 {
					v.Add(fmt.Sprintf("%s[%d]", f, i), "must be a string of 1–64 bytes")
				}
			}
		case "version":
			o, ok := val.(map[string]any)
			if !ok {
				v.Add(f, "must be an object {declared, observed, policy, channel}")
				continue
			}
			for vk, vv := range o {
				switch vk {
				case "declared", "observed", "policy", "channel", "product":
					checkString(vv, 128, false, f+"."+vk, v)
				default:
					v.Add(f+"."+vk, "unknown field (allowed: declared, observed, policy, channel, product)")
				}
			}
		}
	}
}

// ValidateLinkURL allows https (and http when permitted) absolute URLs only; javascript:, data:, file: and the
// like are refused (Q-51).
func ValidateLinkURL(s string, allowHTTP bool) error {
	if s == "" || len(s) > 2048 {
		return fmt.Errorf("must be an absolute URL of at most 2048 characters")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return fmt.Errorf("http links are disabled for this project; use https")
		}
	default:
		return fmt.Errorf("scheme %q is not allowed (https only)", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("must not contain credentials")
	}
	return nil
}

func checkString(val any, max int, multiline bool, field string, v *apperr.Validation) {
	if val == nil {
		return
	}
	s, ok := val.(string)
	if !ok {
		v.Add(field, "must be a string")
		return
	}
	if len(s) > max || !utf8.ValidString(s) {
		v.Add(field, "must be valid UTF-8 of at most %d bytes", max)
	}
	if !multiline && strings.ContainsAny(s, "\r\n\x00") {
		v.Add(field, "must be a single line")
	}
	if strings.ContainsRune(s, 0) {
		v.Add(field, "must not contain NUL")
	}
}

// CheckSecretValues refuses spec values whose keys look secret unless they are secret references (Q-54).
func CheckSecretValues(prefix string, m map[string]any, v *apperr.Validation) {
	for k, val := range m {
		f := prefix + "." + k
		if redact.IsSensitiveKey(k) {
			if o, ok := val.(map[string]any); ok {
				if _, ok := o["secretRef"]; ok && len(o) == 1 {
					continue
				}
			}
			if val != nil {
				v.Add(f, "looks like a secret; use {\"secretRef\": {\"store\": …, \"key\": …}} instead of a value")
			}
			continue
		}
		if o, ok := val.(map[string]any); ok {
			if _, isRef := o["secretRef"]; isRef {
				ValidateSecretRef(f, o["secretRef"], v)
				continue
			}
			CheckSecretValues(f, o, v)
		}
		if s, ok := val.(string); ok && looksLikeSecretMaterial(s) {
			v.Add(f, "looks like key material; Deed stores references to secrets, never secrets")
		}
	}
}

// ValidateSecretRef checks a {"store": …, "key": …} reference.
func ValidateSecretRef(field string, ref any, v *apperr.Validation) {
	o, ok := ref.(map[string]any)
	if !ok {
		v.Add(field, "secretRef must be an object {store, key}")
		return
	}
	store, _ := o["store"].(string)
	key, _ := o["key"].(string)
	if !contains([]string{"vault", "1password", "env", "file", "sops", "aws-secrets-manager", "bitwarden", "other"}, store) {
		v.Add(field+".secretRef.store", "unknown secret store")
	}
	if key == "" || len(key) > 512 {
		v.Add(field+".secretRef.key", "must be 1–512 characters")
	}
}

var pemRE = regexp.MustCompile(`-----BEGIN [A-Z ]*(PRIVATE KEY|CERTIFICATE REQUEST|ENCRYPTED)`)

// looksLikeSecretMaterial flags private keys and well-known token shapes.
func looksLikeSecretMaterial(s string) bool {
	if pemRE.MatchString(s) {
		return true
	}
	for _, p := range []string{"AKIA", "ghp_", "github_pat_", "glpat-", "xoxb-", "xoxp-", "sk_live_", "deed_at_", "deed_st_", "pulse_pat_", "porch_at_", "hallpass_at_"} {
		if strings.HasPrefix(s, p) && len(s) >= len(p)+16 && !strings.ContainsAny(s, " \n") {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// rawJSON marshals v to compact JSON.
func rawJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeObject(s string) map[string]any {
	m := map[string]any{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// ValidLabel reports whether a label pair would pass metadata validation (connectors drop the rest).
func ValidLabel(k, v string) bool {
	return labelKeyRE.MatchString(k) && len(v) <= 256 && utf8.ValidString(v) && !redact.IsSensitiveKey(k)
}
