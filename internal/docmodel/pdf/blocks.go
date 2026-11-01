// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"strconv"
	"strings"

	"kogge.de/kogge/internal/docmodel"
)

// block is a part of the layout of a section: a box, or a grid of cells.
type block any

// box is a part of the layout that is not broken across sheets: a line of text, an image, a rule, some space.
type box struct {
	h float64
	// draw draws the box at the top of its place; x is the left edge of the flow the box belongs to.
	draw func(x, y float64)
	// keep keeps the box on the sheet of the box after it (headings stay with what they head).
	keep bool
	// space is space between blocks, which is left out at the top of a sheet.
	space bool
	// decor are the backgrounds and bars of the blocks around the box (a panel, a quote), drawn behind it.
	decor   []decor
	anchors []string // destinations of links at the top of the box
	outline *outline // a bookmark at the top of the box
	// baseline of the box's line of text (list markers align with it), or 0.
	baseline float64
	// group is a row of a table that is drawn whole (tables in table cells and columns).
	group *group
}

// decor is the part of a background or bar of a block that lies behind one of its boxes; consecutive boxes join
// them, also across sheets.
type decor struct {
	x, w float64 // relative to the flow's left edge
	fill *rgb
	bar  *rgb // a bar of width barW at the left edge
	barW float64
}

type outline struct {
	title string
	level int
}

// flow is where blocks are laid out: indentation and width (relative to the left edge of the text, or of a
// cell), the decorations of the blocks around, the style of text, and whether tables are rows of boxes rather
// than grids (inside table cells and columns, which break across sheets only between their boxes).
type flow struct {
	x, w   float64
	decor  []decor
	text   style
	nested bool
	list   int // depth of lists
}

func (fl flow) indent(d float64) flow {
	fl.x += d
	fl.w -= d
	return fl
}

func (fl flow) with(d decor) flow {
	fl.decor = append(append([]decor(nil), fl.decor...), d)
	return fl
}

func (r *renderer) spaceBox(h float64, fl flow) *box {
	return &box{h: h, space: true, decor: fl.decor}
}

// layout lays out blocks one below the other, with space between them.
func (r *renderer) layout(nodes []*docmodel.Node, fl flow) []block {
	var out []block
	for _, n := range nodes {
		bs := r.node(n, fl)
		if len(bs) == 0 {
			continue
		}
		if len(out) > 0 {
			gap := 2.8
			if n.Type == "heading" {
				gap = 5
			}
			out = append(out, r.spaceBox(gap, fl))
		}
		out = append(out, bs...)
	}
	return out
}

// Font sizes of headings by level.
var headingSizes = map[int64]float64{1: 18, 2: 15, 3: 13, 4: 11.5, 5: 10.5, 6: 10.5}

func (r *renderer) node(n *docmodel.Node, fl flow) []block {
	switch n.Type {
	case "paragraph":
		return blocksOf(r.textBlock(n.Content, fl, fl.text, strAttr(n, "align")))
	case "heading":
		return r.heading(n, fl)
	case "blockquote":
		inner := fl.with(decor{x: fl.x, w: fl.w, bar: &ruleGray, barW: 0.9}).indent(4.5)
		inner.text.color, inner.text.italic = dimmed, true
		return r.layout(n.Content, inner)
	case "panel":
		colors, ok := panelColors[strAttr(n, "kind")]
		if !ok {
			colors = panelColors["info"]
		}
		bg, bar := colors[0], colors[1]
		inner := fl.with(decor{x: fl.x, w: fl.w, fill: &bg, bar: &bar, barW: 1.1})
		content := inner.indent(4.6)
		content.w -= 3
		out := []block{&box{h: 3, decor: inner.decor}}
		out = append(out, r.layout(n.Content, content)...)
		return append(out, &box{h: 3, decor: inner.decor})
	case "expand":
		title := r.textBlock([]*docmodel.Node{{Type: "text", Text: "▼ " + strAttr(n, "title")}}, fl,
			style{family: sans, bold: true, size: fl.text.size, color: fl.text.color}, "")
		var out []block
		for _, b := range title {
			b.keep = true
			out = append(out, b)
		}
		out = append(out, r.spaceBox(1.5, fl))
		return append(out, r.layout(n.Content, fl.with(decor{x: fl.x, w: fl.w, bar: &ruleGray, barW: 0.5}).indent(4))...)
	case "bulletList", "orderedList", "taskList":
		return r.list(n, fl)
	case "codeBlock":
		var text strings.Builder
		for _, c := range n.Content {
			text.WriteString(c.Text)
		}
		if strings.EqualFold(strings.TrimSpace(strAttr(n, "language")), "mermaid") {
			return r.diagram(text.String(), fl)
		}
		return r.code(text.String(), strAttr(n, "language"), strAttr(n, "title"), fl, codeBg, nil)
	case "table":
		return r.table(n, fl)
	case "columns":
		return r.columns(n, fl)
	case "image":
		return r.imageBlock(n, fl)
	case "horizontalRule":
		return []block{&box{h: 6, decor: fl.decor, draw: func(x, y float64) {
			r.stroke(ruleGray, 0.35)
			r.f.Line(x+fl.x, y+3, x+fl.x+fl.w, y+3)
		}}}
	case "macro":
		if strAttr(n, "name") == "mermaid" {
			params, _ := n.Attrs["params"].(map[string]any)
			code, _ := params["code"].(string)
			return r.diagram(code, fl)
		}
		if len(n.Content) > 0 {
			return r.layout(n.Content, fl)
		}
		return r.placeholder("["+strAttr(n, "name")+"]", fl)
	case "unsupported":
		return r.placeholder("["+strings.TrimSpace(strAttr(n, "source")+" "+strAttr(n, "name"))+"]", fl)
	case "math":
		return r.formula(n, fl)
	}
	return r.layout(n.Content, fl)
}

func blocksOf(boxes []*box) []block {
	out := make([]block, len(boxes))
	for i, b := range boxes {
		out[i] = b
	}
	return out
}

// textBlock lays out inline content as lines of boxes.
func (r *renderer) textBlock(content []*docmodel.Node, fl flow, st style, align string) []*box {
	lines := r.lines(r.pieces(r.runs(content, st)), fl.w, st.size)
	out := make([]*box, len(lines))
	for i, ln := range lines {
		ln := ln
		out[i] = &box{h: ln.h, decor: fl.decor, baseline: ln.base, draw: func(x, y float64) {
			r.drawLine(ln, x+fl.x, y, fl.w, align)
		}}
	}
	return out
}

func (r *renderer) heading(n *docmodel.Node, fl flow) []block {
	lvl := intAttr(n, "level")
	size, ok := headingSizes[lvl]
	if !ok {
		lvl, size = 2, headingSizes[2]
	}
	st := fl.text
	st.size, st.bold = size, true
	if lvl >= 5 {
		st.color = dimmed
	}
	boxes := r.textBlock(n.Content, fl, st, strAttr(n, "align"))
	if len(boxes) == 0 {
		return nil
	}
	// Bookmarks nest by the levels of the headings, without skipping a level.
	for len(r.headings) > 0 && r.headings[len(r.headings)-1] >= int(lvl) {
		r.headings = r.headings[:len(r.headings)-1]
	}
	level := r.headingLevel + 1 + len(r.headings)
	r.headings = append(r.headings, int(lvl))
	boxes[0].anchors = []string{r.section + "#" + docmodel.HeadingID(n)}
	if title := strings.TrimSpace(docmodel.NodeText(n)); title != "" {
		boxes[0].outline = &outline{title: title, level: level}
	}
	for _, b := range boxes {
		b.keep = true
	}
	return blocksOf(boxes)
}

// Bullets of lists by depth.
var bullets = []string{"•", "◦", "▪"}

func (r *renderer) list(n *docmodel.Node, fl flow) []block {
	const indent = 6.5
	start := intAttr(n, "start")
	if start < 1 {
		start = 1
	}
	inner := fl.indent(indent)
	inner.list++
	var out []block
	for i, item := range n.Content {
		bs := r.layout(item.Content, inner)
		if len(bs) == 0 {
			bs = []block{&box{h: fl.text.size * ptMM * lineFactor, decor: fl.decor, baseline: fl.text.size * ptMM * 1.1}}
		}
		if i > 0 {
			out = append(out, r.spaceBox(1.2, fl))
		}
		first, ok := bs[0].(*box)
		if ok {
			draw, base := first.draw, first.baseline
			if base == 0 {
				base = fl.text.size * ptMM * 1.1
			}
			st := fl.text
			var marker func(x, y float64)
			switch {
			case item.Type == "taskItem":
				checked, _ := item.Attrs["checked"].(bool)
				marker = func(x, y float64) { r.checkbox(x+fl.x+indent-5, y+base, checked) }
			case n.Type == "orderedList":
				num := strconv.FormatInt(start+int64(i), 10) + "."
				marker = func(x, y float64) {
					r.text(num, st, x+fl.x+indent-1.8-r.width(num, st), y+base)
				}
			default:
				bullet := bullets[min(fl.list, len(bullets)-1)]
				marker = func(x, y float64) {
					r.text(bullet, st, x+fl.x+indent-1.8-r.width(bullet, st), y+base)
				}
			}
			first.draw = func(x, y float64) {
				marker(x, y)
				if draw != nil {
					draw(x, y)
				}
			}
		}
		out = append(out, bs...)
	}
	return out
}

// checkbox draws the box of a task, with its tick when done.
func (r *renderer) checkbox(x, baseline float64, checked bool) {
	const s = 3.0
	y := baseline - s + 0.4
	r.stroke(dimmed, 0.3)
	if checked {
		r.fill(rgb{34, 139, 230})
		r.f.SetDrawColor(34, 139, 230)
		r.f.RoundedRect(x, y, s, s, 0.5, "1234", "FD")
		r.f.SetDrawColor(255, 255, 255)
		r.f.SetLineWidth(0.45)
		r.f.Line(x+0.65, y+1.6, x+1.3, y+2.3)
		r.f.Line(x+1.3, y+2.3, x+2.4, y+0.8)
		return
	}
	r.f.RoundedRect(x, y, s, s, 0.5, "1234", "D")
}

// code lays out preformatted text in a monospaced font on a background; long lines continue on the next line.
func (r *renderer) code(text, language, title string, fl flow, bg rgb, note []*box) []block {
	const pad = 3.0
	st := style{family: mono, size: 8.5, color: black}
	inner := fl.with(decor{x: fl.x, w: fl.w, fill: &bg})
	var out []block
	if title != "" {
		for _, b := range r.textBlock([]*docmodel.Node{{Type: "text", Text: title}}, fl,
			style{family: sans, bold: true, size: 8.5, color: dimmed}, "") {
			b.keep = true
			out = append(out, b)
		}
		out = append(out, &box{h: 1, keep: true, decor: fl.decor})
	}
	out = append(out, &box{h: pad, decor: inner.decor})
	for _, b := range note {
		b.decor = inner.decor
		out = append(out, b)
	}
	lineH := st.size * ptMM * 1.4
	for _, pieces := range r.codeLines(highlight(language, text), st, fl.w-2*pad) {
		out = append(out, &box{h: lineH, decor: inner.decor, draw: func(x, y float64) {
			px := x + fl.x + pad
			for _, p := range pieces {
				r.text(p.text, p.st, px, y+lineH*0.75)
				px += p.w
			}
		}})
	}
	return append(out, &box{h: pad, decor: inner.decor})
}

// expandTabs turns tabs into spaces, to the next multiple of four.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, c := range s {
		if c == '\t' {
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(c)
		col++
	}
	return b.String()
}

// diagram shows the source of a Mermaid diagram (drawing it needs a browser), with a note why.
func (r *renderer) diagram(source string, fl flow) []block {
	note := r.textBlock([]*docmodel.Node{{Type: "text", Text: r.o.Labels.DiagramNote}}, fl.indent(3),
		style{family: sans, italic: true, size: 8, color: dimmed}, "")
	note = append(note, &box{h: 1.5})
	return r.code(source, "", r.o.Labels.Diagram, fl, rgb{248, 249, 250}, note)
}

// formula shows the LaTeX of a formula on lines of its own (typesetting it needs a browser) in a box with its title,
// as diagrams show their source.
func (r *renderer) formula(n *docmodel.Node, fl flow) []block {
	latex := docmodel.FormulaLatex(n)
	if latex == "" {
		return nil
	}
	return r.code(latex, "", r.o.Labels.Formula, fl, rgb{248, 249, 250}, nil)
}

// placeholder is grey text for what the PDF cannot show.
func (r *renderer) placeholder(text string, fl flow) []block {
	st := fl.text
	st.color, st.italic = dimmed, true
	return blocksOf(r.textBlock([]*docmodel.Node{{Type: "text", Text: text}}, fl, st, ""))
}

func (r *renderer) imageBlock(n *docmodel.Node, fl flow) []block {
	img := r.image(intAttr(n, "attachment"), strAttr(n, "src"))
	alt := strAttr(n, "alt")
	switch {
	case img == nil:
		if alt == "" {
			return nil
		}
		return r.placeholder("["+alt+"]", fl)
	case img.svgName != "":
		label := strings.Replace(r.o.Labels.SVG, "%s", "„"+img.svgName+"“", 1)
		st := fl.text
		st.color, st.italic = dimmed, true
		if img.url != "" {
			st.link = &Target{URL: img.url}
			st.color = linkBlue
		}
		return blocksOf(r.textBlock([]*docmodel.Node{{Type: "text", Text: label}}, fl, st, ""))
	}
	sized := img.sized(float64(intAttr(n, "width"))*pxMM, fl.w, r.contentHeight()*0.85)
	x := fl.x
	switch strAttr(n, "align") {
	case "center":
		x += (fl.w - sized.w) / 2
	case "right":
		x += fl.w - sized.w
	}
	return []block{&box{h: sized.h, decor: fl.decor, draw: func(ox, y float64) {
		r.drawImage(sized, ox+x, y)
	}}}
}
