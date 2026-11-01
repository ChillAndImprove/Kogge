// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import "testing"

// TestPageLimit: lists give what was asked for, up to the 500 that the API documents, instead of quietly 50.
func TestPageLimit(t *testing.T) {
	for limit, want := range map[int]int{-1: 50, 0: 50, 1: 1, 30: 30, 200: 200, 201: 201, 500: 500, 501: 500} {
		if got := pageLimit(limit); got != want {
			t.Errorf("pageLimit(%d) = %d, want %d", limit, got, want)
		}
	}
}
