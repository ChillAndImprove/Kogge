// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

const twoColumns = `{"type":"columns","attrs":{"layout":"left"},"content":[
	{"type":"column","content":[{"type":"paragraph","content":[{"type":"text","text":"Sidebar"}]}]},
	{"type":"column","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Main"}]},
		{"type":"paragraph","content":[{"type":"text","text":"Body text"}]}]}]}`

func TestColumnsValidate(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[`+twoColumns+`,
		{"type":"columns","attrs":{"layout":"left"},"content":[
			{"type":"column","content":[{"type":"paragraph"}]},{"type":"column","content":[{"type":"paragraph"}]},
			{"type":"column","content":[{"type":"paragraph"}]}]},
		{"type":"columns","attrs":{"layout":"diagonal"},"content":[
			{"type":"column","content":[{"type":"paragraph"}]},{"type":"column","content":[{"type":"paragraph"}]}]}]}`)
	if got := d.Content[0].Attrs["layout"]; got != "left" {
		t.Errorf("layout of two columns: %v", got)
	}
	// A layout that does not fit the number of columns, or none Kogge knows, shares the width equally.
	if got := d.Content[1].Attrs["layout"]; got != "equal" {
		t.Errorf("left with three columns: %v", got)
	}
	if got := d.Content[2].Attrs["layout"]; got != "equal" {
		t.Errorf("unknown layout: %v", got)
	}
	if w := ColumnWidths(d.Content[0]); len(w) != 2 || w[0] != 1 || w[1] != 2 {
		t.Errorf("widths: %v", w)
	}

	col := `{"type":"column","content":[{"type":"paragraph"}]}`
	for name, raw := range map[string]string{
		"nested in a panel": `[{"type":"panel","content":[` + twoColumns + `]}]`,
		"in a column":       `[{"type":"columns","content":[{"type":"column","content":[` + twoColumns + `]},` + col + `]}]`,
		"loose column":      `[` + col + `]`,
		"one column":        `[{"type":"columns","content":[` + col + `]}]`,
		"four columns":      `[{"type":"columns","content":[` + col + `,` + col + `,` + col + `,` + col + `]}]`,
		"empty column":      `[{"type":"columns","content":[` + col + `,{"type":"column"}]}]`,
		"paragraph inside":  `[{"type":"columns","content":[` + col + `,{"type":"paragraph"}]}]`,
		"in a list":         `[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph"},` + twoColumns + `]}]}]`,
	} {
		if _, err := Parse([]byte(`{"type":"doc","content":`+raw+`}`), Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Changes of shared drafts bring single columns and parts of layouts; the editor's schema decides where they
	// may go.
	var n Node
	if err := json.Unmarshal([]byte(col), &n); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNode(&n, 1, Options{}); err != nil {
		t.Errorf("a column in a change: %v", err)
	}
}

// TestAttributesOfNodesWithoutAttributes: headings and panels without attributes get their defaults (they made
// validation fail on a nil map before).
func TestAttributesOfNodesWithoutAttributes(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"x"}]},
		{"type":"panel","content":[{"type":"paragraph"}]}]}`)
	if d.Content[0].Attrs["level"] != int64(2) || d.Content[1].Attrs["kind"] != "info" {
		t.Fatalf("defaults: %v %v", d.Content[0].Attrs, d.Content[1].Attrs)
	}
	// Formulas without LaTeX are valid (the editor fills them in afterwards); nothing shows them.
	f := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"inlineMath"}]},
		{"type":"math"}]}`)
	if h, md, text, ex := HTML(f, nil), Markdown(f), PlainText(f), Excerpt(f, 100); h != "<p>a</p>" || md != "a\n" || text != "a" || ex != "a" {
		t.Errorf("empty formulas: %q %q %q %q", h, md, text, ex)
	}
}

func TestColumnsRender(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[`+twoColumns+`,{"type":"paragraph","content":[{"type":"text","text":"After"}]}]}`)
	h := HTML(d, nil)
	for _, want := range []string{`<div class="columns"><div class="column" style="flex:1 1 0"><p>Sidebar</p></div>`,
		`<div class="column" style="flex:2 1 0"><h2 id="h-main">Main</h2><p>Body text</p></div></div><p>After</p>`} {
		if !strings.Contains(h, want) {
			t.Errorf("HTML lacks %s:\n%s", want, h)
		}
	}
	md := Markdown(d)
	want := "<!-- columns: left -->\n\n<!-- column -->\n\nSidebar\n\n<!-- column -->\n\n## Main\n\nBody text\n\n<!-- /columns -->\n\nAfter\n"
	if md != want {
		t.Errorf("Markdown:\n%s\nwant:\n%s", md, want)
	}
	if got := PlainText(d); got != "Sidebar\n\nMain\nBody text\n\nAfter" {
		t.Errorf("plain text: %q", got)
	}
}

func TestConfluenceLayouts(t *testing.T) {
	src := `<ac:layout>
		<ac:layout-section ac:type="two_left_sidebar"><ac:layout-cell><p>Nav</p></ac:layout-cell><ac:layout-cell><p>Text</p></ac:layout-cell></ac:layout-section>
		<ac:layout-section ac:type="single"><ac:layout-cell><p>Single</p></ac:layout-cell></ac:layout-section>
		<ac:layout-section ac:type="three_with_sidebars"><ac:layout-cell><p>A</p></ac:layout-cell><ac:layout-cell></ac:layout-cell>
			<ac:layout-cell><p>C</p></ac:layout-cell></ac:layout-section>
		<ac:layout-section ac:type="four_equal"><ac:layout-cell><p>1</p></ac:layout-cell><ac:layout-cell><p>2</p></ac:layout-cell>
			<ac:layout-cell><p>3</p></ac:layout-cell><ac:layout-cell><p>4</p></ac:layout-cell></ac:layout-section>
		</ac:layout>
		<ac:structured-macro ac:name="info"><ac:rich-text-body><ac:layout><ac:layout-section ac:type="two_equal">
			<ac:layout-cell><p>In</p></ac:layout-cell><ac:layout-cell><p>Panel</p></ac:layout-cell></ac:layout-section></ac:layout>
		</ac:rich-text-body></ac:structured-macro>`
	d, _ := FromConfluence(src, ImportResolver{})
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range d.Content {
		got = append(got, n.Type+":"+strAttr(n.Attrs, "layout")+":"+strings.ReplaceAll(PlainText(&Doc{Content: []*Node{n}}), "\n", "|"))
	}
	want := []string{"columns:left:Nav||Text", "paragraph::Single", "columns:sides:A||C", "paragraph::1", "paragraph::2",
		"paragraph::3", "paragraph::4", "panel::In|Panel"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("blocks:\n%v\nwant:\n%v", got, want)
	}
}

func TestHeadingID(t *testing.T) {
	for text, want := range map[string]string{
		"Größe & Maße!":           "h-größe-maße",
		"  Release 2.0 notes ":    "h-release-2-0-notes",
		strings.Repeat("ab ", 30): "h-" + strings.Repeat("ab-", 20),
	} {
		n := &Node{Type: "heading", Content: []*Node{{Type: "text", Text: text}}}
		if got := HeadingID(n); got != want {
			t.Errorf("HeadingID(%q) = %q, want %q", text, got, want)
		}
	}
	if got := HeadingID(&Node{Type: "heading", Attrs: map[string]any{"id": "setup"}}); got != "setup" {
		t.Errorf("explicit id: %q", got)
	}
}
