// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// FromConfluence converts Confluence storage format (XHTML with ac: and ri: elements) into a document. It also
// returns the names of macros that could not be converted (kept as unsupported nodes), with their counts.
func FromConfluence(storage string, r ImportResolver) (*Doc, map[string]int) {
	root, err := html.Parse(strings.NewReader("<html><body>" + escapeCDATA(storage) + "</body></html>"))
	if err != nil {
		return Paragraphs(storage), map[string]int{}
	}
	c := &converter{r: r, unsupported: map[string]int{}}
	d := &Doc{Type: "doc", Version: Version, Content: liftColumns(c.blocks(children(findBody(root))), true)}
	return finish(d), c.unsupported
}

// LiftColumns keeps layouts at the top level of blocks, where documents have them: columns nested elsewhere
// (in imported content, or where content of another page is put into a page) become their content, one column
// after another.
func LiftColumns(nodes []*Node) []*Node { return liftColumns(nodes, true) }

func liftColumns(nodes []*Node, top bool) []*Node {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Type != "columns" {
			n.Content = liftColumns(n.Content, false)
			out = append(out, n)
			continue
		}
		for _, col := range n.Content {
			col.Content = liftColumns(col.Content, false)
			if !top {
				out = append(out, col.Content...)
			}
		}
		if top {
			out = append(out, n)
		}
	}
	return out
}

// Layouts of Confluence's page sections with two or three cells; sections with one cell, or more than three,
// keep their content one block after another.
var sectionLayouts = map[string]string{"two_equal": "equal", "two_left_sidebar": "left", "two_right_sidebar": "right",
	"three_equal": "equal", "three_with_sidebars": "sides"}

func (c *converter) layoutSection(n *html.Node) []*Node {
	var cells []*html.Node
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == "ac:layout-cell" {
			cells = append(cells, ch)
		}
	}
	if len(cells) < 2 || len(cells) > 3 {
		return c.blocks(children(n))
	}
	layout := sectionLayouts[attr(n, "ac:type")]
	if _, ok := columnLayouts[layout][len(cells)]; !ok {
		layout = "equal"
	}
	cols := &Node{Type: "columns", Attrs: map[string]any{"layout": layout}}
	for _, cell := range cells {
		cols.Content = append(cols.Content, wrapBlocks("column", c.blocks(children(cell))))
	}
	return []*Node{cols}
}

// escapeCDATA replaces CDATA sections (code and link bodies) with escaped text: an HTML parser would end them
// at the first ">".
func escapeCDATA(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "<![CDATA[")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i+9:], "]]>")
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(html.EscapeString(s[i+9 : i+9+j]))
		s = s[i+9+j+3:]
	}
}

// macroParams returns the ac:parameter children of a structured macro.
func macroParams(n *html.Node) map[string]string {
	out := map[string]string{}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == "ac:parameter" {
			name := attr(ch, "ac:name")
			// Parameters may hold references (e.g. a page, possibly inside ac:link) instead of text.
			if ref := findDeep(ch, "ri:"); ref != nil {
				out[name] = refValue(ref)
				for _, a := range ref.Attr {
					out[name+"."+strings.TrimPrefix(a.Key, "ri:")] = a.Val
				}
				continue
			}
			out[name] = strings.TrimSpace(textOf(ch))
		}
	}
	return out
}

func findPrefixed(n *html.Node, prefix string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && strings.HasPrefix(ch.Data, prefix) {
			return ch
		}
	}
	return nil
}

func findDeep(n *html.Node, prefix string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && strings.HasPrefix(ch.Data, prefix) {
			return ch
		}
		if f := findDeep(ch, prefix); f != nil {
			return f
		}
	}
	return nil
}

func refValue(ref *html.Node) string {
	for _, k := range []string{"ri:content-title", "ri:value", "ri:filename", "ri:userkey", "ri:username", "ri:account-id", "ri:space-key"} {
		if v := attr(ref, k); v != "" {
			return v
		}
	}
	return ""
}

func macroBody(n *html.Node, tag string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == tag {
			return ch
		}
	}
	return nil
}

var panelKinds = map[string]string{"info": "info", "note": "note", "warning": "warning", "tip": "success", "panel": "info"}

var statusColours = map[string]string{"grey": "gray", "gray": "gray", "red": "red", "yellow": "yellow", "green": "green", "blue": "blue",
	"purple": "violet"}

func (c *converter) confluenceBlock(n *html.Node) []*Node {
	switch n.Data {
	case "ac:structured-macro", "ac:macro":
		return c.macro(n, false)
	case "ac:task-list":
		var items []*Node
		for t := n.FirstChild; t != nil; t = t.NextSibling {
			if t.Type != html.ElementNode || t.Data != "ac:task" {
				continue
			}
			checked := false
			var body []*Node
			for ch := t.FirstChild; ch != nil; ch = ch.NextSibling {
				if ch.Type != html.ElementNode {
					continue
				}
				switch ch.Data {
				case "ac:task-status":
					checked = strings.TrimSpace(textOf(ch)) == "complete"
				case "ac:task-body":
					body = c.blocks(children(ch))
				}
			}
			if len(body) == 0 || body[0].Type != "paragraph" {
				body = append([]*Node{{Type: "paragraph"}}, body...)
			}
			items = append(items, &Node{Type: "taskItem", Attrs: map[string]any{"checked": checked}, Content: body})
		}
		if len(items) == 0 {
			return nil
		}
		return []*Node{{Type: "taskList", Content: items}}
	case "ac:layout", "ac:layout-cell":
		return c.blocks(children(n))
	case "ac:layout-section":
		return c.layoutSection(n)
	case "ac:image":
		if img := c.confluenceImage(n); img != nil {
			return []*Node{img}
		}
		return nil
	}
	return c.blocks(children(n))
}

func (c *converter) confluenceImage(n *html.Node) *Node {
	alt := attr(n, "ac:alt")
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type != html.ElementNode {
			continue
		}
		switch ch.Data {
		case "ri:attachment":
			name := attr(ch, "ri:filename")
			page, space := "", ""
			if p := findPrefixed(ch, "ri:page"); p != nil {
				page, space = attr(p, "ri:content-title"), attr(p, "ri:space-key")
			}
			if c.r.Attachment != nil {
				if id, url := c.r.Attachment(name, page, space); id != 0 {
					attrs := map[string]any{"attachment": id, "src": url}
					if alt != "" {
						attrs["alt"] = alt
					}
					if w := attr(n, "ac:width"); w != "" {
						if v := atoiSafe(w); v > 0 {
							attrs["width"] = int64(v)
						}
					}
					return &Node{Type: "image", Attrs: attrs}
				}
			}
		case "ri:url":
			return c.image(attr(ch, "ri:value"), alt)
		}
	}
	return nil
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// macro converts a structured macro; inline macros produce inline nodes.
func (c *converter) macro(n *html.Node, inline bool) []*Node {
	name := attr(n, "ac:name")
	params := macroParams(n)
	rich := macroBody(n, "ac:rich-text-body")
	plain := macroBody(n, "ac:plain-text-body")
	body := func() []*Node {
		if rich == nil {
			return nil
		}
		return c.blocks(children(rich))
	}
	blockMacro := func(name string, p map[string]any, content []*Node) []*Node {
		attrs := map[string]any{"name": name}
		if len(p) > 0 {
			attrs["params"] = p
		}
		return []*Node{{Type: "macro", Attrs: attrs, Content: content}}
	}
	switch name {
	case "code", "noformat":
		text := ""
		if plain != nil {
			text = textOf(plain)
		}
		return []*Node{codeBlock(text, strings.ToLower(params["language"]), params["title"])}
	case "info", "note", "warning", "tip", "panel":
		content := body()
		if t := params["title"]; t != "" {
			content = append([]*Node{{Type: "paragraph", Content: []*Node{{Type: "text", Text: t, Marks: []*Mark{{Type: "bold"}}}}}}, content...)
		}
		return []*Node{panel(panelKinds[name], content)}
	case "expand":
		n := wrapBlocks("expand", body())
		n.Attrs = map[string]any{"title": params["title"]}
		return []*Node{n}
	case "status":
		colour := statusColours[strings.ToLower(params["colour"])]
		if colour == "" {
			colour = "gray"
		}
		return []*Node{{Type: "status", Attrs: map[string]any{"text": params["title"], "color": colour}}}
	case "anchor":
		if a := params[""]; a != "" {
			return blockMacro("anchor", map[string]any{"name": a}, nil)
		}
		return nil
	case "toc":
		p := map[string]any{}
		if v := atoiSafe(params["maxLevel"]); v > 0 {
			p["maxLevel"] = v
		}
		return blockMacro("toc", p, nil)
	case "children":
		p := map[string]any{}
		if v := atoiSafe(params["depth"]); v > 0 {
			p["depth"] = v
		} else if strings.EqualFold(params["all"], "true") {
			p["depth"] = 5
		}
		return blockMacro("children", p, nil)
	case "pagetree":
		return blockMacro("page-tree", nil, nil)
	case "include", "excerpt-include":
		title, space := params[""], params[".space-key"]
		if title == "" {
			title = params["nopanel"]
		}
		p := map[string]any{}
		if c.r.Page != nil {
			if id, _ := c.r.Page(title, space); id != 0 {
				p["page"] = id
			}
		}
		if len(p) == 0 {
			return c.unknown(n, name)
		}
		return blockMacro(name, p, nil)
	case "excerpt":
		return blockMacro("excerpt", nil, body())
	case "recently-updated":
		p := map[string]any{}
		if v := atoiSafe(params["max"]); v > 0 {
			p["max"] = v
		}
		return blockMacro("recently-updated", p, nil)
	case "contentbylabel", "content-by-label":
		label := params["labels"]
		if label == "" {
			label = params["label"]
		}
		if i := strings.IndexAny(label, ", "); i > 0 {
			label = label[:i]
		}
		if label == "" {
			return c.unknown(n, name)
		}
		return blockMacro("content-by-label", map[string]any{"label": strings.ToLower(label)}, nil)
	case "attachments":
		return blockMacro("attachments", nil, nil)
	case "jira", "jiraissues":
		if key := params["key"]; IsIssueKey(key) {
			if inline {
				return []*Node{{Type: "issue", Attrs: map[string]any{"key": key}}}
			}
			return blockMacro("issue", map[string]any{"key": key, "display": "card"}, nil)
		}
		if q := params["jqlQuery"]; q != "" {
			if strings.EqualFold(params["count"], "true") {
				return blockMacro("issue-count", map[string]any{"query": q}, nil)
			}
			return blockMacro("issue-list", map[string]any{"query": q}, nil)
		}
	case "mathblock", "mathinline":
		return mathMacro(name, params, plain, inline)
	}
	return c.unknown(n, name)
}

// mathMacro converts a formula of the LaTeX Math app: mathblock on lines of its own (where it stands in text, the
// paragraph is split around it, as around images), mathinline within text (in a paragraph of its own where it stands
// between blocks). The LaTeX is the macro's body or its parameter body, which may be URI-encoded. LaTeX too long for
// a formula stays code, or text.
func mathMacro(name string, params map[string]string, plain *html.Node, inline bool) []*Node {
	latex := ""
	if plain != nil {
		latex = textOf(plain)
	}
	if strings.TrimSpace(latex) == "" {
		latex = params["body"]
		if encoded, ok := strings.CutPrefix(latex, "--uriencoded--"); ok {
			latex = encoded
			if s, err := url.QueryUnescape(encoded); err == nil {
				latex = s
			}
		}
	}
	if name == "mathblock" {
		if m := mathNode(displayLatex(latex)); m != nil {
			return []*Node{m}
		}
		return nil
	}
	m := inlineMathNode(strings.TrimSpace(latex))
	switch {
	case m == nil:
		return nil
	case inline:
		return []*Node{m}
	}
	return []*Node{{Type: "paragraph", Content: []*Node{m}}}
}

// unknown keeps a macro Kogge cannot show, with its source, so nothing is lost.
func (c *converter) unknown(n *html.Node, name string) []*Node {
	if c.unsupported != nil {
		c.unsupported[name]++
	}
	var b strings.Builder
	_ = html.Render(&b, n)
	raw := b.String()
	if len(raw) > 1900 {
		raw = raw[:1900]
	}
	return []*Node{{Type: "unsupported", Attrs: map[string]any{"source": "confluence", "name": name, "raw": raw}}}
}

func (c *converter) confluenceInline(n *html.Node, marks []*Mark) []*Node {
	switch n.Data {
	case "ac:structured-macro", "ac:macro":
		out := c.macro(n, true)
		// Block results inside text: keep inline ones and formulas (the paragraph is split around those, see
		// paragraphs), turn others into their text.
		var inline []*Node
		for _, o := range out {
			if nodes[o.Type].inline || o.Type == "math" {
				inline = append(inline, o)
			} else if t := strings.TrimSpace(PlainText(&Doc{Content: []*Node{o}})); t != "" {
				inline = append(inline, &Node{Type: "text", Text: t, Marks: marks})
			}
		}
		return inline
	case "ac:link":
		return c.confluenceLink(n, marks)
	case "ac:emoticon":
		if name := attr(n, "ac:name"); name != "" {
			return []*Node{{Type: "emoji", Attrs: map[string]any{"name": name}}}
		}
		return nil
	case "ac:inline-comment-marker":
		ref := attr(n, "ac:ref")
		if c.r.Anchor != nil && ref != "" {
			if id := c.r.Anchor(ref); id != "" {
				marks = addMark(marks, &Mark{Type: "anchor", Attrs: map[string]any{"id": id}})
			}
		}
		return c.inlines(children(n), marks)
	case "ac:placeholder", "ac:parameter":
		return nil
	case "ac:image":
		// Images are blocks: the paragraph around them is split (see paragraphs).
		if img := c.confluenceImage(n); img != nil {
			return []*Node{img}
		}
		return nil
	}
	return c.inlines(children(n), marks)
}

func (c *converter) confluenceLink(n *html.Node, marks []*Mark) []*Node {
	var label []*Node
	if b := macroBody(n, "ac:plain-text-link-body"); b != nil {
		if t := strings.TrimSpace(textOf(b)); t != "" {
			label = []*Node{{Type: "text", Text: t, Marks: marks}}
		}
	} else if b := macroBody(n, "ac:link-body"); b != nil {
		label = c.inlines(children(b), marks)
	}
	ref := findPrefixed(n, "ri:")
	if ref == nil {
		return label
	}
	href := ""
	text := ""
	switch ref.Data {
	case "ri:user":
		key := attr(ref, "ri:userkey")
		if key == "" {
			key = attr(ref, "ri:username")
		}
		if key == "" {
			key = attr(ref, "ri:account-id")
		}
		if c.r.User != nil {
			if id, l := c.r.User(key); id != 0 {
				return []*Node{{Type: "mention", Attrs: map[string]any{"user": id, "label": l}}}
			}
		}
		text = "@" + key
	case "ri:page", "ri:blog-post":
		title, space := attr(ref, "ri:content-title"), attr(ref, "ri:space-key")
		text = title
		if c.r.Page != nil {
			if _, url := c.r.Page(title, space); url != "" {
				href = url
			}
		}
	case "ri:attachment":
		name := attr(ref, "ri:filename")
		text = name
		if c.r.Attachment != nil {
			page, space := "", ""
			if p := findPrefixed(ref, "ri:page"); p != nil {
				page, space = attr(p, "ri:content-title"), attr(p, "ri:space-key")
			}
			if _, url := c.r.Attachment(name, page, space); url != "" {
				href = url
			}
		}
	case "ri:url":
		href = attr(ref, "ri:value")
		text = href
		if c.r.Link != nil {
			newHref, key, uid, ulabel := c.r.Link(href)
			switch {
			case key != "" && IsIssueKey(key):
				return []*Node{{Type: "issue", Attrs: map[string]any{"key": key}}}
			case uid != 0:
				return []*Node{{Type: "mention", Attrs: map[string]any{"user": uid, "label": ulabel}}}
			case newHref != "":
				href = newHref
			}
		}
	case "ri:space":
		text = attr(ref, "ri:space-key")
	}
	if anchor := attr(n, "ac:anchor"); anchor != "" && href != "" {
		href += "#" + anchor
	}
	if len(label) == 0 && text != "" {
		label = []*Node{{Type: "text", Text: text, Marks: marks}}
	}
	if href == "" || !SafeLink(href) {
		return label
	}
	for _, x := range label {
		if x.Type == "text" {
			x.Marks = addMark(x.Marks, &Mark{Type: "link", Attrs: map[string]any{"href": href}})
		}
	}
	return label
}

// UnsupportedSummary formats unsupported macro counts for reports, most frequent first.
func UnsupportedSummary(counts map[string]int) []string {
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}
