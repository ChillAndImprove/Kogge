// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestWritesWaitForBackups: while a backup holds the Git write lock, pushes and changes in the browser wait and
// reads continue.
func TestWritesWaitForBackups(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "core", "readme": true}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)
	g := newGit(t)
	work := t.TempDir()
	g.must(work, "clone", u.String(), "core")
	dir := filepath.Join(work, "core")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	g.must(dir, "add", ".")
	g.must(dir, "commit", "-m", "Add a")

	backup, err := pgx.ConnectConfig(ctx, env.App.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close(ctx)
	if _, err := backup.Exec(ctx, "SELECT pg_advisory_lock($1)", db.LockGitWrites); err != nil {
		t.Fatal(err)
	}
	pushed := make(chan string, 1)
	go func() {
		out, err := g.run(dir, "push", "origin", "main")
		if err != nil {
			out = "failed: " + out
		}
		pushed <- out
	}()
	edited := make(chan *apitest.Response, 1)
	go func() {
		edited <- admin.Do(http.MethodPut, "/api/v1/code/repos/PLAT/core/files", map[string]any{"branch": "main", "newBranch": "docs",
			"path": "b.txt", "content": "b\n"})
	}()

	// Reads continue.
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/PLAT/core/raw?ref=main&path=README.md", nil); r.Status != http.StatusOK {
		t.Fatalf("read during the backup: %d %s", r.Status, r.Body)
	}
	g.must(work, "clone", u.String(), "copy")
	select {
	case out := <-pushed:
		t.Fatalf("the push did not wait: %s", out)
	case r := <-edited:
		t.Fatalf("the edit did not wait: %d %s", r.Status, r.Body)
	case <-time.After(time.Second):
	}

	if _, err := backup.Exec(ctx, "SELECT pg_advisory_unlock($1)", db.LockGitWrites); err != nil {
		t.Fatal(err)
	}
	if out := <-pushed; strings.HasPrefix(out, "failed") {
		t.Fatalf("push after the backup: %s", out)
	}
	if r := <-edited; r.Status != http.StatusOK {
		t.Fatalf("edit after the backup: %d %s", r.Status, r.Body)
	}
	var branches struct {
		Total int `json:"total"`
	}
	admin.Get("/api/v1/code/repos/PLAT/core/branches", &branches)
	if branches.Total != 2 {
		t.Errorf("branches = %+v", branches)
	}
}

// TestAdministratorsAndContent: administrators manage code projects without reading their content; they grant
// themselves access explicitly. Through access tokens, administration needs the
// admin scope.
func TestAdministratorsAndContent(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("dev")
	var devMe, adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	dev.Get("/api/v1/me", &devMe)
	admin.Get("/api/v1/me", &adminMe)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "codeproject.create",
		"principalType": "user", "principalId": devMe.User.ID}, nil)
	dev.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SECRET", "name": "Secret"}, nil)
	dev.MustDo(http.MethodPost, "/api/v1/code/projects/SECRET/repos", map[string]any{"name": "core", "readme": true}, nil)

	if r := admin.Do(http.MethodGet, "/api/v1/code/projects/SECRET", nil); r.Status != http.StatusOK {
		t.Errorf("the administrator does not see the project: %d", r.Status)
	}
	for _, path := range []string{"/api/v1/code/repos/SECRET/core", "/api/v1/code/repos/SECRET/core/raw?ref=main&path=README.md"} {
		if r := admin.Do(http.MethodGet, path, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s: the administrator reads content without a grant: %d", path, r.Status)
		}
	}
	grants := []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": devMe.User.ID}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
	}
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "no admin", "scopes": []string{"read", "write"}}, &token)
	if r := env.WithToken(token.Token).Do(http.MethodPut, "/api/v1/code/projects/SECRET/permissions", grants); r.Status < 400 {
		t.Errorf("a token without the admin scope changed permissions: %d", r.Status)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/SECRET/permissions", grants, nil)
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/SECRET/core/raw?ref=main&path=README.md", nil); r.Status != http.StatusOK {
		t.Errorf("read after the grant: %d %s", r.Status, r.Body)
	}
}

// TestRawSVG: SVG from repositories shows inline only as an embedded image; opened as a page it is downloaded.
func TestRawSVG(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SVG", "name": "Images"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SVG/repos", map[string]any{"name": "art", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SVG/art/files", map[string]any{"branch": "main", "path": "logo.svg",
		"content": `<svg xmlns="http://www.w3.org/2000/svg"><a href="https://evil.example"><text y="20">Sign in again</text></a></svg>`}, nil)
	path := "/api/v1/code/repos/SVG/art/raw?ref=main&path=logo.svg"
	if r := admin.Do(http.MethodGet, path, nil, "Sec-Fetch-Dest", "image"); !strings.HasPrefix(r.Header.Get("Content-Disposition"), "inline") {
		t.Errorf("embedded image: %q", r.Header.Get("Content-Disposition"))
	}
	for _, dest := range []string{"document", ""} {
		r := admin.Do(http.MethodGet, path, nil, "Sec-Fetch-Dest", dest)
		if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") || r.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("opened as %q: %q", dest, r.Header.Get("Content-Disposition"))
		}
	}
}
