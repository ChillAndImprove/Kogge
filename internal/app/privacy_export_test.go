// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestExportSaysWhatItCut: sections of an export that hold only their newest entries say so, with the number of
// all entries, so that an export never looks complete when it is not.
func TestExportSaysWhatItCut(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	env.CreateUser("vera")
	var vera struct {
		ID int64 `json:"id"`
	}
	var users struct {
		Items []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	admin.Get("/api/v1/admin/users?query=vera", &users)
	for _, u := range users.Items {
		if u.Username == "vera" {
			vera.ID = u.ID
		}
	}
	if _, err := env.App.Pool.Exec(context.Background(), `INSERT INTO notifications (user_id, category, event, entity_type, entity_id, title, url)
		SELECT $1, 'issue', 'issue.updated', 'issue', 'X-' || g, 'Change ' || g, '/browse/X-' || g FROM generate_series(1, 10003) AS g`,
		vera.ID); err != nil {
		t.Fatal(err)
	}
	var export struct {
		Sections  map[string][]map[string]any `json:"sections"`
		Truncated map[string]int              `json:"truncated"`
	}
	admin.MustDo(http.MethodGet, apitest.Fmt("/api/v1/admin/users/%d/export", vera.ID), nil, nil).Decode(&export)
	if n := len(export.Sections["notifications"]); n != 10000 {
		t.Fatalf("notifications in the export: %d", n)
	}
	if export.Sections["notifications"][0]["title"] != "Change 10003" {
		t.Errorf("not the newest first: %v", export.Sections["notifications"][0])
	}
	if export.Truncated["notifications"] != 10003 || len(export.Truncated) != 1 {
		t.Fatalf("truncated: %v", export.Truncated)
	}
}
