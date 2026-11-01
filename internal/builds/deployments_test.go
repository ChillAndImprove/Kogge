// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const stagingPipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Deploy
        jobs:
          - name: staging
            runs-on: [shell]
            environment: staging
            steps:
              - run: echo deploy
`

// TestDeploymentPages: the history of an environment comes in pages with its length, newest first, and each
// deployment keeps its own approvals.
func TestDeploymentPages(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	ctx := context.Background()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "OPS", "name": "Operations"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/OPS/repos", map[string]any{"name": "site", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/OPS/site/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": stagingPipeline, "message": "Deploy the site"}, nil)
	env.WorkJobs()
	// The push's run waits for an agent with its deployment; 54 earlier deployments, one a minute, make the history.
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO deployments (environment_id, run_id, job_id, commit_sha, ref, state, created_by, created_at, finished_at)
		SELECT environment_id, run_id, job_id, commit_sha, ref, 'succeeded', created_by, created_at - g * interval '1 minute', created_at
		FROM deployments, generate_series(1, 54) AS g`); err != nil {
		t.Fatal(err)
	}
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	var approved int64
	if err := env.App.Pool.QueryRow(ctx, `INSERT INTO deployment_approvals (deployment_id, user_id, approved, comment)
		SELECT id, $1, true, 'fine' FROM deployments ORDER BY created_at LIMIT 1 RETURNING deployment_id`, me.User.ID).Scan(&approved); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Total int `json:"total"`
		Items []struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			Created   string `json:"created"`
			Approvals []struct {
				User struct {
					ID int64 `json:"id"`
				} `json:"user"`
				Comment string `json:"comment"`
			} `json:"approvals"`
		} `json:"items"`
	}
	path := "/api/v1/code/repos/OPS/site/environments/staging/deployments"
	seen := map[int64]bool{}
	last := ""
	for _, offset := range []int{0, 20, 40} {
		var p page
		admin.Get(apitest.Fmt(path+"?limit=20&offset=%d", offset), &p)
		want := 20
		if offset == 40 {
			want = 15
		}
		if p.Total != 55 || len(p.Items) != want {
			t.Fatalf("page at %d: total %d, %d deployments", offset, p.Total, len(p.Items))
		}
		for _, d := range p.Items {
			if seen[d.ID] {
				t.Fatalf("deployment %d on two pages", d.ID)
			}
			seen[d.ID] = true
			if last != "" && d.Created > last {
				t.Fatalf("not newest first: %s after %s", d.Created, last)
			}
			last = d.Created
			if (d.ID == approved) != (len(d.Approvals) == 1) || (d.ID == approved && d.Approvals[0].User.ID != me.User.ID) {
				t.Fatalf("approvals of deployment %d: %+v", d.ID, d.Approvals)
			}
		}
	}
	var end page
	admin.Get(path+"?offset=100", &end)
	if end.Total != 55 || len(end.Items) != 0 {
		t.Fatalf("beyond the end: %+v", end)
	}
	if r := admin.Do(http.MethodGet, path+"?limit=501", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("limit 501: %d", r.Status)
	}
	// Who may not read the repository learns nothing about its environments.
	if r := env.CreateUser("otto").Do(http.MethodGet, path, nil); r.Status != http.StatusNotFound {
		t.Fatalf("history for a stranger: %d", r.Status)
	}
}
