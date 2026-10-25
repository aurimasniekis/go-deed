// Adapted from Porch (Apache-2.0), which adapted HallPass internal/obs/redact/redact.go.

// Package redact keeps secrets and sensitive payloads out of logs and error output.
package redact

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Redacted is the placeholder written instead of a sensitive value.
const Redacted = "[REDACTED]"

// Secret wraps a sensitive value so that logging, formatting and JSON encoding never reveal it.
// Call Reveal only at the point of use.
type Secret[T any] struct{ v T }

// NewSecret wraps v.
func NewSecret[T any](v T) Secret[T] { return Secret[T]{v: v} }

// Reveal returns the wrapped value.
func (s Secret[T]) Reveal() T { return s.v }

// LogValue implements slog.LogValuer.
func (Secret[T]) LogValue() slog.Value { return slog.StringValue(Redacted) }

// String implements fmt.Stringer.
func (Secret[T]) String() string { return Redacted }

// GoString implements fmt.GoStringer.
func (Secret[T]) GoString() string { return Redacted }

// Format implements fmt.Formatter for every verb.
func (Secret[T]) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// MarshalJSON implements json.Marshaler.
func (Secret[T]) MarshalJSON() ([]byte, error) { return json.Marshal(Redacted) }

// MarshalText implements encoding.TextMarshaler.
func (Secret[T]) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// exactKeys are attribute keys whose values are always sensitive.
var exactKeys = map[string]bool{
	"reason":         true,
	"request_reason": true,
	"comment":        true,
	"command":        true,
	"exec_command":   true,
	"terminal_data":  true,
	"input":          true,
	"output":         true,
	"stdin":          true,
	"stdout":         true,
	"stderr":         true,
	"cookie":         true,
	"set_cookie":     true, // keys are normalized to underscores before lookup
	"authorization":  true,
	"otp":            true,
	"answer":         true,
	"answers":        true,
	"recovery":       true,
	"code_verifier":  true,
	"id_token":       true,
	"code":           false, // generic; not redacted (exit codes, error codes)
}

// sensitiveSuffixes match keys such as "password", "client_secret", "bot_token", "private_key",
// "proxy_authorization".
var sensitiveSuffixes = []string{
	"authorization", "password", "passwd", "secret", "token", "private_key", "privatekey", "passphrase", "kek", "dek",
	"api_key", "apikey", "totp", "recovery_code", "recovery_codes", "credential", "credentials", "signature",
}

// IsSensitiveKey reports whether an attribute key names a sensitive value. Matching is case-insensitive and
// considers the last dotted segment ("deed.reason" matches "reason").
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		k = k[i+1:]
	}
	k = strings.ReplaceAll(k, "-", "_")
	if v, ok := exactKeys[k]; ok {
		return v
	}
	for _, s := range sensitiveSuffixes {
		if k == s || strings.HasSuffix(k, "_"+s) {
			return true
		}
	}
	return false
}

// ReplaceAttr is an slog.HandlerOptions.ReplaceAttr function that redacts sensitive keys.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindGroup && IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		if s := a.Value.String(); tokenRE.MatchString(s) {
			return slog.String(a.Key, Tokens(s))
		}
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && tokenRE.MatchString(err.Error()) {
			return slog.String(a.Key, Tokens(err.Error()))
		}
	}
	return a
}

// tokenRE matches bearer tokens with a well-known prefix (Deed's own and those of the systems it integrates with).
var tokenRE = regexp.MustCompile(`\b(deed_at_|deed_st_|pulse_pat_|porch_at_|hallpass_at_|ghp_|github_pat_|glpat-)[A-Za-z0-9_-]{16,}`)

// Tokens replaces every well-known token in s, keeping its prefix so the kind of credential stays visible.
func Tokens(s string) string {
	return tokenRE.ReplaceAllStringFunc(s, func(m string) string {
		return tokenRE.FindStringSubmatch(m)[1] + Redacted
	})
}
