// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type dashboard struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Week    struct {
		Runs          int `json:"runs"`
		Succeeded     int `json:"succeeded"`
		Failed        int `json:"failed"`
		MedianSeconds int `json:"medianSeconds"`
	} `json:"week"`
	Days []struct {
		Day       string `json:"day"`
		Succeeded int    `json:"succeeded"`
		Failed    int    `json:"failed"`
	} `json:"days"`
	Pipelines []struct {
		Repository struct {
			ProjectKey string `json:"projectKey"`
			Slug       string `json:"slug"`
		} `json:"repository"`
		Pipeline string `json:"pipeline"`
		Branch   string `json:"branch"`
		Latest   struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		} `json:"latest"`
		History []struct {
			Number  int    `json:"number"`
			State   string `json:"state"`
			Seconds int    `json:"seconds"`
		} `json:"history"`
	} `json:"pipelines"`
}

// TestDashboard: Helling's start page counts what runs and waits and the runs of the week, and shows the
// pipelines of the default branches with their history — of the repositories the user may read only.
func TestDashboard(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	reader := env.CreateUser("rhea")
	var me, adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	reader.Get("/api/v1/me", &me)
	admin.Get("/api/v1/me", &adminMe)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CI", "name": "Continuous"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CI/repos", map[string]any{"name": "App", "readme": true}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SEC", "name": "Secret"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SEC/repos", map[string]any{"name": "Vault", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/CI/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": me.User.ID}},
	}, nil)

	ctx := context.Background()
	run := func(project, repo string, number int, pipeline, event, branch, state, created, duration string) {
		t.Helper()
		// Finished runs started at created and took duration; running ones started at created.
		_, err := env.App.Pool.Exec(ctx, `INSERT INTO runs (repository_id, project_id, number, pipeline, pipeline_name, event,
				branch, commit_sha, state, created_at, started_at, finished_at)
			SELECT r.id, p.id, $3, $4, initcap($4), $5, $6, repeat('a', 40), $7, now() - $8::interval, now() - $8::interval,
				CASE WHEN $7 IN ('queued', 'running') THEN NULL ELSE now() - $8::interval + $9::interval END
			FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE p.key = $1 AND r.slug = $2`,
			project, repo, number, pipeline, event, branch, state, created, duration)
		if err != nil {
			t.Fatal(err)
		}
	}
	run("CI", "app", 1, "ci", "push", "main", "succeeded", "8 days", "60 seconds")
	run("CI", "app", 2, "ci", "push", "main", "failed", "2 days", "120 seconds")
	run("CI", "app", 3, "ci", "push", "main", "succeeded", "1 day", "30 seconds")
	run("CI", "app", 4, "ci", "push", "main", "running", "1 minute", "0 seconds")
	run("CI", "app", 5, "ci", "push", "feature", "failed", "1 hour", "40 seconds")
	run("CI", "app", 6, "nightly", "schedule", "main", "succeeded", "3 hours", "600 seconds")
	run("CI", "app", 7, "ci", "pull-request", "main", "queued", "1 minute", "0 seconds")
	run("SEC", "vault", 1, "ci", "push", "main", "failed", "1 hour", "10 seconds")

	var d dashboard
	reader.Get("/api/v1/builds/dashboard", &d)
	if d.Running != 1 || d.Queued != 1 {
		t.Errorf("running %d, queued %d", d.Running, d.Queued)
	}
	// The week: #2–#7 of the app; the vault is not the reader's.
	if d.Week.Runs != 6 || d.Week.Succeeded != 2 || d.Week.Failed != 2 || d.Week.MedianSeconds != 80 {
		t.Errorf("week = %+v", d.Week)
	}
	if len(d.Days) != 14 {
		t.Fatalf("%d days", len(d.Days))
	}
	finished := 0
	for _, day := range d.Days {
		finished += day.Succeeded + day.Failed
	}
	if finished != 5 { // #1–#3, #5, #6
		t.Errorf("finished runs per day: %+v", d.Days)
	}
	// Pipelines: those of the default branch, without pull requests, newest first.
	if len(d.Pipelines) != 2 {
		t.Fatalf("pipelines = %+v", d.Pipelines)
	}
	ci, nightly := d.Pipelines[0], d.Pipelines[1]
	if ci.Pipeline != "ci" || ci.Branch != "main" || ci.Repository.ProjectKey != "CI" || ci.Latest.Number != 4 || ci.Latest.State != "running" {
		t.Errorf("ci = %+v", ci)
	}
	if len(ci.History) != 4 || ci.History[0].Number != 1 || ci.History[1].State != "failed" || ci.History[1].Seconds != 120 ||
		ci.History[3].Number != 4 {
		t.Errorf("ci history = %+v", ci.History)
	}
	if nightly.Pipeline != "nightly" || nightly.Latest.State != "succeeded" || len(nightly.History) != 1 {
		t.Errorf("nightly = %+v", nightly)
	}

	// Who may read everything sees the vault too.
	admin.Get("/api/v1/builds/dashboard", &d)
	found := false
	for _, p := range d.Pipelines {
		found = found || p.Repository.ProjectKey == "SEC"
	}
	if !found {
		t.Errorf("the administrator's dashboard lacks the vault: %+v", d.Pipelines)
	}
}
