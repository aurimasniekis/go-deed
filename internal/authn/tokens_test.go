package authn

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

func TestValidTokenShape(t *testing.T) {
	secret := strings.Repeat("aZ0", 14) + "x" // 43 base62 characters
	cases := map[string]bool{
		APITokenPrefix + secret:            true,
		SourceTokenPrefix + secret:         true,
		"deed_xx_" + secret:                false,
		APITokenPrefix + secret[:42]:       false,
		APITokenPrefix + secret + "a":      false,
		APITokenPrefix + secret[:42] + "-": false,
		APITokenPrefix + secret[:42] + "_": false,
		SetupTokenPrefix + secret:          false,
		"":                                 false,
		"DEED_AT_" + secret:                false,
	}
	for raw, want := range cases {
		if got := ValidTokenShape(raw); got != want {
			t.Errorf("ValidTokenShape(%q) = %v", raw, got)
		}
	}
	seen := map[string]bool{}
	for range 20 {
		s := newTokenSecret()
		if len(s) != tokenSecretLen || !ValidTokenShape(APITokenPrefix+s) || seen[s] {
			t.Fatalf("secret %q", s)
		}
		seen[s] = true
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MFARequiredRoles = nil })
	u := f.user(t, "alice@example.com", "")
	p := f.login(t, "alice@example.com").Principal

	tok, raw, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: "  terraform  "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, APITokenPrefix) || len(raw) != len(APITokenPrefix)+tokenSecretLen || !ValidTokenShape(raw) {
		t.Fatalf("raw token %q", raw)
	}
	if tok.Name != "terraform" || tok.Kind != "user" || tok.UserID != u.ID || tok.Projects == nil {
		t.Fatalf("token %+v", tok)
	}
	if !tok.ExpiresAt.Equal(f.env.Clock.Now().Add(90 * 24 * time.Hour)) {
		t.Fatalf("default expiry %s", tok.ExpiresAt)
	}
	// Shown once: nothing returned afterwards, nor stored, carries the secret.
	js, _ := json.Marshal(tok)
	if bytes.Contains(js, []byte(raw[len(APITokenPrefix):])) {
		t.Fatal("token metadata contains the secret")
	}
	list, err := f.svc.ListTokens(f.ctx, u.ID, "")
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("list: %v %v", list, err)
	}
	var mac []byte
	var name, projects string
	if err := f.env.Store.R().QueryRow(`SELECT token_mac, name, projects_json FROM api_tokens WHERE id = ?`, tok.ID).Scan(&mac, &name, &projects); err != nil {
		t.Fatal(err)
	}
	want, _ := f.env.Keyring.MAC(keyring.PurposeToken, []byte("bearer|"+raw))
	if !bytes.Equal(mac, want) || bytes.Contains(mac, []byte(raw)) || strings.Contains(name+projects, raw[len(APITokenPrefix):]) {
		t.Fatal("token not stored as its keyed MAC only")
	}
	if f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'token.created' AND target_id = ? AND details_json NOT LIKE ?`,
		tok.ID, "%"+raw[len(APITokenPrefix):]+"%") != 1 {
		t.Fatal("token creation not audited (or audited with its secret)")
	}

	// Lookup.
	tp, err := f.svc.AuthenticateToken(f.ctx, raw, "198.51.100.9")
	if err != nil {
		t.Fatal(err)
	}
	if tp.UserID != u.ID || tp.TokenID != tok.ID || tp.Method != MethodToken || tp.IsSession() || tp.SourceID != "" ||
		tp.Actor() != (audit.Actor{Type: audit.ActorToken, ID: tok.ID}) {
		t.Fatalf("token principal %+v", tp)
	}
	got, _ := f.svc.GetToken(f.ctx, tok.ID)
	var lastIP string
	_ = f.env.Store.R().QueryRow(`SELECT last_used_ip FROM api_tokens WHERE id = ?`, tok.ID).Scan(&lastIP)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(f.env.Clock.Now()) || lastIP != "198.51.100.9" {
		t.Fatalf("last use not tracked: %v %q", got.LastUsedAt, lastIP)
	}
	// Tokens cannot mint tokens, and re-authentication applies to sessions.
	if _, _, err := f.svc.CreateAPIToken(f.ctx, tp, CreateAPITokenInput{Name: "x"}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("token minted a token: %v", err)
	}
	f.env.Clock.Advance(6 * time.Minute)
	if _, _, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: "late"}); apperr.CodeOf(err) != apperr.CodeReauth {
		t.Fatalf("token without recent auth: %v", err)
	}

	// Near misses are refused.
	swapped := raw[:len(raw)-1] + "a"
	if swapped == raw {
		swapped = raw[:len(raw)-1] + "b"
	}
	for _, bad := range []string{swapped, SourceTokenPrefix + raw[len(APITokenPrefix):], raw + " ", strings.ToUpper(raw)} {
		if _, err := f.svc.AuthenticateToken(f.ctx, bad, ""); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("near miss %q: %v", bad, err)
		}
	}

	// Revocation is immediate and final.
	if err := f.svc.RevokeToken(f.ctx, p.Actor(), tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AuthenticateToken(f.ctx, raw, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	if err := f.svc.RevokeToken(f.ctx, p.Actor(), tok.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("double revoke: %v", err)
	}
	if got, _ := f.svc.GetToken(f.ctx, tok.ID); got.RevokedAt == nil {
		t.Fatal("revocation time not recorded")
	}
	if _, err := f.svc.GetToken(f.ctx, "tok_missing"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("missing token: %v", err)
	}
}

func TestAPITokenExpiry(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MFARequiredRoles = nil })
	f.user(t, "alice@example.com", "")
	p := f.login(t, "alice@example.com").Principal
	for _, d := range []time.Duration{time.Minute, 59 * time.Minute, 367 * 24 * time.Hour} {
		if _, _, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: "x", ExpiresIn: d}); apperr.CodeOf(err) != apperr.CodeInvalid {
			t.Fatalf("expiry %s: %v", d, err)
		}
	}
	if _, _, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: "max", ExpiresIn: 366 * 24 * time.Hour}); err != nil {
		t.Fatalf("366 days: %v", err)
	}
	for _, name := range []string{"", "   ", strings.Repeat("n", 101)} {
		if _, _, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: name}); apperr.CodeOf(err) != apperr.CodeInvalid {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	_, raw, err := f.svc.CreateAPIToken(f.ctx, p, CreateAPITokenInput{Name: "short", ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.env.Clock.Advance(time.Hour - time.Microsecond)
	if _, err := f.svc.AuthenticateToken(f.ctx, raw, ""); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	f.env.Clock.Advance(time.Microsecond)
	if _, err := f.svc.AuthenticateToken(f.ctx, raw, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func TestAPITokenScope(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MFARequiredRoles = nil })
	home := f.env.Project(t, "home").ID
	parents := f.env.Project(t, "parents").ID
	owner := f.user(t, "owner@example.com", RoleOwner)
	admin := f.user(t, "admin@example.com", RoleAdmin)
	f.user(t, "auditor@example.com", RoleAuditor)
	member := f.user(t, "member@example.com", "")
	if err := f.svc.SetMember(f.ctx, audit.System, home, member.ID, ProjectEditor); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMember(f.ctx, audit.System, home, owner.ID, ProjectViewer); err != nil {
		t.Fatal(err)
	}
	principal := func(email string) *Principal { return f.login(t, email).Principal }
	create := func(p *Principal, in CreateAPITokenInput) (*Principal, error) {
		t.Helper()
		in.Name = "scoped"
		_, raw, err := f.svc.CreateAPIToken(f.ctx, p, in)
		if err != nil {
			return nil, err
		}
		return f.svc.AuthenticateToken(f.ctx, raw, "")
	}

	// A token never exceeds its creator's installation role.
	refused := []struct {
		email string
		in    CreateAPITokenInput
		code  apperr.Code
	}{
		{"member@example.com", CreateAPITokenInput{InstallationRole: RoleAuditor}, apperr.CodeForbidden},
		{"member@example.com", CreateAPITokenInput{InstallationRole: RoleAdmin}, apperr.CodeForbidden},
		{"auditor@example.com", CreateAPITokenInput{InstallationRole: RoleAdmin}, apperr.CodeForbidden},
		{"owner@example.com", CreateAPITokenInput{InstallationRole: RoleOwner}, apperr.CodeInvalid},
		{"member@example.com", CreateAPITokenInput{ProjectRole: "owner"}, apperr.CodeInvalid},
		{"member@example.com", CreateAPITokenInput{Projects: []string{parents}}, apperr.CodeForbidden},
	}
	for _, tc := range refused {
		if _, err := create(principal(tc.email), tc.in); apperr.CodeOf(err) != tc.code {
			t.Errorf("%s %+v: %v", tc.email, tc.in, err)
		}
	}

	// Member: explicit memberships only, capped and restricted.
	mp, err := create(principal("member@example.com"), CreateAPITokenInput{ProjectRole: ProjectViewer, Projects: []string{home}})
	if err != nil {
		t.Fatal(err)
	}
	if mp.InstallationRole != "" || mp.ProjectRoles[home] != ProjectEditor || mp.TokenProjectRole != ProjectViewer || !slices.Equal(mp.TokenProjects, []string{home}) {
		t.Fatalf("member token %+v", mp)
	}
	// Auditor token keeps the auditor role.
	ap, err := create(principal("auditor@example.com"), CreateAPITokenInput{InstallationRole: RoleAuditor})
	if err != nil || ap.InstallationRole != RoleAuditor || ap.TokenProjects != nil {
		t.Fatalf("auditor token %+v %v", ap, err)
	}
	// Owner token without an installation role: no implicit access, only explicit memberships...
	op, err := create(principal("owner@example.com"), CreateAPITokenInput{})
	if err != nil || op.InstallationRole != "" || len(op.ProjectRoles) != 1 || op.ProjectRoles[home] != ProjectViewer {
		t.Fatalf("owner token without role %+v %v", op, err)
	}
	// ...unless scoped to projects: there the owner acts as project admin (capped by the token's role), including in
	// projects where an explicit membership grants less.
	sp, err := create(principal("owner@example.com"), CreateAPITokenInput{ProjectRole: ProjectEditor, Projects: []string{home, parents}})
	if err != nil || sp.InstallationRole != "" || sp.ProjectRoles[parents] != ProjectAdmin || sp.ProjectRoles[home] != ProjectAdmin ||
		sp.TokenProjectRole != ProjectEditor || !slices.Equal(sp.TokenProjects, []string{home, parents}) {
		t.Fatalf("owner token scoped to projects %+v %v", sp, err)
	}
	// An owner may grant admin (never owner) to a token.
	if p, err := create(principal("owner@example.com"), CreateAPITokenInput{InstallationRole: RoleAdmin}); err != nil || p.InstallationRole != RoleAdmin {
		t.Fatalf("owner admin token %+v %v", p, err)
	}

	// The token follows the user's current role: demoting the user demotes the token.
	_, rawAdmin, err := f.svc.CreateAPIToken(f.ctx, principal("admin@example.com"), CreateAPITokenInput{Name: "admin", InstallationRole: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.AuthenticateToken(f.ctx, rawAdmin, ""); p.InstallationRole != RoleAdmin {
		t.Fatalf("admin token %+v", p)
	}
	auditor := RoleAuditor
	if _, err := f.svc.UpdateUser(f.ctx, audit.System, admin.ID, UpdateUserInput{InstallationRole: &auditor}); err != nil {
		t.Fatal(err)
	}
	if p, err := f.svc.AuthenticateToken(f.ctx, rawAdmin, ""); err != nil || p.InstallationRole != "" {
		t.Fatalf("admin token after demotion to auditor %+v %v", p, err)
	}
	// Disabled users' tokens stop working.
	disabled := "disabled"
	if _, err := f.svc.UpdateUser(f.ctx, audit.System, admin.ID, UpdateUserInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AuthenticateToken(f.ctx, rawAdmin, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled user's token accepted: %v", err)
	}
}

func TestSourceTokens(t *testing.T) {
	f := newFixture(t)
	const sourceID = "src_example"
	actor := audit.Actor{Type: audit.ActorUser, ID: "usr_admin"}
	var tok *Token
	var raw string
	if err := f.env.Store.W().Tx(f.ctx, func(tx *store.Tx) error {
		var err error
		tok, raw, err = f.svc.CreateSourceTokenTx(f.ctx, tx, actor, sourceID, "pusher", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, SourceTokenPrefix) || !ValidTokenShape(raw) || tok.Kind != "source" || tok.SourceID != sourceID || tok.UserID != "" {
		t.Fatalf("source token %+v %q", tok, raw)
	}
	var mac []byte
	if err := f.env.Store.R().QueryRow(`SELECT token_mac FROM api_tokens WHERE id = ?`, tok.ID).Scan(&mac); err != nil {
		t.Fatal(err)
	}
	if want, _ := f.env.Keyring.MAC(keyring.PurposeToken, []byte("bearer|"+raw)); !bytes.Equal(mac, want) {
		t.Fatal("source token not stored as its MAC")
	}
	p, err := f.svc.AuthenticateToken(f.ctx, raw, "192.0.2.50")
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceID != sourceID || p.TokenID != tok.ID || p.UserID != "" || p.Method != MethodSource || len(p.ProjectRoles) != 0 || p.InstallationRole != "" ||
		p.Actor() != (audit.Actor{Type: audit.ActorSource, ID: tok.ID}) {
		t.Fatalf("source principal %+v", p)
	}
	// The API prefix with a source token's secret is a different token.
	if _, err := f.svc.AuthenticateToken(f.ctx, APITokenPrefix+raw[len(SourceTokenPrefix):], ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("prefix swap: %v", err)
	}
	if list, _ := f.svc.ListTokens(f.ctx, "", sourceID); len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("list source tokens: %+v", list)
	}
	if list, _ := f.svc.ListTokens(f.ctx, "usr_admin", ""); len(list) != 0 {
		t.Fatalf("source token listed as a user token: %+v", list)
	}
	if f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'source.token_created' AND target_id = ?`, sourceID) != 1 {
		t.Fatal("source token creation not audited")
	}
	if err := f.env.Store.W().Tx(f.ctx, func(tx *store.Tx) error {
		_, _, err := f.svc.CreateSourceTokenTx(f.ctx, tx, actor, sourceID, "", 0)
		return err
	}); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("unnamed source token: %v", err)
	}
	if err := f.env.Store.W().Tx(f.ctx, func(tx *store.Tx) error { return RevokeSourceTokensTx(f.ctx, tx, sourceID) }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AuthenticateToken(f.ctx, raw, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("token of a deleted source accepted: %v", err)
	}
}
