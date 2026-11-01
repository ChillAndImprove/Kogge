// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import "testing"

func TestReplaceText(t *testing.T) {
	d := &Doc{Type: "doc", Content: []*Node{
		{Type: "paragraph", Content: []*Node{{Type: "text", Text: "Fix the login. Fix the "}, {Type: "text", Text: "login", Marks: []*Mark{{Type: "bold"}}},
			{Type: "text", Text: " page."}}},
	}}
	issue := &Node{Type: "issue", Attrs: map[string]any{"key": "SHOP-7"}}
	if !ReplaceText(d, "Fix the login", 1, issue) {
		t.Fatal("not found")
	}
	got := d.Content[0].Content
	if len(got) != 3 || got[0].Text != "Fix the login. " || got[1].Type != "issue" || got[2].Text != " page." {
		for _, n := range got {
			t.Logf("%s %q", n.Type, n.Text)
		}
		t.Fatal("wrong result")
	}
	if ReplaceText(d, "missing", 0, issue) {
		t.Error("replaced missing text")
	}
}

// TestAnchorsAroundFormulas: a formula within text counts as one character (U+FFFC), as other inline nodes do; text
// after it is found, and an anchor across it keeps the formula as it is, without a mark.
func TestAnchorsAroundFormulas(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Energy "},
		{"type":"inlineMath","attrs":{"latex":"E=mc^2"}},{"type":"text","text":" holds, it holds."}]}]}`)
	if !AddAnchor(d, "c1", "holds", 1) || !AddAnchor(d, "c2", "Energy \uFFFC holds", 0) {
		t.Fatal("not found")
	}
	// The anchored text is that of the text nodes.
	if got := Anchors(d); got["c1"] != "holds" || got["c2"] != "Energy  holds" {
		t.Errorf("anchors: %#v", got)
	}
	f := d.Content[0].Content[1]
	if f.Type != "inlineMath" || f.Attrs["latex"] != "E=mc^2" || f.Marks != nil {
		t.Errorf("the formula: %+v", f)
	}
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := PlainText(d); got != "Energy $E=mc^2$ holds, it holds." {
		t.Errorf("text: %q", got)
	}
}
