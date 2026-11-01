// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package docmodel implements Kogge's rich-text documents: a ProseMirror-compatible JSON
// tree used for issue descriptions, comments, pages and templates. It validates and normalises documents,
// renders them to HTML, plain text and Markdown, and extracts references. It contains no raw HTML, so stored
// documents cannot carry scripts.
package docmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Version is the current document format version.
const Version = 1

// Limits for documents.
const (
	MaxBytes = 2 << 20
	MaxDepth = 64
	// MaxLatex is the most LaTeX a formula holds, in bytes: longer formulas are refused, never cut.
	MaxLatex = 16 << 10
)

// Node is a document node.
type Node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []*Node        `json:"content,omitempty"`
	Marks   []*Mark        `json:"marks,omitempty"`
	Text    string         `json:"text,omitempty"`
}

// Mark is an inline mark on a text node.
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Doc is a document: the root node with its format version.
type Doc struct {
	Type    string  `json:"type"`
	Version int     `json:"version,omitempty"`
	Content []*Node `json:"content,omitempty"`
}

// Root returns the document as a node tree.
func (d *Doc) Root() *Node { return &Node{Type: "doc", Content: d.Content} }

// Empty returns an empty document.
func Empty() *Doc { return &Doc{Type: "doc", Version: Version} }

// Paragraphs returns a document of plain-text paragraphs (one per line; empty lines separate nothing).
func Paragraphs(text string) *Doc {
	d := Empty()
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		p := &Node{Type: "paragraph"}
		if line != "" {
			p.Content = []*Node{{Type: "text", Text: line}}
		}
		d.Content = append(d.Content, p)
	}
	return d
}

type nodeSpec struct {
	attrs  map[string]attrKind
	block  bool
	inline bool
	leaf   bool
}

type attrKind int

const (
	attrString attrKind = iota
	attrAlign           // left, center, right or justify: it becomes a style in the editor
	attrInt
	attrBool
	attrObject
	attrAny
	attrSource // a string kept whole (the LaTeX of formulas): the rules of its node limit its length
)

var nodes = map[string]nodeSpec{
	"paragraph":      {block: true, attrs: map[string]attrKind{"align": attrAlign}},
	"heading":        {block: true, attrs: map[string]attrKind{"level": attrInt, "id": attrString, "align": attrAlign}},
	"blockquote":     {block: true},
	"horizontalRule": {block: true, leaf: true},
	"bulletList":     {block: true},
	"orderedList":    {block: true, attrs: map[string]attrKind{"start": attrInt}},
	"listItem":       {block: true},
	"taskList":       {block: true},
	"taskItem":       {block: true, attrs: map[string]attrKind{"checked": attrBool, "assignee": attrInt, "due": attrString}},
	"codeBlock":      {block: true, attrs: map[string]attrKind{"language": attrString, "title": attrString}},
	"table":          {block: true},
	"tableRow":       {block: true},
	"tableHeader":    {block: true, attrs: map[string]attrKind{"colspan": attrInt, "rowspan": attrInt, "colwidth": attrAny, "background": attrString}},
	"tableCell":      {block: true, attrs: map[string]attrKind{"colspan": attrInt, "rowspan": attrInt, "colwidth": attrAny, "background": attrString}},
	"image":          {block: true, leaf: true, attrs: map[string]attrKind{"attachment": attrInt, "src": attrString, "alt": attrString, "width": attrInt, "align": attrString}},
	"panel":          {block: true, attrs: map[string]attrKind{"kind": attrString}},
	"expand":         {block: true, attrs: map[string]attrKind{"title": attrString}},
	"macro":          {block: true, attrs: map[string]attrKind{"name": attrString, "params": attrObject}},
	"unsupported":    {block: true, leaf: true, attrs: map[string]attrKind{"source": attrString, "name": attrString, "raw": attrString}},
	"text":           {inline: true, leaf: true},
	"hardBreak":      {inline: true, leaf: true},
	"mention":        {inline: true, leaf: true, attrs: map[string]attrKind{"user": attrInt, "label": attrString}},
	"issue":          {inline: true, leaf: true, attrs: map[string]attrKind{"key": attrString, "display": attrString}},
	"status":         {inline: true, leaf: true, attrs: map[string]attrKind{"text": attrString, "color": attrString}},
	"date":           {inline: true, leaf: true, attrs: map[string]attrKind{"value": attrString}},
	"emoji":          {inline: true, leaf: true, attrs: map[string]attrKind{"name": attrString}},
	"inlineMacro":    {inline: true, leaf: true, attrs: map[string]attrKind{"name": attrString, "params": attrObject}},
	// inlineImage is an image within text, as Markdown has them (badges, icons); href is the link around it.
	"inlineImage": {inline: true, leaf: true, attrs: map[string]attrKind{"src": attrString, "alt": attrString, "title": attrString, "href": attrString}},
	// columns lay out two or three columns of blocks side by side, at the top level of a document only; the layout
	// names their widths (see ColumnWidths).
	"columns": {block: true, attrs: map[string]attrKind{"layout": attrString}},
	"column":  {block: true},
	// math is a formula on a line of its own, inlineMath one within text. Both hold their LaTeX, which the web
	// application typesets; the server keeps it and writes it out as it is.
	"math":       {block: true, leaf: true, attrs: map[string]attrKind{"latex": attrSource}},
	"inlineMath": {inline: true, leaf: true, attrs: map[string]attrKind{"latex": attrSource}},
}

var marks = map[string]map[string]attrKind{
	"bold":        nil,
	"italic":      nil,
	"underline":   nil,
	"strike":      nil,
	"code":        nil,
	"subscript":   nil,
	"superscript": nil,
	"textColor":   {"color": attrString},
	"highlight":   {"color": attrString},
	"link":        {"href": attrString, "title": attrString},
	"anchor":      {"id": attrString},
}

// Palette is the set of colours documents may use (text colour, highlight, status, table backgrounds).
var Palette = map[string]bool{"": true, "gray": true, "red": true, "orange": true, "yellow": true, "green": true,
	"teal": true, "blue": true, "violet": true, "pink": true}

// Options control validation.
type Options struct {
	// AllowExternalImages permits image nodes with absolute http(s) sources.
	AllowExternalImages bool
}

// ValidationError describes why a document was rejected.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return "document " + e.Path + ": " + e.Message }

// Parse decodes, validates and normalises a document. Unknown attributes are dropped; unknown node or mark types
// and unsafe links are rejected.
func Parse(raw []byte, opts Options) (*Doc, error) {
	if len(raw) > MaxBytes {
		return nil, &ValidationError{Path: "/", Message: fmt.Sprintf("larger than %d bytes", MaxBytes)}
	}
	var d Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, &ValidationError{Path: "/", Message: "not a JSON document: " + err.Error()}
	}
	if err := Validate(&d, opts); err != nil {
		return nil, err
	}
	return &d, nil
}

// Validate checks and normalises a document in place.
func Validate(d *Doc, opts Options) error {
	if d == nil {
		return &ValidationError{Path: "/", Message: "missing"}
	}
	if d.Type != "doc" {
		return &ValidationError{Path: "/type", Message: "the root must be of type doc"}
	}
	if d.Version == 0 {
		d.Version = Version
	}
	if d.Version > Version {
		return &ValidationError{Path: "/version", Message: fmt.Sprintf("version %d is newer than supported", d.Version)}
	}
	for i, n := range d.Content {
		if err := validateNode(n, fmt.Sprintf("/content/%d", i), 1, opts, true); err != nil {
			return err
		}
	}
	return checkColumns(d)
}

// Column layouts by their number of columns: the widths of the columns, relative to each other.
var columnLayouts = map[string]map[int][]float64{
	"equal": {2: {1, 1}, 3: {1, 1, 1}},
	"left":  {2: {1, 2}},    // a narrow column on the left, as a sidebar
	"right": {2: {2, 1}},    // a narrow column on the right
	"sides": {3: {1, 2, 1}}, // a wide column between two narrow ones
}

// ColumnWidths returns the relative widths of the columns of a columns node; layouts that do not fit the number
// of columns share the width equally.
func ColumnWidths(n *Node) []float64 {
	if w, ok := columnLayouts[strAttr(n.Attrs, "layout")][len(n.Content)]; ok {
		return w
	}
	w := make([]float64, len(n.Content))
	for i := range w {
		w[i] = 1
	}
	return w
}

// checkColumns enforces where layouts stand: columns only at the top level of a document, with two or three
// columns of blocks each, and nothing of a layout anywhere else. ValidateNode leaves this to the editor's schema,
// which keeps the same rules for every change of a shared draft.
func checkColumns(d *Doc) error {
	var inside func(nodes []*Node, path string) error
	inside = func(nodes []*Node, path string) error {
		for i, n := range nodes {
			p := fmt.Sprintf("%s/content/%d", path, i)
			if n.Type == "columns" || n.Type == "column" {
				return &ValidationError{Path: p, Message: "columns may only stand at the top level of a document"}
			}
			if err := inside(n.Content, p); err != nil {
				return err
			}
		}
		return nil
	}
	for i, n := range d.Content {
		path := fmt.Sprintf("/content/%d", i)
		switch n.Type {
		case "column":
			return &ValidationError{Path: path, Message: "a column must be part of columns"}
		case "columns":
			if len(n.Content) < 2 || len(n.Content) > 3 {
				return &ValidationError{Path: path, Message: "columns need two or three columns"}
			}
			if _, ok := columnLayouts[strAttr(n.Attrs, "layout")][len(n.Content)]; !ok {
				n.Attrs = map[string]any{"layout": "equal"}
			}
			for j, c := range n.Content {
				cp := fmt.Sprintf("%s/content/%d", path, j)
				if c.Type != "column" {
					return &ValidationError{Path: cp, Message: "columns may only hold columns"}
				}
				if len(c.Content) == 0 {
					return &ValidationError{Path: cp, Message: "a column needs content"}
				}
				if err := inside(c.Content, cp); err != nil {
					return err
				}
			}
		default:
			if err := inside(n.Content, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateNode checks and normalises a node found at a depth of a document, as Validate does for the nodes of
// documents (but without the rules of where inline nodes may be: the editor's schema keeps those).
func ValidateNode(n *Node, depth int, opts Options) error {
	return validateNode(n, "", depth, opts, false)
}

// ValidateMark checks a mark as Validate does for the marks of text.
func ValidateMark(m *Mark, opts Options) error {
	return validateNode(&Node{Type: "text", Text: "-", Marks: []*Mark{m}}, "", 1, opts, false)
}

func validateNode(n *Node, path string, depth int, opts Options, blockContext bool) error {
	if n == nil {
		return &ValidationError{Path: path, Message: "null node"}
	}
	if depth > MaxDepth {
		return &ValidationError{Path: path, Message: "nested too deeply"}
	}
	spec, ok := nodes[n.Type]
	if !ok {
		return &ValidationError{Path: path + "/type", Message: fmt.Sprintf("unknown node type %q", n.Type)}
	}
	if blockContext && spec.inline && n.Type != "text" {
		// Inline nodes directly in block containers are tolerated only inside text blocks; wrap is the
		// editor's job, so reject here to keep documents well-formed.
		return &ValidationError{Path: path, Message: fmt.Sprintf("inline node %q outside of a text block", n.Type)}
	}
	if n.Type == "math" || n.Type == "inlineMath" {
		// The LaTeX is refused rather than dropped or cut where it does not fit: changes of shared drafts are checked
		// on copies, and the draft and the page published from it must hold the same formula. An empty formula is
		// fine: the editor inserts one and fills it in afterwards.
		if v, ok := n.Attrs["latex"]; ok {
			latex, isString := v.(string)
			if !isString {
				return &ValidationError{Path: path + "/attrs/latex", Message: "the LaTeX of a formula must be a string"}
			}
			if len(latex) > MaxLatex {
				return &ValidationError{Path: path + "/attrs/latex", Message: fmt.Sprintf("the LaTeX of a formula is longer than %d bytes", MaxLatex)}
			}
		}
	}
	n.Attrs = cleanAttrs(n.Attrs, spec.attrs)
	if spec.leaf && len(n.Content) > 0 {
		return &ValidationError{Path: path, Message: fmt.Sprintf("%s must not have content", n.Type)}
	}
	if n.Type != "text" {
		n.Text = ""
	} else if n.Text == "" {
		return &ValidationError{Path: path, Message: "empty text node"}
	}
	if n.Type != "text" && len(n.Marks) > 0 {
		n.Marks = nil
	}
	for i, m := range n.Marks {
		attrs, ok := marks[m.Type]
		if !ok {
			return &ValidationError{Path: fmt.Sprintf("%s/marks/%d", path, i), Message: fmt.Sprintf("unknown mark %q", m.Type)}
		}
		m.Attrs = cleanAttrs(m.Attrs, attrs)
		if m.Type == "link" {
			href, _ := m.Attrs["href"].(string)
			if !SafeLink(href) {
				return &ValidationError{Path: fmt.Sprintf("%s/marks/%d", path, i), Message: "links must use http, https, mailto or a relative URL"}
			}
		}
		if m.Type == "textColor" || m.Type == "highlight" {
			if c, _ := m.Attrs["color"].(string); !Palette[c] {
				m.Attrs["color"] = ""
			}
		}
	}
	switch n.Type {
	case "heading":
		if l, _ := n.Attrs["level"].(int64); l < 1 || l > 6 {
			setAttr(n, "level", int64(2))
		}
	case "panel":
		switch k, _ := n.Attrs["kind"].(string); k {
		case "info", "note", "warning", "success", "error":
		default:
			setAttr(n, "kind", "info")
		}
	case "columns":
		if _, ok := columnLayouts[strAttr(n.Attrs, "layout")]; !ok {
			n.Attrs = map[string]any{"layout": "equal"}
		}
	case "image":
		_, hasAttachment := n.Attrs["attachment"]
		src, _ := n.Attrs["src"].(string)
		if !hasAttachment && src == "" {
			return &ValidationError{Path: path, Message: "image needs an attachment or a source"}
		}
		if src != "" && !safeImage(src, opts.AllowExternalImages) {
			return &ValidationError{Path: path, Message: "images must be attachments or same-origin"}
		}
	case "inlineImage":
		src, _ := n.Attrs["src"].(string)
		if src == "" || !safeImage(src, opts.AllowExternalImages) {
			return &ValidationError{Path: path, Message: "images must be same-origin"}
		}
		if href, _ := n.Attrs["href"].(string); href != "" && !SafeLink(href) {
			return &ValidationError{Path: path, Message: "links must use http, https, mailto or a relative URL"}
		}
	case "issue":
		if k, _ := n.Attrs["key"].(string); !IsIssueKey(k) {
			return &ValidationError{Path: path, Message: "issue node needs a valid key"}
		}
	case "status":
		if c, _ := n.Attrs["color"].(string); !Palette[c] {
			setAttr(n, "color", "gray")
		}
	case "macro", "inlineMacro":
		if name, _ := n.Attrs["name"].(string); name == "" {
			return &ValidationError{Path: path, Message: "macro needs a name"}
		}
	}
	childBlock := !isTextBlock(n.Type)
	for i, c := range n.Content {
		if err := validateNode(c, fmt.Sprintf("%s/content/%d", path, i), depth+1, opts, childBlock); err != nil {
			return err
		}
	}
	return nil
}

func isTextBlock(t string) bool {
	return t == "paragraph" || t == "heading" || t == "codeBlock"
}

// setAttr sets an attribute of a node; nodes without attributes have none to set yet.
func setAttr(n *Node, k string, v any) {
	if n.Attrs == nil {
		n.Attrs = map[string]any{}
	}
	n.Attrs[k] = v
}

func cleanAttrs(in map[string]any, allowed map[string]attrKind) map[string]any {
	if len(in) == 0 || len(allowed) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		kind, ok := allowed[k]
		if !ok || v == nil {
			continue
		}
		switch kind {
		case attrAlign:
			if s, ok := v.(string); ok && (s == "left" || s == "center" || s == "right" || s == "justify") {
				out[k] = s
			}
		case attrString:
			if s, ok := v.(string); ok {
				if len(s) > 2000 {
					s = s[:2000]
				}
				out[k] = s
			}
		case attrInt:
			switch x := v.(type) {
			case float64:
				out[k] = int64(x)
			case int64:
				out[k] = x
			case int:
				out[k] = int64(x)
			}
		case attrBool:
			if b, ok := v.(bool); ok {
				out[k] = b
			}
		case attrObject:
			if m, ok := v.(map[string]any); ok {
				out[k] = m
			}
		case attrAny:
			out[k] = v
		case attrSource:
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SafeLink reports whether a link target is allowed in documents: http(s), mailto, local paths and anchors.
// Browsers drop tabs and newlines from addresses and read backslashes as slashes, so "/\host" or "/<TAB>/host" lead
// to other hosts; such targets are refused.
func SafeLink(href string) bool {
	href = strings.TrimSpace(href)
	if href == "" || strings.ContainsRune(href, '\\') || strings.ContainsFunc(href, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	if strings.HasPrefix(href, "#") {
		return true
	}
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	if strings.HasPrefix(href, "/") {
		return u.Scheme == "" && u.Host == "" && !strings.HasPrefix(href, "//")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return true
	}
	return false
}

func safeImage(src string, external bool) bool {
	if strings.ContainsRune(src, '\\') || strings.ContainsFunc(src, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	if strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//") {
		return true
	}
	if !external {
		return false
	}
	u, err := url.Parse(src)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Marshal encodes a document.
func Marshal(d *Doc) ([]byte, error) {
	if d == nil {
		return nil, errors.New("nil document")
	}
	return json.Marshal(d)
}

// Walk calls fn for every node in document order. Returning false skips the node's children.
func Walk(d *Doc, fn func(n *Node) bool) {
	var walk func(n *Node)
	walk = func(n *Node) {
		if !fn(n) {
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for _, n := range d.Content {
		walk(n)
	}
}
