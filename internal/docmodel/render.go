// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"regexp"
	"slices"
	"strings"
)

var issueKeyPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])([A-Z][A-Z0-9_]{1,9}-[1-9][0-9]{0,9})\b`)

// IsIssueKey reports whether s has the form of an issue key (PROJECT-123).
func IsIssueKey(s string) bool {
	m := issueKeyPattern.FindStringSubmatch(" " + s)
	return m != nil && m[1] == s
}

// FindIssueKeys returns the distinct issue keys in text, in order of appearance. Whether they exist is checked
// by the caller.
func FindIssueKeys(text string) []string {
	var out []string
	for _, m := range issueKeyPattern.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

func intAttr(n *Node, k string) int64 {
	switch v := n.Attrs[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	}
	return 0
}

func strAttr(attrs map[string]any, k string) string {
	s, _ := attrs[k].(string)
	return s
}

// FormulaLatex returns the LaTeX of a formula as it is shown where it is not typeset: that of a formula within text
// (inlineMath) on one line without spaces at its ends, that of a formula on lines of its own (math) without blank
// lines around it (and lines of only spaces empty). Empty formulas (only spaces) give "": they are left out.
func FormulaLatex(n *Node) string {
	latex := strAttr(n.Attrs, "latex")
	if strings.TrimSpace(latex) == "" {
		return ""
	}
	if n.Type == "inlineMath" {
		return inlineLatex(latex)
	}
	return displayLatex(latex)
}

var (
	newlines   = strings.NewReplacer("\r\n", "\n", "\r", "\n")
	lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
)

// inlineLatex puts LaTeX on one line, without the spaces at its ends.
func inlineLatex(latex string) string { return strings.Trim(lineBreaks.Replace(latex), " \t") }

// Refs are the references a document makes.
type Refs struct {
	Users       []int64
	IssueKeys   []string
	Attachments []int64
	Links       []string
}

// References extracts mentioned users, issue keys (nodes, macros and keys in text), attachments and links.
func References(d *Doc) Refs {
	var r Refs
	addKey := func(k string) {
		if !slices.Contains(r.IssueKeys, k) {
			r.IssueKeys = append(r.IssueKeys, k)
		}
	}
	Walk(d, func(n *Node) bool {
		switch n.Type {
		case "mention":
			if id := intAttr(n, "user"); id != 0 && !slices.Contains(r.Users, id) {
				r.Users = append(r.Users, id)
			}
		case "taskItem":
			if id := intAttr(n, "assignee"); id != 0 && !slices.Contains(r.Users, id) {
				r.Users = append(r.Users, id)
			}
		case "issue":
			addKey(strAttr(n.Attrs, "key"))
		case "image":
			if id := intAttr(n, "attachment"); id != 0 && !slices.Contains(r.Attachments, id) {
				r.Attachments = append(r.Attachments, id)
			}
		case "text":
			for _, k := range FindIssueKeys(n.Text) {
				addKey(k)
			}
			for _, m := range n.Marks {
				if m.Type == "link" {
					if h := strAttr(m.Attrs, "href"); h != "" && !slices.Contains(r.Links, h) {
						r.Links = append(r.Links, h)
					}
				}
			}
		case "macro", "inlineMacro":
			if p, ok := n.Attrs["params"].(map[string]any); ok {
				if k, _ := p["key"].(string); IsIssueKey(k) {
					addKey(k)
				}
			}
		}
		return true
	})
	return r
}

// PlainText returns the text of a document for search and notifications: blocks separated by newlines.
func PlainText(d *Doc) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *Node)
	walk = func(n *Node) {
		switch n.Type {
		case "text":
			b.WriteString(n.Text)
			return
		case "hardBreak":
			b.WriteByte('\n')
			return
		case "mention":
			b.WriteString("@" + strAttr(n.Attrs, "label"))
			return
		case "issue":
			b.WriteString(strAttr(n.Attrs, "key"))
			return
		case "status":
			b.WriteString(strAttr(n.Attrs, "text"))
			return
		case "date":
			b.WriteString(strAttr(n.Attrs, "value"))
			return
		case "emoji":
			b.WriteString(":" + strAttr(n.Attrs, "name") + ":")
			return
		case "inlineImage":
			b.WriteString(strAttr(n.Attrs, "alt"))
			return
		case "inlineMath":
			// Formulas as Markdown writes them, so that their LaTeX can be found.
			if latex := FormulaLatex(n); latex != "" {
				b.WriteString("$" + latex + "$")
			}
			return
		case "math":
			if latex := FormulaLatex(n); latex != "" {
				b.WriteString("$$" + latex + "$$")
			}
		case "expand":
			b.WriteString(strAttr(n.Attrs, "title"))
			b.WriteByte('\n')
		}
		for _, c := range n.Content {
			walk(c)
		}
		if nodes[n.Type].block {
			b.WriteByte('\n')
		}
	}
	for _, n := range d.Content {
		walk(n)
	}
	return strings.TrimSpace(collapseNewlines(b.String()))
}

func collapseNewlines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// Summary returns the first characters of the plain text, for notifications and previews.
func Summary(d *Doc, max int) string {
	t := strings.Join(strings.Fields(PlainText(d)), " ")
	if len([]rune(t)) <= max {
		return t
	}
	r := []rune(t)
	return strings.TrimSpace(string(r[:max])) + "…"
}
