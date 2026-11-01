// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

// graphJob is a job of a run as the run's page sees it.
type graphJob struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Waiting  string     `json:"waitingFor"`
	Needs    []int64    `json:"needs"`
	HasNeeds bool       `json:"hasNeeds"`
	Started  *time.Time `json:"started"`
	Finished *time.Time `json:"finished"`
	Skip     *struct {
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	} `json:"skip"`
}

type graphRun struct {
	ID       int64  `json:"id"`
	Number   int    `json:"number"`
	State    string `json:"state"`
	Error    string `json:"error"`
	Pipeline string `json:"pipeline"`
	File     string `json:"file"`
	Includes []struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		Ref        string `json:"ref"`
		Commit     string `json:"commit"`
	} `json:"includes"`
	Stages []struct {
		Name string     `json:"name"`
		Jobs []graphJob `json:"jobs"`
	} `json:"stages"`
}

func (r *graphRun) job(name string) graphJob {
	for _, s := range r.Stages {
		for _, j := range s.Jobs {
			if j.Name == name {
				return j
			}
		}
	}
	return graphJob{}
}

func (r *graphRun) states() string {
	var out []string
	for _, s := range r.Stages {
		for _, j := range s.Jobs {
			x := j.Name + "=" + j.State
			if j.Skip != nil {
				x += "(" + j.Skip.Reason + " " + j.Skip.Detail + ")"
			}
			out = append(out, x)
		}
	}
	return strings.Join(out, " ")
}

// complete reports a claimed job as succeeded.
func (a *fakeAgent) complete(p *protocol.JobPayload) {
	a.t.Helper()
	if r := a.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: protocol.StateSucceeded}); r.Status >= 300 {
		a.t.Fatalf("complete: %d %s", r.Status, r.Body)
	}
}

func waitGraph(t *testing.T, c *apitest.Client, repo, number string, cond func(*graphRun) bool) *graphRun {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var run graphRun
	for time.Now().Before(deadline) {
		run = graphRun{}
		c.Get("/api/v1/code/repos/"+repo+"/runs/"+number, &run)
		if cond(&run) {
			return &run
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach the expected state: %s %s", number, run.State, run.states())
	return nil
}

// startShellAgent registers and runs an agent of this machine with the shell executor.
func startShellAgent(t *testing.T, env *apitest.Env, name string, parallel int, labels ...string) {
	t.Helper()
	var reg struct {
		Token string `json:"token"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": name, "autoApprove": true}, &reg)
	cfg := &agent.Config{Server: env.URL, Name: name, WorkDir: t.TempDir(), Shell: true, Parallel: parallel, Labels: labels}
	if err := agent.Register(context.Background(), cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop, stopAgent := context.WithCancel(context.Background())
	kill, killAgent := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(stop, kill) }()
	t.Cleanup(func() {
		stopAgent()
		killAgent()
		<-done
	})
}

// graphPipeline fans out after compile and in again before package, beside a slow job; docs runs only for changes
// of the documentation, after-all waits for the stages before it.
const graphPipeline = `version: 1
include:
  - .kogge/common.yml
  - { repository: GR/shared, file: ci/vars.yml }
pipelines:
  graph:
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [graph]
            steps: [{ run: "echo compiled > out.txt; echo \"$COMMON and $SHARED\"" }]
            artifacts: [{ name: out, paths: [out.txt] }]
          - name: slow
            runs-on: [graph]
            steps: [{ run: "sleep 8" }]
      - name: Test
        jobs:
          - name: unit
            runs-on: [graph]
            needs: [compile]
            steps: [{ run: "test \"$(cat out.txt)\" = compiled" }]
          - name: lint
            runs-on: [graph]
            needs: [{ job: compile, artifacts: false }]
            steps: [{ run: "test ! -e out.txt" }]
          - name: docs
            runs-on: [graph]
            if: changed('docs/**')
            steps: [{ run: "true" }]
      - name: Package
        jobs:
          - name: package
            runs-on: [graph]
            needs: [unit, lint]
            steps: [{ run: "true" }]
          - name: after-all
            runs-on: [graph]
            steps: [{ run: "true" }]
`

// TestGraphRun: jobs with needs start as soon as the jobs they need succeeded — fanning out and in again while an
// unrelated slow job still runs — and receive their artifacts; jobs without needs wait for the earlier stages; a
// condition skips a job with its reason; the jobs see the variables of files of the repository and of another one
// that the pipeline file includes.
func TestGraphRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test pipeline uses sh")
	}
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "GR", "name": "Graphs"}, nil)
	for _, name := range []string{"app", "shared"} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/GR/repos", map[string]any{"name": name, "readme": true}, nil)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GR/shared/files", map[string]any{"branch": "main", "path": "ci/vars.yml",
		"content": "variables:\n  SHARED: from-shared\n"}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GR/app/files", map[string]any{"branch": "main", "path": ".kogge/common.yml",
		"content": "variables:\n  COMMON: from-common\n"}, nil)
	startShellAgent(t, env, "graph-agent", 4, "graph")
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GR/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": graphPipeline}, nil)
	env.WorkJobs()

	run := waitGraph(t, admin, "GR/app", "1", func(r *graphRun) bool { return r.State == "succeeded" || r.State == "failed" })
	if run.State != "succeeded" {
		t.Fatalf("run: %s %s", run.State, run.states())
	}
	compile, slow, unit, lint, pkg, after := run.job("compile"), run.job("slow"), run.job("unit"), run.job("lint"), run.job("package"),
		run.job("after-all")
	// Fan-out: unit and lint start after compile, while slow still runs; fan-in: package after both, still before
	// slow is done. after-all has no needs and waits for the earlier stages, slow among them.
	for _, j := range []graphJob{unit, lint} {
		if j.Started.Before(*compile.Finished) || !j.Started.Before(*slow.Finished) {
			t.Errorf("%s started %v; compile finished %v, slow %v", j.Name, j.Started, compile.Finished, slow.Finished)
		}
	}
	if pkg.Started.Before(*unit.Finished) || pkg.Started.Before(*lint.Finished) || !pkg.Started.Before(*slow.Finished) {
		t.Errorf("package started %v; unit finished %v, lint %v, slow %v", pkg.Started, unit.Finished, lint.Finished, slow.Finished)
	}
	if after.Started.Before(*slow.Finished) {
		t.Errorf("after-all started %v before slow finished %v", after.Started, slow.Finished)
	}
	if !pkg.HasNeeds || len(pkg.Needs) != 2 || after.HasNeeds || len(after.Needs) != 0 {
		t.Errorf("needs: package %+v, after-all %+v", pkg, after)
	}
	// The push changed only the pipeline file: docs is skipped by its condition.
	if docs := run.job("docs"); docs.State != "skipped" || docs.Skip == nil || docs.Skip.Reason != "condition" || docs.Skip.Detail != "changed('docs/**')" {
		t.Errorf("docs = %+v", docs)
	}
	if log := jobLog(t, admin, compile.ID); !strings.Contains(log, "from-common and from-shared") {
		t.Errorf("compile log:\n%s", log)
	}
	// unit received the artifact of compile; lint asked for none.
	if log := jobLog(t, admin, unit.ID); !strings.Contains(log, "Artifact out of Build/compile") {
		t.Errorf("unit log:\n%s", log)
	}
	if log := jobLog(t, admin, lint.ID); strings.Contains(log, "Artifact out") {
		t.Errorf("lint log:\n%s", log)
	}

	// A change of the documentation runs docs.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GR/app/files", map[string]any{"branch": "main", "path": "docs/guide.md",
		"content": "# Guide\n"}, nil)
	env.WorkJobs()
	run = waitGraph(t, admin, "GR/app", "2", func(r *graphRun) bool { return r.State == "succeeded" || r.State == "failed" })
	if docs := run.job("docs"); docs.State != "succeeded" {
		t.Errorf("docs after a change of the documentation = %+v (%s)", docs, run.states())
	}
}

// TestGraphFailures: a failed need skips the jobs that need it (with the reason) and the jobs of later stages
// without needs, others go on; the run fails. Running the failed job again runs what was skipped because of it.
// Cancelling a run cancels the jobs that wait and stops those that run.
func TestGraphFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test pipeline uses sh")
	}
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "GF", "name": "Failures"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/GF/repos", map[string]any{"name": "app", "readme": true}, nil)
	startShellAgent(t, env, "failure-agent", 3, "fail")
	fixed := filepath.Join(t.TempDir(), "fixed")
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GF/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [fail]
            steps: [{ run: "true" }]
      - name: Test
        jobs:
          - name: unit
            runs-on: [fail]
            needs: [compile]
            steps: [{ run: "test -f ` + fixed + `" }]
          - name: lint
            runs-on: [fail]
            needs: [compile]
            steps: [{ run: "true" }]
          - name: flaky
            runs-on: [fail]
            needs: [compile]
            allow-failure: true
            steps: [{ run: "exit 1" }]
      - name: Package
        jobs:
          - name: package
            runs-on: [fail]
            needs: [unit, lint]
            steps: [{ run: "true" }]
          - name: report
            runs-on: [fail]
            needs: [lint, flaky]
            steps: [{ run: "true" }]
      - name: Publish
        jobs:
          - name: publish
            runs-on: [fail]
            steps: [{ run: "true" }]
          - name: deploy
            runs-on: [fail]
            needs: [package]
            trigger: manual
            steps: [{ run: "true" }]
`}, nil)
	env.WorkJobs()

	run := waitGraph(t, admin, "GF/app", "1", func(r *graphRun) bool { return r.State == "failed" || r.State == "succeeded" })
	want := "compile=succeeded unit=failed lint=succeeded flaky=failed package=skipped(failed Test/unit) report=succeeded " +
		"publish=skipped(failed Test/unit) deploy=skipped(need-skipped Package/package)"
	if run.State != "failed" || run.states() != want {
		t.Fatalf("run: %s %s\nwant %s", run.State, run.states(), want)
	}

	// Fixed: unit runs again, and so do the jobs that were skipped because of it.
	if err := os.WriteFile(fixed, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/retry", run.job("unit").ID), nil, nil)
	run = waitGraph(t, admin, "GF/app", "1", func(r *graphRun) bool {
		return r.job("deploy").State == "waiting" && r.job("publish").State == "succeeded"
	})
	// The manual deployment waits without holding up the run; the job that may fail failed, which does not fail it.
	want = "compile=succeeded unit=succeeded lint=succeeded flaky=failed package=succeeded report=succeeded publish=succeeded deploy=waiting"
	if run.State != "succeeded" || run.states() != want {
		t.Errorf("after running unit again: %s %s\nwant %s", run.State, run.states(), want)
	}

	// Cancelling: the running job stops, the jobs that wait for it are cancelled.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GF/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: long
            runs-on: [fail]
            steps: [{ run: "echo started; sleep 60" }]
          - name: after-long
            runs-on: [fail]
            needs: [long]
            steps: [{ run: "true" }]
          - name: other
            runs-on: [fail]
            needs: []
            steps: [{ run: "sleep 60" }]
      - name: Later
        jobs:
          - name: later
            runs-on: [fail]
            steps: [{ run: "true" }]
`}, nil)
	env.WorkJobs()
	run = waitGraph(t, admin, "GF/app", "2", func(r *graphRun) bool {
		return r.job("long").State == "running" && r.job("other").State == "running"
	})
	if j := run.job("after-long"); j.State != "created" {
		t.Errorf("after-long waits: %+v", j)
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/runs/%d/cancel", run.ID), nil, nil)
	run = waitGraph(t, admin, "GF/app", "2", func(r *graphRun) bool {
		return r.job("long").State == "cancelled" && r.job("other").State == "cancelled"
	})
	if run.State != "cancelled" || run.states() != "long=cancelled after-long=cancelled other=cancelled later=cancelled" {
		t.Errorf("cancelled run: %s %s", run.State, run.states())
	}
}

// TestGraphManualNeeds: a job that needs a manual job waits for it to be started; the run is done meanwhile and
// runs again when it is started.
func TestGraphManualNeeds(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "GM", "name": "Manual"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/GM/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/GM/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
      - name: Release
        jobs:
          - name: approve
            runs-on: [fake]
            trigger: manual
            needs: []
            steps: [{ run: "true" }]
          - name: publish
            runs-on: [fake]
            needs: [build, approve]
            steps: [{ run: "true" }]
      - name: After
        jobs:
          - name: announce
            runs-on: [fake]
            steps: [{ run: "true" }]
`}, nil)
	env.WorkJobs()
	fake := newFakeAgent(t, env)
	p := fake.claim()
	if p.Job.Name != "build" {
		t.Fatalf("first job: %s", p.Job.Name)
	}
	fake.complete(p)
	run := waitGraph(t, admin, "GM/app", "1", func(r *graphRun) bool { return r.State == "succeeded" })
	// publish waits for the manual start of approve, announce for publish: neither holds up the run.
	if got := run.states(); got != "build=succeeded approve=waiting publish=created announce=created" {
		t.Fatalf("before the manual start: %s", got)
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/play", run.job("approve").ID), nil, nil)
	for _, name := range []string{"approve", "publish", "announce"} {
		p := fake.claim()
		if p.Job.Name != name {
			t.Fatalf("claimed %s, want %s", p.Job.Name, name)
		}
		fake.complete(p)
	}
	run = waitGraph(t, admin, "GM/app", "1", func(r *graphRun) bool { return r.State == "succeeded" && r.job("announce").State == "succeeded" })
}
