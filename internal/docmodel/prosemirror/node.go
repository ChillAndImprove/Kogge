// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-FileCopyrightText: 2015-2017 Marijn Haverbeke and others
// SPDX-License-Identifier: EUPL-1.2 AND MIT

package prosemirror

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

// failure is the error of a step or a document that cannot be applied or built. Within the package it is raised
// as a panic (where ProseMirror throws) and returned as an error at its boundary.
type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func fail(format string, args ...any) { panic(failure{msg: fmt.Sprintf(format, args...)}) }

// catch turns a failure, or a runtime error of malformed input, into an error.
func catch(err *error) {
	r := recover()
	switch x := r.(type) {
	case nil:
	case failure:
		*err = x
	case runtime.Error:
		*err = fmt.Errorf("malformed input: %v", x)
	default:
		panic(r)
	}
}

// Mark is a mark on an inline node: its type and attributes.
type Mark struct {
	Type  *MarkType
	Attrs map[string]any
}

// Eq reports whether two marks have the same type and attributes.
func (m *Mark) Eq(o *Mark) bool { return m == o || m.Type == o.Type && attrsEqual(m.Attrs, o.Attrs) }

// AddToSet adds the mark to a set of marks, which stays ordered by rank; marks it excludes are dropped, and it is
// not added if one of the set excludes it.
func (m *Mark) AddToSet(set []*Mark) []*Mark {
	var out []*Mark
	copied, placed := false, false
	for i, other := range set {
		if m.Eq(other) {
			return set
		}
		if m.Type.excludes(other.Type) {
			if !copied {
				out, copied = append([]*Mark(nil), set[:i]...), true
			}
		} else if other.Type.excludes(m.Type) {
			return set
		} else {
			if !placed && other.Type.Rank > m.Type.Rank {
				if !copied {
					out, copied = append([]*Mark(nil), set[:i]...), true
				}
				out = append(out, m)
				placed = true
			}
			if copied {
				out = append(out, other)
			}
		}
	}
	if !copied {
		out = append([]*Mark(nil), set...)
	}
	if !placed {
		out = append(out, m)
	}
	return out
}

// RemoveFromSet removes the mark (by type and attributes) from a set.
func (m *Mark) RemoveFromSet(set []*Mark) []*Mark {
	for i, x := range set {
		if m.Eq(x) {
			out := append([]*Mark(nil), set[:i]...)
			return append(out, set[i+1:]...)
		}
	}
	return set
}

// IsInSet reports whether the set contains the mark.
func (m *Mark) IsInSet(set []*Mark) bool {
	for _, x := range set {
		if m.Eq(x) {
			return true
		}
	}
	return false
}

func (t *MarkType) excludes(other *MarkType) bool {
	for _, x := range t.excluded {
		if x == other {
			return true
		}
	}
	return false
}

func sameMarkSet(a, b []*Mark) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Eq(b[i]) {
			return false
		}
	}
	return true
}

// markSetFrom orders marks by the rank of their types.
func markSetFrom(marks []*Mark) []*Mark {
	if len(marks) == 0 {
		return nil
	}
	out := append([]*Mark(nil), marks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type.Rank < out[j].Type.Rank })
	return out
}

func attrsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// Node is a node of a document. Nodes are never changed: changes build new nodes that share the unchanged ones.
type Node struct {
	typ     *NodeType
	attrs   map[string]any
	content Fragment
	marks   []*Mark
	text    string
	len16   int // length of text in UTF-16 code units, the unit of positions
}

// Type returns the node's type.
func (n *Node) Type() *NodeType { return n.typ }

// Attrs returns the node's attributes (do not change them).
func (n *Node) Attrs() map[string]any { return n.attrs }

// Content returns the node's children.
func (n *Node) Content() Fragment { return n.content }

// Marks returns the node's marks.
func (n *Node) Marks() []*Mark { return n.marks }

// Text returns the text of a text node.
func (n *Node) Text() string { return n.text }

// IsText reports whether the node is text.
func (n *Node) IsText() bool { return n.typ.Text }

// IsLeaf reports whether the node's type has no content.
func (n *Node) IsLeaf() bool { return n.typ.Leaf }

// IsAtom reports whether the node is a leaf or an atom.
func (n *Node) IsAtom() bool { return n.typ.Atom }

// IsInline reports whether the node is inline.
func (n *Node) IsInline() bool { return n.typ.Inline }

// NodeSize is the size of the node in positions: the length of text, 1 for leaves, else its content plus 2.
func (n *Node) NodeSize() int {
	switch {
	case n.typ.Text:
		return n.len16
	case n.typ.Leaf:
		return 1
	default:
		return n.content.size + 2
	}
}

// ChildCount returns the number of children.
func (n *Node) ChildCount() int { return len(n.content.nodes) }

// NodeAt returns the node that starts at a position, or nil (also for positions outside of the node).
func (n *Node) NodeAt(pos int) (node *Node) {
	defer func() {
		if recover() != nil {
			node = nil
		}
	}()
	return n.nodeAt(pos)
}

// Height is the number of levels of nodes below this one (0 for leaves and empty nodes).
func (n *Node) Height() int {
	h := 0
	for _, c := range n.content.nodes {
		h = max(h, c.Height()+1)
	}
	return h
}

// EmptyDoc returns the document the editor starts with: one empty paragraph.
func (s *Schema) EmptyDoc() *Node {
	return s.Top.create(nil, fragmentFrom(s.nodes["paragraph"].create(nil, Fragment{}, nil)), nil)
}

// ReplaceAll is a step that replaces the whole content of a document by the content of another.
func ReplaceAll(doc, with *Node) Step {
	return &ReplaceStep{From: 0, To: doc.content.size, Slice: Slice{Content: with.content}}
}

// Child returns a child; it fails beyond the children.
func (n *Node) Child(i int) *Node { return n.content.Child(i) }

func (n *Node) copy(content Fragment) *Node {
	return &Node{typ: n.typ, attrs: n.attrs, content: content, marks: n.marks}
}

func (n *Node) mark(marks []*Mark) *Node {
	if n.typ.Text {
		return &Node{typ: n.typ, attrs: n.attrs, marks: marks, text: n.text, len16: n.len16}
	}
	return &Node{typ: n.typ, attrs: n.attrs, content: n.content, marks: marks}
}

func (n *Node) withText(text string) *Node {
	if text == n.text {
		return n
	}
	return newText(n.typ, text, n.marks)
}

// cut returns the part of the node between two positions within it.
func (n *Node) cut(from, to int) *Node {
	if n.typ.Text {
		if from == 0 && to == n.len16 {
			return n
		}
		return n.withText(sliceUTF16(n.text, from, to))
	}
	if from == 0 && to == n.content.size {
		return n
	}
	return n.copy(n.content.cut(from, to))
}

func (n *Node) sameMarkup(o *Node) bool {
	return n.typ == o.typ && attrsEqual(n.attrs, o.attrs) && sameMarkSet(n.marks, o.marks)
}

// nodeAt returns the node that starts at a position, or nil.
func (n *Node) nodeAt(pos int) *Node {
	for node := n; ; {
		index, offset := node.content.findIndex(pos, -1)
		node = node.content.maybeChild(index)
		if node == nil {
			return nil
		}
		if offset == pos || node.typ.Text {
			return node
		}
		pos -= offset + 1
	}
}

// slice returns the content between two positions, open where it cuts through nodes.
func (n *Node) slice(from, to int) Slice {
	if from == to {
		return Slice{}
	}
	rFrom, rTo := n.resolve(from), n.resolve(to)
	depth := rFrom.sharedDepth(to)
	start, node := rFrom.start(depth), rFrom.node(depth)
	content := node.content.cut(rFrom.Pos-start, rTo.Pos-start)
	return Slice{Content: content, OpenStart: rFrom.Depth() - depth, OpenEnd: rTo.Depth() - depth}
}

func (n *Node) replace(from, to int, s Slice) *Node {
	return replace(n.resolve(from), n.resolve(to), s)
}

func (n *Node) contentMatchAt(index int) uint64 {
	m := n.typ.content.matchFragment(n.typ.content.start(), n.content, 0, index)
	if m == 0 {
		fail("Called contentMatchAt on a node with invalid content")
	}
	return m
}

// canReplace reports whether replacing the children from..to by the children start..end of a fragment leaves
// valid content.
func (n *Node) canReplace(from, to int, replacement Fragment, start, end int) bool {
	c := n.typ.content
	one := c.matchFragment(n.contentMatchAt(from), replacement, start, end)
	two := c.matchFragment(one, n.content, to, n.content.ChildCount())
	if two == 0 || !c.validEnd(two) {
		return false
	}
	for i := start; i < end; i++ {
		if !n.typ.allowsMarks(replacement.nodes[i].marks) {
			return false
		}
	}
	return true
}

// create builds a node of the type; content is not checked (as in ProseMirror).
func (t *NodeType) create(attrs map[string]any, content Fragment, marks []*Mark) *Node {
	if t.Text {
		fail("NodeType.create can't construct text nodes")
	}
	return &Node{typ: t, attrs: computeAttrs(t.Attrs, attrs), content: content, marks: markSetFrom(marks)}
}

func newText(t *NodeType, text string, marks []*Mark) *Node {
	if text == "" {
		fail("Empty text nodes are not allowed")
	}
	return &Node{typ: t, attrs: emptyAttrs, text: text, len16: lenUTF16(text), marks: marks}
}

// Fragment is a list of child nodes.
type Fragment struct {
	nodes []*Node
	size  int
}

func fragmentOf(nodes []*Node) Fragment {
	size := 0
	for _, n := range nodes {
		size += n.NodeSize()
	}
	return Fragment{nodes: nodes, size: size}
}

// fragmentFromArray builds a fragment, joining adjacent text nodes with the same marks.
func fragmentFromArray(nodes []*Node) Fragment {
	var joined []*Node
	size, copied := 0, false
	for i, node := range nodes {
		size += node.NodeSize()
		if i > 0 && node.typ.Text && nodes[i-1].sameMarkup(node) {
			if !copied {
				joined, copied = append([]*Node(nil), nodes[:i]...), true
			}
			last := joined[len(joined)-1]
			joined[len(joined)-1] = node.withText(last.text + node.text)
		} else if copied {
			joined = append(joined, node)
		}
	}
	if !copied {
		joined = nodes
	}
	return Fragment{nodes: joined, size: size}
}

func fragmentFrom(n *Node) Fragment { return Fragment{nodes: []*Node{n}, size: n.NodeSize()} }

// Size returns the size of the fragment in positions.
func (f Fragment) Size() int { return f.size }

// Nodes returns the nodes of the fragment (do not change the list).
func (f Fragment) Nodes() []*Node { return f.nodes }

// ChildCount returns the number of nodes.
func (f Fragment) ChildCount() int { return len(f.nodes) }

// Child returns a node; it fails beyond the nodes.
func (f Fragment) Child(i int) *Node {
	if i < 0 || i >= len(f.nodes) {
		fail("Index %d out of range for fragment", i)
	}
	return f.nodes[i]
}

func (f Fragment) maybeChild(i int) *Node {
	if i < 0 || i >= len(f.nodes) {
		return nil
	}
	return f.nodes[i]
}

func (f Fragment) firstChild() *Node { return f.maybeChild(0) }

func (f Fragment) lastChild() *Node { return f.maybeChild(len(f.nodes) - 1) }

// cut returns the part of the fragment between two positions.
func (f Fragment) cut(from, to int) Fragment {
	if from == 0 && to == f.size {
		return f
	}
	var result []*Node
	size := 0
	if to > from {
		pos := 0
		for i := 0; pos < to; i++ {
			child := f.nodes[i]
			end := pos + child.NodeSize()
			if end > from {
				if pos < from || end > to {
					if child.typ.Text {
						child = child.cut(max(0, from-pos), min(child.len16, to-pos))
					} else {
						child = child.cut(max(0, from-pos-1), min(child.content.size, to-pos-1))
					}
				}
				result = append(result, child)
				size += child.NodeSize()
			}
			pos = end
		}
	}
	return Fragment{nodes: result, size: size}
}

func (f Fragment) cutByIndex(from, to int) Fragment {
	if from == to {
		return Fragment{}
	}
	if from == 0 && to == len(f.nodes) {
		return f
	}
	return fragmentOf(append([]*Node(nil), f.nodes[from:to]...))
}

// append joins two fragments; text at the seam with the same marks becomes one text node.
func (f Fragment) append(other Fragment) Fragment {
	if other.size == 0 {
		return f
	}
	if f.size == 0 {
		return other
	}
	last, first := f.lastChild(), other.firstChild()
	content := append([]*Node(nil), f.nodes...)
	i := 0
	if last.typ.Text && last.sameMarkup(first) {
		content[len(content)-1] = last.withText(last.text + first.text)
		i = 1
	}
	content = append(content, other.nodes[i:]...)
	return Fragment{nodes: content, size: f.size + other.size}
}

func (f Fragment) replaceChild(index int, node *Node) Fragment {
	current := f.Child(index)
	if current == node {
		return f
	}
	content := append([]*Node(nil), f.nodes...)
	content[index] = node
	return Fragment{nodes: content, size: f.size + node.NodeSize() - current.NodeSize()}
}

// findIndex returns the index of the child at a position and the position where that child starts; round > 0
// takes the child after a boundary.
func (f Fragment) findIndex(pos, round int) (int, int) {
	if pos == 0 {
		return 0, 0
	}
	if pos == f.size {
		return len(f.nodes), pos
	}
	if pos > f.size || pos < 0 {
		fail("Position %d outside of fragment", pos)
	}
	cur := 0
	for i := 0; ; i++ {
		child := f.Child(i)
		end := cur + child.NodeSize()
		if end >= pos {
			if end == pos || round > 0 {
				return i + 1, end
			}
			return i, cur
		}
		cur = end
	}
}

// lenUTF16 is the length of a string in UTF-16 code units, as JavaScript counts it.
func lenUTF16(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// sliceUTF16 cuts a string at positions in UTF-16 code units; a cut through a surrogate pair fails.
func sliceUTF16(s string, from, to int) string {
	var b strings.Builder
	pos := 0
	for _, r := range s {
		if pos >= to {
			break
		}
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if pos+w > from {
			if pos < from || pos+w > to {
				fail("Position inside a surrogate pair")
			}
			b.WriteRune(r)
		}
		pos += w
	}
	return b.String()
}

// ParseNode reads a node from its JSON form, filling in default attributes.
func (s *Schema) ParseNode(data []byte) (node *Node, err error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return s.NodeFromJSON(v)
}

// NodeFromJSON builds a node from decoded JSON.
func (s *Schema) NodeFromJSON(v any) (node *Node, err error) {
	defer catch(&err)
	return s.nodeFromJSON(v), nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

func (s *Schema) nodeFromJSON(v any) *Node {
	obj, ok := v.(map[string]any)
	if !ok {
		fail("Invalid input for Node.fromJSON")
	}
	var marks []*Mark
	if raw := obj["marks"]; truthy(raw) {
		list, ok := raw.([]any)
		if !ok {
			fail("Invalid mark data for Node.fromJSON")
		}
		for _, m := range list {
			marks = append(marks, s.markFromJSON(m))
		}
	}
	if obj["type"] == "text" {
		text, ok := obj["text"].(string)
		if !ok {
			fail("Invalid text node in JSON")
		}
		return newText(s.Text, text, markSetFrom(marks))
	}
	content := s.fragmentFromJSON(obj["content"])
	name, _ := obj["type"].(string)
	t := s.nodes[name]
	if t == nil {
		fail("Unknown node type: %v", obj["type"])
	}
	attrs, _ := obj["attrs"].(map[string]any)
	return t.create(attrs, content, marks)
}

// fragmentFromJSON reads a list of nodes; unlike building fragments, it does not join text nodes.
func (s *Schema) fragmentFromJSON(v any) Fragment {
	if !truthy(v) {
		return Fragment{}
	}
	list, ok := v.([]any)
	if !ok {
		fail("Invalid input for Fragment.fromJSON")
	}
	nodes := make([]*Node, len(list))
	for i, x := range list {
		nodes[i] = s.nodeFromJSON(x)
	}
	return fragmentOf(nodes)
}

func (s *Schema) markFromJSON(v any) *Mark {
	obj, ok := v.(map[string]any)
	if !ok {
		fail("Invalid input for Mark.fromJSON")
	}
	name, _ := obj["type"].(string)
	t := s.marks[name]
	if t == nil {
		fail("There is no mark type %v in this schema", obj["type"])
	}
	attrs, _ := obj["attrs"].(map[string]any)
	return &Mark{Type: t, Attrs: computeAttrs(t.Attrs, attrs)}
}

// JSON returns the node in ProseMirror's JSON form.
func (n *Node) JSON() map[string]any {
	out := map[string]any{"type": n.typ.Name}
	if len(n.attrs) > 0 {
		out["attrs"] = n.attrs
	}
	if n.content.size > 0 {
		list := make([]any, len(n.content.nodes))
		for i, c := range n.content.nodes {
			list[i] = c.JSON()
		}
		out["content"] = list
	}
	if len(n.marks) > 0 {
		list := make([]any, len(n.marks))
		for i, m := range n.marks {
			list[i] = m.JSON()
		}
		out["marks"] = list
	}
	if n.typ.Text {
		out["text"] = n.text
	}
	return out
}

// JSON returns the mark in ProseMirror's JSON form.
func (m *Mark) JSON() map[string]any {
	out := map[string]any{"type": m.Type.Name}
	if len(m.Attrs) > 0 {
		out["attrs"] = m.Attrs
	}
	return out
}

// MarshalJSON encodes the node in ProseMirror's JSON form.
func (n *Node) MarshalJSON() ([]byte, error) { return json.Marshal(n.JSON()) }
