// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package kql

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCanonical(t *testing.T) {
	cases := map[string]string{
		``:                       ``,
		`project = SHOP`:         `project = SHOP`,
		`project = "My Project"`: `project = "My Project"`,
		`status != Done and assignee = currentUser()`:        `(status != Done AND assignee = currentUser())`,
		`a = 1 OR b = 2 AND c = 3`:                           `(a = 1 OR (b = 2 AND c = 3))`,
		`(a = 1 OR b = 2) AND c = 3`:                         `((a = 1 OR b = 2) AND c = 3)`,
		`NOT a = 1 && !b = 2 || c = 3`:                       `((NOT a = 1 AND NOT b = 2) OR c = 3)`,
		`status in (Open, "In Progress", 3)`:                 `status IN (Open, "In Progress", 3)`,
		`status not in (Done)`:                               `status NOT IN (Done)`,
		`assignee is empty`:                                  `assignee IS EMPTY`,
		`assignee IS NOT null`:                               `assignee IS NOT EMPTY`,
		`assignee = EMPTY`:                                   `assignee = EMPTY`,
		`created >= -2w AND due < "2026/12/24 18:00"`:        `(created >= -2w AND due < "2026/12/24 18:00")`,
		`summary ~ "checkout*" and text !~ foo`:              `(summary ~ "checkout*" AND text !~ foo)`,
		`cf[10016] > 3`:                                      `cf[10016] > 3`,
		`"Story Points" >= 5`:                                `"Story Points" >= 5`,
		`key = SHOP-12`:                                      `key = SHOP-12`,
		`sprint in openSprints()`:                            `sprint IN openSprints()`,
		`assignee in membersOf("dev team")`:                  `assignee IN membersOf("dev team")`,
		`updated > startOfWeek("-1w")`:                       `updated > startOfWeek("-1w")`,
		`issue in linkedIssues(SHOP-1, "blocks")`:            `issue IN linkedIssues(SHOP-1, "blocks")`,
		`ORDER BY rank`:                                      `ORDER BY rank`,
		`project = SHOP order by priority desc, created ASC`: `project = SHOP ORDER BY priority DESC, created`,
		`summary ~ 'it\'s'`:                                  `summary ~ "it's"`,
		`reporter = anna@example.com`:                        `reporter = anna@example.com`,
		`status was "In Progress"`:                           `status WAS "In Progress"`,
		`status WAS NOT in (Done, Closed) BY currentUser()`:  `status WAS NOT IN (Done, Closed) BY currentUser()`,
		`assignee was empty during ("2026/01/01", -1w)`:      `assignee WAS EMPTY DURING ("2026/01/01", -1w)`,
		`status changed`:                                     `status CHANGED`,
		`status changed from Open to "In Progress" after -2w before now() by (anna, ben)`: `status CHANGED FROM Open TO "In Progress" AFTER -2w BEFORE now() BY (anna, ben)`,
		`priority changed on 2026-10-01 and status was to`:                                `(priority CHANGED ON 2026-10-01 AND status WAS to)`,
		`status = on`: `status = on`,
	}
	for in, want := range cases {
		q, err := Parse(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := q.String(); got != want {
			t.Errorf("%q:\n got %s\nwant %s", in, got, want)
		}
		// The canonical form parses to itself.
		q2, err := Parse(q.String())
		if err != nil || q2.String() != q.String() {
			t.Errorf("%q: canonical form does not round-trip: %v", in, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct {
		pos  Pos
		frag string
	}{
		`project`:                              {7, "operator"},
		`project = `:                           {10, "value"},
		`project = (`:                          {10, "value"},
		`status in Open`:                       {10, "("},
		`status in (Open`:                      {15, ", or )"},
		`status not Open`:                      {11, "IN after NOT"},
		`a = 1 b = 2`:                          {6, "unexpected"},
		`summary ~ "open`:                      {10, "not closed"},
		`(a = 1`:                               {6, ")"},
		`a = 1 ORDER rank`:                     {12, "BY"},
		`a is something`:                       {5, "EMPTY"},
		`a > EMPTY`:                            {0, "EMPTY can only"},
		`status was Open from Done`:            {16, "FROM and TO can only follow CHANGED"},
		`status changed during ("2026/01/01")`: {15, "DURING needs two dates"},
		`status changed by`:                    {17, "value"},
		`a = 1 and`:                            {9, "field name"},
		`cf[abc] = 1`:                          {0, "numeric"},
		`a ^ 1`:                                {2, "unexpected character"},
	}
	for in, want := range cases {
		_, err := Parse(in)
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%q: expected a syntax error, got %v", in, err)
			continue
		}
		if e.Pos != want.pos || !strings.Contains(e.Message, want.frag) {
			t.Errorf("%q: got %q at %d, want %q at %d", in, e.Message, e.Pos, want.frag, want.pos)
		}
	}
}

func TestTooManyClauses(t *testing.T) {
	parts := make([]string, MaxClauses+1)
	for i := range parts {
		parts[i] = "a = 1"
	}
	if _, err := Parse(strings.Join(parts, " AND ")); err == nil {
		t.Fatal("expected an error")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`project = SHOP AND status in (a, "b") ORDER BY rank`, `a ~ "x*"`, `cf[1] is not empty`, `!a=1||b!=2`,
		`status was not in (a, b) by x during (1, 2)`, `status changed from a to b after -1w`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := Parse(s)
		if err != nil {
			return
		}
		if _, err := Parse(q.String()); err != nil {
			t.Fatalf("canonical form of %q does not parse: %q: %v", s, q.String(), err)
		}
	})
}

// TestLimits: nesting, length and clauses are bounded while parsing, so that no query exhausts the stack or memory.
func TestLimits(t *testing.T) {
	nested := func(n int, open, close string) string {
		return strings.Repeat(open, n) + "a = 1" + strings.Repeat(close, n)
	}
	if _, err := Parse(nested(MaxDepth, "(", ")")); err != nil {
		t.Errorf("nesting at the limit: %v", err)
	}
	for name, q := range map[string]string{
		"parentheses":   nested(MaxDepth+1, "(", ")"),
		"not":           nested(MaxDepth+1, "NOT ", ""),
		"deep":          strings.Repeat("(", 1<<16),
		"long":          "summary ~ \"" + strings.Repeat("x", MaxLength) + "\"",
		"clauses in or": strings.Repeat("a = 1 OR ", MaxClauses) + "a = 1",
	} {
		_, err := Parse(q)
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
