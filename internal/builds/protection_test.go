// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

const protectionPipeline = `version: 1
pipelines:
  ci:
    on:
      push: { branches: ["**"] }
      manual:
        variables:
          CHOICE: { default: a, options: [a, b] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
          - name: new-environment
            runs-on: [fake]
            environment: brand-new
            steps: [{ run: "true" }]
          - name: open-environment
            runs-on: [fake]
            environment: open
            steps: [{ run: "true" }]
`

// fakeAgent speaks the agent protocol without running anything.
type fakeAgent struct {
	t      *testing.T
	c      *apitest.Client
	secret string
}

func newFakeAgent(t *testing.T, env *apitest.Env) *fakeAgent {
	var reg struct {
		Token string `json:"token"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "fake", "autoApprove": true}, &reg)
	a := &fakeAgent{t: t, c: env.Anonymous()}
	var res protocol.RegisterResponse
	a.c.MustDo(http.MethodPost, "/api/agent/v1/register", protocol.RegisterRequest{Token: reg.Token, Name: "fake", Labels: []string{"fake"},
		OS: "linux", Arch: "amd64", Version: "test"}, &res)
	a.secret = res.Secret
	return a
}

// claim takes the next job; there must be one.
func (a *fakeAgent) claim() *protocol.JobPayload {
	a.t.Helper()
	r := a.c.Do(http.MethodPost, "/api/agent/v1/jobs/claim", protocol.ClaimRequest{Labels: []string{"fake"}, OS: "linux", Arch: "amd64",
		Version: "test"}, "Authorization", "Bearer "+a.secret)
	if r.Status != http.StatusOK {
		a.t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	var p protocol.JobPayload
	r.Decode(&p)
	return &p
}

func (a *fakeAgent) do(job *protocol.JobPayload, method, path string, body any) *apitest.Response {
	return a.c.Do(method, apitest.Fmt("/api/agent/v1/jobs/%d%s", job.ID, path), body, "Authorization", "Bearer "+job.Token)
}

// TestProtectedValues: protected values reach protected refs and deployments that people let through, not jobs
// that merely name an environment; caches of protected jobs are their own; manual runs take tags as tags and only
// the variables of their run form.
func TestProtectedValues(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SEC", "name": "Secure"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SEC/repos", map[string]any{"name": "app", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/SEC/branch-permissions", []map[string]any{
		{"pattern": "main", "kind": "restrict-push", "exemptUsers": []int64{me.User.ID}, "exemptGroups": []int64{}},
		{"pattern": "release-*", "kind": "restrict-push", "exemptUsers": []int64{me.User.ID}, "exemptGroups": []int64{}},
		// Rules that do not decide who may push protect nothing.
		{"pattern": "**", "kind": "no-rewrite", "exemptUsers": []int64{}, "exemptGroups": []int64{}},
		{"pattern": "**", "kind": "no-delete", "exemptUsers": []int64{}, "exemptGroups": []int64{}},
	}, nil)
	vars := "/api/v1/builds/variables/%s?scope=%s&project=SEC&repository=app&environment=open"
	admin.MustDo(http.MethodPut, apitest.Fmt(vars, "PROT", "repository"), map[string]any{"value": "protected-1", "secret": true, "protected": true}, nil)
	admin.MustDo(http.MethodPut, apitest.Fmt(vars, "PLAIN", "repository"), map[string]any{"value": "plain-1"}, nil)
	// An environment that nobody guards: any branch may deploy to it.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SEC/app/environments/open", map[string]any{"refs": []string{},
		"deployerUsers": []int64{}, "deployerGroups": []int64{}, "approvals": 0, "approverUsers": []int64{}, "approverGroups": []int64{}}, nil)
	admin.MustDo(http.MethodPut, apitest.Fmt(vars, "OPEN_PROT", "environment"), map[string]any{"value": "open-protected", "protected": true}, nil)
	admin.MustDo(http.MethodPut, apitest.Fmt(vars, "OPEN_PLAIN", "environment"), map[string]any{"value": "open-plain"}, nil)

	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SEC/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": protectionPipeline}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SEC/app/files", map[string]any{"branch": "main", "newBranch": "feature",
		"path": "x.txt", "content": "x\n"}, nil)
	env.WorkJobs()

	agent := newFakeAgent(t, env)
	jobs := map[string]*protocol.JobPayload{}
	complete := func(p *protocol.JobPayload) {
		t.Helper()
		if r := agent.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: protocol.StateSucceeded}); r.Status >= 300 {
			t.Fatalf("complete: %d %s", r.Status, r.Body)
		}
	}
	for range 6 {
		p := agent.claim()
		jobs[p.Branch+"/"+p.Job.Name] = p
		if p.Job.Environment != "" {
			complete(p) // one deployment per environment at a time
		}
	}
	expect := func(job string, names ...string) {
		t.Helper()
		if jobs[job] == nil {
			t.Fatalf("no job %s among %v", job, jobs)
		}
		for _, name := range names {
			if jobs[job].Variables[name] == "" {
				t.Errorf("%s lacks %s", job, name)
			}
		}
	}
	expect("main/build", "PROT", "PLAIN")
	expect("main/open-environment", "PROT", "OPEN_PROT", "OPEN_PLAIN")
	expect("feature/build", "PLAIN")
	expect("feature/new-environment", "PLAIN")
	expect("feature/open-environment", "OPEN_PLAIN")
	expectNot := func(job string, names ...string) {
		t.Helper()
		for _, name := range names {
			if _, ok := jobs[job].Variables[name]; ok {
				t.Errorf("%s has %s", job, name)
			}
		}
	}
	expectNot("feature/build", "PROT")
	expectNot("feature/new-environment", "PROT")
	expectNot("feature/open-environment", "PROT", "OPEN_PROT")

	// Caches: a branch can neither plant a cache for protected jobs nor read theirs.
	mainJob, featureJob := jobs["main/build"], jobs["feature/build"]
	if r := agent.do(mainJob, http.MethodPut, "/caches/deps", bytes.NewReader([]byte("from main"))); r.Status != http.StatusNoContent {
		t.Fatalf("cache upload: %d %s", r.Status, r.Body)
	}
	if r := agent.do(featureJob, http.MethodGet, "/caches/deps", nil); r.Status != http.StatusNotFound {
		t.Errorf("the branch read the protected cache: %d %q", r.Status, r.Body)
	}
	if r := agent.do(featureJob, http.MethodPut, "/caches/deps", bytes.NewReader([]byte("from feature"))); r.Status != http.StatusNoContent {
		t.Fatalf("cache upload: %d %s", r.Status, r.Body)
	}
	if r := agent.do(mainJob, http.MethodGet, "/caches/deps", nil); string(r.Body) != "from main" {
		t.Errorf("the protected job restored %q", r.Body)
	}
	if r := agent.do(featureJob, http.MethodGet, "/caches/deps", nil); string(r.Body) != "from feature" {
		t.Errorf("the branch job restored %q", r.Body)
	}
	complete(mainJob)
	complete(featureJob)

	// Manual runs: only the variables of the run form.
	if r := admin.Do(http.MethodPost, "/api/v1/code/repos/SEC/app/runs", map[string]any{"ref": "main", "pipeline": "ci",
		"variables": map[string]string{"PROT": "overridden"}}); r.Code() != "run.variable" {
		t.Errorf("undeclared variable: %d %s", r.Status, r.Body)
	}
	// A tag whose name matches a protected branch pattern stays a tag (tags are not protected here).
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("admin", token.Token)
	work := t.TempDir()
	git(t, work, "clone", u.String(), "app")
	dir := filepath.Join(work, "app")
	git(t, dir, "tag", "release-1", "origin/feature")
	git(t, dir, "push", "origin", "release-1")
	env.WorkJobs()
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/SEC/app/runs", map[string]any{"ref": "release-1", "pipeline": "ci",
		"variables": map[string]string{"CHOICE": "b"}}, nil)
	for range 3 {
		p := agent.claim()
		complete(p)
		if p.Tag != "release-1" || p.Branch != "" || p.Variables["CHOICE"] != "b" {
			t.Errorf("manual run: tag %q branch %q CHOICE %q", p.Tag, p.Branch, p.Variables["CHOICE"])
		}
		if _, ok := p.Variables["PROT"]; ok {
			t.Errorf("the tag's run %s received the protected value", p.Job.Name)
		}
		if strings.Contains(strings.Join(p.Mask, ","), "protected-1") {
			t.Errorf("the protected secret is among the values to mask")
		}
	}
}

// TestMergeChecksAndStatuses: statuses set through the API (by anyone who can write) count for merging only if the
// repository says so, and running builds hold a merge back.
func TestMergeChecksAndStatuses(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MC", "name": "Checks"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MC/repos", map[string]any{"name": "app", "readme": true}, nil)
	settings := func(external bool) {
		admin.MustDo(http.MethodPatch, "/api/v1/code/repos/MC/app", map[string]any{"settings": map[string]any{
			"mergeStrategies": []string{"merge"}, "requiredApprovals": 0, "noNeedsWork": false, "tasksResolved": false,
			"requiredBuilds": 1, "externalStatuses": external, "resetApprovals": false, "deleteSourceBranch": false,
			"defaultReviewers": []int64{}}}, nil)
	}
	settings(false)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/MC/app/files", map[string]any{"branch": "main", "newBranch": "feature",
		"path": "x.txt", "content": "x\n"}, nil)
	var pr struct {
		Number    int    `json:"number"`
		SourceSHA string `json:"sourceSha"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/MC/app/pull-requests", map[string]any{"title": "X", "source": "feature"}, &pr)
	builds := func() (bool, string) {
		t.Helper()
		var got struct {
			Checks []struct {
				Key     string `json:"key"`
				OK      bool   `json:"ok"`
				Message string `json:"message"`
			} `json:"checks"`
		}
		admin.Get(apitest.Fmt("/api/v1/code/repos/MC/app/pull-requests/%d", pr.Number), &got)
		for _, c := range got.Checks {
			if c.Key == "builds" {
				return c.OK, c.Message
			}
		}
		t.Fatalf("no builds check: %+v", got.Checks)
		return false, ""
	}
	status := func(context, state string) {
		admin.MustDo(http.MethodPost, "/api/v1/code/repos/MC/app/statuses/"+pr.SourceSHA, map[string]any{"context": context, "state": state}, nil)
	}
	status("ext/ci", "success")
	if ok, msg := builds(); ok {
		t.Errorf("an external status counted: %s", msg)
	}
	settings(true)
	if ok, msg := builds(); !ok {
		t.Errorf("external statuses do not count: %s", msg)
	}
	status("ext/lint", "running")
	if ok, msg := builds(); ok || !strings.Contains(msg, "running") {
		t.Errorf("a running build did not hold the merge back: %v %s", ok, msg)
	}
}

const levelsPipeline = `version: 1
pipelines:
  ci:
    on:
      push: { branches: ["**"] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [fake]
            steps: [{ run: "true" }]
`

// TestProtectionLevels: a repository's administrators control its branch permissions and environments, so these
// unlock only the repository's protected values; those of the code project and the installation need a branch
// permission of the code project (audit H5: a self-exempting rule of the repository unlocked them).
func TestProtectionLevels(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	bob := env.CreateUser("bob")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	bob.Get("/api/v1/me", &me)
	bobID := me.User.ID
	admin.Get("/api/v1/me", &me)
	adminID := me.User.ID
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LVL", "name": "Levels"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/LVL/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LVL/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": bobID}, "permission": "repo.admin"}}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/LVL/branch-permissions", []map[string]any{
		{"pattern": "main", "kind": "restrict-push", "exemptUsers": []int64{adminID}, "exemptGroups": []int64{}}}, nil)
	set := func(c *apitest.Client, name, query string) {
		t.Helper()
		c.MustDo(http.MethodPut, "/api/v1/builds/variables/"+name+"?"+query, map[string]any{"value": "value-of-" + name, "secret": true,
			"protected": true}, nil)
	}
	set(admin, "INSTANCE_PROT", "scope=instance")
	set(admin, "PROJECT_PROT", "scope=project&project=LVL")
	set(bob, "REPO_PROT", "scope=repository&project=LVL&repository=app")

	// Bob protects a branch of his own with a rule that exempts him.
	bob.MustDo(http.MethodPut, "/api/v1/code/repos/LVL/app/branch-permissions", []map[string]any{
		{"pattern": "exfil", "kind": "restrict-push", "exemptUsers": []int64{bobID}, "exemptGroups": []int64{}}}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LVL/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": levelsPipeline}, nil)
	bob.MustDo(http.MethodPut, "/api/v1/code/repos/LVL/app/files", map[string]any{"branch": "main", "newBranch": "exfil",
		"path": "steal.sh", "content": "env\n"}, nil)
	env.WorkJobs()

	agent := newFakeAgent(t, env)
	jobs := map[string]*protocol.JobPayload{}
	for range 2 {
		p := agent.claim()
		jobs[p.Branch] = p
		if r := agent.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: protocol.StateSucceeded}); r.Status >= 300 {
			t.Fatalf("complete: %d %s", r.Status, r.Body)
		}
	}
	has := func(branch, name string) bool {
		t.Helper()
		if jobs[branch] == nil {
			t.Fatalf("no job on %s: %v", branch, jobs)
		}
		_, ok := jobs[branch].Variables[name]
		return ok
	}
	// The branch the code project protects gets every protected value.
	for _, name := range []string{"INSTANCE_PROT", "PROJECT_PROT", "REPO_PROT"} {
		if !has("main", name) {
			t.Errorf("main lacks %s", name)
		}
	}
	// Bob's branch gets the repository's protected value, which Bob controls anyway — not the others.
	if !has("exfil", "REPO_PROT") {
		t.Error("the repository's protected value did not reach the branch its administrator protects")
	}
	for _, name := range []string{"INSTANCE_PROT", "PROJECT_PROT"} {
		if has("exfil", name) {
			t.Errorf("a branch protected by the repository's administrator received %s", name)
		}
	}
	// All of them are masked in logs, whichever job ran.
	if mask := strings.Join(jobs["exfil"].Mask, ","); strings.Contains(mask, "value-of-PROJECT_PROT") {
		t.Errorf("the unprotected job's mask list contains a value it did not get: %s", mask)
	}
}

const previewPipeline = `version: 1
pipelines:
  pr:
    on:
      pull-request: { target-branches: [main] }
    stages:
      - name: Deploy
        jobs:
          - name: preview
            runs-on: [fake]
            environment: preview
            steps: [{ run: "true" }]
`

// TestPullRequestRunsAndDeployers: a pull request's run is started by whoever caused it (who opened it or pushed its
// source commit), not by its author, and its deployments wait for a deployer to start them by hand: anyone writing
// to the source branch puts code into the run (audit H6).
func TestPullRequestRunsAndDeployers(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	alice, bob := env.CreateUser("alice"), env.CreateUser("bob")
	id := func(c *apitest.Client) int64 {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		return me.User.ID
	}
	aliceID, bobID := id(alice), id(bob)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PRS", "name": "Previews"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PRS/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": aliceID}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": bobID}, "permission": "repo.write"}}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/app/environments/preview", map[string]any{"refs": []string{},
		"deployerUsers": []int64{aliceID}, "deployerGroups": []int64{}, "approvals": 0, "approverUsers": []int64{}, "approverGroups": []int64{}}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": previewPipeline}, nil)
	alice.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/app/files", map[string]any{"branch": "main", "newBranch": "feature",
		"path": "a.txt", "content": "a\n"}, nil)
	alice.MustDo(http.MethodPost, "/api/v1/code/repos/PRS/app/pull-requests", map[string]any{"title": "Feature", "source": "feature"}, nil)
	env.WorkJobs()
	type runSummary struct {
		Number      int `json:"number"`
		TriggeredBy *struct {
			Username string `json:"username"`
		} `json:"triggeredBy"`
	}
	latest := func() runSummary {
		t.Helper()
		var page struct {
			Items []runSummary `json:"items"`
		}
		admin.Get("/api/v1/code/repos/PRS/app/runs", &page)
		if len(page.Items) == 0 {
			t.Fatal("no runs")
		}
		return page.Items[0]
	}
	first := latest()
	if first.TriggeredBy == nil || first.TriggeredBy.Username != "alice" {
		t.Fatalf("the run of the opened pull request: %+v", first)
	}
	// Even the deployer's own pull request deploys only when a deployer starts the deployment.
	run := waitRunIn(t, admin, "PRS/app", apitest.Fmt("%d", first.Number), func(r *runDetail) bool { return r.job("preview").State == "waiting" })
	if w := run.job("preview").WaitingFor; w != "deployer" {
		t.Fatalf("deployment of a pull request run waits for %q", w)
	}

	// Bob pushes to Alice's source branch: the new run is Bob's.
	bob.MustDo(http.MethodPut, "/api/v1/code/repos/PRS/app/files", map[string]any{"branch": "feature", "path": "b.txt", "content": "b\n"}, nil)
	env.WorkJobs()
	second := latest()
	if second.Number == first.Number || second.TriggeredBy == nil || second.TriggeredBy.Username != "bob" {
		t.Fatalf("the run of Bob's push to the pull request: %+v", second)
	}
	run = waitRunIn(t, admin, "PRS/app", apitest.Fmt("%d", second.Number), func(r *runDetail) bool { return r.job("preview").State == "waiting" })
	if w := run.job("preview").WaitingFor; w != "deployer" {
		t.Fatalf("Bob's pull request run deploys without a deployer: waiting for %q", w)
	}
	if r := bob.Do(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/play", run.job("preview").ID), nil); r.Status < 400 {
		t.Fatalf("Bob, who is no deployer, started the deployment: %d %s", r.Status, r.Body)
	}
	// Alice looks at the change and starts the deployment herself.
	alice.MustDo(http.MethodPost, apitest.Fmt("/api/v1/builds/jobs/%d/play", run.job("preview").ID), nil, nil)
	agent := newFakeAgent(t, env)
	if p := agent.claim(); p.Job.Name != "preview" {
		t.Fatalf("claimed %+v", p.Job)
	}
}

const trustPipeline = `version: 1
pipelines:
  ci:
    on:
      push: { branches: ["**"] }
    stages:
      - name: Build
        jobs:
          - name: vault
            runs-on: [vault]
            steps: [{ run: "true" }]
          - name: compile
            runs-on: [build]
            steps: [{ run: "true" }]
`

// TestAgentTrust: an agent gets jobs by the labels it registered with or an administrator set, not by labels it
// announces later; new agents wait for an administrator's approval unless their registration token approves them;
// registration tokens expire after a week and register one agent unless they say otherwise (audit H7).
func TestAgentTrust(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TRU", "name": "Trust"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TRU/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/TRU/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": trustPipeline}, nil)
	env.WorkJobs()

	var tok struct {
		Token       string  `json:"token"`
		MaxUses     int     `json:"maxUses"`
		AutoApprove bool    `json:"autoApprove"`
		Expires     *string `json:"expires"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "build hosts"}, &tok)
	if tok.MaxUses != 1 || tok.AutoApprove || tok.Expires == nil {
		t.Fatalf("defaults of a registration token: %+v", tok)
	}
	register := func(token, name string, labels []string) (string, int) {
		t.Helper()
		var res protocol.RegisterResponse
		r := env.Anonymous().Do(http.MethodPost, "/api/agent/v1/register", protocol.RegisterRequest{Token: token, Name: name, Labels: labels,
			OS: "linux", Arch: "amd64", Version: "test"})
		if r.Status == http.StatusOK {
			r.Decode(&res)
		}
		return res.Secret, r.Status
	}
	claim := func(secret string, labels []string) *apitest.Response {
		t.Helper()
		return env.Anonymous().Do(http.MethodPost, "/api/agent/v1/jobs/claim", protocol.ClaimRequest{Labels: labels, OS: "linux",
			Arch: "amd64", Version: "test"}, "Authorization", "Bearer "+secret)
	}
	builder, status := register(tok.Token, "builder", []string{"build"})
	if status != http.StatusOK {
		t.Fatalf("registration: %d", status)
	}
	if _, status := register(tok.Token, "second", []string{"build"}); status != http.StatusUnauthorized {
		t.Fatalf("a second registration with a single-use token: %d", status)
	}

	// The new agent waits for approval: it is told so and gets no work.
	if r := claim(builder, []string{"build"}); r.Status != http.StatusNoContent || r.Header.Get("X-Kogge-Agent-State") != "awaiting-approval" {
		t.Fatalf("claim before approval: %d %q", r.Status, r.Header.Get("X-Kogge-Agent-State"))
	}
	var agents []struct {
		ID              int64    `json:"id"`
		Name            string   `json:"name"`
		Approved        bool     `json:"approved"`
		Labels          []string `json:"labels"`
		AnnouncedLabels []string `json:"announcedLabels"`
	}
	agents = nil // decoding into used elements would keep fields the answer leaves out
	admin.Get("/api/v1/admin/agents", &agents)
	if len(agents) != 1 || agents[0].Approved {
		t.Fatalf("agents: %+v", agents)
	}
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/agents/%d", agents[0].ID), map[string]any{"approve": true}, nil)

	// Announcing the label of other jobs does not get the agent those jobs.
	r := claim(builder, []string{"build", "vault"})
	var job protocol.JobPayload
	if r.Status != http.StatusOK {
		t.Fatalf("claim after approval: %d %s", r.Status, r.Body)
	}
	r.Decode(&job)
	if job.Job.Name != "compile" {
		t.Fatalf("the agent got %s", job.Job.Name)
	}
	var run runDetail
	admin.Get("/api/v1/code/repos/TRU/app/runs/1", &run)
	if st := run.job("vault").State; st != "pending" {
		t.Fatalf("the job of a label the agent only announced: %s", st)
	}
	agents = nil // decoding into used elements would keep fields the answer leaves out
	admin.Get("/api/v1/admin/agents", &agents)
	if got := strings.Join(agents[0].AnnouncedLabels, ","); !strings.Contains(got, "vault") {
		t.Fatalf("announced labels shown to administrators: %+v", agents[0])
	}
	// An administrator accepts them: now the agent gets the job.
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/agents/%d", agents[0].ID), map[string]any{"labels": agents[0].AnnouncedLabels}, nil)
	r = claim(builder, []string{"build", "vault"})
	if r.Status != http.StatusOK {
		t.Fatalf("claim after the labels were accepted: %d %s", r.Status, r.Body)
	}
	r.Decode(&job)
	if job.Job.Name != "vault" {
		t.Fatalf("the agent got %s", job.Job.Name)
	}
	agents = nil // decoding into used elements would keep fields the answer leaves out
	admin.Get("/api/v1/admin/agents", &agents)
	if len(agents[0].AnnouncedLabels) != 0 {
		t.Fatalf("accepted labels still shown as announced: %+v", agents[0])
	}

	// A token that approves its agents; an expired one registers none.
	var auto struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"maxUses": 0, "autoApprove": true}, &auto)
	for _, name := range []string{"fleet-1", "fleet-2"} {
		if _, status := register(auto.Token, name, []string{"build"}); status != http.StatusOK {
			t.Fatalf("%s: %d", name, status)
		}
	}
	agents = nil
	admin.Get("/api/v1/admin/agents", &agents)
	for _, a := range agents {
		if strings.HasPrefix(a.Name, "fleet-") && !a.Approved {
			t.Fatalf("an agent of an approving token waits: %+v", a)
		}
	}
	if _, err := env.App.Pool.Exec(context.Background(), "UPDATE agent_tokens SET expires_at = now() - interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if _, status := register(auto.Token, "late", []string{"build"}); status != http.StatusUnauthorized {
		t.Fatalf("registration with an expired token: %d", status)
	}
}
