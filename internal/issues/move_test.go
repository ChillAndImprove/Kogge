// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

type movePlan struct {
	Types []struct {
		From    ref      `json:"from"`
		Choices []ref    `json:"choices"`
		Issues  []string `json:"issues"`
	} `json:"types"`
	Statuses []struct {
		Type    ref      `json:"type"`
		From    ref      `json:"from"`
		Choices []ref    `json:"choices"`
		Issues  []string `json:"issues"`
	} `json:"statuses"`
	Issues []struct {
		Key        string   `json:"key"`
		Movable    bool     `json:"movable"`
		Reason     string   `json:"reason"`
		WithParent string   `json:"withParent"`
		Subtasks   []string `json:"subtasks"`
		Changes    []struct {
			Field   string `json:"field"`
			From    string `json:"from"`
			To      string `json:"to"`
			Removed bool   `json:"removed"`
		} `json:"changes"`
	} `json:"issues"`
}

// TestMoveIssue: an issue moves to another project with its sub-tasks: what the target lacks is mapped (type,
// status) or removed (fields, values), it gets a new key there and keeps its comments, work, watchers and history;
// its old key keeps resolving in the API, in addresses and in queries; who is told of it hears so.
func TestMoveIssue(t *testing.T) {
	f := setup(t)
	bug, task, sub := f.cfg.typ("bug"), f.cfg.typ("task"), f.cfg.typ("subtask")
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "business"}, nil)
	for role, who := range map[int64]string{f.cfg.Roles[1].ID: "dirk", f.cfg.Roles[2].ID: "ute"} {
		f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(role)+"/members", map[string]any{"type": "user", "id": f.ids[who]}, nil)
	}
	var shopComponent, opsComponent, version struct {
		ID int64 `json:"id"`
	}
	f.lead.Post("/api/v1/projects/SHOP/components", map[string]any{"name": "Payments"}, &shopComponent)
	f.lead.Post("/api/v1/projects/OPS/components", map[string]any{"name": "payments"}, &opsComponent)
	f.lead.Post("/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &version)
	var field struct {
		ID int64 `json:"id"`
	}
	var shop struct {
		ID int64 `json:"id"`
	}
	f.lead.Get("/api/v1/projects/SHOP", &shop)
	f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": "Shop notes", "type": "text", "projectIds": []int64{shop.ID}}, &field)
	notes := "cf_" + itoa(field.ID)

	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": bug, "summary": "Card declined",
		"fields": map[string]any{"components": []int64{shopComponent.ID}, "fixVersions": []int64{version.ID}, notes: "Only here"}})
	s := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": sub, "summary": "Retry", "parent": i.Key})
	f.transition(t, i.Key, "inprogress")
	var transitions []availableTransition
	f.dev.Get("/api/v1/issues/"+i.Key+"/transitions", &transitions)
	review := 0
	var inReview int64
	for _, tr := range transitions {
		if tr.To.Category == "inprogress" {
			review, inReview = tr.ID, tr.To.ID
		}
	}
	f.dev.Post("/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": review}, nil)
	f.dev.Post("/api/v1/issues/"+i.Key+"/comments", map[string]any{"markdown": "Seen at checkout."}, nil)
	f.dev.Post("/api/v1/issues/"+i.Key+"/worklogs", map[string]any{"timeSpent": "2h"}, nil)
	f.user.MustDo(http.MethodPut, "/api/v1/issues/"+i.Key+"/watchers", nil, nil)
	var boards struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	f.lead.Get("/api/v1/boards?project=SHOP", &boards)
	var sp struct {
		ID int64 `json:"id"`
	}
	f.lead.Post("/api/v1/boards/"+itoa(boards.Items[0].ID)+"/sprints", map[string]any{"name": "Sprint 1"}, &sp)
	f.lead.Post("/api/v1/sprints/"+itoa(sp.ID)+"/issues", map[string]any{"issues": []string{i.Key}}, nil)
	// Webhooks of both projects hear of the move.
	hooks := map[string]int64{}
	for _, p := range []string{"SHOP", "OPS"} {
		var h struct {
			ID int64 `json:"id"`
		}
		f.lead.Post("/api/v1/webhooks?scope=project&key="+p, map[string]any{"name": p, "url": "https://hooks.example.com/kogge",
			"events": []string{"issue.moved"}, "active": true}, &h)
		hooks[p] = h.ID
	}

	// The plan: Bugs become one of OPS's types, In Review one of its statuses; the version and the field go.
	var plan movePlan
	f.dev.Post("/api/v1/issues/move-plan", map[string]any{"issues": []string{i.Key}, "project": "OPS"}, &plan)
	if len(plan.Types) != 1 || plan.Types[0].From.ID != bug || len(plan.Types[0].Choices) != 1 || plan.Types[0].Choices[0].ID != task {
		t.Fatalf("type questions: %+v", plan.Types)
	}
	if len(plan.Issues) != 2 || !plan.Issues[0].Movable || !slices.Equal(plan.Issues[0].Subtasks, []string{s.Key}) ||
		plan.Issues[1].WithParent != i.Key {
		t.Fatalf("plan issues: %+v", plan.Issues)
	}
	types := []map[string]any{{"from": bug, "to": task}}
	f.dev.Post("/api/v1/issues/move-plan", map[string]any{"issues": []string{i.Key}, "project": "OPS", "types": types}, &plan)
	if len(plan.Statuses) != 1 || plan.Statuses[0].From.ID != inReview || plan.Statuses[0].Type.ID != task || len(plan.Statuses[0].Choices) != 3 {
		t.Fatalf("status questions: %+v", plan.Statuses)
	}
	removed := map[string]bool{}
	for _, c := range plan.Issues[0].Changes {
		if c.Removed {
			removed[c.Field] = true
		}
	}
	if !removed["fixVersions"] || !removed[notes] || removed["components"] || !removed["sprint"] {
		t.Errorf("what the move removes: %+v", plan.Issues[0].Changes)
	}
	progress := f.cfg.status("inprogress")[0]
	statuses := []map[string]any{{"type": task, "from": inReview, "to": progress}}

	// Who may not move, or create in the target, cannot; without the choices, nothing moves.
	if r := f.user.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS"}); r.Code() != "issue.move-denied" {
		t.Errorf("a user moved the issue: %d %s", r.Status, r.Body)
	}
	if r := f.other.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS"}); r.Status != http.StatusNotFound {
		t.Errorf("an outsider moved the issue: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS", "types": types}); r.Code() != "issue.move-status-missing" {
		t.Errorf("a move without the status: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+s.Key+"/move", map[string]any{"project": "OPS", "types": types}); r.Code() != "issue.move-subtask" {
		t.Errorf("a sub-task moved alone: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "SHOP"}); r.Code() != "issue.move-same-project" {
		t.Errorf("a move into its own project: %d %s", r.Status, r.Body)
	}

	var moved fullIssue
	f.dev.Post("/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS", "types": types, "statuses": statuses}, &moved)
	if moved.Key != "OPS-1" || moved.Project.Key != "OPS" || moved.Type.ID != task || moved.Status.ID != progress {
		t.Fatalf("moved: %+v", moved)
	}
	if len(moved.Components) != 1 || moved.Components[0].ID != opsComponent.ID || len(moved.FixVersions) != 0 || moved.Fields[notes] != nil ||
		moved.Sprint != nil {
		t.Errorf("values after the move: %+v", moved)
	}
	if len(moved.Subtasks) != 1 || moved.Subtasks[0].Key != "OPS-2" {
		t.Errorf("sub-tasks after the move: %+v", moved.Subtasks)
	}

	// The old keys keep working: the API, addresses, queries.
	if got := getIssue(t, f.dev, i.Key); got.Key != "OPS-1" {
		t.Errorf("%s resolves to %s", i.Key, got.Key)
	}
	if got := getIssue(t, f.dev, s.Key); got.Key != "OPS-2" || got.Parent == nil || got.Parent.Key != "OPS-1" {
		t.Errorf("%s resolves to %+v", s.Key, got)
	}
	if r := f.dev.Do(http.MethodGet, "/browse/"+i.Key, nil); r.Status != http.StatusFound || r.Header.Get("Location") != "/browse/OPS-1" {
		t.Errorf("the old address: %d %s", r.Status, r.Header.Get("Location"))
	}
	if r := f.other.Do(http.MethodGet, "/browse/"+i.Key, nil); r.Status == http.StatusFound {
		t.Errorf("an outsider is told where the issue went: %s", r.Header.Get("Location"))
	}
	for q, want := range map[string]string{"key = " + i.Key: "OPS-1", "key in (" + s.Key + ", " + i.Key + ") ORDER BY key": "OPS-1,OPS-2",
		"project = SHOP": "", `text ~ "` + i.Key + `"`: "OPS-1", "parent = " + i.Key: "OPS-2"} {
		if got := f.search(t, q); got != want {
			t.Errorf("%s: %s, want %s", q, got, want)
		}
	}

	// What is the issue's own stays; the history shows the move.
	var comments, worklogs []map[string]any
	f.dev.Get("/api/v1/issues/OPS-1/comments", &comments)
	f.dev.Get("/api/v1/issues/OPS-1/worklogs", &worklogs)
	if len(comments) != 1 || len(worklogs) != 1 || !getIssue(t, f.user, "OPS-1").Watching {
		t.Errorf("comments %d, work logs %d, watching %v", len(comments), len(worklogs), getIssue(t, f.user, "OPS-1").Watching)
	}
	if !historyHas(t, f.dev, "OPS-1", "key", "OPS-1") || !historyHas(t, f.dev, "OPS-1", "project", "Operations") ||
		!historyHas(t, f.dev, "OPS-1", "sprint", "") || !historyHas(t, f.dev, "OPS-2", "key", "OPS-2") {
		t.Error("the history does not show the move")
	}
	var events []struct {
		Details map[string]any `json:"details"`
	}
	f.admin.Get("/api/v1/admin/audit?action=issue.move", &events)
	if len(events) != 1 || events[0].Details["from"] != i.Key || events[0].Details["to"] != "OPS-1" {
		t.Errorf("audit: %+v", events)
	}
	f.env.WorkJobs()
	moves := 0
	for _, n := range inboxOf(t, f.user) {
		if n.Event == "issue.moved" && n.EntityID == "OPS-1" {
			moves++
		}
	}
	if moves != 1 {
		t.Errorf("Ute, who watches the issue, was notified %d times of the move", moves)
	}
	for p, id := range hooks {
		var deliveries []struct {
			Event string `json:"event"`
		}
		f.lead.Get("/api/v1/webhooks/"+itoa(id)+"/deliveries", &deliveries)
		// The sub-task moved too: an event for each issue.
		if len(deliveries) != 2 || deliveries[0].Event != "issue.moved" {
			t.Errorf("webhook of %s: %+v", p, deliveries)
		}
	}

	// Back again: a new key, and all the old ones keep resolving.
	f.lead.Post("/api/v1/issues/OPS-1/move", map[string]any{"project": "SHOP"}, &moved)
	if moved.Key != "SHOP-3" {
		t.Fatalf("moved back: %s", moved.Key)
	}
	for _, k := range []string{i.Key, "OPS-1"} {
		if got := getIssue(t, f.dev, k); got.Key != "SHOP-3" {
			t.Errorf("%s resolves to %s", k, got.Key)
		}
	}
}

// TestMoveSecurityLevel: a move that would change who sees an issue — its security level goes or changes — needs the
// permission to set security levels.
func TestMoveSecurityLevel(t *testing.T) {
	f := setup(t)
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Internal", "levels": []map[string]any{
		{"name": "Staff", "members": []map[string]any{{"holderType": "role", "holderId": f.cfg.Roles[1].ID},
			{"holderType": "role", "holderId": f.cfg.Roles[0].ID}}}}}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"securitySchemeId": scheme.ID}, nil)
	var schemes []struct {
		ID     int64 `json:"id"`
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	f.admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	level := schemes[0].Levels[0].ID
	i := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Salaries",
		"fields": map[string]any{"security": level}})
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["dirk"]}, nil)

	var plan movePlan
	f.dev.Post("/api/v1/issues/move-plan", map[string]any{"issues": []string{i.Key}, "project": "OPS"}, &plan)
	if len(plan.Issues) != 1 || plan.Issues[0].Movable || !strings.Contains(plan.Issues[0].Reason, "security level") {
		t.Fatalf("plan: %+v", plan.Issues)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS"}); r.Code() != "issue.move-security" {
		t.Fatalf("a developer removed the security level by moving: %d %s", r.Status, r.Body)
	}
	var moved struct {
		Key           string `json:"key"`
		SecurityLevel *ref   `json:"securityLevel"`
	}
	f.lead.Post("/api/v1/issues/"+i.Key+"/move", map[string]any{"project": "OPS"}, &moved)
	if moved.Key != "OPS-1" || moved.SecurityLevel != nil {
		t.Errorf("moved by the lead: %+v", moved)
	}
}

// TestBulkMove: issues move to another project in bulk with the same choices; a chosen sub-task goes with its parent,
// and the old keys resolve to the new ones.
func TestBulkMove(t *testing.T) {
	f := setup(t)
	story, sub := f.cfg.typ("story"), f.cfg.typ("subtask")
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "business"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["dirk"]}, nil)
	a := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Alpha"})
	b := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Beta"})
	s := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": sub, "summary": "Beta part", "parent": b.Key})
	lone := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": sub, "summary": "Alone", "parent": a.Key})
	task := f.cfg.typ("task")

	var check struct {
		Issues []struct {
			Key       string `json:"key"`
			Permitted bool   `json:"permitted"`
		} `json:"issues"`
		Move *movePlan `json:"move"`
	}
	// The sub-task of Beta comes first: it still moves with its parent.
	selection := []string{s.Key, a.Key, b.Key}
	f.dev.Post("/api/v1/issues/bulk/check", map[string]any{"issues": selection, "action": "move", "move": map[string]any{"project": "OPS"}}, &check)
	if check.Move == nil || len(check.Move.Types) != 1 || check.Move.Types[0].From.ID != story {
		t.Fatalf("bulk move plan: %+v", check.Move)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": selection, "action": "move"}); r.Code() != "bulk.move-target" {
		t.Errorf("a bulk move without a target: %d %s", r.Status, r.Body)
	}
	res := f.runBulk(t, f.dev, map[string]any{"issues": selection, "action": "move", "move": map[string]any{"project": "OPS",
		"types": []map[string]any{{"from": story, "to": task}}}})
	if res.Done != 3 {
		t.Fatalf("bulk move: %+v", res)
	}
	newKeys := map[string]string{}
	for _, it := range res.Items {
		newKeys[it.Key] = it.NewKey
	}
	if newKeys[a.Key] == "" || newKeys[b.Key] == "" || newKeys[s.Key] == "" || !strings.Contains(res.item(t, s.Key).Message, newKeys[b.Key]) {
		t.Fatalf("new keys: %+v", res.Items)
	}
	for old, key := range newKeys {
		if got := getIssue(t, f.dev, old); got.Key != key || got.Project.Key != "OPS" {
			t.Errorf("%s resolves to %+v, want %s", old, got, key)
		}
	}
	// The sub-task of Alpha went with it, although it was not chosen.
	if got := getIssue(t, f.dev, lone.Key); got.Project.Key != "OPS" {
		t.Errorf("the unchosen sub-task stayed: %+v", got)
	}
	if got := f.search(t, "key in ("+a.Key+", "+b.Key+") ORDER BY key"); got != newKeys[a.Key]+","+newKeys[b.Key] {
		t.Errorf("old keys in a query: %s", got)
	}
	// A sub-task chosen without its parent does not move.
	other := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Gamma"})
	part := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": sub, "summary": "Gamma part", "parent": other.Key})
	res = f.runBulk(t, f.dev, map[string]any{"issues": []string{part.Key}, "action": "move", "move": map[string]any{"project": "OPS",
		"types": []map[string]any{{"from": story, "to": task}}}})
	if res.Skipped != 1 || res.item(t, part.Key).Code != "issue.move-subtask" {
		t.Errorf("a sub-task alone: %+v", res)
	}
}
