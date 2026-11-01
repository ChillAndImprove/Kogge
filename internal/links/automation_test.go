// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

func TestAutomation(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Checkout"}, &issue)
	var statuses []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category"`
	}
	admin.Get("/api/v1/projects/SHOP/statuses", &statuses)
	var inProgress, done int64
	for _, s := range statuses {
		if s.Category == "inprogress" && inProgress == 0 {
			inProgress = s.ID
		}
		if s.Category == "done" && done == 0 {
			done = s.ID
		}
	}
	if inProgress == 0 || done == 0 {
		t.Fatalf("statuses = %+v", statuses)
	}
	rule := func(name, trigger, action string, params map[string]any) {
		admin.MustDo(http.MethodPost, "/api/v1/projects/SHOP/automation", map[string]any{"name": name, "trigger": trigger, "action": action,
			"params": params, "issueTypes": []int64{}, "statuses": []int64{}, "active": true}, nil)
	}
	rule("Assign the branch creator", "branch.created", "assign", map[string]any{"to": "author"})
	rule("Note commits", "commit.pushed", "comment", map[string]any{})
	rule("Review", "pull-request.opened", "transition", map[string]any{"status": inProgress})
	rule("Done when merged", "pull-request.merged", "transition", map[string]any{"status": done})
	if res := admin.Do(http.MethodPost, "/api/v1/projects/SHOP/automation", map[string]any{"name": "x", "trigger": "pull-request.merged",
		"action": "transition", "params": map[string]any{}, "issueTypes": []int64{}, "statuses": []int64{}, "active": true}); res.Code() != "automation.status" {
		t.Errorf("transition without a status: %d %s", res.Status, res.Body)
	}

	// A branch and a commit naming the issue, made in the browser.
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, nil)
	branch := "feature/" + issue.Key + "-checkout"
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "newBranch": branch,
		"path": "checkout.go", "content": "package shop\n", "message": issue.Key + " Add the checkout"}, nil)
	env.WorkJobs()
	var got struct {
		Status struct {
			ID int64 `json:"id"`
		} `json:"status"`
		Assignee *struct {
			Username string `json:"username"`
		} `json:"assignee"`
	}
	admin.Get("/api/v1/issues/"+issue.Key, &got)
	if got.Assignee == nil || got.Assignee.Username != "admin" {
		t.Errorf("assignee = %+v", got.Assignee)
	}
	var comments []struct {
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		Body any `json:"body"`
	}
	admin.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 || comments[0].Author.Username != "kogge" {
		t.Errorf("comments = %+v", comments)
	}

	// The pull request moves the issue along.
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/shop/pull-requests", map[string]any{"title": "Checkout", "source": branch}, nil)
	env.WorkJobs()
	admin.Get("/api/v1/issues/"+issue.Key, &got)
	if got.Status.ID != inProgress {
		t.Errorf("after opening the pull request: status %d, want %d", got.Status.ID, inProgress)
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/shop/pull-requests/1/merge", map[string]any{"strategy": "merge"}, nil)
	env.WorkJobs()
	admin.Get("/api/v1/issues/"+issue.Key, &got)
	if got.Status.ID != done {
		t.Errorf("after the merge: status %d, want %d", got.Status.ID, done)
	}

	var rules []struct {
		Name     string `json:"name"`
		LastRuns []struct {
			IssueKey string `json:"issueKey"`
			Result   string `json:"result"`
			Message  string `json:"message"`
		} `json:"lastRuns"`
	}
	admin.Get("/api/v1/projects/SHOP/automation", &rules)
	if len(rules) != 4 {
		t.Fatalf("rules = %+v", rules)
	}
	for _, r := range rules {
		if len(r.LastRuns) == 0 || r.LastRuns[0].Result != "done" || r.LastRuns[0].IssueKey != issue.Key {
			t.Errorf("rule %s ran: %+v", r.Name, r.LastRuns)
		}
	}
	if !strings.Contains(rules[3].LastRuns[0].Message, "was merged") {
		t.Errorf("message = %q", rules[3].LastRuns[0].Message)
	}
	// Only project administrators see the rules.
	dev := env.CreateUser("dev")
	if res := dev.Do(http.MethodGet, "/api/v1/projects/SHOP/automation", nil); res.Status != http.StatusForbidden && res.Status != http.StatusNotFound {
		t.Errorf("rules for a user: %d", res.Status)
	}
}

// TestAutomationNeedsAccess: rules act with their owner's access and only for people who may see the issue — an
// outsider naming an issue key in a repository of their own cannot drive the issue (audit H4); comments name no
// repository or title (audit M15).
func TestAutomationNeedsAccess(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	lead, dev, eve := env.CreateUser("lead"), env.CreateUser("dev"), env.CreateUser("eve")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"lead": lead, "dev": dev, "eve": eve} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	for _, g := range []struct {
		perm, user string
	}{{"project.create", "lead"}, {"codeproject.create", "lead"}, {"codeproject.create", "eve"}} {
		admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": g.perm, "principalType": "user",
			"principalId": ids[g.user]}, nil)
	}
	lead.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "AXP", "name": "Payments", "template": "kanban"}, nil)
	var cfg struct {
		Roles []struct {
			ID int64 `json:"id"`
		} `json:"roles"`
	}
	lead.Get("/api/v1/issue-config", &cfg)
	lead.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/AXP/roles/%d/members", cfg.Roles[1].ID), map[string]any{"type": "user",
		"id": ids["dev"]}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	lead.Get("/api/v1/projects/AXP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	lead.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "AXP", "typeId": types[0].ID, "summary": "Refunds"}, &issue)
	var statuses []struct {
		ID       int64  `json:"id"`
		Category string `json:"category"`
	}
	lead.Get("/api/v1/projects/AXP/statuses", &statuses)
	var inProgress int64
	for _, s := range statuses {
		if s.Category == "inprogress" && inProgress == 0 {
			inProgress = s.ID
		}
	}
	lead.MustDo(http.MethodPost, "/api/v1/projects/AXP/automation", map[string]any{"name": "Opened", "trigger": "pull-request.opened",
		"action": "transition", "params": map[string]any{"status": inProgress}, "issueTypes": []int64{}, "statuses": []int64{}, "active": true}, nil)
	lead.MustDo(http.MethodPost, "/api/v1/projects/AXP/automation", map[string]any{"name": "Note", "trigger": "pull-request.opened",
		"action": "comment", "params": map[string]any{}, "issueTypes": []int64{}, "statuses": []int64{}, "active": true}, nil)
	type ruleView struct {
		Name  string `json:"name"`
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
		LastRuns []struct {
			Result  string `json:"result"`
			Message string `json:"message"`
		} `json:"lastRuns"`
	}
	lastRun := func(name string) string {
		t.Helper()
		var rules []ruleView
		lead.Get("/api/v1/projects/AXP/automation", &rules)
		for _, r := range rules {
			if r.Name == name {
				if r.Owner.Username != "lead" {
					t.Fatalf("owner of %s: %+v", name, r.Owner)
				}
				if len(r.LastRuns) == 0 {
					return ""
				}
				return r.LastRuns[0].Result + ": " + r.LastRuns[0].Message
			}
		}
		t.Fatalf("no rule %s", name)
		return ""
	}
	status := func() int64 {
		var got struct {
			Status struct {
				ID int64 `json:"id"`
			} `json:"status"`
		}
		lead.Get("/api/v1/issues/"+issue.Key, &got)
		return got.Status.ID
	}
	openPR := func(c *apitest.Client, project, repo, title string) {
		t.Helper()
		branch := "work-" + strings.ToLower(project)
		c.MustDo(http.MethodPut, "/api/v1/code/repos/"+project+"/"+repo+"/files", map[string]any{"branch": "main", "newBranch": branch,
			"path": "x.txt", "content": "x\n", "message": "Change"}, nil)
		c.MustDo(http.MethodPost, "/api/v1/code/repos/"+project+"/"+repo+"/pull-requests", map[string]any{"title": title, "source": branch}, nil)
		env.WorkJobs()
	}
	before := status()

	// Eve, who cannot see the project, names the issue in a private repository of her own: nothing happens.
	eve.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "EVE", "name": "Eve"}, nil)
	eve.MustDo(http.MethodPost, "/api/v1/code/projects/EVE/repos", map[string]any{"name": "toolbox", "readme": true}, nil)
	openPR(eve, "EVE", "toolbox", issue.Key+" please [re-login](https://phish.example.invalid/login)")
	if got := lastRun("Opened"); got != "skipped: The owner of the rule cannot read the repository the event came from." {
		t.Fatalf("rule after the outsider's pull request: %q", got)
	}
	// In a repository everyone can read, the rule's owner reads it, but Eve still may not see the issue.
	eve.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "OPEN", "name": "Open", "visibility": "internal"}, nil)
	eve.MustDo(http.MethodPost, "/api/v1/code/projects/OPEN/repos", map[string]any{"name": "tools", "readme": true}, nil)
	openPR(eve, "OPEN", "tools", issue.Key+" tidy up")
	if got := lastRun("Opened"); got != "skipped: Who caused the event may not see the issue." {
		t.Fatalf("rule after the outsider's pull request in an open repository: %q", got)
	}
	if status() != before {
		t.Fatal("an outsider moved the issue")
	}

	// A developer of the project, in a repository the owner reads: the rules act; the comment names no repository.
	lead.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PAY", "name": "Payments"}, nil)
	var grants []map[string]any
	lead.Get("/api/v1/code/projects/PAY/permissions", &grants)
	lead.MustDo(http.MethodPut, "/api/v1/code/projects/PAY/permissions", append(grants,
		map[string]any{"holder": map[string]any{"type": "user", "id": ids["dev"]}, "permission": "repo.write"}), nil)
	lead.MustDo(http.MethodPost, "/api/v1/code/projects/PAY/repos", map[string]any{"name": "service", "readme": true}, nil)
	openPR(dev, "PAY", "service", issue.Key+" Refund through PayPal")
	if got := lastRun("Opened"); !strings.HasPrefix(got, "done") || status() != inProgress {
		t.Fatalf("rule after the developer's pull request: %q, status %d", got, status())
	}
	var comments []struct {
		Body any `json:"body"`
	}
	lead.Get("/api/v1/issues/"+issue.Key+"/comments", &comments)
	if len(comments) != 1 {
		t.Fatalf("comments: %+v", comments)
	}
	if text := apitest.Fmt("%v", comments[0].Body); !strings.Contains(text, "A pull request naming this issue was opened.") ||
		strings.Contains(text, "PAY") || strings.Contains(text, "PayPal") || strings.Contains(text, "phish") {
		t.Fatalf("comment: %s", text)
	}

	// Rules stop when their owner no longer administers the project.
	admin.MustDo(http.MethodPost, "/api/v1/projects/AXP/roles/"+apitest.Fmt("%d", cfg.Roles[0].ID)+"/members",
		map[string]any{"type": "user", "id": ids["dev"]}, nil)
	lead.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/projects/AXP/roles/%d/members?type=user&id=%d", cfg.Roles[0].ID, ids["lead"]), nil, nil)
	lead.MustDo(http.MethodPut, "/api/v1/code/repos/PAY/service/files", map[string]any{"branch": "main", "newBranch": "second",
		"path": "y.txt", "content": "y\n", "message": "More"}, nil)
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PAY/service/pull-requests", map[string]any{"title": issue.Key + " More", "source": "second"}, nil)
	env.WorkJobs()
	var rules []ruleView
	dev.Get("/api/v1/projects/AXP/automation", &rules)
	for _, r := range rules {
		if r.Name == "Opened" && (len(r.LastRuns) == 0 || r.LastRuns[0].Message != "The owner of the rule no longer administers the project.") {
			t.Fatalf("rule of a former administrator: %+v", r.LastRuns)
		}
	}
}
