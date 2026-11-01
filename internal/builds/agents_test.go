// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const protectedAgentPipeline = `version: 1
pipelines:
  ci:
    on:
      push: { branches: ["**"] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
`

// TestProtectedAgents: a protected agent takes only jobs of refs that a branch permission of the code project
// protects — as they are when it claims the job — while other agents take any job.
func TestProtectedAgents(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PA", "name": "Protected agents"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PA/repos", map[string]any{"name": "app", "readme": true}, nil)
	protect := func(patterns ...string) {
		rules := []map[string]any{}
		for _, p := range patterns {
			rules = append(rules, map[string]any{"pattern": p, "kind": "restrict-push", "exemptUsers": []int64{me.User.ID}, "exemptGroups": []int64{}})
		}
		admin.MustDo(http.MethodPut, "/api/v1/code/projects/PA/branch-permissions", rules, nil)
	}
	protect("main", "release-*")

	trusted := newFakeAgent(t, env)
	var agents []struct {
		ID        int64 `json:"id"`
		Protected bool  `json:"protected"`
	}
	admin.Get("/api/v1/admin/agents", &agents)
	if len(agents) != 1 || agents[0].Protected {
		t.Fatalf("agents = %+v", agents)
	}
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/agents/%d", agents[0].ID), map[string]any{"protected": true}, nil)
	admin.Get("/api/v1/admin/agents", &agents)
	if !agents[0].Protected {
		t.Fatal("the agent is not protected")
	}
	other := newFakeAgent(t, env)

	commit := func(branch, newBranch, path string) {
		t.Helper()
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/PA/app/files", map[string]any{"branch": branch, "newBranch": newBranch,
			"path": path, "content": protectedAgentPipeline}, nil)
		env.WorkJobs()
	}
	// Run 1 on an open branch, run 2 on main: the protected agent passes over the first.
	commit("main", "feature", ".kogge/pipelines.yml")
	commit("feature", "", "a.txt")
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PA/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": protectedAgentPipeline + "# main\n"}, nil)
	env.WorkJobs()
	if p := trusted.claim(); p.Branch != "main" {
		t.Errorf("the protected agent took a job of %q", p.Branch)
	}
	if p := other.claim(); p.Branch != "feature" {
		t.Errorf("the other agent took a job of %q", p.Branch)
	}
	if p := other.claim(); p.Branch != "feature" {
		t.Errorf("the other agent took a job of %q", p.Branch)
	}

	// main loses its protection after its run was created: the protected agent leaves the run to others and
	// takes the release branch's.
	commit("main", "", "b.txt")
	commit("main", "release-1", "c.txt")
	protect("release-*")
	if p := trusted.claim(); p.Branch != "release-1" {
		t.Errorf("the protected agent took a job of %q after main lost its protection", p.Branch)
	}
	if p := other.claim(); p.Branch != "main" {
		t.Errorf("the other agent took a job of %q", p.Branch)
	}

	// Changing the protection is audited.
	var events []struct {
		Action  string         `json:"action"`
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=agent.protect", &events)
	if len(events) != 1 || events[0].Details["protected"] != true {
		t.Errorf("audit events = %+v", events)
	}
}
