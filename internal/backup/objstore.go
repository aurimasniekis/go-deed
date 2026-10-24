// Adapted from Porch internal/backup/objstore.go (Apache-2.0): streaming puts and gets, and incomplete multipart
// uploads are aborted.

package backup

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

// MaxObjectSize bounds every object read from storage.
const MaxObjectSize int64 = 16 << 30

// ErrNotFound means the object does not exist.
var ErrNotFound = errors.New("object not found")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ObjectStore is the subset of S3 backups use. Keys are relative to the store's prefix.
type ObjectStore interface {
	// Put streams r (size -1 when unknown: multipart upload).
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// S3Options configure the S3 store.
type S3Options struct {
	Endpoint  string // host[:port]
	Bucket    string
	Prefix    string
	Region    string
	AccessKey string
	SecretKey string
	Insecure  bool   // plain HTTP (development only)
	Lookup    string // auto | path | virtual
	CAPEM     []byte
	// SSE requests SSE-S3 server-side encryption (defense in depth on top of age).
	SSE bool
	// Transport is the base transport; nil means http.DefaultTransport.
	Transport *http.Transport
}

// S3 stores objects with minio-go.
type S3 struct {
	c      *minio.Client
	bucket string
	prefix string
	sse    bool
}

// NewS3 returns a client (no network access until used).
func NewS3(o S3Options) (*S3, error) {
	lookup := minio.BucketLookupAuto
	switch o.Lookup {
	case "path":
		lookup = minio.BucketLookupPath
	case "virtual", "dns":
		lookup = minio.BucketLookupDNS
	}
	base := o.Transport
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	tr := base.Clone()
	if len(o.CAPEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(o.CAPEM) {
			return nil, errors.New("backup: invalid S3 CA PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	c, err := minio.New(o.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""), Secure: !o.Insecure,
		Region: o.Region, BucketLookup: lookup, Transport: tr})
	if err != nil {
		return nil, err
	}
	return &S3{c: c, bucket: o.Bucket, prefix: o.Prefix, sse: o.SSE}, nil
}

// Check verifies the bucket exists. Backups call it first, so a misconfigured store fails the backup, not the start.
func (s *S3) Check(ctx context.Context) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("backup: bucket %s: %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("backup: bucket %s does not exist", s.bucket)
	}
	return nil
}

func (s *S3) key(k string) string { return s.prefix + k }

// Put implements ObjectStore. Unknown sizes upload in 16 MiB parts; minio-go aborts the multipart upload on error.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	o := minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: 16 << 20}
	if s.sse {
		o.ServerSideEncryption = encrypt.NewSSE()
	}
	_, err := s.c.PutObject(ctx, s.bucket, s.key(key), r, size, o)
	return err
}

func notFound(err error) bool {
	r := minio.ToErrorResponse(err)
	return r.Code == "NoSuchKey" || r.StatusCode == http.StatusNotFound
}

// Get implements ObjectStore.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := o.Stat(); err != nil {
		_ = o.Close()
		if notFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(o, MaxObjectSize), o}, nil
}

// List implements ObjectStore (keys are returned without the store prefix).
func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key(prefix), Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, ObjectInfo{Key: strings.TrimPrefix(o.Key, s.prefix), Size: o.Size, ModTime: o.LastModified})
	}
	return out, nil
}

// Delete implements ObjectStore.
func (s *S3) Delete(ctx context.Context, key string) error {
	return s.c.RemoveObject(ctx, s.bucket, s.key(key), minio.RemoveObjectOptions{})
}

// AbortIncomplete removes incomplete multipart uploads under prefix (left by a crash mid-upload).
func (s *S3) AbortIncomplete(ctx context.Context, prefix string) (int, error) {
	n := 0
	for u := range s.c.ListIncompleteUploads(ctx, s.bucket, s.key(prefix), true) {
		if u.Err != nil {
			return n, u.Err
		}
		if err := s.c.RemoveIncompleteUpload(ctx, s.bucket, u.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Mem is an in-memory ObjectStore for tests; Fail injects errors.
type Mem struct {
	mu   sync.Mutex
	objs map[string][]byte
	mod  map[string]time.Time
	// Fail, when set, is consulted before every operation ("put", "get", "list", "delete").
	Fail func(op, key string) error
	// FailAfter makes a put fail after this many bytes (an interrupted upload), when > 0.
	FailAfter int64
	Now       func() time.Time
}

// NewMem returns an empty store.
func NewMem() *Mem {
	return &Mem{objs: map[string][]byte{}, mod: map[string]time.Time{}, Now: time.Now}
}

func (m *Mem) fail(op, key string) error {
	if m.Fail != nil {
		return m.Fail(op, key)
	}
	return nil
}

// Put implements ObjectStore.
func (m *Mem) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	if err := m.fail("put", key); err != nil {
		return err
	}
	var buf bytes.Buffer
	if m.FailAfter > 0 {
		if _, err := io.CopyN(&buf, r, m.FailAfter); err == nil {
			_, _ = io.Copy(io.Discard, r)
			return errors.New("connection reset by peer (injected)")
		}
	} else if _, err := io.Copy(&buf, r); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = buf.Bytes()
	m.mod[key] = m.Now()
	return nil
}

// Get implements ObjectStore.
func (m *Mem) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := m.fail("get", key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), nil
}

// List implements ObjectStore.
func (m *Mem) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	if err := m.fail("list", prefix); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ObjectInfo
	for k, b := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, Size: int64(len(b)), ModTime: m.mod[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete implements ObjectStore.
func (m *Mem) Delete(_ context.Context, key string) error {
	if err := m.fail("delete", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	delete(m.mod, key)
	return nil
}

// Corrupt flips a byte in the middle of an object.
func (m *Mem) Corrupt(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.objs[key]; len(b) > 0 {
		b[len(b)/2] ^= 0xFF
	}
}

// Keys lists every stored key.
func (m *Mem) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
