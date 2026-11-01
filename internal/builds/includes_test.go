// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/testutil/apitest"
)

const includingPipeline = `version: 1
include:
  - .kogge/common.yml
  - repository: INC/shared
    file: templates/ci.yml
    ref: main
pipelines:
  app:
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: make }]
  nightly:
    on: { schedule: [{ cron: "* * * * *", branch: main }] }
    stages:
      - name: Nightly
        jobs:
          - name: nightly
            runs-on: [fake]
            steps: [{ run: make nightly }]
`

// TestIncludes: a pipeline file includes files of its repository and of another repository of its code project —
// with the permissions of the person the run is for, at commits that a run again reads again. Who may not read
// the other repository gets an error run, also a schedule after such a person's push; repositories of other code
// projects are never read.
func TestIncludes(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("dev")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"admin": admin, "dev": dev} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	for _, p := range []struct{ key, repo string }{{"INC", "app"}, {"INC", "shared"}, {"OTHER", "lib"}} {
		if r := admin.Do(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": p.key, "name": p.key}); r.Status >= 300 &&
			r.Status != http.StatusConflict {
			t.Fatalf("project: %d %s", r.Status, r.Body)
		}
		admin.MustDo(http.MethodPut, "/api/v1/code/projects/"+p.key+"/permissions", []map[string]any{
			{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": ids["admin"]}}}, nil)
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/"+p.key+"/repos", map[string]any{"name": p.repo, "readme": true}, nil)
	}
	// dev may write to the app only, not read the shared repository.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/INC/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": ids["dev"]}, "permission": "repo.write"}}, nil)
	put := func(c *apitest.Client, repo, path, content string) {
		t.Helper()
		c.MustDo(http.MethodPut, "/api/v1/code/repos/"+repo+"/files", map[string]any{"branch": "main", "path": path, "content": content}, nil)
		env.WorkJobs()
	}
	put(admin, "INC/shared", "templates/ci.yml", `include: [templates/vars.yml]
pipelines:
  shared-ci:
    stages:
      - name: Shared
        jobs:
          - name: shared-job
            runs-on: [fake]
            steps: [{ run: "echo shared" }]
`)
	put(admin, "INC/shared", "templates/vars.yml", "variables:\n  SHARED_VERSION: v1\n")
	put(admin, "INC/app", ".kogge/common.yml", "variables:\n  COMMON: from-common\n")
	put(admin, "INC/app", ".kogge/pipelines.yml", includingPipeline)

	fake := newFakeAgent(t, env)
	claimed := map[string]string{} // job → SHARED_VERSION, COMMON
	for range 2 {
		p := fake.claim()
		claimed[p.Job.Name] = p.Variables["SHARED_VERSION"] + " " + p.Variables["COMMON"]
		fake.complete(p)
	}
	if claimed["build"] != "v1 from-common" || claimed["shared-job"] != "v1 from-common" {
		t.Fatalf("variables of the jobs: %v", claimed)
	}
	var run graphRun
	admin.Get("/api/v1/code/repos/INC/app/runs/2", &run)
	if run.Pipeline != "shared-ci" || run.File != "INC/shared:templates/ci.yml" || len(run.Includes) != 3 {
		t.Fatalf("run 2 = %+v", run)
	}
	var got []string
	for _, inc := range run.Includes {
		got = append(got, inc.Repository+":"+inc.Path+"@"+inc.Ref)
	}
	if strings.Join(got, " ") != ":.kogge/common.yml@ INC/shared:templates/ci.yml@main INC/shared:templates/vars.yml@main" ||
		run.Includes[1].Commit == "" || run.Includes[1].Commit != run.Includes[2].Commit {
		t.Errorf("includes = %+v", run.Includes)
	}

	// The shared file changes; running run 2 again reads it at the commit run 2 read it at, a new push the new one.
	put(admin, "INC/shared", "templates/vars.yml", "variables:\n  SHARED_VERSION: v2\n")
	var again struct {
		Number int `json:"number"`
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/runs/%d/rerun", run.ID), nil, &again)
	p := fake.claim()
	if p.RunNumber != again.Number || p.Variables["SHARED_VERSION"] != "v1" {
		t.Errorf("run again: run %d, SHARED_VERSION %q", p.RunNumber, p.Variables["SHARED_VERSION"])
	}
	fake.complete(p)
	put(admin, "INC/app", "README.md", "# App\n")
	for range 2 {
		p := fake.claim()
		if p.Variables["SHARED_VERSION"] != "v2" {
			t.Errorf("a new run of %s read SHARED_VERSION %q", p.Job.Name, p.Variables["SHARED_VERSION"])
		}
		fake.complete(p)
	}

	// dev cannot read the shared repository: dev's push produces an error run and runs nothing.
	put(dev, "INC/app", "x.txt", "x\n")
	var runs struct {
		Items []struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		} `json:"items"`
	}
	admin.Get("/api/v1/code/repos/INC/app/runs?limit=1", &runs)
	admin.Get(apitest.Fmt("/api/v1/code/repos/INC/app/runs/%d", runs.Items[0].Number), &run)
	if run.State != "error" || !strings.Contains(run.Error, "line 4, column 5: include INC/shared:templates/ci.yml: "+
		"the repository INC/shared does not exist, or dev may not read it") {
		t.Errorf("dev's push: %s %q", run.State, run.Error)
	}
	// So does the schedule: dev pushed the branch last.
	pool := env.App.Pool
	if _, err := pool.Exec(context.Background(), "UPDATE build_schedules SET last_run_at = now() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.ScheduleArgs{})
	admin.Get("/api/v1/code/repos/INC/app/runs?limit=1", &runs)
	admin.Get(apitest.Fmt("/api/v1/code/repos/INC/app/runs/%d", runs.Items[0].Number), &run)
	if run.State != "error" || !strings.Contains(run.Error, "dev may not read it") {
		t.Errorf("schedule after dev's push: %s %q", run.State, run.Error)
	}
	// After a push of someone who may read it, the schedule runs.
	put(admin, "INC/app", "y.txt", "y\n")
	for range 2 {
		fake.complete(fake.claim())
	}
	if _, err := pool.Exec(context.Background(), "UPDATE build_schedules SET last_run_at = now() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	runJob(t, env, builds.ScheduleArgs{})
	if p := fake.claim(); p.Job.Name != "nightly" || p.Variables["SHARED_VERSION"] != "v2" {
		t.Errorf("scheduled job %s with %q", p.Job.Name, p.Variables["SHARED_VERSION"])
	} else {
		fake.complete(p)
	}

	// The check of the file reads the included files with the permissions of who checks it.
	type check struct {
		Valid    bool `json:"valid"`
		Problems []struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
		Pipelines []struct {
			ID   string `json:"id"`
			File string `json:"file"`
		} `json:"pipelines"`
		Includes   []any `json:"includes"`
		Unresolved bool  `json:"unresolved"`
	}
	var c check
	admin.MustDo(http.MethodPost, "/api/v1/builds/check", map[string]any{"content": includingPipeline, "repository": "INC/app", "ref": "main"}, &c)
	if !c.Valid || len(c.Pipelines) != 3 || c.Pipelines[2].File != "INC/shared:templates/ci.yml" || len(c.Includes) != 3 || c.Unresolved {
		t.Errorf("admin's check: %+v", c)
	}
	c = check{}
	dev.MustDo(http.MethodPost, "/api/v1/builds/check", map[string]any{"content": includingPipeline, "repository": "INC/app"}, &c)
	if c.Valid || len(c.Problems) != 1 || c.Problems[0].Line != 4 || !strings.Contains(c.Problems[0].Message, "dev may not read it") {
		t.Errorf("dev's check: %+v", c)
	}
	c = check{}
	dev.MustDo(http.MethodPost, "/api/v1/builds/check", map[string]any{"content": includingPipeline}, &c)
	if !c.Valid || !c.Unresolved || len(c.Pipelines) != 2 {
		t.Errorf("check without a repository: %+v", c)
	}
	// Who may not read the repository learns nothing from checking against it.
	outsider := env.CreateUser("outsider")
	if r := outsider.Do(http.MethodPost, "/api/v1/builds/check", map[string]any{"content": includingPipeline, "repository": "INC/app"}); r.Status != http.StatusNotFound {
		t.Errorf("outsider's check: %d %s", r.Status, r.Body)
	}

	// Repositories of other code projects are not read, whoever may read them.
	put(admin, "OTHER/lib", "lib.yml", "variables:\n  LIB: x\n")
	put(admin, "INC/app", ".kogge/pipelines.yml", strings.Replace(includingPipeline, "  - .kogge/common.yml\n",
		"  - .kogge/common.yml\n  - { repository: OTHER/lib, file: lib.yml }\n", 1))
	admin.Get("/api/v1/code/repos/INC/app/runs?limit=1", &runs)
	admin.Get(apitest.Fmt("/api/v1/code/repos/INC/app/runs/%d", runs.Items[0].Number), &run)
	if run.State != "error" || !strings.Contains(run.Error, "include OTHER/lib:lib.yml: only files of repositories of the code project INC can be included") {
		t.Errorf("another code project: %s %q", run.State, run.Error)
	}
}
