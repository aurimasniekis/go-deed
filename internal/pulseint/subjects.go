// Package pulseint integrates Deed with Pulse, the NATS JetStream event bus (ADR-006 C):
//
//   - pulse_admin: a pull provider reading the Pulse admin API (clients with grants, streams, durable consumers and
//     subject activity) into services, streams, consumers and subjects with relationships whose evidence is the
//     grant or the consumer configuration;
//   - pulse_tail: a read-only subscription to evt.> that aggregates per-minute counts by (ce-source, subject
//     template, ce-type) into observed publish relationships and rollups; raw messages are never persisted;
//   - an ingest consumer: a durable subscription accepting source-protocol envelopes (deed.ingest.*.v1).
package pulseint

import (
	"regexp"
	"strings"
)

var (
	ulidRE   = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}$`)
	uuidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)
	numRE    = regexp.MustCompile(`^[0-9]+$`)
	hexRE    = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
	prefixRE = regexp.MustCompile(`^[a-z]{1,8}_[0-9a-zA-Z]{12,}$`)
	longRE   = regexp.MustCompile(`^[0-9a-zA-Z_-]{24,}$`)
)

// IsIDToken reports whether a subject token looks like an identifier (replaced by * in templates).
func IsIDToken(t string) bool {
	return ulidRE.MatchString(t) || uuidRE.MatchString(t) || numRE.MatchString(t) || (hexRE.MatchString(t) && strings.ContainsAny(t, "0123456789")) ||
		prefixRE.MatchString(t) || (longRE.MatchString(t) && strings.ContainsAny(t, "0123456789"))
}

// Template normalizes a concrete subject into a template: identifier-like tokens (ULIDs, UUIDs, numbers, long
// hex-ish tokens) after the first one become "*"; subjects longer than 16 tokens are cut and end in ">".
func Template(subject string) string {
	toks := strings.Split(subject, ".")
	if len(toks) > 16 {
		toks = append(toks[:16], ">")
	}
	for i, t := range toks {
		if i == 0 || t == "*" || t == ">" {
			continue
		}
		if IsIDToken(t) {
			toks[i] = "*"
		}
	}
	return strings.Join(toks, ".")
}

// IsPattern reports whether a subject contains wildcards.
func IsPattern(s string) bool {
	for _, t := range strings.Split(s, ".") {
		if t == "*" || t == ">" {
			return true
		}
	}
	return false
}

// Matches reports whether subject matches a NATS filter with * and > wildcards.
func Matches(filter, subject string) bool {
	f := strings.Split(filter, ".")
	s := strings.Split(subject, ".")
	for i, t := range f {
		if t == ">" {
			return len(s) > i
		}
		if i >= len(s) {
			return false
		}
		if t != "*" && t != s[i] {
			return false
		}
	}
	return len(f) == len(s)
}

// EntityName renders a subject or pattern as an entity name (dots kept; * and > spelled out).
func EntityName(s string) string {
	s = strings.ReplaceAll(s, "*", "star")
	s = strings.ReplaceAll(s, ">", "all")
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if len(out) > 240 {
		out = out[:240]
	}
	return out
}
