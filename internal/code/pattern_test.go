// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import "testing"

func TestMatchRef(t *testing.T) {
	for _, c := range []struct {
		pattern, ref string
		want         bool
	}{
		{"main", "refs/heads/main", true},
		{"main", "refs/tags/main", false},
		{"main", "refs/heads/x/main", false},
		{"release/*", "refs/heads/release/1.0", true},
		{"release/*", "refs/heads/release/1.0/hotfix", false},
		{"feature/**", "refs/heads/feature/a/b", true},
		{"feature/**", "refs/heads/feature", false},
		{"**/hotfix", "refs/heads/hotfix", true},
		{"**/hotfix", "refs/heads/release/1.0/hotfix", true},
		{"**/hotfix", "refs/heads/hotfixes", false},
		{"**", "refs/heads/any/branch", true},
		{"refs/tags/v*", "refs/tags/v1.0", true},
		{"refs/tags/v*", "refs/heads/v1.0", false},
		// Patterns of migrated branch permissions: a suffix of the full ref name.
		{"refs/**/main", "refs/heads/main", true},
		{"refs/**/main", "refs/tags/main", true},
		{"refs/**/main", "refs/heads/old/main", true},
		{"refs/**/main", "refs/heads/domain", false},
		{"refs/**/heads/**/master", "refs/heads/master", true},
		{"refs/**/heads/**/master", "refs/heads/stable/master", true},
		{"refs/**/PROJECT-*", "refs/heads/stable/PROJECT-new", true},
		{"refs/**/?.?", "refs/tags/3.1", true},
		{"refs/**/?.?", "refs/tags/3.10", false},
		// Branch names with other characters than ASCII (audit M8).
		{"prüfung", "refs/heads/prüfung", true},
		{"prüfung", "refs/heads/prufung", false},
		{"release/größe-*", "refs/heads/release/größe-2", true},
		{"?ber", "refs/heads/über", true},
		{"日本/*", "refs/heads/日本/main", true},
	} {
		if got := matchRef(c.pattern, c.ref); got != c.want {
			t.Errorf("matchRef(%q, %q) = %v", c.pattern, c.ref, got)
		}
	}
}
