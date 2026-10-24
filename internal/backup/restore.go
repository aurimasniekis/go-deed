package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// ErrUnverified means no supplied verification key matches the backup's signature.
var ErrUnverified = errors.New("backup: manifest signature does not verify with the supplied key(s); refusing to use it")

// Fetched is a downloaded, verified and decrypted backup. DBPath is a temporary file the caller removes (Cleanup).
type Fetched struct {
	Manifest BackupManifest
	DBPath   string
	KEK      []byte
	Verified bool
	Counts   map[string]int64
	dir      string
}

// Cleanup removes the temporary files.
func (f *Fetched) Cleanup() {
	if f != nil && f.dir != "" {
		_ = os.RemoveAll(f.dir)
	}
}

// FetchOptions select and check a backup.
type FetchOptions struct {
	ID         string // "" or "latest" for the newest verified backup
	Identities []age.Identity
	VerifyKeys []ed25519.PublicKey
	// SkipVerify accepts an unsigned or foreign manifest (dangerous: anyone who can write to the bucket can plant one).
	SkipVerify bool
	// TempDir receives the decrypted database (use a directory on the data directory's filesystem for restores).
	TempDir string
}

// Fetch downloads a backup, verifies the manifest signature, every object's ciphertext and plaintext hash and size,
// decrypts the database to a temporary file and runs PRAGMA integrity_check on it.
func Fetch(ctx context.Context, objs ObjectStore, o FetchOptions) (*Fetched, error) {
	id := o.ID
	if id == "" || id == "latest" {
		rs, err := ListVerified(ctx, objs, o.VerifyKeys)
		if err != nil {
			return nil, err
		}
		complete := false
		id = ""
		for _, r := range rs {
			complete = complete || r.Complete
			if r.Complete && (r.Verified || o.SkipVerify) {
				id = r.ID
				break
			}
		}
		if id == "" {
			if complete {
				return nil, ErrUnverified
			}
			return nil, errors.New("backup: no complete backup found")
		}
	}
	env, m, err := readManifest(ctx, objs, id)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("backup %s: no manifest (incomplete or unknown backup)", id)
	}
	if err != nil {
		return nil, err
	}
	f := &Fetched{Manifest: m}
	if !o.SkipVerify {
		if f.Verified = verifyEnvelope(env, o.VerifyKeys); !f.Verified {
			return nil, ErrUnverified
		}
	}
	if f.dir, err = os.MkdirTemp(o.TempDir, ".deed-restore-"); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Cleanup()
		}
	}()
	for _, ob := range m.Objects {
		if ob.Key != objectKey(id, filepath.Base(ob.Key)) {
			return nil, fmt.Errorf("backup %s: object %q outside the backup", id, ob.Key)
		}
		var dst io.Writer
		var buf bytes.Buffer
		var fh *os.File
		switch ob.Name {
		case "db":
			f.DBPath = filepath.Join(f.dir, store.DBFile)
			if fh, err = os.OpenFile(f.DBPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil { //nolint:gosec // our temp dir
				return nil, err
			}
			dst = fh
		case "kek":
			dst = &buf
		default:
			return nil, fmt.Errorf("backup %s: unknown object %q", id, ob.Name)
		}
		err := func() error {
			rc, err := objs.Get(ctx, ob.Key)
			if err != nil {
				return fmt.Errorf("download %s: %w", ob.Name, err)
			}
			defer rc.Close()
			cipher, plain := newCounter(), newCounter()
			if err := openStream(o.Identities, rc, dst, cipher, plain, MaxObjectSize); err != nil {
				if cipher.n != ob.StoredSize || cipher.sum() != ob.StoredSHA256 {
					// A damaged object usually fails decryption: say what happened.
					return fmt.Errorf("backup %s: %s was modified or truncated in storage", id, ob.Name)
				}
				return err
			}
			if cipher.n != ob.StoredSize || cipher.sum() != ob.StoredSHA256 {
				return fmt.Errorf("backup %s: %s was modified in storage", id, ob.Name)
			}
			if plain.n != ob.Size || plain.sum() != ob.SHA256 {
				return fmt.Errorf("backup %s: %s does not match the manifest", id, ob.Name)
			}
			return nil
		}()
		if fh != nil {
			if serr := fh.Sync(); err == nil {
				err = serr
			}
			_ = fh.Close()
		}
		if err != nil {
			return nil, err
		}
		if ob.Name == "kek" {
			f.KEK = bytes.TrimSpace(buf.Bytes())
		}
	}
	if f.DBPath == "" {
		return nil, fmt.Errorf("backup %s: no database object", id)
	}
	if f.Counts, err = checkSnapshot(ctx, f.DBPath); err != nil {
		return nil, fmt.Errorf("backup %s: %w", id, err)
	}
	for k, v := range m.Counts {
		if f.Counts[k] != v {
			return nil, fmt.Errorf("backup %s: %s count %d does not match the manifest (%d)", id, k, f.Counts[k], v)
		}
	}
	ok = true
	return f, nil
}

// RestoreMarker is left in the data directory; the next start records the restore in the audit log and raises a
// finding, then removes it.
const RestoreMarker = "restore-completed.json"

// Marker describes a completed restore.
type Marker struct {
	BackupID       string    `json:"backup_id"`
	BackupCreated  time.Time `json:"backup_created"`
	RestoredAt     time.Time `json:"restored_at"`
	Verified       bool      `json:"verified"`
	PreviousDB     string    `json:"previous_db,omitempty"`
	RevokedTokens  bool      `json:"revoked_tokens"`
	SchemaVersion  int64     `json:"schema_version"`
	InstallationID string    `json:"installation_id"`
}

// RestoreOptions place a fetched backup.
type RestoreOptions struct {
	DataDir string
	// KEKFile receives the escrowed KEK (mode 0400) when it does not exist yet; an existing file must hold the same KEK.
	KEKFile string
	// CurrentKEKID is the ID of the KEK the server will use (when KEKFile is not written): it must match the backup.
	CurrentKEKID string
	// RevokeTokens also revokes every API and source token of the restored database (sessions are always revoked).
	RevokeTokens bool
	Now          time.Time
}

// Validation is the post-restore check.
type Validation struct {
	Counts      map[string]int64 `json:"counts"`
	AuditEvents int64            `json:"audit_events_verified"`
	AuditOK     bool             `json:"audit_chain_ok"`
	PreviousDB  string           `json:"previous_db,omitempty"`
}

// Restore replaces the database in an offline data directory with a fetched backup: it refuses while a server holds
// the data-directory lock and when the backup's schema is newer than this binary, keeps the previous database as
// deed.db.pre-restore-<time>, swaps the new one in atomically, revokes every session (and optionally token) of the
// restored database, and validates counts and the audit hash chain.
func Restore(ctx context.Context, f *Fetched, o RestoreOptions) (*Validation, error) {
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, err
	}
	lock, err := store.AcquireLock(filepath.Join(o.DataDir, store.LockFile))
	if err != nil {
		return nil, fmt.Errorf("restore needs the server stopped: %w", err)
	}
	defer func() { _ = lock.Release() }()
	latest, err := store.LatestSchemaVersion()
	if err != nil {
		return nil, err
	}
	if f.Manifest.SchemaVersion > latest {
		return nil, fmt.Errorf("restore: the backup has schema version %d but this binary supports up to %d; use a newer deed", f.Manifest.SchemaVersion, latest)
	}
	switch {
	case f.KEK != nil && o.KEKFile != "":
		if cur, err := os.ReadFile(o.KEKFile); err == nil { //nolint:gosec // operator-chosen path
			if !bytes.Equal(bytes.TrimSpace(cur), f.KEK) {
				return nil, fmt.Errorf("restore: %s holds a different KEK; move it away or choose another --kek-out", o.KEKFile)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	case o.CurrentKEKID != "" && o.CurrentKEKID != f.Manifest.KEKID:
		return nil, fmt.Errorf("restore: the backup was made with KEK %s but the configured KEK is %s; restore the KEK too (--kek-out) or configure the original KEK",
			f.Manifest.KEKID, o.CurrentKEKID)
	}
	// Prepare the new database next to the old one: invalidate credentials that were live when the backup was taken.
	staged := filepath.Join(o.DataDir, store.DBFile+".restoring")
	_ = os.Remove(staged)
	if err := copyFile(f.DBPath, staged); err != nil {
		return nil, err
	}
	if err := revokeCredentials(ctx, staged, o.RevokeTokens, o.Now); err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	if f.KEK != nil && o.KEKFile != "" {
		if _, err := os.Stat(o.KEKFile); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(o.KEKFile), 0o700); err != nil {
				return nil, err
			}
			if err := writeExclusive(o.KEKFile, f.KEK, 0o400); err != nil {
				return nil, err
			}
		}
	}
	live := filepath.Join(o.DataDir, store.DBFile)
	v := &Validation{}
	if _, err := os.Stat(live); err == nil {
		v.PreviousDB = live + ".pre-restore-" + o.Now.UTC().Format("20060102T150405Z")
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if _, err := os.Stat(live + suffix); err == nil {
				if err := os.Rename(live+suffix, v.PreviousDB+suffix); err != nil {
					return nil, fmt.Errorf("keep previous database: %w", err)
				}
			}
		}
	}
	if err := os.Rename(staged, live); err != nil {
		return nil, err
	}
	if err := fsyncDir(o.DataDir); err != nil {
		return nil, err
	}
	// Validate what is now in place.
	d, err := sql.Open("sqlite", "file:"+live+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if v.Counts, err = checkSnapshot(ctx, live); err != nil {
		return v, err
	}
	res, err := audit.Verify(ctx, d)
	if err != nil {
		return v, fmt.Errorf("audit chain verification: %w", err)
	}
	v.AuditOK, v.AuditEvents = res.OK, res.Events
	if !res.OK {
		return v, fmt.Errorf("restore: the audit hash chain of the restored database does not verify (%s)", res.Problem)
	}
	marker, _ := json.Marshal(Marker{BackupID: f.Manifest.ID, BackupCreated: f.Manifest.Created, RestoredAt: o.Now.UTC(), Verified: f.Verified,
		PreviousDB: v.PreviousDB, RevokedTokens: o.RevokeTokens, SchemaVersion: f.Manifest.SchemaVersion, InstallationID: f.Manifest.InstallationID})
	_ = os.Remove(filepath.Join(o.DataDir, RestoreMarker))
	if err := writeExclusive(filepath.Join(o.DataDir, RestoreMarker), marker, 0o600); err != nil {
		return v, err
	}
	return v, nil
}

func revokeCredentials(ctx context.Context, path string, tokens bool, now time.Time) error {
	d, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer d.Close()
	us := now.UnixMicro()
	if _, err := d.ExecContext(ctx, `UPDATE sessions SET revoked_at = ?, revoke_reason = 'restore' WHERE revoked_at IS NULL`, us); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if tokens {
		if _, err := d.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE revoked_at IS NULL`, us); err != nil {
			return fmt.Errorf("revoke tokens: %w", err)
		}
	}
	// Leave a single-file database (no WAL) behind.
	_, err = d.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// ReadMarker returns the restore marker of a data directory, if any.
func ReadMarker(dataDir string) (*Marker, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, RestoreMarker)) //nolint:gosec // fixed name inside the data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("restore marker: %w", err)
	}
	return &m, nil
}

// ClearMarker removes the marker after the restore was recorded.
func ClearMarker(dataDir string) error { return os.Remove(filepath.Join(dataDir, RestoreMarker)) }

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // our temp file
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the data dir
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // operator-chosen target
	if err != nil {
		return err
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		return err
	}
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // data dir
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
		return err
	}
	return nil
}
