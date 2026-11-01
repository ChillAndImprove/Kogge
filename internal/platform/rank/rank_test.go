// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package rank

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestBetween(t *testing.T) {
	cases := [][2]string{{"", ""}, {"", "V"}, {"V", ""}, {"A", "B"}, {"A", "A1"}, {"Az", "B"}, {"zz", ""}, {"", "01"}, {"0V", "0W"}}
	for _, c := range cases {
		k, err := Between(c[0], c[1])
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if !Valid(k) || (c[0] != "" && k <= c[0]) || (c[1] != "" && k >= c[1]) {
			t.Errorf("Between(%q, %q) = %q", c[0], c[1], k)
		}
	}
	if _, err := Between("B", "A"); err != ErrOrder {
		t.Errorf("reversed bounds: %v", err)
	}
	if _, err := Between("A0", ""); err == nil {
		t.Error("trailing zero accepted")
	}
}

// Random insertions keep the list sorted and keys short enough for practical lists.
func TestRandomInsertions(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	keys := []string{After("")}
	for i := 0; i < 5000; i++ {
		pos := r.IntN(len(keys) + 1)
		lo, hi := "", ""
		if pos > 0 {
			lo = keys[pos-1]
		}
		if pos < len(keys) {
			hi = keys[pos]
		}
		k, err := Between(lo, hi)
		if err != nil {
			t.Fatal(err)
		}
		keys = slices.Insert(keys, pos, k)
	}
	if !slices.IsSorted(keys) {
		t.Fatal("keys not sorted")
	}
	maxLen := 0
	for _, k := range keys {
		maxLen = max(maxLen, len(k))
	}
	if maxLen > 12 {
		t.Errorf("keys grew to %d characters", maxLen)
	}
}

// The worst case — always inserting at the same place — grows linearly and is fixed by rebalancing.
func TestRepeatedInsertAtFront(t *testing.T) {
	first := After("")
	for i := 0; i < 200; i++ {
		k, err := Between("", first)
		if err != nil || k >= first {
			t.Fatalf("step %d: %q %v", i, k, err)
		}
		first = k
	}
}

// Appending (every new issue) and prepending (moving to the top) keep keys short: each new key is the next one at
// the same length, so a list of many thousand keys needs no more than a few characters.
func TestAppendAndPrependStayShort(t *testing.T) {
	keys := []string{After("")}
	for range 100_000 {
		k := After(keys[len(keys)-1])
		if !Valid(k) || k <= keys[len(keys)-1] {
			t.Fatalf("After(%q) = %q", keys[len(keys)-1], k)
		}
		keys = append(keys, k)
	}
	front := []string{keys[0]}
	for range 100_000 {
		first := front[len(front)-1]
		k := Before(first)
		if !Valid(k) || k >= first {
			t.Fatalf("Before(%q) = %q", first, k)
		}
		front = append(front, k)
	}
	slices.Reverse(front)
	keys = append(front[:len(front)-1], keys...)
	if !slices.IsSorted(keys) {
		t.Fatal("keys not sorted")
	}
	for _, k := range keys {
		if len(k) > stepDigits {
			t.Fatalf("key %q is longer than %d characters", k, stepDigits)
		}
	}
	// There is room between neighbours, and at both ends of the key space.
	if k, err := Between(keys[0], keys[1]); err != nil || k <= keys[0] || k >= keys[1] {
		t.Errorf("Between(%q, %q) = %q, %v", keys[0], keys[1], k, err)
	}
	for _, edge := range []string{"zzzz", "z"} {
		if k := After(edge); !Valid(k) || k <= edge {
			t.Errorf("After(%q) = %q", edge, k)
		}
	}
	for _, edge := range []string{"0001", "1", "01"} {
		if k := Before(edge); !Valid(k) || k >= edge {
			t.Errorf("Before(%q) = %q", edge, k)
		}
	}
}

func TestSpread(t *testing.T) {
	for _, n := range []int{1, 2, 10, 61, 62, 1000, 100000} {
		keys := Spread(n)
		if len(keys) != n || !slices.IsSorted(keys) {
			t.Fatalf("Spread(%d) not sorted", n)
		}
		for i, k := range keys {
			if !Valid(k) || (i > 0 && keys[i-1] == k) {
				t.Fatalf("Spread(%d)[%d] = %q", n, i, k)
			}
		}
		if _, err := Between(keys[0], keys[n-1]+"z"); err != nil {
			t.Fatal(err)
		}
	}
}
