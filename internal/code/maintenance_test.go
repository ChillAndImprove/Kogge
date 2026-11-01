// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestMaintenancePacksRepositories: repositories never pack on their own (gc.auto=0, so that nothing repacks while a
// backup copies them); the daily maintenance packs the repositories pushed to, so loose objects do not pile up.
func TestMaintenancePacksRepositories(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "OPS", "name": "Operations"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/OPS/repos", map[string]any{"name": "infra", "readme": true}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)
	g := newGit(t)
	work := t.TempDir()
	g.must(work, "clone", u.String(), "infra")
	dir := filepath.Join(work, "infra")
	for i := range 3 {
		write(t, filepath.Join(dir, fmt.Sprintf("host%d.conf", i)), fmt.Sprintf("host %d\n", i))
		g.must(dir, "add", ".")
		g.must(dir, "commit", "-m", fmt.Sprintf("Add host %d", i))
	}
	g.must(dir, "push", "origin", "main")

	server := code.RepoDir(env.App.Cfg.DataDir, repo.ID)
	count := func(name string) int {
		m := regexp.MustCompile(`(?m)^` + name + `: (\d+)$`).FindStringSubmatch(g.must(server, "count-objects", "-v"))
		if m == nil {
			t.Fatalf("git count-objects shows no %s", name)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	// A small push arrives as loose objects, and nothing packs them by itself.
	env.WorkJobs()
	if count("count") == 0 {
		t.Fatal("the push left no loose objects")
	}

	if _, err := env.App.Jobs.Insert(ctx, code.MaintenanceArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	env.WorkJobs()
	if n := count("count"); n != 0 {
		t.Errorf("%d loose objects after the maintenance", n)
	}
	if count("packs") == 0 {
		t.Error("the maintenance wrote no pack")
	}
	// The repository works as before.
	if out := g.must(dir, "ls-remote", "origin", "main"); len(out) < 40 {
		t.Errorf("ls-remote after the maintenance: %q", out)
	}
}
