// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestAssigneePickerOffersWhoCanBeAssigned checks the assignee picker of an installation with many people: who may
// be assigned is part of the query, so that the picker finds assignable people even when hundreds of others match
// their name first, every kind of grant counts, and it offers no one who cannot be assigned.
func TestAssigneePickerOffersWhoCanBeAssigned(t *testing.T) {
	f := setup(t)
	issue := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Pick someone"})
	// 260 people without a role in SHOP whose names come first; the picker used to look at the first 200 matches.
	if _, err := f.env.App.Pool.Exec(context.Background(), `INSERT INTO users (username, display_name, email)
		SELECT 'filler' || lpad(g::text, 3, '0'), 'Aaa Crew ' || lpad(g::text, 3, '0'), 'filler' || g || '@example.com'
		FROM generate_series(1, 260) g`); err != nil {
		t.Fatal(err)
	}
	user := func(username, name string) int64 {
		var u struct {
			ID int64 `json:"id"`
		}
		f.admin.Post("/api/v1/admin/users", map[string]any{"username": username, "displayName": name,
			"email": username + "@example.com"}, &u)
		return u.ID
	}
	byName, byGroup, byRole, byRoleGroup := user("crew.name", "Crew By Name"), user("crew.group", "Crew By Group"),
		user("crew.role", "Crew By Role"), user("crew.rolegroup", "Crew By Role Group")
	outsider, disabled := user("crew.outsider", "Crew Outsider"), user("crew.disabled", "Crew Disabled")
	group := func(name string, members ...int64) int64 {
		var g struct {
			ID int64 `json:"id"`
		}
		f.admin.Post("/api/v1/admin/groups", map[string]any{"name": name}, &g)
		for _, m := range members {
			f.admin.MustDo(http.MethodPut, "/api/v1/admin/groups/"+itoa(g.ID)+"/members/"+itoa(m), nil, nil)
		}
		return g.ID
	}
	contractors, testers := group("Contractors", byGroup), group("Testers", byRoleGroup)
	role := func(n int) string { return itoa(f.cfg.Roles[n].ID) } // Administrators, Developers, Users
	member := func(n int, typ string, id int64) {
		f.lead.Post("/api/v1/projects/SHOP/roles/"+role(n)+"/members", map[string]any{"type": typ, "id": id}, nil)
	}
	member(1, "user", byRole)
	member(1, "group", testers)
	member(1, "user", disabled)
	member(2, "user", outsider)
	f.admin.MustDo(http.MethodPatch, "/api/v1/admin/users/"+itoa(disabled), map[string]any{"status": "disabled"}, nil)

	// A scheme that grants issue.assignable in every way: to Developers (dirk, Crew By Role and the Testers), to the
	// Contractors, to Crew By Name and to the project lead (lena).
	grants := []map[string]any{}
	grant := func(perm, holder string, id int64) {
		grants = append(grants, map[string]any{"permission": perm, "holderType": holder, "holderId": id})
	}
	for n := range 3 {
		grant("project.browse", "role", f.cfg.Roles[n].ID)
	}
	grant("project.admin", "role", f.cfg.Roles[0].ID)
	grant("issue.assign", "role", f.cfg.Roles[1].ID)
	grant("issue.assignable", "role", f.cfg.Roles[1].ID)
	grant("issue.assignable", "group", contractors)
	grant("issue.assignable", "user", byName)
	grant("issue.assignable", "project-lead", 0)
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/permission-schemes", map[string]any{"name": "Picker", "grants": grants}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"permissionSchemeId": scheme.ID}, nil)

	pick := func(q string) []string {
		t.Helper()
		var users []struct {
			DisplayName string `json:"displayName"`
		}
		f.dev.Get("/api/v1/projects/SHOP/assignable?query="+url.QueryEscape(q), &users)
		names := []string{}
		for _, u := range users {
			names = append(names, u.DisplayName)
		}
		return names
	}
	if got, want := pick("crew"), []string{"Crew By Group", "Crew By Name", "Crew By Role", "Crew By Role Group"}; !slices.Equal(got, want) {
		t.Fatalf("crew: %q, want %q", got, want)
	}
	if got := pick("lena"); !slices.Equal(got, []string{"Lena"}) {
		t.Fatalf("the project lead: %q", got)
	}
	if got, want := pick(""), []string{"Crew By Group", "Crew By Name", "Crew By Role", "Crew By Role Group", "Dirk", "Lena"}; !slices.Equal(got, want) {
		t.Fatalf("everyone assignable: %q, want %q", got, want)
	}

	// Whom the picker offers can be assigned; whom it does not offer cannot.
	assign := func(id int64) *apitest.Response {
		return f.dev.Do(http.MethodPatch, "/api/v1/issues/"+issue.Key, map[string]any{"fields": map[string]any{"assignee": id}})
	}
	for _, id := range []int64{byName, byGroup, byRole, byRoleGroup, f.ids["dirk"], f.ids["lena"]} {
		if r := assign(id); r.Status != http.StatusOK {
			t.Fatalf("assigning user %d: %d %s", id, r.Status, r.Body)
		}
	}
	var filler int64
	if err := f.env.App.Pool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = 'filler001'").Scan(&filler); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{outsider, disabled, filler, f.ids["ute"], f.ids["otto"]} {
		if r := assign(id); r.Code() != "issue.field-invalid" {
			t.Fatalf("assigned user %d, who cannot be assigned: %d %s", id, r.Status, r.Body)
		}
	}

	// Granted to everyone signed in, the picker offers a full page, and still no one disabled.
	grant("issue.assignable", "logged-in", 0)
	f.admin.MustDo(http.MethodPut, "/api/v1/issue-config/permission-schemes/"+itoa(scheme.ID), map[string]any{"name": "Picker",
		"grants": grants}, nil)
	got := pick("crew")
	if len(got) != 20 || got[0] != "Aaa Crew 001" || got[19] != "Aaa Crew 020" {
		t.Fatalf("crew for everyone signed in: %q", got)
	}
	if got := pick("crew disabled"); len(got) != 0 {
		t.Fatalf("a disabled user is offered: %q", got)
	}
	if got := pick("crew outsider"); !slices.Equal(got, []string{"Crew Outsider"}) {
		t.Fatalf("everyone signed in includes the outsider: %q", got)
	}
	if r := assign(outsider); r.Status != http.StatusOK {
		t.Fatalf("assigning the outsider when everyone signed in may be assigned: %d %s", r.Status, r.Body)
	}
}

type sharedItem struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Favourite   bool   `json:"favourite"`
	CanEdit     bool   `json:"canEdit"`
	Shares      []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"shares"`
}

type sharedPage struct {
	Total int          `json:"total"`
	Items []sharedItem `json:"items"`
}

func listShared(t *testing.T, c *apitest.Client, path string) sharedPage {
	t.Helper()
	var p sharedPage
	c.Get(path, &p)
	return p
}

func (p sharedPage) names() string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.Name)
	}
	return strings.Join(out, "|")
}

// sharing creates the group QA (ute) and the project OPS (lena alone) and returns the shares by name: dirk, QA,
// SHOP (dirk and ute browse it), everyone signed in, anyone, and OPS.
func (f *fixture) sharing(t *testing.T) map[string][]map[string]any {
	t.Helper()
	var qa struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "QA"}, &qa)
	f.admin.MustDo(http.MethodPut, "/api/v1/admin/groups/"+itoa(qa.ID)+"/members/"+itoa(f.ids["ute"]), nil, nil)
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	project := func(key string) int64 {
		var p struct {
			ID int64 `json:"id"`
		}
		f.lead.Get("/api/v1/projects/"+key, &p)
		return p.ID
	}
	one := func(share map[string]any) []map[string]any { return []map[string]any{share} }
	return map[string][]map[string]any{
		"private": {},
		"dirk":    one(map[string]any{"type": "user", "id": f.ids["dirk"]}),
		"qa":      one(map[string]any{"type": "group", "id": qa.ID}),
		"shop":    one(map[string]any{"type": "project", "projectId": project("SHOP")}),
		"signed":  one(map[string]any{"type": "logged-in"}),
		"anyone":  one(map[string]any{"type": "anyone"}),
		"ops":     one(map[string]any{"type": "project", "projectId": project("OPS")}),
	}
}

// sharedNames are the names of lena's filters and dashboards, by share; hidden ones sort between visible ones, so
// that pages counted before access would have gaps.
var sharedNames = []struct{ name, share string }{
	{"A private", "private"}, {"B for dirk", "dirk"}, {"C for QA", "qa"}, {"D for SHOP", "shop"},
	{"E for everyone signed in", "signed"}, {"F for anyone", "anyone"}, {"G for OPS", "ops"},
}

// checkSharedList checks a list of saved filters or dashboards (path) of lena's objects (ids by name) and dirk's own
// "H dirk's own": who sees what through each kind of share, pages, totals, search, owner and favourites.
func checkSharedList(t *testing.T, f *fixture, path string, ids map[string]int64) {
	t.Helper()
	list := func(c *apitest.Client, query string) sharedPage { return listShared(t, c, path+"?"+query) }

	// Each reader sees their own and what is shared with them, in pages with the same total on each page.
	var pages []string
	for offset := 0; offset < 6; offset += 2 {
		p := list(f.dev, "limit=2&offset="+strconv.Itoa(offset))
		if p.Total != 5 {
			t.Fatalf("dirk's total at %d: %d", offset, p.Total)
		}
		pages = append(pages, p.names())
	}
	if want := []string{"B for dirk|D for SHOP", "E for everyone signed in|F for anyone", "H dirk's own"}; !slices.Equal(pages, want) {
		t.Fatalf("dirk's pages: %q, want %q", pages, want)
	}
	if p := list(f.dev, "limit=2&offset=10"); p.Total != 5 || len(p.Items) != 0 {
		t.Fatalf("beyond the end: %+v", p)
	}
	for _, c := range []struct {
		client *apitest.Client
		want   string
	}{
		{f.user, "C for QA|D for SHOP|E for everyone signed in|F for anyone"},
		{f.other, "E for everyone signed in|F for anyone"},
		{f.lead, "A private|B for dirk|C for QA|D for SHOP|E for everyone signed in|F for anyone|G for OPS"},
	} {
		if p := list(c.client, "limit=100"); p.names() != c.want || p.Total != len(strings.Split(c.want, "|")) {
			t.Fatalf("%s sees %d: %s, want %s", c.client.Username, p.Total, p.names(), c.want)
		}
	}
	if p := list(f.user, "limit=3&offset=3"); p.names() != "F for anyone" || p.Total != 4 {
		t.Fatalf("ute's second page: %d %s", p.Total, p.names())
	}

	for _, it := range list(f.dev, "limit=100").Items {
		if it.CanEdit != (it.Name == "H dirk's own") {
			t.Fatalf("dirk may change %s: %v", it.Name, it.CanEdit)
		}
	}

	// Search in names and descriptions; only own or only others'; favourites.
	if p := list(f.dev, "query=SHOP"); p.names() != "D for SHOP" || p.Total != 1 {
		t.Fatalf("query shop: %d %s", p.Total, p.names())
	}
	if p := list(f.dev, "query=review"); p.names() != "H dirk's own" {
		t.Fatalf("query in descriptions: %s", p.names())
	}
	if p := list(f.dev, "owner=me"); p.names() != "H dirk's own" || p.Total != 1 {
		t.Fatalf("dirk's own: %d %s", p.Total, p.names())
	}
	if p := list(f.dev, "owner=others&limit=100"); p.names() != "B for dirk|D for SHOP|E for everyone signed in|F for anyone" || p.Total != 4 {
		t.Fatalf("shared with dirk: %d %s", p.Total, p.names())
	}
	f.dev.MustDo(http.MethodPut, path+"/"+itoa(ids["D for SHOP"])+"/favourite", nil, nil)
	f.dev.MustDo(http.MethodPut, path+"/"+itoa(ids["H dirk's own"])+"/favourite", nil, nil)
	if p := list(f.dev, "favourite=true"); p.names() != "D for SHOP|H dirk's own" || p.Total != 2 || !p.Items[0].Favourite {
		t.Fatalf("dirk's favourites: %+v", p)
	}
	if p := list(f.dev, "favourite=true&owner=others"); p.names() != "D for SHOP" {
		t.Fatalf("dirk's favourites of others: %s", p.names())
	}
	for _, it := range list(f.dev, "limit=100").Items {
		if it.Favourite != (it.Name == "D for SHOP" || it.Name == "H dirk's own") {
			t.Fatalf("favourite flag of %s: %+v", it.Name, it)
		}
	}
	if p := list(f.user, "favourite=true"); p.Total != 0 {
		t.Fatalf("dirk's favourites are ute's: %+v", p)
	}

	// Who cannot see an object gets the same answer as for one that does not exist.
	if r := f.dev.Do(http.MethodGet, path+"/"+itoa(ids["C for QA"]), nil); r.Status != http.StatusNotFound {
		t.Fatalf("dirk opens what is shared with QA: %d", r.Status)
	}
	if r := f.user.Do(http.MethodGet, path+"/"+itoa(ids["C for QA"]), nil); r.Status != http.StatusOK {
		t.Fatalf("ute opens what is shared with QA: %d", r.Status)
	}
	for _, q := range []string{"limit=501", "owner=nobody", "offset=-1"} {
		if r := f.dev.Do(http.MethodGet, path+"?"+q, nil); r.Status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", q, r.Status)
		}
	}
}

// TestFilterListPagesAndShares checks the list of saved filters: sharing is applied before counting and paging.
func TestFilterListPagesAndShares(t *testing.T) {
	f := setup(t)
	shares := f.sharing(t)
	ids := map[string]int64{}
	for _, s := range sharedNames {
		var saved struct {
			ID int64 `json:"id"`
		}
		f.lead.Post("/api/v1/filters", map[string]any{"name": s.name, "query": "project = SHOP", "shares": shares[s.share]}, &saved)
		ids[s.name] = saved.ID
	}
	var own struct {
		ID int64 `json:"id"`
	}
	f.dev.Post("/api/v1/filters", map[string]any{"name": "H dirk's own", "description": "Waiting for review",
		"query": "project = SHOP"}, &own)
	ids["H dirk's own"] = own.ID
	checkSharedList(t, f, "/api/v1/filters", ids)

	// Who else sees a filter is shown to who may change it only, with the current names.
	for _, c := range []struct {
		client *apitest.Client
		shares string
	}{{f.dev, ""}, {f.lead, "user Dirk"}} {
		for _, it := range listShared(t, c.client, "/api/v1/filters?query=for+dirk").Items {
			got := []string{}
			for _, sh := range it.Shares {
				got = append(got, sh.Type+" "+sh.Name)
			}
			if strings.Join(got, ",") != c.shares || it.CanEdit != (c.shares != "") {
				t.Fatalf("%s's view of the shares of %s: %+v", c.client.Username, it.Name, it)
			}
		}
	}

	// Without signing in, a reader sees only what is shared with anyone.
	if p := listShared(t, f.env.Anonymous(), "/api/v1/filters"); p.names() != "F for anyone" || p.Total != 1 {
		t.Fatalf("anonymous: %d %s", p.Total, p.names())
	}
	// A filter named in a query must be visible to the reader.
	if got := search(t, f.user, `filter = "C for QA"`); len(got) != 0 {
		t.Fatalf("ute's search by the QA filter: %v", got)
	}
	if r := f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(`filter = "C for QA"`), nil); r.Status != http.StatusBadRequest {
		t.Fatalf("dirk names a filter not shared with him: %d %s", r.Status, r.Body)
	}
}

// TestDashboardListPagesAndShares checks the list of dashboards: sharing is applied before counting and paging, and
// the start page finds the reader's favourites among all dashboards.
func TestDashboardListPagesAndShares(t *testing.T) {
	f := setup(t)
	shares := f.sharing(t)
	ids := map[string]int64{}
	for _, s := range sharedNames {
		var saved struct {
			ID int64 `json:"id"`
		}
		f.lead.Post("/api/v1/dashboards", map[string]any{"name": s.name, "shares": shares[s.share]}, &saved)
		ids[s.name] = saved.ID
	}
	// Who creates a dashboard has it among their favourites.
	var own struct {
		ID int64 `json:"id"`
	}
	f.dev.Post("/api/v1/dashboards", map[string]any{"name": "H dirk's own", "description": "Waiting for review"}, &own)
	ids["H dirk's own"] = own.ID
	if p := listShared(t, f.dev, "/api/v1/dashboards?favourite=true"); p.names() != "H dirk's own" {
		t.Fatalf("a new dashboard is no favourite: %s", p.names())
	}
	f.dev.MustDo(http.MethodDelete, "/api/v1/dashboards/"+itoa(own.ID)+"/favourite", nil, nil)
	checkSharedList(t, f, "/api/v1/dashboards", ids)
	if r := f.env.Anonymous().Do(http.MethodGet, "/api/v1/dashboards", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboards: %d", r.Status)
	}
}

// TestBoardListPagesAndSearch checks the list of boards, which the sprint health widget searches: only boards of
// projects the reader browses count and appear, and search and type apply before paging.
func TestBoardListPagesAndSearch(t *testing.T) {
	f := setup(t) // SHOP with its Scrum board "SHOP board"
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	// The boards the projects come with are named in the installation's language.
	shop := items[board](t, f.lead, "/api/v1/boards?project=SHOP")[0].Name
	ops := items[board](t, f.lead, "/api/v1/boards?project=OPS")[0].Name
	for _, b := range []struct{ name, typ string }{{"Alpha", "kanban"}, {"Beta", "scrum"}, {"Gamma", "kanban"}} {
		f.lead.Post("/api/v1/boards", map[string]any{"name": b.name, "type": b.typ, "project": "SHOP"}, nil)
	}
	list := func(c *apitest.Client, query string) (int, string) {
		t.Helper()
		var p struct {
			Total int     `json:"total"`
			Items []board `json:"items"`
		}
		c.Get("/api/v1/boards?"+query, &p)
		names := []string{}
		for _, b := range p.Items {
			names = append(names, b.Name)
		}
		return p.Total, strings.Join(names, "|")
	}
	var pages []string
	for offset := 0; offset < 4; offset += 2 {
		total, names := list(f.dev, "limit=2&offset="+strconv.Itoa(offset))
		if total != 4 {
			t.Fatalf("dirk's boards at %d: %d", offset, total)
		}
		pages = append(pages, names)
	}
	if want := []string{"Alpha|Beta", "Gamma|" + shop}; !slices.Equal(pages, want) {
		t.Fatalf("dirk's pages: %q, want %q", pages, want)
	}
	for _, c := range []struct {
		client       *apitest.Client
		query, names string
	}{
		{f.lead, "limit=100", "Alpha|Beta|Gamma|" + ops + "|" + shop},
		{f.other, "", ""},
		{f.dev, "query=ALP", "Alpha"},
		{f.dev, "query=shop", "Alpha|Beta|Gamma|" + shop}, // the project's key
		{f.lead, "query=operations", ops},                 // the project's name
		{f.dev, "query=operations", ""},
		{f.dev, "type=kanban", "Alpha|Gamma"},
		{f.lead, "type=kanban&query=a", "Alpha|Gamma|" + ops},
		{f.dev, "project=shop&type=scrum", "Beta|" + shop},
		{f.dev, "project=OPS", ""},
		{f.lead, "project=OPS", ops},
	} {
		total, names := list(c.client, c.query)
		if names != c.names || (names == "" && total != 0) || (names != "" && total != len(strings.Split(names, "|"))) {
			t.Fatalf("%s, %s: %d %q, want %q", c.client.Username, c.query, total, names, c.names)
		}
	}
	if r := f.dev.Do(http.MethodGet, "/api/v1/boards?type=list", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown type: %d", r.Status)
	}
}

// TestChartsSayWhenTheyAreCut checks that statistics with a limit say when they reached it.
func TestChartsSayWhenTheyAreCut(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Labelled"})
	labels := func(n int) {
		ls := []string{}
		for k := range n {
			ls = append(ls, "l"+strconv.Itoa(1000+k))
		}
		f.dev.MustDo(http.MethodPatch, "/api/v1/issues/"+i.Key, map[string]any{"fields": map[string]any{"labels": ls}}, nil)
	}
	var dist struct {
		Total     int   `json:"total"`
		Items     []any `json:"items"`
		Truncated bool  `json:"truncated"`
	}
	var m struct {
		X         []any `json:"x"`
		Y         []any `json:"y"`
		Truncated bool  `json:"truncated"`
	}
	q := url.QueryEscape("project = SHOP")
	// One issue with 100 labels has 100 values of labels and 100 × 100 combinations of them.
	labels(100)
	f.dev.Get("/api/v1/reports/distribution?field=labels&q="+q, &dist)
	if dist.Truncated || len(dist.Items) != 100 || dist.Total != 1 {
		t.Fatalf("100 labels: %d items, total %d, truncated %v", len(dist.Items), dist.Total, dist.Truncated)
	}
	f.dev.Get("/api/v1/reports/matrix?x=labels&y=labels&q="+q, &m)
	if !m.Truncated {
		t.Fatalf("10,000 combinations are not cut: %d × %d", len(m.X), len(m.Y))
	}
	labels(101)
	f.dev.Get("/api/v1/reports/distribution?field=labels&q="+q, &dist)
	if !dist.Truncated || len(dist.Items) != 100 || dist.Total != 1 {
		t.Fatalf("101 labels: %d items, total %d, truncated %v", len(dist.Items), dist.Total, dist.Truncated)
	}
	labels(70) // 4,900 combinations
	f.dev.Get("/api/v1/reports/matrix?x=labels&y=labels&q="+q, &m)
	if m.Truncated || len(m.X) != 70 || len(m.Y) != 70 {
		t.Fatalf("4,900 combinations: %d × %d, truncated %v", len(m.X), len(m.Y), m.Truncated)
	}

	// The board: a sprint with more issues than a board shows, and more issues than the cumulative flow counts.
	boards := items[board](t, f.dev, "/api/v1/boards?project=SHOP")
	b := itoa(boards[0].ID)
	var sp sprint
	f.dev.Post("/api/v1/boards/"+b+"/sprints", map[string]any{}, &sp)
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+itoa(sp.ID)+"/issues", map[string]any{"issues": []string{i.Key}}, nil)
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+itoa(sp.ID)+"/start", map[string]any{}, nil)
	var health struct {
		Sprints []struct {
			Todo int `json:"todo"`
		} `json:"sprints"`
		Truncated bool `json:"truncated"`
	}
	var flow struct {
		Days      []any `json:"days"`
		Truncated bool  `json:"truncated"`
	}
	f.dev.Get("/api/v1/boards/"+b+"/sprint-health", &health)
	f.dev.Get("/api/v1/boards/"+b+"/cumulative-flow?days=7", &flow)
	if health.Truncated || flow.Truncated || len(health.Sprints) != 1 || health.Sprints[0].Todo != 1 || len(flow.Days) != 7 {
		t.Fatalf("a small board is cut: %+v %+v", health, flow)
	}
	// Copies of the issue, written directly: 10,000 more issues, 2,000 of them in the sprint.
	if _, err := f.env.App.Pool.Exec(ctx, `INSERT INTO issues (project_id, number, key, type_id, status_id, summary, reporter_id,
		creator_id, rank) SELECT i.project_id, 100 + g, 'SHOP-' || (100 + g), i.type_id, i.status_id, 'Copy ' || g, i.reporter_id,
		i.creator_id, i.rank || lpad(g::text, 6, '0') FROM issues i, generate_series(1, 10000) g WHERE i.key = $1`, i.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.App.Pool.Exec(ctx, `INSERT INTO issue_sprints (issue_id, sprint_id) SELECT id, $1 FROM issues
		WHERE key LIKE 'SHOP-%' AND number BETWEEN 101 AND 2100`, sp.ID); err != nil {
		t.Fatal(err)
	}
	f.dev.Get("/api/v1/boards/"+b+"/sprint-health", &health)
	if !health.Truncated || len(health.Sprints) != 1 || health.Sprints[0].Todo != 2000 {
		t.Fatalf("2,001 issues in the sprint: %+v", health)
	}
	f.dev.Get("/api/v1/boards/"+b+"/cumulative-flow?days=7", &flow)
	if !flow.Truncated || len(flow.Days) != 7 {
		t.Fatalf("10,001 issues on the board: %+v", flow)
	}
}
