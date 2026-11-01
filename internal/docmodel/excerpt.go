// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Excerpts are what lists and activity streams show of a document: the text a reader skims, not the
// whole of it. Its headings, paragraphs and list items are clauses of their own; a table row reads "Storefront:
// TypeScript, Mara Lindner, Stable"; formulas within text read as $…$; code, formulas on lines of their own, images
// and macros are left out. A document with an excerpt macro is represented by the macro's content.

// Ellipsis marks where text was cut.
const Ellipsis = "…"

// clauseSeparator separates clauses that do not end a sentence.
const clauseSeparator = " · "

// Excerpt returns the beginning of a document as clauses, cut at a word boundary after at most max characters
// (with an ellipsis where it was cut).
func Excerpt(d *Doc, max int) string {
	if d == nil {
		return ""
	}
	nodes := d.Content
	if m := excerptMacro(d.Content); m != nil {
		nodes = m.Content
	}
	var clauses []string
	length := 0
	var add func(ns []*Node) bool
	// add collects clauses until there are enough to fill max characters; it reports whether to go on.
	add = func(ns []*Node) bool {
		for _, n := range ns {
			switch n.Type {
			case "paragraph", "heading":
				if c := inlineText(n); c != "" {
					clauses = append(clauses, c)
					length += utf8.RuneCountInString(c) + 3
				}
			case "expand":
				if c := strings.Join(strings.Fields(strAttr(n.Attrs, "title")), " "); c != "" {
					clauses = append(clauses, c)
					length += utf8.RuneCountInString(c) + 3
				}
				if !add(n.Content) {
					return false
				}
			case "table":
				for _, row := range n.Content {
					if c := rowClause(row); c != "" {
						clauses = append(clauses, c)
						length += utf8.RuneCountInString(c) + 3
					}
					if length > max {
						return false
					}
				}
			case "codeBlock", "math", "image", "horizontalRule", "macro", "unsupported":
				// Code, formulas of their own, pictures and what macros show are not text to skim.
			default:
				// Lists, list items, quotes, panels and columns: their blocks.
				if !add(n.Content) {
					return false
				}
			}
			if length > max {
				return false
			}
		}
		return true
	}
	add(nodes)
	return Cut(JoinClauses(clauses), max)
}

// excerptMacro returns the first excerpt macro with content in the nodes, or nil.
func excerptMacro(nodes []*Node) *Node {
	var found *Node
	Walk(&Doc{Content: nodes}, func(n *Node) bool {
		if found == nil && n.Type == "macro" && strAttr(n.Attrs, "name") == "excerpt" && len(n.Content) > 0 {
			found = n
		}
		return found == nil
	})
	return found
}

// inlineText returns the text of a paragraph or heading on one line.
func inlineText(n *Node) string {
	var b strings.Builder
	var walk func(n *Node)
	walk = func(n *Node) {
		switch n.Type {
		case "text":
			b.WriteString(n.Text)
		case "hardBreak":
			b.WriteByte(' ')
		case "mention":
			b.WriteString("@" + strAttr(n.Attrs, "label"))
		case "issue":
			b.WriteString(strAttr(n.Attrs, "key"))
		case "status":
			b.WriteString(strAttr(n.Attrs, "text"))
		case "date":
			b.WriteString(strAttr(n.Attrs, "value"))
		case "inlineMath":
			// A formula keeps its place in the sentence, as Markdown writes it.
			if latex := FormulaLatex(n); latex != "" {
				b.WriteString("$" + latex + "$")
			}
		case "emoji", "inlineImage", "inlineMacro":
		default:
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// rowClause returns a table row as "first cell: other cells, …"; rows of headers only are left out.
func rowClause(row *Node) string {
	var cells []string
	headers := true
	for _, cell := range row.Content {
		if cell.Type != "tableHeader" {
			headers = false
		}
		var parts []string
		for _, block := range cell.Content {
			if t := blockText(block); t != "" {
				parts = append(parts, t)
			}
		}
		if t := strings.Join(parts, " "); t != "" {
			cells = append(cells, t)
		}
	}
	if headers || len(cells) == 0 {
		return ""
	}
	if len(cells) == 1 {
		return cells[0]
	}
	return strings.TrimRight(cells[0], ":") + ": " + strings.Join(cells[1:], ", ")
}

// blockText returns the text of a block in a table cell, on one line, without code.
func blockText(n *Node) string {
	switch n.Type {
	case "paragraph", "heading":
		return inlineText(n)
	case "codeBlock", "math", "image", "macro", "unsupported", "table":
		return ""
	}
	var parts []string
	for _, c := range n.Content {
		if t := blockText(c); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// JoinClauses joins clauses into one line: after the end of a sentence (or a colon) with a space, otherwise with a
// middle dot, so that headings, list items and table rows do not run together. Empty clauses are left out.
func JoinClauses(clauses []string) string {
	var b strings.Builder
	for _, c := range clauses {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if b.Len() > 0 {
			if endsSentence(b.String()) {
				b.WriteByte(' ')
			} else {
				b.WriteString(clauseSeparator)
			}
		}
		b.WriteString(c)
	}
	return b.String()
}

// endsSentence reports whether a text ends with the end of a sentence or a colon (marks of search matches, \u0001
// and \u0002, and closing quotes and brackets do not count).
func endsSentence(s string) bool {
	s = strings.TrimRight(s, "\u0001\u0002\"'”’»)]")
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".!?:;…", r)
}

// Cut returns a text of at most max characters: a longer one is cut at the last word boundary before (or, for a
// single long word, within) the limit, without the punctuation and separators that would end it, and gets an
// ellipsis.
func Cut(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	cut := max
	// The last space up to the limit, if it leaves at least half of it.
	for i := max; i > max/2; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	out := strings.TrimRightFunc(string(r[:cut]), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:·-–—(\u0001", r)
	})
	// A match cut in the middle is closed, so that what follows is not marked.
	if strings.Count(out, "\u0001") > strings.Count(out, "\u0002") {
		out += "\u0002"
	}
	return out + Ellipsis
}
