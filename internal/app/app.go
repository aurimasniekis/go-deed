// Package app assembles Deed: configuration, storage, keys, services, background jobs and the HTTP listeners.
//
// Structure adapted from Porch internal/app (Apache-2.0).
package app

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"database/sql"
	"errors"
	"fmt"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/buildinfo"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/config"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/oidc"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// App is a running Deed instance's object graph.
type App struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Clock    clock.Clock
	Metrics  *metrics.Metrics
	Store    *store.Store
	KEK      *keyring.LocalKEK
	Keyring  *keyring.Keyring
	Audit    *audit.Writer
	Authn    *authn.Service
	OIDC     *oidc.Service
	Catalog  *catalog.Service
	Guard    *safehttp.Guard
	ClientIP *httpx.ClientIPResolver

	// Modules extend the app (ingest, sources, Pulse, graph, telemetry, findings, alerts, live, backups).
	Modules []Module

	auditFile io.Closer
	loops     []loop
	apiExt    []func(huma.API, *httpapi.Deps)
	rawExt    []func(*http.ServeMux, *httpapi.Deps)
}

// Module is an optional subsystem wired into the app.
type Module interface {
	Name() string
}

// Options adjust Open (tests).
type Options struct {
	Clock   clock.Clock
	Environ []string
	// HashParams overrides Argon2id parameters (tests only).
	HashParams *authn.Params
	// SkipModules builds only the core (CLI commands that do not serve).
	SkipModules bool
}

// Open opens the data directory (taking its lock), loads the keyring and builds every service.
func Open(ctx context.Context, cfg *config.Config, log *slog.Logger, opts Options) (*App, error) {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	a := &App{Cfg: cfg, Log: log, Clock: opts.Clock, Metrics: metrics.New()}
	info := buildinfo.Get()
	a.Metrics.SetBuildInfo(info.Version, info.GoVersion)
	if err := CheckPendingRestore(cfg.DataDir); err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, store.Options{DataDir: cfg.DataDir, AllowNetworkFS: cfg.Storage.AllowNetworkFS,
		PreMigrationSnapshots: cfg.Storage.PreMigrationSnapshots, Logger: log, Clock: opts.Clock, Metrics: a.Metrics})
	if err != nil {
		return nil, err
	}
	a.Store = s
	if err := a.init(ctx, opts); err != nil {
		_ = a.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) init(ctx context.Context, opts Options) error {
	cfg := a.Cfg
	if err := ensureInstallation(ctx, a.Store); err != nil {
		return err
	}
	kek, err := keyring.LoadKEK(keyring.Source{Kind: cfg.KEK.Source, File: cfg.KEK.File, Production: cfg.IsProduction(), DataDir: cfg.DataDir,
		Environ: opts.Environ})
	if err != nil {
		return err
	}
	a.KEK = kek
	if a.Keyring, err = keyring.Load(ctx, a.Store, kek); err != nil {
		return err
	}
	a.Audit = audit.NewWriter(a.Keyring, a.Metrics)
	if err := a.openAuditSink(); err != nil {
		return err
	}
	a.Authn = authn.New(a.Store, a.Keyring, a.Audit, authn.Options{Clock: a.Clock, Metrics: a.Metrics, SessionIdle: cfg.Auth.Session.Idle.D(),
		SessionAbsolute: cfg.Auth.Session.Absolute.D(), ReauthWindow: cfg.Auth.Session.ReauthWindow.D(), MinPasswordBytes: cfg.Auth.Local.MinPasswordBytes,
		MFARequiredRoles: cfg.Auth.Local.MFARequiredRoles, LocalEnabled: cfg.Auth.Local.Enabled, MaxTokenLifetime: cfg.Auth.MaxTokenLifetime.D(),
		Params: opts.HashParams})
	if a.Guard, err = safehttp.New(cfg.Outbound.AllowCIDRs); err != nil {
		return err
	}
	if cfg.Outbound.CAFile != "" {
		pem, err := os.ReadFile(cfg.Outbound.CAFile)
		if err != nil {
			return fmt.Errorf("outbound.ca_file: %w", err)
		}
		if err := a.Guard.AddRootCAs(pem); err != nil {
			return fmt.Errorf("outbound.ca_file: %w", err)
		}
	}
	a.Catalog = catalog.New(a.Store, a.Audit, a.Keyring, a.Clock, a.Metrics, a.Log.With("component", "catalog"))
	a.Catalog.MaxEntities = cfg.Limits.MaxEntities
	a.Catalog.DefaultRemovalPercent = cfg.Limits.MaxRemovalPercent
	if err := a.Catalog.SeedBuiltins(ctx); err != nil {
		return fmt.Errorf("seed built-in kinds: %w", err)
	}
	oidcClient, err := a.Guard.Client(safehttp.Policy{Timeout: 20 * time.Second, InsecureHTTP: !cfg.IsProduction()})
	if err != nil {
		return err
	}
	a.OIDC = &oidc.Service{Store: a.Store, Keyring: a.Keyring, Audit: a.Audit, Authn: a.Authn, Clock: a.Clock, Metrics: a.Metrics,
		Log: a.Log.With("component", "oidc"), PublicURL: cfg.PublicOrigin(), Secure: a.Secure(), HTTP: oidcClient,
		Projects: func(ctx context.Context, slug string) (string, error) {
			p, err := a.Catalog.Project(ctx, slug)
			if err != nil {
				return "", err
			}
			return p.ID, nil
		}}
	if err := a.OIDC.Configure(cfg.Auth.OIDC); err != nil {
		return err
	}
	var cf []string
	if cfg.Proxy.Cloudflare {
		cf = cfg.Proxy.CloudflareRanges
	}
	if a.ClientIP, err = httpx.NewClientIPResolver(cfg.Proxy.TrustedProxies, cf); err != nil {
		return err
	}
	if !cfg.IsProduction() {
		a.Log.Warn("running in development mode: a development KEK and http://localhost are allowed; never use this mode in production")
	}
	if !cfg.Auth.Local.Enabled {
		a.Log.Info("local sign-in is disabled; only SSO and `deed admin recover` can sign in")
	}
	if opts.SkipModules {
		return nil
	}
	return a.initModules(ctx)
}

// Secure reports whether the deployment is served over https.
func (a *App) Secure() bool { return len(a.Cfg.PublicURL) > 8 && a.Cfg.PublicURL[:8] == "https://" }

func ensureInstallation(ctx context.Context, s *store.Store) error {
	return s.W().TxNamed(ctx, "installation.ensure", func(tx *store.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT installation_id FROM installation WHERE id = 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.Exec(ctx, `INSERT INTO installation (id, installation_id, created_at, updated_at) VALUES (1, ?, ?, ?)`,
				ids.New(ids.Installation), tx.NowMicros(), tx.NowMicros())
		}
		return err
	})
}

// InstallationID returns the installation's stable identifier.
func (a *App) InstallationID(ctx context.Context) (string, error) {
	var id string
	err := a.Store.R().QueryRowContext(ctx, `SELECT installation_id FROM installation WHERE id = 1`).Scan(&id)
	return id, err
}

func (a *App) openAuditSink() error {
	switch a.Cfg.Log.AuditFile {
	case "":
		return nil
	case "-":
		a.Audit.SetSink(audit.NewJSONSink(os.Stdout))
		return nil
	}
	f, err := os.OpenFile(a.Cfg.Log.AuditFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // operator-configured path
	if err != nil {
		return fmt.Errorf("open audit file: %w", err)
	}
	a.auditFile = f
	a.Audit.SetSink(audit.NewJSONSink(f))
	return nil
}

// Ready reports whether the instance can serve: storage healthy and keyring loaded.
func (a *App) Ready(ctx context.Context) error {
	if a.Keyring == nil {
		return errors.New("keyring not loaded")
	}
	st := a.Store.Health().Status()
	if st.State != store.HealthOK {
		return fmt.Errorf("storage %s: %s", st.State, st.Reason)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := a.InstallationID(cctx)
	return err
}

// Close releases the data directory.
func (a *App) Close() error {
	var errs []error
	for _, m := range a.Modules {
		if c, ok := m.(io.Closer); ok {
			errs = append(errs, c.Close())
		}
	}
	if a.Store != nil {
		errs = append(errs, a.Store.Close())
	}
	if a.auditFile != nil {
		errs = append(errs, a.auditFile.Close())
	}
	return errors.Join(errs...)
}

// CheckPendingRestore refuses to start while a restore left a staged database behind (ADR-010).
func CheckPendingRestore(dataDir string) error {
	if _, err := os.Stat(dataDir + "/deed.db.restore"); err == nil {
		return fmt.Errorf("a restore was interrupted (%s/deed.db.restore exists); rerun `deed restore` or remove the file", dataDir)
	}
	return nil
}
