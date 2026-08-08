// Package totp implements time-based one-time passwords (RFC 6238).
//
// Written out rather than pulled in as a dependency: the algorithm is HMAC plus
// a modulo, the specification has not moved since 2011, and a second factor
// guarding the accounts that can read personal data is worth understanding
// completely.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Parameters fixed by what authenticator apps actually implement. Google
// Authenticator ignores anything else, so making them configurable would only
// create combinations that silently fail on a user's phone.
const (
	// Period is the length of one code's validity window.
	Period = 30 * time.Second
	// Digits is the code length.
	Digits = 6
	// SecretBytes is the shared secret size (160 bits, as RFC 4226 recommends).
	SecretBytes = 20
)

// Skew is how many windows either side of now are accepted.
//
// One window each way covers ordinary clock drift and a person who starts typing
// as the code rolls over. Wider would meaningfully extend how long a phished code
// stays usable.
const Skew = 1

// ErrInvalidCode means the code did not match any accepted window.
var ErrInvalidCode = errors.New("invalid verification code")

// GenerateSecret returns a new base32-encoded shared secret.
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating totp secret: %w", err)
	}
	// Unpadded: authenticator apps reject the '=' characters.
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// ProvisioningURI returns the otpauth:// URI an authenticator app scans.
func ProvisioningURI(secret, issuer, account string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(Digits))
	v.Set("period", fmt.Sprint(int(Period.Seconds())))

	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// Code returns the expected code for a moment in time.
func Code(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return code(key, uint64(t.Unix())/uint64(Period.Seconds())), nil
}

// Verify checks a user-supplied code against the accepted windows.
func Verify(secret, input string, now time.Time) error {
	key, err := decodeSecret(secret)
	if err != nil {
		return err
	}
	// Whitespace is what people paste; digits are what matters.
	input = strings.TrimSpace(strings.ReplaceAll(input, " ", ""))
	if len(input) != Digits {
		return ErrInvalidCode
	}

	counter := uint64(now.Unix()) / uint64(Period.Seconds())
	for offset := -Skew; offset <= Skew; offset++ {
		c := counter
		switch {
		case offset < 0:
			c -= uint64(-offset)
		case offset > 0:
			c += uint64(offset)
		}
		// Constant time, and every window is checked even after a match: an early
		// return would leak which window succeeded through timing.
		if subtle.ConstantTimeCompare([]byte(code(key, c)), []byte(input)) == 1 {
			return nil
		}
	}
	return ErrInvalidCode
}

func code(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	// SHA-1 is specified by RFC 6238 and is not a weakness here: HMAC-SHA1 does
	// not rely on collision resistance, and every authenticator app expects it.
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for range Digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, value%mod)
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decoding totp secret: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("totp secret is empty")
	}
	return key, nil
}
