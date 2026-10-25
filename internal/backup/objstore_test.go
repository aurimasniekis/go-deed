package backup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// quirkyS3 imitates behaviours of real S3 implementations seen in scripts/validate/s3.sh: Garage answers a wrong
// signature region with 400 (no body on HEAD), SeaweedFS without an SSE-S3 key creates the multipart upload and then
// fails the request, other servers reject SSE-S3 outright.
type quirkyS3 struct {
	mu      sync.Mutex
	uploads map[string]string // upload ID → key
	aborted []string
	next    int
}

func s3Err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
}

func (f *quirkyS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	key := strings.TrimPrefix(r.URL.Path, "/bkt/")
	sse := r.Header.Get("X-Amz-Server-Side-Encryption") != ""
	switch {
	case r.Method == http.MethodHead && r.URL.Path == "/wrong-region/":
		w.WriteHeader(http.StatusBadRequest) // HEAD: no error document
	case r.Method == http.MethodGet && r.URL.Path == "/wrong-region/":
		s3Err(w, http.StatusBadRequest, "AuthorizationHeaderMalformed", "Authorization header malformed, unexpected scope: '20261025/us-east-1/s3/aws4_request', expected: '20261025/garage/s3/aws4_request'")
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.next++
		id := fmt.Sprintf("upload-%d", f.next)
		f.uploads[id] = key
		if sse {
			// Created, then failed: the client never learns the upload ID.
			s3Err(w, http.StatusNotImplemented, "NotImplemented", "SSE-S3 is not configured")
			return
		}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>bkt</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, key, id)
	case r.Method == http.MethodGet && q.Has("uploads"):
		var b strings.Builder
		b.WriteString(`<ListMultipartUploadsResult><Bucket>bkt</Bucket><IsTruncated>false</IsTruncated>`)
		for id, k := range f.uploads {
			if strings.HasPrefix(k, q.Get("prefix")) {
				fmt.Fprintf(&b, `<Upload><Key>%s</Key><UploadId>%s</UploadId><Initiated>2026-10-25T00:00:00.000Z</Initiated></Upload>`, k, id)
			}
		}
		b.WriteString(`</ListMultipartUploadsResult>`)
		_, _ = w.Write([]byte(b.String()))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		f.aborted = append(f.aborted, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && sse:
		s3Err(w, http.StatusNotImplemented, "NotImplemented", "SSE-S3 is not configured")
	case r.Method == http.MethodPut:
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
	default:
		s3Err(w, http.StatusNotImplemented, "NotImplemented", r.Method+" "+r.URL.String())
	}
}

func quirkyStore(t *testing.T, bucket string, sse bool) (*S3, *quirkyS3) {
	t.Helper()
	f := &quirkyS3{uploads: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	st, err := NewS3(S3Options{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Bucket: bucket, Prefix: "deed/", Region: "us-east-1",
		AccessKey: "k", SecretKey: "s", Insecure: true, Lookup: "path", SSE: sse})
	if err != nil {
		t.Fatal(err)
	}
	return st, f
}

func TestS3CheckExplainsRegionMismatch(t *testing.T) {
	st, _ := quirkyStore(t, "wrong-region", false)
	err := st.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expected: '20261025/garage") || !strings.Contains(err.Error(), "backup.s3.region") {
		t.Fatalf("region mismatch: %v", err)
	}
}

func TestS3PutSSERejected(t *testing.T) {
	ctx := context.Background()
	st, f := quirkyStore(t, "bkt", true)
	err := st.Put(ctx, "backups/x/manifest.json", strings.NewReader("{}"), 2)
	if err == nil || !strings.Contains(err.Error(), "SSE-S3 is not configured") || !strings.Contains(err.Error(), "backup.s3.sse: false") {
		t.Fatalf("single put: %v", err)
	}
	// The streamed upload was created before the server failed it: Put aborts it rather than leaving it behind.
	err = st.Put(ctx, "backups/x/deed.db.zst.age", strings.NewReader("ciphertext"), -1)
	if err == nil || !strings.Contains(err.Error(), "backup.s3.sse: false") {
		t.Fatalf("streamed put: %v", err)
	}
	if len(f.uploads) != 0 || len(f.aborted) != 1 {
		t.Fatalf("uploads left %v, aborted %v", f.uploads, f.aborted)
	}
	// Without SSE the same server works and errors carry no SSE hint.
	plain, _ := quirkyStore(t, "bkt", false)
	if err := plain.Put(ctx, "backups/x/manifest.json", strings.NewReader("{}"), 2); err != nil {
		t.Fatal(err)
	}
	if err := plain.Check(ctx); err == nil || strings.Contains(err.Error(), "sse") {
		t.Fatalf("unrelated error: %v", err)
	}
}
