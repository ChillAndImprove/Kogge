// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const triggerPipeline = `version: 1
pipelines:
  release:
    on:
      manual:
        variables:
          VERSION: { default: "0.0.0", description: Version of the library }
    stages:
      - name: Release
        jobs:
          - name: release
            steps: [{ run: "true" }]
  ci:
    on:
      push: { branches: ["**"] }
      api: false
    stages:
      - name: Build
        jobs:
          - name: build
            steps: [{ run: "true" }]
`

// TestPipelineTriggers: a repository's administrators create triggers whose tokens start API runs — with the
// variables of the run form — for the trigger's creator, while the creator may write to the repository; access
// tokens start API runs, too.
func TestPipelineTriggers(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	lead := env.CreateUser("lead")
	var leadMe, adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	lead.Get("/api/v1/me", &leadMe)
	admin.Get("/api/v1/me", &adminMe)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TRG", "name": "Triggers"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TRG/repos", map[string]any{"name": "lib", "readme": true}, nil)
	grant := func(permission string) {
		admin.MustDo(http.MethodPut, "/api/v1/code/projects/TRG/permissions", []map[string]any{
			{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
			{"permission": permission, "holder": map[string]any{"type": "user", "id": leadMe.User.ID}},
		}, nil)
	}
	grant("repo.admin")
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/TRG/lib/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": triggerPipeline}, nil)
	env.WorkJobs()

	// Creating: the token is shown once.
	var created struct {
		ID        int64  `json:"id"`
		Token     string `json:"token"`
		TokenHint string `json:"tokenHint"`
	}
	lead.MustDo(http.MethodPost, "/api/v1/code/repos/TRG/lib/triggers", map[string]any{"description": "Release of the platform"}, &created)
	if !strings.HasPrefix(created.Token, "kgt_") || created.TokenHint == "" || strings.Contains(created.Token, "…") {
		t.Fatalf("created = %+v", created)
	}
	var list []map[string]any
	lead.Get("/api/v1/code/repos/TRG/lib/triggers", &list)
	if len(list) != 1 || list[0]["token"] != nil || list[0]["description"] != "Release of the platform" {
		t.Errorf("triggers = %v", list)
	}

	anon := env.Anonymous()
	trigger := func(token string, body map[string]any) *apitest.Response {
		t.Helper()
		return anon.Do(http.MethodPost, "/api/v1/builds/trigger", body, "Authorization", "Bearer "+token)
	}
	r := trigger(created.Token, map[string]any{"ref": "main", "variables": map[string]string{"VERSION": "1.2.3"}})
	if r.Status != http.StatusOK {
		t.Fatalf("trigger: %d %s", r.Status, r.Body)
	}
	var runs []struct {
		Number   int    `json:"number"`
		Pipeline string `json:"pipeline"`
		URL      string `json:"url"`
	}
	r.Decode(&runs)
	if len(runs) != 1 || runs[0].Pipeline != "release" || !strings.HasSuffix(runs[0].URL, apitest.Fmt("/runs/%d", runs[0].Number)) {
		t.Fatalf("runs = %+v", runs)
	}
	var run struct {
		Event       string            `json:"event"`
		Trigger     string            `json:"trigger"`
		Variables   map[string]string `json:"variables"`
		TriggeredBy struct {
			Username string `json:"username"`
		} `json:"triggeredBy"`
	}
	admin.Get(apitest.Fmt("/api/v1/code/repos/TRG/lib/runs/%d", runs[0].Number), &run)
	if run.Event != "api" || run.Trigger != "Release of the platform" || run.Variables["VERSION"] != "1.2.3" || run.TriggeredBy.Username != "lead" {
		t.Errorf("run = %+v", run)
	}

	// Only the variables of the run form; only pipelines that API runs start.
	if r := trigger(created.Token, map[string]any{"variables": map[string]string{"DEPLOY": "yes"}}); r.Code() != "run.variable" {
		t.Errorf("unknown variable: %d %s", r.Status, r.Body)
	}
	if r := trigger(created.Token, map[string]any{"pipeline": "ci"}); r.Status != http.StatusUnprocessableEntity && r.Status != http.StatusBadRequest {
		t.Errorf("pipeline without api: %d %s", r.Status, r.Body)
	}
	// Tokens that are not a trigger's.
	for _, token := range []string{"kgt_" + strings.Repeat("a", 52), "kgp_" + strings.Repeat("a", 52), ""} {
		if r := trigger(token, map[string]any{}); r.Status != http.StatusUnauthorized {
			t.Errorf("token %q: %d %s", token, r.Status, r.Body)
		}
	}

	// Access tokens start API runs, too: the pipeline without api cannot be started that way.
	var pat struct {
		Token string `json:"token"`
	}
	lead.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read", "write"}}, &pat)
	viaToken := env.WithToken(pat.Token)
	if r := viaToken.Do(http.MethodPost, "/api/v1/code/repos/TRG/lib/runs", map[string]any{"ref": "main", "pipeline": "ci"}); r.Code() != "run.no-pipelines" {
		t.Errorf("api run of a pipeline without api: %d %s", r.Status, r.Body)
	}
	var started []struct {
		Number int `json:"number"`
	}
	viaToken.MustDo(http.MethodPost, "/api/v1/code/repos/TRG/lib/runs", map[string]any{"ref": "main", "pipeline": "release"}, &started)
	if len(started) != 1 {
		t.Fatalf("started = %v", started)
	}
	var page struct {
		Items []struct {
			Event string `json:"event"`
		} `json:"items"`
	}
	admin.Get("/api/v1/code/repos/TRG/lib/runs?pipeline=release", &page)
	if len(page.Items) != 2 || page.Items[0].Event != "api" {
		t.Errorf("release runs = %+v", page.Items)
	}

	// The trigger acts for its creator only while the creator may write to the repository.
	grant("repo.read")
	if r := trigger(created.Token, map[string]any{}); r.Status != http.StatusForbidden || r.Code() != "trigger.creator" {
		t.Errorf("creator without write access: %d %s", r.Status, r.Body)
	}
	grant("repo.admin")

	// Revoked triggers start nothing; creating and revoking is audited.
	lead.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/code/repos/TRG/lib/triggers/%d", created.ID), nil, nil)
	if r := trigger(created.Token, map[string]any{}); r.Status != http.StatusUnauthorized {
		t.Errorf("revoked trigger: %d %s", r.Status, r.Body)
	}
	var events []struct {
		Action string `json:"action"`
	}
	admin.Get("/api/v1/admin/audit?action=pipeline-trigger.create", &events)
	if len(events) != 1 {
		t.Errorf("create events = %+v", events)
	}
	admin.Get("/api/v1/admin/audit?action=pipeline-trigger.delete", &events)
	if len(events) != 1 {
		t.Errorf("delete events = %+v", events)
	}
	// Writers who do not administer the repository cannot see or create triggers.
	grant("repo.write")
	if r := lead.Do(http.MethodGet, "/api/v1/code/repos/TRG/lib/triggers", nil); r.Status != http.StatusForbidden {
		t.Errorf("writer lists triggers: %d", r.Status)
	}
}
