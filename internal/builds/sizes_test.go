// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/apitest"
)

// setLimits changes limits of the settings (Administration → Settings → Limits).
func setLimits(t *testing.T, admin *apitest.Client, limits map[string]any) {
	t.Helper()
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	for k, v := range limits {
		st[k] = v
	}
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
}

// randomBytes returns n bytes that do not compress.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// unsized hides the length of a reader: the request goes chunked, without Content-Length.
type unsized struct{ r io.Reader }

func (u unsized) Read(p []byte) (int, error) { return u.r.Read(p) }

// TestUploadLimitsOfJobs: artifacts and caches over the limits of the settings are refused (413) by their announced
// length before they are sent, and while they are read when no length is announced; the artifacts of a run keep
// to their total; nothing is accepted while the disk is low (507); a log over its limit is cut off with a note and the
// job goes on.
func TestUploadLimitsOfJobs(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	setLimits(t, admin, map[string]any{"maxArtifactMb": 1, "maxRunArtifactsMb": 2, "maxCacheMb": 1, "maxJobLogMb": 1})
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SIZE", "name": "Sizes"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SIZE/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SIZE/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
            artifacts:
              - { name: dist, paths: ["dist/**"] }
              - { name: docs, paths: ["docs/**"] }
              - { name: reports, paths: ["reports/**"] }
`}, nil)
	env.WorkJobs()
	a := newFakeAgent(t, env)
	job := a.claim()
	if job.ID == 0 {
		t.Fatal("no job")
	}
	send := func(path string, body io.Reader, length int64) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, env.URL+apitest.Fmt("/api/agent/v1/jobs/%d%s", job.ID, path), body)
		req.ContentLength = length
		req.Header.Set("Authorization", "Bearer "+job.Token)
		req.Header.Set("Content-Type", "application/zip")
		req.Header.Set("Expect", "100-continue") // as the agent asks
		client := &http.Client{Timeout: 10 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		return res
	}
	expect := func(res *http.Response, status int, code string) {
		t.Helper()
		defer res.Body.Close()
		var p struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(res.Body).Decode(&p)
		if res.StatusCode != status || p.Code != code {
			t.Errorf("answer %d %q (%s), want %d %q", res.StatusCode, p.Code, p.Detail, status, code)
		}
	}

	// By its announced length: refused before it is sent (the content never comes).
	never, _ := io.Pipe()
	expect(send("/artifacts/dist", never, 2<<20), http.StatusRequestEntityTooLarge, "artifact.too-large")
	// Without a length: refused while it is read.
	expect(send("/artifacts/dist", unsized{bytes.NewReader(randomBytes(2 << 20))}, 0), http.StatusRequestEntityTooLarge, "artifact.too-large")
	// The total of the run: 900 KiB twice fit into 2 MiB, a third does not; replacing an artifact counts it once.
	expect(send("/artifacts/dist", bytes.NewReader(randomBytes(900<<10)), 900<<10), http.StatusNoContent, "")
	expect(send("/artifacts/docs", bytes.NewReader(randomBytes(900<<10)), 900<<10), http.StatusNoContent, "")
	expect(send("/artifacts/reports", bytes.NewReader(randomBytes(900<<10)), 900<<10), http.StatusRequestEntityTooLarge, "artifact.run-too-large")
	expect(send("/artifacts/reports", unsized{bytes.NewReader(randomBytes(900 << 10))}, 0), http.StatusRequestEntityTooLarge,
		"artifact.run-too-large")
	expect(send("/artifacts/dist", bytes.NewReader(randomBytes(300<<10)), 300<<10), http.StatusNoContent, "")
	expect(send("/artifacts/reports", bytes.NewReader(randomBytes(700<<10)), 700<<10), http.StatusNoContent, "")
	// Caches.
	expect(send("/caches/deps", never, 2<<20), http.StatusRequestEntityTooLarge, "cache.too-large")
	expect(send("/caches/deps", unsized{bytes.NewReader(randomBytes(2 << 20))}, 0), http.StatusRequestEntityTooLarge, "cache.too-large")
	expect(send("/caches/deps", bytes.NewReader(randomBytes(500<<10)), 500<<10), http.StatusNoContent, "")
	// A low disk takes nothing.
	restore := storage.Fake(1<<30, 100<<30)
	expect(send("/caches/deps", bytes.NewReader(randomBytes(10<<10)), 10<<10), http.StatusInsufficientStorage, "storage.low")
	expect(send("/artifacts/dist", bytes.NewReader(randomBytes(10<<10)), 10<<10), http.StatusInsufficientStorage, "storage.low")
	restore()

	// The log: cut off at its limit with a note, at a line's end; later chunks are dropped, the job goes on.
	line := strings.Repeat("x", 99) + "\n"
	chunk := []byte(strings.Repeat(line, 4000)) // 400,000 bytes
	var size int64
	for range 4 {
		var res protocol.LogResponse
		a.c.MustDo(http.MethodPost, apitest.Fmt("/api/agent/v1/jobs/%d/log?offset=%d", job.ID, size), bytes.NewReader(chunk), &res,
			"Authorization", "Bearer "+job.Token)
		size = res.Size
	}
	if size > 1<<20 || size < 1<<20-int64(len(line))-200 {
		t.Errorf("log size %d at a limit of 1 MiB", size)
	}
	log := jobLog(t, admin, job.ID)
	if !strings.HasSuffix(log, "x\n[Kogge: the log reached its limit (Administration → Settings → Limits); the rest of it was not kept. The job goes on.]\n") ||
		strings.Count(log, "[Kogge: the log reached its limit") != 1 {
		t.Errorf("the end of the log: %q", log[max(0, len(log)-300):])
	}
	if r := a.do(job, http.MethodPost, "/complete", protocol.CompleteRequest{State: protocol.StateSucceeded}); r.Status != http.StatusNoContent {
		t.Errorf("complete: %d %s", r.Status, r.Body)
	}
}

// TestAgentReportsRefusedUploads: the build agent says in the job's log and failure why the server refused an
// artifact or a cache; a refused cache does not fail the job, a log over its limit neither.
func TestAgentReportsRefusedUploads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test pipeline uses sh")
	}
	env := apitest.New(t)
	admin := env.Admin()
	setLimits(t, admin, map[string]any{"maxArtifactMb": 1, "maxCacheMb": 1, "maxJobLogMb": 1})
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CI", "name": "CI"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CI/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/CI/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Build
        jobs:
          - name: artifact
            runs-on: [shell]
            steps:
              - run: mkdir -p dist && head -c 3145728 /dev/urandom > dist/big.bin
            artifacts:
              - { name: dist, paths: ["dist/**"] }
          - name: cache
            runs-on: [shell]
            cache:
              - { key: deps, paths: [deps] }
            steps:
              - run: mkdir -p deps && head -c 3145728 /dev/urandom > deps/blob
          - name: chatty
            runs-on: [shell]
            steps:
              - run: head -c 2097152 /dev/zero | tr '\0' 'x' | fold -w 99
`}, nil)
	env.WorkJobs()
	var reg struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "test", "autoApprove": true}, &reg)
	cfg := &agent.Config{Server: env.URL, Name: "test-agent", WorkDir: t.TempDir(), Shell: true, Parallel: 3}
	if err := agent.Register(context.Background(), cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop, stopAgent := context.WithCancel(context.Background())
	kill, killAgent := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ag.Run(stop, kill) }()
	t.Cleanup(func() {
		stopAgent()
		killAgent()
		<-done
	})

	run := waitRun(t, admin, "1", func(r *runDetail) bool {
		for _, name := range []string{"artifact", "cache", "chatty"} {
			if s := r.job(name).State; s != "failed" && s != "succeeded" {
				return false
			}
		}
		return true
	})
	refused := "dist: server answered 413: The artifact is larger than the 1 MiB this installation allows."
	if j := run.job("artifact"); j.State != "failed" || !strings.Contains(j.Failure, refused) {
		t.Errorf("artifact job: %s %q", j.State, j.Failure)
	} else if log := jobLog(t, admin, j.ID); !strings.Contains(log, refused) {
		t.Errorf("the log does not say why:\n%s", log)
	}
	if j := run.job("cache"); j.State != "succeeded" {
		t.Errorf("cache job: %s %q", j.State, j.Failure)
	} else if log := jobLog(t, admin, j.ID); !strings.Contains(log, "Cache deps: server answered 413: The cache is larger than the 1 MiB this installation allows.") {
		t.Errorf("the log does not say why:\n%s", log)
	}
	if j := run.job("chatty"); j.State != "succeeded" {
		t.Errorf("chatty job: %s %q", j.State, j.Failure)
	} else if log := jobLog(t, admin, j.ID); len(log) > 1<<20 || strings.Count(log, "[Kogge: the log reached its limit") != 1 {
		t.Errorf("chatty log: %d bytes, ends %q", len(log), log[max(0, len(log)-200):])
	}
}
