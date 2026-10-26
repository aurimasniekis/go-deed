package authn

import (
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
)

func TestIssueServiceTokenOnHost(t *testing.T) {
	f := newFixture(t)
	if _, _, _, err := f.svc.IssueServiceTokenOnHost(f.ctx, "terraform", false, RoleAdmin, "first", 24*time.Hour); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("missing account without -create: %v", err)
	}
	sa, tok, raw, err := f.svc.IssueServiceTokenOnHost(f.ctx, "terraform", true, RoleAdmin, "first", 366*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sa.Kind != KindService || sa.InstallationRole != RoleAdmin || tok.InstallationRole != RoleAdmin || tok.UserID != sa.ID {
		t.Fatalf("account %+v token %+v", sa, tok)
	}
	p, err := f.svc.AuthenticateToken(f.ctx, raw, "192.0.2.1")
	if err != nil || p.UserID != sa.ID || p.InstallationRole != RoleAdmin {
		t.Fatalf("token principal %+v: %v", p, err)
	}
	// The account exists now: a second token needs no -create, and may not exceed the account's role.
	if _, tok, _, err = f.svc.IssueServiceTokenOnHost(f.ctx, "terraform", false, RoleAuditor, "audit", time.Hour); err != nil || tok.InstallationRole != RoleAuditor {
		t.Fatalf("second token %+v: %v", tok, err)
	}
	if _, _, _, err := f.svc.IssueServiceTokenOnHost(f.ctx, "reader", true, RoleOwner, "x", time.Hour); err == nil {
		t.Fatal("owner service account was created")
	}
	if _, _, _, err := f.svc.IssueServiceTokenOnHost(f.ctx, "reader", true, "", "x", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.IssueServiceTokenOnHost(f.ctx, "reader", false, RoleAdmin, "x", time.Hour); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("token above the account's role: %v", err)
	}
	// Service accounts cannot sign in interactively and are not listed as people.
	if _, err := f.svc.IssueReset(f.ctx, audit.System, sa.ID, time.Hour); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("reset link for a service account: %v", err)
	}
	users, _ := f.svc.ListUsers(f.ctx)
	if len(users) != 0 {
		t.Fatalf("people: %+v", users)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_events WHERE action = 'service_account.created'`); n != 2 {
		t.Fatalf("%d service_account.created events", n)
	}
}
