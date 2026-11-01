// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestExcerptsAndDetailsOfSearches: lists of pages show excerpts that read (clauses, no code, cut at a word);
// searches mark where their excerpts are cut, name the people of their hits and the paths of pages, and the wiki
// search counts its matches by space and kind, sorts by date and keeps what was updated lately.
func TestExcerptsAndDetailsOfSearches(t *testing.T) {
	w := newSearchWorld(t)
	lena, eve := w.lena, w.eve
	text := func(s string) map[string]any {
		return map[string]any{"type": "text", "text": s}
	}
	para := func(s string) map[string]any {
		return map[string]any{"type": "paragraph", "content": []any{text(s)}}
	}
	cell := func(kind, s string) map[string]any {
		return map[string]any{"type": kind, "content": []any{para(s)}}
	}
	var sp struct {
		HomePageID int64 `json:"homePageId"`
	}
	lena.Get("/api/v1/wiki/spaces/OPEN", &sp)
	var child struct {
		ID int64 `json:"id"`
	}
	lena.Post("/api/v1/wiki/spaces/OPEN/pages", map[string]any{"title": "Mooring masts", "parentId": w.ids["public page"]}, &child)
	long := strings.Repeat("The mast holds the zeppelin while it waits for the wind to turn. ", 8)
	lena.Post(apitest.Fmt("/api/v1/wiki/pages/%d/publish", child.ID), map[string]any{"title": "Mooring masts", "baseVersion": 0,
		"body": map[string]any{"type": "doc", "content": []any{
			map[string]any{"type": "heading", "attrs": map[string]any{"level": 2}, "content": []any{text("Masts")}},
			map[string]any{"type": "bulletList", "content": []any{
				map[string]any{"type": "listItem", "content": []any{para("North mast")}},
				map[string]any{"type": "listItem", "content": []any{para("South mast")}}}},
			map[string]any{"type": "codeBlock", "content": []any{text("moor(zeppelin)")}},
			map[string]any{"type": "table", "content": []any{
				map[string]any{"type": "tableRow", "content": []any{cell("tableHeader", "Mast"), cell("tableHeader", "State")}},
				map[string]any{"type": "tableRow", "content": []any{cell("tableCell", "North"), cell("tableCell", "free")}}}},
			para(long)}}}, nil)

	// The list of recent pages: clauses, no code, cut at a word with an ellipsis.
	var recent []struct {
		Title   string `json:"title"`
		Excerpt string `json:"excerpt"`
	}
	eve.Get("/api/v1/wiki/recent?mode=updated&space=OPEN", &recent)
	var excerpt string
	for _, p := range recent {
		if p.Title == "Mooring masts" {
			excerpt = p.Excerpt
		}
	}
	if !strings.HasPrefix(excerpt, "Masts · North mast · South mast · North: free · The mast holds the zeppelin") ||
		!strings.HasSuffix(excerpt, "…") || strings.Contains(excerpt, "moor(") || len([]rune(excerpt)) > 241 {
		t.Errorf("the excerpt of the list: %q", excerpt)
	}

	// The wiki search: the text around the matches with … where it is cut, the path, the facets.
	var found struct {
		Total int `json:"total"`
		Pages []struct {
			Title   string   `json:"title"`
			Excerpt string   `json:"excerpt"`
			Path    []string `json:"path"`
		} `json:"pages"`
		Facets *struct {
			Spaces []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"spaces"`
			Kinds []struct {
				Kind  string `json:"kind"`
				Count int    `json:"count"`
			} `json:"kinds"`
		} `json:"facets"`
	}
	eve.Get("/api/v1/wiki/search?q=wind&facets=true", &found)
	if found.Total != 1 || found.Pages[0].Title != "Mooring masts" || !strings.HasPrefix(found.Pages[0].Excerpt, "…") ||
		!strings.Contains(found.Pages[0].Excerpt, "\u0001wind\u0002") || len(found.Pages[0].Path) != 1 || found.Pages[0].Path[0] != "Zeppelin public page" {
		t.Errorf("the wiki search: %+v", found)
	}
	eve.Get("/api/v1/wiki/search?q=zeppelin&facets=true&kind=page", &found)
	if found.Facets == nil || len(found.Facets.Spaces) != 1 || found.Facets.Spaces[0].Key != "OPEN" || found.Facets.Spaces[0].Count != 3 ||
		len(found.Facets.Kinds) != 2 || found.Facets.Kinds[0].Count != 3 || found.Facets.Kinds[1].Count != 1 || found.Total != 3 {
		t.Errorf("the facets of eve's search (the space's home, the public page and its child; a blog post): %+v %+v", found.Total, found.Facets)
	}
	eve.Get("/api/v1/wiki/search?q=zeppelin&sort=updated", &found)
	if len(found.Pages) == 0 || found.Pages[0].Title != "Mooring masts" {
		t.Errorf("the newest first: %+v", found.Pages)
	}
	if _, err := w.env.App.Pool.Exec(t.Context(), "UPDATE pages SET updated_at = now() - interval '40 days' WHERE title = 'Zeppelin news'"); err != nil {
		t.Fatal(err)
	}
	eve.Get("/api/v1/wiki/search?q=zeppelin&updatedWithin=30", &found)
	for _, p := range found.Pages {
		if p.Title == "Zeppelin news" {
			t.Errorf("updated within 30 days: %+v", found.Pages)
		}
	}

	// The search page: issues with their assignees, pages with who changed them and their paths.
	lena.MustDo(http.MethodPatch, "/api/v1/issues/OPEN-4", map[string]any{"fields": map[string]any{"assignee": w.ids["lena"]}}, nil)
	var a searchAnswer
	eve.Get("/api/v1/search-all?q=zeppelin&kind=issue&limit=10", &a)
	assigned := false
	for _, h := range a.Groups[0].Hits {
		if h.Key == "OPEN-4" {
			assigned = h.Person != nil && h.Person.Username == "lena"
		}
	}
	if !assigned {
		t.Errorf("the assignee of OPEN-4: %+v", a.Groups)
	}
	eve.Get("/api/v1/search-all?q=wind&kind=page", &a)
	if len(a.Groups) != 1 || len(a.Groups[0].Hits) != 1 || a.Groups[0].Hits[0].Context != "Zeppelin open space › Zeppelin public page" ||
		a.Groups[0].Hits[0].Person == nil || a.Groups[0].Hits[0].Person.Username != "lena" || !strings.HasPrefix(a.Groups[0].Hits[0].Excerpt, "…") {
		t.Errorf("the page found: %+v", a.Groups)
	}
}
