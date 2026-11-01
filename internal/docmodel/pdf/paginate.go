// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

// Pagination distributes the boxes and grids of the sections over sheets. Every section starts on a new sheet;
// space between blocks is left out at the top of a sheet; boxes that keep with the next one (headings) move to
// the next sheet together with it; grid groups that do not fit continue on the next sheet.

type page struct {
	items []placed
}

// placed is a box, or a segment of a grid group, at its vertical position on a sheet.
type placed struct {
	y   float64
	box *box
	seg *segment
}

// segment is the part of a grid group on one sheet.
type segment struct {
	g     *grid
	group *group
	h     float64
	parts []cellPart // per cell of the group
}

type cellPart struct {
	shown  bool
	off    float64 // the top of the cell's part, below the segment's top
	boxes  []*box
	first  bool // the part begins the cell (padding above its boxes)
	finish bool // the part ends the cell
}

type paginator struct {
	r     *renderer
	pages []*page
	cur   *page
	y     float64
	top   float64
	limit float64 // the bottom of the text on a sheet
}

func (p *paginator) newPage() {
	p.cur = &page{}
	p.pages = append(p.pages, p.cur)
	p.y = p.top
}

func (p *paginator) free() float64 { return p.limit - p.y }
func (p *paginator) empty() bool   { return p.y == p.top }

func (r *renderer) paginate(parts [][]block) []*page {
	p := &paginator{r: r, top: marginTop, limit: r.pageH - marginBottom}
	for _, blocks := range parts {
		p.newPage()
		for _, s := range sectionsIn(blocks) {
			r.starts[s] = len(p.pages)
		}
		for i := 0; i < len(blocks); i++ {
			switch b := blocks[i].(type) {
			case *box:
				if b.space && p.empty() {
					continue
				}
				need := b.h
				if b.keep {
					need = min(keepHeight(blocks[i:]), r.contentHeight())
				}
				if need > p.free() && !p.empty() {
					p.newPage()
					if b.space {
						continue
					}
				}
				p.cur.items = append(p.cur.items, placed{y: p.y, box: b})
				p.y += b.h
			case *grid:
				p.placeGrid(b)
			}
		}
	}
	return p.pages
}

// sectionsIn returns the sections whose title is among the blocks.
func sectionsIn(blocks []block) []string {
	var out []string
	for _, b := range blocks {
		if bx, ok := b.(*box); ok {
			for _, a := range bx.anchors {
				if !containsHash(a) {
					out = append(out, a)
				}
			}
		}
	}
	return out
}

func containsHash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			return true
		}
	}
	return false
}

// keepHeight is the height of the boxes that keep together from the first of blocks, and of the start of what
// follows them.
func keepHeight(blocks []block) float64 {
	h := 0.0
	for _, b := range blocks {
		switch v := b.(type) {
		case *box:
			h += v.h
			if !v.keep && !v.space {
				return h
			}
		case *grid:
			if len(v.groups) == 0 {
				return h
			}
			// The header rows and the first row of a table, or a part of a layout, go with its heading.
			for _, g := range v.groups[:min(v.header+1, len(v.groups))] {
				h += min(g.h, 25)
			}
			return h
		}
	}
	return h
}

// placeGrid places the groups of a grid: whole where they fit, on a new sheet where they fit a sheet, broken
// between the boxes of their cells otherwise.
func (p *paginator) placeGrid(g *grid) {
	headerH := 0.0
	for _, grp := range g.groups[:g.header] {
		headerH += grp.h
	}
	repeat := func() {
		// A table continued on another sheet starts with its header rows, where they leave room for more.
		if g.header > 0 && headerH < p.r.contentHeight()/3 {
			for _, grp := range g.groups[:g.header] {
				p.whole(g, grp)
			}
		}
	}
	for gi, grp := range g.groups {
		if grp.h <= p.free() {
			p.whole(g, grp)
			continue
		}
		header := 0.0
		if gi >= g.header {
			header = headerH
		}
		if !p.empty() && grp.h <= p.r.contentHeight()-header {
			p.newPage()
			if gi >= g.header {
				repeat()
			}
			p.whole(g, grp)
			continue
		}
		p.split(g, grp, gi >= g.header, repeat)
	}
}

func (p *paginator) whole(g *grid, grp *group) {
	seg := &segment{g: g, group: grp, h: grp.h, parts: make([]cellPart, len(grp.cells))}
	for i, c := range grp.cells {
		seg.parts[i] = cellPart{shown: true, off: c.top, boxes: c.boxes, first: true, finish: true}
	}
	p.cur.items = append(p.cur.items, placed{y: p.y, seg: seg})
	p.y += grp.h
}

// split places a group over several sheets: each cell continues with its next box. On an empty sheet every cell
// that goes on takes at least one box, so that a group always comes to an end.
func (p *paginator) split(g *grid, grp *group, repeatHeader bool, repeat func()) {
	next := make([]int, len(grp.cells))     // the next box of each cell
	started := make([]bool, len(grp.cells)) // whether the cell has begun
	firstSeg := true
	for {
		seg := &segment{g: g, group: grp, parts: make([]cellPart, len(grp.cells))}
		room := p.free()
		progress, done := false, true
		for i, c := range grp.cells {
			off := 0.0
			if firstSeg {
				off = c.top
			}
			if started[i] && next[i] >= len(c.boxes) {
				// Finished on an earlier sheet: its frame continues to the end of the group.
				seg.parts[i] = cellPart{shown: true}
				continue
			}
			if off+c.pad >= room {
				// A cell of a lower row that does not begin on this sheet.
				done = false
				continue
			}
			part := cellPart{shown: true, off: off, first: !started[i]}
			used := off + c.pad
			for next[i] < len(c.boxes) {
				b := c.boxes[next[i]]
				if used+b.h > room && (len(part.boxes) > 0 || !p.empty()) {
					break
				}
				part.boxes = append(part.boxes, b)
				used += b.h
				next[i]++
				progress = true
			}
			started[i] = true
			if part.finish = next[i] >= len(c.boxes); part.finish {
				used += c.pad
			} else {
				done = false
			}
			seg.h = max(seg.h, used)
			seg.parts[i] = part
		}
		if !progress && !done {
			p.newPage()
			if repeatHeader {
				repeat()
			}
			continue
		}
		switch {
		case !done:
			seg.h = room
		case firstSeg:
			seg.h = max(seg.h, grp.h)
		}
		p.cur.items = append(p.cur.items, placed{y: p.y, seg: seg})
		p.y += seg.h
		if done {
			return
		}
		firstSeg = false
		p.newPage()
		if repeatHeader {
			repeat()
		}
	}
}

// --- drawing grids ---

func (r *renderer) drawSegment(s *segment, x, y float64) {
	for _, d := range s.g.decor {
		if d.fill != nil {
			r.fill(*d.fill)
			r.f.Rect(x+d.x, y, d.w, s.h, "F")
		}
		if d.bar != nil {
			r.fill(*d.bar)
			r.f.Rect(x+d.x, y, d.barW, s.h, "F")
		}
	}
	for i, c := range s.group.cells {
		part := s.parts[i]
		if !part.shown {
			continue
		}
		r.drawCellFrame(c, x, y+part.off, s.h-part.off)
		by := y + part.off + c.pad
		for _, b := range part.boxes {
			r.drawBox(b, x+c.x+c.pad, by)
			by += b.h
		}
	}
}

// drawGroup draws a group whole (tables within table cells and columns).
func (r *renderer) drawGroup(grp *group, x, y float64) {
	for _, c := range grp.cells {
		r.drawCellFrame(c, x, y+c.top, c.h)
		by := y + c.top + c.pad
		for _, b := range c.boxes {
			r.drawBox(b, x+c.x+c.pad, by)
			by += b.h
		}
	}
}

func (r *renderer) drawCellFrame(c *cell, x, y, h float64) {
	if h <= 0 {
		return
	}
	if c.fill != nil {
		r.fill(*c.fill)
		r.f.Rect(x+c.x, y, c.w, h, "F")
	}
	if c.border {
		r.stroke(borderRGB, 0.25)
		r.f.Rect(x+c.x, y, c.w, h, "D")
	}
}
