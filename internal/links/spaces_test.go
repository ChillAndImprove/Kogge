// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

func TestProjectSpace(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "scrum"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "DOCS", "name": "Docs", "open": false}, nil)
	type space struct {
		Space *struct {
			Key string `json:"key"`
		} `json:"space"`
	}
	var got space
	admin.Get("/api/v1/projects/SHOP/space", &got)
	if got.Space != nil {
		t.Fatalf("linked before linking: %+v", got)
	}
	admin.MustDo(http.MethodPut, "/api/v1/projects/SHOP/space", map[string]any{"space": "DOCS"}, &got)
	if got.Space == nil || got.Space.Key != "DOCS" {
		t.Fatalf("linked = %+v", got)
	}
	if res := admin.Do(http.MethodPut, "/api/v1/projects/SHOP/space", map[string]any{"space": "NOPE"}); res.Code() != "project.space" {
		t.Errorf("unknown space: %d %s", res.Status, res.Body)
	}
	// Someone who can read the project but not the (closed) space does not see the link.
	dev := env.CreateUser("dev")
	if res := dev.Do(http.MethodGet, "/api/v1/projects/SHOP/space", nil); res.Status == http.StatusOK {
		var s space
		res.Decode(&s)
		if s.Space != nil {
			t.Errorf("a user who cannot see the space sees the link: %+v", s)
		}
	}
	if res := dev.Do(http.MethodPut, "/api/v1/projects/SHOP/space", map[string]any{"space": ""}); res.Status < 400 {
		t.Errorf("a user unlinked the space: %d", res.Status)
	}
	var unlinked space
	admin.MustDo(http.MethodPut, "/api/v1/projects/SHOP/space", map[string]any{"space": ""}, &unlinked)
	if unlinked.Space != nil {
		t.Errorf("after unlinking: %+v", unlinked)
	}
}
