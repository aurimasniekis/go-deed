// Adapted from Porch internal/backup/crypto.go (Apache-2.0), which adapted Pulse: Deed streams objects (zstd, then
// age) instead of buffering them, and hashes plaintext and ciphertext on the way.

package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

// ParseRecipients parses age X25519 recipients (age1…); entries may hold several, separated by newlines or commas, and
// # comments are ignored.
func ParseRecipients(entries []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, e := range entries {
		for _, line := range strings.FieldsFunc(e, func(r rune) bool { return r == '\n' || r == ',' }) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			r, err := age.ParseX25519Recipient(line)
			if err != nil {
				return nil, fmt.Errorf("backup: age recipient %q: %w", line, err)
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("backup: no age recipients configured (backup.recipients)")
	}
	return out, nil
}

// RecipientFingerprints identifies recipients in manifests without revealing anything beyond the public keys.
func RecipientFingerprints(rs []age.Recipient) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if x, ok := r.(*age.X25519Recipient); ok {
			sum := sha256.Sum256([]byte(x.String()))
			out = append(out, hex.EncodeToString(sum[:8]))
		}
	}
	return out
}

// ParseIdentities parses an age identity file (AGE-SECRET-KEY-1…).
func ParseIdentities(s string) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(s))
	if err != nil {
		return nil, errors.New("backup: invalid age identity file")
	}
	return ids, nil
}

// GenerateIdentity returns a new identity file (age-keygen format) and its recipient.
func GenerateIdentity() (identityFile, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	r := id.Recipient().String()
	return "# public key: " + r + "\n" + id.String() + "\n", r, nil
}

// ErrDecrypt means the identity does not match or the object was modified.
var ErrDecrypt = errors.New("backup: cannot decrypt (wrong identity or damaged object)")

// counter hashes and counts bytes written through it.
type counter struct {
	h hash.Hash
	n int64
}

func newCounter() *counter { return &counter{h: sha256.New()} }

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

func (c *counter) sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

// sealStream returns a reader of src compressed (zstd) and encrypted (age) to rs. plain and cipher receive hashes and
// sizes once the reader reaches EOF.
func sealStream(rs []age.Recipient, src io.Reader, plain, cipher *counter) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		w, err := age.Encrypt(io.MultiWriter(pw, cipher), rs...)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(2))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(zw, io.TeeReader(src, plain)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := zw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := w.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr
}

// openStream decrypts and decompresses src into dst, hashing the ciphertext and plaintext. maxPlain bounds the output.
// The whole ciphertext is always read, also on failure, so the caller can tell a wrong identity (ciphertext intact)
// from a damaged object (ciphertext hash differs).
func openStream(ids []age.Identity, src io.Reader, dst io.Writer, cipher, plain *counter, maxPlain int64) (err error) {
	tr := io.TeeReader(src, cipher)
	defer func() { _, _ = io.Copy(io.Discard, tr) }()
	r, err := age.Decrypt(tr, ids...)
	if err != nil {
		return ErrDecrypt
	}
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<30))
	if err != nil {
		return ErrDecrypt
	}
	defer zr.Close()
	n, err := io.Copy(io.MultiWriter(dst, plain), io.LimitReader(zr, maxPlain+1))
	if err != nil {
		return ErrDecrypt
	}
	if n > maxPlain {
		return fmt.Errorf("backup: object expands beyond %d bytes", maxPlain)
	}
	return nil
}
