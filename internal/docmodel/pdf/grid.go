// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"sort"
	"strings"

	"kogge.de/kogge/internal/docmodel"
)

// Grids are cells side by side: the rows of a table, and the columns of a layout. A grid consists of groups — a
// row of a table, rows joined by cells that span them, or the columns of a layout — which stay whole on a sheet
// where they fit, and otherwise continue on the next sheet between the boxes of their cells. A table's header
// rows repeat on every sheet it continues on.

type grid struct {
	groups []*group
	header int     // the number of groups at the top that are header rows
	decor  []decor // backgrounds and bars of the blocks around the grid
}

type group struct {
	cells []*cell
	h     float64
}

type cell struct {
	x, w   float64 // relative to the flow's left edge
	top, h float64 // within the group
	pad    float64
	boxes  []*box // laid out at the cell's left edge plus padding
	fill   *rgb
	border bool
}

func (c *cell) contentHeight() float64 {
	h := 2 * c.pad
	for _, b := range c.boxes {
		h += b.h
	}
	return h
}

const cellPad = 1.8

type sourceCell struct {
	node              *docmodel.Node
	row, col          int
	rows, cols        int
	header            bool
	minW, maxW, fixed float64
}

func (r *renderer) table(n *docmodel.Node, fl flow) []block {
	// The cells by row and column, with the spans of merged cells.
	var cells []*sourceCell
	taken := map[[2]int]bool{}
	nrows, ncols := len(n.Content), 0
	for ri, row := range n.Content {
		ci := 0
		for _, c := range row.Content {
			for taken[[2]int{ri, ci}] {
				ci++
			}
			cs, rs := max(1, int(intAttr(c, "colspan"))), max(1, int(intAttr(c, "rowspan")))
			rs = min(rs, nrows-ri)
			for dr := range rs {
				for dc := range cs {
					taken[[2]int{ri + dr, ci + dc}] = true
				}
			}
			cells = append(cells, &sourceCell{node: c, row: ri, col: ci, rows: rs, cols: cs, header: c.Type == "tableHeader"})
			ci += cs
			ncols = max(ncols, ci)
		}
	}
	if ncols == 0 {
		return nil
	}
	widths := r.columnWidths(cells, ncols, fl.w)
	colX := make([]float64, ncols+1)
	for i, w := range widths {
		colX[i+1] = colX[i] + w
	}
	// Each cell laid out at its width; rows as high as their highest cell.
	laid := make([]*cell, len(cells))
	rowH := make([]float64, nrows)
	for i, sc := range cells {
		w := colX[sc.col+sc.cols] - colX[sc.col]
		cf := flow{w: w - 2*cellPad, text: fl.text, nested: true}
		var fill *rgb
		if sc.header {
			cf.text.bold = true
			fill = &headerBg
		}
		if bg, ok := lightColors[strAttr(sc.node, "background")]; ok {
			fill = &bg
		}
		c := &cell{x: fl.x + colX[sc.col], w: w, pad: cellPad, boxes: flatten(r.layout(sc.node.Content, cf)), fill: fill, border: true}
		laid[i] = c
		if sc.rows == 1 {
			rowH[sc.row] = max(rowH[sc.row], c.contentHeight())
		}
	}
	minRow := fl.text.size*ptMM*lineFactor + 2*cellPad
	for i := range rowH {
		rowH[i] = max(rowH[i], minRow)
	}
	for i, sc := range cells {
		if sc.rows > 1 {
			total := 0.0
			for _, h := range rowH[sc.row : sc.row+sc.rows] {
				total += h
			}
			if need := laid[i].contentHeight(); need > total {
				rowH[sc.row+sc.rows-1] += need - total
			}
		}
	}
	// Groups: a row starts one unless a cell of an earlier row reaches into it.
	reach := make([]int, nrows) // the last row each row's group reaches
	for i := range reach {
		reach[i] = i
	}
	for _, sc := range cells {
		reach[sc.row] = max(reach[sc.row], sc.row+sc.rows-1)
	}
	g := &grid{decor: fl.decor}
	headerRows := 0
	for ri := 0; ri < nrows && rowIsHeader(n.Content[ri]); ri++ {
		headerRows++
	}
	for start := 0; start < nrows; {
		end := start
		for ri := start; ri <= end; ri++ {
			end = max(end, reach[ri])
		}
		grp := &group{}
		tops := make([]float64, nrows+1)
		for ri := start; ri <= end; ri++ {
			tops[ri+1] = tops[ri] + rowH[ri]
		}
		grp.h = tops[end+1]
		for i, sc := range cells {
			if sc.row < start || sc.row > end {
				continue
			}
			c := laid[i]
			c.top = tops[sc.row]
			c.h = tops[sc.row+sc.rows] - c.top
			grp.cells = append(grp.cells, c)
		}
		if end < headerRows {
			g.header++
		}
		g.groups = append(g.groups, grp)
		start = end + 1
	}
	if fl.nested {
		return blocksOf(g.boxes())
	}
	return []block{g}
}

func rowIsHeader(row *docmodel.Node) bool {
	if len(row.Content) == 0 {
		return false
	}
	for _, c := range row.Content {
		if c.Type != "tableHeader" {
			return false
		}
	}
	return true
}

// columnWidths divides a width among the columns of a table: each gets at least what its longest word needs
// and, as far as the width allows, what its text needs on one line; widths set in the editor count as such.
// A cell across columns needs its width from them together; what they lack is shared among them.
func (r *renderer) columnWidths(cells []*sourceCell, ncols int, avail float64) []float64 {
	minW, maxW := make([]float64, ncols), make([]float64, ncols)
	var spanning []*sourceCell
	for _, sc := range cells {
		if sc.cols != 1 {
			spanning = append(spanning, sc)
			continue
		}
		st := style{family: sans, size: bodySize, bold: sc.header}
		lo, hi := r.measure(sc.node.Content, st)
		lo, hi = lo+2*cellPad, hi+2*cellPad
		if widths, ok := sc.node.Attrs["colwidth"].([]any); ok && len(widths) > 0 {
			if px, ok := widths[0].(float64); ok && px > 0 {
				hi = max(lo, px*pxMM)
			}
		}
		minW[sc.col] = max(minW[sc.col], lo)
		maxW[sc.col] = max(maxW[sc.col], hi)
	}
	for i := range minW {
		minW[i] = max(minW[i], 8)
		maxW[i] = max(maxW[i], minW[i])
	}
	// Narrower spans first, so that a wider one sees what the columns already got.
	sort.SliceStable(spanning, func(i, j int) bool { return spanning[i].cols < spanning[j].cols })
	for _, sc := range spanning {
		lo, hi := r.measure(sc.node.Content, style{family: sans, size: bodySize, bold: sc.header})
		end := sc.col + sc.cols
		widen(minW[sc.col:end], lo+2*cellPad)
		widen(maxW[sc.col:end], hi+2*cellPad)
		for i := sc.col; i < end; i++ {
			maxW[i] = max(maxW[i], minW[i])
		}
	}
	sumMin, sumMax := 0.0, 0.0
	for i := range minW {
		sumMin += minW[i]
		sumMax += maxW[i]
	}
	out := make([]float64, ncols)
	switch {
	case sumMin >= avail:
		for i := range out {
			out[i] = minW[i] * avail / sumMin
		}
	case sumMax <= avail:
		// Tables take the whole width, as they do on the page.
		for i := range out {
			out[i] = maxW[i] * avail / sumMax
		}
	default:
		extra := (avail - sumMin) / (sumMax - sumMin)
		for i := range out {
			out[i] = minW[i] + (maxW[i]-minW[i])*extra
		}
	}
	return out
}

// widen makes widths add up to at least w, each widened in proportion to its width.
func widen(widths []float64, w float64) {
	sum := 0.0
	for _, x := range widths {
		sum += x
	}
	if sum >= w || sum == 0 {
		return
	}
	for i := range widths {
		widths[i] *= w / sum
	}
}

// measure returns the width the longest word of blocks needs, and the width of their longest line unbroken.
func (r *renderer) measure(nodes []*docmodel.Node, st style) (float64, float64) {
	lo, hi := 0.0, 0.0
	for _, n := range nodes {
		switch n.Type {
		case "paragraph", "heading":
			word, line := 0.0, 0.0
			for _, p := range r.pieces(r.runs(n.Content, st)) {
				switch {
				case p.text == "\n" && !p.space:
					hi = max(hi, line)
					word, line = 0, 0
				case p.space:
					word = 0
					line += p.w
				default:
					word += p.w
					line += p.w
					lo = max(lo, word)
				}
			}
			hi = max(hi, line)
		case "codeBlock", "math":
			text := docmodel.NodeText(n)
			if n.Type == "math" {
				text = docmodel.FormulaLatex(n)
			}
			s := style{family: mono, size: 8.5}
			for _, l := range strings.Split(text, "\n") {
				hi = max(hi, r.width(printable(expandTabs(l)), s)+6)
			}
			lo = max(lo, min(hi, 25))
		case "image":
			if img := r.image(intAttr(n, "attachment"), strAttr(n, "src")); img != nil && img.svgName == "" {
				w := img.w
				if px := intAttr(n, "width"); px > 0 {
					w = float64(px) * pxMM
				}
				lo, hi = max(lo, min(w, 30)), max(hi, w)
			}
		case "table":
			lo, hi = max(lo, 30), max(hi, 80)
		default:
			a, b := r.measure(n.Content, st)
			indent := 0.0
			if n.Type == "bulletList" || n.Type == "orderedList" || n.Type == "taskList" {
				indent = 6.5
			}
			lo, hi = max(lo, a+indent), max(hi, b+indent)
		}
	}
	return lo, hi
}

// columns lays out a layout's columns side by side, by the widths of its layout.
func (r *renderer) columns(n *docmodel.Node, fl flow) []block {
	const gap = 7.0
	widths := docmodel.ColumnWidths(n)
	total := 0.0
	for _, w := range widths {
		total += w
	}
	if total == 0 || len(n.Content) == 0 {
		return nil
	}
	avail := fl.w - gap*float64(len(n.Content)-1)
	grp := &group{}
	x := fl.x
	for i, col := range n.Content {
		w := avail * widths[i] / total
		cf := flow{w: w, text: fl.text, nested: true}
		c := &cell{x: x, w: w, boxes: flatten(r.layout(col.Content, cf))}
		c.h = c.contentHeight()
		grp.h = max(grp.h, c.h)
		grp.cells = append(grp.cells, c)
		x += w + gap
	}
	for _, c := range grp.cells {
		c.h = grp.h
	}
	return []block{&grid{groups: []*group{grp}, decor: fl.decor}}
}

// flatten turns the blocks of a cell into boxes: tables in it are rows of boxes.
func flatten(blocks []block) []*box {
	var out []*box
	for _, b := range blocks {
		switch v := b.(type) {
		case *box:
			out = append(out, v)
		case *grid:
			out = append(out, v.boxes()...)
		}
	}
	return out
}

// boxes turns a grid into a box per group, for grids that do not break (in table cells and columns).
func (g *grid) boxes() []*box {
	out := make([]*box, len(g.groups))
	for i, grp := range g.groups {
		out[i] = &box{h: grp.h, decor: g.decor, group: grp}
	}
	return out
}
