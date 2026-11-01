// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestImportRepository imports a repository from another Git server (here: a repository of the same Kogge) with
// its branches and tags; wrong credentials fail the import, which can be started again; forbidden addresses are
// refused.
func TestImportRepository(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SRC", "name": "Source"}, nil)
	var source repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SRC/repos", map[string]any{"name": "origin", "readme": true,
		"defaultBranch": "trunk"}, &source)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SRC/origin/files", map[string]any{"branch": "trunk", "newBranch": "feature",
		"path": "f.txt", "content": "feature\n"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/SRC/origin/branches", map[string]any{"name": "release", "from": "trunk"}, nil)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "import", "scopes": []string{"read"}}, &token)

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "DST", "name": "Destination"}, nil)
	importInto := func(name, url, password string) repository {
		t.Helper()
		var r repository
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/DST/repos", map[string]any{"name": name, "importUrl": url,
			"importUsername": "admin", "importPassword": password}, &r)
		env.WorkJobs()
		admin.Get("/api/v1/code/repos/DST/"+r.Slug, &r)
		return r
	}

	copied := importInto("copy", source.CloneHTTP, token.Token)
	if copied.Import == nil || copied.Import.State != "done" || copied.DefaultBranch != "trunk" {
		t.Fatalf("import: %+v", copied.Import)
	}
	var branches struct {
		Total int `json:"total"`
	}
	admin.Get("/api/v1/code/repos/DST/copy/branches", &branches)
	if branches.Total != 3 {
		t.Errorf("branches = %d", branches.Total)
	}
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/DST/copy/raw?ref=feature&path=f.txt", nil); r.Status != http.StatusOK ||
		string(r.Body) != "feature\n" {
		t.Errorf("file of the imported branch: %d %q", r.Status, r.Body)
	}
	var protocol string
	var secret *string
	if err := env.App.Pool.QueryRow(ctx, `SELECT p.protocol, r.import_secret FROM pushes p JOIN repositories r ON r.id = p.repository_id
		WHERE r.slug = 'copy'`).Scan(&protocol, &secret); err != nil || protocol != "import" || secret != nil {
		t.Errorf("push %q, secret kept %v: %v", protocol, secret != nil, err)
	}

	// Wrong credentials fail the import; it starts again with the right ones.
	failed := importInto("second", source.CloneHTTP, "wrong-token")
	if failed.Import == nil || failed.Import.State != "failed" || failed.Import.Error == "" ||
		strings.Contains(failed.Import.Error, "wrong-token") {
		t.Fatalf("failed import: %+v", failed.Import)
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/DST/second/import", map[string]any{"username": "admin", "password": token.Token}, nil)
	env.WorkJobs()
	admin.Get("/api/v1/code/repos/DST/second", &failed)
	if failed.Import == nil || failed.Import.State != "done" {
		t.Errorf("retried import: %+v", failed.Import)
	}

	// Metadata and link-local addresses are never connected to.
	forbidden := importInto("metadata", "http://169.254.169.254/latest/meta-data.git", "")
	if forbidden.Import == nil || forbidden.Import.State != "failed" {
		t.Errorf("import from a metadata address: %+v", forbidden.Import)
	}
	if r := admin.Do(http.MethodPost, "/api/v1/code/projects/DST/repos", map[string]any{"name": "local", "importUrl": "file:///etc"}); r.Code() !=
		"repository.import-url" {
		t.Errorf("file URL: %d %s", r.Status, r.Body)
	}

	// Internal addresses only when the administrator allows them, as for webhooks; whether an address is not
	// allowed, does not answer or refuses is not told apart (audit M3).
	internal := importInto("internal", "http://10.255.255.1/team/repo.git", "")
	if internal.Import == nil || internal.Import.State != "failed" ||
		!strings.Contains(internal.Import.Error, "could not be reached, or Kogge may not connect to it") {
		t.Fatalf("import from an internal address: %+v", internal.Import)
	}
	closed := importInto("closed", "http://127.0.0.1:1/team/repo.git", "")
	reason := func(r repository) string { _, after, _ := strings.Cut(r.Import.Error, ".git: "); return after }
	if closed.Import == nil || reason(closed) != reason(internal) {
		t.Fatalf("a closed port is told apart from a forbidden address: %q vs %q", closed.Import.Error, internal.Import.Error)
	}
}
