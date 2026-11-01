// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var (
	md = goldmark.New(goldmark.WithExtensions(extension.GFM, mathExtension{}))
	// mdNoMath reads Markdown whose dollars are text.
	mdNoMath = goldmark.New(goldmark.WithExtensions(extension.GFM))
)

// MarkdownOptions adjust how Markdown becomes a document.
type MarkdownOptions struct {
	// Image returns the source an image may be shown from, or false: then the image stays as text — its
	// description, or its address. Without it, images with same-origin sources (/…) are shown.
	Image func(src string) (string, bool)
	// Link rewrites link targets that are paths (such as paths in a repository); nil keeps them.
	Link func(href string) string
	// NoMath reads dollars as text, and fenced blocks of the language math as code: Markdown of systems without
	// formulas (Bitbucket).
	NoMath bool
}

// FromMarkdown converts CommonMark (with tables, task lists, strikethrough and formulas: $…$ within text, $$ around
// formulas on lines of their own, see markdown_math.go) into a document. Raw HTML is dropped; unsafe links become
// plain text; images show only from the instance itself.
func FromMarkdown(source string) *Doc { return FromMarkdownWith(source, MarkdownOptions{}) }

// FromMarkdownWith converts Markdown with options for images, relative links and formulas.
func FromMarkdownWith(source string, opts MarkdownOptions) *Doc {
	src := []byte(source)
	p := md
	if opts.NoMath {
		p = mdNoMath
	}
	root := p.Parser().Parse(text.NewReader(src))
	d := Empty()
	c := &mdConverter{src: src, opts: opts}
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		if b := c.block(n); b != nil {
			d.Content = append(d.Content, b)
		}
	}
	return d
}

type mdConverter struct {
	src  []byte
	opts MarkdownOptions
}

// sameOrigin shows images whose source is a path of the instance.
func sameOrigin(src string) (string, bool) {
	return src, strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//")
}

// pathLink reports whether a link target is a path — relative, or from the top — rather than an address with a
// scheme, a protocol-relative address or an anchor of the document.
func pathLink(href string) bool {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "//") {
		return false
	}
	i := strings.IndexAny(href, ":/?#")
	return i < 0 || href[i] != ':'
}

// plain returns the text of inline nodes (the description of an image).
func (c *mdConverter) plain(n ast.Node) string {
	var b strings.Builder
	for ch := n.FirstChild(); ch != nil; ch = ch.NextSibling() {
		switch v := ch.(type) {
		case *ast.Text:
			b.WriteString(markdownText(v.Segment.Value(c.src)))
		case *ast.String:
			b.Write(v.Value)
		case *mathInline:
			b.WriteString("$" + v.latex + "$")
		default:
			b.WriteString(c.plain(ch))
		}
	}
	return b.String()
}

// image converts an image: shown if the options allow its source, else its description (or address) as text —
// linked to the image when it is not in a link already and the address is one.
func (c *mdConverter) image(v *ast.Image, marks []*Mark) []*Node {
	src, alt := string(v.Destination), c.plain(v)
	resolve := c.opts.Image
	if resolve == nil {
		resolve = sameOrigin
	}
	var href string
	for _, m := range marks {
		if m.Type == "link" {
			href, _ = m.Attrs["href"].(string)
		}
	}
	if shown, ok := resolve(src); ok && safeImage(shown, true) {
		attrs := map[string]any{"src": shown}
		if alt != "" {
			attrs["alt"] = alt
		}
		if t := string(v.Title); t != "" {
			attrs["title"] = t
		}
		if href != "" {
			attrs["href"] = href
		}
		return []*Node{{Type: "inlineImage", Attrs: attrs}}
	}
	t := alt
	if t == "" {
		t = src
	}
	if t == "" {
		return nil
	}
	ms := append([]*Mark(nil), marks...)
	if href == "" && (strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://")) && SafeLink(src) {
		ms = append(ms, &Mark{Type: "link", Attrs: map[string]any{"href": src}})
	}
	node := &Node{Type: "text", Text: t}
	if len(ms) > 0 {
		node.Marks = ms
	}
	return []*Node{node}
}

func (c *mdConverter) block(n ast.Node) *Node {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		inl := c.inlines(n, nil)
		if len(inl) == 0 {
			return nil
		}
		return &Node{Type: "paragraph", Content: inl}
	case *ast.Heading:
		return &Node{Type: "heading", Attrs: map[string]any{"level": int64(v.Level)}, Content: c.inlines(n, nil)}
	case *ast.ThematicBreak:
		return &Node{Type: "horizontalRule"}
	case *ast.Blockquote:
		return &Node{Type: "blockquote", Content: c.blocks(n)}
	case *mathBlock:
		return mathNode(v.latex())
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var b strings.Builder
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			b.Write(seg.Value(c.src))
		}
		code := strings.TrimSuffix(b.String(), "\n")
		node := &Node{Type: "codeBlock"}
		if f, ok := v.(*ast.FencedCodeBlock); ok {
			lang := string(f.Language(c.src))
			if lang == "math" && !c.opts.NoMath {
				// GitHub's other form of formulas on lines of their own.
				return mathNode(displayLatex(code))
			}
			if lang != "" {
				node.Attrs = map[string]any{"language": lang}
			}
		}
		if code != "" {
			node.Content = []*Node{{Type: "text", Text: code}}
		}
		return node
	case *ast.List:
		task := false
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			if first := item.FirstChild(); first != nil {
				if _, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
					task = true
				}
			}
		}
		list := &Node{Type: "bulletList"}
		if v.IsOrdered() {
			list.Type = "orderedList"
			if v.Start > 1 {
				list.Attrs = map[string]any{"start": int64(v.Start)}
			}
		}
		if task {
			list.Type = "taskList"
		}
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			li := &Node{Type: "listItem"}
			if task {
				li.Type = "taskItem"
				checked := false
				if first := item.FirstChild(); first != nil {
					if cb, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
						checked = cb.IsChecked
					}
				}
				li.Attrs = map[string]any{"checked": checked}
			}
			li.Content = c.blocks(item)
			if len(li.Content) == 0 {
				li.Content = []*Node{{Type: "paragraph"}}
			}
			list.Content = append(list.Content, li)
		}
		return list
	case *extast.Table:
		table := &Node{Type: "table"}
		for row := n.FirstChild(); row != nil; row = row.NextSibling() {
			tr := &Node{Type: "tableRow"}
			_, header := row.(*extast.TableHeader)
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				typ := "tableCell"
				if header {
					typ = "tableHeader"
				}
				p := &Node{Type: "paragraph", Content: c.inlines(cell, nil)}
				tr.Content = append(tr.Content, &Node{Type: typ, Content: []*Node{p}})
			}
			table.Content = append(table.Content, tr)
		}
		return table
	case *ast.HTMLBlock:
		return nil
	}
	if n.HasChildren() {
		if inner := c.blocks(n); len(inner) == 1 {
			return inner[0]
		}
	}
	return nil
}

func (c *mdConverter) blocks(parent ast.Node) []*Node {
	var out []*Node
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		if b := c.block(n); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func (c *mdConverter) inlines(parent ast.Node, marks []*Mark) []*Node {
	var out []*Node
	add := func(t string, ms []*Mark) {
		if t == "" {
			return
		}
		node := &Node{Type: "text", Text: t}
		if len(ms) > 0 {
			node.Marks = append([]*Mark(nil), ms...)
		}
		out = append(out, node)
	}
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		switch v := n.(type) {
		case *ast.Text:
			add(markdownText(v.Segment.Value(c.src)), marks)
			if v.HardLineBreak() {
				out = append(out, &Node{Type: "hardBreak"})
			} else if v.SoftLineBreak() {
				add(" ", marks)
			}
		case *ast.String:
			add(string(v.Value), marks)
		case *ast.CodeSpan:
			var b strings.Builder
			for t := v.FirstChild(); t != nil; t = t.NextSibling() {
				if tx, ok := t.(*ast.Text); ok {
					b.Write(tx.Segment.Value(c.src))
				}
			}
			add(b.String(), append(append([]*Mark{}, marks...), &Mark{Type: "code"}))
		case *ast.Emphasis:
			m := "italic"
			if v.Level >= 2 {
				m = "bold"
			}
			out = append(out, c.inlines(n, append(append([]*Mark{}, marks...), &Mark{Type: m}))...)
		case *extast.Strikethrough:
			out = append(out, c.inlines(n, append(append([]*Mark{}, marks...), &Mark{Type: "strike"}))...)
		case *ast.Link:
			href := string(v.Destination)
			if c.opts.Link != nil && pathLink(href) {
				href = c.opts.Link(href)
			}
			if SafeLink(href) {
				out = append(out, c.inlines(n, append(append([]*Mark{}, marks...), &Mark{Type: "link", Attrs: map[string]any{"href": href}}))...)
			} else {
				out = append(out, c.inlines(n, marks)...)
			}
		case *ast.AutoLink:
			href := string(v.URL(c.src))
			if SafeLink(href) {
				add(href, append(append([]*Mark{}, marks...), &Mark{Type: "link", Attrs: map[string]any{"href": href}}))
			} else {
				add(href, marks)
			}
		case *ast.Image:
			out = append(out, c.image(v, marks)...)
		case *mathInline:
			// Formulas take no marks: documents keep none on them.
			if f := inlineMathNode(v.latex); f != nil {
				out = append(out, f)
			}
		case *extast.TaskCheckBox, *ast.RawHTML:
		default:
			out = append(out, c.inlines(n, marks)...)
		}
	}
	return mergeText(out)
}

// markdownText returns the text of a Markdown text segment as it reads: escaped punctuation without its backslash,
// entities and character references as their characters (the parser keeps them in the segments as written).
func markdownText(raw []byte) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 < len(raw) && util.IsPunct(raw[i+1]):
			i++
			b.WriteByte(raw[i])
		case c == '&':
			if s, n := characterReference(raw[i:]); n > 0 {
				b.WriteString(s)
				i += n - 1
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// characterReference reads an entity (&amp;) or a numeric character reference (&#228;, &#xE4;) at the start of
// s and returns its characters and length, or 0.
func characterReference(s []byte) (string, int) {
	end := bytes.IndexByte(s, ';')
	if end < 2 || end > 33 {
		return "", 0
	}
	name := string(s[1:end])
	if num, ok := strings.CutPrefix(name, "#"); ok {
		base := 10
		if hex, ok := strings.CutPrefix(strings.ToLower(num), "x"); ok {
			num, base = hex, 16
		}
		v, err := strconv.ParseUint(num, base, 32)
		if err != nil || num == "" || len(num) > 7 {
			return "", 0
		}
		r := rune(v)
		if r == 0 || !utf8.ValidRune(r) {
			r = utf8.RuneError
		}
		return string(r), end + 1
	}
	if e, ok := util.LookUpHTML5EntityByName(name); ok {
		return string(e.Characters), end + 1
	}
	return "", 0
}

// mergeText joins adjacent text nodes with identical marks. The text of a run is collected first and set once:
// joining node by node would copy the text so far each time (a paragraph of many lines took seconds).
func mergeText(in []*Node) []*Node {
	var out []*Node
	var run strings.Builder
	joining := false
	for _, n := range in {
		if len(out) > 0 {
			last := out[len(out)-1]
			if last.Type == "text" && n.Type == "text" && sameMarks(last.Marks, n.Marks) {
				if !joining {
					run.Reset()
					run.WriteString(last.Text)
					joining = true
				}
				run.WriteString(n.Text)
				continue
			}
		}
		if joining {
			out[len(out)-1].Text, joining = run.String(), false
		}
		out = append(out, n)
	}
	if joining {
		out[len(out)-1].Text = run.String()
	}
	return out
}

func sameMarks(a, b []*Mark) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || len(a[i].Attrs) != len(b[i].Attrs) {
			return false
		}
		for k, v := range a[i].Attrs {
			if b[i].Attrs[k] != v {
				return false
			}
		}
	}
	return true
}
