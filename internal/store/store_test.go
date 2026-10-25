package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
)

func openAt(t *testing.T, dir string, mod func(*Options)) (*Store, error) {
	t.Helper()
	o := Options{DataDir: dir, Readers: 2}
	if mod != nil {
		mod(&o)
	}
	s, err := Open(context.Background(), o)
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

func mustOpen(t *testing.T, dir string, mod func(*Options)) *Store {
	t.Helper()
	s, err := openAt(t, dir, mod)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func dataDir(t *testing.T) string { return filepath.Join(t.TempDir(), "data") }

// rawDB opens the database file directly, bypassing the store (for simulating other binaries).
func rawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(dir, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenFreshMigratesToLatest(t *testing.T) {
	ctx := context.Background()
	dir := dataDir(t)
	s := mustOpen(t, dir, nil)

	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(EmbeddedMigrations(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var files int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files++
		}
	}
	if latest <= 0 || latest != int64(files) {
		t.Fatalf("LatestSchemaVersion = %d, want %d (one per embedded migration)", latest, files)
	}
	cur, lat, err := SchemaVersions(ctx, s.R(), EmbeddedMigrations())
	if err != nil {
		t.Fatal(err)
	}
	if cur != latest || lat != latest {
		t.Fatalf("SchemaVersions = %d/%d, want %d", cur, lat, latest)
	}
	// A fresh database has nothing to roll back to: no pre-migration snapshot.
	if snaps := listSnapshots(t, dir); len(snaps) != 0 {
		t.Fatalf("unexpected snapshots on a fresh database: %v", snaps)
	}
	// Data dir and database files are private.
	for _, p := range []string{dir, filepath.Join(dir, BackupsDir), filepath.Join(dir, DBFile), filepath.Join(dir, LockFile)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s has mode %04o", p, fi.Mode().Perm())
		}
	}
	if s.Dir() != dir || s.Path(DBFile) != filepath.Join(dir, DBFile) {
		t.Fatalf("Dir/Path = %s / %s", s.Dir(), s.Path(DBFile))
	}
	// Reopening an up-to-date database is a no-op migration.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir, nil)
	if snaps := listSnapshots(t, dir); len(snaps) != 0 {
		t.Fatalf("reopen wrote snapshots: %v", snaps)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	dir := dataDir(t)
	s := mustOpen(t, dir, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a newer deed having migrated this database.
	d := rawDB(t, dir)
	if _, err := d.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, latest+1); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	for _, skip := range []bool{false, true} {
		_, err := openAt(t, dir, func(o *Options) { o.SkipMigrate = skip })
		if !errors.Is(err, ErrSchemaTooNew) {
			t.Fatalf("SkipMigrate=%v: err = %v, want ErrSchemaTooNew", skip, err)
		}
	}
	// The refusal released the lock and left the database untouched.
	d = rawDB(t, dir)
	var v int64
	if err := d.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != latest+1 {
		t.Fatalf("version %d after refusal, want %d", v, latest+1)
	}
}

// partialMigrations returns the first n embedded migrations.
func partialMigrations(t *testing.T, n int) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(EmbeddedMigrations(), ".")
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	for _, e := range entries[:n] {
		b, err := fs.ReadFile(EmbeddedMigrations(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = &fstest.MapFile{Data: b}
	}
	return m
}

func listSnapshots(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, BackupsDir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pre-migrate-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func integrityOK(t *testing.T, path string) {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var res string
	if err := d.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&res); err != nil {
		t.Fatal(err)
	}
	if res != "ok" {
		t.Fatalf("integrity_check(%s) = %q", path, res)
	}
}

func TestMigrationWritesPreMigrationSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := dataDir(t)
	c := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := mustOpen(t, dir, func(o *Options) { o.migrations = partialMigrations(t, 2); o.Clock = c })
	if err := s.W().Exec(ctx, `CREATE TABLE probe_rows (v TEXT); INSERT INTO probe_rows (v) VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	latest, _ := LatestSchemaVersion()
	s = mustOpen(t, dir, func(o *Options) { o.Clock = c })
	snaps := listSnapshots(t, dir)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v, want exactly one", snaps)
	}
	want := fmt.Sprintf("pre-migrate-0002-%04d-20261001T120000Z.db", latest)
	if snaps[0] != want {
		t.Fatalf("snapshot name %q, want %q", snaps[0], want)
	}
	path := filepath.Join(dir, BackupsDir, snaps[0])
	integrityOK(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %04o", fi.Mode().Perm())
	}
	cur, _, err := SchemaVersions(ctx, s.R(), EmbeddedMigrations())
	if err != nil || cur != latest {
		t.Fatalf("live database at %d (%v), want %d", cur, err, latest)
	}
	// The snapshot holds the pre-upgrade schema version and data.
	snapDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer snapDB.Close()
	var v int64
	if err := snapDB.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("snapshot version %d (%v), want 2", v, err)
	}
	var kept string
	if err := snapDB.QueryRowContext(ctx, `SELECT v FROM probe_rows`).Scan(&kept); err != nil || kept != "kept" {
		t.Fatalf("snapshot data %q (%v)", kept, err)
	}
}

func TestPruneSnapshotsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"pre-migrate-0001-0002-20260101T000000Z.db",
		"pre-migrate-0002-0003-20260201T000000Z.db",
		"pre-migrate-0003-0004-20260301T000000Z.db",
		"pre-migrate-0004-0005-20260401T000000Z.db",
		"unrelated.db",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneSnapshots(dir, 2, slog.New(slog.DiscardHandler))
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{names[2], names[3], "unrelated.db"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after prune: %v, want %v", got, want)
	}
}

func TestLockPreventsSecondOpen(t *testing.T) {
	dir := dataDir(t)
	s := mustOpen(t, dir, nil)
	if _, err := openAt(t, dir, nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v, want ErrLocked", err)
	}
	if _, err := AcquireLock(filepath.Join(dir, LockFile)); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock while open: %v, want ErrLocked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Close is idempotent and releases the lock.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir, nil)
}

func TestDataDirPermissions(t *testing.T) {
	root := t.TempDir()

	open := filepath.Join(root, "shared")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o750, 0o705, 0o777} {
		if err := os.Chmod(open, mode); err != nil {
			t.Fatal(err)
		}
		if err := PrepareDataDir(open); err == nil || !strings.Contains(err.Error(), "group or others") {
			t.Fatalf("mode %04o: PrepareDataDir err = %v, want refusal", mode, err)
		}
		if _, err := openAt(t, open, nil); err == nil {
			t.Fatalf("mode %04o: Open succeeded", mode)
		}
	}
	// Nothing was created inside the refused directory.
	if entries, _ := os.ReadDir(open); len(entries) != 0 {
		t.Fatalf("refused dir has entries: %v", entries)
	}

	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataDir(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file as data dir: %v", err)
	}

	fresh := filepath.Join(root, "a", "b")
	if err := PrepareDataDir(fresh); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(fresh)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("created data dir mode %v (%v)", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Join(fresh, BackupsDir)); err != nil || !fi.IsDir() {
		t.Fatalf("backups dir not created: %v", err)
	}
}

func TestSecureFilesTightensModes(t *testing.T) {
	dir := dataDir(t)
	s := mustOpen(t, dir, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, DBFile)
	if err := os.Chmod(db, 0o644); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir, nil)
	fi, err := os.Stat(db)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode after open %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

func TestCheckLocalFilesystem(t *testing.T) {
	if err := CheckLocalFilesystem(t.TempDir()); err != nil {
		t.Fatalf("temp dir refused: %v", err)
	}
}

func TestReaderPoolIsQueryOnly(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, dataDir(t), nil)
	if err := s.W().Exec(ctx, `CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO t (v) VALUES ('x')`, `CREATE TABLE u (v TEXT)`, `DELETE FROM t`} {
		if _, err := s.R().ExecContext(ctx, q); err == nil {
			t.Fatalf("reader executed %q", q)
		}
	}
	err := s.ReadTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO t (v) VALUES ('y')`)
		return err
	})
	if err == nil {
		t.Fatal("write inside ReadTx succeeded")
	}
	var n int
	if err := s.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows %d (%v), want 0", n, err)
	}
	// The writer can write; the reader sees the committed row.
	if err := s.W().Tx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO t (v) VALUES ('z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows %d (%v), want 1", n, err)
	}
	if err := VerifyPragmas(ctx, s.R(), 2, true, DefaultBusyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPragmas(ctx, s.R(), 1, false, DefaultBusyTimeout); err == nil {
		t.Fatal("reader pool verified as writable")
	}
}

func TestMaintenance(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, dataDir(t), nil)
	if err := s.W().Exec(ctx, `CREATE TABLE blob_rows (b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if err := s.W().Tx(ctx, func(tx *Tx) error {
		for range 64 {
			if _, err := tx.Exec(ctx, `INSERT INTO blob_rows (b) VALUES (zeroblob(16384))`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(ctx, s.R()); err != nil {
		t.Fatal(err)
	}
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if _, wal := s.Sizes(); wal == 0 {
		t.Fatal("expected a non-empty WAL before checkpoint")
	}
	if err := s.Checkpoint(ctx, "bogus"); err == nil {
		t.Fatal("invalid checkpoint mode accepted")
	}
	for _, m := range []string{"PASSIVE", "FULL", "RESTART", "TRUNCATE"} {
		if err := s.Checkpoint(ctx, m); err != nil {
			t.Fatalf("checkpoint %s: %v", m, err)
		}
	}
	dbBytes, wal := s.Sizes()
	if dbBytes == 0 || wal != 0 {
		t.Fatalf("after TRUNCATE checkpoint: db %d wal %d", dbBytes, wal)
	}
	// Freed pages go back to the filesystem with incremental vacuum (auto_vacuum=INCREMENTAL).
	if err := s.W().Exec(ctx, `DELETE FROM blob_rows`); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx, "TRUNCATE"); err != nil {
		t.Fatal(err)
	}
	var free int64
	if err := s.R().QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil || free == 0 {
		t.Fatalf("freelist %d (%v), want > 0", free, err)
	}
	if err := s.IncrementalVacuum(ctx, -5); err != nil { // negative means all
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx, "TRUNCATE"); err != nil {
		t.Fatal(err)
	}
	if err := s.R().QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil || free != 0 {
		t.Fatalf("freelist after incremental vacuum %d (%v), want 0", free, err)
	}
	after, _ := s.Sizes()
	if after >= dbBytes {
		t.Fatalf("database did not shrink: %d -> %d", dbBytes, after)
	}
	if st := s.Health().Status(); st.State != HealthOK {
		t.Fatalf("health %+v", st)
	}
}

func TestOpenRefusesCorruptDatabase(t *testing.T) {
	dir := dataDir(t)
	if err := PrepareDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DBFile), []byte(strings.Repeat("not a database ", 512)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(t, dir, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestSnapshotProducesIntactCopy(t *testing.T) {
	ctx := context.Background()
	dir := dataDir(t)
	s := mustOpen(t, dir, nil)
	if err := s.W().Exec(ctx, `CREATE TABLE snap_rows (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := s.W().Tx(ctx, func(tx *Tx) error {
		for range 100 {
			if _, err := tx.Exec(ctx, `INSERT INTO snap_rows (v) VALUES ('198.51.100.7')`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Snapshot(ctx, dest); err != nil {
		t.Fatal(err)
	}
	integrityOK(t, dest)
	fi, err := os.Stat(dest)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v (%v)", fi.Mode().Perm(), err)
	}
	d, err := sql.Open("sqlite", "file:"+dest+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM snap_rows`).Scan(&n); err != nil || n != 100 {
		t.Fatalf("snapshot rows %d (%v)", n, err)
	}
	cur, _, err := SchemaVersions(ctx, d, EmbeddedMigrations())
	latest, _ := LatestSchemaVersion()
	if err != nil || cur != latest {
		t.Fatalf("snapshot schema %d (%v), want %d", cur, err, latest)
	}
	// Existing targets are never overwritten; quotes cannot reach the SQL.
	if err := s.Snapshot(ctx, dest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite: %v", err)
	}
	if err := s.Snapshot(ctx, filepath.Join(t.TempDir(), "it's.db")); err == nil {
		t.Fatal("quote in snapshot path accepted")
	}
}
