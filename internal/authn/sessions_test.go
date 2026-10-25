package authn

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/keyring"
)

func TestSessionTokenStoredAsMAC(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "alice@example.com", "")
	res := f.login(t, "alice@example.com")
	raw, err := base64.RawURLEncoding.DecodeString(res.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("session token is not 256 random bits: %q", res.Token)
	}
	if again := f.login(t, "alice@example.com"); again.Token == res.Token {
		t.Fatal("two logins share a token")
	}
	var idMAC []byte
	var publicID, ip, ua string
	if err := f.env.Store.R().QueryRow(`SELECT id_mac, public_id, ip, user_agent FROM sessions WHERE public_id = ?`, res.Principal.SessionID).
		Scan(&idMAC, &publicID, &ip, &ua); err != nil {
		t.Fatal(err)
	}
	want, _ := f.env.Keyring.MAC(keyring.PurposeToken, []byte("session|"+res.Token))
	if !bytes.Equal(idMAC, want) {
		t.Fatal("sessions are not keyed by the token's MAC")
	}
	if bytes.Contains(idMAC, []byte(res.Token)) || publicID == res.Token {
		t.Fatal("raw session token stored")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = ? AND (CAST(id_mac AS TEXT) = ? OR public_id = ?)`, u.ID, res.Token, res.Token); n != 0 {
		t.Fatal("raw session token stored")
	}
	if ip != meta.IP || ua != meta.UserAgent {
		t.Fatalf("client meta %q %q", ip, ua)
	}
	// Unknown, empty and oversized tokens are refused without a lookup error.
	for _, tok := range []string{"", "nope", res.Token + "x", string(make([]byte, 200))} {
		if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("token %q: %v", tok, err)
		}
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	f := newFixture(t)
	f.user(t, "alice@example.com", "")
	tok := f.login(t, "alice@example.com").Token
	start := f.env.Clock.Now()
	// Activity keeps the session alive past the idle timeout.
	for range 4 {
		f.env.Clock.Advance(29 * time.Minute)
		f.auth(t, tok)
	}
	f.env.Clock.Advance(30 * time.Minute)
	f.auth(t, tok) // exactly the idle timeout is still valid
	f.env.Clock.Advance(30*time.Minute + time.Microsecond)
	if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("idle session accepted at %s: %v", f.env.Clock.Now().Sub(start), err)
	}

	// Validate does not count as activity, and reports the idle deadline.
	tok = f.login(t, "alice@example.com").Token
	loginAt := f.env.Clock.Now()
	f.env.Clock.Advance(20 * time.Minute)
	p, err := f.svc.Validate(f.ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ExpiresAt.Equal(loginAt.Add(30 * time.Minute)) {
		t.Fatalf("Validate expiry %s, want the idle deadline", p.ExpiresAt)
	}
	f.env.Clock.Advance(11 * time.Minute)
	if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Validate extended the session: %v", err)
	}
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	f := newFixture(t)
	f.user(t, "alice@example.com", "")
	res := f.login(t, "alice@example.com")
	loginAt := f.env.Clock.Now()
	if !res.Principal.ExpiresAt.Equal(loginAt.Add(12 * time.Hour)) {
		t.Fatalf("expires at %s", res.Principal.ExpiresAt)
	}
	for f.env.Clock.Now().Add(25 * time.Minute).Before(loginAt.Add(12 * time.Hour)) {
		f.env.Clock.Advance(25 * time.Minute)
		f.auth(t, res.Token)
	}
	f.env.Clock.Set(loginAt.Add(12*time.Hour - time.Microsecond))
	f.auth(t, res.Token)
	f.env.Clock.Set(loginAt.Add(12 * time.Hour))
	if _, err := f.svc.Authenticate(f.ctx, res.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session outlived the absolute lifetime: %v", err)
	}
}

func TestSessionRevocation(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "alice@example.com", "")
	f.user(t, "bob@example.com", "")
	a := f.login(t, "alice@example.com")
	b := f.login(t, "alice@example.com")
	c := f.login(t, "alice@example.com")
	bob := f.login(t, "bob@example.com")

	list, err := f.svc.ListSessions(f.ctx, a.Principal)
	if err != nil || len(list) != 3 {
		t.Fatalf("sessions: %v %v", list, err)
	}
	current := 0
	for _, s := range list {
		if s.Current {
			current++
			if s.ID != a.Principal.SessionID {
				t.Fatal("wrong session marked current")
			}
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current", current)
	}

	// A user cannot revoke someone else's session, or an unknown one.
	if err := f.svc.RevokeSession(f.ctx, a.Principal, bob.Principal.SessionID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("revoked another user's session: %v", err)
	}
	f.auth(t, bob.Token)
	if err := f.svc.RevokeSession(f.ctx, a.Principal, b.Principal.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, b.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked session accepted: %v", err)
	}
	if err := f.svc.RevokeSession(f.ctx, a.Principal, b.Principal.SessionID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("double revoke: %v", err)
	}
	// Sign out everywhere else keeps only the current session.
	if err := f.svc.RevokeOtherSessions(f.ctx, a.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, c.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("other session survived: %v", err)
	}
	f.auth(t, a.Token)
	f.auth(t, bob.Token)
	if err := f.svc.Logout(f.ctx, a.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, a.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session accepted after logout: %v", err)
	}
	if f.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, u.ID) != 0 {
		t.Fatal("alice still has live sessions")
	}
	if f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action IN ('session.revoked', 'session.revoked_others', 'session.logout') AND actor_id = ?`, u.ID) != 3 {
		t.Fatal("session revocations not audited")
	}
}

func TestSessionsEndOnAccountChanges(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MFARequiredRoles = nil })
	u := f.user(t, "alice@example.com", RoleAuditor)
	other := f.user(t, "owner@example.com", RoleOwner)
	a := f.login(t, "alice@example.com")
	// Privilege change: every session of the user must re-authenticate.
	role := RoleAdmin
	if _, err := f.svc.UpdateUser(f.ctx, audit.Actor{Type: audit.ActorUser, ID: other.ID}, u.ID, UpdateUserInput{InstallationRole: &role}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, a.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session survived a privilege change: %v", err)
	}
	b := f.login(t, "alice@example.com")
	if b.Principal.InstallationRole != RoleAdmin {
		t.Fatalf("role %q", b.Principal.InstallationRole)
	}
	// Password change keeps the current session and ends the others.
	c := f.login(t, "alice@example.com")
	if err := f.svc.SetPassword(f.ctx, b.Principal, "wrong current password", "a brand new passphrase"); apperr.CodeOf(err) != apperr.CodeUnauthorized {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := f.svc.SetPassword(f.ctx, b.Principal, goodPassword, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	f.auth(t, b.Token)
	if _, err := f.svc.Authenticate(f.ctx, c.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("other session survived a password change: %v", err)
	}
	// Disabling the account ends every session.
	status := "disabled"
	if _, err := f.svc.UpdateUser(f.ctx, audit.System, u.ID, UpdateUserInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, b.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled user's session accepted: %v", err)
	}
}

// Rotating the token key keeps API tokens and recovery codes working (MACAll) but signs every browser session out;
// docs/OPERATIONS.md documents both.
func TestTokenKeyRotation(t *testing.T) {
	f := newFixture(t)
	f.user(t, "owner@example.com", RoleOwner)
	_, codes, full := f.enroll(t, f.login(t, "owner@example.com").Principal)
	_, rawTok, err := f.svc.CreateAPIToken(f.ctx, f.auth(t, full), CreateAPITokenInput{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Keyring.Rotate(f.ctx, f.env.Store, keyring.PurposeToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, full); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session after token-key rotation: %v", err)
	}
	if _, err := f.svc.AuthenticateToken(f.ctx, rawTok, "192.0.2.10"); err != nil {
		t.Fatalf("API token after token-key rotation: %v", err)
	}
	p := f.login(t, "owner@example.com").Principal
	if _, err := f.svc.VerifyMFA(f.ctx, p, codes[0], meta); err != nil {
		t.Fatalf("recovery code after token-key rotation: %v", err)
	}
}

func TestCSRFAndRecentAuth(t *testing.T) {
	f := newFixture(t)
	f.user(t, "alice@example.com", "")
	a := f.auth(t, f.login(t, "alice@example.com").Token)
	b := f.auth(t, f.login(t, "alice@example.com").Token)
	if !a.IsSession() || a.CSRFToken() == "" || a.CSRFToken() == b.CSRFToken() {
		t.Fatal("each session needs its own CSRF token")
	}
	if !a.VerifyCSRF(a.CSRFToken()) || a.VerifyCSRF(b.CSRFToken()) || a.VerifyCSRF("") {
		t.Fatal("VerifyCSRF")
	}
	if (&Principal{}).VerifyCSRF("") || (&Principal{TokenID: "tok"}).IsSession() {
		t.Fatal("principals without a session secret have no CSRF token")
	}
	if a.Actor() != (audit.Actor{Type: audit.ActorUser, ID: a.UserID}) {
		t.Fatalf("actor %+v", a.Actor())
	}
	if err := f.svc.RequireRecentAuth(a); err != nil {
		t.Fatal(err)
	}
	f.env.Clock.Advance(5*time.Minute + time.Second)
	if err := f.svc.RequireRecentAuth(a); apperr.CodeOf(err) != apperr.CodeReauth {
		t.Fatalf("stale auth: %v", err)
	}
	if err := f.svc.RequireRecentAuth(&Principal{TokenID: "tok_1"}); err != nil {
		t.Fatalf("tokens are exempt: %v", err)
	}
	// Re-authentication rotates the session and refreshes the auth time.
	tok, err := f.svc.Reauth(f.ctx, a, goodPassword, "", meta)
	if err != nil {
		t.Fatal(err)
	}
	fresh := f.auth(t, tok)
	if err := f.svc.RequireRecentAuth(fresh); err != nil {
		t.Fatalf("after reauth: %v", err)
	}
	if fresh.SessionID == a.SessionID {
		t.Fatal("reauth did not rotate the session")
	}
	if _, err := f.svc.Reauth(f.ctx, fresh, "wrong password here", "", meta); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reauth with a wrong password: %v", err)
	}
}

func TestSessionCookie(t *testing.T) {
	c := SessionCookie(true, "tok", 3600)
	if c.Name != "__Host-deed_session" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("production cookie %+v", c)
	}
	if d := SessionCookie(false, "tok", -1); d.Name != "deed_session" || d.Secure || d.MaxAge != -1 {
		t.Fatalf("development cookie %+v", d)
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	if TokenFromRequest(r) != "" {
		t.Fatal("token without a cookie")
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "abc"})
	if TokenFromRequest(r) != "abc" {
		t.Fatal("token from cookie")
	}
}

func TestPruneSessions(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "alice@example.com", "")
	old := f.login(t, "alice@example.com")
	if err := f.svc.Logout(f.ctx, old.Principal); err != nil {
		t.Fatal(err)
	}
	f.env.Clock.Advance(25 * time.Hour)
	live := f.login(t, "alice@example.com")
	if err := f.svc.PruneSessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID); n != 1 {
		t.Fatalf("%d sessions after pruning", n)
	}
	f.auth(t, live.Token)
}
