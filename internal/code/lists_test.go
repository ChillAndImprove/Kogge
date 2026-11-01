// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/testutil/apitest"
)

type repoPage struct {
	Total int `json:"total"`
	Items []struct {
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
		Slug       string `json:"slug"`
		Permission string `json:"permission"`
		Archived   bool   `json:"archived"`
	} `json:"items"`
}

// names lists the repositories of a page as KEY/slug.
func (p repoPage) names() string {
	out := []string{}
	for _, r := range p.Items {
		out = append(out, r.Project.Key+"/"+r.Slug)
	}
	return strings.Join(out, " ")
}

type projectPage struct {
	Total int `json:"total"`
	Items []struct {
		Key          string `json:"key"`
		Name         string `json:"name"`
		Repositories int    `json:"repositories"`
		CanAdmin     bool   `json:"canAdmin"`
	} `json:"items"`
}

func (p projectPage) keys() string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.Key)
	}
	return strings.Join(out, " ")
}

// newListFixture creates code projects and repositories of which dirk may see some: those hidden from him sort
// between those he sees, so that counting or paging before access would show as gaps or wrong totals.
//
//	APP  Apps       alpha, charlie (dirk reads), echo (dirk writes); bravo, delta, foxtrot hidden
//	BANK Banking    ledger, hidden
//	LIB  Libraries  core, util: dirk reads the project
//	MOB  Mobile     app: internal, all signed-in users read
//	SEC  Secrets    vault, hidden
//	TOOL Tools      scripts: dirk administers the project
func newListFixture(t *testing.T) (*apitest.Env, *apitest.Client, *apitest.Client) {
	t.Helper()
	env := apitest.New(t)
	admin := env.Admin()
	dirk := env.CreateUser("dirk")
	adminID, dirkID := userID(t, admin), userID(t, dirk)
	grant := func(permission string, id int64) map[string]any {
		return map[string]any{"permission": permission, "holder": map[string]any{"type": "user", "id": id}}
	}
	for _, p := range []struct{ key, name, visibility, repos string }{
		{"APP", "Apps", "private", "alpha bravo charlie delta echo foxtrot"},
		{"BANK", "Banking", "private", "ledger"},
		{"LIB", "Libraries", "private", "core util"},
		{"MOB", "Mobile", "internal", "app"},
		{"SEC", "Secrets", "private", "vault"},
		{"TOOL", "Tools", "private", "scripts"},
	} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": p.key, "name": p.name, "visibility": p.visibility}, nil)
		for _, slug := range strings.Fields(p.repos) {
			admin.MustDo(http.MethodPost, "/api/v1/code/projects/"+p.key+"/repos", map[string]any{"name": slug, "readme": true}, nil)
		}
	}
	for _, slug := range []string{"alpha", "charlie"} {
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/APP/"+slug+"/permissions", []map[string]any{grant("repo.read", dirkID)}, nil)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/APP/echo/permissions", []map[string]any{grant("repo.write", dirkID)}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/LIB/permissions", []map[string]any{grant("codeproject.admin", adminID),
		grant("repo.read", dirkID)}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/TOOL/permissions", []map[string]any{grant("codeproject.admin", adminID),
		grant("codeproject.admin", dirkID)}, nil)
	return env, admin, dirk
}

func listRepos(t *testing.T, c *apitest.Client, query string) repoPage {
	t.Helper()
	var p repoPage
	c.Get("/api/v1/code/repos?"+query, &p)
	return p
}

// TestRepositoryListPages: the repository list pages with totals; access is applied before counting and paging,
// so that pages are full and totals count only what the reader may see, and filters and orders work on all
// repositories, not on a page of them.
func TestRepositoryListPages(t *testing.T) {
	env, admin, dirk := newListFixture(t)

	// Dirk sees seven repositories, in pages of three with the same total on each page.
	var got []string
	for offset := 0; offset < 9; offset += 3 {
		p := listRepos(t, dirk, "limit=3&offset="+strconv.Itoa(offset))
		if p.Total != 7 {
			t.Fatalf("dirk's total at %d: %d", offset, p.Total)
		}
		got = append(got, p.names())
	}
	if want := []string{"APP/alpha APP/charlie APP/echo", "LIB/core LIB/util MOB/app", "TOOL/scripts"}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
	if p := listRepos(t, dirk, "limit=3&offset=50"); p.Total != 7 || len(p.Items) != 0 {
		t.Fatalf("beyond the end: %+v", p)
	}
	if p := listRepos(t, dirk, ""); p.Total != 7 || len(p.Items) != 7 {
		t.Fatalf("the default page: %d of %d", len(p.Items), p.Total)
	}
	// The administrator created the projects and administers them, so he sees all twelve.
	if p := listRepos(t, admin, "limit=500"); p.Total != 12 || len(p.Items) != 12 ||
		!strings.HasPrefix(p.names(), "APP/alpha APP/bravo APP/charlie") {
		t.Fatalf("the administrator's repositories: %d %s", p.Total, p.names())
	}

	// Filters by project, text and the caller's access, before paging.
	for _, c := range []struct {
		query, want string
		total       int
	}{
		{"project=lib", "LIB/core LIB/util", 2},
		{"project=LIB&limit=1&offset=1", "LIB/util", 2},
		{"project=sec", "", 0},
		{"query=ch", "APP/charlie APP/echo", 2},
		{"query=LIB", "LIB/core LIB/util", 2},
		{"query=vault", "", 0},
		{"query=" + apitest.Query("%"), "", 0},
		{"query=_", "", 0},
		{"permission=read", "APP/alpha APP/charlie APP/echo LIB/core LIB/util MOB/app TOOL/scripts", 7},
		{"permission=write", "APP/echo TOOL/scripts", 2},
		{"permission=write&limit=1&offset=1", "TOOL/scripts", 2},
		{"permission=admin", "TOOL/scripts", 1},
		{"permission=write&project=APP", "APP/echo", 1},
	} {
		if p := listRepos(t, dirk, c.query); p.names() != c.want || p.Total != c.total {
			t.Errorf("%s: %d %q, want %d %q", c.query, p.Total, p.names(), c.total, c.want)
		}
	}
	for _, it := range listRepos(t, dirk, "limit=50").Items {
		if want := map[string]string{"echo": "write", "scripts": "admin"}[it.Slug]; it.Permission != cmp.Or(want, "read") {
			t.Errorf("dirk's access to %s: %s", it.Slug, it.Permission)
		}
	}

	// The latest push first; repositories never pushed follow, the newest first.
	for _, slug := range []string{"LIB/util", "APP/alpha"} {
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/"+slug+"/files", map[string]any{"branch": "main", "path": "x.txt", "content": "x\n"}, nil)
	}
	got = nil
	for offset := 0; offset < 9; offset += 3 {
		got = append(got, listRepos(t, dirk, "sort=pushed&limit=3&offset="+strconv.Itoa(offset)).names())
	}
	if want := []string{"APP/alpha LIB/util TOOL/scripts", "MOB/app LIB/core APP/echo", "APP/charlie"}; !slices.Equal(got, want) {
		t.Errorf("by the latest push: %q, want %q", got, want)
	}
	// Repositories pushed and created at the same time are ordered by id, so that pages never overlap.
	if _, err := env.App.Pool.Exec(context.Background(), "UPDATE repositories SET pushed_at = now(), created_at = now()"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for offset := range 7 {
		p := listRepos(t, dirk, "sort=pushed&limit=1&offset="+strconv.Itoa(offset))
		if len(p.Items) != 1 || seen[p.names()] {
			t.Fatalf("page %d of equal repositories: %q (seen %v)", offset, p.names(), seen)
		}
		seen[p.names()] = true
	}

	// Archived repositories, alone or left out.
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/APP/echo", map[string]any{"archived": true}, nil)
	if p := listRepos(t, dirk, "archived=true"); p.names() != "APP/echo" || !p.Items[0].Archived {
		t.Errorf("archived: %+v", p)
	}
	if p := listRepos(t, dirk, "archived=false"); p.Total != 6 || strings.Contains(p.names(), "echo") {
		t.Errorf("not archived: %d %s", p.Total, p.names())
	}
	if p := listRepos(t, dirk, "permission=write&archived=false"); p.names() != "TOOL/scripts" || p.Total != 1 {
		t.Errorf("repositories dirk may push to: %d %s", p.Total, p.names())
	}

	for _, query := range []string{"limit=501", "limit=0", "offset=-1", "sort=size", "permission=owner", "archived=maybe"} {
		if r := dirk.Do(http.MethodGet, "/api/v1/code/repos?"+query, nil); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", query, r.Status)
		}
	}
}

// TestCodeProjectListPages: code projects page with totals, counted after access; the numbers of repositories of
// a page come from one query and count only the repositories the reader may see.
func TestCodeProjectListPages(t *testing.T) {
	_, admin, dirk := newListFixture(t)
	list := func(c *apitest.Client, query string) projectPage {
		t.Helper()
		var p projectPage
		c.Get("/api/v1/code/projects?"+query, &p)
		return p
	}

	// Dirk sees four projects; Banking and Secrets sort between them.
	var got []string
	for offset := 0; offset < 6; offset += 2 {
		p := list(dirk, "limit=2&offset="+strconv.Itoa(offset))
		if p.Total != 4 {
			t.Fatalf("dirk's total at %d: %d", offset, p.Total)
		}
		got = append(got, p.keys())
	}
	if want := []string{"APP LIB", "MOB TOOL", ""}; !slices.Equal(got, want) {
		t.Fatalf("dirk's pages: %q, want %q", got, want)
	}
	counts := map[string]int{}
	for _, it := range list(dirk, "").Items {
		counts[it.Key] = it.Repositories
		if it.CanAdmin != (it.Key == "TOOL") {
			t.Errorf("dirk administers %s: %v", it.Key, it.CanAdmin)
		}
	}
	if want := map[string]int{"APP": 3, "LIB": 2, "MOB": 1, "TOOL": 1}; !maps.Equal(counts, want) {
		t.Errorf("dirk's repositories per project: %v, want %v", counts, want)
	}
	var single struct {
		Repositories int `json:"repositories"`
	}
	dirk.Get("/api/v1/code/projects/APP", &single)
	if single.Repositories != 3 {
		t.Errorf("dirk's repositories of APP: %d", single.Repositories)
	}

	// Administrators see every project, to manage them, and count what they may read.
	p := list(admin, "limit=500")
	counts = map[string]int{}
	for _, it := range p.Items {
		counts[it.Key] = it.Repositories
	}
	if want := map[string]int{"APP": 6, "BANK": 1, "LIB": 2, "MOB": 1, "SEC": 1, "TOOL": 1}; p.Total != 6 || !maps.Equal(counts, want) {
		t.Errorf("the administrator's projects: %d %v", p.Total, counts)
	}

	// Projects to create repositories in, and searches by key and name, before paging.
	for _, c := range []struct {
		query, want string
		total       int
	}{
		{"admin=true", "TOOL", 1},
		{"query=lib", "LIB", 1},
		{"query=mob", "MOB", 1},
		{"query=a", "APP LIB", 2},
		{"query=a&limit=1&offset=1", "LIB", 2},
		{"query=sec", "", 0},
		{"query=_", "", 0},
		{"query=" + apitest.Query("%"), "", 0},
	} {
		if p := list(dirk, c.query); p.keys() != c.want || p.Total != c.total {
			t.Errorf("%s: %d %q, want %d %q", c.query, p.Total, p.keys(), c.total, c.want)
		}
	}
	if p := list(admin, "admin=true&limit=500"); p.Total != 6 {
		t.Errorf("projects the administrator may create repositories in: %d", p.Total)
	}
	for _, query := range []string{"limit=501", "offset=-1"} {
		if r := dirk.Do(http.MethodGet, "/api/v1/code/projects?"+query, nil); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", query, r.Status)
		}
	}
}

// TestPullRequestListPages: pull requests of a repository and of the caller page without overlapping, also when
// they were updated at the same time; the caller's list counts only repositories they may still read; the commits
// of a pull request page and say whether more follow.
func TestPullRequestListPages(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t)
	admin := env.Admin()
	dirk := env.CreateUser("dirk")
	dirkID := userID(t, dirk)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PRS", "name": "Pull requests"}, nil)
	for _, slug := range []string{"one", "two"} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/PRS/repos", map[string]any{"name": slug, "readme": true}, nil)
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/"+slug+"/permissions", []map[string]any{{"permission": "repo.write",
			"holder": map[string]any{"type": "user", "id": dirkID}}}, nil)
	}
	open := func(slug, branch string, commits ...string) {
		t.Helper()
		for i, message := range commits {
			change := map[string]any{"branch": branch, "path": branch + ".txt", "content": message + "\n", "message": message}
			if i == 0 {
				change["branch"], change["newBranch"] = "main", branch
			}
			dirk.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/"+slug+"/files", change, nil)
		}
		dirk.MustDo(http.MethodPost, "/api/v1/code/repos/PRS/"+slug+"/pull-requests", map[string]any{"title": branch, "source": branch}, nil)
	}
	open("one", "first", "first one", "first two", "first three")
	open("one", "second", "second one")
	open("one", "third", "third one")
	open("two", "fourth", "fourth one")
	open("two", "fifth", "fifth one")
	// All were updated at the same time: the id decides, and pages still do not overlap.
	if _, err := env.App.Pool.Exec(ctx, "UPDATE pull_requests SET updated_at = now()"); err != nil {
		t.Fatal(err)
	}
	type prs []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	titles := func(list prs) string {
		out := []string{}
		for _, pr := range list {
			out = append(out, pr.Title)
		}
		return strings.Join(out, " ")
	}
	var got []string
	for offset := range 4 {
		var page struct {
			Total int `json:"total"`
			Items prs `json:"items"`
		}
		dirk.Get("/api/v1/code/repos/PRS/one/pull-requests?limit=1&offset="+strconv.Itoa(offset), &page)
		if page.Total != 3 {
			t.Fatalf("total at %d: %d", offset, page.Total)
		}
		got = append(got, titles(page.Items))
	}
	if want := []string{"third", "second", "first", ""}; !slices.Equal(got, want) {
		t.Errorf("pages of a repository: %q, want %q", got, want)
	}

	// The caller's pull requests in all repositories, in pages; those of a repository they may no longer read are
	// left out before paging.
	mine := func(query string) string {
		t.Helper()
		var list prs
		dirk.Get("/api/v1/code/pull-requests?role=author&"+query, &list)
		return titles(list)
	}
	if got := mine("limit=10"); got != "fifth fourth third second first" {
		t.Errorf("dirk's pull requests: %q", got)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/two/permissions", []map[string]any{}, nil)
	got = nil
	for offset := 0; offset < 6; offset += 2 {
		got = append(got, mine("limit=2&offset="+strconv.Itoa(offset)))
	}
	if want := []string{"third second", "first", ""}; !slices.Equal(got, want) {
		t.Errorf("dirk's pages without repository two: %q, want %q", got, want)
	}

	// The commits of a pull request, newest first, in pages that say whether more follow.
	commits := func(query string) (string, bool) {
		t.Helper()
		var list struct {
			Commits []struct {
				Subject string `json:"subject"`
			} `json:"commits"`
			More bool `json:"more"`
		}
		dirk.Get("/api/v1/code/repos/PRS/one/pull-requests/1/commits?"+query, &list)
		out := []string{}
		for _, c := range list.Commits {
			out = append(out, c.Subject)
		}
		return strings.Join(out, ", "), list.More
	}
	for query, want := range map[string]struct {
		subjects string
		more     bool
	}{
		"":                  {"first three, first two, first one", false},
		"limit=2":           {"first three, first two", true},
		"limit=2&offset=2":  {"first one", false},
		"limit=3":           {"first three, first two, first one", false},
		"limit=2&offset=10": {"", false},
	} {
		if subjects, more := commits(query); subjects != want.subjects || more != want.more {
			t.Errorf("commits %s: %q %v, want %q %v", query, subjects, more, want.subjects, want.more)
		}
	}
	if r := dirk.Do(http.MethodGet, "/api/v1/code/repos/PRS/one/pull-requests/1/commits?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("501 commits: %d", r.Status)
	}
	if r := dirk.Do(http.MethodGet, "/api/v1/code/pull-requests?offset=-1", nil); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("negative offset: %d", r.Status)
	}
}

// TestBranchAndTagPages: branches and tags page with totals; a branch or tag named like the filter comes first,
// however many others contain it; the distances to the default branch are those of the page's branches.
func TestBranchAndTagPages(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "BR", "name": "Branches"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/BR/repos", map[string]any{"name": "app", "readme": true}, &repo)
	// Branches of one commit sort by name; the one named like the filter lies between the others either way.
	for _, name := range []string{"a-fix", "fix", "z-fix", "other"} {
		admin.MustDo(http.MethodPost, "/api/v1/code/repos/BR/app/branches", map[string]any{"name": name, "from": "main"}, nil)
	}
	type branchPage struct {
		Total int `json:"total"`
		Items []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
			Ahead   int    `json:"ahead"`
			Behind  int    `json:"behind"`
		} `json:"items"`
	}
	branches := func(query string) branchPage {
		t.Helper()
		var p branchPage
		admin.Get("/api/v1/code/repos/BR/app/branches?"+query, &p)
		return p
	}
	seen := []string{}
	for offset := range 3 {
		p := branches("filter=fix&limit=1&offset=" + strconv.Itoa(offset))
		if p.Total != 3 || len(p.Items) != 1 {
			t.Fatalf("filter fix at %d: %+v", offset, p)
		}
		seen = append(seen, p.Items[0].Name)
	}
	if seen[0] != "fix" || !slices.Contains(seen, "a-fix") || !slices.Contains(seen, "z-fix") {
		t.Errorf("pages of fix: %q", seen)
	}
	if p := branches("filter=fix&limit=1&offset=3"); p.Total != 3 || len(p.Items) != 0 {
		t.Errorf("beyond the end: %+v", p)
	}

	// One commit on fix and one on main: the distances of every branch, the default branch first.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/BR/app/files", map[string]any{"branch": "fix", "path": "fix.txt", "content": "fix\n"}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/BR/app/files", map[string]any{"branch": "main", "path": "main.txt", "content": "main\n"}, nil)
	p := branches("limit=2")
	if p.Total != 5 || len(p.Items) != 2 || p.Items[0].Name != "main" || !p.Items[0].Default {
		t.Fatalf("the first page: %+v", p)
	}
	distances := map[string][2]int{}
	for offset := 0; offset < 5; offset += 2 {
		for _, b := range branches("limit=2&offset=" + strconv.Itoa(offset)).Items {
			distances[b.Name] = [2]int{b.Ahead, b.Behind}
		}
	}
	if want := map[string][2]int{"main": {0, 0}, "fix": {1, 1}, "a-fix": {0, 1}, "z-fix": {0, 1}, "other": {0, 1}}; !maps.Equal(distances, want) {
		t.Errorf("distances: %v, want %v", distances, want)
	}

	// Tags, made in the repository on disk.
	server := code.RepoDir(env.App.Cfg.DataDir, repo.ID)
	g := newGit(t)
	for _, name := range []string{"release-v1", "v1", "v1.1", "v2"} {
		g.must(server, "tag", name, "main")
	}
	type tagPage struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	tags := func(query string) tagPage {
		t.Helper()
		var p tagPage
		admin.Get("/api/v1/code/repos/BR/app/tags?"+query, &p)
		return p
	}
	if p := tags("filter=v1&limit=1"); p.Total != 3 || len(p.Items) != 1 || p.Items[0].Name != "v1" {
		t.Errorf("tags like v1: %+v", p)
	}
	seen = nil
	for offset := range 5 {
		for _, tg := range tags("limit=1&offset=" + strconv.Itoa(offset)).Items {
			seen = append(seen, tg.Name)
		}
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"release-v1", "v1", "v1.1", "v2"}) {
		t.Errorf("pages of tags: %q", seen)
	}
}
