// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"strings"
	"testing"

	"kogge.de/kogge/internal/docmodel"
)

func TestReplaceLine(t *testing.T) {
	for _, c := range []struct {
		name, text string
		line       int
		old        string
		lines      []string
		want       string
	}{
		{"one line", "a\nb\nc\n", 2, "b", []string{"B"}, "a\nB\nc\n"},
		{"several", "a\nb\nc\n", 2, "b", []string{"b1", "b2"}, "a\nb1\nb2\nc\n"},
		{"removed", "a\nb\nc\n", 2, "b", []string{}, "a\nc\n"},
		{"first", "a\nb\n", 1, "a", []string{"A"}, "A\nb\n"},
		{"last without newline", "a\nb", 2, "b", []string{"B", "C"}, "a\nB\nC"},
		{"windows", "a\r\nb\r\nc\r\n", 2, "b", []string{"x", "y"}, "a\r\nx\r\ny\r\nc\r\n"},
		{"windows, last", "a\r\nb", 2, "b", []string{"x", "y"}, "a\r\nx\r\ny"},
		{"indented", "func f() {\n\treturn 1\n}\n", 2, "\treturn 1", []string{"\treturn 2"}, "func f() {\n\treturn 2\n}\n"},
	} {
		got, err := replaceLine(c.text, c.line, c.old, c.lines)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for name, c := range map[string]struct {
		text string
		line int
		old  string
	}{
		"changed":   {"a\nb\n", 2, "x"},
		"too far":   {"a\nb\n", 3, ""},
		"zero":      {"a\n", 0, ""},
		"not UTF-8": {"a\xff\n", 1, "a\xff"},
	} {
		if _, err := replaceLine(c.text, c.line, c.old, []string{"y"}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSuggestionOf(t *testing.T) {
	lines, ok := suggestionOf(docmodel.FromMarkdown("Shorter:\n\n```suggestion\nreturn x\n```\n\nThanks."))
	if !ok || strings.Join(lines, "|") != "return x" {
		t.Errorf("one line: %v %v", lines, ok)
	}
	lines, ok = suggestionOf(docmodel.FromMarkdown("```suggestion\n```"))
	if !ok || len(lines) != 0 {
		t.Errorf("remove the line: %v %v", lines, ok)
	}
	lines, ok = suggestionOf(docmodel.FromMarkdown("```suggestion\na\n\nb\n```"))
	if !ok || strings.Join(lines, "|") != "a||b" {
		t.Errorf("with an empty line: %v %v", lines, ok)
	}
	if _, ok := suggestionOf(docmodel.FromMarkdown("```go\nreturn x\n```")); ok {
		t.Error("a code block of another language suggests a change")
	}
	if _, ok := suggestionOf(nil); ok {
		t.Error("no body suggests a change")
	}
}
