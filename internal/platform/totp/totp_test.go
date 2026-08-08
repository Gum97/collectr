package totp

import (
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

// rfcSecret is the ASCII string "12345678901234567890" from RFC 6238's test
// vectors, encoded as base32.
var rfcSecret = base32.StdEncoding.WithPadding(base32.NoPadding).
	EncodeToString([]byte("12345678901234567890"))

// TestRFC6238Vectors checks the implementation against the specification's own
// published values. Rolling this by hand is only defensible if it agrees with
// the standard exactly.
func TestRFC6238Vectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		unix int64
		want string
	}{
		{unix: 59, want: "287082"},
		{unix: 1111111109, want: "081804"},
		{unix: 1111111111, want: "050471"},
		{unix: 1234567890, want: "005924"},
		{unix: 2000000000, want: "279037"},
		{unix: 20000000000, want: "353130"},
	}

	for _, tc := range tests {
		t.Run(time.Unix(tc.unix, 0).UTC().Format(time.RFC3339), func(t *testing.T) {
			t.Parallel()

			got, err := Code(rfcSecret, time.Unix(tc.unix, 0))
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got != tc.want {
				t.Errorf("Code = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestVerifyAcceptsCurrentCode(t *testing.T) {
	t.Parallel()

	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	now := time.Now()
	code, err := Code(secret, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if err := Verify(secret, code, now); err != nil {
		t.Errorf("Verify of the current code: %v", err)
	}
}

// TestVerifyToleratesDrift covers the reason Skew exists: phone clocks drift, and
// people finish typing after the window turns over.
func TestVerifyToleratesDrift(t *testing.T) {
	t.Parallel()

	secret, _ := GenerateSecret()
	now := time.Now()

	tests := []struct {
		name       string
		codeAt     time.Time
		verifyAt   time.Time
		wantAccept bool
	}{
		{name: "same window", codeAt: now, verifyAt: now, wantAccept: true},
		{name: "one window behind", codeAt: now.Add(-Period), verifyAt: now, wantAccept: true},
		{name: "one window ahead", codeAt: now.Add(Period), verifyAt: now, wantAccept: true},
		{name: "two windows behind", codeAt: now.Add(-2 * Period), verifyAt: now},
		{name: "two windows ahead", codeAt: now.Add(2 * Period), verifyAt: now},
		{name: "an hour stale", codeAt: now.Add(-time.Hour), verifyAt: now},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			code, err := Code(secret, tc.codeAt)
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			err = Verify(secret, code, tc.verifyAt)
			if tc.wantAccept && err != nil {
				t.Errorf("Verify = %v, want accepted", err)
			}
			if !tc.wantAccept && err == nil {
				t.Error("Verify accepted a code outside the allowed windows")
			}
		})
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Parallel()

	secret, _ := GenerateSecret()
	now := time.Now()
	valid, _ := Code(secret, now)

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "too short", input: "12345"},
		{name: "too long", input: "1234567"},
		{name: "letters", input: "abcdef"},
		{name: "all zeroes", input: "000000"},
		{name: "digits reversed", input: reverse(valid)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := Verify(secret, tc.input, now); !errors.Is(err, ErrInvalidCode) {
				// "000000" and a reversed code can legitimately be the real code
				// once in a million; the test is about the shape of the answer.
				if tc.input == "000000" || tc.input == valid {
					t.Skip("collided with the genuine code")
				}
				t.Errorf("Verify(%q) = %v, want ErrInvalidCode", tc.input, err)
			}
		})
	}
}

// TestVerifyRejectsAnotherSecret is the property that matters: one user's code
// must never open another user's account.
func TestVerifyRejectsAnotherSecret(t *testing.T) {
	t.Parallel()

	mine, _ := GenerateSecret()
	theirs, _ := GenerateSecret()
	now := time.Now()

	code, err := Code(theirs, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if err := Verify(mine, code, now); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("Verify with another user's code = %v, want ErrInvalidCode", err)
	}
}

func TestVerifyAcceptsSpacedInput(t *testing.T) {
	t.Parallel()

	secret, _ := GenerateSecret()
	now := time.Now()
	code, _ := Code(secret, now)

	// Authenticator apps display "123 456"; people paste it as shown.
	spaced := code[:3] + " " + code[3:]
	if err := Verify(secret, spaced, now); err != nil {
		t.Errorf("Verify of a spaced code = %v, want accepted", err)
	}
}

func TestGenerateSecretIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 100)
	for range 100 {
		s, err := GenerateSecret()
		if err != nil {
			t.Fatalf("GenerateSecret: %v", err)
		}
		if _, dup := seen[s]; dup {
			t.Fatal("GenerateSecret returned a duplicate")
		}
		seen[s] = struct{}{}
	}
}

func TestProvisioningURI(t *testing.T) {
	t.Parallel()

	uri := ProvisioningURI("ABCDEF", "Collectr", "a@acme.vn")
	for _, want := range []string{"otpauth://totp/", "secret=ABCDEF", "issuer=Collectr", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("provisioning URI %q is missing %q", uri, want)
		}
	}
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
