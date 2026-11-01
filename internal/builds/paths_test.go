// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const pathsPipeline = `version: 1
pipelines:
  web:
    on:
      push: { branches: ["**"], paths: ["web/**"] }
      pull-request: { paths: ["web/**"] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [shell]
            steps:
              - run: make web
  server:
    on:
      push: { branches: ["**"], tags: ["v*"], paths-ignore: ["docs/**", "**/*.md"] }
      pull-request: { paths-ignore: ["docs/**", "**/*.md"] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [shell]
            steps:
              - run: make server
`

// TestPathsFilters: pushes and pull requests start the pipelines whose paths filters match the files they change;
// the others report the status skipped, which merge checks count as done, and which never replaces the status of
// a run at the same commit.
func TestPathsFilters(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MONO", "name": "Monorepo"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MONO/repos", map[string]any{"name": "app", "readme": true}, &repo)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/MONO/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"merge"}, "requiredApprovals": 0, "noNeedsWork": false, "tasksResolved": false,
		"requiredBuilds": 2, "externalStatuses": false, "resetApprovals": false, "deleteSourceBranch": false,
		"defaultReviewers": []int64{}}}, nil)

	commit := func(change map[string]any) string {
		t.Helper()
		var c struct {
			SHA string `json:"sha"`
		}
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/MONO/app/files", change, &c)
		env.WorkJobs()
		return c.SHA
	}
	// runs returns the pipelines of the runs of a commit, as event:pipeline.
	runs := func(sha string) string {
		t.Helper()
		var page struct {
			Items []struct {
				Pipeline string `json:"pipeline"`
				Event    string `json:"event"`
			} `json:"items"`
		}
		admin.Get("/api/v1/code/repos/MONO/app/runs?commit="+sha, &page)
		var out []string
		for _, r := range page.Items {
			out = append(out, r.Event+":"+r.Pipeline)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	// statuses returns the statuses of a commit, as context=state.
	statuses := func(sha string) string {
		t.Helper()
		var got map[string][]struct {
			Context string `json:"context"`
			State   string `json:"state"`
		}
		admin.Get("/api/v1/code/repos/MONO/app/statuses?sha="+sha, &got)
		var out []string
		for _, s := range got[sha] {
			out = append(out, s.Context+"="+s.State)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}

	// Adding the pipeline file changes no file of the web application.
	first := commit(map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": pathsPipeline})
	if got := runs(first); got != "push:server" {
		t.Errorf("runs of the pipeline file = %q", got)
	}
	if got := statuses(first); got != "kogge/server=pending,kogge/web=skipped" {
		t.Errorf("statuses of the pipeline file = %q", got)
	}
	// Documentation only: nothing runs.
	docs := commit(map[string]any{"branch": "main", "path": "docs/guide.md", "content": "# Guide\n"})
	if got := runs(docs); got != "" {
		t.Errorf("runs of documentation = %q", got)
	}
	if got := statuses(docs); got != "kogge/server=skipped,kogge/web=skipped" {
		t.Errorf("statuses of documentation = %q", got)
	}
	web := commit(map[string]any{"branch": "main", "path": "web/app.ts", "content": "export const a = 1;\n"})
	if got := runs(web); got != "push:server,push:web" {
		t.Errorf("runs of the web application = %q", got)
	}

	// A new branch counts what changed since it left the default branch.
	branch := commit(map[string]any{"branch": "main", "newBranch": "docs-update", "path": "docs/faq.md", "content": "# FAQ\n"})
	if got := runs(branch); got != "" {
		t.Errorf("runs of a new branch with documentation = %q", got)
	}
	// A pull request of documentation only: its pipelines are skipped, and the merge check counts them as done.
	var pr struct {
		Number int `json:"number"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/MONO/app/pull-requests", map[string]any{"title": "FAQ", "source": "docs-update"}, &pr)
	env.WorkJobs()
	if got := runs(branch); got != "" {
		t.Errorf("runs of a documentation pull request = %q", got)
	}
	type mergeCheck struct {
		Key     string         `json:"key"`
		OK      bool           `json:"ok"`
		Reason  string         `json:"reason"`
		Values  map[string]int `json:"values"`
		Message string         `json:"message"`
	}
	builds := func() mergeCheck {
		t.Helper()
		var got struct {
			Checks []mergeCheck `json:"checks"`
		}
		admin.Get(apitest.Fmt("/api/v1/code/repos/MONO/app/pull-requests/%d", pr.Number), &got)
		for _, c := range got.Checks {
			if c.Key == "builds" {
				return c
			}
		}
		t.Fatalf("no builds check: %+v", got.Checks)
		return mergeCheck{}
	}
	if c := builds(); !c.OK || c.Reason != "passed" || c.Values["done"] != 2 || c.Values["skipped"] != 2 || c.Values["successful"] != 0 ||
		c.Message != "2 pipelines skipped: no changed file matches their paths." {
		t.Errorf("builds check of a documentation pull request: %+v", c)
	}

	// A change of the web application on the branch: the push counts only it, the pull request everything since
	// the branch left main.
	code := commit(map[string]any{"branch": "docs-update", "path": "web/faq.ts", "content": "export const faq = [];\n"})
	if got := runs(code); got != "pull-request:server,pull-request:web,push:server,push:web" {
		t.Errorf("runs of a web change in the pull request = %q", got)
	}
	if c := builds(); c.OK || c.Reason != "running" || c.Values["running"] != 2 || c.Values["required"] != 2 || !strings.Contains(c.Message, "running") {
		t.Errorf("builds check with running builds: %+v", c)
	}

	// The change taken back: the push changed web/faq.ts and runs; the pull request changes documentation only,
	// and its skipped pipelines leave the statuses of the push's runs alone.
	back := commit(map[string]any{"branch": "docs-update", "path": "web/faq.ts", "delete": true})
	if got := runs(back); got != "push:server,push:web" {
		t.Errorf("runs of the reverted change = %q", got)
	}
	if got := statuses(back); got != "kogge/server=pending,kogge/web=pending" {
		t.Errorf("statuses of the reverted change = %q", got)
	}

	// Tags are not filtered: the tag of the documentation commit runs the pipeline that builds tags.
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)
	work := t.TempDir()
	git(t, work, "clone", u.String(), "app")
	dir := filepath.Join(work, "app")
	git(t, dir, "tag", "v1", docs)
	git(t, dir, "push", "origin", "v1")
	env.WorkJobs()
	if got := runs(docs); got != "push:server" {
		t.Errorf("runs of the tag = %q", got)
	}
}
