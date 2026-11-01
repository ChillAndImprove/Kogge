// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package kql

import (
	"strconv"
	"strings"
)

type tokenKind int

const (
	tEOF tokenKind = iota
	tWord
	tString
	tOp
	tLParen
	tRParen
	tComma
	tCustom // cf[123]
)

type token struct {
	kind tokenKind
	text string
	pos  Pos
}

var keywords = map[string]bool{
	"and": true, "or": true, "not": true, "in": true, "is": true, "empty": true, "null": true,
	"order": true, "by": true, "asc": true, "desc": true, "was": true, "changed": true,
}

// isWordChar reports whether c may appear in an unquoted word.
func isWordChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c >= 0x80:
		return true
	}
	return strings.IndexByte("_.-@:/+*$#%", c) >= 0
}

type lexer struct {
	src  string
	pos  int
	toks []token
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src}
	for {
		for l.pos < len(src) && (src[l.pos] == ' ' || src[l.pos] == '\t' || src[l.pos] == '\n' || src[l.pos] == '\r') {
			l.pos++
		}
		if l.pos >= len(src) {
			l.toks = append(l.toks, token{kind: tEOF, pos: Pos(l.pos)})
			return l.toks, nil
		}
		start := l.pos
		c := src[l.pos]
		switch {
		case c == '(':
			l.add(tLParen, "(", start, 1)
		case c == ')':
			l.add(tRParen, ")", start, 1)
		case c == ',':
			l.add(tComma, ",", start, 1)
		case c == '"' || c == '\'':
			s, err := l.quoted(c)
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tString, text: s, pos: Pos(start)})
		case c == '=':
			l.add(tOp, "=", start, 1)
		case c == '~':
			l.add(tOp, "~", start, 1)
		case c == '!':
			switch {
			case strings.HasPrefix(src[l.pos:], "!="):
				l.add(tOp, "!=", start, 2)
			case strings.HasPrefix(src[l.pos:], "!~"):
				l.add(tOp, "!~", start, 2)
			default:
				l.add(tWord, "not", start, 1)
			}
		case c == '>' || c == '<':
			if l.pos+1 < len(src) && src[l.pos+1] == '=' {
				l.add(tOp, string(c)+"=", start, 2)
			} else {
				l.add(tOp, string(c), start, 1)
			}
		case c == '&' && strings.HasPrefix(src[l.pos:], "&&"):
			l.add(tWord, "and", start, 2)
		case c == '|' && strings.HasPrefix(src[l.pos:], "||"):
			l.add(tWord, "or", start, 2)
		case isWordChar(c):
			for l.pos < len(src) && isWordChar(src[l.pos]) {
				l.pos++
			}
			word := src[start:l.pos]
			if strings.EqualFold(word, "cf") && l.pos < len(src) && src[l.pos] == '[' {
				end := strings.IndexByte(src[l.pos:], ']')
				if end < 0 {
					return nil, newError(Pos(start), "cf[ is not closed")
				}
				id := src[l.pos+1 : l.pos+end]
				if _, err := strconv.ParseInt(id, 10, 64); err != nil {
					return nil, newError(Pos(start), "cf[...] needs a numeric field id")
				}
				l.pos += end + 1
				l.toks = append(l.toks, token{kind: tCustom, text: id, pos: Pos(start)})
				continue
			}
			l.toks = append(l.toks, token{kind: tWord, text: word, pos: Pos(start)})
		default:
			return nil, newError(Pos(start), "unexpected character %s", strconv.QuoteRune(rune(c)))
		}
	}
}

func (l *lexer) add(k tokenKind, text string, start, n int) {
	l.toks = append(l.toks, token{kind: k, text: text, pos: Pos(start)})
	l.pos = start + n
}

func (l *lexer) quoted(q byte) (string, error) {
	start := l.pos
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && l.pos+1 < len(l.src):
			b.WriteByte(l.src[l.pos+1])
			l.pos += 2
		case c == q:
			l.pos++
			return b.String(), nil
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return "", newError(Pos(start), "the string is not closed")
}

type parser struct {
	toks    []token
	i       int
	depth   int // of parentheses and NOT
	clauses int
}

// Limits of a query; they bound the work of parsing and compiling, also for queries from parameters without a
// length limit of their own.
const (
	MaxClauses = 100
	MaxDepth   = 32
	MaxLength  = 20000
)

// Parse parses a query.
func Parse(src string) (*Query, error) {
	if len(src) > MaxLength {
		return nil, newError(MaxLength, "the query is too long")
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	q := &Query{}
	if !p.isKeyword("order") && p.peek().kind != tEOF {
		if q.Where, err = p.or(); err != nil {
			return nil, err
		}
	}
	if p.isKeyword("order") {
		p.next()
		if !p.isKeyword("by") {
			return nil, p.errorf("expected BY after ORDER")
		}
		p.next()
		for {
			f, err := p.field()
			if err != nil {
				return nil, err
			}
			o := Order{Field: f}
			if p.isKeyword("asc") {
				p.next()
			} else if p.isKeyword("desc") {
				p.next()
				o.Desc = true
			}
			q.OrderBy = append(q.OrderBy, o)
			if p.peek().kind != tComma {
				break
			}
			p.next()
		}
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, newError(t.pos, "unexpected %s", describe(t))
	}
	return q, nil
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of query"
	case tString:
		return strconv.Quote(t.text)
	}
	return "“" + t.text + "”"
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) isKeyword(k string) bool {
	t := p.peek()
	return t.kind == tWord && strings.EqualFold(t.text, k)
}

func (p *parser) errorf(msg string) error {
	t := p.peek()
	return newError(t.pos, "%s, found %s", msg, describe(t))
}

func (p *parser) or() (Expr, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("or") {
		p.next()
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &Or{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) and() (Expr, error) {
	left, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("and") {
		p.next()
		right, err := p.not()
		if err != nil {
			return nil, err
		}
		left = &And{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) not() (Expr, error) {
	if p.isKeyword("not") || p.peek().kind == tLParen {
		if p.depth++; p.depth > MaxDepth {
			return nil, newError(p.peek().pos, "the query is nested too deeply")
		}
		defer func() { p.depth-- }()
	}
	if p.isKeyword("not") {
		p.next()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return &Not{X: x}, nil
	}
	if p.peek().kind == tLParen {
		p.next()
		x, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, p.errorf("expected )")
		}
		p.next()
		return x, nil
	}
	return p.clause()
}

func (p *parser) field() (Field, error) {
	t := p.peek()
	switch t.kind {
	case tCustom:
		p.next()
		id, _ := strconv.ParseInt(t.text, 10, 64)
		return Field{CustomID: id, Pos: t.pos}, nil
	case tString:
		p.next()
		return Field{Name: t.text, Pos: t.pos}, nil
	case tWord:
		if keywords[strings.ToLower(t.text)] {
			return Field{}, p.errorf("expected a field name")
		}
		p.next()
		return Field{Name: t.text, Pos: t.pos}, nil
	}
	return Field{}, p.errorf("expected a field name")
}

func (p *parser) clause() (Expr, error) {
	if p.clauses++; p.clauses > MaxClauses {
		return nil, newError(p.peek().pos, "the query has too many clauses")
	}
	f, err := p.field()
	if err != nil {
		return nil, err
	}
	c := &Clause{Field: f, Pos: f.Pos}
	t := p.peek()
	switch {
	case t.kind == tOp:
		p.next()
		c.Op = Op(t.text)
	case p.isKeyword("in"):
		p.next()
		c.Op = OpIn
	case p.isKeyword("not"):
		p.next()
		if !p.isKeyword("in") {
			return nil, p.errorf("expected IN after NOT")
		}
		p.next()
		c.Op = OpNotIn
	case p.isKeyword("is"):
		p.next()
		c.Op = OpIs
		if p.isKeyword("not") {
			p.next()
			c.Op = OpIsNot
		}
	case p.isKeyword("was"), p.isKeyword("changed"):
		return p.history(f)
	default:
		return nil, p.errorf("expected an operator such as =, !=, ~, IN or IS")
	}
	switch c.Op {
	case OpIs, OpIsNot:
		if !p.isKeyword("empty") && !p.isKeyword("null") {
			return nil, p.errorf("expected EMPTY after IS")
		}
		c.Value = Empty{Pos: p.next().pos}
	case OpIn, OpNotIn:
		v, err := p.listOperand()
		if err != nil {
			return nil, err
		}
		c.Value = v
	default:
		v, err := p.operand()
		if err != nil {
			return nil, err
		}
		if _, isEmpty := v.(Empty); isEmpty && c.Op != OpEq && c.Op != OpNe {
			return nil, newError(c.Pos, "EMPTY can only be compared with = and !=")
		}
		c.Value = v
	}
	return c, nil
}

// predicates are the words that narrow a history clause (only there: elsewhere they are values).
var predicates = map[string]bool{"after": true, "before": true, "on": true, "during": true, "by": true, "from": true, "to": true}

// history parses WAS [NOT] [IN] value and CHANGED, followed by predicates.
func (p *parser) history(f Field) (Expr, error) {
	h := &History{Field: f, Pos: f.Pos}
	if p.isKeyword("changed") {
		p.next()
		h.Op = OpChanged
	} else {
		p.next()
		h.Op = OpWas
		if p.isKeyword("not") {
			p.next()
			h.Op = OpWasNot
		}
		var v Operand
		var err error
		if p.isKeyword("in") {
			p.next()
			if h.Op == OpWasNot {
				h.Op = OpWasNotIn
			} else {
				h.Op = OpWasIn
			}
			v, err = p.listOperand()
		} else {
			v, err = p.operand()
		}
		if err != nil {
			return nil, err
		}
		h.Value = v
	}
	for {
		t := p.peek()
		kind := strings.ToLower(t.text)
		if t.kind != tWord || !predicates[kind] {
			return h, nil
		}
		if (kind == "from" || kind == "to") && h.Op != OpChanged {
			return nil, newError(t.pos, "FROM and TO can only follow CHANGED")
		}
		p.next()
		pr := Predicate{Kind: kind, Pos: t.pos}
		var err error
		switch {
		case kind == "during":
			if pr.Value, err = p.listOperand(); err != nil {
				return nil, err
			}
			if l, ok := pr.Value.(List); !ok || len(l.Items) != 2 {
				return nil, newError(t.pos, "DURING needs two dates: DURING (\"2026/01/01\", \"2026/03/31\")")
			}
		case kind == "by" && p.peek().kind == tLParen:
			pr.Value, err = p.listOperand()
		default:
			pr.Value, err = p.operand()
		}
		if err != nil {
			return nil, err
		}
		h.Predicates = append(h.Predicates, pr)
	}
}

func (p *parser) listOperand() (Operand, error) {
	t := p.peek()
	if t.kind == tWord && p.toks[p.i+1].kind == tLParen && !keywords[strings.ToLower(t.text)] {
		return p.operand() // a function returning a list
	}
	if t.kind != tLParen {
		return nil, p.errorf("expected ( after IN")
	}
	p.next()
	l := List{Pos: t.pos}
	for {
		v, err := p.operand()
		if err != nil {
			return nil, err
		}
		l.Items = append(l.Items, v)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		if p.peek().kind != tRParen {
			return nil, p.errorf("expected , or )")
		}
		p.next()
		return l, nil
	}
}

func (p *parser) operand() (Operand, error) {
	t := p.peek()
	switch t.kind {
	case tString:
		p.next()
		return Value{Text: t.text, Quoted: true, Pos: t.pos}, nil
	case tWord:
		lower := strings.ToLower(t.text)
		if lower == "empty" || lower == "null" {
			p.next()
			return Empty{Pos: t.pos}, nil
		}
		if keywords[lower] {
			return nil, p.errorf("expected a value")
		}
		p.next()
		if p.peek().kind == tLParen {
			p.next()
			f := Func{Name: t.text, Pos: t.pos}
			if p.peek().kind != tRParen {
				for {
					a := p.next()
					if a.kind != tWord && a.kind != tString {
						return nil, newError(a.pos, "function arguments must be values")
					}
					f.Args = append(f.Args, Value{Text: a.text, Quoted: a.kind == tString, Pos: a.pos})
					if p.peek().kind == tComma {
						p.next()
						continue
					}
					break
				}
			}
			if p.peek().kind != tRParen {
				return nil, p.errorf("expected ) after the function arguments")
			}
			p.next()
			return f, nil
		}
		return Value{Text: t.text, Pos: t.pos}, nil
	}
	return nil, p.errorf("expected a value")
}
