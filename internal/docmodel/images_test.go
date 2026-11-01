// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

func inline(d *Doc) []*Node {
	if len(d.Content) == 0 {
		return nil
	}
	return d.Content[0].Content
}

// TestMarkdownImages: images of Markdown stay within their text; by default only those of the instance show, the
// others keep their description, linked to the image; options resolve sources and relative links.
func TestMarkdownImages(t *testing.T) {
	d := FromMarkdown("Logo ![Kogge](/logo.png \"The logo\") and [![build](/badge.svg)](https://ci.example.com/x) and ![chart](https://ext.example.com/c.png) ![](javascript:alert(1))")
	nodes := inline(d)
	var kinds []string
	for _, n := range nodes {
		kinds = append(kinds, n.Type)
	}
	if got := strings.Join(kinds, ","); got != "text,inlineImage,text,inlineImage,text,text,text" {
		t.Fatalf("nodes = %s", got)
	}
	if a := nodes[1].Attrs; a["src"] != "/logo.png" || a["alt"] != "Kogge" || a["title"] != "The logo" || a["href"] != nil {
		t.Errorf("image = %v", a)
	}
	if a := nodes[3].Attrs; a["src"] != "/badge.svg" || a["href"] != "https://ci.example.com/x" {
		t.Errorf("linked image = %v", a)
	}
	// Images from elsewhere: their description, linked to them; unsafe addresses stay text.
	if n := nodes[5]; n.Text != "chart" || len(n.Marks) != 1 || n.Marks[0].Attrs["href"] != "https://ext.example.com/c.png" {
		t.Errorf("external image = %+v", n)
	}
	if n := nodes[6]; n.Text != " javascript:alert(1)" || len(n.Marks) != 0 {
		t.Errorf("unsafe image = %+v", n)
	}
	if err := Validate(d, Options{}); err != nil {
		t.Errorf("validate: %v", err)
	}
	if got := PlainText(d); !strings.Contains(got, "Logo Kogge and build and chart") {
		t.Errorf("plain text = %q", got)
	}
	html := HTML(d, nil)
	if !strings.Contains(html, `<a href="https://ci.example.com/x" rel="noopener noreferrer"><img src="/badge.svg" alt="build"></a>`) {
		t.Errorf("html = %s", html)
	}
	if md := Markdown(d); !strings.Contains(md, "[![build](/badge.svg)](https://ci.example.com/x)") {
		t.Errorf("markdown = %s", md)
	}

	// Options: images and relative links of a repository.
	d = FromMarkdownWith("![shot](docs/shot.png) [guide](docs/guide.md) [top](#top) [web](https://example.com) ![ext](https://ext.example.com/a.png)",
		MarkdownOptions{
			Image: func(src string) (string, bool) {
				if strings.HasPrefix(src, "https://") {
					return src, true // the instance allows images from elsewhere
				}
				return "/raw?path=" + src, true
			},
			Link: func(href string) string { return "/src/" + href },
		})
	b, _ := json.Marshal(d)
	for _, want := range []string{`"src":"/raw?path=docs/shot.png"`, `"href":"/src/docs/guide.md"`, `"href":"#top"`, `"href":"https://example.com"`,
		`"src":"https://ext.example.com/a.png"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	if err := Validate(d, Options{}); err == nil {
		t.Error("an external image passed without the instance's permission")
	}
	if err := Validate(d, Options{AllowExternalImages: true}); err != nil {
		t.Errorf("validate with external images: %v", err)
	}
}

// TestInlineImageValidation: inline images need a safe source and a safe link, and stay in text blocks.
func TestInlineImageValidation(t *testing.T) {
	for raw, want := range map[string]string{
		`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"inlineImage","attrs":{"src":"/a.png","href":"/x"}}]}]}`:           "",
		`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"inlineImage","attrs":{"src":"//evil.example/a.png"}}]}]}`:         "same-origin",
		`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"inlineImage","attrs":{"src":"/a.png","href":"javascript:x"}}]}]}`: "links must use",
		`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"inlineImage","attrs":{}}]}]}`:                                     "same-origin",
		`{"type":"doc","content":[{"type":"inlineImage","attrs":{"src":"/a.png"}}]}`:                                                        "outside of a text block",
	} {
		_, err := Parse([]byte(raw), Options{})
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
}
