package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SubjectSession is a verified data subject session.
type SubjectSession struct {
	TenantID     uuid.UUID
	SubjectID    uuid.UUID
	Scope        string
	SubmissionID *uuid.UUID
	Expires      time.Time
}

// SubjectSigner mints and verifies portal session cookies.
//
// The session is a signed value rather than a database row: it holds no secret
// beyond the identifiers, it expires on its own, and a portal used once and
// abandoned leaves nothing behind to clean up. The scope travels inside it, so a
// receipt link cannot be widened by editing a cookie.
type SubjectSigner struct {
	pepper []byte
	ttl    time.Duration
}

// NewSubjectSigner returns a SubjectSigner.
func NewSubjectSigner(pepper []byte, ttl time.Duration) (*SubjectSigner, error) {
	if len(pepper) < 32 {
		return nil, fmt.Errorf("session pepper must be at least 32 bytes, got %d", len(pepper))
	}
	return &SubjectSigner{pepper: pepper, ttl: ttl}, nil
}

// Mint returns a signed session value.
func (s *SubjectSigner) Mint(tenantID, subjectID uuid.UUID, scope string, submissionID *uuid.UUID, now time.Time) string {
	sub := ""
	if submissionID != nil {
		sub = submissionID.String()
	}
	raw := strings.Join([]string{
		tenantID.String(), subjectID.String(), scope, sub,
		strconv.FormatInt(now.Add(s.ttl).Unix(), 10),
	}, "|")
	body := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return body + "." + s.sign(body)
}

// Verify authenticates a session value.
func (s *SubjectSigner) Verify(token string, now time.Time) (SubjectSession, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return SubjectSession{}, ErrInvalidToken
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(body))) {
		return SubjectSession{}, ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return SubjectSession{}, ErrInvalidToken
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 {
		return SubjectSession{}, ErrInvalidToken
	}

	tenantID, err := uuid.Parse(parts[0])
	if err != nil {
		return SubjectSession{}, ErrInvalidToken
	}
	subjectID, err := uuid.Parse(parts[1])
	if err != nil {
		return SubjectSession{}, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return SubjectSession{}, ErrInvalidToken
	}
	expires := time.Unix(exp, 0)
	if now.After(expires) {
		return SubjectSession{}, ErrInvalidToken
	}

	sess := SubjectSession{
		TenantID: tenantID, SubjectID: subjectID, Scope: parts[2], Expires: expires,
	}
	if parts[3] != "" {
		id, err := uuid.Parse(parts[3])
		if err != nil {
			return SubjectSession{}, ErrInvalidToken
		}
		sess.SubmissionID = &id
	}
	return sess, nil
}

func (s *SubjectSigner) sign(body string) string {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte("dsr-session:"))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
