// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

const badgePipelines = `version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
  lint:
    stages:
      - name: Lint
        jobs:
          - name: lint
            runs-on: [fake]
            steps: [{ run: "true" }]
`

// TestStatusBadge: the badge shows the state of a branch's latest runs — of all pipelines or one — to those who
// may read the repository; others get "unknown", as for a repository that does not exist.
func TestStatusBadge(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "BDG", "name": "Badges"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/BDG/repos", map[string]any{"name": "app", "readme": true}, nil)
	badge := func(c *apitest.Client, path string) string {
		t.Helper()
		r := c.Do(http.MethodGet, path, nil)
		if r.Status != http.StatusOK || r.Header.Get("Content-Type") != "image/svg+xml" || r.Header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("%s: %d %v", path, r.Status, r.Header)
		}
		body := string(r.Body)
		_, after, _ := strings.Cut(body, "<title>")
		title, _, _ := strings.Cut(after, "</title>")
		return title
	}
	const path = "/api/v1/code/repos/BDG/app/badge.svg"
	if got := badge(admin, path); got != "build: no runs" {
		t.Errorf("without runs = %q", got)
	}
	agent := newFakeAgent(t, env)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/BDG/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": badgePipelines}, nil)
	env.WorkJobs()
	if got := badge(admin, path); got != "build: running" {
		t.Errorf("queued = %q", got)
	}
	for range 2 {
		p := agent.claim()
		state := protocol.StateSucceeded
		if p.Job.Name == "lint" {
			state = protocol.StateFailed
		}
		agent.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: state})
	}
	for query, want := range map[string]string{
		"":                                  "build: failing",
		"?pipeline=ci":                      "build: passing",
		"?pipeline=ci&label=Tests":          "Tests: passing",
		"?pipeline=lint&branch=main":        "build: failing",
		"?branch=feature":                   "build: no runs",
		"?label=%3Cscript%3Ex%3C/script%3E": "&lt;script&gt;x&lt;/script&gt;: failing",
	} {
		if got := badge(admin, path+query); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	// Who may not read the repository cannot tell it from one that does not exist.
	outsider := env.CreateUser("outsider")
	if got := badge(outsider, path); got != "build: unknown" {
		t.Errorf("outsider = %q", got)
	}
	if got := badge(env.Anonymous(), path); got != "build: unknown" {
		t.Errorf("anonymous = %q", got)
	}
	if got := badge(admin, "/api/v1/code/repos/BDG/nothing/badge.svg"); got != "build: unknown" {
		t.Errorf("no repository = %q", got)
	}
}
