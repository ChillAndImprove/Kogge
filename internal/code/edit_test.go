// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

func TestEditInBrowser(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "WEB", "name": "Web"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/WEB/repos", map[string]any{"name": "Site"}, nil) // empty

	type commit struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
		Parents []string
	}
	var first commit
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "path": ".kogge/pipelines.yml", "content": "version: 1\npipelines: {}\n", "message": "Add a pipeline",
	}, &first)
	if first.Subject != "Add a pipeline" || len(first.Parents) != 0 {
		t.Fatalf("first commit = %+v", first)
	}
	var file struct {
		Lines []string `json:"lines"`
	}
	admin.Get("/api/v1/code/repos/WEB/site/file?ref=main&path=.kogge/pipelines.yml", &file)
	if len(file.Lines) != 2 {
		t.Fatalf("file = %+v", file)
	}

	var second commit
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "path": "docs/readme.md", "content": "# Docs\n", "base": first.SHA,
	}, &second)
	if second.Subject != "Update docs/readme.md" || len(second.Parents) != 1 || second.Parents[0] != first.SHA {
		t.Fatalf("second commit = %+v", second)
	}
	// An edit based on an older commit is refused.
	if res := admin.Do(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "path": "docs/readme.md", "content": "# Old\n", "base": first.SHA,
	}); res.Code() != "file.stale" {
		t.Errorf("stale edit: %d %s", res.Status, res.Body)
	}
	for _, bad := range []string{"../x", ".git/config", "a//b", "x/./y"} {
		if res := admin.Do(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{"branch": "main", "path": bad, "content": "x"}); res.Code() != "file.path" {
			t.Errorf("path %q: %d %s", bad, res.Status, res.Body)
		}
	}
	if res := admin.Do(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "path": "docs/readme.md", "content": "# Docs\n",
	}); res.Code() != "file.unchanged" {
		t.Errorf("unchanged: %d %s", res.Status, res.Body)
	}

	// Branch permissions apply: main takes pull requests only; a new branch works.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WEB/site/branch-permissions", []map[string]any{
		{"pattern": "main", "kind": "pull-request-only", "exemptUsers": []int64{}, "exemptGroups": []int64{}},
	}, nil)
	if res := admin.Do(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "path": "docs/readme.md", "content": "# Changed\n",
	}); res.Code() != "file.denied" {
		t.Errorf("protected branch: %d %s", res.Status, res.Body)
	}
	var onBranch commit
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "main", "newBranch": "docs-update", "path": "docs/readme.md", "content": "# Changed\n",
	}, &onBranch)
	if len(onBranch.Parents) != 1 || onBranch.Parents[0] != second.SHA {
		t.Errorf("new branch commit = %+v", onBranch)
	}
	var deleted commit
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{
		"branch": "docs-update", "path": "docs/readme.md", "delete": true,
	}, &deleted)
	if deleted.Subject != "Delete docs/readme.md" {
		t.Errorf("delete = %+v", deleted)
	}
	if res := admin.Do(http.MethodGet, "/api/v1/code/repos/WEB/site/file?ref=docs-update&path=docs/readme.md", nil); res.Status != http.StatusNotFound {
		t.Errorf("deleted file: %d", res.Status)
	}

	// Readers cannot commit.
	reader := env.CreateUser("reader")
	if res := reader.Do(http.MethodPut, "/api/v1/code/repos/WEB/site/files", map[string]any{"branch": "main", "path": "x", "content": "x"}); res.Status != http.StatusNotFound {
		t.Errorf("reader: %d", res.Status)
	}
}
