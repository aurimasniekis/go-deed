package config

import (
	"strings"
	"testing"
)

func TestValidatePulseIngestSettings(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.Pulse.Enabled, c.Pulse.URL, c.Pulse.CredsFile = true, "tls://events.example.com:4222", "/etc/deed/pulse.creds"
		return c
	}
	cases := map[string]struct {
		mut  func(*Config)
		want string
	}{
		"defaults":             {func(*Config) {}, ""},
		"wildcard star filter": {func(c *Config) { c.Pulse.IngestFilter = "evt.deed.ingest.*.items" }, ""},
		"literal filter":       {func(c *Config) { c.Pulse.IngestFilter = "evt.deed.ingest.billing.items" }, "pulse.ingest_filter"},
		"filter outside evt":   {func(c *Config) { c.Pulse.IngestFilter = "deed.ingest.>" }, "pulse.ingest_filter"},
		"durable 48 chars":     {func(c *Config) { c.Pulse.IngestDurable = strings.Repeat("a", 48) }, ""},
		"durable too long":     {func(c *Config) { c.Pulse.IngestDurable = strings.Repeat("a", 49) }, "pulse.ingest_durable"},
		"durable leading _":    {func(c *Config) { c.Pulse.IngestDurable = "_deed" }, "pulse.ingest_durable"},
	}
	for name, tc := range cases {
		c := base()
		tc.mut(c)
		v := &validator{}
		c.validatePulse(v, true)
		err := v.err()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want an error about %s, got %v", name, tc.want, err)
		}
	}
}
