// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package rank implements fractional ranking: string keys that sort byte-wise (COLLATE "C") and between any two
// of which a new key can be created, so moving an item writes only that item.
package rank

import (
	"errors"
	"strings"
)

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const base = len(digits)

// MaxLength is the key length above which a list should be rebalanced.
const MaxLength = 48

func index(c byte) int { return strings.IndexByte(digits, c) }

// Valid reports whether s is a rank key: non-empty, only base-62 digits, not ending in '0'.
func Valid(s string) bool {
	if s == "" || s[len(s)-1] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if index(s[i]) < 0 {
			return false
		}
	}
	return true
}

// ErrOrder is returned when the bounds are not in ascending order.
var ErrOrder = errors.New("rank: lower bound is not below upper bound")

// Between returns a key strictly between a and b. An empty a means "before everything", an empty b "after
// everything".
func Between(a, b string) (string, error) {
	if (a != "" && !Valid(a)) || (b != "" && !Valid(b)) {
		return "", errors.New("rank: invalid key")
	}
	if a != "" && b != "" && a >= b {
		return "", ErrOrder
	}
	return midpoint(a, b), nil
}

// midpoint returns a key between a and b, where b == "" means the upper end of the key space.
func midpoint(a, b string) string {
	var prefix strings.Builder
	for n := 0; ; n++ {
		da := 0
		if n < len(a) {
			da = index(a[n])
		}
		db := base
		if b != "" && n < len(b) {
			db = index(b[n])
		}
		if da == db {
			prefix.WriteByte(digits[da])
			continue
		}
		if mid := (da + db) / 2; mid > da {
			prefix.WriteByte(digits[mid])
			return prefix.String()
		}
		// Adjacent digits: keep a's digit and continue below the upper end.
		prefix.WriteByte(digits[da])
		rest := ""
		if n+1 < len(a) {
			rest = a[n+1:]
		}
		return prefix.String() + midpoint(rest, "")
	}
}

// stepDigits is the length in which appending and prepending step: one more at the last of these digits (or of a
// longer key). A list appended to keeps keys of this length for base^stepDigits keys; the midpoint towards the end
// of the key space, which halves what is left, made them a digit longer every few keys.
const stepDigits = 4

// After returns a key after a (at the end of a list).
func After(a string) string {
	if a == "" {
		return midpoint("", "")
	}
	d := digitsOf(a)
	for i := len(d) - 1; i >= 0; i-- {
		if d[i] < base-1 {
			d[i]++
			return keyOf(d)
		}
		d[i] = 0
	}
	// Nothing comes after a at its length (it is all z): a longer key does.
	return midpoint(a, "")
}

// Before returns a key before b (at the start of a list).
func Before(b string) string {
	if b == "" {
		return midpoint("", "")
	}
	d := digitsOf(b)
	for i := len(d) - 1; i >= 0; i-- {
		if d[i] > 0 {
			d[i]--
			if k := keyOf(d); k != "" {
				return k
			}
			break
		}
		d[i] = base - 1
	}
	// Nothing comes before b at its length: a longer key does.
	return midpoint("", b)
}

// digitsOf returns the digits of a key, with zeros up to stepDigits.
func digitsOf(k string) []int {
	d := make([]int, max(len(k), stepDigits))
	for i := 0; i < len(k); i++ {
		d[i] = max(index(k[i]), 0)
	}
	return d
}

// keyOf returns the key of digits, without trailing zeros (a key never ends in '0').
func keyOf(d []int) string {
	n := len(d)
	for n > 0 && d[n-1] == 0 {
		n--
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[d[i]]
	}
	return string(b)
}

// Spread returns n ascending, evenly spaced keys of equal length, for rebalancing or bulk imports.
func Spread(n int) []string {
	if n <= 0 {
		return nil
	}
	width := 1
	for capacity := base - 1; capacity < n*4; capacity *= base {
		width++
	}
	space := 1
	for i := 0; i < width; i++ {
		space *= base
	}
	step := space / (n + 1)
	out := make([]string, n)
	for i := range out {
		v := step * (i + 1)
		b := make([]byte, width)
		for j := width - 1; j >= 0; j-- {
			b[j] = digits[v%base]
			v /= base
		}
		key := strings.TrimRight(string(b), "0")
		out[i] = key
	}
	return out
}
