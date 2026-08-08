package password

import (
	"errors"
	"strings"
	"testing"
)

const goodPassword = "correct horse battery staple"

func TestHashVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	encoded, err := Hash(goodPassword, DefaultParams)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if strings.Contains(encoded, goodPassword) {
		t.Fatal("the encoded hash contains the password")
	}

	needsRehash, err := Verify(goodPassword, encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if needsRehash {
		t.Error("a hash made with the current defaults should not need rehashing")
	}
}

// TestHashIsSalted pins that two users with the same password do not share a
// hash: without it, one cracked password would reveal everyone who reused it.
func TestHashIsSalted(t *testing.T) {
	t.Parallel()

	first, err := Hash(goodPassword, DefaultParams)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := Hash(goodPassword, DefaultParams)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if first == second {
		t.Fatal("hashing the same password twice produced identical output")
	}
	if _, err := Verify(goodPassword, second); err != nil {
		t.Errorf("Verify against the second hash: %v", err)
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	encoded, err := Hash(goodPassword, DefaultParams)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	tests := []string{
		"wrong password entirely",
		goodPassword + " ",
		strings.ToUpper(goodPassword),
		goodPassword[:len(goodPassword)-1],
		"",
	}
	for _, pw := range tests {
		t.Run(pw, func(t *testing.T) {
			t.Parallel()
			if _, err := Verify(pw, encoded); !errors.Is(err, ErrMismatch) {
				t.Errorf("Verify(%q) = %v, want ErrMismatch", pw, err)
			}
		})
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		encoded string
		want    error
	}{
		{name: "empty", encoded: "", want: ErrInvalidHash},
		{name: "not a phc string", encoded: "plaintext", want: ErrInvalidHash},
		{name: "bcrypt", encoded: "$2a$10$abcdefghijklmnopqrstuv", want: ErrInvalidHash},
		{name: "argon2i instead of id", encoded: "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5", want: ErrUnsupported},
		{name: "truncated", encoded: "$argon2id$v=19$m=19456,t=2,p=1", want: ErrInvalidHash},
		{name: "bad base64 salt", encoded: "$argon2id$v=19$m=19456,t=2,p=1$!!!$a2V5", want: ErrInvalidHash},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Verify(goodPassword, tc.encoded); !errors.Is(err, tc.want) {
				t.Errorf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyReportsWeakParameters covers the upgrade path: a hash made under
// older, cheaper settings still verifies, and says it should be replaced.
func TestVerifyReportsWeakParameters(t *testing.T) {
	t.Parallel()

	weak := Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	encoded, err := Hash(goodPassword, weak)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	needsRehash, err := Verify(goodPassword, encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !needsRehash {
		t.Error("a hash made with weaker parameters should be flagged for rehashing")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pw   string
		want error
	}{
		{name: "long passphrase", pw: goodPassword},
		{name: "exactly the minimum", pw: strings.Repeat("a", MinLength)},
		{name: "one short", pw: strings.Repeat("a", MinLength-1), want: ErrTooShort},
		{name: "empty", pw: "", want: ErrTooShort},
		{name: "absurdly long", pw: strings.Repeat("a", MaxLength+1), want: ErrTooLong},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.pw)
			if tc.want == nil && err != nil {
				t.Errorf("Validate = %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}

func BenchmarkHash(b *testing.B) {
	// Worth watching: if this drops far below ~50ms the parameters have become
	// too cheap for the hardware.
	for b.Loop() {
		if _, err := Hash(goodPassword, DefaultParams); err != nil {
			b.Fatalf("Hash: %v", err)
		}
	}
}
