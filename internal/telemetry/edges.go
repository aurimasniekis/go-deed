package telemetry

import (
	"context"
	"slices"
	"time"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/pulseint"
)

// EdgeRate is the measured activity of one relationship for traffic overlays. Rate is set only when a measurement
// supports it; the UI animates traffic only for measured (or explicitly labelled inferred) values.
type EdgeRate struct {
	RelationID string     `json:"relation_id"`
	Type       string     `json:"type"`
	Rate       *float64   `json:"rate,omitempty"`
	Unit       string     `json:"unit,omitempty"`
	Status     string     `json:"status" enum:"measured,inferred,stale,unavailable"`
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
	Window     string     `json:"window"`
	Basis      string     `json:"basis" doc:"Where the number comes from: pulse_tail, binding:<name>, or none."`
	Note       string     `json:"note,omitempty"`
}

// MaxEdgeRates bounds one request.
const MaxEdgeRates = 200

// EdgeRates measures relationships (already authorized by the caller).
func (s *Service) EdgeRates(ctx context.Context, rels []*catalog.Relation, window string) ([]EdgeRate, error) {
	w, ok := Windows[window]
	if !ok {
		window, w = "15m", 15*time.Minute
	}
	if len(rels) > MaxEdgeRates {
		rels = rels[:MaxEdgeRates]
	}
	ids := map[string]bool{}
	for _, r := range rels {
		ids[r.From.ID], ids[r.To.ID] = true, true
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	ents := map[string]*catalog.Entity{}
	if len(list) > 0 {
		page, err := s.Catalog.ListEntities(ctx, catalog.EntityFilter{IDs: list, Limit: len(list)})
		if err != nil {
			return nil, err
		}
		for _, e := range page.Items {
			ents[e.Metadata.ID] = e
		}
	}
	bindings, err := s.Store.Bindings(ctx)
	if err != nil {
		return nil, err
	}
	byType := map[string][]*Binding{}
	for _, b := range bindings {
		if b.Role == "edge_rate" {
			byType[b.RelationshipType] = append(byType[b.RelationshipType], b)
		}
	}
	// Pulse rollups of each project's tail sources, read once per request.
	rollups := map[string][]Rollup{}
	now := s.Clock.Now()
	gran, size := granularityFor(w)
	end := now.Truncate(size)
	pulseFor := func(projectID string) ([]Rollup, bool, error) {
		if rs, ok := rollups[projectID]; ok {
			return rs, rs != nil, nil
		}
		srcs, err := s.Catalog.Sources(ctx, []string{projectID})
		if err != nil {
			return nil, false, err
		}
		var tails []string
		for _, src := range srcs {
			if src.Provider == pulseint.TailType && src.Enabled {
				tails = append(tails, src.ID)
			}
		}
		if len(tails) == 0 {
			rollups[projectID] = nil
			return nil, false, nil
		}
		rs, err := Rollups(ctx, s.Store.Store, gran, end.Add(-w), end, tails)
		if err != nil {
			return nil, false, err
		}
		if rs == nil {
			rs = []Rollup{}
		}
		rollups[projectID] = rs
		return rs, true, nil
	}
	out := make([]EdgeRate, 0, len(rels))
	for _, r := range rels {
		er := EdgeRate{RelationID: r.ID, Type: r.Type, Window: window, Status: StatusUnavailable, Basis: "none"}
		from, to := ents[r.From.ID], ents[r.To.ID]
		if from == nil || to == nil {
			er.Note = "endpoint not readable"
			out = append(out, er)
			continue
		}
		done := false
		for _, b := range byType[r.Type] {
			if len(b.Kinds) > 0 && !slices.Contains(b.Kinds, from.Kind) {
				continue
			}
			res := s.Run(ctx, b, Subject{From: from, To: to}, window)
			if res.Data == nil || res.Data.Value == nil {
				continue
			}
			v := *res.Data.Value
			er.Rate, er.Unit, er.Status, er.MeasuredAt, er.Basis, er.Note = &v, b.Unit, res.Status, res.MeasuredAt, "binding:"+b.Name, res.Data.Note
			done = true
			break
		}
		if !done && (r.Type == "publishes" || r.Type == "subscribes_to") && to.Kind == "nats_subject" {
			s.pulseEdge(&er, r, from, to, pulseFor, w, end)
		} else if !done {
			er.Note = "no measurement source for " + r.Type
		}
		out = append(out, er)
	}
	return out, nil
}

func (s *Service) pulseEdge(er *EdgeRate, r *catalog.Relation, from, to *catalog.Entity, pulseFor func(string) ([]Rollup, bool, error), w time.Duration, end time.Time) {
	subject, _ := to.Spec["subject"].(string)
	if subject == "" {
		er.Note = "subject entity has no spec.subject"
		return
	}
	rs, ok, err := pulseFor(r.ProjectID)
	if err != nil {
		er.Note = "rollups unavailable"
		return
	}
	if !ok {
		er.Note = "no pulse_tail source in this project"
		return
	}
	publisher, _ := from.Spec["pulse_source"].(string)
	if r.Type == "publishes" && publisher == "" {
		er.Note = "publisher has no spec.pulse_source to match ce-source"
		return
	}
	var sum float64
	sampled := false
	for _, x := range rs {
		if subject != x.Subject && !pulseint.Matches(subject, x.Subject) {
			continue
		}
		if r.Type == "publishes" && x.Publisher != publisher {
			continue
		}
		sum += x.Messages
		sampled = sampled || x.Sampled
	}
	rate := sum / w.Seconds()
	t := end
	er.Rate, er.Unit, er.MeasuredAt, er.Basis = &rate, "msg/s", &t, "pulse_tail"
	er.Status = StatusMeasured
	er.Note = "messages observed by the evt.> tail, " + promDuration(w) + " window"
	if r.Type == "subscribes_to" {
		er.Status = StatusInferred
		er.Note = "publish rate of the subject; delivery to this subscriber is not measured"
	}
	if sampled {
		er.Status = StatusInferred
		er.Note += "; sampled"
	}
}
