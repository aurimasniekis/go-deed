package authn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/store"
)

func TestLoginOutcomes(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "alice@example.com", "")
	res, err := f.svc.Login(f.ctx, "  Alice@Example.COM ", goodPassword, meta)
	if err != nil {
		t.Fatal(err)
	}
	if res.MFAPending || res.MustEnroll || res.Principal.Limited || res.Principal.UserID != u.ID || res.Principal.Method != MethodPassword {
		t.Fatalf("login without MFA policy: %+v", res)
	}
	if got, _ := f.svc.GetUser(f.ctx, u.ID); got.LastLoginAt == nil || !got.LastLoginAt.Equal(f.env.Clock.Now()) {
		t.Fatalf("last login not recorded: %+v", got.LastLoginAt)
	}
	disabled := f.user(t, "bob@example.com", "")
	status := "disabled"
	if _, err := f.svc.UpdateUser(f.ctx, audit.System, disabled.ID, UpdateUserInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	// Wrong password, unknown account, disabled account and malformed input all look the same.
	// Each attempt comes from its own address so that the source throttle stays out of the way.
	for name, tc := range map[string]struct{ email, pw, ip string }{
		"wrong password":   {"alice@example.com", goodPassword + "?", "198.51.100.1"},
		"unknown account":  {"nobody@example.com", goodPassword, "198.51.100.2"},
		"disabled account": {"bob@example.com", goodPassword, "198.51.100.3"},
		"malformed email":  {"not an email", goodPassword, "198.51.100.4"},
		"empty password":   {"alice@example.com", "", "198.51.100.5"},
	} {
		if _, err := f.svc.Login(f.ctx, tc.email, tc.pw, SessionMeta{IP: tc.ip}); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'auth.login' AND outcome = 'failure' AND reason_code = 'invalid_credentials'`); n != 5 {
		t.Fatalf("%d failed logins audited", n)
	}
	off := newFixture(t, func(o *Options) { o.LocalEnabled = false })
	if _, err := off.svc.Login(off.ctx, "alice@example.com", goodPassword, meta); !errors.Is(err, ErrLocalDisabled) {
		t.Fatalf("local login disabled: %v", err)
	}
}

func TestRehashOnLogin(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "alice@example.com", "")
	phc := func() (string, int64) {
		var h string
		var v int64
		if err := f.env.Store.R().QueryRow(`SELECT password_phc, params_version FROM local_credentials WHERE user_id = ?`, u.ID).Scan(&h, &v); err != nil {
			t.Fatal(err)
		}
		return h, v
	}
	before, v := phc()
	if v != int64(fastParams.Version) {
		t.Fatalf("stored version %d", v)
	}
	upgraded := Params{Version: fastParams.Version + 1, Memory: 2048, Time: 2, Threads: 1, KeyLen: 32, SaltLen: 16}
	svc2 := New(f.env.Store, f.env.Keyring, f.env.Audit, Options{Clock: f.env.Clock, Params: &upgraded, LocalEnabled: true})

	// A failed login never rewrites the hash.
	if _, err := svc2.Login(f.ctx, "alice@example.com", "wrong password here", meta); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if h, _ := phc(); h != before {
		t.Fatal("hash rewritten by a failed login")
	}
	if _, err := svc2.Login(f.ctx, "alice@example.com", goodPassword, meta); err != nil {
		t.Fatal(err)
	}
	after, v := phc()
	if v != int64(upgraded.Version) || after == before || !strings.Contains(after, "$m=2048,t=2,p=1$") {
		t.Fatalf("not rehashed with the current parameters: v%d %s", v, after)
	}
	// Once current, logins leave the hash alone; the old service still verifies (parameters live in the hash).
	if _, err := svc2.Login(f.ctx, "alice@example.com", goodPassword, meta); err != nil {
		t.Fatal(err)
	}
	if again, _ := phc(); again != after {
		t.Fatal("current hash rewritten")
	}
	if _, err := f.svc.Login(f.ctx, "alice@example.com", goodPassword, meta); err != nil {
		t.Fatal(err)
	}
}

func TestThrottleSchedule(t *testing.T) {
	f := newFixture(t)
	key := accountKey("alice@example.com")
	fail := func() (failures int64, delay time.Duration) {
		t.Helper()
		if err := f.env.Store.W().Tx(f.ctx, func(tx *store.Tx) error { return recordFailure(f.ctx, tx, key) }); err != nil {
			t.Fatal(err)
		}
		var next int64
		if err := f.env.Store.R().QueryRow(`SELECT failures, next_allowed_at FROM throttles WHERE key = ?`, key).Scan(&failures, &next); err != nil {
			t.Fatal(err)
		}
		return failures, time.UnixMicro(next).Sub(f.env.Clock.Now())
	}
	throttled := func() bool {
		t.Helper()
		th, err := f.svc.throttled(f.ctx, "ip:unrelated", key)
		if err != nil {
			t.Fatal(err)
		}
		return th
	}
	// 3 free failures, then 1 s doubling per failure, capped at 15 minutes.
	want := []time.Duration{0, 0, 0, 1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 512 * time.Second, 15 * time.Minute, 15 * time.Minute}
	for i, d := range want {
		n, got := fail()
		if n != int64(i+1) || got != d {
			t.Fatalf("failure %d: count %d, delay %s, want %s", i+1, n, got, d)
		}
		if th := throttled(); th != (d > 0) {
			t.Fatalf("failure %d: throttled=%v with delay %s", i+1, th, d)
		}
	}
	// The delay never exceeds the cap however many failures pile up.
	for range 40 {
		fail()
	}
	if n, d := fail(); d != throttleMax || n != int64(len(want)+41) {
		t.Fatalf("after many failures: count %d delay %s", n, d)
	}
	f.env.Clock.Advance(throttleMax)
	if throttled() {
		t.Fatal("still throttled after the maximum delay: there must be no permanent lockout")
	}
	// Counters decay after a day without failures.
	f.env.Clock.Advance(throttleDecay)
	if n, d := fail(); n != 1 || d != 0 {
		t.Fatalf("after decay: count %d delay %s", n, d)
	}
	// ...but not before.
	f.env.Clock.Advance(throttleDecay - time.Second)
	if n, _ := fail(); n != 2 {
		t.Fatalf("counter decayed early: %d", n)
	}
}

func TestThrottleKeys(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":             "ip:192.0.2.10",
		"::ffff:192.0.2.10":      "ip:192.0.2.10",
		"2001:db8:1:2:3:4:5:6":   "ip:2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1":   "ip:2001:db8:1:2::/64",
		"2001:db8:1:3::1":        "ip:2001:db8:1:3::/64",
		"not-an-ip":              "ip:not-an-ip",
		"fe80::1%eth0":           "ip:fe80::/64",
		"2001:0db8:0001:0002::7": "ip:2001:db8:1:2::/64",
	}
	for in, want := range cases {
		if got := ipKey(in); got != want {
			t.Errorf("ipKey(%q) = %q, want %q", in, got, want)
		}
	}
	if accountKey(" Alice@Example.com ") != accountKey("alice@example.com") {
		t.Error("account key must ignore case and surrounding space")
	}
}

func TestLoginThrottlePerAccountAndSource(t *testing.T) {
	f := newFixture(t)
	f.user(t, "alice@example.com", "")
	login := func(email, pw, ip string) error {
		_, err := f.svc.Login(f.ctx, email, pw, SessionMeta{IP: ip})
		return err
	}
	// Three failures are free; a success clears the account counter (not the source's).
	for i := range throttleFree {
		if err := login("alice@example.com", "wrong password "+string(rune('a'+i)), "192.0.2.10"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if err := login("alice@example.com", goodPassword, "192.0.2.10"); err != nil {
		t.Fatalf("login after the free failures: %v", err)
	}
	if f.count(t, `SELECT COUNT(*) FROM throttles WHERE key = ?`, accountKey("alice@example.com")) != 0 {
		t.Fatal("account counter not cleared by a successful login")
	}
	if f.count(t, `SELECT failures FROM throttles WHERE key = ?`, ipKey("192.0.2.10")) != throttleFree {
		t.Fatal("source counter must survive a successful login")
	}

	// Per account: failures from many sources still throttle the account.
	for i := range throttleFree + 1 {
		ip := "203.0.113." + string(rune('1'+i))
		if err := login("alice@example.com", "wrong password here", ip); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if err := login("alice@example.com", goodPassword, "198.51.100.200"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("account not throttled: %v", err)
	}
	f.env.Clock.Advance(time.Second)
	if err := login("alice@example.com", goodPassword, "198.51.100.200"); err != nil {
		t.Fatalf("after one second: %v", err)
	}

	// Per source: failures against many accounts throttle the /64 for every account.
	for i := range throttleFree + 1 {
		u := string(rune('a'+i)) + "-user@example.com"
		f.user(t, u, "")
		if err := login(u, "wrong password here", "2001:db8:aa:bb::"+string(rune('1'+i))); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if err := login("alice@example.com", goodPassword, "2001:db8:aa:bb:ffff::9"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("source /64 not throttled: %v", err)
	}
	if err := login("alice@example.com", goodPassword, "2001:db8:aa:cc::1"); err != nil {
		t.Fatalf("another /64 must not be throttled: %v", err)
	}
}

func TestLoginRespectsContext(t *testing.T) {
	f := newFixture(t)
	f.user(t, "alice@example.com", "")
	// Hold every Argon2id slot: a login whose context ends gives up instead of waiting forever.
	for range cap(f.svc.sem) {
		f.svc.sem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(f.ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := f.svc.Login(ctx, "alice@example.com", goodPassword, meta); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("login with all slots busy: %v", err)
	}
}
