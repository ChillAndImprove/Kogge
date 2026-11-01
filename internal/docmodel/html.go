// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Importers convert rich text of other products into documents: HTML as rendered by Jira, and the
// storage format of Confluence (XHTML with macros, see confluence.go). Both use the same converter; references
// to users, issues, pages and attachments are resolved by the caller.

// Resolver for imported content. Every function may be nil.
type ImportResolver struct {
	// Link maps a link target; it may turn the link into an issue node (issueKey set) or a user mention.
	Link func(href string) (newHref, issueKey string, userID int64, userLabel string)
	// User maps a user reference (user name, key or account id) to a Kogge user.
	User func(ref string) (id int64, label string)
	// Attachment maps an attachment (by file name, of the current or another page) to its id and URL.
	Attachment func(filename, page, space string) (id int64, url string)
	// Page maps a page reference (title and space key) to a Kogge page id and URL.
	Page func(title, space string) (id int64, url string)
	// Issue reports whether an issue key exists (keys of unknown projects stay text).
	Issue func(key string) bool
	// Anchor maps an inline comment marker of the source to an anchor id.
	Anchor func(ref string) string
}

// FromHTML converts HTML (e.g. Jira's rendered fields) into a document.
func FromHTML(source string, r ImportResolver) *Doc {
	root, err := html.Parse(strings.NewReader("<html><body>" + source + "</body></html>"))
	if err != nil {
		return Paragraphs(source)
	}
	body := findBody(root)
	c := &converter{r: r}
	d := &Doc{Type: "doc", Version: Version, Content: c.blocks(children(body))}
	return finish(d)
}

func findBody(root *html.Node) *html.Node {
	if b := findTag(root, "body"); b != nil {
		return b
	}
	return root
}

func children(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, c)
	}
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// finish validates the result; on failure (which would be a converter bug) the text is kept as paragraphs.
func finish(d *Doc) *Doc {
	if len(d.Content) == 0 {
		d.Content = []*Node{{Type: "paragraph"}}
	}
	if err := Validate(d, Options{AllowExternalImages: true}); err != nil {
		return Paragraphs(PlainText(d))
	}
	return d
}

type converter struct {
	r ImportResolver
	// unsupported counts macros that could not be converted, by name.
	unsupported map[string]int
}

var blockTags = map[string]bool{"p": true, "div": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
	"blockquote": true, "pre": true, "hr": true, "section": true, "article": true, "header": true, "footer": true, "figure": true,
	"dl": true, "dt": true, "dd": true, "img": true}

func isBlock(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if blockTags[n.Data] {
		return true
	}
	return strings.HasPrefix(n.Data, "ac:structured-macro") || n.Data == "ac:task-list" || n.Data == "ac:layout" ||
		n.Data == "ac:layout-section" || n.Data == "ac:layout-cell" || n.Data == "ac:image"
}

// blocks converts a list of nodes into block nodes; inline runs become paragraphs.
func (c *converter) blocks(nodes []*html.Node) []*Node {
	var out []*Node
	var inline []*html.Node
	flush := func() {
		if len(inline) == 0 {
			return
		}
		out = append(out, paragraphs(c.inlines(inline, nil), nil)...)
		inline = nil
	}
	for _, n := range nodes {
		if isBlock(n) {
			flush()
			out = append(out, c.block(n)...)
			continue
		}
		inline = append(inline, n)
	}
	flush()
	return out
}

func (c *converter) block(n *html.Node) []*Node {
	switch n.Data {
	case "p":
		var attrs map[string]any
		if a := alignOf(n); a != "" {
			attrs = map[string]any{"align": a}
		}
		return paragraphs(c.inlines(children(n), nil), attrs)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		content := trimInline(imagesAsLinks(c.inlines(children(n), nil)))
		if len(content) == 0 {
			return nil
		}
		level, _ := strconv.Atoi(n.Data[1:])
		return []*Node{{Type: "heading", Attrs: map[string]any{"level": int64(level)}, Content: content}}
	case "hr":
		return []*Node{{Type: "horizontalRule"}}
	case "blockquote":
		return []*Node{wrapBlocks("blockquote", c.blocks(children(n)))}
	case "pre":
		lang := ""
		for _, cl := range strings.Fields(attr(n, "class")) {
			if l, ok := strings.CutPrefix(cl, "code-"); ok {
				lang = l
			} else if l, ok := strings.CutPrefix(cl, "language-"); ok {
				lang = l
			}
		}
		return []*Node{codeBlock(textOf(n), lang, "")}
	case "ul", "ol":
		return []*Node{c.list(n)}
	case "li":
		return []*Node{wrapBlocks("bulletList", []*Node{c.listItem(n, false)})}
	case "table":
		return []*Node{c.table(n)}
	case "img":
		if img := c.image(attr(n, "src"), attr(n, "alt")); img != nil {
			return []*Node{img}
		}
		return nil
	case "div":
		switch {
		case hasClass(n, "code") && hasClass(n, "panel"), hasClass(n, "preformatted"):
			// Jira's {code} and {noformat}: the pre inside carries the text.
			if pre := findTag(n, "pre"); pre != nil {
				return c.block(pre)
			}
		case hasClass(n, "panel"):
			var body []*Node
			for _, ch := range children(n) {
				if ch.Type == html.ElementNode && hasClass(ch, "panelHeader") {
					if t := strings.TrimSpace(textOf(ch)); t != "" {
						body = append(body, &Node{Type: "paragraph", Content: []*Node{{Type: "text", Text: t, Marks: []*Mark{{Type: "bold"}}}}})
					}
					continue
				}
				body = append(body, c.blocks([]*html.Node{ch})...)
			}
			return []*Node{panel("info", body)}
		}
		return c.blocks(children(n))
	}
	if strings.HasPrefix(n.Data, "ac:") {
		return c.confluenceBlock(n)
	}
	return c.blocks(children(n))
}

func findTag(n *html.Node, tag string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == tag {
			return ch
		}
		if f := findTag(ch, tag); f != nil {
			return f
		}
	}
	return nil
}

func alignOf(n *html.Node) string {
	style := strings.ToLower(attr(n, "style"))
	for _, a := range []string{"center", "right", "justify"} {
		if strings.Contains(style, "text-align: "+a) || strings.Contains(style, "text-align:"+a) {
			return a
		}
	}
	return ""
}

func wrapBlocks(typ string, content []*Node) *Node {
	if len(content) == 0 {
		content = []*Node{{Type: "paragraph"}}
	}
	return &Node{Type: typ, Content: content}
}

func panel(kind string, content []*Node) *Node {
	n := wrapBlocks("panel", content)
	n.Attrs = map[string]any{"kind": kind}
	return n
}

func codeBlock(text, lang, title string) *Node {
	n := &Node{Type: "codeBlock"}
	text = strings.TrimSuffix(text, "\n")
	if text != "" {
		n.Content = []*Node{{Type: "text", Text: text}}
	}
	attrs := map[string]any{}
	if lang != "" {
		attrs["language"] = lang
	}
	if title != "" {
		attrs["title"] = title
	}
	if len(attrs) > 0 {
		n.Attrs = attrs
	}
	return n
}

// textOf returns the raw text of a subtree (CDATA included), for code blocks.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.CommentNode:
			if s, ok := strings.CutPrefix(n.Data, "[CDATA["); ok {
				b.WriteString(strings.TrimSuffix(s, "]]"))
			}
		case html.ElementNode:
			if n.Data == "br" {
				b.WriteByte('\n')
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

func (c *converter) list(n *html.Node) *Node {
	typ := "bulletList"
	if n.Data == "ol" {
		typ = "orderedList"
	}
	task := false
	var items []*Node
	for _, ch := range children(n) {
		if ch.Type != html.ElementNode || ch.Data != "li" {
			if ch.Type == html.ElementNode && (ch.Data == "ul" || ch.Data == "ol") && len(items) > 0 {
				last := items[len(items)-1]
				last.Content = append(last.Content, c.list(ch))
			}
			continue
		}
		cb := checkboxOf(ch)
		task = task || cb != nil
		items = append(items, c.listItem(ch, cb != nil && *cb))
	}
	if task {
		typ = "taskList"
		for _, it := range items {
			it.Type = "taskItem"
			if it.Attrs == nil {
				it.Attrs = map[string]any{"checked": false}
			}
		}
	}
	if len(items) == 0 {
		items = []*Node{{Type: "listItem", Content: []*Node{{Type: "paragraph"}}}}
	}
	out := &Node{Type: typ, Content: items}
	if typ == "orderedList" {
		if s, err := strconv.Atoi(attr(n, "start")); err == nil && s > 1 {
			out.Attrs = map[string]any{"start": int64(s)}
		}
	}
	return out
}

func checkboxOf(li *html.Node) *bool {
	for ch := li.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == "input" && strings.EqualFold(attr(ch, "type"), "checkbox") {
			checked := false
			for _, a := range ch.Attr {
				if a.Key == "checked" {
					checked = true
				}
			}
			return &checked
		}
	}
	return nil
}

func (c *converter) listItem(li *html.Node, checked bool) *Node {
	content := c.blocks(children(li))
	if len(content) == 0 || content[0].Type != "paragraph" {
		content = append([]*Node{{Type: "paragraph"}}, content...)
	}
	n := &Node{Type: "listItem", Content: content}
	if checked {
		n.Attrs = map[string]any{"checked": true}
	}
	return n
}

func (c *converter) table(t *html.Node) *Node {
	var rows []*Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type != html.ElementNode {
				continue
			}
			switch ch.Data {
			case "thead", "tbody", "tfoot":
				walk(ch)
			case "tr":
				var cells []*Node
				for cell := ch.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type != html.ElementNode || (cell.Data != "td" && cell.Data != "th") {
						continue
					}
					typ := "tableCell"
					if cell.Data == "th" {
						typ = "tableHeader"
					}
					content := c.blocks(children(cell))
					if len(content) == 0 {
						content = []*Node{{Type: "paragraph"}}
					}
					node := &Node{Type: typ, Content: content}
					attrs := map[string]any{}
					if v, err := strconv.Atoi(attr(cell, "colspan")); err == nil && v > 1 {
						attrs["colspan"] = int64(v)
					}
					if v, err := strconv.Atoi(attr(cell, "rowspan")); err == nil && v > 1 {
						attrs["rowspan"] = int64(v)
					}
					if len(attrs) > 0 {
						node.Attrs = attrs
					}
					cells = append(cells, node)
				}
				if len(cells) > 0 {
					rows = append(rows, &Node{Type: "tableRow", Content: cells})
				}
			}
		}
	}
	walk(t)
	if len(rows) == 0 {
		return &Node{Type: "paragraph"}
	}
	return &Node{Type: "table", Content: rows}
}

// paragraphs builds paragraphs of inline content, splitting it around images and formulas of their own, which are
// blocks.
func paragraphs(content []*Node, attrs map[string]any) []*Node {
	var out, run []*Node
	flush := func() {
		if run = trimInline(run); len(run) > 0 {
			out = append(out, &Node{Type: "paragraph", Attrs: attrs, Content: run})
		}
		run = nil
	}
	for _, n := range content {
		if n.Type == "image" || n.Type == "math" {
			flush()
			out = append(out, n)
			continue
		}
		run = append(run, n)
	}
	flush()
	return out
}

// imagesAsLinks replaces images by links where only inline content is allowed (and formulas of their own by
// formulas within text).
func imagesAsLinks(content []*Node) []*Node {
	out := content[:0]
	for _, n := range content {
		if n.Type == "math" {
			n = &Node{Type: "inlineMath", Attrs: n.Attrs}
		}
		if n.Type == "image" {
			src, _ := n.Attrs["src"].(string)
			label, _ := n.Attrs["alt"].(string)
			if label == "" {
				label = src
			}
			if src == "" || !SafeLink(src) {
				continue
			}
			n = &Node{Type: "text", Text: label, Marks: []*Mark{{Type: "link", Attrs: map[string]any{"href": src}}}}
		}
		out = append(out, n)
	}
	return out
}

func (c *converter) image(src, alt string) *Node {
	attrs := map[string]any{}
	if alt != "" {
		attrs["alt"] = alt
	}
	if src == "" {
		return nil
	}
	if c.r.Link != nil {
		// The source system's address of an attachment or thumbnail.
		if href, _, _, _ := c.r.Link(src); href != "" {
			attrs["src"] = href
			return &Node{Type: "image", Attrs: attrs}
		}
	}
	if c.r.Attachment != nil {
		name := src
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if q := strings.IndexAny(name, "?#"); q >= 0 {
			name = name[:q]
		}
		if id, url := c.r.Attachment(name, "", ""); id != 0 {
			attrs["attachment"] = int64(id)
			attrs["src"] = url
			return &Node{Type: "image", Attrs: attrs}
		}
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || (strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//")) {
		attrs["src"] = src
		return &Node{Type: "image", Attrs: attrs}
	}
	return nil
}

// --- inline content ---

var markTags = map[string]string{"strong": "bold", "b": "bold", "em": "italic", "i": "italic", "cite": "italic", "u": "underline",
	"ins": "underline", "s": "strike", "del": "strike", "strike": "strike", "code": "code", "tt": "code", "kbd": "code",
	"sub": "subscript", "sup": "superscript", "mark": "highlight"}

func addMark(marks []*Mark, m *Mark) []*Mark {
	for _, x := range marks {
		if x.Type == m.Type && m.Type != "anchor" {
			return marks
		}
	}
	return append(append([]*Mark{}, marks...), m)
}

func (c *converter) inlines(nodes []*html.Node, marks []*Mark) []*Node {
	var out []*Node
	for _, n := range nodes {
		out = append(out, c.inline(n, marks)...)
	}
	return out
}

var spaceReplacer = strings.NewReplacer("\n", " ", "\t", " ", "\r", " ", " ", " ")

func collapse(s string) string {
	s = spaceReplacer.Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func (c *converter) inline(n *html.Node, marks []*Mark) []*Node {
	switch n.Type {
	case html.TextNode:
		t := collapse(n.Data)
		if t == "" {
			return nil
		}
		return []*Node{{Type: "text", Text: t, Marks: marks}}
	case html.CommentNode:
		if s, ok := strings.CutPrefix(n.Data, "[CDATA["); ok {
			if t := collapse(strings.TrimSuffix(s, "]]")); t != "" {
				return []*Node{{Type: "text", Text: t, Marks: marks}}
			}
		}
		return nil
	case html.ElementNode:
	default:
		return nil
	}
	if m, ok := markTags[n.Data]; ok {
		return c.inlines(children(n), addMark(marks, &Mark{Type: m}))
	}
	switch n.Data {
	case "br":
		return []*Node{{Type: "hardBreak"}}
	case "a":
		return c.anchorLink(n, marks)
	case "img":
		if hasClass(n, "emoticon") {
			if alt := attr(n, "alt"); alt != "" {
				return []*Node{{Type: "text", Text: alt, Marks: marks}}
			}
			return nil
		}
		// Images are blocks: the paragraph around them is split (see paragraphs).
		if img := c.image(attr(n, "src"), attr(n, "alt")); img != nil {
			return []*Node{img}
		}
		if alt := attr(n, "alt"); alt != "" {
			return []*Node{{Type: "text", Text: alt, Marks: marks}}
		}
		return nil
	case "span", "font":
		if color := colorOf(n); color != "" {
			marks = addMark(marks, &Mark{Type: "textColor", Attrs: map[string]any{"color": color}})
		}
		return c.inlines(children(n), marks)
	case "time":
		if v := attr(n, "datetime"); len(v) >= 10 {
			return []*Node{{Type: "date", Attrs: map[string]any{"value": v[:10]}}}
		}
	case "input", "script", "style":
		return nil
	}
	if strings.HasPrefix(n.Data, "ac:") || strings.HasPrefix(n.Data, "ri:") {
		return c.confluenceInline(n, marks)
	}
	return c.inlines(children(n), marks)
}

// colorOf maps an inline colour style to the document palette.
func colorOf(n *html.Node) string {
	style := strings.ToLower(attr(n, "style"))
	if !strings.Contains(style, "color") {
		return ""
	}
	for name, hints := range map[string][]string{
		"red":    {"red", "#ff0000", "#de350b", "#bf2600", "#d04437", "rgb(255,0,0)", "rgb(255, 0, 0)"},
		"orange": {"orange", "#ff8b00", "#ff991f"},
		"yellow": {"#ffc400", "#ffab00", "yellow"},
		"green":  {"green", "#00875a", "#14892c", "#36b37e", "#008000"},
		"blue":   {"blue", "#0052cc", "#0065ff", "#3572b0", "#0000ff"},
		"violet": {"purple", "#6554c0", "#5243aa", "#800080"},
		"gray":   {"gray", "grey", "#97a0af", "#707070", "#999999"},
	} {
		for _, h := range hints {
			if strings.Contains(style, "color: "+h) || strings.Contains(style, "color:"+h) {
				return name
			}
		}
	}
	return ""
}

func (c *converter) anchorLink(n *html.Node, marks []*Mark) []*Node {
	href := attr(n, "href")
	// Jira user and issue links.
	if c.r.User != nil && (hasClass(n, "user-hover") || strings.Contains(href, "ViewProfile.jspa")) {
		ref := attr(n, "rel")
		if ref == "" {
			if i := strings.Index(href, "name="); i >= 0 {
				ref = href[i+5:]
			}
		}
		if id, label := c.r.User(ref); id != 0 {
			return []*Node{{Type: "mention", Attrs: map[string]any{"user": id, "label": label}}}
		}
	}
	if key := attr(n, "data-issue-key"); key != "" && IsIssueKey(key) && (c.r.Issue == nil || c.r.Issue(key)) {
		return []*Node{{Type: "issue", Attrs: map[string]any{"key": key}}}
	}
	if c.r.Link != nil && href != "" {
		newHref, key, uid, ulabel := c.r.Link(href)
		switch {
		case key != "":
			return []*Node{{Type: "issue", Attrs: map[string]any{"key": key}}}
		case uid != 0:
			return []*Node{{Type: "mention", Attrs: map[string]any{"user": uid, "label": ulabel}}}
		case newHref != "":
			href = newHref
		}
	}
	content := c.inlines(children(n), marks)
	if !SafeLink(href) {
		return content
	}
	for _, x := range content {
		if x.Type == "text" {
			x.Marks = addMark(x.Marks, &Mark{Type: "link", Attrs: map[string]any{"href": href}})
		}
	}
	return content
}

// trimInline removes leading and trailing spaces of a paragraph and merges adjacent text with equal marks.
func trimInline(content []*Node) []*Node {
	content = mergeText(content)
	for len(content) > 0 && content[0].Type == "text" {
		content[0].Text = strings.TrimLeft(content[0].Text, " ")
		if content[0].Text != "" {
			break
		}
		content = content[1:]
	}
	for len(content) > 0 && content[len(content)-1].Type == "text" {
		last := content[len(content)-1]
		last.Text = strings.TrimRight(last.Text, " ")
		if last.Text != "" {
			break
		}
		content = content[:len(content)-1]
	}
	for len(content) > 0 && content[len(content)-1].Type == "hardBreak" {
		content = content[:len(content)-1]
	}
	out := content[:0]
	for _, n := range content {
		if n.Type == "text" && n.Text == "" {
			continue
		}
		out = append(out, n)
	}
	return out
}
