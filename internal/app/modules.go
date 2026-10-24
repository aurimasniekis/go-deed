package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"

	"github.com/aurimasniekis/go-deed/internal/events"
)

// loop is a named background job; run returns when ctx ends.
type loop struct {
	name string
	run  func(ctx context.Context)
}

func (a *App) addLoop(name string, fn func(ctx context.Context)) {
	a.loops = append(a.loops, loop{name: name, run: fn})
}

// every runs fn immediately and then every interval until ctx ends, recording job metrics.
func (a *App) every(name string, interval time.Duration, fn func(ctx context.Context) error) {
	a.addLoop(name, func(ctx context.Context) {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			err := fn(ctx)
			switch {
			case err == nil:
				a.Metrics.JobRun(name, "ok")
			case ctx.Err() != nil:
				return
			default:
				a.Metrics.JobRun(name, "error")
				a.Log.Warn("background job failed", "event.action", "job."+name, "event.outcome", "failure", "error.message", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
}

// initModules wires the optional subsystems and their background jobs.
func (a *App) initModules(ctx context.Context) error {
	a.initMaintenance()
	if err := a.initPulse(ctx); err != nil {
		return err
	}
	return a.initExtensions(ctx)
}

// initPulse starts the outbox relay (and, through initExtensions, the ingest consumer and tail) once Pulse is
// reachable. Connection failures never block start-up or catalog writes: the relay keeps retrying every 30 s.
func (a *App) initPulse(_ context.Context) error {
	p := a.Cfg.Pulse
	relay := &events.Relay{Store: a.Store, Clock: a.Clock, Metrics: a.Metrics, Log: a.Log.With("component", "relay"), Retention: a.Cfg.Retention.Outbox.D()}
	a.every("outbox_maintain", time.Minute, relay.Maintain)
	if !p.Enabled || !p.Publish {
		return nil
	}
	a.addLoop("pulse_relay", func(ctx context.Context) {
		c := a.connectPulse(ctx, "publisher")
		if c == nil {
			return
		}
		defer c.Close()
		relay.Pub = c
		relay.Run(ctx, 2*time.Second)
	})
	return nil
}

// connectPulse dials Pulse, retrying every 30 s until it succeeds or ctx ends.
func (a *App) connectPulse(ctx context.Context, conn string) *pulse.Client {
	p := a.Cfg.Pulse
	log := a.Log.With("component", "pulse", "conn", conn)
	for {
		opts := []pulse.Option{pulse.WithCredsFile(p.CredsFile), pulse.WithSource(p.Source), pulse.WithName("deed-" + conn)}
		if p.CAFile != "" {
			opts = append(opts, pulse.WithCAFile(p.CAFile))
		}
		c, err := pulse.Connect(p.URL, opts...)
		if err == nil {
			a.Metrics.PulseConnected(conn, true)
			log.Info("connected to pulse", "event.action", "pulse.connect", "event.outcome", "success")
			return c
		}
		a.Metrics.PulseConnected(conn, false)
		log.Warn("pulse unavailable; retrying in 30s", "event.action", "pulse.connect", "event.outcome", "failure", "error.message", err.Error())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

var _ = slog.Default
