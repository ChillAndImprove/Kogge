// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

// chainPipeline: deploy follows successful builds of main, alert failed builds of any branch; ring-a and ring-b
// follow each other after deploy; long-1 … long-10 follow each other after builds of the branch long.
func chainPipeline() string {
	job := func(name string, extra string) string {
		return fmt.Sprintf(`    stages:
      - name: Run
        jobs:
          - name: %s
            runs-on: [fake]
%s            steps: [{ run: "true" }]
`, name, extra)
	}
	var b strings.Builder
	b.WriteString(`version: 1
pipelines:
  build:
    on:
      push: { branches: ["**"] }
` + job("compile", "            artifacts: [{ name: dist, paths: [dist] }]\n") + `  deploy:
    on:
      pipeline: { name: build, branches: [main] }
    stages:
      - name: Deploy
        jobs:
          - name: ship
            runs-on: [fake]
            steps: [{ run: "true" }]
          - name: notes
            runs-on: [fake]
            dependencies: []
            steps: [{ run: "true" }]
  alert:
    on:
      pipeline: { name: build, result: failure }
` + job("page", "") + `  ring-b:
    on:
      pipeline: [deploy, ring-a]
` + job("b", "") + `  ring-a:
    on:
      pipeline: ring-b
` + job("a", ""))
	for i := 1; i <= 10; i++ {
		after := fmt.Sprintf("{ name: long-%d }", i-1)
		if i == 1 {
			after = "{ name: build, branches: [long] }"
		}
		b.WriteString(fmt.Sprintf("  long-%d:\n    on:\n      pipeline: %s\n", i, after) + job(fmt.Sprintf("step-%d", i), ""))
	}
	return b.String()
}

const followerPipeline = `version: 1
pipelines:
  publish:
    on:
      pipeline: { name: build, repository: %s, branches: [main] }
    stages:
      - name: Publish
        jobs:
          - name: publish
            runs-on: [fake]
            steps: [{ run: "true" }]
`

// TestPipelineChains: a run's end starts the pipelines that follow it — of its repository at its commit, for the
// same person and with its artifacts; of other repositories of its code project at their default branch; never of
// other code projects. Each pipeline follows a run once, a chain never runs a pipeline twice and ends after ten
// runs, and a run again keeps the run it followed.
func TestPipelineChains(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	for _, p := range [][2]string{{"CHAIN", "Chain"}, {"OTHER", "Other"}} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": p[0], "name": p[1]}, nil)
	}
	for _, r := range [][2]string{{"CHAIN", "app"}, {"CHAIN", "site"}, {"OTHER", "spy"}} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/"+r[0]+"/repos", map[string]any{"name": r[1], "readme": true}, nil)
	}
	put := func(repo string, change map[string]any) string {
		t.Helper()
		var c struct {
			SHA string `json:"sha"`
		}
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/"+repo+"/files", change, &c)
		env.WorkJobs()
		return c.SHA
	}
	// Followers in another repository of the code project, and in another code project.
	put("CHAIN/site", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": fmt.Sprintf(followerPipeline, "CHAIN/app")})
	put("OTHER/spy", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": fmt.Sprintf(followerPipeline, "chain/app")})
	sha := put("CHAIN/app", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": chainPipeline()})

	agent := newFakeAgent(t, env)
	finish := func(p *protocol.JobPayload, state string) {
		t.Helper()
		if r := agent.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: state}); r.Status >= 300 {
			t.Fatalf("complete: %d %s", r.Status, r.Body)
		}
		env.WorkJobs() // the run's end starts its followers
	}
	// runs lists the runs of a repository, oldest first, as pipeline:event:state.
	runs := func(repo string) []string {
		t.Helper()
		var page struct {
			Items []struct {
				Pipeline string `json:"pipeline"`
				Event    string `json:"event"`
				State    string `json:"state"`
			} `json:"items"`
		}
		admin.Get("/api/v1/code/repos/"+repo+"/runs?limit=100", &page)
		var out []string
		for _, r := range page.Items {
			out = append([]string{r.Pipeline + ":" + r.Event + ":" + r.State}, out...)
		}
		return out
	}
	// unfinished lists the runs of all three repositories that are queued or running.
	unfinished := func() string {
		t.Helper()
		var out []string
		for _, repo := range []string{"CHAIN/app", "CHAIN/site", "OTHER/spy"} {
			for _, r := range runs(repo) {
				if strings.HasSuffix(r, ":queued") || strings.HasSuffix(r, ":running") {
					out = append(out, repo+" "+r)
				}
			}
		}
		return strings.Join(out, ", ")
	}
	type link struct {
		Repository struct {
			ProjectKey string `json:"projectKey"`
			Slug       string `json:"slug"`
		} `json:"repository"`
		Number   int    `json:"number"`
		Pipeline string `json:"pipeline"`
	}
	var detail struct {
		ID          int64  `json:"id"`
		Event       string `json:"event"`
		Commit      string `json:"commit"`
		Branch      string `json:"branch"`
		TriggeredBy *struct {
			ID int64 `json:"id"`
		} `json:"triggeredBy"`
		Upstream   *link  `json:"upstream"`
		Downstream []link `json:"downstream"`
	}
	run := func(repo string, number int) {
		t.Helper()
		detail.Upstream, detail.Downstream, detail.TriggeredBy = nil, nil, nil
		admin.Get(apitest.Fmt("/api/v1/code/repos/%s/runs/%d", repo, number), &detail)
	}
	names := func(ls []link) string {
		var out []string
		for _, l := range ls {
			out = append(out, fmt.Sprintf("%s/%s#%d:%s", l.Repository.ProjectKey, l.Repository.Slug, l.Number, l.Pipeline))
		}
		return strings.Join(out, " ")
	}

	// The build of main succeeds with an artifact.
	build := agent.claim()
	if build.Pipeline != "build" || build.Branch != "main" {
		t.Fatalf("first job = %+v", build)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, _ := zw.Create("dist/app.bin")
	_, _ = w.Write([]byte("binary"))
	_ = zw.Close()
	if r := agent.do(build, http.MethodPut, "/artifacts/dist", bytes.NewReader(archive.Bytes())); r.Status >= 300 {
		t.Fatalf("artifact: %d %s", r.Status, r.Body)
	}
	finish(build, protocol.StateSucceeded)
	if got := strings.Join(runs("CHAIN/app"), " "); got != "build:push:succeeded deploy:pipeline:queued" {
		t.Errorf("runs of app = %s", got)
	}
	if got := strings.Join(runs("CHAIN/site"), " "); got != "publish:pipeline:queued" {
		t.Errorf("runs of site = %s", got)
	}
	if got := runs("OTHER/spy"); len(got) != 0 {
		t.Errorf("a pipeline of another code project followed: %v", got)
	}
	// Lists name the run that a run of the same repository followed.
	var list struct {
		Items []struct {
			Pipeline string `json:"pipeline"`
			After    *struct {
				Number int    `json:"number"`
				Name   string `json:"name"`
			} `json:"after"`
		} `json:"items"`
	}
	admin.Get("/api/v1/code/repos/CHAIN/app/runs", &list)
	if a := list.Items[0].After; list.Items[0].Pipeline != "deploy" || a == nil || a.Number != 1 || a.Name != "build" {
		t.Errorf("the list names the build: %+v", list.Items[0])
	}
	list.Items = nil
	admin.Get("/api/v1/code/repos/CHAIN/site/runs", &list)
	if len(list.Items) != 1 || list.Items[0].After != nil {
		t.Errorf("a run of another repository is named: %+v", list.Items)
	}
	run("CHAIN/app", 1)
	if got := names(detail.Downstream); got != "CHAIN/app#2:deploy CHAIN/site#1:publish" {
		t.Errorf("downstream of the build = %s", got)
	}
	run("CHAIN/app", 2)
	if detail.Event != "pipeline" || detail.Commit != sha || detail.Branch != "main" || detail.Upstream == nil ||
		detail.Upstream.Number != 1 || detail.TriggeredBy == nil || detail.TriggeredBy.ID != me.User.ID {
		t.Errorf("deploy = %+v", detail)
	}
	run("CHAIN/site", 1)
	if detail.Upstream == nil || names([]link{*detail.Upstream}) != "CHAIN/app#1:build" || detail.TriggeredBy != nil || detail.Branch != "main" {
		t.Errorf("publish = %+v", detail)
	}

	// The jobs know the run they follow; those of the same repository get its artifacts.
	jobs := map[string]*protocol.JobPayload{}
	for range 3 {
		p := agent.claim()
		jobs[p.Job.Name] = p
	}
	ship, notes, publish := jobs["ship"], jobs["notes"], jobs["publish"]
	if ship == nil || notes == nil || publish == nil {
		t.Fatalf("jobs = %v", jobs)
	}
	for k, v := range map[string]string{"KOGGE_TRIGGER": "pipeline", "KOGGE_UPSTREAM_REPOSITORY": "CHAIN/app", "KOGGE_UPSTREAM_PIPELINE": "build",
		"KOGGE_UPSTREAM_RUN_NUMBER": "1", "KOGGE_UPSTREAM_COMMIT": sha, "KOGGE_UPSTREAM_BRANCH": "main", "KOGGE_UPSTREAM_RESULT": "success"} {
		if ship.Variables[k] != v || publish.Variables[k] != v {
			t.Errorf("%s = %q and %q, want %q", k, ship.Variables[k], publish.Variables[k], v)
		}
	}
	if !strings.HasSuffix(ship.Variables["KOGGE_UPSTREAM_RUN_URL"], "/builds/chain/app/runs/1") {
		t.Errorf("upstream URL = %s", ship.Variables["KOGGE_UPSTREAM_RUN_URL"])
	}
	if len(ship.Artifacts) != 1 || ship.Artifacts[0].Name != "dist" || ship.Artifacts[0].Job != "build #1: Run/compile" {
		t.Fatalf("artifacts of ship = %+v", ship.Artifacts)
	}
	if len(notes.Artifacts) != 0 || len(publish.Artifacts) != 0 {
		t.Errorf("artifacts of notes %+v, of publish %+v", notes.Artifacts, publish.Artifacts)
	}
	artifact := apitest.Fmt("/artifacts/%d", ship.Artifacts[0].ID)
	if r := agent.do(ship, http.MethodGet, artifact, nil); r.Status != http.StatusOK || r.Header.Get("Content-Type") != "application/zip" {
		t.Errorf("ship downloads the build's artifact: %d", r.Status)
	}
	if r := agent.do(publish, http.MethodGet, artifact, nil); r.Status != http.StatusNotFound {
		t.Errorf("another repository's job downloads the artifact: %d", r.Status)
	}
	finish(publish, protocol.StateSucceeded)
	finish(notes, protocol.StateSucceeded)
	finish(ship, protocol.StateSucceeded)

	// After deploy the ring: b, then a, and not b again.
	for _, want := range []string{"b", "a"} {
		p := agent.claim()
		if p.Job.Name != want {
			t.Fatalf("ring: want %s, got %s", want, p.Job.Name)
		}
		finish(p, protocol.StateSucceeded)
	}
	if got := unfinished(); got != "" {
		t.Fatalf("the ring went on: %s", got)
	}

	// A run that ends again (a job retried) starts no pipeline twice.
	run("CHAIN/app", 1)
	var full struct {
		Stages []struct {
			Jobs []struct {
				ID int64 `json:"id"`
			} `json:"jobs"`
		} `json:"stages"`
	}
	admin.Get("/api/v1/code/repos/CHAIN/app/runs/1", &full)
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/retry", full.Stages[0].Jobs[0].ID), nil, nil)
	finish(agent.claim(), protocol.StateSucceeded)
	if got := unfinished(); got != "" {
		t.Fatalf("a pipeline followed the same run twice: %s", got)
	}

	// A failed build of another branch alerts; deploy and publish follow main only.
	put("CHAIN/app", map[string]any{"branch": "main", "newBranch": "feature", "path": "x.txt", "content": "x\n"})
	failed := agent.claim()
	if failed.Branch != "feature" {
		t.Fatalf("build of feature = %+v", failed)
	}
	finish(failed, protocol.StateFailed)
	page := agent.claim()
	if page.Pipeline != "alert" || page.Variables["KOGGE_UPSTREAM_RESULT"] != "failure" || page.Branch != "feature" {
		t.Fatalf("after the failed build: %s %v", page.Pipeline, page.Variables)
	}
	finish(page, protocol.StateSucceeded)
	if got := unfinished(); got != "" {
		t.Fatalf("after the alert: %s", got)
	}

	// A chain ends after ten runs: the build and long-1 … long-9.
	put("CHAIN/app", map[string]any{"branch": "main", "newBranch": "long", "path": "y.txt", "content": "y\n"})
	var ran []string
	for range 10 {
		p := agent.claim()
		ran = append(ran, p.Pipeline)
		finish(p, protocol.StateSucceeded)
	}
	if ran[0] != "build" || ran[9] != "long-9" {
		t.Errorf("the long chain ran %v", ran)
	}
	if got := unfinished(); got != "" || slices.Contains(runs("CHAIN/app"), "long-10:pipeline:queued") {
		t.Errorf("the chain went on: %s", got)
	}

	// A run again of deploy keeps the build it followed (and its artifacts).
	run("CHAIN/app", 2)
	var again struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/runs/%d/rerun", detail.ID), nil, &again)
	for range 2 {
		p := agent.claim()
		if p.Pipeline != "deploy" || p.Variables["KOGGE_UPSTREAM_RUN_NUMBER"] != "1" {
			t.Fatalf("run again = %s %v", p.Pipeline, p.Variables)
		}
		if p.Job.Name == "ship" && len(p.Artifacts) != 1 {
			t.Errorf("the run again lost the artifacts: %+v", p.Artifacts)
		}
	}
}
