// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"strings"
)

// Token prefixes make leaked tokens recognisable for secret scanners.
const (
	PrefixAccessToken = "kgp" // personal access token
	PrefixAgentSecret = "kga" // build agent
	PrefixJobToken    = "kgj" // build job
	PrefixAgentReg    = "kgr" // agent registration
	PrefixHookToken   = "kgh" // Git hook call
	PrefixSession     = "kgs" // browser session
	PrefixTrigger     = "kgt" // pipeline trigger
)

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns a random 256-bit token with the prefix (e.g. kgp_ABC…) and its SHA-256 hash for storage.
func NewToken(prefix string) (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = prefix + "_" + strings.ToLower(tokenEncoding.EncodeToString(b))
	return token, HashToken(token)
}

// HashToken returns the SHA-256 of a token. Tokens have 256 bits of entropy, so a fast hash is sufficient.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// TokenHint returns the part of a token that may be shown later to recognise it.
func TokenHint(token string) string {
	if len(token) < 12 {
		return token
	}
	return token[:8] + "…" + token[len(token)-4:]
}

// RandomBase32 returns n random characters of base32 (letters and the digits 2–7: easy to type), 5 bits each.
func RandomBase32(n int) string {
	b := make([]byte, (n*5+7)/8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return tokenEncoding.EncodeToString(b)[:n]
}

// RandomString returns n random bytes, base64url-encoded without padding.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// EqualStrings compares two strings in constant time.
func EqualStrings(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
