// Package storage abstracts where uploaded files live.
//
// One interface, two drivers. The local driver is the default because the
// volumes this system targets -- around a gigabyte a day -- do not justify
// object storage. The seam exists so that switching later is a configuration
// change rather than a rewrite; see docs/06-deep-dives.md for the thresholds
// that make the switch worth making.
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound means the object is not there.
var ErrNotFound = errors.New("object not found")

// ErrInvalidKey means a storage key was malformed or tried to escape the root.
var ErrInvalidKey = errors.New("invalid storage key")

// Storage reads and writes opaque objects.
type Storage interface {
	// Put writes r under key. It returns the number of bytes written.
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	// Get opens an object for reading.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes an object. Deleting something absent is not an error.
	Delete(ctx context.Context, key string) error
	// SignedURL returns a time-limited URL a browser can fetch directly.
	SignedURL(key string, ttl time.Duration) (string, error)
}

// Local stores objects on a mounted filesystem.
type Local struct {
	root    string
	baseURL string
	pepper  []byte
}

// NewLocal returns a Local rooted at dir.
func NewLocal(dir, baseURL string, pepper []byte) (*Local, error) {
	if dir == "" {
		return nil, errors.New("storage path is required")
	}
	if len(pepper) < 32 {
		return nil, fmt.Errorf("signing pepper must be at least 32 bytes, got %d", len(pepper))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving storage path: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("creating storage directory: %w", err)
	}
	return &Local{root: abs, baseURL: strings.TrimRight(baseURL, "/"), pepper: pepper}, nil
}

// Put writes an object atomically.
//
// The write lands in a temporary file and is renamed into place, so a crash
// halfway through leaves no partial object for a later reader to mistake for a
// complete one.
func (l *Local) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	path, err := l.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, fmt.Errorf("creating object directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return 0, fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Both are cleanup after a possible failure: the file is already written
		// or already renamed, and there is nobody left to tell.
		_ = tmp.Close()
		_ = os.Remove(tmpName) // no-op once the rename has succeeded
	}()

	n, err := io.Copy(tmp, r)
	if err != nil {
		return 0, fmt.Errorf("writing object: %w", err)
	}
	// Flushed before the rename: otherwise a power loss can leave a correctly
	// named file with no contents.
	if err := tmp.Sync(); err != nil {
		return 0, fmt.Errorf("flushing object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("closing object: %w", err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return 0, fmt.Errorf("setting object permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, fmt.Errorf("finalising object: %w", err)
	}
	return n, nil
}

// Get opens an object.
func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("opening object: %w", err)
	}
	return f, nil
}

// Delete removes an object.
func (l *Local) Delete(_ context.Context, key string) error {
	path, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

// SignedURL returns a URL the application will honour until it expires.
//
// The same shape as a presigned S3 URL, so callers do not change when the driver
// does. Unlike S3 the application still serves the bytes, but it hands off to the
// reverse proxy rather than streaming them through Go.
func (l *Local) SignedURL(key string, ttl time.Duration) (string, error) {
	if _, err := l.path(key); err != nil {
		return "", err
	}
	exp := time.Now().Add(ttl).Unix()
	sig := Sign(l.pepper, key, exp)

	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", sig)
	return fmt.Sprintf("%s/api/pub/files/%s?%s", l.baseURL, url.PathEscape(key), q.Encode()), nil
}

// path resolves a storage key to a filesystem path, refusing anything that
// escapes the root.
//
// Keys are generated by the application, never by a caller, but this is the last
// line before the filesystem: a traversal bug here reads arbitrary files.
func (l *Local) path(key string) (string, error) {
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return "", ErrInvalidKey
	}
	clean := filepath.Clean(filepath.Join(l.root, filepath.FromSlash(key)))
	if !strings.HasPrefix(clean, l.root+string(os.PathSeparator)) {
		return "", ErrInvalidKey
	}
	return clean, nil
}

// Sign returns the signature for a storage key and expiry.
func Sign(pepper []byte, key string, exp int64) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte("file-url:"))
	mac.Write([]byte(key))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a signed URL's parameters.
func VerifySignature(pepper []byte, key, sig string, exp int64, now time.Time) bool {
	if now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(Sign(pepper, key, exp)))
}

// UsableSpace reports the free bytes on the filesystem holding the objects, or
// zero if it cannot be determined.
//
// Local storage runs out silently otherwise: uploads simply start failing, and
// the first anyone hears of it is a respondent who could not attach their file.
func (l *Local) UsableSpace() (uint64, error) {
	return usableSpace(l.root)
}
