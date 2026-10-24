// Command deed-agent is the reference external source runner: it collects Docker host facts and pushes them to a
// Deed push source as complete snapshots with a source token (deed_st_…). It needs read access to the Docker socket
// and nothing else; it never sends environment variables, commands or unlisted labels.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aurimasniekis/go-deed/internal/agent"
	"github.com/aurimasniekis/go-deed/internal/buildinfo"
	"github.com/aurimasniekis/go-deed/pkg/deedclient"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "deed-agent:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run() error {
	fs := flag.NewFlagSet("deed-agent", flag.ContinueOnError)
	endpoint := fs.String("endpoint", env("DEED_ENDPOINT", ""), "Deed base URL ($DEED_ENDPOINT)")
	source := fs.String("source", env("DEED_SOURCE_ID", ""), "push source ID ($DEED_SOURCE_ID)")
	tokenFile := fs.String("token-file", "", "file holding the source token (default $DEED_SOURCE_TOKEN)")
	caFile := fs.String("ca-file", env("DEED_CA_FILE", ""), "extra CA bundle for the Deed endpoint")
	docker := fs.String("docker", env("DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker endpoint")
	labels := fs.String("labels", "", "comma-separated extra container label keys to forward (values must not be secrets)")
	interval := fs.Duration("interval", time.Minute, "time between snapshots")
	once := fs.Bool("once", false, "send one snapshot and exit")
	dryRun := fs.Bool("dry-run", false, "print the snapshot as JSON instead of sending it")
	preview := fs.Bool("preview", false, "send the snapshot as a dry-run commit and print the summary")
	version := fs.Bool("version", false, "print the version")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *version {
		fmt.Println(buildinfo.Get().Version)
		return nil
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	d, err := agent.NewDocker(*docker)
	if err != nil {
		return err
	}
	if *labels != "" {
		for _, l := range strings.Split(*labels, ",") {
			if l = strings.TrimSpace(l); l != "" {
				d.Labels = append(d.Labels, l)
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun {
		b, err := d.Collect(ctx)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(b)
	}
	token := os.Getenv("DEED_SOURCE_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	opts := []deedclient.Option{deedclient.WithUserAgent("deed-agent/" + buildinfo.Get().Version)}
	if *caFile != "" {
		opts = append(opts, deedclient.WithCAFile(*caFile))
	}
	c, err := deedclient.New(*endpoint, token, *source, opts...)
	if err != nil {
		return err
	}
	push := func() error {
		start := time.Now()
		b, err := d.Collect(ctx)
		if err != nil {
			return err
		}
		res, err := c.Sync(ctx, b, deedclient.CommitOptions{DryRun: *preview})
		if err != nil {
			return err
		}
		log.Info("snapshot sent", "event.action", "agent.snapshot", "items", b.Len(), "created", res.Summary.Created, "updated", res.Summary.Updated,
			"removed", res.Summary.Removed, "errors", res.Summary.Errors, "dry_run", res.DryRun, "duration_ms", time.Since(start).Milliseconds())
		return nil
	}
	if err := push(); err != nil {
		if *once || *preview {
			return err
		}
		log.Warn("snapshot failed", "event.action", "agent.snapshot", "event.outcome", "failure", "error.message", err.Error())
	}
	if *once || *preview {
		return nil
	}
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := push(); err != nil {
				log.Warn("snapshot failed", "event.action", "agent.snapshot", "event.outcome", "failure", "error.message", err.Error())
			}
		}
	}
}
