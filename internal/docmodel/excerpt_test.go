// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerpt(t *testing.T) {
	cases := map[string]struct {
		doc  string
		max  int
		want string
	}{
		"blocks are clauses": {`{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"As a returning customer I pay with a saved card."}]},
			{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Acceptance criteria"}]},
			{"type":"bulletList","content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Saved cards come first"}]}]},
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"An expired card cannot be chosen"}]}]}]}]}`,
			200, "As a returning customer I pay with a saved card. Acceptance criteria · Saved cards come first · An expired card cannot be chosen"},
		"no code, table rows": {`{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"Errors are values:"}]},
			{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"if err != nil { return err }"}]},
			{"type":"table","content":[
				{"type":"tableRow","content":[
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Service"}]}]},
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Language"}]}]}]},
				{"type":"tableRow","content":[
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Storefront"}]}]},
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"TypeScript"}]}]},
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Mara"}]}]}]}]}]}`,
			200, "Errors are values: Storefront: TypeScript, Mara"},
		"cut at a word": {`{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"Can I change an order? Until it ships, you can change the address."}]}]}`,
			30, "Can I change an order? Until…"},
		"expand titles and mentions": {`{"type":"doc","content":[
			{"type":"expand","attrs":{"title":"How do I save a card?"},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"Ask "},{"type":"mention","attrs":{"user":3,"label":"Mara Lindner"}},
					{"type":"emoji","attrs":{"name":"smile"}},{"type":"text","text":" about "},{"type":"issue","attrs":{"key":"SHOP-4"}}]}]}]}`,
			200, "How do I save a card? Ask @Mara Lindner about SHOP-4"},
		"the excerpt macro wins": {`{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"Table of contents"}]},
			{"type":"macro","attrs":{"name":"excerpt"},"content":[{"type":"paragraph","content":[{"type":"text","text":"The short version."}]}]}]}`,
			200, "The short version."},
		"only code": {`{"type":"doc","content":[{"type":"codeBlock","content":[{"type":"text","text":"x := 1"}]}]}`, 200, ""},
		// A formula within text keeps its place; one of its own is left out, as code is.
		"formulas": {`{"type":"doc","content":[
			{"type":"paragraph","content":[{"type":"text","text":"The area is "},{"type":"inlineMath","attrs":{"latex":" \\pi\nr^2 "}},
				{"type":"text","text":"."},{"type":"inlineMath","attrs":{"latex":""}}]},
			{"type":"math","attrs":{"latex":"A = \\pi r^2"}},
			{"type":"table","content":[{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Circle"}]}]},
				{"type":"tableCell","content":[{"type":"math","attrs":{"latex":"\\pi r^2"}},
					{"type":"paragraph","content":[{"type":"text","text":"its area"}]}]}]}]}]}`,
			200, "The area is $\\pi r^2$. Circle: its area"},
	}
	for name, c := range cases {
		if got := Excerpt(mustParse(t, c.doc), c.max); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if Excerpt(nil, 10) != "" {
		t.Error("no document")
	}
}

func TestExcerptStopsEarly(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"doc","content":[`)
	for i := range 500 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"paragraph","content":[{"type":"text","text":"A sentence that goes on for a while."}]}`)
	}
	b.WriteString("]}")
	got := Excerpt(mustParse(t, b.String()), 100)
	if n := utf8.RuneCountInString(got); n > 101 || !strings.HasSuffix(got, Ellipsis) {
		t.Errorf("%d characters: %q", n, got)
	}
}

func TestJoinClauses(t *testing.T) {
	got := JoinClauses([]string{"Release 1.1 is out.", " ", "What comes next", "Search with \u0001suggestions\u0002", "Done!", "Note:", "x"})
	if want := "Release 1.1 is out. What comes next · Search with \u0001suggestions\u0002 · Done! Note: x"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestCut(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"rotating the key regularly", 20, "rotating the key…"},
		{"a list · of clauses · that goes on", 16, "a list · of…"},
		{"Supercalifragilisticexpialidocious", 10, "Supercalif…"},
		{"the \u0001card number\u0002 is", 12, "the \u0001card\u0002…"},
		{"Ünïcödé wörds äre cöünted äs chäräcters", 18, "Ünïcödé wörds äre…"},
	}
	for _, c := range cases {
		if got := Cut(c.in, c.max); got != c.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
