// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Conditions (if) decide whether stages and jobs run, from what is known when a run is created: its event, branch,
// tag and pull request, the commit message, variables and the files a change touched. They are a small language
// without side effects: comparisons, && || !, parentheses, in [...] and a few functions. Conditions are checked for
// their types when the file is read, and evaluating one takes time proportional to its length (changed() also to
// the number of changed files, once per run).

// Limits of conditions.
const (
	MaxCondition      = 2000 // characters
	maxConditionDepth = 32   // nesting of parentheses and operators
	maxListItems      = 100
	maxChangedArgs    = 16
)

// ConditionNames are the names a condition can use besides vars.NAME, with their meaning.
var ConditionNames = []string{"event", "branch", "tag", "ref", "source_branch", "target_branch", "commit_message"}

// Condition is a parsed condition.
type Condition struct {
	src  string
	root *expr
}

// ParseCondition reads a condition. Errors are of type *ConditionError and tell where in the text the problem is.
func ParseCondition(src string) (*Condition, error) {
	if len(src) > MaxCondition {
		return nil, &ConditionError{Message: fmt.Sprintf("the condition has more than %d characters", MaxCondition)}
	}
	if strings.TrimSpace(src) == "" {
		return nil, &ConditionError{Message: "the condition is empty"}
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &condParser{toks: toks}
	root, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.unexpected(t)
	}
	if root.kind != kindBool {
		return nil, &ConditionError{Offset: root.pos, Message: "the condition must be true or false, such as branch == 'main'"}
	}
	return &Condition{src: src, root: root}, nil
}

// String returns the condition as it was written.
func (c *Condition) String() string { return c.src }

// MarshalJSON stores a condition as its text (jobs are stored as JSON).
func (c *Condition) MarshalJSON() ([]byte, error) { return json.Marshal(c.src) }

// UnmarshalJSON reads a condition stored as its text. Conditions are evaluated when a run is created; a stored one
// that does not parse (as an agent of another version may find it in a job) keeps its text and counts as true.
func (c *Condition) UnmarshalJSON(b []byte) error {
	var src string
	if err := json.Unmarshal(b, &src); err != nil {
		return err
	}
	*c = Condition{src: src}
	if parsed, err := ParseCondition(src); err == nil {
		*c = *parsed
	}
	return nil
}

// UsesChanges reports whether the condition looks at the changed files (changed()).
func (c *Condition) UsesChanges() bool {
	return c != nil && c.root != nil && c.root.uses("changed")
}

// ConditionError is a mistake in a condition; Offset is the byte offset in its text.
type ConditionError struct {
	Offset  int
	Message string
}

func (e *ConditionError) Error() string { return e.Message }

// Env is what a condition is evaluated against.
type Env struct {
	Event Event
	// Message is the commit message of the run.
	Message string
	// Variables are those the stage or job sees when the run is created: of the file, the pipeline, the job (with
	// its matrix values) and the run.
	Variables map[string]string
	// changed caches the results of changed() for a run: the changes are the same for all its stages and jobs.
	changed map[string]bool
}

// Eval evaluates the condition.
func (c *Condition) Eval(env *Env) bool {
	if c == nil || c.root == nil {
		return true
	}
	return c.root.eval(env).b
}

type valueKind int

const (
	kindString valueKind = iota
	kindBool
	kindList
)

func (k valueKind) String() string {
	return [...]string{"a text", "true or false", "a list"}[k]
}

type value struct {
	s string
	b bool
}

// expr is a node of a condition.
type expr struct {
	op   string // lit, bool, name, var, call, list, ==, !=, in, !, &&, ||
	str  string // literal text, name, variable or function
	b    bool
	args []*expr
	pos  int
	kind valueKind
}

func (e *expr) uses(fn string) bool {
	if e.op == "call" && e.str == fn {
		return true
	}
	for _, a := range e.args {
		if a.uses(fn) {
			return true
		}
	}
	return false
}

func (e *expr) eval(env *Env) value {
	switch e.op {
	case "lit":
		return value{s: e.str}
	case "bool":
		return value{b: e.b}
	case "name":
		return value{s: contextValue(e.str, env)}
	case "var":
		return value{s: env.Variables[e.str]}
	case "==":
		return value{b: e.args[0].eval(env) == e.args[1].eval(env)}
	case "!=":
		return value{b: e.args[0].eval(env) != e.args[1].eval(env)}
	case "in":
		v := e.args[0].eval(env).s
		for _, item := range e.args[1].args {
			if item.eval(env).s == v {
				return value{b: true}
			}
		}
		return value{}
	case "!":
		return value{b: !e.args[0].eval(env).b}
	case "&&":
		return value{b: e.args[0].eval(env).b && e.args[1].eval(env).b}
	case "||":
		return value{b: e.args[0].eval(env).b || e.args[1].eval(env).b}
	case "call":
		return value{b: call(e, env)}
	}
	return value{}
}

func call(e *expr, env *Env) bool {
	arg := func(i int) string { return e.args[i].eval(env).s }
	switch e.str {
	case "startsWith":
		return strings.HasPrefix(arg(0), arg(1))
	case "endsWith":
		return strings.HasSuffix(arg(0), arg(1))
	case "contains":
		return strings.Contains(arg(0), arg(1))
	case "matches":
		return MatchGlob(arg(1), arg(0))
	case "changed":
		patterns := make([]string, len(e.args))
		for i := range e.args {
			patterns[i] = arg(i)
		}
		return changed(env, patterns)
	}
	return false
}

// changed reports whether the event changed a file that matches one of the patterns. Unknown changes (tags, runs
// started by hand, by the API or a schedule, changes of more than 10,000 files) count as changes of every file, as
// for paths filters: a condition must not leave out what may have changed.
func changed(env *Env, patterns []string) bool {
	if !env.Event.ChangesKnown {
		return true
	}
	key := strings.Join(patterns, "\x00")
	if v, ok := env.changed[key]; ok {
		return v
	}
	result := false
	for _, f := range env.Event.Changes {
		if matchAny(patterns, f) {
			result = true
			break
		}
	}
	if env.changed == nil {
		env.changed = map[string]bool{}
	}
	env.changed[key] = result
	return result
}

// contextValue returns the value of a name of the run's context.
func contextValue(name string, env *Env) string {
	ev := env.Event
	switch name {
	case "event":
		return ev.Kind
	case "branch":
		return ev.Branch
	case "tag":
		return ev.Tag
	case "ref":
		switch {
		case ev.Tag != "":
			return "refs/tags/" + ev.Tag
		case ev.Branch != "":
			return "refs/heads/" + ev.Branch
		}
		return ""
	case "source_branch":
		if ev.Kind == EventPullRequest {
			return ev.Branch
		}
		return ""
	case "target_branch":
		return ev.TargetBranch
	case "commit_message":
		return env.Message
	}
	return ""
}

// The functions of conditions: their number of arguments (at least, at most), which are all texts.
var functions = map[string][2]int{
	"startsWith": {2, 2},
	"endsWith":   {2, 2},
	"contains":   {2, 2},
	"matches":    {2, 2},
	"changed":    {1, maxChangedArgs},
}

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tOp // == != && || !
	tPunct
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func lex(src string) ([]token, error) {
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'' || c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(src) && src[j] != c; j++ {
				if src[j] == '\\' && j+1 < len(src) && (src[j+1] == c || src[j+1] == '\\') {
					j++
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, &ConditionError{Offset: i, Message: "a text is not closed with " + string(c)}
			}
			out = append(out, token{kind: tString, text: b.String(), pos: i})
			i = j + 1
		case isIdentStart(c):
			j := i
			for j < len(src) && (isIdentStart(src[j]) || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			out = append(out, token{kind: tIdent, text: src[i:j], pos: i})
			i = j
		case strings.HasPrefix(src[i:], "==") || strings.HasPrefix(src[i:], "!=") || strings.HasPrefix(src[i:], "&&") ||
			strings.HasPrefix(src[i:], "||"):
			out = append(out, token{kind: tOp, text: src[i : i+2], pos: i})
			i += 2
		case c == '!':
			out = append(out, token{kind: tOp, text: "!", pos: i})
			i++
		case strings.IndexByte("()[],.", c) >= 0:
			out = append(out, token{kind: tPunct, text: string(c), pos: i})
			i++
		case c == '=' || c == '&' || c == '|':
			return nil, &ConditionError{Offset: i, Message: fmt.Sprintf("%q is not an operator; use ==, !=, && or ||", string(c))}
		default:
			r, _ := utf8.DecodeRuneInString(src[i:])
			return nil, &ConditionError{Offset: i, Message: fmt.Sprintf("unexpected %q", r)}
		}
	}
	return append(out, token{kind: tEOF, pos: len(src)}), nil
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

type condParser struct {
	toks []token
	i    int
}

func (p *condParser) peek() token { return p.toks[p.i] }

func (p *condParser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *condParser) is(text string) bool {
	t := p.peek()
	return (t.kind == tOp || t.kind == tPunct) && t.text == text
}

func (p *condParser) unexpected(t token) error {
	if t.kind == tEOF {
		return &ConditionError{Offset: t.pos, Message: "the condition ends too early"}
	}
	return &ConditionError{Offset: t.pos, Message: fmt.Sprintf("unexpected %q", t.text)}
}

func (p *condParser) deep(depth int) error {
	if depth > maxConditionDepth {
		return &ConditionError{Offset: p.peek().pos, Message: "the condition is nested too deeply"}
	}
	return nil
}

// both checks that the operands of && and || are conditions.
func both(op string, l, r *expr) error {
	for _, e := range []*expr{l, r} {
		if e.kind != kindBool {
			return &ConditionError{Offset: e.pos, Message: fmt.Sprintf("%s needs a condition on both sides, not %s", op, e.kind)}
		}
	}
	return nil
}

func (p *condParser) or(depth int) (*expr, error) {
	if err := p.deep(depth); err != nil {
		return nil, err
	}
	left, err := p.and(depth + 1)
	if err != nil {
		return nil, err
	}
	for p.is("||") {
		op := p.next()
		right, err := p.and(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := both("||", left, right); err != nil {
			return nil, err
		}
		left = &expr{op: "||", args: []*expr{left, right}, pos: op.pos, kind: kindBool}
	}
	return left, nil
}

func (p *condParser) and(depth int) (*expr, error) {
	left, err := p.not(depth + 1)
	if err != nil {
		return nil, err
	}
	for p.is("&&") {
		op := p.next()
		right, err := p.not(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := both("&&", left, right); err != nil {
			return nil, err
		}
		left = &expr{op: "&&", args: []*expr{left, right}, pos: op.pos, kind: kindBool}
	}
	return left, nil
}

func (p *condParser) not(depth int) (*expr, error) {
	if err := p.deep(depth); err != nil {
		return nil, err
	}
	if p.is("!") {
		op := p.next()
		e, err := p.not(depth + 1)
		if err != nil {
			return nil, err
		}
		if e.kind != kindBool {
			return nil, &ConditionError{Offset: e.pos, Message: "! needs a condition, not " + e.kind.String()}
		}
		return &expr{op: "!", args: []*expr{e}, pos: op.pos, kind: kindBool}, nil
	}
	return p.compare(depth + 1)
}

func (p *condParser) compare(depth int) (*expr, error) {
	left, err := p.primary(depth + 1)
	if err != nil {
		return nil, err
	}
	switch t := p.peek(); {
	case p.is("==") || p.is("!="):
		p.next()
		right, err := p.primary(depth + 1)
		if err != nil {
			return nil, err
		}
		if left.kind == kindList || right.kind == kindList {
			return nil, &ConditionError{Offset: t.pos, Message: "lists are compared with in, such as branch in ['main', 'develop']"}
		}
		if left.kind != right.kind {
			return nil, &ConditionError{Offset: t.pos, Message: fmt.Sprintf("%s compares %s with %s", t.text, left.kind, right.kind)}
		}
		return &expr{op: t.text, args: []*expr{left, right}, pos: t.pos, kind: kindBool}, nil
	case t.kind == tIdent && t.text == "in":
		p.next()
		if left.kind != kindString {
			return nil, &ConditionError{Offset: left.pos, Message: "in needs a text on its left, not " + left.kind.String()}
		}
		if !p.is("[") {
			return nil, &ConditionError{Offset: p.peek().pos, Message: "in needs a list, such as ['main', 'develop']"}
		}
		right, err := p.list(depth + 1)
		if err != nil {
			return nil, err
		}
		return &expr{op: "in", args: []*expr{left, right}, pos: t.pos, kind: kindBool}, nil
	}
	if left.kind == kindList {
		return nil, &ConditionError{Offset: left.pos, Message: "a list can only follow in"}
	}
	return left, nil
}

func (p *condParser) list(depth int) (*expr, error) {
	open := p.next()
	l := &expr{op: "list", pos: open.pos, kind: kindList}
	for !p.is("]") {
		if len(l.args) > 0 {
			if !p.is(",") {
				return nil, p.unexpected(p.peek())
			}
			p.next()
		}
		item, err := p.primary(depth + 1)
		if err != nil {
			return nil, err
		}
		if item.kind != kindString {
			return nil, &ConditionError{Offset: item.pos, Message: "the items of a list must be texts"}
		}
		if l.args = append(l.args, item); len(l.args) > maxListItems {
			return nil, &ConditionError{Offset: item.pos, Message: fmt.Sprintf("a list may have at most %d items", maxListItems)}
		}
	}
	p.next()
	return l, nil
}

func (p *condParser) primary(depth int) (*expr, error) {
	if err := p.deep(depth); err != nil {
		return nil, err
	}
	t := p.next()
	switch {
	case t.kind == tString:
		return &expr{op: "lit", str: t.text, pos: t.pos, kind: kindString}, nil
	case t.kind == tPunct && t.text == "(":
		e, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if !p.is(")") {
			return nil, p.unexpected(p.peek())
		}
		p.next()
		return e, nil
	case t.kind == tPunct && t.text == "[":
		p.i--
		return p.list(depth + 1)
	case t.kind != tIdent:
		return nil, p.unexpected(t)
	case t.text == "true" || t.text == "false":
		return &expr{op: "bool", b: t.text == "true", pos: t.pos, kind: kindBool}, nil
	case t.text == "vars":
		if !p.is(".") {
			return nil, &ConditionError{Offset: t.pos, Message: "write vars.NAME for the variable NAME"}
		}
		p.next()
		name := p.next()
		if name.kind != tIdent || !varPattern.MatchString(name.text) {
			return nil, &ConditionError{Offset: name.pos, Message: "write vars.NAME for the variable NAME"}
		}
		return &expr{op: "var", str: name.text, pos: t.pos, kind: kindString}, nil
	case p.is("("):
		return p.call(t, depth)
	case slices.Contains(ConditionNames, t.text):
		return &expr{op: "name", str: t.text, pos: t.pos, kind: kindString}, nil
	}
	return nil, &ConditionError{Offset: t.pos, Message: fmt.Sprintf("%q is not known; conditions use %s, vars.NAME and the functions "+
		"startsWith, endsWith, contains, matches and changed", t.text, strings.Join(ConditionNames, ", "))}
}

func (p *condParser) call(name token, depth int) (*expr, error) {
	arity, ok := functions[name.text]
	if !ok {
		return nil, &ConditionError{Offset: name.pos, Message: fmt.Sprintf("there is no function %s; use startsWith, endsWith, contains, matches or changed",
			name.text)}
	}
	p.next() // (
	e := &expr{op: "call", str: name.text, pos: name.pos, kind: kindBool}
	for !p.is(")") {
		if len(e.args) > 0 {
			if !p.is(",") {
				return nil, p.unexpected(p.peek())
			}
			p.next()
		}
		arg, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if arg.kind != kindString {
			return nil, &ConditionError{Offset: arg.pos, Message: fmt.Sprintf("the arguments of %s must be texts, not %s", name.text, arg.kind)}
		}
		e.args = append(e.args, arg)
		if len(e.args) > arity[1] {
			break
		}
	}
	if len(e.args) < arity[0] || len(e.args) > arity[1] {
		want := fmt.Sprintf("%d", arity[0])
		if arity[1] != arity[0] {
			want = fmt.Sprintf("%d to %d", arity[0], arity[1])
		}
		return nil, &ConditionError{Offset: name.pos, Message: fmt.Sprintf("%s needs %s arguments", name.text, want)}
	}
	p.next() // )
	return e, nil
}
