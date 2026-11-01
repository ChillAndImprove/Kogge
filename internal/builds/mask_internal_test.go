// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// TestMaskSecrets: the server masks what agents send (also agents of older versions, which mask only the secrets
// as they are) and test reports: secrets as they are and encoded, the longer forms of several secrets first.
func TestMaskSecrets(t *testing.T) {
	forms := maskForms([]string{"token-1234\n", "token-1234-and-more", "abc"})
	for i := 1; i < len(forms); i++ {
		if len(forms[i]) > len(forms[i-1]) {
			t.Fatalf("forms are not longest first: %q", forms)
		}
	}
	log := "a token-1234-and-more b token-1234 c " + base64.StdEncoding.EncodeToString([]byte("user:token-1234-and-more")) +
		" d " + url.QueryEscape("token-1234-and-more") + " short abc\n"
	got := string(maskSecrets([]byte(log), forms))
	if strings.Contains(got, "token") || strings.Contains(got, "and-more") || !strings.HasSuffix(got, " short abc\n") ||
		strings.Count(got, "****") != 4 {
		t.Errorf("masked: %q", got)
	}
}
