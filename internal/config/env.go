// Adapted from Porch (Apache-2.0), which adapted HallPass internal/config/env.go.

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix prefixes every environment override.
const EnvPrefix = "DEED_"

// Environment variables with the prefix that are not configuration overrides.
var nonConfigEnv = map[string]bool{
	"DEED_KEK":          true, // base64 KEK when kek.source = env
	"DEED_CONFIG":       true, // config file path
	"DEED_TEST_TX_MAX":  true, // test-only transaction duration guard
	"DEED_TEST_S3":      true, // test-only S3 endpoint for backup integration tests
	"DEED_TEST_NATS":    true, // test-only NATS URL for Pulse contract tests
	"DEED_ENDPOINT":     true, // CLI / provider client endpoint
	"DEED_TOKEN":        true, // CLI / provider client token
	"DEED_SOURCE_TOKEN": true, // deed-agent source token
	"DEED_SOURCE":       true, // Terraform provider declaring source
	"DEED_CA_FILE":      true, // CLI / provider CA bundle
	"DEED_ACC_KEEP":     true, // provider acceptance tests: keep the server
	"DEED_BINARY":       true, // provider acceptance tests: deed binary path
}

// EnvKeys lists every supported override variable, sorted. Used for documentation and tests.
func EnvKeys() []string {
	var keys []string
	walk(reflect.ValueOf(Default()).Elem(), nil, func(path []string, _ reflect.Value) {
		keys = append(keys, envName(path))
	})
	sort.Strings(keys)
	return keys
}

func envName(path []string) string {
	return EnvPrefix + strings.ToUpper(strings.Join(path, "_"))
}

// walk visits every leaf field (non-struct) with its yaml path.
func walk(v reflect.Value, path []string, fn func([]string, reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		p := append(append([]string(nil), path...), tag)
		fv := v.Field(i)
		if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Struct {
			continue // lists of objects (OIDC providers) are YAML-only
		}
		if fv.Kind() == reflect.Struct && fv.Type() != reflect.TypeFor[Duration]() {
			walk(fv, p, fn)
			continue
		}
		fn(p, fv)
	}
}

func applyEnv(cfg *Config, environ []string) error {
	env := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, EnvPrefix) {
			env[k] = v
		}
	}
	known := map[string]bool{}
	var firstErr error
	walk(reflect.ValueOf(cfg).Elem(), nil, func(path []string, fv reflect.Value) {
		name := envName(path)
		known[name] = true
		raw, ok := env[name]
		if !ok || firstErr != nil {
			return
		}
		if err := setFromString(fv, raw); err != nil {
			firstErr = fmt.Errorf("environment %s: %w", name, err)
		}
	})
	if firstErr != nil {
		return firstErr
	}
	var unknown []string
	for k := range env {
		if !known[k] && !nonConfigEnv[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown environment override(s): %s", strings.Join(unknown, ", "))
	}
	return nil
}

func setFromString(fv reflect.Value, raw string) error {
	switch {
	case fv.Type() == reflect.TypeFor[Duration]():
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	case fv.Kind() == reflect.String:
		fv.SetString(raw)
		return nil
	case fv.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
		return nil
	case fv.Kind() == reflect.Int || fv.Kind() == reflect.Int64 || fv.Kind() == reflect.Float64:
		if fv.Kind() == reflect.Float64 {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return err
			}
			fv.SetFloat(f)
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
		return nil
	case fv.Kind() == reflect.Slice:
		parts := splitList(raw)
		s := reflect.MakeSlice(fv.Type(), len(parts), len(parts))
		for i, p := range parts {
			if err := setFromString(s.Index(i), p); err != nil {
				return err
			}
		}
		fv.Set(s)
		return nil
	}
	return fmt.Errorf("unsupported field type %s", fv.Type())
}

func splitList(raw string) []string {
	var out []string
	for p := range strings.SplitSeq(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
