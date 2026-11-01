// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		commit commit
		want   change
		ok     bool
	}{
		{commit{"a1", "feat(issues): bulk edit", ""}, change{kind: "feat", scope: "issues", summary: "bulk edit", hash: "a1"}, true},
		{commit{"a2", "fix: a canceled request is not a server error", "Co-Authored-By: X <x@example.com>\n"},
			change{kind: "fix", summary: "a canceled request is not a server error", hash: "a2"}, true},
		{commit{"a3", "feat(api)!: tokens expire", ""}, change{kind: "feat", scope: "api", summary: "tokens expire", hash: "a3", breaking: true}, true},
		{commit{"a4", "refactor(code): one Git runner", "Why it changed.\n\nBREAKING CHANGE: hooks move to\n  git-hooks/ of the data directory.\nRefs: #12\n"},
			change{kind: "refactor", scope: "code", summary: "one Git runner", hash: "a4", breaking: true,
				note: "hooks move to git-hooks/ of the data directory."}, true},
		{commit{"a5", "Merge branch 'x'", ""}, change{}, false},
		{commit{"a6", "Fix things", ""}, change{}, false},
	} {
		got, ok := parse(c.commit)
		if ok != c.ok || got != c.want {
			t.Errorf("parse(%q) = %+v, %v; want %+v, %v", c.commit.subject, got, ok, c.want, c.ok)
		}
	}
}

func TestParseLog(t *testing.T) {
	log := "h1\x00feat: one\x00body one\n\x1e\nh2\x00fix: two\x00\x1e\n"
	got := parseLog(log)
	if len(got) != 2 || got[0] != (commit{"h1", "feat: one", "body one\n"}) || got[1] != (commit{"h2", "fix: two", ""}) {
		t.Errorf("parseLog = %+v", got)
	}
}

// TestNotes: the notes group the changes users and operators care about, list breaking changes first with their
// explanation, name the migrations of an upgrade, and count what they leave out.
func TestNotes(t *testing.T) {
	commits := []commit{
		{"1111111111111111", "feat(issues): bulk edit", ""},
		{"2222222222222222", "fix(security): IPv4 inside IPv6 in the network policy", ""},
		{"3333333333333333", "fix(wiki): keep drafts", ""},
		{"4444444444444444", "feat(api)!: tokens expire after a year", "BREAKING CHANGE: renew tokens older than a year.\n"},
		{"5555555555555555", "perf: index unstemmed text search", ""},
		{"6666666666666666", "test(code): cover renames", ""},
		{"7777777777777777", "docs: explain backups", ""},
		{"8888888888888888", "Update README", ""},
	}
	got := notes("1.2.0", "1.1.0", commits, []string{"internal/migrations/00042_paging.sql"})
	for _, want := range []string{
		"## Kogge 1.2.0\n\nThe changes since 1.1.0.\n",
		"### Upgrading\n", "- `00042_paging.sql`\n",
		"### Breaking changes\n\n- **api:** tokens expire after a year (444444444444)\n  renew tokens older than a year.\n",
		"### Security\n\n- **security:** IPv4 inside IPv6 in the network policy (222222222222)\n",
		"### Features\n\n- **issues:** bulk edit (111111111111)\n- **api:** tokens expire after a year (444444444444)\n",
		"### Fixes\n\n- **wiki:** keep drafts (333333333333)\n",
		"### Performance\n\n- index unstemmed text search (555555555555)\n",
		"### Other changes\n\n- Update README (888888888888)\n",
		"2 more commits change documentation, tests, the build or the code's structure only.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the notes lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "IPv4 inside IPv6 in the network policy (222222222222)\n\n### Features") &&
		strings.Count(got, "IPv4 inside IPv6") != 1 {
		t.Errorf("a security fix is listed twice:\n%s", got)
	}
	first := notes("1.0.0", "", commits[:1], nil)
	if !strings.Contains(first, "The first release.") || strings.Contains(first, "Upgrading") {
		t.Errorf("first release:\n%s", first)
	}
}
