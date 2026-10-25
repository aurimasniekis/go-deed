package authn

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/keyring"
)

var rfcSecret = []byte("12345678901234567890")

func TestTOTPRFC6238Vectors(t *testing.T) {
	// RFC 6238 Appendix B (SHA-1); the 6-digit code is the low six digits of the 8-digit reference value.
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range vectors {
		if got := TOTPCode(rfcSecret, TOTPStep(time.Unix(ts, 0))); got != want {
			t.Errorf("t=%d: got %s, want %s", ts, got, want)
		}
	}
}

func TestVerifyTOTPWindowAndReuse(t *testing.T) {
	now := time.Unix(1_800_000_015, 0)
	cur := TOTPStep(now)
	code := func(d int64) string { return TOTPCode(rfcSecret, cur+d) }
	seen := map[string]bool{}
	for d := int64(-2); d <= 2; d++ {
		if seen[code(d)] {
			t.Fatal("fixture codes collide; pick another time")
		}
		seen[code(d)] = true
	}
	cases := []struct {
		name     string
		code     string
		lastStep int64
		ok       bool
		step     int64
	}{
		{"current", code(0), 0, true, cur},
		{"previous step", code(-1), 0, true, cur - 1},
		{"next step", code(1), 0, true, cur + 1},
		{"two steps old", code(-2), 0, false, 0},
		{"two steps ahead", code(2), 0, false, 0},
		{"surrounding whitespace", " " + code(0) + "\n", 0, true, cur},
		{"reused step", code(0), cur, false, 0},
		{"older than last use", code(-1), cur, false, 0},
		{"newer than last use", code(1), cur, true, cur + 1},
		{"current after previous use", code(0), cur - 1, true, cur},
		{"wrong code", "000000", 0, code(0) == "000000", cur},
		{"too short", code(0)[:5], 0, false, 0},
		{"too long", code(0) + "0", 0, false, 0},
		{"empty", "", 0, false, 0},
	}
	for _, tc := range cases {
		step, ok := VerifyTOTP(rfcSecret, tc.code, now, tc.lastStep)
		if ok != tc.ok || (ok && step != tc.step) {
			t.Errorf("%s: step=%d ok=%v, want step=%d ok=%v", tc.name, step, ok, tc.step, tc.ok)
		}
	}
	if _, ok := VerifyTOTP([]byte("another secret value"), code(0), now, 0); ok {
		t.Error("code accepted for another secret")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI(rfcSecret, "Deed", "alice@example.com")
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("uri %q: %v", uri, err)
	}
	if u.Path != "/Deed:alice@example.com" {
		t.Fatalf("label %q", u.Path)
	}
	q := u.Query()
	if q.Get("secret") != EncodeTOTPSecret(rfcSecret) || q.Get("issuer") != "Deed" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatalf("query %v", q)
	}
	if EncodeTOTPSecret(rfcSecret) != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" || strings.Contains(EncodeTOTPSecret([]byte{1}), "=") {
		t.Fatal("secret must be unpadded base32")
	}
	s1, _ := NewTOTPSecret()
	s2, _ := NewTOTPSecret()
	if len(s1) != 20 || bytes.Equal(s1, s2) {
		t.Fatal("TOTP secrets must be 160 random bits")
	}
}

func TestEnrollmentAndSecondFactorLogin(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "owner@example.com", RoleOwner)
	first := f.login(t, "owner@example.com")
	if !first.MustEnroll || first.MFAPending || !first.Principal.Limited || !first.Principal.NeedsEnroll {
		t.Fatalf("owner without TOTP must enroll first: %+v", first)
	}
	p := first.Principal
	if _, err := f.svc.VerifyMFA(f.ctx, p, "123456", meta); apperr.CodeOf(err) != apperr.CodePrecondition {
		t.Fatalf("verify before enrollment: %v", err)
	}
	if _, _, err := f.svc.ConfirmTOTP(f.ctx, p, "123456", meta); !errors.Is(err, errStartEnrollment) {
		t.Fatalf("confirm before begin: %v", err)
	}
	en, err := f.svc.BeginTOTP(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b32.DecodeString(en.Secret)
	if !strings.Contains(en.URI, "secret="+en.Secret) || !strings.Contains(en.URI, "Deed:owner@example.com") {
		t.Fatalf("provisioning URI %q", en.URI)
	}
	wrong := TOTPCode(secret, step(f)+5)
	if _, _, err := f.svc.ConfirmTOTP(f.ctx, p, wrong, meta); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("confirm with a wrong code: %v", err)
	}
	enrollStep := step(f)
	codes, full, err := f.svc.ConfirmTOTP(f.ctx, p, TOTPCode(secret, enrollStep), meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("%d recovery codes", len(codes))
	}
	// Enrollment rotates the session: the limited token is dead, the new one is full and MFA-verified.
	if _, err := f.svc.Authenticate(f.ctx, first.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enrollment session survived rotation: %v", err)
	}
	fp := f.auth(t, full)
	if fp.Limited || fp.NeedsEnroll || fp.MFAAt == nil || fp.UserID != u.ID {
		t.Fatalf("full session after enrollment: %+v", fp)
	}
	// The secret is sealed and bound to the user.
	var enc []byte
	var last int64
	if err := f.env.Store.R().QueryRow(`SELECT secret_enc, last_step FROM mfa_totp WHERE user_id = ?`, u.ID).Scan(&enc, &last); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, secret) || last != enrollStep {
		t.Fatalf("stored TOTP: plaintext=%v last_step=%d", bytes.Contains(enc, secret), last)
	}
	if got, err := f.env.Keyring.Open(keyring.PurposeField, totpAAD(u.ID), enc); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("sealed secret: %v", err)
	}
	if f.count(t, `SELECT COUNT(*) FROM mfa_totp_pending WHERE user_id = ?`, u.ID) != 0 {
		t.Fatal("pending enrollment left behind")
	}

	// A new login now needs the second factor.
	second := f.login(t, "owner@example.com")
	if !second.MFAPending || second.MustEnroll || !second.Principal.Limited {
		t.Fatalf("second login: %+v", second)
	}
	// The code used for enrollment cannot be replayed.
	if _, err := f.svc.VerifyMFA(f.ctx, second.Principal, TOTPCode(secret, enrollStep), meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("replayed enrollment code: %v", err)
	}
	advanceToNextStep(f)
	code := TOTPCode(secret, step(f))
	tok, err := f.svc.VerifyMFA(f.ctx, second.Principal, code, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, second.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("limited session survived MFA: %v", err)
	}
	if p := f.auth(t, tok); p.Limited || p.MFAAt == nil {
		t.Fatalf("session after MFA: %+v", p)
	}
	// Same step again, on a fresh login: refused.
	third := f.login(t, "owner@example.com")
	if _, err := f.svc.VerifyMFA(f.ctx, third.Principal, code, meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("step reuse: %v", err)
	}
	// The next step's code is accepted early (clock skew window).
	if _, err := f.svc.VerifyMFA(f.ctx, third.Principal, TOTPCode(secret, step(f)+1), meta); err != nil {
		t.Fatalf("adjacent step: %v", err)
	}
	// ...which in turn burns the current step.
	fourth := f.login(t, "owner@example.com")
	if _, err := f.svc.VerifyMFA(f.ctx, fourth.Principal, TOTPCode(secret, step(f)), meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("step older than the last accepted one: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'auth.mfa' AND outcome = 'failure' AND actor_id = ?`, u.ID); n != 3 {
		t.Fatalf("failed second factors audited %d times", n)
	}
}

func TestSecondFactorGates(t *testing.T) {
	f := newFixture(t)
	f.user(t, "admin@example.com", RoleAdmin)
	_, _, full := f.enroll(t, f.login(t, "admin@example.com").Principal)
	limited := f.login(t, "admin@example.com").Principal
	// A session that still has to verify its factor cannot replace it or mint tokens.
	if _, err := f.svc.BeginTOTP(f.ctx, limited); !errors.Is(err, ErrCompleteMFAFirst) {
		t.Fatalf("begin on a limited session: %v", err)
	}
	if _, _, err := f.svc.CreateAPIToken(f.ctx, limited, CreateAPITokenInput{Name: "x"}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("token from a limited session: %v", err)
	}
	// Replacing a factor needs a recent sign-in.
	f.env.Clock.Advance(6 * time.Minute)
	fp := f.auth(t, full)
	if _, err := f.svc.BeginTOTP(f.ctx, fp); apperr.CodeOf(err) != apperr.CodeReauth {
		t.Fatalf("begin without recent auth: %v", err)
	}
	// MFA is mandatory for admins.
	if err := f.svc.DisableTOTP(f.ctx, fp); apperr.CodeOf(err) != apperr.CodeReauth {
		t.Fatalf("disable without recent auth: %v", err)
	}
	fp.AuthAt = f.env.Clock.Now()
	if err := f.svc.DisableTOTP(f.ctx, fp); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("disable mandatory MFA: %v", err)
	}
	// An expired pending enrollment cannot be confirmed.
	en, err := f.svc.BeginTOTP(f.ctx, fp)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b32.DecodeString(en.Secret)
	f.env.Clock.Advance(TOTPPendingTTL)
	fp.AuthAt = f.env.Clock.Now()
	if _, _, err := f.svc.ConfirmTOTP(f.ctx, fp, TOTPCode(secret, step(f)), meta); !errors.Is(err, errStartEnrollment) {
		t.Fatalf("expired enrollment: %v", err)
	}
}

var recoveryShape = regexp.MustCompile(`^[0-9abcdefghjkmnpqrstvwxyz]{5}-[0-9abcdefghjkmnpqrstvwxyz]{5}$`)

func TestRecoveryCodes(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "owner@example.com", RoleOwner)
	other := f.user(t, "other@example.com", RoleOwner)
	_, codes, full := f.enroll(t, f.login(t, "owner@example.com").Principal)
	_, otherCodes, _ := f.enroll(t, f.login(t, "other@example.com").Principal)

	seen := map[string]bool{}
	for _, c := range codes {
		if !recoveryShape.MatchString(c) || seen[c] {
			t.Fatalf("recovery code %q malformed or repeated", c)
		}
		seen[c] = true
	}
	// Stored as keyed MACs, never in plaintext.
	rows, err := f.env.Store.R().Query(`SELECT code_mac FROM recovery_codes WHERE user_id = ?`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]bool{}
	for rows.Next() {
		var m []byte
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		stored[string(m)] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(stored) != RecoveryCodeCount {
		t.Fatalf("%d stored codes", len(stored))
	}
	for _, c := range codes {
		m, _ := f.env.Keyring.MAC(keyring.PurposeToken, []byte("recovery|"+u.ID+"|"+normRecovery(c)))
		if !stored[string(m)] || stored[c] || stored[normRecovery(c)] {
			t.Fatalf("code %q not stored as its MAC", c)
		}
	}

	login := func() *Principal { return f.login(t, "owner@example.com").Principal }
	// Codes are accepted without the dash and in upper case.
	if _, err := f.svc.VerifyMFA(f.ctx, login(), strings.ToUpper(strings.ReplaceAll(codes[0], "-", "")), meta); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	if n, _ := f.svc.RemainingRecoveryCodes(f.ctx, u.ID); n != RecoveryCodeCount-1 {
		t.Fatalf("remaining %d", n)
	}
	if f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'auth.recovery_code_used' AND actor_id = ?`, u.ID) != 1 {
		t.Fatal("recovery code use not audited")
	}
	// Single use.
	if _, err := f.svc.VerifyMFA(f.ctx, login(), codes[0], meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused recovery code: %v", err)
	}
	// Bound to the user.
	if _, err := f.svc.VerifyMFA(f.ctx, login(), otherCodes[0], meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("another user's recovery code: %v", err)
	}
	if n, _ := f.svc.RemainingRecoveryCodes(f.ctx, other.ID); n != RecoveryCodeCount {
		t.Fatalf("other user's codes consumed: %d", n)
	}
	// Regeneration replaces every code.
	fresh, err := f.svc.RegenerateRecoveryCodes(f.ctx, f.auth(t, full))
	if err != nil || len(fresh) != RecoveryCodeCount {
		t.Fatalf("regenerate: %v", err)
	}
	if _, err := f.svc.VerifyMFA(f.ctx, login(), codes[1], meta); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("old code after regeneration: %v", err)
	}
	if n, _ := f.svc.RemainingRecoveryCodes(f.ctx, u.ID); n != RecoveryCodeCount {
		t.Fatalf("remaining after regeneration %d", n)
	}
}

func TestSecondFactorThrottle(t *testing.T) {
	f := newFixture(t)
	f.user(t, "owner@example.com", RoleOwner)
	secret, _, _ := f.enroll(t, f.login(t, "owner@example.com").Principal)
	advanceToNextStep(f)
	p := f.login(t, "owner@example.com").Principal
	bad := TOTPCode(secret, step(f)+3)
	for i := range throttleFree + 1 {
		if _, err := f.svc.VerifyMFA(f.ctx, p, bad, meta); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	good := TOTPCode(secret, step(f))
	if _, err := f.svc.VerifyMFA(f.ctx, p, good, meta); !errors.Is(err, ErrThrottled) {
		t.Fatalf("throttle not applied to the second factor: %v", err)
	}
	f.env.Clock.Advance(time.Second)
	if _, err := f.svc.VerifyMFA(f.ctx, p, TOTPCode(secret, step(f)), meta); err != nil {
		t.Fatalf("after the delay: %v", err)
	}
	if f.count(t, `SELECT COUNT(*) FROM throttles WHERE key = ?`, mfaKey(p.UserID)) != 0 {
		t.Fatal("success must clear the MFA throttle")
	}
}
