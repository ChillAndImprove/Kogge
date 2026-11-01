// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"strings"
	"testing"
)

func TestOwnerPattern(t *testing.T) {
	for _, c := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		{"*", []string{"a", "a/b/c.go", ".kogge/CODEOWNERS"}, nil},
		{"*.go", []string{"main.go", "internal/x/y.go"}, []string{"main.go.txt", "go"}},
		{"/docs/", []string{"docs/a.md", "docs/x/y.md"}, []string{"docs", "web/docs/a.md"}},
		{"docs/", []string{"docs/a.md", "web/docs/a.md"}, []string{"docs", "documentation/a.md"}},
		{"docs", []string{"docs", "docs/a.md", "web/docs/b.md"}, []string{"docsy/a.md", "a/xdocs"}},
		{"/build/logs/*", []string{"build/logs/a.log", "build/logs/old/a.log"}, []string{"build/logs", "x/build/logs/a.log"}},
		{"internal/payments", []string{"internal/payments/a.go", "internal/payments"}, []string{"x/internal/payments/a.go"}},
		{"apps/**/test", []string{"apps/test/a", "apps/x/y/test/b"}, []string{"apps/testing/a", "test/a"}},
		{"**/vendor/", []string{"vendor/a", "x/vendor/a"}, []string{"vendors/a"}},
		{"/README.md", []string{"README.md"}, []string{"docs/README.md"}},
		{"Makefile?", []string{"Makefile1"}, []string{"Makefile", "Makefile/x/y1"}},
		{"/", []string{"anything/at/all"}, nil},
		{"größe/*.md", []string{"größe/a.md"}, []string{"grosse/a.md"}},
		{"a+b(c).txt", []string{"a+b(c).txt", "x/a+b(c).txt"}, []string{"aab(c).txt"}},
	} {
		re, err := ownerPattern(c.pattern)
		if err != nil {
			t.Errorf("%s: %v", c.pattern, err)
			continue
		}
		for _, p := range c.match {
			if !re.MatchString(p) {
				t.Errorf("%s does not match %s", c.pattern, p)
			}
		}
		for _, p := range c.miss {
			if re.MatchString(p) {
				t.Errorf("%s matches %s", c.pattern, p)
			}
		}
	}
	for _, bad := range []string{"!docs/", "[ab].go", `a\ b`, strings.Repeat("a", 1025)} {
		if _, err := ownerPattern(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestParseCodeOwners(t *testing.T) {
	rules, problems := parseCodeOwners([]byte("# Owners of the shop\n\n" +
		"*              @anna   # everything\n" +
		"/docs/         writers@example.com @\"Technical Writers\"\n" +
		"*.go           @ben @carol\r\n" +
		"/vendor/\n" +
		"!generated/    @dave\n" +
		"/src/          anna \"unclosed\n" +
		"/web/          @erin\n"))
	got := []string{}
	for _, r := range rules {
		got = append(got, r.pattern+"="+strings.Join(r.names, ","))
	}
	want := `*=@anna /docs/=writers@example.com,@Technical Writers *.go=@ben,@carol /vendor/= /web/=@erin`
	if strings.Join(got, " ") != want {
		t.Errorf("rules:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	if len(problems) != 2 || problems[0].Line != 7 || problems[0].Code != "exclusion" || problems[0].Values["name"] != "!generated/" ||
		!strings.Contains(problems[0].Message, "cannot leave files out") ||
		problems[1].Line != 8 || problems[1].Code != "unclosed-quote" || !strings.Contains(problems[1].Message, "quote is not closed") {
		t.Errorf("problems = %+v", problems)
	}
	_, problems = parseCodeOwners([]byte("/src/ anna\n"))
	if len(problems) != 1 || problems[0].Code != "not-an-owner" || problems[0].Values["name"] != "anna" ||
		!strings.Contains(problems[0].Message, `"anna" is not an owner`) {
		t.Errorf("a bare name: %+v", problems)
	}
	// Later lines win; a line without owners leaves its files without.
	c := &codeOwners{}
	c.rules, _ = parseCodeOwners([]byte("* @anna\n*.go @ben\n/vendor/\n"))
	for path, owner := range map[string]string{"README.md": "@anna", "main.go": "@ben", "vendor/x.go": ""} {
		r := c.ruleOf(path)
		if r == nil || strings.Join(r.names, ",") != owner {
			t.Errorf("owner of %s = %+v, want %q", path, r, owner)
		}
	}
	// Limits.
	var big strings.Builder
	for range maxOwnerRules + 5 {
		big.WriteString("*.x @anna\n")
	}
	rules, problems = parseCodeOwners([]byte(big.String()))
	if len(rules) != maxOwnerRules || len(problems) != 1 {
		t.Errorf("%d rules, problems %+v", len(rules), problems)
	}
	rules, problems = parseCodeOwners([]byte("* " + strings.Repeat("@a ", maxRuleOwners+3)))
	if len(rules[0].names) != maxRuleOwners || len(problems) != 1 {
		t.Errorf("%d owners, problems %+v", len(rules[0].names), problems)
	}
	_, problems = parseCodeOwners([]byte(strings.Repeat("# comment\n", 8000)))
	if len(problems) != 1 || problems[0].Line != 0 {
		t.Errorf("a large file: %+v", problems)
	}
}
