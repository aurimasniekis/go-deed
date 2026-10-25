package ingest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/testenv"
)

// itemsBody is the push API request body for POST /sources/{id}/items (internal/sources/api.go).
type itemsBody struct {
	ingest.Batch
	Atomic bool `json:"atomic,omitempty"`
}

// FuzzBatch feeds request bodies through the incremental (Items) and staged-snapshot (PutBatch) entry points. Neither
// may panic; malformed or oversized input must be refused with a client (apperr) error, never a storage failure.
func FuzzBatch(f *testing.F) {
	for _, s := range []string{
		`{"entities":[{"kind":"server","name":"web-1","external_id":"srv-1","spec":{"hostname":"web-1.example.com","ipv4":"192.0.2.10"},"metadata":{"labels":{"tier":"web"}}}]}`,
		`{"atomic":true,"entities":[{"kind":"server","name":"web-1","spec":{"hostname":"web-1.example.com"}},{"kind":"vm","name":"Bad Name"}]}`,
		`{"entities":[{"kind":"server","name":"db-1","spec":{"hostname":"db-1.example.com","ipv4":"198.51.100.20"}},{"kind":"database","name":"orders","spec":{}}],` +
			`"relations":[{"type":"runs_on","from":{"kind":"database","name":"orders"},"to":{"kind":"server","name":"db-1"}}]}`,
		`{"relations":[{"type":"depends_on","from":{"external_id":"a"},"to":{"external_id":"b"},"inferred":true,"confidence":0.4,"evidence":{"rule":"dns","matched":"203.0.113.10"}}]}`,
		`{"relations":[{"type":"bogus","external_id":"r1","delete":true}]}`,
		`{"entities":[{"ref":{"kind":"server","name":"web-1"},"health":{"state":"warning","reason":"disk 91%"},"observed":{"version":"1.2"},"observed_at":"2026-10-01T12:00:00Z"}]}`,
		`{"entities":[{"ref":{"id":"ent_nope"},"delete":true}]}`,
		`{"entities":[{"kind":"server","name":"s","spec":{"password":"hunter2"}}]}`,
		`{"entities":[{"kind":"server","name":"s","metadata":{"labels":{"a":1},"annotations":[]}}]}`,
		`{"entities":[{"kind":"nope","name":"x"}]}`,
		`{"entities":[{}]}`,
		`{"entities":[]}`,
		`{}`,
		`{"entities":` + strings.Repeat(`{"kind":"server"},`, 3) + `]}`,
		`{"entities":[` + strings.TrimSuffix(strings.Repeat(`{"kind":"server","name":"x"},`, 30), ",") + `]}`,
		`null`,
		`[]`,
	} {
		f.Add([]byte(s))
	}
	env := testenv.New(f)
	p := env.Project(f, "home")
	src := env.Source(f, p.ID, "inventory", catalog.LayerImported)
	const maxItems = 20
	svc := &ingest.Service{Store: env.Store, Catalog: env.Catalog, Audit: env.Audit, Clock: env.Clock,
		Limits: ingest.Limits{MaxItems: maxItems, MaxSnapshotItems: 1 << 30, SnapshotTimeout: 24 * time.Hour, MaxRemovalPercent: 25}}
	ctx := context.Background()
	snap, err := svc.Begin(ctx, src, 0, false, audit.System)
	if err != nil {
		f.Fatal(err)
	}
	seq := 0
	f.Fuzz(func(t *testing.T, body []byte) {
		var in itemsBody
		if err := json.Unmarshal(body, &in); err != nil {
			return // the HTTP layer refuses it before ingest
		}
		n := in.Len()
		res, err := svc.Items(ctx, src, in.Batch, in.Atomic, audit.System)
		if n == 0 || n > maxItems {
			if apperr.CodeOf(err) != apperr.CodeInvalid {
				t.Fatalf("%d items: err = %v, want invalid", n, err)
			}
		} else {
			if err != nil {
				t.Fatalf("Items failed with a non-item error: %v", err)
			}
			checkResults(t, res.Results, n, n)
			if in.Atomic && res.Summary.Errors > 0 && res.Applied {
				t.Fatal("atomic batch with item errors applied")
			}
		}

		seq++
		br, err := svc.PutBatch(ctx, src, snap.ID, seq, in.Batch)
		if n == 0 || n > maxItems {
			if apperr.CodeOf(err) != apperr.CodeInvalid {
				t.Fatalf("PutBatch %d items: err = %v, want invalid", n, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("PutBatch failed with a non-item error: %v", err)
		}
		if br.Accepted+len(br.Errors) != n {
			t.Fatalf("PutBatch accepted %d + %d errors for %d items", br.Accepted, len(br.Errors), n)
		}
		checkResults(t, br.Errors, len(br.Errors), n)
		// Re-sending the same batch is an idempotent replay.
		again, err := svc.PutBatch(ctx, src, snap.ID, seq, in.Batch)
		if err != nil || !again.Replayed || again.Accepted != br.Accepted {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	})
}

// checkResults asserts want results with distinct in-range indexes and a structured error wherever one is set.
func checkResults(t *testing.T, rs []ingest.ItemResult, want, n int) {
	t.Helper()
	if len(rs) != want {
		t.Fatalf("%d results, want %d", len(rs), want)
	}
	seen := map[int]bool{}
	for _, r := range rs {
		if r.Index < 0 || r.Index >= n || seen[r.Index] {
			t.Fatalf("result index %d (of %d) repeated or out of range", r.Index, n)
		}
		seen[r.Index] = true
		if r.Type != "entity" && r.Type != "relation" {
			t.Fatalf("result type %q", r.Type)
		}
		if r.Error != nil && (r.Error.Code == "" || r.Error.Message == "") {
			t.Fatalf("unstructured item error %+v", r.Error)
		}
	}
}
