package backup

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

type rig struct {
	env      *testenv.Env
	svc      *Service
	objs     ObjectStore
	identity []age.Identity
	verify   []ed25519.PublicKey
}

func newRig(t *testing.T, objs ObjectStore) *rig {
	t.Helper()
	env := testenv.New(t)
	ctx := context.Background()
	if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO installation (id, installation_id, created_at, updated_at) VALUES (1, 'ins_test', 0, 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := env.Project(t, "home")
	src := env.Source(t, p.ID, "api", "")
	for i := 0; i < 50; i++ {
		if _, err := env.Apply(t, src, catalog.EntityItem{Kind: "server", Name: fmt.Sprintf("srv-%d", i)}, catalog.ApplyOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	idFile, rcpt, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := ParseRecipients([]string{rcpt})
	ids, _ := ParseIdentities(idFile)
	svc := New(Options{Store: env.Store, Keyring: env.Keyring, Audit: env.Audit, Clock: env.Clock, Objects: objs, Recipients: rs,
		KEK: func() []byte { return []byte("deed-kek-v1:test-kek-material") }, Retention: Retention{Daily: 7, Weekly: 4, Monthly: 6}, AppVersion: "test"})
	vk, _ := svc.VerifyKey()
	pub, err := ParseVerifyKey(vk)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{env: env, svc: svc, objs: objs, identity: ids, verify: []ed25519.PublicKey{pub}}
}

func (r *rig) fetch(t *testing.T, id string) (*Fetched, error) {
	t.Helper()
	f, err := Fetch(context.Background(), r.objs, FetchOptions{ID: id, Identities: r.identity, VerifyKeys: r.verify, TempDir: t.TempDir()})
	if f != nil {
		t.Cleanup(f.Cleanup)
	}
	return f, err
}

func TestBackupRoundTripAndRestore(t *testing.T) {
	mem := NewMem()
	r := newRig(t, mem)
	ctx := context.Background()
	run, err := r.svc.Run(ctx, "manual", audit.System)
	if err != nil || run.Result != "ok" || run.SizeBytes == 0 || run.PlainSHA256 == "" || run.SchemaVersion == 0 {
		t.Fatalf("run: %+v %v", run, err)
	}
	for _, k := range mem.Keys() {
		b, _ := mem.Get(ctx, k)
		raw := make([]byte, 1<<20)
		n, _ := b.Read(raw)
		if strings.Contains(string(raw[:n]), "srv-1") || strings.Contains(string(raw[:n]), "test-kek-material") {
			t.Fatalf("%s holds plaintext", k)
		}
	}
	f, err := r.fetch(t, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Verified || f.Counts["entities"] != 50 || string(f.KEK) != "deed-kek-v1:test-kek-material" || f.Manifest.Compression != "zstd" {
		t.Fatalf("fetched: %+v", f)
	}

	// Restore into a data directory that already has a database: the old one is kept, sessions are revoked.
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, store.DBFile), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	kekOut := filepath.Join(t.TempDir(), "kek")
	v, err := Restore(ctx, f, RestoreOptions{DataDir: dir, KEKFile: kekOut, Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), RevokeTokens: true})
	if err != nil {
		t.Fatalf("restore: %v %+v", err, v)
	}
	if !v.AuditOK || v.AuditEvents == 0 || v.Counts["entities"] != 50 || v.PreviousDB == "" {
		t.Fatalf("validation: %+v", v)
	}
	if b, _ := os.ReadFile(v.PreviousDB); string(b) != "old" {
		t.Fatal("previous database not kept")
	}
	if b, _ := os.ReadFile(kekOut); string(b) != "deed-kek-v1:test-kek-material" {
		t.Fatal("KEK not restored")
	}
	if m, err := ReadMarker(dir); err != nil || m == nil || m.BackupID != run.ID || !m.RevokedTokens {
		t.Fatalf("marker: %+v %v", m, err)
	}
	restored, err := store.Open(ctx, store.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	var live int
	_ = restored.R().QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live)
	_ = restored.Close()
	if live != 0 {
		t.Fatalf("%d sessions survived the restore", live)
	}
	// An existing KEK file with another key is never overwritten.
	_ = os.Chmod(kekOut, 0o600)
	if err := os.WriteFile(kekOut, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, f, RestoreOptions{DataDir: t.TempDir(), KEKFile: kekOut, Now: time.Now()}); err == nil {
		t.Fatal("restore overwrote a different KEK")
	}
}

func TestRestoreRefusals(t *testing.T) {
	r := newRig(t, NewMem())
	ctx := context.Background()
	if _, err := r.svc.Run(ctx, "manual", audit.System); err != nil {
		t.Fatal(err)
	}
	f, err := r.fetch(t, "latest")
	if err != nil {
		t.Fatal(err)
	}
	// A running server holds the lock.
	dir := t.TempDir()
	lock, err := store.AcquireLock(filepath.Join(dir, store.LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, f, RestoreOptions{DataDir: dir, Now: time.Now()}); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("restore under lock: %v", err)
	}
	_ = lock.Release()
	// A KEK mismatch without the escrowed KEK being written.
	if _, err := Restore(ctx, f, RestoreOptions{DataDir: dir, CurrentKEKID: "other", Now: time.Now()}); err == nil || !strings.Contains(err.Error(), "KEK") {
		t.Fatalf("KEK mismatch: %v", err)
	}
	// A backup from a newer schema.
	f.Manifest.SchemaVersion = 9999
	if _, err := Restore(ctx, f, RestoreOptions{DataDir: dir, Now: time.Now()}); err == nil || !strings.Contains(err.Error(), "newer deed") {
		t.Fatalf("newer schema: %v", err)
	}
}

func TestBackupFailureModes(t *testing.T) {
	mem := NewMem()
	r := newRig(t, mem)
	ctx := context.Background()
	good, err := r.svc.Run(ctx, "manual", audit.System)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong identity.
	otherID, _, _ := GenerateIdentity()
	ids, _ := ParseIdentities(otherID)
	if _, err := Fetch(ctx, mem, FetchOptions{Identities: ids, VerifyKeys: r.verify, TempDir: t.TempDir()}); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong identity: %v", err)
	}
	// Unverifiable manifest (another installation's key).
	pub, _, _ := ed25519.GenerateKey(nil)
	if _, err := Fetch(ctx, mem, FetchOptions{Identities: r.identity, VerifyKeys: []ed25519.PublicKey{pub}, TempDir: t.TempDir()}); !errors.Is(err, ErrUnverified) {
		t.Fatalf("foreign key: %v", err)
	}

	// S3 outage: the run fails, records the error and writes no manifest.
	mem.Fail = func(op, key string) error {
		if op == "put" {
			return errors.New("dial tcp: connection refused")
		}
		return nil
	}
	r.env.Clock.Advance(time.Hour)
	failed, err := r.svc.Run(ctx, "schedule", audit.System)
	if err == nil || failed.Result != "error" || !strings.Contains(failed.Error, "connection refused") {
		t.Fatalf("outage: %+v %v", failed, err)
	}
	mem.Fail = nil

	// Interrupted upload: the database object breaks mid-stream; the backup stays incomplete and latest is still good.
	mem.FailAfter = 512
	r.env.Clock.Advance(time.Hour)
	if _, err := r.svc.Run(ctx, "schedule", audit.System); err == nil {
		t.Fatal("interrupted upload reported success")
	}
	mem.FailAfter = 0
	f, err := r.fetch(t, "latest")
	if err != nil || f.Manifest.ID != good.ID {
		t.Fatalf("latest after failures: %v %+v", err, f)
	}

	// Corruption in storage is detected.
	key := objectKey(good.ID, dbObject)
	mem.Corrupt(key)
	if _, err := r.fetch(t, good.ID); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("corrupt object: %v", err)
	}

	runs, _ := r.svc.Runs(ctx, 10)
	results := map[string]int{}
	for _, run := range runs {
		results[run.Result]++
	}
	if results["ok"] != 1 || results["error"] != 2 {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestRetentionKeep(t *testing.T) {
	base := time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC)
	var created []time.Time
	// Four backups a day for 120 days, newest first.
	for d := 0; d < 120; d++ {
		for h := 0; h < 4; h++ {
			created = append(created, base.Add(-time.Duration(d)*24*time.Hour-time.Duration(h)*5*time.Hour))
		}
	}
	keep := Retention{Daily: 7, Weekly: 4, Monthly: 6}.Keep(created)
	n := 0
	for _, k := range keep {
		if k {
			n++
		}
	}
	// 7 daily, plus weekly and monthly representatives not already kept: at most 7 + 4 + 6.
	if !keep[0] || n < 7 || n > 17 {
		t.Fatalf("kept %d", n)
	}
	if keep[1] {
		t.Fatal("a second backup of the newest day was kept")
	}
	months, keptMonths := map[string]bool{}, map[string]bool{}
	for i, c := range created {
		months[c.Format("2006-01")] = true
		if keep[i] {
			keptMonths[c.Format("2006-01")] = true
		}
	}
	if len(keptMonths) != min(6, len(months)) {
		t.Fatalf("kept %d of %d months", len(keptMonths), len(months))
	}
}

func TestPruneDropsIncompleteAndOld(t *testing.T) {
	mem := NewMem()
	r := newRig(t, mem)
	mem.Now = r.env.Clock.Now
	ctx := context.Background()
	r.svc.o.Retention = Retention{Daily: 2}
	var ids []string
	for i := 0; i < 4; i++ {
		run, err := r.svc.Run(ctx, "schedule", audit.System)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
		r.env.Clock.Advance(25 * time.Hour)
	}
	// A planted manifest (foreign signature) is neither kept-counted nor deleted.
	_ = mem.Put(ctx, objectKey("bkp_01jbzzzzzzzzzzzzzzzzzzzzzz", manifestName), strings.NewReader(`{"manifest":{}}`), -1)
	if _, err := r.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	rs, _ := List(ctx, mem)
	have := map[string]bool{}
	for _, x := range rs {
		have[x.ID] = true
	}
	if !have[ids[3]] || !have[ids[2]] || have[ids[0]] || have[ids[1]] {
		t.Fatalf("after prune: %v (ids %v)", have, ids)
	}
}

// TestMinIO runs a backup against a real MinIO server: DEED_TEST_S3 (http://user:password@host:port, as in CI service
// containers) or a local `minio` binary, which this test also stops to simulate an outage.
func TestMinIO(t *testing.T) {
	ctx := context.Background()
	var endpoint, user, pass string
	var stop func()
	tgt, ok := s3FromEnv(t)
	if ok {
		endpoint, user, pass = tgt.endpoint, tgt.user, tgt.pass
	} else {
		bin, err := exec.LookPath("minio")
		if err != nil {
			t.Skip("set DEED_TEST_S3 or install minio")
		}
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		endpoint = l.Addr().String()
		_ = l.Close()
		user, pass = "deedtest", "deedtest-secret"
		cmd := exec.Command(bin, "server", t.TempDir(), "--address", endpoint, "--console-address", "127.0.0.1:0", "--quiet")
		cmd.Env = append(os.Environ(), "MINIO_ROOT_USER="+user, "MINIO_ROOT_PASSWORD="+pass)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stop = func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
		defer stop()
	}
	tgt.endpoint, tgt.user, tgt.pass = endpoint, user, pass
	bucket := fmt.Sprintf("deed-test-%d", time.Now().UnixNano())
	tgt.makeBucket(t, ctx, bucket)
	// sse=on in DEED_TEST_S3 runs the backup with SSE-S3 requested, as production does by default.
	r := newRig(t, tgt.store(t, bucket, "deed/", tgt.sse == "on"))
	run, err := r.svc.Run(ctx, "manual", audit.System)
	if err != nil {
		t.Fatalf("backup to minio: %v", err)
	}
	f, err := r.fetch(t, run.ID)
	if err != nil || f.Counts["entities"] != 50 {
		t.Fatalf("fetch from minio: %v", err)
	}
	// Outage: the server goes away (local binary) or the endpoint is wrong (service container).
	if stop != nil {
		stop()
	} else {
		bad, _ := NewS3(S3Options{Endpoint: "127.0.0.1:1", Bucket: bucket, AccessKey: user, SecretKey: pass, Insecure: true, Lookup: "path"})
		r.svc.o.Objects = bad
	}
	failed, err := r.svc.Run(ctx, "schedule", audit.System)
	if err == nil || failed.Result != "error" {
		t.Fatalf("backup during outage: %+v", failed)
	}
	var n int
	_ = r.env.Store.R().QueryRow(`SELECT COUNT(*) FROM backup_runs WHERE result = 'error'`).Scan(&n)
	if n != 1 {
		t.Fatal("outage not recorded")
	}
}
