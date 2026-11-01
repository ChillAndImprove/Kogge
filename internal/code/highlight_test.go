// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"strings"
	"testing"
)

// TestHighlightSnippet: snippets are split into lines of tokens with chroma's classes; unknown languages and
// oversized text stay plain, and empty lines are kept.
func TestHighlightSnippet(t *testing.T) {
	lines, lang := HighlightSnippet("go", "func main() {\n\n\treturn \"<b>\"\n}\n")
	if lang != "Go" || len(lines) != 4 {
		t.Fatalf("go: %q, %d lines: %+v", lang, len(lines), lines)
	}
	if lines[0][0] != (Token{Class: "kd", Text: "func"}) {
		t.Errorf("first token = %+v", lines[0][0])
	}
	if len(lines[1]) != 0 {
		t.Errorf("empty line = %+v", lines[1])
	}
	var text strings.Builder
	for _, tok := range lines[2] {
		text.WriteString(tok.Text)
	}
	if text.String() != "\treturn \"<b>\"" {
		t.Errorf("line 3 = %q: tokens carry the text unescaped", text.String())
	}

	// Aliases name languages.
	if _, lang := HighlightSnippet("ts", "const a = 1"); lang != "TypeScript" {
		t.Errorf("ts = %q", lang)
	}
	if _, lang := HighlightSnippet("bash", "echo hi"); lang != "Bash" {
		t.Errorf("bash = %q", lang)
	}

	for name, input := range map[string][2]string{
		"unknown language": {"no-such-language", "a\nb"},
		"no language":      {"", "a\nb"},
		"too long":         {"go", strings.Repeat("x", maxSnippet+1) + "\nb"},
	} {
		lines, lang := HighlightSnippet(input[0], input[1])
		if lang != "" || len(lines) != 2 || lines[1][0] != (Token{Text: "b"}) || lines[0][0].Class != "" {
			t.Errorf("%s: %q %+v", name, lang, lines[1])
		}
	}

	// The HTML of files is unchanged by the tokens.
	html, _ := highlight("main.go", "func main() {}")
	if len(html) != 1 || !strings.HasPrefix(html[0], `<span class="kd">func</span> <span class="nf">main</span>`) {
		t.Errorf("html = %q", html)
	}
}
