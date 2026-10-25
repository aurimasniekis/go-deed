// Adapted from Porch (Apache-2.0), which adapted HallPass internal/config/validate.go.

package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CloudflareIPRanges are Cloudflare's published edge ranges (https://www.cloudflare.com/ips/, retrieved 2026-10).
// Operators should refresh them; Deed only trusts CF-Connecting-IP from these peers.
var CloudflareIPRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// Default returns the default configuration (production mode; public_url and kek.file must still be set).
func Default() *Config {
	return &Config{
		Mode:    ModeProduction,
		DataDir: "/var/lib/deed",
		KEK:     KEKConfig{Source: "file"},
		HTTP: HTTPConfig{
			Listen:            ":8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			ReadTimeout:       Duration(60 * time.Second),
			WriteTimeout:      Duration(60 * time.Second),
			IdleTimeout:       Duration(120 * time.Second),
			MaxHeaderBytes:    32 << 10,
			MaxBodyBytes:      1 << 20,
		},
		Management: ManagementConfig{Listen: "127.0.0.1:9464"},
		Proxy:      ProxyConfig{CloudflareRanges: CloudflareIPRanges},
		Log:        LogConfig{Level: "info", Format: "json"},
		Storage:    StorageConfig{PreMigrationSnapshots: 3},
		Auth: AuthConfig{
			Local:            LocalAuthConfig{Enabled: true, MinPasswordBytes: 12, MFARequiredRoles: []string{"owner", "admin"}},
			Session:          SessionConfig{Idle: Duration(30 * time.Minute), Absolute: Duration(12 * time.Hour), ReauthWindow: Duration(5 * time.Minute)},
			SetupTokenTTL:    Duration(30 * time.Minute),
			MaxTokenLifetime: Duration(366 * 24 * time.Hour),
		},
		Pulse: PulseConfig{Source: "deed", IngestFilter: "evt.deed.ingest.>", IngestDurable: "deed_ingest", Publish: true, Ingest: true},
		Backup: BackupConfig{Interval: Duration(6 * time.Hour), KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6,
			S3: BackupS3Config{Prefix: "deed/", Lookup: "auto", SSE: true}},
		Retention: RetentionConfig{
			Tombstones: Duration(30 * 24 * time.Hour), ChangeLog: Duration(365 * 24 * time.Hour), Outbox: Duration(7 * 24 * time.Hour),
			SourceRuns: Duration(30 * 24 * time.Hour), TelemetryMinute: Duration(7 * 24 * time.Hour), TelemetryHour: Duration(90 * 24 * time.Hour),
			ClosedFindings: Duration(90 * 24 * time.Hour),
		},
		Limits: LimitsConfig{MaxEntities: 200000, IngestMaxItems: 1000, IngestMaxBytes: 4 << 20, StateMaxBytes: 64 << 20,
			GraphMaxDepth: 10, GraphMaxNodes: 2000, GraphClusterAbove: 500, IngestPerSourceRPM: 600, MaxRemovalPercent: 25,
			TelemetryTimeout: Duration(10 * time.Second), TelemetryConcurrency: 4, TelemetryCacheEntries: 2000, TelemetryStreams: 100},
		Sources:  SourcesConfig{SchedulerEnabled: true, Concurrency: 4, SnapshotTimeout: Duration(time.Hour)},
		Versions: VersionsConfig{Enabled: true, Interval: Duration(12 * time.Hour), EOLURL: "https://endoflife.date"},
	}
}

// ValidationError aggregates every configuration problem found.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid configuration:\n  - " + strings.Join(e.Problems, "\n  - ")
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Validate checks the configuration, including production-mode restrictions.
func (c *Config) Validate() error {
	v := &validator{}
	switch c.Mode {
	case ModeProduction, ModeDevelopment:
	default:
		v.add("mode must be %q or %q, got %q", ModeProduction, ModeDevelopment, c.Mode)
	}
	prod := c.IsProduction()
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		v.add("data_dir must be an absolute path")
	}
	c.validatePublicURL(v, prod)
	c.validateKEK(v, prod)
	v.listen("http.listen", c.HTTP.Listen)
	if (c.HTTP.TLSCertFile == "") != (c.HTTP.TLSKeyFile == "") {
		v.add("http.tls_cert_file and http.tls_key_file must be set together")
	}
	if c.HTTP.ClientCAFile != "" && c.HTTP.TLSCertFile == "" {
		v.add("http.client_ca_file requires TLS (http.tls_cert_file)")
	}
	v.durationRange("http.read_header_timeout", c.HTTP.ReadHeaderTimeout, time.Second, time.Minute)
	v.durationRange("http.read_timeout", c.HTTP.ReadTimeout, time.Second, 10*time.Minute)
	v.durationRange("http.write_timeout", c.HTTP.WriteTimeout, time.Second, 10*time.Minute)
	v.durationRange("http.idle_timeout", c.HTTP.IdleTimeout, time.Second, 30*time.Minute)
	v.positive("http.max_header_bytes", c.HTTP.MaxHeaderBytes)
	if c.HTTP.MaxBodyBytes <= 0 || c.HTTP.MaxBodyBytes > 64<<20 {
		v.add("http.max_body_bytes must be within 1..%d", 64<<20)
	}
	v.listen("management.listen", c.Management.Listen)
	if host, _, err := net.SplitHostPort(c.Management.Listen); err == nil && !IsLoopbackHost(host) && prod && c.Management.Pprof {
		v.add("management.pprof must not be exposed on a non-loopback management.listen in production")
	}
	for _, p := range c.Proxy.TrustedProxies {
		v.prefix("proxy.trusted_proxies", p)
	}
	for _, p := range c.Proxy.CloudflareRanges {
		v.prefix("proxy.cloudflare_ranges", p)
	}
	if c.Proxy.Cloudflare && len(c.Proxy.CloudflareRanges) == 0 {
		v.add("proxy.cloudflare requires proxy.cloudflare_ranges")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		v.add("log.level must be debug, info, warn or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		v.add("log.format must be json or text")
	}
	if prod && c.Storage.AllowNetworkFS {
		v.add("storage.allow_network_fs is development-only")
	}
	v.positive("storage.pre_migration_snapshots", c.Storage.PreMigrationSnapshots)
	c.validateAuth(v, prod)
	c.validatePulse(v, prod)
	c.validateBackup(v)
	for _, p := range c.Outbound.AllowCIDRs {
		v.prefix("outbound.allow_cidrs", p)
	}
	r := c.Retention
	for name, d := range map[string]Duration{"tombstones": r.Tombstones, "change_log": r.ChangeLog, "outbox": r.Outbox,
		"source_runs": r.SourceRuns, "telemetry_minute": r.TelemetryMinute, "telemetry_hour": r.TelemetryHour, "closed_findings": r.ClosedFindings} {
		v.durationRange("retention."+name, d, time.Hour, 10*365*24*time.Hour)
	}
	l := c.Limits
	v.within("limits.max_entities", l.MaxEntities, 100, 5_000_000)
	v.within("limits.ingest_max_items", l.IngestMaxItems, 1, 10_000)
	if l.IngestMaxBytes < 1<<10 || l.IngestMaxBytes > 64<<20 {
		v.add("limits.ingest_max_bytes must be within 1KiB..64MiB")
	}
	if l.StateMaxBytes < 1<<10 || l.StateMaxBytes > 512<<20 {
		v.add("limits.state_max_bytes must be within 1KiB..512MiB")
	}
	v.within("limits.graph_max_depth", l.GraphMaxDepth, 1, 20)
	v.within("limits.graph_max_nodes", l.GraphMaxNodes, 10, 20000)
	v.within("limits.graph_cluster_above", l.GraphClusterAbove, 10, l.GraphMaxNodes)
	v.within("limits.ingest_per_source_rpm", l.IngestPerSourceRPM, 1, 100000)
	if l.MaxRemovalPercent <= 0 || l.MaxRemovalPercent > 100 {
		v.add("limits.max_removal_percent must be within (0, 100]")
	}
	v.durationRange("limits.telemetry_timeout", l.TelemetryTimeout, time.Second, 2*time.Minute)
	v.within("limits.telemetry_concurrency", l.TelemetryConcurrency, 1, 64)
	v.within("limits.telemetry_cache_entries", l.TelemetryCacheEntries, 10, 1_000_000)
	v.within("limits.telemetry_streams", l.TelemetryStreams, 1, 10_000)
	v.within("sources.concurrency", c.Sources.Concurrency, 1, 64)
	if c.Versions.Enabled {
		v.durationRange("versions.interval", c.Versions.Interval, 15*time.Minute, 7*24*time.Hour)
	}
	if c.Versions.EOL {
		if u, err := url.Parse(c.Versions.EOLURL); err != nil || u.Scheme != "https" || u.Host == "" {
			v.add("versions.eol_url must be an https URL")
		}
	}
	v.durationRange("sources.snapshot_timeout", c.Sources.SnapshotTimeout, time.Minute, 24*time.Hour)
	return v.err()
}

func (c *Config) validateAuth(v *validator, prod bool) {
	a := c.Auth
	if a.Local.MinPasswordBytes < 8 || a.Local.MinPasswordBytes > 128 {
		v.add("auth.local.min_password_bytes must be within 8..128")
	}
	for _, r := range a.Local.MFARequiredRoles {
		if r != "owner" && r != "admin" && r != "auditor" {
			v.add("auth.local.mfa_required_roles: unknown role %q", r)
		}
	}
	if prod && !containsAll(a.Local.MFARequiredRoles, "owner", "admin") {
		v.add("auth.local.mfa_required_roles must include owner and admin in production")
	}
	v.durationRange("auth.session.idle", a.Session.Idle, time.Minute, 24*time.Hour)
	v.durationRange("auth.session.absolute", a.Session.Absolute, a.Session.Idle.D(), 30*24*time.Hour)
	v.durationRange("auth.session.reauth_window", a.Session.ReauthWindow, 30*time.Second, 30*time.Minute)
	v.durationRange("auth.setup_token_ttl", a.SetupTokenTTL, time.Minute, 24*time.Hour)
	v.durationRange("auth.max_token_lifetime", a.MaxTokenLifetime, time.Hour, 366*24*time.Hour)
	if !a.Local.Enabled && len(a.OIDC) == 0 {
		v.add("auth: local accounts are disabled and no OIDC provider is configured; nobody could sign in")
	}
	seen := map[string]bool{}
	for i, p := range a.OIDC {
		n := fmt.Sprintf("auth.oidc[%d]", i)
		if !slugRE.MatchString(p.Slug) {
			v.add("%s.slug must be a lowercase slug", n)
		}
		if seen[p.Slug] {
			v.add("%s.slug %q is duplicated", n, p.Slug)
		}
		seen[p.Slug] = true
		u, err := url.Parse(p.Issuer)
		switch {
		case err != nil || u.Host == "":
			v.add("%s.issuer must be an absolute URL", n)
		case u.Scheme != "https" && (prod || !IsLoopbackHost(u.Hostname())):
			v.add("%s.issuer must use https (http only for localhost in development)", n)
		}
		if p.ClientID == "" {
			v.add("%s.client_id is required", n)
		}
		if p.ClientSecret != "" && prod {
			v.add("%s.client_secret must be provided through client_secret_file in production", n)
		}
		if (p.RequiredTenant == "") != (p.TenantClaim == "") && p.RequiredTenant != "" {
			v.add("%s.required_tenant needs tenant_claim", n)
		}
		for j, m := range p.Mappings {
			mn := fmt.Sprintf("%s.mappings[%d]", n, j)
			if m.Group == "" {
				v.add("%s.group is required", mn)
			}
			switch m.InstallationRole {
			case "", "owner", "admin", "auditor":
			default:
				v.add("%s.installation_role must be owner, admin or auditor", mn)
			}
			if (m.Project == "") != (m.ProjectRole == "") {
				v.add("%s: project and project_role must be set together", mn)
			}
			switch m.ProjectRole {
			case "", "viewer", "editor", "admin":
			default:
				v.add("%s.project_role must be viewer, editor or admin", mn)
			}
			if m.InstallationRole == "" && m.ProjectRole == "" {
				v.add("%s grants nothing", mn)
			}
		}
	}
}

func (c *Config) validatePulse(v *validator, prod bool) {
	p := c.Pulse
	if !p.Enabled {
		return
	}
	if !strings.HasPrefix(p.URL, "tls://") && !strings.HasPrefix(p.URL, "nats://") {
		v.add("pulse.url must be a nats:// or tls:// URL")
	}
	if strings.HasPrefix(p.URL, "nats://") && prod {
		v.add("pulse.url must use tls:// in production mode")
	}
	// Development mode may connect to a local, unauthenticated NATS (deploy/compose.dev.yaml); production needs the
	// client credentials Pulse issued for Deed.
	if p.CredsFile == "" && prod {
		v.add("pulse.creds_file is required in production mode")
	}
	if !slugRE.MatchString(p.Source) {
		v.add("pulse.source must be a lowercase slug")
	}
	// The token at the filter's first wildcard names the publisher (evt.deed.ingest.<pulse_source>.…), so a filter
	// without a wildcard could never accept an event.
	if !strings.HasPrefix(p.IngestFilter, "evt.") || !strings.Contains("."+p.IngestFilter+".", ".>.") && !strings.Contains("."+p.IngestFilter+".", ".*.") {
		v.add("pulse.ingest_filter must be an evt.* subject filter with a wildcard for the publisher token, e.g. evt.deed.ingest.>")
	}
	// Pulse's durable grant names: ^[a-z0-9][a-z0-9_-]{0,47}$ (the consumer is "<client-id>_<name>").
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,47}$`).MatchString(p.IngestDurable) {
		v.add("pulse.ingest_durable must match ^[a-z0-9][a-z0-9_]{0,47}$ (a Pulse durable grant name)")
	}
}

func (c *Config) validatePublicURL(v *validator, prod bool) {
	if c.PublicURL == "" {
		v.add("public_url is required (the externally reachable https URL; used for Origin checks and OIDC callbacks)")
		return
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		v.add("public_url must be an absolute URL without credentials, query or fragment")
		return
	}
	if u.Path != "" && u.Path != "/" {
		v.add("public_url must not contain a path (serve Deed at the origin root)")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if prod {
			v.add("public_url must use https in production")
		} else if !IsLoopbackHost(u.Hostname()) {
			v.add("public_url may use http only for localhost in development mode")
		}
	default:
		v.add("public_url scheme must be https")
	}
}

func (c *Config) validateKEK(v *validator, prod bool) {
	switch c.KEK.Source {
	case "file":
		if c.KEK.File == "" {
			if prod {
				v.add("kek.file is required in production (generate one with `deed keys init --out FILE`)")
			}
			return
		}
		if !filepath.IsAbs(c.KEK.File) {
			v.add("kek.file must be an absolute path")
			return
		}
		if prod && isWithin(c.KEK.File, c.DataDir) {
			v.add("kek.file must be stored outside data_dir in production")
		}
	case "env":
	case "credential":
		if c.KEK.File == "" || strings.ContainsAny(c.KEK.File, "/\\") {
			v.add("kek.file must name the systemd credential (e.g. deed-kek) when kek.source is credential")
		}
	default:
		v.add("kek.source must be file, env or credential")
	}
}

func (c *Config) validateBackup(v *validator) {
	b := c.Backup
	if !b.Enabled {
		return
	}
	v.durationRange("backup.interval", b.Interval, 15*time.Minute, 7*24*time.Hour)
	if b.KeepDaily < 1 || b.KeepDaily > 365 || b.KeepWeekly < 0 || b.KeepWeekly > 520 || b.KeepMonthly < 0 || b.KeepMonthly > 240 {
		v.add("backup.keep_daily (1..365), keep_weekly (0..520) and keep_monthly (0..240) are out of range")
	}
	if len(b.Recipients) == 0 {
		v.add("backup.recipients: at least one age recipient (age1…) is required")
	}
	for _, r := range b.Recipients {
		if !strings.HasPrefix(strings.TrimSpace(r), "age1") {
			v.add("backup.recipients: %q is not an age X25519 recipient", r)
		}
	}
	s := b.S3
	if s.Endpoint == "" || strings.Contains(s.Endpoint, "/") {
		v.add("backup.s3.endpoint must be host[:port]")
	}
	if s.Bucket == "" {
		v.add("backup.s3.bucket is required")
	}
	if s.Prefix != "" && !strings.HasSuffix(s.Prefix, "/") {
		v.add("backup.s3.prefix must end with /")
	}
	if s.AccessKeyID == "" || (s.SecretAccessKey == "" && s.SecretAccessKeyFile == "") {
		v.add("backup.s3: access_key_id and secret_access_key (or secret_access_key_file) are required")
	}
	switch s.Lookup {
	case "", "auto", "path", "virtual":
	default:
		v.add("backup.s3.lookup must be auto, path or virtual")
	}
	if s.Insecure && c.IsProduction() {
		v.add("backup.s3.insecure (plain HTTP) is not allowed in production mode")
	}
}

// PublicOrigin returns scheme://host of public_url without a trailing slash.
func (c *Config) PublicOrigin() string { return strings.TrimSuffix(c.PublicURL, "/") }

// IsLoopbackHost reports whether host is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range list {
			if l == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type validator struct{ problems []string }

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) positive(name string, n int) {
	if n <= 0 {
		v.add("%s must be positive", name)
	}
}

func (v *validator) within(name string, n, lo, hi int) {
	if n < lo || n > hi {
		v.add("%s must be within %d..%d, got %d", name, lo, hi, n)
	}
}

func (v *validator) durationRange(name string, d Duration, lo, hi time.Duration) {
	if d.D() < lo || d.D() > hi {
		v.add("%s must be within %s..%s, got %s", name, lo, hi, d.D())
	}
}

func (v *validator) listen(name, addr string) {
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
		v.add("%s must be host:port, got %q", name, addr)
	}
}

func (v *validator) prefix(name, s string) {
	if _, err := netip.ParsePrefix(s); err != nil {
		v.add("%s entry %q is not a CIDR prefix", name, s)
	}
}

func (v *validator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}
