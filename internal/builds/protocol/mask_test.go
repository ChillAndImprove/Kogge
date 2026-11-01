// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package protocol

import (
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestMaskForms: a secret is found in base64 wherever it stands in the encoded data, with either alphabet, and
// URL-encoded; short secrets have no encoded forms that would hide ordinary text.
func TestMaskForms(t *testing.T) {
	secret := "s3cr3t/Value+42?"
	forms := MaskForms(secret)
	if forms[0] != secret {
		t.Fatalf("forms %q do not start with the secret", forms)
	}
	found := func(text string) bool {
		return slices.ContainsFunc(forms, func(f string) bool { return strings.Contains(text, f) })
	}
	for _, prefix := range []string{"", "u", "us", "user:", "deploy-bot:"} {
		for _, suffix := range []string{"", "\n", ":x", "@registry"} {
			data := []byte(prefix + secret + suffix)
			for name, enc := range map[string]*base64.Encoding{"std": base64.StdEncoding, "url": base64.URLEncoding,
				"raw": base64.RawStdEncoding} {
				if text := enc.EncodeToString(data); !found(text) {
					t.Errorf("%s base64 of %q: %s is not masked", name, data, text)
				}
			}
		}
	}
	for _, text := range []string{"https://bot:" + url.QueryEscape(secret) + "@git.example.com/x.git",
		"https://git.example.com/" + url.PathEscape(secret) + "/x"} {
		if !found(text) {
			t.Errorf("%s is not masked", text)
		}
	}
	if f := MaskForms("abc"); f != nil {
		t.Errorf("forms of a short secret: %q", f)
	}
	if f := MaskForms("abcde"); len(f) != 1 || f[0] != "abcde" {
		t.Errorf("forms of a secret of five letters: %q (its encoded forms would be too short)", f)
	}
	for _, f := range forms {
		if len(f) < MinMasked {
			t.Errorf("form %q is shorter than %d", f, MinMasked)
		}
	}
}
