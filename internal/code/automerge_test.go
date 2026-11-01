// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

// ciPipeline builds every pull request into main on an agent with the label ci.
const ciPipeline = `version: 1
pipelines:
  ci:
    on:
      pull-request: { target-branches: [main] }
    stages:
      - name: Build
        jobs:
          - name: build
            runs-on: [ci]
            steps: [{ run: "true" }]
`

// ciAgent speaks the agent protocol and reports the results the test chooses, without running anything.
type ciAgent struct {
	t      *testing.T
	c      *apitest.Client
	secret string
}

func newCIAgent(t *testing.T, env *apitest.Env) *ciAgent {
	t.Helper()
	var reg struct {
		Token string `json:"token"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "ci", "autoApprove": true}, &reg)
	a := &ciAgent{t: t, c: env.Anonymous()}
	var res protocol.RegisterResponse
	a.c.MustDo(http.MethodPost, "/api/agent/v1/register", protocol.RegisterRequest{Token: reg.Token, Name: "ci", Labels: []string{"ci"},
		OS: "linux", Arch: "amd64", Version: "test"}, &res)
	a.secret = res.Secret
	return a
}

// build takes the next job, which must be one of commit ("": any), and finishes it with state.
func (a *ciAgent) build(commit, state string) {
	a.t.Helper()
	r := a.c.Do(http.MethodPost, "/api/agent/v1/jobs/claim", protocol.ClaimRequest{Labels: []string{"ci"}, OS: "linux", Arch: "amd64",
		Version: "test"}, "Authorization", "Bearer "+a.secret)
	if r.Status != http.StatusOK {
		a.t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	var job protocol.JobPayload
	r.Decode(&job)
	if commit != "" && job.Commit != commit {
		a.t.Fatalf("the next job builds %s, want %s", job.Commit, commit)
	}
	if r := a.c.Do(http.MethodPost, apitest.Fmt("/api/agent/v1/jobs/%d/complete", job.ID), protocol.CompleteRequest{State: state},
		"Authorization", "Bearer "+job.Token); r.Status >= 300 {
		a.t.Fatalf("complete: %d %s", r.Status, r.Body)
	}
}

// pending counts the build jobs waiting for an agent.
func pending(t *testing.T, env *apitest.Env) int {
	t.Helper()
	var n int
	if err := env.App.Pool.QueryRow(context.Background(), "SELECT count(*) FROM run_jobs WHERE state = 'pending'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type autoMergeView struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	SourceSHA string `json:"sourceSha"`
	MergeSHA  string `json:"mergeSha"`
	Version   int    `json:"version"`
	ClosedBy  *struct {
		Username string `json:"username"`
	} `json:"closedBy"`
	AutoMerge *struct {
		EnabledBy struct {
			Username string `json:"username"`
		} `json:"enabledBy"`
		Strategy string `json:"strategy"`
		Blocked  string `json:"blocked"`
	} `json:"autoMerge"`
	Permissions struct {
		Merge            bool `json:"merge"`
		DisableAutoMerge bool `json:"disableAutoMerge"`
	} `json:"permissions"`
}

type autoMergeFixture struct {
	t                *testing.T
	env              *apitest.Env
	admin            *apitest.Client
	dev, mia, olga   *apitest.Client
	rita             *apitest.Client
	ids              map[string]int64
	agent            *ciAgent
	repo, prs, files string
}

// newAutoMergeFixture: a repository whose pull requests into main need a successful build; dev writes pull
// requests, mia and olga can merge, rita reads.
func newAutoMergeFixture(t *testing.T) *autoMergeFixture {
	env := apitest.New(t)
	f := &autoMergeFixture{t: t, env: env, admin: env.Admin(), dev: env.CreateUser("dev"), mia: env.CreateUser("mia"),
		olga: env.CreateUser("olga"), rita: env.CreateUser("rita"), ids: map[string]int64{}}
	for name, c := range map[string]*apitest.Client{"admin": f.admin, "dev": f.dev, "mia": f.mia, "olga": f.olga, "rita": f.rita} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		f.ids[name] = me.User.ID
	}
	f.admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "AM", "name": "Auto"}, nil)
	f.admin.MustDo(http.MethodPost, "/api/v1/code/projects/AM/repos", map[string]any{"name": "app", "readme": true}, nil)
	f.repo, f.prs, f.files = "/api/v1/code/repos/AM/app", "/api/v1/code/repos/AM/app/pull-requests", "/api/v1/code/repos/AM/app/files"
	f.grants("repo.write")
	f.settings(0)
	f.admin.MustDo(http.MethodPut, f.files, map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": ciPipeline}, nil)
	f.admin.MustDo(http.MethodPut, f.files, map[string]any{"branch": "main", "path": "shared.txt", "content": "shared\n"}, nil)
	f.agent = newCIAgent(t, env)
	env.WorkJobs()
	return f
}

// grants gives dev, olga and mia write access (mia: miaPerm) and rita read access.
func (f *autoMergeFixture) grants(miaPerm string) {
	grants := []map[string]any{{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": f.ids["admin"]}}}
	for name, perm := range map[string]string{"dev": "repo.write", "olga": "repo.write", "mia": miaPerm, "rita": "repo.read"} {
		grants = append(grants, map[string]any{"permission": perm, "holder": map[string]any{"type": "user", "id": f.ids[name]}})
	}
	f.admin.MustDo(http.MethodPut, "/api/v1/code/projects/AM/permissions", grants, nil)
}

func (f *autoMergeFixture) settings(approvals int) {
	f.admin.MustDo(http.MethodPatch, f.repo, map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"squash", "merge", "rebase"}, "requiredApprovals": approvals, "noNeedsWork": true, "tasksResolved": true,
		"requiredBuilds": 1, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{}}}, nil)
}

// open opens a pull request by dev of a new branch that changes path.
func (f *autoMergeFixture) open(branch, path, content string) autoMergeView {
	f.t.Helper()
	f.dev.MustDo(http.MethodPut, f.files, map[string]any{"branch": "main", "newBranch": branch, "path": path, "content": content}, nil)
	var pr autoMergeView
	f.dev.MustDo(http.MethodPost, f.prs, map[string]any{"title": "Change " + path, "source": branch}, &pr)
	f.env.WorkJobs()
	return pr
}

func (f *autoMergeFixture) get(c *apitest.Client, number int) autoMergeView {
	f.t.Helper()
	var pr autoMergeView
	c.Get(apitest.Fmt("%s/%d", f.prs, number), &pr)
	return pr
}

// commit commits a change of a file to a branch as c and returns the commit.
func (f *autoMergeFixture) commit(c *apitest.Client, branch, path, content string) string {
	f.t.Helper()
	var info struct {
		SHA string `json:"sha"`
	}
	c.MustDo(http.MethodPut, f.files, map[string]any{"branch": branch, "path": path, "content": content}, &info)
	f.env.WorkJobs()
	return info.SHA
}

// events returns the events of the pull request's timeline as kind:reason (or kind:strategy).
func (f *autoMergeFixture) events(number int) []string {
	f.t.Helper()
	var timeline []struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	f.dev.Get(apitest.Fmt("%s/%d/timeline", f.prs, number), &timeline)
	var out []string
	for _, a := range timeline {
		if !strings.HasPrefix(a.Kind, "auto-merge") && a.Kind != "merged" {
			continue
		}
		detail, _ := a.Data["reason"].(string)
		if detail == "" {
			detail, _ = a.Data["strategy"].(string)
		}
		if a.Data["auto"] == true {
			detail += ",auto"
		}
		out = append(out, a.Kind+":"+detail)
	}
	return out
}

// notified returns the events of c's notifications about pull request number.
func (f *autoMergeFixture) notified(c *apitest.Client, prefix string) []string {
	f.t.Helper()
	var list []struct {
		Event string `json:"event"`
		Title string `json:"title"`
	}
	c.Get("/api/v1/notifications?limit=200", &list)
	var out []string
	for _, n := range list {
		if strings.HasPrefix(n.Title, prefix) && strings.Contains(n.Event, "auto-merge") {
			out = append(out, n.Event)
		}
	}
	slices.Sort(out)
	return out
}

// TestAutoMerge: auto-merge merges for who turned it on as soon as the last check passes (here: the build), keeps
// waiting through their own pushes, and ends — with a notice to them and the author — when someone else pushes,
// when they may no longer merge, or when someone turns it off; the pull request list shows it.
func TestAutoMerge(t *testing.T) {
	f := newAutoMergeFixture(t)

	// Merged when the required build turns green.
	pr := f.open("feature/a", "a.txt", "a\n")
	if r := f.rita.Do(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "squash"}); r.Status != http.StatusForbidden {
		t.Errorf("a reader turned auto-merge on: %d %s", r.Status, r.Body)
	}
	if r := f.mia.Do(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "fast-forward"}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("a strategy the repository does not allow: %d %s", r.Status, r.Body)
	}
	var on autoMergeView
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "squash", "version": pr.Version}, &on)
	if on.AutoMerge == nil || on.AutoMerge.EnabledBy.Username != "mia" || on.AutoMerge.Strategy != "squash" || on.State != "open" {
		t.Fatalf("auto-merge on = %+v", on)
	}
	f.env.WorkJobs()
	if got := f.get(f.rita, pr.Number); got.State != "open" || got.AutoMerge == nil || got.AutoMerge.Blocked != "" || got.Permissions.DisableAutoMerge {
		t.Fatalf("waiting for the build = %+v", got)
	}
	var list struct {
		Items []autoMergeView `json:"items"`
	}
	f.rita.Get(f.prs, &list)
	if len(list.Items) != 1 || list.Items[0].AutoMerge == nil || list.Items[0].AutoMerge.Strategy != "squash" {
		t.Errorf("the list does not show auto-merge: %+v", list.Items)
	}
	f.agent.build(pr.SourceSHA, protocol.StateSucceeded)
	f.env.WorkJobs()
	merged := f.get(f.dev, pr.Number)
	if merged.State != "merged" || merged.AutoMerge != nil || merged.ClosedBy == nil || merged.ClosedBy.Username != "mia" {
		t.Fatalf("after the build = %+v", merged)
	}
	if got := f.events(pr.Number); !slices.Equal(got, []string{"auto-merge-enabled:squash", "merged:squash,auto"}) {
		t.Errorf("timeline = %v", got)
	}
	prefix := apitest.Fmt("#%d ", pr.Number)
	if got := f.notified(f.dev, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-enabled", "pull-request.auto-merged"}) {
		t.Errorf("the author was told %v", got)
	}
	if got := f.notified(f.mia, prefix); !slices.Equal(got, []string{"pull-request.auto-merged"}) {
		t.Errorf("who turned it on was told %v", got)
	}

	// Pushes: who turned it on keeps working, someone else's push ends it.
	pr = f.open("feature/b", "b.txt", "b\n")
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "merge"}, nil)
	f.env.WorkJobs()
	own := f.commit(f.mia, "feature/b", "b.txt", "b, better\n")
	if got := f.get(f.dev, pr.Number); got.AutoMerge == nil || got.SourceSHA != own {
		t.Fatalf("after a push of who turned it on = %+v", got)
	}
	other := f.commit(f.olga, "feature/b", "b.txt", "b, by olga\n")
	if got := f.get(f.dev, pr.Number); got.AutoMerge != nil || got.SourceSHA != other {
		t.Fatalf("after someone else's push = %+v", got)
	}
	for _, sha := range []string{pr.SourceSHA, own, other} {
		f.agent.build(sha, protocol.StateSucceeded)
	}
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "open" {
		t.Errorf("merged without auto-merge: %+v", got)
	}
	if got := f.events(pr.Number); !slices.Equal(got, []string{"auto-merge-enabled:merge", "auto-merge-cancelled:pushed"}) {
		t.Errorf("timeline = %v", got)
	}
	prefix = apitest.Fmt("#%d ", pr.Number)
	if got := f.notified(f.mia, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-cancelled"}) {
		t.Errorf("who turned it on was told %v", got)
	}
	// Someone else's push behind a push of their own, both processed together, ends it too.
	hidden := f.open("feature/b2", "b2.txt", "b2\n")
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, hidden.Number), map[string]any{}, nil)
	f.env.WorkJobs()
	f.olga.MustDo(http.MethodPut, f.files, map[string]any{"branch": "feature/b2", "path": "b2.txt", "content": "b2, by olga\n"}, nil)
	f.mia.MustDo(http.MethodPut, f.files, map[string]any{"branch": "feature/b2", "path": "b2.txt", "content": "b2, by mia\n"}, nil)
	f.env.WorkJobs()
	if got := f.get(f.dev, hidden.Number); got.AutoMerge != nil {
		t.Fatalf("after someone else's push and one of theirs = %+v", got)
	}
	for range pending(t, f.env) {
		f.agent.build("", protocol.StateSucceeded)
	}
	f.env.WorkJobs()
	if got := f.get(f.dev, hidden.Number); got.State != "open" {
		t.Errorf("merged without auto-merge: %+v", got)
	}

	// Losing the permission to merge ends it at the next look, with a notice; nothing is merged.
	pr = f.open("feature/c", "c.txt", "c\n")
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{}, nil)
	f.env.WorkJobs()
	f.grants("repo.read")
	f.agent.build(pr.SourceSHA, protocol.StateSucceeded)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "open" || got.AutoMerge != nil {
		t.Fatalf("after losing the permission = %+v", got)
	}
	if got := f.events(pr.Number); !slices.Equal(got, []string{"auto-merge-enabled:squash", "auto-merge-cancelled:permission"}) {
		t.Errorf("timeline = %v", got)
	}
	prefix = apitest.Fmt("#%d ", pr.Number)
	if got := f.notified(f.mia, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-cancelled"}) {
		t.Errorf("who lost the permission was told %v", got)
	}
	f.grants("repo.write")

	// Turned off: by the author (readers may not).
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{}, nil)
	if r := f.rita.Do(http.MethodDelete, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), nil); r.Status != http.StatusForbidden {
		t.Errorf("a reader turned auto-merge off: %d %s", r.Status, r.Body)
	}
	var off autoMergeView
	f.dev.MustDo(http.MethodDelete, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), nil, &off)
	if off.AutoMerge != nil {
		t.Errorf("still on: %+v", off)
	}
	f.env.WorkJobs()
	if got := f.notified(f.mia, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-cancelled", "pull-request.auto-merge-cancelled"}) {
		t.Errorf("who turned it on was told %v", got)
	}
	if got := f.get(f.dev, pr.Number); got.State != "open" {
		t.Errorf("merged after auto-merge was turned off: %+v", got)
	}
}

// TestAutoMergeBlocked: a conflict blocks auto-merge — who turned it on and the author hear of it once — and it
// merges once the conflict is resolved and the checks pass again; a failed build blocks it the same way.
func TestAutoMergeBlocked(t *testing.T) {
	f := newAutoMergeFixture(t)
	pr := f.open("feature/d", "shared.txt", "shared, changed by the branch\n")
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "merge"}, nil)
	f.env.WorkJobs()
	// main changes the same line: a conflict.
	f.commit(f.admin, "main", "shared.txt", "shared, changed on main\n")
	got := f.get(f.dev, pr.Number)
	if got.AutoMerge == nil || got.AutoMerge.Blocked != "conflicts" {
		t.Fatalf("after the conflict = %+v", got)
	}
	// Further looks at it (the build of the old commit finishes) say nothing new.
	f.agent.build(pr.SourceSHA, protocol.StateSucceeded)
	f.env.WorkJobs()
	prefix := apitest.Fmt("#%d ", pr.Number)
	if got := f.notified(f.mia, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-blocked"}) {
		t.Errorf("who turned it on was told %v", got)
	}
	if got := f.notified(f.dev, prefix); !slices.Equal(got, []string{"pull-request.auto-merge-blocked", "pull-request.auto-merge-enabled"}) {
		t.Errorf("the author was told %v", got)
	}
	// Who turned it on resolves the conflict: it waits for the new build, which fails — blocked again — and then
	// for the next one, which merges.
	fixed := f.commit(f.mia, "feature/d", "shared.txt", "shared, changed on main\n")
	if got := f.get(f.dev, pr.Number); got.AutoMerge == nil || got.AutoMerge.Blocked != "" || got.State != "open" {
		t.Fatalf("after resolving the conflict = %+v", got)
	}
	f.agent.build(fixed, protocol.StateFailed)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.AutoMerge == nil || got.AutoMerge.Blocked != "builds" {
		t.Fatalf("after the failed build = %+v", got)
	}
	again := f.commit(f.mia, "feature/d", "fix.txt", "fixed\n")
	f.agent.build(again, protocol.StateSucceeded)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "merged" {
		t.Fatalf("after the fix = %+v", got)
	}
	if got := f.events(pr.Number); !slices.Equal(got, []string{"auto-merge-enabled:merge", "auto-merge-blocked:conflicts", "auto-merge-blocked:builds",
		"merged:merge,auto"}) {
		t.Errorf("timeline = %v", got)
	}
}

// TestAutoMergeOtherChecks: the status another build system reports, and a pipeline that its paths filter skips (no
// run announces that), let an auto-merge merge as a build does.
func TestAutoMergeOtherChecks(t *testing.T) {
	f := newAutoMergeFixture(t)
	f.admin.MustDo(http.MethodPut, f.files, map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": strings.Replace(ciPipeline, "{ target-branches: [main] }", `{ target-branches: [main], paths: ["src/**"] }`, 1)}, nil)
	settings := func(builds int, external bool) {
		f.admin.MustDo(http.MethodPatch, f.repo, map[string]any{"settings": map[string]any{
			"mergeStrategies": []string{"squash"}, "requiredApprovals": 0, "noNeedsWork": true, "tasksResolved": true, "requiredBuilds": builds,
			"externalStatuses": external, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{}}}, nil)
	}

	// Kogge's build and another system's check: the check reported last merges.
	settings(2, true)
	pr := f.open("feature/f", "src/f.go", "package src\n")
	f.agent.build(pr.SourceSHA, protocol.StateSucceeded)
	f.env.WorkJobs()
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{}, nil)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "open" {
		t.Fatalf("merged with one of two builds: %+v", got)
	}
	f.olga.MustDo(http.MethodPost, f.repo+"/statuses/"+pr.SourceSHA, map[string]any{"context": "lint", "state": "success"}, nil)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "merged" {
		t.Fatalf("after the other system's check = %+v", got)
	}

	// The change ends up touching no file of the pipeline's paths: it is skipped, which counts as done.
	settings(1, false)
	pr = f.open("feature/g", "src/g.go", "package src\n")
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{}, nil)
	f.env.WorkJobs()
	f.mia.MustDo(http.MethodPut, f.files, map[string]any{"branch": "feature/g", "path": "src/g.go", "delete": true}, nil)
	f.commit(f.mia, "feature/g", "docs/g.md", "# G\n")
	if got := f.get(f.dev, pr.Number); got.State != "merged" {
		t.Fatalf("after the pipeline was skipped = %+v", got)
	}
}

// TestAutoMergeOnce: many looks at an auto-merge at the same time — the jobs of a burst of events, run in parallel —
// merge the pull request once.
func TestAutoMergeOnce(t *testing.T) {
	f := newAutoMergeFixture(t)
	f.settings(1)
	pr := f.open("feature/e", "e.txt", "e\n")
	f.agent.build(pr.SourceSHA, protocol.StateSucceeded)
	f.env.WorkJobs()
	f.mia.MustDo(http.MethodPut, apitest.Fmt("%s/%d/auto-merge", f.prs, pr.Number), map[string]any{"strategy": "merge"}, nil)
	f.env.WorkJobs()
	if got := f.get(f.dev, pr.Number); got.State != "open" || got.AutoMerge == nil {
		t.Fatalf("waiting for the approval = %+v", got)
	}
	// A burst of events, each of which looks at the auto-merge; the approval lets the first look merge.
	for i := range 8 {
		f.olga.MustDo(http.MethodPost, apitest.Fmt("%s/%d/comments", f.prs, pr.Number), map[string]any{"markdown": apitest.Fmt("Look %d", i)}, nil)
	}
	f.olga.MustDo(http.MethodPut, apitest.Fmt("%s/%d/review", f.prs, pr.Number), map[string]any{"state": "approved"}, nil)
	var waiting int
	if err := f.env.App.Pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'code.auto-merge' AND state = 'available'").
		Scan(&waiting); err != nil || waiting < 9 {
		t.Fatalf("auto-merge jobs waiting: %d %v", waiting, err)
	}
	f.env.WorkJobs()
	got := f.get(f.dev, pr.Number)
	if got.State != "merged" {
		t.Fatalf("after the burst = %+v", got)
	}
	if events := f.events(pr.Number); !slices.Equal(events, []string{"auto-merge-enabled:merge", "merged:merge,auto"}) {
		t.Errorf("timeline = %v", events)
	}
	var failed int
	if err := f.env.App.Pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'code.auto-merge'
		AND (state <> 'completed' OR errors IS NOT NULL AND errors <> '{}')`).Scan(&failed); err != nil || failed != 0 {
		t.Errorf("auto-merge jobs that did not complete cleanly: %d %v", failed, err)
	}
	var merges int
	if err := f.env.App.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pushes WHERE protocol = 'merge'`).Scan(&merges); err != nil || merges != 1 {
		t.Errorf("merges recorded: %d %v", merges, err)
	}
}
