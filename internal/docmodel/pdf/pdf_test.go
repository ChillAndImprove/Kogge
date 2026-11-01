// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	rscpdf "rsc.io/pdf"

	"kogge.de/kogge/internal/docmodel"
)

// The tests read the PDFs back with a PDF reader (rsc.io/pdf, only in tests): pages, the text of their content
// streams, bookmarks, links and images.

type parsed struct {
	r     *rscpdf.Reader
	pages []string // the text of each page, the strings of its text operators separated by spaces
	raw   [][]byte // the content streams of the pages
}

var textOp = regexp.MustCompile(`\((?:\\.|[^\\)])*\)\s*Tj`)

func readPDF(t *testing.T, data []byte) *parsed {
	t.Helper()
	r, err := rscpdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the PDF does not read back: %v", err)
	}
	p := &parsed{r: r}
	for i := 1; i <= r.NumPage(); i++ {
		content := r.Page(i).V.Key("Contents")
		var raw []byte
		if content.Kind() == rscpdf.Array {
			for j := 0; j < content.Len(); j++ {
				b, _ := io.ReadAll(content.Index(j).Reader())
				raw = append(raw, b...)
			}
		} else {
			raw, _ = io.ReadAll(content.Reader())
		}
		p.raw = append(p.raw, raw)
		var words []string
		for _, m := range textOp.FindAll(raw, -1) {
			s := m[1:bytes.LastIndexByte(m, ')')]
			words = append(words, decodeUTF16(unescapePDF(s)))
		}
		p.pages = append(p.pages, strings.Join(words, " "))
	}
	return p
}

// unescapePDF reads a PDF literal string: backslash escapes and octal codes.
func unescapePDF(s []byte) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			out = append(out, s[i])
			continue
		}
		i++
		switch c := s[i]; {
		case c == 'n':
			out = append(out, '\n')
		case c == 'r':
			out = append(out, '\r')
		case c == 't':
			out = append(out, '\t')
		case c >= '0' && c <= '7':
			v := 0
			for j := 0; j < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7'; j++ {
				v = v*8 + int(s[i]-'0')
				i++
			}
			i--
			out = append(out, byte(v))
		default:
			out = append(out, c)
		}
	}
	return out
}

// decodeUTF16 reads text of the fonts, which fpdf writes as UTF-16BE.
func decodeUTF16(b []byte) string {
	if len(b)%2 != 0 {
		return string(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return string(utf16.Decode(u))
}

func (p *parsed) text() string { return strings.Join(p.pages, "\n") }

type mark struct {
	Title string
	Depth int
}

func (p *parsed) outline() []mark {
	var out []mark
	var walk func(o rscpdf.Outline, d int)
	walk = func(o rscpdf.Outline, d int) {
		for _, c := range o.Child {
			out = append(out, mark{c.Title, d})
			walk(c, d+1)
		}
	}
	walk(p.r.Outline(), 0)
	return out
}

// links returns the URIs of the link annotations, and the number of links within the PDF.
func (p *parsed) links() ([]string, int) {
	var uris []string
	internal := 0
	for i := 1; i <= p.r.NumPage(); i++ {
		annots := p.r.Page(i).V.Key("Annots")
		for j := 0; j < annots.Len(); j++ {
			a := annots.Index(j)
			if uri := a.Key("A").Key("URI"); uri.Kind() == rscpdf.String {
				uris = append(uris, uri.RawString())
			} else if a.Key("Dest").Kind() == rscpdf.Array {
				internal++
			}
		}
	}
	return uris, internal
}

// images counts the images the pages draw.
func (p *parsed) images() int {
	n := 0
	seen := map[string]bool{}
	for i := 1; i <= p.r.NumPage(); i++ {
		xo := p.r.Page(i).Resources().Key("XObject")
		for _, k := range xo.Keys() {
			if xo.Key(k).Key("Subtype").Name() == "Image" && !seen[k] {
				seen[k] = true
				n++
			}
		}
	}
	return n
}

// --- fixtures ---

func pngImage(w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func jpegImage(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func gifImage() []byte {
	img := image.NewPaletted(image.Rect(0, 0, 20, 10), color.Palette{color.White, color.RGBA{200, 0, 0, 255}})
	img.SetColorIndex(5, 5, 1)
	var b bytes.Buffer
	_ = gif.Encode(&b, img, nil)
	return b.Bytes()
}

// png16 is a PNG with 16 bits per channel, which fpdf does not embed as it is.
func png16() []byte {
	img := image.NewNRGBA64(image.Rect(0, 0, 30, 20))
	for x := 0; x < 30; x++ {
		img.Set(x, 3, color.NRGBA64{0xffff, 0, 0, 0x8000})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

type fakeResources struct{}

var fixtureImages = map[int64]*Image{
	1: {Data: pngImage(320, 160, color.RGBA{30, 120, 200, 255}), Type: "image/png", Name: "chart.png"},
	2: {Data: jpegImage(400, 300), Type: "image/jpeg", Name: "photo.jpg"},
	3: {Data: gifImage(), Type: "image/gif", Name: "anim.gif"},
	4: {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), Type: "image/svg+xml", Name: "logo.svg",
		URL: "https://kogge.example/api/v1/wiki/attachments/4"},
	5: {Data: png16(), Type: "image/png", Name: "deep.png"},
	6: {Data: pngImage(5000, 100, color.Black), Type: "image/png", Name: "wide.png"},
	7: {Data: []byte("not an image"), Type: "image/png", Name: "broken.png"},
}

func (fakeResources) Image(id int64, src string) *Image {
	if id != 0 {
		return fixtureImages[id]
	}
	if src == "/badge.png" {
		return &Image{Data: pngImage(40, 12, color.RGBA{40, 160, 60, 255}), Type: "image/png", Name: "badge.png"}
	}
	return nil
}

func (fakeResources) Link(href string) Target {
	switch {
	case href == "/wiki/spaces/DOCS/pages/2/second":
		return Target{Section: "page-2"}
	case href == "/wiki/spaces/DOCS/pages/2/second#h-details":
		return Target{Section: "page-2", Anchor: "h-details"}
	case strings.HasPrefix(href, "/"):
		return Target{URL: "https://kogge.example" + href}
	}
	return Target{URL: href}
}

func (fakeResources) UserName(id int64, label string) string {
	if id == 7 {
		return "Anna Admin"
	}
	return label
}

func (fakeResources) Issue(key string) string {
	if key == "SHOP-12" {
		return "Checkout fails with umlauts"
	}
	return ""
}

func mustDoc(t *testing.T, raw string) *docmodel.Doc {
	t.Helper()
	d, err := docmodel.Parse([]byte(raw), docmodel.Options{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return d
}

// everything is a document with every kind of node and mark.
var everything = `{"type":"doc","content":[
	{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Größenänderung der Straße"}]},
	{"type":"paragraph","content":[
		{"type":"text","text":"Plain, "},{"type":"text","text":"bold","marks":[{"type":"bold"}]},{"type":"text","text":", "},
		{"type":"text","text":"italic","marks":[{"type":"italic"}]},{"type":"text","text":", "},
		{"type":"text","text":"code()","marks":[{"type":"code"}]},{"type":"text","text":", "},
		{"type":"text","text":"struck","marks":[{"type":"strike"}]},{"type":"text","text":", "},
		{"type":"text","text":"underlined","marks":[{"type":"underline"}]},{"type":"text","text":" H"},
		{"type":"text","text":"2","marks":[{"type":"subscript"}]},{"type":"text","text":"O E=mc"},
		{"type":"text","text":"2","marks":[{"type":"superscript"}]},{"type":"text","text":" "},
		{"type":"text","text":"rot","marks":[{"type":"textColor","attrs":{"color":"red"}}]},{"type":"text","text":" "},
		{"type":"text","text":"markiert","marks":[{"type":"highlight","attrs":{"color":"yellow"}}]},
		{"type":"text","text":" „Anführungszeichen“ kosten 5 € – und mehr…"},{"type":"hardBreak"},
		{"type":"text","text":"Kogge","marks":[{"type":"link","attrs":{"href":"https://kogge.example/docs"}}]},
		{"type":"text","text":" · "},
		{"type":"text","text":"zweite Seite","marks":[{"type":"link","attrs":{"href":"/wiki/spaces/DOCS/pages/2/second"}}]},
		{"type":"text","text":" · "},
		{"type":"text","text":"Details","marks":[{"type":"link","attrs":{"href":"/wiki/spaces/DOCS/pages/2/second#h-details"}}]},
		{"type":"text","text":" · "},
		{"type":"text","text":"nach oben","marks":[{"type":"link","attrs":{"href":"#h-größenänderung-der-straße"}}]},
		{"type":"text","text":" · "},{"type":"mention","attrs":{"user":7,"label":"anna"}},
		{"type":"text","text":" · "},{"type":"issue","attrs":{"key":"SHOP-12"}},
		{"type":"text","text":" · "},{"type":"status","attrs":{"text":"done","color":"green"}},
		{"type":"text","text":" · "},{"type":"date","attrs":{"value":"2026-10-21"}},
		{"type":"text","text":" · "},{"type":"emoji","attrs":{"name":"rocket"}},
		{"type":"text","text":" · 漢字 · "},{"type":"inlineImage","attrs":{"src":"/badge.png","alt":"build","href":"https://ci.example/run"}},
		{"type":"inlineMacro","attrs":{"name":"anchor"}}]},
	{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Listen"}]},
	{"type":"bulletList","content":[
		{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"erster Punkt"}]},
			{"type":"orderedList","attrs":{"start":3},"content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"dritter Schritt"}]}]},
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"vierter Schritt"}]}]}]}]},
		{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"zweiter Punkt"}]}]}]},
	{"type":"taskList","content":[
		{"type":"taskItem","attrs":{"checked":true},"content":[{"type":"paragraph","content":[{"type":"text","text":"erledigt"}]}]},
		{"type":"taskItem","attrs":{"checked":false},"content":[{"type":"paragraph","content":[{"type":"text","text":"offen"}]}]}]},
	{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Tabelle"}]},
	{"type":"table","content":[
		{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Name"}]}]},
			{"type":"tableHeader","attrs":{"colspan":2},"content":[{"type":"paragraph","content":[{"type":"text","text":"Beschreibung"}]}]}]},
		{"type":"tableRow","content":[{"type":"tableCell","attrs":{"rowspan":2,"background":"blue"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Zusammengefasst"}]}]},
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Ein langer Text in einer Zelle, der umbricht, weil er nicht in eine Zeile passt und weitergeht."}]}]},
			{"type":"tableCell","content":[{"type":"table","content":[{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"innen"}]}]}]}]}]}]},
		{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"zweite Zeile"}]}]},
			{"type":"tableCell","content":[{"type":"paragraph"}]}]}]},
	{"type":"codeBlock","attrs":{"language":"go","title":"main.go"},"content":[{"type":"text","text":"func main() {\n\tfmt.Println(\"Grüße\") // ` + strings.Repeat("ein sehr langer Kommentar ", 6) + `\n}"}]},
	{"type":"codeBlock","attrs":{"language":"mermaid"},"content":[{"type":"text","text":"graph TD\n  A --> B"}]},
	{"type":"macro","attrs":{"name":"mermaid","params":{"code":"sequenceDiagram\n  Alice->>Bob: Hallo"}}},
	{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Formeln "},{"type":"inlineMath","attrs":{"latex":"e^{i\\pi}"}}]},
	{"type":"paragraph","content":[{"type":"text","text":"Energie "},{"type":"inlineMath","attrs":{"latex":"E=mc^2"}},{"type":"text","text":" gilt."}]},
	{"type":"math","attrs":{"latex":"\\int_0^1 x^2\\,dx = \\tfrac13"}},
	{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"math","attrs":{"latex":"a^2+b^2=c^2"}}]},
		{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Pythagoras"}]}]}]}]},
	{"type":"panel","attrs":{"kind":"info"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Information im Kasten"}]}]},
	{"type":"panel","attrs":{"kind":"warning"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Warnung im Kasten"}]}]},
	{"type":"panel","attrs":{"kind":"note"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Notiz"}]}]},
	{"type":"panel","attrs":{"kind":"success"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Erfolg"}]}]},
	{"type":"panel","attrs":{"kind":"error"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Fehler"}]}]},
	{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Ein Zitat"}]}]},
	{"type":"expand","attrs":{"title":"Mehr dazu"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Aufgeklappt"}]}]},
	{"type":"horizontalRule"},
	{"type":"image","attrs":{"attachment":1,"alt":"Diagramm","width":200,"align":"center"}},
	{"type":"image","attrs":{"attachment":2,"alt":"Foto"}},
	{"type":"image","attrs":{"attachment":3,"alt":"Animation"}},
	{"type":"image","attrs":{"attachment":4,"alt":"Logo"}},
	{"type":"image","attrs":{"attachment":5,"alt":"Tief"}},
	{"type":"image","attrs":{"attachment":6,"alt":"Breit"}},
	{"type":"image","attrs":{"attachment":7,"alt":"Kaputt"}},
	{"type":"image","attrs":{"attachment":99,"alt":"Verborgen"}},
	{"type":"columns","attrs":{"layout":"left"},"content":[
		{"type":"column","content":[{"type":"paragraph","content":[{"type":"text","text":"Linke Spalte"}]}]},
		{"type":"column","content":[{"type":"paragraph","content":[{"type":"text","text":"Rechte Spalte"}]},
			{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"in der Spalte"}]}]}]}]}]},
	{"type":"macro","attrs":{"name":"gadget"}},
	{"type":"unsupported","attrs":{"source":"confluence","name":"gliffy","raw":"<x/>"}},
	{"type":"heading","attrs":{"level":4},"content":[{"type":"text","text":"Klein"}]},
	{"type":"heading","attrs":{"level":5},"content":[{"type":"text","text":"Kleiner"}]},
	{"type":"heading","attrs":{"level":6},"content":[{"type":"text","text":"Am kleinsten"}]},
	{"type":"paragraph","attrs":{"align":"right"},"content":[{"type":"text","text":"Rechts ` + strings.Repeat("Wortwiederholung ", 3) + `Donaudampfschifffahrtsgesellschaftskapitänswitwenrentenversicherungsanstalt` + strings.Repeat("X", 120) + `"}]}
]}`

func sections(t *testing.T) []Section {
	return []Section{
		{ID: "page-1", Title: "Handbuch für Größenänderungen", Meta: "DOCS · Lena Lehmann · 21.10.2026 · Version 7", Doc: mustDoc(t, everything)},
		{ID: "page-2", Title: "Zweite Seite", Level: 1, Doc: mustDoc(t, `{"type":"doc","content":[
			{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Details"}]},
			{"type":"paragraph","content":[{"type":"text","text":"zurück zum Handbuch"}]}]}`)},
	}
}

func germanLabels() Labels {
	return Labels{Page: "Seite %d von %d", Contents: "Inhalt", Diagram: "Diagramm (Mermaid)",
		DiagramNote: "Kogge zeichnet das Diagramm, wenn die Seite angesehen wird; das PDF zeigt seinen Quelltext.",
		Formula:     "Formel (LaTeX)", SVG: "Bild %s (SVG) — in Kogge öffnen",
		Date: func(v string) string {
			if d, err := time.Parse("2006-01-02", v); err == nil {
				return d.Format("02.01.2006")
			}
			return v
		}}
}

func TestEveryNode(t *testing.T) {
	var buf bytes.Buffer
	o := Options{Title: "Bereich DOCS", Header: "DOCS · Handbuch", Footer: "Kogge · exportiert am 21.10.2026",
		Contents: true, Intro: []string{"Exportiert von Lena Lehmann"}, Labels: germanLabels(), Language: "de",
		Created: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	if err := Write(&buf, sections(t), fakeResources{}, o); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KOGGE_PDF_OUT") != "" {
		_ = os.WriteFile(os.Getenv("KOGGE_PDF_OUT"), buf.Bytes(), 0o644)
	}
	p := readPDF(t, buf.Bytes())
	if len(p.pages) < 4 {
		t.Fatalf("pages: %d", len(p.pages))
	}
	text := p.text()
	for _, want := range []string{
		"Größenänderung", "Straße", "„Anführungszeichen“", "5 €", "Plain,", "bold", "code()", "H", "@Anna Admin",
		"SHOP-12 Checkout fails with umlauts", "DONE", "21.10.2026", ":rocket:", "□□", "erster Punkt", "3.", "4.",
		"dritter Schritt", "erledigt", "Zusammengefasst", "Beschreibung", "innen", "zweite Zeile", "main.go",
		"func   main ()", `Println ( "Grüße" )`, "graph TD", "Alice->>Bob: Hallo", "Diagramm (Mermaid)",
		// Formulas show their LaTeX: within text as code, on lines of their own in a box with its title.
		`Formeln e^{i\pi}`, "Energie E=mc^2 gilt.", "Formel (LaTeX)", `\int_0^1 x^2\,dx = \tfrac13`, "a^2+b^2=c^2 Pythagoras",
		"Information im Kasten", "Warnung im Kasten", "Ein Zitat", "▼ Mehr dazu", "Aufgeklappt", "„logo.svg“",
		"[Kaputt]", "[Verborgen]", "Linke Spalte", "Rechte Spalte", "in der Spalte", "[gadget]", "[confluence gliffy]",
		"Am kleinsten", "Donaudampfschifffahrts", "Seite 1 von", "Kogge · exportiert am 21.10.2026", "DOCS · Handbuch",
		"Inhalt", "Handbuch für Größenänderungen", "Zweite Seite", "Exportiert von Lena Lehmann",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the PDF lacks %q", want)
		}
	}
	// The contents name each section with its page.
	if !regexp.MustCompile(`Handbuch für Größenänderungen \d+`).MatchString(p.pages[0]) {
		t.Errorf("contents: %s", p.pages[0])
	}
	// Code is highlighted: the keyword func in the keywords' colour, the comment in italics.
	if !bytes.Contains(bytes.Join(p.raw, nil), []byte("0.812 0.133 0.180 rg")) {
		t.Error("the code is not highlighted")
	}
	got := fmt.Sprint(p.outline())
	for _, want := range []string{"{Inhalt 0}", "{Handbuch für Größenänderungen 0}", "{Größenänderung der Straße 1}",
		"{Listen 2}", "{Tabelle 3}", "{Klein 4}", "{Zweite Seite 1}", "{Details 2}"} {
		if !strings.Contains(got, want) {
			t.Errorf("bookmarks lack %s: %s", want, got)
		}
	}
	uris, internal := p.links()
	for _, want := range []string{"https://kogge.example/docs", "https://kogge.example/browse/SHOP-12", "https://ci.example/run",
		"https://kogge.example/api/v1/wiki/attachments/4"} {
		if !strings.Contains(strings.Join(uris, " "), want) {
			t.Errorf("links lack %s: %v", want, uris)
		}
	}
	// To the second section and its heading, to the first heading, and from the contents.
	if internal < 5 {
		t.Errorf("links within the PDF: %d", internal)
	}
	// PNG, JPEG, GIF, the 16-bit PNG and the wide one (scaled), the badge; not the SVG and the broken file.
	if n := p.images(); n != 6 {
		t.Errorf("images: %d", n)
	}
	if title := p.r.Trailer().Key("Info").Key("Title").Text(); title != "Bereich DOCS" {
		t.Errorf("title: %q", title)
	}
}

func TestTableHeaderRepeatsAndColumnsBreak(t *testing.T) {
	var rows []string
	rows = append(rows, `{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Kopf A"}]}]},
		{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Kopf B"}]}]}]}`)
	for i := range 90 {
		rows = append(rows, fmt.Sprintf(`{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Zeile %d"}]}]},
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"Wert %d"}]}]}]}`, i, i))
	}
	long := strings.Repeat(`{"type":"paragraph","content":[{"type":"text","text":"Absatz in der Spalte, lang genug für mehrere Zeilen Text."}]},`, 60)
	doc := `{"type":"doc","content":[{"type":"table","content":[` + strings.Join(rows, ",") + `]},
		{"type":"columns","content":[{"type":"column","content":[` + long + `{"type":"paragraph","content":[{"type":"text","text":"Ende links"}]}]},
		{"type":"column","content":[` + long + `{"type":"paragraph","content":[{"type":"text","text":"Ende rechts"}]}]}]}]}`
	var buf bytes.Buffer
	if err := Write(&buf, []Section{{ID: "s", Title: "Lang", Doc: mustDoc(t, doc)}}, fakeResources{}, Options{Paper: Letter}); err != nil {
		t.Fatal(err)
	}
	p := readPDF(t, buf.Bytes())
	tablePages := 0
	for _, page := range p.pages {
		if strings.Contains(page, "Zeile ") {
			tablePages++
			if !strings.Contains(page, "Kopf A") || strings.Index(page, "Kopf A") > strings.Index(page, "Zeile ") {
				t.Errorf("a page of the table does not start with its header: %.200s", page)
			}
		}
	}
	if tablePages < 2 {
		t.Errorf("the table is on %d pages", tablePages)
	}
	text := p.text()
	for i := range 90 {
		if !strings.Contains(text, fmt.Sprintf("Zeile %d ", i)) && !strings.Contains(text, fmt.Sprintf("Zeile %d\n", i)) {
			t.Errorf("row %d is missing", i)
		}
	}
	if !strings.Contains(text, "Ende links") || !strings.Contains(text, "Ende rechts") {
		t.Error("the columns lost their ends")
	}
	box := p.r.Page(1).V.Key("MediaBox")
	if box.Kind() != rscpdf.Array {
		box = p.r.Page(1).V.Key("Parent").Key("MediaBox")
	}
	if box.Index(2).Float64() != 612 || box.Index(3).Float64() != 792 {
		t.Errorf("Letter: %v", box)
	}
}

func TestCellsAcrossColumns(t *testing.T) {
	// Two narrow columns and a wide one; a cell across the narrow ones needs the room of its long word.
	cell := func(kind, text, attrs string) string {
		return `{"type":"` + kind + `","attrs":{` + attrs + `},"content":[{"type":"paragraph","content":[{"type":"text","text":"` + text + `"}]}]}`
	}
	wide := strings.Repeat("Ein längerer Text, der die dritte Spalte breit macht. ", 4)
	doc := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"tableRow","content":[` + cell("tableHeader", "Nr.", "") + `,` + cell("tableHeader", "mm", "") + `,` + cell("tableHeader", "Text", "") + `]},
		{"type":"tableRow","content":[` + cell("tableCell", "Donaudampfschifffahrtsgesellschaft", `"colspan":2`) + `,` + cell("tableCell", "rechts", "") + `]},
		{"type":"tableRow","content":[` + cell("tableCell", "1", "") + `,` + cell("tableCell", "3", "") + `,` + cell("tableCell", wide, "") + `]}]}]}`
	var buf bytes.Buffer
	if err := Write(&buf, []Section{{ID: "s", Title: "Spannen", Doc: mustDoc(t, doc)}}, fakeResources{}, Options{}); err != nil {
		t.Fatal(err)
	}
	if text := readPDF(t, buf.Bytes()).text(); !strings.Contains(text, "Donaudampfschifffahrtsgesellschaft") {
		t.Errorf("the word across two columns is broken: %s", text)
	}
}

// TestSmoke compares the text of a PDF page by page, and its bookmarks, with testdata/smoke.txt
// (KOGGE_UPDATE_GOLDEN=1 writes it): changes of the layout show there as text that moved to other pages. (The
// bytes of two PDFs of the same document differ in the order of the glyphs of their font subsets.)
func TestSmoke(t *testing.T) {
	o := Options{Title: "Smoke", Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Labels: germanLabels()}
	var buf bytes.Buffer
	if err := Write(&buf, sections(t), fakeResources{}, o); err != nil {
		t.Fatal(err)
	}
	p := readPDF(t, buf.Bytes())
	var out strings.Builder
	for i, page := range p.pages {
		fmt.Fprintf(&out, "--- page %d\n%s\n", i+1, page)
	}
	raw, _ := json.Marshal(p.outline())
	fmt.Fprintf(&out, "--- bookmarks\n%s\n", raw)
	file := "testdata/smoke.txt"
	if os.Getenv("KOGGE_UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(file, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (KOGGE_UPDATE_GOLDEN=1 writes it)", err)
	}
	if string(want) != out.String() {
		t.Errorf("the PDF's text differs from %s (KOGGE_UPDATE_GOLDEN=1 writes it):\n%s", file, out.String())
	}
}

// bombPNG is a PNG of 1-bit grey pixels, all zero: a few kilobytes that decode into w × h bytes.
func bombPNG(w, h int) []byte {
	var b bytes.Buffer
	chunk := func(kind string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		c := crc32.NewIEEE()
		c.Write([]byte(kind))
		c.Write(data)
		b.WriteString(kind)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, c.Sum32())
	}
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 1, 0 // 1 bit per pixel, grey
	chunk("IHDR", ihdr)
	var raw bytes.Buffer
	z := zlib.NewWriter(&raw)
	row := make([]byte, 1+(w+7)/8) // the filter byte and the pixels
	for y := 0; y < h; y++ {
		_, _ = z.Write(row)
	}
	_ = z.Close()
	chunk("IDAT", raw.Bytes())
	chunk("IEND", nil)
	return b.Bytes()
}

// Images whose pixels would take more memory than embedding them is worth are not decoded: a small file can name
// billions of pixels, and decoding allocates them all.
func TestImageBombs(t *testing.T) {
	bomb := bombPNG(10000, 6000) // 60 million pixels in some kilobytes
	if len(bomb) > 1<<20 {
		t.Fatalf("the bomb has %d bytes", len(bomb))
	}
	if _, _, _, _, err := prepareImage(bomb); err == nil || !strings.Contains(err.Error(), "pixels") {
		t.Errorf("a PNG of 60 million pixels: %v", err)
	}
	// Only the header tells the size of these; what a decoder would allocate is refused before it reads more.
	gifHeader := []byte("GIF89a\x10\x27\x10\x27\x80\x00\x00\x00\x00\x00\xff\xff\xff")
	webpHeader := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x0f\x27\x00\x0f\x27\x00")
	binary.LittleEndian.PutUint32(webpHeader[4:], uint32(len(webpHeader)-8))
	jpegHeader := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00" +
		"\xff\xc0\x00\x11\x08\x27\x10\x27\x10\x03\x01\x11\x00\x02\x11\x00\x03\x11\x00")
	for name, data := range map[string][]byte{"gif": gifHeader, "webp": webpHeader, "jpeg": jpegHeader} {
		if _, _, _, _, err := prepareImage(data); err == nil || !strings.Contains(err.Error(), "pixels") {
			t.Errorf("%s of 100 million pixels: %v", name, err)
		}
	}
	// Large photographs still embed, scaled down.
	if _, _, w, h, err := prepareImage(pngImage(3000, 1500, color.Black)); err != nil || w != maxPixels || h != 1200 {
		t.Errorf("a PNG of 3000 × 1500 pixels: %d × %d, %v", w, h, err)
	}
	// A document with a bomb renders without it.
	var buf bytes.Buffer
	doc := &docmodel.Doc{Type: "doc", Content: []*docmodel.Node{{Type: "image", Attrs: map[string]any{"attachment": int64(1), "alt": "Bombe"}}}}
	if err := Write(&buf, []Section{{ID: "s", Title: "Bomb", Doc: doc}}, bombResources{data: bomb}, Options{Title: "Bomb"}); err != nil {
		t.Fatal(err)
	}
	if p := readPDF(t, buf.Bytes()); p.images() != 0 || !strings.Contains(strings.Join(p.pages, " "), "Bombe") {
		t.Errorf("images %d, pages %q", p.images(), p.pages)
	}
}

type bombResources struct {
	fakeResources
	data []byte
}

func (r bombResources) Image(int64, string) *Image {
	return &Image{Data: r.data, Type: "image/png", Name: "bomb.png"}
}
