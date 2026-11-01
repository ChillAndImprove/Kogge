// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

// editIssue changes the fields of an extracted issue.
func editIssue(t *testing.T, work, page, key string, edit func(fields map[string]any)) {
	t.Helper()
	file := filepath.Join(work, "jira", "projects", "SHOP", "issues", page)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, is := range p["issues"].([]any) {
		m := is.(map[string]any)
		if m["key"] == key {
			edit(m["fields"].(map[string]any))
			found = true
		}
	}
	if !found {
		t.Fatalf("%s is not on %s", key, page)
	}
	raw, _ = json.Marshal(p)
	if err := os.WriteFile(file, raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGoesOnAfterFailures(t *testing.T) {
	fake := fakeJira(t)
	work := t.TempDir()
	ctx := context.Background()
	j := importer.NewJira(importer.JiraOptions{URL: fake.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	// SHOP-1 cannot be loaded (its type was not extracted); its reporter, a user no other entity names before, is
	// created as a placeholder in the issue's savepoint and rolled back with it. SHOP-3 names the same user later.
	ghost := map[string]any{"name": "ghost", "key": "JIRAUSER19999", "displayName": "Ghost", "emailAddress": "ghost@example.org", "active": true}
	editIssue(t, work, "page-00000.json", "SHOP-1", func(f map[string]any) {
		f["issuetype"] = map[string]any{"id": "999", "name": "Unknown"}
		f["reporter"] = ghost
	})
	editIssue(t, work, "page-00001.json", "SHOP-3", func(f map[string]any) { f["assignee"] = ghost })

	env := apitest.New(t)
	admin := env.Admin()
	env.CreateUser("mmuster")
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Log: quiet()}
	strict := loader
	strict.StopOnError = true
	if _, err := j.Load(ctx, strict); err == nil || !strings.Contains(err.Error(), "SHOP-1") {
		t.Fatalf("the strict load: %v", err)
	}
	rep, err := j.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["failures"] != 1 || len(rep.Failures) != 1 || rep.Failures[0].Name != "SHOP-1" || rep.Failures[0].SourceID != "10001" ||
		!strings.Contains(rep.Failures[0].Error, "was not extracted") {
		t.Fatalf("failures: %v %+v", rep.Counts, rep.Failures)
	}
	if rep.Counts["issues"] != 3 {
		t.Errorf("the other issues were not loaded: %v", rep.Counts)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "SHOP-2: the parent SHOP-1 was not migrated") {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	var bug struct {
		Assignee *struct {
			Username string `json:"username"`
		} `json:"assignee"`
	}
	admin.Get("/api/v1/issues/SHOP-3", &bug)
	if bug.Assignee == nil || bug.Assignee.Username != "ghost" {
		t.Errorf("the assignee created after the rolled-back issue: %+v", bug.Assignee)
	}
	var stored importer.Report
	raw, _ := os.ReadFile(filepath.Join(work, "jira-report.json"))
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored.Failures) != 1 {
		t.Errorf("the report on disk: %v %+v", err, stored.Failures)
	}
	if strings.Count(strings.Join(stored.Placeholders, ","), "ghost") != 1 {
		t.Errorf("placeholders: %v", stored.Placeholders)
	}
}
