// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestReviewsCannotBeBypassed: the author cannot remove a reviewer who asks for more work (audit M5), and approvals
// end when the target branch changes (audit M6).
func TestReviewsCannotBeBypassed(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	author, alice, eve := env.CreateUser("author"), env.CreateUser("alice"), env.CreateUser("eve")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"author": author, "alice": alice, "eve": eve} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "REV", "name": "Reviews"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/REV/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/REV/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": ids["author"]}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": ids["alice"]}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": ids["eve"]}, "permission": "repo.write"}}, nil)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/REV/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"merge"}, "requiredApprovals": 1, "noNeedsWork": true, "tasksResolved": false,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{}}}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/REV/app/branches", map[string]any{"name": "develop", "from": "main"}, nil)
	author.MustDo(http.MethodPut, "/api/v1/code/repos/REV/app/files", map[string]any{"branch": "develop", "newBranch": "feature",
		"path": "a.txt", "content": "a\n"}, nil)
	author.MustDo(http.MethodPost, "/api/v1/code/repos/REV/app/pull-requests", map[string]any{"title": "Feature", "source": "feature",
		"target": "develop", "reviewers": []int64{ids["alice"], ids["eve"]}}, nil)
	pr := "/api/v1/code/repos/REV/app/pull-requests/1"
	alice.MustDo(http.MethodPut, pr+"/review", map[string]any{"state": "approved"}, nil)
	eve.MustDo(http.MethodPut, pr+"/review", map[string]any{"state": "needs-work"}, nil)

	// The author cannot drop Eve to merge anyway; an administrator can, and the activity shows it.
	if r := author.Do(http.MethodPatch, pr, map[string]any{"reviewers": []int64{ids["alice"]}}); r.Code() != "pull-request.reviewer-veto" {
		t.Fatalf("the author removed a reviewer who asks for more work: %d %s", r.Status, r.Body)
	}
	if r := author.Do(http.MethodPost, pr+"/merge", map[string]any{"strategy": "merge"}); r.Status != http.StatusConflict {
		t.Fatalf("merged against the reviewer: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPatch, pr, map[string]any{"reviewers": []int64{ids["alice"]}}, nil)
	var activity []struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	author.Get(pr+"/timeline", &activity)
	found := false
	for _, a := range activity {
		if a.Data["reviewersRemoved"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("the removal is not in the activity: %+v", activity)
	}

	// A new target: what would be merged changes, so the approval ends.
	author.MustDo(http.MethodPatch, pr, map[string]any{"target": "main"}, nil)
	var got struct {
		Reviewers []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
			State string `json:"state"`
		} `json:"reviewers"`
	}
	author.Get(pr, &got)
	for _, rv := range got.Reviewers {
		if rv.User.Username == "alice" && rv.State != "unapproved" {
			t.Fatalf("the approval survived the new target: %+v", got.Reviewers)
		}
	}
	if r := author.Do(http.MethodPost, pr+"/merge", map[string]any{"strategy": "merge"}); r.Status != http.StatusConflict ||
		!strings.Contains(string(r.Body), "required approval") {
		t.Fatalf("merged into the new target with the old approval: %d %s", r.Status, r.Body)
	}
}

// TestMoveNeedsBothProjects: an administrator of a repository cannot move it out of its project — away from the
// project's branch permissions — into a project of their own (audit M7); its build runs move along (audit M13).
func TestMoveNeedsBothProjects(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	bob := env.CreateUser("bob")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	bob.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "codeproject.create",
		"principalType": "user", "principalId": me.User.ID}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CORP", "name": "Corporate"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CORP/repos", map[string]any{"name": "payroll", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/CORP/payroll/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": me.User.ID}, "permission": "repo.admin"}}, nil)
	// A run of the repository, listed with its project.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/CORP/payroll/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": "version: 1\npipelines:\n  ci:\n    on: { push: { branches: [\"**\"] } }\n    stages:\n      - name: Build\n        jobs:\n          - name: build\n            runs-on: [none]\n            steps: [{ run: \"true\" }]\n"}, nil)
	env.WorkJobs()
	bob.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "BOB", "name": "Bob"}, nil)
	if r := bob.Do(http.MethodPatch, "/api/v1/code/repos/CORP/payroll", map[string]any{"project": "BOB"}); r.Status != http.StatusForbidden {
		t.Fatalf("a repository administrator moved the repository out of its project: %d %s", r.Status, r.Body)
	}
	// An administrator of both projects moves it; the audit log says from where to where.
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/BOB/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": me.User.ID}, "permission": "codeproject.admin"}}, nil)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/CORP/payroll", map[string]any{"project": "BOB"}, nil)
	var events []struct {
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=repository.update", &events)
	if len(events) == 0 || events[0].Details["movedFrom"] != "CORP" || events[0].Details["movedTo"] != "BOB" {
		t.Fatalf("audit of the move: %+v", events)
	}
	// Its runs moved along: they are listed (and their jobs claimed) with the new project.
	var stale, runs int
	if err := env.App.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE r.project_id <> repo.project_id), count(*)
		FROM runs r JOIN repositories repo ON repo.id = r.repository_id WHERE repo.slug = 'payroll'`).Scan(&stale, &runs); err != nil {
		t.Fatal(err)
	}
	if runs == 0 || stale != 0 {
		t.Fatalf("runs of the moved repository: %d of %d still in the old project", stale, runs)
	}
}

// TestTasksAreSettledByTheirParties: a task is resolved by its author, the author of the pull request or who can
// write to the repository — not by any reader, since merge checks wait for tasks.
func TestTasksAreSettledByTheirParties(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	author, rita := env.CreateUser("author"), env.CreateUser("rita")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"author": author, "rita": rita} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TSK", "name": "Tasks"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TSK/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/TSK/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": ids["author"]}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": ids["rita"]}, "permission": "repo.read"}}, nil)
	author.MustDo(http.MethodPut, "/api/v1/code/repos/TSK/app/files", map[string]any{"branch": "main", "newBranch": "feature",
		"path": "a.txt", "content": "a\n"}, nil)
	author.MustDo(http.MethodPost, "/api/v1/code/repos/TSK/app/pull-requests", map[string]any{"title": "Feature", "source": "feature"}, nil)
	pr := "/api/v1/code/repos/TSK/app/pull-requests/1"
	type comment struct {
		ID         int64 `json:"id"`
		CanResolve bool  `json:"canResolve"`
	}
	var own, admins comment
	rita.MustDo(http.MethodPost, pr+"/comments", map[string]any{"markdown": "Please add tests", "task": true}, &own)
	admin.MustDo(http.MethodPost, pr+"/comments", map[string]any{"markdown": "Update the docs", "task": true}, &admins)
	resolve := func(c *apitest.Client, id int64) int {
		return c.Do(http.MethodPut, apitest.Fmt("/api/v1/code/pull-request-comments/%d/resolved", id), nil).Status
	}

	// The reader settles her own task, not the administrator's.
	if own.ID == 0 || !own.CanResolve {
		t.Fatalf("the reader's own task: %+v", own)
	}
	if status := resolve(rita, own.ID); status != http.StatusNoContent {
		t.Errorf("the reader resolves her own task: %d", status)
	}
	if status := resolve(rita, admins.ID); status != http.StatusForbidden {
		t.Errorf("the reader resolved another's task: %d", status)
	}
	var timeline []struct {
		Comment *comment `json:"comment"`
	}
	rita.Get(pr+"/timeline", &timeline)
	for _, a := range timeline {
		if a.Comment != nil && a.Comment.ID == admins.ID && a.Comment.CanResolve {
			t.Errorf("the reader is offered another's task")
		}
	}
	// The author of the pull request settles every task of it.
	if status := resolve(author, admins.ID); status != http.StatusNoContent {
		t.Errorf("the author of the pull request resolves a task: %d", status)
	}
	if r := author.Do(http.MethodDelete, apitest.Fmt("/api/v1/code/pull-request-comments/%d/resolved", own.ID), nil); r.Status != http.StatusNoContent {
		t.Errorf("the author of the pull request reopens a task: %d %s", r.Status, r.Body)
	}
}

// TestPushedObjectsAreChecked: pushes are fsck-checked (audit L29). A tree with a ".git" entry — which would write
// into the repository configuration of whoever checks it out — is refused; ordinary history goes in.
func TestPushedObjectsAreChecked(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "FSCK", "name": "Checks"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/FSCK/repos", map[string]any{"name": "app"}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)

	g := newGit(t)
	dir := t.TempDir()
	g.must(dir, "init", "-q")
	write(t, filepath.Join(dir, "README.md"), "# App\n")
	g.must(dir, "add", ".")
	g.must(dir, "commit", "-q", "-m", "Start")
	g.must(dir, "push", u.String(), "main")

	// A tree whose entry is named ".git", written past git's own checks.
	blob := strings.TrimSpace(g.must(dir, "hash-object", "-w", "README.md"))
	raw, _ := hex.DecodeString(blob)
	tree := append([]byte("100644 .git\x00"), raw...)
	treeFile := filepath.Join(t.TempDir(), "tree")
	write(t, treeFile, string(tree))
	treeID := strings.TrimSpace(g.must(dir, "hash-object", "-w", "-t", "tree", "--literally", treeFile))
	commit := strings.TrimSpace(g.must(dir, "commit-tree", treeID, "-p", "HEAD", "-m", "Evil"))
	out, err := g.run(dir, "push", u.String(), commit+":refs/heads/evil")
	if err == nil || !strings.Contains(out, "hasDotgit") {
		t.Fatalf("a tree with .git was accepted: %v\n%s", err, out)
	}
}
