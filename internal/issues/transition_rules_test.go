// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type availableTransition struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	To              ref      `json:"to"`
	Screen          []string `json:"screen"`
	CommentRequired bool     `json:"commentRequired"`
}

func transitionNamed(list []availableTransition, name string) *availableTransition {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func comment(text string) map[string]any {
	return map[string]any{"type": "doc", "content": []map[string]any{
		{"type": "paragraph", "content": []map[string]any{{"type": "text", "text": text}}}}}
}

// TestTransitionRules runs a workflow's conditions, validators and post-functions: who sees and may run a
// transition (assignee only, a project role, sub-tasks done), what it needs (a comment, a field), and what it changes
// afterwards (a comment, the resolution, the assignee).
func TestTransitionRules(t *testing.T) {
	f := setup(t)
	open, progress, done := f.cfg.status("todo")[0], f.cfg.status("inprogress")[0], f.cfg.status("done")[0]
	developers := f.cfg.Roles[1].ID
	resolution := f.cfg.Resolutions[len(f.cfg.Resolutions)-1].ID
	var wf struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/workflows", map[string]any{"name": "Gated", "definition": map[string]any{
		"statuses": []int64{open, progress, done}, "initial": open, "options": map[string]any{"autoResolution": false},
		"transitions": []map[string]any{
			{"id": 1, "name": "Start", "from": []int64{open}, "to": progress,
				"conditions":    []map[string]any{{"type": "assignee-only"}},
				"postFunctions": []map[string]any{{"type": "add-comment", "text": "Work has started."}}},
			{"id": 2, "name": "Finish", "from": []int64{progress}, "to": done,
				"conditions": []map[string]any{{"type": "role", "role": developers}, {"type": "subtasks-done"}},
				"validators": []map[string]any{{"type": "comment-required"}, {"type": "field-required", "field": "duedate"}},
				"postFunctions": []map[string]any{{"type": "set-resolution", "resolution": resolution},
					{"type": "unassign"}}},
			{"id": 3, "name": "Reopen", "from": []int64{done}, "to": open,
				"postFunctions": []map[string]any{{"type": "clear-resolution"}, {"type": "assign-to-reporter"}}},
		},
	}}, &wf)

	// Stories of SHOP follow the workflow; sub-tasks keep the project's default one.
	story := f.cfg.typ("story")
	var types []map[string]any
	f.lead.Get("/api/v1/projects/SHOP/issue-types", &types)
	for _, ty := range types {
		if int64(ty["issueTypeId"].(float64)) == story {
			ty["workflowId"] = wf.ID
		}
	}
	f.lead.MustDo(http.MethodPut, "/api/v1/projects/SHOP/issue-types", types, nil)

	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Gift cards"})
	if i.Status.ID != open {
		t.Fatalf("initial status: %+v", i.Status)
	}
	transitions := func(c *apitest.Client) []availableTransition {
		var list []availableTransition
		c.Get("/api/v1/issues/"+i.Key+"/transitions", &list)
		return list
	}

	// Start: for the assignee only, and it comments.
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/"+i.Key, map[string]any{"fields": map[string]any{"assignee": f.ids["lena"]}}, nil)
	if transitionNamed(transitions(f.dev), "Start") != nil {
		t.Fatal("Start offered to someone who is not the assignee")
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": 1}); r.Code() != "issue.transition-not-allowed" {
		t.Fatalf("Start by someone who is not the assignee: %d %s", r.Status, r.Body)
	}
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/"+i.Key, map[string]any{"fields": map[string]any{"assignee": f.ids["dirk"]}}, nil)
	if transitionNamed(transitions(f.dev), "Start") == nil {
		t.Fatal("Start not offered to the assignee")
	}
	f.dev.Post("/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": 1}, &i)
	if i.Status.ID != progress {
		t.Fatalf("after Start: %+v", i.Status)
	}
	if r := f.dev.MustDo(http.MethodGet, "/api/v1/issues/"+i.Key+"/comments", nil, nil); !strings.Contains(string(r.Body), "Work has started.") {
		t.Fatalf("Start added no comment: %s", r.Body)
	}

	// Finish: for the Developers role, once the sub-tasks are done.
	if transitionNamed(transitions(f.lead), "Finish") != nil {
		t.Fatal("Finish offered outside the Developers role")
	}
	if r := f.lead.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": 2}); r.Code() != "issue.transition-not-allowed" {
		t.Fatalf("Finish outside the Developers role: %d %s", r.Status, r.Body)
	}
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Design", "parent": i.Key})
	if transitionNamed(transitions(f.dev), "Finish") != nil {
		t.Fatal("Finish offered while a sub-task is open")
	}
	var subTransitions []availableTransition
	f.dev.Get("/api/v1/issues/"+sub.Key+"/transitions", &subTransitions)
	for _, tr := range subTransitions {
		if tr.To.Category == "done" {
			f.dev.Post("/api/v1/issues/"+sub.Key+"/transitions", map[string]any{"transitionId": tr.ID}, &sub)
			break
		}
	}
	if sub.Status.Category != "done" {
		t.Fatalf("sub-task not done: %+v", sub.Status)
	}
	finish := transitionNamed(transitions(f.dev), "Finish")
	if finish == nil || !finish.CommentRequired || !contains(finish.Screen, "duedate") {
		t.Fatalf("Finish for the Developers role with sub-tasks done: %+v", finish)
	}

	// Its validators: a comment and a due date.
	path := "/api/v1/issues/" + i.Key + "/transitions"
	if r := f.dev.Do(http.MethodPost, path, map[string]any{"transitionId": 2}); r.Code() != "issue.comment-required" {
		t.Fatalf("Finish without a comment: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, path, map[string]any{"transitionId": 2, "comment": comment("Shipped.")}); r.Code() != "issue.field-required" {
		t.Fatalf("Finish without a due date: %d %s", r.Status, r.Body)
	}
	var unchanged issue
	if r := f.dev.MustDo(http.MethodGet, "/api/v1/issues/"+i.Key, nil, &unchanged); unchanged.Status.ID != progress {
		t.Fatalf("a refused transition changed the issue: %s", r.Body)
	}
	// Answers leave out empty fields, so each one is read into a new value.
	var finished issue
	f.dev.Post(path, map[string]any{"transitionId": 2, "comment": comment("Shipped."),
		"fields": map[string]any{"duedate": "2026-12-31"}}, &finished)
	if finished.Status.ID != done || finished.Resolution == nil || finished.Resolution.ID != resolution || finished.Assignee != nil {
		t.Fatalf("after Finish: status %+v, resolution %+v, assignee %+v", finished.Status, finished.Resolution, finished.Assignee)
	}

	// Reopen: clears the resolution and gives the issue back to its reporter.
	var reopened issue
	f.lead.Post(path, map[string]any{"transitionId": 3}, &reopened)
	if reopened.Status.ID != open || reopened.Resolution != nil || reopened.Assignee == nil || reopened.Assignee.ID != f.ids["dirk"] {
		t.Fatalf("after Reopen: status %+v, resolution %+v, assignee %+v", reopened.Status, reopened.Resolution, reopened.Assignee)
	}
}
