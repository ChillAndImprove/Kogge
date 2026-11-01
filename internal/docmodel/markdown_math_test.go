// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// outline writes blocks compactly: paragraph p[…], heading h[…], other blocks by their type; text in quotes with its
// marks in braces, formulas within text as m‹…›, formulas of their own as M‹…›, code as code‹…›, line breaks as ↵.
func outline(nodes []*Node) string {
	var parts []string
	for _, n := range nodes {
		switch n.Type {
		case "text":
			s := `"` + n.Text + `"`
			if len(n.Marks) > 0 {
				var ms []string
				for _, m := range n.Marks {
					if h := strAttr(m.Attrs, "href"); h != "" {
						ms = append(ms, m.Type+"="+h)
					} else {
						ms = append(ms, m.Type)
					}
				}
				s += "{" + strings.Join(ms, ",") + "}"
			}
			parts = append(parts, s)
		case "inlineMath":
			parts = append(parts, "m‹"+strAttr(n.Attrs, "latex")+"›")
		case "math":
			parts = append(parts, "M‹"+strAttr(n.Attrs, "latex")+"›")
		case "codeBlock":
			s := "code‹" + NodeText(n) + "›"
			if l := strAttr(n.Attrs, "language"); l != "" {
				s += "{" + l + "}"
			}
			parts = append(parts, s)
		case "inlineImage":
			parts = append(parts, "img‹"+strAttr(n.Attrs, "alt")+"›")
		case "hardBreak":
			parts = append(parts, "br")
		case "paragraph":
			parts = append(parts, "p["+outline(n.Content)+"]")
		case "heading":
			parts = append(parts, "h["+outline(n.Content)+"]")
		default:
			parts = append(parts, n.Type+"["+outline(n.Content)+"]")
		}
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "\n", "↵")
}

// readMarkdown converts Markdown and checks that the result is a valid document.
func readMarkdown(t *testing.T, src string, opts MarkdownOptions) *Doc {
	t.Helper()
	d := FromMarkdownWith(src, opts)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw, Options{}); err != nil {
		t.Errorf("%q gives an invalid document: %v\n%s", src, err, raw)
	}
	return d
}

func TestMarkdownMathRead(t *testing.T) {
	cases := []struct{ in, want string }{
		// Formulas within text.
		{`Euler: $e^{i\pi}+1=0$.`, `p["Euler: " m‹e^{i\pi}+1=0› "."]`},
		{`$x\$y$`, `p[m‹x\$y›]`},
		{`$a_1$ and $b_1$`, `p[m‹a_1› " and " m‹b_1›]`},           // no emphasis within formulas
		{`$\{x\}$ and $\,\;\!$`, `p[m‹\{x\}› " and " m‹\,\;\!›]`}, // no escapes either
		{`$a &lt; b$`, `p[m‹a &lt; b›]`},                          // nor entities
		{`$n$th`, `p[m‹n› "th"]`},
		{`$a$$b$`, `p[m‹a› m‹b›]`},
		{`a $$x$$ b`, `p["a " m‹x› " b"]`},
		{`$$x$$ is famous`, `p[m‹x› " is famous"]`},
		{`**$x$**`, `p[m‹x›]`}, // formulas take no marks
		{`**a $x$ b**`, `p["a "{bold} m‹x› " b"{bold}]`},
		{`[$x$](https://e.com)`, `p[m‹x›]`},
		{`$x$&#50;`, `p[m‹x› "2"]`},
		{`US\$$x$`, `p["US$" m‹x›]`},
		{"$`a+b`$ and $`` `x` ``$", "p[m‹a+b› \" and \" m‹`x`›]"}, // GitHub's other form
		{`$HOME/$USER`, `p[m‹HOME/› "USER"]`},                     // the rules allow it (pandoc's, too)
		{`$5 and $10`, `p["$5 and $10"]`},
		{`from $5-$10`, `p["from $5-$10"]`},
		{`$x$5`, `p["$x$5"]`},
		{`$ x$`, `p["$ x$"]`},
		{`$x $`, `p["$x $"]`},
		{`$x` + "\t$", `p["$x` + "\t" + `$"]`},
		{`\$x$`, `p["$x$"]`},
		{"`$x$`", `p["$x$"{code}]`},
		{`costs $$`, `p["costs $$"]`},
		{`$$$`, `p["$$$"]`},
		{`$$x$`, `p["$$x$"]`},
		{`$$ dollars`, `p["$$ dollars"]`},
		{"$a\nb$", `p["$a b$"]`}, // formulas end with their line
		{`[a \$b](https://e.com/c$d)`, `p["a $b"{link=https://e.com/c$d}]`},
		{`[a](https://e.com/c$d$e) $f$`, `p["a"{link=https://e.com/c$d$e} " " m‹f›]`}, // no formulas in link targets
		{`[a $b](c$d)`, `p["[a " m‹b](c› "d)"]`},                                      // formulas come first, as code spans do
		{`see https://x.com/a$b$c ok`, `p["see " "https://x.com/a$b$c"{link=https://x.com/a$b$c} " ok"]`},
		{`# Formula $x^2$`, `h["Formula " m‹x^2›]`},
		{`![$x$ chart](/c.png)`, `p[img‹$x$ chart›]`},
		// Formulas of their own.
		{"$$\n\\int_0^1 x\\,dx\n$$", `M‹\int_0^1 x\,dx›`},
		{`$$ x^2 $$`, `M‹x^2›`},
		{"$$\n\\begin{aligned}\na&=b\\\\\nc&=d\n\\end{aligned}$$", `M‹\begin{aligned}↵a&=b\\↵c&=d↵\end{aligned}›`},
		{"$$x\ny$$", `p["$$x y$$"]`}, // only a line of $$ opens a formula of lines
		{"$$5 a month\n\nmore", `p["$$5 a month"] p["more"]`},
		{"Text\n$$\nx\n$$\nmore", `p["Text"] M‹x› p["more"]`},
		{"a\n$$x$$\nb", `p["a"] M‹x› p["b"]`},
		{"$$\na\n\nb\n$$", `M‹a↵↵b›`},
		{"$$\n\n  x\n\n$$", `M‹  x›`},
		{"$$\n- a\n# b\n---\n$$", `M‹- a↵# b↵---›`},
		{"$$\na \\$$\nb\n$$", `M‹a \$$↵b›`},           // an escaped dollar does not close it
		{"$$\na \\\\$$\nafter", `M‹a \\› p["after"]`}, // a line break of TeX before the closing dollars does
		{"  $$\n  a\n   b\n  $$", `M‹a↵ b›`},          // the opening line's indentation is left out
		{"$$\n$$", ``},
		{"$$\nx", `M‹x›`}, // an unclosed formula runs to the end
		{"```\n$x$\n```", `code‹$x$›`},
		{"    $$x$$", `code‹$$x$$›`},
		{"```math\n\\frac12\n$$\n```", `M‹\frac12↵$$›`},
		{"~~~math\nx\n~~~", `M‹x›`},
		// In containers.
		{"> $$\n> x\n>\n> y\n> $$", `blockquote[M‹x↵↵y›]`},
		{"> $$\n> x\n\nafter $y$", `blockquote[M‹x›] p["after " m‹y›]`},
		{"- $$\n  x\n\n  y\n  $$\n- z", `bulletList[listItem[M‹x↵↵y›] listItem[p["z"]]]`},
		{"- x\n\n  $$\n  a\n\n  b\n  $$", `bulletList[listItem[p["x"] M‹a↵↵b›]]`},
		{"- $$\n  x\n\nnot in the list", `bulletList[listItem[M‹x›]] p["not in the list"]`},
		{"1. $$\n   \\frac12\n   $$\n2. y", `orderedList[listItem[M‹\frac12›] listItem[p["y"]]]`},
		{"> - $$\n>   x\n>   $$", `blockquote[bulletList[listItem[M‹x›]]]`},
		// In tables, pipes are escaped.
		{"| A |\n| --- |\n| $\\|x\\|$ |", `table[tableRow[tableHeader[p["A"]]] tableRow[tableCell[p[m‹|x|›]]]]`},
		{"| A |\n| --- |\n| $\\\\|x\\\\|$ |", `table[tableRow[tableHeader[p["A"]]] tableRow[tableCell[p[m‹\|x\|›]]]]`},
		{"| $a$ | b |\n| --- | --- |\n| $$x$$ | 1 |",
			`table[tableRow[tableHeader[p[m‹a›]] tableHeader[p["b"]]] tableRow[tableCell[p[m‹x›]] tableCell[p["1"]]]]`},
	}
	for _, c := range cases {
		if got := outline(readMarkdown(t, c.in, MarkdownOptions{}).Content); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.in, got, c.want)
		}
	}

	// Markdown of systems without formulas keeps its dollars as text, and math as code.
	src := "$x$ and $$\ny\n$$\n\n```math\nz\n```"
	if got := outline(readMarkdown(t, src, MarkdownOptions{NoMath: true}).Content); got != `p["$x$ and $$ y $$"] code‹z›{math}` {
		t.Errorf("without formulas: %s", got)
	}

	// LaTeX too long for a formula stays code, or text; the document is valid.
	long := strings.Repeat("x", MaxLatex+1)
	if got := outline(readMarkdown(t, "$$\n"+long+"\n$$\n\n$"+long+"$", MarkdownOptions{}).Content); got != "code‹"+long+"›{latex} p[\"$"+long+"$\"]" {
		t.Errorf("too long: %.80s", got)
	}
}

// doc builds a document of blocks given as JSON.
func doc(t *testing.T, blocks ...string) *Doc {
	t.Helper()
	return mustParse(t, `{"type":"doc","content":[`+strings.Join(blocks, ",")+`]}`)
}

// para is a paragraph of inline nodes given as JSON.
func para(nodes ...string) string {
	return `{"type":"paragraph","content":[` + strings.Join(nodes, ",") + `]}`
}

func txt(s string, marks ...string) string {
	j, _ := json.Marshal(s)
	if len(marks) == 0 {
		return `{"type":"text","text":` + string(j) + `}`
	}
	var ms []string
	for _, m := range marks {
		ms = append(ms, `{"type":"`+m+`"}`)
	}
	return `{"type":"text","text":` + string(j) + `,"marks":[` + strings.Join(ms, ",") + `]}`
}

func formula(typ, latex string) string {
	j, _ := json.Marshal(latex)
	return `{"type":"` + typ + `","attrs":{"latex":` + string(j) + `}}`
}

func im(latex string) string { return formula("inlineMath", latex) }
func dm(latex string) string { return formula("math", latex) }

// mathWrites are documents and their Markdown; exact ones read back as they were.
var mathWrites = []struct {
	name   string
	blocks []string
	md     string
	exact  bool
}{
	{"inline", []string{para(txt("Euler: "), im(`e^{i\pi}+1=0`), txt("."))}, "Euler: $e^{i\\pi}+1=0$.\n", true},
	{"display", []string{dm(`\int_0^1 x\,dx`)}, "$$\n\\int_0^1 x\\,dx\n$$\n", true},
	{"display with blank lines", []string{dm("\\begin{x}\n  a\n\n  b\n\\end{x}")}, "$$\n\\begin{x}\n  a\n\n  b\n\\end{x}\n$$\n", true},
	{"prices", []string{para(txt("Preis: $5 und 5 $ pro Monat"))}, "Preis: \\$5 und 5 $ pro Monat\n", true},
	{"ranges", []string{para(txt("$5 and $10"))}, "\\$5 and \\$10\n", true},
	{"dollar at the end", []string{para(txt("Preis in $"))}, "Preis in \\$\n", true},
	{"dollars like a formula", []string{para(txt("$x$"))}, "\\$x\\$\n", true},
	{"dollars around a failed opener", []string{para(txt("$a $b$"))}, "\\$a \\$b\\$\n", true},
	{"escaped dollar in text", []string{para(txt(`\$`))}, "\\\\\\$\n", true},
	{"text dollar before a formula", []string{para(txt("US$"), im("x"))}, "US\\$$x$\n", true},
	{"digit after a formula", []string{para(im("x"), txt("2 times"))}, "$x$&#50; times\n", true},
	{"date after a formula", []string{para(im("x"), `{"type":"date","attrs":{"value":"2026-11-02"}}`)}, "$x$&#50;026-11-02\n", false},
	{"two formulas", []string{para(im("a"), im("b"))}, "$a$$b$\n", true},
	{"marks close before a formula", []string{para(txt("a"), txt("b", "bold"), im("x"), txt("c", "bold"))}, "a**b**$x$**c**\n", true},
	{"marks and spaces", []string{para(txt("a ", "bold"), im("x"), txt(" b", "bold"))}, "**a** $x$ **b**\n", false},
	{"heading", []string{`{"type":"heading","attrs":{"level":2},"content":[` + txt("Formula ") + "," + im("x^2") + `]}`}, "## Formula $x^2$\n", true},
	{"list", []string{`{"type":"bulletList","content":[{"type":"listItem","content":[` + para(txt("x")) + "," + dm("a\n\nb") + `]}]}`},
		"- x\n\n  $$\n  a\n\n  b\n  $$\n", true},
	{"quote", []string{`{"type":"blockquote","content":[` + dm("x") + `]}`}, "> $$\n> x\n> $$\n", true},
	{"line that would close", []string{dm("a\n$$\nb")}, "```math\na\n$$\nb\n```\n", true},
	{"line break before dollars", []string{dm(`x \\$$`)}, "```math\nx \\\\$$\n```\n", true},
	{"escaped dollar at a line's end", []string{dm(`a \$$`)}, "$$\na \\$$\n$$\n", true},
	{"fence longer than backticks", []string{dm("```\n$$")}, "````math\n```\n$$\n````\n", true},
	{"table", []string{`{"type":"table","content":[{"type":"tableRow","content":[` +
		`{"type":"tableCell","content":[` + para(im("|x|")) + `]},` +
		`{"type":"tableCell","content":[` + para(im(`\|x\|`)) + `]},` +
		`{"type":"tableCell","content":[` + dm("a\nb") + `]}]}]}`},
		"| $\\|x\\|$ | $\\\\|x\\\\|$ | $a b$ |\n| --- | --- | --- |\n", false},
	// Normalised: spaces at the ends, bare dollars, line breaks and a backslash at the end.
	{"spaces", []string{para(im(" x "))}, "$x$\n", false},
	{"bare dollar", []string{para(im("a$b"))}, "$a\\$b$\n", false},
	{"line break", []string{para(im("a\nb"))}, "$a b$\n", false},
	{"backslash at the end", []string{para(im(`x\`))}, "$x$\n", false},
	{"line break of TeX at the end", []string{para(im(`a \\`))}, "$a \\\\$\n", true},
	{"blank lines around", []string{dm("\n  \nx\n\n")}, "$$\nx\n$$\n", false},
	{"lines of spaces", []string{dm("a\n \t\nb")}, "$$\na\n\nb\n$$\n", false},
	{"backticks at both ends", []string{para(im("`x`"))}, "$`` `x` ``$\n", true},
	{"empty", []string{para(txt("a"), im(" ")), dm("")}, "a\n", false},
	{"hard break", []string{para(txt("a"), `{"type":"hardBreak"}`, txt("$$"), im("x"))}, "a\\\n\\$\\$$x$\n", true},
}

func TestMarkdownMathWrite(t *testing.T) {
	for _, c := range mathWrites {
		d := doc(t, c.blocks...)
		got := Markdown(d)
		if got != c.md {
			t.Errorf("%s: %q, want %q", c.name, got, c.md)
			continue
		}
		back := readMarkdown(t, got, MarkdownOptions{})
		if c.exact {
			want, _ := json.Marshal(d)
			if raw, _ := json.Marshal(back); string(raw) != string(want) {
				t.Errorf("%s: %q reads back as\n%s\nwant\n%s", c.name, got, raw, want)
			}
		}
	}
}

func TestMarkdownMathRoundTrip(t *testing.T) {
	for _, src := range []string{
		"Euler: $e^{i\\pi}+1=0$.\n\n$$\n\\int_0^1 x\\,dx\n$$\n\nPreis: \\$5 und 5 $ pro Monat\n",
		"- x\n\n  $$\n  a\n\n  b\n  $$\n",
		"> $$\n> x\n> $$\n",
		"$a$$b$ and $x$&#50;\n",
		"| A | B |\n| --- | --- |\n| $\\|x\\|$ | $\\\\|x\\\\|$ |\n",
		"```math\na\n$$\nb\n```\n",
		"**a** $x$ **b**\n",
		"## Formula $x^2$\n",
	} {
		if got := Markdown(readMarkdown(t, src, MarkdownOptions{})); got != src {
			t.Errorf("%q comes back as %q", src, got)
		}
	}

	// Text with dollars stays text, however its dollars stand: in paragraphs, headings and cells, and next to formulas.
	for _, s := range []string{"$a $b$", "$$x$", "a$ b $c", `\$`, `$\`, "5$", "$5", "$ $", "$$$$", "x$$y", "$`x`$", "$x$5",
		"$a|b$", "price: $5, or $6", "$x$\u00a0y", "a $$ b $$ c", "$1 + $2 = $3", "1. $x$"} {
		for _, d := range []*Doc{
			doc(t, para(txt(s))),
			doc(t, `{"type":"heading","attrs":{"level":1},"content":[`+txt(s)+`]}`),
			doc(t, `{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableHeader","content":[`+para(txt("A"))+`]}]},`+
				`{"type":"tableRow","content":[{"type":"tableCell","content":[`+para(txt(s))+`]}]}]}`),
			doc(t, para(im("x"), txt(s))),
			doc(t, para(txt(s), im("x"))),
		} {
			md := Markdown(d)
			want, _ := json.Marshal(d)
			if raw, _ := json.Marshal(readMarkdown(t, md, MarkdownOptions{})); string(raw) != string(want) {
				t.Errorf("%q, written as %q, reads back as\n%s\nwant\n%s", s, md, raw, want)
			}
		}
	}
}

// formulas lists the formulas of nodes in document order: m‹…› within text, M‹…› of their own.
func formulas(nodes []*Node) []string {
	found := []string{}
	Walk(&Doc{Content: nodes}, func(n *Node) bool {
		switch n.Type {
		case "inlineMath":
			found = append(found, "m‹"+strAttr(n.Attrs, "latex")+"›")
		case "math":
			found = append(found, "M‹"+strAttr(n.Attrs, "latex")+"›")
		}
		return true
	})
	return found
}

// TestMarkdownMathCorpus reads the Markdown of testdata/markdown_math.json, which the web application's
// highlighting reads as well (markdownMath.test.ts): both find the formulas it lists, the server also with CR LF at
// the ends of lines. What the writer makes of the documents reads back as the same formulas.
func TestMarkdownMathCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/markdown_math.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Note     string              `json:"note"`
		Markdown string              `json:"markdown"`
		Formulas []map[string]string `json:"formulas"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		want := []string{}
		for _, f := range c.Formulas {
			if latex, ok := f["display"]; ok {
				want = append(want, "M‹"+latex+"›")
			} else {
				want = append(want, "m‹"+f["inline"]+"›")
			}
		}
		d := readMarkdown(t, c.Markdown, MarkdownOptions{})
		if got := formulas(d.Content); !slices.Equal(got, want) {
			t.Errorf("%s: %q has %q, want %q", c.Note, c.Markdown, got, want)
			continue
		}
		// Files in repositories may end their lines with CR LF.
		crlf := strings.ReplaceAll(c.Markdown, "\n", "\r\n")
		if got := formulas(readMarkdown(t, crlf, MarkdownOptions{}).Content); !slices.Equal(got, want) {
			t.Errorf("%s: %q has %q, want %q", c.Note, crlf, got, want)
		}
		md := Markdown(d)
		if got := formulas(readMarkdown(t, md, MarkdownOptions{}).Content); !slices.Equal(got, want) {
			t.Errorf("%s: %q, written as %q, has %q, want %q", c.Note, c.Markdown, md, got, want)
		}
	}
}

// TestMarkdownMathLinear: reading stays linear in the length of the text, also where dollars open nothing.
func TestMarkdownMathLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("large inputs")
	}
	for name, src := range map[string]string{
		"openers without closers": strings.Repeat("$a ", 350000),
		"amounts":                 strings.Repeat("$5-$1 ", 170000),
		"runs of dollars":         strings.Repeat("$$x ", 250000),
		"escaped dollars":         "$" + strings.Repeat(`a\$`, 330000),
		"display openers":         strings.Repeat("$$ a\n", 200000),
		"unclosed display":        "$$\n" + strings.Repeat("a\n", 500000),
		"many displays":           strings.Repeat("$$\nx\n$$\n", 100000),
		"backticks":               strings.Repeat("$`", 500000),
		"lines without formulas":  strings.Repeat("x a\n", 250000), // many text nodes to join
	} {
		start := time.Now()
		FromMarkdown(src)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: %v", name, d)
		}
	}
}
