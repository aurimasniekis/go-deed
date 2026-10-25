// Command pulsepub is the e2e helper for Pulse-compatible traffic against a local NATS JetStream server (no pulsed):
// it creates the EVENTS stream like pulsed does, publishes CloudEvents-style messages with go-pulse as a named client,
// and waits for Deed's own events. Labelled simulated in docs/VALIDATION.md: real pulsed validation is separate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pulsepub:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: pulsepub stream|publish|ingest|wait [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	url := fs.String("url", "nats://127.0.0.1:34222", "NATS URL")
	source := fs.String("source", "", "publisher identity (ce-source)")
	subject := fs.String("subject", "", "subject")
	typ := fs.String("type", "", "event type")
	n := fs.Int("n", 1, "messages to publish")
	deedSource := fs.String("deed-source", "", "Deed push source ID (ingest)")
	file := fs.String("file", "", "JSON file with {entities, relations} (ingest)")
	timeout := fs.Duration("timeout", 60*time.Second, "wait timeout")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+10*time.Second)
	defer cancel()
	nc, err := nats.Connect(*url, nats.Name("deed-e2e-pulsepub"))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	switch os.Args[1] {
	case "stream":
		_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"evt.>"}, Duplicates: 2 * time.Minute})
		if err == nil {
			fmt.Println("stream EVENTS ready")
		}
		return err
	case "publish":
		c, err := pulse.New(nc, *source)
		if err != nil {
			return err
		}
		for i := range *n {
			if _, err := c.Publish(ctx, fmt.Sprintf(*subject, i), *typ, map[string]any{"n": i, "synthetic": true}); err != nil {
				return err
			}
		}
		fmt.Printf("published %d %s as %s\n", *n, *typ, *source)
		return nil
	case "ingest":
		b, err := os.ReadFile(*file) //nolint:gosec // operator-chosen fixture path
		if err != nil {
			return err
		}
		var env map[string]any
		if err := json.Unmarshal(b, &env); err != nil {
			return err
		}
		env["source_id"] = *deedSource
		c, err := pulse.New(nc, *source)
		if err != nil {
			return err
		}
		if _, err := c.Publish(ctx, "evt.deed.ingest."+*source+".items", "deed.ingest.items.v1", env); err != nil {
			return err
		}
		fmt.Println("ingest envelope published as", *source)
		return nil
	case "wait":
		// The last message on the subject in the stream (Deed publishes through its outbox with retries).
		st, err := js.Stream(ctx, "EVENTS")
		if err != nil {
			return err
		}
		deadline := time.Now().Add(*timeout)
		for time.Now().Before(deadline) {
			m, err := st.GetLastMsgForSubject(ctx, *subject)
			if err == nil {
				fmt.Printf("received %s type=%s source=%s bytes=%d\n", m.Subject, m.Header.Get("ce-type"), m.Header.Get("ce-source"), len(m.Data))
				return nil
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("no message on %s within %s", *subject, *timeout)
	}
	return fmt.Errorf("unknown command %q", os.Args[1])
}
