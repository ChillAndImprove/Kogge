// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestDevelopmentLimitsAfterAccess: the development section lists the newest commits the reader may see, with
// their total; commits in repositories the reader may not read neither take their places nor count.
func TestDevelopmentLimitsAfterAccess(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	rita := env.CreateUser("rita")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"admin": admin, "rita": rita} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	var roles []struct {
		Role struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"role"`
	}
	admin.Get("/api/v1/projects/SHOP/roles", &roles)
	for _, r := range roles {
		if r.Role.Name == "Developers" || r.Role.Name == "Entwickler" {
			admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/SHOP/roles/%d/members", r.Role.ID), map[string]any{"type": "user",
				"id": ids["rita"]}, nil)
		}
	}
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Checkout"}, nil)

	// An open repository Rita may read and a secret one with many newer commits that name the issue.
	repos := map[string]int64{}
	for key, readers := range map[string][]map[string]any{
		"OPEN": {{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": ids["rita"]}}},
		"SEC":  {},
	} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": key, "name": key}, nil)
		var repo struct {
			ID int64 `json:"id"`
		}
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/"+key+"/repos", map[string]any{"name": "app"}, &repo)
		repos[key] = repo.ID
		admin.MustDo(http.MethodPut, "/api/v1/code/projects/"+key+"/permissions", append([]map[string]any{
			{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": ids["admin"]}}}, readers...), nil)
	}
	ctx := context.Background()
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO issue_commits (issue_key, repository_id, sha, message, committed_at)
		SELECT 'SHOP-1', $1::bigint, md5('secret' || g), 'SHOP-1 secret change ' || g, now() - g * interval '1 minute' FROM generate_series(1, 205) AS g
		UNION ALL SELECT 'SHOP-1', $2::bigint, md5('open' || g), 'SHOP-1 open change ' || g, now() - interval '1 day' - g * interval '1 minute'
		FROM generate_series(1, 3) AS g`, repos["SEC"], repos["OPEN"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO issue_branches (issue_key, repository_id, branch) VALUES
		('SHOP-1', $1, 'feature/SHOP-1-secret'), ('SHOP-1', $2, 'feature/SHOP-1-open')`, repos["SEC"], repos["OPEN"]); err != nil {
		t.Fatal(err)
	}

	type development struct {
		Branches []struct {
			Name string `json:"name"`
		} `json:"branches"`
		BranchesTotal int `json:"branchesTotal"`
		Commits       []struct {
			Subject    string `json:"subject"`
			Repository struct {
				ID int64 `json:"id"`
			} `json:"repository"`
		} `json:"commits"`
		CommitsTotal int `json:"commitsTotal"`
	}
	var d development
	rita.Get("/api/v1/issues/SHOP-1/development", &d)
	if len(d.Commits) != 3 || d.CommitsTotal != 3 || d.Commits[0].Subject != "SHOP-1 open change 1" {
		t.Fatalf("rita's commits: %d of %d, %+v", len(d.Commits), d.CommitsTotal, d.Commits)
	}
	if len(d.Branches) != 1 || d.BranchesTotal != 1 || d.Branches[0].Name != "feature/SHOP-1-open" {
		t.Fatalf("rita's branches: %+v (%d)", d.Branches, d.BranchesTotal)
	}
	admin.Get("/api/v1/issues/SHOP-1/development", &d)
	if len(d.Commits) != 200 || d.CommitsTotal != 208 || d.BranchesTotal != 2 {
		t.Fatalf("the administrator's commits: %d of %d, branches %d", len(d.Commits), d.CommitsTotal, d.BranchesTotal)
	}
}
