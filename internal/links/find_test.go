// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/testutil/apitest"
)

// The world of the search tests: lena owns two of everything — one "public" that eve may see and one "secret" that
// she may not — whose titles all name Zeppelin.
type searchWorld struct {
	env              *apitest.Env
	admin, lena, eve *apitest.Client
	ids              map[string]int64
}

type hit struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Title   string `json:"title"`
	Context string `json:"context"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Excerpt string `json:"excerpt"`
	Status  *struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	} `json:"status"`
	Person *struct {
		Username string `json:"username"`
	} `json:"person"`
}

type quickAnswer struct {
	Groups []struct {
		Product string `json:"product"`
		Hits    []hit  `json:"hits"`
	} `json:"groups"`
	Incomplete []string `json:"incomplete"`
}

func (a quickAnswer) hits(product string) []hit {
	for _, g := range a.Groups {
		if g.Product == product {
			return g.Hits
		}
	}
	return nil
}

func (a quickAnswer) all() []hit {
	var out []hit
	for _, g := range a.Groups {
		out = append(out, g.Hits...)
	}
	return out
}

func titles(hits []hit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.Title)
	}
	return out
}

// quick searches; a product that did not answer within its budget (a busy test machine) is asked again.
func quick(t *testing.T, c *apitest.Client, q string) quickAnswer {
	t.Helper()
	var a quickAnswer
	for range 5 {
		c.Get("/api/v1/quick-search?q="+url.QueryEscape(q), &a)
		if len(a.Incomplete) == 0 {
			return a
		}
	}
	t.Fatalf("quick search for %q: %v did not answer in time", q, a.Incomplete)
	return a
}

func newSearchWorld(t *testing.T) *searchWorld {
	env := apitest.New(t)
	w := &searchWorld{env: env, admin: env.Admin(), ids: map[string]int64{}}
	w.lena, w.eve = env.CreateUser("lena"), env.CreateUser("eve")
	env.CreateUser("zeppelin-fan")
	lena, eve := w.lena, w.eve
	w.ids["lena"], w.ids["eve"] = userID(t, lena), userID(t, eve)
	for _, perm := range []string{"project.create", "space.create", "codeproject.create"} {
		w.admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": perm, "principalType": "user", "principalId": w.ids["lena"]}, nil)
	}

	// Kontor: OPEN, where eve is among the users, and SECR.
	for _, key := range []string{"OPEN", "SECR"} {
		lena.Post("/api/v1/projects", map[string]any{"key": key, "name": "Zeppelin " + strings.ToLower(key) + " project", "template": "kanban"}, nil)
	}
	var roles []struct {
		Role struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"role"`
	}
	lena.Get("/api/v1/projects/OPEN/roles", &roles)
	for _, r := range roles {
		if r.Role.Name == "Users" || r.Role.Name == "Benutzer" {
			lena.Post(apitest.Fmt("/api/v1/projects/OPEN/roles/%d/members", r.Role.ID), map[string]any{"type": "user", "id": w.ids["eve"]}, nil)
		}
	}
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	lena.Get("/api/v1/projects/OPEN/issue-types", &types)
	issue := func(project, summary, description string) int64 {
		var i struct {
			ID int64 `json:"id"`
		}
		lena.Post("/api/v1/issues", map[string]any{"project": project, "typeId": types[0].ID, "summary": summary,
			"descriptionMarkdown": description}, &i)
		return i.ID
	}
	w.ids["secret issue"] = issue("SECR", "Zeppelin secret launch", "")
	issue("OPEN", "Zeppelin", "")                 // OPEN-1, the whole title
	issue("OPEN", "The zeppelin hangar", "")      // OPEN-2, a word of the title
	issue("OPEN", "Zeppelin landing notes", "")   // OPEN-3, the beginning of the title
	issue("OPEN", "Airship", "About a zeppelin.") // OPEN-4, only its text
	for n := 5; n <= 27; n++ {
		issue("OPEN", apitest.Fmt("Paging case %d", n), "")
	}

	// Logbuch: an open space (all signed-in users read it) and a closed one.
	page := func(space, kind, title string) int64 {
		var sp struct {
			HomePageID int64 `json:"homePageId"`
		}
		lena.Get("/api/v1/wiki/spaces/"+space, &sp)
		var pg struct {
			ID int64 `json:"id"`
		}
		lena.Post("/api/v1/wiki/spaces/"+space+"/pages", map[string]any{"title": title, "kind": kind, "parentId": sp.HomePageID}, &pg)
		lena.Post(apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), map[string]any{"title": title, "baseVersion": 0,
			"body": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": "Notes about the flight."}}}}}}, nil)
		return pg.ID
	}
	for _, key := range []string{"OPEN", "SECR"} {
		open := key == "OPEN"
		lena.Post("/api/v1/wiki/spaces", map[string]any{"key": key, "name": "Zeppelin " + strings.ToLower(key) + " space", "open": &open}, nil)
	}
	w.ids["public page"] = page("OPEN", "page", "Zeppelin public page")
	page("OPEN", "blog", "Zeppelin news")
	w.ids["secret page"] = page("SECR", "page", "Zeppelin secret page")

	// Hafen and Helling: an internal code project (all signed-in users read it) and a private one, each with a
	// repository, a pull request and a run (of a broken pipeline file, which needs no agent).
	for key, visibility := range map[string]string{"OPNC": "internal", "SECC": "private"} {
		name := map[string]string{"OPNC": "public", "SECC": "secret"}[key]
		lena.Post("/api/v1/code/projects", map[string]any{"key": key, "name": "Zeppelin " + name + " code", "visibility": visibility}, nil)
		lena.Post("/api/v1/code/projects/"+key+"/repos", map[string]any{"name": "zeppelin-" + name, "readme": true}, nil)
		repo := "/api/v1/code/repos/" + key + "/zeppelin-" + name
		lena.MustDo(http.MethodPut, repo+"/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
			"content": "version: [broken\n", "message": "A broken pipeline"}, nil)
		lena.MustDo(http.MethodPut, repo+"/files", map[string]any{"branch": "main", "newBranch": "change", "path": "notes.md",
			"content": "# Notes\n", "message": "Notes"}, nil)
		var pr struct {
			ID     int64 `json:"id"`
			Number int   `json:"number"`
		}
		lena.Post(repo+"/pull-requests", map[string]any{"source": "change", "target": "main", "title": "Zeppelin " + name + " change"}, &pr)
		w.ids[name+" pull request"] = pr.ID
	}
	env.WorkJobs() // the pushes start the runs
	lena.Post("/api/v1/code/repos/OPNC/zeppelin-public/pull-requests/1/merge", map[string]any{}, nil)
	env.WorkJobs()
	return w
}

// leaks fails when an answer to eve names anything secret.
func (w *searchWorld) leaks(t *testing.T, what string, body []byte) {
	t.Helper()
	if s := strings.ToLower(string(body)); strings.Contains(s, "secret") || strings.Contains(s, "secr") || strings.Contains(s, "secc") {
		t.Errorf("%s shows eve what she may not see: %s", what, body)
	}
}

func TestQuickSearch(t *testing.T) {
	w := newSearchWorld(t)

	// lena finds both of every kind, in every product; eve only what she may see.
	all := quick(t, w.lena, "zeppelin")
	for product, want := range map[string][]string{
		"kontor":  {"Zeppelin secret launch", "Zeppelin open project", "Zeppelin secr project"},
		"logbuch": {"Zeppelin public page", "Zeppelin secret page", "Zeppelin news", "Zeppelin open space", "Zeppelin secr space"},
		"hafen":   {"zeppelin-public", "zeppelin-secret", "Zeppelin public change", "Zeppelin secret change", "Zeppelin public code", "Zeppelin secret code"},
		"helling": {"zeppelin-public", "zeppelin-secret"},
		"people":  {"Zeppelin-fan"},
	} {
		got := titles(all.hits(product))
		for _, title := range want {
			if !slices.Contains(got, title) {
				t.Errorf("lena's %s results lack %q: %v", product, title, got)
			}
		}
	}
	mine := quick(t, w.eve, "zeppelin")
	raw, _ := json.Marshal(mine)
	w.leaks(t, "the quick search", raw)
	for _, h := range mine.all() {
		if h.URL == "" || !strings.HasPrefix(h.URL, "/") {
			t.Errorf("hit without an address: %+v", h)
		}
	}
	for _, title := range []string{"Zeppelin", "Zeppelin public page", "Zeppelin news", "zeppelin-public", "Zeppelin public change",
		"Zeppelin public code", "Zeppelin open space", "Zeppelin open project", "Zeppelin-fan"} {
		if !slices.Contains(titles(mine.all()), title) {
			t.Errorf("eve does not find %q: %v", title, titles(mine.all()))
		}
	}
	// The pipelines of a repository are found with the state of its newest run.
	if p := mine.hits("helling"); len(p) != 1 || p[0].URL != "/builds/opnc/zeppelin-public" || p[0].State != "error" {
		t.Errorf("eve's pipelines: %+v", p)
	}
	if prs := mine.hits("hafen"); !slices.ContainsFunc(prs, func(h hit) bool { return h.Kind == "pull-request" && h.State == "merged" && h.Key == "#1" }) {
		t.Errorf("the merged pull request: %+v", prs)
	}

	// Ranking: the whole title, then its beginning, then its words; the quick search matches titles only.
	issues := []string{}
	for _, h := range mine.hits("kontor") {
		if h.Kind == "issue" {
			issues = append(issues, h.Title)
		}
	}
	if want := []string{"Zeppelin", "Zeppelin landing notes", "The zeppelin hangar"}; !slices.Equal(issues, want) {
		t.Errorf("issues ranked %v, want %v", issues, want)
	}
	// As you type: the beginnings of words.
	if got := titles(quick(t, w.eve, "zep hang").hits("kontor")); !slices.Equal(got, []string{"The zeppelin hangar"}) {
		t.Errorf("zep hang: %v", got)
	}
	// Short beginnings, which who sees few projects reads project by project, rank and filter the same way.
	for _, c := range []struct {
		client *apitest.Client
		want   []string
	}{
		{w.eve, []string{"Zeppelin", "Zeppelin landing notes", "The zeppelin hangar"}},
		{w.lena, []string{"Zeppelin", "Zeppelin landing notes", "Zeppelin secret launch", "The zeppelin hangar"}},
	} {
		var got []string
		for _, h := range quick(t, c.client, "zep").hits("kontor") {
			if h.Kind == "issue" {
				got = append(got, h.Title)
			}
		}
		if len(got) > 2 {
			slices.Sort(got[1 : len(got)-1]) // the beginnings of titles, ranked by likeness
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s, zep: %v, want %v", c.client.Username, got, c.want)
		}
	}

	// An exact key comes first, then the keys it begins; who may not see the issue gets nothing, not even its title.
	keys := []string{}
	for _, h := range quick(t, w.eve, "open-1").hits("kontor") {
		keys = append(keys, h.Key)
	}
	if len(keys) < 4 || !slices.Equal(keys[:4], []string{"OPEN-1", "OPEN-10", "OPEN-11", "OPEN-12"}) {
		t.Errorf("keys as typed: %v", keys)
	}
	if first := quick(t, w.lena, "SECR-1").all(); len(first) == 0 || first[0].Key != "SECR-1" || first[0].Status == nil {
		t.Errorf("lena's exact key: %+v", first)
	}
	r := w.eve.MustDo(http.MethodGet, "/api/v1/quick-search?q=SECR-1", nil, nil)
	var none quickAnswer
	r.Decode(&none)
	if len(none.all()) != 0 {
		t.Errorf("eve finds SECR-1: %s", r.Body)
	}
	// Signed out (the instance does not allow anonymous access): nothing at all.
	r = w.env.Anonymous().MustDo(http.MethodGet, "/api/v1/quick-search?q=zeppelin", nil, nil)
	r.Decode(&none)
	if len(none.all()) != 0 {
		t.Errorf("anonymous: %s", r.Body)
	}
	// Nothing to search for, or only punctuation.
	for _, q := range []string{"", "   ", "%!"} {
		if a := quick(t, w.lena, q); len(a.all()) != 0 {
			t.Errorf("%q found %v", q, titles(a.all()))
		}
	}
}

type searchAnswer struct {
	Groups []struct {
		Product string `json:"product"`
		Kind    string `json:"kind"`
		Total   int    `json:"total"`
		Hits    []hit  `json:"hits"`
	} `json:"groups"`
	Incomplete []string `json:"incomplete"`
}

func TestSearchAll(t *testing.T) {
	w := newSearchWorld(t)
	var a searchAnswer
	r := w.eve.MustDo(http.MethodGet, "/api/v1/search-all?q=zeppelin", nil, nil)
	w.leaks(t, "the search page", r.Body)
	r.Decode(&a)
	totals := map[string]int{}
	for _, g := range a.Groups {
		totals[g.Kind] = g.Total
	}
	// The search page also finds the texts: OPEN-4 names a zeppelin in its description only.
	// The open space's home page is named after it.
	want := map[string]int{"issue": 4, "project": 1, "page": 2, "blog": 1, "space": 1, "repository": 1, "pull-request": 1,
		"code-project": 1, "pipelines": 1, "user": 1}
	for kind, n := range want {
		if totals[kind] != n {
			t.Errorf("eve's %s: %d, want %d (%v)", kind, totals[kind], n, totals)
		}
	}
	var issues searchAnswer
	w.eve.Get("/api/v1/search-all?q=zeppelin&kind=issue&limit=10", &issues)
	if len(issues.Groups) != 1 || len(issues.Groups[0].Hits) != 4 || issues.Groups[0].Hits[3].Key != "OPEN-4" ||
		!strings.Contains(issues.Groups[0].Hits[3].Excerpt, "\u0001zeppelin\u0002") {
		t.Errorf("issues of the search page: %+v", issues.Groups)
	}

	// Paging: 23 issues match "paging"; pages of 10 list each once.
	seen := map[string]bool{}
	for offset := 0; offset < 30; offset += 10 {
		var p searchAnswer
		w.eve.Get(apitest.Fmt("/api/v1/search-all?q=paging&kind=issue&limit=10&offset=%d", offset), &p)
		if len(p.Groups) != 1 || p.Groups[0].Total != 23 {
			t.Fatalf("page at %d: %+v", offset, p.Groups)
		}
		for _, h := range p.Groups[0].Hits {
			if seen[h.Key] {
				t.Errorf("%s on two pages", h.Key)
			}
			seen[h.Key] = true
		}
	}
	if len(seen) != 23 {
		t.Errorf("the pages list %d issues", len(seen))
	}

	// Who may not browse users finds no people.
	w.admin.MustDo(http.MethodDelete, "/api/v1/admin/global-permissions?permission=user.browse&principalType=logged-in&principalId=0", nil, nil)
	var people searchAnswer
	w.eve.Get("/api/v1/search-all?q=zeppelin&kind=user", &people)
	if len(people.Groups) != 0 {
		t.Errorf("people without user.browse: %+v", people.Groups)
	}
	if hits := quick(t, w.eve, "zeppelin").hits("people"); len(hits) != 0 {
		t.Errorf("quick search for people without user.browse: %+v", hits)
	}
}

func TestRecentVisits(t *testing.T) {
	w := newSearchWorld(t)
	visit := func(c *apitest.Client, kind string, id int64) {
		t.Helper()
		c.MustDo(http.MethodPost, "/api/v1/me/recent", map[string]any{"kind": kind, "id": id}, nil)
	}
	visit(w.lena, "page", w.ids["public page"])
	visit(w.lena, "issue", w.ids["secret issue"])
	visit(w.lena, "pull-request", w.ids["secret pull request"])
	visit(w.lena, "page", w.ids["public page"]) // again: it moves to the top
	var recent []hit
	w.lena.Get("/api/v1/me/recent", &recent)
	if got := titles(recent); !slices.Equal(got, []string{"Zeppelin public page", "Zeppelin secret change", "Zeppelin secret launch"}) {
		t.Errorf("lena's recent visits: %v", got)
	}
	// Recording what one may not see is harmless: reading the visits shows only what one may see.
	visit(w.eve, "issue", w.ids["secret issue"])
	visit(w.eve, "page", w.ids["secret page"])
	visit(w.eve, "page", w.ids["public page"])
	r := w.eve.MustDo(http.MethodGet, "/api/v1/me/recent", nil, nil)
	w.leaks(t, "recent visits", r.Body)
	r.Decode(&recent)
	if got := titles(recent); !slices.Equal(got, []string{"Zeppelin public page"}) {
		t.Errorf("eve's recent visits: %v", got)
	}
	if r := w.lena.Do(http.MethodPost, "/api/v1/me/recent", map[string]any{"kind": "dashboard", "id": 1}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("an unknown kind: %d %s", r.Status, r.Body)
	}
	if r := w.env.Anonymous().Do(http.MethodGet, "/api/v1/me/recent", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.Status)
	}
	// Only the newest visits are kept.
	for id := int64(1000); id < 1110; id++ {
		visit(w.lena, "run", id)
	}
	var kept int
	if err := w.env.App.Pool.QueryRow(t.Context(), "SELECT count(*) FROM recent_visits WHERE user_id = $1", w.ids["lena"]).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 100 {
		t.Errorf("%d visits kept", kept)
	}
}

type activityPage struct {
	Entries []struct {
		Key     string    `json:"key"`
		Product string    `json:"product"`
		Verb    string    `json:"verb"`
		At      time.Time `json:"at"`
		Title   string    `json:"title"`
		URL     string    `json:"url"`
		Actor   *struct {
			Username string `json:"username"`
		} `json:"actor"`
		Container struct {
			Kind string `json:"kind"`
			Key  string `json:"key"`
		} `json:"container"`
		Refs []struct {
			Name   string `json:"name"`
			Change string `json:"change"`
		} `json:"refs"`
		Detail string `json:"detail"`
	} `json:"entries"`
	Next string `json:"next"`
}

func (p activityPage) verbs() map[string]int {
	out := map[string]int{}
	for _, e := range p.Entries {
		out[e.Product+" "+e.Verb]++
	}
	return out
}

func TestActivityStream(t *testing.T) {
	w := newSearchWorld(t)
	lena, eve := w.lena, w.eve
	lena.Post("/api/v1/issues/OPEN-1/comments", map[string]any{"markdown": "On its way"}, nil)

	var everything activityPage
	lena.Get("/api/v1/activity-stream?limit=100", &everything)
	verbs := everything.verbs()
	for _, v := range []string{"kontor issue.created", "kontor issue.commented", "logbuch page.created", "logbuch blog.created", "hafen pushed",
		"hafen pull-request.opened", "hafen pull-request.merged", "helling run.error"} {
		if verbs[v] == 0 {
			t.Errorf("lena's stream has no %s: %v", v, verbs)
		}
	}
	for i := 1; i < len(everything.Entries); i++ {
		if everything.Entries[i].At.After(everything.Entries[i-1].At) {
			t.Fatalf("not newest first at %d: %+v", i, everything.Entries[i-1:i+1])
		}
	}
	var push bool
	for _, e := range everything.Entries {
		if e.Verb == "pushed" && e.Container.Key == "OPNC/zeppelin-public" && len(e.Refs) > 0 && e.Refs[0].Name == "change" &&
			e.Refs[0].Change == "created" && e.Actor != nil && e.Actor.Username == "lena" {
			push = true
		}
	}
	if !push {
		t.Errorf("the push of the new branch: %+v", everything.Entries)
	}

	// eve sees only what she may see — and with it the merged pull request and the page.
	r := eve.MustDo(http.MethodGet, "/api/v1/activity-stream?limit=100", nil, nil)
	w.leaks(t, "the activity stream", r.Body)
	var hers activityPage
	r.Decode(&hers)
	for _, v := range []string{"kontor issue.created", "logbuch page.created", "hafen pull-request.merged", "helling run.error"} {
		if hers.verbs()[v] == 0 {
			t.Errorf("eve's stream has no %s: %v", v, hers.verbs())
		}
	}

	// Filters: a product, what one is involved in, a person, a container.
	var hafen activityPage
	eve.Get("/api/v1/activity-stream?product=hafen", &hafen)
	for _, e := range hafen.Entries {
		if e.Product != "hafen" {
			t.Errorf("product=hafen: %+v", e)
		}
	}
	var involved activityPage
	eve.Get("/api/v1/activity-stream?mine=true", &involved)
	if len(involved.Entries) != 0 {
		t.Errorf("eve is involved in nothing: %v", involved.verbs())
	}
	lena.Get("/api/v1/activity-stream?mine=true&limit=100", &involved)
	if involved.verbs()["hafen pushed"] == 0 || involved.verbs()["kontor issue.created"] == 0 {
		t.Errorf("lena's own: %v", involved.verbs())
	}
	var byLena activityPage
	r = eve.MustDo(http.MethodGet, "/api/v1/activity-stream?user=lena&limit=100", nil, nil)
	w.leaks(t, "lena's stream", r.Body)
	r.Decode(&byLena)
	if len(byLena.Entries) == 0 {
		t.Error("lena's stream as eve sees it is empty")
	}
	var project activityPage
	eve.Get("/api/v1/activity-stream?project=OPEN&limit=100", &project)
	for _, e := range project.Entries {
		if e.Container.Key != "OPEN" {
			t.Errorf("project=OPEN: %+v", e)
		}
	}
	if len(project.Entries) < 27 {
		t.Errorf("project=OPEN: %d entries", len(project.Entries))
	}
	var repo activityPage
	eve.Get("/api/v1/activity-stream?repository=OPNC/zeppelin-public&limit=100", &repo)
	if repo.verbs()["hafen pull-request.merged"] != 1 || repo.verbs()["helling run.error"] == 0 {
		t.Errorf("repository=OPNC/zeppelin-public: %v", repo.verbs())
	}
	// Containers eve may not see do not exist for her.
	for query, code := range map[string]string{"project=SECR": "project.not-found", "space=SECR": "space.not-found",
		"repository=SECC/zeppelin-secret": "repository.not-found"} {
		r := eve.Do(http.MethodGet, "/api/v1/activity-stream?"+query, nil)
		ghost := eve.Do(http.MethodGet, "/api/v1/activity-stream?"+strings.NewReplacer("SECR", "GONE", "SECC", "GONE").Replace(query), nil)
		if r.Status != http.StatusNotFound || r.Code() != code || ghost.Status != r.Status || ghost.Code() != r.Code() {
			t.Errorf("%s: %d %s, for nothing %d %s", query, r.Status, r.Code(), ghost.Status, ghost.Code())
		}
	}
	if r := eve.Do(http.MethodGet, "/api/v1/activity-stream?project=OPEN&space=OPEN", nil); r.Code() != "activity.container" {
		t.Errorf("two containers: %d %s", r.Status, r.Body)
	}
	if r := eve.Do(http.MethodGet, "/api/v1/activity-stream?before=yesterday", nil); r.Code() != "activity.before" {
		t.Errorf("a bad cursor: %d %s", r.Status, r.Body)
	}

	// Paging: pages of three, read on from the last key, make up the whole stream.
	var keys []string
	before := ""
	for range 100 {
		var page activityPage
		lena.Get("/api/v1/activity-stream?limit=3&before="+url.QueryEscape(before), &page)
		for _, e := range page.Entries {
			keys = append(keys, e.Key)
		}
		if page.Next == "" {
			break
		}
		before = page.Next
	}
	var whole []string
	for _, e := range everything.Entries {
		whole = append(whole, e.Key)
	}
	if !slices.Equal(keys, whole) {
		raw, _ := json.Marshal(map[string]any{"paged": keys, "whole": whole})
		t.Errorf("paged stream differs: %s", raw)
	}
}
