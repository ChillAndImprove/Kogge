// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestNotifyUserField: a notification scheme notifies the people named in a user or multi-user field of the issue,
// and, as for every recipient, only those who can see the issue.
func TestNotifyUserField(t *testing.T) {
	f := setup(t)
	field := func(name, typ string) int64 {
		t.Helper()
		var saved struct {
			ID int64 `json:"id"`
		}
		f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": name, "type": typ}, &saved)
		return saved.ID
	}
	reviewer, testers := field("Reviewer", "user"), field("Testers", "multiuser")
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/notification-schemes", map[string]any{"name": "By field", "entries": []map[string]any{
		{"event": "created", "recipientType": "user-field", "recipientId": reviewer},
		{"event": "commented", "recipientType": "user-field", "recipientId": testers},
	}}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"notificationSchemeId": scheme.ID}, nil)

	notified := func(c *apitest.Client, key, event string) bool {
		t.Helper()
		var inbox []struct {
			Event    string `json:"event"`
			EntityID string `json:"entityId"`
		}
		c.Get("/api/v1/notifications", &inbox)
		for _, n := range inbox {
			if n.Event == "issue."+event && n.EntityID == key {
				return true
			}
		}
		return false
	}

	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Vouchers",
		"fields": map[string]any{"cf_" + fmtInt(reviewer): f.ids["ute"], "cf_" + fmtInt(testers): []int64{f.ids["lena"], f.ids["ute"]}}})
	if i.Key == "" {
		t.Fatal("issue not created")
	}
	if !notified(f.user, i.Key, "created") {
		t.Error("the reviewer was not notified of the new issue")
	}
	if notified(f.lead, i.Key, "created") {
		t.Error("a tester was notified of the new issue, which is the reviewer's event")
	}
	f.dev.Post("/api/v1/issues/"+i.Key+"/comments", map[string]any{"markdown": "Ready for testing."}, nil)
	if !notified(f.lead, i.Key, "commented") || !notified(f.user, i.Key, "commented") {
		t.Error("the testers were not both notified of the comment")
	}

	// Someone named in the field who may not see the project is left out.
	hidden := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Coupons",
		"fields": map[string]any{"cf_" + fmtInt(reviewer): f.ids["otto"]}})
	if notified(f.other, hidden.Key, "created") {
		t.Error("someone who cannot see the issue was notified")
	}
}
