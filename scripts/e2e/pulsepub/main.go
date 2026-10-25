// Command pulsepub is the e2e helper for Pulse traffic. Against a local NATS JetStream server (scripts/e2e.sh, no
// pulsed) it creates the EVENTS stream like pulsed does and publishes as an unauthenticated client; with -creds (and
// -ca) it connects to a real pulsed NATS listener as a Pulse client (scripts/validate/pulse.sh). It publishes
// CloudEvents-style messages with go-pulse, sends Deed ingest envelopes, waits for Deed's own events and dumps the
// events on a filter with their raw headers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
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

func connect(url, creds, ca, source string) (*pulse.Client, error) {
	if creds == "" {
		nc, err := nats.Connect(url, nats.Name("deed-e2e-pulsepub"))
		if err != nil {
			return nil, err
		}
		return pulse.New(nc, source)
	}
	opts := []pulse.Option{pulse.WithCredsFile(creds), pulse.WithSource(source), pulse.WithName("deed-validate-pulsepub")}
	if ca != "" {
		opts = append(opts, pulse.WithCAFile(ca))
	}
	return pulse.Connect(url, opts...)
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: pulsepub stream|publish|ingest|wait|durable|events [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	url := fs.String("url", "nats://127.0.0.1:34222", "NATS URL")
	creds := fs.String("creds", "", "Pulse client .creds file (real pulsed); empty for an unauthenticated NATS")
	ca := fs.String("ca", "", "extra CA (PEM) for a tls:// URL")
	source := fs.String("source", "", "publisher identity (ce-source)")
	subject := fs.String("subject", "", "subject (publish: may contain one %d for the message index)")
	filter := fs.String("filter", "evt.>", "subject filter (events)")
	typ := fs.String("type", "", "event type")
	id := fs.String("id", "", "event ID (Nats-Msg-Id) for every published message; empty generates one per message")
	n := fs.Int("n", 1, "messages to publish")
	name := fs.String("name", "", "durable consumer name (durable)")
	deedSource := fs.String("deed-source", "", "Deed push source ID (ingest)")
	file := fs.String("file", "", "JSON file with {entities, relations} (ingest)")
	timeout := fs.Duration("timeout", 60*time.Second, "wait timeout")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+10*time.Second)
	defer cancel()
	c, err := connect(*url, *creds, *ca, *source)
	if err != nil {
		return err
	}
	defer func() {
		if *creds == "" {
			c.Conn().Close()
		} else {
			c.Close()
		}
	}()
	js := c.JetStream()
	switch os.Args[1] {
	case "stream":
		_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"evt.>"}, Duplicates: 2 * time.Minute})
		if err == nil {
			fmt.Println("stream EVENTS ready")
		}
		return err
	case "publish":
		var opts []pulse.PublishOption
		if *id != "" {
			opts = append(opts, pulse.WithID(*id))
		}
		dups := 0
		var last *pulse.PubAck
		for i := range *n {
			subj := *subject
			if strings.Contains(subj, "%") {
				subj = fmt.Sprintf(subj, i)
			}
			ack, err := c.Publish(ctx, subj, *typ, map[string]any{"n": i, "synthetic": true}, opts...)
			if err != nil {
				return err
			}
			if ack.Duplicate {
				dups++
			}
			last = ack
		}
		fmt.Printf("published %d %s as %s (duplicates %d, last id %s seq %d)\n", *n, *typ, *source, dups, last.ID, last.Sequence)
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
		subj := *subject
		if subj == "" {
			subj = "evt.deed.ingest." + *source + ".items"
		}
		t := *typ
		if t == "" {
			t = "deed.ingest.items.v1"
		}
		ack, err := c.Publish(ctx, subj, t, env)
		if err != nil {
			return err
		}
		fmt.Printf("ingest envelope published as %s on %s (id %s seq %d)\n", *source, subj, ack.ID, ack.Sequence)
		return nil
	case "wait":
		// The last message on the subject in the stream (Deed publishes through its outbox with retries).
		deadline := time.Now().Add(*timeout)
		for time.Now().Before(deadline) {
			evs, err := dump(ctx, js, *subject, 2*time.Second)
			if err == nil && len(evs) > 0 {
				m := evs[len(evs)-1]
				fmt.Printf("received %s type=%s source=%s bytes=%d\n", m.Subject, m.Headers["ce-type"], m.Headers["ce-source"], m.Bytes)
				return nil
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("no message on %s within %s", *subject, *timeout)
	case "durable":
		// Creates (or binds to) the durable consumer "<client-id>_<name>" on filter, like a subscriber would at start-up.
		sub, err := c.Subscribe(ctx, *filter, func(context.Context, pulse.Event) error { return nil }, pulse.Durable(*name), pulse.OnlyNew())
		if err != nil {
			return err
		}
		fmt.Println("durable consumer", sub.Consumer(), "on", *filter)
		sub.Stop()
		return nil
	case "events":
		evs, err := dump(ctx, js, *filter, 3*time.Second)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", os.Args[1])
}

// stored is one stored event with its raw headers.
type stored struct {
	Subject    string            `json:"subject"`
	Sequence   uint64            `json:"sequence"`
	StreamTime time.Time         `json:"stream_time"`
	Headers    map[string]string `json:"headers"`
	Bytes      int               `json:"bytes"`
	Data       json.RawMessage   `json:"data,omitempty"`
}

// dump replays every stored event on filter through an ordered consumer (what a subscribe grant allows) until the
// stream has nothing pending or idle passes without a message.
func dump(ctx context.Context, js jetstream.JetStream, filter string, idle time.Duration) ([]stored, error) {
	cons, err := js.OrderedConsumer(ctx, "EVENTS", jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}, DeliverPolicy: jetstream.DeliverAllPolicy})
	if err != nil {
		return nil, err
	}
	var out []stored
	for {
		batch, err := cons.Fetch(100, jetstream.FetchMaxWait(idle))
		if err != nil {
			return nil, err
		}
		got := 0
		for m := range batch.Messages() {
			got++
			s := stored{Subject: m.Subject(), Headers: map[string]string{}, Bytes: len(m.Data())}
			for k := range m.Headers() {
				s.Headers[k] = m.Headers().Get(k)
			}
			if json.Valid(m.Data()) {
				s.Data = append(json.RawMessage(nil), m.Data()...)
			}
			md, err := m.Metadata()
			if err == nil {
				s.Sequence, s.StreamTime = md.Sequence.Stream, md.Timestamp
			}
			out = append(out, s)
			if err == nil && md.NumPending == 0 {
				return out, nil
			}
		}
		if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) {
			return nil, err
		}
		if got == 0 {
			return out, nil
		}
	}
}
