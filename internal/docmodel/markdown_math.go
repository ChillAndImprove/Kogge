// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Formulas in Markdown follow pandoc's rules, with GitHub's syntax:
//
//   - Within text, $…$ is a formula (and so is $$…$$): the opening dollar is not followed by a space, the closing one
//     is the next dollar of the line, which is not preceded by a space nor followed by a digit — "$5 and $10" stays
//     text. A backslash and the character after it are skipped, so \$ is a dollar of the formula. GitHub's $`…`$ is
//     read, too.
//   - A line of $$ (indented by at most three spaces) opens a formula of lines of its own, which a line ending in $$
//     closes (or the end of its container); a line $$…$$ alone is such a formula, and so is a fenced block of the
//     language math.
//
// Spaces are exactly space, tab, line feed and carriage return (as in the web application's highlighting). The LaTeX
// is read from the source as it is: Markdown's escapes, entities and emphasis do not apply within formulas, and no
// formula is read within code. The writer (markdown_out.go) relies on the same rules: it escapes every dollar of text
// that could open a formula.

// mathSpace reports whether a byte is a space by the rules of formulas.
func mathSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// scanInlineMath reads a formula within text at the start of s (a line): $…$ or $$…$$. It returns the formula's
// LaTeX and its end, or an end of -1 for none; run is the number of dollars at the start of s. The first dollar
// after the opening one decides: the formula ends there or there is none (no later dollar is tried, which keeps
// reading linear).
func scanInlineMath(s []byte) (latex []byte, end, run int) {
	for run < len(s) && s[run] == '$' {
		run++
	}
	if run > 2 || run >= len(s) || mathSpace(s[run]) {
		return nil, -1, run
	}
	for i := run; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\n', '\r':
			return nil, -1, run
		case '$':
			if run == 2 && (i+1 >= len(s) || s[i+1] != '$') || mathSpace(s[i-1]) || i+run < len(s) && isDigit(s[i+run]) {
				return nil, -1, run
			}
			return s[run:i], i + run, run
		}
	}
	return nil, -1, run
}

// backtickMath reads GitHub's other form of a formula within text, $`…`$: the LaTeX between the backticks, with one
// space at both ends left out as code spans do. Other LaTeX is returned as it is.
func backtickMath(latex []byte) []byte {
	n := len(latex)
	open, close := 0, 0
	for open < n && latex[open] == '`' {
		open++
	}
	for close < n-open && latex[n-1-close] == '`' {
		close++
	}
	if open == 0 || open != close || open+close >= n {
		return latex
	}
	inner := latex[open : n-close]
	if len(inner) > 2 && inner[0] == ' ' && inner[len(inner)-1] == ' ' && len(bytes.Trim(inner, " ")) > 0 {
		inner = inner[1 : len(inner)-1]
	}
	return inner
}

// displayClose returns where the first $$ of a line is that no backslash escapes, or -1.
func displayClose(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '$':
			if b[i+1] == '$' {
				return i
			}
		}
	}
	return -1
}

// closingLine returns where a line ends with $$ (after spaces are left out) that no backslash escapes: the line
// closes a formula of lines of its own. It returns -1 for other lines.
func closingLine(line []byte) int {
	line = bytes.TrimRight(line, " \t\r\n")
	n := len(line)
	if n < 2 || line[n-1] != '$' || line[n-2] != '$' {
		return -1
	}
	backslashes := 0
	for i := n - 3; i >= 0 && line[i] == '\\'; i-- {
		backslashes++
	}
	if backslashes%2 == 1 {
		return -1
	}
	return n - 2
}

// displayLatex is the LaTeX of a formula on lines of its own as it is kept and written: lines of only spaces are
// empty (Markdown reads them as blank lines), and there are none at its start and end.
func displayLatex(latex string) string {
	lines := strings.Split(newlines.Replace(latex), "\n")
	for i, l := range lines {
		if strings.Trim(l, " \t") == "" {
			lines[i] = ""
		}
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

var (
	kindMathInline = ast.NewNodeKind("MathInline")
	kindMathBlock  = ast.NewNodeKind("MathBlock")
)

// mathInline is a formula within text.
type mathInline struct {
	ast.BaseInline
	latex string
}

func (n *mathInline) Kind() ast.NodeKind { return kindMathInline }

func (n *mathInline) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Latex": n.latex}, nil)
}

// mathBlock is a formula on lines of its own.
type mathBlock struct {
	ast.BaseBlock
	indent int // of the opening line: the other lines lose as much of theirs
	closed bool
	lines  []string
}

func (n *mathBlock) Kind() ast.NodeKind { return kindMathBlock }

// IsRaw keeps Markdown out of the formula's lines.
func (n *mathBlock) IsRaw() bool { return true }

func (n *mathBlock) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Latex": n.latex()}, nil)
}

// latex is the formula's LaTeX (an unclosed formula in a list item takes the blank lines after it, which are left
// out).
func (n *mathBlock) latex() string { return displayLatex(strings.Join(n.lines, "\n")) }

type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$'} }

func (mathInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	latex, end, run := scanInlineMath(line)
	if end < 0 {
		if run < 2 {
			return nil // the dollar is text
		}
		// Dollars that open no formula are text, all of them: the second of "$$x$" opens none either.
		block.Advance(run)
		return ast.NewTextSegment(seg.WithStop(seg.Start + run))
	}
	block.Advance(end)
	if parent.Kind() == extast.KindTableCell {
		// Pipes are escaped within the cells of tables, also within formulas (GFM).
		latex = bytes.ReplaceAll(latex, []byte(`\|`), []byte("|"))
	}
	if run == 1 {
		latex = backtickMath(latex)
	}
	return &mathInline{latex: string(latex)}
}

type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || !bytes.HasPrefix(line[pos:], []byte("$$")) || pos+2 < len(line) && line[pos+2] == '$' {
		return nil, parser.NoChildren
	}
	rest := bytes.TrimRight(line[pos+2:], " \t\r\n")
	node := &mathBlock{indent: pos}
	if i := displayClose(rest); i >= 0 {
		// $$…$$ alone on its line is a formula of its own; with more after it, one within text.
		latex := bytes.Trim(rest[:i], " \t")
		if i+2 != len(rest) || len(latex) == 0 {
			return nil, parser.NoChildren
		}
		node.lines, node.closed = []string{string(latex)}, true
	} else if len(rest) > 0 {
		// Only a line of $$ opens a formula of lines: "$$5 a month" is text.
		return nil, parser.NoChildren
	}
	reader.AdvanceToEOL()
	return node, parser.NoChildren
}

func (mathBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	n := node.(*mathBlock)
	if n.closed {
		return parser.Close
	}
	line, seg := reader.PeekLine()
	// The lines lose the indentation of the opening one, as those of fenced code do.
	pos, padding := util.IndentPositionPadding(line, reader.LineOffset(), seg.Padding, n.indent)
	if pos < 0 {
		pos = max(0, util.FirstNonSpacePosition(line)) - seg.Padding
		padding = 0
	}
	s := text.NewSegmentPadding(seg.Start+pos, seg.Stop, padding)
	content := bytes.TrimRight(s.Value(reader.Source()), "\r\n")
	reader.AdvanceToEOL()
	if i := closingLine(content); i >= 0 {
		if before := bytes.TrimRight(content[:i], " \t"); len(bytes.Trim(before, " \t")) > 0 {
			n.lines = append(n.lines, string(before))
		}
		n.closed = true
		return parser.Close
	}
	n.lines = append(n.lines, string(content))
	return parser.Continue | parser.NoChildren
}

func (mathBlockParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

func (mathBlockParser) CanInterruptParagraph() bool { return true }

func (mathBlockParser) CanAcceptIndentedLine() bool { return false }

// mathExtension reads formulas (see above).
type mathExtension struct{}

func (mathExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithBlockParsers(util.Prioritized(mathBlockParser{}, 750)),
		parser.WithInlineParsers(util.Prioritized(mathInlineParser{}, 150)),
	)
}

// mathNode returns a formula on a line of its own, or nil for an empty one. LaTeX too long for a formula stays code,
// so that the document is still valid.
func mathNode(latex string) *Node {
	if strings.TrimSpace(latex) == "" {
		return nil
	}
	if len(latex) > MaxLatex {
		return codeBlock(latex, "latex", "")
	}
	return &Node{Type: "math", Attrs: map[string]any{"latex": latex}}
}

// inlineMathNode returns a formula within text, or nil for an empty one. LaTeX too long for a formula stays text.
func inlineMathNode(latex string) *Node {
	if strings.TrimSpace(latex) == "" {
		return nil
	}
	if len(latex) > MaxLatex {
		return &Node{Type: "text", Text: "$" + latex + "$"}
	}
	return &Node{Type: "inlineMath", Attrs: map[string]any{"latex": latex}}
}

// inlineMathMarkdown writes LaTeX as a formula within text, $…$: on one line without spaces at its ends (as
// FormulaLatex gives it), its dollars escaped (a bare one is no TeX, and it would end the formula), a backslash at
// its end left out (it would escape the closing dollar), and in the cells of tables its pipes escaped as well. It is
// "" for an empty formula ($$ would be two dollars).
func inlineMathMarkdown(latex string, inTable bool) string {
	latex = inlineLatex(latex)
	for trailingBackslashes(latex)%2 == 1 {
		latex = strings.TrimRight(latex[:len(latex)-1], " \t")
	}
	if strings.TrimSpace(latex) == "" {
		return ""
	}
	latex = escapeLatexDollars(latex)
	if inTable {
		latex = strings.ReplaceAll(latex, "|", `\|`)
	}
	if strings.HasPrefix(latex, "`") && strings.HasSuffix(latex, "`") {
		// $`…`$ would lose the backticks: GitHub's form keeps them, as code spans do.
		fence := strings.Repeat("`", longestRun(latex, '`')+1)
		return "$" + fence + " " + latex + " " + fence + "$"
	}
	return "$" + latex + "$"
}

func trailingBackslashes(s string) int { return len(s) - len(strings.TrimRight(s, `\`)) }

// displayMathMarkdown writes a formula on lines of its own, with its LaTeX as FormulaLatex gives it: between lines
// of $$, or, where a line of the LaTeX would end the formula there, as a fenced block of the language math.
func displayMathMarkdown(latex string) []string {
	if latex == "" {
		return nil
	}
	lines := strings.Split(latex, "\n")
	for _, l := range lines {
		if closingLine([]byte(l)) >= 0 {
			fence := strings.Repeat("`", max(3, longestRun(latex, '`')+1))
			return append(append([]string{fence + "math"}, lines...), fence)
		}
	}
	return append(append([]string{"$$"}, lines...), "$$")
}

// escapeLatexDollars puts a backslash before the dollars of LaTeX that have none; what follows a backslash stays as
// it is.
func escapeLatexDollars(latex string) string {
	if !strings.Contains(latex, "$") {
		return latex
	}
	var b strings.Builder
	for i := 0; i < len(latex); i++ {
		switch latex[i] {
		case '\\':
			b.WriteByte('\\')
			if i+1 < len(latex) {
				i++
				b.WriteByte(latex[i])
			}
			continue
		case '$':
			b.WriteByte('\\')
		}
		b.WriteByte(latex[i])
	}
	return b.String()
}
