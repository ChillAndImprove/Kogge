// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/testutil/apitest"
)

func TestMain(m *testing.M) {
	// Pushes run the hooks, which start this test binary as `kogge hook …`.
	code.HookMain()
	os.Exit(m.Run())
}

const pipelineFile = `version: 1
variables:
  GREETING: hello
pipelines:
  ci:
    on:
      push:
        branches: ["**"]
      manual:
        variables:
          TARGET: { default: world }
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [shell]
            cache:
              - key: deps
                paths: [deps]
            steps:
              - name: Compile
                run: |
                  echo "$GREETING $TARGET from $KOGGE_BRANCH"
                  echo "the key is $API_KEY"
                  test -f deps/marker && echo "cache restored"
                  mkdir -p dist deps && echo binary > dist/app.bin && echo x > deps/marker
                  printf '<testsuite name="unit"><testcase name="ok" time="0.25"/><testcase name="bad" classname="pkg"><failure message="boom">trace %s</failure></testcase></testsuite>' "$API_KEY" > report.xml
            artifacts:
              - name: dist
                paths: ["dist/**"]
            reports:
              junit: [report.xml]
      - name: Test
        jobs:
          - name: verify
            runs-on: [shell]
            steps:
              - run: test "$(cat dist/app.bin)" = binary
              - name: Optional
                run: exit 3
                continue-on-error: true
              - name: Never
                run: echo "not on success"
                when: on-failure
      - name: Deploy
        jobs:
          - name: production
            runs-on: [shell]
            environment: production
            trigger: manual
            steps:
              - run: echo "deploying to $KOGGE_ENVIRONMENT with $DEPLOY_URL"
`

type jobInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	WaitingFor  string `json:"waitingFor"`
	Failure     string `json:"failure"`
	ExitCode    *int   `json:"exitCode"`
	Environment string `json:"environment"`
	Deployment  *struct {
		ID int64 `json:"id"`
	} `json:"deployment"`
	Artifacts []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"artifacts"`
	Report *struct {
		Total    int `json:"total"`
		Failed   int `json:"failed"`
		Failures []struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			Output  string `json:"output"`
		} `json:"failures"`
	} `json:"report"`
}

type runDetail struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	State  string `json:"state"`
	Event  string `json:"event"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	Error  string `json:"error"`
	Stages []struct {
		Name string    `json:"name"`
		Jobs []jobInfo `json:"jobs"`
	} `json:"stages"`
}

func (r *runDetail) job(name string) jobInfo {
	for _, s := range r.Stages {
		for _, j := range s.Jobs {
			if j.Name == name {
				return j
			}
		}
	}
	return jobInfo{}
}

// waitRun polls a run of CI/app until cond holds.
func waitRun(t *testing.T, c *apitest.Client, number string, cond func(*runDetail) bool) *runDetail {
	t.Helper()
	return waitRunIn(t, c, "CI/app", number, cond)
}

// waitRunIn polls a run of a repository (KEY/slug) until cond holds.
func waitRunIn(t *testing.T, c *apitest.Client, repo, number string, cond func(*runDetail) bool) *runDetail {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var run runDetail
	for time.Now().Before(deadline) {
		c.Get("/api/v1/code/repos/"+repo+"/runs/"+number, &run)
		if cond(&run) {
			return &run
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach the expected state: %+v", number, run)
	return nil
}

func jobLog(t *testing.T, c *apitest.Client, id int64) string {
	t.Helper()
	res := c.Do(http.MethodGet, apitest.Fmt("/api/v1/builds/jobs/%d/log/raw", id), nil)
	if res.Status != http.StatusOK {
		t.Fatalf("log: %d %s", res.Status, res.Body)
	}
	return string(res.Body)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "init.defaultBranch=main"},
		args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestPipelines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test pipeline uses sh")
	}
	env := apitest.New(t)
	admin := env.Admin()
	lead := env.CreateUser("lead")
	var leadMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	lead.Get("/api/v1/me", &leadMe)
	var adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &adminMe)

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CI", "name": "Continuous"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CI/repos", map[string]any{"name": "App", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/CI/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": leadMe.User.ID}},
	}, nil)

	// Variables: a secret of the repository, a value of the environment.
	admin.MustDo(http.MethodPut, "/api/v1/builds/variables/API_KEY?scope=repository&project=CI&repository=app",
		map[string]any{"value": "s3cr3t-value-42", "secret": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/CI/app/environments/production", map[string]any{
		"refs": []string{"main"}, "deployerUsers": []int64{}, "deployerGroups": []int64{}, "approvals": 1,
		"approverUsers": []int64{leadMe.User.ID}, "approverGroups": []int64{},
	}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/builds/variables/DEPLOY_URL?scope=environment&project=CI&repository=app&environment=production",
		map[string]any{"value": "https://prod.example.com"}, nil)
	var vars []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Secret bool   `json:"secret"`
	}
	admin.Get("/api/v1/builds/variables?scope=repository&project=CI&repository=app", &vars)
	if len(vars) != 1 || vars[0].Value != "" || !vars[0].Secret {
		t.Fatalf("variables = %+v", vars)
	}
	if res := lead.Do(http.MethodGet, "/api/v1/builds/variables?scope=repository&project=CI&repository=app", nil); res.Status != http.StatusForbidden &&
		res.Status != http.StatusNotFound {
		t.Fatalf("readers see variables: %d", res.Status)
	}

	// Push the pipeline file.
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)
	work := t.TempDir()
	git(t, work, "clone", u.String(), "app")
	dir := filepath.Join(work, "app")
	if err := os.MkdirAll(filepath.Join(dir, ".kogge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".kogge", "pipelines.yml"), []byte(pipelineFile), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "Add the pipeline")
	git(t, dir, "push", "origin", "main")
	env.WorkJobs() // push processing starts the run

	var runs struct {
		Items []struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Event  string `json:"event"`
		} `json:"items"`
		Total int `json:"total"`
	}
	admin.Get("/api/v1/code/repos/CI/app/runs", &runs)
	if runs.Total != 1 || runs.Items[0].State != "queued" || runs.Items[0].Event != "push" {
		t.Fatalf("runs = %+v", runs)
	}

	// An agent with the shell executor.
	var reg struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "test", "autoApprove": true}, &reg)
	cfg := &agent.Config{Server: env.URL, Name: "test-agent", WorkDir: t.TempDir(), Shell: true, Parallel: 2, Labels: []string{"linux-box"}}
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
	var agents []struct {
		Name   string   `json:"name"`
		Labels []string `json:"labels"`
		Online bool     `json:"online"`
	}
	admin.Get("/api/v1/admin/agents", &agents)
	if len(agents) != 1 || !agents[0].Online || !strings.Contains(strings.Join(agents[0].Labels, ","), "shell") {
		t.Fatalf("agents = %+v", agents)
	}

	// Build and test run; the deployment waits for a manual start.
	run := waitRun(t, admin, "1", func(r *runDetail) bool {
		return r.job("production").State == "waiting" && r.job("verify").State == "succeeded"
	})
	if run.State != "succeeded" {
		t.Errorf("run state = %s", run.State)
	}
	compile := run.job("compile")
	if compile.State != "succeeded" || len(compile.Artifacts) != 1 || compile.Artifacts[0].Name != "dist" {
		t.Fatalf("compile = %+v", compile)
	}
	if compile.Report == nil || compile.Report.Total != 2 || compile.Report.Failed != 1 || compile.Report.Failures[0].Message != "boom" {
		t.Errorf("report = %+v", compile.Report)
	} else if out := compile.Report.Failures[0].Output; strings.Contains(out, "s3cr3t-value-42") || !strings.Contains(out, "trace ****") {
		t.Errorf("the report's secret is not masked: %q", out)
	}
	log := jobLog(t, admin, compile.ID)
	for _, want := range []string{"hello  from main", "the key is ****", "##[step] Compile", "Cache deps: not found", "Cache deps: saved"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "s3cr3t-value-42") {
		t.Errorf("the secret is in the log")
	}
	verify := jobLog(t, admin, run.job("verify").ID)
	if !strings.Contains(verify, "exit code 3") || !strings.Contains(verify, "continue-on-error") || strings.Contains(verify, "not on success") {
		t.Errorf("verify log:\n%s", verify)
	}
	res := admin.Do(http.MethodGet, apitest.Fmt("/api/v1/builds/artifacts/%d", compile.Artifacts[0].ID), nil)
	if res.Status != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" || len(res.Body) == 0 {
		t.Errorf("artifact download: %d", res.Status)
	}
	var statuses map[string][]struct {
		Context string `json:"context"`
		State   string `json:"state"`
	}
	admin.Get("/api/v1/code/repos/CI/app/statuses?sha="+run.Commit, &statuses)
	if st := statuses[run.Commit]; len(st) != 1 || st[0].Context != "kogge/ci" || st[0].State != "success" {
		t.Errorf("statuses = %+v", statuses)
	}

	// Deploy: started by hand, approved by the lead.
	prod := run.job("production")
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/play", prod.ID), nil, nil)
	run = waitRun(t, admin, "1", func(r *runDetail) bool { return r.job("production").WaitingFor == "approval" })
	if run.State != "running" {
		t.Errorf("run waiting for approval: state %s", run.State)
	}
	if res := admin.Do(http.MethodPost, apitest.Fmt("/api/v1/builds/deployments/%d/approval", run.job("production").Deployment.ID),
		map[string]any{"approved": true}); res.Status != http.StatusForbidden {
		t.Errorf("approval by a non-approver: %d", res.Status)
	}
	// The approver was asked; the one who started the deployment cannot approve it.
	var inbox []struct {
		Event string `json:"event"`
		Title string `json:"title"`
	}
	lead.Get("/api/v1/notifications", &inbox)
	if len(inbox) == 0 || inbox[0].Event != "deployment.approval" || !strings.Contains(inbox[0].Title, "main → production") {
		t.Errorf("approver's inbox = %+v", inbox)
	}
	lead.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/deployments/%d/approval", run.job("production").Deployment.ID),
		map[string]any{"approved": true, "comment": "go"}, nil)
	run = waitRun(t, admin, "1", func(r *runDetail) bool { return r.job("production").State == "succeeded" })
	if !strings.Contains(jobLog(t, admin, prod.ID), "deploying to production with https://prod.example.com") {
		t.Errorf("deploy log:\n%s", jobLog(t, admin, prod.ID))
	}
	var envs []struct {
		Name    string `json:"name"`
		Current *struct {
			RunNumber int    `json:"runNumber"`
			State     string `json:"state"`
		} `json:"current"`
	}
	admin.Get("/api/v1/code/repos/CI/app/environments", &envs)
	if len(envs) != 1 || envs[0].Current == nil || envs[0].Current.RunNumber != 1 {
		t.Errorf("environments = %+v", envs)
	}

	// A manual run with a variable of the run form: the cache of the first run is restored.
	var started []struct {
		Number int `json:"number"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/CI/app/runs", map[string]any{"pipeline": "ci", "variables": map[string]string{"TARGET": "Bavaria"}},
		&started)
	if len(started) != 1 || started[0].Number != 2 {
		t.Fatalf("started = %+v", started)
	}
	run = waitRun(t, admin, "2", func(r *runDetail) bool { return r.job("verify").State == "succeeded" })
	log = jobLog(t, admin, run.job("compile").ID)
	if !strings.Contains(log, "hello Bavaria from main") || !strings.Contains(log, "cache restored") {
		t.Errorf("manual run log:\n%s", log)
	}

	// Cancel a run whose job sleeps; the agent stops it.
	if err := os.WriteFile(filepath.Join(dir, ".kogge", "pipelines.yml"), []byte(`version: 1
pipelines:
  slow:
    on: { push: { branches: [slow] } }
    stages:
      - name: Wait
        jobs:
          - name: sleep
            runs-on: [shell]
            steps:
              - run: echo started; sleep 60
`), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "checkout", "-b", "slow")
	git(t, dir, "commit", "-am", "Slow pipeline")
	git(t, dir, "push", "origin", "slow")
	env.WorkJobs()
	run = waitRun(t, admin, "3", func(r *runDetail) bool { return r.job("sleep").State == "running" })
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/runs/%d/cancel", run.ID), nil, nil)
	run = waitRun(t, admin, "3", func(r *runDetail) bool { return r.job("sleep").State == "cancelled" })
	if run.State != "cancelled" {
		t.Errorf("cancelled run: %s", run.State)
	}

	// An invalid pipeline file is a run in state error.
	if err := os.WriteFile(filepath.Join(dir, ".kogge", "pipelines.yml"), []byte("version: 1\npipelines:\n  bad:\n    stages: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-am", "Broken pipeline")
	git(t, dir, "push", "origin", "slow")
	env.WorkJobs()
	run = waitRun(t, admin, "4", func(r *runDetail) bool { return r.State != "" })
	if run.State != "error" || !strings.Contains(run.Error, "line 4") {
		t.Errorf("invalid file: %+v", run)
	}
}
