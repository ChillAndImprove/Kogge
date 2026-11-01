// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package search

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in, text, prefix string
		words            []string
	}{
		{"  Deploy   the  conf ", "Deploy the conf", "deploy:* & the:* & conf:*", []string{"deploy", "the", "conf"}},
		{"Über-Größe", "Über-Größe", "über:* & größe:*", []string{"über", "größe"}},
		{"fix a bug", "fix a bug", "fix:* & bug:*", []string{"fix", "a", "bug"}},
		{"a b", "a b", "", []string{"a", "b"}},
		{"KOG-12", "KOG-12", "kog:* & 12:*", []string{"kog", "12"}},
		{"it's:*&|!()<>", "it's:*&|!()<>", "it:*", []string{"it", "s"}},
		{"login login", "login login", "login:*", []string{"login"}},
		{"", "", "", nil},
		{"!!!", "!!!", "", nil},
	}
	for _, c := range cases {
		q := Parse(c.in)
		if q.Text != c.text || q.Prefix() != c.prefix || !slices.Equal(q.Words, c.words) {
			t.Errorf("Parse(%q) = %q %q %q, want %q %q %q", c.in, q.Text, q.Words, q.Prefix(), c.text, c.words, c.prefix)
		}
	}
	long := Parse(strings.Repeat("ab ", 200))
	if n := len([]rune(long.Text)); n > MaxLength {
		t.Errorf("a long text keeps %d characters", n)
	}
	if len(long.Words) != 1 {
		t.Errorf("repeated words: %q", long.Words)
	}
	many := Parse("one two three four five six seven eight nine ten")
	if len(many.Words) != maxWords {
		t.Errorf("%d words", len(many.Words))
	}
}

func TestShortWords(t *testing.T) {
	for text, want := range map[string]bool{"sch": true, "de ab": true, "a de": true, "a": false, "dep": true, "depl": false,
		"de deploy": false, "über": false, "üb": true, "": false} {
		if got := Parse(text).ShortWords(); got != want {
			t.Errorf("ShortWords(%q) = %v", text, got)
		}
	}
}

func TestPatterns(t *testing.T) {
	q := Parse("100%_Done\\")
	if q.StartsWith() != `100\%\_done\\%` || q.Contains() != `%100\%\_done\\%` {
		t.Errorf("patterns: %q %q", q.StartsWith(), q.Contains())
	}
}

func TestTitleTier(t *testing.T) {
	q := Parse("Login page")
	for title, want := range map[string]int{"login  PAGE": TierExact, "Login page broken": TierPrefix, "The login page": TierWords} {
		if got := TitleTier(q, title); got != want {
			t.Errorf("TitleTier(%q) = %d, want %d", title, got, want)
		}
	}
}

func TestSort(t *testing.T) {
	old, recent := time.Now().Add(-time.Hour), time.Now()
	hits := []Hit{
		{ID: 1, Title: "words, old", Tier: TierWords, Updated: &old},
		{ID: 2, Title: "words, recent", Tier: TierWords, Updated: &recent},
		{ID: 3, Title: "archived exact", Tier: TierExact, Archived: true},
		{ID: 4, Title: "exact", Tier: TierExact},
		{ID: 5, Title: "words, better", Tier: TierWords, Score: 0.5, Updated: &old},
		{ID: 6, Title: "prefix", Tier: TierPrefix},
	}
	Sort(hits)
	var got []int64
	for _, h := range hits {
		got = append(got, h.ID)
	}
	if want := []int64{4, 3, 6, 5, 2, 1}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestSplit(t *testing.T) {
	cases := []struct{ n, offset, limit, from, to, textOffset, textLimit int }{
		{n: 7, offset: 0, limit: 5, from: 0, to: 5, textOffset: 0, textLimit: 0},
		{n: 7, offset: 5, limit: 5, from: 5, to: 7, textOffset: 0, textLimit: 3},
		{n: 7, offset: 10, limit: 5, from: 7, to: 7, textOffset: 3, textLimit: 5},
		{n: 0, offset: 0, limit: 25, from: 0, to: 0, textOffset: 0, textLimit: 25},
	}
	for _, c := range cases {
		from, to, textOffset, textLimit := Split(c.n, c.offset, c.limit)
		if from != c.from || to != c.to || textOffset != c.textOffset || textLimit != c.textLimit {
			t.Errorf("Split(%d, %d, %d) = %d %d %d %d", c.n, c.offset, c.limit, from, to, textOffset, textLimit)
		}
	}
}

func TestLimitOr(t *testing.T) {
	if (Request{}).LimitOr(5, 20) != 5 || (Request{Limit: 50}).LimitOr(5, 20) != 20 || (Request{Limit: 7}).LimitOr(5, 20) != 7 {
		t.Error("LimitOr")
	}
}

func TestHeadline(t *testing.T) {
	m := func(s string) string { return "\u0001" + s + "\u0002" }
	story := "As a returning customer I pay with a card I saved before, without typing it again.\nAcceptance criteria\n" +
		"The checkout offers saved cards first\nAn expired card is shown but cannot be chosen.\nfoo bar card number."
	tokens := "Tokens at rest are encrypted with the key from the vault; rotating the key re-encrypts them in the background."
	long := strings.Repeat("Filler words that do not match. ", 200) + "The rotating key is here. And more text follows."
	cases := []struct{ name, headline, text, want string }{
		{"the beginning left out, the end there (its full stop left out by ts_headline)",
			"returning customer I pay with a " + m("card") + " I saved before, without typing it again.\nAcceptance criteria\n" +
				"The checkout offers saved " + m("cards") + " first\nAn expired … foo bar " + m("card") + " number",
			story,
			"…returning customer I pay with a " + m("card") + " I saved before, without typing it again. Acceptance criteria · " +
				"The checkout offers saved " + m("cards") + " first · An expired … foo bar " + m("card") + " number"},
		{"the beginning there, the end not", "As a returning " + m("customer") + " I pay with a card", story,
			"As a returning " + m("customer") + " I pay with a card…"},
		{"the whole text", story, story, "As a returning customer I pay with a card I saved before, without typing it again. " +
			"Acceptance criteria · The checkout offers saved cards first · An expired card is shown but cannot be chosen. foo bar card number."},
		{"a hyphenated word cut at the end", "Tokens at rest are encrypted with the " + m("key") + " from the vault; rotating the " + m("key") + " re",
			tokens, "Tokens at rest are encrypted with the " + m("key") + " from the vault; rotating the " + m("key") + " re-encrypts…"},
		{"a hyphenated word cut at the beginning", "encrypts them in the " + m("background"), tokens,
			"…re-encrypts them in the " + m("background")},
		{"punctuation before the beginning", "Card” is a " + m("word"), "“Card” is a word", "Card” is a " + m("word")},
		{"beyond the beginning that was read", "The rotating " + m("key") + " is here", long,
			"…The rotating " + m("key") + " is here…"},
		{"nothing", "", story, ""},
	}
	for _, c := range cases {
		head := c.text
		if r := []rune(c.text); len(r) > headLength {
			head = string(r[:headLength])
		}
		tail := c.text
		if r := []rune(c.text); len(r) > tailLength {
			tail = string(r[len(r)-tailLength:])
		}
		if got := Headline(c.headline, head, tail); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if got := HeadlineEnds("body_text"); got != "left(body_text, 4000), right(body_text, 80)" {
		t.Errorf("ends: %s", got)
	}
}
