// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

func types(d *Doc) []string {
	var out []string
	Walk(d, func(n *Node) bool {
		out = append(out, n.Type)
		return true
	})
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestFromJiraHTML(t *testing.T) {
	resolver := ImportResolver{
		User: func(ref string) (int64, string) {
			if ref == "lena" {
				return 7, "Lena Huber"
			}
			return 0, ""
		},
		Issue: func(key string) bool { return key == "SHOP-1" },
	}
	src := `<p>Hello <a href="/secure/ViewProfile.jspa?name=lena" class="user-hover" rel="lena">Lena</a>, see
		<a href="https://jira.example.com/browse/SHOP-1" class="issue-link" data-issue-key="SHOP-1">SHOP-1</a> and <b>bold <i>both</i></b>.</p>
		<div class="code panel"><div class="codeContent panelContent"><pre class="code-java">int a = 1;
int b = 2;</pre></div></div>
		<div class="panel"><div class="panelHeader"><b>Note</b></div><div class="panelContent"><p>Careful</p></div></div>
		<ul><li>one<ul><li>nested</li></ul></li><li>two</li></ul>
		<table><tbody><tr><th>A</th><th>B</th></tr><tr><td>1</td><td colspan="2">2</td></tr></tbody></table>
		<p><span style="color: #ff0000">red</span><br/>next line &amp; more</p>
		<h2>Title</h2><blockquote><p>quoted</p></blockquote><hr/>`
	d := FromHTML(src, resolver)
	if err := Validate(d, Options{AllowExternalImages: true}); err != nil {
		t.Fatal(err)
	}
	got := types(d)
	for _, want := range []string{"mention", "issue", "codeBlock", "panel", "bulletList", "table", "tableHeader", "hardBreak", "heading", "blockquote", "horizontalRule"} {
		if !contains(got, want) {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	text := PlainText(d)
	if !strings.Contains(text, "int a = 1;\nint b = 2;") || !strings.Contains(text, "next line & more") || strings.Contains(text, "  ") {
		t.Fatalf("text: %q", text)
	}
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), `"language":"java"`) || !strings.Contains(string(raw), `"color":"red"`) || !strings.Contains(string(raw), `"colspan":2`) {
		t.Fatalf("attributes: %s", raw)
	}
}

func TestFromConfluenceStorage(t *testing.T) {
	resolver := ImportResolver{
		User: func(ref string) (int64, string) { return 3, "Dirk" },
		Page: func(title, space string) (int64, string) {
			if title == "Setup" {
				return 42, "/wiki/spaces/DOCS/pages/42/setup"
			}
			return 0, ""
		},
		Attachment: func(name, page, space string) (int64, string) {
			if name == "diagram.png" {
				return 9, "/api/v1/wiki/attachments/9"
			}
			return 0, ""
		},
		Anchor: func(ref string) string { return "c" + ref },
	}
	src := `<p>Welcome <ac:link><ri:user ri:userkey="ff80"/></ac:link>! Read
		<ac:link><ri:page ri:content-title="Setup" ri:space-key="DOCS"/><ac:plain-text-link-body><![CDATA[the setup]]></ac:plain-text-link-body></ac:link>.
		<ac:inline-comment-marker ac:ref="abc">commented</ac:inline-comment-marker> <ac:emoticon ac:name="smile"/></p>
		<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter><ac:parameter ac:name="title">main.go</ac:parameter>
		<ac:plain-text-body><![CDATA[func main() {
	println("<hi>")
}]]></ac:plain-text-body></ac:structured-macro>
		<ac:structured-macro ac:name="warning"><ac:parameter ac:name="title">Attention</ac:parameter><ac:rich-text-body><p>Hot</p></ac:rich-text-body></ac:structured-macro>
		<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">More</ac:parameter><ac:rich-text-body><p>Hidden</p></ac:rich-text-body></ac:structured-macro>
		<p><ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Green</ac:parameter><ac:parameter ac:name="title">DONE</ac:parameter></ac:structured-macro>
		<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">SHOP-12</ac:parameter></ac:structured-macro></p>
		<ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = SHOP</ac:parameter></ac:structured-macro>
		<ac:structured-macro ac:name="toc"><ac:parameter ac:name="maxLevel">2</ac:parameter></ac:structured-macro>
		<ac:structured-macro ac:name="include"><ac:parameter ac:name=""><ac:link><ri:page ri:content-title="Setup" ri:space-key="DOCS"/></ac:link></ac:parameter></ac:structured-macro>
		<ac:task-list><ac:task><ac:task-status>complete</ac:task-status><ac:task-body>Ship it</ac:task-body></ac:task>
		<ac:task><ac:task-status>incomplete</ac:task-status><ac:task-body>Celebrate</ac:task-body></ac:task></ac:task-list>
		<ac:image ac:width="300"><ri:attachment ri:filename="diagram.png"/></ac:image>
		<ac:layout><ac:layout-section><ac:layout-cell><p>Cell</p></ac:layout-cell></ac:layout-section></ac:layout>
		<ac:structured-macro ac:name="gliffy"><ac:parameter ac:name="name">Flow</ac:parameter></ac:structured-macro>`
	d, unsupported := FromConfluence(src, resolver)
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
	got := types(d)
	for _, want := range []string{"mention", "codeBlock", "panel", "expand", "status", "issue", "macro", "taskList", "taskItem", "image", "emoji", "unsupported"} {
		if !contains(got, want) {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if unsupported["gliffy"] != 1 || len(unsupported) != 1 {
		t.Fatalf("unsupported: %v", unsupported)
	}
	raw, _ := json.Marshal(d)
	for _, want := range []string{`"href":"/wiki/spaces/DOCS/pages/42/setup"`, `"id":"cabc"`, `"name":"issue-list"`, `"name":"include","params":{"page":42}`,
		`"kind":"warning"`, `"title":"main.go"`, `"checked":true`, `"attachment":9`, `"maxLevel":2`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	if !strings.Contains(PlainText(d), `println("<hi>")`) {
		t.Fatalf("code text: %q", PlainText(d))
	}
}

// TestConfluenceFormulas: the formulas of the LaTeX Math app become formulas — mathblock on lines of its own (the
// paragraph around it split, as around images; within a heading, a formula within text), mathinline within text (in
// a paragraph of its own between blocks). Their LaTeX is the body or the parameter body, which may be URI-encoded.
func TestConfluenceFormulas(t *testing.T) {
	src := `<p>Euler: <ac:structured-macro ac:name="mathinline" ac:schema-version="1"><ac:parameter ac:name="body">e^{i\pi}+1=0</ac:parameter></ac:structured-macro> holds.</p>
		<ac:structured-macro ac:name="mathblock" ac:schema-version="1"><ac:parameter ac:name="anchor">eq1</ac:parameter>
		<ac:plain-text-body><![CDATA[\frac{a}{b} < 1 & x]]></ac:plain-text-body></ac:structured-macro>
		<p>Before <ac:structured-macro ac:name="mathblock"><ac:plain-text-body><![CDATA[\sum_i x_i]]></ac:plain-text-body></ac:structured-macro> after</p>
		<ac:structured-macro ac:name="mathinline"><ac:parameter ac:name="body">--uriencoded--%5Csqrt%7Bx%2B1%7D%20</ac:parameter></ac:structured-macro>
		<h2>Formula <ac:structured-macro ac:name="mathblock"><ac:plain-text-body><![CDATA[x^2]]></ac:plain-text-body></ac:structured-macro></h2>
		<table><tbody><tr><td><ac:structured-macro ac:name="mathblock"><ac:parameter ac:name="body">y</ac:parameter></ac:structured-macro></td></tr></tbody></table>
		<ac:structured-macro ac:name="mathblock"><ac:plain-text-body><![CDATA[ ]]></ac:plain-text-body></ac:structured-macro>
		<ac:structured-macro ac:name="mathblock-ref"><ac:parameter ac:name="anchor">eq1</ac:parameter></ac:structured-macro>`
	d, unsupported := FromConfluence(src, ImportResolver{})
	if err := Validate(d, Options{}); err != nil {
		t.Fatal(err)
	}
	want := `p["Euler: " m‹e^{i\pi}+1=0› " holds."] M‹\frac{a}{b} < 1 & x› p["Before"] M‹\sum_i x_i› p["after"] p[m‹\sqrt{x+1}›] ` +
		`h["Formula " m‹x^2›] table[tableRow[tableCell[M‹y›]]] unsupported[]`
	if got := outline(d.Content); got != want {
		t.Errorf("blocks:\n%s\nwant:\n%s", got, want)
	}
	if len(unsupported) != 1 || unsupported["mathblock-ref"] != 1 {
		t.Errorf("unsupported: %v", unsupported)
	}
}
