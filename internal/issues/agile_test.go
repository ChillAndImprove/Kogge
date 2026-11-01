// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type board struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config struct {
		Columns []struct {
			Name     string  `json:"name"`
			Statuses []int64 `json:"statuses"`
		} `json:"columns"`
		Estimation struct {
			Type  string `json:"type"`
			Field string `json:"field"`
		} `json:"estimation"`
		QuickFilters []struct {
			ID int `json:"id"`
		} `json:"quickFilters"`
	} `json:"config"`
	CanAdmin         bool `json:"canAdmin"`
	CanManageSprints bool `json:"canManageSprints"`
}

type card struct {
	Key      string   `json:"key"`
	Column   int      `json:"column"`
	Estimate *float64 `json:"estimate"`
	SprintID int64    `json:"sprintId"`
	Done     bool     `json:"done"`
	Epic     *struct {
		Key string `json:"key"`
	} `json:"epic"`
}

type sprint struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type backlog struct {
	Sprints []struct {
		Sprint sprint `json:"sprint"`
		Cards  []card `json:"cards"`
	} `json:"sprints"`
	Backlog []card `json:"backlog"`
	Epics   []struct {
		Key        string  `json:"key"`
		Issues     int     `json:"issues"`
		IssuesDone int     `json:"issuesDone"`
		Estimate   float64 `json:"estimate"`
	} `json:"epics"`
}

func cardKeys(cs []card) string {
	keys := make([]string, len(cs))
	for i, c := range cs {
		keys[i] = c.Key
	}
	return strings.Join(keys, ",")
}

func (f *fixture) transition(t *testing.T, key, category string) {
	t.Helper()
	var transitions []struct {
		ID int `json:"id"`
		To ref `json:"to"`
	}
	f.dev.Get("/api/v1/issues/"+key+"/transitions", &transitions)
	for _, tr := range transitions {
		if tr.To.Category == category {
			f.dev.Post("/api/v1/issues/"+key+"/transitions", map[string]any{"transitionId": tr.ID}, nil)
			return
		}
	}
	t.Fatalf("no transition of %s to %s: %+v", key, category, transitions)
}

func (f *fixture) search(t *testing.T, q string) string {
	t.Helper()
	var res struct {
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	f.dev.Get("/api/v1/search?q="+url.QueryEscape(q), &res)
	keys := make([]string, len(res.Issues))
	for i, is := range res.Issues {
		keys[i] = is.Key
	}
	return strings.Join(keys, ",")
}

func TestScrumBoardSprintsAndReports(t *testing.T) {
	f := setup(t)
	points := f.cfg.field("number")

	// The Scrum template comes with a board estimating in story points.
	boards := items[board](t, f.dev, "/api/v1/boards?project=SHOP")
	if len(boards) != 1 || boards[0].Type != "scrum" || boards[0].Config.Estimation.Field != points || len(boards[0].Config.Columns) != 4 {
		t.Fatalf("default board: %+v", boards)
	}
	b := boards[0]
	if b.CanAdmin || !b.CanManageSprints {
		t.Fatalf("developer permissions on the board: %+v", b)
	}
	bid := fmtInt(b.ID)

	epic := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("epic"), "summary": "Payments"})
	for i, sp := range []int{3, 5, 8} {
		f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Story " + fmtInt(int64(i+1)),
			"parent": epic.Key, "fields": map[string]any{points: sp}})
	}
	// SHOP-1 is the epic; SHOP-2..4 are the stories.
	var bl backlog
	f.dev.Get("/api/v1/boards/"+bid+"/backlog", &bl)
	if cardKeys(bl.Backlog) != "SHOP-2,SHOP-3,SHOP-4" || len(bl.Sprints) != 0 || len(bl.Epics) != 1 || bl.Epics[0].Issues != 3 ||
		bl.Epics[0].Estimate != 16 || bl.Backlog[0].Epic == nil || bl.Backlog[0].Epic.Key != "SHOP-1" {
		t.Fatalf("initial backlog: %+v", bl)
	}

	// Members of the Users role neither plan nor schedule.
	if r := f.user.Do(http.MethodPost, "/api/v1/boards/"+bid+"/sprints", map[string]any{}); r.Status != http.StatusForbidden {
		t.Fatalf("user created a sprint: %d", r.Status)
	}
	var sp sprint
	f.dev.Post("/api/v1/boards/"+bid+"/sprints", map[string]any{"goal": "Pay"}, &sp)
	if sp.Name != "SHOP Sprint 1" || sp.State != "future" {
		t.Fatalf("sprint: %+v", sp)
	}
	sid := fmtInt(sp.ID)
	if r := f.user.Do(http.MethodPost, "/api/v1/sprints/"+sid+"/issues", map[string]any{"issues": []string{"SHOP-2"}}); r.Status != http.StatusForbidden {
		t.Fatalf("user scheduled an issue: %d", r.Status)
	}
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+sid+"/issues", map[string]any{"issues": []string{"SHOP-2", "SHOP-3"}}, nil)
	f.dev.Get("/api/v1/boards/"+bid+"/backlog", &bl)
	if len(bl.Sprints) != 1 || cardKeys(bl.Sprints[0].Cards) != "SHOP-2,SHOP-3" || cardKeys(bl.Backlog) != "SHOP-4" {
		t.Fatalf("backlog after planning: %+v", bl)
	}

	// A sub-task follows its parent into the sprint.
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Sandbox account", "parent": "SHOP-2"})
	var full struct {
		Sprint *sprint `json:"sprint"`
	}
	f.dev.Get("/api/v1/issues/"+sub.Key, &full)
	if full.Sprint == nil || full.Sprint.ID != sp.ID {
		t.Fatalf("sub-task sprint: %+v", full.Sprint)
	}

	// Ranking moves SHOP-3 before SHOP-2.
	f.dev.MustDo(http.MethodPut, "/api/v1/issues/rank", map[string]any{"issues": []string{"SHOP-3"}, "before": "SHOP-2"}, nil)
	f.dev.Get("/api/v1/boards/"+bid+"/backlog", &bl)
	if cardKeys(bl.Sprints[0].Cards) != "SHOP-3,SHOP-2" {
		t.Fatalf("rank: %s", cardKeys(bl.Sprints[0].Cards))
	}

	// Start: the board shows the sprint's issues (and sub-tasks) in the first column.
	f.dev.Post("/api/v1/sprints/"+sid+"/start", map[string]any{}, &sp)
	if sp.State != "active" {
		t.Fatalf("started: %+v", sp)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/sprints/"+sid+"/start", map[string]any{}); r.Status != http.StatusConflict {
		t.Fatalf("second start: %d", r.Status)
	}
	var view struct {
		Sprints []sprint `json:"sprints"`
		Cards   []card   `json:"cards"`
	}
	f.dev.Get("/api/v1/boards/"+bid+"/view", &view)
	if len(view.Sprints) != 1 || cardKeys(view.Cards) != "SHOP-3,SHOP-2,"+sub.Key || view.Cards[0].Column != 0 {
		t.Fatalf("board view: %+v", view)
	}
	// Quick filter "only my issues" (nothing is assigned).
	f.dev.Get("/api/v1/boards/"+bid+"/view?quickFilter="+fmtInt(int64(b.Config.QuickFilters[0].ID)), &view)
	if len(view.Cards) != 0 {
		t.Fatalf("quick filter: %s", cardKeys(view.Cards))
	}

	// Scope change, re-estimate and progress.
	f.dev.MustDo(http.MethodPost, "/api/v1/sprints/"+sid+"/issues", map[string]any{"issues": []string{"SHOP-4"}}, nil)
	var cur issue
	f.dev.Get("/api/v1/issues/SHOP-2", &cur)
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/SHOP-2", map[string]any{"fields": map[string]any{points: 2}}, nil)
	f.transition(t, "SHOP-3", "done")

	var bd struct {
		Points []struct {
			Remaining float64 `json:"remaining"`
			Event     string  `json:"event"`
			Key       string  `json:"key"`
		} `json:"points"`
	}
	f.dev.Get("/api/v1/sprints/"+sid+"/burndown", &bd)
	var events []string
	for _, pt := range bd.Points {
		events = append(events, pt.Event+":"+pt.Key)
	}
	if strings.Join(events, " ") != "start: added:SHOP-4 estimate:SHOP-2 completed:SHOP-3 now:" || bd.Points[0].Remaining != 8 ||
		bd.Points[len(bd.Points)-1].Remaining != 10 {
		t.Fatalf("burndown: %+v", bd.Points)
	}

	// Complete into a new sprint: SHOP-3 is done, SHOP-2 and SHOP-4 carry over.
	var done struct {
		Completed int    `json:"completed"`
		Moved     int    `json:"moved"`
		MovedTo   sprint `json:"movedTo"`
	}
	f.dev.Post("/api/v1/sprints/"+sid+"/complete", map[string]any{"moveTo": "new"}, &done)
	if done.Completed != 1 || done.Moved != 3 || done.MovedTo.Name != "SHOP Sprint 2" {
		t.Fatalf("complete: %+v", done)
	}
	var moved struct {
		Sprint        *sprint  `json:"sprint"`
		ClosedSprints []sprint `json:"closedSprints"`
	}
	f.dev.Get("/api/v1/issues/SHOP-2", &moved)
	if moved.Sprint == nil || moved.Sprint.ID != done.MovedTo.ID || len(moved.ClosedSprints) != 1 || moved.ClosedSprints[0].ID != sp.ID {
		t.Fatalf("carried over: %+v", moved)
	}

	var rep struct {
		Committed float64 `json:"committed"`
		Completed []struct {
			Key string `json:"key"`
		} `json:"completed"`
		NotCompleted []struct {
			Key   string `json:"key"`
			Added bool   `json:"added"`
		} `json:"notCompleted"`
	}
	f.dev.Get("/api/v1/sprints/"+sid+"/report", &rep)
	if rep.Committed != 8 || len(rep.Completed) != 1 || rep.Completed[0].Key != "SHOP-3" || len(rep.NotCompleted) != 3 {
		t.Fatalf("sprint report: %+v", rep)
	}
	added := map[string]bool{}
	for _, ri := range rep.NotCompleted {
		added[ri.Key] = ri.Added
	}
	if !added["SHOP-4"] || added["SHOP-2"] {
		t.Fatalf("scope changes: %+v", rep.NotCompleted)
	}
	var vel struct {
		Sprints []struct {
			Committed float64 `json:"committed"`
			Completed float64 `json:"completed"`
		} `json:"sprints"`
	}
	f.dev.Get("/api/v1/boards/"+bid+"/velocity", &vel)
	if len(vel.Sprints) != 1 || vel.Sprints[0].Committed != 8 || vel.Sprints[0].Completed != 5 {
		t.Fatalf("velocity: %+v", vel)
	}

	// KQL knows sprints.
	if got := f.search(t, "sprint in openSprints() ORDER BY key"); got != "SHOP-2,SHOP-4,"+sub.Key {
		t.Fatalf("open sprints: %s", got)
	}
	if got := f.search(t, `sprint = "SHOP Sprint 1" ORDER BY key`); got != "SHOP-2,SHOP-3,SHOP-4,"+sub.Key {
		t.Fatalf("closed sprint by name: %s", got)
	}
	if got := f.search(t, "sprint is EMPTY ORDER BY key"); got != "SHOP-1" {
		t.Fatalf("no sprint: %s", got)
	}

	// Reports over the board's history.
	var cfd struct {
		Columns []string `json:"columns"`
		Days    []struct {
			Counts []int `json:"counts"`
		} `json:"days"`
	}
	f.dev.Get("/api/v1/boards/"+bid+"/cumulative-flow?days=3", &cfd)
	if len(cfd.Days) != 3 || len(cfd.Days[2].Counts) != 4 || cfd.Days[2].Counts[3] != 1 {
		t.Fatalf("cumulative flow: %+v", cfd)
	}
	var cvr struct {
		Periods []struct {
			Created  int `json:"created"`
			Resolved int `json:"resolved"`
		} `json:"periods"`
	}
	f.dev.Get("/api/v1/reports/created-vs-resolved?days=7&q="+url.QueryEscape("project = SHOP"), &cvr)
	if len(cvr.Periods) != 7 || cvr.Periods[6].Created != 5 || cvr.Periods[6].Resolved != 1 {
		t.Fatalf("created vs resolved: %+v", cvr)
	}

	// Moving to the backlog and deleting a future sprint.
	f.dev.MustDo(http.MethodPost, "/api/v1/backlog/issues", map[string]any{"issues": []string{"SHOP-4"}}, nil)
	f.dev.MustDo(http.MethodDelete, "/api/v1/sprints/"+fmtInt(done.MovedTo.ID), nil, nil)
	f.dev.Get("/api/v1/boards/"+bid+"/backlog", &bl)
	if len(bl.Sprints) != 0 || cardKeys(bl.Backlog) != "SHOP-2,SHOP-4" {
		t.Fatalf("backlog at the end: %+v", bl)
	}
}

func TestKanbanBoardAndBoardAdministration(t *testing.T) {
	f := setup(t)
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	boards := items[board](t, f.lead, "/api/v1/boards?project=OPS")
	if len(boards) != 1 || boards[0].Type != "kanban" || len(boards[0].Config.Columns) != 3 || !boards[0].CanAdmin {
		t.Fatalf("kanban board: %+v", boards)
	}
	b := boards[0]
	bid := fmtInt(b.ID)
	for _, s := range []string{"Disk full", "Renew certificate"} {
		f.create(t, f.lead, map[string]any{"project": "OPS", "typeId": f.cfg.typ("task"), "summary": s})
	}
	var view struct {
		Cards []card `json:"cards"`
	}
	f.lead.Get("/api/v1/boards/"+bid+"/view", &view)
	if cardKeys(view.Cards) != "OPS-1,OPS-2" {
		t.Fatalf("kanban cards: %+v", view)
	}
	if r := f.lead.Do(http.MethodPost, "/api/v1/boards/"+bid+"/sprints", map[string]any{}); r.Status < 400 {
		t.Fatalf("sprint on a kanban board: %d", r.Status)
	}

	// Columns can be merged; a status may map to one column only.
	var full map[string]any
	r := f.lead.MustDo(http.MethodGet, "/api/v1/boards/"+bid, nil, &full)
	cfg := full["config"].(map[string]any)
	cols := cfg["columns"].([]any)
	first, second := cols[0].(map[string]any), cols[1].(map[string]any)
	merged := map[string]any{"name": "Open", "statuses": append(first["statuses"].([]any), second["statuses"].([]any)...)}
	cfg["columns"] = []any{merged, cols[2]}
	if r2 := f.lead.Do(http.MethodPut, "/api/v1/boards/"+bid, map[string]any{"name": "Ops", "config": map[string]any{
		"columns": []any{merged, merged}, "swimlanes": map[string]any{"type": "none"}, "quickFilters": []any{},
		"card": map[string]any{"fields": []any{}, "colors": "none"}, "estimation": map[string]any{"type": "count"}}}); r2.Status != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate mapping: %d %s", r2.Status, r2.Body)
	}
	var updated board
	f.lead.MustDo(http.MethodPut, "/api/v1/boards/"+bid, map[string]any{"name": "Ops", "config": cfg}, &updated, "If-Match", r.Header.Get("ETag"))
	if updated.Name != "Ops" || len(updated.Config.Columns) != 2 {
		t.Fatalf("updated board: %+v", updated)
	}
	if r := f.lead.Do(http.MethodPut, "/api/v1/boards/"+bid, map[string]any{"name": "Stale"}, "If-Match", r.Header.Get("ETag")); r.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale board update: %d", r.Status)
	}
	// Outsiders do not see the board; the project admin may delete it.
	if r := f.other.Do(http.MethodGet, "/api/v1/boards/"+bid, nil); r.Status != http.StatusNotFound {
		t.Fatalf("outsider sees the board: %d", r.Status)
	}
	f.lead.MustDo(http.MethodDelete, "/api/v1/boards/"+bid, nil, nil)
}

// TestSprintField: the sprint field puts an issue into a sprint or back into the backlog, also when it is the only
// field that changes, with an entry in the history; taking an issue into a sprint of another project's board, or out
// of it, needs scheduling in that project too (audit L16).
func TestSprintField(t *testing.T) {
	f := setup(t)
	var boards struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	f.lead.Get("/api/v1/boards?project=SHOP", &boards)
	var sp sprint
	f.lead.Post("/api/v1/boards/"+itoa(boards.Items[0].ID)+"/sprints", map[string]any{"name": "Launch"}, &sp)
	sprintOf := func(key string) int64 {
		t.Helper()
		var i struct {
			Sprint *struct {
				ID int64 `json:"id"`
			} `json:"sprint"`
		}
		f.lead.Get("/api/v1/issues/"+key, &i)
		if i.Sprint == nil {
			return 0
		}
		return i.Sprint.ID
	}
	story := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Pay by invoice"})
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/"+story.Key, map[string]any{"fields": map[string]any{"sprint": sp.ID}}, nil)
	if got := sprintOf(story.Key); got != sp.ID {
		t.Fatalf("the sprint set alone: %d", got)
	}
	var history []struct {
		Items []struct {
			Field    string `json:"field"`
			ToString string `json:"toString"`
		} `json:"items"`
	}
	f.dev.Get("/api/v1/issues/"+story.Key+"/history", &history)
	if n := len(history); n == 0 || history[n-1].Items[0].Field != "sprint" || history[n-1].Items[0].ToString != "Launch" {
		t.Errorf("history: %+v", history)
	}
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/"+story.Key, map[string]any{"fields": map[string]any{"sprint": nil}}, nil)
	if got := sprintOf(story.Key); got != 0 {
		t.Errorf("the sprint removed: %d", got)
	}

	// Ute schedules in OPS, but only browses SHOP: the sprints of its board are not hers to plan.
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	ops := f.create(t, f.user, map[string]any{"project": "OPS", "typeId": f.cfg.typ("task"), "summary": "Backups"})
	plan := func(id any) *apitest.Response {
		return f.user.Do(http.MethodPatch, "/api/v1/issues/"+ops.Key, map[string]any{"fields": map[string]any{"sprint": id}})
	}
	if r := plan(sp.ID); r.Code() != "sprint.schedule-denied" {
		t.Fatalf("an issue went into the sprint of a project its editor does not schedule in: %d %s", r.Status, r.Body)
	}
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+ops.Key, map[string]any{"fields": map[string]any{"sprint": sp.ID}}, nil)
	if r := plan(nil); r.Code() != "sprint.schedule-denied" {
		t.Fatalf("an issue left the sprint of a project its editor does not schedule in: %d %s", r.Status, r.Body)
	}
	// Who schedules there, may.
	f.lead.Post("/api/v1/projects/SHOP/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	if r := plan(nil); r.Status != http.StatusOK || sprintOf(ops.Key) != 0 {
		t.Errorf("out of the sprint: %d %s", r.Status, r.Body)
	}
	if r := plan(sp.ID); r.Status != http.StatusOK || sprintOf(ops.Key) != sp.ID {
		t.Errorf("into the sprint: %d %s", r.Status, r.Body)
	}
}
