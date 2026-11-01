// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command mkfixture fills a Kogge built from commit 0a8b7a5 through its API with the data of the fixture
// kogge-backup-00013.tar (see old_backup_test.go for the whole procedure). It talks to KOGGE_URL (default
// http://127.0.0.1:18130), whose bootstrap administrator is admin with the password Fixture-Root-Secret-1:
//
//	go run mkfixture.go data      # prints the agent registration token; then register and run the old agent
//	go run mkfixture.go runs      # the states of the runs, until the agent's run succeeded
//	go run mkfixture.go webhook   # last, so that no delivery waits in the backup
//
// It lives in testdata, so the go tool leaves it out of builds and tests.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
)

var base = func() string {
	if u := os.Getenv("KOGGE_URL"); u != "" {
		return u
	}
	return "http://127.0.0.1:18130"
}()

type client struct {
	http *http.Client
	csrf string
}

func login(user, password string) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{http: &http.Client{Jar: jar}}
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": password}, &me)
	c.csrf = me.CSRFToken
	return c
}

func (c *client) do(method, path string, body any, out any) {
	var r io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case *bytes.Buffer:
		r = b
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
		ct = "application/json"
	}
	req, _ := http.NewRequest(method, base+path, r)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	req.Header.Set("Origin", base)
	resp, err := c.http.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "%s %s: %d %s\n", method, path, resp.StatusCode, raw)
		os.Exit(1)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			panic(fmt.Sprintf("%s %s: %v: %s", method, path, err, raw))
		}
	}
}

func (c *client) upload(path, name, content string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = io.WriteString(fw, content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", base+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	req.Header.Set("Origin", base)
	resp, err := c.http.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "upload %s: %d %s\n", path, resp.StatusCode, raw)
		os.Exit(1)
	}
}

func doc(text string) map[string]any {
	var content []any
	for _, line := range strings.Split(text, "\n") {
		p := map[string]any{"type": "paragraph"}
		if line != "" {
			p["content"] = []any{map[string]any{"type": "text", "text": line}}
		}
		content = append(content, p)
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

const pipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Build
        jobs:
          - name: test
            runs-on: [shell]
            steps:
              - run: echo "hello from the old agent"
`

func main() {
	step := os.Args[1]
	admin := login("admin", "Fixture-Root-Secret-1")
	switch step {
	case "data":
		var bob struct {
			ID int64 `json:"id"`
		}
		admin.do("POST", "/api/v1/admin/users", map[string]string{"username": "bob", "displayName": "Bob Builder",
			"password": "Fixture-User-Secret-1", "email": "bob@example.com"}, &bob)

		// An issue project with a version, bob as developer, and an issue with history, attachments and comments.
		admin.do("POST", "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "scrum"}, nil)
		var roles []struct {
			Role struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"role"`
		}
		admin.do("GET", "/api/v1/projects/SHOP/roles", nil, &roles)
		var admins, devs, users int64
		for _, r := range roles {
			switch r.Role.Name {
			case "Developers", "Entwickler":
				devs = r.Role.ID
			case "Administrators", "Administratoren":
				admins = r.Role.ID
			case "Users", "Benutzer":
				users = r.Role.ID
			}
		}
		admin.do("POST", fmt.Sprintf("/api/v1/projects/SHOP/roles/%d/members", devs), map[string]any{"type": "user", "id": bob.ID}, nil)
		var version struct {
			ID int64 `json:"id"`
		}
		admin.do("POST", "/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &version)
		var types []struct {
			ID   int64  `json:"issueTypeId"`
			Name string `json:"name"`
		}
		admin.do("GET", "/api/v1/projects/SHOP/issue-types", nil, &types)
		// The default issue types in the order the defaults create them: epic, story, task, bug, sub-task.
		task, subtask := types[2].ID, types[4].ID
		var issue struct {
			Key string `json:"key"`
		}
		admin.do("POST", "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": task,
			"summary":     "Checkout: Grüße, \"quotes\", tabs\tand\nnew lines \\ backslashes",
			"description": doc("The checkout needs a second payment provider.\nSee the runbook.")}, &issue)
		admin.upload("/api/v1/issues/"+issue.Key+"/attachments", "notes.txt", "hello attachment")
		admin.upload("/api/v1/issues/"+issue.Key+"/attachments", "more.txt", "another attachment")
		admin.do("PATCH", "/api/v1/issues/"+issue.Key, map[string]any{"fields": map[string]any{
			"fixVersions": []int64{version.ID}, "assignee": bob.ID, "labels": []string{"payments"}}}, nil)
		var transitions []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		admin.do("GET", "/api/v1/issues/"+issue.Key+"/transitions", nil, &transitions)
		for _, t := range transitions {
			if t.Name == "Start progress" || t.Name == "Bearbeitung beginnen" {
				admin.do("POST", "/api/v1/issues/"+issue.Key+"/transitions", map[string]any{"transitionId": t.ID}, nil)
			}
		}
		bobc := login("bob", "Fixture-User-Secret-1")
		bobc.do("POST", "/api/v1/issues/"+issue.Key+"/comments", map[string]any{"markdown": "Working on it."}, nil)
		admin.do("POST", "/api/v1/issues/"+issue.Key+"/comments", map[string]any{"markdown": "Only for administrators: the old provider is zanzibar.",
			"visibilityType": "role", "visibilityId": admins}, nil)
		_ = users
		var sub struct {
			Key string `json:"key"`
		}
		admin.do("POST", "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": subtask, "parent": issue.Key,
			"summary": "Write the provider's adapter"}, &sub)

		// A space with a page published twice, and bob's draft of a third version.
		admin.do("POST", "/api/v1/wiki/spaces", map[string]any{"key": "DOCS", "name": "Docs", "open": true}, nil)
		var page struct {
			ID int64 `json:"id"`
		}
		admin.do("POST", "/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Runbook"}, &page)
		admin.do("POST", fmt.Sprintf("/api/v1/wiki/pages/%d/publish", page.ID), map[string]any{"title": "Runbook",
			"body": doc("Restart the shop with care."), "baseVersion": 0, "message": "First version"}, nil)
		admin.do("POST", fmt.Sprintf("/api/v1/wiki/pages/%d/publish", page.ID), map[string]any{"title": "Runbook",
			"body": doc("Restart the shop with care.\nCall SHOP-1's assignee first."), "baseVersion": 1, "message": "Who to call"}, nil)
		bobc.do("PUT", fmt.Sprintf("/api/v1/wiki/pages/%d/draft", page.ID), map[string]any{"title": "Runbook",
			"body": doc("Restart the shop with care.\nCall SHOP-1's assignee first.\nBob's unpublished addition."), "baseVersion": 2}, nil)

		// A repository whose push starts a pipeline.
		admin.do("POST", "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
		admin.do("POST", "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, nil)
		admin.do("PUT", "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
			"content": pipeline, "message": issue.Key + " Build the shop"}, nil)
		// The automation rule comments on the issues a merged pull request names.
		admin.do("POST", "/api/v1/projects/SHOP/automation", map[string]any{"name": "Note merges", "trigger": "pull-request.merged",
			"issueTypes": []int64{}, "statuses": []int64{}, "action": "comment", "params": map[string]any{"text": "Merged."}, "active": true}, nil)
		var token struct {
			Token string `json:"token"`
		}
		admin.do("POST", "/api/v1/admin/agent-tokens", map[string]any{"description": "fixture agent"}, &token)
		fmt.Println(token.Token)
	case "webhook":
		// Last, so that it has no deliveries waiting.
		admin.do("POST", "/api/v1/webhooks?scope=repository&key=PLAT/shop", map[string]any{"name": "CI mirror",
			"url": "https://hooks.example.com/kogge", "events": []string{}, "active": true, "secret": "fixture-webhook-secret"}, nil)
	case "runs":
		var runs struct {
			Items []struct {
				State string `json:"state"`
			} `json:"items"`
		}
		admin.do("GET", "/api/v1/builds/runs", nil, &runs)
		for _, r := range runs.Items {
			fmt.Println(r.State)
		}
	}
}
