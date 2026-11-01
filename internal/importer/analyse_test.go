// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

func figure(figs []importer.Figure, key string) int64 {
	for _, f := range figs {
		if f.Key == key {
			return f.Value
		}
	}
	return -1
}

func decision(a *importer.Analysis, code string) *importer.Decision {
	for i := range a.Decisions {
		if a.Decisions[i].Code == code {
			return &a.Decisions[i]
		}
	}
	return nil
}

func finding(a *importer.Analysis, kind, name string) *importer.Finding {
	for i := range a.Untranslatable {
		if f := &a.Untranslatable[i]; f.Kind == kind && strings.HasPrefix(f.Name, name) {
			return f
		}
	}
	return nil
}

func userMatch(a *importer.Analysis, name string) *importer.UserMatch {
	for i := range a.Users {
		if a.Users[i].Name == name {
			return &a.Users[i]
		}
	}
	return nil
}

// readReport checks that a report was written as JSON and HTML and returns the page.
func readReport(t *testing.T, work, name string, v any) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(work, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s.json: %v", name, err)
	}
	page, err := os.ReadFile(filepath.Join(work, name+".html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(page), "<!doctype html>") || strings.Contains(string(page), "<script") || strings.Contains(string(page), "http://") &&
		strings.Contains(string(page), "<link") {
		t.Errorf("%s.html is not a self-contained page", name)
	}
	return string(page)
}

func TestJiraAnalysis(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	env.CreateUser("mmuster")
	// A Kogge user with the user name of the source's jdoe, but another e-mail address: perhaps another person.
	env.CreateUser("jdoe")
	// The target has a project with the key of the source's, and a group of its own with a source group's name.
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Kogge's shop", "template": "kanban"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "jira-developers"}, nil)
	fake := fakeJira(t)
	work := t.TempDir()
	ctx := context.Background()
	j := importer.NewJira(importer.JiraOptions{URL: fake.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "jira/workflows/SHOP bugs.xml")); err != nil {
		t.Errorf("the workflow definition was not extracted: %v", err)
	}

	// Without a target: what the extraction holds, and what cannot be translated.
	a, err := j.Analyse(ctx, nil, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"projects": 1, "issues": 4, "comments": 5, "attachments": 1, "subtasks": 1, "worklogs": 1,
		"history": 2, "sprints": 3, "boards": 2, "versions": 1, "components": 1, "restrictedComments": 2} {
		if got := figure(a.Counts, key); got != want {
			t.Errorf("count %s = %d, want %d", key, got, want)
		}
	}
	if got := figure(a.Sizes, "attachmentFileBytes"); got != 8 {
		t.Errorf("attachment bytes = %d", got)
	}
	if u := userMatch(a, "jdoe"); u == nil || u.Match != "unknown" || u.Email != "john.doe@example.org" {
		t.Errorf("jdoe without a target: %+v", u)
	}
	if f := finding(a, "custom field", "Checklist"); f == nil || !strings.Contains(f.Note, "app") {
		t.Errorf("the custom field of an app: %+v", f)
	}
	for _, k := range [][2]string{{"permission", "VIEW_READONLY_WORKFLOW"}, {"permission holder", "userCustomField"}, {"quick filter", "SHOP board: Broken"},
		{"workflow condition", "com.atlassian.jira.workflow.condition.AlwaysFalseCondition"},
		{"workflow post-function", "com.onresolve.jira.groovy.GroovyFunctionPlugin"}} {
		if finding(a, k[0], k[1]) == nil {
			t.Errorf("no finding %s %s in %+v", k[0], k[1], a.Untranslatable)
		}
	}
	if f := finding(a, "workflow condition", "com.atlassian.jira.workflow.condition.AllowOnlyAssignee"); f == nil || !strings.Contains(f.Note, "assignee-only") {
		t.Errorf("a condition with a counterpart: %+v", f)
	}
	for _, code := range []string{"security-levels", "scripted-workflows", "comment-visibility", "users-unmatched"} {
		if decision(a, code) == nil {
			t.Errorf("no decision %s in %+v", code, a.Decisions)
		}
	}
	if d := decision(a, "key-collision"); d != nil {
		t.Errorf("a key collision without a target: %+v", d)
	}
	if len(a.Notes) != 1 || a.Notes[0].Kind != "workflow" || a.Notes[0].Name != "jira" {
		t.Errorf("extraction notes: %+v", a.Notes)
	}

	// With the target: users and keys are matched against it.
	target, err := importer.NewTarget(ctx, env.App.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	a, err = j.Analyse(ctx, target, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	if u := userMatch(a, "mmuster"); u == nil || u.Match != "username" || u.Kogge != "mmuster" || u.Note != "" {
		t.Errorf("mmuster: %+v", u)
	}
	if u := userMatch(a, "jdoe"); u == nil || u.Match != "username" || !strings.Contains(u.Note, "jdoe@example.com") {
		t.Errorf("jdoe: %+v", u)
	}
	if d := decision(a, "users-by-hand"); d == nil || len(d.Items) != 1 || !strings.HasPrefix(d.Items[0], "jdoe:") {
		t.Errorf("users to map by hand: %+v", d)
	}
	if d := decision(a, "key-collision"); d == nil || d.Severity != "high" || !strings.Contains(d.Message, "SHOP") {
		t.Errorf("key collision: %+v", d)
	}
	if d := decision(a, "group-renamed"); d == nil || len(d.Items) != 1 || !strings.Contains(d.Items[0], "jira-developers (migrated)") {
		t.Errorf("renamed group: %+v", d)
	}
	if a.Decisions[0].Severity != "high" {
		t.Errorf("decisions are not ordered by severity: %+v", a.Decisions)
	}
	var stored importer.Analysis
	page := readReport(t, work, "jira-analysis", &stored)
	if len(stored.Decisions) != len(a.Decisions) || stored.Target == "" {
		t.Errorf("the JSON report: %+v", stored)
	}
	for _, want := range []string{"Analysis of the Jira migration", "Decisions to take before the load", "Kogge has a project with the key SHOP",
		"SHOP bugs", "com.onresolve.jira.groovy.GroovyFunctionPlugin"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// In German.
	if _, err := j.Analyse(ctx, target, importer.German); err != nil {
		t.Fatal(err)
	}
	page = readReport(t, work, "jira-analysis", &stored)
	for _, want := range []string{`<html lang="de">`, "Entscheidungen vor dem Laden", "Kogge hat ein Projekt mit dem Schlüssel SHOP", "Vorgänge"} {
		if !strings.Contains(page, want) {
			t.Errorf("the German page lacks %q", want)
		}
	}

	// After a trial load, the analysis estimates the duration of the next one from the measured throughput.
	admin.MustDo(http.MethodDelete, "/api/v1/projects/SHOP", nil, nil)
	if _, err := j.Load(ctx, importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Log: quiet()}); err != nil {
		t.Fatal(err)
	}
	target2, err := importer.NewTarget(ctx, env.App.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer target2.Close()
	a, err = j.Analyse(ctx, target2, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	if a.Estimate == nil || a.Estimate.Seconds <= 0 || !strings.Contains(a.Estimate.Basis, "issues: 4") {
		t.Errorf("estimate: %+v", a.Estimate)
	}
	if decision(a, "key-collision") != nil {
		t.Errorf("the project loaded before counts as a key collision: %+v", a.Decisions)
	}
}

func TestConfluenceAnalysis(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "DOC", "name": "Kogge's docs", "open": false}, nil)
	fake := fakeConfluence(t)
	work := t.TempDir()
	ctx := context.Background()
	c := importer.NewConfluence(importer.ConfluenceOptions{URL: fake.URL, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
		History: true, PageSize: 2}, quiet())
	if err := c.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := importer.NewTarget(ctx, env.App.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	a, err := c.Analyse(ctx, target, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"spaces": 1, "pages": 4, "archivedPages": 1, "blogPosts": 1, "comments": 3, "inlineComments": 1,
		"attachments": 1, "restrictedPages": 1, "versions": 8, "labels": 3} {
		if got := figure(a.Counts, key); got != want {
			t.Errorf("count %s = %d, want %d", key, got, want)
		}
	}
	// Unsupported macros with their pages, and the one whose content would be cut.
	if f := finding(a, "macro", "gliffy"); f == nil || f.Count != 1 || len(f.Examples) != 1 || f.Examples[0] != "DOC: Documentation Home (1001)" {
		t.Errorf("the unsupported macro: %+v", f)
	}
	if d := decision(a, "macro-content-cut"); d == nil || len(d.Items) != 1 || d.Items[0] != "DOC: Secret Child (1004): section" {
		t.Errorf("cut macros: %+v", d)
	}
	for _, name := range []string{"info", "mathblock", "mathinline"} {
		if f := finding(a, "macro", name); f != nil {
			t.Errorf("a supported macro is reported: %+v", f)
		}
	}
	if d := decision(a, "labels-invalid"); d == nil || len(d.Items) != 1 || !strings.Contains(d.Items[0], "Bad Label") {
		t.Errorf("invalid labels: %+v", d)
	}
	if f := finding(a, "space permission", "space/delete_mail"); f == nil {
		t.Errorf("no finding for the unsupported space permission: %+v", a.Untranslatable)
	}
	if d := decision(a, "key-collision"); d == nil || !strings.Contains(d.Message, "DOC") {
		t.Errorf("key collision: %+v", a.Decisions)
	}
	if u := userMatch(a, "admin"); u == nil || u.Match != "username" {
		t.Errorf("admin: %+v", u)
	}
	if u := userMatch(a, "jdoe"); u == nil || u.Match != "new" {
		t.Errorf("jdoe: %+v", u)
	}
	var stored importer.Analysis
	page := readReport(t, work, "confluence-analysis", &stored)
	for _, want := range []string{"Macros", "gliffy", "section", "Permissions of the space DOC"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

func TestBitbucketAnalysis(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	// Kogge has a repository SHOP/web of its own: the load would take it over.
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SHOP", "name": "Shop"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SHOP/repos", map[string]any{"name": "web"}, nil)
	src := newBitbucket(t)
	work := t.TempDir()
	ctx := context.Background()
	bb := importer.NewBitbucket(importer.BitbucketOptions{URL: src.URL, Token: bbToken, Workdir: work}, quiet())
	if err := bb.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := importer.NewTarget(ctx, env.App.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	a, err := bb.Analyse(ctx, target, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"projects": 1, "personalProjects": 1, "repositories": 1, "pullRequests": 4, "open": 2,
		"comments": 4, "tasks": 1, "lfsObjects": 1, "branchPermissions": 4} {
		if got := figure(a.Counts, key); got != want {
			t.Errorf("count %s = %d, want %d", key, got, want)
		}
	}
	if got := figure(a.Sizes, "repositoryBytes"); got <= 0 {
		t.Errorf("repository size = %d", got)
	}
	if d := decision(a, "repository-taken-over"); d == nil || d.Severity != "high" || len(d.Items) != 1 || d.Items[0] != "SHOP/web" {
		t.Errorf("taken over: %+v", d)
	}
	if d := decision(a, "code-project-exists"); d == nil {
		t.Errorf("no decision for the existing code project: %+v", a.Decisions)
	}
	if d := decision(a, "fork-pull-requests"); d == nil || len(d.Items) != 1 || !strings.Contains(d.Items[0], "#4") {
		t.Errorf("pull requests from forks: %+v", d)
	}
	if d := decision(a, "webhooks"); d == nil || len(d.Items) != 1 || strings.Contains(d.Items[0], "secret") || !strings.Contains(d.Items[0], "Jenkins") {
		t.Errorf("webhooks: %+v", d)
	}
	if d := decision(a, "default-reviewer-conditions"); d == nil || len(d.Items) != 1 {
		t.Errorf("default reviewer conditions: %+v", d)
	}
	if d := decision(a, "groups-without-members"); d == nil {
		t.Errorf("no decision for the groups without members: %+v", a.Decisions)
	}
	for _, k := range [][2]string{{"hook", "Jira Commit Checker"}, {"branch permission", "on the branching model"}, {"not migrated", "personal project ~JDOE"}} {
		if finding(a, k[0], k[1]) == nil {
			t.Errorf("no finding %s %s in %+v", k[0], k[1], a.Untranslatable)
		}
	}
	var stored importer.Analysis
	readReport(t, work, "bitbucket-analysis", &stored)
}

func TestBambooAnalysis(t *testing.T) {
	src := fakeBamboo(t)
	work := t.TempDir()
	ctx := context.Background()
	bm := importer.NewBamboo(importer.BambooOptions{URL: src.URL, Token: bambooToken, Workdir: work,
		Repositories: map[string]string{"OPS-INFRA": "SHOP/web"}}, quiet())
	if err := bm.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := bm.Analyse(ctx, nil, importer.German)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"plans": 4, "exported": 3, "deployments": 1, "environments": 2, "placeholders": 1} {
		if got := figure(a.Counts, key); got != want {
			t.Errorf("count %s = %d, want %d", key, got, want)
		}
	}
	if d := decision(a, "plans-not-exported"); d == nil || len(d.Items) != 1 || d.Items[0] != "SHOP-OLD" {
		t.Errorf("not exported: %+v", d)
	}
	if d := decision(a, "secrets"); d == nil || !strings.Contains(strings.Join(d.Items, " "), "deploy_password") ||
		strings.Contains(strings.Join(d.Items, " "), "BAMSCRT") {
		t.Errorf("secrets: %+v", d)
	}
	if d := decision(a, "placeholders"); d == nil || len(d.Items) != 1 || d.Items[0] != "SHOP-WEB: com.example:upload" {
		t.Errorf("placeholders: %+v", d)
	}
	if f := finding(a, "Task", "com.example:upload"); f == nil || f.Count != 1 {
		t.Errorf("the task without a counterpart: %+v", a.Untranslatable)
	}
	var stored importer.Analysis
	page := readReport(t, work, "bamboo-analysis", &stored)
	if !strings.Contains(page, "Pläne") || !strings.Contains(page, "SHOP-WEB") {
		t.Errorf("the German page:\n%s", page)
	}
}
