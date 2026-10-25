package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Target is an S3 server under test from DEED_TEST_S3: http://user:password@host:port[?region=…][&sse=on|off]
// [&vhost=domain]. region is the region to configure (empty: discovered); sse=on asserts SSE-S3 uploads succeed,
// sse=off asserts they are rejected with Deed's hint, and an unset sse only logs what the server does; vhost is the
// server's virtual-host base domain, which enables the lookup: virtual test.
type s3Target struct {
	endpoint, user, pass, region, sse, vhost string
}

func s3FromEnv(t *testing.T) (s3Target, bool) {
	t.Helper()
	v := os.Getenv("DEED_TEST_S3")
	if v == "" {
		return s3Target{}, false
	}
	u, err := url.Parse(v)
	if err != nil || u.User == nil || u.Host == "" {
		t.Fatalf("DEED_TEST_S3 must be http://user:password@host:port[?region=…&sse=on|off&vhost=…]")
	}
	pass, _ := u.User.Password()
	q := u.Query()
	return s3Target{endpoint: u.Host, user: u.User.Username(), pass: pass, region: q.Get("region"), sse: q.Get("sse"), vhost: q.Get("vhost")}, true
}

func (s s3Target) client(t *testing.T) *minio.Client {
	t.Helper()
	mc, err := minio.New(s.endpoint, &minio.Options{Creds: credentials.NewStaticV4(s.user, s.pass, ""), Region: s.region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		t.Fatal(err)
	}
	return mc
}

// makeBucket creates a fresh bucket, retrying while the server starts.
func (s s3Target) makeBucket(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	mc := s.client(t)
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := mc.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: s.region})
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("make bucket %s at %s: %v", name, s.endpoint, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s s3Target) store(t *testing.T, bucket, prefix string, sse bool) *S3 {
	t.Helper()
	st, err := NewS3(S3Options{Endpoint: s.endpoint, Bucket: bucket, Prefix: prefix, Region: s.region, AccessKey: s.user, SecretKey: s.pass,
		Insecure: true, Lookup: "path", SSE: sse})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func readAll(t *testing.T, st ObjectStore, key string) []byte {
	t.Helper()
	rc, err := st.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return b
}

// TestS3Compat exercises every object-store operation backups depend on against a real S3 implementation
// (DEED_TEST_S3): bucket check, single and multipart puts, SSE-S3, not-found mapping, ListObjectsV2 paging, deletes
// (including of missing keys) and aborting incomplete multipart uploads under the prefix only.
func TestS3Compat(t *testing.T) {
	tgt, ok := s3FromEnv(t)
	if !ok {
		t.Skip("set DEED_TEST_S3 to run against a real S3 implementation")
	}
	ctx := context.Background()
	bucket := fmt.Sprintf("deed-compat-%d", time.Now().UnixNano())
	tgt.makeBucket(t, ctx, bucket)
	st := tgt.store(t, bucket, "deed/", false)
	mc := tgt.client(t)

	t.Run("check", func(t *testing.T) {
		if err := st.Check(ctx); err != nil {
			t.Fatal(err)
		}
		missing := tgt.store(t, bucket+"-missing", "deed/", false)
		if err := missing.Check(ctx); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("missing bucket: %v", err)
		}
	})

	t.Run("put-get-small", func(t *testing.T) {
		body := []byte(`{"manifest":"small"}`)
		if err := st.Put(ctx, "small/known.json", bytes.NewReader(body), int64(len(body))); err != nil {
			t.Fatal(err)
		}
		if err := st.Put(ctx, "small/stream.bin", bytes.NewReader(body), -1); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"small/known.json", "small/stream.bin"} {
			if got := readAll(t, st, k); !bytes.Equal(got, body) {
				t.Fatalf("%s: got %q", k, got)
			}
		}
	})

	t.Run("multipart", func(t *testing.T) {
		// Three 16 MiB parts, the last one short: what a large database upload looks like.
		data := make([]byte, 40<<20)
		_, _ = rand.Read(data)
		want := sha256.Sum256(data)
		if err := st.Put(ctx, "big/db.bin", io.MultiReader(bytes.NewReader(data)), -1); err != nil {
			t.Fatal(err)
		}
		if got := sha256.Sum256(readAll(t, st, "big/db.bin")); got != want {
			t.Fatal("multipart object differs after the round trip")
		}
		infos, err := st.List(ctx, "big/")
		if err != nil || len(infos) != 1 || infos[0].Size != int64(len(data)) || infos[0].ModTime.IsZero() {
			t.Fatalf("list after multipart: %+v %v", infos, err)
		}
	})

	t.Run("sse-s3", func(t *testing.T) {
		sse := tgt.store(t, bucket, "deed/", true)
		small := bytes.Repeat([]byte("sse "), 1024)
		body := bytes.Repeat([]byte("sse "), 5<<20) // 20 MiB: a multipart upload, where SSE is requested on the initiate
		errKnown := sse.Put(ctx, "sse/known.bin", bytes.NewReader(small), int64(len(small)))
		errStream := sse.Put(ctx, "sse/stream.bin", bytes.NewReader(body), -1)
		switch {
		case errKnown == nil && errStream == nil:
			oi, err := mc.StatObject(ctx, bucket, "deed/sse/stream.bin", minio.StatObjectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			applied := oi.Metadata.Get("X-Amz-Server-Side-Encryption")
			t.Logf("SSE-S3 accepted; x-amz-server-side-encryption on HEAD: %q", applied)
			if got := readAll(t, sse, "sse/stream.bin"); !bytes.Equal(got, body) {
				t.Fatal("SSE object differs after the round trip")
			}
			if tgt.sse == "off" {
				t.Fatal("expected the server to reject SSE-S3 (sse=off)")
			}
		case errKnown != nil && errStream != nil:
			t.Logf("SSE-S3 rejected: %v", errStream)
			for _, err := range []error{errKnown, errStream} {
				if !strings.Contains(err.Error(), "backup.s3.sse: false") {
					t.Fatalf("SSE rejection without the configuration hint: %v", err)
				}
			}
			if tgt.sse == "on" {
				t.Fatal("expected the server to accept SSE-S3 (sse=on)")
			}
		default:
			t.Fatalf("SSE-S3 accepted for one upload and rejected for the other: known size %v, streamed %v", errKnown, errStream)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		if _, err := st.Get(ctx, "nope/manifest.json"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing key: %v", err)
		}
	})

	t.Run("list-paging", func(t *testing.T) {
		// More keys than one ListObjectsV2 page (1000), sorted before a key that must still be found.
		const n = 1100
		var wg sync.WaitGroup
		errs := make(chan error, n)
		sem := make(chan struct{}, 16)
		for i := 0; i < n; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				errs <- st.Put(ctx, fmt.Sprintf("page/a-%04d", i), strings.NewReader("x"), 1)
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Put(ctx, "page/z-last", strings.NewReader("z"), 1); err != nil {
			t.Fatal(err)
		}
		infos, err := st.List(ctx, "page/")
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != n+1 || infos[len(infos)-1].Key != "page/z-last" {
			t.Fatalf("listed %d keys (want %d), last %q", len(infos), n+1, infos[len(infos)-1].Key)
		}
		for _, o := range infos {
			if !strings.HasPrefix(o.Key, "page/") {
				t.Fatalf("key %q keeps the store prefix or escapes it", o.Key)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := st.Delete(ctx, "small/known.json"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Get(ctx, "small/known.json"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
		// Prune may race a concurrent delete: deleting a missing key must succeed as it does on AWS.
		if err := st.Delete(ctx, "small/never-existed.json"); err != nil {
			t.Fatalf("delete of a missing key: %v", err)
		}
	})

	t.Run("abort-incomplete", func(t *testing.T) {
		core := minio.Core{Client: mc}
		part := bytes.Repeat([]byte("p"), 5<<20)
		start := func(key string) string {
			id, err := core.NewMultipartUpload(ctx, bucket, key, minio.PutObjectOptions{})
			if err != nil {
				t.Fatalf("new multipart upload %s: %v", key, err)
			}
			if _, err := core.PutObjectPart(ctx, bucket, key, id, 1, bytes.NewReader(part), int64(len(part)), minio.PutObjectPartOptions{}); err != nil {
				t.Fatalf("upload part %s: %v", key, err)
			}
			return id
		}
		start("deed/backups/bkp_interrupted/deed.db.zst.age")
		start("deed/backups/bkp_interrupted/deed.db.zst.age") // a retried upload of the same key
		outside := start("other/backups/keep.bin")
		n, err := st.AbortIncomplete(ctx, "backups/")
		if err != nil {
			t.Fatal(err)
		}
		if n < 1 {
			t.Fatalf("aborted %d uploads", n)
		}
		var left []string
		for u := range mc.ListIncompleteUploads(ctx, bucket, "", true) {
			if u.Err != nil {
				t.Fatal(u.Err)
			}
			left = append(left, u.Key)
		}
		if len(left) != 1 || left[0] != "other/backups/keep.bin" {
			t.Fatalf("incomplete uploads left: %v", left)
		}
		_ = core.AbortMultipartUpload(ctx, bucket, "other/backups/keep.bin", outside)
	})

	t.Run("virtual-host", func(t *testing.T) {
		if tgt.vhost == "" {
			t.Skip("set vhost=<domain> in DEED_TEST_S3 (the server's virtual-host base domain) to test lookup: virtual")
		}
		// <bucket>.<domain> has no DNS entry: dial the server for every name, the Host header still carries it.
		_, port, _ := strings.Cut(tgt.endpoint, ":")
		var mu sync.Mutex
		hosts := map[string]bool{}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, tgt.endpoint)
		}
		tr.Proxy = func(r *http.Request) (*url.URL, error) { // records the hosts requested; no proxy
			mu.Lock()
			defer mu.Unlock()
			hosts[r.URL.Hostname()] = true
			return nil, nil
		}
		vh, err := NewS3(S3Options{Endpoint: tgt.vhost + ":" + port, Bucket: bucket, Prefix: "deed/", Region: tgt.region, AccessKey: tgt.user,
			SecretKey: tgt.pass, Insecure: true, Lookup: "virtual", Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		if err := vh.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if err := vh.Put(ctx, "vhost/manifest.json", strings.NewReader("{}"), -1); err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, st, "vhost/manifest.json"); string(got) != "{}" { // read back path-style
			t.Fatalf("virtual-host object: %q", got)
		}
		if infos, err := vh.List(ctx, "vhost/"); err != nil || len(infos) != 1 {
			t.Fatalf("virtual-host list: %+v %v", infos, err)
		}
		if err := vh.Delete(ctx, "vhost/manifest.json"); err != nil {
			t.Fatal(err)
		}
		if !hosts[bucket+"."+tgt.vhost] {
			t.Fatalf("requests did not use virtual-host addressing: %v", hosts)
		}
	})

	t.Run("wrong-region", func(t *testing.T) {
		// Servers that check the signature scope must fail with a hint at backup.s3.region; others ignore it.
		wrong, err := NewS3(S3Options{Endpoint: tgt.endpoint, Bucket: bucket, Prefix: "deed/", Region: "deed-wrong-region-1", AccessKey: tgt.user,
			SecretKey: tgt.pass, Insecure: true, Lookup: "path"})
		if err != nil {
			t.Fatal(err)
		}
		if err := wrong.Check(ctx); err != nil {
			t.Logf("wrong region rejected: %v", err)
			if !strings.Contains(err.Error(), "backup.s3.region") {
				t.Fatalf("region mismatch without the configuration hint: %v", err)
			}
		} else {
			t.Log("wrong region accepted (the server does not check the signature scope)")
		}
	})

	t.Run("wrong-credentials", func(t *testing.T) {
		bad, err := NewS3(S3Options{Endpoint: tgt.endpoint, Bucket: bucket, Region: tgt.region, AccessKey: tgt.user, SecretKey: tgt.pass + "x",
			Insecure: true, Lookup: "path"})
		if err != nil {
			t.Fatal(err)
		}
		err = bad.Check(ctx)
		if err == nil {
			t.Fatal("wrong secret accepted")
		}
		r := minio.ToErrorResponse(errors.Unwrap(err))
		t.Logf("wrong secret: HTTP %d code %q: %v", r.StatusCode, r.Code, err)
	})
}
