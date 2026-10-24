// Adapted from Porch cmd/porch/server_cmds.go (Apache-2.0).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-deed/internal/app"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/config"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	deedlog "github.com/aurimasniekis/go-deed/internal/obs/log"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// configFlag registers -config (default $DEED_CONFIG).
func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", os.Getenv("DEED_CONFIG"), "path to deed.yaml (default $DEED_CONFIG)")
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path, os.Environ())
}

func openApp(ctx context.Context, cfgPath string, stderr io.Writer, serving bool) (*app.App, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	log := deedlog.New(stderr, cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(log)
	return app.Open(ctx, cfg, log, app.Options{SkipModules: !serving})
}

func runServe(ctx context.Context, args []string, _, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, true)
	if err != nil {
		return err
	}
	defer a.Close()
	ls, err := a.Listen(ctx)
	if err != nil {
		return err
	}
	return a.Serve(ctx, ls)
}

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("migrate", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, store.Options{DataDir: cfg.DataDir, AllowNetworkFS: cfg.Storage.AllowNetworkFS,
		PreMigrationSnapshots: cfg.Storage.PreMigrationSnapshots, Logger: deedlog.New(stderr, cfg.Log.Level, cfg.Log.Format)})
	if err != nil {
		return err
	}
	defer s.Close()
	cur, latest, err := store.SchemaVersions(ctx, s.R(), store.EmbeddedMigrations())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "schema version %d (latest %d)\n", cur, latest)
	return err
}

func runConfigCheck(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("config check", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	// Never print secrets: blank inline secret values before echoing the effective configuration.
	cfg.Backup.S3.SecretAccessKey = redactIfSet(cfg.Backup.S3.SecretAccessKey)
	for i := range cfg.Auth.OIDC {
		cfg.Auth.OIDC[i].ClientSecret = redactIfSet(cfg.Auth.OIDC[i].ClientSecret)
	}
	enc := yaml.NewEncoder(stdout)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stderr, "configuration OK")
	return err
}

func redactIfSet(s string) string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

// OpenAPIDocument builds the API document without an app (all extensions registered with nil services).
func OpenAPIDocument() ([]byte, error) {
	d := &httpapi.Deps{Version: "v1"}
	d.Extensions = app.APIExtensionsForSpec()
	api := httpapi.New(http.NewServeMux(), d)
	return json.MarshalIndent(api.OpenAPI(), "", "  ")
}

func runOpenAPI(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("openapi", stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	b, err := OpenAPIDocument()
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(b, '\n'))
	return err
}

func runHealthcheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("healthcheck", stderr)
	url := fs.String("url", "http://127.0.0.1:9464/readyz", "readiness URL on the management listener")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return &exitError{code: 1, err: fmt.Errorf("%s: %s", resp.Status, body)}
	}
	_, err = stdout.Write(body)
	return err
}

func runKeysInit(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys init", stderr)
	out := fs.String("out", "", "path of the new KEK file (must not exist)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return &exitError{code: 2, err: errors.New("--out is required")}
	}
	k, err := keyring.GenerateKEK()
	if err != nil {
		return err
	}
	if err := keyring.WriteKEKFile(*out, k); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote KEK %s (id %s). Backups carry it encrypted to your age recipients; keep the age identity offline.\n", *out, keyring.KEKID(k))
	return err
}

func runKeysStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys status", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"kek_id": a.Keyring.KEKID(), "purposes": a.Keyring.Status()})
}

func runKeysRotate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys rotate", stderr)
	cfgPath := configFlag(fs)
	purpose := fs.String("purpose", "", "purpose to rotate: field, token, backup-meta")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	v, err := a.Keyring.Rotate(ctx, a.Store, keyring.Purpose(*purpose))
	if err != nil {
		return err
	}
	err = a.Store.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := a.Audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "keyring.rotated", Target: audit.Target{Type: "keyring", ID: *purpose},
			Details: map[string]any{"version": v}})
		return err
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s key rotated to v%d; existing values stay readable with the decrypt-only previous version\n", *purpose, v)
	return err
}

func runKeysRotateKEK(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys rotate-kek", stderr)
	cfgPath := configFlag(fs)
	newFile := fs.String("new-kek", "", "path of the new KEK file (create it with `deed keys init`)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *newFile == "" {
		return &exitError{code: 2, err: errors.New("--new-kek is required")}
	}
	data, err := os.ReadFile(*newFile)
	if err != nil {
		return err
	}
	raw, err := keyring.ParseKEK(data)
	if err != nil {
		return err
	}
	nw, err := keyring.NewLocalKEK(raw)
	if err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	n, err := keyring.RotateKEK(ctx, a.Store, a.KEK, nw)
	if err != nil {
		return err
	}
	err = a.Store.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := a.Audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "keyring.kek_rotated", Details: map[string]any{"keys": n, "kek_id": nw.ID()}})
		return err
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "re-wrapped %d data keys under KEK %s; point kek.file at %s and take a new backup\n", n, nw.ID(), *newFile)
	return err
}

func runBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("admin bootstrap", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	l, err := a.Authn.IssueBootstrap(ctx, a.Cfg.Auth.SetupTokenTTL.D())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Open this single-use link within %s to create the first owner:\n\n  %s\n\n(expires %s)\n",
		a.Cfg.Auth.SetupTokenTTL.D(), l.URL(a.Cfg.PublicOrigin()), l.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

func runRecover(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("admin recover", stderr)
	cfgPath := configFlag(fs)
	email := fs.String("email", "", "email of the account to recover")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *email == "" {
		return &exitError{code: 2, err: errors.New("--email is required")}
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	l, err := a.Authn.IssueRecovery(ctx, *email)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "BREAK-GLASS: this single-use link signs in as %s without the second factor once. It is audited and raises an alert.\n\n  %s\n\n(expires %s)\n",
		*email, l.URL(a.Cfg.PublicOrigin()), l.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

func runCreateProject(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("admin create-project", stderr)
	cfgPath := configFlag(fs)
	name := fs.String("name", "", "project slug")
	display := fs.String("display-name", "", "display name")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	p, err := a.Catalog.CreateProject(ctx, audit.CLI, catalog.ProjectInput{Name: *name, DisplayName: *display}, "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "created project %s (%s)\n", p.Name, p.ID)
	return err
}

func runAuditVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("audit verify", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	res, err := audit.Verify(ctx, a.Store.R())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return err
	}
	if !res.OK {
		return &exitError{code: 3, err: errors.New("audit chain verification FAILED")}
	}
	return nil
}
