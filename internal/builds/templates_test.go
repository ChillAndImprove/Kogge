// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type template struct {
	ID          string `json:"id"`
	Builtin     bool   `json:"builtin"`
	Name        string `json:"name"`
	Content     string `json:"content"`
	Recommended bool   `json:"recommended"`
}

type check struct {
	Valid    bool `json:"valid"`
	Problems []struct {
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		Message string `json:"message"`
	} `json:"problems"`
	Pipelines []struct {
		ID       string   `json:"id"`
		Triggers []string `json:"triggers"`
		Jobs     int      `json:"jobs"`
	} `json:"pipelines"`
}

// TestTemplates: the built-in templates are valid pipeline files; those for the kind of a repository's project are
// recommended; Helling's administrators maintain templates of the organisation, which must be valid too.
func TestTemplates(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("devi")
	var me, adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	dev.Get("/api/v1/me", &me)
	admin.Get("/api/v1/me", &adminMe)

	var list []template
	dev.Get("/api/v1/builds/templates", &list)
	ids := []string{}
	for _, tpl := range list {
		ids = append(ids, tpl.ID)
		if !tpl.Builtin || tpl.Recommended {
			t.Errorf("template %s without a repository: %+v", tpl.ID, tpl)
		}
		// Every built-in template is a valid pipeline file.
		var c check
		dev.Post("/api/v1/builds/check", map[string]any{"content": tpl.Content}, &c)
		if !c.Valid || len(c.Pipelines) == 0 {
			t.Errorf("template %s: %+v", tpl.ID, c)
		}
	}
	if !slices.Equal(ids, []string{"go", "node", "maven", "gradle", "python", "dotnet", "container", "minimal"}) {
		t.Errorf("built-in templates = %v", ids)
	}
	if r := env.Anonymous().Do(http.MethodGet, "/api/v1/builds/templates", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.Status)
	}

	// A Go module with a Dockerfile: Go and container image are recommended.
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "CI", "name": "Continuous"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/CI/repos", map[string]any{"name": "App", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/CI/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": me.User.ID}},
	}, nil)
	for path, content := range map[string]string{"go.mod": "module example.com/app\n", "Dockerfile": "FROM scratch\n"} {
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/CI/app/files", map[string]any{"branch": "main", "path": path,
			"content": content}, nil)
	}
	dev.Get("/api/v1/builds/templates?project=CI&repository=app", &list)
	recommended := []string{}
	for _, tpl := range list {
		if tpl.Recommended {
			recommended = append(recommended, tpl.ID)
		}
	}
	if !slices.Equal(recommended, []string{"go", "container"}) {
		t.Errorf("recommended = %v", recommended)
	}
	other := env.CreateUser("otto")
	if r := other.Do(http.MethodGet, "/api/v1/builds/templates?project=CI&repository=app", nil); r.Status != http.StatusNotFound {
		t.Errorf("someone without access asked about the repository: %d", r.Status)
	}

	// Templates of the organisation: only Helling's administrators, only valid files, unique names.
	body := map[string]any{"name": "Java service", "description": "Our standard", "content": list[2].Content}
	if r := dev.Do(http.MethodPost, "/api/v1/builds/templates", body); r.Status != http.StatusForbidden {
		t.Errorf("a developer created a template: %d %s", r.Status, r.Body)
	}
	var saved template
	admin.Post("/api/v1/builds/templates", body, &saved)
	if !strings.HasPrefix(saved.ID, "org-") || saved.Name != "Java service" {
		t.Fatalf("saved = %+v", saved)
	}
	if r := admin.Do(http.MethodPost, "/api/v1/builds/templates", body); r.Code() != "template.name-taken" {
		t.Errorf("a second template of the name: %s", r.Body)
	}
	broken := map[string]any{"name": "Broken", "content": "version: 1\npipelines:\n  ci:\n    stages: 3\n"}
	if r := admin.Do(http.MethodPost, "/api/v1/builds/templates", broken); r.Code() != "template.invalid" ||
		!strings.Contains(string(r.Body), "line 4") {
		t.Errorf("an invalid template: %s", r.Body)
	}
	dev.Get("/api/v1/builds/templates", &list)
	if list[0].ID != saved.ID || list[0].Builtin {
		t.Errorf("the organisation's template does not come first: %+v", list[0])
	}
	body["name"] = "Java services"
	admin.MustDo(http.MethodPut, "/api/v1/builds/templates/"+saved.ID, body, nil)
	admin.MustDo(http.MethodDelete, "/api/v1/builds/templates/"+saved.ID, nil, nil)
	if r := admin.Do(http.MethodDelete, "/api/v1/builds/templates/"+saved.ID, nil); r.Status != http.StatusNotFound {
		t.Errorf("deleted twice: %d", r.Status)
	}
	if r := admin.Do(http.MethodDelete, "/api/v1/builds/templates/go", nil); r.Status != http.StatusNotFound {
		t.Errorf("a built-in template was deleted: %d", r.Status)
	}
	var actions []string
	rows, err := env.App.Pool.Query(context.Background(), `SELECT action FROM audit_events WHERE target_type = 'pipeline-template'
		ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if !slices.Equal(actions, []string{"pipeline-template.create", "pipeline-template.update", "pipeline-template.delete"}) {
		t.Errorf("audit = %v", actions)
	}
}

// TestCheckPipelineFile: the check names the mistakes of a file with line and column, or what a valid file
// defines.
func TestCheckPipelineFile(t *testing.T) {
	env := apitest.New(t)
	dev := env.CreateUser("devi")
	var c check
	dev.Post("/api/v1/builds/check", map[string]any{"content": pipelineFile}, &c)
	if !c.Valid || len(c.Pipelines) != 1 || c.Pipelines[0].ID != "ci" || c.Pipelines[0].Jobs != 3 ||
		!slices.Equal(c.Pipelines[0].Triggers, []string{"push", "manual", "api"}) {
		t.Errorf("valid file: %+v", c)
	}
	dev.Post("/api/v1/builds/check", map[string]any{"content": "version: 1\npipelines:\n  ci:\n    stages:\n      - name: x\n        jobs: nope\n"}, &c)
	if c.Valid || len(c.Problems) == 0 || c.Problems[0].Line != 6 {
		t.Errorf("invalid file: %+v", c)
	}
	dev.Post("/api/v1/builds/check", map[string]any{"content": "version: [1"}, &c)
	if c.Valid || len(c.Problems) != 1 {
		t.Errorf("no YAML: %+v", c)
	}
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/builds/check", map[string]any{"content": "x"}); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.Status)
	}
}
