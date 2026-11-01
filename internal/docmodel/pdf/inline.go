// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"strings"
	"unicode/utf8"

	"kogge.de/kogge/internal/docmodel"
)

// Text: inline content becomes runs of styled text, the runs pieces (words and spaces), and the pieces lines that
// fill the width of their block; lines break between words, and inside words longer than a line.

const (
	ptMM       = 25.4 / 72 // one point in millimetres
	lineFactor = 1.45      // line height relative to the font size
	// slack absorbs rounding: a table column as wide as its longest word must hold the word.
	slack = 1e-6
)

// style is how a piece of text looks and where it leads.
type style struct {
	family    string
	bold      bool
	italic    bool
	size      float64 // points
	color     rgb
	bg        *rgb // behind the text: code, highlights, mentions
	lozenge   bool // a status: padded, on its background
	underline bool
	strike    bool
	shift     float64 // of the baseline, in points (subscript, superscript)
	link      *Target
}

func (s style) fontStyle() string {
	switch {
	case s.bold && s.italic:
		return "BI"
	case s.bold:
		return "B"
	case s.italic:
		return "I"
	}
	return ""
}

// run is text in one style, a line break, or an image within text.
type run struct {
	text string
	st   style
	img  *placedImage
	brk  bool
}

// piece is the part of a line in one style: a word or part of one, a space, or an image.
type piece struct {
	text  string
	st    style
	w     float64 // millimetres
	img   *placedImage
	space bool
}

type line struct {
	pieces []piece
	w      float64 // the width of the pieces
	h      float64 // the line's height
	base   float64 // the baseline, below the top of the line
}

func (r *renderer) setFont(st style) {
	r.f.SetFont(st.family, st.fontStyle(), st.size)
}

// width measures text in a style, in millimetres.
func (r *renderer) width(text string, st style) float64 {
	key := widthKey{st.family, st.fontStyle(), st.size, text}
	if w, ok := r.widths[key]; ok {
		return w
	}
	r.setFont(st)
	w := r.f.GetStringWidth(text)
	if len(r.widths) < 200_000 {
		r.widths[key] = w
	}
	return w
}

type widthKey struct {
	family, style string
	size          float64
	text          string
}

// runs flattens inline content in a style: marks change the style, inline nodes become text of their own.
func (r *renderer) runs(nodes []*docmodel.Node, base style) []run {
	var out []run
	for _, n := range nodes {
		st := base
		for _, m := range n.Marks {
			r.applyMark(&st, m)
		}
		switch n.Type {
		case "text":
			out = append(out, run{text: n.Text, st: st})
		case "hardBreak":
			out = append(out, run{brk: true, st: st})
		case "mention":
			st.color, st.bg = linkBlue, &mentionBg
			out = append(out, run{text: "@" + r.res.UserName(intAttr(n, "user"), strAttr(n, "label")), st: st})
		case "issue":
			key := strAttr(n, "key")
			t := r.target("/browse/" + key)
			st.link, st.color = &t, linkBlue
			text := key
			if summary := r.res.Issue(key); summary != "" {
				text += " " + summary
			}
			out = append(out, run{text: text, st: st})
		case "status":
			st.lozenge, st.bold = true, true
			st.size = base.size * 0.78
			color := strAttr(n, "color")
			if c, ok := textColors[color]; ok {
				st.color = c
			} else {
				st.color = textColors["gray"]
			}
			bg := lightColors["gray"]
			if c, ok := lightColors[color]; ok {
				bg = c
			}
			st.bg = &bg
			out = append(out, run{text: strings.ToUpper(strAttr(n, "text")), st: st})
		case "date":
			v := strAttr(n, "value")
			if r.o.Labels.Date != nil {
				v = r.o.Labels.Date(v)
			}
			out = append(out, run{text: v, st: st})
		case "emoji":
			out = append(out, run{text: ":" + strAttr(n, "name") + ":", st: st})
		case "inlineImage":
			if href := strAttr(n, "href"); href != "" {
				t := r.target(href)
				st.link = &t
			}
			if img := r.image(0, strAttr(n, "src")); img != nil && img.svgName == "" {
				out = append(out, run{img: img, st: st})
			} else if alt := strAttr(n, "alt"); alt != "" {
				out = append(out, run{text: alt, st: st})
			}
		case "inlineMacro":
			st.color = dimmed
			out = append(out, run{text: "[" + strAttr(n, "name") + "]", st: st})
		case "inlineMath":
			// The LaTeX of a formula within text, which the PDF cannot typeset, in the look of code.
			if latex := docmodel.FormulaLatex(n); latex != "" {
				st.family, st.size, st.bg = mono, st.size*0.9, &codeBg
				out = append(out, run{text: latex, st: st})
			}
		}
	}
	return out
}

func (r *renderer) applyMark(st *style, m *docmodel.Mark) {
	switch m.Type {
	case "bold":
		st.bold = true
	case "italic":
		st.italic = true
	case "underline":
		st.underline = true
	case "strike":
		st.strike = true
	case "code":
		st.family = mono
		st.size *= 0.9
		st.bg = &codeBg
	case "subscript":
		st.size *= 0.72
		st.shift = -st.size * 0.3
	case "superscript":
		st.size *= 0.72
		st.shift = st.size * 0.55
	case "textColor":
		if c, ok := textColors[markAttr(m, "color")]; ok {
			st.color = c
		}
	case "highlight":
		bg := lightColors["yellow"]
		if c, ok := lightColors[markAttr(m, "color")]; ok {
			bg = c
		}
		st.bg = &bg
	case "link":
		if href := markAttr(m, "href"); docmodel.SafeLink(href) {
			t := r.target(href)
			st.link = &t
			st.color, st.underline = linkBlue, true
		}
	}
}

// pieces splits runs into words, spaces and images; a line break is a nil piece list entry (text "\n").
func (r *renderer) pieces(runs []run) []piece {
	var out []piece
	for _, rn := range runs {
		switch {
		case rn.brk:
			out = append(out, piece{text: "\n"})
			continue
		case rn.img != nil:
			out = append(out, piece{img: rn.img, st: rn.st, w: rn.img.w})
			continue
		}
		text := printable(rn.text)
		for text != "" {
			i := strings.IndexByte(text, ' ')
			switch {
			case i == 0:
				// Spaces in a row count once, as browsers show them.
				if n := len(out); n == 0 || !out[n-1].space {
					out = append(out, piece{text: " ", st: rn.st, w: r.width(" ", rn.st), space: true})
				}
				text = strings.TrimLeft(text, " ")
			case i < 0:
				out = append(out, piece{text: text, st: rn.st, w: r.width(text, rn.st)})
				text = ""
			default:
				out = append(out, piece{text: text[:i], st: rn.st, w: r.width(text[:i], rn.st)})
				text = text[i:]
			}
		}
	}
	return out
}

// lines breaks pieces into lines of a width. Words that do not fit a line of their own break between their
// characters; minSize is the font size of empty lines.
func (r *renderer) lines(ps []piece, width float64, minSize float64) []*line {
	var out []*line
	cur := &line{}
	flush := func() {
		cur.trimSpace()
		cur.finish(minSize)
		out = append(out, cur)
		cur = &line{}
	}
	var word []piece // a word may change its style midway: its pieces stay together
	place := func() {
		if len(word) == 0 {
			return
		}
		ww := 0.0
		for _, p := range word {
			ww += p.w
		}
		if cur.hasContent() && cur.w+ww > width+slack {
			flush()
		}
		if cur.w+ww <= width+slack {
			for _, p := range word {
				cur.add(p)
			}
			word = word[:0]
			return
		}
		// Too long for a line of its own: the word breaks where the line is full.
		for _, p := range word {
			for {
				if p.img != nil {
					if cur.hasContent() && cur.w+p.w > width+slack {
						flush()
					}
					cur.add(p)
					break
				}
				if p.text == "" {
					break
				}
				fit := r.fitChars(p, width-cur.w)
				if fit == "" {
					if cur.hasContent() {
						flush()
						continue
					}
					// Not even one character fits: it goes on a line of its own.
					_, size := utf8.DecodeRuneInString(p.text)
					fit = p.text[:size]
				}
				cur.add(piece{text: fit, st: p.st, w: r.width(fit, p.st)})
				if p.text = p.text[len(fit):]; p.text != "" {
					flush()
				}
			}
		}
		word = word[:0]
	}
	for _, p := range ps {
		switch {
		case p.text == "\n" && !p.space && p.img == nil:
			place()
			flush()
		case p.space:
			place()
			if cur.hasContent() {
				cur.add(p)
			}
		default:
			word = append(word, p)
		}
	}
	place()
	if cur.hasContent() || len(out) == 0 {
		flush()
	}
	return out
}

// fitChars returns the longest prefix of the piece's text that fits a width.
func (r *renderer) fitChars(p piece, width float64) string {
	lo, hi := 0, len(p.text)
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		for mid > 0 && mid < len(p.text) && !utf8.RuneStart(p.text[mid]) {
			mid--
		}
		if r.width(p.text[:mid], p.st) <= width+slack {
			best = mid
			lo = mid + 1
			for lo < len(p.text) && !utf8.RuneStart(p.text[lo]) {
				lo++
			}
		} else {
			hi = mid - 1
		}
	}
	return p.text[:best]
}

func (l *line) add(p piece) {
	l.pieces = append(l.pieces, p)
	l.w += p.w
}

func (l *line) hasContent() bool {
	for _, p := range l.pieces {
		if !p.space {
			return true
		}
	}
	return false
}

func (l *line) trimSpace() {
	for n := len(l.pieces); n > 0 && l.pieces[n-1].space; n = len(l.pieces) {
		l.w -= l.pieces[n-1].w
		l.pieces = l.pieces[:n-1]
	}
}

// finish computes the height and baseline of the line from its largest text.
func (l *line) finish(minSize float64) {
	size := minSize
	imgH := 0.0
	for _, p := range l.pieces {
		if p.img != nil {
			imgH = max(imgH, p.img.h)
			continue
		}
		if p.st.size > size && !p.st.lozenge {
			size = p.st.size
		}
	}
	l.h = size * ptMM * lineFactor
	l.base = (l.h-size*ptMM)/2 + size*ptMM*0.8
	if imgH > size*ptMM {
		// An image within text sits on the baseline and makes the line higher.
		l.base += imgH - size*ptMM
		l.h += imgH - size*ptMM
	}
}

// drawLine draws a line with its left edge at x (alignments move it within the width).
func (r *renderer) drawLine(l *line, x, y, width float64, align string) {
	switch align {
	case "center":
		x += (width - l.w) / 2
	case "right":
		x += width - l.w
	}
	base := y + l.base
	for _, p := range l.pieces {
		if p.img != nil {
			top := base - p.img.h
			r.drawImage(p.img, x, top)
			r.linkArea(p.st.link, x, top, p.w, p.img.h)
			x += p.w
			continue
		}
		sizeMM := p.st.size * ptMM
		shift := p.st.shift * ptMM
		if p.st.bg != nil && !p.space {
			r.fill(*p.st.bg)
			pad := 0.0
			if p.st.lozenge {
				pad = 0.6
			}
			r.f.Rect(x-pad, base-shift-sizeMM*0.85, p.w+2*pad, sizeMM*1.12, "F")
		}
		if !p.space {
			r.setFont(p.st)
			r.f.SetTextColor(p.st.color.r, p.st.color.g, p.st.color.b)
			r.f.Text(x, base-shift, p.text)
			if p.st.underline || p.st.strike {
				r.f.SetDrawColor(p.st.color.r, p.st.color.g, p.st.color.b)
				r.f.SetLineWidth(max(0.15, sizeMM*0.06))
				if p.st.underline {
					r.f.Line(x, base-shift+sizeMM*0.12, x+p.w, base-shift+sizeMM*0.12)
				}
				if p.st.strike {
					r.f.Line(x, base-shift-sizeMM*0.28, x+p.w, base-shift-sizeMM*0.28)
				}
			}
		} else if p.st.underline {
			// Links underline the spaces between their words.
			r.f.SetDrawColor(p.st.color.r, p.st.color.g, p.st.color.b)
			r.f.SetLineWidth(max(0.15, sizeMM*0.06))
			r.f.Line(x, base+sizeMM*0.12, x+p.w, base+sizeMM*0.12)
		}
		r.linkArea(p.st.link, x, y, p.w, l.h)
		x += p.w
	}
}

func markAttr(m *docmodel.Mark, k string) string {
	s, _ := m.Attrs[k].(string)
	return s
}

func strAttr(n *docmodel.Node, k string) string {
	s, _ := n.Attrs[k].(string)
	return s
}

func intAttr(n *docmodel.Node, k string) int64 {
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
