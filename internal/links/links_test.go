// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/links"
	"kogge.de/kogge/internal/testutil/apitest"
)

func TestMain(m *testing.M) {
	code.HookMain()
	os.Exit(m.Run())
}

func TestParseCommands(t *testing.T) {
	got := links.ParseCommands("SHOP-12 #comment Fixed rounding #time 1h 30m tricky #resolve-issue\n\nAlso SHOP-3 and OPS-1: #in-progress\n" +
		"#comment without a key\nPlain message about SHOP-4")
	want := []links.Command{
		{Keys: []string{"SHOP-12"}, Name: "comment", Args: "Fixed rounding"},
		{Keys: []string{"SHOP-12"}, Name: "time", Args: "1h 30m tricky"},
		{Keys: []string{"SHOP-12"}, Name: "resolve-issue"},
		{Keys: []string{"SHOP-3", "OPS-1"}, Name: "in-progress"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %+v", got)
	}
}

func TestDevelopment(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("dev")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	dev.Get("/api/v1/me", &me)

	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	var roles []struct {
		Role struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"role"`
	}
	admin.Get("/api/v1/projects/SHOP/roles", &roles)
	usersRole := int64(0)
	for _, r := range roles {
		if r.Role.Name == "Users" || r.Role.Name == "Benutzer" {
			usersRole = r.Role.ID
		}
		if r.Role.Name == "Developers" || r.Role.Name == "Entwickler" {
			admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/SHOP/roles/%d/members", r.Role.ID), map[string]any{"type": "user",
				"id": me.User.ID}, nil)
		}
	}
	var issue struct {
		Key string `json:"key"`
	}
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Login"}, &issue)
	var transitions []struct {
		Name string `json:"name"`
		To   struct {
			Name string `json:"name"`
		} `json:"to"`
	}
	dev.Get("/api/v1/issues/"+issue.Key+"/transitions", &transitions)
	if len(transitions) == 0 {
		t.Fatal("no transitions")
	}
	command := "#" + strings.ReplaceAll(strings.ToLower(transitions[0].Name), " ", "-")

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, &repo)
	var adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &adminMe)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/PLAT/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": me.User.ID}},
	}, nil)
	var token struct {
		Token string `json:"token"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("dev", token.Token)

	home := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	work := t.TempDir()
	git(work, "clone", u.String(), "shop")
	dir := filepath.Join(work, "shop")
	git(dir, "checkout", "-b", "feature/"+issue.Key+"-login")
	if err := os.WriteFile(filepath.Join(dir, "login.txt"), []byte("login\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", ".")
	git(dir, "commit", "-m", issue.Key+" Add login\n\n"+issue.Key+" #comment Implemented the form #time 1h 30m pairing "+command+"\nUNKNOWN-1 #comment nothing")
	git(dir, "push", "-u", "origin", "HEAD")
	env.WorkJobs()

	var d links.Development
	dev.Get("/api/v1/issues/"+issue.Key+"/development", &d)
	if len(d.Branches) != 1 || d.Branches[0].Name != "feature/"+issue.Key+"-login" || len(d.Commits) != 1 || d.Commits[0].Subject != issue.Key+" Add login" ||
		d.Commits[0].Author == nil {
		t.Fatalf("development = %+v", d)
	}
	if len(d.Commands) != 1 || strings.Contains(d.Commands[0].Result, "skipped") || strings.Count(d.Commands[0].Result, "done") != 3 {
		t.Errorf("command results = %+v", d.Commands)
	}
	var comments []struct {
		Body map[string]any `json:"body"`
	}
	dev.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("comments = %+v", comments)
	}
	var worklogs []struct {
		Seconds int64 `json:"seconds"`
	}
	dev.Get("/api/v1/issues/"+issue.Key+"/worklogs", &worklogs)
	if len(worklogs) != 1 || worklogs[0].Seconds != 5400 {
		t.Errorf("work logs = %+v", worklogs)
	}
	var after struct {
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
	}
	dev.Get("/api/v1/issues/"+issue.Key, &after)
	if after.Status.Name != transitions[0].To.Name {
		t.Errorf("status = %s, want %s", after.Status.Name, transitions[0].To.Name)
	}

	// The same commit in another branch does not run the commands again.
	git(dir, "push", "origin", "HEAD:refs/heads/copy")
	env.WorkJobs()
	dev.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("commands ran twice: %d comments", len(comments))
	}

	// Anyone can write any author into a commit: commands run only for the pusher's own commits.
	env.CreateUser("cfo")
	if err := os.WriteFile(filepath.Join(dir, "approval.txt"), []byte("approved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", ".")
	git(dir, "commit", "--author", "CFO <cfo@example.com>", "-m", issue.Key+" Approval\n\n"+issue.Key+" #comment Approved by the CFO")
	git(dir, "push", "origin", "HEAD")
	env.WorkJobs()
	dev.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("a command ran for another author: %d comments", len(comments))
	}
	dev.Get("/api/v1/issues/"+issue.Key+"/development", &d)
	found := false
	for _, c := range d.Commands {
		found = found || strings.Contains(c.Result, "pushed by someone else")
	}
	if !found {
		t.Errorf("command results = %+v", d.Commands)
	}

	// Pull requests naming the issue.
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/shop/pull-requests", map[string]any{"title": "Login form",
		"source": "feature/" + issue.Key + "-login"}, nil)
	env.WorkJobs()
	dev.Get("/api/v1/issues/"+issue.Key+"/development", &d)
	if len(d.PullRequests) != 1 || d.PullRequests[0].Title != "Login form" {
		t.Errorf("pull requests = %+v", d.PullRequests)
	}
	// Merges by Kogge run no commands: their messages carry texts of pull requests.
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/shop/pull-requests/1/merge", map[string]any{"strategy": "merge",
		"message": "Merge the login\n\n" + issue.Key + " #comment Merged with a command"}, nil)
	env.WorkJobs()
	dev.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("the merge ran a command: %d comments", len(comments))
	}
	// Users without access to the repository see no code.
	outsider := env.CreateUser("outsider")
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/SHOP/roles/%d/members", usersRole), map[string]any{"type": "user",
		"id": userID(t, outsider)}, nil)
	var hidden links.Development
	outsider.Get("/api/v1/issues/"+issue.Key+"/development", &hidden)
	if len(hidden.Branches)+len(hidden.Commits)+len(hidden.PullRequests) != 0 {
		t.Errorf("outsider sees code: %+v", hidden)
	}
}

func userID(t *testing.T, c *apitest.Client) int64 {
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	c.Get("/api/v1/me", &me)
	return me.User.ID
}
