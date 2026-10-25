package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/pulseint"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// PulseRollups reads the pulse_tail rollups Deed keeps itself: message counts per (publisher, subject template,
// event type) per minute and hour. Counts are exact unless the tail hit its key budget (then marked sampled).
//
// Query language: `rate|messages|bytes [publisher=<ce-source>] [subject=<pattern>] [type=<ce-type>]`; subject accepts
// NATS wildcards. rate is messages per second over the window.
type PulseRollups struct {
	Store *store.Store
	Clock clock.Clock
}

// Info implements Adapter.
func (*PulseRollups) Info() AdapterInfo {
	return AdapterInfo{Type: "pulse", Title: "Pulse activity (built-in)", Description: "Message rates from Deed's own read-only evt.> tail " +
		"(pulse_tail sources). Publishers come from ce-source; Pulse records no per-subscriber delivery, so these are publish rates.",
		ConfigSchema: json.RawMessage(`{"type": "object", "additionalProperties": false, "properties": {}}`), Local: true,
		Shapes:    []string{ShapeScalar, ShapeTimeseries, ShapeTable},
		QueryHelp: "`rate publisher={{pulse_source}} subject=evt.app.>`; verbs rate, messages, bytes.",
		Verified:  "tests against a local nats-server through the pulse_tail aggregator"}
}

// Validate implements Adapter.
func (*PulseRollups) Validate(json.RawMessage, map[string]string) error { return nil }

type pulseQuery struct {
	verb                     string
	publisher, subject, kind string
}

func parsePulse(text string) (*pulseQuery, error) {
	f := strings.Fields(text)
	if len(f) == 0 || (f[0] != "rate" && f[0] != "messages" && f[0] != "bytes") {
		return nil, fmt.Errorf("expected rate, messages or bytes")
	}
	q := &pulseQuery{verb: f[0]}
	for _, kv := range f[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" || len(v) > 256 {
			return nil, fmt.Errorf("expected key=value, got %q", kv)
		}
		switch k {
		case "publisher":
			q.publisher = v
		case "subject":
			q.subject = v
		case "type":
			q.kind = v
		default:
			return nil, fmt.Errorf("unknown key %q", k)
		}
	}
	return q, nil
}

// ValidateQuery implements Adapter.
func (*PulseRollups) ValidateQuery(text, _ string) error {
	_, err := parsePulse(text)
	return err
}

// Escape implements Adapter: whitespace would split the key=value grammar.
func (*PulseRollups) Escape(v string) string { return strings.Join(strings.Fields(v), "") }

// Test implements Adapter.
func (p *PulseRollups) Test(ctx context.Context, _ Conn) (string, error) {
	var n int64
	var last sql.NullInt64
	if err := p.Store.R().QueryRowContext(ctx, `SELECT COUNT(*), MAX(bucket) FROM pulse_rollups WHERE granularity = 'minute'`).Scan(&n, &last); err != nil {
		return "", err
	}
	if !last.Valid {
		return "no Pulse activity recorded yet (create a pulse_tail source)", nil
	}
	return fmt.Sprintf("%d minute rollups, latest %s", n, clock.FromMicros(last.Int64).UTC().Format(time.RFC3339)), nil
}

// Rollup is one aggregated bucket.
type Rollup struct {
	Bucket    time.Time
	Publisher string
	Subject   string
	Type      string
	Messages  float64
	Bytes     float64
	Sampled   bool
}

// Rollups reads buckets in [start, end) at a granularity, optionally restricted to the given tail sources.
func Rollups(ctx context.Context, s *store.Store, granularity string, start, end time.Time, sourceIDs []string) ([]Rollup, error) {
	q := `SELECT bucket, publisher, subject, ce_type, messages, bytes, sampled FROM pulse_rollups WHERE granularity = ? AND bucket >= ? AND bucket < ?`
	args := []any{granularity, start.UnixMicro(), end.UnixMicro()}
	if sourceIDs != nil {
		if len(sourceIDs) == 0 {
			return nil, nil
		}
		q += ` AND source_id IN (?` + strings.Repeat(", ?", len(sourceIDs)-1) + `)`
		for _, id := range sourceIDs {
			args = append(args, id)
		}
	}
	rows, err := s.R().QueryContext(ctx, q+` ORDER BY bucket LIMIT 200000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rollup
	for rows.Next() {
		var r Rollup
		var b int64
		if err := rows.Scan(&b, &r.Publisher, &r.Subject, &r.Type, &r.Messages, &r.Bytes, &r.Sampled); err != nil {
			return nil, err
		}
		r.Bucket = clock.FromMicros(b)
		out = append(out, r)
	}
	return out, rows.Err()
}

func granularityFor(window time.Duration) (string, time.Duration) {
	if window > 6*time.Hour {
		return "hour", time.Hour
	}
	return "minute", time.Minute
}

func (q *pulseQuery) match(r Rollup) bool {
	return (q.publisher == "" || q.publisher == r.Publisher) && (q.kind == "" || q.kind == r.Type) &&
		(q.subject == "" || pulseint.Matches(q.subject, r.Subject) || q.subject == r.Subject)
}

// Query implements Adapter.
func (p *PulseRollups) Query(ctx context.Context, _ Conn, qq Query) (*TelemetryData, error) {
	q, err := parsePulse(qq.Text)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "%s", err.Error())
	}
	window := qq.End.Sub(qq.Start)
	gran, size := granularityFor(window)
	// Scalars include the current (partial) bucket; time series show complete buckets only.
	end := p.Clock.Now().Truncate(size)
	if qq.Shape != ShapeTimeseries {
		end = p.Clock.Now().Add(time.Microsecond)
	}
	start := end.Add(-window).Truncate(size)
	rs, err := Rollups(ctx, p.Store, gran, start, end, nil)
	if err != nil {
		return nil, err
	}
	d := &TelemetryData{Note: fmt.Sprintf("Pulse tail rollups, %s buckets", gran)}
	value := func(r Rollup) float64 {
		if q.verb == "bytes" {
			return r.Bytes
		}
		return r.Messages
	}
	sampled := false
	switch qq.Shape {
	case ShapeScalar:
		var sum float64
		for _, r := range rs {
			if q.match(r) {
				sum += value(r)
				sampled = sampled || r.Sampled
			}
		}
		if q.verb == "rate" {
			sum /= window.Seconds()
		}
		d.Value = &sum
	case ShapeTimeseries:
		byBucket := map[int64]float64{}
		for _, r := range rs {
			if q.match(r) {
				byBucket[r.Bucket.UnixMicro()] += value(r)
				sampled = sampled || r.Sampled
			}
		}
		s := TelemetrySeries{Labels: map[string]string{"verb": q.verb}, Points: []TelemetryPoint{}}
		for t := start; t.Before(end); t = t.Add(size) {
			v := byBucket[t.UnixMicro()]
			if q.verb == "rate" {
				v /= size.Seconds()
			}
			s.Points = append(s.Points, TelemetryPoint{T: t, V: v})
		}
		d.Series = []TelemetrySeries{s}
	case ShapeTable:
		type key struct{ pub, sub, typ string }
		agg := map[key]float64{}
		for _, r := range rs {
			if q.match(r) {
				agg[key{r.Publisher, r.Subject, r.Type}] += value(r)
				sampled = sampled || r.Sampled
			}
		}
		for k, v := range agg {
			if q.verb == "rate" {
				v /= window.Seconds()
			}
			d.Rows = append(d.Rows, map[string]string{"publisher": k.pub, "subject": k.sub, "type": k.typ, "value": fmt.Sprintf("%.4g", v)})
		}
	}
	if sampled {
		d.Inferred = true
		d.Note += "; sampled (the tail exceeded its key budget)"
	}
	return d, nil
}
