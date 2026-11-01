// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package crypto

import (
	"strings"
	"testing"
	"time"
)

// TestTOTP: the test vectors of RFC 6238 (SHA-1, cut to six digits), drift of one step, no reuse of a step.
func TestTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924",
		2000000000: "279037", 20000000000: "353130"} {
		if got := TOTPCode(secret, TOTPStep(time.Unix(unix, 0))); got != want {
			t.Errorf("code at %d = %s, want %s", unix, got, want)
		}
	}
	now := time.Unix(1111111111, 0)
	step := TOTPStep(now)
	for _, d := range []int64{-1, 0, 1} {
		if s, ok := VerifyTOTP(secret, TOTPCode(secret, step+d), now, 0); !ok || s != step+d {
			t.Errorf("drift %d: %d %v", d, s, ok)
		}
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, step+2), now, 0); ok {
		t.Error("a code two steps ahead counted")
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, step), now, step); ok {
		t.Error("a used step counted again")
	}
	if _, ok := VerifyTOTP(secret, "05 04 71", now, 0); !ok {
		t.Error("spaces in the code")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(secret, bad, now, 0); ok {
			t.Errorf("%q counted", bad)
		}
	}
	uri := TOTPURI("Kogge Test", "anna@example.com", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/Kogge%20Test:anna@example.com?") || !strings.Contains(uri, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ") ||
		!strings.Contains(uri, "issuer=Kogge+Test") {
		t.Errorf("uri = %s", uri)
	}
	if got := TOTPSecretText(secret); got != "GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ" {
		t.Errorf("secret text = %s", got)
	}
	if len(NewTOTPSecret()) != 20 {
		t.Error("secret size")
	}
}
