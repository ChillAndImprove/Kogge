// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

const scheduledPipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: [main] } }
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [shell]
            steps:
              - run: echo hi
  nightly:
    on:
      schedule:
        - cron: "* * * * *"
          branch: main
    stages:
      - name: Check
        jobs:
          - name: check
            runs-on: [shell]
            steps:
              - run: echo nightly
`

func runJob(t *testing.T, env *apitest.Env, args river.JobArgs) {
	t.Helper()
	if _, err := env.App.Jobs.Insert(context.Background(), args, nil); err != nil {
		t.Fatal(err)
	}
	env.WorkJobs()
}

// TestBackgroundJobs covers the periodic work: lost agents, schedules, retention.
func TestBackgroundJobs(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	pool := env.App.Pool
	ctx := context.Background()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "BG", "name": "Background"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/BG/repos", map[string]any{"name": "App", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/BG/app/files", map[string]any{
		"branch": "main", "path": ".kogge/pipelines.yml", "content": scheduledPipeline,
	}, nil)
	env.WorkJobs()

	// An agent claims the job and disappears.
	var reg struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"autoApprove": true}, &reg)
	cfg := &agent.Config{Server: env.URL, Name: "ghost", WorkDir: t.TempDir(), Shell: true}
	if err := agent.Register(ctx, cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	client := agent.NewClient(env.URL, cfg.Secret, "test", nil)
	job, err := client.Claim(ctx, protocol.ClaimRequest{Labels: cfg.AllLabels(), OS: "linux", Arch: "amd64", Version: "test"})
	if err != nil || job == nil || job.Job.Name != "compile" {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE run_jobs SET lease_until = now() - interval '1 minute' WHERE id = $1", job.ID); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.ReaperArgs{})
	var run runDetail
	admin.Get("/api/v1/code/repos/BG/app/runs/1", &run)
	if c := run.job("compile"); c.State != "failed" || c.Failure != "The agent stopped responding." || run.State != "failed" {
		t.Errorf("after the reaper: run %s, job %+v", run.State, c)
	}
	// The user who pushed learns about the failure.
	var inbox []struct {
		Event string `json:"event"`
		Title string `json:"title"`
	}
	admin.Get("/api/v1/notifications", &inbox)
	if len(inbox) == 0 || inbox[0].Event != "run.failed" || inbox[0].Title != "BG/app #1 ci" {
		t.Errorf("inbox = %+v", inbox)
	}
	// Its token no longer works.
	if _, err := client.Job(job).Heartbeat(ctx); err == nil {
		t.Error("the token of a lost job still works")
	}

	// A job past its deadline fails, too.
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/retry", run.job("compile").ID), nil, nil)
	job, err = client.Claim(ctx, protocol.ClaimRequest{Labels: cfg.AllLabels(), OS: "linux", Arch: "amd64", Version: "test"})
	if err != nil || job == nil || job.Attempt != 2 {
		t.Fatalf("second claim: %+v, %v", job, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE run_jobs SET deadline_at = now() - interval '10 minutes' WHERE id = $1", job.ID); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.ReaperArgs{})
	admin.Get("/api/v1/code/repos/BG/app/runs/1", &run)
	if c := run.job("compile"); c.State != "failed" || !strings.Contains(c.Failure, "time limit") {
		t.Errorf("after the deadline: %+v", c)
	}

	// Schedules: due minutes since the last run start the pipeline once.
	var schedules int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM build_schedules").Scan(&schedules); err != nil || schedules != 1 {
		t.Fatalf("schedules = %d, %v", schedules, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE build_schedules SET last_run_at = now() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.ScheduleArgs{})
	runJob(t, env, builds.ScheduleArgs{})
	var runs struct {
		Items []struct {
			Number   int    `json:"number"`
			Event    string `json:"event"`
			Pipeline string `json:"pipeline"`
		} `json:"items"`
	}
	admin.Get("/api/v1/code/repos/BG/app/runs?pipeline=nightly", &runs)
	if len(runs.Items) != 1 || runs.Items[0].Event != "schedule" {
		t.Errorf("scheduled runs = %+v", runs.Items)
	}

	// Retention: expired artifacts and their blob references go.
	if _, err := pool.Exec(ctx, `INSERT INTO blobs (sha256, size, content_type) VALUES ('deadbeef', 1, 'application/zip') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	var artifactID int64
	if err := pool.QueryRow(ctx, `INSERT INTO artifacts (run_id, job_id, name, sha256, size, expires_at)
		SELECT run_id, id, 'dist', 'deadbeef', 1, now() - interval '1 day' FROM run_jobs WHERE id = $1 RETURNING id`, job.ID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO blob_refs (sha256, owner_type, owner_id) VALUES ('deadbeef', 'artifact', $1)", artifactID); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.RetentionArgs{})
	var left int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM artifacts) + (SELECT count(*) FROM blob_refs WHERE owner_type = 'artifact')").Scan(&left); err != nil || left != 0 {
		t.Errorf("artifacts and references left: %d, %v", left, err)
	}
}
