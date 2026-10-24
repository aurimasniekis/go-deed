// Package deedclient is the Go SDK for Deed's source push protocol (ADR-006, ADR-013): external programs holding a
// source token (deed_st_…) write entities, relationships and observations into one source, either incrementally or
// as complete snapshots that tombstone what they no longer contain.
//
// The client retries transient failures (connection errors, 429, 502–504) with jittered backoff, honours
// Retry-After, reuses one Idempotency-Key across the retries of a request, never follows redirects and never logs
// request bodies.
package deedclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Ref locates an entity: by Deed ID, by this source's external ID, or by kind and name (in the source's project).
type Ref struct {
	ID         string `json:"id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Project    string `json:"project,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Health is a health observation.
type Health struct {
	State  string `json:"state"` // ok, warning, failed, unknown
	Reason string `json:"reason,omitempty"`
}

// Entity is one entity fact.
type Entity struct {
	Ref        *Ref           `json:"ref,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Name       string         `json:"name,omitempty"`
	ExternalID string         `json:"external_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Spec       map[string]any `json:"spec,omitempty"`
	Observed   map[string]any `json:"observed,omitempty"`
	Health     *Health        `json:"health,omitempty"`
	Revision   int64          `json:"revision,omitempty"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Delete     bool           `json:"delete,omitempty"`
}

// Relation is one relationship assertion.
type Relation struct {
	Type          string         `json:"type"`
	From          Ref            `json:"from"`
	To            Ref            `json:"to"`
	ExternalID    string         `json:"external_id,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Evidence      map[string]any `json:"evidence,omitempty"`
	Inferred      bool           `json:"inferred,omitempty"`
	Confidence    *float64       `json:"confidence,omitempty"`
	EvidenceClass string         `json:"evidence_class,omitempty"`
	Revision      int64          `json:"revision,omitempty"`
	ObservedAt    *time.Time     `json:"observed_at,omitempty"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	Delete        bool           `json:"delete,omitempty"`
}

// Batch is a set of items (≤ 1000 items / 4 MiB by default).
type Batch struct {
	Entities  []Entity   `json:"entities,omitempty"`
	Relations []Relation `json:"relations,omitempty"`
}

// Len counts items.
func (b Batch) Len() int { return len(b.Entities) + len(b.Relations) }

// ItemError is a structured per-item error.
type ItemError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fields  []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"fields,omitempty"`
}

// ItemResult is one item's outcome.
type ItemResult struct {
	Index      int        `json:"index"`
	Type       string     `json:"type"`
	Action     string     `json:"action"`
	EntityID   string     `json:"entity_id,omitempty"`
	RelationID string     `json:"relation_id,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Error      *ItemError `json:"error,omitempty"`
}

// Summary counts outcomes.
type Summary struct {
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Deleted    int `json:"deleted"`
	Stale      int `json:"stale"`
	NotFound   int `json:"not_found"`
	Unresolved int `json:"unresolved"`
	Errors     int `json:"errors"`
	Removed    int `json:"removed"`
	Orphaned   int `json:"orphaned"`
}

// ItemsResult answers an incremental write.
type ItemsResult struct {
	Results []ItemResult `json:"results"`
	Summary Summary      `json:"summary"`
	Applied bool         `json:"applied"`
}

// Snapshot is a staged snapshot.
type Snapshot struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"source_id"`
	Generation int64     `json:"generation"`
	Complete   bool      `json:"complete"`
	State      string    `json:"state"`
	ItemCount  int       `json:"item_count"`
	ErrorCount int       `json:"error_count"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// BatchResult answers a staged batch.
type BatchResult struct {
	Seq      int          `json:"seq"`
	Accepted int          `json:"accepted"`
	Errors   []ItemResult `json:"errors"`
	Replayed bool         `json:"replayed"`
}

// CommitOptions control a snapshot commit.
type CommitOptions struct {
	DryRun             bool `json:"dry_run,omitempty"`
	AllowMassRemoval   bool `json:"allow_mass_removal,omitempty"`
	AllowPartialErrors bool `json:"allow_partial_errors,omitempty"`
}

// CommitResult reports a commit or preview.
type CommitResult struct {
	Snapshot *Snapshot    `json:"snapshot"`
	Summary  Summary      `json:"summary"`
	Results  []ItemResult `json:"results"`
	DryRun   bool         `json:"dry_run"`
}

// Error is an RFC 9457 problem returned by Deed.
type Error struct {
	Status     int
	Code       string `json:"code"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("deed: %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("deed: %d %s", e.Status, e.Title)
}

// IsNotFound reports a Deed not_found problem.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound && e.Code == "not_found"
}

// Client talks to one Deed source.
type Client struct {
	base     string
	token    string
	source   string
	http     *http.Client
	retries  int
	maxBody  int64
	agent    string
	maxBatch int
}

// Option configures a Client.
type Option func(*Client) error

// WithHTTPClient replaces the HTTP client (its CheckRedirect is overridden: redirects are never followed).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) error { c.http = h; return nil } }

// WithCAFile trusts an extra CA bundle (PEM).
func WithCAFile(path string) Option {
	return func(c *Client) error {
		pem, err := readFile(path)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("deedclient: no certificates in CA file")
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		c.http.Transport = tr
		return nil
	}
}

// WithRetries sets the number of retries for transient failures (default 5).
func WithRetries(n int) Option { return func(c *Client) error { c.retries = n; return nil } }

// WithUserAgent sets the User-Agent product token.
func WithUserAgent(ua string) Option { return func(c *Client) error { c.agent = ua; return nil } }

// WithBatchSize sets the snapshot batch size (default 500, at most 1000).
func WithBatchSize(n int) Option {
	return func(c *Client) error {
		if n < 1 || n > 1000 {
			return errors.New("deedclient: batch size must be within 1..1000")
		}
		c.maxBatch = n
		return nil
	}
}

// New returns a client for endpoint (e.g. https://deed.example.com), a source token and the source ID.
func New(endpoint, token, sourceID string, opts ...Option) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("deedclient: endpoint must be an http(s) URL")
	}
	if !strings.HasPrefix(token, "deed_st_") && !strings.HasPrefix(token, "deed_at_") {
		return nil, errors.New("deedclient: token must be a Deed source token (deed_st_…)")
	}
	if sourceID == "" {
		return nil, errors.New("deedclient: source ID required")
	}
	c := &Client{base: strings.TrimSuffix(endpoint, "/") + "/api/v1", token: token, source: sourceID, http: &http.Client{Timeout: 60 * time.Second},
		retries: 5, maxBody: 8 << 20, agent: "deedclient", maxBatch: 500}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func jitter(d time.Duration) time.Duration {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(d/2)+1))
	return d/2 + time.Duration(n.Int64())
}

// call sends one request with retries. Mutating requests carry an Idempotency-Key reused across retries.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	key := newKey()
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.agent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := c.http.Do(req)
		var retryAfter time.Duration
		if err == nil {
			if resp.StatusCode < 300 {
				defer resp.Body.Close()
				if out == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					return nil
				}
				return json.NewDecoder(io.LimitReader(resp.Body, c.maxBody)).Decode(out)
			}
			perr := &Error{Status: resp.StatusCode, Title: http.StatusText(resp.StatusCode)}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(perr)
			resp.Body.Close()
			if v := resp.Header.Get("Retry-After"); v != "" {
				if s, perr2 := strconv.Atoi(v); perr2 == nil {
					retryAfter = time.Duration(s) * time.Second
				}
			}
			perr.RetryAfter = retryAfter
			switch resp.StatusCode {
			case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				err = perr
			default:
				return perr
			}
		}
		if attempt >= c.retries {
			return err
		}
		wait := jitter(backoff)
		if retryAfter > 0 {
			wait = min(retryAfter, 5*time.Minute)
		}
		backoff = min(backoff*2, 30*time.Second)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) path(p string) string { return "/sources/" + url.PathEscape(c.source) + p }

// Items applies an incremental batch. With atomic the whole batch is rejected when any item fails.
func (c *Client) Items(ctx context.Context, b Batch, atomic bool) (*ItemsResult, error) {
	var out ItemsResult
	body := struct {
		Batch
		Atomic bool `json:"atomic,omitempty"`
	}{b, atomic}
	return &out, c.call(ctx, http.MethodPost, c.path("/items"), body, &out)
}

// Observations writes time-bounded observations (status.observed / health) of existing entities.
func (c *Client) Observations(ctx context.Context, obs []Entity) (*ItemsResult, error) {
	var out ItemsResult
	return &out, c.call(ctx, http.MethodPost, c.path("/observations"), map[string]any{"observations": obs}, &out)
}

// Heartbeat marks the source alive without data.
func (c *Client) Heartbeat(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, c.path("/heartbeat"), map[string]any{}, nil)
}

// Begin starts a snapshot. generation 0 means last committed + 1; a complete snapshot tombstones absent facts.
func (c *Client) Begin(ctx context.Context, generation int64, complete bool) (*Snapshot, error) {
	var out Snapshot
	return &out, c.call(ctx, http.MethodPost, c.path("/snapshots"), map[string]any{"generation": generation, "complete": complete}, &out)
}

// PutBatch stages batch seq (1-based) of a snapshot; re-sending the same seq and content is idempotent.
func (c *Client) PutBatch(ctx context.Context, snapshotID string, seq int, b Batch) (*BatchResult, error) {
	var out BatchResult
	return &out, c.call(ctx, http.MethodPut, c.path("/snapshots/"+url.PathEscape(snapshotID)+"/batches/"+strconv.Itoa(seq)), b, &out)
}

// Commit applies a snapshot (or previews it with DryRun).
func (c *Client) Commit(ctx context.Context, snapshotID string, o CommitOptions) (*CommitResult, error) {
	var out CommitResult
	return &out, c.call(ctx, http.MethodPost, c.path("/snapshots/"+url.PathEscape(snapshotID)+"/commit"), o, &out)
}

// Abort discards a snapshot; the previous generation stays intact.
func (c *Client) Abort(ctx context.Context, snapshotID string) error {
	return c.call(ctx, http.MethodPost, c.path("/snapshots/"+url.PathEscape(snapshotID)+"/abort"), map[string]any{}, nil)
}

// Sync sends a complete snapshot of everything the source currently knows: begin, batches, commit — aborting the
// snapshot on any failure so a partial upload never removes data.
func (c *Client) Sync(ctx context.Context, all Batch, o CommitOptions) (*CommitResult, error) {
	snap, err := c.Begin(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = c.Abort(actx, snap.ID)
		}
	}()
	seq := 0
	var cur Batch
	flush := func() error {
		if cur.Len() == 0 {
			return nil
		}
		seq++
		if _, err := c.PutBatch(ctx, snap.ID, seq, cur); err != nil {
			return err
		}
		cur = Batch{}
		return nil
	}
	for _, e := range all.Entities {
		cur.Entities = append(cur.Entities, e)
		if cur.Len() >= c.maxBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	for _, r := range all.Relations {
		cur.Relations = append(cur.Relations, r)
		if cur.Len() >= c.maxBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	res, err := c.Commit(ctx, snap.ID, o)
	if err != nil {
		return nil, err
	}
	ok = true
	return res, nil
}
