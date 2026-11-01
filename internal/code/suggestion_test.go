// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestSuggestions: a reviewer suggests a change of a line; who can write applies it to the source branch with a
// commit that names the reviewer, once, and only while the line is as it was.
func TestSuggestions(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	rita, dev, outsider := env.CreateUser("rita"), env.CreateUser("dev"), env.CreateUser("outsider")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"rita": rita, "dev": dev, "admin": admin} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SUG", "name": "Suggestions"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SUG/repos", map[string]any{"name": "calc", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/SUG/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": ids["admin"]}, "permission": "codeproject.admin"},
		{"holder": map[string]any{"type": "user", "id": ids["dev"]}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": ids["rita"]}, "permission": "repo.read"}}, nil)
	files := "/api/v1/code/repos/SUG/calc/files"
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": "calc.go",
		"content": "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n"}, nil)
	dev.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "newBranch": "fix", "path": "calc.go",
		"content": "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b // wrong?\n}\n\nfunc Sub(a, b int) int {\n\treturn a - b\n}\n"}, nil)
	env.WorkJobs()
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/SUG/calc/pull-requests", map[string]any{"title": "Fix", "source": "fix"}, nil)
	pr := "/api/v1/code/repos/SUG/calc/pull-requests/1"

	type comment struct {
		ID         int64 `json:"id"`
		Suggestion *struct {
			Lines   []string `json:"lines"`
			Applied *struct {
				Commit string `json:"commit"`
				By     struct {
					Username string `json:"username"`
				} `json:"by"`
			} `json:"applied"`
		} `json:"suggestion"`
		CanApply bool `json:"canApply"`
	}
	suggest := func(c *apitest.Client, line int, side, markdown string) comment {
		t.Helper()
		var out comment
		c.MustDo(http.MethodPost, pr+"/comments", map[string]any{"path": "calc.go", "line": line, "side": side, "markdown": markdown}, &out)
		return out
	}
	byID := func(c *apitest.Client, id int64) comment {
		t.Helper()
		var timeline []struct {
			Comment *comment `json:"comment"`
		}
		c.Get(pr+"/timeline", &timeline)
		for _, a := range timeline {
			if a.Comment != nil && a.Comment.ID == id {
				return *a.Comment
			}
		}
		t.Fatalf("comment %d not in the timeline", id)
		return comment{}
	}
	apply := func(c *apitest.Client, id int64) *apitest.Response {
		return c.Do(http.MethodPost, apitest.Fmt("/api/v1/code/pull-request-comments/%d/apply", id), nil)
	}
	fileAt := func(ref string) string {
		t.Helper()
		r := admin.Do(http.MethodGet, "/api/v1/code/repos/SUG/calc/raw?ref="+ref+"&path=calc.go", nil)
		if r.Status != http.StatusOK {
			t.Fatalf("raw: %d", r.Status)
		}
		return string(r.Body)
	}

	s := suggest(rita, 4, "new", "This should add:\n\n```suggestion\n\treturn a + b\n```")
	if s.Suggestion == nil || strings.Join(s.Suggestion.Lines, "|") != "\treturn a + b" || s.CanApply {
		t.Fatalf("suggestion as rita sees it: %+v", s)
	}
	if c := byID(dev, s.ID); !c.CanApply {
		t.Errorf("dev cannot apply: %+v", c)
	}
	// Readers cannot apply; who cannot read the repository does not learn of the comment.
	if r := apply(rita, s.ID); r.Status != http.StatusForbidden {
		t.Errorf("rita applied: %d %s", r.Status, r.Body)
	}
	if r := apply(outsider, s.ID); r.Status != http.StatusNotFound {
		t.Errorf("an outsider: %d %s", r.Status, r.Body)
	}
	var commit struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
	}
	r := apply(dev, s.ID)
	if r.Status != http.StatusOK {
		t.Fatalf("apply: %d %s", r.Status, r.Body)
	}
	r.Decode(&commit)
	if !strings.Contains(fileAt("fix"), "func Add(a, b int) int {\n\treturn a + b\n}") || !strings.Contains(fileAt("fix"), "func Sub") {
		t.Errorf("file after applying:\n%s", fileAt("fix"))
	}
	if !strings.Contains(commit.Message, "Apply suggestion to calc.go") || !strings.Contains(commit.Message, "Co-authored-by: Rita <rita@example.com>") {
		t.Errorf("commit message: %q", commit.Message)
	}
	env.WorkJobs()
	if c := byID(dev, s.ID); c.Suggestion == nil || c.Suggestion.Applied == nil || c.Suggestion.Applied.Commit != commit.SHA ||
		c.Suggestion.Applied.By.Username != "dev" || c.CanApply {
		t.Errorf("after applying: %+v", c)
	}
	if r := apply(dev, s.ID); r.Status != http.StatusConflict || r.Code() != "suggestion.applied" {
		t.Errorf("applied twice: %d %s", r.Status, r.Body)
	}

	// The line changed after the suggestion: it is outdated, before the push is processed and after.
	early := suggest(rita, 8, "new", "```suggestion\n\treturn a - b // subtract\n```")
	dev.MustDo(http.MethodPut, files, map[string]any{"branch": "fix", "path": "calc.go",
		"content": strings.Replace(fileAt("fix"), "func Sub(a, b int) int {\n\treturn a - b", "func Sub(a, b int) int {\n\treturn -(b - a)", 1)}, nil)
	if r := apply(dev, early.ID); r.Status != http.StatusConflict || r.Code() != "suggestion.outdated" {
		t.Errorf("a changed line, before the push was processed: %d %s", r.Status, r.Body)
	}
	env.WorkJobs()
	if c := byID(dev, early.ID); c.CanApply {
		t.Errorf("an outdated suggestion can be applied: %+v", c)
	}
	if r := apply(dev, early.ID); r.Status != http.StatusConflict || r.Code() != "suggestion.outdated" {
		t.Errorf("a changed line: %d %s", r.Status, r.Body)
	}

	// Removing a line, as the author of the suggestion; the old side and replies suggest nothing.
	remove := suggest(dev, 2, "new", "```suggestion\n```")
	if r := apply(dev, remove.ID); r.Status != http.StatusOK {
		t.Fatalf("remove a line: %d %s", r.Status, r.Body)
	}
	if got := fileAt("fix"); strings.HasPrefix(got, "package calc\n\n") || !strings.HasPrefix(got, "package calc\nfunc Add") {
		t.Errorf("file after removing line 2:\n%s", got)
	}
	r.Decode(&commit)
	old := suggest(rita, 4, "old", "```suggestion\n\treturn b + a\n```")
	if old.Suggestion != nil {
		t.Errorf("the old side suggests: %+v", old)
	}
	if r := apply(dev, old.ID); r.Status != http.StatusUnprocessableEntity || r.Code() != "suggestion.none" {
		t.Errorf("old side: %d %s", r.Status, r.Body)
	}
	var reply comment
	rita.MustDo(http.MethodPost, pr+"/comments", map[string]any{"parentId": s.ID, "markdown": "```suggestion\nx\n```"}, &reply)
	if reply.Suggestion != nil {
		t.Errorf("a reply suggests: %+v", reply)
	}

	// Closed pull requests take no suggestions.
	late := suggest(rita, 4, "new", "```suggestion\n\treturn b + a\n```")
	env.WorkJobs()
	dev.MustDo(http.MethodPost, pr+"/decline", nil, nil)
	if r := apply(dev, late.ID); r.Status != http.StatusConflict || r.Code() != "pull-request.closed" {
		t.Errorf("declined: %d %s", r.Status, r.Body)
	}
}
