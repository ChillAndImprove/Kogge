// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestNavigatorColumns: search results carry what the navigator's columns show — components, versions, the open
// sprint and custom field values as text (names of options, people, groups and versions; the beginning of rich
// text) — and filters keep their columns.
func TestNavigatorColumns(t *testing.T) {
	f := setup(t)
	field := func(name, typ string, options ...string) string {
		t.Helper()
		var opts []map[string]any
		for _, o := range options {
			opts = append(opts, map[string]any{"id": 0, "value": o})
		}
		var saved struct {
			ID int64 `json:"id"`
		}
		f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": name, "type": typ, "options": opts}, &saved)
		return "cf_" + itoa(saved.ID)
	}
	tier, reviewer, team, notes, points := field("Tier", "select", "Gold", "Silver"), field("Reviewer", "user"),
		field("Team", "group"), field("Notes", "textarea"), field("Points", "number")
	var cfg struct {
		Fields []struct {
			Key     string `json:"key"`
			Options []ref  `json:"options"`
		} `json:"fields"`
	}
	f.admin.Get("/api/v1/issue-config", &cfg)
	var gold int64
	for _, fl := range cfg.Fields {
		if fl.Key == tier {
			gold = fl.Options[0].ID
		}
	}
	var group ref
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "payments"}, &group)
	var comp, v1, v2 ref
	f.lead.Post("/api/v1/projects/SHOP/components", map[string]any{"name": "Checkout"}, &comp)
	f.lead.Post("/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &v1)
	f.lead.Post("/api/v1/projects/SHOP/versions", map[string]any{"name": "2.0"}, &v2)
	doc := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": "Ask finance before the release."}}}}}
	is := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("bug"), "summary": "Refunds",
		"fields": map[string]any{"components": []int64{comp.ID}, "fixVersions": []int64{v2.ID},
			tier: gold, reviewer: f.ids["dirk"], team: group.ID, notes: doc, points: 5}})
	// Affected versions are set after the creation (by the default field layout).
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+is.Key, map[string]any{"fields": map[string]any{
		"affectsVersions": []int64{v1.ID}}}, nil)
	boards := items[ref](t, f.lead, "/api/v1/boards?project=SHOP")
	var sprint ref
	f.lead.Post("/api/v1/boards/"+itoa(boards[0].ID)+"/sprints", map[string]any{"name": "Launch"}, &sprint)
	f.lead.Post("/api/v1/sprints/"+itoa(sprint.ID)+"/issues", map[string]any{"issues": []string{is.Key}}, nil)

	var res struct {
		Issues []struct {
			Key             string            `json:"key"`
			Components      []ref             `json:"components"`
			FixVersions     []ref             `json:"fixVersions"`
			AffectsVersions []ref             `json:"affectsVersions"`
			Sprint          *ref              `json:"sprint"`
			Display         map[string]string `json:"display"`
		} `json:"issues"`
	}
	f.dev.Get("/api/v1/search?q="+url.QueryEscape("project = SHOP"), &res)
	if len(res.Issues) != 1 {
		t.Fatalf("results: %+v", res)
	}
	r := res.Issues[0]
	if len(r.Components) != 1 || r.Components[0].Name != "Checkout" || len(r.FixVersions) != 1 || r.FixVersions[0].Name != "2.0" ||
		len(r.AffectsVersions) != 1 || r.AffectsVersions[0].Name != "1.0" || r.Sprint == nil || r.Sprint.Name != "Launch" {
		t.Errorf("components, versions and sprint: %+v", r)
	}
	for key, want := range map[string]string{tier: "Gold", reviewer: "Dirk", team: "payments", notes: "Ask finance before the release.",
		points: "5"} {
		if got := r.Display[key]; got != want {
			t.Errorf("display of %s: %q, want %q (%v)", key, got, want, r.Display)
		}
	}

	// Filters keep their columns; keys of other forms are refused.
	var saved struct {
		ID      int64    `json:"id"`
		Columns []string `json:"columns"`
	}
	cols := []string{"key", "summary", tier, "sprint"}
	f.lead.Post("/api/v1/filters", map[string]any{"name": "With columns", "query": "project = SHOP", "columns": cols}, &saved)
	if strings.Join(saved.Columns, ",") != strings.Join(cols, ",") {
		t.Errorf("columns of the filter: %v", saved.Columns)
	}
	if r := f.lead.Do(http.MethodPost, "/api/v1/filters", map[string]any{"name": "Bad", "query": "project = SHOP",
		"columns": []string{"summary; DROP"}}); r.Code() != "filter.column" {
		t.Errorf("bad column: %d %s", r.Status, r.Body)
	}
}
