// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Code blocks are highlighted as pages show them: by chroma, the highlighter of the server, with the colours of
// the web application's light theme (web/src/styles/highlight.css) for chroma's token classes.

const maxHighlight = 64 << 10 // longer code is plain text, as on pages

type tokenLook struct {
	color        rgb
	italic, bold bool
}

var (
	hlKeyword  = rgb{207, 34, 46}
	hlType     = rgb{149, 56, 0}
	hlFunction = rgb{130, 80, 223}
	hlString   = rgb{10, 48, 105}
	hlNumber   = rgb{5, 80, 174}
	hlComment  = rgb{110, 119, 129}
	hlTag      = rgb{17, 99, 41}
)

func lookOf(class string) (tokenLook, bool) {
	switch class {
	case "k", "kc", "kd", "kn", "kp", "kr", "ow", "gd", "err":
		return tokenLook{color: hlKeyword}, true
	case "kt", "nc", "ne":
		return tokenLook{color: hlType}, true
	case "nf", "fm", "nd":
		return tokenLook{color: hlFunction}, true
	case "s", "sa", "sb", "sc", "dl", "sd", "s2", "se", "sh", "si", "sx", "sr", "s1", "ss":
		return tokenLook{color: hlString}, true
	case "m", "mb", "mf", "mh", "mi", "il", "mo", "l", "ld", "no", "bp", "nb", "vc", "vg", "vi", "vm", "na", "py":
		return tokenLook{color: hlNumber}, true
	case "c", "ch", "cm", "c1", "cs", "cp", "cpf":
		return tokenLook{color: hlComment, italic: true}, true
	case "nt", "gi":
		return tokenLook{color: hlTag}, true
	case "gh", "gu", "gs":
		return tokenLook{color: black, bold: true}, true
	case "ge":
		return tokenLook{color: black, italic: true}, true
	}
	return tokenLook{}, false
}

type codeToken struct {
	text string
	look tokenLook
}

// highlight splits code into lines of tokens; code of languages chroma does not know is one token per line.
func highlight(language, text string) [][]codeToken {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	plain := func() [][]codeToken {
		out := make([][]codeToken, len(lines))
		for i, l := range lines {
			out[i] = []codeToken{{text: l, look: tokenLook{color: black}}}
		}
		return out
	}
	var lexer chroma.Lexer
	if language = strings.TrimSpace(language); language != "" && len(text) <= maxHighlight {
		lexer = lexers.Get(language)
	}
	if lexer == nil {
		return plain()
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, strings.TrimSuffix(text, "\n")+"\n")
	if err != nil {
		return plain()
	}
	out := [][]codeToken{nil}
	for tok := it(); tok != chroma.EOF; tok = it() {
		look, ok := lookOf(chroma.StandardTypes[tok.Type])
		if !ok {
			look = tokenLook{color: black}
		}
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				out = append(out, nil)
			}
			if part != "" {
				out[len(out)-1] = append(out[len(out)-1], codeToken{text: part, look: look})
			}
		}
	}
	// The lexer ends the text with a line break of its own.
	for len(out) > len(lines) && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	if len(out) != len(lines) {
		return plain()
	}
	return out
}

// codeLines lays out lines of tokens at a width: tabs become spaces to the next multiple of four, and lines
// longer than the width continue on the next line.
func (r *renderer) codeLines(tokens [][]codeToken, base style, width float64) [][]piece {
	var out [][]piece
	for _, line := range tokens {
		var cur []piece
		curW := 0.0
		col := 0
		for _, tok := range line {
			st := base
			st.color, st.italic, st.bold = tok.look.color, tok.look.italic, tok.look.bold
			var b strings.Builder
			for _, c := range tok.text {
				if c == '\t' {
					n := 4 - col%4
					b.WriteString(strings.Repeat(" ", n))
					col += n
					continue
				}
				b.WriteRune(c)
				col++
			}
			text := printable(b.String())
			for text != "" {
				w := r.width(text, st)
				if curW+w <= width+slack {
					cur = append(cur, piece{text: text, st: st, w: w})
					curW += w
					break
				}
				fit := r.fitChars(piece{text: text, st: st}, width-curW)
				if fit == "" && len(cur) == 0 {
					_, size := utf8.DecodeRuneInString(text)
					fit = text[:size]
				}
				if fit != "" {
					cur = append(cur, piece{text: fit, st: st, w: r.width(fit, st)})
				}
				out = append(out, cur)
				cur, curW = nil, 0
				text = text[len(fit):]
			}
		}
		out = append(out, cur)
	}
	return out
}
