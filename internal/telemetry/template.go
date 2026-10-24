package telemetry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

var (
	paramNameRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	placeholderRE = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]{0,31})\s*\}\}`)
	// builtins are filled by the engine: the query window and the step of range queries (Prometheus durations).
	builtins = map[string]bool{"window": true, "step": true}
	// paramTypes constrain values before the adapter escapes them; label and subject values are inert in every
	// supported query language even before escaping.
	paramTypes = map[string]*regexp.Regexp{
		"label":    regexp.MustCompile(`^[A-Za-z0-9_.:@/+-]{1,253}$`),
		"string":   nil,
		"int":      regexp.MustCompile(`^-?[0-9]{1,18}$`),
		"duration": regexp.MustCompile(`^[0-9]{1,6}(ms|s|m|h|d)$`),
		"subject":  regexp.MustCompile(`^[A-Za-z0-9_$.*>-]{1,256}$`),
	}
)

// checkParam validates a parameter value against its type and optional pattern.
func checkParam(p BindingParam, v string) error {
	if len(v) > 256 {
		return fmt.Errorf("value too long")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("value contains control characters")
		}
	}
	if re := paramTypes[p.Type]; re != nil && !re.MatchString(v) {
		return fmt.Errorf("value is not a valid %s", p.Type)
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil || !re.MatchString(v) {
			return fmt.Errorf("value does not match the parameter pattern")
		}
	}
	return nil
}

func placeholders(q string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(q, -1) {
		out = append(out, m[1])
	}
	return out
}

// render substitutes placeholders with value(name), which returns the final (validated, escaped) text.
func render(q string, value func(name string) (string, bool)) (string, error) {
	var missing error
	out := placeholderRE.ReplaceAllStringFunc(q, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := value(name)
		if !ok {
			if missing == nil {
				missing = apperr.New(apperr.CodeInvalid, "missing parameter %s", name)
			}
			return ""
		}
		return v
	})
	return out, missing
}

func sampleValue(params []BindingParam, name string) string {
	switch name {
	case "window":
		return "5m"
	case "step":
		return "60s"
	}
	for _, p := range params {
		if p.Name == name {
			switch p.Type {
			case "int":
				return "1"
			case "duration":
				return "5m"
			case "subject":
				return "evt.sample"
			}
			return "sample"
		}
	}
	return "sample"
}

// promDuration renders d as a Prometheus duration (whole seconds or minutes).
func promDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(max(int64(d/time.Second), 1), 10) + "s"
}

// pointer resolves an RFC 6901 pointer in a decoded JSON document to a scalar string.
func pointer(doc any, ptr string) (string, bool) {
	if ptr == "" || ptr[0] != '/' {
		return "", false
	}
	cur := doc
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[tok]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(c) {
				return "", false
			}
			cur = c[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, v != ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}
