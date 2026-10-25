package authn

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// fastParams keeps service tests quick; the production parameters are checked in password_test.go.
var fastParams = Params{Version: CurrentParams.Version, Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

const goodPassword = "correct horse battery staple"

var meta = SessionMeta{IP: "192.0.2.10", UserAgent: "authn-test"}

type fixture struct {
	env *testenv.Env
	svc *Service
	ctx context.Context
}

func newFixture(t *testing.T, mods ...func(*Options)) *fixture {
	t.Helper()
	env := testenv.New(t)
	opts := Options{Clock: env.Clock, Params: &fastParams, LocalEnabled: true, MFARequiredRoles: []string{RoleOwner, RoleAdmin}}
	for _, m := range mods {
		m(&opts)
	}
	return &fixture{env: env, svc: New(env.Store, env.Keyring, env.Audit, opts), ctx: context.Background()}
}

func (f *fixture) user(t *testing.T, email, role string) *User {
	t.Helper()
	u, err := f.svc.CreateUser(f.ctx, audit.System, CreateUserInput{Email: email, DisplayName: email, Password: goodPassword, InstallationRole: role})
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u
}

func (f *fixture) login(t *testing.T, email string) *LoginResult {
	t.Helper()
	res, err := f.svc.Login(f.ctx, email, goodPassword, meta)
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return res
}

func (f *fixture) auth(t *testing.T, token string) *Principal {
	t.Helper()
	p, err := f.svc.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Store.R().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// enroll completes TOTP enrollment for p and returns the secret, the recovery codes and the rotated session token.
func (f *fixture) enroll(t *testing.T, p *Principal) ([]byte, []string, string) {
	t.Helper()
	en, err := f.svc.BeginTOTP(f.ctx, p)
	if err != nil {
		t.Fatalf("begin TOTP: %v", err)
	}
	secret, err := b32.DecodeString(en.Secret)
	if err != nil || len(secret) != totpSecret {
		t.Fatalf("enrollment secret %q: %v", en.Secret, err)
	}
	codes, token, err := f.svc.ConfirmTOTP(f.ctx, p, TOTPCode(secret, TOTPStep(f.env.Clock.Now())), meta)
	if err != nil {
		t.Fatalf("confirm TOTP: %v", err)
	}
	return secret, codes, token
}

func step(f *fixture) int64 { return TOTPStep(f.env.Clock.Now()) }

func advanceToNextStep(f *fixture) { f.env.Clock.Advance(totpPeriod * time.Second) }
