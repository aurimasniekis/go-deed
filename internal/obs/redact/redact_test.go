package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// Exact keys.
		{"reason", true},
		{"request_reason", true},
		{"comment", true},
		{"command", true},
		{"exec_command", true},
		{"terminal_data", true},
		{"input", true},
		{"output", true},
		{"stdin", true},
		{"stdout", true},
		{"stderr", true},
		{"cookie", true},
		{"otp", true},
		{"answer", true},
		{"answers", true},
		{"recovery", true},
		{"code_verifier", true},
		{"id_token", true},

		// HTTP header names, any case, with dashes.
		{"authorization", true},
		{"Authorization", true},
		{"AUTHORIZATION", true},
		{"Proxy-Authorization", true},
		{"Cookie", true},
		{"set-cookie", true},
		{"Set-Cookie", true},
		{"set_cookie", true},
		{"X-Api-Key", true},
		{"X-Auth-Token", true},

		// Suffixes.
		{"password", true},
		{"new_password", true},
		{"passwd", true},
		{"secret", true},
		{"client_secret", true},
		{"Client-Secret", true},
		{"token", true},
		{"access_token", true},
		{"refresh_token", true},
		{"bot_token", true},
		{"setup_token", true},
		{"private_key", true},
		{"ssh_private_key", true},
		{"privatekey", true},
		{"passphrase", true},
		{"kek", true},
		{"dek", true},
		{"wrapped_dek", true},
		{"api_key", true},
		{"apikey", true},
		{"totp", true},
		{"recovery_code", true},
		{"recovery_codes", true},
		{"credential", true},
		{"credentials", true},
		{"aws_credentials", true},
		{"signature", true},
		{"webhook_signature", true},

		// Dotted keys use the last segment.
		{"deed.reason", true},
		{"http.request.header.authorization", true},
		{"oidc.client_secret", true},
		{"reason.length", false},
		{"token.id", false},

		// Not sensitive.
		{"", false},
		{"code", false},
		{"exit_code", false},
		{"error_code", false},
		{"user", false},
		{"username", false},
		{"email", false},
		{"token_id", false},
		{"token_prefix", false},
		{"tokens_issued", false},
		{"kek_file", false},
		{"mytoken", false},
		{"secretary", false},
		{"path", false},
		{"status", false},
		{"source", false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := IsSensitiveKey(c.key); got != c.want {
				t.Fatalf("IsSensitiveKey(%q) = %v, want %v", c.key, got, c.want)
			}
		})
	}
}

func TestReplaceAttr(t *testing.T) {
	cases := []struct {
		name string
		in   slog.Attr
		want slog.Value
	}{
		{"sensitive string", slog.String("password", "hunter2"), slog.StringValue(Redacted)},
		{"sensitive int", slog.Int("otp", 123456), slog.StringValue(Redacted)},
		{"sensitive any", slog.Any("authorization", []string{"Bearer deed_at_x"}), slog.StringValue(Redacted)},
		{"plain", slog.String("user", "alice"), slog.StringValue("alice")},
		{"code kept", slog.Int("code", 2), slog.IntValue(2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReplaceAttr(nil, c.in)
			if got.Key != c.in.Key || !got.Value.Equal(c.want) {
				t.Fatalf("ReplaceAttr(%v) = %v, want %s=%v", c.in, got, c.in.Key, c.want)
			}
		})
	}
	// A group attribute is left for its members to be checked one by one.
	g := slog.Group("credentials", slog.String("user", "alice"))
	if got := ReplaceAttr(nil, g); got.Value.Kind() != slog.KindGroup {
		t.Fatalf("group replaced: %v", got)
	}
}

func TestReplaceAttrInHandler(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: ReplaceAttr}))
	l.Info("msg",
		"user", "alice",
		"client_secret", "s3cr3t-value",
		slog.Group("oidc", slog.String("client_secret", "nested-secret"), slog.String("issuer", "https://example.com")),
		slog.Group("token", slog.String("prefix", "deed_at_")),
		"wrapped", NewSecret("wrapped-secret"),
	)
	out := buf.String()
	for _, leak := range []string{"s3cr3t-value", "nested-secret", "wrapped-secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q: %s", leak, out)
		}
	}
	var rec struct {
		User         string `json:"user"`
		ClientSecret string `json:"client_secret"`
		OIDC         struct {
			ClientSecret string `json:"client_secret"`
			Issuer       string `json:"issuer"`
		} `json:"oidc"`
		Token struct {
			Prefix string `json:"prefix"`
		} `json:"token"`
		Wrapped string `json:"wrapped"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.User != "alice" || rec.ClientSecret != Redacted || rec.OIDC.ClientSecret != Redacted ||
		rec.OIDC.Issuer != "https://example.com" || rec.Token.Prefix != "deed_at_" || rec.Wrapped != Redacted {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestSecret(t *testing.T) {
	const raw = "deed_at_0123456789abcdef"
	s := NewSecret(raw)
	if s.Reveal() != raw {
		t.Fatal("Reveal lost the value")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10s", "%-10v"} {
		t.Run(verb, func(t *testing.T) {
			out := fmt.Sprintf(verb, s)
			if strings.Contains(out, raw) || strings.Contains(out, "6465") /* hex of "de" */ {
				t.Fatalf("Sprintf(%s) leaked: %s", verb, out)
			}
			if !strings.Contains(out, Redacted) {
				t.Fatalf("Sprintf(%s) = %q, want %s", verb, out, Redacted)
			}
		})
	}
	if s.String() != Redacted || s.GoString() != Redacted {
		t.Fatal("String/GoString")
	}
	if fmt.Sprint(s) != Redacted || fmt.Sprintf("%v", &s) != Redacted || fmt.Sprintf("%v", []any{s}) != "["+Redacted+"]" {
		t.Fatal("Sprint")
	}
	if err := fmt.Errorf("login: %v", s); strings.Contains(err.Error(), raw) {
		t.Fatal("error wrapping leaked")
	}

	// JSON, nested in a struct and in a map.
	b, err := json.Marshal(struct {
		Token Secret[string]            `json:"token"`
		Map   map[string]Secret[[]byte] `json:"map"`
	}{s, map[string]Secret[[]byte]{"k": NewSecret([]byte(raw))}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(raw)) || string(b) != `{"token":"[REDACTED]","map":{"k":"[REDACTED]"}}` {
		t.Fatalf("json = %s", b)
	}
	// JSON map keys use MarshalText.
	b, err = json.Marshal(map[Secret[string]]int{s: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"[REDACTED]":1}` {
		t.Fatalf("json key = %s", b)
	}
	if txt, _ := s.MarshalText(); string(txt) != Redacted {
		t.Fatalf("MarshalText = %s", txt)
	}
	if v := s.LogValue(); v.String() != Redacted {
		t.Fatalf("LogValue = %v", v)
	}

	// Logged under a non-sensitive key, by both handlers.
	for _, h := range []func(*bytes.Buffer) slog.Handler{
		func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(h(&buf)).Info("msg", "value", s, slog.Any("nested", map[string]any{"v": s}))
		if strings.Contains(buf.String(), raw) {
			t.Fatalf("handler leaked: %s", buf.String())
		}
	}
}
