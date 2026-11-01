// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Resolver supplies display data for references when rendering HTML. Implementations must apply the
// reader's permissions (an issue the reader may not see renders as its key only).
type Resolver interface {
	UserName(id int64) string
	IssueURL(key string) string
	AttachmentURL(id int64) string
}

// Links is implemented by resolvers that change where a rendered document points: exports, whose pages link to
// each other and to their attachments by relative paths, and to everything else by the instance's address.
type Links interface {
	// Href returns the target of a link; "" keeps the text without the link.
	Href(href string) string
	// ImageSource returns the source of an image, given its attachment (0 for none) and src; "" shows its
	// description instead of the image.
	ImageSource(attachment int64, src string) string
}

type nopResolver struct{}

func (nopResolver) UserName(id int64) string   { return "user " + strconv.FormatInt(id, 10) }
func (nopResolver) IssueURL(key string) string { return "/browse/" + key }
func (nopResolver) AttachmentURL(id int64) string {
	return "/api/v1/attachments/" + strconv.FormatInt(id, 10)
}

var headingIDJunk = regexp.MustCompile(`[^a-z0-9äöüß]+`)

// HeadingID returns the anchor of a heading, as tables of contents link to it: its id, or "h-" and the words of
// its text (the web application derives the same ids).
func HeadingID(n *Node) string {
	if id := strAttr(n.Attrs, "id"); id != "" {
		return id
	}
	s := strings.Trim(headingIDJunk.ReplaceAllString(strings.ToLower(NodeText(n)), "-"), "-")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return "h-" + s
}

// NodeText returns the text of the text nodes below a node, without separators (for headings and titles).
func NodeText(n *Node) string {
	if n.Type == "text" {
		return n.Text
	}
	var b strings.Builder
	for _, c := range n.Content {
		b.WriteString(NodeText(c))
	}
	return b.String()
}

// HTML renders a document as sanitised HTML (for e-mails and exports). Every value is escaped; links and images
// follow the rules of documents, also where a resolver rewrites them.
func HTML(d *Doc, r Resolver) string {
	if d == nil {
		return ""
	}
	if r == nil {
		r = nopResolver{}
	}
	h := &htmlRenderer{r: r}
	h.links, _ = r.(Links)
	for _, n := range d.Content {
		h.node(n)
	}
	return h.b.String()
}

var esc = html.EscapeString

type htmlRenderer struct {
	b     strings.Builder
	r     Resolver
	links Links
}

func (h *htmlRenderer) children(n *Node) {
	for _, c := range n.Content {
		h.node(c)
	}
}

func (h *htmlRenderer) wrap(n *Node, tag, attrs string) {
	h.b.WriteString("<" + tag + attrs + ">")
	h.children(n)
	h.b.WriteString("</" + tag + ">")
}

// href returns the target of a link as it is rendered, or "" for none.
func (h *htmlRenderer) href(target string) string {
	if !SafeLink(target) {
		return ""
	}
	if h.links == nil {
		return target
	}
	out := h.links.Href(target)
	if out == "" || !SafeLink(out) && !relativePath(out) {
		return ""
	}
	return out
}

// relativePath reports whether a link is a relative path without a scheme, as exports write them.
func relativePath(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00") {
		return false
	}
	i := strings.IndexAny(s, ":/?#")
	return i < 0 || s[i] != ':'
}

// imageSource returns the source an image is shown from, or "".
func (h *htmlRenderer) imageSource(n *Node) string {
	id, src := intAttr(n, "attachment"), strAttr(n.Attrs, "src")
	if h.links == nil {
		if id != 0 {
			return h.r.AttachmentURL(id)
		}
		return src
	}
	out := h.links.ImageSource(id, src)
	if safeImage(out, true) || relativePath(out) || embeddedImage.MatchString(out) {
		return out
	}
	return ""
}

// embeddedImage matches images that a rendered document carries itself (exports of single pages).
var embeddedImage = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=]+$`)

func alignStyle(n *Node) string {
	switch a := strAttr(n.Attrs, "align"); a {
	case "center", "right", "justify":
		return ` style="text-align:` + a + `"`
	}
	return ""
}

func (h *htmlRenderer) node(n *Node) {
	b := &h.b
	switch n.Type {
	case "paragraph":
		h.wrap(n, "p", alignStyle(n))
	case "heading":
		lvl := intAttr(n, "level")
		if lvl < 1 || lvl > 6 {
			lvl = 2
		}
		h.wrap(n, fmt.Sprintf("h%d", lvl), ` id="`+esc(HeadingID(n))+`"`+alignStyle(n))
	case "blockquote":
		h.wrap(n, "blockquote", "")
	case "horizontalRule":
		b.WriteString("<hr>")
	case "bulletList":
		h.wrap(n, "ul", "")
	case "orderedList":
		start := ""
		if s := intAttr(n, "start"); s > 1 {
			start = fmt.Sprintf(` start="%d"`, s)
		}
		h.wrap(n, "ol", start)
	case "listItem":
		h.wrap(n, "li", "")
	case "taskList":
		h.wrap(n, "ul", ` class="task-list"`)
	case "taskItem":
		checked := ""
		if c, _ := n.Attrs["checked"].(bool); c {
			checked = " checked"
		}
		b.WriteString(`<li class="task-item"><input type="checkbox" disabled` + checked + `> `)
		h.children(n)
		b.WriteString("</li>")
	case "codeBlock":
		lang := strAttr(n.Attrs, "language")
		if t := strAttr(n.Attrs, "title"); t != "" {
			b.WriteString(`<div class="code-title">` + esc(t) + `</div>`)
		}
		cls := ""
		if lang != "" {
			cls = ` class="language-` + esc(lang) + `"`
		}
		b.WriteString("<pre><code" + cls + ">")
		for _, c := range n.Content {
			b.WriteString(esc(c.Text))
		}
		b.WriteString("</code></pre>")
	case "table":
		// In a frame, which scrolls sideways where the table is wider than the page.
		b.WriteString(`<div class="table-frame"><table><tbody>`)
		h.children(n)
		b.WriteString("</tbody></table></div>")
	case "tableRow":
		h.wrap(n, "tr", "")
	case "tableHeader", "tableCell":
		tag := "td"
		if n.Type == "tableHeader" {
			tag = "th"
		}
		attrs := ""
		if c := intAttr(n, "colspan"); c > 1 {
			attrs += fmt.Sprintf(` colspan="%d"`, c)
		}
		if rs := intAttr(n, "rowspan"); rs > 1 {
			attrs += fmt.Sprintf(` rowspan="%d"`, rs)
		}
		if bg := strAttr(n.Attrs, "background"); bg != "" && Palette[bg] {
			attrs += ` class="bg-` + bg + `"`
		}
		h.wrap(n, tag, attrs)
	case "image":
		alt := strAttr(n.Attrs, "alt")
		src := h.imageSource(n)
		if src == "" {
			if alt != "" {
				b.WriteString(`<p class="image-missing">` + esc(alt) + `</p>`)
			}
			return
		}
		width := ""
		if w := intAttr(n, "width"); w > 0 {
			width = fmt.Sprintf(` width="%d"`, w)
		}
		b.WriteString(`<img src="` + esc(src) + `" alt="` + esc(alt) + `"` + width + `>`)
	case "inlineImage":
		alt := strAttr(n.Attrs, "alt")
		src := h.imageSource(n)
		img := esc(alt)
		if src != "" {
			img = `<img src="` + esc(src) + `" alt="` + esc(alt) + `">`
		}
		if t := h.href(strAttr(n.Attrs, "href")); t != "" {
			img = `<a href="` + esc(t) + `" rel="noopener noreferrer">` + img + `</a>`
		}
		b.WriteString(img)
	case "panel":
		h.wrap(n, "div", ` class="panel panel-`+esc(strAttr(n.Attrs, "kind"))+`"`)
	case "expand":
		b.WriteString("<details open><summary>" + esc(strAttr(n.Attrs, "title")) + "</summary>")
		h.children(n)
		b.WriteString("</details>")
	case "columns":
		b.WriteString(`<div class="columns">`)
		widths := ColumnWidths(n)
		for i, c := range n.Content {
			b.WriteString(fmt.Sprintf(`<div class="column" style="flex:%g 1 0">`, widths[i]))
			h.children(c)
			b.WriteString("</div>")
		}
		b.WriteString("</div>")
	case "macro":
		b.WriteString(`<div class="macro">[` + esc(strAttr(n.Attrs, "name")) + `]</div>`)
	case "inlineMacro":
		b.WriteString(`<span class="macro">[` + esc(strAttr(n.Attrs, "name")) + `]</span>`)
	case "unsupported":
		b.WriteString(`<div class="unsupported">[` + esc(strAttr(n.Attrs, "source")) + " " + esc(strAttr(n.Attrs, "name")) + `]</div>`)
	case "math":
		// Formulas show their LaTeX (rendered HTML runs no scripts to typeset it), with pandoc's classes and delimiters.
		if latex := FormulaLatex(n); latex != "" {
			b.WriteString(`<div class="math display">\[` + esc(latex) + `\]</div>`)
		}
	case "inlineMath":
		if latex := FormulaLatex(n); latex != "" {
			b.WriteString(`<span class="math inline">\(` + esc(latex) + `\)</span>`)
		}
	case "hardBreak":
		b.WriteString("<br>")
	case "mention":
		name := h.r.UserName(intAttr(n, "user"))
		if name == "" {
			name = strAttr(n.Attrs, "label") // a person the resolver does not know any more
		}
		b.WriteString(`<span class="mention">@` + esc(name) + "</span>")
	case "issue":
		k := strAttr(n.Attrs, "key")
		if t := h.href(h.r.IssueURL(k)); t != "" {
			b.WriteString(`<a class="issue" href="` + esc(t) + `">` + esc(k) + "</a>")
		} else {
			b.WriteString(`<span class="issue">` + esc(k) + "</span>")
		}
	case "status":
		b.WriteString(`<span class="status status-` + esc(strAttr(n.Attrs, "color")) + `">` + esc(strAttr(n.Attrs, "text")) + "</span>")
	case "date":
		v := strAttr(n.Attrs, "value")
		b.WriteString(`<time datetime="` + esc(v) + `">` + esc(v) + "</time>")
	case "emoji":
		b.WriteString(":" + esc(strAttr(n.Attrs, "name")) + ":")
	case "text":
		h.text(n)
	default:
		h.children(n)
	}
}

func (h *htmlRenderer) text(n *Node) {
	open, close := "", ""
	for _, m := range n.Marks {
		var o, c string
		switch m.Type {
		case "bold":
			o, c = "<strong>", "</strong>"
		case "italic":
			o, c = "<em>", "</em>"
		case "underline":
			o, c = "<u>", "</u>"
		case "strike":
			o, c = "<s>", "</s>"
		case "code":
			o, c = "<code>", "</code>"
		case "subscript":
			o, c = "<sub>", "</sub>"
		case "superscript":
			o, c = "<sup>", "</sup>"
		case "link":
			if t := h.href(strAttr(m.Attrs, "href")); t != "" {
				o, c = `<a href="`+esc(t)+`" rel="noopener noreferrer">`, "</a>"
			}
		case "textColor":
			if color := strAttr(m.Attrs, "color"); color != "" {
				o, c = `<span class="color-`+esc(color)+`">`, "</span>"
			}
		case "highlight":
			o, c = `<mark class="highlight-`+esc(strAttr(m.Attrs, "color"))+`">`, "</mark>"
		}
		open += o
		close = c + close
	}
	h.b.WriteString(open + esc(n.Text) + close)
}
