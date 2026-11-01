// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"kogge.de/kogge/internal/links"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestMovedIssueKeepsItsReferences: commits, branches and pull requests that name an issue by the key it had before
// it moved to another project — those of before the move and new ones — still belong to it, commit commands with
// the old key still act on it, and wiki pages that mention the old key are listed for it.
func TestMovedIssueKeepsItsReferences(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Login"}, &issue)
	old := issue.Key

	// Dev develops in both projects and pushes commits of their own, whose commands run.
	dev := env.CreateUser("dev")
	devID := userID(t, dev)
	for _, p := range []string{"SHOP", "OPS"} {
		var roles []struct {
			Role struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"role"`
		}
		admin.Get("/api/v1/projects/"+p+"/roles", &roles)
		for _, r := range roles {
			if r.Role.Name == "Developers" || r.Role.Name == "Entwickler" {
				admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/%s/roles/%d/members", p, r.Role.ID), map[string]any{"type": "user",
					"id": devID}, nil)
			}
		}
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/PLAT/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": userID(t, admin)}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": devID}},
	}, nil)
	var token struct {
		Token string `json:"token"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("dev", token.Token)
	home := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	work := t.TempDir()
	git(work, "clone", u.String(), "shop")
	dir := filepath.Join(work, "shop")
	commit := func(name, message string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", ".")
		git(dir, "commit", "-m", message)
		git(dir, "push", "-u", "origin", "HEAD")
		env.WorkJobs()
	}
	git(dir, "checkout", "-b", "feature/"+old+"-login")
	commit("login.txt", old+" Add the login form")
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/shop/pull-requests", map[string]any{"title": old + " Login",
		"source": "feature/" + old + "-login"}, nil)
	env.WorkJobs()
	// A wiki page that mentions the issue by its key of then.
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "DOCS", "name": "Docs"}, nil)
	var space struct {
		HomePageID int64 `json:"homePageId"`
	}
	admin.Get("/api/v1/wiki/spaces/DOCS", &space)
	var page struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Login design", "parentId": space.HomePageID}, &page)
	admin.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+strconv.FormatInt(page.ID, 10)+"/publish", map[string]any{"title": "Login design",
		"baseVersion": 0, "body": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "The login of " + old + " is described here."}}}}}}, nil)

	var moved struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues/"+old+"/move", map[string]any{"project": "OPS"}, &moved)
	if moved.Key == old {
		t.Fatalf("not moved: %s", moved.Key)
	}
	// New commits that name the old key count too, and their commands act on the moved issue.
	commit("logout.txt", old+" Add the logout\n\n"+old+" #comment Logout works")

	var d links.Development
	admin.Get("/api/v1/issues/"+moved.Key+"/development", &d)
	if len(d.Branches) != 1 || len(d.Commits) != 2 || len(d.PullRequests) != 1 {
		t.Fatalf("development of the moved issue: %+v", d)
	}
	var comments []map[string]any
	admin.Get("/api/v1/issues/"+moved.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("the commit command of the old key: %d comments", len(comments))
	}
	var pages []struct {
		Title string `json:"title"`
	}
	admin.Get("/api/v1/wiki/mentions?issue="+moved.Key, &pages)
	if len(pages) != 1 || pages[0].Title != "Login design" {
		t.Errorf("pages that mention the old key: %+v", pages)
	}
	// Who cannot see the issue learns of no other key by the mentions.
	outsider := env.CreateUser("outsider")
	outsider.Get("/api/v1/wiki/mentions?issue="+moved.Key, &pages)
	if len(pages) != 0 {
		t.Errorf("an outsider sees pages: %+v", pages)
	}
}
