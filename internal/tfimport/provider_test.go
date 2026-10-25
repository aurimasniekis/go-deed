package tfimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

type recorder struct {
	entities  []catalog.EntityItem
	relations []catalog.RelationItem
	diags     []sources.Diagnostic
}

func (r *recorder) Entity(it catalog.EntityItem) error {
	r.entities = append(r.entities, it)
	return nil
}
func (r *recorder) Relation(it catalog.RelationItem) error {
	r.relations = append(r.relations, it)
	return nil
}
func (r *recorder) Diag(d sources.Diagnostic) { r.diags = append(r.diags, d) }
func (r *recorder) Partial(string)            {}

func newProvider(t *testing.T) *Provider {
	t.Helper()
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return &Provider{Catalog: c}
}

func env(t *testing.T, cfg map[string]any, secrets map[string]string) sources.Env {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	guard, err := safehttp.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return sources.Env{Config: raw, Secrets: secrets, HTTP: guard.Client}
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/state-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHTTPBackend(t *testing.T) {
	state := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tf-read-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/state/core" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(state)
	}))
	defer srv.Close()
	p := newProvider(t)
	ctx := context.Background()
	cfg := func(path string) map[string]any {
		return map[string]any{"backend": "http", "http": map[string]any{"url": srv.URL + path}, "insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}}
	}
	token := map[string]string{"http_token": "tf-read-token"}

	var rec recorder
	if err := p.Sync(ctx, env(t, cfg("/state/core"), token), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.entities) == 0 || len(rec.diags) == 0 || rec.diags[0].Code != "state" {
		t.Fatalf("sync: %d entities, diags %+v", len(rec.entities), rec.diags)
	}
	if d := p.Test(ctx, env(t, cfg("/state/core"), token)); len(d) != 1 || d[0].Code != "ok" || !strings.Contains(d[0].Message, "mapped") {
		t.Fatalf("test: %+v", d)
	}

	// Errors name the status, never the body.
	if d := p.Test(ctx, env(t, cfg("/state/core"), nil)); d[0].Level != "error" || !strings.Contains(d[0].Message, "401") {
		t.Fatalf("unauthorized: %+v", d)
	}
	if err := p.Sync(ctx, env(t, cfg("/state/missing"), token), &recorder{}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing: %v", err)
	}
	// Without an allowed CIDR the loopback backend is refused at dial time.
	c := cfg("/state/core")
	delete(c, "allow_cidrs")
	if err := p.Sync(ctx, env(t, c, token), &recorder{}); err == nil {
		t.Fatal("reached a loopback backend without allow_cidrs")
	}
	// A state larger than max_bytes is refused.
	c = cfg("/state/core")
	c["max_bytes"] = 1024
	if err := p.Sync(ctx, env(t, c, token), &recorder{}); err == nil {
		t.Fatal("state over max_bytes accepted")
	}
}

func TestValidateConfig(t *testing.T) {
	p := newProvider(t)
	for name, cfg := range map[string]string{
		"s3 without block":   `{"backend":"s3"}`,
		"s3 endpoint url":    `{"backend":"s3","s3":{"endpoint":"https://s3.example.com","bucket":"b","key":"k"}}`,
		"http without url":   `{"backend":"http"}`,
		"unknown field":      `{"backend":"push","bakend":"s3"}`,
		"bad custom mapping": `{"mappings":[{"type":"x_thing"}]}`,
	} {
		if err := p.Validate(json.RawMessage(cfg), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := p.Validate(json.RawMessage(`{"backend":"s3","s3":{"endpoint":"s3.example.com","bucket":"b","key":"env:/prod/core.tfstate"}}`), nil); err != nil {
		t.Fatal(err)
	}
	if d := p.Test(context.Background(), env(t, map[string]any{"backend": "push"}, nil)); d[0].Code != "push" {
		t.Fatalf("push test: %+v", d)
	}
}

// TestS3Backend reads a state object from MinIO: DEED_TEST_S3=http://user:password@host:port (CI), or a `minio`
// binary on PATH.
func TestS3Backend(t *testing.T) {
	endpoint, user, pass := minioEndpoint(t)
	ctx := context.Background()
	mc, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(user, pass, ""), Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	bucket := fmt.Sprintf("deed-tfstate-%d", time.Now().UnixNano())
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err = mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	state := fixture(t)
	if _, err := mc.PutObject(ctx, bucket, "env:/prod/core.tfstate", bytes.NewReader(state), int64(len(state)), minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}

	p := newProvider(t)
	cfg := func(key string) map[string]any {
		return map[string]any{"backend": "s3", "s3": map[string]any{"endpoint": endpoint, "bucket": bucket, "key": key, "use_ssl": false, "lookup": "path"},
			"insecure_http": true, "allow_cidrs": []string{"127.0.0.1/32"}}
	}
	creds := map[string]string{"s3_access_key_id": user, "s3_secret_access_key": pass}
	var rec recorder
	if err := p.Sync(ctx, env(t, cfg("env:/prod/core.tfstate"), creds), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.entities) == 0 {
		t.Fatal("no entities from the S3 state")
	}
	if d := p.Test(ctx, env(t, cfg("env:/prod/core.tfstate"), creds)); d[0].Code != "ok" {
		t.Fatalf("test: %+v", d)
	}
	if d := p.Test(ctx, env(t, cfg("missing.tfstate"), creds)); d[0].Level != "error" || !strings.Contains(d[0].Message, "not found") {
		t.Fatalf("missing object: %+v", d)
	}
	wrong := map[string]string{"s3_access_key_id": user, "s3_secret_access_key": "not-the-secret"}
	if d := p.Test(ctx, env(t, cfg("env:/prod/core.tfstate"), wrong)); d[0].Level != "error" || !strings.Contains(d[0].Message, "denied") {
		t.Fatalf("wrong credentials: %+v", d)
	}
}

func minioEndpoint(t *testing.T) (endpoint, user, pass string) {
	t.Helper()
	if v := os.Getenv("DEED_TEST_S3"); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.User == nil {
			t.Fatalf("DEED_TEST_S3 must be http://user:password@host:port")
		}
		pass, _ = u.User.Password()
		return u.Host, u.User.Username(), pass
	}
	bin, err := exec.LookPath("minio")
	if err != nil {
		t.Skip("set DEED_TEST_S3 or install minio")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint = l.Addr().String()
	_ = l.Close()
	user, pass = "deedtest", "deedtest-secret"
	cmd := exec.Command(bin, "server", t.TempDir(), "--address", endpoint, "--console-address", "127.0.0.1:0", "--quiet")
	cmd.Env = append(os.Environ(), "MINIO_ROOT_USER="+user, "MINIO_ROOT_PASSWORD="+pass)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return endpoint, user, pass
}
