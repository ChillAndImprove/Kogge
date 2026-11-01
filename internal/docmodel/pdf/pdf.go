// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package pdf renders documents as PDF, for the wiki's exports: pages with their title and a line of
// metadata, a table of contents where there are several, bookmarks for pages and headings, links that stay
// clickable (within the PDF where they lead to a page of it), images, tables whose header rows repeat on every
// sheet, columns, and page numbers. Kogge writes the PDF itself (fpdf, MIT licence) with the Go fonts (BSD
// licence) embedded as subsets; there is no external renderer.
//
// Layout happens before drawing: blocks become boxes (a line of text, an image, some space) and grids (the rows
// of tables, the columns of layouts), the boxes are distributed over pages, and only then is anything drawn — so
// that the table of contents knows the page of every section, and links know where they lead.
package pdf

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"

	"kogge.de/kogge/internal/docmodel"
)

// Paper sizes.
const (
	A4     = "a4"
	Letter = "letter"
)

// Options describe a PDF.
type Options struct {
	Paper string // A4 (default) or Letter
	// Title is the document's title (in its metadata, and on its contents page).
	Title   string
	Author  string
	Creator string
	// Header is printed at the top of every sheet, Footer at the bottom left (the page number is at the right).
	Header string
	Footer string
	// Contents starts the PDF with a table of contents of its sections, below the title and the lines of Intro.
	Contents bool
	Intro    []string
	// Language of the document's text (BCP 47, e.g. "de").
	Language string
	Created  time.Time
	Labels   Labels
}

// Labels are the words the PDF itself contains, in the reader's language.
type Labels struct {
	Page        string // "Page %d of %d"
	Contents    string
	Diagram     string // the title of a diagram shown as its source
	DiagramNote string
	Formula     string // the title of a formula on lines of its own, shown as its source
	// SVG names an image the PDF cannot show ("%s" is its name).
	SVG string
	// Date formats a date (YYYY-MM-DD) of a document; nil keeps it.
	Date func(value string) string
}

// EnglishLabels are the labels in English.
var EnglishLabels = Labels{Page: "Page %d of %d", Contents: "Contents", Diagram: "Diagram (Mermaid)",
	DiagramNote: "Kogge draws the diagram when the page is viewed; the PDF shows its source.",
	Formula:     "Formula (LaTeX)", SVG: "Image %s (SVG) — open it in Kogge"}

// Section is a page of the content: it starts on a new sheet, with its title and metadata.
type Section struct {
	// ID is what links to the section use (Target.Section).
	ID    string
	Title string
	Meta  string
	// Level is the depth of the section in the tree of pages (bookmarks and contents).
	Level int
	Doc   *docmodel.Doc
}

// Image is the data of an image of a document.
type Image struct {
	Data []byte
	Type string // MIME type
	Name string // file name
	URL  string // where it can be opened, for images the PDF cannot show
}

// Target is where a link leads: a section of the PDF (and a heading in it), or an address.
type Target struct {
	Section string
	Anchor  string
	URL     string
}

// Resources supply what documents refer to. The caller applies the reader's permissions: images of attachments
// and summaries of issues the reader may not see are not returned.
type Resources interface {
	// Image returns an image of an attachment, or of a source within the instance (src); nil if there is none.
	Image(attachment int64, src string) *Image
	// Link returns where a link leads; Target{} drops the link (its text stays).
	Link(href string) Target
	// UserName returns the name of a mentioned user (label is the name the mention was written with).
	UserName(id int64, label string) string
	// Issue returns the summary of an issue, or "".
	Issue(key string) string
}

// Sheets: margins of the text, the header and the footer (millimetres).
const (
	marginX      = 20.0
	marginTop    = 22.0
	marginBottom = 20.0
	bodySize     = 10.5
)

type renderer struct {
	f      *fpdf.Fpdf
	res    Resources
	o      Options
	pageW  float64
	pageH  float64
	widths map[widthKey]float64
	images map[string]*placedImage
	// imageBytes counts the data of the embedded images, which stay in memory until the PDF is written.
	imageBytes    int
	maxImageTotal int

	section      string          // the section being laid out
	headingLevel int             // the bookmark level of the section's title
	headings     []int           // levels of the headings above, for the nesting of bookmarks
	anchors      map[string]bool // the destinations of links: sections and their headings
	links        map[string]int  // fpdf's ids of the destinations
	drawn        map[string]bool // the destinations drawn so far
	starts       map[string]int  // the first sheet of each section
	page         int             // the sheet being drawn
	pages        int             // the number of sheets
}

// MaxImageBytes is the most image data a PDF embeds (later images are left out, with their descriptions).
const MaxImageBytes = 400 << 20

// Write renders sections as one PDF.
func Write(w io.Writer, sections []Section, res Resources, o Options) error {
	size := "A4"
	if o.Paper == Letter {
		size = "Letter"
	}
	if o.Labels.Page == "" {
		o.Labels = EnglishLabels
	}
	f := fpdf.NewCustom(&fpdf.InitType{OrientationStr: "P", UnitStr: "mm", SizeStr: size})
	f.SetAutoPageBreak(false, 0)
	f.SetCompression(true)
	f.SetCatalogSort(true)
	if !o.Created.IsZero() {
		f.SetCreationDate(o.Created)
		f.SetModificationDate(o.Created)
	}
	f.SetTitle(o.Title, true)
	f.SetAuthor(o.Author, true)
	f.SetCreator(o.Creator, true)
	f.SetProducer("Kogge", true)
	if o.Language != "" {
		f.SetLang(o.Language)
	}
	addFonts(f)
	r := &renderer{f: f, res: res, o: o, widths: map[widthKey]float64{}, images: map[string]*placedImage{},
		maxImageTotal: MaxImageBytes, anchors: map[string]bool{}, links: map[string]int{}, drawn: map[string]bool{},
		starts: map[string]int{}}
	r.pageW, r.pageH = f.GetPageSize()
	if err := f.Error(); err != nil {
		return err
	}

	// Every section and heading can be linked to: they are known before the layout links to them.
	for _, s := range sections {
		r.anchors[s.ID] = true
		docmodel.Walk(s.Doc, func(n *docmodel.Node) bool {
			if n.Type == "heading" {
				r.anchors[s.ID+"#"+docmodel.HeadingID(n)] = true
			}
			return true
		})
	}
	var parts [][]block
	if o.Contents && len(sections) > 0 {
		parts = append(parts, r.contents(sections))
	}
	for _, s := range sections {
		parts = append(parts, r.sectionBlocks(s))
	}
	pages := r.paginate(parts)
	r.pages = len(pages)
	for _, a := range slices.Sorted(maps.Keys(r.anchors)) {
		r.links[a] = f.AddLink()
	}
	for i, p := range pages {
		r.page = i + 1
		f.AddPage()
		r.drawPage(p)
	}
	// Destinations that were never drawn (they cannot be: every section and heading is) lead to the first page.
	for a, id := range r.links {
		if _, ok := r.drawn[a]; !ok {
			f.SetLink(id, 0, 1)
		}
	}
	if err := f.Error(); err != nil {
		return err
	}
	return f.Output(w)
}

// sectionBlocks lays out a section: its title, metadata and document.
func (r *renderer) sectionBlocks(s Section) []block {
	r.section = s.ID
	r.headings = nil
	fl := r.topFlow()
	var out []block
	title := r.textBlock([]*docmodel.Node{{Type: "text", Text: s.Title}}, fl, style{family: sans, bold: true, size: 20, color: black}, "")
	for i, b := range title {
		b.keep = true
		if i == 0 {
			b.anchors = []string{s.ID}
			b.outline = &outline{title: s.Title, level: s.Level}
		}
		out = append(out, b)
	}
	if s.Meta != "" {
		out = append(out, r.spaceBox(1, fl))
		for _, b := range r.textBlock([]*docmodel.Node{{Type: "text", Text: s.Meta}}, fl, style{family: sans, size: 8.5, color: dimmed}, "") {
			b.keep = true
			out = append(out, b)
		}
	}
	out = append(out, &box{h: 5, keep: true, draw: func(x, y float64) {
		r.stroke(ruleGray, 0.3)
		r.f.Line(x+fl.x, y+2.5, x+fl.x+fl.w, y+2.5)
	}})
	r.headingLevel = s.Level
	return append(out, r.layout(s.Doc.Content, fl)...)
}

// contents lays out the contents page: title, introduction, and a line per section with its page.
func (r *renderer) contents(sections []Section) []block {
	fl := r.topFlow()
	var out []block
	for _, b := range r.textBlock([]*docmodel.Node{{Type: "text", Text: r.o.Title}}, fl, style{family: sans, bold: true, size: 22, color: black}, "") {
		out = append(out, b)
	}
	for _, line := range r.o.Intro {
		out = append(out, r.spaceBox(1.5, fl))
		for _, b := range r.textBlock([]*docmodel.Node{{Type: "text", Text: line}}, fl, style{family: sans, size: 9, color: dimmed}, "") {
			out = append(out, b)
		}
	}
	out = append(out, r.spaceBox(7, fl))
	head := r.textBlock([]*docmodel.Node{{Type: "text", Text: r.o.Labels.Contents}}, fl, style{family: sans, bold: true, size: 14, color: black}, "")
	head[0].outline = &outline{title: r.o.Labels.Contents, level: 0}
	for _, b := range head {
		b.keep = true
		out = append(out, b)
	}
	out = append(out, r.spaceBox(2, fl))
	st := style{family: sans, size: 10, color: black}
	numW := r.width("0000", st)
	lineH := st.size * ptMM * 1.7
	for _, s := range sections {
		s := s
		indent := float64(min(s.Level, 6)) * 5
		title := printable(s.Title)
		avail := fl.w - indent - numW - 4
		if r.width(title, st) > avail {
			for title != "" && r.width(title+"…", st) > avail {
				_, size := lastRune(title)
				title = title[:len(title)-size]
			}
			title += "…"
		}
		ts := st
		if s.Level == 0 {
			ts.bold = true
		}
		out = append(out, &box{h: lineH, draw: func(x, y float64) {
			base := y + lineH*0.68
			r.text(title, ts, x+fl.x+indent, base)
			num := fmt.Sprint(r.starts[s.ID])
			nw := r.width(num, st)
			r.text(num, st, x+fl.x+fl.w-nw, base)
			// Dots lead from the title to its page.
			tw := r.width(title, ts)
			from, to := x+fl.x+indent+tw+2, x+fl.x+fl.w-nw-2
			if to > from {
				r.fill(dimmed)
				for d := to; d > from; d -= 1.6 {
					r.f.Rect(d-0.25, base-0.5, 0.35, 0.35, "F")
				}
			}
			r.linkArea(&Target{Section: s.ID}, x+fl.x+indent, y, fl.w-indent, lineH)
		}})
	}
	return out
}

func lastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i]&0xc0 != 0x80 {
			return []rune(s[i:])[0], len(s) - i
		}
	}
	return 0, 0
}

func (r *renderer) topFlow() flow {
	return flow{x: 0, w: r.pageW - 2*marginX, text: style{family: sans, size: bodySize, color: black}}
}

func (r *renderer) contentHeight() float64 { return r.pageH - marginTop - marginBottom }

// --- drawing ---

func (r *renderer) fill(c rgb)              { r.f.SetFillColor(c.r, c.g, c.b) }
func (r *renderer) stroke(c rgb, w float64) { r.f.SetDrawColor(c.r, c.g, c.b); r.f.SetLineWidth(w) }

func (r *renderer) text(s string, st style, x, baseline float64) {
	r.setFont(st)
	r.f.SetTextColor(st.color.r, st.color.g, st.color.b)
	r.f.Text(x, baseline, printable(s))
}

// target resolves a link of the section being laid out: anchors within it, or what the resources say.
func (r *renderer) target(href string) Target {
	if strings.HasPrefix(href, "#") {
		return Target{Section: r.section, Anchor: href[1:]}
	}
	return r.res.Link(href)
}

// linkArea makes an area of the sheet lead to a target.
func (r *renderer) linkArea(t *Target, x, y, w, h float64) {
	if t == nil || w <= 0 {
		return
	}
	if t.Section != "" {
		key := t.Section
		if t.Anchor != "" && r.anchors[t.Section+"#"+t.Anchor] {
			key += "#" + t.Anchor
		}
		if id, ok := r.links[key]; ok {
			r.f.Link(x, y, w, h, id)
		}
		return
	}
	if t.URL != "" {
		r.f.LinkString(x, y, w, h, t.URL)
	}
}

func (r *renderer) drawPage(p *page) {
	r.drawFrame()
	for _, it := range p.items {
		if it.seg != nil {
			r.drawSegment(it.seg, marginX, it.y)
			continue
		}
		r.drawBox(it.box, marginX, it.y)
	}
}

// drawFrame draws the header and the footer of the sheet.
func (r *renderer) drawFrame() {
	st := style{family: sans, size: 7.5, color: dimmed}
	if r.o.Header != "" {
		r.text(r.clip(r.o.Header, st, r.pageW-2*marginX), st, marginX, 12)
		r.stroke(ruleGray, 0.2)
		r.f.Line(marginX, 14, r.pageW-marginX, 14)
	}
	num := fmt.Sprintf(r.o.Labels.Page, r.page, r.pages)
	nw := r.width(num, st)
	y := r.pageH - 10
	r.text(num, st, r.pageW-marginX-nw, y)
	if r.o.Footer != "" {
		r.text(r.clip(r.o.Footer, st, r.pageW-2*marginX-nw-8), st, marginX, y)
	}
}

// clip shortens text to a width, with an ellipsis.
func (r *renderer) clip(s string, st style, w float64) string {
	s = printable(s)
	if r.width(s, st) <= w {
		return s
	}
	for s != "" && r.width(s+"…", st) > w {
		_, size := lastRune(s)
		s = s[:len(s)-size]
	}
	return s + "…"
}

func (r *renderer) drawBox(b *box, x, y float64) {
	for _, d := range b.decor {
		if d.fill != nil {
			r.fill(*d.fill)
			r.f.Rect(x+d.x, y, d.w, b.h, "F")
		}
		if d.bar != nil {
			r.fill(*d.bar)
			r.f.Rect(x+d.x, y, d.barW, b.h, "F")
		}
	}
	for _, a := range b.anchors {
		if id, ok := r.links[a]; ok {
			r.f.SetLink(id, y, r.page)
			r.drawn[a] = true
		}
	}
	if b.outline != nil {
		// Bookmarks take their text in the encoding of the font set last.
		r.setFont(style{family: sans, size: bodySize})
		r.f.Bookmark(printable(b.outline.title), b.outline.level, y)
	}
	if b.draw != nil {
		b.draw(x, y)
	}
	if b.group != nil {
		r.drawGroup(b.group, x, y)
	}
}
