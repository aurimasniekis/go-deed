// Adapted from Porch internal/config/config.go (Apache-2.0).

// Package config loads and validates Deed configuration.
//
// Configuration comes from a YAML file plus DEED_* environment overrides derived from the YAML path
// (http.listen → DEED_HTTP_LISTEN). Unknown keys and unknown DEED_* variables are rejected. The runtime mode is
// "production" unless explicitly set to "development"; development-only conveniences are rejected in production.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Mode is the runtime mode.
type Mode string

// Runtime modes.
const (
	ModeProduction  Mode = "production"
	ModeDevelopment Mode = "development"
)

// Duration is a time.Duration that unmarshals from Go duration strings ("30s", "15m").
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"30s\": %w", n.Line, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config is the complete Deed configuration.
type Config struct {
	Mode       Mode             `yaml:"mode"`
	DataDir    string           `yaml:"data_dir"`
	PublicURL  string           `yaml:"public_url"`
	KEK        KEKConfig        `yaml:"kek"`
	HTTP       HTTPConfig       `yaml:"http"`
	Management ManagementConfig `yaml:"management"`
	Proxy      ProxyConfig      `yaml:"proxy"`
	Log        LogConfig        `yaml:"log"`
	Storage    StorageConfig    `yaml:"storage"`
	Auth       AuthConfig       `yaml:"auth"`
	Pulse      PulseConfig      `yaml:"pulse"`
	Backup     BackupConfig     `yaml:"backup"`
	Outbound   OutboundConfig   `yaml:"outbound"`
	Retention  RetentionConfig  `yaml:"retention"`
	Limits     LimitsConfig     `yaml:"limits"`
	Sources    SourcesConfig    `yaml:"sources"`
}

// KEKConfig locates the master key-encryption key.
type KEKConfig struct {
	// Source is "file" (default), "credential" (systemd LoadCredential=: File names the credential) or "env"
	// (reads base64 from DEED_KEK; discouraged).
	Source string `yaml:"source"`
	File   string `yaml:"file"`
}

// HTTPConfig configures the public listener.
type HTTPConfig struct {
	Listen      string `yaml:"listen"`
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
	// ClientCAFile, when set with TLS, requires and verifies client certificates (Cloudflare Authenticated Origin
	// Pulls: the Cloudflare origin-pull CA).
	ClientCAFile      string   `yaml:"client_ca_file"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	ReadTimeout       Duration `yaml:"read_timeout"`
	WriteTimeout      Duration `yaml:"write_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	MaxHeaderBytes    int      `yaml:"max_header_bytes"`
	// MaxBodyBytes caps ordinary request bodies; ingest and Terraform state uploads have their own limits.
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
}

// ManagementConfig configures the private management listener (metrics, health, pprof).
type ManagementConfig struct {
	Listen string `yaml:"listen"`
	Pprof  bool   `yaml:"pprof"`
}

// ProxyConfig decides which peers may set client-IP headers.
type ProxyConfig struct {
	// TrustedProxies are CIDRs of reverse proxies whose X-Forwarded-For entries are believed.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// Cloudflare trusts CF-Connecting-IP, but only from CloudflareRanges.
	Cloudflare       bool     `yaml:"cloudflare"`
	CloudflareRanges []string `yaml:"cloudflare_ranges"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	// AuditFile, if set, receives every committed audit record as one ECS JSON line (mode 0600, append). "-" means stdout.
	AuditFile string `yaml:"audit_file"`
	// AccessLog logs every HTTP request at info level (otherwise debug, with 5xx at warn).
	AccessLog bool `yaml:"access_log"`
}

// StorageConfig configures storage safety.
type StorageConfig struct {
	// AllowNetworkFS overrides the network-filesystem refusal. Never set in production.
	AllowNetworkFS        bool `yaml:"allow_network_fs"`
	PreMigrationSnapshots int  `yaml:"pre_migration_snapshots"`
}

// AuthConfig configures authentication.
type AuthConfig struct {
	Local   LocalAuthConfig `yaml:"local"`
	Session SessionConfig   `yaml:"session"`
	OIDC    []OIDCProvider  `yaml:"oidc"`
	// SetupTokenTTL bounds the bootstrap link printed by `deed admin bootstrap`.
	SetupTokenTTL Duration `yaml:"setup_token_ttl"`
	// MaxTokenLifetime bounds API and source token expiry.
	MaxTokenLifetime Duration `yaml:"max_token_lifetime"`
}

// LocalAuthConfig configures local accounts (ADR-005).
type LocalAuthConfig struct {
	Enabled bool `yaml:"enabled"`
	// MinPasswordBytes is the minimum normalized password length.
	MinPasswordBytes int `yaml:"min_password_bytes"`
	// MFARequiredRoles lists installation roles that must enroll TOTP before their first session completes.
	MFARequiredRoles []string `yaml:"mfa_required_roles"`
}

// SessionConfig configures browser sessions.
type SessionConfig struct {
	Idle     Duration `yaml:"idle"`
	Absolute Duration `yaml:"absolute"`
	// ReauthWindow is how recent an authentication must be for sensitive operations.
	ReauthWindow Duration `yaml:"reauth_window"`
}

// OIDCProvider is one SSO provider (YAML-only; lists of objects cannot be set through the environment).
type OIDCProvider struct {
	// Slug identifies the provider in URLs (/auth/oidc/{slug}/start).
	Slug         string `yaml:"slug"`
	DisplayName  string `yaml:"display_name"`
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// ClientSecretFile is preferred over ClientSecret.
	ClientSecretFile string   `yaml:"client_secret_file"`
	Scopes           []string `yaml:"scopes"`
	// GroupsClaim is a dotted JSON path into the ID token / userinfo claims (default "porch.groups").
	GroupsClaim string `yaml:"groups_claim"`
	// TenantClaim and RequiredTenant restrict logins to one tenant (Porch: "porch.tenant.slug").
	TenantClaim          string `yaml:"tenant_claim"`
	RequiredTenant       string `yaml:"required_tenant"`
	RequireEmailVerified bool   `yaml:"require_email_verified"`
	// AllowedAMR, if set, requires one of these authentication methods in the "amr" claim.
	AllowedAMR []string `yaml:"allowed_amr"`
	// JIT creates users on first login when at least one mapping grants a role.
	JIT      bool           `yaml:"jit"`
	Mappings []GroupMapping `yaml:"mappings"`
}

// GroupMapping maps an upstream group to an installation or project role.
type GroupMapping struct {
	Group string `yaml:"group"`
	// InstallationRole is owner, admin or auditor.
	InstallationRole string `yaml:"installation_role"`
	// Project (slug) and ProjectRole (viewer, editor, admin) grant a project role.
	Project     string `yaml:"project"`
	ProjectRole string `yaml:"project_role"`
}

// PulseConfig connects Deed to Pulse (NATS JetStream) for the outbox relay, the ingest consumer and the tail.
type PulseConfig struct {
	Enabled bool `yaml:"enabled"`
	// URL of the Pulse NATS endpoint, e.g. tls://events.example.com:4222.
	URL string `yaml:"url"`
	// CredsFile is the Pulse client .creds file issued for Deed.
	CredsFile string `yaml:"creds_file"`
	CAFile    string `yaml:"ca_file"`
	// Source is the CloudEvents source of published events.
	Source string `yaml:"source"`
	// IngestFilter is the subject filter of the durable ingest consumer.
	IngestFilter  string `yaml:"ingest_filter"`
	IngestDurable string `yaml:"ingest_durable"`
	// Publish disables the outbox relay when false (events stay in the outbox).
	Publish bool `yaml:"publish"`
	// Ingest enables the durable ingest consumer.
	Ingest bool `yaml:"ingest"`
}

// BackupConfig configures encrypted backups to S3-compatible storage (ADR-010).
type BackupConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Interval Duration `yaml:"interval"`
	// Keep* implement grandfather-father-son retention of complete backups.
	KeepDaily   int `yaml:"keep_daily"`
	KeepWeekly  int `yaml:"keep_weekly"`
	KeepMonthly int `yaml:"keep_monthly"`
	// Recipients are age X25519 public keys (age1…). The matching identities stay offline.
	Recipients []string `yaml:"recipients"`
	// ExcludeKEK leaves the KEK out of backups (it must then be kept and restored separately).
	ExcludeKEK bool           `yaml:"exclude_kek"`
	S3         BackupS3Config `yaml:"s3"`
}

// BackupS3Config locates the bucket.
type BackupS3Config struct {
	Endpoint            string `yaml:"endpoint"` // host[:port]
	Bucket              string `yaml:"bucket"`
	Prefix              string `yaml:"prefix"`
	Region              string `yaml:"region"`
	AccessKeyID         string `yaml:"access_key_id"`
	SecretAccessKey     string `yaml:"secret_access_key"`
	SecretAccessKeyFile string `yaml:"secret_access_key_file"`
	// Lookup is auto, path or virtual (bucket addressing style).
	Lookup string `yaml:"lookup"`
	CAFile string `yaml:"ca_file"`
	// Insecure uses plain HTTP (development only).
	Insecure bool `yaml:"insecure"`
	// SSE requests SSE-S3 server-side encryption (defense in depth; default true).
	SSE bool `yaml:"sse"`
}

// OutboundConfig controls outbound connections (SSRF guard, ADR-008).
type OutboundConfig struct {
	// AllowCIDRs are private destinations connectors may reach (LAN Prometheus, MinIO, HallPass…).
	AllowCIDRs []string `yaml:"allow_cidrs"`
	// CAFile adds PEM CA certificates trusted for outbound TLS on top of the system roots.
	CAFile string `yaml:"ca_file"`
}

// RetentionConfig bounds history (Q-05).
type RetentionConfig struct {
	Tombstones      Duration `yaml:"tombstones"`
	ChangeLog       Duration `yaml:"change_log"`
	Outbox          Duration `yaml:"outbox"`
	SourceRuns      Duration `yaml:"source_runs"`
	TelemetryMinute Duration `yaml:"telemetry_minute"`
	TelemetryHour   Duration `yaml:"telemetry_hour"`
	ClosedFindings  Duration `yaml:"closed_findings"`
}

// LimitsConfig bounds requests and queries.
type LimitsConfig struct {
	MaxEntities        int   `yaml:"max_entities"`
	IngestMaxItems     int   `yaml:"ingest_max_items"`
	IngestMaxBytes     int64 `yaml:"ingest_max_bytes"`
	StateMaxBytes      int64 `yaml:"state_max_bytes"`
	GraphMaxDepth      int   `yaml:"graph_max_depth"`
	GraphMaxNodes      int   `yaml:"graph_max_nodes"`
	GraphClusterAbove  int   `yaml:"graph_cluster_above"`
	IngestPerSourceRPM int   `yaml:"ingest_per_source_rpm"`
	// MaxRemovalPercent is the default snapshot mass-removal guard.
	MaxRemovalPercent float64 `yaml:"max_removal_percent"`
}

// SourcesConfig configures the source scheduler.
type SourcesConfig struct {
	// SchedulerEnabled runs pull providers on their intervals.
	SchedulerEnabled bool `yaml:"scheduler_enabled"`
	Concurrency      int  `yaml:"concurrency"`
	// SnapshotTimeout aborts snapshots that were begun but never committed.
	SnapshotTimeout Duration `yaml:"snapshot_timeout"`
}

// Load reads path (optional) and applies environment overrides from environ, then validates.
func Load(path string, environ []string) (*Config, error) {
	cfg := Default()
	if path != "" {
		f, err := os.Open(path) //nolint:gosec // operator-supplied config path
		if err != nil {
			return nil, fmt.Errorf("open config: %w", err)
		}
		defer f.Close()
		if err := decode(f, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := applyEnv(cfg, environ); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func decode(r io.Reader, cfg *Config) error {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("config must contain a single YAML document")
	}
	return nil
}

// IsProduction reports whether the configuration runs in production mode.
func (c *Config) IsProduction() bool { return c.Mode != ModeDevelopment }

// ReadSecret returns inline or the trimmed contents of file (file wins).
func ReadSecret(inline, file string) (string, error) {
	if file == "" {
		return inline, nil
	}
	b, err := os.ReadFile(file) //nolint:gosec // operator-configured secret path
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	return string(bytes.TrimSpace(b)), nil
}
