// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestGroupMemberPages: members come in pages with their total, and people of the same name are neither lost
// nor shown twice between pages.
func TestGroupMemberPages(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var group struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Everyone"}, &group)
	want := map[int64]bool{}
	for _, name := range []string{"anna1", "anna2", "anna3", "anna4", "anna5"} {
		var u struct {
			ID int64 `json:"id"`
		}
		// The same display name for all of them: only the tie-breaker orders them.
		admin.MustDo(http.MethodPost, "/api/v1/admin/users", map[string]any{"username": name, "displayName": "Anna Berg"}, &u)
		admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", group.ID, u.ID), nil, nil)
		want[u.ID] = true
	}
	seen := map[int64]bool{}
	for offset := 0; offset < 6; offset += 2 {
		var page struct {
			Total int `json:"total"`
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		admin.Get(apitest.Fmt("/api/v1/admin/groups/%d/members?limit=2&offset=%d", group.ID, offset), &page)
		if page.Total != 5 {
			t.Fatalf("total at %d: %d", offset, page.Total)
		}
		for _, m := range page.Items {
			if seen[m.ID] || !want[m.ID] {
				t.Fatalf("member %d at offset %d: seen before or not a member", m.ID, offset)
			}
			seen[m.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("members on the pages: %v", seen)
	}
}
