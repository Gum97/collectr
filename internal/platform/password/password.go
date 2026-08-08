// Package password hashes and verifies user passwords with Argon2id.
//
// Argon2id rather than bcrypt: it resists GPU and ASIC cracking through memory
// cost, which bcrypt's fixed 4 KB working set does not. The parameters below are
// encoded into every hash, so raising them later does not invalidate existing
// passwords -- old hashes keep verifying under their own settings and are
// upgraded the next time their owner signs in.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost settings.
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams follow the OWASP recommendation for Argon2id: 19 MiB of memory
// with two passes. On a modest server this costs roughly 50 ms per verification,
// which is unnoticeable to a person signing in and ruinous to someone working
// through a leaked password list.
var DefaultParams = Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// Errors returned by this package.
var (
	ErrMismatch    = errors.New("password does not match")
	ErrInvalidHash = errors.New("hash is not in the expected format")
	ErrUnsupported = errors.New("hash uses an unsupported algorithm or version")
	ErrTooShort    = errors.New("password is too short")
	ErrTooLong     = errors.New("password is too long")
)

// MinLength is the shortest password accepted.
//
// Length beats composition rules: "Tr0ub4dor&3" is weaker than four ordinary
// words, and complexity requirements mostly teach people to append "1!".
const MinLength = 12

// MaxLength caps input so a very long password cannot be used to burn CPU.
const MaxLength = 1024

// Validate checks a password before hashing it.
func Validate(pw string) error {
	switch {
	case len(pw) < MinLength:
		return fmt.Errorf("%w: must be at least %d characters", ErrTooShort, MinLength)
	case len(pw) > MaxLength:
		return ErrTooLong
	default:
		return nil
	}
}

// Hash returns an encoded Argon2id hash of pw.
func Hash(pw string, p Params) (string, error) {
	if err := Validate(pw); err != nil {
		return "", err
	}

	salt := make([]byte, p.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(pw), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)

	// The PHC string format, so the parameters travel with the hash and a future
	// cost increase does not lock anyone out.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify checks pw against an encoded hash.
//
// It reports whether the hash should be recomputed because it was made with
// weaker parameters than the current default.
func Verify(pw, encoded string) (needsRehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(pw), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	// Constant time: a byte-by-byte comparison leaks how much of the hash matched
	// through timing.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, ErrMismatch
	}

	weaker := p.Memory < DefaultParams.Memory ||
		p.Iterations < DefaultParams.Iterations ||
		uint32(len(want)) < DefaultParams.KeyLength
	return weaker, nil
}

func decode(encoded string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return Params{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrUnsupported
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return Params{}, nil, nil, ErrUnsupported
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}

// dummyHash is verified against when an account does not exist.
var dummyHash, _ = Hash("collectr-timing-equaliser-placeholder", DefaultParams)

// VerifyDummy burns the same work as a real verification.
//
// Sign-in must take the same time whether or not the email exists. Returning
// early for an unknown address turns the login form into a way to test which of
// a list of addresses have accounts.
func VerifyDummy(pw string) {
	_, _ = Verify(pw, dummyHash)
}
