// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/testutil/apitest"
)

// liveStream reads the live updates of a signed-in client until the test ends.
func liveStream(t *testing.T, env *apitest.Env, c *apitest.Client) <-chan map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.URL+"/api/v1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Send(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live: %d", res.StatusCode)
	}
	out := make(chan map[string]any, 100)
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				if json.Unmarshal([]byte(data), &m) == nil {
					out <- m
				}
			}
		}
	}()
	return out
}

// waitFor returns the ids of the messages of a type that arrive until want arrived or the time is up.
func waitFor(stream <-chan map[string]any, typ, want string, d time.Duration) []string {
	var ids []string
	deadline := time.After(d)
	for {
		select {
		case m := <-stream:
			if m["type"] == typ {
				id, _ := m["id"].(string)
				ids = append(ids, id)
				if id == want {
					return ids
				}
			}
		case <-deadline:
			return ids
		}
	}
}

// TestLiveUpdatesRespectVisibility: live updates reach only who may see what changed. A commit that names a
// secured issue updates the browsers of its viewers, not those of everyone in the project (audit L11).
func TestLiveUpdatesRespectVisibility(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	lena, dirk := env.CreateUser("lena"), env.CreateUser("dirk")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"lena": lena, "dirk": dirk} {
		var m me
		c.Get("/api/v1/me", &m)
		ids[name] = m.User.ID
	}
	for _, perm := range []string{"project.create", "codeproject.create", "space.create"} {
		admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": perm, "principalType": "user",
			"principalId": ids["lena"]}, nil)
	}
	var cfg struct {
		IssueTypes []struct {
			ID   int64  `json:"id"`
			Icon string `json:"icon"`
		} `json:"issueTypes"`
		Roles []struct {
			ID int64 `json:"id"`
		} `json:"roles"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	var task int64
	for _, it := range cfg.IssueTypes {
		if it.Icon == "task" {
			task = it.ID
		}
	}
	lena.Post("/api/v1/projects", map[string]any{"key": "LIV", "name": "Live"}, nil)
	lena.Post("/api/v1/projects/LIV/roles/"+apitest.Fmt("%d", cfg.Roles[1].ID)+"/members", map[string]any{"type": "user",
		"id": ids["dirk"]}, nil)
	var scheme struct {
		ID int64 `json:"id"`
	}
	admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Staff only", "levels": []map[string]any{
		{"name": "Staff", "members": []map[string]any{{"holderType": "user", "holderId": ids["lena"]}}}}}, &scheme)
	admin.MustDo(http.MethodPatch, "/api/v1/projects/LIV", map[string]any{"securitySchemeId": scheme.ID}, nil)
	var schemes []struct {
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	lena.Post("/api/v1/issues", map[string]any{"project": "LIV", "typeId": task, "summary": "Restructuring",
		"fields": map[string]any{"security": schemes[0].Levels[0].ID}}, nil)
	lena.Post("/api/v1/issues", map[string]any{"project": "LIV", "typeId": task, "summary": "Docs"}, nil)
	lena.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LIVC", "name": "Live code"}, nil)
	lena.MustDo(http.MethodPost, "/api/v1/code/projects/LIVC/repos", map[string]any{"name": "app", "readme": true}, nil)
	lena.MustDo(http.MethodPut, "/api/v1/code/projects/LIVC/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": ids["lena"]}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": ids["dirk"]}}}, nil)

	// An open space: Dirk reads it, except the page Lena keeps to herself.
	var sp struct {
		HomePageID int64 `json:"homePageId"`
	}
	lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "LIVW", "name": "Live wiki"}, &sp)
	page := func(title string) string {
		var pg struct {
			ID int64 `json:"id"`
		}
		lena.Post("/api/v1/wiki/spaces/LIVW/pages", map[string]any{"title": title, "parentId": sp.HomePageID}, &pg)
		lena.Post(apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), map[string]any{"title": title, "baseVersion": 0,
			"body": map[string]any{"type": "doc", "content": []any{}}}, nil)
		return apitest.Fmt("%d", pg.ID)
	}
	private, public := page("Private"), page("Public")
	lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+private+"/restrictions", map[string]any{"edit": []any{},
		"view": []map[string]any{{"type": "user", "id": ids["lena"]}}}, nil)

	lenaLive, dirkLive := liveStream(t, env, lena), liveStream(t, env, dirk)
	commit := func(message, path string) {
		lena.MustDo(http.MethodPut, "/api/v1/code/repos/LIVC/app/files", map[string]any{"branch": "main", "path": path,
			"content": message, "message": message}, nil)
		env.WorkJobs()
	}
	commit("LIV-1 Plan the restructuring", "plan.txt")
	commit("LIV-2 Write the docs", "docs.txt")
	if got := waitFor(lenaLive, "issue", "LIV-2", 10*time.Second); !contains(got, "LIV-1") || !contains(got, "LIV-2") {
		t.Errorf("lena's updates: %v", got)
	}
	got := waitFor(dirkLive, "issue", "LIV-2", 10*time.Second)
	if !contains(got, "LIV-2") {
		t.Errorf("dirk does not get updates: %v", got)
	}
	if contains(got, "LIV-1") {
		t.Errorf("dirk heard of the secured issue: %v", got)
	}

	// Pages: changes of a restricted page reach only who may read it.
	for _, id := range []string{private, public} {
		lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id+"/like", nil, nil)
		lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id+"/labels", []string{"live"}, nil)
	}
	env.WorkJobs()
	if got := waitFor(lenaLive, "page", public, 10*time.Second); !contains(got, private) {
		t.Errorf("lena's page updates: %v", got)
	}
	got = waitFor(dirkLive, "page", public, 10*time.Second)
	if !contains(got, public) || contains(got, private) {
		t.Errorf("dirk's page updates: %v", got)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
