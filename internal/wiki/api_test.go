// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"bytes"
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

type space struct {
	ID          int64    `json:"id"`
	Key         string   `json:"key"`
	HomePageID  int64    `json:"homePageId"`
	Permissions []string `json:"permissions"`
}

type page struct {
	ID          int64                `json:"id"`
	Title       string               `json:"title"`
	Status      string               `json:"status"`
	Version     int                  `json:"version"`
	ParentID    int64                `json:"parentId"`
	Body        map[string]any       `json:"body"`
	Labels      []string             `json:"labels"`
	Restricted  bool                 `json:"restricted"`
	Ancestors   []struct{ ID int64 } `json:"ancestors"`
	Permissions map[string]bool      `json:"permissions"`
}

func doc(text string) map[string]any {
	return map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": text}}}}}
}

func id(i int64) string { return strconv.FormatInt(i, 10) }

type fixture struct {
	env              *apitest.Env
	admin            *apitest.Client
	lena, dirk, otto *apitest.Client
	ids              map[string]int64
	space            space
}

func setup(t *testing.T) *fixture {
	t.Helper()
	env := apitest.New(t)
	f := &fixture{env: env, admin: env.Admin(), ids: map[string]int64{}}
	f.lena, f.dirk, f.otto = env.CreateUser("lena"), env.CreateUser("dirk"), env.CreateUser("otto")
	for name, c := range map[string]*apitest.Client{"lena": f.lena, "dirk": f.dirk, "otto": f.otto} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		f.ids[name] = me.User.ID
	}
	f.admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": "space.create", "principalType": "user", "principalId": f.ids["lena"]}, nil)
	f.lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "docs", "name": "Documentation"}, &f.space)
	return f
}

// publishNew creates and publishes a page.
func (f *fixture) publishNew(t *testing.T, c *apitest.Client, title string, parent int64, text string) page {
	t.Helper()
	var p page
	c.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": title, "parentId": parent}, &p)
	c.Post("/api/v1/wiki/pages/"+id(p.ID)+"/publish", map[string]any{"title": title, "body": doc(text), "baseVersion": 0}, &p)
	return p
}

func (f *fixture) search(t *testing.T, c *apitest.Client, q string) []string {
	t.Helper()
	var res struct {
		Pages []struct {
			Title   string `json:"title"`
			Excerpt string `json:"excerpt"`
		} `json:"pages"`
	}
	c.Get("/api/v1/wiki/search?q="+url.QueryEscape(q), &res)
	out := []string{}
	for _, p := range res.Pages {
		out = append(out, p.Title)
	}
	return out
}

func TestSpacesAndPermissions(t *testing.T) {
	f := setup(t)
	if f.space.Key != "DOCS" || f.space.HomePageID == 0 || len(f.space.Permissions) != 12 {
		t.Fatalf("space: %+v", f.space)
	}
	// Open spaces let signed-in users read and edit; spaces need space.create.
	var sp space
	f.dirk.Get("/api/v1/wiki/spaces/docs", &sp)
	if !contains(sp.Permissions, "page.edit") || contains(sp.Permissions, "space.admin") {
		t.Fatalf("dirk's permissions: %v", sp.Permissions)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "X", "name": "X"}); r.Status != http.StatusForbidden {
		t.Fatalf("space without permission: %d", r.Status)
	}
	f.lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "HR", "name": "Human resources", "open": false}, &sp)
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces/HR", nil); r.Status != http.StatusNotFound {
		t.Fatalf("closed space visible: %d", r.Status)
	}
	var list struct {
		Total int     `json:"total"`
		Items []space `json:"items"`
	}
	f.dirk.Get("/api/v1/wiki/spaces", &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Key != "DOCS" {
		t.Fatalf("dirk's spaces: %+v", list)
	}
	// Space administrators manage grants and must keep an administrator.
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/spaces/DOCS/permissions", nil); r.Status != http.StatusForbidden {
		t.Fatalf("non-admin reads grants: %d", r.Status)
	}
	grants := []map[string]any{
		{"permission": "space.admin", "holder": map[string]any{"type": "user", "id": f.ids["lena"]}},
		{"permission": "space.view", "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}},
	}
	if r := f.lena.Do(http.MethodPut, "/api/v1/wiki/spaces/HR/permissions", grants[1:]); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("grants without admin: %d", r.Status)
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/HR/permissions", grants, nil)
	f.dirk.Get("/api/v1/wiki/spaces/HR", &sp)
	if !contains(sp.Permissions, "space.view") || contains(sp.Permissions, "page.edit") {
		t.Fatalf("dirk in HR: %v", sp.Permissions)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces/HR/pages", map[string]any{"title": "Nope"}); r.Status != http.StatusForbidden {
		t.Fatalf("view-only user created a page: %d", r.Status)
	}
	// Personal space.
	f.dirk.Post("/api/v1/wiki/spaces/personal", nil, &sp)
	if sp.Key != "~DIRK" {
		t.Fatalf("personal space: %+v", sp)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/spaces/personal", nil); r.Status != http.StatusConflict {
		t.Fatalf("second personal space: %d", r.Status)
	}
	// Archived spaces are read-only.
	f.lena.MustDo(http.MethodPatch, "/api/v1/wiki/spaces/HR", map[string]any{"archived": true}, nil)
	f.lena.Get("/api/v1/wiki/spaces/HR", &sp)
	if contains(sp.Permissions, "page.edit") || !contains(sp.Permissions, "space.view") {
		t.Fatalf("archived space permissions: %v", sp.Permissions)
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

func TestDraftsPublishingAndVersions(t *testing.T) {
	f := setup(t)
	var p page
	f.lena.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Installation guide", "parentId": f.space.HomePageID}, &p)
	if p.Status != "draft" || p.Version != 0 {
		t.Fatalf("new page: %+v", p)
	}
	pid := id(p.ID)
	// Drafts are private.
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+pid, nil); r.Status != http.StatusNotFound {
		t.Fatalf("draft visible to others: %d", r.Status)
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pid+"/draft", map[string]any{"title": "Installation guide", "body": doc("Draft text"), "baseVersion": 0}, nil)
	var d struct {
		Saved bool           `json:"saved"`
		Body  map[string]any `json:"body"`
	}
	f.lena.Get("/api/v1/wiki/pages/"+pid+"/draft", &d)
	if !d.Saved {
		t.Fatalf("draft: %+v", d)
	}
	f.lena.Post("/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Installation guide",
		"body": doc("Install the agent first, then the agent config. See SHOP-1."), "baseVersion": 0}, &p)
	if p.Status != "current" || p.Version != 1 || len(p.Ancestors) != 1 {
		t.Fatalf("published: %+v", p)
	}
	f.dirk.Get("/api/v1/wiki/pages/"+pid, &p)

	// Titles are unique per space.
	var dup page
	f.dirk.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "installation GUIDE"}, &dup)
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(dup.ID)+"/publish", map[string]any{"title": "installation GUIDE",
		"body": doc("x"), "baseVersion": 0}); r.Status != http.StatusConflict || r.Code() != "page.title-taken" {
		t.Fatalf("duplicate title: %d %s", r.Status, r.Body)
	}
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(dup.ID)+"/draft", nil, nil)

	// Concurrent edits: the second publisher based on an old version gets a conflict.
	f.lena.Post("/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Installation guide", "body": doc("Lena's text"), "baseVersion": 1}, &p)
	r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Installation guide", "body": doc("Dirk's text"), "baseVersion": 1})
	if r.Status != http.StatusConflict || r.Code() != "page.conflict" {
		t.Fatalf("conflict: %d %s", r.Status, r.Body)
	}
	var versions struct {
		Total int `json:"total"`
		Items []struct {
			Version int `json:"version"`
		} `json:"items"`
	}
	f.dirk.Get("/api/v1/wiki/pages/"+pid+"/versions", &versions)
	if versions.Total != 2 || len(versions.Items) != 2 || versions.Items[0].Version != 2 {
		t.Fatalf("versions: %+v", versions)
	}
	f.dirk.Post("/api/v1/wiki/pages/"+pid+"/versions/1/restore", nil, &p)
	if p.Version != 3 || !strings.Contains(textOf(p.Body), "agent config") {
		t.Fatalf("restored: %+v", p)
	}

	// The page mentions SHOP-1 and is found by its text.
	var mentions []struct{ Title string }
	f.dirk.Get("/api/v1/wiki/mentions?issue=SHOP-1", &mentions)
	if len(mentions) != 1 {
		t.Fatalf("mentions: %+v", mentions)
	}
	if got := f.search(t, f.dirk, "agent"); len(got) != 1 || got[0] != "Installation guide" {
		t.Fatalf("search: %v", got)
	}
	// Tree and children.
	var tree struct {
		Total int `json:"total"`
		Items []struct {
			ID          int64 `json:"id"`
			HasChildren bool  `json:"hasChildren"`
		} `json:"items"`
	}
	f.dirk.Get("/api/v1/wiki/spaces/DOCS/tree", &tree)
	if tree.Total != 1 || len(tree.Items) != 1 || tree.Items[0].ID != f.space.HomePageID || !tree.Items[0].HasChildren {
		t.Fatalf("top level: %+v", tree)
	}
	f.dirk.Get("/api/v1/wiki/spaces/DOCS/tree?parent="+id(f.space.HomePageID), &tree)
	if tree.Total != 1 || len(tree.Items) != 1 || tree.Items[0].ID != p.ID {
		t.Fatalf("children: %+v", tree)
	}
	// Export.
	exp := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+pid+"/export?format=md", nil)
	if exp.Status != http.StatusOK || !strings.HasPrefix(string(exp.Body), "# Installation guide") {
		t.Fatalf("export: %d %s", exp.Status, exp.Body)
	}
}

// TestFormulaSearch: formulas are found by their LaTeX, which results show as Markdown writes it.
func TestFormulaSearch(t *testing.T) {
	f := setup(t)
	geo := f.publishNew(t, f.lena, "Geometry", f.space.HomePageID, "x")
	publishBody(t, f.lena, geo, map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": "The diagonal is "}, map[string]any{"type": "inlineMath", "attrs": map[string]any{"latex": `\sqrt{2}`}}}}}})
	var found struct {
		Pages []struct {
			Title   string `json:"title"`
			Excerpt string `json:"excerpt"`
		} `json:"pages"`
	}
	f.dirk.Get("/api/v1/wiki/search?q=sqrt", &found)
	if len(found.Pages) != 1 || found.Pages[0].Title != "Geometry" || !strings.Contains(found.Pages[0].Excerpt, `diagonal is $\`) {
		t.Fatalf("search for a formula: %+v", found)
	}
}

func textOf(body map[string]any) string {
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if s, ok := x["text"].(string); ok {
				b.WriteString(s)
			}
			walk(x["content"])
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(body)
	return b.String()
}

func TestRestrictionsTreeAndTrash(t *testing.T) {
	f := setup(t)
	parent := f.publishNew(t, f.lena, "Salaries", f.space.HomePageID, "Confidential numbers")
	child := f.publishNew(t, f.lena, "Bonus 2026", parent.ID, "Bonus rules")
	other := f.publishNew(t, f.dirk, "Team lunch", f.space.HomePageID, "Fridays")

	// View restrictions hide the page and its descendants, also in search.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(parent.ID)+"/restrictions", map[string]any{
		"view": []map[string]any{{"type": "user", "id": f.ids["lena"]}}, "edit": []any{}}, nil)
	for _, pid := range []int64{parent.ID, child.ID} {
		if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(pid), nil); r.Status != http.StatusNotFound {
			t.Fatalf("restricted page %d visible: %d", pid, r.Status)
		}
	}
	if got := f.search(t, f.dirk, "bonus"); len(got) != 0 {
		t.Fatalf("restricted page in search: %v", got)
	}
	var lp page
	f.lena.Get("/api/v1/wiki/pages/"+id(child.ID), &lp)
	if !lp.Restricted {
		t.Fatalf("inherited restriction not shown: %+v", lp)
	}
	// Moving the child out of the restricted parent makes it visible.
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(child.ID)+"/move", map[string]any{"parentId": f.space.HomePageID}, nil)
	f.dirk.Get("/api/v1/wiki/pages/"+id(child.ID), &lp)
	// A page cannot move below itself.
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(f.space.HomePageID)+"/move", map[string]any{"parentId": child.ID}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("cycle: %d %s", r.Status, r.Body)
	}
	// Edit restrictions apply to the page only.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(child.ID)+"/restrictions", map[string]any{
		"view": []any{}, "edit": []map[string]any{{"type": "user", "id": f.ids["lena"]}}}, nil)
	f.dirk.Get("/api/v1/wiki/pages/"+id(child.ID), &lp)
	if lp.Permissions["edit"] {
		t.Fatalf("edit-restricted page editable: %+v", lp.Permissions)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(child.ID)+"/publish", map[string]any{"title": "Bonus 2026", "body": doc("x"), "baseVersion": 1}); r.Status != http.StatusForbidden {
		t.Fatalf("edit-restricted publish: %d", r.Status)
	}

	// Trash: the author may delete their own page; it can be restored.
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(other.ID), nil, nil)
	var trash struct {
		Total int                  `json:"total"`
		Items []struct{ ID int64 } `json:"items"`
	}
	f.dirk.Get("/api/v1/wiki/spaces/DOCS/trash", &trash)
	if trash.Total != 1 || len(trash.Items) != 1 || trash.Items[0].ID != other.ID {
		t.Fatalf("trash: %+v", trash)
	}
	if r := f.otto.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(other.ID), nil); r.Status != http.StatusNotFound {
		t.Fatalf("trashed page visible: %d", r.Status)
	}
	if r := f.dirk.Do(http.MethodDelete, "/api/v1/wiki/pages/"+id(f.space.HomePageID), nil); r.Status != http.StatusForbidden {
		t.Fatalf("dirk deleted the home page of others: %d", r.Status)
	}
	f.dirk.Post("/api/v1/wiki/pages/"+id(other.ID)+"/restore", nil, &lp)
	if lp.Status != "current" {
		t.Fatalf("restored: %+v", lp)
	}
	// Space administrators purge.
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(other.ID), nil, nil)
	if r := f.dirk.Do(http.MethodDelete, "/api/v1/wiki/pages/"+id(other.ID)+"/purge", nil); r.Status != http.StatusForbidden {
		t.Fatalf("non-admin purged: %d", r.Status)
	}
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(other.ID)+"/purge", nil, nil)
}

func TestCommentsLabelsAttachmentsAndTemplates(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Agent setup", 0, "Install the agent first, then the agent config.")
	pid := id(p.ID)

	var c struct {
		ID         int64  `json:"id"`
		Anchor     string `json:"anchor"`
		AnchorText string `json:"anchorText"`
		Detached   bool   `json:"detached"`
	}
	f.dirk.Post("/api/v1/wiki/pages/"+pid+"/comments", map[string]any{"body": doc("Which version?")}, &c)
	f.lena.Post("/api/v1/wiki/pages/"+pid+"/comments", map[string]any{"body": doc("The latest."), "parentId": c.ID}, nil)
	f.dirk.Post("/api/v1/wiki/pages/"+pid+"/inline-comments", map[string]any{"body": doc("Link the download"), "text": "agent", "occurrence": 1}, &c)
	if c.Anchor == "" || c.AnchorText != "agent" || c.Detached {
		t.Fatalf("inline comment: %+v", c)
	}
	var pg page
	f.dirk.Get("/api/v1/wiki/pages/"+pid, &pg)
	if pg.Version != 1 || !strings.Contains(string(mustJSON(pg.Body)), c.Anchor) {
		t.Fatalf("anchor not in page: %+v", pg)
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/comments/"+id(c.ID)+"/resolved", nil, nil)
	var comments []struct {
		ID       int64   `json:"id"`
		ParentID int64   `json:"parentId"`
		Resolved *string `json:"resolved"`
		CanEdit  bool    `json:"canEdit"`
	}
	f.lena.Get("/api/v1/wiki/pages/"+pid+"/comments", &comments)
	if len(comments) != 3 || comments[1].ParentID != comments[0].ID || comments[2].Resolved == nil || comments[2].CanEdit {
		t.Fatalf("comments: %+v", comments)
	}
	if r := f.lena.Do(http.MethodPut, "/api/v1/wiki/comments/"+id(c.ID), map[string]any{"body": doc("edited")}); r.Status != http.StatusForbidden {
		t.Fatalf("edited someone else's comment: %d", r.Status)
	}
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/comments/"+id(c.ID), nil, nil)
	f.dirk.Get("/api/v1/wiki/pages/"+pid, &pg)
	if strings.Contains(string(mustJSON(pg.Body)), c.Anchor) {
		t.Fatal("anchor kept after deleting the inline comment")
	}

	// Labels.
	var labels []string
	f.dirk.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pid+"/labels", []string{"setup", "Linux", "setup"}, &labels)
	if strings.Join(labels, ",") != "linux,setup" {
		t.Fatalf("labels: %v", labels)
	}
	if r := f.dirk.Do(http.MethodPut, "/api/v1/wiki/pages/"+pid+"/labels", []string{"two words"}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid label: %d", r.Status)
	}
	var byLabel struct {
		Total int `json:"total"`
	}
	f.otto.Get("/api/v1/wiki/search?label=linux", &byLabel)
	if byLabel.Total != 1 {
		t.Fatalf("by label: %+v", byLabel)
	}

	// Attachments with versions.
	upload := func(c *apitest.Client, content string) *apitest.Response {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "setup.txt")
		_, _ = io.WriteString(fw, content)
		_ = mw.Close()
		return c.Do(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/attachments", &buf, "Content-Type", mw.FormDataContentType())
	}
	if r := upload(f.dirk, "one"); r.Status != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
	upload(f.dirk, "two")
	var atts []struct {
		ID      int64  `json:"id"`
		Version int    `json:"version"`
		URL     string `json:"url"`
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid+"/attachments", &atts)
	if len(atts) != 1 || atts[0].Version != 2 {
		t.Fatalf("attachments: %+v", atts)
	}
	if d := f.otto.Do(http.MethodGet, atts[0].URL, nil); string(d.Body) != "two" || !strings.Contains(d.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("download: %s", d.Body)
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid+"/attachments?all=true", &atts)
	if len(atts) != 2 {
		t.Fatalf("all versions: %+v", atts)
	}
	if r := f.otto.Do(http.MethodDelete, "/api/v1/wiki/attachments/"+id(atts[0].ID), nil); r.Status != http.StatusForbidden {
		t.Fatalf("otto deleted dirk's attachment: %d", r.Status)
	}
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/attachments/"+id(atts[0].ID), nil, nil)
	f.otto.Get("/api/v1/wiki/pages/"+pid+"/attachments?all=true", &atts)
	if len(atts) != 0 {
		t.Fatalf("after delete: %+v", atts)
	}

	// Templates: built-in ones exist; pages start from them.
	var templates []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Builtin bool   `json:"builtin"`
	}
	f.dirk.Get("/api/v1/wiki/templates?space=DOCS", &templates)
	if len(templates) < 7 || !templates[0].Builtin {
		t.Fatalf("templates: %+v", templates)
	}
	var fromTemplate page
	f.dirk.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "Weekly", "templateId": templates[0].ID}, &fromTemplate)
	if len(textOf(fromTemplate.Body)) == 0 || len(fromTemplate.Labels) == 0 {
		t.Fatalf("page from template: %+v", fromTemplate)
	}
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/templates", map[string]any{"name": "Global", "body": doc("x")}); r.Status != http.StatusForbidden {
		t.Fatalf("non-admin created a global template: %d", r.Status)
	}
	f.lena.Post("/api/v1/wiki/templates?space=DOCS", map[string]any{"name": "Runbook", "body": doc("Steps")}, nil)

	// Recently viewed pages.
	f.otto.Get("/api/v1/wiki/pages/"+pid, &pg)
	var recent []struct{ Title string }
	f.otto.Get("/api/v1/wiki/recent?mode=viewed", &recent)
	if len(recent) != 1 || recent[0].Title != "Agent setup" {
		t.Fatalf("recently viewed: %+v", recent)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TestMoveToAnotherSpace: moving a page to another space takes its descendants along; someone who cannot see or
// edit all of them (here: restricted below an open page) cannot move it, e.g. into their personal space, where
// they would be administrator.
func TestMoveToAnotherSpace(t *testing.T) {
	f := setup(t)
	team := f.publishNew(t, f.lena, "Team", f.space.HomePageID, "About the team")
	salaries := f.publishNew(t, f.lena, "Salaries", team.ID, "Confidential numbers")
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(salaries.ID)+"/restrictions", map[string]any{
		"view": []map[string]any{{"type": "user", "id": f.ids["lena"]}}, "edit": []any{}}, nil)
	var personal space
	f.dirk.Post("/api/v1/wiki/spaces/personal", nil, &personal)
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(team.ID)+"/move", map[string]any{"space": personal.Key}); r.Status != http.StatusForbidden {
		t.Fatalf("move with a hidden descendant: %d %s", r.Status, r.Body)
	}
	if r := f.dirk.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(salaries.ID), nil); r.Status != http.StatusNotFound {
		t.Fatalf("the restricted page became visible: %d", r.Status)
	}
	// Someone who sees and edits everything below may move it.
	var other space
	f.lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "hr", "name": "Human resources"}, &other)
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(team.ID)+"/move", map[string]any{"space": other.Key}, nil)
	var moved page
	f.lena.Get("/api/v1/wiki/pages/"+id(salaries.ID), &moved)
	if !moved.Restricted {
		t.Errorf("the moved page lost its restriction: %+v", moved)
	}
}

func TestPageOrderAndNesting(t *testing.T) {
	f := setup(t)
	home := f.space.HomePageID
	alpha := f.publishNew(t, f.lena, "Alpha", home, "a")
	beta := f.publishNew(t, f.lena, "Beta", home, "b")
	gamma := f.publishNew(t, f.lena, "Gamma", home, "c")
	children := func(parent int64) string {
		t.Helper()
		var nodes struct {
			Items []struct {
				Title string `json:"title"`
			} `json:"items"`
		}
		f.dirk.Get("/api/v1/wiki/spaces/DOCS/tree?parent="+id(parent), &nodes)
		titles := make([]string, len(nodes.Items))
		for i, n := range nodes.Items {
			titles[i] = n.Title
		}
		return strings.Join(titles, ",")
	}
	move := func(p page, body map[string]any) {
		t.Helper()
		f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(p.ID)+"/move", body, nil)
	}
	if got := children(home); got != "Alpha,Beta,Gamma" {
		t.Fatalf("new pages are not appended: %s", got)
	}
	// Reorder among siblings, then nest one page below another.
	move(gamma, map[string]any{"parentId": home, "before": alpha.ID})
	if got := children(home); got != "Gamma,Alpha,Beta" {
		t.Fatalf("before: %s", got)
	}
	move(alpha, map[string]any{"parentId": home, "after": beta.ID})
	if got := children(home); got != "Gamma,Beta,Alpha" {
		t.Fatalf("after: %s", got)
	}
	move(beta, map[string]any{"parentId": gamma.ID})
	move(alpha, map[string]any{"parentId": gamma.ID, "before": beta.ID})
	if got, below := children(home), children(gamma.ID); got != "Gamma" || below != "Alpha,Beta" {
		t.Fatalf("nesting: %s / %s", got, below)
	}
	var moved page
	f.dirk.Get("/api/v1/wiki/pages/"+id(beta.ID), &moved)
	if moved.ParentID != gamma.ID || len(moved.Ancestors) != 2 || moved.Ancestors[0].ID != home || moved.Ancestors[1].ID != gamma.ID {
		t.Fatalf("ancestors after the move: %+v", moved)
	}
	// The anchor must be a child of the new parent; a page cannot go below its own descendant.
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(alpha.ID)+"/move", map[string]any{"parentId": home, "before": beta.ID}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("anchor of another parent: %d %s", r.Status, r.Body)
	}
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(gamma.ID)+"/move", map[string]any{"parentId": beta.ID}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("move below a descendant: %d %s", r.Status, r.Body)
	}
	if got := children(gamma.ID); got != "Alpha,Beta" {
		t.Fatalf("a rejected move changed the order: %s", got)
	}
	// Blog posts and drafts are not part of the tree.
	var blog page
	f.lena.Post("/api/v1/wiki/spaces/DOCS/pages", map[string]any{"title": "News", "kind": "blog"}, &blog)
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(blog.ID)+"/move", map[string]any{"parentId": home}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("moved a blog post draft: %d %s", r.Status, r.Body)
	}
	// Moving needs edit permission: someone who may only read the space cannot rearrange it.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", []map[string]any{
		{"permission": "space.admin", "holder": map[string]any{"type": "user", "id": f.ids["lena"]}},
		{"permission": "space.view", "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}},
	}, nil)
	if r := f.dirk.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(alpha.ID)+"/move", map[string]any{"parentId": home}); r.Status != http.StatusForbidden {
		t.Fatalf("a reader moved a page: %d %s", r.Status, r.Body)
	}
	if got := children(gamma.ID); got != "Alpha,Beta" {
		t.Fatalf("the reader's move changed the tree: %s", got)
	}
}

// TestRestrictionsHold: restrictions cannot be lifted, dropped or got around by people they keep out — not by
// changing them while excluded by an edit restriction (audit M9), not by trashing a restricted parent (M10), not by
// moving a page between restricted and open parts of the tree (M11), and an uploader's delete removes only their
// own versions of an attachment (M12). The trash lists only what its reader may see (L20).
func TestRestrictionsHold(t *testing.T) {
	f := setup(t)
	home := f.space.HomePageID
	restrict := func(c *apitest.Client, pageID int64, view, edit []int64) *apitest.Response {
		t.Helper()
		holders := func(ids []int64) []map[string]any {
			out := []map[string]any{}
			for _, id := range ids {
				out = append(out, map[string]any{"type": "user", "id": id})
			}
			return out
		}
		return c.Do(http.MethodPut, "/api/v1/wiki/pages/"+id(pageID)+"/restrictions", map[string]any{"view": holders(view), "edit": holders(edit)})
	}
	sees := func(c *apitest.Client, pageID int64) bool {
		t.Helper()
		return c.Do(http.MethodGet, "/api/v1/wiki/pages/"+id(pageID), nil).Status == http.StatusOK
	}
	// Dirk may restrict pages (and edit, in this open space); Otto may only read.
	var grants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/DOCS/permissions", &grants)
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", append(grants,
		map[string]any{"permission": "page.restrict", "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}}), nil)

	// M9: an edit restriction keeps Dirk out — he cannot lift it.
	minutes := f.publishNew(t, f.lena, "Board minutes", home, "Decisions")
	restrict(f.lena, minutes.ID, nil, []int64{f.ids["lena"]}).Decode(&struct{}{})
	if r := restrict(f.dirk, minutes.ID, nil, nil); r.Code() != "page.restrict-denied" {
		t.Fatalf("Dirk lifted an edit restriction that keeps him out: %d %s", r.Status, r.Body)
	}

	// M10: a restricted page takes its child pages along to the trash, and back.
	salaries := f.publishNew(t, f.lena, "Salaries", home, "Numbers")
	bonus := f.publishNew(t, f.lena, "Bonus 2026", salaries.ID, "Rules")
	restrict(f.lena, salaries.ID, []int64{f.ids["lena"]}, nil).Decode(&struct{}{})
	if sees(f.dirk, bonus.ID) {
		t.Fatal("the child of a restricted page is visible")
	}
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+id(salaries.ID), nil, nil)
	if sees(f.dirk, bonus.ID) {
		t.Fatal("trashing the restricted parent showed its child")
	}
	var trash struct {
		Total int `json:"total"`
		Items []struct {
			Title    string `json:"title"`
			Children int    `json:"children"`
		} `json:"items"`
	}
	f.lena.Get("/api/v1/wiki/spaces/DOCS/trash", &trash)
	if trash.Total != 1 || len(trash.Items) != 1 || trash.Items[0].Title != "Salaries" || trash.Items[0].Children != 1 {
		t.Fatalf("trash: %+v", trash)
	}
	f.dirk.Get("/api/v1/wiki/spaces/DOCS/trash", &trash)
	for _, tp := range trash.Items {
		if tp.Title == "Salaries" || tp.Title == "Bonus 2026" {
			t.Fatalf("the trash shows Dirk a restricted page: %+v", trash)
		}
	}
	if trash.Total != len(trash.Items) {
		t.Fatalf("the trash counts for Dirk what it does not show him: %+v", trash)
	}
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(bonus.ID)+"/restore", nil); r.Code() != "page.trashed-with-parent" {
		t.Fatalf("restored the child alone: %d %s", r.Status, r.Body)
	}
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(salaries.ID)+"/restore", nil, nil)
	var restored page
	f.lena.Get("/api/v1/wiki/pages/"+id(bonus.ID), &restored)
	if restored.Status != "current" || restored.ParentID != salaries.ID || sees(f.dirk, bonus.ID) {
		t.Fatalf("the child after the restore: %+v (Dirk sees it: %v)", restored, sees(f.dirk, bonus.ID))
	}

	// M11: Otto may edit and move pages but not restrict them — so not move them to where other people may see them.
	open := f.publishNew(t, f.otto, "Team notes", home, "Notes")
	private := f.publishNew(t, f.lena, "Private", home, "Lena, Dirk and Otto")
	restrict(f.lena, private.ID, []int64{f.ids["lena"], f.ids["dirk"], f.ids["otto"]}, nil).Decode(&struct{}{})
	if r := f.otto.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(open.ID)+"/move", map[string]any{"parentId": private.ID}); r.Code() != "page.move-restricted" {
		t.Fatalf("Otto restricted a page by moving it: %d %s", r.Status, r.Body)
	}
	inner := f.publishNew(t, f.otto, "Private notes", private.ID, "Inside")
	if r := f.otto.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(inner.ID)+"/move", map[string]any{"parentId": home}); r.Code() != "page.move-restricted" {
		t.Fatalf("Otto opened a restricted page by moving it: %d %s", r.Status, r.Body)
	}
	if outsider := f.env.CreateUser("ute"); sees(outsider, inner.ID) {
		t.Fatal("the restricted page became visible")
	}
	sibling := f.publishNew(t, f.otto, "Meeting notes", home, "Notes")
	f.otto.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(sibling.ID)+"/move", map[string]any{"parentId": open.ID}, nil)
	// Dirk, who may restrict pages, may move them there.
	f.dirk.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+id(open.ID)+"/move", map[string]any{"parentId": private.ID}, nil)

	// M12: Otto uploaded a file, Dirk a new version of it; Dirk's delete removes his version only.
	upload := func(c *apitest.Client, pageID int64, content string) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "plan.txt")
		_, _ = io.WriteString(fw, content)
		_ = mw.Close()
		if r := c.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(pageID)+"/attachments", &buf, "Content-Type", mw.FormDataContentType()); r.Status != http.StatusCreated {
			t.Fatalf("upload: %d %s", r.Status, r.Body)
		}
	}
	upload(f.otto, open.ID, "otto's plan")
	upload(f.dirk, open.ID, "dirk's plan")
	var atts []struct {
		ID      int64 `json:"id"`
		Version int   `json:"version"`
	}
	f.lena.Get("/api/v1/wiki/pages/"+id(open.ID)+"/attachments", &atts)
	f.dirk.MustDo(http.MethodDelete, "/api/v1/wiki/attachments/"+id(atts[0].ID), nil, nil)
	f.lena.Get("/api/v1/wiki/pages/"+id(open.ID)+"/attachments?all=true", &atts)
	if len(atts) != 1 || atts[0].Version != 1 {
		t.Fatalf("Dirk's delete removed other people's versions: %+v", atts)
	}
}

// TestPagesChangeOnlyWithTheirRestrictions: edit restrictions protect a page from trashing, archiving and the
// deletion of attachments (audit L19), archived pages take no uploads (L25), replies of others survive their
// thread (L24), children one cannot see are not counted (L26), and administrators who may repair a space's
// grants do not read its templates (L21).
func TestPagesChangeOnlyWithTheirRestrictions(t *testing.T) {
	f := setup(t)
	home := f.space.HomePageID
	var grants []map[string]any
	f.lena.Get("/api/v1/wiki/spaces/DOCS/permissions", &grants)
	for _, perm := range []string{"page.delete", "page.archive", "attachment.delete"} {
		grants = append(grants, map[string]any{"permission": perm, "holder": map[string]any{"type": "user", "id": f.ids["dirk"]}})
	}
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/spaces/DOCS/permissions", grants, nil)
	upload := func(c *apitest.Client, pageID int64) *apitest.Response {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, _ := w.CreateFormFile("file", "plan.txt")
		_, _ = part.Write([]byte("plan"))
		_ = w.Close()
		return c.Do(http.MethodPost, "/api/v1/wiki/pages/"+id(pageID)+"/attachments", &buf, "Content-Type", w.FormDataContentType())
	}

	// L19: Lena keeps the page to herself for editing; Dirk's space permissions do not reach it.
	plan := f.publishNew(t, f.lena, "Plan", home, "Steps")
	var attachments []struct {
		ID int64 `json:"id"`
	}
	upload(f.lena, plan.ID).Decode(&attachments)
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(plan.ID)+"/restrictions", map[string]any{"view": []any{},
		"edit": []map[string]any{{"type": "user", "id": f.ids["lena"]}}}, nil)
	for what, r := range map[string]*apitest.Response{
		"trashed":            f.dirk.Do(http.MethodDelete, "/api/v1/wiki/pages/"+id(plan.ID), nil),
		"archived":           f.dirk.Do(http.MethodPut, "/api/v1/wiki/pages/"+id(plan.ID)+"/archived", nil),
		"deleted a file of":  f.dirk.Do(http.MethodDelete, "/api/v1/wiki/attachments/"+id(attachments[0].ID), nil),
		"uploaded a file to": upload(f.dirk, plan.ID),
	} {
		if r.Status != http.StatusForbidden {
			t.Errorf("Dirk %s an edit-restricted page: %d %s", what, r.Status, r.Body)
		}
	}

	// L25: archived pages take no uploads, not even from administrators.
	old := f.publishNew(t, f.lena, "Old plan", home, "Steps")
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(old.ID)+"/archived", nil, nil)
	if r := upload(f.lena, old.ID); r.Status != http.StatusForbidden {
		t.Errorf("an archived page took an upload: %d %s", r.Status, r.Body)
	}

	// L24: Otto's comment has Dirk's reply; Otto cannot take the reply with it, an administrator can.
	talk := f.publishNew(t, f.lena, "Next plan", home, "Steps")
	var comment struct {
		ID int64 `json:"id"`
	}
	f.otto.Post("/api/v1/wiki/pages/"+id(talk.ID)+"/comments", map[string]any{"body": doc("Is this still valid?")}, &comment)
	f.dirk.Post("/api/v1/wiki/pages/"+id(talk.ID)+"/comments", map[string]any{"body": doc("No."), "parentId": comment.ID}, nil)
	if r := f.otto.Do(http.MethodDelete, "/api/v1/wiki/comments/"+id(comment.ID), nil); r.Code() != "comment.has-replies" {
		t.Errorf("Otto deleted Dirk's reply with his comment: %d %s", r.Status, r.Body)
	}
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/comments/"+id(comment.ID), nil, nil)

	// L26: children one cannot see are not counted.
	parent := f.publishNew(t, f.lena, "Projects", home, "All projects")
	f.publishNew(t, f.lena, "Public project", parent.ID, "Open")
	secret := f.publishNew(t, f.lena, "Secret project", parent.ID, "Closed")
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+id(secret.ID)+"/restrictions", map[string]any{
		"view": []map[string]any{{"type": "user", "id": f.ids["lena"]}}, "edit": []any{}}, nil)
	var counted struct {
		Children int `json:"children"`
	}
	f.dirk.Get("/api/v1/wiki/pages/"+id(parent.ID), &counted)
	if counted.Children != 1 {
		t.Errorf("Dirk counts %d children", counted.Children)
	}

	// L21: the administrator sees the closed space (to repair its grants), not its templates.
	var sp space
	f.lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "HR", "name": "Human resources", "open": false}, &sp)
	f.lena.Post("/api/v1/wiki/templates?space=HR", map[string]any{"name": "Dismissal letter", "body": doc("Dear ...")}, nil)
	r := f.admin.MustDo(http.MethodGet, "/api/v1/wiki/templates?space=HR", nil, nil)
	if strings.Contains(string(r.Body), "Dismissal") {
		t.Errorf("the administrator reads the templates of a space he cannot see: %s", r.Body)
	}
	r = f.lena.MustDo(http.MethodGet, "/api/v1/wiki/templates?space=HR", nil, nil)
	if !strings.Contains(string(r.Body), "Dismissal") {
		t.Errorf("the space's administrator lacks its template: %s", r.Body)
	}
}
