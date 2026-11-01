// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type ref struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Category string `json:"category"`
	Level    int    `json:"level"`
}

type config struct {
	IssueTypes  []ref `json:"issueTypes"`
	Statuses    []ref `json:"statuses"`
	Priorities  []ref `json:"priorities"`
	Resolutions []ref `json:"resolutions"`
	LinkTypes   []ref `json:"linkTypes"`
	Roles       []ref `json:"roles"`
	Fields      []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"fields"`
}

func (c config) typ(icon string) int64 {
	for _, t := range c.IssueTypes {
		if t.Icon == icon {
			return t.ID
		}
	}
	return 0
}

func (c config) status(category string) []int64 {
	var out []int64
	for _, s := range c.Statuses {
		if s.Category == category {
			out = append(out, s.ID)
		}
	}
	return out
}

func (c config) field(typ string) string {
	for _, f := range c.Fields {
		if f.Type == typ && strings.HasPrefix(f.Key, "cf_") {
			return f.Key
		}
	}
	return ""
}

type issue struct {
	ID         int64  `json:"id"`
	Key        string `json:"key"`
	Summary    string `json:"summary"`
	Status     ref    `json:"status"`
	Type       ref    `json:"type"`
	Version    int64  `json:"version"`
	Labels     []string
	Fields     map[string]any `json:"fields"`
	Resolution *ref           `json:"resolution"`
	Assignee   *struct {
		ID int64 `json:"id"`
	} `json:"assignee"`
	Parent *struct {
		Key string `json:"key"`
	} `json:"parent"`
	RemainingEstimate *int64 `json:"remainingEstimate"`
	TimeSpent         int64  `json:"timeSpent"`
	Permissions       map[string]bool
	Links             []struct {
		Label string `json:"label"`
		Issue struct {
			Key string `json:"key"`
		} `json:"issue"`
	} `json:"links"`
	Watching    bool `json:"watching"`
	Votes       int  `json:"votes"`
	Attachments []struct {
		ID        int64  `json:"id"`
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		CanDelete bool   `json:"canDelete"`
	} `json:"attachments"`
}

type fixture struct {
	env   *apitest.Env
	admin *apitest.Client
	lead  *apitest.Client // project creator (Administrators role)
	dev   *apitest.Client // Developers role
	user  *apitest.Client // Users role
	other *apitest.Client // no role
	cfg   config
	ids   map[string]int64
}

func userID(t *testing.T, c *apitest.Client) int64 {
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	c.Get("/api/v1/me", &me)
	return me.User.ID
}

func setup(t *testing.T, opts ...apitest.Option) *fixture {
	t.Helper()
	env := apitest.New(t, opts...)
	f := &fixture{env: env, admin: env.Admin(), ids: map[string]int64{}}
	f.lead, f.dev, f.user, f.other = env.CreateUser("lena"), env.CreateUser("dirk"), env.CreateUser("ute"), env.CreateUser("otto")
	for name, c := range map[string]*apitest.Client{"lena": f.lead, "dirk": f.dev, "ute": f.user, "otto": f.other} {
		f.ids[name] = userID(t, c)
	}
	f.admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": "project.create",
		"principalType": "user", "principalId": f.ids["lena"]}, nil)
	f.admin.Get("/api/v1/issue-config", &f.cfg)
	f.lead.Post("/api/v1/projects", map[string]any{"key": "SHOP", "name": "Webshop", "template": "scrum"}, nil)
	roleID := func(n int) int64 { return f.cfg.Roles[n].ID } // Administrators, Developers, Users
	f.lead.Post("/api/v1/projects/SHOP/roles/"+fmtInt(roleID(1))+"/members", map[string]any{"type": "user", "id": f.ids["dirk"]}, nil)
	f.lead.Post("/api/v1/projects/SHOP/roles/"+fmtInt(roleID(2))+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	return f
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func fmtInt(i int64) string { return strconv.FormatInt(i, 10) }

func (f *fixture) create(t *testing.T, c *apitest.Client, body map[string]any) issue {
	t.Helper()
	var i issue
	c.Post("/api/v1/issues", body, &i)
	return i
}

func TestProjectVisibilityFollowsRoles(t *testing.T) {
	f := setup(t)
	var projects []struct {
		Key string `json:"key"`
	}
	f.other.Get("/api/v1/projects", &projects)
	if len(projects) != 0 {
		t.Fatalf("a user without a role sees projects: %v", projects)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/projects/SHOP", nil); r.Status != http.StatusNotFound {
		t.Fatalf("project without role: %d", r.Status)
	}
	f.user.Get("/api/v1/projects", &projects)
	if len(projects) != 1 || projects[0].Key != "SHOP" {
		t.Fatalf("role member: %v", projects)
	}
	var view struct {
		Permissions []string `json:"permissions"`
		IssueTypes  []ref    `json:"issueTypes"`
	}
	f.user.Get("/api/v1/projects/SHOP", &view)
	if len(view.IssueTypes) != 5 || contains(view.Permissions, "issue.edit") || !contains(view.Permissions, "comment.add") {
		t.Fatalf("user view: %+v", view)
	}
	// Only administrators of the project change it.
	if r := f.dev.Do(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"name": "Hijacked"}); r.Status != http.StatusForbidden {
		t.Fatalf("developer changed the project: %d", r.Status)
	}
	if r := f.other.Do(http.MethodPost, "/api/v1/projects", map[string]any{"key": "NOPE", "name": "Nope"}); r.Status != http.StatusForbidden {
		t.Fatalf("project creation without permission: %d", r.Status)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestIssueLifecycle(t *testing.T) {
	f := setup(t)
	points := f.cfg.field("number")
	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Checkout with PayPal",
		"descriptionMarkdown": "As a buyer I **want** to pay with PayPal.\n\nSee SHOP-99.",
		"fields":              map[string]any{"labels": []string{"payment", "checkout", "payment"}, points: 5}})
	if i.Key != "SHOP-1" || len(i.Labels) != 2 || i.Fields[points] != float64(5) || !i.Watching {
		t.Fatalf("created: %+v", i)
	}
	if i.Status.Category != "todo" {
		t.Fatalf("initial status: %+v", i.Status)
	}

	// Optimistic concurrency.
	r := f.dev.Do(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"fields": map[string]any{"summary": "Stale"}}, "If-Match", `"999"`)
	if r.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale update: %d %s", r.Status, r.Body)
	}
	r = f.dev.Do(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"fields": map[string]any{"summary": "Checkout with PayPal and SEPA",
		"assignee": f.ids["dirk"]}}, "If-Match", `"`+fmtInt(i.Version)+`"`)
	if r.Status != http.StatusOK || r.Header.Get("ETag") == "" {
		t.Fatalf("update: %d %s", r.Status, r.Body)
	}
	// Users (role) may comment but not edit; others do not see the issue.
	if r := f.user.Do(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"fields": map[string]any{"summary": "x"}}); r.Status != http.StatusForbidden {
		t.Fatalf("user edited: %d", r.Status)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/issues/SHOP-1", nil); r.Status != http.StatusNotFound {
		t.Fatalf("outsider sees the issue: %d", r.Status)
	}
	f.user.Post("/api/v1/issues/SHOP-1/comments", map[string]any{"markdown": "Looks good, @dirk"}, nil)

	// Transitions: start progress, done (resolution set automatically), reopen (cleared).
	var transitions []struct {
		ID int `json:"id"`
		To ref `json:"to"`
	}
	f.dev.Get("/api/v1/issues/SHOP-1/transitions", &transitions)
	var start, done int
	for _, tr := range transitions {
		switch tr.To.Category {
		case "inprogress":
			start = tr.ID
		case "done":
			done = tr.ID
		}
	}
	if start == 0 || done == 0 {
		t.Fatalf("transitions: %+v", transitions)
	}
	f.dev.Post("/api/v1/issues/SHOP-1/transitions", map[string]any{"transitionId": start}, &i)
	if i.Status.Category != "inprogress" {
		t.Fatalf("after start: %+v", i.Status)
	}
	f.dev.Post("/api/v1/issues/SHOP-1/transitions", map[string]any{"transitionId": done}, &i)
	if i.Status.Category != "done" || i.Resolution == nil {
		t.Fatalf("after done: %+v %+v", i.Status, i.Resolution)
	}
	f.dev.Get("/api/v1/issues/SHOP-1/transitions", &transitions)
	var reopened issue
	f.dev.Post("/api/v1/issues/SHOP-1/transitions", map[string]any{"transitionId": transitions[0].ID}, &reopened)
	if reopened.Resolution != nil || reopened.Status.Category != "todo" {
		t.Fatalf("reopened issue: %+v %+v", reopened.Status, reopened.Resolution)
	}
	if r := f.user.Do(http.MethodPost, "/api/v1/issues/SHOP-1/transitions", map[string]any{"transitionId": start}); r.Status < 400 {
		t.Fatalf("user transitioned: %d", r.Status)
	}

	var history []struct {
		Items []struct {
			Field    string `json:"field"`
			ToString string `json:"toString"`
		} `json:"items"`
	}
	f.dev.Get("/api/v1/issues/SHOP-1/history", &history)
	fields := []string{}
	for _, h := range history {
		for _, it := range h.Items {
			fields = append(fields, it.Field)
		}
	}
	for _, want := range []string{"summary", "assignee", "status", "resolution"} {
		if !contains(fields, want) {
			t.Errorf("history lacks %s: %v", want, fields)
		}
	}

	// The assignee was notified about the comment (mention and assignee).
	var inbox []struct {
		Event string `json:"event"`
	}
	f.dev.Get("/api/v1/notifications", &inbox)
	if len(inbox) == 0 {
		t.Fatal("no notification for the assignee")
	}
}

func TestDescriptionAsMarkdown(t *testing.T) {
	f := setup(t)
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Shopping list",
		"descriptionMarkdown": "Buy **milk**.\n\n- [ ] bread\n- [x] eggs"})
	type described struct {
		Description map[string]any `json:"description"`
		Markdown    string         `json:"descriptionMarkdown"`
	}
	var got described
	f.dev.Get("/api/v1/issues/SHOP-1", &got)
	if got.Description == nil || got.Markdown != "" {
		t.Fatalf("without markdown=true: %+v", got)
	}
	f.dev.Get("/api/v1/issues/SHOP-1?markdown=true", &got)
	if !strings.Contains(got.Markdown, "**milk**") || !strings.Contains(got.Markdown, "eggs") {
		t.Fatalf("as Markdown: %q", got.Markdown)
	}

	// Changing it as Markdown; empty removes it; both ways at once is refused.
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"descriptionMarkdown": "Buy *oat* milk."}, nil)
	f.dev.Get("/api/v1/issues/SHOP-1?markdown=true", &got)
	if !strings.Contains(got.Markdown, "*oat*") || strings.Contains(got.Markdown, "eggs") {
		t.Fatalf("changed: %q", got.Markdown)
	}
	r := f.dev.Do(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"descriptionMarkdown": "x",
		"fields": map[string]any{"description": nil}})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "issue.description-twice") {
		t.Fatalf("both: %d %s", r.Status, r.Body)
	}
	f.dev.MustDo(http.MethodPatch, "/api/v1/issues/SHOP-1", map[string]any{"fields": map[string]any{}, "descriptionMarkdown": ""}, nil)
	got = described{}
	f.dev.Get("/api/v1/issues/SHOP-1?markdown=true", &got)
	if got.Description != nil || got.Markdown != "" {
		t.Fatalf("removed: %+v", got)
	}
}

func TestSubtasksEpicsAndLinks(t *testing.T) {
	f := setup(t)
	epic := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("epic"), "summary": "Payments"})
	story := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "PayPal", "parent": epic.Key})
	if story.Parent == nil || story.Parent.Key != epic.Key {
		t.Fatalf("story parent: %+v", story.Parent)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Orphan"}); r.Code() != "issue.parent-required" {
		t.Fatalf("sub-task without parent: %s", r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Bad", "parent": epic.Key}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("sub-task under an epic: %d %s", r.Status, r.Body)
	}
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Sandbox account", "parent": story.Key})
	bug := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("bug"), "summary": "Rounding error"})
	f.dev.Post("/api/v1/issues/"+bug.Key+"/links", map[string]any{"typeId": f.cfg.LinkTypes[0].ID, "issue": story.Key}, nil)
	var got issue
	f.dev.Get("/api/v1/issues/"+story.Key, &got)
	if len(got.Links) != 1 || got.Links[0].Issue.Key != bug.Key {
		t.Fatalf("links: %+v", got.Links)
	}
	var parent struct {
		Subtasks []struct {
			Key string `json:"key"`
		} `json:"subtasks"`
	}
	f.dev.Get("/api/v1/issues/"+story.Key, &parent)
	if len(parent.Subtasks) != 1 || parent.Subtasks[0].Key != sub.Key {
		t.Fatalf("subtasks: %+v", parent.Subtasks)
	}
	var res struct {
		Total int `json:"total"`
	}
	f.dev.Get("/api/v1/search?q="+url.QueryEscape("issue in childIssuesOf("+epic.Key+")"), &res)
	if res.Total != 2 {
		t.Fatalf("childIssuesOf: %d", res.Total)
	}
	f.dev.Get("/api/v1/search?q="+url.QueryEscape("issue in linkedIssues("+story.Key+")"), &res)
	if res.Total != 1 {
		t.Fatalf("linkedIssues: %d", res.Total)
	}
}

func TestExistingIssuesBecomeSubtasks(t *testing.T) {
	f := setup(t)
	epic := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("epic"), "summary": "Payments"})
	story := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "PayPal", "parent": epic.Key})
	bug := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("bug"), "summary": "Call the bank", "parent": epic.Key})
	add := func(c *apitest.Client, parent, child string) *apitest.Response {
		return c.Do(http.MethodPost, "/api/v1/issues/"+parent+"/subtasks", map[string]any{"issue": child})
	}
	if r := add(f.other, story.Key, bug.Key); r.Status != http.StatusNotFound {
		t.Fatalf("no access: %d %s", r.Status, r.Body)
	}
	if r := add(f.user, story.Key, bug.Key); r.Status != http.StatusForbidden || r.Code() != "issue.edit-denied" {
		t.Fatalf("no edit: %d %s", r.Status, r.Body)
	}
	if r := add(f.dev, story.Key, bug.Key); r.Status != http.StatusNoContent {
		t.Fatalf("add: %d %s", r.Status, r.Body)
	}
	var got issue
	f.dev.Get("/api/v1/issues/"+bug.Key, &got)
	if got.Type.ID != f.cfg.typ("subtask") || got.Parent == nil || got.Parent.Key != story.Key {
		t.Fatalf("the bug became %+v under %+v", got.Type, got.Parent)
	}
	var parent struct {
		Subtasks []struct {
			Key string `json:"key"`
		} `json:"subtasks"`
	}
	f.dev.Get("/api/v1/issues/"+story.Key, &parent)
	if len(parent.Subtasks) != 1 || parent.Subtasks[0].Key != bug.Key {
		t.Fatalf("subtasks: %+v", parent.Subtasks)
	}
	if keys := search(t, f.dev, "parent = "+epic.Key); len(keys) != 1 || keys[0] != story.Key {
		t.Fatalf("the epic's children: %v", keys)
	}
	var history []struct {
		Items []struct {
			Field string `json:"field"`
		} `json:"items"`
	}
	f.dev.Get("/api/v1/issues/"+bug.Key+"/history", &history)
	if len(history) != 1 || len(history[0].Items) != 2 || history[0].Items[0].Field != "issuetype" || history[0].Items[1].Field != "parent" {
		t.Fatalf("history: %+v", history)
	}
	if r := add(f.dev, story.Key, bug.Key); r.Status != http.StatusNoContent {
		t.Fatalf("again: %d %s", r.Status, r.Body)
	}

	// A sub-task changes its parent and keeps its type.
	other := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Invoices"})
	add(f.dev, other.Key, bug.Key)
	got = issue{}
	f.dev.Get("/api/v1/issues/"+bug.Key, &got)
	if got.Type.ID != f.cfg.typ("subtask") || got.Parent == nil || got.Parent.Key != other.Key {
		t.Fatalf("moved sub-task: %+v under %+v", got.Type, got.Parent)
	}

	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "scrum"}, nil)
	foreign := f.create(t, f.lead, map[string]any{"project": "OPS", "typeId": f.cfg.typ("story"), "summary": "Elsewhere"})
	for name, c := range map[string]struct {
		parent, child, code string
	}{
		"itself":           {story.Key, story.Key, "issue.subtask-self"},
		"under an epic":    {epic.Key, story.Key, "issue.subtask-parent"},
		"under a sub-task": {bug.Key, story.Key, "issue.subtask-parent"},
		"an epic":          {story.Key, epic.Key, "issue.subtask-epic"},
		"with sub-tasks":   {story.Key, other.Key, "issue.subtask-children"},
		"another project":  {story.Key, foreign.Key, "issue.subtask-project"},
	} {
		if r := add(f.lead, c.parent, c.child); r.Status != http.StatusUnprocessableEntity || r.Code() != c.code {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
}

// items returns the items of a page of a list ({items, total}).
func items[T any](t *testing.T, c *apitest.Client, path string) []T {
	t.Helper()
	var page struct {
		Items []T `json:"items"`
	}
	c.Get(path, &page)
	return page.Items
}

func search(t *testing.T, c *apitest.Client, q string) []string {
	t.Helper()
	var res struct {
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	c.Get("/api/v1/search?limit=100&q="+url.QueryEscape(q), &res)
	keys := []string{}
	for _, i := range res.Issues {
		keys = append(keys, i.Key)
	}
	return keys
}

func TestSearch(t *testing.T) {
	f := setup(t)
	points := f.cfg.field("number")
	high := f.cfg.Priorities[1].ID
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("bug"), "summary": "Rounding errors in invoices",
		"fields": map[string]any{"priority": high, "labels": []string{"finance"}, "assignee": f.ids["dirk"], points: 3}})
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Gift cards",
		"descriptionMarkdown": "Customers redeem vouchers", "fields": map[string]any{points: 8}})
	f.create(t, f.user, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Update shipping texts"})

	cases := map[string]string{
		`project = SHOP ORDER BY key`:                              "SHOP-1,SHOP-2,SHOP-3",
		`assignee = dirk`:                                          "SHOP-1",
		`assignee = currentUser()`:                                 "SHOP-1",
		`assignee is EMPTY ORDER BY key`:                           "SHOP-2,SHOP-3",
		`assignee != dirk`:                                         "",
		`reporter = ute`:                                           "SHOP-3",
		`summary ~ "rounding"`:                                     "SHOP-1",
		`summary ~ invoice`:                                        "SHOP-1",
		`text ~ vouchers`:                                          "SHOP-2",
		`summary ~ "gift*"`:                                        "SHOP-2",
		`labels = finance`:                                         "SHOP-1",
		`labels not in (finance) ORDER BY key`:                     "",
		`"` + "Story Points" + `" > 4`:                             "SHOP-2",
		`cf[` + strings.TrimPrefix(points, "cf_") + `] <= 3`:       "SHOP-1",
		`key > SHOP-1 ORDER BY key DESC`:                           "SHOP-3,SHOP-2",
		`statusCategory = "To Do" AND created >= -1d ORDER BY key`: "SHOP-1,SHOP-2,SHOP-3",
		`created < -1d`:                                            "",
		`created >= 2020-01-01 ORDER BY key`:                       "SHOP-1,SHOP-2,SHOP-3",
		`created < 2020-01-01`:                                     "",
		`updated > 2099-12-31`:                                     "",
		`resolved <= 2099-12-31`:                                   "",
		`project = SHOP ORDER BY priority DESC, key`:               "SHOP-1,SHOP-2,SHOP-3",
		`NOT summary ~ gift ORDER BY key`:                          "SHOP-1,SHOP-3",
	}
	for q, want := range cases {
		if got := strings.Join(search(t, f.dev, q), ","); got != want {
			t.Errorf("%s: got %q, want %q", q, got, want)
		}
	}
	// Outsiders see nothing, not even that the project exists; errors carry the position.
	if got := search(t, f.other, "summary ~ rounding"); len(got) != 0 {
		t.Fatalf("outsider search: %v", got)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/search?q=project%20%3D%20SHOP", nil); r.Status != http.StatusBadRequest {
		t.Fatalf("outsider learns about the project: %d %s", r.Status, r.Body)
	}
	r := f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape("status = Nonexistent"), nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "q:9") {
		t.Fatalf("unknown status: %d %s", r.Status, r.Body)
	}
	r = f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape("project = "), nil)
	if r.Status != http.StatusBadRequest {
		t.Fatalf("syntax error: %d %s", r.Status, r.Body)
	}
}

func TestSecurityLevels(t *testing.T) {
	f := setup(t)
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Confidential", "levels": []map[string]any{
		{"name": "Management", "members": []map[string]any{{"holderType": "user", "holderId": f.ids["lena"]}, {"holderType": "reporter"}}},
	}}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"securitySchemeId": scheme.ID}, nil)
	var schemes []struct {
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	f.admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	level := schemes[0].Levels[0].ID
	secret := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Salary review",
		"fields": map[string]any{"security": level}})
	f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Public task"})
	if r := f.dev.Do(http.MethodGet, "/api/v1/issues/"+secret.Key, nil); r.Status != http.StatusNotFound {
		t.Fatalf("developer sees a restricted issue: %d", r.Status)
	}
	if got := search(t, f.dev, "project = SHOP"); strings.Join(got, ",") != "SHOP-2" {
		t.Fatalf("developer search: %v", got)
	}
	if got := search(t, f.lead, "project = SHOP ORDER BY key"); strings.Join(got, ",") != "SHOP-1,SHOP-2" {
		t.Fatalf("lead search: %v", got)
	}
	// Live updates of the restricted issue reach only those who see it.
	var pid int64
	if err := f.env.App.Pool.QueryRow(context.Background(), "SELECT id FROM issue_projects WHERE key = 'SHOP'").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	container := apitest.Fmt("issue:%d:%d", pid, secret.ID)
	if f.env.App.Issues.IssueVisibility(context.Background(), f.ids["dirk"], nil, container) {
		t.Error("the developer gets live updates of the restricted issue")
	}
	if !f.env.App.Issues.IssueVisibility(context.Background(), f.ids["lena"], nil, container) {
		t.Error("the lead does not get live updates of the restricted issue")
	}
	// Developers cannot set a level they do not belong to.
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"),
		"summary": "x", "fields": map[string]any{"security": level}}); r.Status < 400 {
		t.Fatalf("developer set a foreign level: %d", r.Status)
	}
}

// TestSecurityLevelChoices: the create form and the issue view offer the levels the caller may set — those whose
// members include them — and name the default level of new issues.
func TestSecurityLevelChoices(t *testing.T) {
	f := setup(t)
	type choices struct {
		CanSet  bool `json:"canSet"`
		Default *struct {
			Name string `json:"name"`
		} `json:"default"`
		Levels []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"levels"`
	}
	var none choices
	f.lead.Get("/api/v1/projects/SHOP/security-levels", &none)
	if !none.CanSet || none.Default != nil || len(none.Levels) != 0 {
		t.Fatalf("without a scheme: %+v", none)
	}

	var group struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "Board members"}, &group)
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Confidential", "defaultLevelId": -1,
		"levels": []map[string]any{
			{"id": -1, "name": "Management", "description": "Leads only",
				"members": []map[string]any{{"holderType": "user", "holderId": f.ids["lena"]}}},
			{"id": -2, "name": "Board", "members": []map[string]any{{"holderType": "group", "holderId": group.ID}}},
		}}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"securitySchemeId": scheme.ID}, nil)

	var lead choices
	f.lead.Get("/api/v1/projects/SHOP/security-levels", &lead)
	if !lead.CanSet || lead.Default == nil || lead.Default.Name != "Management" || len(lead.Levels) != 1 ||
		lead.Levels[0].Name != "Management" {
		t.Errorf("the lead's choices: %+v", lead)
	}
	// What is offered can be set.
	f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Budget",
		"fields": map[string]any{"security": lead.Levels[0].ID}})

	var dev choices
	f.dev.Get("/api/v1/projects/SHOP/security-levels", &dev)
	if dev.CanSet || len(dev.Levels) != 0 || dev.Default == nil {
		t.Errorf("the developer's choices: %+v", dev)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/projects/SHOP/security-levels", nil); r.Status != http.StatusNotFound {
		t.Errorf("someone without access: %d %s", r.Status, r.Body)
	}
}

func TestCommentsWorklogsWatchersVotesAttachments(t *testing.T) {
	f := setup(t)
	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Prepare release",
		"fields": map[string]any{"originalEstimate": "1d"}})
	if i.RemainingEstimate == nil || *i.RemainingEstimate != 8*3600 {
		t.Fatalf("estimate: %v", i.RemainingEstimate)
	}
	f.dev.Post("/api/v1/issues/"+i.Key+"/worklogs", map[string]any{"timeSpent": "2h 30m", "comment": "drafting"}, nil)
	f.dev.Get("/api/v1/issues/"+i.Key, &i)
	if i.TimeSpent != 9000 || *i.RemainingEstimate != 8*3600-9000 {
		t.Fatalf("after work log: spent %d remaining %d", i.TimeSpent, *i.RemainingEstimate)
	}

	// Comments restricted to the Developers role are hidden from users.
	f.dev.Post("/api/v1/issues/"+i.Key+"/comments", map[string]any{"markdown": "internal note", "visibilityType": "role",
		"visibilityId": f.cfg.Roles[1].ID}, nil)
	f.user.Post("/api/v1/issues/"+i.Key+"/comments", map[string]any{"markdown": "public question"}, nil)
	var comments []struct {
		ID      int64 `json:"id"`
		CanEdit bool  `json:"canEdit"`
	}
	f.user.Get("/api/v1/issues/"+i.Key+"/comments", &comments)
	if len(comments) != 1 || !comments[0].CanEdit {
		t.Fatalf("user comments: %+v", comments)
	}
	f.dev.Get("/api/v1/issues/"+i.Key+"/comments", &comments)
	if len(comments) != 2 {
		t.Fatalf("developer comments: %d", len(comments))
	}
	// The restricted comment is not searchable, editable or deletable for those who do not see it.
	if got := search(t, f.user, "comment ~ internal"); len(got) != 0 {
		t.Errorf("a restricted comment is searchable: %v", got)
	}
	if got := search(t, f.dev, "comment ~ question"); len(got) != 1 {
		t.Errorf("a public comment is not searchable: %v", got)
	}
	restricted := comments[0].ID
	if r := f.user.Do(http.MethodPut, "/api/v1/issues/"+i.Key+"/comments/"+fmtInt(restricted), map[string]any{"markdown": "changed"}); r.Status != http.StatusNotFound {
		t.Errorf("edit of a hidden comment: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodDelete, "/api/v1/issues/"+i.Key+"/comments/"+fmtInt(comments[1].ID), nil); r.Status != http.StatusForbidden {
		t.Fatalf("developer deleted someone else's comment: %d", r.Status)
	}
	if r := f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape("comment ~ question"), nil); !strings.Contains(string(r.Body), i.Key) {
		t.Fatalf("comment search: %s", r.Body)
	}
	// A comment of a formula alone is not empty, whether a document or Markdown; search finds its LaTeX.
	f.user.MustDo(http.MethodPost, "/api/v1/issues/"+i.Key+"/comments", map[string]any{"body": map[string]any{"type": "doc",
		"content": []any{map[string]any{"type": "math", "attrs": map[string]any{"latex": `\sqrt{2}`}}}}}, nil)
	f.user.MustDo(http.MethodPost, "/api/v1/issues/"+i.Key+"/comments", map[string]any{"markdown": "$\\frac{a}{b}$"}, nil)
	if r := f.user.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/comments", map[string]any{"body": map[string]any{"type": "doc",
		"content": []any{map[string]any{"type": "math", "attrs": map[string]any{"latex": " "}}}}}); r.Code() != "comment.body" {
		t.Errorf("a comment of an empty formula: %d %s", r.Status, r.Body)
	}
	for _, q := range []string{"comment ~ sqrt", "comment ~ frac"} {
		if got := search(t, f.dev, q); len(got) != 1 {
			t.Errorf("%s: %v", q, got)
		}
	}

	// Watching and voting.
	f.user.MustDo(http.MethodPut, "/api/v1/issues/"+i.Key+"/watchers", nil, nil)
	f.user.MustDo(http.MethodPut, "/api/v1/issues/"+i.Key+"/vote", nil, nil)
	f.user.Get("/api/v1/issues/"+i.Key, &i)
	if !i.Watching || i.Votes != 1 {
		t.Fatalf("watching %v votes %d", i.Watching, i.Votes)
	}
	if r := f.dev.Do(http.MethodPut, "/api/v1/issues/"+i.Key+"/vote", nil); r.Code() != "vote.own" {
		t.Fatalf("reporter voted: %s", r.Body)
	}

	// Attachments: upload, download with safe headers, delete.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../../etc/notes.txt")
	_, _ = io.WriteString(fw, "hello attachment")
	_ = mw.Close()
	r := f.dev.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/attachments", &buf, "Content-Type", mw.FormDataContentType())
	if r.Status != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
	f.dev.Get("/api/v1/issues/"+i.Key, &i)
	if len(i.Attachments) != 1 || i.Attachments[0].Filename != "notes.txt" {
		t.Fatalf("attachments: %+v", i.Attachments)
	}
	d := f.user.Do(http.MethodGet, i.Attachments[0].URL, nil)
	if string(d.Body) != "hello attachment" || !strings.Contains(d.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("download: %s %v", d.Body, d.Header)
	}
	if r := f.other.Do(http.MethodGet, i.Attachments[0].URL, nil); r.Status != http.StatusNotFound {
		t.Fatalf("outsider downloaded: %d", r.Status)
	}
	// Who may delete an attachment is told with it: its author, not another user.
	if !i.Attachments[0].CanDelete {
		t.Error("the author may not delete the attachment")
	}
	var seen issue
	f.user.Get("/api/v1/issues/"+i.Key, &seen)
	if seen.Attachments[0].CanDelete {
		t.Error("another user may delete the attachment")
	}
	if r := f.user.Do(http.MethodDelete, "/api/v1/issue-attachments/"+fmtInt(seen.Attachments[0].ID), nil); r.Status != http.StatusForbidden {
		t.Errorf("another user deleted the attachment: %d", r.Status)
	}
}

func TestFiltersAndSharing(t *testing.T) {
	f := setup(t)
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("bug"), "summary": "Broken cart"})
	var group struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "QA"}, &group)
	f.admin.MustDo(http.MethodPut, "/api/v1/admin/groups/"+fmtInt(group.ID)+"/members/"+fmtInt(f.ids["ute"]), nil, nil)
	var filter struct {
		ID int64 `json:"id"`
	}
	f.dev.Post("/api/v1/filters", map[string]any{"name": "Open bugs", "query": "project = SHOP AND issuetype = " + fmtInt(f.cfg.typ("bug")),
		"shares": []map[string]any{{"type": "group", "id": group.ID}}}, &filter)
	user := f.env.Login("ute", apitest.UserPassword) // new session with group membership
	if got := search(t, user, `filter = "Open bugs"`); strings.Join(got, ",") != "SHOP-1" {
		t.Fatalf("shared filter: %v", got)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/filters/"+fmtInt(filter.ID), nil); r.Status != http.StatusNotFound {
		t.Fatalf("unshared filter visible: %d", r.Status)
	}
	if r := user.Do(http.MethodPut, "/api/v1/filters/"+fmtInt(filter.ID), map[string]any{"name": "Mine", "query": "project = SHOP"}); r.Status != http.StatusForbidden {
		t.Fatalf("non-owner changed the filter: %d", r.Status)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/filters", map[string]any{"name": "Broken", "query": "project = "}); r.Status != http.StatusBadRequest {
		t.Fatalf("invalid filter saved: %d", r.Status)
	}
}

func TestProjectKeyChangeKeepsOldKeys(t *testing.T) {
	f := setup(t)
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Old key"})
	f.lead.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"key": "STORE"}, nil)
	var i issue
	f.dev.Get("/api/v1/issues/SHOP-1", &i)
	if i.Key != "STORE-1" {
		t.Fatalf("old key resolves to %q", i.Key)
	}
	f.dev.Get("/api/v1/projects/SHOP", nil)
	n := f.create(t, f.dev, map[string]any{"project": "STORE", "typeId": f.cfg.typ("task"), "summary": "New key"})
	if n.Key != "STORE-2" {
		t.Fatalf("numbering after rename: %s", n.Key)
	}
}

func TestConfigOrderAndValidation(t *testing.T) {
	f := setup(t)
	var ids []int64
	for i := len(f.cfg.Priorities) - 1; i >= 0; i-- {
		ids = append(ids, f.cfg.Priorities[i].ID)
	}
	if r := f.lead.Do(http.MethodPut, "/api/v1/issue-config/priorities-order", ids); r.Status != http.StatusForbidden {
		t.Fatalf("project admin reordered global priorities: %d", r.Status)
	}
	f.admin.MustDo(http.MethodPut, "/api/v1/issue-config/priorities-order", ids, nil)
	var cfg config
	f.admin.Get("/api/v1/issue-config", &cfg)
	for i, p := range cfg.Priorities {
		if p.ID != ids[i] {
			t.Fatalf("priority order after reordering: %+v, want ids %v", cfg.Priorities, ids)
		}
	}
	// Clients format durations with the instance's units.
	var units struct {
		TimeTracking struct {
			HoursPerDay int `json:"hoursPerDay"`
			DaysPerWeek int `json:"daysPerWeek"`
		} `json:"timeTracking"`
	}
	f.user.Get("/api/v1/issue-config", &units)
	if units.TimeTracking.HoursPerDay != 8 || units.TimeTracking.DaysPerWeek != 5 {
		t.Errorf("time tracking units = %+v", units.TimeTracking)
	}
	if r := f.admin.Do(http.MethodPost, "/api/v1/issue-config/statuses", map[string]any{"name": "Waiting", "category": "later"}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("status with an unknown category: %d", r.Status)
	}
	var saved struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/statuses", map[string]any{"name": "Waiting", "category": "inprogress"}, &saved)
	if saved.ID == 0 {
		t.Fatal("status was not created")
	}
	// A workflow transition accepts "from" as "any" or as a list of status ids.
	open, done := f.cfg.status("todo")[0], f.cfg.status("done")[0]
	f.admin.Post("/api/v1/issue-config/workflows", map[string]any{"name": "Tiny", "definition": map[string]any{
		"statuses": []int64{open, saved.ID, done}, "initial": open, "options": map[string]any{"autoResolution": true},
		"transitions": []map[string]any{
			{"id": 1, "name": "Wait", "from": []int64{open}, "to": saved.ID},
			{"id": 2, "name": "Finish", "from": "any", "to": done},
		},
	}}, &saved)
	var workflows []struct {
		Name       string `json:"name"`
		Definition struct {
			Transitions []struct {
				From any `json:"from"`
			} `json:"transitions"`
		} `json:"definition"`
	}
	f.admin.Get("/api/v1/issue-config/workflows", &workflows)
	found := false
	for _, w := range workflows {
		if w.Name == "Tiny" {
			found = true
			if len(w.Definition.Transitions) != 2 || w.Definition.Transitions[1].From != "any" {
				t.Fatalf("workflow round trip: %+v", w.Definition)
			}
		}
	}
	if !found {
		t.Fatal("workflow Tiny missing")
	}
}

// TestVersionsOfOtherProjects: deleting a version moves its issues only between versions of the same project.
func TestVersionsOfOtherProjects(t *testing.T) {
	f := setup(t)
	f.admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SEC", "name": "Secure", "template": "kanban"}, nil)
	var secVersion, shopVersion struct {
		ID int64 `json:"id"`
	}
	f.admin.MustDo(http.MethodPost, "/api/v1/projects/SEC/versions", map[string]any{"name": "1.0"}, &secVersion)
	f.lead.MustDo(http.MethodPost, "/api/v1/projects/SHOP/versions", map[string]any{"name": "2.0"}, &shopVersion)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	f.admin.Get("/api/v1/projects/SEC/issue-types", &types)
	var secret issue
	f.admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SEC", "typeId": types[0].ID, "summary": "Hidden",
		"fields": map[string]any{"fixVersions": []int64{secVersion.ID}}}, &secret)

	// The administrator of SHOP names a version of SEC.
	for _, path := range []string{
		apitest.Fmt("/api/v1/projects/SHOP/versions/%d?moveTo=%d", secVersion.ID, shopVersion.ID),
		apitest.Fmt("/api/v1/projects/SHOP/versions/%d?moveTo=%d", shopVersion.ID, secVersion.ID),
	} {
		if r := f.lead.Do(http.MethodDelete, path, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	var ids []int64
	if err := f.env.App.Pool.QueryRow(context.Background(), `SELECT coalesce(array_agg(version_id ORDER BY version_id), '{}')
		FROM issue_versions v JOIN issues i ON i.id = v.issue_id WHERE i.key = $1`, secret.Key).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != secVersion.ID {
		t.Errorf("versions of %s = %v", secret.Key, ids)
	}
}

// TestCustomFieldReferences: user, multiuser and group fields come with the names of the people and groups they
// refer to, so that clients can show them; updates return them too.
func TestCustomFieldReferences(t *testing.T) {
	f := setup(t)
	field := func(name, typ string) string {
		t.Helper()
		var saved struct {
			ID int64 `json:"id"`
		}
		f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": name, "type": typ}, &saved)
		return "cf_" + itoa(saved.ID)
	}
	reviewer, testers, team := field("Reviewer", "user"), field("Testers", "multiuser"), field("Team", "group")
	var group struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "payments", "description": "Payments team"}, &group)
	is := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Refunds",
		"fields": map[string]any{reviewer: f.ids["dirk"], testers: []int64{f.ids["ute"], f.ids["lena"]}, team: group.ID}})
	type refs struct {
		FieldUsers []struct {
			ID          int64  `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"fieldUsers"`
		FieldGroups []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"fieldGroups"`
	}
	names := func(r refs) string {
		var out []string
		for _, u := range r.FieldUsers {
			out = append(out, u.DisplayName)
		}
		for _, g := range r.FieldGroups {
			out = append(out, "group:"+g.Name)
		}
		return strings.Join(out, ",")
	}
	var got refs
	f.user.Get("/api/v1/issues/"+is.Key, &got)
	if n := names(got); n != "Lena,Dirk,Ute,group:payments" {
		t.Fatalf("references: %s", n)
	}
	r := f.dev.Do(http.MethodPatch, "/api/v1/issues/"+is.Key, map[string]any{"fields": map[string]any{reviewer: f.ids["ute"],
		testers: []int64{}, team: nil}})
	if r.Status != http.StatusOK {
		t.Fatalf("update: %d %s", r.Status, r.Body)
	}
	got = refs{}
	r.Decode(&got)
	if n := names(got); n != "Ute" {
		t.Fatalf("references after the update: %s", n)
	}
}

// TestSecurityLevelDefaultsAndSubtasks: new issues get the default level of the security scheme; sub-tasks always
// have the level of their parent — on creation, when the parent's level changes and when they move — and deleting
// an issue needs every sub-task (audit H1, H2).
func TestSecurityLevelDefaultsAndSubtasks(t *testing.T) {
	f := setup(t)
	both := []map[string]any{{"holderType": "user", "holderId": f.ids["lena"]}, {"holderType": "user", "holderId": f.ids["dirk"]}}
	dirkOnly := []map[string]any{{"holderType": "user", "holderId": f.ids["dirk"]}}
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Confidential", "levels": []map[string]any{
		{"name": "Board", "members": both}, {"name": "Team", "members": both}, {"name": "Dirk", "members": dirkOnly}}}, &scheme)
	var schemes []struct {
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	f.admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	board, team, dirk := schemes[0].Levels[0].ID, schemes[0].Levels[1].ID, schemes[0].Levels[2].ID
	f.admin.MustDo(http.MethodPut, "/api/v1/issue-config/security-schemes/"+itoa(scheme.ID), map[string]any{
		"name": "Confidential", "defaultLevelId": board, "levels": []map[string]any{
			{"id": board, "name": "Board", "members": both}, {"id": team, "name": "Team", "members": both},
			{"id": dirk, "name": "Dirk", "members": dirkOnly}}}, nil)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"securitySchemeId": scheme.ID}, nil)
	visible := func(c *apitest.Client, key string) bool {
		t.Helper()
		r := c.Do(http.MethodGet, "/api/v1/issues/"+key, nil)
		if r.Status != http.StatusOK && r.Status != http.StatusNotFound {
			t.Fatalf("GET %s: %d %s", key, r.Status, r.Body)
		}
		return r.Status == http.StatusOK
	}
	patch := func(c *apitest.Client, key string, fields map[string]any) *apitest.Response {
		t.Helper()
		return c.Do(http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"fields": fields})
	}

	// An issue created without a level gets the default one: ute (role Users, in no level) does not see it.
	story := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Acquisition of Foo GmbH"})
	if visible(f.user, story.Key) || !visible(f.dev, story.Key) {
		t.Fatal("the default level was not applied")
	}
	// Choosing no level explicitly is respected.
	public := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Office move",
		"fields": map[string]any{"security": nil}})
	if !visible(f.user, public.Key) {
		t.Fatal("an explicitly public issue is hidden")
	}

	// Sub-tasks take the level of their parent and cannot have one of their own.
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Due diligence",
		"parent": story.Key})
	if visible(f.user, sub.Key) {
		t.Fatal("the sub-task of a secured issue is visible")
	}
	if r := patch(f.lead, sub.Key, map[string]any{"security": nil}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a sub-task got a level of its own: %d %s", r.Status, r.Body)
	}
	if r := f.lead.Do(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"),
		"summary": "x", "parent": story.Key, "fields": map[string]any{"security": nil}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a new sub-task got a level of its own: %d %s", r.Status, r.Body)
	}

	// When the parent's level changes, the sub-tasks follow, with an entry in their history.
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+story.Key, map[string]any{"fields": map[string]any{"security": nil}}, nil)
	if !visible(f.user, story.Key) || !visible(f.user, sub.Key) {
		t.Fatal("removing the parent's level did not reach the sub-task")
	}
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+story.Key, map[string]any{"fields": map[string]any{"security": team}}, nil)
	if visible(f.user, story.Key) || visible(f.user, sub.Key) {
		t.Fatal("securing the parent did not reach the sub-task")
	}
	var history []struct {
		Items []struct {
			Field    string `json:"field"`
			ToString string `json:"toString"`
		} `json:"items"`
	}
	f.lead.Get("/api/v1/issues/"+sub.Key+"/history", &history)
	if n := len(history); n == 0 || history[n-1].Items[0].Field != "security" || history[n-1].Items[0].ToString != "Team" {
		t.Fatalf("history of the sub-task: %+v", history)
	}

	// A sub-task moved to another parent takes on that parent's level.
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+sub.Key, map[string]any{"fields": map[string]any{"parent": public.Key}}, nil)
	if !visible(f.user, sub.Key) {
		t.Fatal("the moved sub-task kept the level of its old parent")
	}

	// Deleting an issue deletes its sub-tasks, so it needs every one of them. A sub-task with another level (data from
	// before this rule) that lena cannot see blocks her.
	hidden := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Old data",
		"parent": public.Key})
	if _, err := f.env.App.Pool.Exec(context.Background(), "UPDATE issues SET security_level_id = $1 WHERE key = $2", dirk, hidden.Key); err != nil {
		t.Fatal(err)
	}
	if r := f.lead.Do(http.MethodDelete, "/api/v1/issues/"+public.Key, nil); r.Status != http.StatusForbidden || r.Code() != "issue.delete-subtasks" {
		t.Fatalf("deleted an issue with a hidden sub-task: %d %s", r.Status, r.Body)
	}
	if !visible(f.dev, hidden.Key) {
		t.Fatal("the hidden sub-task is gone")
	}
	// Once she sees it, the issue goes with all its sub-tasks.
	if _, err := f.env.App.Pool.Exec(context.Background(), "UPDATE issues SET security_level_id = NULL WHERE key = $1", hidden.Key); err != nil {
		t.Fatal(err)
	}
	f.lead.MustDo(http.MethodDelete, "/api/v1/issues/"+public.Key, nil, nil)
	for _, key := range []string{public.Key, sub.Key, hidden.Key} {
		if visible(f.lead, key) || visible(f.dev, key) {
			t.Errorf("%s is still there", key)
		}
	}
}

// TestViewDevelopment: project.view-development decides who sees branches, commits, builds and deployments of
// issues and versions (audit L38); who cannot see the project learns nothing about it.
func TestViewDevelopment(t *testing.T) {
	f := setup(t)
	i := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Checkout"})
	var version struct {
		ID int64 `json:"id"`
	}
	f.lead.Post("/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &version)
	// A scheme in which the role Users may only browse.
	grants := []map[string]any{{"permission": "project.browse", "holderType": "role", "holderId": f.cfg.Roles[2].ID}}
	for _, perm := range []string{"project.browse", "project.admin", "project.view-development"} {
		grants = append(grants, map[string]any{"permission": perm, "holderType": "role", "holderId": f.cfg.Roles[0].ID})
	}
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/permission-schemes", map[string]any{"name": "Browse only", "grants": grants}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"permissionSchemeId": scheme.ID}, nil)

	var got issue
	f.user.Get("/api/v1/issues/"+i.Key, &got)
	if got.Permissions["development"] {
		t.Errorf("the issue offers its development to a user without project.view-development")
	}
	versionPath := "/api/v1/projects/SHOP/versions/" + itoa(version.ID) + "/deployments"
	for _, path := range []string{"/api/v1/issues/" + i.Key + "/development", versionPath} {
		if r := f.user.Do(http.MethodGet, path, nil); r.Status != http.StatusForbidden || r.Code() != "development.denied" {
			t.Errorf("%s without project.view-development: %d %s", path, r.Status, r.Body)
		}
		if r := f.other.Do(http.MethodGet, path, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s outside of the project: %d %s", path, r.Status, r.Body)
		}
		f.lead.MustDo(http.MethodGet, path, nil, nil)
	}
	f.lead.Get("/api/v1/issues/"+i.Key, &got)
	if !got.Permissions["development"] {
		t.Errorf("the lead does not see the development")
	}
}

// TestHiddenIssuesStayHidden: issues one may not see are not named by the history of issues one sees, nor as
// parents in search results, issue views and boards (audit L7, L14).
func TestHiddenIssuesStayHidden(t *testing.T) {
	f := setup(t)
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/security-schemes", map[string]any{"name": "Confidential", "levels": []map[string]any{
		{"name": "Management", "members": []map[string]any{{"holderType": "user", "holderId": f.ids["lena"]},
			{"holderType": "assignee"}}},
	}}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"securitySchemeId": scheme.ID}, nil)
	var schemes []struct {
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	f.admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	level := schemes[0].Levels[0].ID
	secret := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Layoffs",
		"fields": map[string]any{"security": level}})
	public := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Office move"})
	f.lead.MustDo(http.MethodPost, "/api/v1/issues/"+public.Key+"/links", map[string]any{"issue": secret.Key,
		"typeId": f.cfg.LinkTypes[0].ID}, nil)
	// Dirk works on a sub-task of the hidden story: he sees the sub-task (as its assignee), not its parent.
	sub := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Pack the boxes",
		"parent": secret.Key, "fields": map[string]any{"assignee": f.ids["dirk"]}})

	history := func(c *apitest.Client, key string) string {
		r := c.MustDo(http.MethodGet, "/api/v1/issues/"+key+"/history", nil, nil)
		return string(r.Body)
	}
	if h := history(f.lead, public.Key); !strings.Contains(h, secret.Key) {
		t.Fatalf("the lead's history lacks the link: %s", h)
	}
	if h := history(f.dev, public.Key); strings.Contains(h, secret.Key) {
		t.Errorf("the history names a hidden issue: %s", h)
	}
	var view struct {
		Key    string `json:"key"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
	}
	f.dev.Get("/api/v1/issues/"+sub.Key, &view)
	if view.Key != sub.Key || view.Parent != nil {
		t.Errorf("the issue view names its hidden parent: %+v", view)
	}
	r := f.dev.MustDo(http.MethodGet, "/api/v1/search?q="+url.QueryEscape("project = SHOP"), nil, nil)
	if !strings.Contains(string(r.Body), sub.Key) || strings.Contains(string(r.Body), secret.Key) {
		t.Errorf("search: %s", r.Body)
	}
	boards := items[ref](t, f.lead, "/api/v1/boards?project=SHOP")
	for _, b := range boards {
		r := f.dev.MustDo(http.MethodGet, "/api/v1/boards/"+itoa(b.ID)+"/backlog", nil, nil)
		if strings.Contains(string(r.Body), secret.Key) {
			t.Errorf("the backlog of board %d names the hidden parent: %s", b.ID, r.Body)
		}
	}
}

// TestQueriesTellNothingAboutHiddenProjects: KQL answers names of hidden issues, components, sprints, versions and
// projects like names that do not exist, shows watchers only with watchers.view and users only with user.browse
// (audit L8, L9, L18).
func TestQueriesTellNothingAboutHiddenProjects(t *testing.T) {
	f := setup(t)
	f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("story"), "summary": "Checkout"})
	f.lead.Post("/api/v1/projects/SHOP/components", map[string]any{"name": "Payments"}, nil)
	boards := items[ref](t, f.lead, "/api/v1/boards?project=SHOP")
	f.lead.Post("/api/v1/boards/"+itoa(boards[0].ID)+"/sprints", map[string]any{"name": "Launch"}, nil)
	f.lead.MustDo(http.MethodPut, "/api/v1/issues/SHOP-1/watchers", nil, nil)

	answer := func(c *apitest.Client, q string) (int, string) {
		r := c.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil)
		var p struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(r.Body, &p)
		return r.Status, strings.ReplaceAll(strings.ReplaceAll(p.Detail, "SHOP", "X"), "NOPE", "X")
	}
	// Otto, who sees nothing of SHOP, gets the same answer for its names as for names that do not exist.
	for _, pair := range [][2]string{
		{"key in linkedIssues(SHOP-1)", "key in linkedIssues(NOPE-1)"},
		{"key in childIssuesOf(SHOP-1)", "key in childIssuesOf(NOPE-1)"},
		{"parent = SHOP-1", "parent = NOPE-1"},
		{"key > SHOP-1", "key > NOPE-1"},
		{"component = Payments", "component = Nothing"},
		{"sprint = Launch", "sprint = Nothing"},
		{"fixVersion in releasedVersions(SHOP)", "fixVersion in releasedVersions(NOPE)"},
	} {
		s1, d1 := answer(f.other, pair[0])
		s2, d2 := answer(f.other, pair[1])
		if d1 = strings.NewReplacer("Payments", "Nothing", "Launch", "Nothing").Replace(d1); s1 != s2 || d1 != d2 {
			t.Errorf("%q answers %d %q, %q answers %d %q", pair[0], s1, d1, pair[1], s2, d2)
		}
	}
	if got := search(t, f.lead, "sprint = Launch OR component = Payments OR key in linkedIssues(SHOP-1)"); got == nil {
		t.Errorf("the lead's query failed")
	}

	// Watchers: only with watchers.view, except one's own.
	grants := []map[string]any{{"permission": "project.browse", "holderType": "role", "holderId": f.cfg.Roles[2].ID}}
	for _, perm := range []string{"project.browse", "project.admin", "watchers.view", "watchers.manage"} {
		grants = append(grants, map[string]any{"permission": perm, "holderType": "role", "holderId": f.cfg.Roles[0].ID})
	}
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/permission-schemes", map[string]any{"name": "Browse only", "grants": grants}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"permissionSchemeId": scheme.ID}, nil)
	if got := search(t, f.user, "watcher = lena"); len(got) != 0 {
		t.Errorf("a user without watchers.view learns who watches: %v", got)
	}
	if got := search(t, f.lead, "watcher = lena"); strings.Join(got, ",") != "SHOP-1" {
		t.Errorf("the lead's watcher query: %v", got)
	}
	f.user.MustDo(http.MethodPut, "/api/v1/issues/SHOP-1/watchers", nil, nil)
	if got := search(t, f.user, "watcher = currentUser()"); strings.Join(got, ",") != "SHOP-1" {
		t.Errorf("one's own watching: %v", got)
	}

	// Naming other users needs user.browse; currentUser() does not.
	f.admin.MustDo(http.MethodDelete, "/api/v1/admin/global-permissions?permission=user.browse&principalType=logged-in", nil, nil)
	if status, detail := answer(f.user, "assignee = lena"); status != http.StatusBadRequest || !strings.Contains(detail, "browse users") {
		t.Errorf("a user without user.browse names others: %d %s", status, detail)
	}
	if status, _ := answer(f.user, "assignee = currentUser()"); status != http.StatusOK {
		t.Errorf("currentUser() without user.browse: %d", status)
	}
}

// TestPlanningAndAssignmentRules: transition comments need comment.add (audit L10), the assignee picker is for
// who assigns and finds people by e-mail only with user.browse (L13), owners of boards of archived projects cannot
// change them (L15), and sprints of a board are planned only by who schedules in its project (L16).
func TestPlanningAndAssignmentRules(t *testing.T) {
	f := setup(t)
	i := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Invoice"})

	// The assignee picker.
	if r := f.user.Do(http.MethodGet, "/api/v1/projects/SHOP/assignable", nil); r.Status != http.StatusForbidden {
		t.Errorf("a user who cannot assign uses the picker: %d", r.Status)
	}
	pick := func(q string) int {
		var users []struct {
			Username string `json:"username"`
		}
		f.dev.Get("/api/v1/projects/SHOP/assignable?query="+url.QueryEscape(q), &users)
		return len(users)
	}
	if pick("dirk@example") != 1 || pick("Dirk") != 1 {
		t.Errorf("the picker does not find dirk")
	}
	f.admin.MustDo(http.MethodDelete, "/api/v1/admin/global-permissions?permission=user.browse&principalType=logged-in", nil, nil)
	if pick("dirk@example") != 0 || pick("Dirk") != 1 {
		t.Errorf("without user.browse, the picker finds people by e-mail")
	}

	// Sprints: Ute schedules in OPS, not in SHOP; she cannot plan her OPS issue into a SHOP sprint.
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	ops := f.create(t, f.lead, map[string]any{"project": "OPS", "typeId": f.cfg.typ("task"), "summary": "Backup"})
	boards := items[ref](t, f.lead, "/api/v1/boards?project=SHOP")
	var sprint struct {
		ID int64 `json:"id"`
	}
	f.lead.Post("/api/v1/boards/"+itoa(boards[0].ID)+"/sprints", map[string]any{"name": "Launch"}, &sprint)
	if r := f.user.Do(http.MethodPost, "/api/v1/sprints/"+itoa(sprint.ID)+"/issues", map[string]any{"issues": []string{ops.Key}}); r.Code() != "sprint.schedule-denied" {
		t.Errorf("an issue of another project went into the sprint: %d %s", r.Status, r.Body)
	}
	f.lead.MustDo(http.MethodPost, "/api/v1/sprints/"+itoa(sprint.ID)+"/issues", map[string]any{"issues": []string{ops.Key}}, nil)
	if r := f.user.Do(http.MethodPost, "/api/v1/backlog/issues", map[string]any{"issues": []string{ops.Key}}); r.Code() != "sprint.schedule-denied" {
		t.Errorf("an issue was taken out of another project's sprint: %d %s", r.Status, r.Body)
	}

	// Transition comments: a scheme in which Users transition but do not comment.
	grants := []map[string]any{}
	for _, perm := range []string{"project.browse", "issue.transition"} {
		grants = append(grants, map[string]any{"permission": perm, "holderType": "role", "holderId": f.cfg.Roles[2].ID})
	}
	for _, perm := range []string{"project.browse", "project.admin"} {
		grants = append(grants, map[string]any{"permission": perm, "holderType": "role", "holderId": f.cfg.Roles[0].ID})
	}
	var scheme struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/permission-schemes", map[string]any{"name": "No comments", "grants": grants}, &scheme)
	f.admin.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"permissionSchemeId": scheme.ID}, nil)
	var transitions []struct {
		ID int64 `json:"id"`
	}
	f.user.Get("/api/v1/issues/"+i.Key+"/transitions", &transitions)
	if len(transitions) == 0 {
		t.Fatal("no transitions")
	}
	r := f.user.Do(http.MethodPost, "/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": transitions[0].ID,
		"comment": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "Done"}}}}}})
	if r.Code() != "comment.denied" {
		t.Errorf("a transition commented without comment.add: %d %s", r.Status, r.Body)
	}
	f.user.MustDo(http.MethodPost, "/api/v1/issues/"+i.Key+"/transitions", map[string]any{"transitionId": transitions[0].ID}, nil)

	// Boards of archived projects are read-only, also for their owners.
	f.lead.MustDo(http.MethodPatch, "/api/v1/projects/SHOP", map[string]any{"archived": true}, nil)
	if r := f.lead.Do(http.MethodPut, "/api/v1/boards/"+itoa(boards[0].ID), map[string]any{"name": "Changed", "project": "SHOP",
		"type": "scrum"}); r.Status != http.StatusForbidden {
		t.Errorf("the owner changed a board of an archived project: %d %s", r.Status, r.Body)
	}
}
