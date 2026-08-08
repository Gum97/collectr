// Package idgen produces the identifiers users see.
//
// Short codes are random rather than derived from a counter. A counter would be
// collision-free but enumerable, and enumerating a Collectr deployment means
// harvesting every form that collects personal data, plus a usable estimate of
// each tenant's campaign volume. At 62^7 (~3.5e12) combinations the collision
// probability per insert is ~1.4e-7 for 500k links, which a bounded retry over a
// unique constraint absorbs comfortably.
package idgen

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// CodeAlphabet is base62. Base64 is unusable here: '/' is a path separator and
// '+' decodes to a space in query strings.
const CodeAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// DefaultCodeLength balances brevity against keyspace; see the package comment.
const DefaultCodeLength = 7

// reservedCodes are paths the router owns. A custom alias colliding with one
// would shadow a real route, so they are rejected at creation time.
var reservedCodes = map[string]struct{}{
	"r": {}, "f": {}, "q": {}, "api": {}, "admin": {}, "dsr": {},
	"health": {}, "healthz": {}, "metrics": {}, "static": {}, "assets": {},
	"login": {}, "logout": {}, "signup": {}, "settings": {}, "_next": {},
}

// Code returns a cryptographically random base62 string of length n.
func Code(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("code length must be positive, got %d", n)
	}
	// Rejection sampling keeps the distribution uniform: 256 is not a multiple of
	// 62, so naive modulo would bias the first few letters of the alphabet.
	const limit = 256 - (256 % len(CodeAlphabet))
	var sb strings.Builder
	sb.Grow(n)
	buf := make([]byte, n)
	for sb.Len() < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("reading random bytes: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			sb.WriteByte(CodeAlphabet[int(b)%len(CodeAlphabet)])
			if sb.Len() == n {
				break
			}
		}
	}
	return sb.String(), nil
}

// ValidateAlias checks a user-supplied custom alias.
func ValidateAlias(alias string) error {
	if len(alias) < 3 || len(alias) > 64 {
		return fmt.Errorf("alias must be between 3 and 64 characters")
	}
	if _, reserved := reservedCodes[strings.ToLower(alias)]; reserved {
		return fmt.Errorf("alias %q is reserved", alias)
	}
	for _, r := range alias {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return fmt.Errorf("alias may only contain letters, digits, '-' and '_'")
		}
	}
	return nil
}
