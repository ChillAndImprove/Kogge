// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/testutil/apitest"
)

type directoryItem struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Favourite   bool       `json:"favourite"`
	Permissions []string   `json:"permissions"`
	LastUpdated *time.Time `json:"lastUpdated"`
}

type directory struct {
	Total int             `json:"total"`
	Items []directoryItem `json:"items"`
}

func listSpaces(t *testing.T, c *apitest.Client, query string) directory {
	t.Helper()
	var d directory
	c.Get("/api/v1/wiki/spaces?"+query, &d)
	return d
}

func keys(d directory) string {
	out := []string{}
	for _, it := range d.Items {
		out = append(out, it.Key)
	}
	return strings.Join(out, " ")
}

// TestSpaceDirectoryPages checks the pages of the space directory: access is applied before counting and paging,
// so that pages are full and totals count only what the reader may see, and filters and orders work on all
// spaces, not on a page of them.
func TestSpaceDirectoryPages(t *testing.T) {
	f := setup(t) // lena's open space DOCS ("Documentation")
	// Closed spaces sort between the open ones: counted or paged before access, they would leave gaps.
	for i, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel"} {
		body := map[string]any{"key": "S" + name[:1], "name": name, "open": i%2 == 0}
		if name == "Charlie" || name == "Delta" {
			body["categories"] = []string{"Ops"}
		}
		f.lena.Post("/api/v1/wiki/spaces", body, nil)
	}

	// Dirk sees the open spaces only, in pages of two, with the same total on every page.
	var got []string
	for offset := 0; offset < 6; offset += 2 {
		d := listSpaces(t, f.dirk, "limit=2&offset="+id(int64(offset)))
		if d.Total != 5 {
			t.Fatalf("dirk's total at %d: %d", offset, d.Total)
		}
		got = append(got, keys(d))
	}
	if want := []string{"SA SC", "DOCS SE", "SG"}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
	if d := listSpaces(t, f.dirk, "limit=2&offset=10"); d.Total != 5 || len(d.Items) != 0 {
		t.Fatalf("beyond the end: %+v", d)
	}
	if d := listSpaces(t, f.lena, "limit=100"); d.Total != 9 {
		t.Fatalf("lena's spaces: %d %s", d.Total, keys(d))
	}
	// Administrators see every space only when they ask for it.
	if d := listSpaces(t, f.admin, "all=true&limit=100"); d.Total != 9 {
		t.Fatalf("all spaces for the administrator: %d %s", d.Total, keys(d))
	}
	if d := listSpaces(t, f.dirk, "all=true&limit=100"); d.Total != 5 {
		t.Fatalf("all=true for dirk: %d %s", d.Total, keys(d))
	}

	// Search by name or key, and filters by category, favourite, type and permission, before paging.
	if d := listSpaces(t, f.dirk, "query=HA"); keys(d) != "SA SC" || d.Total != 2 {
		t.Fatalf("query ha: %d %s", d.Total, keys(d))
	}
	if d := listSpaces(t, f.dirk, "query=sc"); keys(d) != "SC" {
		t.Fatalf("query by key: %s", keys(d))
	}
	if d := listSpaces(t, f.dirk, "category=ops"); keys(d) != "SC" {
		t.Fatalf("category of dirk: %s", keys(d))
	}
	if d := listSpaces(t, f.lena, "category=ops"); keys(d) != "SC SD" {
		t.Fatalf("category of lena: %s", keys(d))
	}
	f.dirk.MustDo(http.MethodPut, "/api/v1/wiki/spaces/SE/favourite", nil, nil)
	if d := listSpaces(t, f.dirk, "favourite=true"); keys(d) != "SE" || !d.Items[0].Favourite {
		t.Fatalf("favourites: %+v", d)
	}
	if d := listSpaces(t, f.lena, "favourite=true"); d.Total != 0 {
		t.Fatalf("dirk's favourite is lena's: %+v", d)
	}
	for _, it := range listSpaces(t, f.dirk, "limit=100").Items {
		if it.Favourite != (it.Key == "SE") || !slices.Contains(it.Permissions, "page.edit") {
			t.Fatalf("the caller's view of %s: %+v", it.Key, it)
		}
	}
	var own struct {
		Key string `json:"key"`
	}
	f.dirk.Post("/api/v1/wiki/spaces/personal", nil, &own)
	if d := listSpaces(t, f.dirk, "type=personal"); keys(d) != own.Key || d.Items[0].Type != "personal" {
		t.Fatalf("personal spaces: %+v", d)
	}
	if d := listSpaces(t, f.dirk, "type=team"); d.Total != 5 {
		t.Fatalf("team spaces: %d %s", d.Total, keys(d))
	}
	if d := listSpaces(t, f.lena, "owner=DIRK"); keys(d) != own.Key {
		t.Fatalf("dirk's spaces for lena: %s", keys(d))
	}
	if d := listSpaces(t, f.lena, "owner=lena"); d.Total != 0 {
		t.Fatalf("lena owns no personal space: %s", keys(d))
	}
	// A space dirk may only read is no place to write in.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/SB/permissions", []map[string]any{
		{"permission": "space.admin", "holder": map[string]any{"type": "user", "id": f.ids["lena"]}},
		{"permission": "space.view", "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}},
	}, nil)
	if d := listSpaces(t, f.dirk, "type=team&limit=100"); !strings.Contains(keys(d), "SB") {
		t.Fatalf("readable space missing: %s", keys(d))
	}
	if d := listSpaces(t, f.dirk, "type=team&permission=page.edit&limit=100"); strings.Contains(keys(d), "SB") || d.Total != 5 {
		t.Fatalf("writable spaces: %d %s", d.Total, keys(d))
	}
	// Archived spaces have a list of their own.
	f.lena.MustDo(http.MethodPatch, "/api/v1/wiki/spaces/SG", map[string]any{"archived": true}, nil)
	if d := listSpaces(t, f.dirk, "type=team&limit=100"); strings.Contains(keys(d), "SG") {
		t.Fatalf("archived space among the others: %s", keys(d))
	}
	if d := listSpaces(t, f.dirk, "archived=true"); keys(d) != "SG" {
		t.Fatalf("archived spaces: %s", keys(d))
	}

	// Orders: by key, and by the latest change the reader may see.
	if d := listSpaces(t, f.dirk, "type=team&sort=key&order=desc&limit=100"); keys(d) != "SE SC SB SA DOCS" {
		t.Fatalf("by key, descending: %s", keys(d))
	}
	f.publishPage(t, f.dirk, "SA", "Team lunch")
	secret := f.publishPage(t, f.lena, "SC", "Salaries")
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(secret)+"/restrictions", map[string]any{
		"view": []map[string]any{{"type": "user", "id": f.ids["lena"]}}, "edit": []any{}}, nil)
	if d := listSpaces(t, f.lena, "type=team&sort=updated&limit=2"); keys(d) != "SC SA" {
		t.Fatalf("lena's latest changes: %s", keys(d))
	}
	d := listSpaces(t, f.dirk, "type=team&sort=updated&limit=100")
	if !strings.HasPrefix(keys(d), "SA ") || d.Items[0].LastUpdated == nil {
		t.Fatalf("dirk's latest changes: %s", keys(d))
	}
	for _, it := range d.Items[1:] {
		if it.LastUpdated.After(*d.Items[0].LastUpdated) {
			t.Fatalf("a hidden page shows through the latest change of %s: %v after %v", it.Key, it.LastUpdated, d.Items[0].LastUpdated)
		}
	}
	if d := listSpaces(t, f.dirk, "type=team&sort=updated&order=asc&limit=100"); !strings.HasSuffix(keys(d), " SA") {
		t.Fatalf("oldest change first: %s", keys(d))
	}

	// Pages are at most 500 spaces long.
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("limit 501: %d", r.Status)
	}
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces?sort=size", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown order: %d", r.Status)
	}
}

// publishPage publishes a page below the home page of a space and returns its id.
func (f *fixture) publishPage(t *testing.T, c *apitest.Client, key, title string) int64 {
	t.Helper()
	var sp space
	c.Get("/api/v1/wiki/spaces/"+key, &sp)
	var p page
	c.Post("/api/v1/wiki/spaces/"+key+"/pages", map[string]any{"title": title, "parentId": sp.HomePageID}, &p)
	c.Post("/api/v1/wiki/pages/"+id(p.ID)+"/publish", map[string]any{"title": title, "body": doc(title), "baseVersion": 0}, &p)
	return p.ID
}
