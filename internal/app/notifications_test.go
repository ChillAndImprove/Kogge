// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/smtptest"
)

// TestNotificationsFollowAccess: notifications are written for who may see their subject at the time; when that
// access ends, the inbox stops showing what they said and pending e-mails are not sent (audit L37).
func TestNotificationsFollowAccess(t *testing.T) {
	smtp := smtptest.New(t)
	env := apitest.New(t, func(c *config.Config) {
		c.SMTP = config.SMTP{Host: smtp.Host, Port: smtp.Port, From: "Kogge <kogge@example.com>", TLS: "none"}
	})
	ctx := context.Background()
	admin := env.Admin()
	lena, dirk := env.CreateUser("lena"), env.CreateUser("dirk")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"lena": lena, "dirk": dirk} {
		var m me
		c.Get("/api/v1/me", &m)
		ids[name] = m.User.ID
	}
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET email_verified = true"); err != nil {
		t.Fatal(err)
	}
	admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": "project.create", "principalType": "user",
		"principalId": ids["lena"]}, nil)
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
	lena.Post("/api/v1/projects", map[string]any{"key": "NOT", "name": "Notes"}, nil)
	usersRole := apitest.Fmt("/api/v1/projects/NOT/roles/%d/members", cfg.Roles[2].ID)
	lena.Post(usersRole, map[string]any{"type": "user", "id": ids["dirk"]}, nil)
	lena.Post("/api/v1/issues", map[string]any{"project": "NOT", "typeId": cfg.IssueTypes[0].ID, "summary": "Reorganisation plan"}, nil)
	// Dirk gets his issue notifications as an hourly digest.
	dirk.MustDo(http.MethodPut, "/api/v1/me/notification-preferences", []map[string]string{{"category": "issues", "mode": "hourly"}}, nil)
	lena.Post("/api/v1/issues/NOT-1/comments", map[string]any{"body": map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "Confidential details for "},
			map[string]any{"type": "mention", "attrs": map[string]any{"user": ids["dirk"], "label": "dirk"}}}}}}}, nil)
	env.WorkJobs()

	inbox := func() []map[string]any {
		var list []map[string]any
		dirk.Get("/api/v1/notifications", &list)
		return list
	}
	if list := inbox(); len(list) != 1 || !strings.Contains(list[0]["title"].(string), "Reorganisation") {
		t.Fatalf("dirk's inbox: %v", list)
	}

	// Dirk leaves the project: the notification stays, but says nothing any more, and no e-mail goes out.
	lena.MustDo(http.MethodDelete, usersRole+apitest.Fmt("?type=user&id=%d", ids["dirk"]), nil, nil)
	list := inbox()
	if len(list) != 1 || list[0]["hidden"] != true || list[0]["title"] != "" || list[0]["url"] != "" ||
		strings.Contains(apitest.Fmt("%v", list), "Reorganisation") {
		t.Errorf("dirk's inbox after leaving: %v", list)
	}
	if _, err := env.App.Jobs.Insert(ctx, notify.DigestArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	env.WorkJobs()
	if msgs := smtp.Wait(1, 2*time.Second); len(msgs) != 0 {
		t.Errorf("a digest was mailed about what dirk no longer sees: %s", msgs[0].Data)
	}
	var state string
	if err := env.App.Pool.QueryRow(ctx, "SELECT mail_state FROM notifications WHERE user_id = $1", ids["dirk"]).Scan(&state); err != nil ||
		state != "skipped" {
		t.Errorf("mail state: %q %v", state, err)
	}
}
