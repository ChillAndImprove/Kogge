// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *Doc {
	t.Helper()
	d, err := Parse([]byte(s), Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func TestValidateAcceptsAndNormalises(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[
		{"type":"heading","attrs":{"level":9,"onclick":"x"},"content":[{"type":"text","text":"Title"}]},
		{"type":"paragraph","content":[
			{"type":"text","text":"see ","marks":[{"type":"bold"}]},
			{"type":"text","text":"docs","marks":[{"type":"link","attrs":{"href":"https://example.com","target":"_blank"}}]},
			{"type":"mention","attrs":{"user":7,"label":"Anna"}},
			{"type":"issue","attrs":{"key":"SHOP-12"}}
		]},
		{"type":"panel","attrs":{"kind":"bogus"},"content":[{"type":"paragraph"}]}
	]}`)
	if d.Version != Version {
		t.Fatalf("version not set: %d", d.Version)
	}
	h := d.Content[0]
	if h.Attrs["level"] != int64(2) || h.Attrs["onclick"] != nil {
		t.Errorf("heading attrs not normalised: %v", h.Attrs)
	}
	if link := d.Content[1].Content[1].Marks[0]; link.Attrs["target"] != nil {
		t.Errorf("unknown link attribute kept: %v", link.Attrs)
	}
	if d.Content[2].Attrs["kind"] != "info" {
		t.Errorf("panel kind: %v", d.Content[2].Attrs)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]string{
		"script":        `{"type":"doc","content":[{"type":"script","text":"alert(1)"}]}`,
		"javascript":    `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}`,
		"data url":      `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"link","attrs":{"href":"data:text/html,<b>"}}]}]}]}`,
		"external img":  `{"type":"doc","content":[{"type":"image","attrs":{"src":"https://tracker.example/pixel.gif"}}]}`,
		"bad root":      `{"type":"paragraph"}`,
		"unknown mark":  `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"blink"}]}]}]}`,
		"empty text":    `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":""}]}]}`,
		"leaf children": `{"type":"doc","content":[{"type":"horizontalRule","content":[{"type":"paragraph"}]}]}`,
		"bad issue key": `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"issue","attrs":{"key":"<img>"}}]}]}`,
		"future":        `{"type":"doc","version":99}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw), Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	deep := strings.Repeat(`{"type":"blockquote","content":[`, 70) + `{"type":"paragraph"}` + strings.Repeat(`]}`, 70)
	if _, err := Parse([]byte(`{"type":"doc","content":[`+deep+`]}`), Options{}); err == nil {
		t.Error("deep nesting accepted")
	}
	if _, err := Parse([]byte(`{"type":"doc","content":[{"type":"image","attrs":{"src":"https://cdn.example/x.png"}}]}`),
		Options{AllowExternalImages: true}); err != nil {
		t.Errorf("external image with permission: %v", err)
	}
}

// TestFormulas: formulas keep their LaTeX whole, longer than other texts may be; LaTeX beyond MaxLatex, or not a
// string, is refused rather than cut or dropped, also in changes of shared drafts (checked node by node), so that a
// draft and the page published from it hold the same formula. Formulas take no marks and stand where text or blocks
// do.
func TestFormulas(t *testing.T) {
	long := strings.Repeat(`\alpha+`, 700) // more than the 2000 bytes of other texts
	latex, _ := json.Marshal(long)
	d := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Energy "},
		{"type":"inlineMath","attrs":{"latex":"E=mc^2","display":true},"marks":[{"type":"bold"}]}]},
		{"type":"math","attrs":{"latex":`+string(latex)+`}},{"type":"math"},{"type":"math","attrs":{"latex":""}},
		{"type":"math","attrs":{"latex":"`+strings.Repeat("x", MaxLatex)+`"}}]}`)
	if f := d.Content[0].Content[1]; len(f.Attrs) != 1 || f.Attrs["latex"] != "E=mc^2" || f.Marks != nil {
		t.Errorf("formula within text: %+v", f)
	}
	if d.Content[1].Attrs["latex"] != long {
		t.Errorf("long LaTeX cut: %d bytes", len(strAttr(d.Content[1].Attrs, "latex")))
	}
	tooLong := strings.Repeat("x", MaxLatex+1)
	for name, raw := range map[string]string{
		"within text at the top":   `[{"type":"inlineMath","attrs":{"latex":"x"}}]`,
		"with content":             `[{"type":"math","attrs":{"latex":"x"},"content":[{"type":"text","text":"y"}]}]`,
		"too long":                 `[{"type":"math","attrs":{"latex":"` + tooLong + `"}}]`,
		"too long within text":     `[{"type":"paragraph","content":[{"type":"inlineMath","attrs":{"latex":"` + tooLong + `"}}]}]`,
		"not a string":             `[{"type":"math","attrs":{"latex":{"a":[1]}}}]`,
		"a number within text":     `[{"type":"paragraph","content":[{"type":"inlineMath","attrs":{"latex":5}}]}]`,
		"null":                     `[{"type":"math","attrs":{"latex":null}}]`,
		"within text with content": `[{"type":"paragraph","content":[{"type":"inlineMath","content":[{"type":"text","text":"y"}]}]}]`,
	} {
		if _, err := Parse([]byte(`{"type":"doc","content":`+raw+`}`), Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The nodes that changes of shared drafts bring follow the same rules.
	for raw, ok := range map[string]bool{
		`{"type":"inlineMath","attrs":{"latex":"` + tooLong + `"}}`: false,
		`{"type":"math","attrs":{"latex":true}}`:                    false,
		`{"type":"math","attrs":{"latex":""}}`:                      true,
		`{"type":"inlineMath"}`:                                     true,
	} {
		var n Node
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		if err := ValidateNode(&n, 1, Options{}); (err == nil) != ok {
			t.Errorf("%.60s: %v", raw, err)
		}
	}
}

func TestRenderingEscapes(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[
		{"type":"text","text":"<script>alert(1)</script> & \"quotes\""}]},
		{"type":"codeBlock","attrs":{"language":"go\" onmouseover=\"x"},"content":[{"type":"text","text":"if a < b {}"}]}]}`)
	h := HTML(d, nil)
	if strings.Contains(h, "<script>") || !strings.Contains(h, "&lt;script&gt;") {
		t.Errorf("text not escaped: %s", h)
	}
	if strings.Contains(h, `" onmouseover="`) {
		t.Errorf("attribute not escaped: %s", h)
	}
	// Formulas show their LaTeX, escaped, with pandoc's classes and delimiters; empty ones nothing.
	f := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a "},
		{"type":"inlineMath","attrs":{"latex":" x<y & \"z\"</span><script>\n"}},{"type":"inlineMath","attrs":{"latex":" "}}]},
		{"type":"math","attrs":{"latex":"\n\\int_0^1 x\\,dx\n</div><script>alert(1)</script>\n"}},{"type":"math"}]}`)
	want := `<p>a <span class="math inline">\(x&lt;y &amp; &#34;z&#34;&lt;/span&gt;&lt;script&gt;\)</span></p>` +
		`<div class="math display">\[\int_0^1 x\,dx` + "\n" + `&lt;/div&gt;&lt;script&gt;alert(1)&lt;/script&gt;\]</div>`
	if h := HTML(f, nil); h != want {
		t.Errorf("formulas:\n%s\nwant:\n%s", h, want)
	}
}

func TestPlainTextAndReferences(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[
		{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Release"}]},
		{"type":"paragraph","content":[{"type":"text","text":"Fixes SHOP-1 and SHOP-22, not shop-3. "},
			{"type":"mention","attrs":{"user":5,"label":"Ben"}},{"type":"issue","attrs":{"key":"PLAT-7"}}]},
		{"type":"taskList","content":[{"type":"taskItem","attrs":{"checked":false,"assignee":9},"content":[{"type":"paragraph","content":[{"type":"text","text":"Deploy"}]}]}]}
	]}`)
	text := PlainText(d)
	if !strings.Contains(text, "Release\nFixes SHOP-1") || !strings.Contains(text, "@Ben") {
		t.Errorf("plain text: %q", text)
	}
	r := References(d)
	if strings.Join(r.IssueKeys, ",") != "SHOP-1,SHOP-22,PLAT-7" {
		t.Errorf("issue keys: %v", r.IssueKeys)
	}
	if len(r.Users) != 2 || r.Users[0] != 5 || r.Users[1] != 9 {
		t.Errorf("users: %v", r.Users)
	}
	// Formulas count as text (search finds them, a comment of a formula is not empty), as Markdown writes them; what
	// looks like a key within one is no reference.
	f := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Energy "},
		{"type":"inlineMath","attrs":{"latex":" E=mc^2\n"}},{"type":"text","text":" holds."},{"type":"inlineMath","attrs":{"latex":""}}]},
		{"type":"math","attrs":{"latex":"\\text{SHOP-1} = \\int_0^1 x\\,dx"}},{"type":"math"}]}`)
	if got := PlainText(f); got != "Energy $E=mc^2$ holds.\n$$\\text{SHOP-1} = \\int_0^1 x\\,dx$$" {
		t.Errorf("plain text: %q", got)
	}
	if got := PlainText(&Doc{Type: "doc", Content: f.Content[1:2]}); got == "" {
		t.Error("a document of a formula is empty")
	}
	if r := References(f); len(r.IssueKeys) != 0 {
		t.Errorf("issue keys in formulas: %v", r.IssueKeys)
	}
	if got := Summary(f, 200); got != "Energy $E=mc^2$ holds. $$\\text{SHOP-1} = \\int_0^1 x\\,dx$$" {
		t.Errorf("summary: %q", got)
	}
}

func TestIssueKeys(t *testing.T) {
	cases := map[string][]string{
		"SHOP-1 fixed":                          {"SHOP-1"},
		"feature/SHOP-12-checkout":              {"SHOP-12"},
		"see (AB-3), AB-3 again":                {"AB-3"},
		"utf-8 is not, ISO-8859 is a candidate": {"ISO-8859"},
		"X-1 is too short, PROJ-0 no":           nil,
		"https://kogge/browse/WEB-42":           {"WEB-42"},
	}
	for in, want := range cases {
		got := FindIssueKeys(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
	if !IsIssueKey("SHOP-1") || IsIssueKey("SHOP-1x") || IsIssueKey("shop-1") {
		t.Error("IsIssueKey")
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	src := "# Plan\n\nSome **bold** and _italic_ text with `code` and a [link](https://example.com).\n\n" +
		"- one\n- two\n\n1. first\n2. second\n\n- [x] done\n- [ ] open\n\n```go\nfmt.Println(1)\n```\n\n" +
		"Area $\\pi r^2$ costs \\$5.\n\n$$\n\\int_0^1 x\\,dx\n$$\n\n" +
		"| A | B |\n| --- | --- |\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))\n"
	d := FromMarkdown(src)
	raw, _ := json.Marshal(d)
	if _, err := Parse(raw, Options{}); err != nil {
		t.Fatalf("converted document is invalid: %v\n%s", err, raw)
	}
	types := []string{}
	for _, n := range d.Content {
		types = append(types, n.Type)
	}
	want := "heading,paragraph,bulletList,orderedList,taskList,codeBlock,paragraph,math,table,paragraph"
	if strings.Join(types, ",") != want {
		t.Fatalf("blocks: %v", types)
	}
	if strings.Contains(string(raw), "javascript:") || strings.Contains(string(raw), "<script>") {
		t.Errorf("unsafe content survived: %s", raw)
	}
	out := Markdown(d)
	for _, frag := range []string{"# Plan", "**bold**", "[link](https://example.com)", "- [x] done", "```go", "| A | B |",
		"Area $\\pi r^2$ costs \\$5.", "$$\n\\int_0^1 x\\,dx\n$$"} {
		if !strings.Contains(out, frag) {
			t.Errorf("markdown output lacks %q:\n%s", frag, out)
		}
	}
}

// Cells hold no blocks: what would start a list or a heading on a line of its own is written as it is.
func TestMarkdownTableCellsUnescaped(t *testing.T) {
	src := "| Machine | IP |\n| --- | --- |\n| PC | 192.168.178.24 |\n| # 1 | - x |\n"
	if out := Markdown(FromMarkdown(src)); out != src {
		t.Errorf("table:\n%s\nwant:\n%s", out, src)
	}
	// Outside tables, the marks keep their backslash.
	if out := Markdown(Paragraphs("1. not a list")); out != "1\\. not a list\n" {
		t.Errorf("paragraph: %q", out)
	}
}

func TestSummary(t *testing.T) {
	d := Paragraphs("Hello\nworld, this is long")
	if got := Summary(d, 11); got != "Hello world…" {
		t.Errorf("summary: %q", got)
	}
}

func TestAnchors(t *testing.T) {
	d, err := Parse([]byte(`{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"Install the "},{"type":"text","text":"agent","marks":[{"type":"bold"}]},{"type":"text","text":" first, then the agent config."}]},
		{"type":"paragraph","content":[{"type":"text","text":"No agent here"}]}]}`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !AddAnchor(d, "c1", "the agent", 0) {
		t.Fatal("first occurrence not found")
	}
	if !AddAnchor(d, "c2", "agent", 2) {
		t.Fatal("third occurrence not found")
	}
	if AddAnchor(d, "c3", "missing", 0) {
		t.Fatal("found missing text")
	}
	got := Anchors(d)
	if got["c1"] != "the agent" || got["c2"] != "agent" {
		t.Fatalf("anchors: %#v", got)
	}
	if PlainText(d) != "Install the agent first, then the agent config.\nNo agent here" {
		t.Fatalf("text changed: %q", PlainText(d))
	}
	// The bold mark survives the split and the anchor.
	bold := 0
	Walk(d, func(n *Node) bool {
		for _, m := range n.Marks {
			if m.Type == "bold" {
				bold++
			}
		}
		return true
	})
	if bold != 1 {
		t.Fatalf("bold marks: %d", bold)
	}
	if !RemoveAnchor(d, "c1") || len(Anchors(d)) != 1 {
		t.Fatalf("remove: %#v", Anchors(d))
	}
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
}

// TestLinksAndAlignment: links that browsers would send to other hosts, and alignments that are not alignments,
// do not survive validation.
func TestLinksAndAlignment(t *testing.T) {
	for href, want := range map[string]bool{
		"https://example.com/x": true, "/browse/SHOP-1": true, "#top": true, "mailto:a@example.com": true,
		"//evil.example": false, "/\\evil.example": false, "/\t/evil.example": false, "javascript:alert(1)": false,
		"https://ok.example/a\\b": false, "https:evil": false,
	} {
		if got := SafeLink(href); got != want {
			t.Errorf("SafeLink(%q) = %v", href, got)
		}
	}
	d := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","attrs":{"align":"left;position:fixed;inset:0"},"content":[{"type":"text","text":"x"}]},
		{"type":"heading","attrs":{"level":2,"align":"center"},"content":[{"type":"text","text":"y"}]}]}`)
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Content[0].Attrs["align"]; ok {
		t.Errorf("paragraph align = %v", d.Content[0].Attrs["align"])
	}
	if d.Content[1].Attrs["align"] != "center" {
		t.Errorf("heading align = %v", d.Content[1].Attrs["align"])
	}
}
