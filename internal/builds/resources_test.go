// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

// limitedAgent is a fake container agent that announces the most it allows one container.
type limitedAgent struct {
	*fakeAgent
	limits *protocol.Limits
}

func newLimitedAgent(t *testing.T, env *apitest.Env, name string, limits *protocol.Limits) *limitedAgent {
	var reg struct {
		Token string `json:"token"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": name, "autoApprove": true}, &reg)
	a := &limitedAgent{fakeAgent: &fakeAgent{t: t, c: env.Anonymous()}, limits: limits}
	var res protocol.RegisterResponse
	a.c.MustDo(http.MethodPost, "/api/agent/v1/register", protocol.RegisterRequest{Token: reg.Token, Name: name,
		Labels: []string{"fake", "docker"}, OS: "linux", Arch: "amd64", Version: "test"}, &res)
	a.secret = res.Secret
	return a
}

func (a *limitedAgent) claim() *protocol.JobPayload {
	a.t.Helper()
	r := a.c.Do(http.MethodPost, "/api/agent/v1/jobs/claim", protocol.ClaimRequest{Labels: []string{"fake", "docker"}, OS: "linux",
		Arch: "amd64", Version: "test", Limits: a.limits}, "Authorization", "Bearer "+a.secret)
	if r.Status != http.StatusOK {
		a.t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	var p protocol.JobPayload
	r.Decode(&p)
	return &p
}

// TestResourceMatching: a job whose container asks for more than an agent allows goes to an agent that allows it;
// while none does, the run's page tells why it waits. Shell jobs ask for nothing.
func TestResourceMatching(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "RES", "name": "Resources"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/RES/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/RES/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": `version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: big
            image: docker.io/library/alpine:3
            runs-on: [fake]
            resources: { cpus: 4, memory: 8g }
            steps: [{ run: "true" }]
          - name: small
            image: docker.io/library/alpine:3
            runs-on: [fake]
            services:
              - { name: db, image: "docker.io/library/postgres:18", resources: { memory: 2g } }
            steps: [{ run: "true" }]
          - name: shell
            runs-on: [fake, docker]
            resources: { cpus: 64 }
            steps: [{ run: "true" }]
`}, nil)
	env.WorkJobs()

	small := newLimitedAgent(t, env, "small", &protocol.Limits{MilliCPUs: 2000, Memory: 4 << 30})
	for _, want := range []string{"small", "shell"} {
		p := small.claim()
		if p.Job.Name != want {
			t.Fatalf("the small agent got %s, want %s", p.Job.Name, want)
		}
		small.complete(p)
	}
	type job struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Resources *struct {
			MilliCPUs int64 `json:"milliCpus"`
			Memory    int64 `json:"memory"`
			NoAgent   bool  `json:"noAgent"`
		} `json:"resources"`
	}
	big := func() job {
		var run struct {
			Stages []struct {
				Jobs []job `json:"jobs"`
			} `json:"stages"`
		}
		admin.Get("/api/v1/code/repos/RES/app/runs/1", &run)
		for _, j := range run.Stages[0].Jobs {
			if j.Name == "big" {
				return j
			}
		}
		t.Fatal("no job big")
		return job{}
	}
	if j := big(); j.State != "pending" || j.Resources == nil || j.Resources.MilliCPUs != 4000 || j.Resources.Memory != 8<<30 || !j.Resources.NoAgent {
		t.Errorf("big waits for an agent that allows it: %+v %+v", j, j.Resources)
	}
	// An agent without limits can take it.
	large := newLimitedAgent(t, env, "large", nil)
	if j := big(); j.Resources == nil || j.Resources.NoAgent {
		t.Errorf("an agent allows big now: %+v", j.Resources)
	}
	if p := large.claim(); p.Job.Name != "big" || p.Job.Resources.MilliCPUs != 4000 {
		t.Errorf("the large agent got %s (%+v)", p.Job.Name, p.Job.Resources)
	} else {
		large.complete(p)
	}
}
