package signing

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner([]byte(strings.Repeat("k", 32)), 30*time.Minute)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSignerRejectsShortPepper(t *testing.T) {
	t.Parallel()

	if _, err := NewSigner([]byte("too short"), time.Minute); err == nil {
		t.Fatal("NewSigner with a 9-byte pepper = nil error, want error")
	}
}

func TestMintVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestSigner(t)
	now := time.Now()
	visitID, linkID := uuid.New(), uuid.New()

	got, err := s.Verify(s.Mint(visitID, linkID, now), now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.VisitID != visitID {
		t.Errorf("VisitID = %s, want %s", got.VisitID, visitID)
	}
	if got.LinkID != linkID {
		t.Errorf("LinkID = %s, want %s", got.LinkID, linkID)
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Parallel()

	s := newTestSigner(t)
	now := time.Now()
	valid := s.Mint(uuid.New(), uuid.New(), now)
	body, sig, _ := strings.Cut(valid, ".")

	tests := []struct {
		name  string
		token string
		at    time.Time
	}{
		{name: "empty", token: "", at: now},
		{name: "no signature", token: body, at: now},
		{name: "tampered body", token: body[:len(body)-2] + "AB" + "." + sig, at: now},
		{name: "tampered signature", token: body + "." + sig[:len(sig)-2] + "AB", at: now},
		{name: "signature of another token", token: body + "." + strings.Repeat("x", len(sig)), at: now},
		{name: "not base64", token: "!!!." + sig, at: now},
		{name: "expired", token: valid, at: now.Add(31 * time.Minute)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := s.Verify(tc.token, tc.at); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Verify(%q) = %v, want ErrInvalidToken", tc.name, err)
			}
		})
	}
}

// TestVerifyRejectsForeignPepper covers the property that matters most: tokens
// must not be transferable between deployments, so one operator's token cannot
// be replayed against another's funnel.
func TestVerifyRejectsForeignPepper(t *testing.T) {
	t.Parallel()

	mine := newTestSigner(t)
	theirs, err := NewSigner([]byte(strings.Repeat("z", 32)), 30*time.Minute)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	now := time.Now()
	token := theirs.Mint(uuid.New(), uuid.New(), now)
	if _, err := mine.Verify(token, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Verify of foreign token = %v, want ErrInvalidToken", err)
	}
}
