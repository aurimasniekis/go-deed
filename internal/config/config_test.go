package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// minimalYAML is the smallest valid production configuration.
const minimalYAML = `
public_url: https://deed.example.com
kek:
  file: /etc/deed/kek
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "deed.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// valid returns the default configuration completed with the two settings production requires.
func valid() *Config {
	c := Default()
	c.PublicURL = "https://deed.example.com"
	c.KEK.File = "/etc/deed/kek"
	return c
}

func dev() *Config {
	c := valid()
	c.Mode = ModeDevelopment
	return c
}

func TestDefaults(t *testing.T) {
	d := Default()
	if !d.IsProduction() || d.Mode != ModeProduction {
		t.Fatal("default mode must be production")
	}
	// Default alone is incomplete: production needs public_url and kek.file.
	err := d.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatalf("Default().Validate() = %v, want exactly the public_url and kek.file problems", err)
	}
	if !strings.Contains(err.Error(), "public_url is required") || !strings.Contains(err.Error(), "kek.file is required") {
		t.Fatalf("unexpected problems: %v", err)
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("defaults plus public_url and kek.file must validate: %v", err)
	}
	// Security-relevant defaults.
	checks := []struct {
		name string
		ok   bool
	}{
		{"management on loopback", d.Management.Listen == "127.0.0.1:9464"},
		{"pprof off", !d.Management.Pprof},
		{"kek from file", d.KEK.Source == "file"},
		{"mfa for owner and admin", slices.Equal(d.Auth.Local.MFARequiredRoles, []string{"owner", "admin"})},
		{"no trusted proxies", len(d.Proxy.TrustedProxies) == 0},
		{"cloudflare off", !d.Proxy.Cloudflare},
		{"cloudflare ranges present", len(d.Proxy.CloudflareRanges) > 0},
		{"no outbound private cidrs", len(d.Outbound.AllowCIDRs) == 0},
		{"no network fs", !d.Storage.AllowNetworkFS},
		{"backup sse", d.Backup.S3.SSE},
		{"backup secure", !d.Backup.S3.Insecure},
		{"eol off", !d.Versions.EOL},
		{"local auth on", d.Auth.Local.Enabled},
		{"min password 12", d.Auth.Local.MinPasswordBytes == 12},
		{"session idle 30m", d.Auth.Session.Idle.D() == 30*time.Minute},
		{"log json info", d.Log.Format == "json" && d.Log.Level == "info"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("default: %s", c.name)
		}
	}
	// Default returns a fresh value each time.
	a, b := Default(), Default()
	a.Auth.Local.MFARequiredRoles[0] = "auditor"
	if b.Auth.Local.MFARequiredRoles[0] != "owner" {
		t.Fatal("Default shares slices between calls")
	}
}

func TestIsProduction(t *testing.T) {
	for mode, want := range map[Mode]bool{ModeProduction: true, ModeDevelopment: false, "": true, "dev": true, "Development": true} {
		if got := (&Config{Mode: mode}).IsProduction(); got != want {
			t.Errorf("IsProduction(%q) = %v, want %v", mode, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		base func() *Config
		mut  func(*Config)
		want string // "" means valid
	}{
		// Mode.
		{"mode development", valid, func(c *Config) { c.Mode = ModeDevelopment }, ""},
		{"mode unknown", valid, func(c *Config) { c.Mode = "staging" }, "mode must be"},
		{"mode empty", valid, func(c *Config) { c.Mode = "" }, "mode must be"},

		// data_dir.
		{"data_dir relative", valid, func(c *Config) { c.DataDir = "data" }, "data_dir must be an absolute path"},
		{"data_dir empty", valid, func(c *Config) { c.DataDir = "" }, "data_dir must be an absolute path"},

		// public_url.
		{"public_url trailing slash", valid, func(c *Config) { c.PublicURL = "https://deed.example.com/" }, ""},
		{"public_url port", valid, func(c *Config) { c.PublicURL = "https://deed.example.com:8443" }, ""},
		{"public_url missing", valid, func(c *Config) { c.PublicURL = "" }, "public_url is required"},
		{"public_url relative", valid, func(c *Config) { c.PublicURL = "/deed" }, "public_url must be an absolute URL"},
		{"public_url credentials", valid, func(c *Config) { c.PublicURL = "https://user:pass@deed.example.com" }, "without credentials"},
		{"public_url query", valid, func(c *Config) { c.PublicURL = "https://deed.example.com?x=1" }, "without credentials, query"},
		{"public_url fragment", valid, func(c *Config) { c.PublicURL = "https://deed.example.com#x" }, "without credentials, query or fragment"},
		{"public_url path", valid, func(c *Config) { c.PublicURL = "https://deed.example.com/deed" }, "must not contain a path"},
		{"public_url http in production", valid, func(c *Config) { c.PublicURL = "http://deed.example.com" }, "public_url must use https in production"},
		{"public_url http localhost in production", valid, func(c *Config) { c.PublicURL = "http://localhost:8080" }, "public_url must use https in production"},
		{"public_url http localhost in development", dev, func(c *Config) { c.PublicURL = "http://localhost:8080" }, ""},
		{"public_url http loopback v4 in development", dev, func(c *Config) { c.PublicURL = "http://127.0.0.1:8080" }, ""},
		{"public_url http loopback v6 in development", dev, func(c *Config) { c.PublicURL = "http://[::1]:8080" }, ""},
		{"public_url http lan in development", dev, func(c *Config) { c.PublicURL = "http://192.168.1.10:8080" }, "http only for localhost"},
		{"public_url other scheme", valid, func(c *Config) { c.PublicURL = "ftp://deed.example.com" }, "public_url scheme must be https"},

		// KEK.
		{"kek file missing in production", valid, func(c *Config) { c.KEK.File = "" }, "kek.file is required in production"},
		{"kek file missing in development", dev, func(c *Config) { c.KEK.File = "" }, ""},
		{"kek file relative", valid, func(c *Config) { c.KEK.File = "kek" }, "kek.file must be an absolute path"},
		{"kek file inside data_dir", valid, func(c *Config) { c.KEK.File = "/var/lib/deed/kek" }, "outside data_dir"},
		{"kek file deep inside data_dir", valid, func(c *Config) { c.KEK.File = "/var/lib/deed/keys/kek" }, "outside data_dir"},
		{"kek file is data_dir", valid, func(c *Config) { c.KEK.File = "/var/lib/deed" }, "outside data_dir"},
		{"kek file dotted into data_dir", valid, func(c *Config) { c.KEK.File = "/etc/../var/lib/deed/kek" }, "outside data_dir"},
		{"kek file sibling of data_dir", valid, func(c *Config) { c.KEK.File = "/var/lib/deed-kek" }, ""},
		{"kek file dotted out of data_dir", valid, func(c *Config) { c.KEK.File = "/var/lib/deed/../kek" }, ""},
		{"kek file inside data_dir in development", dev, func(c *Config) { c.KEK.File = "/var/lib/deed/kek" }, ""},
		{"kek source env", valid, func(c *Config) { c.KEK = KEKConfig{Source: "env"} }, ""},
		{"kek source credential", valid, func(c *Config) { c.KEK = KEKConfig{Source: "credential", File: "deed-kek"} }, ""},
		{"kek credential missing name", valid, func(c *Config) { c.KEK = KEKConfig{Source: "credential"} }, "must name the systemd credential"},
		{"kek credential path", valid, func(c *Config) { c.KEK = KEKConfig{Source: "credential", File: "/run/deed-kek"} }, "must name the systemd credential"},
		{"kek credential backslash", valid, func(c *Config) { c.KEK = KEKConfig{Source: "credential", File: `a\b`} }, "must name the systemd credential"},
		{"kek source unknown", valid, func(c *Config) { c.KEK.Source = "vault" }, "kek.source must be file, env or credential"},
		{"kek source empty", valid, func(c *Config) { c.KEK.Source = "" }, "kek.source must be file, env or credential"},

		// HTTP listener.
		{"http listen host port", valid, func(c *Config) { c.HTTP.Listen = "127.0.0.1:8080" }, ""},
		{"http listen no port", valid, func(c *Config) { c.HTTP.Listen = "127.0.0.1" }, "http.listen must be host:port"},
		{"http listen empty port", valid, func(c *Config) { c.HTTP.Listen = "127.0.0.1:" }, "http.listen must be host:port"},
		{"http listen empty", valid, func(c *Config) { c.HTTP.Listen = "" }, "http.listen must be host:port"},
		{"http tls pair", valid, func(c *Config) { c.HTTP.TLSCertFile, c.HTTP.TLSKeyFile = "/etc/deed/c.pem", "/etc/deed/k.pem" }, ""},
		{"http tls cert only", valid, func(c *Config) { c.HTTP.TLSCertFile = "/etc/deed/c.pem" }, "must be set together"},
		{"http tls key only", valid, func(c *Config) { c.HTTP.TLSKeyFile = "/etc/deed/k.pem" }, "must be set together"},
		{"http client ca without tls", valid, func(c *Config) { c.HTTP.ClientCAFile = "/etc/deed/ca.pem" }, "client_ca_file requires TLS"},
		{"http client ca with tls", valid, func(c *Config) {
			c.HTTP.TLSCertFile, c.HTTP.TLSKeyFile, c.HTTP.ClientCAFile = "/etc/deed/c.pem", "/etc/deed/k.pem", "/etc/deed/ca.pem"
		}, ""},
		{"http read header timeout low", valid, func(c *Config) { c.HTTP.ReadHeaderTimeout = 0 }, "http.read_header_timeout must be within"},
		{"http read header timeout high", valid, func(c *Config) { c.HTTP.ReadHeaderTimeout = Duration(2 * time.Minute) }, "http.read_header_timeout must be within"},
		{"http read timeout high", valid, func(c *Config) { c.HTTP.ReadTimeout = Duration(11 * time.Minute) }, "http.read_timeout must be within"},
		{"http write timeout low", valid, func(c *Config) { c.HTTP.WriteTimeout = Duration(time.Millisecond) }, "http.write_timeout must be within"},
		{"http idle timeout high", valid, func(c *Config) { c.HTTP.IdleTimeout = Duration(time.Hour) }, "http.idle_timeout must be within"},
		{"http max header bytes zero", valid, func(c *Config) { c.HTTP.MaxHeaderBytes = 0 }, "http.max_header_bytes must be positive"},
		{"http max body bytes zero", valid, func(c *Config) { c.HTTP.MaxBodyBytes = 0 }, "http.max_body_bytes must be within"},
		{"http max body bytes at cap", valid, func(c *Config) { c.HTTP.MaxBodyBytes = 64 << 20 }, ""},
		{"http max body bytes over cap", valid, func(c *Config) { c.HTTP.MaxBodyBytes = 64<<20 + 1 }, "http.max_body_bytes must be within"},

		// Management listener.
		{"management listen bad", valid, func(c *Config) { c.Management.Listen = "9464" }, "management.listen must be host:port"},
		{"pprof on loopback", valid, func(c *Config) { c.Management.Pprof = true }, ""},
		{"pprof on localhost", valid, func(c *Config) { c.Management.Listen, c.Management.Pprof = "localhost:9464", true }, ""},
		{"pprof on loopback v6", valid, func(c *Config) { c.Management.Listen, c.Management.Pprof = "[::1]:9464", true }, ""},
		{"pprof on all interfaces", valid, func(c *Config) { c.Management.Listen, c.Management.Pprof = ":9464", true }, "management.pprof must not be exposed"},
		{"pprof on lan", valid, func(c *Config) { c.Management.Listen, c.Management.Pprof = "10.0.0.5:9464", true }, "management.pprof must not be exposed"},
		{"pprof on all interfaces in development", dev, func(c *Config) { c.Management.Listen, c.Management.Pprof = "0.0.0.0:9464", true }, ""},
		{"management on lan without pprof", valid, func(c *Config) { c.Management.Listen = "10.0.0.5:9464" }, ""},

		// Proxies.
		{"trusted proxies", valid, func(c *Config) { c.Proxy.TrustedProxies = []string{"10.0.0.0/8", "fd00::/8"} }, ""},
		{"trusted proxy bare ip", valid, func(c *Config) { c.Proxy.TrustedProxies = []string{"10.0.0.1"} }, "proxy.trusted_proxies entry \"10.0.0.1\" is not a CIDR prefix"},
		{"cloudflare range bad", valid, func(c *Config) { c.Proxy.CloudflareRanges = []string{"nope"} }, "proxy.cloudflare_ranges entry"},
		{"cloudflare enabled", valid, func(c *Config) { c.Proxy.Cloudflare = true }, ""},
		{"cloudflare without ranges", valid, func(c *Config) { c.Proxy.Cloudflare, c.Proxy.CloudflareRanges = true, nil }, "proxy.cloudflare requires proxy.cloudflare_ranges"},

		// Logging.
		{"log level debug", valid, func(c *Config) { c.Log.Level = "debug" }, ""},
		{"log level unknown", valid, func(c *Config) { c.Log.Level = "trace" }, "log.level must be"},
		{"log level uppercase", valid, func(c *Config) { c.Log.Level = "INFO" }, "log.level must be"},
		{"log format text", valid, func(c *Config) { c.Log.Format = "text" }, ""},
		{"log format unknown", valid, func(c *Config) { c.Log.Format = "logfmt" }, "log.format must be json or text"},

		// Storage.
		{"network fs in production", valid, func(c *Config) { c.Storage.AllowNetworkFS = true }, "storage.allow_network_fs is development-only"},
		{"network fs in development", dev, func(c *Config) { c.Storage.AllowNetworkFS = true }, ""},
		{"pre-migration snapshots zero", valid, func(c *Config) { c.Storage.PreMigrationSnapshots = 0 }, "storage.pre_migration_snapshots must be positive"},

		// Local auth and sessions.
		{"min password 8", valid, func(c *Config) { c.Auth.Local.MinPasswordBytes = 8 }, ""},
		{"min password 7", valid, func(c *Config) { c.Auth.Local.MinPasswordBytes = 7 }, "auth.local.min_password_bytes must be within 8..128"},
		{"min password 129", valid, func(c *Config) { c.Auth.Local.MinPasswordBytes = 129 }, "auth.local.min_password_bytes must be within 8..128"},
		{"mfa auditor too", valid, func(c *Config) { c.Auth.Local.MFARequiredRoles = []string{"owner", "admin", "auditor"} }, ""},
		{"mfa unknown role", valid, func(c *Config) { c.Auth.Local.MFARequiredRoles = []string{"owner", "admin", "viewer"} }, `unknown role "viewer"`},
		{"mfa without admin in production", valid, func(c *Config) { c.Auth.Local.MFARequiredRoles = []string{"owner"} }, "must include owner and admin in production"},
		{"mfa empty in production", valid, func(c *Config) { c.Auth.Local.MFARequiredRoles = nil }, "must include owner and admin in production"},
		{"mfa empty in development", dev, func(c *Config) { c.Auth.Local.MFARequiredRoles = nil }, ""},
		{"session idle low", valid, func(c *Config) { c.Auth.Session.Idle = Duration(30 * time.Second) }, "auth.session.idle must be within"},
		{"session idle high", valid, func(c *Config) { c.Auth.Session.Idle = Duration(25 * time.Hour) }, "auth.session.idle must be within"},
		{"session absolute below idle", valid, func(c *Config) { c.Auth.Session.Absolute = Duration(10 * time.Minute) }, "auth.session.absolute must be within"},
		{"session absolute high", valid, func(c *Config) { c.Auth.Session.Absolute = Duration(31 * 24 * time.Hour) }, "auth.session.absolute must be within"},
		{"reauth window low", valid, func(c *Config) { c.Auth.Session.ReauthWindow = Duration(time.Second) }, "auth.session.reauth_window must be within"},
		{"setup token ttl high", valid, func(c *Config) { c.Auth.SetupTokenTTL = Duration(48 * time.Hour) }, "auth.setup_token_ttl must be within"},
		{"max token lifetime low", valid, func(c *Config) { c.Auth.MaxTokenLifetime = Duration(time.Minute) }, "auth.max_token_lifetime must be within"},
		{"max token lifetime high", valid, func(c *Config) { c.Auth.MaxTokenLifetime = Duration(367 * 24 * time.Hour) }, "auth.max_token_lifetime must be within"},
		{"nobody can sign in", valid, func(c *Config) { c.Auth.Local.Enabled = false }, "nobody could sign in"},
		{"oidc only", valid, func(c *Config) { c.Auth.Local.Enabled = false; c.Auth.OIDC = []OIDCProvider{oidc()} }, ""},

		// OIDC providers.
		{"oidc slug bad", valid, withOIDC(func(p *OIDCProvider) { p.Slug = "Corp SSO" }), "auth.oidc[0].slug must be a lowercase slug"},
		{"oidc slug empty", valid, withOIDC(func(p *OIDCProvider) { p.Slug = "" }), "auth.oidc[0].slug must be a lowercase slug"},
		{"oidc slug duplicated", valid, func(c *Config) { c.Auth.OIDC = []OIDCProvider{oidc(), oidc()} }, `auth.oidc[1].slug "corp" is duplicated`},
		{"oidc issuer relative", valid, withOIDC(func(p *OIDCProvider) { p.Issuer = "/issuer" }), "auth.oidc[0].issuer must be an absolute URL"},
		{"oidc issuer http in production", valid, withOIDC(func(p *OIDCProvider) { p.Issuer = "http://sso.example.com" }), "auth.oidc[0].issuer must use https"},
		{"oidc issuer http localhost in production", valid, withOIDC(func(p *OIDCProvider) { p.Issuer = "http://localhost:5556" }), "auth.oidc[0].issuer must use https"},
		{"oidc issuer http localhost in development", dev, withOIDC(func(p *OIDCProvider) { p.Issuer = "http://localhost:5556" }), ""},
		{"oidc issuer http lan in development", dev, withOIDC(func(p *OIDCProvider) { p.Issuer = "http://10.0.0.5:5556" }), "auth.oidc[0].issuer must use https"},
		{"oidc client id missing", valid, withOIDC(func(p *OIDCProvider) { p.ClientID = "" }), "auth.oidc[0].client_id is required"},
		{"oidc inline secret in production", valid, withOIDC(func(p *OIDCProvider) { p.ClientSecret = "s3cr3t" }), "client_secret must be provided through client_secret_file"},
		{"oidc inline secret in development", dev, withOIDC(func(p *OIDCProvider) { p.ClientSecret = "s3cr3t" }), ""},
		{"oidc tenant without claim", valid, withOIDC(func(p *OIDCProvider) { p.RequiredTenant = "acme" }), "required_tenant needs tenant_claim"},
		{"oidc tenant with claim", valid, withOIDC(func(p *OIDCProvider) { p.RequiredTenant, p.TenantClaim = "acme", "tenant.slug" }), ""},
		{"oidc claim without tenant", valid, withOIDC(func(p *OIDCProvider) { p.TenantClaim = "tenant.slug" }), ""},
		{"mapping group missing", valid, withOIDC(func(p *OIDCProvider) { p.Mappings[0].Group = "" }), "auth.oidc[0].mappings[0].group is required"},
		{"mapping installation role bad", valid, withOIDC(func(p *OIDCProvider) { p.Mappings[0].InstallationRole = "root" }), "installation_role must be owner, admin or auditor"},
		{"mapping project without role", valid, withOIDC(func(p *OIDCProvider) { p.Mappings[0].Project = "infra" }), "project and project_role must be set together"},
		{"mapping role without project", valid, withOIDC(func(p *OIDCProvider) { p.Mappings[0].ProjectRole = "viewer" }), "project and project_role must be set together"},
		{"mapping project role bad", valid, withOIDC(func(p *OIDCProvider) {
			p.Mappings[0] = GroupMapping{Group: "g", Project: "infra", ProjectRole: "owner"}
		}), "project_role must be viewer, editor or admin"},
		{"mapping project role", valid, withOIDC(func(p *OIDCProvider) {
			p.Mappings[0] = GroupMapping{Group: "g", Project: "infra", ProjectRole: "editor"}
		}), ""},
		{"mapping grants nothing", valid, withOIDC(func(p *OIDCProvider) { p.Mappings[0].InstallationRole = "" }), "auth.oidc[0].mappings[0] grants nothing"},

		// Pulse.
		{"pulse disabled ignores settings", valid, func(c *Config) { c.Pulse.URL = "bogus" }, ""},
		{"pulse tls", valid, withPulse(nil), ""},
		{"pulse bad scheme", valid, withPulse(func(p *PulseConfig) { p.URL = "https://events.example.com" }), "pulse.url must be a nats:// or tls:// URL"},
		{"pulse plaintext in production", valid, withPulse(func(p *PulseConfig) { p.URL = "nats://events.example.com:4222" }), "pulse.url must use tls:// in production"},
		{"pulse plaintext in development", dev, withPulse(func(p *PulseConfig) { p.URL, p.CredsFile = "nats://127.0.0.1:4222", "" }), ""},
		{"pulse creds missing in production", valid, withPulse(func(p *PulseConfig) { p.CredsFile = "" }), "pulse.creds_file is required in production"},
		{"pulse source bad", valid, withPulse(func(p *PulseConfig) { p.Source = "Deed!" }), "pulse.source must be a lowercase slug"},
		{"pulse ingest filter bad", valid, withPulse(func(p *PulseConfig) { p.IngestFilter = ">" }), "pulse.ingest_filter must be an evt.* subject filter"},
		{"pulse durable bad", valid, withPulse(func(p *PulseConfig) { p.IngestDurable = "deed-ingest" }), "pulse.ingest_durable must match"},

		// Backups.
		{"backup disabled ignores settings", valid, func(c *Config) { c.Backup.S3.Endpoint = "https://s3.example.com/" }, ""},
		{"backup enabled", valid, withBackup(nil), ""},
		{"backup secret file", valid, withBackup(func(b *BackupConfig) { b.S3.SecretAccessKey, b.S3.SecretAccessKeyFile = "", "/etc/deed/s3" }), ""},
		{"backup interval low", valid, withBackup(func(b *BackupConfig) { b.Interval = Duration(time.Minute) }), "backup.interval must be within"},
		{"backup keep daily zero", valid, withBackup(func(b *BackupConfig) { b.KeepDaily = 0 }), "backup.keep_daily"},
		{"backup keep weekly negative", valid, withBackup(func(b *BackupConfig) { b.KeepWeekly = -1 }), "backup.keep_daily"},
		{"backup keep monthly high", valid, withBackup(func(b *BackupConfig) { b.KeepMonthly = 241 }), "backup.keep_daily"},
		{"backup no recipients", valid, withBackup(func(b *BackupConfig) { b.Recipients = nil }), "at least one age recipient"},
		{"backup bad recipient", valid, withBackup(func(b *BackupConfig) { b.Recipients = []string{"ssh-ed25519 AAAA"} }), "is not an age X25519 recipient"},
		{"backup endpoint missing", valid, withBackup(func(b *BackupConfig) { b.S3.Endpoint = "" }), "backup.s3.endpoint must be host[:port]"},
		{"backup endpoint url", valid, withBackup(func(b *BackupConfig) { b.S3.Endpoint = "https://s3.example.com" }), "backup.s3.endpoint must be host[:port]"},
		{"backup bucket missing", valid, withBackup(func(b *BackupConfig) { b.S3.Bucket = "" }), "backup.s3.bucket is required"},
		{"backup prefix without slash", valid, withBackup(func(b *BackupConfig) { b.S3.Prefix = "deed" }), "backup.s3.prefix must end with /"},
		{"backup prefix empty", valid, withBackup(func(b *BackupConfig) { b.S3.Prefix = "" }), ""},
		{"backup access key missing", valid, withBackup(func(b *BackupConfig) { b.S3.AccessKeyID = "" }), "access_key_id and secret_access_key"},
		{"backup secret missing", valid, withBackup(func(b *BackupConfig) { b.S3.SecretAccessKey = "" }), "access_key_id and secret_access_key"},
		{"backup lookup bad", valid, withBackup(func(b *BackupConfig) { b.S3.Lookup = "dns" }), "backup.s3.lookup must be auto, path or virtual"},
		{"backup insecure in production", valid, withBackup(func(b *BackupConfig) { b.S3.Insecure = true }), "backup.s3.insecure (plain HTTP) is not allowed in production"},
		{"backup insecure in development", dev, withBackup(func(b *BackupConfig) { b.S3.Insecure = true }), ""},

		// Outbound.
		{"outbound cidrs", valid, func(c *Config) { c.Outbound.AllowCIDRs = []string{"10.0.0.0/8", "192.168.1.0/24"} }, ""},
		{"outbound cidr bad", valid, func(c *Config) { c.Outbound.AllowCIDRs = []string{"192.168.1.1"} }, "outbound.allow_cidrs entry"},

		// Retention.
		{"retention low", valid, func(c *Config) { c.Retention.Outbox = Duration(time.Minute) }, "retention.outbox must be within"},
		{"retention high", valid, func(c *Config) { c.Retention.ChangeLog = Duration(11 * 365 * 24 * time.Hour) }, "retention.change_log must be within"},
		{"retention zero", valid, func(c *Config) { c.Retention.ClosedFindings = 0 }, "retention.closed_findings must be within"},

		// Limits.
		{"max entities low", valid, func(c *Config) { c.Limits.MaxEntities = 99 }, "limits.max_entities must be within"},
		{"ingest items high", valid, func(c *Config) { c.Limits.IngestMaxItems = 10_001 }, "limits.ingest_max_items must be within"},
		{"ingest bytes low", valid, func(c *Config) { c.Limits.IngestMaxBytes = 1023 }, "limits.ingest_max_bytes must be within"},
		{"ingest bytes high", valid, func(c *Config) { c.Limits.IngestMaxBytes = 64<<20 + 1 }, "limits.ingest_max_bytes must be within"},
		{"state bytes high", valid, func(c *Config) { c.Limits.StateMaxBytes = 512<<20 + 1 }, "limits.state_max_bytes must be within"},
		{"graph depth high", valid, func(c *Config) { c.Limits.GraphMaxDepth = 21 }, "limits.graph_max_depth must be within"},
		{"graph nodes low", valid, func(c *Config) { c.Limits.GraphMaxNodes = 9 }, "limits.graph_max_nodes must be within"},
		{"graph cluster above max nodes", valid, func(c *Config) { c.Limits.GraphClusterAbove = c.Limits.GraphMaxNodes + 1 }, "limits.graph_cluster_above must be within"},
		{"graph cluster at max nodes", valid, func(c *Config) { c.Limits.GraphClusterAbove = c.Limits.GraphMaxNodes }, ""},
		{"ingest rpm zero", valid, func(c *Config) { c.Limits.IngestPerSourceRPM = 0 }, "limits.ingest_per_source_rpm must be within"},
		{"removal percent zero", valid, func(c *Config) { c.Limits.MaxRemovalPercent = 0 }, "limits.max_removal_percent must be within"},
		{"removal percent 100", valid, func(c *Config) { c.Limits.MaxRemovalPercent = 100 }, ""},
		{"removal percent over", valid, func(c *Config) { c.Limits.MaxRemovalPercent = 100.5 }, "limits.max_removal_percent must be within"},
		{"telemetry timeout high", valid, func(c *Config) { c.Limits.TelemetryTimeout = Duration(3 * time.Minute) }, "limits.telemetry_timeout must be within"},
		{"telemetry concurrency high", valid, func(c *Config) { c.Limits.TelemetryConcurrency = 65 }, "limits.telemetry_concurrency must be within"},
		{"telemetry cache low", valid, func(c *Config) { c.Limits.TelemetryCacheEntries = 9 }, "limits.telemetry_cache_entries must be within"},
		{"telemetry streams zero", valid, func(c *Config) { c.Limits.TelemetryStreams = 0 }, "limits.telemetry_streams must be within"},

		// Sources and versions.
		{"sources concurrency zero", valid, func(c *Config) { c.Sources.Concurrency = 0 }, "sources.concurrency must be within"},
		{"snapshot timeout low", valid, func(c *Config) { c.Sources.SnapshotTimeout = Duration(time.Second) }, "sources.snapshot_timeout must be within"},
		{"versions interval low", valid, func(c *Config) { c.Versions.Interval = Duration(time.Minute) }, "versions.interval must be within"},
		{"versions disabled ignores interval", valid, func(c *Config) { c.Versions.Enabled, c.Versions.Interval = false, 0 }, ""},
		{"eol default url", valid, func(c *Config) { c.Versions.EOL = true }, ""},
		{"eol http url", valid, func(c *Config) { c.Versions.EOL, c.Versions.EOLURL = true, "http://eol.example.com" }, "versions.eol_url must be an https URL"},
		{"eol empty url", valid, func(c *Config) { c.Versions.EOL, c.Versions.EOLURL = true, "" }, "versions.eol_url must be an https URL"},
		{"eol disabled ignores url", valid, func(c *Config) { c.Versions.EOLURL = "http://eol.example.com" }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.base()
			c.mut(cfg)
			err := cfg.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a *ValidationError containing %q", err, c.want)
			}
			if !slices.ContainsFunc(ve.Problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("problems %q do not mention %q", ve.Problems, c.want)
			}
		})
	}
}

func oidc() OIDCProvider {
	return OIDCProvider{
		Slug:             "corp",
		Issuer:           "https://sso.example.com",
		ClientID:         "deed",
		ClientSecretFile: "/etc/deed/oidc-secret",
		Mappings:         []GroupMapping{{Group: "deed-admins", InstallationRole: "admin"}},
	}
}

func withOIDC(mut func(*OIDCProvider)) func(*Config) {
	return func(c *Config) {
		p := oidc()
		mut(&p)
		c.Auth.OIDC = []OIDCProvider{p}
	}
}

func withPulse(mut func(*PulseConfig)) func(*Config) {
	return func(c *Config) {
		c.Pulse.Enabled = true
		c.Pulse.URL = "tls://events.example.com:4222"
		c.Pulse.CredsFile = "/etc/deed/pulse.creds"
		if mut != nil {
			mut(&c.Pulse)
		}
	}
}

func withBackup(mut func(*BackupConfig)) func(*Config) {
	return func(c *Config) {
		b := &c.Backup
		b.Enabled = true
		b.Recipients = []string{"age1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs3290gq"}
		b.S3.Endpoint = "s3.example.com"
		b.S3.Bucket = "deed-backups"
		b.S3.AccessKeyID = "AKIDEXAMPLE"
		b.S3.SecretAccessKey = "example-secret"
		if mut != nil {
			mut(b)
		}
	}
}

func TestValidationErrorAggregates(t *testing.T) {
	c := Default()
	c.Mode = "staging"
	c.DataDir = "relative"
	c.Log.Level = "loud"
	err := c.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	// An unknown mode is treated as production, so kek.file is still required.
	for _, want := range []string{"mode must be", "data_dir", "log.level", "public_url is required", "kek.file is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if !strings.HasPrefix(err.Error(), "invalid configuration:\n  - ") || strings.Count(err.Error(), "\n  - ") != len(ve.Problems) {
		t.Fatalf("unexpected rendering:\n%v", err)
	}
}

func TestLoadYAML(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  string // error substring; "" means success
		check func(*testing.T, *Config)
	}{
		{"minimal", minimalYAML, "", func(t *testing.T, c *Config) {
			if c.PublicURL != "https://deed.example.com" || c.KEK.File != "/etc/deed/kek" || c.HTTP.Listen != ":8080" {
				t.Fatalf("unexpected config: %+v", c)
			}
		}},
		{"durations", minimalYAML + `
auth:
  session:
    idle: 15m
    absolute: 8h
http:
  read_timeout: 90s
proxy:
  trusted_proxies: [10.0.0.0/8]
`, "", func(t *testing.T, c *Config) {
			if c.Auth.Session.Idle.D() != 15*time.Minute || c.Auth.Session.Absolute.D() != 8*time.Hour ||
				c.HTTP.ReadTimeout.D() != 90*time.Second || !slices.Equal(c.Proxy.TrustedProxies, []string{"10.0.0.0/8"}) {
				t.Fatalf("unexpected config: %+v", c)
			}
			// Unset siblings keep their defaults.
			if c.Auth.Session.ReauthWindow.D() != 5*time.Minute || c.HTTP.WriteTimeout.D() != time.Minute {
				t.Fatal("defaults lost for unset keys")
			}
		}},
		{"oidc list", minimalYAML + `
auth:
  oidc:
    - slug: corp
      issuer: https://sso.example.com
      client_id: deed
      client_secret_file: /etc/deed/oidc
      mappings:
        - group: admins
          installation_role: admin
`, "", func(t *testing.T, c *Config) {
			if len(c.Auth.OIDC) != 1 || c.Auth.OIDC[0].Mappings[0].InstallationRole != "admin" {
				t.Fatalf("oidc = %+v", c.Auth.OIDC)
			}
		}},
		{"unknown top-level key", minimalYAML + "listen: :80\n", "field listen not found", nil},
		{"unknown nested key", minimalYAML + "http:\n  lsiten: :80\n", "field lsiten not found", nil},
		{"unknown oidc key", minimalYAML + "auth:\n  oidc:\n    - slug: corp\n      secret: x\n", "field secret not found", nil},
		{"duplicate key", minimalYAML + "public_url: https://other.example.com\n", "already defined", nil},
		{"two documents", minimalYAML + "---\nmode: development\n", "single YAML document", nil},
		{"bad duration", minimalYAML + "auth:\n  session:\n    idle: forever\n", "invalid duration", nil},
		{"duration without unit", minimalYAML + "auth:\n  session:\n    idle: 30\n", "missing unit", nil},
		{"duration not scalar", minimalYAML + "auth:\n  session:\n    idle: [1]\n", "duration must be a string", nil},
		{"wrong type", minimalYAML + "http:\n  max_header_bytes: lots\n", "cannot unmarshal", nil},
		{"syntax error", "public_url: [\n", "parse", nil},
		{"invalid values", minimalYAML + "mode: staging\n", "mode must be", nil},
		{"production refusal", "public_url: http://deed.example.com\nkek:\n  file: /etc/deed/kek\n", "public_url must use https in production", nil},
		{"empty file is all defaults", "", "public_url is required", nil},
		{"whitespace only", "\n  \n", "public_url is required", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, c.body), nil)
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want %q", err, c.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.check != nil {
				c.check(t, cfg)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), nil); err == nil || !strings.Contains(err.Error(), "open config") {
		t.Fatalf("err = %v", err)
	}
}

// A configuration larger than the read limit must be refused, not silently truncated.
func TestLoadSizeLimit(t *testing.T) {
	const limit = 1 << 20
	// Exactly at the limit is fine.
	atLimit := minimalYAML + "#" + strings.Repeat("x", limit-len(minimalYAML)-2) + "\n"
	if len(atLimit) != limit {
		t.Fatalf("test setup: %d bytes", len(atLimit))
	}
	if _, err := Load(writeConfig(t, atLimit), nil); err != nil {
		t.Fatalf("config at the limit refused: %v", err)
	}
	// Anything past it is refused, here an invalid mode that truncation would have dropped.
	over := atLimit + "mode: staging\n"
	if _, err := Load(writeConfig(t, over), nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized config: err = %v", err)
	}
}

func TestLoadNoFile(t *testing.T) {
	cfg, err := Load("", []string{"DEED_PUBLIC_URL=https://deed.example.com", "DEED_KEK_FILE=/etc/deed/kek"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://deed.example.com" {
		t.Fatal("env not applied")
	}
	if _, err := Load("", nil); err == nil {
		t.Fatal("bare defaults must not validate in production")
	}
}

func TestEnvOverrides(t *testing.T) {
	base := []string{"DEED_PUBLIC_URL=https://deed.example.com", "DEED_KEK_FILE=/etc/deed/kek"}
	cases := []struct {
		name  string
		env   []string
		check func(*Config) bool
	}{
		{"string", []string{"DEED_HTTP_LISTEN=127.0.0.1:9000"}, func(c *Config) bool { return c.HTTP.Listen == "127.0.0.1:9000" }},
		{"mode", []string{"DEED_MODE=development"}, func(c *Config) bool { return c.Mode == ModeDevelopment }},
		{"nested three deep", []string{"DEED_AUTH_SESSION_IDLE=45m"}, func(c *Config) bool { return c.Auth.Session.Idle.D() == 45*time.Minute }},
		{"bool", []string{"DEED_LOG_ACCESS_LOG=true"}, func(c *Config) bool { return c.Log.AccessLog }},
		{"bool false", []string{"DEED_PULSE_PUBLISH=0"}, func(c *Config) bool { return !c.Pulse.Publish }},
		{"int", []string{"DEED_HTTP_MAX_HEADER_BYTES=65536"}, func(c *Config) bool { return c.HTTP.MaxHeaderBytes == 65536 }},
		{"int64", []string{"DEED_LIMITS_INGEST_MAX_BYTES=2048"}, func(c *Config) bool { return c.Limits.IngestMaxBytes == 2048 }},
		{"float", []string{"DEED_LIMITS_MAX_REMOVAL_PERCENT=12.5"}, func(c *Config) bool { return c.Limits.MaxRemovalPercent == 12.5 }},
		{"list", []string{"DEED_PROXY_TRUSTED_PROXIES= 10.0.0.0/8 , ,192.168.0.0/16 "}, func(c *Config) bool {
			return slices.Equal(c.Proxy.TrustedProxies, []string{"10.0.0.0/8", "192.168.0.0/16"})
		}},
		{"list replaces default", []string{"DEED_AUTH_LOCAL_MFA_REQUIRED_ROLES=owner,admin,auditor"}, func(c *Config) bool {
			return slices.Equal(c.Auth.Local.MFARequiredRoles, []string{"owner", "admin", "auditor"})
		}},
		{"deep backup", []string{"DEED_BACKUP_S3_LOOKUP=path"}, func(c *Config) bool { return c.Backup.S3.Lookup == "path" }},
		{"value with equals", []string{"DEED_VERSIONS_EOL_URL=https://eol.example.com/?a=b"}, func(c *Config) bool {
			return c.Versions.EOLURL == "https://eol.example.com/?a=b"
		}},
		{"non-config deed variables ignored", []string{"DEED_KEK=AAAA", "DEED_CONFIG=/etc/deed/deed.yaml", "DEED_TOKEN=deed_at_x", "DEED_SOURCE_TOKEN=deed_st_x",
			"DEED_ENDPOINT=https://deed.example.com", "DEED_SOURCE=tf", "DEED_SOURCE_ID=1", "DEED_CA_FILE=/x", "DEED_TEST_TX_MAX=1s",
			"DEED_TEST_S3=x", "DEED_TEST_NATS=x", "DEED_ACC_KEEP=1", "DEED_BINARY=/x"}, func(c *Config) bool { return true }},
		{"other prefixes ignored", []string{"HOME=/root", "PORCH_FOO=1", "deed_http_listen=bogus", "XDEED_HTTP_LISTEN=bogus"}, func(c *Config) bool {
			return c.HTTP.Listen == ":8080"
		}},
		{"malformed entry ignored", []string{"DEED_HTTP_LISTEN"}, func(c *Config) bool { return c.HTTP.Listen == ":8080" }},
		{"last duplicate wins", []string{"DEED_HTTP_LISTEN=127.0.0.1:1", "DEED_HTTP_LISTEN=127.0.0.1:2"}, func(c *Config) bool {
			return c.HTTP.Listen == "127.0.0.1:2"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load("", append(slices.Clone(base), c.env...))
			if err != nil {
				t.Fatal(err)
			}
			if !c.check(cfg) {
				t.Fatalf("override not applied: %+v", cfg)
			}
		})
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	p := writeConfig(t, minimalYAML+"http:\n  listen: 127.0.0.1:1000\nlog:\n  level: debug\n")
	cfg, err := Load(p, []string{"DEED_HTTP_LISTEN=127.0.0.1:2000"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:2000" || cfg.Log.Level != "debug" {
		t.Fatalf("listen %q level %q", cfg.HTTP.Listen, cfg.Log.Level)
	}
	// The environment is validated with the file: an override can make a valid file invalid.
	if _, err := Load(p, []string{"DEED_PUBLIC_URL=http://deed.example.com"}); err == nil {
		t.Fatal("env override bypassed validation")
	}
}

func TestEnvErrors(t *testing.T) {
	base := []string{"DEED_PUBLIC_URL=https://deed.example.com", "DEED_KEK_FILE=/etc/deed/kek"}
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"unknown", []string{"DEED_HTTP_LISTN=:80"}, "unknown environment override(s): DEED_HTTP_LISTN"},
		{"unknown sorted", []string{"DEED_ZZZ=1", "DEED_AAA=1"}, "DEED_AAA, DEED_ZZZ"},
		{"struct is not a leaf", []string{"DEED_HTTP=x"}, "unknown environment override(s): DEED_HTTP"},
		{"oidc list is yaml-only", []string{"DEED_AUTH_OIDC=x"}, "unknown environment override(s): DEED_AUTH_OIDC"},
		{"bare prefix", []string{"DEED_=x"}, "unknown environment override(s): DEED_"},
		{"lowercase suffix", []string{"DEED_http_listen=:80"}, "unknown environment override(s): DEED_http_listen"},
		{"bad bool", []string{"DEED_LOG_ACCESS_LOG=yes"}, "environment DEED_LOG_ACCESS_LOG"},
		{"bad int", []string{"DEED_HTTP_MAX_HEADER_BYTES=32k"}, "environment DEED_HTTP_MAX_HEADER_BYTES"},
		{"bad float", []string{"DEED_LIMITS_MAX_REMOVAL_PERCENT=half"}, "environment DEED_LIMITS_MAX_REMOVAL_PERCENT"},
		{"bad duration", []string{"DEED_AUTH_SESSION_IDLE=30"}, "environment DEED_AUTH_SESSION_IDLE"},
		{"invalid value fails validation", []string{"DEED_LOG_LEVEL=loud"}, "log.level must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load("", append(slices.Clone(base), c.env...))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestEnvKeys(t *testing.T) {
	keys := EnvKeys()
	if !sort.StringsAreSorted(keys) {
		t.Fatal("EnvKeys not sorted")
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("two configuration paths map to %s", k)
		}
		seen[k] = true
		if !strings.HasPrefix(k, EnvPrefix) || k != strings.ToUpper(k) {
			t.Fatalf("bad key %s", k)
		}
		if nonConfigEnv[k] {
			t.Fatalf("%s is both a configuration override and a non-configuration variable", k)
		}
	}
	for _, want := range []string{"DEED_MODE", "DEED_PUBLIC_URL", "DEED_KEK_SOURCE", "DEED_KEK_FILE", "DEED_HTTP_LISTEN",
		"DEED_AUTH_SESSION_IDLE", "DEED_BACKUP_S3_SECRET_ACCESS_KEY_FILE", "DEED_OUTBOUND_ALLOW_CIDRS", "DEED_VERSIONS_EOL_URL"} {
		if !seen[want] {
			t.Errorf("EnvKeys lacks %s", want)
		}
	}
	for _, absent := range []string{"DEED_AUTH_OIDC", "DEED_KEK", "DEED_HTTP"} {
		if seen[absent] {
			t.Errorf("EnvKeys includes %s", absent)
		}
	}
	// Every leaf of Config is reachable: count the yaml-tagged leaves independently.
	var count func(reflect.Type) int
	count = func(t reflect.Type) int {
		n := 0
		for i := range t.NumField() {
			f := t.Field(i)
			switch {
			case f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Struct:
			case f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[Duration]():
				n += count(f.Type)
			default:
				n++
			}
		}
		return n
	}
	if got := count(reflect.TypeFor[Config]()); got != len(keys) {
		t.Fatalf("Config has %d leaves, EnvKeys lists %d", got, len(keys))
	}
}

func TestDurationYAML(t *testing.T) {
	var v struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal([]byte("d: 1h30m\n"), &v); err != nil || v.D.D() != 90*time.Minute {
		t.Fatalf("unmarshal: %v %v", v.D.D(), err)
	}
	out, err := yaml.Marshal(v)
	if err != nil || strings.TrimSpace(string(out)) != "d: 1h30m0s" {
		t.Fatalf("marshal: %q %v", out, err)
	}
	if err := yaml.Unmarshal([]byte("d: {a: 1}\n"), &v); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("map duration: %v", err)
	}
}

func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("  from-file\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, inline, file, want string
		err                      bool
	}{
		{"inline only", "inline", "", "inline", false},
		{"empty", "", "", "", false},
		{"file wins and is trimmed", "inline", file, "from-file", false},
		{"missing file", "inline", filepath.Join(dir, "missing"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadSecret(c.inline, c.file)
			if (err != nil) != c.err || got != c.want {
				t.Fatalf("ReadSecret = %q, %v; want %q (err %v)", got, err, c.want, c.err)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":   true,
		"LocalHost":   true,
		"127.0.0.1":   true,
		"127.1.2.3":   true,
		"::1":         true,
		"[::1]":       false,
		"0.0.0.0":     false,
		"":            false,
		"10.0.0.1":    false,
		"example.com": false,
		"localhost.":  false,
	}
	for host, want := range cases {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestPublicOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://deed.example.com":       "https://deed.example.com",
		"https://deed.example.com/":      "https://deed.example.com",
		"https://deed.example.com:8443/": "https://deed.example.com:8443",
	} {
		if got := (&Config{PublicURL: in}).PublicOrigin(); got != want {
			t.Errorf("PublicOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}
