// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/code/git"
)

// TestMatchingRefs: refs whose names contain the filter in any case, those named like it first, the others in
// their order (the most recent commit first, as git lists them).
func TestMatchingRefs(t *testing.T) {
	var refs []git.Ref
	for _, name := range []string{"feature/fix-login", "Fix", "release/1.0", "fix", "hotfix", "main"} {
		refs = append(refs, git.Ref{Name: "refs/heads/" + name})
	}
	names := func(list []git.Ref) string {
		out := []string{}
		for _, r := range list {
			out = append(out, r.ShortName())
		}
		return strings.Join(out, " ")
	}
	for filter, want := range map[string]string{
		"":         "feature/fix-login Fix release/1.0 fix hotfix main",
		"fix":      "Fix fix feature/fix-login hotfix",
		" FIX ":    "Fix fix feature/fix-login hotfix",
		"hot":      "hotfix",
		"release/": "release/1.0",
		"nothing":  "",
	} {
		if got := names(matchingRefs(refs, filter)); got != want {
			t.Errorf("%q: %q, want %q", filter, got, want)
		}
	}
}

// TestPageOf: the page at an offset, an empty one beyond the end, and limits kept within 1 to 200.
func TestPageOf(t *testing.T) {
	list := make([]int, 250)
	for i := range list {
		list[i] = i
	}
	for _, c := range []struct {
		limit, offset int
		first, n      int
	}{
		{2, 0, 0, 2},
		{2, 2, 2, 2},
		{100, 200, 200, 50},
		{10, 250, 0, 0},
		{10, 1000, 0, 0},
		{10, -5, 0, 10},
		{0, 0, 0, 50},
		{201, 0, 0, 50},
		{200, 0, 0, 200},
	} {
		got := pageOf(list, c.limit, c.offset)
		if len(got) != c.n || (c.n > 0 && got[0] != c.first) || !slices.Equal(got, list[min(max(c.offset, 0), len(list)):][:c.n]) {
			t.Errorf("limit %d, offset %d: %d items from %v", c.limit, c.offset, len(got), got[:min(len(got), 1)])
		}
	}
}
