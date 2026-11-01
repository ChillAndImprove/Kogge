// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import "strings"

// Inline comments refer to an anchor mark around the commented text: the mark moves with the text
// when the page is edited.

const objectReplacement = '￼'

// textBlock reports whether a node holds inline content.
func textBlock(n *Node) bool {
	switch n.Type {
	case "paragraph", "heading", "codeBlock":
		return true
	}
	return false
}

// AddAnchor wraps the given occurrence (0-based, counted in document order) of text inside one text block in an
// anchor mark with the id. It reports whether the text was found.
func AddAnchor(d *Doc, id, text string, occurrence int) bool {
	needle := []rune(text)
	if len(needle) == 0 || id == "" {
		return false
	}
	seen := 0
	done := false
	Walk(d, func(n *Node) bool {
		if done {
			return false
		}
		if !textBlock(n) {
			return true
		}
		// Concatenate the inline content; non-text nodes count as one placeholder character.
		var hay []rune
		for _, c := range n.Content {
			if c.Type == "text" {
				hay = append(hay, []rune(c.Text)...)
			} else {
				hay = append(hay, objectReplacement)
			}
		}
		for start := 0; start+len(needle) <= len(hay); start++ {
			if string(hay[start:start+len(needle)]) != string(needle) {
				continue
			}
			if seen < occurrence {
				seen++
				continue
			}
			n.Content = markRange(n.Content, start, start+len(needle), &Mark{Type: "anchor", Attrs: map[string]any{"id": id}})
			done = true
			return false
		}
		return false
	})
	return done
}

// markRange adds a mark to the text between two positions of the concatenated inline content, splitting text
// nodes at the boundaries.
func markRange(content []*Node, from, to int, m *Mark) []*Node {
	var out []*Node
	pos := 0
	for _, c := range content {
		if c.Type != "text" {
			out = append(out, c)
			pos++
			continue
		}
		r := []rune(c.Text)
		start, end := pos, pos+len(r)
		pos = end
		if end <= from || start >= to {
			out = append(out, c)
			continue
		}
		a, b := max(from-start, 0), min(to-start, len(r))
		if a > 0 {
			out = append(out, &Node{Type: "text", Text: string(r[:a]), Marks: c.Marks})
		}
		marks := append(append([]*Mark{}, c.Marks...), m)
		out = append(out, &Node{Type: "text", Text: string(r[a:b]), Marks: marks})
		if b < len(r) {
			out = append(out, &Node{Type: "text", Text: string(r[b:]), Marks: c.Marks})
		}
	}
	return out
}

// RemoveAnchor removes the anchor mark with the id and merges the text nodes it split.
func RemoveAnchor(d *Doc, id string) bool {
	found := false
	Walk(d, func(n *Node) bool {
		if !textBlock(n) {
			return true
		}
		changed := false
		for _, c := range n.Content {
			kept := c.Marks[:0]
			for _, m := range c.Marks {
				if m.Type == "anchor" && strAttr(m.Attrs, "id") == id {
					changed = true
					continue
				}
				kept = append(kept, m)
			}
			if len(kept) == 0 {
				c.Marks = nil
			} else {
				c.Marks = kept
			}
		}
		if changed {
			found = true
			n.Content = mergeText(n.Content)
		}
		return false
	})
	return found
}

// Anchors returns the ids of the anchor marks in a document with the text they cover.
func Anchors(d *Doc) map[string]string {
	out := map[string]string{}
	var b strings.Builder
	Walk(d, func(n *Node) bool {
		if n.Type != "text" {
			return true
		}
		for _, m := range n.Marks {
			if m.Type == "anchor" {
				id := strAttr(m.Attrs, "id")
				b.Reset()
				b.WriteString(out[id])
				b.WriteString(n.Text)
				out[id] = b.String()
			}
		}
		return true
	})
	return out
}

// ReplaceText replaces the given occurrence (0-based, in document order) of text inside one text block with a node,
// e.g. an issue node for text an issue was created from. It reports whether the text was found.
func ReplaceText(d *Doc, text string, occurrence int, node *Node) bool {
	needle := []rune(text)
	if len(needle) == 0 || node == nil {
		return false
	}
	seen := 0
	done := false
	Walk(d, func(n *Node) bool {
		if done {
			return false
		}
		if !textBlock(n) {
			return true
		}
		var hay []rune
		for _, c := range n.Content {
			if c.Type == "text" {
				hay = append(hay, []rune(c.Text)...)
			} else {
				hay = append(hay, objectReplacement)
			}
		}
		for start := 0; start+len(needle) <= len(hay); start++ {
			if string(hay[start:start+len(needle)]) != string(needle) {
				continue
			}
			if seen < occurrence {
				seen++
				continue
			}
			n.Content = replaceRange(n.Content, start, start+len(needle), node)
			done = true
			return false
		}
		return false
	})
	return done
}

// replaceRange puts a node in place of the inline content between two positions, splitting text nodes at the
// boundaries.
func replaceRange(content []*Node, from, to int, node *Node) []*Node {
	var out []*Node
	pos := 0
	inserted := false
	insert := func() {
		if !inserted {
			out = append(out, node)
			inserted = true
		}
	}
	for _, c := range content {
		if c.Type != "text" {
			if pos >= from && pos < to {
				insert()
			} else {
				out = append(out, c)
			}
			pos++
			continue
		}
		r := []rune(c.Text)
		start, end := pos, pos+len(r)
		pos = end
		if end <= from || start >= to {
			out = append(out, c)
			continue
		}
		a, b := max(from-start, 0), min(to-start, len(r))
		if a > 0 {
			out = append(out, &Node{Type: "text", Text: string(r[:a]), Marks: c.Marks})
		}
		insert()
		if b < len(r) {
			out = append(out, &Node{Type: "text", Text: string(r[b:]), Marks: c.Marks})
		}
	}
	return mergeText(out)
}
