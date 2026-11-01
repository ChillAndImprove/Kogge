// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package kql parses KQL, Kogge's issue query language. It produces an AST with source
// positions; resolving names and generating SQL is the issues service's job.
package kql

import (
	"fmt"
	"strconv"
	"strings"
)

// Pos is a byte offset in the query.
type Pos int

// Query is a parsed query.
type Query struct {
	Where   Expr // nil for "all issues"
	OrderBy []Order
}

// Expr is a boolean expression.
type Expr interface {
	exprNode()
	String() string
}

// And is a conjunction.
type And struct{ Left, Right Expr }

// Or is a disjunction.
type Or struct{ Left, Right Expr }

// Not negates an expression.
type Not struct{ X Expr }

// Op is a clause operator.
type Op string

// Operators.
const (
	OpEq       Op = "="
	OpNe       Op = "!="
	OpGt       Op = ">"
	OpGe       Op = ">="
	OpLt       Op = "<"
	OpLe       Op = "<="
	OpLike     Op = "~"
	OpNotLike  Op = "!~"
	OpIn       Op = "in"
	OpNotIn    Op = "not in"
	OpIs       Op = "is"
	OpIsNot    Op = "is not"
	OpWas      Op = "was"
	OpChanged  Op = "changed"
	OpWasNot   Op = "was not"
	OpWasIn    Op = "was in"
	OpWasNotIn Op = "was not in"
)

// Clause compares a field with an operand.
type Clause struct {
	Field Field
	Op    Op
	Value Operand
	Pos   Pos
}

// History is a clause about the past of a field: WAS (with NOT and IN) or CHANGED, narrowed by predicates.
type History struct {
	Field      Field
	Op         Op      // OpWas, OpWasNot, OpWasIn, OpWasNotIn or OpChanged
	Value      Operand // nil for CHANGED
	Predicates []Predicate
	Pos        Pos
}

// Predicate narrows a history clause: AFTER, BEFORE, ON, DURING (a list of two dates), BY, FROM or TO.
type Predicate struct {
	Kind  string // after, before, on, during, by, from, to
	Value Operand
	Pos   Pos
}

// Field is a field reference: a name or a custom field id.
type Field struct {
	Name     string // as written (unquoted), or "" for cf[id]
	CustomID int64
	Pos      Pos
}

func (f Field) String() string {
	if f.CustomID != 0 {
		return "cf[" + strconv.FormatInt(f.CustomID, 10) + "]"
	}
	return quoteIfNeeded(f.Name)
}

// Operand is the right-hand side of a clause.
type Operand interface {
	operandNode()
	String() string
}

// Value is a single value (string or word).
type Value struct {
	Text   string
	Quoted bool
	Pos    Pos
}

// List is a parenthesised list of operands.
type List struct {
	Items []Operand
	Pos   Pos
}

// Func is a function call.
type Func struct {
	Name string
	Args []Value
	Pos  Pos
}

// Empty is EMPTY or NULL.
type Empty struct{ Pos Pos }

// Order is one ORDER BY item.
type Order struct {
	Field Field
	Desc  bool
}

func (And) exprNode()     {}
func (Or) exprNode()      {}
func (Not) exprNode()     {}
func (Clause) exprNode()  {}
func (History) exprNode() {}

func (Value) operandNode() {}
func (List) operandNode()  {}
func (Func) operandNode()  {}
func (Empty) operandNode() {}

func (e And) String() string { return "(" + e.Left.String() + " AND " + e.Right.String() + ")" }
func (e Or) String() string  { return "(" + e.Left.String() + " OR " + e.Right.String() + ")" }
func (e Not) String() string { return "NOT " + e.X.String() }
func (c Clause) String() string {
	return c.Field.String() + " " + strings.ToUpper(string(c.Op)) + " " + c.Value.String()
}

func (h History) String() string {
	var b strings.Builder
	b.WriteString(h.Field.String() + " " + strings.ToUpper(string(h.Op)))
	if h.Value != nil {
		b.WriteString(" " + h.Value.String())
	}
	for _, p := range h.Predicates {
		b.WriteString(" " + strings.ToUpper(p.Kind) + " " + p.Value.String())
	}
	return b.String()
}

func (v Value) String() string {
	if v.Quoted || needsQuotes(v.Text) {
		return strconv.Quote(v.Text)
	}
	return v.Text
}

func (l List) String() string {
	parts := make([]string, len(l.Items))
	for i, it := range l.Items {
		parts[i] = it.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (f Func) String() string {
	parts := make([]string, len(f.Args))
	for i, a := range f.Args {
		parts[i] = a.String()
	}
	return f.Name + "(" + strings.Join(parts, ", ") + ")"
}

func (Empty) String() string { return "EMPTY" }

// String prints the query in canonical form.
func (q *Query) String() string {
	var b strings.Builder
	if q.Where != nil {
		b.WriteString(q.Where.String())
	}
	if len(q.OrderBy) > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("ORDER BY ")
		for i, o := range q.OrderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(o.Field.String())
			if o.Desc {
				b.WriteString(" DESC")
			}
		}
	}
	return b.String()
}

func needsQuotes(s string) bool {
	if s == "" || keywords[strings.ToLower(s)] {
		return true
	}
	for i := 0; i < len(s); i++ {
		if !isWordChar(s[i]) {
			return true
		}
	}
	return false
}

func quoteIfNeeded(s string) string {
	if needsQuotes(s) {
		return strconv.Quote(s)
	}
	return s
}

// Error is a syntax error at a position.
type Error struct {
	Pos     Pos
	Message string
	// Format and Args are the message before formatting, for its translations.
	Format string
	Args   []any
}

// newError formats the message of an error at a position.
func newError(pos Pos, format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{Pos: pos, Message: msg, Format: format, Args: args}
}

func (e *Error) Error() string { return fmt.Sprintf("%s (at position %d)", e.Message, e.Pos) }

// Walk calls fn for every clause in the expression.
func Walk(e Expr, fn func(c *Clause)) {
	switch x := e.(type) {
	case *And:
		Walk(x.Left, fn)
		Walk(x.Right, fn)
	case *Or:
		Walk(x.Left, fn)
		Walk(x.Right, fn)
	case *Not:
		Walk(x.X, fn)
	case *Clause:
		fn(x)
	}
}
