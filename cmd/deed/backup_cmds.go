package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/aurimasniekis/go-deed/internal/app"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/backup"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

func backupCommands() []*command {
	return []*command{
		{name: "keygen", summary: "generate an age identity for backups (keep it offline) and print its recipient", run: runBackupKeygen},
		{name: "run", summary: "take a backup now (offline; use POST /api/v1/backups while the server runs)", run: runBackupRun},
		{name: "list", summary: "list backups in storage and whether their manifests verify", run: runBackupList},
		{name: "verify", summary: "download, decrypt and check a backup (hashes, integrity_check, counts) without restoring it", run: runBackupVerify},
		{name: "verify-key", summary: "print the key that verifies this installation's backup manifests (store it with the age identity)", run: runBackupVerifyKey},
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runBackupKeygen(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup keygen", stderr)
	out := fs.String("out", "", "write the identity to this file (mode 0400; must not exist); default prints it")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	id, rcpt, err := backup.GenerateIdentity()
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Fprint(stdout, id)
		fmt.Fprintf(stderr, "recipient (put in backup.recipients): %s\n", rcpt)
		return nil
	}
	fh, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400) //nolint:gosec // operator-chosen path
	if err != nil {
		return err
	}
	if _, err := io.WriteString(fh, id); err != nil {
		_ = fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", rcpt)
	fmt.Fprintf(stderr, "identity written to %s; keep it offline (it decrypts every backup). Put the recipient above in backup.recipients.\n", *out)
	return nil
}

func runBackupRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup run", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("%w: the server is running; start a backup with POST /api/v1/backups (or the Backups page) instead", err)
	}
	if err != nil {
		return err
	}
	defer a.Close()
	svc, err := a.BackupService()
	if err != nil {
		return err
	}
	run, err := svc.Run(ctx, "cli", audit.System)
	if perr := printJSON(stdout, run); perr != nil {
		return perr
	}
	return err
}

// backupStore loads the configuration and builds only the object store (no data-directory lock).
func backupStore(cfgPath string) (backup.ObjectStore, string, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, "", err
	}
	objs, err := app.NewObjectStore(cfg.Backup.S3)
	if err != nil {
		return nil, "", err
	}
	if objs == nil {
		return nil, "", errors.New("backup.s3 is not configured")
	}
	return objs, cfg.DataDir, nil
}

func parseVerifyKeys(keys []string, file string) ([]ed25519.PublicKey, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // operator-chosen path
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				keys = append(keys, l)
			}
		}
	}
	out := make([]ed25519.PublicKey, 0, len(keys))
	for _, k := range keys {
		pk, err := backup.ParseVerifyKey(k)
		if err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, nil
}

func identities(file string) ([]age.Identity, error) {
	if file == "" {
		return nil, errors.New("-identity is required (the age identity file from `deed backup keygen`)")
	}
	b, err := os.ReadFile(file) //nolint:gosec // operator-chosen path
	if err != nil {
		return nil, err
	}
	ids, err := backup.ParseIdentities(string(b))
	if err != nil {
		return nil, err
	}
	return ids, nil
}

type fetchFlags struct {
	cfgPath, identity, verifyFile, id *string
	verify                            stringList
	skipVerify                        *bool
}

func addFetchFlags(fs *flag.FlagSet) *fetchFlags {
	f := &fetchFlags{cfgPath: configFlag(fs), identity: fs.String("identity", "", "age identity file"),
		verifyFile: fs.String("verify-key-file", "", "file with verification keys (one per line)"), id: fs.String("id", "latest", "backup ID or latest"),
		skipVerify: fs.Bool("skip-verify", false, "DANGEROUS: accept a manifest no verification key verifies (anyone with bucket write access can plant one)")}
	fs.Var(&f.verify, "verify-key", "verification key (deed-backup-ed25519:…); repeatable")
	return f
}

func (f *fetchFlags) fetch(ctx context.Context, tempDir string) (*backup.Fetched, string, error) {
	objs, dataDir, err := backupStore(*f.cfgPath)
	if err != nil {
		return nil, "", err
	}
	ids, err := identities(*f.identity)
	if err != nil {
		return nil, "", err
	}
	keys, err := parseVerifyKeys(f.verify, *f.verifyFile)
	if err != nil {
		return nil, "", err
	}
	if len(keys) == 0 && !*f.skipVerify {
		return nil, "", errors.New("-verify-key (from `deed backup verify-key` or GET /api/v1/backups) is required, or pass -skip-verify")
	}
	switch tempDir {
	case "":
		tempDir = os.TempDir()
	case "data":
		tempDir = dataDir
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, "", err
		}
	}
	fetched, err := backup.Fetch(ctx, objs, backup.FetchOptions{ID: *f.id, Identities: ids, VerifyKeys: keys, SkipVerify: *f.skipVerify, TempDir: tempDir})
	return fetched, dataDir, err
}

func runBackupList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup list", stderr)
	cfgPath := configFlag(fs)
	var verify stringList
	fs.Var(&verify, "verify-key", "verification key; repeatable (marks manifests as verified)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	objs, _, err := backupStore(*cfgPath)
	if err != nil {
		return err
	}
	keys, err := parseVerifyKeys(verify, "")
	if err != nil {
		return err
	}
	rs, err := backup.ListVerified(ctx, objs, keys)
	if err != nil {
		return err
	}
	return printJSON(stdout, rs)
}

func runBackupVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup verify", stderr)
	f := addFetchFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	fetched, _, err := f.fetch(ctx, "")
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	defer fetched.Cleanup()
	return printJSON(stdout, map[string]any{"ok": true, "verified_signature": fetched.Verified, "manifest": fetched.Manifest, "counts": fetched.Counts,
		"kek_included": fetched.KEK != nil})
}

func runBackupVerifyKey(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup verify-key", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("%w: read verify_key from GET /api/v1/backups instead", err)
	}
	if err != nil {
		return err
	}
	defer a.Close()
	svc, err := a.BackupService()
	if err != nil {
		return err
	}
	k, err := svc.VerifyKey()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, k)
	return nil
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("restore", stderr)
	f := addFetchFlags(fs)
	kekOut := fs.String("kek-out", "", "write the backup's escrowed KEK to this file (mode 0400) and use it; omit to keep the configured KEK")
	revokeTokens := fs.Bool("revoke-tokens", false, "also revoke every API and source token of the restored database (sessions are always revoked)")
	yes := fs.Bool("yes", false, "confirm replacing the database in the data directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*yes {
		return &exitError{code: 2, err: errors.New("restore replaces the database (the current one is kept as deed.db.pre-restore-…); re-run with -yes")}
	}
	cfg, err := loadConfig(*f.cfgPath)
	if err != nil {
		return err
	}
	fetched, dataDir, err := f.fetch(ctx, "data")
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	defer fetched.Cleanup()
	o := backup.RestoreOptions{DataDir: dataDir, KEKFile: *kekOut, RevokeTokens: *revokeTokens, Now: time.Now()}
	if *kekOut == "" {
		kek, err := keyring.LoadKEK(keyring.Source{Kind: cfg.KEK.Source, File: cfg.KEK.File, Production: cfg.IsProduction(), DataDir: dataDir, Environ: os.Environ()})
		if err != nil {
			return fmt.Errorf("load the configured KEK (or pass -kek-out to use the backup's): %w", err)
		}
		o.CurrentKEKID = kek.ID()
	}
	v, err := backup.Restore(ctx, fetched, o)
	if perr := printJSON(stdout, map[string]any{"backup_id": fetched.Manifest.ID, "backup_created": fetched.Manifest.Created, "validation": v}); perr != nil {
		return perr
	}
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	fmt.Fprintf(stderr, "restored backup %s; start deed to finish (the restore is recorded in the audit log). Point kek.file at %s if you used -kek-out.\n",
		fetched.Manifest.ID, *kekOut)
	return nil
}
