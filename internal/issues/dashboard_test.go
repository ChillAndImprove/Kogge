// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"net/url"
	"testing"
)

func TestDashboardsAndStatistics(t *testing.T) {
	f := setup(t)
	for _, s := range []string{"One", "Two", "Three"} {
		f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": s})
	}
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"fields": map[string]any{"assignee": f.ids["dirk"]}}, nil)
	f.transition(t, "SHOP-2", "done")

	var dist struct {
		Total int `json:"total"`
		Items []struct {
			Label string `json:"label"`
			Count int    `json:"count"`
			Query string `json:"query"`
		} `json:"items"`
	}
	f.dev.Get("/api/v1/reports/distribution?field=statusCategory&q="+url.QueryEscape("project = SHOP"), &dist)
	counts := map[string]int{}
	for _, it := range dist.Items {
		counts[it.Query] = it.Count
	}
	if dist.Total != 3 || counts[`statusCategory = "To Do"`] != 2 || counts[`statusCategory = "Done"`] != 1 {
		t.Fatalf("distribution: %+v", dist)
	}
	// Every item's query selects its issues.
	for _, it := range dist.Items {
		if got := f.search(t, "project = SHOP AND "+it.Query); len(got) == 0 {
			t.Fatalf("query %q of %s finds nothing", it.Query, it.Label)
		}
	}
	f.dev.Get("/api/v1/reports/distribution?field=assignee&q="+url.QueryEscape("project = SHOP"), &dist)
	if len(dist.Items) != 2 {
		t.Fatalf("by assignee: %+v", dist)
	}
	if r := f.dev.Do(http.MethodGet, "/api/v1/reports/distribution?field=summary&q=", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported field: %d", r.Status)
	}
	var m struct {
		X      []struct{ Label string } `json:"x"`
		Y      []struct{ Label string } `json:"y"`
		Counts [][]int                  `json:"counts"`
		Total  int                      `json:"total"`
	}
	f.dev.Get("/api/v1/reports/matrix?x=statusCategory&y=assignee&q="+url.QueryEscape("project = SHOP"), &m)
	sum := 0
	for _, row := range m.Counts {
		for _, n := range row {
			sum += n
		}
	}
	if m.Total != 3 || sum != 3 || len(m.X) != 2 || len(m.Y) != 2 {
		t.Fatalf("matrix: %+v", m)
	}
	// Outsiders count nothing (and may not even name the project).
	if r := f.other.Do(http.MethodGet, "/api/v1/reports/distribution?field=status&q="+url.QueryEscape("project = SHOP"), nil); r.Status != http.StatusBadRequest {
		t.Fatalf("outsider names the project: %d", r.Status)
	}
	f.other.Get("/api/v1/reports/distribution?field=status&q="+url.QueryEscape("summary ~ one"), &dist)
	if dist.Total != 0 {
		t.Fatalf("outsider statistics: %+v", dist)
	}

	var activity []struct {
		Verb  string `json:"verb"`
		Title string `json:"title"`
	}
	f.dev.Get("/api/v1/activity?project=SHOP", &activity)
	if len(activity) < 3 {
		t.Fatalf("activity: %+v", activity)
	}
	f.other.Get("/api/v1/activity", &activity)
	if len(activity) != 0 {
		t.Fatalf("outsider activity: %+v", activity)
	}

	// Dashboards: private until shared, editable by the owner only.
	var d struct {
		ID        int64 `json:"id"`
		Favourite bool  `json:"favourite"`
		CanEdit   bool  `json:"canEdit"`
		Widgets   []struct {
			ID string `json:"id"`
		} `json:"widgets"`
	}
	widgets := []map[string]any{
		{"id": "w1", "type": "distribution", "column": 0, "position": 0, "config": map[string]any{"query": "project = SHOP", "field": "status"}},
		{"id": "w2", "type": "assigned-to-me", "column": 1, "position": 0},
	}
	f.dev.Post("/api/v1/dashboards", map[string]any{"name": "Team", "columns": 2, "widgets": widgets}, &d)
	if !d.Favourite || !d.CanEdit || len(d.Widgets) != 2 {
		t.Fatalf("created dashboard: %+v", d)
	}
	id := fmtInt(d.ID)
	if r := f.user.Do(http.MethodGet, "/api/v1/dashboards/"+id, nil); r.Status != http.StatusNotFound {
		t.Fatalf("private dashboard visible: %d", r.Status)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/dashboards", map[string]any{"name": "Bad", "columns": 1,
		"widgets": []map[string]any{{"id": "w1", "type": "distribution", "column": 2, "position": 0}}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("widget outside the columns: %d", r.Status)
	}
	var projects []struct {
		ID int64 `json:"id"`
	}
	f.dev.Get("/api/v1/projects", &projects)
	f.dev.MustDo(http.MethodPut, "/api/v1/dashboards/"+id, map[string]any{"name": "Team", "columns": 2, "widgets": widgets,
		"shares": []map[string]any{{"type": "project", "projectId": projects[0].ID}}}, nil)
	f.user.Get("/api/v1/dashboards/"+id, &d)
	if d.CanEdit || d.Favourite {
		t.Fatalf("shared dashboard for a viewer: %+v", d)
	}
	if r := f.user.Do(http.MethodPut, "/api/v1/dashboards/"+id, map[string]any{"name": "Mine"}); r.Status != http.StatusForbidden {
		t.Fatalf("viewer changed the dashboard: %d", r.Status)
	}
	f.user.MustDo(http.MethodPut, "/api/v1/dashboards/"+id+"/favourite", nil, nil)
	list := items[struct {
		ID        int64 `json:"id"`
		Favourite bool  `json:"favourite"`
	}](t, f.user, "/api/v1/dashboards")
	if len(list) != 1 || !list[0].Favourite {
		t.Fatalf("viewer's dashboards: %+v", list)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/dashboards/"+id, nil); r.Status != http.StatusNotFound {
		t.Fatalf("outsider sees the dashboard: %d", r.Status)
	}
	f.dev.MustDo(http.MethodDelete, "/api/v1/dashboards/"+id, nil, nil)

	// Sprint health of the Scrum board.
	boards := items[board](t, f.dev, "/api/v1/boards?project=SHOP")
	var sp sprint
	f.dev.Post("/api/v1/boards/"+fmtInt(boards[0].ID)+"/sprints", map[string]any{}, &sp)
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+fmtInt(sp.ID)+"/issues", map[string]any{"issues": []string{"SHOP-1", "SHOP-3"}}, nil)
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+fmtInt(sp.ID)+"/start", map[string]any{}, nil)
	var health struct {
		Sprints []struct {
			Todo int `json:"todo"`
			Done int `json:"done"`
		} `json:"sprints"`
	}
	f.dev.Get("/api/v1/boards/"+fmtInt(boards[0].ID)+"/sprint-health", &health)
	if len(health.Sprints) != 1 || health.Sprints[0].Todo != 2 {
		t.Fatalf("sprint health: %+v", health)
	}
}
