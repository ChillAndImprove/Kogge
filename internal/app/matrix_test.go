// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/testutil/apitest"
)

// The permission matrix: every route of the server — the operations of the API and the routes
// outside of it (uploads, downloads, Git, the agent protocol) — has an entry in matrix that names what it needs.
// The test calls each route as an anonymous client, as a signed-in outsider and as a reader of the fixture's
// project, space and repository, and checks the answers against that entry. A route without an entry fails the
// test, so a new endpoint cannot ship without an authorisation decision.
//
// The fixture belongs to lena. Everything she creates carries the marker, which neither anonymous clients nor
// the outsider may ever see, in no answer of any route.

const marker = "MATRIX-SECRET"

// key is the key of the fixture's project, space and code project (spelled out in the fixture's paths). Answers to
// outsiders name it only when their request did.
const key = "QZXV"

// ghostKey and the other ghost values name objects that do not exist.
const ghostKey = "ZZZZ"

// Principals of the matrix.
const (
	anonymous = "anonymous" // no credentials; the instance allows anonymous access to content granted to anyone
	outsider  = "eve"       // signed in, no grants on the fixture
	reader    = "rita"      // the fixture's readers: role Users in the project, viewer and commenter in the space, repo.read
)

// Needs of routes that are not permissions.
const (
	anyone   = "anyone"    // no sign-in needed; lists and searches show only what the caller may see
	signedIn = "signed-in" // any signed-in user, for themselves
	owner    = "owner"     // only the user the object belongs to (lena)
	shared   = "shared"    // users the object is shared with (the project's members)
	agentKey = "agent"     // credentials of their own: of an agent, a job or a pipeline trigger
	deployer = "deployer"  // the deployers or approvers of the environment
)

// readerHolds are the permissions of the reader: the default scheme's role Users, space.view and comment.add in
// the space, repo.read in the code project, and the defaults of signed-in users.
var readerHolds = []string{"project.browse", "issue.create", "comment.add", "comment.edit-own", "comment.delete-own",
	"attachment.create", "attachment.delete-own", "watchers.view", "project.view-development", "issue.link",
	"space.view", "repo.read", shared}

// signedInHolds are the global permissions every signed-in user has by default (migration 00002).
var signedInHolds = []string{authz.UserBrowse, authz.SpaceCreatePersonal}

// entry is the authorisation decision for a route and how the matrix calls it.
type entry struct {
	// need is what the route requires: a permission (global or of the fixture's project, space or repository) or
	// one of anyone, signedIn, owner, shared, agentKey and deployer.
	need string
	// query is added to the path; {name} stands for a value of the fixture.
	query string
	// body is sent as JSON (ref values are replaced by values of the fixture), or as a multipart upload (upload) or
	// raw content (raw).
	body any
	// git routes authenticate with the principal's access token (HTTP basic authentication).
	git bool
	// stream routes answer with an event stream; only the status and the first bytes are read.
	stream bool
	// found lists statuses besides 2xx that count as success for those who may (e.g. the redirects of old
	// addresses, or 404 of an unknown API path).
	found []int
	// params chooses fixture values for path parameters by name, where the collection does not decide.
	params map[string]string
	// alt are further calls of the route that need something else (other scopes of the same operation).
	alt []entry
}

// ref is a value of the fixture in a request body.
type ref string

// upload is a multipart upload of one file.
type upload struct{ name, content string }

// raw is a body with its content type.
type raw struct{ contentType, content string }

type matrixFixture struct {
	t           *testing.T
	env         *apitest.Env
	admin, lena *apitest.Client
	clients     map[string]*apitest.Client
	vals        map[string]any
	tokens      map[string]string // Git access tokens
}

func (f *matrixFixture) str(name string) string {
	v, ok := f.vals[name]
	if !ok {
		f.t.Fatalf("the fixture has no value %q", name)
	}
	return fmt.Sprint(v)
}

func (f *matrixFixture) set(name string, v any) { f.vals[name] = v }

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func obj(t *testing.T, r *apitest.Response) map[string]any {
	t.Helper()
	var m map[string]any
	r.Decode(&m)
	return m
}

func list(t *testing.T, r *apitest.Response) []map[string]any {
	t.Helper()
	var l []map[string]any
	r.Decode(&l)
	return l
}

func doc(text string) map[string]any {
	return map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": text}}}}}
}

// docMentioning is a document that mentions a user (who must not learn anything of what they cannot see).
func docMentioning(text string, userID int64, name string) map[string]any {
	return map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": text + " "},
		map[string]any{"type": "mention", "attrs": map[string]any{"user": userID, "label": name}}}}}}
}

// newSSHKey returns a new public key in the authorized_keys format.
func newSSHKey() string {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func multipartBody(name, content string) (io.Reader, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", name)
	_, _ = part.Write([]byte(content))
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

const matrixPipeline = `version: 1
pipelines:
  ci:
    on:
      push:
        branches: [main]
      manual: {}
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [shell]
            steps:
              - run: mkdir -p dist && echo "MATRIX-SECRET artifact" > dist/app.txt && echo "MATRIX-SECRET log"
            artifacts:
              - name: dist
                paths: ["dist/**"]
      - name: Deploy
        jobs:
          - name: production
            runs-on: [shell]
            environment: production
            steps:
              - run: echo deploying
          - name: later
            runs-on: [shell]
            trigger: manual
            steps:
              - run: echo later
`

// newMatrixFixture builds lena's project, space and code project with their content, and the readers' grants.
func newMatrixFixture(t *testing.T, env *apitest.Env) *matrixFixture {
	f := &matrixFixture{t: t, env: env, admin: env.Admin(), vals: map[string]any{}, tokens: map[string]string{}}
	f.lena = env.CreateUser("lena")
	rita, eve := env.CreateUser(reader), env.CreateUser(outsider)
	f.clients = map[string]*apitest.Client{anonymous: env.Anonymous(), outsider: eve, reader: rita}
	for name, c := range map[string]*apitest.Client{"lena": f.lena, reader: rita, outsider: eve, "admin": f.admin} {
		var m me
		c.Get("/api/v1/me", &m)
		f.set(name, m.User.ID)
	}
	for _, perm := range []string{authz.ProjectCreate, authz.SpaceCreate, authz.CodeProjectCreate} {
		f.admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": perm, "principalType": "user",
			"principalId": f.vals["lena"]}, nil)
	}
	// Anonymous clients take the paths of anonymous access: content granted to anyone is visible to them.
	var st map[string]any
	f.admin.Get("/api/v1/admin/settings", &st)
	st["allowAnonymous"] = true
	f.admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)

	f.administration()
	f.issues()
	f.wiki()
	f.code()
	f.builds()
	f.personal()
	f.env.WorkJobs() // notifications and webhooks of all of the above
	return f
}

// administration: a group with lena, a custom field and the ids of the issue configuration.
func (f *matrixFixture) administration() {
	t := f.t
	g := obj(t, f.admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "matrix-team"}, nil))
	f.set("group", num(g["id"]))
	f.admin.MustDo(http.MethodPut, "/api/v1/admin/groups/"+f.str("group")+"/members/"+f.str("lena"), nil, nil)
	field := obj(t, f.admin.MustDo(http.MethodPost, "/api/v1/issue-config/fields", map[string]any{"name": "Matrix field",
		"type": "text"}, nil))
	f.set("field", num(field["id"]))

	var cfg struct {
		IssueTypes  []map[string]any `json:"issueTypes"`
		Statuses    []map[string]any `json:"statuses"`
		Priorities  []map[string]any `json:"priorities"`
		Resolutions []map[string]any `json:"resolutions"`
		LinkTypes   []map[string]any `json:"linkTypes"`
		Roles       []map[string]any `json:"roles"`
	}
	f.admin.Get("/api/v1/issue-config", &cfg)
	for _, it := range cfg.IssueTypes {
		if it["icon"] == "story" {
			f.set("issueType", num(it["id"]))
		}
	}
	f.set("status", num(cfg.Statuses[0]["id"]))
	f.set("priority", num(cfg.Priorities[0]["id"]))
	f.set("resolution", num(cfg.Resolutions[0]["id"]))
	f.set("linkType", num(cfg.LinkTypes[0]["id"]))
	f.set("role", num(cfg.Roles[2]["id"])) // Administrators, Developers, Users
	first := func(path, name string) {
		var l []map[string]any
		f.admin.Get(path, &l)
		if len(l) == 0 {
			t.Fatalf("%s is empty", path)
		}
		f.set(name, num(l[0]["id"]))
	}
	first("/api/v1/issue-config/workflows", "workflow")
	first("/api/v1/issue-config/permission-schemes", "permissionScheme")
	first("/api/v1/issue-config/notification-schemes", "notificationScheme")
	sec := obj(t, f.admin.MustDo(http.MethodPost, "/api/v1/issue-config/security-schemes", map[string]any{"name": "Matrix levels",
		"levels": []map[string]any{{"name": "Staff", "members": []map[string]any{{"holderType": "project-lead"}}}}}, nil))
	f.set("securityScheme", num(sec["id"]))
}

// issues: project QZXV with two issues, a comment that mentions the outsider, work, a link, an attachment, a
// version, a component, a board with a sprint, a shared filter and dashboard, and an automation rule.
func (f *matrixFixture) issues() {
	t, lena := f.t, f.lena
	p := obj(t, lena.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "QZXV", "name": marker + " project",
		"description": marker, "template": "scrum"}, nil))
	f.set("project", num(p["id"]))
	lena.Post("/api/v1/projects/QZXV/roles/"+f.str("role")+"/members", map[string]any{"type": "user", "id": f.vals[reader]}, nil)

	for _, summary := range []string{marker + " first issue", marker + " second issue"} {
		lena.Post("/api/v1/issues", map[string]any{"project": "QZXV", "typeId": f.vals["issueType"], "summary": summary,
			"descriptionMarkdown": marker + " description"}, nil)
	}
	c := obj(t, lena.MustDo(http.MethodPost, "/api/v1/issues/QZXV-1/comments", map[string]any{
		"body": docMentioning(marker+" comment for", num(f.vals[outsider]), outsider)}, nil))
	f.set("comment", num(c["id"]))
	lena.Post("/api/v1/issues/QZXV-1/worklogs", map[string]any{"timeSpent": "1h", "comment": marker + " work"}, nil)
	var worklogs []map[string]any
	lena.Get("/api/v1/issues/QZXV-1/worklogs", &worklogs)
	f.set("worklog", num(worklogs[0]["id"]))
	lena.Post("/api/v1/issues/QZXV-1/links", map[string]any{"issue": "QZXV-2", "typeId": f.vals["linkType"]}, nil)
	var issue struct {
		Links []map[string]any `json:"links"`
	}
	lena.Get("/api/v1/issues/QZXV-1", &issue)
	f.set("link", num(issue.Links[0]["id"]))
	body, contentType := multipartBody(marker+".txt", marker+" attachment")
	at := list(t, lena.MustDo(http.MethodPost, "/api/v1/issues/QZXV-1/attachments", body, nil, "Content-Type", contentType))
	f.set("issueAttachment", num(at[0]["id"]))
	var transitions []map[string]any
	lena.Get("/api/v1/issues/QZXV-1/transitions", &transitions)
	f.set("transition", num(transitions[0]["id"]))
	// A bulk change of lena's that changed QZXV-1 (its job runs with the others at the end of the fixture).
	bulk := obj(t, lena.MustDo(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": []string{"QZXV-1"}, "action": "edit",
		"edit": map[string]any{"labels": map[string]any{"mode": "add", "values": []string{"matrix"}}}}, nil))
	f.set("bulkChange", num(bulk["id"]))

	v := obj(t, lena.MustDo(http.MethodPost, "/api/v1/projects/QZXV/versions", map[string]any{"name": marker + " 1.0"}, nil))
	f.set("version", num(v["id"]))
	comp := obj(t, lena.MustDo(http.MethodPost, "/api/v1/projects/QZXV/components", map[string]any{"name": marker + " component"}, nil))
	f.set("component", num(comp["id"]))
	board := obj(t, lena.MustDo(http.MethodPost, "/api/v1/boards", map[string]any{"name": marker + " board", "project": "QZXV",
		"type": "scrum"}, nil))
	f.set("board", num(board["id"]))
	sprint := obj(t, lena.MustDo(http.MethodPost, "/api/v1/boards/"+f.str("board")+"/sprints", map[string]any{
		"name": marker + " sprint", "goal": marker}, nil))
	f.set("sprint", num(sprint["id"]))
	lena.Post("/api/v1/sprints/"+f.str("sprint")+"/issues", map[string]any{"issues": []string{"QZXV-1"}}, nil)
	lena.Post("/api/v1/sprints/"+f.str("sprint")+"/start", map[string]any{}, nil)
	next := obj(t, lena.MustDo(http.MethodPost, "/api/v1/boards/"+f.str("board")+"/sprints", map[string]any{
		"name": marker + " next sprint", "goal": marker}, nil))
	f.set("nextSprint", num(next["id"]))

	share := []map[string]any{{"type": "project", "projectId": f.vals["project"]}}
	filter := obj(t, lena.MustDo(http.MethodPost, "/api/v1/filters", map[string]any{"name": marker + " filter",
		"query": "project = QZXV", "description": marker, "shares": share}, nil))
	f.set("filter", num(filter["id"]))
	// Lena's subscription of her filter (the matrix's installation sends no e-mail, so it is written directly).
	var subscription int64
	if err := f.env.App.Pool.QueryRow(context.Background(), `INSERT INTO filter_subscriptions (filter_id, user_id, created_by, frequency,
		minute, weekdays, time_zone, next_run_at) VALUES ($1, $2, $2, 'daily', 480, '{1}', 'UTC', now() + interval '1 day') RETURNING id`,
		f.vals["filter"], f.vals["lena"]).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	f.set("filterSubscription", subscription)
	dash := obj(t, lena.MustDo(http.MethodPost, "/api/v1/dashboards", map[string]any{"name": marker + " dashboard",
		"description": marker, "shares": share, "widgets": []map[string]any{{"id": "w1", "type": "text", "column": 0,
			"position": 0, "title": marker, "config": map[string]any{"text": marker}}}}, nil))
	f.set("dashboard", num(dash["id"]))
	rule := obj(t, lena.MustDo(http.MethodPost, "/api/v1/projects/QZXV/automation", map[string]any{"name": marker + " rule",
		"trigger": "pull-request.merged", "action": "comment", "params": map[string]any{"text": marker},
		"issueTypes": []int64{}, "statuses": []int64{}, "active": false}, nil))
	f.set("rule", num(rule["id"]))
	hook := obj(t, lena.MustDo(http.MethodPost, "/api/v1/webhooks?scope=project&key=QZXV", map[string]any{"name": marker + " hook",
		"url": "https://hooks.example.com/kogge", "events": []string{"issue.created"}, "active": true}, nil))
	f.set("webhook", num(hook["id"]))
	lena.MustDo(http.MethodPost, "/api/v1/webhooks/"+f.str("webhook")+"/ping", nil, nil)
	f.env.WorkJobs()
	var deliveries []map[string]any
	lena.Get("/api/v1/webhooks/"+f.str("webhook")+"/deliveries", &deliveries)
	if len(deliveries) == 0 {
		t.Fatal("the ping left no delivery")
	}
	f.set("delivery", num(deliveries[0]["id"]))
}

// wiki: closed space QZXV with a page that mentions QZXV-1, a blog post, comments (one mentions the outsider), an
// attachment, a label and a template.
func (f *matrixFixture) wiki() {
	t, lena := f.t, f.lena
	open := false
	sp := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "QZXV", "name": marker + " space",
		"description": marker, "open": &open}, nil))
	f.set("home", num(sp["homePageId"]))
	var grants []map[string]any
	lena.Get("/api/v1/wiki/spaces/QZXV/permissions", &grants)
	for _, perm := range []string{"space.view", "comment.add"} {
		grants = append(grants, map[string]any{"permission": perm, "holder": map[string]any{"type": "user", "id": f.vals[reader]}})
	}
	lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/QZXV/permissions", grants, nil)

	publish := func(kind, title string) int64 {
		pg := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/spaces/QZXV/pages", map[string]any{"title": title, "kind": kind,
			"parentId": f.vals["home"]}, nil))
		id := strconv.FormatInt(num(pg["id"]), 10)
		lena.Post("/api/v1/wiki/pages/"+id+"/publish", map[string]any{"title": title, "baseVersion": 0,
			"body": doc(marker + " text about QZXV-1")}, nil)
		return num(pg["id"])
	}
	f.set("page", publish("page", marker+" page"))
	f.set("blog", publish("blog", marker+" news"))
	pageID := f.str("page")
	c := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pageID+"/comments", map[string]any{
		"body": docMentioning(marker+" remark for", num(f.vals[outsider]), outsider)}, nil))
	f.set("pageComment", num(c["id"]))
	inline := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pageID+"/inline-comments", map[string]any{
		"body": doc(marker + " inline"), "text": "text about"}, nil))
	f.set("inlineComment", num(inline["id"]))
	f.set("trashed", publish("page", marker+" old page"))
	lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+f.str("trashed"), nil, nil)
	lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pageID+"/labels", []string{"matrix-secret"}, nil)
	body, contentType := multipartBody(marker+".txt", marker+" file")
	at := list(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pageID+"/attachments", body, nil, "Content-Type", contentType))
	f.set("pageAttachment", num(at[0]["id"]))
	tpl := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/templates?space=QZXV", map[string]any{"name": marker + " template",
		"description": marker, "body": doc(marker)}, nil))
	f.set("template", num(tpl["id"]))
	// A draft of lena's that nobody else sees.
	lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pageID+"/draft", map[string]any{"title": marker + " draft",
		"baseVersion": 1, "body": doc(marker + " draft")}, nil)
	// An export of the space, which only lena downloads (the fixture's jobs write it).
	export := obj(t, lena.MustDo(http.MethodPost, "/api/v1/wiki/spaces/QZXV/exports", map[string]any{"format": "html"}, nil))
	f.set("export", num(export["id"]))
}

// code: private code project QZXV with repository app, a commit on main, a branch with a pull request and a comment
// that mentions the outsider, an environment, a deploy key, variables, a commit status and an LFS object.
func (f *matrixFixture) code() {
	t, lena := f.t, f.lena
	lena.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "QZXV", "name": marker + " code",
		"description": marker}, nil)
	repo := obj(t, lena.MustDo(http.MethodPost, "/api/v1/code/projects/QZXV/repos", map[string]any{"name": "app", "description": marker,
		"readme": true}, nil))
	f.set("repository", num(repo["id"]))
	lena.MustDo(http.MethodPut, "/api/v1/code/projects/QZXV/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": f.vals["lena"]}},
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": f.vals[reader]}},
	}, nil)
	lena.MustDo(http.MethodPut, "/api/v1/code/repos/QZXV/app/environments/production", map[string]any{
		"description": marker, "refs": []string{"main"}, "deployerUsers": []int64{}, "deployerGroups": []int64{},
		"approvals": 1, "approverUsers": []any{f.vals["lena"]}, "approverGroups": []int64{}}, nil)
	for _, scope := range []string{"scope=project&project=QZXV", "scope=repository&project=QZXV&repository=app"} {
		lena.MustDo(http.MethodPut, "/api/v1/builds/variables/MATRIX_VAR?"+scope, map[string]any{"value": marker,
			"secret": false}, nil)
	}
	commit := func(change map[string]any) string {
		var c struct {
			SHA string `json:"sha"`
		}
		lena.MustDo(http.MethodPut, "/api/v1/code/repos/QZXV/app/files", change, &c)
		return c.SHA
	}
	commit(map[string]any{"branch": "main", "path": "secret.txt", "content": marker + "\n", "message": marker + " commit"})
	commit(map[string]any{"branch": "main", "newBranch": "feature", "path": "feature.txt", "content": marker + "\n",
		"message": marker + " feature"})
	f.set("sha", commit(map[string]any{"branch": "main", "path": ".kogge/pipelines.yml", "content": matrixPipeline,
		"message": marker + " pipeline"}))
	lena.Post("/api/v1/code/repos/QZXV/app/pull-requests", map[string]any{"source": "feature", "target": "main",
		"title": marker + " change", "descriptionMarkdown": marker}, nil)
	c := obj(t, lena.MustDo(http.MethodPost, "/api/v1/code/repos/QZXV/app/pull-requests/1/comments", map[string]any{
		"body": docMentioning(marker+" review for", num(f.vals[outsider]), outsider), "task": true}, nil))
	f.set("prComment", num(c["id"]))
	tr := obj(t, lena.MustDo(http.MethodPost, "/api/v1/code/repos/QZXV/app/triggers", map[string]any{"description": marker + " trigger"}, nil))
	f.set("pipelineTrigger", num(tr["id"]))
	dk := obj(t, lena.MustDo(http.MethodPost, "/api/v1/code/repos/QZXV/app/deploy-keys", map[string]any{"name": marker + " key",
		"key": newSSHKey()}, nil))
	f.set("deployKey", num(dk["id"]))
	lena.Post("/api/v1/code/repos/QZXV/app/statuses/"+f.str("sha"), map[string]any{"context": marker, "state": "success",
		"description": marker}, nil)

	// Git access tokens of the readers and the outsider; an LFS object of lena's.
	for _, who := range []string{"lena", reader, outsider} {
		c := map[string]*apitest.Client{"lena": lena, reader: f.clients[reader], outsider: f.clients[outsider]}[who]
		var tok struct {
			Token string `json:"token"`
		}
		c.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &tok)
		f.tokens[who] = tok.Token
	}
	content := marker + " large file"
	sum := sha256.Sum256([]byte(content))
	f.set("oid", hex.EncodeToString(sum[:]))
	f.set("lfsSize", len(content))
	batch := fmt.Sprintf(`{"operation":"upload","transfers":["basic"],"objects":[{"oid":%q,"size":%d}]}`, f.str("oid"), len(content))
	res := f.gitRequest("lena", http.MethodPost, "/scm/QZXV/app.git/info/lfs/objects/batch", "application/vnd.git-lfs+json", batch)
	var b struct {
		Objects []struct {
			Actions map[string]struct {
				Href   string            `json:"href"`
				Header map[string]string `json:"header"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(res, &b); err != nil || len(b.Objects) != 1 {
		t.Fatalf("LFS batch: %s", res)
	}
	up := b.Objects[0].Actions["upload"]
	req, _ := http.NewRequest(http.MethodPut, up.Href, strings.NewReader(content))
	for k, v := range up.Header {
		req.Header.Set(k, v)
	}
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("LFS upload: %v %v", err, r)
	}
}

func (f *matrixFixture) gitRequest(who, method, path, contentType, body string) []byte {
	req, _ := http.NewRequest(method, f.env.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", contentType)
	req.SetBasicAuth(who, f.tokens[who])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
	}
	return data
}

// builds: an agent runs the pipeline of main: the build job leaves a log and an artifact, the deployment waits for
// lena's approval and a manual job for its start.
func (f *matrixFixture) builds() {
	t := f.t
	var reg struct {
		Token string `json:"token"`
	}
	f.admin.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": marker, "autoApprove": true}, &reg)
	// Templates are for every signed-in user: no marker in it.
	tpl := obj(t, f.admin.MustDo(http.MethodPost, "/api/v1/builds/templates", map[string]any{"name": "Matrix template",
		"content": "version: 1\npipelines:\n  ci:\n    on: {push: {branches: [main]}}\n    stages:\n      - name: Build\n        jobs:\n          - name: build\n            steps:\n              - run: \"true\"\n"}, nil))
	f.set("pipelineTemplate", tpl["id"])
	cfg := &agent.Config{Server: f.env.URL, Name: "matrix-agent", WorkDir: t.TempDir(), Shell: true, Parallel: 1}
	if err := agent.Register(context.Background(), cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(cfg, "test", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	stop, stopAgent := context.WithCancel(context.Background())
	kill, killAgent := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(stop, kill) }()
	f.env.WorkJobs() // the push of the pipeline starts the run
	type job struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		State      string `json:"state"`
		Deployment *struct {
			ID int64 `json:"id"`
		} `json:"deployment"`
		Artifacts []struct {
			ID int64 `json:"id"`
		} `json:"artifacts"`
	}
	var run struct {
		ID     int64 `json:"id"`
		Stages []struct {
			Jobs []job `json:"jobs"`
		} `json:"stages"`
	}
	jobs := map[string]job{}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if r := f.lena.Do(http.MethodGet, "/api/v1/code/repos/QZXV/app/runs/1", nil); r.Status == http.StatusOK {
			r.Decode(&run)
			for _, s := range run.Stages {
				for _, j := range s.Jobs {
					jobs[j.Name] = j
				}
			}
			if p := jobs["production"]; jobs["compile"].State == "succeeded" && p.Deployment != nil && jobs["later"].State == "waiting" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pipeline did not get to its deployment: %+v", jobs)
		}
		f.env.WorkJobs()
		time.Sleep(100 * time.Millisecond)
	}
	stopAgent()
	killAgent()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("agent: %v", err)
	}
	f.set("run", run.ID)
	f.set("job", jobs["compile"].ID)
	f.set("manualJob", jobs["later"].ID)
	f.set("deployment", jobs["production"].Deployment.ID)
	f.set("artifact", jobs["compile"].Artifacts[0].ID)
	var agents []map[string]any
	f.admin.Get("/api/v1/admin/agents", &agents)
	f.set("agent", num(agents[0]["id"]))
	var tokens []map[string]any
	f.admin.Get("/api/v1/admin/agent-tokens", &tokens)
	f.set("agentToken", num(tokens[0]["id"]))
}

// personal: lena's session, access token and SSH key.
func (f *matrixFixture) personal() {
	t := f.t
	var sessions []map[string]any
	f.lena.Get("/api/v1/me/sessions", &sessions)
	f.set("session", sessions[0]["id"])
	var tokens []map[string]any
	f.lena.Get("/api/v1/me/tokens", &tokens)
	f.set("token", num(tokens[0]["id"]))
	k := obj(t, f.lena.MustDo(http.MethodPost, "/api/v1/me/ssh-keys", map[string]any{"name": marker + " laptop",
		"key": newSSHKey()}, nil))
	f.set("sshKey", num(k["id"]))
}

// pathValue returns the fixture's value for a path parameter, chosen by the collection it follows, or with ghost a
// value of the same kind that names nothing.
func (f *matrixFixture) pathValue(path []string, i int, e entry, ghost bool) string {
	name := strings.Trim(path[i], "{}")
	prev := ""
	if i > 0 {
		prev = path[i-1]
	}
	if v, ok := e.params[name]; ok && !ghost {
		return f.str(v)
	}
	inWiki := slices.Contains(path, "wiki")
	pick := func(real, none string) string {
		if ghost {
			return none
		}
		return real
	}
	const noID = "999999999"
	switch name {
	case "key":
		switch prev {
		case "issues":
			return pick(key+"-1", ghostKey+"-1")
		case "caches":
			return pick("deps", "none")
		}
		return pick(key, ghostKey)
	case "space":
		return pick(key, ghostKey)
	case "slug":
		return pick("app", "none")
	case "repo":
		return pick("app.git", "none.git")
	case "number", "version":
		return pick("1", "999999")
	case "username":
		return pick("lena", "nobody")
	case "issue":
		return pick(key+"-1", ghostKey+"-1")
	case "tiny":
		return "abc"
	case "*":
		return "x"
	}
	if ghost {
		switch {
		case name == "id" && prev == "sessions":
			return strings.Repeat("0", 16)
		case name == "sha":
			return strings.Repeat("1", 40)
		case name == "oid":
			return strings.Repeat("1", 64)
		case name == "file" || name == "name":
			return "none"
		}
		return noID
	}
	switch name {
	case "userId":
		return f.str("lena")
	case "tokenId":
		return f.str("token")
	case "keyId":
		return f.str("sshKey")
	case "roleId":
		return f.str("role")
	case "typeId":
		return f.str("issueType")
	case "sha":
		return f.str("sha")
	case "oid":
		return f.str("oid")
	case "artifact":
		return f.str("artifact")
	case "page":
		return f.str("page")
	case "file":
		if prev == "downloads" {
			return "kogge-agent-linux-amd64"
		}
		return marker + ".txt"
	case "name":
		switch prev {
		case "environments":
			return "production"
		case "variables":
			return "MATRIX_VAR"
		case "artifacts":
			return "dist"
		}
	case "id":
		byCollection := map[string]string{"agent-tokens": "agentToken", "agents": "agent", "groups": "group",
			"users": "lena", "sessions": "session", "ssh-keys": "sshKey", "tokens": "token", "boards": "board",
			"deployments": "deployment", "jobs": "job", "runs": "run", "pull-request-comments": "prComment",
			"deploy-keys": "deployKey", "dashboards": "dashboard", "filters": "filter", "issue-attachments": "issueAttachment",
			"filter-subscriptions": "filterSubscription",
			"fields":               "field", "issue-types": "issueType", "link-types": "linkType", "notification-schemes": "notificationScheme",
			"permission-schemes": "permissionScheme", "priorities": "priority", "resolutions": "resolution", "roles": "role",
			"security-schemes": "securityScheme", "statuses": "status", "workflows": "workflow", "issue-links": "link",
			"worklogs": "worklog", "automation": "rule", "components": "component", "versions": "version", "sprints": "sprint",
			"webhooks": "webhook", "deliveries": "delivery", "pages": "page", "templates": "template", "comments": "comment",
			"exports": "export", "attachments": "issueAttachment", "attachment": "issueAttachment", "artifacts": "artifact",
			"repositories": "repository", "bulk": "bulkChange"}
		v := byCollection[prev]
		if inWiki && prev == "comments" {
			v = "pageComment"
		}
		if inWiki && prev == "attachments" {
			v = "pageAttachment"
		}
		if v != "" {
			return f.str(v)
		}
	}
	f.t.Fatalf("no fixture value for %s in %s", path[i], strings.Join(path, "/"))
	return ""
}

func (f *matrixFixture) fill(pattern string, e entry, ghost bool) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") || p == "*" {
			parts[i] = url.PathEscape(f.pathValue(parts, i, e, ghost))
		}
	}
	return strings.Join(parts, "/")
}

var placeholder = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// fillQuery replaces {name} by the fixture's value.
func (f *matrixFixture) fillQuery(q string) string {
	return placeholder.ReplaceAllStringFunc(q, func(m string) string { return url.QueryEscape(f.str(m[1 : len(m)-1])) })
}

func (f *matrixFixture) fillBody(v any) any {
	switch b := v.(type) {
	case ref:
		return f.vals[string(b)]
	case map[string]any:
		out := map[string]any{}
		for k, x := range b {
			out[k] = f.fillBody(x)
		}
		return out
	case []any:
		out := make([]any, len(b))
		for i, x := range b {
			out[i] = f.fillBody(x)
		}
		return out
	}
	return v
}

// route is a method and pattern of the router with the operation id of the API, if it is one.
type route struct {
	method, pattern, id string
}

func (r route) name() string {
	if r.id != "" {
		return r.id
	}
	return r.method + " " + r.pattern
}

// routes lists every route of the server.
func routes(t *testing.T, env *apitest.Env) []route {
	ids := map[string]string{}
	for path, item := range env.App.API().OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPost: item.Post,
			http.MethodPut: item.Put, http.MethodPatch: item.Patch, http.MethodDelete: item.Delete} {
			if op != nil {
				ids[method+" "+path] = op.OperationID
			}
		}
	}
	var out []route
	err := chi.Walk(env.App.Routes(), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodHead || method == http.MethodOptions || method == http.MethodConnect || method == http.MethodTrace {
			return nil
		}
		out = append(out, route{method: method, pattern: pattern, id: ids[method+" "+pattern]})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// In a stable order; signing out ends the session the other calls use, so it comes last.
	sort.Slice(out, func(i, j int) bool {
		if li, lj := out[i].id == "logout", out[j].id == "logout"; li != lj {
			return lj
		}
		return out[i].method+" "+out[i].pattern < out[j].method+" "+out[j].pattern
	})
	return out
}

// request is a call of a route as it was sent.
type request struct {
	method, path, body string
}

// result is the answer of one call.
type result struct {
	status   int
	code     string
	body     string
	location string
}

func (r result) String() string { return fmt.Sprintf("%d %s %.300s", r.status, r.code, r.body) }

// call calls a route as a principal; with ghost, the path names objects that do not exist.
func (f *matrixFixture) call(who string, c *apitest.Client, r route, e entry, ghost bool) (request, result) {
	path := f.fill(r.pattern, e, ghost)
	if e.query != "" {
		q := f.fillQuery(e.query)
		if ghost {
			q = strings.ReplaceAll(q, key, ghostKey)
		}
		path += "?" + q
	}
	var data []byte
	contentType := ""
	switch b := e.body.(type) {
	case nil:
	case upload:
		var rd io.Reader
		rd, contentType = multipartBody(b.name, b.content)
		data, _ = io.ReadAll(rd)
	case raw:
		data, contentType = []byte(f.fillQuery(b.content)), b.contentType
	default:
		var err error
		if data, err = json.Marshal(f.fillBody(b)); err != nil {
			f.t.Fatal(err)
		}
		contentType = "application/json"
	}
	if ghost {
		data = bytes.ReplaceAll(data, []byte(key), []byte(ghostKey))
	}
	sent := request{method: r.method, path: path, body: string(data)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.method, f.env.URL+path, bytes.NewReader(data))
	if err != nil {
		f.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if e.git {
		req.Header.Set("Accept", contentType)
		if tok := f.tokens[who]; tok != "" {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(who+":"+tok)))
		}
	}
	res, err := c.Send(req)
	if err != nil {
		f.t.Fatalf("%s %s as %s: %v", r.method, path, who, err)
	}
	defer res.Body.Close()
	var body []byte
	if e.stream {
		body = make([]byte, 4096)
		n, _ := io.ReadAtLeast(res.Body, body, 1)
		body = body[:n]
	} else {
		body, _ = io.ReadAll(res.Body)
	}
	out := result{status: res.StatusCode, body: string(body), location: res.Header.Get("Location")}
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	out.code = p.Code
	return sent, out
}

// global reports whether the entry needs a global permission.
func (e entry) global() bool { return authz.IsGlobalPermission(e.need) }

// onResource reports whether the entry needs access to an object of the fixture.
func (e entry) onResource() bool {
	return e.need != anyone && e.need != signedIn && e.need != agentKey && !e.global()
}

// permitted reports whether a principal may do what the entry needs.
func permitted(who string, e entry) bool {
	switch e.need {
	case anyone:
		return true
	case signedIn:
		return who != anonymous
	case owner, agentKey, deployer:
		return false
	}
	if e.global() {
		return who != anonymous && slices.Contains(signedInHolds, e.need)
	}
	return who == reader && slices.Contains(readerHolds, e.need)
}

func (e entry) succeeded(status int) bool {
	return status >= 200 && status <= 299 || slices.Contains(e.found, status)
}

func denial(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound
}

// leaks reports what an answer to an outsider or anonymous client shows of the fixture: its content (the marker,
// anywhere) or the key of its containers (unless the request named it).
func leaks(req request, res result) string {
	answer := strings.ToUpper(res.body + " " + res.location)
	if strings.Contains(answer, marker) {
		return "the fixture's content"
	}
	if !strings.Contains(req.path+req.body, key) && strings.Contains(answer, key) {
		return "the key " + key
	}
	return ""
}

func TestPermissionMatrix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture's pipeline uses sh")
	}
	env := apitest.New(t)
	f := newMatrixFixture(t, env)
	all := routes(t, env)
	seen := map[string]bool{}
	for _, r := range all {
		seen[r.name()] = true
		if _, ok := matrix[r.name()]; !ok {
			t.Errorf("%s %s (%s) has no entry in the permission matrix: decide what it needs", r.method, r.pattern, r.name())
		}
	}
	for name := range matrix {
		if !seen[name] {
			t.Errorf("the permission matrix names %q, which is not a route", name)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	for _, r := range all {
		for _, e := range append([]entry{matrix[r.name()]}, matrix[r.name()].alt...) {
			f.check(t, r, e)
		}
	}

	// Administrators manage permissions and configuration, not what projects hold: the global
	// administrator, who has no grants on the fixture, reads none of its content.
	for _, path := range []string{"/api/v1/builds/variables?scope=project&project=QZXV", "/api/v1/builds/variables?" + repoVars,
		"/api/v1/code/repos/QZXV/app/file?ref=main&path=secret.txt", "/api/v1/issues/QZXV-1",
		"/api/v1/wiki/pages/" + f.str("page"), "/api/v1/wiki/templates?space=QZXV"} {
		r := f.admin.Do(http.MethodGet, path, nil)
		if strings.Contains(strings.ToUpper(string(r.Body)), marker) {
			t.Errorf("GET %s as the administrator: %d %.300s", path, r.Status, r.Body)
		}
	}
}

// check calls a route as the principals of the matrix and checks the answers against the entry.
func (f *matrixFixture) check(t *testing.T, r route, e entry) {
	// The positive check of every reading route: lena, who owns the fixture, or an administrator gets an
	// answer, so that the denials below are about permissions, not about wrong paths.
	if r.method == http.MethodGet && e.need != agentKey {
		c, who := f.lena, "lena"
		if e.global() {
			c, who = f.admin, "admin"
		}
		if _, res := f.call(who, c, r, e, false); !e.succeeded(res.status) {
			t.Errorf("GET %s as %s: %s", r.name(), who, res)
		}
	}
	for _, who := range []string{anonymous, outsider, reader} {
		req, res := f.call(who, f.clients[who], r, e, false)
		where := fmt.Sprintf("%s %s?%s as %s (needs %s)", r.method, r.name(), e.query, who, e.need)
		if res.status >= 500 {
			t.Errorf("%s: server error %s", where, res)
			continue
		}
		if who != reader {
			if what := leaks(req, res); what != "" {
				t.Errorf("%s: the answer shows %s: %s", where, what, res)
			}
		}
		switch ok := permitted(who, e); {
		case ok && r.method == http.MethodGet:
			if !e.succeeded(res.status) {
				t.Errorf("%s: may, got %s", where, res)
			}
		case ok:
			if who != anonymous && res.status == http.StatusUnauthorized && !e.succeeded(res.status) ||
				who == reader && e.onResource() && denial(res.status) && !e.succeeded(res.status) {
				t.Errorf("%s: may, but was denied: %s", where, res)
			}
		case who == anonymous:
			if !denial(res.status) || (e.need == signedIn || e.global()) && res.status != http.StatusUnauthorized {
				t.Errorf("%s: anonymous clients may not, got %s", where, res)
			}
		case e.need == agentKey:
			if res.status != http.StatusUnauthorized && res.status != http.StatusForbidden {
				t.Errorf("%s: only agents may, got %s", where, res)
			}
		case e.global():
			if res.status != http.StatusForbidden {
				t.Errorf("%s: users without %s get 403, got %s", where, e.need, res)
			}
		default:
			if res.status != http.StatusForbidden && res.status != http.StatusNotFound {
				t.Errorf("%s: may not, got %s", where, res)
			}
		}
		// Outsiders cannot tell the fixture's objects from objects that do not exist.
		if who == outsider && e.onResource() && !permitted(who, e) {
			_, none := f.call(who, f.clients[who], r, e, true)
			if none.status != res.status || none.code != res.code {
				t.Errorf("%s: outsiders can tell it from what does not exist: %d %s, but %d %s for nothing: %.200s", where,
					res.status, res.code, none.status, none.code, res.body)
			}
		}
	}
}
