package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// NATSMon reads the NATS server monitoring endpoint (port 8222): /varz, /connz, /jsz, /subsz, …
//
// Query language: `[rate ]<endpoint>[?k=v&…] <path>` where path is dot-separated keys with array selectors
// `key[3]` or `key[field=value]`, e.g. `varz in_msgs`, `rate varz out_bytes` or
// `jsz?streams=true account_details[0].stream_detail[name=EVENTS].state.messages`. `rate` turns a monotonic counter
// into a per-second rate from the previous sample (inferred). The table shape takes a path to an array of objects.
type NATSMon struct {
	mu   sync.Mutex
	prev map[string]counterSample
}

type counterSample struct {
	v  float64
	at time.Time
}

const natsSchema = `{
  "type": "object", "additionalProperties": false, "required": ["url"],
  "properties": {` + httpSchemaProps + `}
}`

var natsEndpoints = map[string]bool{"varz": true, "connz": true, "routez": true, "gatewayz": true, "leafz": true, "subsz": true, "jsz": true,
	"healthz": true, "accountz": true, "accstatz": true}

var natsSeg = regexp.MustCompile(`^([A-Za-z0-9_]+)(\[([0-9]+|[A-Za-z0-9_]+=[^\]]{1,256})\])?$`)

// Info implements Adapter.
func (*NATSMon) Info() AdapterInfo {
	return AdapterInfo{Type: "natsmon", Title: "NATS monitoring", Description: "Reads the NATS server HTTP monitoring endpoint (varz, connz, jsz, …). " +
		"Expose it privately (it has no authentication) and list its address in allow_cidrs.", ConfigSchema: json.RawMessage(natsSchema),
		Shapes:    []string{ShapeScalar, ShapeTable},
		QueryHelp: "`[rate ]<endpoint>[?query] <path>`, e.g. `rate varz in_msgs` or `jsz?streams=true account_details[0].stream_detail[name={{stream}}].state.messages`.",
		Verified:  "contract tests against NATS monitoring JSON fixtures; local nats-server validation in tests (-tags with DEED_TEST_NATS)"}
}

// Validate implements Adapter.
func (*NATSMon) Validate(cfg json.RawMessage, _ map[string]string) error { return validateHTTP(cfg) }

type natsQuery struct {
	rate     bool
	endpoint string
	params   url.Values
	path     []string
}

func parseNATSQuery(text string) (*natsQuery, error) {
	f := strings.Fields(text)
	q := &natsQuery{}
	if len(f) > 0 && f[0] == "rate" {
		q.rate, f = true, f[1:]
	}
	if len(f) != 2 {
		return nil, fmt.Errorf("expected `[rate ]<endpoint>[?query] <path>`")
	}
	ep, raw, _ := strings.Cut(f[0], "?")
	if !natsEndpoints[ep] {
		return nil, fmt.Errorf("unknown monitoring endpoint %q", ep)
	}
	params, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint query")
	}
	q.endpoint, q.params = ep, params
	for _, seg := range strings.Split(f[1], ".") {
		if !natsSeg.MatchString(seg) {
			return nil, fmt.Errorf("invalid path segment %q", seg)
		}
		q.path = append(q.path, seg)
	}
	return q, nil
}

// ValidateQuery implements Adapter.
func (*NATSMon) ValidateQuery(text, shape string) error {
	q, err := parseNATSQuery(text)
	if err != nil {
		return err
	}
	if q.rate && shape != ShapeScalar {
		return fmt.Errorf("rate needs the scalar shape")
	}
	return nil
}

// Escape implements Adapter: values may only appear inside selectors, so anything that could break the grammar is removed.
func (*NATSMon) Escape(v string) string {
	return strings.NewReplacer("]", "", " ", "", ".", "_dot_", "&", "", "?", "").Replace(v)
}

func walkPath(doc any, path []string) (any, error) {
	cur := doc
	for _, seg := range path {
		m := natsSeg.FindStringSubmatch(seg)
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q: not an object", seg)
		}
		cur, ok = obj[m[1]]
		if !ok {
			return nil, fmt.Errorf("path %q: missing", m[1])
		}
		if m[3] == "" {
			continue
		}
		arr, ok := cur.([]any)
		if !ok {
			return nil, fmt.Errorf("path %q: not an array", m[1])
		}
		if i, err := strconv.Atoi(m[3]); err == nil {
			if i >= len(arr) {
				return nil, fmt.Errorf("path %q: index out of range", m[1])
			}
			cur = arr[i]
			continue
		}
		k, want, _ := strings.Cut(m[3], "=")
		want = strings.ReplaceAll(want, "_dot_", ".")
		found := false
		for _, el := range arr {
			if o, ok := el.(map[string]any); ok && fmt.Sprint(o[k]) == want {
				cur, found = o, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("path %q: no element with %s", m[1], m[3])
		}
	}
	return cur, nil
}

func (n *NATSMon) fetch(ctx context.Context, c Conn, q *natsQuery) (any, error) {
	cl, _, err := newHTTPClient(c, 8<<20)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := cl.do(ctx, http.MethodGet, "/"+q.endpoint, q.params, nil, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Test implements Adapter.
func (n *NATSMon) Test(ctx context.Context, c Conn) (string, error) {
	doc, err := n.fetch(ctx, c, &natsQuery{endpoint: "varz"})
	if err != nil {
		return "", err
	}
	o, _ := doc.(map[string]any)
	return fmt.Sprintf("NATS %v (%v) monitoring reachable", o["server_name"], o["version"]), nil
}

// Query implements Adapter.
func (n *NATSMon) Query(ctx context.Context, c Conn, qq Query) (*TelemetryData, error) {
	q, err := parseNATSQuery(qq.Text)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "%s", err.Error())
	}
	doc, err := n.fetch(ctx, c, q)
	if err != nil {
		return nil, err
	}
	v, err := walkPath(doc, q.path)
	if err != nil {
		return nil, apperr.New(apperr.CodeNotFound, "%s", err.Error())
	}
	d := &TelemetryData{}
	if qq.Shape == ShapeTable {
		arr, ok := v.([]any)
		if !ok {
			return nil, apperr.New(apperr.CodeInvalid, "table shape needs a path to an array")
		}
		for _, el := range arr {
			o, ok := el.(map[string]any)
			if !ok {
				continue
			}
			row := map[string]string{}
			keys := make([]string, 0, len(o))
			for k := range o {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch x := o[k].(type) {
				case string, float64, bool:
					if len(row) < 20 {
						row[k] = fmt.Sprint(x)
					}
				}
			}
			d.Rows = append(d.Rows, row)
		}
		return d, nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalid, "path does not point at a number")
	}
	if !q.rate {
		d.Value = &f
		return d, nil
	}
	// Rates are derived from the previous sample of the same counter on the same integration.
	key := c.Name + "|" + qq.Text
	now := time.Now()
	n.mu.Lock()
	if n.prev == nil {
		n.prev = map[string]counterSample{}
	}
	prev, had := n.prev[key]
	n.prev[key] = counterSample{v: f, at: now}
	n.mu.Unlock()
	d.Inferred = true
	if !had || now.Sub(prev.at) < time.Second || f < prev.v {
		d.Note = "rate needs two samples; available on the next refresh"
		return d, nil
	}
	r := (f - prev.v) / now.Sub(prev.at).Seconds()
	d.Value = &r
	d.Note = fmt.Sprintf("counter delta over %s", now.Sub(prev.at).Round(time.Second))
	return d, nil
}
