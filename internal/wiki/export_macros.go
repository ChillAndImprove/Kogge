// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
)

// Macros in exports: readers see their output, written into the document — a table of contents, lists of pages
// and attachments, included pages, issues, code and the status of builds — with the permissions of the person who
// exports. What that person may not see is left out (or named as not available), as on the page.

// MacroSource supplies the data of macros that show other products, with a reader's permissions, for the output
// of macros that the wiki writes itself (exports). The application connects it to issues, code and builds.
type MacroSource interface {
	// Issue returns an issue, or nil if it does not exist or the reader may not see it.
	Issue(ctx context.Context, p *identity.Principal, key string) (*MacroIssue, error)
	// Issues returns the first issues of a query (KQL) and how many it finds.
	Issues(ctx context.Context, p *identity.Principal, query string, limit int) ([]MacroIssue, int, error)
	// File returns a file of a repository ("KEY/slug") at a ref; nil if the reader may not see it.
	File(ctx context.Context, p *identity.Principal, repository, ref, path string) (*MacroFile, error)
	// Readme returns the README of a repository at a ref; nil if there is none the reader may see.
	Readme(ctx context.Context, p *identity.Principal, repository, ref string) (*MacroFile, error)
	// LatestRun returns the latest run of a repository's pipeline (on a branch); nil if there is none.
	LatestRun(ctx context.Context, p *identity.Principal, repository, pipeline, branch string) (*MacroRun, error)
}

// MacroIssue is an issue in the output of macros.
type MacroIssue struct {
	Key            string
	Summary        string
	Status         string
	StatusCategory string // todo, inprogress, done
	Assignee       string
}

// MacroFile is a file of a repository: its lines as text, or Markdown as a document.
type MacroFile struct {
	Path     string
	Ref      string
	Language string
	Lines    []string
	Markdown *docmodel.Doc
	URL      string // where it is in Kogge
}

// MacroRun is a run of a pipeline.
type MacroRun struct {
	Name    string
	Number  int
	State   string
	Branch  string
	Created time.Time
	URL     string
}

// SetMacroSource connects the data of macros that show other products.
func (s *Service) SetMacroSource(m MacroSource) { s.macros = m }

func text(s string, marks ...*docmodel.Mark) *docmodel.Node {
	return &docmodel.Node{Type: "text", Text: s, Marks: marks}
}

func linkMark(href string) *docmodel.Mark {
	return &docmodel.Mark{Type: "link", Attrs: map[string]any{"href": href}}
}

func paragraph(content ...*docmodel.Node) *docmodel.Node {
	var nonEmpty []*docmodel.Node
	for _, c := range content {
		if c != nil && (c.Type != "text" || c.Text != "") {
			nonEmpty = append(nonEmpty, c)
		}
	}
	return &docmodel.Node{Type: "paragraph", Content: nonEmpty}
}

// list builds a bullet list of items, each a paragraph with nested items below it.
type listItem struct {
	content []*docmodel.Node
	items   []listItem
}

func bulletList(items []listItem) *docmodel.Node {
	l := &docmodel.Node{Type: "bulletList"}
	for _, it := range items {
		li := &docmodel.Node{Type: "listItem", Content: []*docmodel.Node{paragraph(it.content...)}}
		if len(it.items) > 0 {
			li.Content = append(li.Content, bulletList(it.items))
		}
		l.Content = append(l.Content, li)
	}
	return l
}

// expand returns blocks with the output of their macros in their place; depth counts included pages.
func (x *exporter) expand(pg *pageRow, nodes []*docmodel.Node, depth int) []*docmodel.Node {
	var out []*docmodel.Node
	for _, n := range nodes {
		switch n.Type {
		case "macro":
			out = append(out, x.macro(pg, n, depth)...)
		case "inlineMacro":
			out = append(out, text("["+strAttr(n.Attrs, "name")+"]"))
		default:
			c := *n
			c.Content = x.expand(pg, n.Content, depth)
			out = append(out, &c)
		}
	}
	return out
}

func strAttr(attrs map[string]any, k string) string {
	s, _ := attrs[k].(string)
	return s
}

func paramString(params map[string]any, k string) string {
	switch v := params[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func paramInt(params map[string]any, k string, def int64) int64 {
	switch v := params[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return i
		}
	}
	return def
}

func (x *exporter) notAvailable() []*docmodel.Node {
	return []*docmodel.Node{paragraph(text(x.l.notAvailable, &docmodel.Mark{Type: "italic"}))}
}

func (x *exporter) macro(pg *pageRow, n *docmodel.Node, depth int) []*docmodel.Node {
	params, _ := n.Attrs["params"].(map[string]any)
	name := strAttr(n.Attrs, "name")
	ctx := x.ctx
	switch name {
	case "toc":
		return x.toc(pg, int(paramInt(params, "maxLevel", 3)))
	case "children":
		return x.pageList(pg, pg.ID, int(paramInt(params, "depth", 1)))
	case "page-tree":
		return x.pageList(pg, paramInt(params, "root", pg.ID), 5)
	case "include":
		if depth >= 3 {
			return x.notAvailable()
		}
		inc, err := x.s.readablePage(ctx, x.s.pool, x.a, paramInt(params, "page", 0), false)
		if err != nil || inc.Status != StatusCurrent {
			return x.notAvailable()
		}
		return x.expand(inc, inc.Body.Content, depth+1)
	case "excerpt":
		return x.expand(pg, n.Content, depth)
	case "excerpt-include":
		inc, err := x.s.readablePage(ctx, x.s.pool, x.a, paramInt(params, "page", 0), false)
		if err != nil || inc.Status != StatusCurrent || depth >= 3 {
			return x.notAvailable()
		}
		var excerpt []*docmodel.Node
		found := false
		docmodel.Walk(inc.Body, func(m *docmodel.Node) bool {
			if !found && m.Type == "macro" && strAttr(m.Attrs, "name") == "excerpt" {
				excerpt, found = m.Content, true
			}
			return !found
		})
		out := x.expand(inc, excerpt, depth+1)
		sp, err := spaceByID(ctx, x.s.pool, inc.SpaceID)
		if err != nil {
			return out
		}
		return append(out, paragraph(text(inc.Title, linkMark(pageURL(sp.Key, inc.ID, inc.Title)))))
	case "recently-updated", "content-by-label":
		limit := int(paramInt(params, "max", 10))
		var list []PageSummary
		var err error
		if name == "recently-updated" {
			list, err = x.s.Recent(ctx, x.p, "updated", paramString(params, "space"), limit)
		} else {
			var res *SearchResult
			res, err = x.s.Search(ctx, x.p, SearchInput{Label: paramString(params, "label"), Space: paramString(params, "space"),
				Limit: int(paramInt(params, "max", 20))})
			if res != nil {
				list = res.Pages
			}
		}
		if err != nil || len(list) == 0 {
			return []*docmodel.Node{paragraph(text(x.l.noPages, &docmodel.Mark{Type: "italic"}))}
		}
		var items []listItem
		for _, ps := range list {
			items = append(items, listItem{content: []*docmodel.Node{text(ps.Title, linkMark(ps.URL)),
				text(" · " + ps.Space.Name + " · " + x.day(ps.Updated))}})
		}
		return []*docmodel.Node{bulletList(items)}
	case "attachments":
		list, err := x.s.Attachments(ctx, x.p, pg.ID, false)
		if err != nil || len(list) == 0 {
			return []*docmodel.Node{paragraph(text(x.l.noAttachments, &docmodel.Mark{Type: "italic"}))}
		}
		var items []listItem
		for _, at := range list {
			items = append(items, listItem{content: []*docmodel.Node{text(at.Filename, linkMark(at.URL))}})
		}
		return []*docmodel.Node{bulletList(items)}
	case "issue":
		key := paramString(params, "key")
		if !docmodel.IsIssueKey(key) {
			return x.notAvailable()
		}
		return []*docmodel.Node{x.issueLine(key)}
	case "issue-list", "issue-count":
		return x.issueList(paramString(params, "query"), int(paramInt(params, "max", 20)), name == "issue-count")
	case "anchor":
		return nil
	case "build-status":
		return x.buildStatus(paramString(params, "repository"), paramString(params, "pipeline"), paramString(params, "branch"))
	case "code-snippet":
		return x.codeSnippet(paramString(params, "repository"), paramString(params, "ref"), paramString(params, "path"),
			paramString(params, "lines"))
	case "readme":
		return x.readme(paramString(params, "repository"), paramString(params, "ref"))
	case "mermaid":
		code := paramString(params, "code")
		block := &docmodel.Node{Type: "codeBlock", Attrs: map[string]any{"language": "mermaid"}}
		if code != "" {
			block.Content = []*docmodel.Node{text(code)}
		}
		return []*docmodel.Node{block}
	}
	return []*docmodel.Node{paragraph(text("["+name+"]", &docmodel.Mark{Type: "italic"}))}
}

// toc lists the headings of a page up to a level, nested by their levels, linked to them.
func (x *exporter) toc(pg *pageRow, maxLevel int) []*docmodel.Node {
	type entry struct {
		level int
		node  *docmodel.Node
	}
	var heads []entry
	for _, n := range pg.Body.Content {
		if n.Type == "heading" {
			if lvl := int(intOf(n.Attrs["level"])); lvl >= 1 && lvl <= maxLevel {
				heads = append(heads, entry{lvl, n})
			}
		}
	}
	if len(heads) == 0 {
		return nil
	}
	var build func(i int, level int) ([]listItem, int)
	build = func(i int, level int) ([]listItem, int) {
		var items []listItem
		for i < len(heads) && heads[i].level >= level {
			if heads[i].level > level && len(items) > 0 {
				var sub []listItem
				sub, i = build(i, heads[i].level)
				items[len(items)-1].items = append(items[len(items)-1].items, sub...)
				continue
			}
			h := heads[i]
			items = append(items, listItem{content: []*docmodel.Node{text(docmodel.NodeText(h.node), linkMark("#"+docmodel.HeadingID(h.node)))}})
			i++
		}
		return items, i
	}
	min := heads[0].level
	for _, h := range heads {
		min = minInt(min, h.level)
	}
	items, _ := build(0, min)
	return []*docmodel.Node{bulletList(items)}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func intOf(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// pageList lists the pages below a page that the reader may see, to a depth.
func (x *exporter) pageList(pg *pageRow, parent int64, depth int) []*docmodel.Node {
	sp, err := spaceByID(x.ctx, x.s.pool, pg.SpaceID)
	if err != nil {
		return nil
	}
	// The children of a page, read a page of the tree at a time; below a page with very many children, the list
	// shows the first ones and says how many more there are.
	const treePage, maxChildren = 500, 1000
	var walk func(parent int64, depth int) []listItem
	walk = func(parent int64, depth int) []listItem {
		var items []listItem
		for offset := 0; offset < maxChildren; offset += treePage {
			children, err := x.s.Tree(x.ctx, x.p, sp.Key, parent, treePage, offset)
			if err != nil {
				return items
			}
			for _, c := range children.Items {
				it := listItem{content: []*docmodel.Node{text(c.Title, linkMark(pageURL(sp.Key, c.ID, c.Title)))}}
				if depth > 1 && c.HasChildren {
					it.items = walk(c.ID, depth-1)
				}
				items = append(items, it)
			}
			if more := children.Total - offset - len(children.Items); more <= 0 || len(children.Items) == 0 {
				return items
			} else if offset+treePage >= maxChildren {
				items = append(items, listItem{content: []*docmodel.Node{text(fmt.Sprintf(x.l.morePages, more))}})
			}
		}
		return items
	}
	items := walk(parent, max(1, min(depth, 10)))
	if len(items) == 0 {
		return nil
	}
	return []*docmodel.Node{bulletList(items)}
}

func (x *exporter) issueURL(key string) string { return "/browse/" + key }

// issueLine shows an issue with its summary and status.
func (x *exporter) issueLine(key string) *docmodel.Node {
	issue := &docmodel.Node{Type: "issue", Attrs: map[string]any{"key": key}}
	if x.s.macros == nil {
		return paragraph(issue)
	}
	is, err := x.s.macros.Issue(x.ctx, x.p, key)
	if err != nil || is == nil {
		return paragraph(issue)
	}
	return paragraph(text(is.Key, linkMark(x.issueURL(is.Key))), text(" "+is.Summary+" "), x.status(is.Status, is.StatusCategory))
}

func (x *exporter) status(name, category string) *docmodel.Node {
	color := map[string]string{"done": "green", "inprogress": "blue"}[category]
	if color == "" {
		color = "gray"
	}
	return &docmodel.Node{Type: "status", Attrs: map[string]any{"text": name, "color": color}}
}

// issueList shows the issues of a query as a table, or their number.
func (x *exporter) issueList(query string, limit int, count bool) []*docmodel.Node {
	if x.s.macros == nil || strings.TrimSpace(query) == "" {
		return x.notAvailable()
	}
	if count {
		limit = 1
	}
	issues, total, err := x.s.macros.Issues(x.ctx, x.p, query, max(1, min(limit, 200)))
	if err != nil {
		return []*docmodel.Node{paragraph(text(fmt.Sprintf(x.l.queryFailed, query), &docmodel.Mark{Type: "italic"}))}
	}
	search := "/issues/search?q=" + url.QueryEscape(query)
	summary := paragraph(text(fmt.Sprintf(x.l.issueCount, total), linkMark(search)))
	if count {
		return []*docmodel.Node{summary}
	}
	cell := func(typ string, content ...*docmodel.Node) *docmodel.Node {
		return &docmodel.Node{Type: typ, Content: []*docmodel.Node{paragraph(content...)}}
	}
	table := &docmodel.Node{Type: "table", Content: []*docmodel.Node{{Type: "tableRow", Content: []*docmodel.Node{
		cell("tableHeader", text(x.l.key)), cell("tableHeader", text(x.l.summary)), cell("tableHeader", text(x.l.status)),
		cell("tableHeader", text(x.l.assignee))}}}}
	for _, is := range issues {
		table.Content = append(table.Content, &docmodel.Node{Type: "tableRow", Content: []*docmodel.Node{
			cell("tableCell", text(is.Key, linkMark(x.issueURL(is.Key)))), cell("tableCell", text(is.Summary)),
			cell("tableCell", x.status(is.Status, is.StatusCategory)), cell("tableCell", text(nonEmpty(is.Assignee, "—")))}})
	}
	return []*docmodel.Node{table, summary}
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (x *exporter) buildStatus(repository, pipeline, branch string) []*docmodel.Node {
	if x.s.macros == nil {
		return x.notAvailable()
	}
	run, err := x.s.macros.LatestRun(x.ctx, x.p, repository, pipeline, branch)
	if err != nil || run == nil {
		return []*docmodel.Node{paragraph(text(x.l.noRuns, &docmodel.Mark{Type: "italic"}))}
	}
	color := map[string]string{"succeeded": "green", "failed": "red", "error": "red", "running": "blue", "queued": "gray",
		"cancelled": "gray"}[run.State]
	line := []*docmodel.Node{&docmodel.Node{Type: "status", Attrs: map[string]any{"text": run.State, "color": nonEmpty(color, "gray")}},
		text(" "), text(fmt.Sprintf("%s #%d", run.Name, run.Number), linkMark(run.URL)), text(" · " + repository)}
	if run.Branch != "" {
		line = append(line, text(" · "+run.Branch))
	}
	return []*docmodel.Node{paragraph(append(line, text(" · "+x.day(run.Created)))...)}
}

func (x *exporter) codeSnippet(repository, ref, path, lines string) []*docmodel.Node {
	if x.s.macros == nil || path == "" {
		return x.notAvailable()
	}
	f, err := x.s.macros.File(x.ctx, x.p, repository, ref, path)
	if err != nil || f == nil {
		return x.notAvailable()
	}
	from, to := 1, len(f.Lines)
	if a, b, ok := strings.Cut(strings.TrimSpace(lines), "-"); strings.TrimSpace(lines) != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(a)); err == nil {
			from, to = max(1, v), v
		}
		if ok {
			if v, err := strconv.Atoi(strings.TrimSpace(b)); err == nil {
				to = v
			}
		}
	}
	to = min(to, len(f.Lines))
	title := repository + ": " + f.Path
	if strings.TrimSpace(lines) != "" {
		title += fmt.Sprintf(":%d", from)
		if to != from {
			title += fmt.Sprintf("-%d", to)
		}
	}
	block := &docmodel.Node{Type: "codeBlock", Attrs: map[string]any{"title": title}}
	if f.Language != "" {
		block.Attrs["language"] = strings.ToLower(f.Language)
	}
	if from <= to {
		if code := strings.Join(f.Lines[from-1:to], "\n"); code != "" {
			block.Content = []*docmodel.Node{text(code)}
		}
	}
	return []*docmodel.Node{block}
}

func (x *exporter) readme(repository, ref string) []*docmodel.Node {
	if x.s.macros == nil {
		return x.notAvailable()
	}
	f, err := x.s.macros.Readme(x.ctx, x.p, repository, ref)
	if err != nil || f == nil {
		return x.notAvailable()
	}
	if f.Markdown != nil {
		return docmodel.LiftColumns(f.Markdown.Content)
	}
	block := &docmodel.Node{Type: "codeBlock", Attrs: map[string]any{"title": repository + ": " + f.Path}}
	if code := strings.Join(f.Lines, "\n"); code != "" {
		block.Content = []*docmodel.Node{text(code)}
	}
	return []*docmodel.Node{block}
}
