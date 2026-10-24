package tfimport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *State {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Parse(bytes.NewReader(b), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBuiltinCatalogIsValid(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup("cloudflare_record"); !ok {
		t.Fatal("cloudflare_record not mapped")
	}
	if len(c.KindTypes("dns_record")) < 4 {
		t.Fatalf("dns_record types %v", c.KindTypes("dns_record"))
	}
}

func TestExtractV4(t *testing.T) {
	st := load(t, "state-v4.json")
	if st.Format != "state-v4" || st.Serial != 42 {
		t.Fatalf("format %s serial %d", st.Format, st.Serial)
	}
	b, d := Extract(context.Background(), st, MustBuiltin(), Options{Workspace: "core"})
	out, _ := json.Marshal(b)
	diag, _ := json.Marshal(d)
	for _, blob := range [][]byte{out, diag} {
		if bytes.Contains(blob, []byte("PLANTED-SECRET")) || bytes.Contains(blob, []byte("PRIVATE KEY")) {
			t.Fatalf("secret material leaked: %s", blob)
		}
	}
	if d.Unsupported["tls_private_key"] != 1 || d.Unsupported["random_password"] != 1 {
		t.Fatalf("unsupported %v", d.Unsupported)
	}
	if d.Mapped["hcloud_server"] != 3 || d.Mapped["cloudflare_record"] != 2 {
		t.Fatalf("mapped %v", d.Mapped)
	}
	names := map[string]bool{}
	for _, e := range b.Entities {
		names[e.Kind+"/"+e.Name] = true
		if e.Kind == "grafana_dashboard" && e.Spec["title"] != "Infrastructure overview" {
			t.Fatalf("json: extraction failed: %v", e.Spec)
		}
	}
	for _, want := range []string{"dns_zone/example.com", "dns_record/www.example.com-a", "vm/web-1", "vm/worker-1", "vm/worker-2",
		"repository/go-deed", "certificate/www.example.com", "grafana_dashboard/overview"} {
		if !names[want] {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
	rels := map[string]bool{}
	for _, r := range b.Relations {
		rels[r.Type+" "+r.From.ExternalID+" "+r.To.ExternalID] = true
	}
	for _, want := range []string{
		"contains core:cloudflare_zone.example core:cloudflare_record.www",
		"resolves_to core:cloudflare_record.www core:hcloud_server.web",
	} {
		if !rels[want] {
			t.Fatalf("missing relation %q in %v", want, rels)
		}
	}
	// Even an allowlisted attribute is dropped when Terraform marks it sensitive.
	custom, err := MustBuiltin().With([]Mapping{{Resource: "hcloud_server", Kind: "vm", Fields: map[string]string{"/spec/hostname": "name", "/spec/os": "user_data"}}})
	if err != nil {
		t.Fatal(err)
	}
	b, d = Extract(context.Background(), st, custom, Options{})
	out, _ = json.Marshal(b)
	if d.DroppedSensitive == 0 || bytes.Contains(out, []byte("PLANTED-SECRET")) {
		t.Fatalf("sensitive attribute not dropped: %d %s", d.DroppedSensitive, out)
	}
}

func TestExtractShowJSON(t *testing.T) {
	st := load(t, "show.json")
	b, d := Extract(context.Background(), st, MustBuiltin(), Options{})
	out, _ := json.Marshal(b)
	if bytes.Contains(out, []byte("PLANTED-SECRET")) {
		t.Fatalf("leak: %s", out)
	}
	if d.Mapped["digitalocean_record"] != 1 || len(b.Entities) != 3 {
		t.Fatalf("diag %+v entities %d", d, len(b.Entities))
	}
	found := false
	for _, r := range b.Relations {
		if r.Type == "resolves_to" && strings.HasSuffix(r.To.ExternalID, "digitalocean_droplet.app") {
			found = true
		}
	}
	if !found {
		t.Fatalf("join across modules failed: %+v", b.Relations)
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"v3":      `{"version": 3, "modules": []}`,
		"garbage": `not json PLANTED-SECRET`,
		"other":   `{"hello": "world"}`,
	} {
		_, err := Parse(strings.NewReader(doc), 1<<20)
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		if strings.Contains(err.Error(), "PLANTED") {
			t.Fatalf("%s: error echoes input", name)
		}
	}
	big := `{"version":4,"resources":[` + strings.Repeat(`{"mode":"managed","type":"x","name":"y","instances":[]},`, 2000) + `{}]}`
	if _, err := Parse(strings.NewReader(big), 1024); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("size cap not enforced: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, n := range []string{"state-v4.json", "show.json"} {
		b, _ := os.ReadFile("testdata/" + n)
		f.Add(b)
	}
	cat := MustBuiltin()
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := Parse(bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		b, d := Extract(context.Background(), st, cat, Options{InferDependencies: true})
		_, _ = json.Marshal(b)
		_, _ = json.Marshal(d)
	})
}
