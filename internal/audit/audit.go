// Adapted from Porch (Apache-2.0), which adapted HallPass internal/audit/audit.go.

// Package audit writes the append-only, hash-chained audit log.
//
// Every security-relevant action appends an event inside the same write transaction as the change it describes,
// so the audit trail and the state can never disagree. Each row stores hash = SHA-256(prev_hash || canonical row),
// which makes deletion, reordering or modification inside the database detectable by Verify. The chain is
// tamper-evident, not tamper-proof: a party with database write access can rewrite the whole chain. The ECS file sink
// (log.audit_file) shipped to external storage is the mitigation.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/obs/redact"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Actor types.
const (
	ActorUser   = "user"
	ActorToken  = "token"
	ActorSource = "source"
	ActorSystem = "system"
	ActorCLI    = "cli"
)

// Outcomes.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// Actor identifies who performed an action.
type Actor struct {
	Type string
	ID   string
}

// System is the actor for automatic actions (expiry, reconciliation, jobs).
var System = Actor{Type: ActorSystem, ID: "deed"}

// CLI is the actor for host-side administrative commands.
var CLI = Actor{Type: ActorCLI, ID: "deed-cli"}

// Target identifies what an action applied to.
type Target struct {
	Type string
	ID   string
}

// Event is one audit record (schema v1).
type Event struct {
	Actor      Actor
	ProjectID  string
	Action     string // dotted, e.g. "tenant.created"
	Target     Target
	Outcome    string
	ReasonCode string // stable machine-readable reason for failures and denials
	SourceIP   string // trusted client IP (from the proxy-aware resolver), never a raw header
	AuthMethod string
	RequestID  string
	// Details are non-sensitive structured fields stored in plaintext. Keys that look sensitive (see redact) are
	// rejected: put such values in Sensitive.
	Details map[string]any
	// Sensitive details (free text, reasons) are sealed with the field key.
	Sensitive map[string]any
}

// Record is a stored audit event as returned to readers and sinks.
type Record struct {
	ID           string         `json:"id"`
	Seq          int64          `json:"seq"`
	At           int64          `json:"at"`
	RequestID    string         `json:"request_id,omitempty"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id,omitempty"`
	ProjectID    string         `json:"project_id,omitempty"`
	Action       string         `json:"action"`
	TargetType   string         `json:"target_type,omitempty"`
	TargetID     string         `json:"target_id,omitempty"`
	Outcome      string         `json:"outcome"`
	ReasonCode   string         `json:"reason_code,omitempty"`
	SourceIP     string         `json:"source_ip,omitempty"`
	AuthMethod   string         `json:"auth_method,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	HasSensitive bool           `json:"has_sensitive,omitempty"`
}

// Sink receives committed audit records (e.g. a JSON-lines file for SIEM ingestion).
type Sink interface {
	Write(Record) error
}

// Writer appends audit events.
type Writer struct {
	kr      *keyring.Keyring
	metrics *metrics.Metrics
	mu      sync.RWMutex
	sink    Sink
}

// NewWriter returns a writer. kr may be nil only if events never carry sensitive details.
func NewWriter(kr *keyring.Keyring, m *metrics.Metrics) *Writer { return &Writer{kr: kr, metrics: m} }

// SetSink attaches a sink that receives every record after its transaction commits.
func (w *Writer) SetSink(s Sink) {
	w.mu.Lock()
	w.sink = s
	w.mu.Unlock()
}

// GenesisHash is the prev_hash of the first event.
var GenesisHash = make([]byte, sha256.Size)

// ErrSensitiveDetail means a plaintext detail key names a secret-like value.
var ErrSensitiveDetail = errors.New("audit: sensitive key in plaintext details")

// Append writes ev inside tx and returns its ID. An error must abort the caller's transaction: a mutation without its
// audit record never commits.
func (w *Writer) Append(ctx context.Context, tx *store.Tx, ev Event) (string, error) {
	if ev.Action == "" || ev.Actor.Type == "" {
		return "", errors.New("audit: action and actor type are required")
	}
	if ev.Outcome == "" {
		ev.Outcome = OutcomeSuccess
	}
	for k := range ev.Details {
		if redact.IsSensitiveKey(k) {
			return "", fmt.Errorf("%w: %q", ErrSensitiveDetail, k)
		}
	}
	m := MetaFrom(ctx)
	if ev.SourceIP == "" {
		ev.SourceIP = m.SourceIP
	}
	if ev.RequestID == "" {
		ev.RequestID = m.RequestID
	}
	if ev.AuthMethod == "" {
		ev.AuthMethod = m.AuthMethod
	}
	details, err := canonicalJSON(ev.Details)
	if err != nil {
		return "", fmt.Errorf("audit details: %w", err)
	}
	id := ids.New(ids.Audit)
	var enc []byte
	if len(ev.Sensitive) > 0 {
		if w.kr == nil {
			return "", errors.New("audit: sensitive details require a keyring")
		}
		pt, err := canonicalJSON(ev.Sensitive)
		if err != nil {
			return "", err
		}
		if enc, err = w.kr.Seal(keyring.PurposeField, keyring.FieldAAD("audit_events", "details_enc", id), []byte(pt)); err != nil {
			return "", err
		}
	}
	seq := int64(1)
	prev := GenesisHash
	var lastSeq int64
	var lastHash []byte
	err = tx.QueryRow(ctx, `SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &lastHash)
	switch {
	case err == nil:
		seq, prev = lastSeq+1, lastHash
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	rec := Record{ID: id, Seq: seq, At: tx.NowMicros(), RequestID: ev.RequestID, ActorType: ev.Actor.Type, ActorID: ev.Actor.ID,
		ProjectID: ev.ProjectID, Action: ev.Action, TargetType: ev.Target.Type, TargetID: ev.Target.ID, Outcome: ev.Outcome,
		ReasonCode: ev.ReasonCode, SourceIP: ev.SourceIP, AuthMethod: ev.AuthMethod, Details: ev.Details, HasSensitive: enc != nil}
	hash := chainHash(prev, rowFields(rec, details, enc))
	_, err = tx.Exec(ctx, `INSERT INTO audit_events (id, seq, at, request_id, actor_type, actor_id, project_id, action,
		target_type, target_id, outcome, reason_code, source_ip, auth_method, details_json, details_enc, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, rec.At, nullStr(rec.RequestID), rec.ActorType, nullStr(rec.ActorID), nullStr(rec.ProjectID), rec.Action,
		nullStr(rec.TargetType), nullStr(rec.TargetID), rec.Outcome, nullStr(rec.ReasonCode), nullStr(rec.SourceIP),
		nullStr(rec.AuthMethod), details, enc, prev, hash)
	if err != nil {
		return "", err
	}
	w.mu.RLock()
	sink := w.sink
	w.mu.RUnlock()
	tx.AfterCommit(func() { w.metrics.AuditEvent(rec.Outcome) })
	if sink != nil {
		tx.AfterCommit(func() { _ = sink.Write(rec) })
	}
	return id, nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// canonicalJSON renders m with sorted keys (encoding/json sorts map keys); nil → "{}".
func canonicalJSON(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// rowFields length-prefixes every field so that no two distinct rows serialize identically.
func rowFields(r Record, details string, enc []byte) []byte {
	var buf bytes.Buffer
	writeField := func(b []byte) {
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(len(b)))
		buf.Write(l[:])
		buf.Write(b)
	}
	var n [8]byte
	writeField([]byte("deed/audit/v1"))
	writeField([]byte(r.ID))
	binary.BigEndian.PutUint64(n[:], uint64(r.Seq)) //nolint:gosec // positive by CHECK
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(r.At)) //nolint:gosec // timestamps are positive
	writeField(n[:])
	for _, s := range []string{r.RequestID, r.ActorType, r.ActorID, r.ProjectID, r.Action, r.TargetType, r.TargetID, r.Outcome,
		r.ReasonCode, r.SourceIP, r.AuthMethod, details} {
		writeField([]byte(s))
	}
	writeField(enc)
	return buf.Bytes()
}

func chainHash(prev, fields []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(fields)
	return h.Sum(nil)
}

// VerifyResult summarizes a chain verification.
type VerifyResult struct {
	Events   int64  `json:"events"`
	OK       bool   `json:"ok"`
	BrokenAt int64  `json:"broken_at,omitempty"`
	Problem  string `json:"problem,omitempty"`
	HeadHash []byte `json:"head_hash,omitempty"`
}

// Verify walks the whole chain in sequence order and reports the first inconsistency.
func Verify(ctx context.Context, d *sql.DB) (VerifyResult, error) {
	const page = 1000
	res := VerifyResult{OK: true}
	prev := GenesisHash
	var after, expectSeq int64 = 0, 1
	for {
		rows, err := loadRows(ctx, d, `WHERE seq > ? ORDER BY seq LIMIT ?`, after, page)
		if err != nil {
			return res, err
		}
		for _, r := range rows {
			res.Events++
			switch {
			case r.Seq != expectSeq:
				return fail(res, r.Seq, fmt.Sprintf("sequence gap: expected %d, found %d (events deleted)", expectSeq, r.Seq)), nil
			case !bytes.Equal(r.PrevHash, prev):
				return fail(res, r.Seq, "prev_hash does not match the previous event (events reordered or deleted)"), nil
			}
			want := chainHash(prev, rowFields(r.Record, r.DetailsJSON, r.DetailsEnc))
			if !bytes.Equal(want, r.Hash) {
				return fail(res, r.Seq, "hash mismatch (event modified)"), nil
			}
			prev, after = r.Hash, r.Seq
			expectSeq++
		}
		if len(rows) < page {
			res.HeadHash = prev
			return res, nil
		}
	}
}

func fail(res VerifyResult, seq int64, problem string) VerifyResult {
	res.OK, res.BrokenAt, res.Problem = false, seq, problem
	return res
}

// StoredRow is a stored audit event including its chain fields.
type StoredRow struct {
	Record
	DetailsJSON string
	DetailsEnc  []byte
	PrevHash    []byte
	Hash        []byte
}

const rowCols = `id, seq, at, COALESCE(request_id, ''), actor_type, COALESCE(actor_id, ''), COALESCE(project_id, ''), action,
	COALESCE(target_type, ''), COALESCE(target_id, ''), outcome, COALESCE(reason_code, ''), COALESCE(source_ip, ''),
	COALESCE(auth_method, ''), details_json, details_enc, prev_hash, hash`

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadRows(ctx context.Context, q querier, where string, args ...any) ([]StoredRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+rowCols+` FROM audit_events `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredRow
	for rows.Next() {
		var r StoredRow
		if err := rows.Scan(&r.ID, &r.Seq, &r.At, &r.RequestID, &r.ActorType, &r.ActorID, &r.ProjectID, &r.Action, &r.TargetType,
			&r.TargetID, &r.Outcome, &r.ReasonCode, &r.SourceIP, &r.AuthMethod, &r.DetailsJSON, &r.DetailsEnc, &r.PrevHash, &r.Hash); err != nil {
			return nil, err
		}
		r.HasSensitive = len(r.DetailsEnc) > 0
		if r.DetailsJSON != "" && r.DetailsJSON != "{}" {
			_ = json.Unmarshal([]byte(r.DetailsJSON), &r.Details)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Filter selects audit records for listing.
type Filter struct {
	ProjectIDs []string // nil = all projects (installation auditors); empty slice = none
	TargetType string
	TargetID   string
	ActorID    string
	Action     string // prefix match
	Outcome    string
	BeforeSeq  int64
	Limit      int
}

// List returns records newest first.
func List(ctx context.Context, d *sql.DB, f Filter) ([]Record, error) {
	var conds []string
	var args []any
	if f.ProjectIDs != nil {
		if len(f.ProjectIDs) == 0 {
			return []Record{}, nil
		}
		ph := strings.Repeat("?,", len(f.ProjectIDs))
		conds = append(conds, "project_id IN ("+ph[:len(ph)-1]+")")
		for _, p := range f.ProjectIDs {
			args = append(args, p)
		}
	}
	add := func(cond, v string) {
		if v != "" {
			conds = append(conds, cond)
			args = append(args, v)
		}
	}
	add("target_type = ?", f.TargetType)
	add("target_id = ?", f.TargetID)
	add("actor_id = ?", f.ActorID)
	add("outcome = ?", f.Outcome)
	if f.Action != "" {
		conds = append(conds, "action >= ? AND action < ?")
		args = append(args, f.Action, f.Action+"\uffff")
	}
	if f.BeforeSeq > 0 {
		conds = append(conds, "seq < ?")
		args = append(args, f.BeforeSeq)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := loadRows(ctx, d, where+" ORDER BY seq DESC LIMIT ?", append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(rows))
	for i, r := range rows {
		out[i] = r.Record
	}
	return out, nil
}

// OpenSensitive decrypts the sensitive details of a stored event.
func (w *Writer) OpenSensitive(id string, enc []byte) (map[string]any, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	pt, err := w.kr.Open(keyring.PurposeField, keyring.FieldAAD("audit_events", "details_enc", id), enc)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(pt, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// JSONSink writes one ECS-aligned JSON object per line (for OpenSearch / Elastic ingestion).
type JSONSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONSink returns a sink writing to w.
func NewJSONSink(w io.Writer) *JSONSink { return &JSONSink{w: w} }

// ECS renders r with Elastic Common Schema field names; Deed-specific fields live under "deed".
func ECS(r Record) map[string]any {
	outcome := r.Outcome
	if outcome == OutcomeDenied {
		outcome = "failure"
	}
	ev := map[string]any{
		"@timestamp": time.UnixMicro(r.At).UTC().Format(time.RFC3339Nano),
		"ecs":        map[string]any{"version": "8.11.0"},
		"event": map[string]any{
			"id": r.ID, "kind": "event", "category": []string{"configuration"}, "action": r.Action, "outcome": outcome,
			"sequence": r.Seq, "dataset": "deed.audit",
		},
		"deed": map[string]any{
			"audit": map[string]any{"schema": 1, "outcome": r.Outcome, "reason_code": r.ReasonCode, "auth_method": r.AuthMethod,
				"target": map[string]any{"type": r.TargetType, "id": r.TargetID}, "details": r.Details, "has_sensitive": r.HasSensitive},
			"project": map[string]any{"id": r.ProjectID},
		},
		"user": map[string]any{"id": r.ActorID},
	}
	if r.ActorType != "" {
		ev["deed"].(map[string]any)["actor"] = map[string]any{"type": r.ActorType}
	}
	if r.SourceIP != "" {
		ev["source"] = map[string]any{"ip": r.SourceIP}
	}
	if r.RequestID != "" {
		ev["http"] = map[string]any{"request": map[string]any{"id": r.RequestID}}
	}
	return ev
}

// Write implements Sink.
func (s *JSONSink) Write(r Record) error {
	b, err := json.Marshal(ECS(r))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.w.Write(append(b, '\n'))
	return err
}
