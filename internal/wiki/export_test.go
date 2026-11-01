// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	rscpdf "rsc.io/pdf"

	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/wiki"
)

type exportInfo struct {
	ID         int64  `json:"id"`
	State      string `json:"state"`
	Format     string `json:"format"`
	PagesDone  int    `json:"pagesDone"`
	PagesTotal int    `json:"pagesTotal"`
	Error      string `json:"error"`
	Filename   string `json:"filename"`
	URL        string `json:"url"`
	Size       int64  `json:"size"`
}

func pngData() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 60, 30))
	for x := 0; x < 60; x++ {
		img.Set(x, 10, color.RGBA{200, 30, 30, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func uploadFile(t *testing.T, c *apitest.Client, pageID int64, name string, data []byte, contentType string) int64 {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := map[string][]string{"Content-Disposition": {`form-data; name="file"; filename="` + name + `"`}, "Content-Type": {contentType}}
	part, _ := w.CreatePart(h)
	_, _ = part.Write(data)
	_ = w.Close()
	var out []struct {
		ID int64 `json:"id"`
	}
	c.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(pageID)+"/attachments", &buf, &out, "Content-Type", w.FormDataContentType())
	return out[0].ID
}

func publishBody(t *testing.T, c *apitest.Client, p page, body map[string]any) page {
	t.Helper()
	var out page
	c.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(p.ID)+"/publish", map[string]any{"title": p.Title, "body": body,
		"baseVersion": p.Version}, &out)
	return out
}

type exportTree struct {
	handbook, details, secret, secretChild, blog page
	image                                        int64
}

// exportFixture: a handbook with a child page, an image and the macros of a page; a restricted page below it that
// only lena sees (with a child of its own), and a blog post.
func exportFixture(t *testing.T, f *fixture) exportTree {
	t.Helper()
	var x exportTree
	x.handbook = f.publishNew(t, f.lena, "Handbook", f.space.HomePageID, "draft")
	x.details = f.publishNew(t, f.lena, "Details", x.handbook.ID, "Details for „Größen“ and 5 €")
	x.secret = f.publishNew(t, f.lena, "Secret plans", x.handbook.ID, "Only for Lena")
	x.secretChild = f.publishNew(t, f.lena, "Secret child", x.secret.ID, "Also hidden")
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(x.secret.ID)+"/restrictions", map[string]any{
		"view": []map[string]any{{"type": "user", "id": f.ids["lena"]}}, "edit": []any{}}, nil)
	x.image = uploadFile(t, f.lena, x.handbook.ID, "diagram.png", pngData(), "image/png")
	uploadFile(t, f.lena, x.handbook.ID, "notes.txt", []byte("plain notes"), "text/plain")
	detailsURL := "/wiki/spaces/DOCS/pages/" + id(x.details.ID) + "/details"
	t2 := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
	link := func(s, href string) map[string]any {
		return map[string]any{"type": "text", "text": s, "marks": []any{map[string]any{"type": "link", "attrs": map[string]any{"href": href}}}}
	}
	p := func(content ...any) map[string]any { return map[string]any{"type": "paragraph", "content": content} }
	body := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "macro", "attrs": map[string]any{"name": "toc", "params": map[string]any{}}},
		map[string]any{"type": "heading", "attrs": map[string]any{"level": 2}, "content": []any{t2("Setup")}},
		p(t2("Read "), link("the details", detailsURL), t2(" and "), link("the notes", "/api/v1/wiki/attachments/"+id(x.image)), t2(".")),
		map[string]any{"type": "image", "attrs": map[string]any{"attachment": x.image, "src": "/api/v1/wiki/attachments/" + id(x.image),
			"alt": "Diagram"}},
		map[string]any{"type": "columns", "attrs": map[string]any{"layout": "left"}, "content": []any{
			map[string]any{"type": "column", "content": []any{p(t2("Left column"))}},
			map[string]any{"type": "column", "content": []any{p(t2("Right column"))}}}},
		map[string]any{"type": "macro", "attrs": map[string]any{"name": "children", "params": map[string]any{}}},
		map[string]any{"type": "codeBlock", "attrs": map[string]any{"language": "mermaid"}, "content": []any{t2("graph TD\n  A --> B")}},
		p(t2("Energy "), map[string]any{"type": "inlineMath", "attrs": map[string]any{"latex": "E=mc^2"}}, t2(" holds.")),
		map[string]any{"type": "math", "attrs": map[string]any{"latex": `\int_0^1 x\,dx < 1`}},
	}}
	x.handbook = publishBody(t, f.lena, x.handbook, body)
	var b page
	f.lena.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Launch news", "kind": "blog"}, &b)
	f.lena.Post("/api/v1/wiki/pages/"+id(b.ID)+"/publish", map[string]any{"title": "Launch news", "body": doc("We launched."), "baseVersion": 0}, &x.blog)
	return x
}

// pdfText returns the text of a PDF's pages (fpdf writes the strings of its fonts as UTF-16) and its bookmarks.
func pdfText(t *testing.T, data []byte) (string, []string, int) {
	t.Helper()
	r, err := rscpdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a PDF: %v", err)
	}
	op := regexp.MustCompile(`\(((?:\\.|[^\\)])*)\)\s*Tj`)
	var all []string
	for i := 1; i <= r.NumPage(); i++ {
		raw, _ := io.ReadAll(r.Page(i).V.Key("Contents").Reader())
		for _, m := range op.FindAllSubmatch(raw, -1) {
			s := regexp.MustCompile(`\\(.)`).ReplaceAll(m[1], []byte("$1"))
			if len(s)%2 == 0 {
				u := make([]uint16, len(s)/2)
				for j := range u {
					u[j] = uint16(s[2*j])<<8 | uint16(s[2*j+1])
				}
				all = append(all, string(utf16.Decode(u)))
			}
		}
	}
	var marks []string
	var walk func(o rscpdf.Outline)
	walk = func(o rscpdf.Outline) {
		for _, c := range o.Child {
			marks = append(marks, c.Title)
			walk(c)
		}
	}
	walk(r.Outline())
	return strings.Join(all, " "), marks, r.NumPage()
}

func TestExportPage(t *testing.T) {
	f := setup(t)
	x := exportFixture(t, f)
	pid := id(x.handbook.ID)
	base := f.env.URL

	r := f.dirk.MustDo(http.MethodGet, "/api/v1/wiki/pages/"+pid+"/export?format=pdf&paper=letter", nil, nil)
	if r.Header.Get("Content-Type") != "application/pdf" || !strings.Contains(r.Header.Get("Content-Disposition"), "handbook.pdf") ||
		!bytes.HasPrefix(r.Body, []byte("%PDF-")) {
		t.Fatalf("PDF: %v %.20s", r.Header, r.Body)
	}
	text, marks, pages := pdfText(t, r.Body)
	for _, want := range []string{"Handbook", "Setup", "Left column", "Right column", "Details", "graph TD",
		"Energy E=mc^2 holds.", `\int_0^1 x\,dx < 1`} {
		if !strings.Contains(text, want) {
			t.Errorf("the PDF lacks %q: %s", want, text)
		}
	}
	if pages < 1 || strings.Join(marks, "|") != "Handbook|Setup" {
		t.Errorf("pages %d, bookmarks %v", pages, marks)
	}
	if !bytes.Contains(r.Body, []byte("/Subtype /Image")) {
		t.Error("the PDF has no image")
	}
	if !bytes.Contains(r.Body, []byte("/URI ("+base+"/wiki/spaces/DOCS/pages/"+id(x.details.ID))) {
		t.Error("the link to the details does not lead to the instance")
	}
	// Pages become PDFs a few at a time; the others wait for their turn, and all get their PDF.
	n := runtime.GOMAXPROCS(0) + 2
	results := make(chan string, n)
	for range n {
		go func() {
			req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/wiki/pages/"+pid+"/export?format=pdf", nil)
			resp, err := f.dirk.Send(req)
			if err != nil {
				results <- err.Error()
				return
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(body, []byte("%PDF-")) {
				results <- fmt.Sprintf("%d %.100s", resp.StatusCode, body)
				return
			}
			results <- ""
		}()
	}
	for range n {
		if res := <-results; res != "" {
			t.Errorf("one of %d PDFs at once: %s", n, res)
		}
	}

	html := string(f.dirk.MustDo(http.MethodGet, "/api/v1/wiki/pages/"+pid+"/export?format=html", nil, nil).Body)
	for _, want := range []string{`<h1 class="page-title">Handbook</h1>`, `<a href="#h-setup" rel="noopener noreferrer">Setup</a>`,
		`<img src="data:image/png;base64,`, `<div class="columns"><div class="column" style="flex:1 1 0"><p>Left column</p>`,
		`href="` + base + `/wiki/spaces/DOCS/pages/` + id(x.details.ID), `<style>`, `language-mermaid`,
		// Formulas show their LaTeX: exports run no scripts.
		`<p>Energy <span class="math inline">\(E=mc^2\)</span> holds.</p>`, `<div class="math display">\[\int_0^1 x\,dx &lt; 1\]</div>`} {
		if !strings.Contains(html, want) {
			t.Errorf("the HTML lacks %s:\n%s", want, html)
		}
	}
	if strings.Contains(html, "Secret") || strings.Contains(html, "<script") {
		t.Errorf("the HTML shows what it must not:\n%s", html)
	}
	md := string(f.dirk.MustDo(http.MethodGet, "/api/v1/wiki/pages/"+pid+"/export?format=md", nil, nil).Body)
	for _, want := range []string{"# Handbook\n", "- [Setup](#h-setup)", "<!-- columns: left -->", "```mermaid",
		"[the details](" + base + "/wiki/spaces/DOCS/pages/" + id(x.details.ID) + ")", "![Diagram](" + base + "/api/v1/wiki/attachments/",
		"Energy $E=mc^2$ holds.", "$$\n\\int_0^1 x\\,dx < 1\n$$"} {
		if !strings.Contains(md, want) {
			t.Errorf("the Markdown lacks %q:\n%s", want, md)
		}
	}
	// Lena's children list names her restricted page; Dirk's does not.
	if !strings.Contains(string(f.lena.MustDo(http.MethodGet, "/api/v1/wiki/pages/"+pid+"/export?format=md", nil, nil).Body), "Secret plans") {
		t.Error("the space administrator's export lacks the restricted child")
	}

	// Where the instance and the space let anyone read and export, anonymous readers export, too.
	var st map[string]any
	f.admin.Get("/api/v1/admin/settings", &st)
	st["allowAnonymous"] = true
	f.admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	var docsGrants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/DOCS/permissions", &docsGrants)
	for _, perm := range []string{"space.view", "space.export"} {
		docsGrants = append(docsGrants, map[string]any{"permission": perm, "holder": map[string]any{"type": "anyone"}})
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", docsGrants, nil)
	if r := f.env.Anonymous().Do(http.MethodGet, "/api/v1/wiki/pages/"+id(x.details.ID)+"/export?format=pdf", nil); r.Status != http.StatusOK ||
		!bytes.HasPrefix(r.Body, []byte("%PDF-")) {
		t.Errorf("anonymous PDF export: %d %.200s", r.Status, r.Body)
	}

	// The restricted page does not exist for Dirk; without space.export there is nothing to export.
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(x.secret.ID)+"/export?format=pdf", nil); r.Status != http.StatusNotFound {
		t.Errorf("restricted page: %d", r.Status)
	}
	var sp space
	open := false
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "HR", "name": "People", "open": &open}, &sp)
	var grants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/HR/permissions", &grants)
	grants = append(grants, map[string]any{"permission": "space.view", "holder": map[string]any{"type": "user", "id": f.ids["otto"]}})
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/HR/permissions", grants, nil)
	if r := f.otto.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(sp.HomePageID)+"/export?format=pdf", nil); r.Status != http.StatusForbidden ||
		r.Code() != "space.export-denied" {
		t.Errorf("without space.export: %d %s", r.Status, r.Body)
	}
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(sp.HomePageID)+"/export?format=pdf", nil); r.Status != http.StatusNotFound {
		t.Errorf("outsider: %d", r.Status)
	}
	if r := f.otto.Do(http.MethodPost, "/api/v1/wiki/spaces/HR/exports", map[string]any{"format": "pdf"}); r.Status != http.StatusForbidden {
		t.Errorf("space export without space.export: %d %s", r.Status, r.Body)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces/HR/exports", map[string]any{"format": "pdf"}); r.Status != http.StatusNotFound {
		t.Errorf("space export by an outsider: %d %s", r.Status, r.Body)
	}
}

func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a ZIP: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func startExport(t *testing.T, c *apitest.Client, body map[string]any) exportInfo {
	t.Helper()
	var e exportInfo
	r := c.MustDo(http.MethodPost, "/api/v1/wiki/spaces/DOCS/exports", body, &e)
	if r.Status != http.StatusAccepted || e.State != "queued" {
		t.Fatalf("start: %d %+v", r.Status, e)
	}
	return e
}

func finished(t *testing.T, f *fixture, c *apitest.Client, e exportInfo) (exportInfo, []byte) {
	t.Helper()
	f.env.WorkJobs()
	c.Get("/api/v1/wiki/exports/"+id(e.ID), &e)
	if e.State != "done" || e.URL == "" || e.PagesDone != e.PagesTotal || e.PagesTotal == 0 {
		t.Fatalf("export: %+v", e)
	}
	return e, c.MustDo(http.MethodGet, e.URL, nil, nil).Body
}

// TestExportPageBusy: while every turn of rendering PDFs is taken longer than a request waits, it is answered that
// the server is busy (503, with when to try again), instead of waiting without end; then it gets its PDF.
func TestExportPageBusy(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Busy", f.space.HomePageID, "Hello")
	path := "/api/v1/wiki/pages/" + id(p.ID) + "/export?format=pdf"
	release := sync.OnceFunc(wiki.HoldPDFTurns(200 * time.Millisecond))
	defer release()
	start := time.Now()
	r := f.lena.Do(http.MethodGet, path, nil, "Accept-Language", "de")
	if r.Status != http.StatusServiceUnavailable || r.Code() != "export.pdf-busy" || r.Header.Get("Retry-After") != "30" ||
		!strings.Contains(string(r.Body), "Gerade werden viele Seiten zu PDFs") {
		t.Fatalf("busy: %d %v %s", r.Status, r.Header, r.Body)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond || waited > 10*time.Second {
		t.Errorf("waited %v for a turn", waited)
	}
	release()
	if r := f.lena.Do(http.MethodGet, path, nil); r.Status != http.StatusOK || !bytes.HasPrefix(r.Body, []byte("%PDF-")) {
		t.Errorf("after the turns came back: %d %.100s", r.Status, r.Body)
	}
}

func TestExportSpace(t *testing.T) {
	f := setup(t)
	x := exportFixture(t, f)
	// Exports speak the language of who exports: Dirk reads English, Lena the instance's German.
	f.dirk.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"locale": "en"}, nil)
	var home page
	f.dirk.Get("/api/v1/wiki/pages/"+id(f.space.HomePageID), &home)

	// An HTML site of the whole space, as Dirk sees it.
	e, data := finished(t, f, f.dirk, startExport(t, f.dirk, map[string]any{"format": "html"}))
	files := readZip(t, data)
	root := strings.TrimSuffix(e.Filename, ".zip") + "/"
	if !strings.HasPrefix(e.Filename, "DOCS-documentation-html-") {
		t.Errorf("file name: %s", e.Filename)
	}
	handbook := root + "pages/" + id(x.handbook.ID) + "-handbook.html"
	for _, name := range []string{root + "index.html", root + "style.css", handbook, root + "pages/" + id(x.details.ID) + "-details.html",
		root + "pages/" + id(f.space.HomePageID) + "-" + slugOf(home.Title) + ".html", root + "pages/" + id(x.blog.ID) + "-launch-news.html",
		root + "attachments/" + id(x.handbook.ID) + "/diagram.png", root + "attachments/" + id(x.handbook.ID) + "/notes.txt"} {
		if _, ok := files[name]; !ok {
			t.Errorf("the archive lacks %s: %v", name, fileNames(files))
		}
	}
	for name, content := range files {
		if strings.Contains(name, "secret") || strings.Contains(content, "Secret plans") || strings.Contains(content, "Only for Lena") {
			t.Errorf("the restricted page is in %s", name)
		}
	}
	page := files[handbook]
	for _, want := range []string{`href="` + id(x.details.ID) + `-details.html"`, `<img src="../attachments/` + id(x.handbook.ID) + `/diagram.png"`,
		`href="../style.css"`, `<a href="../index.html">Documentation</a>`, `href="../attachments/` + id(x.handbook.ID) + `/diagram.png"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s:\n%s", want, page)
		}
	}
	index := files[root+"index.html"]
	for _, want := range []string{"view restrictions", `href="pages/` + id(x.handbook.ID) + `-handbook.html">Handbook</a><ul><li>`,
		">Blog</h2>", "Launch news"} {
		if !strings.Contains(index, want) {
			t.Errorf("the index lacks %s:\n%s", want, index)
		}
	}
	if files[root+"attachments/"+id(x.handbook.ID)+"/diagram.png"] != string(pngData()) {
		t.Error("the attachment differs")
	}

	// Markdown of the handbook's tree, with front matter and relative links.
	_, data = finished(t, f, f.dirk, startExport(t, f.dirk, map[string]any{"format": "markdown", "pageId": x.handbook.ID}))
	files = readZip(t, data)
	var md, readme string
	for name, content := range files {
		switch {
		case strings.HasSuffix(name, "/pages/"+id(x.handbook.ID)+"-handbook.md"):
			md = content
		case strings.HasSuffix(name, "/README.md"):
			readme = content
		case strings.Contains(name, "/pages/") && !strings.Contains(name, "-details.md") && !strings.Contains(name, "-handbook.md"):
			t.Errorf("not part of the tree: %s", name)
		}
	}
	for _, want := range []string{"---\ntitle: \"Handbook\"\nid: " + id(x.handbook.ID) + "\nspace: \"DOCS\"\nparent: " + id(f.space.HomePageID),
		"# Handbook", "[the details](" + id(x.details.ID) + "-details.md)", "![Diagram](../attachments/" + id(x.handbook.ID) + "/diagram.png)"} {
		if !strings.Contains(md, want) {
			t.Errorf("the Markdown lacks %q:\n%s", want, md)
		}
	}
	if !strings.Contains(readme, "- [Handbook](pages/"+id(x.handbook.ID)+"-handbook.md)\n  - [Details](pages/"+id(x.details.ID)+"-details.md)") {
		t.Errorf("README:\n%s", readme)
	}

	// One PDF with contents, as Lena (who sees the restricted pages) exports it.
	e, data = finished(t, f, f.lena, startExport(t, f.lena, map[string]any{"format": "pdf", "pageId": x.handbook.ID}))
	text, marks, pages := pdfText(t, data)
	if e.PagesTotal != 4 || pages < 5 {
		t.Errorf("pages: %d of the tree, %d in the PDF", e.PagesTotal, pages)
	}
	for _, want := range []string{"Handbook", "Details for „Größen“ and 5 €", "Secret plans", "Secret child", "Inhalt", "Seite 1 von"} {
		if !strings.Contains(text, want) {
			t.Errorf("the PDF lacks %q", want)
		}
	}
	if got := strings.Join(marks, "|"); got != "Inhalt|Handbook|Setup|Details|Secret plans|Secret child" {
		t.Errorf("bookmarks: %s", got)
	}
}

// slugOf is the file name the export gives a title (as page addresses have it).
func slugOf(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == 'ä' || r == 'ö' || r == 'ü':
			b.WriteString(map[rune]string{'ä': "ae", 'ö': "oe", 'ü': "ue"}[r])
		case r == 'ß':
			b.WriteString("ss")
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func fileNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExportsBelongToTheirOwner(t *testing.T) {
	f := setup(t)
	exportFixture(t, f)
	e := startExport(t, f.dirk, map[string]any{"format": "html"})
	// Not ready yet.
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/exports/"+id(e.ID)+"/download", nil); r.Status != http.StatusConflict ||
		r.Code() != "export.not-ready" {
		t.Errorf("download before the export is done: %d %s", r.Status, r.Body)
	}
	// A few at a time.
	startExport(t, f.dirk, map[string]any{"format": "markdown"})
	startExport(t, f.dirk, map[string]any{"format": "pdf"})
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces/DOCS/exports", map[string]any{"format": "html"}); r.Status != http.StatusConflict ||
		r.Code() != "export.busy" {
		t.Errorf("a fourth export: %d %s", r.Status, r.Body)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces/DOCS/exports", map[string]any{"format": "docx"}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("unknown format: %d", r.Status)
	}
	e, _ = finished(t, f, f.dirk, e)
	var list []exportInfo
	f.dirk.Get("/api/v1/wiki/exports?space=DOCS", &list)
	if len(list) != 3 || list[2].ID != e.ID {
		t.Errorf("Dirk's exports: %+v", list)
	}
	// Others know nothing of them.
	f.otto.Get("/api/v1/wiki/exports", &list)
	if len(list) != 0 {
		t.Errorf("Otto's exports: %+v", list)
	}
	for _, r := range []*apitest.Response{
		f.otto.Do(http.MethodGet, "/api/v1/wiki/exports/"+id(e.ID), nil),
		f.otto.Do(http.MethodGet, e.URL, nil),
		f.otto.Do(http.MethodDelete, "/api/v1/wiki/exports/"+id(e.ID), nil),
		f.lena.Do(http.MethodGet, e.URL, nil),
	} {
		if r.Status != http.StatusNotFound || r.Code() != "export.not-found" {
			t.Errorf("another's export: %d %s", r.Status, r.Body)
		}
	}
	if r := f.env.Anonymous().Do(http.MethodGet, e.URL, nil); r.Status != http.StatusUnauthorized && r.Status != http.StatusNotFound {
		t.Errorf("anonymous: %d", r.Status)
	}

	// Who loses the permission to export the space no longer downloads what was exported.
	var grants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/DOCS/permissions", &grants)
	var kept []map[string]any
	for _, g := range grants {
		if g["permission"] != "space.export" {
			kept = append(kept, g)
		}
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", kept, nil)
	if r := f.dirk.Do(http.MethodGet, e.URL, nil); r.Status != http.StatusNotFound {
		t.Errorf("download without space.export: %d", r.Status)
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", grants, nil)
	f.dirk.MustDo(http.MethodGet, e.URL, nil, nil)
	// And an export that waits for its job fails when it runs.
	waiting := startExport(t, f.dirk, map[string]any{"format": "markdown"})
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", kept, nil)
	f.env.WorkJobs()
	var failed exportInfo
	f.dirk.Get("/api/v1/wiki/exports/"+id(waiting.ID), &failed)
	if failed.State != "failed" || failed.Error != "Sie dürfen Seiten dieses Bereichs nicht mehr exportieren." || failed.URL != "" {
		t.Errorf("export without permission: %+v", failed)
	}
}

func TestExportsExpireAndDelete(t *testing.T) {
	f := setup(t)
	exportFixture(t, f)
	a, _ := finished(t, f, f.dirk, startExport(t, f.dirk, map[string]any{"format": "html"}))
	b, _ := finished(t, f, f.dirk, startExport(t, f.dirk, map[string]any{"format": "markdown"}))
	ctx := context.Background()
	refs := func(exportID int64) int {
		var n int
		if err := f.env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM blob_refs WHERE owner_type = 'wiki-export' AND owner_id = $1",
			exportID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if refs(a.ID) != 1 || refs(b.ID) != 1 {
		t.Fatalf("blob references: %d %d", refs(a.ID), refs(b.ID))
	}
	// Deleting an export removes it and the reference to its file.
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/exports/"+id(a.ID), nil, nil)
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/exports/"+id(a.ID), nil); r.Status != http.StatusNotFound || refs(a.ID) != 0 {
		t.Errorf("deleted export: %d, %d references", r.Status, refs(a.ID))
	}
	// An expired export is gone at once for its owner, and the cleanup removes it.
	if _, err := f.env.App.Pool.Exec(ctx, "UPDATE wiki_exports SET expires_at = now() - interval '1 minute' WHERE id = $1", b.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.dirk.Do(http.MethodGet, b.URL, nil); r.Status != http.StatusNotFound {
		t.Errorf("expired export: %d", r.Status)
	}
	n, err := f.env.App.Wiki.PurgeExports(ctx)
	if err != nil || n != 1 || refs(b.ID) != 0 {
		t.Errorf("cleanup: %d %v, %d references", n, err, refs(b.ID))
	}
	var left int
	if err := f.env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM wiki_exports").Scan(&left); err != nil || left != 0 {
		t.Errorf("exports left: %d %v", left, err)
	}
	_ = time.Now
}
