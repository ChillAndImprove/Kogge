// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// readme is a README of docs/; BASE stands for the instance's address.
const readme = `# Service

[![Build](BASE/api/v1/code/repos/MD/app/badge.svg)](BASE/builds/md/app) ![Logo](logo.svg "Our logo") ![Top](/assets/top.svg)

![Chart](https://charts.example.com/c.png) ![Escape](../../../outside.png)

See [the guide](guide.md#setup), [the top](/README.md), [above](#service) and [the web](https://example.com).

It answers in $O(\log n)$, as GitHub shows it:

$$
\sum_{i=1}^n i = \frac{n(n+1)}{2}
$$
`

// TestMarkdownFiles: Markdown files of a repository show their images — paths of the repository from the file's
// commit, the instance's own addresses — and link to files in Hafen; images from other servers show only where the
// instance allows them, which also opens the pages' content policy to them.
func TestMarkdownFiles(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MD", "name": "Markdown"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MD/repos", map[string]any{"name": "app", "readme": true}, nil)
	logo := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/MD/app/files", map[string]any{"branch": "main", "path": "docs/logo.svg", "content": logo}, nil)
	var commit struct {
		SHA string `json:"sha"`
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/MD/app/files", map[string]any{"branch": "main", "newBranch": "docs",
		"path": "docs/README.md", "content": strings.ReplaceAll(readme, "BASE", env.URL)}, &commit)

	file := func() string {
		t.Helper()
		var f struct {
			Markdown json.RawMessage `json:"markdown"`
		}
		admin.Get("/api/v1/code/repos/MD/app/file?ref=docs&path=docs/README.md", &f)
		return string(f.Markdown)
	}
	raw := func(path string) string {
		return `"src":"/api/v1/code/repos/MD/app/raw?ref=` + commit.SHA + `&path=` + url.QueryEscape(path) + `"`
	}
	doc := file()
	for _, want := range []string{
		// The instance's own addresses become paths (a leading slash in the file means the repository's top).
		`"type":"inlineImage","attrs":{"alt":"Build","href":"` + env.URL + `/builds/md/app","src":"/api/v1/code/repos/MD/app/badge.svg"}`,
		raw("docs/logo.svg"), `"title":"Our logo"`,
		raw("assets/top.svg"), // from the repository's top
		raw("outside.png"),    // ".." stops at the top
		`"href":"/code/md/app/src/docs/guide.md?at=docs#setup"`,
		`"href":"/code/md/app/src/README.md?at=docs"`,
		`"href":"#service"`, `"href":"https://example.com"`,
		// Not allowed: the description, linked to the image.
		`"marks":[{"type":"link","attrs":{"href":"https://charts.example.com/c.png"}}],"text":"Chart"`,
		// Formulas, as GitHub reads them.
		`{"type":"inlineMath","attrs":{"latex":"O(\\log n)"}}`, `{"type":"math","attrs":{"latex":"\\sum_{i=1}^n i = \\frac{n(n+1)}{2}"}}`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("markdown lacks %s:\n%s", want, doc)
		}
	}
	// The image itself, as the browser asks for it.
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/MD/app/raw?ref="+commit.SHA+"&path=docs/logo.svg", nil, "Sec-Fetch-Dest", "image"); r.Status != http.StatusOK ||
		r.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(string(r.Body), "<rect") {
		t.Errorf("image: %d %v", r.Status, r.Header)
	}

	var me struct {
		ExternalImages bool `json:"externalImages"`
	}
	admin.Get("/api/v1/me", &me)
	if me.ExternalImages {
		t.Error("external images are allowed by default")
	}
	page := env.Anonymous().Do(http.MethodGet, "/code/md/app", nil)
	if csp := page.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob:;") {
		t.Errorf("page policy = %q", csp)
	}

	// The instance allows images from elsewhere.
	var settings map[string]any
	admin.Get("/api/v1/admin/settings", &settings)
	settings["allowExternalImages"] = true
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", settings, nil)
	if doc := file(); !strings.Contains(doc, `"src":"https://charts.example.com/c.png"`) {
		t.Errorf("external image not shown:\n%s", doc)
	}
	admin.Get("/api/v1/me", &me)
	if !me.ExternalImages {
		t.Error("me does not say that external images are allowed")
	}
	page = env.Anonymous().Do(http.MethodGet, "/code/md/app", nil)
	if csp := page.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob: https:;") {
		t.Errorf("page policy with external images = %q", csp)
	}
}

// TestMarkdownTexts: comments and descriptions written in Markdown can be previewed as they will be stored, and
// whoever may change them gets their Markdown back, so that a change keeps the formatting.
func TestMarkdownTexts(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev, reader := env.CreateUser("dev"), env.CreateUser("reader")
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MDT", "name": "Texts"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MDT/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/MDT/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": userID(t, admin)}, "permission": "codeproject.admin"},
		{"holder": map[string]any{"type": "user", "id": userID(t, dev)}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": userID(t, reader)}, "permission": "repo.read"}}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/MDT/app/files", map[string]any{"branch": "main", "newBranch": "topic",
		"path": "notes.txt", "content": "notes\n"}, nil)
	env.WorkJobs()

	text := "## Plan\n\n- **Bold** step with `code`\n- *Second*  step\n\n```go\nfunc main() {}\n```\n\n" +
		"Area $\\pi r^2$ costs \\$5.\n\n$$\n\\sum_i x_i\n$$\n"
	type doc struct {
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"content"`
	}
	// The preview is the stored form; it needs a session.
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/code/markdown", map[string]any{"markdown": text}); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous preview: %d", r.Status)
	}
	var preview struct {
		Doc doc `json:"doc"`
	}
	reader.MustDo(http.MethodPost, "/api/v1/code/markdown", map[string]any{"markdown": text}, &preview)
	var types []string
	for _, n := range preview.Doc.Content {
		types = append(types, n.Type)
	}
	if strings.Join(types, ",") != "heading,bulletList,codeBlock,paragraph,math" {
		t.Errorf("preview: %s", types)
	}
	// Formulas within text and of their own; the dollar of a price is text.
	if inline := preview.Doc.Content[3].Content; len(inline) != 3 || inline[1].Type != "inlineMath" {
		t.Errorf("preview of the formula within text: %+v", inline)
	}

	pr := "/api/v1/code/repos/MDT/app/pull-requests"
	var created struct {
		Description json.RawMessage `json:"description"`
	}
	dev.MustDo(http.MethodPost, pr, map[string]any{"title": "Topic", "source": "topic", "descriptionMarkdown": text}, &created)
	var stored struct {
		Doc json.RawMessage `json:"doc"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/code/markdown", map[string]any{"markdown": text}, &stored)
	if string(stored.Doc) != string(created.Description) {
		t.Errorf("the preview differs from the stored description:\n%s\n%s", stored.Doc, created.Description)
	}
	// And back: the document as Markdown, for editing it as such; it needs a session, too.
	source := map[string]any{"doc": stored.Doc}
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/code/markdown/source", source); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous Markdown source: %d", r.Status)
	}
	var written struct {
		Markdown string `json:"markdown"`
	}
	reader.MustDo(http.MethodPost, "/api/v1/code/markdown/source", source, &written)
	if written.Markdown != text {
		t.Errorf("Markdown source: %q, want %q", written.Markdown, text)
	}

	// The description's Markdown, for its author; readers get none.
	type pull struct {
		Description json.RawMessage `json:"description"`
		Markdown    string          `json:"descriptionMarkdown"`
	}
	var mine, theirs pull
	dev.Get(pr+"/1", &mine)
	reader.Get(pr+"/1", &theirs)
	if mine.Markdown != text {
		t.Errorf("description Markdown: %q", mine.Markdown)
	}
	if theirs.Markdown != "" {
		t.Errorf("a reader got the Markdown: %q", theirs.Markdown)
	}
	// Saving the Markdown unchanged keeps the description as it was.
	dev.MustDo(http.MethodPatch, pr+"/1", map[string]any{"descriptionMarkdown": mine.Markdown}, nil)
	var again pull
	dev.Get(pr+"/1", &again)
	if string(again.Description) != string(mine.Description) {
		t.Errorf("description changed by a round trip:\n%s\n%s", mine.Description, again.Description)
	}

	// A description given as a document has no Markdown of its own: it is written out.
	dev.MustDo(http.MethodPatch, pr+"/1", map[string]any{"description": map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Plain",
			"marks": []any{map[string]any{"type": "bold"}}}}}}}}, nil)
	dev.Get(pr+"/1", &again)
	if again.Markdown != "**Plain**\n" {
		t.Errorf("Markdown of a document: %q", again.Markdown)
	}

	// Comments: their author gets the Markdown back, others do not.
	var c struct {
		ID       int64           `json:"id"`
		Body     json.RawMessage `json:"body"`
		Markdown string          `json:"markdown"`
	}
	reader.MustDo(http.MethodPost, pr+"/1/comments", map[string]any{"markdown": "Use *emphasis* and [a link](https://example.com)."}, &c)
	if c.Markdown != "Use *emphasis* and [a link](https://example.com)." {
		t.Errorf("comment Markdown: %q", c.Markdown)
	}
	var timeline []struct {
		Comment *struct {
			Markdown string `json:"markdown"`
		} `json:"comment"`
	}
	dev.Get(pr+"/1/timeline", &timeline)
	for _, a := range timeline {
		if a.Comment != nil && a.Comment.Markdown != "" {
			t.Errorf("another reader got a comment's Markdown: %q", a.Comment.Markdown)
		}
	}
	var edited struct {
		Body json.RawMessage `json:"body"`
	}
	reader.MustDo(http.MethodPut, "/api/v1/code/pull-request-comments/"+strconv.FormatInt(c.ID, 10), map[string]any{"markdown": c.Markdown}, &edited)
	if string(edited.Body) != string(c.Body) {
		t.Errorf("comment changed by a round trip:\n%s\n%s", c.Body, edited.Body)
	}
}

func userID(t *testing.T, c *apitest.Client) int64 {
	t.Helper()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	c.Get("/api/v1/me", &me)
	return me.User.ID
}
