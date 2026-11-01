// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConditions(t *testing.T) {
	push := Env{Event: Event{Kind: EventPush, Branch: "main"}, Message: "Fix the build\n\n[deploy]",
		Variables: map[string]string{"TARGET": "prod", "EMPTY": ""}}
	tag := Env{Event: Event{Kind: EventPush, Tag: "v1.2.0"}}
	pr := Env{Event: Event{Kind: EventPullRequest, Branch: "feature/login", TargetBranch: "main"}}
	docs := Env{Event: Event{Kind: EventPush, Branch: "main", Changes: []string{"docs/guide.md", "README.md"}, ChangesKnown: true}}
	code := Env{Event: Event{Kind: EventPush, Branch: "main", Changes: []string{"internal/app/app.go"}, ChangesKnown: true}}
	none := Env{Event: Event{Kind: EventPush, Branch: "main", ChangesKnown: true}}
	for _, c := range []struct {
		cond string
		env  Env
		want bool
	}{
		{"branch == 'main'", push, true},
		{`branch == "main"`, push, true},
		{"branch != 'main'", push, false},
		{"event == 'push' && branch == 'main'", push, true},
		{"event == 'pull-request' || branch == 'main'", push, true},
		{"event == 'pull-request' || branch == 'develop'", push, false},
		{"!(branch == 'main')", push, false},
		{"!!(branch == 'main')", push, true},
		{"branch in ['develop', 'main']", push, true},
		{"branch in []", push, false},
		{"!(branch in ['develop'])", push, true},
		{"ref == 'refs/heads/main'", push, true},
		{"ref == 'refs/tags/v1.2.0' && tag == 'v1.2.0' && branch == ''", tag, true},
		{"startsWith(tag, 'v1.')", tag, true},
		{"endsWith(tag, '.0')", tag, true},
		{"contains(commit_message, '[deploy]')", push, true},
		{"contains(commit_message, '[skip]')", push, false},
		{"matches(branch, 'feature/*')", pr, true},
		{"matches(branch, 'release/**')", pr, false},
		{"source_branch == 'feature/login' && target_branch == 'main'", pr, true},
		{"source_branch == ''", push, true},
		{"vars.TARGET == 'prod'", push, true},
		{"vars.EMPTY == '' && vars.MISSING == ''", push, true},
		{"vars.TARGET in ['staging', 'prod']", push, true},
		{"true", push, true},
		{"false || branch == 'main'", push, true},
		{"true == (branch == 'main')", push, true},
		{"changed('docs/**')", docs, true},
		{"changed('docs/**', 'internal/**')", code, true},
		{"changed('docs/**')", code, false},
		{"changed('**/*.md')", docs, true},
		{"!changed('**/*.md')", code, true},
		{"changed('docs/**')", none, false},
		// Unknown changes count as changes of every file, as for paths filters.
		{"changed('docs/**')", tag, true},
		// && binds stronger than ||.
		{"branch == 'x' && false || true", push, true},
		{"branch == 'x' && (false || true)", push, false},
		{"'it\\'s' == \"it's\"", push, true},
	} {
		cond, err := ParseCondition(c.cond)
		if err != nil {
			t.Errorf("%s: %v", c.cond, err)
			continue
		}
		env := c.env
		if got := cond.Eval(&env); got != c.want {
			t.Errorf("%s = %v, want %v", c.cond, got, c.want)
		}
	}

	for cond, want := range map[string]string{
		"":                                  "the condition is empty",
		"branch":                            "must be true or false",
		"branch = 'main'":                   `"=" is not an operator`,
		"branch == 'main":                   "a text is not closed",
		"branch == 'main' &&":               "ends too early",
		"(branch == 'main'":                 "ends too early",
		"branch == 'main')":                 `unexpected ")"`,
		"brnch == 'main'":                   `"brnch" is not known`,
		"branch == true":                    "== compares a text with true or false",
		"branch && true":                    "&& needs a condition on both sides, not a text",
		"!branch":                           "! needs a condition, not a text",
		"branch in 'main'":                  "in needs a list",
		"(branch == 'a') in ['a']":          "in needs a text on its left",
		"branch in [true]":                  "the items of a list must be texts",
		"branch == ['main']":                "lists are compared with in",
		"['a']":                             "a list can only follow in",
		"lower(branch) == 'main'":           "there is no function lower",
		"startsWith(branch)":                "startsWith needs 2 arguments",
		"changed()":                         "changed needs 1 to 16 arguments",
		"startsWith(branch, true)":          "the arguments of startsWith must be texts",
		"vars == 'x'":                       "write vars.NAME",
		"vars. == 'x'":                      "write vars.NAME",
		"branch == 'a' # comment":           `unexpected '#'`,
		strings.Repeat("(", 40) + "true":    "nested too deeply",
		strings.Repeat("x", MaxCondition+1): "more than 2000 characters",
	} {
		_, err := ParseCondition(cond)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", cond, err, want)
		}
	}
	// Errors tell where in the condition they are.
	_, err := ParseCondition("branch == 'main' && brnch == 'x'")
	if ce, ok := err.(*ConditionError); !ok || ce.Offset != 20 {
		t.Errorf("offset: %#v", err)
	}
	// Conditions are stored with jobs as their text.
	c, _ := ParseCondition("branch == 'main'")
	b, _ := json.Marshal(c)
	var back Condition
	if err := json.Unmarshal(b, &back); err != nil || back.String() != "branch == 'main'" || !back.Eval(&push) {
		t.Errorf("JSON: %s %v %q", b, err, back.String())
	}
	// A stored condition that does not parse (another version's) keeps its text: jobs of runs decode all the same.
	var odd Condition
	if err := json.Unmarshal([]byte(`"future(branch)"`), &odd); err != nil || odd.String() != "future(branch)" || !odd.Eval(&push) ||
		odd.UsesChanges() {
		t.Errorf("an unknown stored condition: %v %q", err, odd.String())
	}
	if c.UsesChanges() || !mustCondition(t, "!changed('a/**')").UsesChanges() {
		t.Error("UsesChanges")
	}
}

func mustCondition(t *testing.T, src string) *Condition {
	t.Helper()
	c, err := ParseCondition(src)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestConditionPositions: mistakes in conditions are reported at their line and column in the file.
func TestConditionPositions(t *testing.T) {
	_, err := Parse([]byte(`version: 1
pipelines:
  ci:
    stages:
      - name: Test
        if: branch == 'main' && brnch == 'x'
        jobs:
          - name: unit
            if: "event == 'push' && bad"
            steps: [{run: "true"}]
          - name: lint
            if: |
              branch ==
              'x' &&
            steps: [{run: "true"}]
`))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		`line 6, column 33: if: "brnch" is not known`,
		`line 9, column 37: if: "bad" is not known`,
		// Conditions over several lines are reported at their start.
		`line 12, column 17: if: the condition ends too early`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
