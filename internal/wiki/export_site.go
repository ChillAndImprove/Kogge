// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"archive/zip"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"kogge.de/kogge/internal/docmodel"
)

// Archives of several pages: an HTML site — an index with the tree of pages, a file per page, the attachments and
// a stylesheet — or Markdown files with front matter, an index and the attachments. Pages link to each other and to
// their attachments by relative paths, to everything else by the instance's address. The HTML is the
// documents' own: escaped text and no scripts, as in exports of single pages.

//go:embed export.css
var exportCSS string

// exportTexts are the words of exports, by language.
var exportTexts = map[string]*exportLabels{
	"en": {
		meta:          "%s · last changed by %s on %s · version %d",
		footer:        "%s · exported on %s",
		exportedBy:    "Exported by %s on %s from %s (%s).",
		leftOut:       "Pages with view restrictions that exclude the exporter are not part of the export.",
		notAvailable:  "This content is not available.",
		noPages:       "No pages.",
		noAttachments: "No attachments.",
		noRuns:        "No runs yet.",
		queryFailed:   "The issues of %q could not be listed.",
		issueCount:    "%d issues",
		morePages:     "… and %d more pages",
		key:           "Key",
		summary:       "Summary",
		status:        "Status",
		assignee:      "Assignee",
		pages:         "Pages",
		blog:          "Blog",
		openInKogge:   "Open in Kogge",
		contents:      "Contents",
	},
	"de": {
		meta:          "%s · zuletzt geändert von %s am %s · Version %d",
		footer:        "%s · exportiert am %s",
		exportedBy:    "Exportiert von %s am %s aus %s (%s).",
		leftOut:       "Seiten mit Leseeinschränkungen, die die exportierende Person ausschließen, gehören nicht zum Export.",
		notAvailable:  "Dieser Inhalt ist nicht verfügbar.",
		noPages:       "Keine Seiten.",
		noAttachments: "Keine Anhänge.",
		noRuns:        "Noch keine Läufe.",
		queryFailed:   "Die Vorgänge von „%s“ ließen sich nicht auflisten.",
		issueCount:    "%d Vorgänge",
		morePages:     "… und %d weitere Seiten",
		key:           "Schlüssel",
		summary:       "Zusammenfassung",
		status:        "Status",
		assignee:      "Bearbeitung",
		pages:         "Seiten",
		blog:          "Blog",
		openInKogge:   "In Kogge öffnen",
		contents:      "Inhalt",
	},
}

type exportLabels struct {
	meta, footer, exportedBy, leftOut, notAvailable, noPages, noAttachments, noRuns, queryFailed, issueCount string
	morePages                                                                                                string
	key, summary, status, assignee, pages, blog, openInKogge, contents                                       string
}

var esc = html.EscapeString

// htmlPage writes a page of an HTML export: with the stylesheet of the archive (style), or with it inline.
func (x *exporter) htmlPage(pg *pageRow, d *docmodel.Doc, nav, style string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + x.lang + `"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + esc(pg.Title) + ` · ` + esc(x.sp.Name) + `</title>`)
	if style != "" {
		b.WriteString(`<link rel="stylesheet" href="` + esc(style) + `">`)
	} else {
		b.WriteString(`<style>` + exportCSS + `</style>`)
	}
	b.WriteString(`</head><body>`)
	if nav != "" {
		b.WriteString(`<header class="export-header">` + nav + `</header>`)
	}
	b.WriteString(`<main><h1 class="page-title">` + esc(pg.Title) + `</h1><p class="page-meta">` + esc(x.meta(pg)) + `</p>`)
	b.WriteString(docmodel.HTML(d, htmlLinks{x: x}))
	b.WriteString(`</main><footer class="export-footer">` + esc(fmt.Sprintf(x.l.footer, x.name, x.day(x.now))) + ` · <a href="` +
		esc(x.absolute(target{page: pg.ID})) + `">` + esc(x.l.openInKogge) + `</a></footer></body></html>`)
	return b.String()
}

// markdownPage writes a page of a Markdown export: front matter (in archives), its title and its content.
func (x *exporter) markdownPage(pg *pageRow, d *docmodel.Doc, front bool) (string, error) {
	var b strings.Builder
	if front {
		fields := [][2]any{{"title", pg.Title}, {"id", pg.ID}, {"space", x.sp.Key}}
		if pg.ParentID != 0 {
			fields = append(fields, [2]any{"parent", pg.ParentID})
		}
		if pg.Kind == "blog" {
			fields = append(fields, [2]any{"kind", "blog"})
		}
		fields = append(fields, [2]any{"version", pg.Version})
		if pg.UpdatedBy != 0 {
			users, err := identityRefs(x, pg.UpdatedBy)
			if err != nil {
				return "", err
			}
			fields = append(fields, [2]any{"author", users})
		}
		fields = append(fields, [2]any{"updated", pg.UpdatedAt.UTC().Format(time.RFC3339)})
		labels, err := x.labels(pg.ID)
		if err != nil {
			return "", err
		}
		if len(labels) > 0 {
			fields = append(fields, [2]any{"labels", labels})
		}
		fields = append(fields, [2]any{"url", x.absolute(target{page: pg.ID})})
		b.WriteString(docmodel.FrontMatter(fields) + "\n")
	}
	b.WriteString("# " + strings.TrimSpace(strings.ReplaceAll(pg.Title, "\n", " ")) + "\n\n")
	b.WriteString(docmodel.MarkdownWith(d, x.markdownLinks()))
	return b.String(), nil
}

func identityRefs(x *exporter, id int64) (string, error) {
	if n, ok := x.names[id]; ok {
		return n, nil
	}
	if err := x.loadNames(&docmodel.Doc{Content: []*docmodel.Node{{Type: "mention", Attrs: map[string]any{"user": id}}}}); err != nil {
		return "", err
	}
	return x.names[id], nil
}

func (x *exporter) labels(pageID int64) ([]string, error) {
	rows, err := x.s.pool.Query(x.ctx, "SELECT label FROM page_labels WHERE page_id = $1 ORDER BY label", pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// writeZip writes the archive of an HTML or Markdown export.
func (x *exporter) writeZip(w io.Writer, tree []*exportPage, rootID int64) error {
	zw := zip.NewWriter(w)
	root, _ := x.fileName(tree, rootID)
	root = strings.TrimSuffix(root, ".zip") + "/"
	add := func(name string, data string) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: root + name, Method: zip.Deflate, Modified: x.now})
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, data)
		return err
	}
	md := x.format == ExportMarkdown
	for i, ep := range tree {
		pg, d, err := x.page(ep.id)
		if err != nil {
			return err
		}
		// Every page's own attachments come along, whether the page shows them or not.
		list, err := x.s.Attachments(x.ctx, x.p, pg.ID, false)
		if err != nil {
			return err
		}
		for _, at := range list {
			if a := x.attachment(at.ID); a != nil {
				a.used = true
			}
		}
		if md {
			page, err := x.markdownPage(pg, d, true)
			if err != nil {
				return err
			}
			if err := add("pages/"+ep.file, page); err != nil {
				return err
			}
		} else if err := add("pages/"+ep.file, x.htmlPage(pg, d, x.breadcrumbs(ep), "../style.css")); err != nil {
			return err
		}
		if err := x.progress(i + 1); err != nil {
			return err
		}
	}
	if md {
		if err := add("README.md", x.markdownIndex(tree)); err != nil {
			return err
		}
	} else {
		if err := add("index.html", x.htmlIndex(tree)); err != nil {
			return err
		}
		if err := add("style.css", exportCSS); err != nil {
			return err
		}
	}
	// The attachments the pages have and show, in a folder per page.
	for _, id := range sortedKeys(x.attachments) {
		at := x.attachments[id]
		if at == nil || !at.used {
			continue
		}
		if err := x.addAttachment(zw, root, at); err != nil {
			return err
		}
	}
	return zw.Close()
}

func sortedKeys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Files that are compressed already are stored as they are.
var storedTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/zip", "application/gzip",
	"application/pdf", "video/", "audio/"}

func (x *exporter) addAttachment(zw *zip.Writer, root string, at *exportAttachment) error {
	method := zip.Deflate
	for _, t := range storedTypes {
		if strings.HasPrefix(at.contentType, t) {
			method = zip.Store
		}
	}
	f, err := x.s.blobs.Open(at.sha)
	if errors.Is(err, fs.ErrNotExist) {
		// A file lost from the data directory (fsck reports it) leaves a gap, not a failed export.
		x.s.log.Warn("an attachment of an export has no file", "attachment", at.id, "sha256", at.sha)
		return nil
	}
	if err != nil {
		return fmt.Errorf("attachment %d: %w", at.id, err)
	}
	defer f.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: root + at.archivePath(), Method: method, Modified: x.now})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// breadcrumbs lead from a page of an HTML archive to the index and the page's ancestors in the export.
func (x *exporter) breadcrumbs(ep *exportPage) string {
	var chain []*exportPage
	for id := ep.parent; id != 0; {
		p, ok := x.pages[id]
		if !ok {
			break
		}
		chain = append([]*exportPage{p}, chain...)
		id = p.parent
	}
	b := `<a href="../index.html">` + esc(x.sp.Name) + `</a>`
	for _, p := range chain {
		b += ` › <a href="` + esc(url.PathEscape(p.file)) + `">` + esc(p.title) + `</a>`
	}
	return b
}

func (x *exporter) htmlIndex(tree []*exportPage) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + x.lang + `"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + esc(x.sp.Name) + `</title><link rel="stylesheet" href="style.css"></head><body><main>`)
	b.WriteString(`<h1 class="page-title">` + esc(x.sp.Name) + `</h1>`)
	for _, line := range x.intro() {
		b.WriteString(`<p class="page-meta">` + esc(line) + `</p>`)
	}
	var pages, blog []*exportPage
	for _, ep := range tree {
		if ep.blog {
			blog = append(blog, ep)
		} else {
			pages = append(pages, ep)
		}
	}
	item := func(ep *exportPage) string {
		return `<a href="pages/` + esc(url.PathEscape(ep.file)) + `">` + esc(ep.title) + `</a>`
	}
	b.WriteString(`<nav class="tree"><h2>` + esc(x.l.pages) + `</h2>` + treeList(pages, item))
	if len(blog) > 0 {
		b.WriteString(`<h2>` + esc(x.l.blog) + `</h2>` + treeList(blog, item))
	}
	b.WriteString(`</nav></main><footer class="export-footer">` + esc(fmt.Sprintf(x.l.footer, x.name, x.day(x.now))) +
		`</footer></body></html>`)
	return b.String()
}

// treeList writes pages in the order of a tree as nested lists (a page's depth is at most one more than the
// depth of the page before it).
func treeList(pages []*exportPage, item func(*exportPage) string) string {
	var b strings.Builder
	depth := -1
	for _, ep := range pages {
		d := ep.depth
		switch {
		case d > depth:
			b.WriteString("<ul>")
		case d == depth:
			b.WriteString("</li>")
		default:
			b.WriteString("</li>")
			for ; depth > d; depth-- {
				b.WriteString("</ul></li>")
			}
		}
		depth = d
		b.WriteString("<li>" + item(ep))
	}
	if depth >= 0 {
		b.WriteString("</li>")
		for ; depth > 0; depth-- {
			b.WriteString("</ul></li>")
		}
		b.WriteString("</ul>")
	}
	return b.String()
}

func (x *exporter) markdownIndex(tree []*exportPage) string {
	var b strings.Builder
	b.WriteString("# " + x.sp.Name + "\n\n")
	for _, line := range x.intro() {
		b.WriteString(line + "\n\n")
	}
	b.WriteString("## " + x.l.pages + "\n\n")
	inBlog := false
	for _, ep := range tree {
		if ep.blog && !inBlog {
			b.WriteString("\n## " + x.l.blog + "\n\n")
			inBlog = true
		}
		link := "[" + strings.NewReplacer("[", `\[`, "]", `\]`).Replace(ep.title) + "](pages/" + url.PathEscape(ep.file) + ")"
		b.WriteString(strings.Repeat("  ", ep.depth) + "- " + link + "\n")
	}
	return b.String()
}
