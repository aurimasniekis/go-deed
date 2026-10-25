package authn

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

func TestCurrentParamsMatchDesign(t *testing.T) {
	// ADR-005: Argon2id m=64 MiB, t=3, p=2, 16-byte salt, 32-byte key.
	want := Params{Version: 1, Memory: 64 * 1024, Time: 3, Threads: 2, KeyLen: 32, SaltLen: 16}
	if CurrentParams != want {
		t.Fatalf("CurrentParams = %+v, want %+v", CurrentParams, want)
	}
	if ParamSets[CurrentParams.Version] != CurrentParams {
		t.Fatal("CurrentParams is not registered in ParamSets")
	}
}

func TestHashPasswordEncodesParameters(t *testing.T) {
	pw := []byte(goodPassword)
	phc, err := HashPassword(pw, CurrentParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("PHC string %q does not encode the design parameters", phc)
	}
	parts := strings.Split(phc, "$")
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) != 16 || len(key) != 32 {
		t.Fatalf("salt %d bytes, key %d bytes (%v %v)", len(salt), len(key), err1, err2)
	}
	if ok, err := VerifyPassword(pw, phc); !ok || err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if ok, err := VerifyPassword([]byte(goodPassword+"!"), phc); ok || err != nil {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(dummyHash(), "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatal("the dummy hash must cost the same as a real one")
	}
}

func TestHashPasswordSaltsAndHonoursParams(t *testing.T) {
	a, _ := HashPassword([]byte(goodPassword), fastParams)
	b, _ := HashPassword([]byte(goodPassword), fastParams)
	if a == b {
		t.Fatal("two hashes of the same password are identical: the salt is not random")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("params not encoded: %s", a)
	}
	// Verification uses the parameters in the hash, not the current ones.
	if ok, err := VerifyPassword([]byte(goodPassword), a); !ok || err != nil {
		t.Fatalf("verify with non-current params: %v", err)
	}
	if ok, _ := VerifyPassword([]byte("Correct horse battery staple"), a); ok {
		t.Fatal("verification is case-insensitive")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	good, _ := HashPassword([]byte(goodPassword), fastParams)
	parts := strings.Split(good, "$")
	salt, key := parts[4], parts[5]
	bad := map[string]string{
		"empty":         "",
		"bcrypt":        "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234",
		"argon2i":       "$argon2i$v=19$m=1024,t=1,p=1$" + salt + "$" + key,
		"old version":   "$argon2id$v=16$m=1024,t=1,p=1$" + salt + "$" + key,
		"missing part":  "$argon2id$v=19$m=1024,t=1,p=1$" + salt,
		"zero memory":   "$argon2id$v=19$m=0,t=1,p=1$" + salt + "$" + key,
		"zero time":     "$argon2id$v=19$m=1024,t=0,p=1$" + salt + "$" + key,
		"zero threads":  "$argon2id$v=19$m=1024,t=1,p=0$" + salt + "$" + key,
		"huge memory":   "$argon2id$v=19$m=8388608,t=1,p=1$" + salt + "$" + key,
		"huge time":     "$argon2id$v=19$m=1024,t=1000,p=1$" + salt + "$" + key,
		"garbled param": "$argon2id$v=19$memory=1024$" + salt + "$" + key,
		"short salt":    "$argon2id$v=19$m=1024,t=1,p=1$" + base64.RawStdEncoding.EncodeToString([]byte("1234567")) + "$" + key,
		"bad salt":      "$argon2id$v=19$m=1024,t=1,p=1$!!!$" + key,
		"short key":     "$argon2id$v=19$m=1024,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 8)),
		"long key":      "$argon2id$v=19$m=1024,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 65)),
	}
	for name, phc := range bad {
		ok, err := VerifyPassword([]byte(goodPassword), phc)
		if ok || !errors.Is(err, errBadHash) {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestNormalizePassword(t *testing.T) {
	// NFKC: the "ﬁ" ligature and full-width digits normalize to their ASCII forms.
	got, err := NormalizePassword("ﬁsh １２３ and chips", 12)
	if err != nil || string(got) != "fish 123 and chips" {
		t.Fatalf("NFKC: %q %v", got, err)
	}
	if a, _ := NormalizePassword("ﬁsh １２３ and chips", 1); string(a) != string(got) {
		t.Fatal("normalization depends on the minimum")
	}
	cases := map[string]struct {
		pw  string
		min int
	}{
		"too short": {"short", 12},
		"NUL":       {"correct horse\x00battery", 1},
		"too long":  {strings.Repeat("a", MaxPasswordBytes+1), 1},
		"huge":      {strings.Repeat("a", 5*MaxPasswordBytes), 1},
		// "㌀" is 3 bytes but normalizes to 12 ("アパート"): the bound applies after normalization.
		"too long after NFKC": {strings.Repeat("㌀", 100), 1},
	}
	for name, tc := range cases {
		if _, err := NormalizePassword(tc.pw, tc.min); apperr.CodeOf(err) != apperr.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NormalizePassword(strings.Repeat("a", MaxPasswordBytes), 12); err != nil {
		t.Fatalf("exactly the maximum: %v", err)
	}
	// Length is counted in bytes: four 3-byte characters are 12 bytes.
	if _, err := NormalizePassword("日本語日", 12); err != nil {
		t.Fatalf("12 bytes of CJK: %v", err)
	}
}

func TestCheckStrength(t *testing.T) {
	for _, pw := range []string{"aaaaaaaaaaaaaaaa", "abababababababab", "password1234", "Deed-deed-deed", "qwertyuiop12"} {
		if apperr.CodeOf(checkStrength(pw)) != apperr.CodeInvalid {
			t.Errorf("%q accepted", pw)
		}
	}
	for _, pw := range []string{goodPassword, "my password is a long sentence"} {
		if err := checkStrength(pw); err != nil {
			t.Errorf("%q rejected: %v", pw, err)
		}
	}
}
