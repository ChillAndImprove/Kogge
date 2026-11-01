// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-FileCopyrightText: 2015-2017 Marijn Haverbeke and others
// SPDX-License-Identifier: EUPL-1.2 AND MIT

package prosemirror

import (
	"encoding/json"
	"math"
)

// Step is an atomic change of a document, as the editor's transforms make them.
type Step interface {
	apply(doc *Node) *Node
	// JSON returns the step in ProseMirror's JSON form.
	JSON() map[string]any
}

// Apply applies a step to a document.
func Apply(doc *Node, s Step) (out *Node, err error) {
	defer catch(&err)
	return s.apply(doc), nil
}

// ParseStep reads a step from its JSON form.
func (s *Schema) ParseStep(data []byte) (Step, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return s.StepFromJSON(v)
}

// StepFromJSON builds a step from decoded JSON.
func (s *Schema) StepFromJSON(v any) (step Step, err error) {
	defer catch(&err)
	obj, ok := v.(map[string]any)
	if !ok {
		fail("Invalid input for Step.fromJSON")
	}
	switch obj["stepType"] {
	case "replace":
		return &ReplaceStep{From: intField(obj, "from"), To: intField(obj, "to"), Slice: s.sliceFromJSON(obj["slice"]),
			Structure: truthy(obj["structure"])}, nil
	case "replaceAround":
		return &ReplaceAroundStep{From: intField(obj, "from"), To: intField(obj, "to"), GapFrom: intField(obj, "gapFrom"),
			GapTo: intField(obj, "gapTo"), Insert: intField(obj, "insert"), Slice: s.sliceFromJSON(obj["slice"]),
			Structure: truthy(obj["structure"])}, nil
	case "addMark":
		return &AddMarkStep{From: intField(obj, "from"), To: intField(obj, "to"), Mark: s.markFromJSON(obj["mark"])}, nil
	case "removeMark":
		return &RemoveMarkStep{From: intField(obj, "from"), To: intField(obj, "to"), Mark: s.markFromJSON(obj["mark"])}, nil
	case "addNodeMark":
		return &AddNodeMarkStep{Pos: intField(obj, "pos"), Mark: s.markFromJSON(obj["mark"])}, nil
	case "removeNodeMark":
		return &RemoveNodeMarkStep{Pos: intField(obj, "pos"), Mark: s.markFromJSON(obj["mark"])}, nil
	case "attr":
		name, ok := obj["attr"].(string)
		if !ok {
			fail("Invalid input for AttrStep.fromJSON")
		}
		return &AttrStep{Pos: intField(obj, "pos"), Attr: name, Value: obj["value"]}, nil
	case "docAttr":
		name, ok := obj["attr"].(string)
		if !ok {
			fail("Invalid input for DocAttrStep.fromJSON")
		}
		return &DocAttrStep{Attr: name, Value: obj["value"]}, nil
	default:
		fail("No step type %v defined", obj["stepType"])
		return nil, nil
	}
}

// intField reads a position; the editor's positions are integers.
func intField(obj map[string]any, name string) int {
	f, ok := obj[name].(float64)
	if !ok || f != math.Trunc(f) || f < 0 || f > math.MaxInt32 {
		fail("Invalid input for step: %s", name)
	}
	return int(f)
}

func (s *Schema) sliceFromJSON(v any) Slice {
	if !truthy(v) {
		return Slice{}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		fail("Invalid input for Slice.fromJSON")
	}
	open := func(name string) int {
		raw, present := obj[name]
		if !present || !truthy(raw) {
			return 0
		}
		f, ok := raw.(float64)
		if !ok || f != math.Trunc(f) || f < 0 || f > 1000 {
			fail("Invalid input for Slice.fromJSON")
		}
		return int(f)
	}
	return Slice{Content: s.fragmentFromJSON(obj["content"]), OpenStart: open("openStart"), OpenEnd: open("openEnd")}
}

func checkRange(doc *Node, from, to int) {
	if from > to || to > doc.content.size {
		fail("Range %d-%d outside of the document", from, to)
	}
}

// ReplaceStep replaces the content between two positions by a slice.
type ReplaceStep struct {
	From, To  int
	Slice     Slice
	Structure bool // may only change structure (no content between the positions)
}

func (st *ReplaceStep) apply(doc *Node) *Node {
	checkRange(doc, st.From, st.To)
	if st.Structure && contentBetween(doc, st.From, st.To) {
		fail("Structure replace would overwrite content")
	}
	return doc.replace(st.From, st.To, st.Slice)
}

// ReplaceAroundStep replaces a range but keeps a gap within it, which is inserted into the slice at Insert.
type ReplaceAroundStep struct {
	From, To, GapFrom, GapTo, Insert int
	Slice                            Slice
	Structure                        bool
}

func (st *ReplaceAroundStep) apply(doc *Node) *Node {
	checkRange(doc, st.From, st.To)
	if st.GapFrom < st.From || st.GapTo > st.To || st.GapFrom > st.GapTo {
		fail("Gap outside of the range")
	}
	if st.Structure && (contentBetween(doc, st.From, st.GapFrom) || contentBetween(doc, st.GapTo, st.To)) {
		fail("Structure gap-replace would overwrite content")
	}
	gap := doc.slice(st.GapFrom, st.GapTo)
	if gap.OpenStart != 0 || gap.OpenEnd != 0 {
		fail("Gap is not a flat range")
	}
	inserted, ok := st.Slice.insertAt(st.Insert, gap.Content)
	if !ok {
		fail("Content does not fit in gap")
	}
	return doc.replace(st.From, st.To, inserted)
}

// contentBetween reports whether there is content between two positions (as opposed to only node boundaries).
func contentBetween(doc *Node, from, to int) bool {
	rFrom := doc.resolve(from)
	dist, depth := to-from, rFrom.Depth()
	for dist > 0 && depth > 0 && rFrom.indexAfter(depth) == rFrom.node(depth).ChildCount() {
		depth--
		dist--
	}
	if dist > 0 {
		next := rFrom.node(depth).content.maybeChild(rFrom.indexAfter(depth))
		for dist > 0 {
			if next == nil || next.typ.Leaf {
				return true
			}
			next = next.content.firstChild()
			dist--
		}
	}
	return false
}

// mapFragment rebuilds a fragment with a function applied to its inline nodes.
func mapFragment(f Fragment, fn func(node, parent *Node) *Node, parent *Node) Fragment {
	mapped := make([]*Node, 0, len(f.nodes))
	for _, child := range f.nodes {
		if child.content.size > 0 {
			child = child.copy(mapFragment(child.content, fn, child))
		}
		if child.typ.Inline {
			child = fn(child, parent)
		}
		mapped = append(mapped, child)
	}
	return fragmentFromArray(mapped)
}

// AddMarkStep adds a mark to the inline content between two positions.
type AddMarkStep struct {
	From, To int
	Mark     *Mark
}

func (st *AddMarkStep) apply(doc *Node) *Node {
	checkRange(doc, st.From, st.To)
	old := doc.slice(st.From, st.To)
	rFrom := doc.resolve(st.From)
	parent := rFrom.node(rFrom.sharedDepth(st.To))
	content := mapFragment(old.Content, func(node, parent *Node) *Node {
		if !node.typ.Atom || !parent.typ.AllowsMarkType(st.Mark.Type) {
			return node
		}
		return node.mark(st.Mark.AddToSet(node.marks))
	}, parent)
	return doc.replace(st.From, st.To, Slice{Content: content, OpenStart: old.OpenStart, OpenEnd: old.OpenEnd})
}

// RemoveMarkStep removes a mark from the inline content between two positions.
type RemoveMarkStep struct {
	From, To int
	Mark     *Mark
}

func (st *RemoveMarkStep) apply(doc *Node) *Node {
	checkRange(doc, st.From, st.To)
	old := doc.slice(st.From, st.To)
	content := mapFragment(old.Content, func(node, _ *Node) *Node {
		return node.mark(st.Mark.RemoveFromSet(node.marks))
	}, doc)
	return doc.replace(st.From, st.To, Slice{Content: content, OpenStart: old.OpenStart, OpenEnd: old.OpenEnd})
}

// replaceMarkup replaces the node at a position by a copy with other attributes or marks, keeping its content.
func replaceMarkup(doc *Node, pos int, node, updated *Node) *Node {
	open := 1
	if node.typ.Leaf {
		open = 0
	}
	return doc.replace(pos, pos+1, Slice{Content: fragmentFrom(updated), OpenStart: 0, OpenEnd: open})
}

// AddNodeMarkStep adds a mark to the node at a position.
type AddNodeMarkStep struct {
	Pos  int
	Mark *Mark
}

func (st *AddNodeMarkStep) apply(doc *Node) *Node {
	node := doc.nodeAt(st.Pos)
	if node == nil {
		fail("No node at mark step's position")
	}
	return replaceMarkup(doc, st.Pos, node, node.typ.create(node.attrs, Fragment{}, st.Mark.AddToSet(node.marks)))
}

// RemoveNodeMarkStep removes a mark from the node at a position.
type RemoveNodeMarkStep struct {
	Pos  int
	Mark *Mark
}

func (st *RemoveNodeMarkStep) apply(doc *Node) *Node {
	node := doc.nodeAt(st.Pos)
	if node == nil {
		fail("No node at mark step's position")
	}
	return replaceMarkup(doc, st.Pos, node, node.typ.create(node.attrs, Fragment{}, st.Mark.RemoveFromSet(node.marks)))
}

// AttrStep sets an attribute of the node at a position.
type AttrStep struct {
	Pos   int
	Attr  string
	Value any
}

func (st *AttrStep) apply(doc *Node) *Node {
	node := doc.nodeAt(st.Pos)
	if node == nil {
		fail("No node at attribute step's position")
	}
	if node.typ.Text {
		fail("NodeType.create can't construct text nodes")
	}
	attrs := make(map[string]any, len(node.attrs)+1)
	for k, v := range node.attrs {
		attrs[k] = v
	}
	attrs[st.Attr] = st.Value
	return replaceMarkup(doc, st.Pos, node, node.typ.create(attrs, Fragment{}, node.marks))
}

// DocAttrStep sets an attribute of the document.
type DocAttrStep struct {
	Attr  string
	Value any
}

func (st *DocAttrStep) apply(doc *Node) *Node {
	attrs := make(map[string]any, len(doc.attrs)+1)
	for k, v := range doc.attrs {
		attrs[k] = v
	}
	attrs[st.Attr] = st.Value
	return doc.typ.create(attrs, doc.content, doc.marks)
}

func sliceJSON(s Slice) map[string]any {
	content := make([]any, len(s.Content.nodes))
	for i, n := range s.Content.nodes {
		content[i] = n.JSON()
	}
	out := map[string]any{"content": content}
	if s.OpenStart > 0 {
		out["openStart"] = s.OpenStart
	}
	if s.OpenEnd > 0 {
		out["openEnd"] = s.OpenEnd
	}
	return out
}

// JSON returns the step in ProseMirror's JSON form.
func (st *ReplaceStep) JSON() map[string]any {
	out := map[string]any{"stepType": "replace", "from": st.From, "to": st.To}
	if st.Slice.Size() > 0 {
		out["slice"] = sliceJSON(st.Slice)
	}
	if st.Structure {
		out["structure"] = true
	}
	return out
}

// JSON returns the step in ProseMirror's JSON form.
func (st *ReplaceAroundStep) JSON() map[string]any {
	out := map[string]any{"stepType": "replaceAround", "from": st.From, "to": st.To, "gapFrom": st.GapFrom,
		"gapTo": st.GapTo, "insert": st.Insert}
	if st.Slice.Size() > 0 {
		out["slice"] = sliceJSON(st.Slice)
	}
	if st.Structure {
		out["structure"] = true
	}
	return out
}

// JSON returns the step in ProseMirror's JSON form.
func (st *AddMarkStep) JSON() map[string]any {
	return map[string]any{"stepType": "addMark", "mark": st.Mark.JSON(), "from": st.From, "to": st.To}
}

// JSON returns the step in ProseMirror's JSON form.
func (st *RemoveMarkStep) JSON() map[string]any {
	return map[string]any{"stepType": "removeMark", "mark": st.Mark.JSON(), "from": st.From, "to": st.To}
}

// JSON returns the step in ProseMirror's JSON form.
func (st *AddNodeMarkStep) JSON() map[string]any {
	return map[string]any{"stepType": "addNodeMark", "pos": st.Pos, "mark": st.Mark.JSON()}
}

// JSON returns the step in ProseMirror's JSON form.
func (st *RemoveNodeMarkStep) JSON() map[string]any {
	return map[string]any{"stepType": "removeNodeMark", "pos": st.Pos, "mark": st.Mark.JSON()}
}

// JSON returns the step in ProseMirror's JSON form.
func (st *AttrStep) JSON() map[string]any {
	return map[string]any{"stepType": "attr", "pos": st.Pos, "attr": st.Attr, "value": st.Value}
}

// JSON returns the step in ProseMirror's JSON form.
func (st *DocAttrStep) JSON() map[string]any {
	return map[string]any{"stepType": "docAttr", "attr": st.Attr, "value": st.Value}
}
