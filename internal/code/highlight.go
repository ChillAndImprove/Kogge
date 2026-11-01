// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"html"
	"path"
	"runtime"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"kogge.de/kogge/internal/code/gitdiff"
)

// Syntax highlighting: lines as HTML with chroma's short token classes (k, s, c1, …) and nothing
// else; all text is escaped. The web application styles the classes for light and dark mode.

// maxHighlight is the size above which text is shown without highlighting.
const maxHighlight = 512 << 10

// lexerCache remembers the lexers of file names: finding one compares the name with the patterns of every lexer,
// which takes milliseconds.
var lexerCache = struct {
	sync.Mutex
	m map[string]chroma.Lexer
}{m: map[string]chroma.Lexer{}}

func lexerByName(filename string) chroma.Lexer {
	base := path.Base(filename)
	lexerCache.Lock()
	l, ok := lexerCache.m[base]
	lexerCache.Unlock()
	if ok {
		return l
	}
	l = lexers.Match(base)
	lexerCache.Lock()
	if len(lexerCache.m) >= 10_000 {
		clear(lexerCache.m)
	}
	lexerCache.m[base] = l
	lexerCache.Unlock()
	return l
}

func lexerFor(filename, text string) chroma.Lexer {
	l := lexerByName(filename)
	if l == nil && text != "" {
		sample := text
		if len(sample) > 4096 {
			sample = sample[:4096]
		}
		l = lexers.Analyse(sample)
	}
	return l
}

// LanguageOf returns the name of the language of a file, or "".
func LanguageOf(filename string) string {
	if l := lexerByName(filename); l != nil {
		return l.Config().Name
	}
	return ""
}

// highlight splits text into lines of HTML; without a known language the lines are only escaped.
func highlight(filename, text string) ([]string, string) {
	text = strings.TrimSuffix(text, "\n")
	lexer := lexerFor(filename, text)
	if lexer == nil || len(text) > maxHighlight {
		return plainLines(text), ""
	}
	tokens, ok := tokenLines(lexer, text)
	if !ok {
		return plainLines(text), ""
	}
	lines := make([]string, len(tokens))
	var b strings.Builder
	for i, line := range tokens {
		b.Reset()
		for _, tok := range line {
			if tok.Class == "" {
				b.WriteString(html.EscapeString(tok.Text))
				continue
			}
			b.WriteString(`<span class="`)
			b.WriteString(tok.Class)
			b.WriteString(`">`)
			b.WriteString(html.EscapeString(tok.Text))
			b.WriteString(`</span>`)
		}
		lines[i] = b.String()
	}
	return lines, lexer.Config().Name
}

// Token is a piece of a highlighted line: its text and chroma's short class of its kind ("" for plain text).
type Token struct {
	Class string `json:"c,omitempty" doc:"Kind of token: k keyword, s string, c1 comment, … (none for plain text)"`
	Text  string `json:"t"`
}

// tokenLines splits text into lines of tokens; it fails if the lexer does.
func tokenLines(lexer chroma.Lexer, text string) ([][]Token, bool) {
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text+"\n")
	if err != nil {
		return nil, false
	}
	var lines [][]Token
	var line []Token
	for tok := it(); tok != chroma.EOF; tok = it() {
		class := chroma.StandardTypes[tok.Type]
		if tok.Type == chroma.Text || tok.Type == chroma.TextWhitespace {
			class = ""
		}
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, line)
				line = nil
			}
			if part != "" {
				line = append(line, Token{Class: class, Text: part})
			}
		}
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	want := strings.Count(text, "\n") + 1
	for len(lines) < want {
		lines = append(lines, nil)
	}
	return lines[:want], true
}

// maxSnippet is the longest snippet that is highlighted (wiki code blocks).
const maxSnippet = 64 << 10

// HighlightSnippet highlights a snippet in a language given by name or alias (go, ts, bash, …); without one, or
// with an unknown one, the lines are plain text. It returns the lines and the name of the language.
func HighlightSnippet(language, text string) ([][]Token, string) {
	text = strings.TrimSuffix(text, "\n")
	plain := func() [][]Token {
		var out [][]Token
		for _, l := range strings.Split(text, "\n") {
			if l == "" {
				out = append(out, nil)
			} else {
				out = append(out, []Token{{Text: l}})
			}
		}
		return out
	}
	var lexer chroma.Lexer
	if language = strings.TrimSpace(language); language != "" {
		lexer = lexers.Get(language)
	}
	if lexer == nil || len(text) > maxSnippet {
		return plain(), ""
	}
	lines, ok := tokenLines(lexer, text)
	if !ok {
		return plain(), ""
	}
	return lines, lexer.Config().Name
}

func plainLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = html.EscapeString(l)
	}
	return lines
}

// highlightDiff adds highlighted HTML to the lines of a parsed diff: each side of a hunk is highlighted as one
// text, so constructs spanning lines are recognised.
func highlightDiff(d *gitdiff.Diff) {
	// Files are highlighted in parallel.
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for fi := range d.Files {
		f := &d.Files[fi]
		if f.Binary || len(f.Hunks) == 0 || lexerFor(f.Path(), "") == nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			highlightFile(f)
		}()
	}
	wg.Wait()
}

func highlightFile(f *gitdiff.File) {
	for hi := range f.Hunks {
		h := &f.Hunks[hi]
		var oldText, newText []string
		for _, l := range h.Lines {
			if l.Kind != gitdiff.Addition {
				oldText = append(oldText, l.Text)
			}
			if l.Kind != gitdiff.Deletion {
				newText = append(newText, l.Text)
			}
		}
		oldHTML, _ := highlight(f.Path(), strings.Join(oldText, "\n"))
		newHTML, _ := highlight(f.Path(), strings.Join(newText, "\n"))
		oi, ni := 0, 0
		for li := range h.Lines {
			l := &h.Lines[li]
			switch l.Kind {
			case gitdiff.Addition:
				if ni < len(newHTML) {
					l.HTML = newHTML[ni]
				}
				ni++
			case gitdiff.Deletion:
				if oi < len(oldHTML) {
					l.HTML = oldHTML[oi]
				}
				oi++
			default:
				if ni < len(newHTML) {
					l.HTML = newHTML[ni]
				}
				oi++
				ni++
			}
		}
	}
}
