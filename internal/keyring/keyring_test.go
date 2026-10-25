package keyring

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/store/storetest"
)

func newKEK(t *testing.T) *LocalKEK {
	t.Helper()
	raw, err := GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewLocalKEK(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func load(t *testing.T, s *store.Store, w KeyWrapper) *Keyring {
	t.Helper()
	kr, err := Load(context.Background(), s, w)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return kr
}

type keyState struct {
	version int64
	state   string
	kekID   string
	wrapped []byte
}

func keyRows(t *testing.T, s *store.Store, p Purpose) []keyState {
	t.Helper()
	rows, err := s.R().Query(`SELECT version, state, wrap_kek_id, wrapped FROM keyring_keys WHERE purpose = ? ORDER BY version`, string(p))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []keyState
	for rows.Next() {
		var k keyState
		if err := rows.Scan(&k.version, &k.state, &k.kekID, &k.wrapped); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLocalKEKWrapUnwrap(t *testing.T) {
	k := newKEK(t)
	pt := []byte("data key material, 32 bytes long")
	aad := []byte("deed/keyring/v1|field|1")
	w1, err := k.Wrap(pt, aad)
	if err != nil {
		t.Fatal(err)
	}
	w2, _ := k.Wrap(pt, aad)
	if bytes.Equal(w1, w2) {
		t.Fatal("wrapping is deterministic: nonces must be random")
	}
	if bytes.Contains(w1, pt) {
		t.Fatal("wrapped key contains the plaintext")
	}
	got, err := k.Unwrap(w1, aad)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("unwrap: %q %v", got, err)
	}
	if _, err := k.Unwrap(w1, []byte("deed/keyring/v1|token|1")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other purpose AAD: %v", err)
	}
	if _, err := newKEK(t).Unwrap(w1, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other KEK: %v", err)
	}
	flipped := bytes.Clone(w1)
	flipped[len(flipped)-1] ^= 1
	if _, err := k.Unwrap(flipped, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := k.Unwrap(w1[:20], aad); !errors.Is(err, ErrBadSealed) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestNewLocalKEKRejectsBadKeys(t *testing.T) {
	for _, raw := range [][]byte{nil, make([]byte, 16), make([]byte, 33), make([]byte, KEKSize)} {
		if _, err := NewLocalKEK(raw); !errors.Is(err, ErrBadKEKFile) {
			t.Errorf("%d-byte key (all zero=%v): %v", len(raw), bytes.Equal(raw, make([]byte, len(raw))), err)
		}
	}
	raw, _ := GenerateKEK()
	a, _ := NewLocalKEK(raw)
	b, _ := NewLocalKEK(bytes.Clone(raw))
	if a.ID() != b.ID() || a.ID() != KEKID(raw) || len(a.ID()) != 16 {
		t.Fatalf("KEK ID not stable: %s %s", a.ID(), b.ID())
	}
	if a.ID() == newKEK(t).ID() {
		t.Fatal("different KEKs share an ID")
	}
	if !bytes.Equal(a.Encoded(), EncodeKEK(raw)) {
		t.Fatal("Encoded differs from EncodeKEK")
	}
	// The wrapper keeps its own copy of the key.
	raw[0] ^= 0xff
	if a.ID() != b.ID() {
		t.Fatal("ID changed with the caller's buffer")
	}
	if w, err := a.Wrap([]byte("x"), nil); err != nil {
		t.Fatal(err)
	} else if _, err := b.Unwrap(w, nil); err != nil {
		t.Fatalf("wrapper aliased the caller's buffer: %v", err)
	}
}

func TestParseKEK(t *testing.T) {
	raw, _ := GenerateKEK()
	for raw[0] >= 0x20 && raw[0] <= 0x7e { // make sure the raw form is not printable
		raw[0]++
	}
	cases := map[string][]byte{
		"std padded":       EncodeKEK(raw),
		"std raw":          []byte(base64.RawStdEncoding.EncodeToString(raw)),
		"url padded":       []byte(base64.URLEncoding.EncodeToString(raw) + "\r\n"),
		"url raw":          []byte("  " + base64.RawURLEncoding.EncodeToString(raw)),
		"raw binary bytes": raw,
	}
	for name, in := range cases {
		got, err := ParseKEK(in)
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%s: %x %v", name, got, err)
		}
	}
	for name, in := range map[string][]byte{
		"empty":       nil,
		"short":       []byte(base64.StdEncoding.EncodeToString(raw[:16])),
		"not base64":  []byte("this is definitely not a key!!!!"),
		"long base64": []byte(base64.StdEncoding.EncodeToString(append(bytes.Clone(raw), 1))),
	} {
		if _, err := ParseKEK(in); !errors.Is(err, ErrBadKEKFile) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWriteKEKFile(t *testing.T) {
	raw, _ := GenerateKEK()
	path := filepath.Join(t.TempDir(), "kek")
	if err := WriteKEKFile(path, raw); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o400 {
		t.Fatalf("mode %04o, want 0400", fi.Mode().Perm())
	}
	got, err := readKEKFile(path, false)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("read back: %v", err)
	}
	other, _ := GenerateKEK()
	if err := WriteKEKFile(path, other); err == nil {
		t.Fatal("an existing KEK file was overwritten")
	}
	if got, _ := readKEKFile(path, false); !bytes.Equal(got, raw) {
		t.Fatal("KEK file content changed")
	}
}

func writeMode(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestReadKEKFilePermissions(t *testing.T) {
	raw, _ := GenerateKEK()
	cases := []struct {
		mode       os.FileMode
		credential bool
		ok         bool
	}{
		{0o400, false, true},
		{0o600, false, true},
		{0o640, false, false},
		{0o440, false, false},
		{0o404, false, false},
		{0o644, false, false},
		{0o400, true, true},
		{0o440, true, true},
		{0o460, true, false},
		{0o444, true, false},
	}
	dir := t.TempDir()
	for i, tc := range cases {
		path := filepath.Join(dir, fmt.Sprintf("kek-%d", i))
		writeMode(t, path, EncodeKEK(raw), tc.mode)
		_, err := readKEKFile(path, tc.credential)
		if tc.ok && err != nil {
			t.Errorf("mode %04o credential=%v refused: %v", tc.mode, tc.credential, err)
		}
		if !tc.ok && !errors.Is(err, ErrBadKEKFile) {
			t.Errorf("mode %04o credential=%v accepted: %v", tc.mode, tc.credential, err)
		}
	}
	sub := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readKEKFile(sub, false); !errors.Is(err, ErrBadKEKFile) {
		t.Fatalf("directory accepted: %v", err)
	}
	if _, err := readKEKFile(filepath.Join(dir, "missing"), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestLoadKEKSources(t *testing.T) {
	raw, _ := GenerateKEK()
	want := KEKID(raw)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "kek")
	writeMode(t, keyFile, EncodeKEK(raw), 0o400)
	credDir := filepath.Join(dir, "creds")
	if err := os.Mkdir(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMode(t, filepath.Join(credDir, "deed-kek"), EncodeKEK(raw), 0o440)

	ok := []struct {
		name string
		src  Source
	}{
		{"file", Source{Kind: "file", File: keyFile, Production: true}},
		{"default kind", Source{File: keyFile}},
		{"env", Source{Kind: "env", Environ: []string{"HOME=/root", "DEED_KEK=" + base64.StdEncoding.EncodeToString(raw)}}},
		{"credential", Source{Kind: "credential", File: "/etc/deed/deed-kek", Environ: []string{"CREDENTIALS_DIRECTORY=" + credDir}}},
	}
	for _, tc := range ok {
		k, err := LoadKEK(tc.src)
		if err != nil || k.ID() != want {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	bad := []struct {
		name string
		src  Source
		err  error
	}{
		{"env unset", Source{Kind: "env", Environ: []string{"DEED_KEK="}}, ErrNoKEK},
		{"env invalid", Source{Kind: "env", Environ: []string{"DEED_KEK=short"}}, ErrBadKEKFile},
		{"credential without directory", Source{Kind: "credential", File: "deed-kek"}, ErrNoKEK},
		{"credential missing", Source{Kind: "credential", File: "other", Environ: []string{"CREDENTIALS_DIRECTORY=" + credDir}}, os.ErrNotExist},
		{"file missing", Source{Kind: "file", File: filepath.Join(dir, "nope")}, ErrNoKEK},
		{"production without file", Source{Kind: "file", Production: true, DataDir: dir}, ErrNoKEK},
		{"development without data dir", Source{Kind: "file"}, ErrNoKEK},
	}
	for _, tc := range bad {
		if _, err := LoadKEK(tc.src); !errors.Is(err, tc.err) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.err)
		}
	}
	if _, err := LoadKEK(Source{Kind: "vault"}); err == nil {
		t.Error("unknown source accepted")
	}
	loose := filepath.Join(dir, "loose")
	writeMode(t, loose, EncodeKEK(raw), 0o644)
	if _, err := LoadKEK(Source{File: loose}); !errors.Is(err, ErrBadKEKFile) {
		t.Errorf("world-readable KEK file accepted: %v", err)
	}
}

func TestLoadKEKDevelopmentKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	k1, err := LoadKEK(Source{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dataDir, DevKEKFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o400 {
		t.Fatalf("dev KEK mode %04o", fi.Mode().Perm())
	}
	k2, err := LoadKEK(Source{DataDir: dataDir})
	if err != nil || k2.ID() != k1.ID() {
		t.Fatalf("dev KEK not reused: %v", err)
	}
}

func TestLoadCreatesPurposeKeysAndCanary(t *testing.T) {
	s := storetest.Open(t)
	kek := newKEK(t)
	kr := load(t, s, kek)
	if kr.KEKID() != kek.ID() {
		t.Fatalf("KEKID = %s", kr.KEKID())
	}
	for _, st := range kr.Status() {
		if st.ActiveVersion != 1 || !slices.Equal(st.Versions, []uint32{1}) {
			t.Fatalf("status %+v", st)
		}
		rows := keyRows(t, s, st.Purpose)
		if len(rows) != 1 || rows[0].state != StateActive || rows[0].kekID != kek.ID() {
			t.Fatalf("%s rows %+v", st.Purpose, rows)
		}
		if bytes.Contains(rows[0].wrapped, kr.keys[st.Purpose][1].raw) {
			t.Fatalf("%s data key stored unwrapped", st.Purpose)
		}
	}
	// Purpose keys are independent.
	if bytes.Equal(kr.keys[PurposeField][1].raw, kr.keys[PurposeToken][1].raw) {
		t.Fatal("purposes share a data key")
	}
	var canary string
	if err := s.R().QueryRow(`SELECT value FROM settings WHERE key = ?`, canaryKey).Scan(&canary); err != nil || canary == "" {
		t.Fatalf("canary missing: %v", err)
	}
	// Loading again reuses the stored keys.
	sealed, err := kr.Seal(PurposeField, FieldAAD("t", "c", "r1"), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := kr.MAC(PurposeToken, []byte("bearer|x"))
	kr2 := load(t, s, kek)
	if pt, err := kr2.Open(PurposeField, FieldAAD("t", "c", "r1"), sealed); err != nil || string(pt) != "secret" {
		t.Fatalf("reload open: %q %v", pt, err)
	}
	if m2, _ := kr2.MAC(PurposeToken, []byte("bearer|x")); !bytes.Equal(m1, m2) {
		t.Fatal("MAC changed across loads")
	}
	if n := len(keyRows(t, s, PurposeField)); n != 1 {
		t.Fatalf("reload created keys: %d", n)
	}
}

// sameIDWrapper claims another KEK's ID, so loading gets past the ID check and must fail on unwrap.
type sameIDWrapper struct {
	*LocalKEK
	id string
}

func (w sameIDWrapper) ID() string { return w.id }

func TestWrongKEKRefused(t *testing.T) {
	s := storetest.Open(t)
	kek := newKEK(t)
	load(t, s, kek)
	if _, err := Load(context.Background(), s, newKEK(t)); !errors.Is(err, ErrWrongKEK) {
		t.Fatalf("other KEK: %v", err)
	}
	if _, err := Load(context.Background(), s, sameIDWrapper{LocalKEK: newKEK(t), id: kek.ID()}); !errors.Is(err, ErrWrongKEK) {
		t.Fatalf("other KEK with a forged ID: %v", err)
	}
	// A canary that no longer verifies means the keys do not belong to this database.
	if err := s.W().Tx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE settings SET value = ? WHERE key = ?`, base64.StdEncoding.EncodeToString([]byte{1, 0, 0, 0, 1, 2, 3}), canaryKey)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), s, kek); !errors.Is(err, ErrWrongKEK) {
		t.Fatalf("broken canary: %v", err)
	}
}

func TestSealOpen(t *testing.T) {
	s := storetest.Open(t)
	kr := load(t, s, newKEK(t))
	aad := FieldAAD("sources", "credentials_enc", "src_1")
	pt := []byte(`{"token":"s3cr3t"}`)
	sealed, err := kr.Seal(PurposeField, aad, pt)
	if err != nil {
		t.Fatal(err)
	}
	if sealed[0] != sealFormat {
		t.Fatalf("format byte %x", sealed[0])
	}
	if v, err := SealedVersion(sealed); err != nil || v != 1 {
		t.Fatalf("version %d %v", v, err)
	}
	if bytes.Contains(sealed, pt) {
		t.Fatal("plaintext visible in sealed value")
	}
	if again, _ := kr.Seal(PurposeField, aad, pt); bytes.Equal(again, sealed) {
		t.Fatal("sealing is deterministic")
	}
	if got, err := kr.Open(PurposeField, aad, sealed); err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("open: %q %v", got, err)
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-3] ^= 0x10
	unknownVersion := bytes.Clone(sealed)
	unknownVersion[4] = 9
	badFormat := bytes.Clone(sealed)
	badFormat[0] = 0x02
	cases := []struct {
		name   string
		p      Purpose
		aad    []byte
		sealed []byte
		err    error
	}{
		{"other row", PurposeField, FieldAAD("sources", "credentials_enc", "src_2"), sealed, ErrDecrypt},
		{"other column", PurposeField, FieldAAD("sources", "other_enc", "src_1"), sealed, ErrDecrypt},
		{"other purpose", PurposeToken, aad, sealed, ErrDecrypt},
		{"tampered", PurposeField, aad, tampered, ErrDecrypt},
		{"unknown version", PurposeField, aad, unknownVersion, ErrUnknownKey},
		{"bad format", PurposeField, aad, badFormat, ErrBadSealed},
		{"truncated header", PurposeField, aad, sealed[:3], ErrBadSealed},
		{"truncated body", PurposeField, aad, sealed[:sealHeader+4], ErrBadSealed},
		{"unknown purpose", Purpose("nope"), aad, sealed, ErrUnknownKey},
	}
	for _, tc := range cases {
		if _, err := kr.Open(tc.p, tc.aad, tc.sealed); !errors.Is(err, tc.err) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.err)
		}
	}
	if _, err := kr.Seal(Purpose("nope"), aad, pt); !errors.Is(err, ErrBadPurpose) {
		t.Errorf("seal unknown purpose: %v", err)
	}
	if _, err := kr.MAC(Purpose("nope"), pt); !errors.Is(err, ErrBadPurpose) {
		t.Errorf("MAC unknown purpose: %v", err)
	}
}

func TestMAC(t *testing.T) {
	s := storetest.Open(t)
	kr := load(t, s, newKEK(t))
	m1, err := kr.MAC(PurposeToken, []byte("session|abc"))
	if err != nil || len(m1) != 32 {
		t.Fatalf("MAC: %x %v", m1, err)
	}
	if m2, _ := kr.MAC(PurposeToken, []byte("session|abd")); bytes.Equal(m1, m2) {
		t.Fatal("different inputs share a MAC")
	}
	if m3, _ := kr.MAC(PurposeBackupMeta, []byte("session|abc")); bytes.Equal(m1, m3) {
		t.Fatal("MAC not separated by purpose")
	}
	if !kr.VerifyMAC(PurposeToken, []byte("session|abc"), m1) || kr.VerifyMAC(PurposeToken, []byte("session|abd"), m1) {
		t.Fatal("VerifyMAC")
	}
	if kr.VerifyMAC(Purpose("nope"), []byte("session|abc"), m1) {
		t.Fatal("VerifyMAC with an unknown purpose")
	}
}

func TestUnloadedKeyring(t *testing.T) {
	kr := &Keyring{}
	if _, err := kr.Seal(PurposeField, nil, []byte("x")); !errors.Is(err, ErrNotUnlocked) {
		t.Errorf("seal: %v", err)
	}
	if _, err := kr.Open(PurposeField, nil, []byte{1, 0, 0, 0, 1}); !errors.Is(err, ErrNotUnlocked) {
		t.Errorf("open: %v", err)
	}
	if _, err := kr.MAC(PurposeToken, nil); !errors.Is(err, ErrNotUnlocked) {
		t.Errorf("mac: %v", err)
	}
	if _, err := kr.MACAll(PurposeToken, nil); !errors.Is(err, ErrNotUnlocked) {
		t.Errorf("mac all: %v", err)
	}
	if kr.VerifyMAC(PurposeToken, nil, nil) || kr.ActiveVersion(PurposeField) != 0 {
		t.Error("unloaded keyring verified or reported a version")
	}
}

func TestRotate(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	kek := newKEK(t)
	kr := load(t, s, kek)
	aad := FieldAAD("mfa_totp", "secret_enc", "usr_1")
	old, _ := kr.Seal(PurposeField, aad, []byte("totp-secret"))
	oldMAC, _ := kr.MAC(PurposeToken, []byte("bearer|deed_at_x"))

	if _, err := kr.Rotate(ctx, s, Purpose("nope")); !errors.Is(err, ErrBadPurpose) {
		t.Fatalf("rotate unknown purpose: %v", err)
	}
	v, err := kr.Rotate(ctx, s, PurposeField)
	if err != nil || v != 2 || kr.ActiveVersion(PurposeField) != 2 {
		t.Fatalf("rotate field: v%d %v", v, err)
	}
	if kr.ActiveVersion(PurposeToken) != 1 {
		t.Fatal("rotating field changed the token key")
	}
	fresh, _ := kr.Seal(PurposeField, aad, []byte("totp-secret"))
	if fv, _ := SealedVersion(fresh); fv != 2 {
		t.Fatalf("new writes use v%d", fv)
	}
	if pt, err := kr.Open(PurposeField, aad, old); err != nil || string(pt) != "totp-secret" {
		t.Fatalf("old value after rotation: %v", err)
	}
	if !kr.NeedsReseal(PurposeField, old) || kr.NeedsReseal(PurposeField, fresh) || kr.NeedsReseal(PurposeField, []byte("junk")) {
		t.Fatal("NeedsReseal")
	}
	resealed, err := kr.Reseal(PurposeField, aad, old)
	if err != nil {
		t.Fatal(err)
	}
	if rv, _ := SealedVersion(resealed); rv != 2 {
		t.Fatalf("resealed under v%d", rv)
	}
	if _, err := kr.Reseal(PurposeField, FieldAAD("x", "y", "z"), old); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("reseal with wrong AAD: %v", err)
	}

	if v, err := kr.Rotate(ctx, s, PurposeToken); err != nil || v != 2 {
		t.Fatalf("rotate token: %v", err)
	}
	newMAC, _ := kr.MAC(PurposeToken, []byte("bearer|deed_at_x"))
	if bytes.Equal(newMAC, oldMAC) {
		t.Fatal("MAC unchanged after rotation")
	}
	all, err := kr.MACAll(PurposeToken, []byte("bearer|deed_at_x"))
	if err != nil || len(all) != 2 || !bytes.Equal(all[0], newMAC) || !bytes.Equal(all[1], oldMAC) {
		t.Fatalf("MACAll must list the active key first, then older versions: %x", all)
	}
	if !kr.VerifyMAC(PurposeToken, []byte("bearer|deed_at_x"), oldMAC) {
		t.Fatal("MACs from before the rotation no longer verify")
	}

	rows := keyRows(t, s, PurposeField)
	if len(rows) != 2 || rows[0].state != StateDecryptOnly || rows[1].state != StateActive {
		t.Fatalf("field key rows: %+v", rows)
	}
	// A reload sees the same versions, still opens old values, and the canary was resealed under the new key.
	kr2 := load(t, s, kek)
	for _, st := range kr2.Status() {
		want := uint32(1)
		if st.Purpose != PurposeBackupMeta {
			want = 2
		}
		if st.ActiveVersion != want || len(st.Versions) != int(want) {
			t.Fatalf("reloaded status %+v", st)
		}
	}
	if pt, err := kr2.Open(PurposeField, aad, old); err != nil || string(pt) != "totp-secret" {
		t.Fatalf("old value after reload: %v", err)
	}
	if v, err := kr2.Rotate(ctx, s, PurposeField); err != nil || v != 3 {
		t.Fatalf("second rotation: v%d %v", v, err)
	}
	// The first keyring's view is stale: rotating from it must not create a second active key.
	if _, err := kr.Rotate(ctx, s, PurposeField); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale rotation: %v", err)
	}
}

func TestRetire(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	kek := newKEK(t)
	kr := load(t, s, kek)
	aad := FieldAAD("t", "c", "r")
	old, _ := kr.Seal(PurposeField, aad, []byte("v1 value"))
	if err := kr.Retire(ctx, s, PurposeField, 1); err == nil {
		t.Fatal("the active key was retired")
	}
	if err := kr.Retire(ctx, s, PurposeField, 7); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retire unknown version: %v", err)
	}
	if _, err := kr.Rotate(ctx, s, PurposeField); err != nil {
		t.Fatal(err)
	}
	if err := kr.Retire(ctx, s, PurposeField, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Open(PurposeField, aad, old); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retired key still opens: %v", err)
	}
	if rows := keyRows(t, s, PurposeField); rows[0].state != StateRetired {
		t.Fatalf("rows %+v", rows)
	}
	// The canary was resealed under v2, so retiring v1 does not lock the keyring.
	kr2 := load(t, s, kek)
	if st := kr2.Status()[0]; st.Purpose != PurposeField || !slices.Equal(st.Versions, []uint32{2}) {
		t.Fatalf("retired key loaded: %+v", st)
	}
}

func TestRotateKEK(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	oldK, newK := newKEK(t), newKEK(t)
	kr := load(t, s, oldK)
	aad := FieldAAD("t", "c", "r")
	v1, _ := kr.Seal(PurposeField, aad, []byte("first"))
	if _, err := kr.Rotate(ctx, s, PurposeField); err != nil {
		t.Fatal(err)
	}
	v2, _ := kr.Seal(PurposeField, aad, []byte("second"))
	if _, err := kr.Rotate(ctx, s, PurposeField); err != nil {
		t.Fatal(err)
	}
	if err := kr.Retire(ctx, s, PurposeField, 1); err != nil {
		t.Fatal(err)
	}
	mac, _ := kr.MAC(PurposeToken, []byte("bearer|x"))

	if _, err := RotateKEK(ctx, s, oldK, oldK); err == nil {
		t.Fatal("rotation to the same KEK accepted")
	}
	if _, err := RotateKEK(ctx, s, newKEK(t), newK); !errors.Is(err, ErrWrongKEK) {
		t.Fatalf("rotation from a KEK that does not wrap the keys: %v", err)
	}
	n, err := RotateKEK(ctx, s, oldK, newK)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 { // field v1 (retired), v2, v3; token v1; backup-meta v1
		t.Fatalf("rewrapped %d keys", n)
	}
	for _, p := range Purposes {
		for _, r := range keyRows(t, s, p) {
			if r.kekID != newK.ID() {
				t.Fatalf("%s v%d still wrapped by %s", p, r.version, r.kekID)
			}
		}
	}
	if _, err := Load(ctx, s, oldK); !errors.Is(err, ErrWrongKEK) {
		t.Fatalf("old KEK still loads: %v", err)
	}
	kr2 := load(t, s, newK)
	if pt, err := kr2.Open(PurposeField, aad, v2); err != nil || string(pt) != "second" {
		t.Fatalf("open after KEK rotation: %v", err)
	}
	if _, err := kr2.Open(PurposeField, aad, v1); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retired key loaded after KEK rotation: %v", err)
	}
	if m, _ := kr2.MAC(PurposeToken, []byte("bearer|x")); !bytes.Equal(m, mac) {
		t.Fatal("KEK rotation changed the data keys")
	}
}

func TestConcurrentUseDuringRotation(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	kr := load(t, s, newKEK(t))
	aad := FieldAAD("t", "c", "r")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				sealed, err := kr.Seal(PurposeField, aad, []byte("x"))
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := kr.Open(PurposeField, aad, sealed); err != nil {
					t.Error(err)
					return
				}
				if _, err := kr.MACAll(PurposeToken, []byte("y")); err != nil {
					t.Error(err)
					return
				}
				_ = kr.Status()
			}
		})
	}
	for range 3 {
		if _, err := kr.Rotate(ctx, s, PurposeField); err != nil {
			t.Error(err)
		}
		if _, err := kr.Rotate(ctx, s, PurposeToken); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
	if kr.ActiveVersion(PurposeField) != 4 || kr.ActiveVersion(PurposeToken) != 4 {
		t.Fatalf("versions %d %d", kr.ActiveVersion(PurposeField), kr.ActiveVersion(PurposeToken))
	}
}
