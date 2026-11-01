// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-FileCopyrightText: 2015-2017 Marijn Haverbeke and others
// SPDX-License-Identifier: EUPL-1.2 AND MIT

package prosemirror

// ResolvedPos is a position with the path of nodes that lead to it.
type ResolvedPos struct {
	Pos          int
	nodes        []*Node
	indexes      []int
	offsets      []int // per depth: the position where the child at the index starts
	ParentOffset int
}

func (n *Node) resolve(pos int) *ResolvedPos {
	if pos < 0 || pos > n.content.size {
		fail("Position %d out of range", pos)
	}
	r := &ResolvedPos{Pos: pos}
	start, parentOffset := 0, pos
	for node := n; ; {
		index, offset := node.content.findIndex(parentOffset, -1)
		rem := parentOffset - offset
		r.nodes = append(r.nodes, node)
		r.indexes = append(r.indexes, index)
		r.offsets = append(r.offsets, start+offset)
		if rem == 0 {
			break
		}
		node = node.Child(index)
		if node.typ.Text {
			break
		}
		parentOffset = rem - 1
		start += offset + 1
	}
	r.ParentOffset = parentOffset
	return r
}

// Depth is the number of levels between the position and the document.
func (r *ResolvedPos) Depth() int { return len(r.nodes) - 1 }

func (r *ResolvedPos) node(depth int) *Node { return r.nodes[depth] }

func (r *ResolvedPos) parent() *Node { return r.nodes[len(r.nodes)-1] }

func (r *ResolvedPos) index(depth int) int { return r.indexes[depth] }

func (r *ResolvedPos) indexAfter(depth int) int {
	if depth == r.Depth() && r.textOffset() == 0 {
		return r.indexes[depth]
	}
	return r.indexes[depth] + 1
}

func (r *ResolvedPos) start(depth int) int {
	if depth == 0 {
		return 0
	}
	return r.offsets[depth-1] + 1
}

func (r *ResolvedPos) end(depth int) int { return r.start(depth) + r.nodes[depth].content.size }

func (r *ResolvedPos) textOffset() int { return r.Pos - r.offsets[len(r.offsets)-1] }

// nodeAfter is the node after the position (the rest of a text node when it is inside one).
func (r *ResolvedPos) nodeAfter() *Node {
	parent, index := r.parent(), r.indexes[r.Depth()]
	if index == parent.ChildCount() {
		return nil
	}
	child := parent.Child(index)
	if off := r.textOffset(); off > 0 {
		return child.cut(off, child.NodeSize())
	}
	return child
}

// nodeBefore is the node before the position (the start of a text node when it is inside one).
func (r *ResolvedPos) nodeBefore() *Node {
	index := r.indexes[r.Depth()]
	if off := r.textOffset(); off > 0 {
		return r.parent().Child(index).cut(0, off)
	}
	if index == 0 {
		return nil
	}
	return r.parent().Child(index - 1)
}

// sharedDepth is the depth up to which the position and another one share their ancestors.
func (r *ResolvedPos) sharedDepth(pos int) int {
	for depth := r.Depth(); depth > 0; depth-- {
		if r.start(depth) <= pos && r.end(depth) >= pos {
			return depth
		}
	}
	return 0
}

// Slice is a piece of a document: a fragment that may be open (cut through nodes) at its start and end.
type Slice struct {
	Content            Fragment
	OpenStart, OpenEnd int
}

// Size is the number of positions the slice adds.
func (s Slice) Size() int { return s.Content.size - s.OpenStart - s.OpenEnd }

// insertAt inserts a fragment at a position within the slice; ok is false if it does not fit there.
func (s Slice) insertAt(pos int, f Fragment) (Slice, bool) {
	content, ok := insertInto(s.Content, pos+s.OpenStart, f, s.OpenStart+1, s.OpenEnd+1, nil)
	if !ok {
		return Slice{}, false
	}
	return Slice{Content: content, OpenStart: s.OpenStart, OpenEnd: s.OpenEnd}, true
}

// insertInto inserts a fragment at a position; where the slice is open (cut through nodes), the content is not
// checked, since the replace joins it with the document's.
func insertInto(content Fragment, dist int, insert Fragment, openStart, openEnd int, parent *Node) (Fragment, bool) {
	index, offset := content.findIndex(dist, -1)
	child := content.maybeChild(index)
	if offset == dist || child.typ.Text {
		if parent != nil && openStart <= 0 && openEnd <= 0 && !parent.canReplace(index, index, insert, 0, insert.ChildCount()) {
			return Fragment{}, false
		}
		return content.cut(0, dist).append(insert).append(content.cut(dist, content.size)), true
	}
	innerStart, innerEnd := 0, 0
	if index == 0 {
		innerStart = openStart - 1
	}
	if index == content.ChildCount()-1 {
		innerEnd = openEnd - 1
	}
	inner, ok := insertInto(child.content, dist-offset-1, insert, innerStart, innerEnd, child)
	if !ok {
		return Fragment{}, false
	}
	return content.replaceChild(index, child.copy(inner)), true
}

// replace replaces the content between two resolved positions by a slice.
func replace(from, to *ResolvedPos, s Slice) *Node {
	if s.OpenStart > from.Depth() {
		fail("Inserted content deeper than insertion position")
	}
	if from.Depth()-s.OpenStart != to.Depth()-s.OpenEnd {
		fail("Inconsistent open depths")
	}
	return replaceOuter(from, to, s, 0)
}

func replaceOuter(from, to *ResolvedPos, s Slice, depth int) *Node {
	index, node := from.index(depth), from.node(depth)
	switch {
	case index == to.index(depth) && depth < from.Depth()-s.OpenStart:
		inner := replaceOuter(from, to, s, depth+1)
		return node.copy(node.content.replaceChild(index, inner))
	case s.Content.size == 0:
		return closeNode(node, replaceTwoWay(from, to, depth))
	case s.OpenStart == 0 && s.OpenEnd == 0 && from.Depth() == depth && to.Depth() == depth:
		parent := from.parent()
		content := parent.content
		return closeNode(parent, content.cut(0, from.ParentOffset).append(s.Content).append(content.cut(to.ParentOffset, content.size)))
	default:
		start, end := prepareSliceForReplace(s, from)
		return closeNode(node, replaceThreeWay(from, start, end, to, depth))
	}
}

func checkJoin(main, sub *Node) {
	if !sub.typ.compatibleContent(main.typ) {
		fail("Cannot join %s onto %s", sub.typ.Name, main.typ.Name)
	}
}

func joinable(before, after *ResolvedPos, depth int) *Node {
	node := before.node(depth)
	checkJoin(node, after.node(depth))
	return node
}

func addNode(child *Node, target []*Node) []*Node {
	last := len(target) - 1
	if last >= 0 && child.typ.Text && child.sameMarkup(target[last]) {
		target[last] = child.withText(target[last].text + child.text)
		return target
	}
	return append(target, child)
}

func addRange(start, end *ResolvedPos, depth int, target []*Node) []*Node {
	var node *Node
	if end != nil {
		node = end.node(depth)
	} else {
		node = start.node(depth)
	}
	startIndex, endIndex := 0, node.ChildCount()
	if end != nil {
		endIndex = end.index(depth)
	}
	if start != nil {
		startIndex = start.index(depth)
		if start.Depth() > depth {
			startIndex++
		} else if start.textOffset() > 0 {
			target = addNode(start.nodeAfter(), target)
			startIndex++
		}
	}
	for i := startIndex; i < endIndex; i++ {
		target = addNode(node.Child(i), target)
	}
	if end != nil && end.Depth() == depth && end.textOffset() > 0 {
		target = addNode(end.nodeBefore(), target)
	}
	return target
}

func closeNode(node *Node, content Fragment) *Node {
	node.typ.checkContent(content)
	return node.copy(content)
}

func replaceThreeWay(from, start, end, to *ResolvedPos, depth int) Fragment {
	var openStart, openEnd *Node
	if from.Depth() > depth {
		openStart = joinable(from, start, depth+1)
	}
	if to.Depth() > depth {
		openEnd = joinable(end, to, depth+1)
	}
	var content []*Node
	content = addRange(nil, from, depth, content)
	if openStart != nil && openEnd != nil && start.index(depth) == end.index(depth) {
		checkJoin(openStart, openEnd)
		content = addNode(closeNode(openStart, replaceThreeWay(from, start, end, to, depth+1)), content)
	} else {
		if openStart != nil {
			content = addNode(closeNode(openStart, replaceTwoWay(from, start, depth+1)), content)
		}
		content = addRange(start, end, depth, content)
		if openEnd != nil {
			content = addNode(closeNode(openEnd, replaceTwoWay(end, to, depth+1)), content)
		}
	}
	content = addRange(to, nil, depth, content)
	return fragmentOf(content)
}

func replaceTwoWay(from, to *ResolvedPos, depth int) Fragment {
	var content []*Node
	content = addRange(nil, from, depth, content)
	if from.Depth() > depth {
		t := joinable(from, to, depth+1)
		content = addNode(closeNode(t, replaceTwoWay(from, to, depth+1)), content)
	}
	content = addRange(to, nil, depth, content)
	return fragmentOf(content)
}

func prepareSliceForReplace(s Slice, along *ResolvedPos) (*ResolvedPos, *ResolvedPos) {
	extra := along.Depth() - s.OpenStart
	parent := along.node(extra)
	node := parent.copy(s.Content)
	for i := extra - 1; i >= 0; i-- {
		node = along.node(i).copy(fragmentFrom(node))
	}
	return node.resolve(s.OpenStart + extra), node.resolve(node.content.size - s.OpenEnd - extra)
}
