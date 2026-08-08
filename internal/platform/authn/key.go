package authn

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// keyPrefixLen is how many leading characters of the secret are stored in clear
// so a key can be looked up (and shown in the UI) without a table scan.
const keyPrefixLen = 8

// generateKey returns a raw API key and its lookup prefix.
//
// The clc_live_ prefix is deliberate: secret scanners key off recognisable
// prefixes, so a key pasted into a public repository can be caught and revoked
// rather than sitting there unnoticed.
func generateKey() (raw, prefix string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("reading random bytes: %w", err)
	}
	// URL-safe, unpadded: keys get pasted into headers, env files and shell
	// commands where '+', '/' and '=' all cause trouble.
	secret := strings.NewReplacer("-", "", "_", "").Replace(base64.RawURLEncoding.EncodeToString(buf))
	if len(secret) < keyPrefixLen+16 {
		return "", "", fmt.Errorf("generated key too short after normalisation")
	}
	return "clc_live_" + secret, secret[:keyPrefixLen], nil
}
