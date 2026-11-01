// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/docmodel/pdf"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/version"
)

// exporter renders pages for one reader: the reader's permissions decide what macros show, which images and
// attachments are part of the export and where links lead.
type exporter struct {
	s      *Service
	ctx    context.Context
	p      *identity.Principal
	a      *Access
	sp     *Space
	format string
	l      *exportLabels
	lang   string
	loc    *time.Location
	base   string // the instance's address
	name   string // the instance's name
	now    time.Time

	// pages of the export by id (with their files, in exports of several pages).
	pages map[int64]*exportPage
	// attachments the export refers to, by id (nil: not readable); included ones are written to the archive.
	attachments map[int64]*exportAttachment
	names       map[int64]string
	issues      map[string]string
	// single exports of a page embed their images (HTML) and link everything else to the instance.
	single bool
	// progress is told how many pages are done.
	progress func(done int) error
}

type exportPage struct {
	id     int64
	title  string
	depth  int
	blog   bool
	parent int64
	file   string // the page's file in the archive, below pages/
}

type exportAttachment struct {
	id          int64
	pageID      int64
	filename    string
	version     int
	latest      bool
	contentType string
	sha         string
	size        int64
	used        bool // written to the archive
}

func (s *Service) newExporter(ctx context.Context, p *identity.Principal, a *Access, sp *Space, format string) (*exporter, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	lang, loc := s.exportLanguage(ctx, p)
	x := &exporter{s: s, ctx: ctx, p: p, a: a, sp: sp, format: format, lang: lang, loc: loc, base: s.publicBase(),
		name: st.Name, now: now(), pages: map[int64]*exportPage{}, attachments: map[int64]*exportAttachment{},
		names: map[int64]string{}, issues: map[string]string{}, progress: func(int) error { return nil }}
	x.l = exportTexts[lang]
	return x, nil
}

// collect returns the pages of a tree (a page and the pages below it, or the whole space with its blog) that the
// reader may see, in the order of the tree.
func (x *exporter) collect(rootID int64) ([]*exportPage, error) {
	var args db.Args
	visible := x.a.VisibleSQL("p", &args)
	// Pages in the order of the tree; blog posts newest first.
	rows, err := x.s.pool.Query(x.ctx, `SELECT p.id, coalesce(p.parent_id, 0), p.title, p.kind FROM pages p
		WHERE p.space_id = `+args.Add(x.sp.ID)+` AND p.status = 'current' AND `+visible+`
		ORDER BY p.kind DESC, p.rank, coalesce(p.published_at, p.created_at) DESC, p.id`, args...)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, parent int64
		title      string
		blog       bool
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		var kind string
		err := r.Scan(&v.id, &v.parent, &v.title, &kind)
		v.blog = kind == "blog"
		return v, err
	})
	if err != nil {
		return nil, err
	}
	children := map[int64][]row{}
	byID := map[int64]row{}
	var blog []row
	for _, r := range all {
		byID[r.id] = r
		if r.blog {
			blog = append(blog, r)
			continue
		}
		children[r.parent] = append(children[r.parent], r)
	}
	var out []*exportPage
	var walk func(r row, depth int)
	walk = func(r row, depth int) {
		if depth > 50 {
			return
		}
		out = append(out, &exportPage{id: r.id, title: r.title, depth: depth, parent: r.parent})
		for _, c := range children[r.id] {
			walk(c, depth+1)
		}
	}
	if rootID != 0 {
		r, ok := byID[rootID]
		if !ok || r.blog {
			return nil, failure(apperr.Invalidf("export.page-gone", "You may no longer see the page whose tree was to be exported."))
		}
		walk(r, 0)
	} else {
		// The top level: pages without a parent (the home page among them).
		for _, r := range children[0] {
			walk(r, 0)
		}
		for _, b := range blog {
			out = append(out, &exportPage{id: b.id, title: b.title, blog: true})
		}
	}
	ext := ".html"
	if x.format == ExportMarkdown {
		ext = ".md"
	}
	for _, pg := range out {
		name := slug(pg.title)
		if name == "" {
			name = "page"
		}
		pg.file = itoa(pg.id) + "-" + name + ext
		x.pages[pg.id] = pg
	}
	return out, nil
}

// fileName names the file of an export: the space (and page), the format and the day.
func (x *exporter) fileName(tree []*exportPage, rootID int64) (string, string) {
	base := x.sp.Key
	if rootID != 0 && len(tree) > 0 {
		if s := slug(tree[0].title); s != "" {
			base += "-" + s
		}
	} else if s := slug(x.sp.Name); s != "" {
		base += "-" + s
	}
	day := x.now.In(x.loc).Format("20060102")
	switch x.format {
	case ExportPDF:
		return base + "-" + day + ".pdf", "application/pdf"
	case ExportMarkdown:
		return base + "-markdown-" + day + ".zip", "application/zip"
	}
	return base + "-html-" + day + ".zip", "application/zip"
}

// page loads a page of the export for rendering, with its macros' output in place.
func (x *exporter) page(id int64) (*pageRow, *docmodel.Doc, error) {
	pg, err := loadPage(x.ctx, x.s.pool, id, false)
	if err != nil {
		return nil, nil, err
	}
	d := &docmodel.Doc{Type: "doc", Version: pg.Body.Version, Content: docmodel.LiftColumns(x.expand(pg, pg.Body.Content, 0))}
	if err := x.loadNames(d); err != nil {
		return nil, nil, err
	}
	return pg, d, nil
}

func (x *exporter) loadNames(d *docmodel.Doc) error {
	refs := docmodel.References(d)
	var missing []int64
	for _, id := range refs.Users {
		if _, ok := x.names[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	users, err := identity.GetUserRefs(x.ctx, x.s.pool, missing)
	if err != nil {
		return err
	}
	for id, u := range users {
		x.names[id] = u.DisplayName
	}
	return nil
}

func (x *exporter) userName(id int64, label string) string {
	if n, ok := x.names[id]; ok && n != "" {
		return n
	}
	return label
}

// day formats a date in the reader's language and time zone.
func (x *exporter) day(t time.Time) string {
	t = t.In(x.loc)
	if x.lang == "de" {
		return t.Format("02.01.2006")
	}
	return t.Format("2 January 2006")
}

func (x *exporter) isoDay(v string) string {
	if d, err := time.Parse("2006-01-02", v); err == nil {
		if x.lang == "de" {
			return d.Format("02.01.2006")
		}
		return d.Format("2 January 2006")
	}
	return v
}

// meta is the line below a page's title: its space, who changed it last and when, its version.
func (x *exporter) meta(pg *pageRow) string {
	who := ""
	if pg.UpdatedBy != 0 {
		if users, err := identity.GetUserRefs(x.ctx, x.s.pool, []int64{pg.UpdatedBy}); err == nil {
			if u, ok := users[pg.UpdatedBy]; ok {
				who = u.DisplayName
			}
		}
	}
	return fmt.Sprintf(x.l.meta, x.sp.Name, who, x.day(pg.UpdatedAt), pg.Version)
}

// --- where links and images lead ---

var (
	pageLinkPattern       = regexp.MustCompile(`^/wiki/spaces/[^/?#]+/pages/([0-9]+)(?:/[^?#]*)?(?:\?[^#]*)?(?:#(.*))?$`)
	attachmentLinkPattern = regexp.MustCompile(`^/api/v1/wiki/attachments/([0-9]+)(?:\?[^#]*)?$`)
)

// target is where a link of a document leads.
type target struct {
	local      string // an anchor of the same page (without "#")
	page       int64
	anchor     string
	attachment int64
	url        string // an address elsewhere, absolute
}

func (x *exporter) parseLink(href string) target {
	switch {
	case strings.HasPrefix(href, "#"):
		return target{local: href[1:]}
	case strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"):
		if m := pageLinkPattern.FindStringSubmatch(href); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			anchor, _ := url.PathUnescape(m[2])
			return target{page: id, anchor: anchor}
		}
		if m := attachmentLinkPattern.FindStringSubmatch(href); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			return target{attachment: id}
		}
		return target{url: x.base + href}
	}
	return target{url: href}
}

// absolute returns the address of a target on the instance.
func (x *exporter) absolute(t target) string {
	switch {
	case t.page != 0:
		u := x.base + "/wiki/spaces/" + x.sp.Key + "/pages/" + itoa(t.page)
		if t.anchor != "" {
			u += "#" + url.PathEscape(t.anchor)
		}
		return u
	case t.attachment != 0:
		return x.base + attachmentURL(t.attachment)
	}
	return t.url
}

// attachment returns an attachment the reader may see, or nil.
func (x *exporter) attachment(id int64) *exportAttachment {
	if at, ok := x.attachments[id]; ok {
		return at
	}
	x.attachments[id] = nil
	var at exportAttachment
	err := x.s.pool.QueryRow(x.ctx, `SELECT a.id, a.page_id, a.filename, a.version, a.content_type, a.sha256, a.size,
		a.version = (SELECT max(b.version) FROM page_attachments b WHERE b.page_id = a.page_id AND b.filename = a.filename)
		FROM page_attachments a WHERE a.id = $1`, id).Scan(&at.id, &at.pageID, &at.filename, &at.version, &at.contentType, &at.sha,
		&at.size, &at.latest)
	if err != nil {
		return nil
	}
	if _, err := x.s.readablePage(x.ctx, x.s.pool, x.a, at.pageID, false); err != nil {
		return nil
	}
	x.attachments[id] = &at
	return &at
}

// archivePath is where an attachment is in an archive, relative to the archive's root.
func (at *exportAttachment) archivePath() string {
	name := safeFileName(at.filename)
	if !at.latest {
		name = "v" + strconv.Itoa(at.version) + "-" + name
	}
	return "attachments/" + itoa(at.pageID) + "/" + name
}

// safeFileName keeps a file name within its directory of an archive.
func safeFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < 0x20 || r == 0x7f:
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "file"
	}
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

// archiveHref returns a link of a page of an archive: relative within the archive, absolute elsewhere.
func (x *exporter) archiveHref(href string) string {
	t := x.parseLink(href)
	switch {
	case t.local != "":
		return href
	case t.page != 0:
		if pg, ok := x.pages[t.page]; ok {
			if t.anchor != "" {
				return pg.file + "#" + url.PathEscape(t.anchor)
			}
			return pg.file
		}
	case t.attachment != 0:
		if at := x.attachment(t.attachment); at != nil {
			at.used = true
			return "../" + escapePath(at.archivePath())
		}
	}
	return x.absolute(t)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// imageAttachment returns the attachment an image shows: its attachment, or one its source names.
func (x *exporter) imageAttachment(id int64, src string) *exportAttachment {
	if id == 0 {
		if t := x.parseLink(src); t.attachment != 0 {
			id = t.attachment
		}
	}
	if id == 0 {
		return nil
	}
	return x.attachment(id)
}

// --- HTML and Markdown rendering ---

// htmlLinks rewrites the HTML of a page for its export.
type htmlLinks struct {
	x *exporter
}

func (h htmlLinks) UserName(id int64) string      { return h.x.userName(id, "") }
func (h htmlLinks) IssueURL(key string) string    { return h.x.issueURL(key) }
func (h htmlLinks) AttachmentURL(id int64) string { return attachmentURL(id) }

func (h htmlLinks) Href(href string) string {
	if h.x.single {
		if t := h.x.parseLink(href); t.local == "" {
			return h.x.absolute(t)
		}
		return href
	}
	return h.x.archiveHref(href)
}

// maxEmbedded is the largest image a single exported HTML page carries itself.
const maxEmbedded = 8 << 20

func (h htmlLinks) ImageSource(id int64, src string) string {
	x := h.x
	at := x.imageAttachment(id, src)
	switch {
	case at != nil && x.single:
		if at.size <= maxEmbedded && strings.HasPrefix(at.contentType, "image/") && !strings.Contains(at.contentType, "svg") {
			if data, err := x.readBlob(at.sha, maxEmbedded); err == nil {
				return "data:" + at.contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
		}
		return x.absolute(target{attachment: at.id})
	case at != nil:
		at.used = true
		return "../" + escapePath(at.archivePath())
	case id != 0:
		return "" // an attachment the reader may not see
	}
	// Other images: of the instance (absolute), or from elsewhere where the administration allows it.
	return x.absolute(x.parseLink(src))
}

func (x *exporter) readBlob(sha string, limit int64) ([]byte, error) {
	f, err := x.s.blobs.Open(sha)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("blob %s is larger than %d bytes", sha, limit)
	}
	return data, err
}

func (x *exporter) markdownLinks() docmodel.MarkdownLinks {
	h := htmlLinks{x: x}
	return docmodel.MarkdownLinks{
		Href: h.Href,
		Image: func(id int64, src string) string {
			if x.single {
				at := x.imageAttachment(id, src)
				if at != nil {
					return x.absolute(target{attachment: at.id})
				}
				if id != 0 {
					return ""
				}
				return x.absolute(x.parseLink(src))
			}
			return h.ImageSource(id, src)
		},
		User: x.userName,
	}
}

// --- PDF ---

type pdfResources struct {
	x *exporter
}

func (r pdfResources) Image(id int64, src string) *pdf.Image {
	at := r.x.imageAttachment(id, src)
	if at == nil || at.size > 25<<20 {
		return nil
	}
	data, err := r.x.readBlob(at.sha, 25<<20)
	if err != nil {
		return nil
	}
	return &pdf.Image{Data: data, Type: at.contentType, Name: at.filename, URL: r.x.absolute(target{attachment: at.id})}
}

func (r pdfResources) Link(href string) pdf.Target {
	t := r.x.parseLink(href)
	if t.page != 0 {
		if _, ok := r.x.pages[t.page]; ok && !r.x.single {
			return pdf.Target{Section: sectionID(t.page), Anchor: t.anchor}
		}
	}
	return pdf.Target{URL: r.x.absolute(t)}
}

func (r pdfResources) UserName(id int64, label string) string { return r.x.userName(id, label) }

func (r pdfResources) Issue(key string) string {
	x := r.x
	if s, ok := x.issues[key]; ok {
		return s
	}
	summary := ""
	if x.s.macros != nil {
		if is, err := x.s.macros.Issue(x.ctx, x.p, key); err == nil && is != nil {
			summary = is.Summary
		}
	}
	x.issues[key] = summary
	return summary
}

func sectionID(page int64) string { return "page-" + itoa(page) }

func (x *exporter) pdfOptions(paper, title, header string, contents bool, intro []string) pdf.Options {
	labels := pdf.EnglishLabels
	if x.lang == "de" {
		labels = pdf.Labels{Page: "Seite %d von %d", Contents: "Inhalt", Diagram: "Diagramm (Mermaid)",
			DiagramNote: "Kogge zeichnet das Diagramm, wenn die Seite angesehen wird; das PDF zeigt seinen Quelltext.",
			Formula:     "Formel (LaTeX)", SVG: "Bild %s (SVG) – in Kogge öffnen"}
	}
	labels.Date = x.isoDay
	if paper != "letter" {
		paper = pdf.A4
	} else {
		paper = pdf.Letter
	}
	author := ""
	if x.p.User != nil {
		author = x.p.User.DisplayName // anonymous readers export public pages, too
	}
	return pdf.Options{Paper: paper, Title: title, Author: author, Creator: "Kogge " + version.Version,
		Header: header, Footer: fmt.Sprintf(x.l.footer, x.name, x.day(x.now)), Contents: contents, Intro: intro,
		Language: x.lang, Created: x.now, Labels: labels}
}

// writePDF writes the pages of a tree as one PDF with a table of contents.
func (x *exporter) writePDF(w io.Writer, tree []*exportPage, paper string, rootID int64) error {
	var sections []pdf.Section
	for i, ep := range tree {
		pg, d, err := x.page(ep.id)
		if err != nil {
			return err
		}
		level := ep.depth
		if ep.blog {
			level = 0
		}
		sections = append(sections, pdf.Section{ID: sectionID(pg.ID), Title: pg.Title, Meta: x.meta(pg), Level: level, Doc: d})
		if err := x.progress(i + 1); err != nil {
			return err
		}
	}
	title := x.sp.Name
	if rootID != 0 && len(tree) > 0 {
		title = tree[0].title
	}
	o := x.pdfOptions(paper, title, x.sp.Name, true, x.intro())
	return pdf.Write(w, sections, pdfResources{x: x}, o)
}

// intro is what an export of several pages says about itself.
func (x *exporter) intro() []string {
	who := ""
	if x.p.User != nil {
		who = x.p.User.DisplayName
	}
	return []string{fmt.Sprintf(x.l.exportedBy, who, x.day(x.now), x.name, x.base), x.l.leftOut}
}
