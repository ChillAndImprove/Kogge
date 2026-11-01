// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Markdown output (the API and exports): CommonMark with GitHub's tables, task lists, strikethrough, alerts and
// formulas ($…$ within text, lines of $$ around formulas of their own, see markdown_math.go). What Markdown cannot
// express takes its closest form: panels become alerts, expandable sections quotes with their title, columns their
// content one after another (marked by HTML comments), underline, subscript, superscript and highlights HTML
// elements. Text is escaped where Markdown would read it as markup, its dollars where they could open a formula.

// MarkdownLinks rewrites where Markdown output points (exports link pages and attachments relatively); nil
// functions keep links and images as they are.
type MarkdownLinks struct {
	// Href returns the target of a link; "" keeps the text without the link.
	Href func(href string) string
	// Image returns the source of an image, given its attachment (0 for none) and src; "" writes its description
	// instead.
	Image func(attachment int64, src string) string
	// User returns the name of a mentioned user; nil writes the label of the mention.
	User func(id int64, label string) string
}

// Markdown renders a document as Markdown.
func Markdown(d *Doc) string { return MarkdownWith(d, MarkdownLinks{}) }

// MarkdownWith renders a document as Markdown, with links and images rewritten.
func MarkdownWith(d *Doc, l MarkdownLinks) string {
	if d == nil {
		return ""
	}
	m := &mdWriter{links: l}
	return strings.TrimRight(strings.Join(m.blocks(d.Content), "\n"), "\n") + "\n"
}

type mdWriter struct {
	links MarkdownLinks
}

// blocks renders blocks as lines, separated by blank lines; blocks without output are left out.
func (m *mdWriter) blocks(nodes []*Node) []string {
	var out []string
	for _, n := range nodes {
		lines := m.block(n)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

// prefixed puts a prefix before every line (the first line may have another one, as list items have); blank
// lines get the prefix without its trailing spaces.
func prefixed(lines []string, first, rest string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		p := rest
		if i == 0 {
			p = first
		}
		if l == "" {
			out[i] = strings.TrimRight(p, " ")
		} else {
			out[i] = p + l
		}
	}
	return out
}

// Alerts of GitHub's Markdown for the kinds of panels.
var panelAlerts = map[string]string{"info": "NOTE", "note": "IMPORTANT", "success": "TIP", "warning": "WARNING", "error": "CAUTION"}

func (m *mdWriter) block(n *Node) []string {
	switch n.Type {
	case "paragraph":
		if t := m.inline(n.Content, false); t != "" {
			return strings.Split(t, "\n")
		}
		return nil
	case "heading":
		lvl := int(intAttr(n, "level"))
		if lvl < 1 || lvl > 6 {
			lvl = 2
		}
		t := strings.ReplaceAll(m.inline(n.Content, false), "\\\n", " ")
		if t == "" {
			return nil
		}
		return []string{strings.Repeat("#", lvl) + " " + t}
	case "blockquote":
		return prefixed(m.blocks(n.Content), "> ", "> ")
	case "panel":
		kind := panelAlerts[strAttr(n.Attrs, "kind")]
		if kind == "" {
			kind = "NOTE"
		}
		lines := append([]string{"[!" + kind + "]"}, m.blocks(n.Content)...)
		return prefixed(lines, "> ", "> ")
	case "expand":
		var lines []string
		if t := strings.TrimSpace(strAttr(n.Attrs, "title")); t != "" {
			lines = append(lines, "**"+escapeMarkdown(t, true, false)+"**", "")
		}
		return prefixed(append(lines, m.blocks(n.Content)...), "> ", "> ")
	case "horizontalRule":
		return []string{"---"}
	case "bulletList", "orderedList", "taskList":
		return m.list(n)
	case "codeBlock":
		var text strings.Builder
		for _, c := range n.Content {
			text.WriteString(c.Text)
		}
		fence := strings.Repeat("`", max(3, longestRun(text.String(), '`')+1))
		lines := []string{fence + strings.TrimSpace(strAttr(n.Attrs, "language"))}
		if text.Len() > 0 {
			lines = append(lines, strings.Split(text.String(), "\n")...)
		}
		return append(lines, fence)
	case "table":
		return m.table(n)
	case "image":
		alt := strAttr(n.Attrs, "alt")
		if src := m.imageSource(n); src != "" {
			return []string{"![" + escapeMarkdown(alt, false, false) + "](" + markdownTarget(src) + ")"}
		}
		if alt != "" {
			return []string{escapeMarkdown(alt, true, false)}
		}
		return nil
	case "columns":
		layout := strAttr(n.Attrs, "layout")
		if layout == "" {
			layout = "equal"
		}
		lines := []string{"<!-- columns: " + layout + " -->"}
		for _, c := range n.Content {
			lines = append(lines, "", "<!-- column -->")
			if inner := m.blocks(c.Content); len(inner) > 0 {
				lines = append(lines, "")
				lines = append(lines, inner...)
			}
		}
		return append(lines, "", "<!-- /columns -->")
	case "unsupported":
		return []string{escapeMarkdown("["+strAttr(n.Attrs, "source")+" "+strAttr(n.Attrs, "name")+"]", true, false)}
	case "math":
		return displayMathMarkdown(FormulaLatex(n))
	default:
		// Macros show their body (an excerpt); exports put the output of macros in their place beforehand.
		return m.blocks(n.Content)
	}
}

func (m *mdWriter) list(n *Node) []string {
	start := int(intAttr(n, "start"))
	if start < 1 {
		start = 1
	}
	// A list is loose (blank lines between its items) when an item holds several blocks other than nested lists.
	loose := false
	for _, item := range n.Content {
		blocks := 0
		for _, c := range item.Content {
			if c.Type != "bulletList" && c.Type != "orderedList" && c.Type != "taskList" {
				blocks++
			}
		}
		loose = loose || blocks > 1
	}
	var out []string
	for i, item := range n.Content {
		marker := "- "
		switch {
		case n.Type == "orderedList":
			marker = strconv.Itoa(start+i) + ". "
		case item.Type == "taskItem":
			if c, _ := item.Attrs["checked"].(bool); c {
				marker = "- [x] "
			} else {
				marker = "- [ ] "
			}
		}
		indent := strings.Repeat(" ", len(marker))
		if item.Type == "taskItem" {
			indent = "  "
		}
		var lines []string
		for j, c := range item.Content {
			inner := m.block(c)
			if len(inner) == 0 {
				continue
			}
			nested := c.Type == "bulletList" || c.Type == "orderedList" || c.Type == "taskList"
			if j > 0 && len(lines) > 0 && !nested {
				lines = append(lines, "")
			}
			lines = append(lines, inner...)
		}
		if len(lines) == 0 {
			lines = []string{""}
		}
		if loose && i > 0 {
			out = append(out, "")
		}
		out = append(out, prefixed(lines, marker, indent)...)
		if lines[0] == "" {
			out[len(out)-len(lines)] = strings.TrimRight(marker, " ")
		}
	}
	return out
}

func (m *mdWriter) table(n *Node) []string {
	cols := 0
	rows := make([][]string, 0, len(n.Content))
	for _, row := range n.Content {
		var cells []string
		for _, cell := range row.Content {
			var parts []string
			for _, c := range cell.Content {
				var t string
				if c.Type == "paragraph" || c.Type == "heading" {
					t = m.inline(c.Content, true)
				} else if c.Type == "math" {
					// Cells hold no blocks: a formula of its own becomes one within the cell's text.
					t = inlineMathMarkdown(strAttr(c.Attrs, "latex"), true)
				} else {
					t = escapeMarkdown(PlainText(&Doc{Content: []*Node{c}}), false, true)
				}
				if t = strings.TrimSpace(t); t != "" {
					parts = append(parts, strings.ReplaceAll(t, "\n", "<br>"))
				}
			}
			cells = append(cells, strings.Join(parts, "<br>"))
			// Markdown has no merged cells: the cells a merged cell spans stay empty.
			for k := int64(1); k < intAttr(cell, "colspan"); k++ {
				cells = append(cells, "")
			}
		}
		cols = max(cols, len(cells))
		rows = append(rows, cells)
	}
	if cols == 0 {
		return nil
	}
	line := func(cells []string) string {
		for len(cells) < cols {
			cells = append(cells, "")
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	out := []string{line(rows[0]), "|" + strings.Repeat(" --- |", cols)}
	for _, r := range rows[1:] {
		out = append(out, line(r))
	}
	return out
}

func (m *mdWriter) imageSource(n *Node) string {
	id, src := intAttr(n, "attachment"), strAttr(n.Attrs, "src")
	if m.links.Image != nil {
		return m.links.Image(id, src)
	}
	return src
}

func (m *mdWriter) href(target string) string {
	if !SafeLink(target) {
		return ""
	}
	if m.links.Href != nil {
		return m.links.Href(target)
	}
	return target
}

// A segment of inline content: text with its marks, or Markdown already (images, code spans, line breaks,
// formulas).
type mdSegment struct {
	text    string
	marks   []*Mark
	raw     bool
	formula bool
}

// Marks as Markdown writes them, outermost first; others (colours, comment anchors) are not written.
var mdMarkOrder = map[string]int{"link": 0, "bold": 1, "italic": 2, "strike": 3, "underline": 4, "subscript": 5,
	"superscript": 6, "highlight": 7}

func (m *mdWriter) segments(nodes []*Node, inTable bool) []mdSegment {
	var out []mdSegment
	for _, c := range nodes {
		var marks []*Mark
		for _, mk := range c.Marks {
			if _, ok := mdMarkOrder[mk.Type]; !ok {
				continue
			}
			if mk.Type == "link" {
				t := m.href(strAttr(mk.Attrs, "href"))
				if t == "" {
					continue
				}
				mk = &Mark{Type: "link", Attrs: map[string]any{"href": t}}
			}
			marks = append(marks, mk)
		}
		sortMarks(marks)
		switch c.Type {
		case "text":
			if hasMark(c.Marks, "code") {
				out = append(out, mdSegment{text: codeSpan(c.Text), marks: marks, raw: true})
			} else {
				out = append(out, mdSegment{text: c.Text, marks: marks})
			}
		case "hardBreak":
			// Marks end before a line break, so that a break at the end of a paragraph can be left out.
			br := "\\\n"
			if inTable {
				br = "<br>"
			}
			out = append(out, mdSegment{text: br, raw: true})
		case "mention":
			label := strAttr(c.Attrs, "label")
			if m.links.User != nil {
				label = m.links.User(intAttr(c, "user"), label)
			}
			out = append(out, mdSegment{text: "@" + label, marks: marks})
		case "issue":
			key := strAttr(c.Attrs, "key")
			if t := m.href("/browse/" + key); t != "" && m.links.Href != nil {
				marks = append([]*Mark{{Type: "link", Attrs: map[string]any{"href": t}}}, withoutLink(marks)...)
			}
			out = append(out, mdSegment{text: key, marks: marks})
		case "status":
			out = append(out, mdSegment{text: strings.ToUpper(strAttr(c.Attrs, "text")), marks: addMarkType(marks, "bold")})
		case "date":
			out = append(out, mdSegment{text: strAttr(c.Attrs, "value"), marks: marks})
		case "emoji":
			out = append(out, mdSegment{text: ":" + strAttr(c.Attrs, "name") + ":", marks: marks, raw: true})
		case "inlineImage":
			alt := escapeMarkdown(strAttr(c.Attrs, "alt"), false, inTable)
			img := alt
			if src := m.imageSource(c); src != "" {
				img = "![" + alt + "](" + markdownTarget(src) + ")"
			}
			if t := m.href(strAttr(c.Attrs, "href")); t != "" {
				img = "[" + img + "](" + markdownTarget(t) + ")"
			}
			out = append(out, mdSegment{text: img, marks: withoutLink(marks), raw: true})
		case "inlineMacro":
			out = append(out, mdSegment{text: "[" + strAttr(c.Attrs, "name") + "]", marks: marks})
		case "inlineMath":
			// Formulas take no marks: the marks around one close before it and open again after it.
			if t := inlineMathMarkdown(strAttr(c.Attrs, "latex"), inTable); t != "" {
				out = append(out, mdSegment{text: t, raw: true, formula: true})
			}
		}
	}
	return out
}

func sortMarks(marks []*Mark) {
	for i := 1; i < len(marks); i++ {
		for j := i; j > 0 && mdMarkOrder[marks[j].Type] < mdMarkOrder[marks[j-1].Type]; j-- {
			marks[j], marks[j-1] = marks[j-1], marks[j]
		}
	}
}

func hasMark(marks []*Mark, typ string) bool {
	for _, m := range marks {
		if m.Type == typ {
			return true
		}
	}
	return false
}

func withoutLink(marks []*Mark) []*Mark {
	var out []*Mark
	for _, m := range marks {
		if m.Type != "link" {
			out = append(out, m)
		}
	}
	return out
}

func addMarkType(marks []*Mark, typ string) []*Mark {
	if hasMark(marks, typ) {
		return marks
	}
	out := append(append([]*Mark(nil), marks...), &Mark{Type: typ})
	sortMarks(out)
	return out
}

func sameMark(a, b *Mark) bool {
	return a.Type == b.Type && strAttr(a.Attrs, "href") == strAttr(b.Attrs, "href")
}

func markOpener(m *Mark) string {
	switch m.Type {
	case "link":
		return "["
	case "bold":
		return "**"
	case "italic":
		return "*"
	case "strike":
		return "~~"
	case "underline":
		return "<u>"
	case "subscript":
		return "<sub>"
	case "superscript":
		return "<sup>"
	case "highlight":
		return "<mark>"
	}
	return ""
}

func markCloser(m *Mark) string {
	switch m.Type {
	case "link":
		return "](" + markdownTarget(strAttr(m.Attrs, "href")) + ")"
	case "underline", "subscript", "superscript", "highlight":
		return "</" + strings.Trim(markOpener(m), "<>") + ">"
	}
	return markOpener(m)
}

// inline renders inline content: marks open and close as the content changes, with spaces outside of their
// delimiters (Markdown does not read "** bold**" as bold). Hard breaks become "\" and a new line.
func (m *mdWriter) inline(nodes []*Node, inTable bool) string {
	var b strings.Builder
	var open []*Mark
	pending := ""
	formulaEnd := -1 // where the last formula ends
	// At the start of a line, Markdown reads what follows spaces as it reads the line's first characters.
	lineStart := func() bool {
		s := b.String()
		return strings.TrimLeft(s[strings.LastIndexByte(s, '\n')+1:], " \t") == ""
	}
	for _, s := range m.segments(nodes, inTable) {
		keep := 0
		for keep < len(open) && keep < len(s.marks) && sameMark(open[keep], s.marks[keep]) {
			keep++
		}
		lead, core, trail := "", s.text, ""
		if !s.raw {
			lead, core, trail = splitSpace(s.text)
		}
		if core == "" {
			// Only spaces: they need no marks of their own.
			pending += lead + trail
			continue
		}
		for i := len(open) - 1; i >= keep; i-- {
			b.WriteString(markCloser(open[i]))
		}
		open = open[:keep]
		// Spaces at the start of a line mean nothing in Markdown, or indented code from four on.
		if !lineStart() {
			b.WriteString(pending + lead)
		}
		pending = ""
		opened := false
		for _, mk := range s.marks[keep:] {
			b.WriteString(markOpener(mk))
			open = append(open, mk)
			opened = true
		}
		out := core
		if !s.raw {
			out = escapeMarkdown(core, !opened && lineStart(), inTable)
		}
		if b.Len() == formulaEnd && out != "" && out[0] >= '0' && out[0] <= '9' {
			// A digit right after a formula would keep its closing dollar from closing it: it is written as a
			// character reference.
			out = "&#" + strconv.Itoa(int(out[0])) + ";" + out[1:]
		}
		b.WriteString(out)
		if s.formula {
			formulaEnd = b.Len()
		}
		pending = trail
	}
	for i := len(open) - 1; i >= 0; i-- {
		b.WriteString(markCloser(open[i]))
	}
	out := strings.TrimRight(b.String(), " ")
	for strings.HasSuffix(out, "\\\n") {
		out = strings.TrimRight(strings.TrimSuffix(out, "\\\n"), " ")
	}
	return out
}

func splitSpace(s string) (lead, core, trail string) {
	core = strings.TrimLeftFunc(s, unicode.IsSpace)
	lead = s[:len(s)-len(core)]
	trimmed := strings.TrimRightFunc(core, unicode.IsSpace)
	trail = core[len(trimmed):]
	return lead, trimmed, trail
}

func longestRun(s string, c byte) int {
	best, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

// codeSpan writes text as inline code, with a fence longer than the backticks it contains.
func codeSpan(text string) string {
	fence := strings.Repeat("`", longestRun(text, '`')+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") || strings.HasPrefix(text, " ") && strings.HasSuffix(text, " ") {
		return fence + " " + text + " " + fence
	}
	return fence + text + fence
}

// markdownTarget writes a link target; targets with spaces or parentheses are put in angle brackets.
func markdownTarget(t string) string {
	if strings.ContainsAny(t, " ()<>") {
		return "<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(t) + ">"
	}
	return t
}

var (
	entityLike     = regexp.MustCompile(`^&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});`)
	orderedMarker  = regexp.MustCompile(`^[0-9]{1,9}[.)]`)
	lineStartChars = "#>-+=|"
)

// escapeMarkdown escapes text so that Markdown reads it as text: punctuation that opens markup, entities, dollars
// that could open a formula, and at the start of a line what would begin a heading, quote, list or table.
func escapeMarkdown(s string, lineStart, inTable bool) string {
	var b strings.Builder
	// A table's cell holds no blocks: "1." or "#" at its start is text ("192.168.1.1" needs no backslash there).
	if lineStart && !inTable {
		if loc := orderedMarker.FindStringIndex(s); loc != nil {
			b.WriteString(s[:loc[1]-1] + "\\")
			s = s[loc[1]-1:]
		} else if s != "" && strings.ContainsRune(lineStartChars, rune(s[0])) {
			b.WriteByte('\\')
		}
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\\', '`', '*', '[', ']', '<', '~':
			b.WriteByte('\\')
		case '|':
			if inTable {
				b.WriteByte('\\')
			}
		case '_':
			before, _ := utf8.DecodeLastRuneInString(s[:i])
			after, _ := utf8.DecodeRuneInString(s[i+size:])
			if !(isWordRune(before) && isWordRune(after)) {
				b.WriteByte('\\')
			}
		case '&':
			if entityLike.MatchString(s[i:]) {
				b.WriteByte('\\')
			}
		case '$':
			// Every dollar that is not followed by a space: then no text opens a formula, whatever follows ("5 $ a
			// month" needs no backslash).
			if i+1 >= len(s) || !mathSpace(s[i+1]) {
				b.WriteByte('\\')
			}
		case '\n':
			b.WriteRune(r)
			i += size
			b.WriteString(escapeMarkdown(s[i:], true, inTable))
			return b.String()
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// FrontMatter writes YAML front matter for Markdown files of exports: keys in the given order, values quoted
// where YAML could read them otherwise.
func FrontMatter(fields [][2]any) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, f := range fields {
		k := fmt.Sprint(f[0])
		switch v := f[1].(type) {
		case []string:
			quoted := make([]string, len(v))
			for i, s := range v {
				quoted[i] = yamlString(s)
			}
			b.WriteString(k + ": [" + strings.Join(quoted, ", ") + "]\n")
		case string:
			b.WriteString(k + ": " + yamlString(v) + "\n")
		default:
			b.WriteString(fmt.Sprintf("%s: %v\n", k, v))
		}
	}
	b.WriteString("---\n")
	return b.String()
}

// yamlString writes a string as a double-quoted YAML scalar.
func yamlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf(`\x%02x`, r))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
