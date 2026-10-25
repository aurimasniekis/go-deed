package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/store/storetest"
)

type env struct {
	s     *store.Store
	kr    *keyring.Keyring
	w     *Writer
	clock *clock.Fake
}

func newEnv(t *testing.T) *env {
	t.Helper()
	c := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := storetest.Open(t, storetest.WithClock(c))
	kek, err := keyring.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	lk, err := keyring.NewLocalKEK(kek)
	if err != nil {
		t.Fatal(err)
	}
	kr, err := keyring.Load(context.Background(), s, lk)
	if err != nil {
		t.Fatal(err)
	}
	return &env{s: s, kr: kr, w: NewWriter(kr, nil), clock: c}
}

// appendOne appends ev in its own transaction.
func (e *env) appendOne(t *testing.T, ctx context.Context, ev Event) string {
	t.Helper()
	var id string
	if err := e.s.W().Tx(ctx, func(tx *store.Tx) error {
		var err error
		id, err = e.w.Append(ctx, tx, ev)
		return err
	}); err != nil {
		t.Fatalf("append %s: %v", ev.Action, err)
	}
	return id
}

// appendN appends n events, at most 100 per transaction to keep each one short.
func (e *env) appendN(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	for done := 0; done < n; {
		batch := min(100, n-done)
		if err := e.s.W().Tx(ctx, func(tx *store.Tx) error {
			for i := range batch {
				if _, err := e.w.Append(ctx, tx, Event{Actor: System, Action: "test.event", Target: Target{Type: "thing", ID: fmt.Sprint(done + i)},
					Details: map[string]any{"i": done + i}}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		done += batch
		e.clock.Advance(time.Second)
	}
}

func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := e.s.W().Tx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), query, args...)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (e *env) rows(t *testing.T) []StoredRow {
	t.Helper()
	rows, err := loadRows(context.Background(), e.s.R(), `ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e *env) verify(t *testing.T) VerifyResult {
	t.Helper()
	res, err := Verify(context.Background(), e.s.R())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAppendBuildsHashChain(t *testing.T) {
	e := newEnv(t)
	e.appendN(t, 5)
	rows := e.rows(t)
	if len(rows) != 5 {
		t.Fatalf("rows = %d", len(rows))
	}
	prev := GenesisHash
	for i, r := range rows {
		if r.Seq != int64(i+1) {
			t.Fatalf("row %d has seq %d", i, r.Seq)
		}
		if !bytes.Equal(r.PrevHash, prev) {
			t.Fatalf("seq %d: prev_hash does not link to the previous event", r.Seq)
		}
		want := chainHash(prev, rowFields(r.Record, r.DetailsJSON, r.DetailsEnc))
		if !bytes.Equal(r.Hash, want) || len(r.Hash) != 32 {
			t.Fatalf("seq %d: stored hash is not SHA-256(prev || row)", r.Seq)
		}
		prev = r.Hash
	}
	res := e.verify(t)
	if !res.OK || res.Events != 5 || res.BrokenAt != 0 || res.Problem != "" || !bytes.Equal(res.HeadHash, prev) {
		t.Fatalf("verify intact chain: %+v", res)
	}
}

func TestVerifyEmptyChain(t *testing.T) {
	e := newEnv(t)
	res := e.verify(t)
	if !res.OK || res.Events != 0 || !bytes.Equal(res.HeadHash, GenesisHash) {
		t.Fatalf("empty chain: %+v", res)
	}
}

func TestVerifySpansPages(t *testing.T) {
	e := newEnv(t)
	e.appendN(t, 1001)
	if res := e.verify(t); !res.OK || res.Events != 1001 {
		t.Fatalf("verify across pages: %+v", res)
	}
	e.exec(t, `UPDATE audit_events SET action = 'tampered' WHERE seq = 1001`)
	if res := e.verify(t); res.OK || res.BrokenAt != 1001 {
		t.Fatalf("edit on the second page not detected: %+v", res)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	cases := []struct {
		name     string
		tamper   func(t *testing.T, e *env)
		brokenAt int64
		problem  string
	}{
		{"edited action", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET action = 'project.created' WHERE seq = 3`)
		}, 3, "hash mismatch"},
		{"edited outcome", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET outcome = 'success', reason_code = NULL WHERE seq = 2`)
		}, 2, "hash mismatch"},
		{"edited details", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET details_json = '{"i":99}' WHERE seq = 4`)
		}, 4, "hash mismatch"},
		{"edited timestamp", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET at = at + 1 WHERE seq = 1`)
		}, 1, "hash mismatch"},
		{"edited source ip", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET source_ip = '198.51.100.7' WHERE seq = 5`)
		}, 5, "hash mismatch"},
		{"edited head hash", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET hash = zeroblob(32) WHERE seq = 5`)
		}, 5, "hash mismatch"},
		{"edited row with its hash recomputed", func(t *testing.T, e *env) {
			r := e.rows(t)[2]
			r.Action = "project.created"
			h := chainHash(r.PrevHash, rowFields(r.Record, r.DetailsJSON, r.DetailsEnc))
			e.exec(t, `UPDATE audit_events SET action = ?, hash = ? WHERE seq = 3`, r.Action, h)
		}, 4, "prev_hash"},
		{"deleted middle row", func(t *testing.T, e *env) {
			e.exec(t, `DELETE FROM audit_events WHERE seq = 2`)
		}, 3, "sequence gap"},
		{"deleted first row", func(t *testing.T, e *env) {
			e.exec(t, `DELETE FROM audit_events WHERE seq = 1`)
		}, 2, "sequence gap"},
		{"deleted row and renumbered", func(t *testing.T, e *env) {
			e.exec(t, `DELETE FROM audit_events WHERE seq = 2`)
			for s := 3; s <= 5; s++ {
				e.exec(t, `UPDATE audit_events SET seq = ? WHERE seq = ?`, s-1, s)
			}
		}, 2, "prev_hash"},
		{"reordered rows", func(t *testing.T, e *env) {
			e.exec(t, `UPDATE audit_events SET seq = 1000 WHERE seq = 2`)
			e.exec(t, `UPDATE audit_events SET seq = 2 WHERE seq = 3`)
			e.exec(t, `UPDATE audit_events SET seq = 3 WHERE seq = 1000`)
		}, 2, "prev_hash"},
		{"reordered rows with relinked prev_hash", func(t *testing.T, e *env) {
			rows := e.rows(t)
			e.exec(t, `UPDATE audit_events SET seq = 1000 WHERE seq = 2`)
			e.exec(t, `UPDATE audit_events SET seq = 2, prev_hash = ? WHERE seq = 3`, rows[0].Hash)
			e.exec(t, `UPDATE audit_events SET seq = 3, prev_hash = ? WHERE seq = 1000`, rows[2].Hash)
		}, 2, "hash mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			ctx := WithMeta(context.Background(), Meta{SourceIP: "192.0.2.10", RequestID: "req-1"})
			for i := range 5 {
				ev := Event{Actor: Actor{Type: ActorUser, ID: "usr_1"}, Action: "entity.updated", Target: Target{Type: "entity", ID: fmt.Sprint(i)},
					Details: map[string]any{"i": i}}
				if i == 1 {
					ev.Outcome, ev.ReasonCode = OutcomeDenied, "forbidden"
				}
				e.appendOne(t, ctx, ev)
				e.clock.Advance(time.Second)
			}
			if res := e.verify(t); !res.OK {
				t.Fatalf("chain broken before tampering: %+v", res)
			}
			tc.tamper(t, e)
			res := e.verify(t)
			if res.OK || res.BrokenAt != tc.brokenAt || !strings.Contains(res.Problem, tc.problem) {
				t.Fatalf("want broken at %d (%q), got %+v", tc.brokenAt, tc.problem, res)
			}
		})
	}
}

// The chain is tamper-evident, not tamper-proof (THREAT_MODEL T17): truncating the tail or rewriting every hash goes
// unnoticed by Verify; only a head hash recorded elsewhere (or the ECS file sink) reveals it.
func TestVerifyLimitsAreDocumented(t *testing.T) {
	t.Run("truncated tail", func(t *testing.T) {
		e := newEnv(t)
		e.appendN(t, 4)
		before := e.verify(t)
		e.exec(t, `DELETE FROM audit_events WHERE seq = 4`)
		after := e.verify(t)
		if !after.OK || after.Events != 3 {
			t.Fatalf("truncation: %+v", after)
		}
		if bytes.Equal(before.HeadHash, after.HeadHash) {
			t.Fatal("head hash must change when the tail is truncated")
		}
	})
	t.Run("whole chain rewritten", func(t *testing.T) {
		e := newEnv(t)
		e.appendN(t, 4)
		e.exec(t, `UPDATE audit_events SET action = 'rewritten' WHERE seq = 2`)
		prev := GenesisHash
		for _, r := range e.rows(t) {
			h := chainHash(prev, rowFields(r.Record, r.DetailsJSON, r.DetailsEnc))
			e.exec(t, `UPDATE audit_events SET prev_hash = ?, hash = ? WHERE seq = ?`, prev, h, r.Seq)
			prev = h
		}
		if res := e.verify(t); !res.OK {
			t.Fatalf("a consistent rewrite is expected to verify: %+v", res)
		}
	})
}

func TestAppendValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, ev := range []Event{{Action: "x"}, {Actor: System}} {
		err := e.s.W().Tx(ctx, func(tx *store.Tx) error { _, err := e.w.Append(ctx, tx, ev); return err })
		if err == nil {
			t.Fatalf("append %+v without action or actor type succeeded", ev)
		}
	}
	for _, key := range []string{"password", "client_secret", "api_token"} {
		err := e.s.W().Tx(ctx, func(tx *store.Tx) error {
			_, err := e.w.Append(ctx, tx, Event{Actor: System, Action: "x", Details: map[string]any{key: "hunter2"}})
			return err
		})
		if !errors.Is(err, ErrSensitiveDetail) {
			t.Fatalf("detail %q: %v", key, err)
		}
	}
	noKR := NewWriter(nil, nil)
	err := e.s.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := noKR.Append(ctx, tx, Event{Actor: System, Action: "x", Sensitive: map[string]any{"reason": "r"}})
		return err
	})
	if err == nil {
		t.Fatal("sensitive details without a keyring were accepted")
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Fatalf("rejected events were stored: %d", len(rows))
	}
}

func TestAppendDefaultsAndMeta(t *testing.T) {
	e := newEnv(t)
	if m := MetaFrom(context.Background()); m != (Meta{}) {
		t.Fatalf("MetaFrom without meta: %+v", m)
	}
	ctx := WithMeta(context.Background(), Meta{SourceIP: "192.0.2.10", RequestID: "req-1", AuthMethod: "password"})
	if m := MetaFrom(ctx); m.SourceIP != "192.0.2.10" || m.RequestID != "req-1" || m.AuthMethod != "password" {
		t.Fatalf("MetaFrom: %+v", m)
	}
	e.appendOne(t, ctx, Event{Actor: Actor{Type: ActorUser, ID: "usr_1"}, Action: "auth.login"})
	e.appendOne(t, ctx, Event{Actor: Actor{Type: ActorToken, ID: "tok_1"}, Action: "entity.created", SourceIP: "198.51.100.20",
		RequestID: "req-2", AuthMethod: "token", ProjectID: "prj_1"})
	rows := e.rows(t)
	r1, r2 := rows[0], rows[1]
	if r1.Outcome != OutcomeSuccess || r1.SourceIP != "192.0.2.10" || r1.RequestID != "req-1" || r1.AuthMethod != "password" {
		t.Fatalf("meta not applied: %+v", r1.Record)
	}
	if r1.DetailsJSON != "{}" || r1.Details != nil {
		t.Fatalf("empty details: %q %v", r1.DetailsJSON, r1.Details)
	}
	if r2.SourceIP != "198.51.100.20" || r2.RequestID != "req-2" || r2.AuthMethod != "token" || r2.ProjectID != "prj_1" {
		t.Fatalf("explicit fields must win over meta: %+v", r2.Record)
	}
	if r1.At != clock.Micros(e.clock.Now()) {
		t.Fatalf("at = %d, want the transaction time", r1.At)
	}
}

func TestSensitiveDetailsAreSealed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.appendOne(t, ctx, Event{Actor: System, Action: "source.credentials_changed", Details: map[string]any{"field": "token"},
		Sensitive: map[string]any{"reason": "rotated after leak-4711"}})
	r := e.rows(t)[0]
	if !r.HasSensitive || len(r.DetailsEnc) == 0 {
		t.Fatalf("sensitive details not stored: %+v", r)
	}
	if bytes.Contains(r.DetailsEnc, []byte("leak-4711")) || strings.Contains(r.DetailsJSON, "leak-4711") {
		t.Fatal("sensitive plaintext stored in the clear")
	}
	m, err := e.w.OpenSensitive(id, r.DetailsEnc)
	if err != nil || m["reason"] != "rotated after leak-4711" {
		t.Fatalf("open sensitive: %v %v", m, err)
	}
	// The ciphertext is bound to its row.
	if _, err := e.w.OpenSensitive("aud_other", r.DetailsEnc); err == nil {
		t.Fatal("sealed details opened under another event ID")
	}
	if m, err := e.w.OpenSensitive(id, nil); m != nil || err != nil {
		t.Fatalf("no sensitive details: %v %v", m, err)
	}
	if res := e.verify(t); !res.OK {
		t.Fatalf("verify: %+v", res)
	}
	e.exec(t, `UPDATE audit_events SET details_enc = ? WHERE seq = 1`, append([]byte{}, r.DetailsEnc[:len(r.DetailsEnc)-1]...))
	if res := e.verify(t); res.OK || res.BrokenAt != 1 {
		t.Fatalf("edited ciphertext not detected: %+v", res)
	}
}

type memSink struct {
	mu   sync.Mutex
	recs []Record
}

func (m *memSink) Write(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func TestRolledBackAppendLeavesNoTrace(t *testing.T) {
	e := newEnv(t)
	sink := &memSink{}
	e.w.SetSink(sink)
	ctx := context.Background()
	boom := errors.New("mutation failed")
	err := e.s.W().Tx(ctx, func(tx *store.Tx) error {
		if _, err := e.w.Append(ctx, tx, Event{Actor: System, Action: "project.created"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx: %v", err)
	}
	if len(e.rows(t)) != 0 || len(sink.recs) != 0 {
		t.Fatal("a rolled-back append was stored or sent to the sink")
	}
	id := e.appendOne(t, ctx, Event{Actor: System, Action: "project.created", Target: Target{Type: "project", ID: "prj_1"}})
	if len(sink.recs) != 1 || sink.recs[0].ID != id || sink.recs[0].Seq != 1 || sink.recs[0].TargetID != "prj_1" {
		t.Fatalf("sink after commit: %+v", sink.recs)
	}
	if res := e.verify(t); !res.OK || res.Events != 1 {
		t.Fatalf("verify: %+v", res)
	}
}

func TestJSONSinkWritesECSLines(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	e.w.SetSink(NewJSONSink(&buf))
	ctx := WithMeta(context.Background(), Meta{SourceIP: "203.0.113.5", RequestID: "req-9", AuthMethod: "token"})
	e.appendOne(t, ctx, Event{Actor: Actor{Type: ActorToken, ID: "tok_1"}, ProjectID: "prj_1", Action: "entity.deleted",
		Target: Target{Type: "entity", ID: "ent_1"}, Outcome: OutcomeDenied, ReasonCode: "forbidden", Details: map[string]any{"kind": "server"}})
	e.appendOne(t, context.Background(), Event{Actor: System, Action: "session.pruned"})

	sc := bufio.NewScanner(&buf)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line is not JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	l := lines[0]
	ev := l["event"].(map[string]any)
	if ev["action"] != "entity.deleted" || ev["outcome"] != "failure" || ev["sequence"] != float64(1) || ev["dataset"] != "deed.audit" {
		t.Fatalf("event: %v", ev)
	}
	da := l["deed"].(map[string]any)["audit"].(map[string]any)
	if da["outcome"] != OutcomeDenied || da["reason_code"] != "forbidden" || da["auth_method"] != "token" {
		t.Fatalf("deed.audit: %v", da)
	}
	if l["source"].(map[string]any)["ip"] != "203.0.113.5" || l["http"].(map[string]any)["request"].(map[string]any)["id"] != "req-9" {
		t.Fatalf("source/http: %v %v", l["source"], l["http"])
	}
	if l["user"].(map[string]any)["id"] != "tok_1" || l["deed"].(map[string]any)["actor"].(map[string]any)["type"] != ActorToken {
		t.Fatalf("actor: %v", l)
	}
	ts, err := time.Parse(time.RFC3339Nano, l["@timestamp"].(string))
	if err != nil || !ts.Equal(e.clock.Now()) {
		t.Fatalf("@timestamp %v: %v", l["@timestamp"], err)
	}
	if _, ok := lines[1]["source"]; ok {
		t.Fatal("source must be omitted without an IP")
	}
	if _, ok := lines[1]["http"]; ok {
		t.Fatal("http must be omitted without a request ID")
	}
	if lines[1]["event"].(map[string]any)["outcome"] != OutcomeSuccess {
		t.Fatalf("success outcome: %v", lines[1]["event"])
	}
}

func TestJSONSinkConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if err := s.Write(Record{ID: fmt.Sprint(i), Seq: int64(i + 1), Action: "x", Outcome: OutcomeSuccess, ActorType: ActorSystem}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	n := 0
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			t.Fatalf("interleaved line: %q", sc.Text())
		}
		n++
	}
	if n != 50 {
		t.Fatalf("lines = %d", n)
	}
}

func TestList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	add := func(ev Event) { e.appendOne(t, ctx, ev) }
	add(Event{Actor: Actor{Type: ActorUser, ID: "usr_a"}, ProjectID: "prj_1", Action: "entity.created", Target: Target{Type: "entity", ID: "ent_1"}})
	add(Event{Actor: Actor{Type: ActorUser, ID: "usr_b"}, ProjectID: "prj_2", Action: "entity.updated", Target: Target{Type: "entity", ID: "ent_2"}})
	add(Event{Actor: Actor{Type: ActorUser, ID: "usr_a"}, ProjectID: "prj_1", Action: "project.updated", Outcome: OutcomeDenied})
	add(Event{Actor: System, Action: "entityx.created"})
	add(Event{Actor: Actor{Type: ActorUser, ID: "usr_a"}, ProjectID: "prj_2", Action: "entity.deleted", Target: Target{Type: "entity", ID: "ent_2"},
		Details: map[string]any{"kind": "server"}})

	seqs := func(f Filter) []int64 {
		t.Helper()
		recs, err := List(ctx, e.s.R(), f)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, r := range recs {
			out = append(out, r.Seq)
		}
		return out
	}
	cases := []struct {
		name string
		f    Filter
		want []int64
	}{
		{"all newest first", Filter{}, []int64{5, 4, 3, 2, 1}},
		{"no projects", Filter{ProjectIDs: []string{}}, []int64{}},
		{"one project", Filter{ProjectIDs: []string{"prj_1"}}, []int64{3, 1}},
		{"two projects", Filter{ProjectIDs: []string{"prj_1", "prj_2"}}, []int64{5, 3, 2, 1}},
		{"target", Filter{TargetType: "entity", TargetID: "ent_2"}, []int64{5, 2}},
		{"actor", Filter{ActorID: "usr_a"}, []int64{5, 3, 1}},
		{"action prefix", Filter{Action: "entity."}, []int64{5, 2, 1}},
		{"outcome", Filter{Outcome: OutcomeDenied}, []int64{3}},
		{"before", Filter{BeforeSeq: 3}, []int64{2, 1}},
		{"limit", Filter{Limit: 2}, []int64{5, 4}},
		{"limit clamped", Filter{Limit: 100000}, []int64{5, 4, 3, 2, 1}},
	}
	for _, tc := range cases {
		if got := seqs(tc.f); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	recs, _ := List(ctx, e.s.R(), Filter{Limit: 1})
	if recs[0].Details["kind"] != "server" || recs[0].ActorType != ActorUser {
		t.Fatalf("record fields: %+v", recs[0])
	}
}

func TestRowFieldsAreUnambiguous(t *testing.T) {
	a := rowFields(Record{ID: "x", Seq: 1, At: 1, ActorType: "ab", ActorID: "c"}, "{}", nil)
	b := rowFields(Record{ID: "x", Seq: 1, At: 1, ActorType: "a", ActorID: "bc"}, "{}", nil)
	if bytes.Equal(a, b) {
		t.Fatal("field boundaries must be part of the serialization")
	}
	c := rowFields(Record{ID: "x", Seq: 1, At: 1}, "{}", []byte{})
	d := rowFields(Record{ID: "x", Seq: 1, At: 1}, "{}", nil)
	if !bytes.Equal(c, d) {
		t.Fatal("empty and nil ciphertexts serialize differently")
	}
	if s, _ := canonicalJSON(nil); s != "{}" {
		t.Fatalf("nil details: %q", s)
	}
	if s, _ := canonicalJSON(map[string]any{"b": 1, "a": map[string]any{"z": 1, "y": 2}}); s != `{"a":{"y":2,"z":1},"b":1}` {
		t.Fatalf("details not canonical: %s", s)
	}
}

func TestECSOmitsEmptyActorType(t *testing.T) {
	m := ECS(Record{ID: "aud_1", Seq: 1, At: 0, Action: "x", Outcome: OutcomeFailure})
	if _, ok := m["deed"].(map[string]any)["actor"]; ok {
		t.Fatal("actor present without a type")
	}
	if m["event"].(map[string]any)["outcome"] != OutcomeFailure {
		t.Fatalf("outcome: %v", m["event"])
	}
}
