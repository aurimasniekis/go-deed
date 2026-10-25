package ops

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/findings"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

func TestListVisibility(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	busy, quiet := env.Project(t, "busy"), env.Project(t, "quiet")
	raise := func(key, project, severity string) {
		t.Helper()
		if err := env.Store.W().Tx(ctx, func(tx *store.Tx) error {
			return findings.RaiseTx(ctx, tx, findings.Finding{DedupKey: key, Type: findings.TypeMissingOwner, Severity: severity,
				Class: findings.ClassConfirmed, ProjectID: project, Title: key, Message: "test"}, time.Hour)
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Many critical findings in a project the caller cannot read rank above the one warning it can read.
	for i := range 20 {
		raise(fmt.Sprintf("busy-%d", i), busy.ID, findings.Critical)
	}
	raise("quiet-1", quiet.ID, findings.Warning)
	raise("installation-1", "", findings.Critical)
	a := &API{Store: env.Store, Clock: env.Clock}
	list := func(readable []string, installation bool, in findingsInput) []string {
		t.Helper()
		if in.State == "" {
			in.State = "open"
		}
		if in.Limit == 0 {
			in.Limit = 2
		}
		fs, err := a.List(ctx, readable, installation, &in)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, f := range fs {
			keys = append(keys, f.DedupKey)
		}
		return keys
	}
	if got := list([]string{quiet.ID}, false, findingsInput{}); len(got) != 1 || got[0] != "quiet-1" {
		t.Fatalf("viewer of quiet sees %v, want [quiet-1]", got)
	}
	if got := list(nil, false, findingsInput{Project: quiet.ID}); len(got) != 1 || got[0] != "quiet-1" {
		t.Fatalf("project filter: %v", got)
	}
	if got := list([]string{}, false, findingsInput{}); len(got) != 0 {
		t.Fatalf("a caller with no projects sees %v", got)
	}
	// Installation findings only for installation roles, and the limit is exact.
	if got := list([]string{quiet.ID}, true, findingsInput{Limit: 5}); len(got) != 2 || got[0] != "installation-1" || got[1] != "quiet-1" {
		t.Fatalf("installation role restricted to quiet sees %v", got)
	}
	if got := list(nil, true, findingsInput{Limit: 3}); len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	if got := list(nil, false, findingsInput{Limit: 100}); len(got) != 21 {
		t.Fatalf("project findings for an unrestricted non-installation caller: %d, want 21", len(got))
	}
}
