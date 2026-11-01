// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestDataSubjectRights: administrators export what Kogge holds about a person and anonymise a person.
// The person's content stays, attributed to a pseudonym; their sign-ins, credentials, grants and inbox
// go; pages restricted to them stay restricted.
func TestDataSubjectRights(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	jana, otto := env.CreateUser("jana"), env.CreateUser("otto")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"jana": jana, "otto": otto} {
		var m me
		c.Get("/api/v1/me", &m)
		ids[name] = m.User.ID
	}
	for _, perm := range []string{"project.create", "space.create"} {
		admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": perm, "principalType": "user",
			"principalId": ids["jana"]}, nil)
	}
	var cfg struct {
		IssueTypes []struct {
			ID int64 `json:"id"`
		} `json:"issueTypes"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	jana.Post("/api/v1/projects", map[string]any{"key": "GDPR", "name": "Privacy"}, nil)
	jana.Post("/api/v1/issues", map[string]any{"project": "GDPR", "typeId": cfg.IssueTypes[0].ID, "summary": "Jana's plan"}, nil)
	jana.Post("/api/v1/issues/GDPR-1/comments", map[string]any{"markdown": "Written by Jana herself"}, nil)
	jana.Post("/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, nil)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	jana.Post("/api/v1/me/ssh-keys", map[string]any{"name": "laptop", "key": string(ssh.MarshalAuthorizedKey(key))}, nil)
	jana.Post("/api/v1/wiki/spaces/personal", nil, nil)
	// Otto may see the space, not Jana's restricted page.
	var sp struct {
		HomePageID int64 `json:"homePageId"`
	}
	jana.Post("/api/v1/wiki/spaces", map[string]any{"key": "TEAM", "name": "Team"}, &sp)
	var pg struct {
		ID int64 `json:"id"`
	}
	jana.Post("/api/v1/wiki/spaces/TEAM/pages", map[string]any{"title": "Only Jana", "parentId": sp.HomePageID}, &pg)
	jana.Post(apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), map[string]any{"title": "Only Jana", "baseVersion": 0,
		"body": map[string]any{"type": "doc", "content": []any{}}}, nil)
	jana.MustDo(http.MethodPut, apitest.Fmt("/api/v1/wiki/pages/%d/restrictions", pg.ID), map[string]any{"edit": []any{},
		"view": []map[string]any{{"type": "user", "id": ids["jana"]}}}, nil)
	// What she opened last (the quick search's recent visits).
	jana.MustDo(http.MethodPost, "/api/v1/me/recent", map[string]any{"kind": "page", "id": pg.ID}, nil)

	// A filter she subscribed to (this installation sends no e-mail: the subscription is written directly).
	var filter struct {
		ID int64 `json:"id"`
	}
	jana.Post("/api/v1/filters", map[string]any{"name": "My work", "query": "project = GDPR"}, &filter)
	if _, err := env.App.Pool.Exec(context.Background(), `INSERT INTO filter_subscriptions (filter_id, user_id, created_by, frequency,
		minute, weekdays, time_zone, next_run_at) VALUES ($1, $2, $2, 'daily', 480, '{1,2,3,4,5}', 'Europe/Berlin', now() + interval '1 day')`,
		filter.ID, ids["jana"]); err != nil {
		t.Fatal(err)
	}

	// The export has her data — and nothing of Otto's.
	r := admin.MustDo(http.MethodGet, apitest.Fmt("/api/v1/admin/users/%d/export", ids["jana"]), nil, nil)
	var export struct {
		Sections map[string][]map[string]any `json:"sections"`
	}
	r.Decode(&export)
	if !strings.Contains(r.Header.Get("Content-Disposition"), "attachment") || export.Sections["profile"][0]["username"] != "jana" ||
		len(export.Sections["issuesReported"]) != 1 || export.Sections["issueComments"][0]["text"] != "Written by Jana herself" ||
		len(export.Sections["accessTokens"]) != 1 || len(export.Sections["sshKeys"]) != 1 || len(export.Sections["sessions"]) == 0 ||
		len(export.Sections["auditEvents"]) == 0 || len(export.Sections["globalPermissions"]) != 2 ||
		len(export.Sections["filterSubscriptions"]) != 1 || export.Sections["filterSubscriptions"][0]["filter"] != "My work" ||
		len(export.Sections["recentVisits"]) != 1 || export.Sections["recentVisits"][0]["kind"] != "page" {
		t.Fatalf("export: %s", r.Body)
	}
	if strings.Contains(string(r.Body), "otto") {
		t.Errorf("the export names another person: %s", r.Body)
	}

	// Anonymising needs the user name as confirmation, and cannot hit oneself.
	path := apitest.Fmt("/api/v1/admin/users/%d/anonymise", ids["jana"])
	if r := admin.Do(http.MethodPost, path, map[string]any{"confirm": "otto"}); r.Code() != "user.anonymise-confirm" {
		t.Fatalf("anonymised without confirmation: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPost, path, map[string]any{"confirm": "jana"}, nil)

	// She is gone: no session, no sign-in, no user name; the user name is free again.
	if r := jana.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("her session survived: %d", r.Status)
	}
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "jana",
		"password": apitest.UserPassword}); r.Status != http.StatusUnauthorized {
		t.Errorf("she signed in: %d", r.Status)
	}
	var user struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Status      string `json:"status"`
	}
	admin.Get(apitest.Fmt("/api/v1/admin/users/%d", ids["jana"]), &user)
	if user.DisplayName != apitest.Fmt("Former user %d", ids["jana"]) || user.Email != "" || user.Status != "anonymised" ||
		strings.Contains(user.Username, "jana") {
		t.Errorf("after anonymising: %+v", user)
	}
	env.CreateUser("jana") // a new Jana

	// Her content stays, under the pseudonym; her restricted page stays hidden from Otto.
	var issue struct {
		Reporter struct {
			DisplayName string `json:"displayName"`
		} `json:"reporter"`
	}
	admin.Post("/api/v1/projects/GDPR/roles/1/members", map[string]any{"type": "user", "id": ids["otto"]}, nil)
	otto.Get("/api/v1/issues/GDPR-1", &issue)
	if issue.Reporter.DisplayName != user.DisplayName {
		t.Errorf("the issue's reporter: %+v", issue.Reporter)
	}
	if r := otto.Do(http.MethodGet, apitest.Fmt("/api/v1/wiki/pages/%d", pg.ID), nil); r.Status != http.StatusNotFound {
		t.Errorf("the restricted page opened up: %d", r.Status)
	}
	// Her personal space keeps its content, without her name.
	var spaces struct {
		Items []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"items"`
	}
	admin.Get("/api/v1/wiki/spaces?type=personal&all=true", &spaces)
	kept := false
	for _, s := range spaces.Items {
		if strings.Contains(strings.ToLower(s.Key+s.Name), "jana") {
			t.Errorf("a personal space names her: %+v", s)
		}
		kept = kept || s.Name == user.DisplayName
	}
	if !kept {
		t.Errorf("her personal space is gone: %+v", spaces)
	}
	r = admin.MustDo(http.MethodGet, apitest.Fmt("/api/v1/admin/users/%d/export", ids["jana"]), nil, nil)
	r.Decode(&export)
	for _, section := range []string{"sessions", "accessTokens", "sshKeys", "globalPermissions", "groups", "filterSubscriptions",
		"recentVisits"} {
		if len(export.Sections[section]) != 0 {
			t.Errorf("%s after anonymising: %v", section, export.Sections[section])
		}
	}
	if r := admin.Do(http.MethodPost, path, map[string]any{"confirm": user.Username}); r.Code() != "user.anonymised" {
		t.Errorf("anonymised twice: %d %s", r.Status, r.Body)
	}
}
