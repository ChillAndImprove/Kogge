// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-FileCopyrightText: 2015-2017 Marijn Haverbeke and others
// SPDX-License-Identifier: EUPL-1.2 AND MIT

package prosemirror

import (
	"fmt"
	"math/bits"
	"strconv"
)

// A content expression ("paragraph block*", "(tableCell | tableHeader)*") as a nondeterministic automaton over
// node types. A match is the set of states the automaton may be in, as a bit set (0: no match).
type contentMatch struct {
	eps    [][]int
	edges  [][]contentEdge
	accept int
	inline bool // the expression names inline nodes
	empty  bool // the empty expression: leaf nodes
	first  uint64
}

type contentEdge struct {
	types []*NodeType
	to    int
}

type exprKind int

const (
	exprName exprKind = iota
	exprChoice
	exprSeq
	exprPlus
	exprStar
	exprOpt
	exprRange
)

type expr struct {
	kind     exprKind
	types    []*NodeType
	exprs    []*expr
	min, max int // range; max -1 is unbounded
}

type exprStream struct {
	tokens []string
	pos    int
	schema *Schema
	inline *bool
}

func (s *exprStream) next() string {
	if s.pos < len(s.tokens) {
		return s.tokens[s.pos]
	}
	return ""
}

func (s *exprStream) eat(tok string) bool {
	if s.next() == tok {
		s.pos++
		return true
	}
	return false
}

// tokenize splits an expression into names and punctuation, as ProseMirror does.
func tokenize(src string) []string {
	var out []string
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case isWordChar(c):
			j := i
			for j < len(src) && isWordChar(src[j]) {
				j++
			}
			out = append(out, src[i:j])
			i = j
		default:
			out = append(out, src[i:i+1])
			i++
		}
	}
	return out
}

func isWordChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func parseContent(src string, schema *Schema) (*contentMatch, error) {
	s := &exprStream{tokens: tokenize(src), schema: schema}
	if len(s.tokens) == 0 {
		return &contentMatch{eps: [][]int{nil}, edges: [][]contentEdge{nil}, accept: 0, empty: true, first: 1}, nil
	}
	var e *expr
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("content expression %q: %v", src, r)
			}
		}()
		e = s.parseExpr()
		if s.next() != "" {
			panic("unexpected trailing text")
		}
	}()
	if err != nil {
		return nil, err
	}
	m := &contentMatch{}
	m.eps, m.edges = [][]int{nil}, [][]contentEdge{nil}
	m.accept = m.compile(e, 0)
	if len(m.eps) > 64 {
		return nil, fmt.Errorf("content expression %q is too complex", src)
	}
	m.inline = s.inline != nil && *s.inline
	m.first = m.closure(1)
	return m, nil
}

func (s *exprStream) parseExpr() *expr {
	exprs := []*expr{s.parseSeq()}
	for s.eat("|") {
		exprs = append(exprs, s.parseSeq())
	}
	if len(exprs) == 1 {
		return exprs[0]
	}
	return &expr{kind: exprChoice, exprs: exprs}
}

func (s *exprStream) parseSeq() *expr {
	var exprs []*expr
	for {
		exprs = append(exprs, s.parseSubscript())
		if n := s.next(); n == "" || n == ")" || n == "|" {
			break
		}
	}
	if len(exprs) == 1 {
		return exprs[0]
	}
	return &expr{kind: exprSeq, exprs: exprs}
}

func (s *exprStream) parseSubscript() *expr {
	e := s.parseAtom()
	for {
		switch {
		case s.eat("+"):
			e = &expr{kind: exprPlus, exprs: []*expr{e}}
		case s.eat("*"):
			e = &expr{kind: exprStar, exprs: []*expr{e}}
		case s.eat("?"):
			e = &expr{kind: exprOpt, exprs: []*expr{e}}
		case s.eat("{"):
			e = s.parseRange(e)
		default:
			return e
		}
	}
}

func (s *exprStream) parseNum() int {
	n, err := strconv.Atoi(s.next())
	if err != nil {
		panic("expected a number")
	}
	s.pos++
	return n
}

func (s *exprStream) parseRange(e *expr) *expr {
	min := s.parseNum()
	max := min
	if s.eat(",") {
		if s.next() != "}" {
			max = s.parseNum()
		} else {
			max = -1
		}
	}
	if !s.eat("}") {
		panic("unclosed braced range")
	}
	return &expr{kind: exprRange, exprs: []*expr{e}, min: min, max: max}
}

func (s *exprStream) parseAtom() *expr {
	if s.eat("(") {
		e := s.parseExpr()
		if !s.eat(")") {
			panic("missing closing paren")
		}
		return e
	}
	name := s.next()
	if name == "" || !isWordChar(name[0]) {
		panic(fmt.Sprintf("unexpected token %q", name))
	}
	var types []*NodeType
	if t := s.schema.nodes[name]; t != nil {
		types = []*NodeType{t}
	} else {
		for _, t := range s.schema.Nodes {
			if t.InGroup(name) {
				types = append(types, t)
			}
		}
	}
	if len(types) == 0 {
		panic(fmt.Sprintf("no node type or group %q", name))
	}
	for _, t := range types {
		if s.inline == nil {
			inline := t.Inline
			s.inline = &inline
		} else if *s.inline != t.Inline {
			panic("mixing inline and block content")
		}
	}
	s.pos++
	return &expr{kind: exprName, types: types}
}

func (m *contentMatch) node() int {
	m.eps = append(m.eps, nil)
	m.edges = append(m.edges, nil)
	return len(m.eps) - 1
}

func (m *contentMatch) connect(from, to int) { m.eps[from] = append(m.eps[from], to) }

// compile adds the automaton of an expression that starts in state from and returns its end state.
func (m *contentMatch) compile(e *expr, from int) int {
	switch e.kind {
	case exprName:
		to := m.node()
		m.edges[from] = append(m.edges[from], contentEdge{types: e.types, to: to})
		return to
	case exprSeq:
		for _, sub := range e.exprs {
			from = m.compile(sub, from)
		}
		return from
	case exprChoice:
		out := m.node()
		for _, sub := range e.exprs {
			m.connect(m.compile(sub, from), out)
		}
		return out
	case exprStar:
		loop := m.node()
		m.connect(from, loop)
		m.connect(m.compile(e.exprs[0], loop), loop)
		return loop
	case exprPlus:
		loop := m.node()
		m.connect(m.compile(e.exprs[0], from), loop)
		m.connect(m.compile(e.exprs[0], loop), loop)
		return loop
	case exprOpt:
		out := m.node()
		m.connect(from, out)
		m.connect(m.compile(e.exprs[0], from), out)
		return out
	default: // range
		cur := from
		for i := 0; i < e.min; i++ {
			cur = m.compile(e.exprs[0], cur)
		}
		if e.max == -1 {
			m.connect(m.compile(e.exprs[0], cur), cur)
			return cur
		}
		out := m.node()
		m.connect(cur, out)
		for i := e.min; i < e.max; i++ {
			cur = m.compile(e.exprs[0], cur)
			m.connect(cur, out)
		}
		return out
	}
}

// closure adds the states reachable without consuming a node.
func (m *contentMatch) closure(set uint64) uint64 {
	for {
		next := set
		for s := set; s != 0; s &= s - 1 {
			for _, to := range m.eps[bits.TrailingZeros64(s)] {
				next |= 1 << to
			}
		}
		if next == set {
			return set
		}
		set = next
	}
}

func (m *contentMatch) start() uint64 { return m.first }

func (m *contentMatch) matchType(set uint64, t *NodeType) uint64 {
	var next uint64
	for s := set; s != 0; s &= s - 1 {
		for _, e := range m.edges[bits.TrailingZeros64(s)] {
			for _, x := range e.types {
				if x == t {
					next |= 1 << e.to
					break
				}
			}
		}
	}
	if next == 0 {
		return 0
	}
	return m.closure(next)
}

// matchFragment advances a match over the children start..end of a fragment; 0 if they do not match.
func (m *contentMatch) matchFragment(set uint64, f Fragment, start, end int) uint64 {
	for i := start; set != 0 && i < end; i++ {
		set = m.matchType(set, f.nodes[i].typ)
	}
	return set
}

func (m *contentMatch) validEnd(set uint64) bool { return set&(1<<m.accept) != 0 }

// nextTypes are the node types that may come next in a match.
func (m *contentMatch) nextTypes(set uint64) map[*NodeType]bool {
	out := map[*NodeType]bool{}
	for s := set; s != 0; s &= s - 1 {
		for _, e := range m.edges[bits.TrailingZeros64(s)] {
			for _, t := range e.types {
				out[t] = true
			}
		}
	}
	return out
}

// compatible reports whether the two expressions can start with a common node type.
func (m *contentMatch) compatible(other *contentMatch) bool {
	mine := m.nextTypes(m.first)
	for t := range other.nextTypes(other.first) {
		if mine[t] {
			return true
		}
	}
	return false
}
