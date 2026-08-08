// Package signing mints and verifies the short-lived visit tokens that stitch a
// click to a form view to a submission.
//
// The token replaces a tracking cookie on purpose. It is scoped to one link, it
// expires in minutes, and it carries no identity -- which keeps the funnel
// measurable while keeping the platform's own tracking out of the category of
// data that would itself require a consent basis.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidToken means the token was malformed, tampered with, or expired.
// Callers must not distinguish between those cases to the outside world.
var ErrInvalidToken = errors.New("invalid visit token")

// Visit is the decoded payload of a visit token.
type Visit struct {
	VisitID uuid.UUID
	LinkID  uuid.UUID
	Expires time.Time
}

// Signer mints and verifies visit tokens using a deployment-local pepper.
type Signer struct {
	pepper []byte
	ttl    time.Duration
}

// NewSigner returns a Signer. The pepper must be at least 32 bytes.
func NewSigner(pepper []byte, ttl time.Duration) (*Signer, error) {
	if len(pepper) < 32 {
		return nil, fmt.Errorf("visit pepper must be at least 32 bytes, got %d", len(pepper))
	}
	return &Signer{pepper: pepper, ttl: ttl}, nil
}

// Mint returns a token binding a fresh visit id to linkID.
func (s *Signer) Mint(visitID, linkID uuid.UUID, now time.Time) string {
	body := s.body(visitID, linkID, now.Add(s.ttl).Unix())
	return body + "." + s.sign(body)
}

// Verify decodes and authenticates a token.
func (s *Signer) Verify(token string, now time.Time) (Visit, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Visit{}, ErrInvalidToken
	}
	// Constant-time comparison: a byte-by-byte check leaks the signature through
	// response timing, which is enough to forge one given patience.
	if !hmac.Equal([]byte(sig), []byte(s.sign(body))) {
		return Visit{}, ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Visit{}, ErrInvalidToken
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return Visit{}, ErrInvalidToken
	}
	visitID, err := uuid.Parse(parts[0])
	if err != nil {
		return Visit{}, ErrInvalidToken
	}
	linkID, err := uuid.Parse(parts[1])
	if err != nil {
		return Visit{}, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Visit{}, ErrInvalidToken
	}
	expires := time.Unix(exp, 0)
	if now.After(expires) {
		return Visit{}, ErrInvalidToken
	}
	return Visit{VisitID: visitID, LinkID: linkID, Expires: expires}, nil
}

func (s *Signer) body(visitID, linkID uuid.UUID, exp int64) string {
	raw := visitID.String() + "|" + linkID.String() + "|" + strconv.FormatInt(exp, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
