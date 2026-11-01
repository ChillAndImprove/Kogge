// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"strings"
	"sync"
	"unicode"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/text/unicode/norm"
)

// The fonts are the Go fonts (Bigelow & Holmes, BSD licence), a sans-serif face for text and a monospaced one
// for code, each regular, bold, italic and bold italic. They cover Latin, Greek and Cyrillic (WGL4): German text
// with umlauts, ß, typographic quotes and the euro sign included. PDFs embed the subsets they use.
const (
	sans = "gosans"
	mono = "gomono"
)

var faces = []struct {
	family, style string
	ttf           []byte
}{
	{sans, "", goregular.TTF}, {sans, "B", gobold.TTF}, {sans, "I", goitalic.TTF}, {sans, "BI", gobolditalic.TTF},
	{mono, "", gomono.TTF}, {mono, "B", gomonobold.TTF}, {mono, "I", gomonoitalic.TTF}, {mono, "BI", gomonobolditalic.TTF},
}

func addFonts(f *fpdf.Fpdf) {
	for _, face := range faces {
		f.AddUTF8FontFromBytes(face.family, face.style, face.ttf)
	}
}

// coverage knows the characters the fonts have glyphs for; others would print as nothing or a box.
var coverage = struct {
	once  sync.Once
	sans  *sfnt.Font
	mono  *sfnt.Font
	mu    sync.Mutex
	known map[rune]bool
}{known: map[rune]bool{}}

func covered(r rune) bool {
	coverage.once.Do(func() {
		coverage.sans, _ = sfnt.Parse(goregular.TTF)
		coverage.mono, _ = sfnt.Parse(gomono.TTF)
	})
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	if ok, seen := coverage.known[r]; seen {
		return ok
	}
	var b sfnt.Buffer
	ok := false
	if coverage.sans != nil && coverage.mono != nil {
		g1, err1 := coverage.sans.GlyphIndex(&b, r)
		g2, err2 := coverage.mono.GlyphIndex(&b, r)
		ok = err1 == nil && err2 == nil && g1 != 0 && g2 != 0
	}
	if len(coverage.known) < 1<<16 {
		coverage.known[r] = ok
	}
	return ok
}

// missingGlyph stands for characters the fonts do not have (scripts other than Latin, Greek and Cyrillic, emoji).
const missingGlyph = '□'

// printable returns text as the fonts can print it: composed (an "a" with a combining diaeresis becomes "ä"),
// white space as spaces, and characters without glyphs as a box.
func printable(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == '\u00a0':
			b.WriteByte(' ')
		case unicode.IsControl(r) || r == '\u200b' || r == '\ufeff' || unicode.Is(unicode.Variation_Selector, r):
		case unicode.Is(unicode.Mn, r) && !covered(r):
			// Combining marks the fonts lack are left out; the base character stays.
		case covered(r):
			b.WriteRune(r)
		default:
			b.WriteRune(missingGlyph)
		}
	}
	return b.String()
}

type rgb struct{ r, g, b int }

var (
	black     = rgb{33, 37, 41}
	dimmed    = rgb{110, 117, 125}
	linkBlue  = rgb{25, 113, 194}
	ruleGray  = rgb{206, 212, 218}
	codeBg    = rgb{246, 248, 250}
	headerBg  = rgb{241, 243, 245}
	borderRGB = rgb{206, 212, 218}
	mentionBg = rgb{231, 245, 255}
)

// The palette of documents: text in a dark shade that reads on white, backgrounds (highlights, table cells,
// statuses) in a light one.
var (
	textColors = map[string]rgb{"gray": {73, 80, 87}, "red": {201, 42, 42}, "orange": {217, 72, 15}, "yellow": {176, 106, 0},
		"green": {43, 138, 62}, "teal": {8, 127, 91}, "blue": {24, 100, 171}, "violet": {95, 61, 196}, "pink": {166, 30, 77}}
	lightColors = map[string]rgb{"gray": {233, 236, 239}, "red": {255, 227, 227}, "orange": {255, 232, 204},
		"yellow": {255, 243, 191}, "green": {211, 249, 216}, "teal": {195, 250, 232}, "blue": {208, 235, 255},
		"violet": {229, 219, 255}, "pink": {255, 222, 235}}
)

// Panels by kind: background and the bar at their left.
var panelColors = map[string][2]rgb{
	"info":    {{231, 245, 255}, {28, 126, 214}},
	"note":    {{243, 240, 255}, {112, 72, 232}},
	"warning": {{255, 249, 219}, {245, 159, 0}},
	"success": {{235, 251, 238}, {55, 178, 77}},
	"error":   {{255, 245, 245}, {240, 62, 62}},
}
