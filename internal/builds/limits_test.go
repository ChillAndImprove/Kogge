// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// pipelineWithJobs is a pipeline of one stage with n jobs for the fake agent.
func pipelineWithJobs(n int) string {
	var b strings.Builder
	b.WriteString("version: 1\npipelines:\n  ci:\n    on:\n      push: { branches: [\"**\"] }\n    stages:\n      - name: Build\n        jobs:\n")
	for i := range n {
		fmt.Fprintf(&b, "          - name: job-%d\n            runs-on: [fake]\n            steps: [{ run: \"true\" }]\n", i)
	}
	return b.String()
}

// TestWaitingJobsDoNotHoldUpOthers: the jobs of a repository that runs as many as it may wait together, and agents
// meanwhile take the jobs of other repositories, however many wait before them.
func TestWaitingJobsDoNotHoldUpOthers(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LIM", "name": "Limits"}, nil)
	for _, slug := range []string{"busy", "other"} {
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/LIM/repos", map[string]any{"name": slug, "readme": true}, nil)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LIM/busy/build-settings", map[string]any{"autoCancel": false, "maxParallelJobs": 1}, nil)
	for _, r := range []struct {
		slug string
		jobs int
	}{{"busy", 8}, {"other", 1}} {
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/LIM/"+r.slug+"/files", map[string]any{"branch": "main",
			"path": ".kogge/pipelines.yml", "content": pipelineWithJobs(r.jobs)}, nil)
		env.WorkJobs()
	}

	agent := newFakeAgent(t, env)
	if p := agent.claim(); p.Repository != "LIM/busy" {
		t.Fatalf("the first job is of %s", p.Repository)
	}
	// busy runs as many jobs as it may: its seven other jobs wait, and the agent takes the job of other.
	if p := agent.claim(); p.Repository != "LIM/other" {
		t.Fatalf("the second job is of %s", p.Repository)
	}
}
