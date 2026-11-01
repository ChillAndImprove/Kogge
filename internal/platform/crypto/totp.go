// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 and every authenticator app use HMAC-SHA1; HMAC keeps it sound
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Time-based one-time passwords (RFC 6238), as authenticator apps make them: HMAC-SHA1 over 30-second steps,
// six digits.
const (
	TOTPPeriod = 30
	totpDigits = 6
	// totpSkew is how many steps before and after the current one count: clocks of phones drift.
	totpSkew = 1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random secret of 160 bits (the size RFC 4226 recommends).
func NewTOTPSecret() []byte {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// TOTPSecretText is the secret as people type it into an app: base32 in groups of four.
func TOTPSecretText(secret []byte) string {
	s := totpEncoding.EncodeToString(secret)
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// TOTPURI is the address an authenticator app reads from a QR code.
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{"secret": {totpEncoding.EncodeToString(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(TOTPPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// TOTPStep is the time step of a moment.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// TOTPCode is the code of a time step.
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

// VerifyTOTP checks a code against the steps around now and returns the step it belongs to. Only steps after
// usedStep count, so that a code cannot be used twice.
func VerifyTOTP(secret []byte, code string, now time.Time, usedStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	current := TOTPStep(now)
	var found int64
	ok := 0
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		// Every step is compared, in constant time, whether or not an earlier one matched.
		match := subtle.ConstantTimeCompare([]byte(TOTPCode(secret, step)), []byte(code))
		if match == 1 && step > usedStep && found == 0 {
			found, ok = step, 1
		}
	}
	return found, ok == 1
}
