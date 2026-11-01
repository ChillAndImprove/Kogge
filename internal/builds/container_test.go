// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/testutil/apitest"
)

// testImage is an image that is usually present where the tests run (the test database uses it); the test never
// pulls images.
const testImage = "postgres:18-alpine"

const containerPipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: [main] } }
    stages:
      - name: Test
        jobs:
          - name: query
            image: ` + testImage + `
            resources: { cpus: 0.5, memory: 96m }
            services:
              - name: database
                image: ` + testImage + `
                variables: { POSTGRES_HOST_AUTH_METHOD: trust }
                resources: { memory: 512m }
            steps:
              - name: Query the service
                run: |
                  for i in $(seq 1 60); do pg_isready -h database -U postgres -q && break; sleep 1; done
                  psql -h database -U postgres -tAc "select 40 + 2" > answer.txt
                  echo "answer $(cat answer.txt) in $KOGGE_WORKSPACE as $(id -u)"
                  echo "token $API_TOKEN"
                  echo "memory $(cat /sys/fs/cgroup/memory.max) swap $(cat /sys/fs/cgroup/memory.swap.max) cpu $(cat /sys/fs/cgroup/cpu.max) pids $(cat /sys/fs/cgroup/pids.max)"
            artifacts:
              - name: answer
                paths: [answer.txt]
`

func TestContainerExecutor(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI")
	}
	if err := exec.Command("docker", "image", "inspect", testImage).Run(); err != nil {
		t.Skipf("the image %s is not present", testImage)
	}
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CT", "name": "Containers"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CT/repos", map[string]any{"name": "App", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/builds/variables/API_TOKEN?scope=repository&project=CT&repository=app",
		map[string]any{"value": "container-secret-77", "secret": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/CT/app/files", map[string]any{
		"branch": "main", "path": ".kogge/pipelines.yml", "content": containerPipeline,
	}, nil)
	env.WorkJobs()

	var reg struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"autoApprove": true}, &reg)
	// The container engine must see the work directory (Docker installed as a snap does not see /tmp).
	work := t.TempDir()
	if dir := os.Getenv("KOGGE_TEST_AGENT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		if work, err = os.MkdirTemp(dir, "agent-"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(work) })
	}
	if err := os.WriteFile(filepath.Join(work, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if exec.Command("docker", "run", "--rm", "--volume", work+":/x", "--entrypoint", "test", testImage, "-e", "/x/probe").Run() != nil {
		t.Skipf("docker cannot mount %s; set KOGGE_TEST_AGENT_DIR to a directory below the home directory", work)
	}
	cfg := &agent.Config{Server: env.URL, Name: "docker-agent", WorkDir: work, Container: "docker",
		Resources: &agent.Resources{MaxMemory: "1g", Pids: 256}}
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

	run := waitRunIn(t, admin, "CT/app", "1", func(r *runDetail) bool { return r.State == "succeeded" || r.State == "failed" })
	job := run.job("query")
	log := jobLog(t, admin, job.ID)
	if run.State != "succeeded" {
		t.Fatalf("run %s, job %+v\n%s", run.State, job, log)
	}
	if !strings.Contains(log, "answer 42 in /workspace as 0") || !strings.Contains(log, "token ****") || strings.Contains(log, "container-secret-77") {
		t.Errorf("log:\n%s", log)
	}
	// The job's container has the resources it asked for, no swap, and the agent's limit of processes (cgroup v2).
	if !strings.Contains(log, "memory 100663296 swap 0 cpu 50000 100000 pids 256") {
		t.Errorf("the container's limits:\n%s", log)
	}
	if len(job.Artifacts) != 1 {
		t.Errorf("artifacts = %+v", job.Artifacts)
	}
	// The containers, the network and the workspace are gone (files written as root were given back first).
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "label=kogge.agent=docker-agent", "--format", "{{.Names}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("containers left: %s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(work, "w")); len(entries) != 0 {
		t.Errorf("workspaces left: %d", len(entries))
	}
}
