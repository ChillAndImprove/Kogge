// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"strings"
	"testing"
)

func TestMarkdownOutput(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"1. Not a list, *not* emphasis, a_b_c and _x_ "},
			{"type":"text","text":"bold ","marks":[{"type":"bold"}]},
			{"type":"text","text":"bold italic","marks":[{"type":"bold"},{"type":"italic"}]},
			{"type":"text","text":" link","marks":[{"type":"link","attrs":{"href":"https://example.com/a b"}}]},
			{"type":"hardBreak"},{"type":"text","text":"# no heading & &amp; <tag>"},{"type":"hardBreak"}]},
		{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},
			{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Quoted heading"}]},
			{"type":"paragraph","content":[{"type":"text","text":"two"}]}]},
		{"type":"orderedList","attrs":{"start":9},"content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nine"}]},
				{"type":"codeBlock","attrs":{"language":"sh"},"content":[{"type":"text","text":"echo `+"```"+`"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"ten"}]},
				{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nested"}]}]}]},
				{"type":"math","attrs":{"latex":"\\sum_{i=1}^{10} i"}}]}]},
		{"type":"panel","attrs":{"kind":"warning"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Careful"}]}]},
		{"type":"table","content":[
			{"type":"tableRow","content":[{"type":"tableHeader","attrs":{"colspan":2},"content":[{"type":"paragraph","content":[{"type":"text","text":"a|b"}]}]}]},
			{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"x"}]},
				{"type":"paragraph","content":[{"type":"text","text":"y"}]}]},{"type":"tableCell","content":[{"type":"paragraph"}]}]},
			{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"math","attrs":{"latex":"\\|v\\|\n= 1"}}]},
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"$5"}]}]}]}]},
		{"type":"paragraph","content":[{"type":"text","text":"code ","marks":[{"type":"code"}]},{"type":"status","attrs":{"text":"done","color":"green"}},
			{"type":"text","text":" H"},{"type":"text","text":"2","marks":[{"type":"subscript"}]},{"type":"text","text":"O"}]},
		{"type":"paragraph","content":[{"type":"text","text":"Costs $5 or 5 $ each, "},{"type":"inlineMath","attrs":{"latex":"x^2"}},
			{"type":"text","text":"2 times "},{"type":"inlineMath","attrs":{"latex":"a $ b"}},{"type":"text","text":"."}]}
	]}`)
	got := Markdown(d)
	want := "1\\. Not a list, \\*not\\* emphasis, a_b_c and \\_x\\_ **bold *bold italic*** [link](<https://example.com/a b>)\\\n" +
		"\\# no heading & \\&amp; \\<tag>\n\n" +
		"> one\n>\n> ### Quoted heading\n>\n> two\n\n" +
		"9. nine\n\n   ````sh\n   echo ```\n   ````\n\n10. ten\n    - nested\n\n    $$\n    \\sum_{i=1}^{10} i\n    $$\n\n" +
		"> [!WARNING]\n> Careful\n\n" +
		"| a\\|b |  |\n| --- | --- |\n| x<br>y |  |\n| $\\\\|v\\\\| = 1$ | \\$5 |\n\n" +
		"`code `**DONE** H<sub>2</sub>O\n\n" +
		"Costs \\$5 or 5 $ each, $x^2$&#50; times $a \\$ b$.\n"
	if got != want {
		t.Errorf("Markdown:\n%s\nwant:\n%s", got, want)
	}
	// What Markdown says comes back as it was.
	back := FromMarkdown(got)
	if text := PlainText(back); !strings.Contains(text, "1. Not a list, *not* emphasis, a_b_c and _x_ bold bold italic link") ||
		!strings.Contains(text, "# no heading & &amp; <tag>") {
		t.Errorf("read back: %q", text)
	}
	var types []string
	for _, n := range back.Content {
		types = append(types, n.Type)
	}
	if strings.Join(types, ",") != "paragraph,blockquote,orderedList,blockquote,table,paragraph,paragraph" {
		t.Errorf("blocks read back: %v", types)
	}
	// Formulas come back as formulas, the dollars of text as text.
	if got := outline(back.Content[len(back.Content)-1:]); got != `p["Costs $5 or 5 $ each, " m‹x^2› "2 times " m‹a \$ b› "."]` {
		t.Errorf("formulas read back: %s", got)
	}
	if got := outline(back.Content[2].Content[1].Content[2:]); got != `M‹\sum_{i=1}^{10} i›` {
		t.Errorf("formula in a list read back: %s", got)
	}
	if got := outline(back.Content[4].Content[2].Content); got != `tableCell[p[m‹\|v\| = 1›]] tableCell[p["$5"]]` {
		t.Errorf("formula in a table read back: %s", got)
	}
}

func TestMarkdownLinksAndFrontMatter(t *testing.T) {
	d := mustParse(t, `{"type":"doc","content":[{"type":"paragraph","content":[
		{"type":"text","text":"Setup","marks":[{"type":"link","attrs":{"href":"/wiki/spaces/DOCS/pages/7/setup"}}]},
		{"type":"text","text":" by "},{"type":"mention","attrs":{"user":3,"label":"old"}},{"type":"text","text":" "},
		{"type":"issue","attrs":{"key":"SHOP-1"}}]},
		{"type":"image","attrs":{"attachment":12,"src":"/api/v1/wiki/attachments/12","alt":"Plan"}},
		{"type":"image","attrs":{"src":"/files/gone.png","alt":"Gone"}}]}`)
	got := MarkdownWith(d, MarkdownLinks{
		Href: func(h string) string {
			if h == "/wiki/spaces/DOCS/pages/7/setup" {
				return "7-setup.md"
			}
			return "https://kogge.example" + h
		},
		Image: func(id int64, src string) string {
			if id == 12 {
				return "../attachments/7/plan (1).png"
			}
			return ""
		},
		User: func(id int64, label string) string { return "Dirk Dahl" },
	})
	want := "[Setup](7-setup.md) by @Dirk Dahl [SHOP-1](https://kogge.example/browse/SHOP-1)\n\n" +
		"![Plan](<../attachments/7/plan (1).png>)\n\nGone\n"
	if got != want {
		t.Errorf("Markdown:\n%s\nwant:\n%s", got, want)
	}
	fm := FrontMatter([][2]any{{"title", `Release "2.0": notes`}, {"id", 7}, {"labels", []string{"ops", "a\nb"}}})
	if fm != "---\ntitle: \"Release \\\"2.0\\\": notes\"\nid: 7\nlabels: [\"ops\", \"a\\nb\"]\n---\n" {
		t.Errorf("front matter:\n%s", fm)
	}
}
