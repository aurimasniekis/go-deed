package pulseint

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/ingest"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
)

// Ingest event types (versioned envelopes mirroring the source-protocol API).
const (
	TypeItems        = "deed.ingest.items.v1"
	TypeObservations = "deed.ingest.observations.v1"
	TypeHeartbeat    = "deed.ingest.heartbeat.v1"
)

// Envelope is the payload of an ingest event.
type Envelope struct {
	SourceID     string                 `json:"source_id"`
	Entities     []catalog.EntityItem   `json:"entities,omitempty"`
	Relations    []catalog.RelationItem `json:"relations,omitempty"`
	Observations []catalog.EntityItem   `json:"observations,omitempty"`
	Atomic       bool                   `json:"atomic,omitempty"`
}

// Consumer applies ingest events from Pulse. A source accepts events only from the publisher named in its config
// (`pulse_source`, matched against ce-source), so a Pulse client can never write into another source.
type Consumer struct {
	Catalog *catalog.Service
	Ingest  *ingest.Service
	Metrics *metrics.Metrics
	Log     *slog.Logger
}

// ErrNotAllowed is terminal: the publisher may not write into the source.
var ErrNotAllowed = errors.New("publisher is not allowed to write into this source")

// Handle processes one event. Validation and authorization failures are terminal (never redelivered); storage
// failures are retried by Pulse with backoff.
func (c *Consumer) Handle(ctx context.Context, e pulse.Event) error {
	err := c.handle(ctx, e)
	switch {
	case err == nil:
		c.Metrics.PulseConsumed("ok")
		return nil
	case apperr.CodeOf(err) != "" || errors.Is(err, ErrNotAllowed):
		c.Metrics.PulseConsumed("terminal")
		c.Log.Warn("ingest event rejected", "event.action", "pulse.ingest", "event.outcome", "failure", "event.id", e.ID, "event.type", e.Type,
			"publisher", e.Source, "error.message", err.Error())
		return pulse.Terminal(err)
	default:
		c.Metrics.PulseConsumed("retry")
		return err
	}
}

func (c *Consumer) handle(ctx context.Context, e pulse.Event) error {
	if len(e.Data) > 8<<20 {
		return apperr.New(apperr.CodeInvalid, "event too large")
	}
	var env Envelope
	if err := json.Unmarshal(e.Data, &env); err != nil {
		return apperr.New(apperr.CodeInvalid, "event is not a deed ingest envelope")
	}
	src, err := c.Catalog.Source(ctx, env.SourceID)
	if err != nil {
		return apperr.New(apperr.CodeNotFound, "unknown source")
	}
	var cfg struct {
		PulseSource string `json:"pulse_source"`
	}
	_ = json.Unmarshal(src.Config, &cfg)
	if src.Provider != "push" || !src.Enabled || cfg.PulseSource == "" || cfg.PulseSource != e.Source {
		return ErrNotAllowed
	}
	actor := audit.Actor{Type: audit.ActorSource, ID: src.ID}
	switch e.Type {
	case TypeItems:
		res, err := c.Ingest.Items(ctx, src, ingest.Batch{Entities: env.Entities, Relations: env.Relations}, env.Atomic, actor)
		if err != nil {
			return err
		}
		if res.Summary.Errors > 0 {
			c.Log.Info("ingest event applied with item errors", "event.id", e.ID, "errors", res.Summary.Errors)
		}
		return nil
	case TypeObservations:
		_, err := c.Ingest.Observations(ctx, src, env.Observations, actor)
		return err
	case TypeHeartbeat:
		return c.Catalog.Heartbeat(ctx, src.ID)
	}
	return apperr.New(apperr.CodeInvalid, "unsupported event type %q", e.Type)
}

// Run subscribes until ctx ends. With a Pulse client identity the subscription is durable (acked, redelivered on
// failure, resumed after restarts); without one (tests) it is an ordered consumer.
func (c *Consumer) Run(ctx context.Context, client *pulse.Client, filter, durable string) error {
	opts := []pulse.SubOption{pulse.MaxDeliver(10), pulse.OnError(func(e pulse.Event, err error) {
		c.Log.Debug("ingest delivery error", "event.id", e.ID, "error.message", err.Error())
	})}
	if client.ClientID() != "" && durable != "" {
		opts = append(opts, pulse.Durable(durable))
	} else {
		opts = append(opts, pulse.OnlyNew())
	}
	sub, err := client.Subscribe(ctx, filter, c.Handle, opts...)
	if err != nil {
		return err
	}
	c.Log.Info("pulse ingest consumer running", "event.action", "pulse.ingest", "filter", filter, "consumer", sub.Consumer())
	<-ctx.Done()
	sub.Stop()
	return nil
}
