// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const deployPipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Deploy
        jobs:
          - name: production
            runs-on: [shell]
            environment: production
            steps:
              - run: echo deploy
`

// TestDevelopmentBuilds: the development section of an issue shows the latest runs of its commits and the
// environments whose current deployment contains one of them.
func TestDevelopmentBuilds(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	ctx := context.Background()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Checkout"}, &issue)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": deployPipeline, "message": issue.Key + " Deploy the shop"}, nil)
	env.WorkJobs()

	// The deployment of run 1 succeeded (no agent needed for the test).
	if _, err := env.App.Pool.Exec(ctx, "UPDATE deployments SET state = 'succeeded', finished_at = now()"); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Builds []struct {
			Run struct {
				Number int    `json:"number"`
				State  string `json:"state"`
			} `json:"run"`
		} `json:"builds"`
		Deployments []struct {
			Environment string `json:"environment"`
			RunNumber   int    `json:"runNumber"`
		} `json:"deployments"`
	}
	admin.Get("/api/v1/issues/"+issue.Key+"/development", &d)
	if len(d.Builds) != 1 || d.Builds[0].Run.Number != 1 {
		t.Errorf("builds = %+v", d.Builds)
	}
	if len(d.Deployments) != 1 || d.Deployments[0].Environment != "production" || d.Deployments[0].RunNumber != 1 {
		t.Errorf("deployments = %+v", d.Deployments)
	}

	// A later commit without the key, deployed: the environment still contains the issue's commit.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "path": "x.txt", "content": "x\n"}, nil)
	env.WorkJobs()
	if _, err := env.App.Pool.Exec(ctx, "UPDATE deployments SET state = 'succeeded', finished_at = now() WHERE state <> 'succeeded'"); err != nil {
		t.Fatal(err)
	}
	admin.Get("/api/v1/issues/"+issue.Key+"/development", &d)
	if len(d.Deployments) != 1 || d.Deployments[0].RunNumber != 2 {
		t.Errorf("deployments after a later run = %+v", d.Deployments)
	}

	// The version of the issue shows that production runs it.
	var version struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &version)
	admin.MustDo(http.MethodPatch, "/api/v1/issues/"+issue.Key, map[string]any{"fields": map[string]any{"fixVersions": []int64{version.ID}}}, nil)
	var vd []struct {
		Environment string   `json:"environment"`
		Deployed    []string `json:"deployed"`
		Pending     []string `json:"pending"`
	}
	admin.Get(apitest.Fmt("/api/v1/projects/SHOP/versions/%d/deployments", version.ID), &vd)
	if len(vd) != 1 || vd[0].Environment != "production" || len(vd[0].Deployed) != 1 || vd[0].Deployed[0] != issue.Key || len(vd[0].Pending) != 0 {
		t.Errorf("version deployments = %+v", vd)
	}
}
