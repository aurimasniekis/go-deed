// Package backup writes encrypted backups of the Deed database (and, by default, its KEK) to S3-compatible storage
// and restores them offline (ADR-010).
//
// A backup is a VACUUM INTO snapshot that passed PRAGMA integrity_check, streamed through zstd and age (X25519
// recipients whose identities stay offline, so Deed can write backups but not read them) into a multipart upload.
// A plaintext manifest, written last as the completeness marker, records schema and app versions, recipient
// fingerprints, plaintext and ciphertext SHA-256 and sizes. It is signed with an Ed25519 key derived from the keyring:
// age recipients are public, so without the signature anyone with write access to the bucket could plant a forged
// installation encrypted to the same recipients.
package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"filippo.io/age"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Object layout under the store prefix.
const (
	backupPrefix = "backups/"
	dbObject     = "deed.db.zst.age"
	kekObject    = "kek.age"
	manifestName = "manifest.json"
)

func objectKey(id, name string) string { return backupPrefix + id + "/" + name }

// FormatVersion is the manifest format version.
const FormatVersion = 1

// BackupManifest describes one backup. It holds no secrets.
type BackupManifest struct {
	Format         int              `json:"format"`
	ID             string           `json:"id"`
	Created        time.Time        `json:"created"`
	AppVersion     string           `json:"app_version"`
	SchemaVersion  int64            `json:"schema_version"`
	InstallationID string           `json:"installation_id"`
	KEKID          string           `json:"kek_id"`
	KEKIncluded    bool             `json:"kek_included"`
	Recipients     []string         `json:"recipient_fingerprints"`
	Compression    string           `json:"compression"`
	Encryption     string           `json:"encryption"`
	Counts         map[string]int64 `json:"counts"`
	Objects        []BackupObject   `json:"objects"`
}

// BackupObject is one encrypted file of a backup.
type BackupObject struct {
	Name         string `json:"name"` // "db" or "kek"
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	StoredSize   int64  `json:"stored_size"`
	StoredSHA256 string `json:"stored_sha256"`
}

// envelope is the stored manifest.json: the exact manifest bytes and their signature.
type envelope struct {
	Manifest  json.RawMessage `json:"manifest"`
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
}

// VerifyKeyPrefix marks backup verification keys.
const VerifyKeyPrefix = "deed-backup-ed25519:"

// FormatVerifyKey encodes a public verification key.
func FormatVerifyKey(pub ed25519.PublicKey) string {
	return VerifyKeyPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// ParseVerifyKey decodes a verification key.
func ParseVerifyKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, VerifyKeyPrefix))
	if err != nil || len(b) != ed25519.PublicKeySize || !strings.HasPrefix(s, VerifyKeyPrefix) {
		return nil, errors.New("backup: invalid verification key (want deed-backup-ed25519:…)")
	}
	return ed25519.PublicKey(b), nil
}

func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// SigningKey derives the installation's backup signing key from the keyring backup-meta purpose key. Rotating that
// purpose changes the verification key; older backups stay verifiable with the old public key.
func SigningKey(kr *keyring.Keyring) (ed25519.PrivateKey, error) {
	seed, err := kr.MAC(keyring.PurposeBackupMeta, []byte("deed/backup-signing/v1"))
	if err != nil {
		return nil, err
	}
	if len(seed) < ed25519.SeedSize {
		return nil, errors.New("backup: short signing seed")
	}
	return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize]), nil
}

// Retention is grandfather-father-son retention of complete, verified backups.
type Retention struct {
	Daily, Weekly, Monthly int
}

// Options configure the service.
type Options struct {
	Store      *store.Store
	Keyring    *keyring.Keyring
	Audit      *audit.Writer
	Metrics    *metrics.Metrics
	Clock      clock.Clock
	Log        *slog.Logger
	Objects    ObjectStore
	Recipients []age.Recipient
	// KEK returns the encoded KEK to escrow; nil leaves the KEK out of backups.
	KEK        func() []byte
	Retention  Retention
	AppVersion string
}

// Service runs backups.
type Service struct {
	o       Options
	running atomic.Bool
}

// New returns the service.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o}
}

// Busy reports whether a backup is running.
func (s *Service) Busy() bool { return s.running.Load() }

// Objects returns the configured object store (nil when backups are not configured).
func (s *Service) Objects() ObjectStore { return s.o.Objects }

// Configured reports whether storage and recipients are set.
func (s *Service) Configured() bool { return s.o.Objects != nil && len(s.o.Recipients) > 0 }

// VerifyKey returns the public key that verifies this installation's backups.
func (s *Service) VerifyKey() (string, error) {
	k, err := SigningKey(s.o.Keyring)
	if err != nil {
		return "", err
	}
	return FormatVerifyKey(k.Public().(ed25519.PublicKey)), nil
}

// BackupRun is one backup attempt (a backup_runs row).
type BackupRun struct {
	ID            string     `json:"id"`
	Trigger       string     `json:"trigger"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Result        string     `json:"result" enum:"running,ok,error"`
	ObjectKey     string     `json:"object_key,omitempty"`
	SizeBytes     int64      `json:"size_bytes"`
	PlainSHA256   string     `json:"plain_sha256,omitempty"`
	SchemaVersion int64      `json:"schema_version"`
	Error         string     `json:"error,omitempty"`
}

// Runs returns the most recent runs, newest first.
func (s *Service) Runs(ctx context.Context, limit int) ([]BackupRun, error) {
	rows, err := s.o.Store.R().QueryContext(ctx, `SELECT id, trigger, started_at, finished_at, result, object_key, size_bytes, plain_sha256,
		schema_version, error FROM backup_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupRun{}
	for rows.Next() {
		var r BackupRun
		var st int64
		var fin sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Trigger, &st, &fin, &r.Result, &r.ObjectKey, &r.SizeBytes, &r.PlainSHA256, &r.SchemaVersion, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt = clock.FromMicros(st)
		if fin.Valid {
			t := clock.FromMicros(fin.Int64)
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrBusy means a backup is already running.
var ErrBusy = errors.New("backup: a backup is already running")

// Run takes a backup now. trigger is schedule, manual or cli.
func (s *Service) Run(ctx context.Context, trigger string, actor audit.Actor) (BackupRun, error) {
	if !s.running.CompareAndSwap(false, true) {
		return BackupRun{}, ErrBusy
	}
	defer s.running.Store(false)
	id := ids.New(ids.BackupRun)
	start := s.o.Clock.Now()
	if err := s.o.Store.W().TxNamed(ctx, "backup.start", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO backup_runs (id, started_at, trigger) VALUES (?, ?, ?)`, id, tx.NowMicros(), trigger)
		return err
	}); err != nil {
		return BackupRun{}, err
	}
	began := time.Now()
	m, stored, err := s.run(ctx, id, start)
	ok := err == nil
	msg := ""
	var dbObj BackupObject
	for _, o := range m.Objects {
		if o.Name == "db" {
			dbObj = o
		}
	}
	if err != nil {
		msg = err.Error()
		s.o.Log.Error("backup failed", "event.action", "backup.run", "event.outcome", "failure", "backup.id", id, "error.message", msg)
	} else {
		s.o.Log.Info("backup complete", "event.action", "backup.run", "event.outcome", "success", "backup.id", id, "stored_bytes", stored)
	}
	result := map[bool]string{true: "ok", false: "error"}[ok]
	finishErr := s.o.Store.W().TxNamed(context.WithoutCancel(ctx), "backup.finish", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE backup_runs SET finished_at = ?, result = ?, object_key = ?, size_bytes = ?, plain_sha256 = ?, schema_version = ?,
			error = ? WHERE id = ?`, tx.NowMicros(), result, dbObj.Key, stored, dbObj.SHA256, m.SchemaVersion, msg, id); err != nil {
			return err
		}
		ev := audit.Event{Actor: actor, Action: "backup.completed", Target: audit.Target{Type: "backup", ID: id},
			Details: map[string]any{"trigger": trigger, "stored_bytes": stored, "kek_included": m.KEKIncluded}}
		if !ok {
			ev.Action, ev.Outcome, ev.ReasonCode, ev.Details = "backup.failed", audit.OutcomeFailure, "backup_error", map[string]any{"trigger": trigger}
		}
		_, err := s.o.Audit.Append(ctx, tx, ev)
		return err
	})
	s.o.Metrics.BackupRun(result, s.o.Clock.Now(), time.Since(began), stored)
	if err == nil {
		err = finishErr
	}
	runs, _ := s.Runs(context.WithoutCancel(ctx), 20)
	for _, r := range runs {
		if r.ID == id {
			return r, err
		}
	}
	return BackupRun{ID: id, Trigger: trigger, StartedAt: start, Result: result, Error: msg}, err
}

// checkSnapshot runs PRAGMA integrity_check on a database file and counts key tables.
func checkSnapshot(ctx context.Context, path string) (map[string]int64, error) {
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	var res string
	if err := d.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return nil, fmt.Errorf("integrity check: %w", err)
	}
	if res != "ok" {
		return nil, fmt.Errorf("integrity check failed: %s", res)
	}
	counts := map[string]int64{}
	for _, t := range []string{"entities", "relations", "sources", "projects", "users", "audit_events"} {
		var n int64
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		counts[t] = n
	}
	return counts, nil
}

func (s *Service) run(ctx context.Context, id string, start time.Time) (BackupManifest, int64, error) {
	m := BackupManifest{Format: FormatVersion, ID: id, Created: start.UTC(), AppVersion: s.o.AppVersion, KEKID: s.o.Keyring.KEKID(),
		Compression: "zstd", Encryption: "age-x25519", Recipients: RecipientFingerprints(s.o.Recipients)}
	if !s.Configured() {
		return m, 0, errors.New("backups are not configured (backup.s3 and backup.recipients)")
	}
	if c, ok := s.o.Objects.(interface{ Check(context.Context) error }); ok {
		if err := c.Check(ctx); err != nil {
			return m, 0, err
		}
	}
	if err := s.o.Store.R().QueryRowContext(ctx, `SELECT installation_id FROM installation WHERE id = 1`).Scan(&m.InstallationID); err != nil {
		return m, 0, err
	}
	var err error
	if m.SchemaVersion, _, err = store.SchemaVersions(ctx, s.o.Store.R(), store.EmbeddedMigrations()); err != nil {
		return m, 0, err
	}
	dir, err := os.MkdirTemp(s.o.Store.Dir(), ".backup-")
	if err != nil {
		return m, 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	snap := filepath.Join(dir, store.DBFile)
	if err := s.o.Store.Snapshot(ctx, snap); err != nil {
		return m, 0, err
	}
	if m.Counts, err = checkSnapshot(ctx, snap); err != nil {
		return m, 0, err
	}
	var stored int64
	put := func(name, file string, src io.Reader) error {
		plain, cipher := newCounter(), newCounter()
		body := sealStream(s.o.Recipients, src, plain, cipher)
		defer body.Close()
		key := objectKey(id, file)
		if err := s.o.Objects.Put(ctx, key, body, -1); err != nil {
			return fmt.Errorf("upload %s: %w", file, err)
		}
		m.Objects = append(m.Objects, BackupObject{Name: name, Key: key, Size: plain.n, SHA256: plain.sum(), StoredSize: cipher.n, StoredSHA256: cipher.sum()})
		stored += cipher.n
		return nil
	}
	fh, err := os.Open(snap) //nolint:gosec // our own temp file
	if err != nil {
		return m, 0, err
	}
	err = put("db", dbObject, fh)
	_ = fh.Close()
	if err != nil {
		return m, stored, err
	}
	if s.o.KEK != nil {
		m.KEKIncluded = true
		if err := put("kek", kekObject, strings.NewReader(string(s.o.KEK()))); err != nil {
			return m, stored, err
		}
	}
	env, err := s.sign(m)
	if err != nil {
		return m, stored, err
	}
	if err := s.o.Objects.Put(ctx, objectKey(id, manifestName), strings.NewReader(string(env)), int64(len(env))); err != nil {
		return m, stored, fmt.Errorf("upload manifest: %w", err)
	}
	stored += int64(len(env))
	if _, err := s.Prune(ctx); err != nil {
		s.o.Log.Warn("backup prune failed; the backup itself succeeded", "error.message", err.Error())
	}
	return m, stored, nil
}

func (s *Service) sign(m BackupManifest) ([]byte, error) {
	k, err := SigningKey(s.o.Keyring)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	pub := k.Public().(ed25519.PublicKey)
	return json.Marshal(envelope{Manifest: body, KeyID: keyID(pub), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(k, body))})
}

// StoredBackup is a backup found in storage.
type StoredBackup struct {
	ID       string          `json:"id"`
	Created  time.Time       `json:"created"`
	Complete bool            `json:"complete"`
	Manifest *BackupManifest `json:"manifest,omitempty"`
	KeyID    string          `json:"key_id,omitempty"`
	Bytes    int64           `json:"bytes"`
	// Verified is set when a supplied key verifies the manifest signature.
	Verified bool `json:"verified"`
	env      envelope
}

// List returns the backups in storage, newest first. Manifests are parsed but not verified.
func List(ctx context.Context, objs ObjectStore) ([]StoredBackup, error) {
	infos, err := objs.List(ctx, backupPrefix)
	if err != nil {
		return nil, err
	}
	byID := map[string]*StoredBackup{}
	for _, o := range infos {
		id, name, ok := strings.Cut(strings.TrimPrefix(o.Key, backupPrefix), "/")
		if !ok || !ids.Valid(ids.BackupRun, id) {
			continue
		}
		r := byID[id]
		if r == nil {
			r = &StoredBackup{ID: id, Created: o.ModTime}
			byID[id] = r
		}
		r.Bytes += o.Size
		if name == manifestName {
			r.Complete = true
		}
	}
	out := make([]StoredBackup, 0, len(byID))
	for _, r := range byID {
		if r.Complete {
			if env, m, err := readManifest(ctx, objs, r.ID); err == nil {
				r.Manifest, r.Created, r.KeyID, r.env = &m, m.Created, env.KeyID, env
			}
		}
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b StoredBackup) int { return strings.Compare(b.ID, a.ID) })
	return out, nil
}

// ListVerified is List with Verified set for manifests one of keys verifies. Unverified manifests may have been
// planted by anyone with write access to the bucket.
func ListVerified(ctx context.Context, objs ObjectStore, keys []ed25519.PublicKey) ([]StoredBackup, error) {
	rs, err := List(ctx, objs)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].Verified = rs[i].Manifest != nil && verifyEnvelope(rs[i].env, keys)
	}
	return rs, nil
}

func verifyEnvelope(env envelope, keys []ed25519.PublicKey) bool {
	sig, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil {
		return false
	}
	for _, k := range keys {
		if ed25519.Verify(k, env.Manifest, sig) {
			return true
		}
	}
	return false
}

func readManifest(ctx context.Context, objs ObjectStore, id string) (envelope, BackupManifest, error) {
	var env envelope
	var m BackupManifest
	rc, err := objs.Get(ctx, objectKey(id, manifestName))
	if err != nil {
		return env, m, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return env, m, err
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return env, m, fmt.Errorf("backup %s: bad manifest", id)
	}
	if err := json.Unmarshal(env.Manifest, &m); err != nil {
		return env, m, fmt.Errorf("backup %s: bad manifest", id)
	}
	if m.ID != id || m.Format != FormatVersion {
		return env, m, fmt.Errorf("backup %s: manifest does not match its location or format", id)
	}
	return env, m, nil
}

// Keep decides GFS retention over complete backups sorted newest first: the newest backup always stays, plus the
// newest of each of the last Daily days, Weekly ISO weeks and Monthly months that have a backup.
func (r Retention) Keep(created []time.Time) []bool {
	keep := make([]bool, len(created))
	if len(created) == 0 {
		return keep
	}
	keep[0] = true
	bucket := func(n int, key func(time.Time) string) {
		seen := map[string]bool{}
		for i, t := range created {
			k := key(t.UTC())
			if seen[k] {
				continue
			}
			if len(seen) >= n {
				return
			}
			seen[k] = true
			keep[i] = true
		}
	}
	bucket(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(r.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
	bucket(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	return keep
}

// Prune applies GFS retention to this installation's verified backups, deletes incomplete backups older than a day
// and aborts stale multipart uploads. Manifests this installation did not sign are neither counted nor deleted, so a
// planted manifest cannot push real backups out of retention.
func (s *Service) Prune(ctx context.Context) (int, error) {
	k, err := SigningKey(s.o.Keyring)
	if err != nil {
		return 0, err
	}
	rs, err := ListVerified(ctx, s.o.Objects, []ed25519.PublicKey{k.Public().(ed25519.PublicKey)})
	if err != nil {
		return 0, err
	}
	var complete []StoredBackup
	var created []time.Time
	var drop []StoredBackup
	for _, r := range rs { // newest first
		switch {
		case r.Complete && !r.Verified:
			s.o.Log.Warn("backup storage holds a manifest this installation did not sign; ignoring it", "backup.id", r.ID)
		case r.Complete:
			complete = append(complete, r)
			created = append(created, r.Created)
		case s.o.Clock.Now().Sub(r.Created) > 24*time.Hour:
			drop = append(drop, r)
		}
	}
	for i, k := range s.o.Retention.Keep(created) {
		if !k {
			drop = append(drop, complete[i])
		}
	}
	n := 0
	for _, r := range drop {
		objs, err := s.o.Objects.List(ctx, backupPrefix+r.ID+"/")
		if err != nil {
			return n, err
		}
		// Manifest first: a half-deleted backup is then incomplete, never a complete one with missing objects.
		slices.SortFunc(objs, func(a, b ObjectInfo) int {
			am, bm := strings.HasSuffix(a.Key, manifestName), strings.HasSuffix(b.Key, manifestName)
			switch {
			case am == bm:
				return 0
			case am:
				return -1
			}
			return 1
		})
		for _, o := range objs {
			if err := s.o.Objects.Delete(ctx, o.Key); err != nil {
				return n, fmt.Errorf("delete %s (object lock?): %w", o.Key, err)
			}
		}
		n++
	}
	if a, ok := s.o.Objects.(interface {
		AbortIncomplete(context.Context, string) (int, error)
	}); ok {
		if _, err := a.AbortIncomplete(ctx, backupPrefix); err != nil {
			s.o.Log.Warn("abort incomplete uploads", "error.message", err.Error())
		}
		// MinIO lists incomplete uploads only for an exact object name, so the prefix sweep above finds nothing there:
		// also abort by key for the database object of recent runs that did not finish.
		for _, id := range s.unfinishedRuns(ctx) {
			if _, err := a.AbortIncomplete(ctx, objectKey(id, dbObject)); err != nil {
				s.o.Log.Warn("abort incomplete uploads", "backup.id", id, "error.message", err.Error())
			}
		}
	}
	return n, nil
}

// unfinishedRuns lists backup runs of the last 30 days that did not succeed (failed, or interrupted while running).
func (s *Service) unfinishedRuns(ctx context.Context) []string {
	if s.o.Store == nil {
		return nil
	}
	rows, err := s.o.Store.R().QueryContext(ctx, `SELECT id FROM backup_runs WHERE result != 'ok' AND started_at >= ? ORDER BY started_at DESC LIMIT 100`,
		s.o.Clock.Now().Add(-30*24*time.Hour).UnixMicro())
	if err != nil {
		s.o.Log.Warn("list unfinished backup runs", "error.message", err.Error())
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		s.o.Log.Warn("list unfinished backup runs", "error.message", err.Error())
	}
	return ids
}
