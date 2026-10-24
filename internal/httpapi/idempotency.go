// Adapted from Porch internal/mgmtapi/idempotency.go (Apache-2.0).

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// IdempotencyHeader makes a POST safe to retry: a repeated request with the same key, credential and body gets the
// stored response instead of running again (24 h). Responses that carried a secret are never replayed (ADR-0041).
const IdempotencyHeader = "Idempotency-Key"

const (
	idempotencyTTL     = 24 * time.Hour
	idempotencyMaxBody = 1 << 20
)

// secretFields are response fields that must never be returned twice.
var secretFields = []string{"token", "secret", "setup_link", "recovery_codes"}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.buf.Len() < idempotencyMaxBody {
		r.buf.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for _, c := range k {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// credential returns the raw credential the request authenticates with ("" for anonymous requests).
func credential(r *http.Request) string {
	if t, ok := BearerToken(r); ok {
		return "bearer:" + t
	}
	if c := authn.TokenFromRequest(r); c != "" {
		return "session:" + c
	}
	return ""
}

func (d *Deps) credentialValid(r *http.Request) bool {
	if t, ok := BearerToken(r); ok {
		_, err := d.Authn.AuthenticateToken(r.Context(), t, httpx.ClientIP(r.Context()).String())
		return err == nil
	}
	_, err := d.Authn.Validate(r.Context(), authn.TokenFromRequest(r))
	return err == nil
}

// Flush lets streaming handlers flush through the recorder.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *Deps) writeIdempotencyProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem(status, apperr.Code(code), detail, nil))
}

// idempotency wraps the API handler.
func (d *Deps) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyHeader)
		cred := credential(r)
		if r.Method != http.MethodPost || key == "" || cred == "" || d.Keyring == nil {
			next.ServeHTTP(w, r)
			return
		}
		// Only authenticated callers get replays: a revoked token or ended session must not read stored responses, and
		// anonymous junk must not cost write transactions. Invalid credentials fall through to the 401.
		if !d.credentialValid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !validIdempotencyKey(key) {
			d.writeIdempotencyProblem(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 1–255 printable ASCII characters")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			d.writeIdempotencyProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		credSum := sha256.Sum256([]byte(cred))
		mac, err := d.Keyring.MAC(keyring.PurposeToken, []byte("deed/idempotency/v1|"+hex.EncodeToString(credSum[:])+"|"+key))
		if err != nil {
			d.log().Error("idempotency mac", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		reqSum := sha256.Sum256([]byte(r.Method + " " + r.URL.RequestURI() + "\n" + string(body)))
		ctx := r.Context()
		now := d.now()
		var inserted int64
		err = d.Store.W().TxNamed(ctx, "idempotency.begin", func(tx *store.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE key_mac = ? AND expires_at <= ?`, mac, tx.NowMicros()); err != nil {
				return err
			}
			var err error
			inserted, err = tx.ExecN(ctx, `INSERT OR IGNORE INTO idempotency_keys (key_mac, request_hash, method, path, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?)`, mac, reqSum[:], r.Method, r.URL.Path, tx.NowMicros(), tx.Now.Add(idempotencyTTL).UnixMicro())
			return err
		})
		if err != nil {
			d.writeIdempotencyProblem(w, http.StatusServiceUnavailable, "unavailable", "try again later")
			return
		}
		if inserted == 0 {
			d.replay(ctx, w, mac, reqSum[:], now)
			return
		}
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		d.complete(context.WithoutCancel(ctx), rec, mac, r.URL.Path)
	})
}

// replay answers a repeated key.
func (d *Deps) replay(ctx context.Context, w http.ResponseWriter, mac, reqSum []byte, now time.Time) {
	var row struct {
		RequestHash, Body                  []byte
		State, ContentType, Location, Etag string
		Status, SecretWithheld             int64
	}
	err := d.Store.R().QueryRowContext(ctx, `SELECT request_hash, state, status, content_type, body, location, etag, secret_withheld
		FROM idempotency_keys WHERE key_mac = ? AND expires_at > ?`, mac, now.UnixMicro()).Scan(&row.RequestHash, &row.State, &row.Status,
		&row.ContentType, &row.Body, &row.Location, &row.Etag, &row.SecretWithheld)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		d.writeIdempotencyProblem(w, http.StatusConflict, "idempotency_in_progress", "a request with this Idempotency-Key is being processed; retry shortly")
	case err != nil:
		d.writeIdempotencyProblem(w, http.StatusServiceUnavailable, "unavailable", "try again later")
	case !bytes.Equal(row.RequestHash, reqSum):
		d.writeIdempotencyProblem(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
			"this Idempotency-Key was used for a different request")
	case row.State != "done":
		d.writeIdempotencyProblem(w, http.StatusConflict, "idempotency_in_progress", "a request with this Idempotency-Key is being processed; retry shortly")
	case row.SecretWithheld == 1:
		if row.Location != "" {
			w.Header().Set("Location", row.Location)
		}
		d.writeIdempotencyProblem(w, http.StatusConflict, "idempotent_secret_withheld",
			"the original request succeeded and its response contained a secret, which is never returned twice; read the resource instead")
	default:
		w.Header().Set("Content-Type", row.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Idempotent-Replayed", "true")
		if row.Location != "" {
			w.Header().Set("Location", row.Location)
		}
		if row.Etag != "" {
			w.Header().Set("ETag", row.Etag)
		}
		w.WriteHeader(int(row.Status))
		_, _ = w.Write(row.Body)
	}
}

// complete stores a successful response, or releases the key so a failed request can be retried.
func (d *Deps) complete(ctx context.Context, rec *recorder, mac []byte, path string) {
	keep := rec.status >= 200 && rec.status <= 299 && rec.buf.Len() < idempotencyMaxBody
	body := rec.buf.Bytes()
	withheld, location, contentType, et := int64(0), rec.Header().Get("Location"), rec.Header().Get("Content-Type"), rec.Header().Get("ETag")
	var obj map[string]any
	if keep && json.Unmarshal(body, &obj) == nil {
		for _, f := range secretFields {
			if v, ok := obj[f].(string); ok && v != "" {
				withheld = 1
			}
		}
		if id, ok := obj["id"].(string); ok && location == "" && !strings.Contains(id, "/") {
			location = strings.TrimSuffix(path, "/") + "/" + id
		}
	}
	if withheld == 1 {
		body = nil
	}
	err := d.Store.W().TxNamed(ctx, "idempotency.complete", func(tx *store.Tx) error {
		if !keep {
			_, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE key_mac = ?`, mac)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE idempotency_keys SET state = 'done', status = ?, content_type = ?, body = ?, secret_withheld = ?, location = ?, etag = ?
			WHERE key_mac = ?`, rec.status, contentType, body, withheld, location, et, mac)
		return err
	})
	if err != nil {
		d.log().Error("idempotency complete", "err", err)
	}
}
