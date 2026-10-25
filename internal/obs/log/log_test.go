package log

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/obs/redact"
)

func TestLevels(t *testing.T) {
	cases := []struct {
		level   string
		enabled []slog.Level
		off     []slog.Level
	}{
		{"debug", []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelError}, nil},
		{"info", []slog.Level{slog.LevelInfo, slog.LevelWarn}, []slog.Level{slog.LevelDebug}},
		{"warn", []slog.Level{slog.LevelWarn, slog.LevelError}, []slog.Level{slog.LevelDebug, slog.LevelInfo}},
		{"error", []slog.Level{slog.LevelError}, []slog.Level{slog.LevelInfo, slog.LevelWarn}},
		// Unknown or empty levels fall back to info.
		{"", []slog.Level{slog.LevelInfo}, []slog.Level{slog.LevelDebug}},
		{"trace", []slog.Level{slog.LevelInfo}, []slog.Level{slog.LevelDebug}},
		{"DEBUG", []slog.Level{slog.LevelInfo}, []slog.Level{slog.LevelDebug}},
	}
	for _, c := range cases {
		t.Run(c.level, func(t *testing.T) {
			l := New(&bytes.Buffer{}, c.level, "json")
			for _, lv := range c.enabled {
				if !l.Enabled(context.Background(), lv) {
					t.Errorf("%s should be enabled", lv)
				}
			}
			for _, lv := range c.off {
				if l.Enabled(context.Background(), lv) {
					t.Errorf("%s should be disabled", lv)
				}
			}
		})
	}
}

func TestFormats(t *testing.T) {
	cases := []struct {
		format string
		json   bool
	}{
		{"json", true},
		{"", true},
		{"logfmt", true},
		{"text", false},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			var buf bytes.Buffer
			New(&buf, "info", c.format).Info("hello", "k", "v")
			line := strings.TrimSpace(buf.String())
			var m map[string]any
			isJSON := json.Unmarshal([]byte(line), &m) == nil
			if isJSON != c.json {
				t.Fatalf("format %q produced %q", c.format, line)
			}
			if c.json {
				if m["msg"] != "hello" || m["level"] != "INFO" || m["k"] != "v" || m["time"] == nil {
					t.Fatalf("record = %v", m)
				}
			} else if !strings.Contains(line, "msg=hello") || !strings.Contains(line, "k=v") {
				t.Fatalf("text record = %q", line)
			}
		})
	}
}

func TestRedaction(t *testing.T) {
	secrets := []string{"hunter2", "deed_at_abcdef", "deed_st_123456", "Bearer deed_at_zzz", "sid=cookievalue", "wrapped-value"}
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, "debug", format).With("api_token", "deed_at_abcdef")
			l = l.WithGroup("req")
			l.Debug("login",
				"password", "hunter2",
				"source_token", "deed_st_123456",
				"Authorization", "Bearer deed_at_zzz",
				"Cookie", "sid=cookievalue",
				"secret_ref", redact.NewSecret("wrapped-value"),
				"user", "alice",
				"exit_code", 3,
			)
			out := buf.String()
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Fatalf("leaked %q: %s", s, out)
				}
			}
			for _, keep := range []string{"alice", redact.Redacted, "login"} {
				if !strings.Contains(out, keep) {
					t.Fatalf("missing %q: %s", keep, out)
				}
			}
			if format == "json" {
				var rec struct {
					APIToken string         `json:"api_token"`
					Req      map[string]any `json:"req"`
				}
				if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
					t.Fatal(err)
				}
				if rec.APIToken != redact.Redacted || rec.Req["password"] != redact.Redacted ||
					rec.Req["user"] != "alice" || rec.Req["exit_code"] != float64(3) {
					t.Fatalf("record = %+v", rec)
				}
			}
		})
	}
}
