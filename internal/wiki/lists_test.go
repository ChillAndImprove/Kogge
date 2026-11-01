// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// treeNode is an entry of the page tree.
type treeNode struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	HasChildren bool   `json:"hasChildren"`
	Restricted  bool   `json:"restricted"`
}

type treeLevel struct {
	Total int        `json:"total"`
	Items []treeNode `json:"items"`
}

func childPages(t *testing.T, c *apitest.Client, parent int64, query string) treeLevel {
	t.Helper()
	var l treeLevel
	c.Get("/api/v1/wiki/spaces/DOCS/tree?parent="+id(parent)+"&"+query, &l)
	return l
}

func titles(l treeLevel) string {
	out := []string{}
	for _, n := range l.Items {
		out = append(out, n.Title)
	}
	return strings.Join(out, ", ")
}

// restrictView lets only Lena and the named users see a page and the pages below it.
func (f *fixture) restrictView(t *testing.T, pageID int64, users ...string) {
	t.Helper()
	holders := []map[string]any{}
	for _, u := range append([]string{"lena"}, users...) {
		holders = append(holders, map[string]any{"type": "user", "id": f.ids[u]})
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(pageID)+"/restrictions", map[string]any{"view": holders, "edit": []any{}}, nil)
}

// grant adds space permissions of DOCS for a user.
func (f *fixture) grant(t *testing.T, user string, perms ...string) {
	t.Helper()
	var grants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/DOCS/permissions", &grants)
	for _, perm := range perms {
		grants = append(grants, map[string]any{"permission": perm, "holder": map[string]any{"type": "user", "id": f.ids[user]}})
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", grants, nil)
}

// TestPageTreePages checks the pages of the page tree: the children of a page come a page at a time in their order,
// with their total. Pages the reader may not see — restricted pages and everything below them — are left out before
// counting and paging, so that pages are full and hidden pages between visible ones leave no gaps.
func TestPageTreePages(t *testing.T) {
	f := setup(t)
	parent := f.publishNew(t, f.lena, "Meeting notes", f.space.HomePageID, "All meetings")
	meeting := map[int]int64{}
	for i := 1; i <= 9; i++ {
		meeting[i] = f.publishNew(t, f.lena, fmt.Sprintf("Meeting %d", i), parent.ID, "Notes").ID
	}
	// Meetings 3 and 6 are for Lena only, and so is what is below them; meeting 4 has a child for Lena only,
	// meeting 5 one for everyone.
	f.restrictView(t, meeting[3])
	f.restrictView(t, meeting[6])
	f.publishNew(t, f.lena, "Minutes of meeting 6", meeting[6], "Secret")
	appendix := f.publishNew(t, f.lena, "Appendix of meeting 4", meeting[4], "Secret")
	f.restrictView(t, appendix.ID)
	f.publishNew(t, f.lena, "Appendix of meeting 5", meeting[5], "Open")
	// Drafts and trashed pages are not in the tree.
	f.lena.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Meeting 10", "parentId": parent.ID}, nil)
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(meeting[9]), nil, nil)

	// Dirk sees six meetings, in pages of two, with the same total on every page.
	var got []string
	for offset := 0; offset < 8; offset += 2 {
		l := childPages(t, f.dirk, parent.ID, "limit=2&offset="+strconv.Itoa(offset))
		if l.Total != 6 {
			t.Fatalf("dirk's total at %d: %d", offset, l.Total)
		}
		got = append(got, titles(l))
	}
	if want := []string{"Meeting 1, Meeting 2", "Meeting 4, Meeting 5", "Meeting 7, Meeting 8", ""}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
	if l := childPages(t, f.dirk, parent.ID, "offset=100"); l.Total != 6 || len(l.Items) != 0 {
		t.Fatalf("beyond the end: %+v", l)
	}
	// A page has children only if the reader would see some when opening it.
	for _, n := range childPages(t, f.dirk, parent.ID, "").Items {
		if want := n.Title == "Meeting 5"; n.HasChildren != want || n.Restricted {
			t.Fatalf("dirk's %s: %+v", n.Title, n)
		}
	}
	// Lena sees all eight current meetings and what is below them.
	l := childPages(t, f.lena, parent.ID, "limit=5&offset=5")
	if l.Total != 8 || titles(l) != "Meeting 6, Meeting 7, Meeting 8" || !l.Items[0].HasChildren || !l.Items[0].Restricted {
		t.Fatalf("lena's second page: %+v", l)
	}
	for _, n := range childPages(t, f.lena, parent.ID, "").Items {
		if want := n.Title == "Meeting 4" || n.Title == "Meeting 5" || n.Title == "Meeting 6"; n.HasChildren != want {
			t.Fatalf("lena's %s: %+v", n.Title, n)
		}
	}
	// Below a page Dirk may not see, there is nothing for him — not even a count.
	if l := childPages(t, f.dirk, meeting[6], ""); l.Total != 0 || len(l.Items) != 0 {
		t.Fatalf("below a restricted page: %+v", l)
	}
	if l := childPages(t, f.lena, meeting[6], ""); l.Total != 1 || titles(l) != "Minutes of meeting 6" {
		t.Fatalf("lena below the restricted page: %+v", l)
	}

	// The top level of the space pages the same way.
	var home page
	f.lena.Get("/api/v1/wiki/pages/"+id(f.space.HomePageID), &home)
	f.publishNew(t, f.lena, "Archive", 0, "Old things")
	hidden := f.publishNew(t, f.lena, "Board", 0, "Board only")
	f.restrictView(t, hidden.ID)
	f.publishNew(t, f.lena, "Contacts", 0, "Phone numbers")
	got = nil
	for offset := 0; offset < 3; offset++ {
		var top treeLevel
		f.dirk.Get("/api/v1/wiki/spaces/DOCS/tree?limit=1&offset="+strconv.Itoa(offset), &top)
		if top.Total != 3 {
			t.Fatalf("dirk's top level at %d: %+v", offset, top)
		}
		got = append(got, titles(top))
	}
	if want := []string{home.Title, "Archive", "Contacts"}; !slices.Equal(got, want) {
		t.Fatalf("dirk's top level: %q, want %q", got, want)
	}
	var top treeLevel
	f.lena.Get("/api/v1/wiki/spaces/DOCS/tree", &top)
	if top.Total != 4 || titles(top) != home.Title+", Archive, Board, Contacts" {
		t.Fatalf("lena's top level: %+v", top)
	}

	// Pages are at most 500 pages long.
	for _, q := range []string{"limit=501", "limit=0", "offset=-1"} {
		if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces/DOCS/tree?"+q, nil); r.Status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", q, r.Status)
		}
	}
}

type versionList struct {
	Total int `json:"total"`
	Items []struct {
		Version int `json:"version"`
	} `json:"items"`
}

func numbers(l versionList) string {
	out := []string{}
	for _, v := range l.Items {
		out = append(out, strconv.Itoa(v.Version))
	}
	return strings.Join(out, " ")
}

// TestPageHistoryPages: the versions of a page come newest first, a page at a time with their total. Who may not see
// the page — or a page above it — gets no versions.
func TestPageHistoryPages(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Release notes", f.space.HomePageID, "Version 1")
	for v := 2; v <= 7; v++ {
		f.lena.Post("/api/v1/wiki/pages/"+id(p.ID)+"/publish", map[string]any{"title": "Release notes",
			"body": doc(fmt.Sprintf("Version %d", v)), "baseVersion": v - 1}, nil)
	}
	path := "/api/v1/wiki/pages/" + id(p.ID) + "/versions"
	var got []string
	for offset := 0; offset < 9; offset += 3 {
		var l versionList
		f.dirk.Get(path+"?limit=3&offset="+strconv.Itoa(offset), &l)
		if l.Total != 7 {
			t.Fatalf("total at %d: %d", offset, l.Total)
		}
		got = append(got, numbers(l))
	}
	if want := []string{"7 6 5", "4 3 2", "1"}; !slices.Equal(got, want) {
		t.Fatalf("pages of versions: %q, want %q", got, want)
	}
	var l versionList
	f.dirk.Get(path+"?offset=7", &l)
	if l.Total != 7 || len(l.Items) != 0 {
		t.Fatalf("beyond the end: %+v", l)
	}
	f.dirk.Get(path, &l)
	if l.Total != 7 || numbers(l) != "7 6 5 4 3 2 1" {
		t.Fatalf("all versions: %+v", l)
	}
	if r := f.dirk.Do(http.MethodGet, path+"?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("limit 501: %d", r.Status)
	}

	// A view restriction hides the versions of the page and of the pages below it.
	child := f.publishNew(t, f.lena, "Release 1.0", p.ID, "Notes")
	f.restrictView(t, p.ID)
	for _, pid := range []int64{p.ID, child.ID} {
		if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(pid)+"/versions", nil); r.Status != http.StatusNotFound {
			t.Fatalf("versions of restricted page %d: %d %s", pid, r.Status, r.Body)
		}
	}
	f.lena.Get("/api/v1/wiki/pages/"+id(child.ID)+"/versions", &l)
	if l.Total != 1 || numbers(l) != "1" {
		t.Fatalf("lena's versions of the child: %+v", l)
	}
}

type blogList struct {
	Total int `json:"total"`
	Pages []struct {
		Title string `json:"title"`
	} `json:"pages"`
}

func (b blogList) titles() string {
	out := []string{}
	for _, p := range b.Pages {
		out = append(out, strings.TrimPrefix(p.Title, "News "))
	}
	return strings.Join(out, " ")
}

// TestBlogPages: the blog of a space comes a page at a time with its total, newest first, and a page is as long as
// asked for, up to the maximum of 500. Posts the reader may not see are left out before counting and paging.
func TestBlogPages(t *testing.T) {
	f := setup(t)
	post := map[int]int64{}
	for i := 1; i <= 27; i++ {
		title := fmt.Sprintf("News %02d", i)
		var p page
		f.lena.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": title, "kind": "blog"}, &p)
		f.lena.Post("/api/v1/wiki/pages/"+id(p.ID)+"/publish", map[string]any{"title": title, "body": doc(title), "baseVersion": 0}, nil)
		post[i] = p.ID
	}
	f.restrictView(t, post[26])
	f.restrictView(t, post[13])

	// More than 200 posts at once were 25 without saying so.
	var b blogList
	f.lena.Get("/api/v1/wiki/spaces/DOCS/blog?limit=201", &b)
	if b.Total != 27 || len(b.Pages) != 27 {
		t.Fatalf("lena's blog with limit 201: %d of %d", len(b.Pages), b.Total)
	}
	var found blogList
	f.lena.Get("/api/v1/wiki/search?kind=blog&limit=300", &found)
	if found.Total != 27 || len(found.Pages) != 27 {
		t.Fatalf("search with limit 300: %d of %d", len(found.Pages), found.Total)
	}
	if r := f.lena.Do(http.MethodGet, "/api/v1/wiki/spaces/DOCS/blog?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("limit 501: %d", r.Status)
	}

	// Dirk sees 25 posts in full pages; the hidden ones leave no gaps.
	var got []string
	for offset := 0; offset < 30; offset += 10 {
		var b blogList
		f.dirk.Get("/api/v1/wiki/spaces/DOCS/blog?limit=10&offset="+strconv.Itoa(offset), &b)
		if b.Total != 25 {
			t.Fatalf("dirk's total at %d: %d", offset, b.Total)
		}
		got = append(got, b.titles())
	}
	if want := []string{"27 25 24 23 22 21 20 19 18 17", "16 15 14 12 11 10 09 08 07 06", "05 04 03 02 01"}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
}

type trashList struct {
	Total int `json:"total"`
	Items []struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Children int    `json:"children"`
	} `json:"items"`
}

func (l trashList) titles() string {
	out := []string{}
	for _, p := range l.Items {
		out = append(out, p.Title)
	}
	return strings.Join(out, ", ")
}

func trash(t *testing.T, c *apitest.Client, query string) trashList {
	t.Helper()
	var l trashList
	c.Get("/api/v1/wiki/spaces/DOCS/trash?"+query, &l)
	return l
}

// TestTrashPages: the trash of a space comes a page at a time with its total, the latest deleted first. Pages the
// reader may not see are left out before counting and paging, people who may delete only their own pages see only
// those, and pages are restored from any page of the list.
func TestTrashPages(t *testing.T) {
	f := setup(t)
	f.grant(t, "dirk", "page.delete")
	// Eight pages go to the trash one after the other; Lena alone may see T3 and T6, T5 is Otto's.
	for i := 1; i <= 8; i++ {
		c := f.lena
		if i == 5 {
			c = f.otto
		}
		p := f.publishNew(t, c, fmt.Sprintf("T%d", i), f.space.HomePageID, "Old")
		if i == 3 || i == 6 {
			f.restrictView(t, p.ID)
		}
		c.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(p.ID), nil, nil)
	}
	var got []string
	for offset := 0; offset < 9; offset += 3 {
		l := trash(t, f.dirk, "limit=3&offset="+strconv.Itoa(offset))
		if l.Total != 6 {
			t.Fatalf("dirk's total at %d: %d", offset, l.Total)
		}
		got = append(got, l.titles())
	}
	if want := []string{"T8, T7, T5", "T4, T2, T1", ""}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
	if l := trash(t, f.lena, "limit=100"); l.Total != 8 || l.titles() != "T8, T7, T6, T5, T4, T3, T2, T1" {
		t.Fatalf("lena's trash: %+v", l)
	}
	// Otto may delete his own pages only, so he sees only those.
	if l := trash(t, f.otto, ""); l.Total != 1 || l.titles() != "T5" {
		t.Fatalf("otto's trash: %+v", l)
	}

	// A page restored from the second page leaves the trash; the pages close up.
	second := trash(t, f.dirk, "limit=3&offset=3")
	f.dirk.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(second.Items[1].ID)+"/restore", nil, nil)
	if l := trash(t, f.dirk, "limit=3&offset=3"); l.Total != 5 || l.titles() != "T4, T1" {
		t.Fatalf("after restoring T2: %+v", l)
	}
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(second.Items[0].ID)+"/purge", nil, nil)
	if l := trash(t, f.dirk, "limit=3"); l.Total != 4 || l.titles() != "T8, T7, T5" {
		t.Fatalf("after purging T4: %+v", l)
	}

	// A restricted page takes its children to the trash; the trash counts those the reader may see.
	salaries := f.publishNew(t, f.lena, "Salaries", f.space.HomePageID, "Numbers")
	f.restrictView(t, salaries.ID, "dirk")
	bonus := f.publishNew(t, f.lena, "Bonus", salaries.ID, "Rules")
	f.restrictView(t, bonus.ID)
	f.publishNew(t, f.lena, "Raises", salaries.ID, "Rules")
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(salaries.ID), nil, nil)
	if l := trash(t, f.lena, "limit=1"); l.Total != 7 || l.titles() != "Salaries" || l.Items[0].Children != 2 {
		t.Fatalf("lena's trash: %+v", l)
	}
	if l := trash(t, f.dirk, "limit=1"); l.Total != 5 || l.titles() != "Salaries" || l.Items[0].Children != 1 {
		t.Fatalf("dirk's trash: %+v", l)
	}
	if l := trash(t, f.dirk, "offset=100"); l.Total != 5 || len(l.Items) != 0 {
		t.Fatalf("beyond the end: %+v", l)
	}
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces/DOCS/trash?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("limit 501: %d", r.Status)
	}

	// Who may only read the space has no trash to see.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", []map[string]any{
		{"permission": "space.admin", "holder": map[string]any{"type": "user", "id": f.ids["lena"]}},
		{"permission": "space.view", "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}},
	}, nil)
	if l := trash(t, f.dirk, ""); l.Total != 0 || len(l.Items) != 0 {
		t.Fatalf("a reader's trash: %+v", l)
	}
}
