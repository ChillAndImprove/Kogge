// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package protocol

import (
	"encoding/base64"
	"net/url"
	"slices"
)

// Logs mask the secrets a job receives in the forms programs print them: as they are, and encoded.
// Base64 carries credentials in HTTP basic authentication, in container registry and Kubernetes configuration
// and in JSON web tokens, usually together with other text ("user:secret"); URL encoding carries them in addresses
// with credentials. Masking is a safeguard against mistakes: a pipeline that sets out to reveal a secret can always
// transform it some other way.

const (
	// MinMasked is the length below which a secret is not masked: it would hide ordinary words.
	MinMasked = 4
	// minEncoded is the length below which an encoded form is not masked (it is a part of a secret's encoding).
	minEncoded = 8
)

// MaskForms returns the texts that logs replace for a secret value: the value and its encoded forms, without
// duplicates. A value shorter than MinMasked has none. Whoever replaces the forms of several secrets replaces the
// longer ones first, so that a shorter one does not leave the rest of a longer one behind.
func MaskForms(secret string) []string {
	if len(secret) < MinMasked {
		return nil
	}
	out := []string{secret}
	add := func(s string) {
		if len(s) >= minEncoded && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		for _, s := range base64Cores(secret, enc) {
			add(s)
		}
	}
	add(url.QueryEscape(secret))
	add(url.PathEscape(secret))
	return out
}

// base64Cores returns what base64 makes of a value wherever it stands in encoded data. Base64 writes three bytes as
// four characters, so the characters depend on the value's position within those groups: for each of the three
// positions, the characters that the value's bytes alone decide (those that also encode bits of the bytes before
// or after it differ from one text to the next).
func base64Cores(value string, enc *base64.Encoding) []string {
	out := make([]string, 0, 3)
	for shift := 0; shift < 3; shift++ {
		data := make([]byte, shift, shift+len(value))
		data = append(data, value...)
		text := enc.EncodeToString(data)
		if start, end := (8*shift+5)/6, 8*len(data)/6; end > start {
			out = append(out, text[start:end])
		}
	}
	return out
}
