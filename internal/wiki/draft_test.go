// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/testutil/apitest"
)

type editSession struct {
	Version      int            `json:"version"`
	Doc          map[string]any `json:"doc"`
	Title        string         `json:"title"`
	BaseVersion  int            `json:"baseVersion"`
	Changed      bool           `json:"changed"`
	Contributors []struct {
		Username string `json:"username"`
	} `json:"contributors"`
}

func insertText(pos int, text string) map[string]any {
	return map[string]any{"stepType": "replace", "from": pos, "to": pos,
		"slice": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}}
}

func steps(version int, client string, list ...map[string]any) map[string]any {
	return map[string]any{"version": version, "clientId": client, "steps": list}
}

// TestSharedDrafts: everyone editing a page edits one draft. Steps build on the draft's version: a step on an older
// version is refused, and its editor catches up and sends it again; the server applies the steps itself, refuses
// what does not apply or what documents may not contain, and publishes the draft as of a version, with who wrote
// it. A body published through the API does not overwrite the unpublished changes of others; the draft follows
// versions written another way.
func TestSharedDrafts(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Runbook", f.space.HomePageID, "Restart")
	pid := id(p.ID)
	edit := "/api/v1/wiki/pages/" + pid + "/edit"
	// Lena and Dirk edit the page; Otto reads it.
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pid+"/restrictions", map[string]any{"view": []any{},
		"edit": []map[string]any{{"type": "user", "id": f.ids["lena"]}, {"type": "user", "id": f.ids["dirk"]}}}, nil)
	lena, dirk := fmt.Sprintf("%d:tab", f.ids["lena"]), fmt.Sprintf("%d:tab", f.ids["dirk"])

	var s1, s2 editSession
	f.lena.Get(edit, &s1)
	f.dirk.Get(edit, &s2)
	// Publishing the first version brought the draft to it: steps continue from there.
	v := s1.Version
	if s2.Version != v || textOf(s1.Doc) != "Restart" || s1.BaseVersion != 1 || s1.Changed || s1.Title != "Runbook" {
		t.Fatalf("joined: %+v", s1)
	}
	for _, path := range []string{edit, edit + "/steps?since=0"} {
		if r := f.otto.Do(http.MethodGet, path, nil); r.Status != http.StatusForbidden {
			t.Errorf("a reader edits: %s: %d %s", path, r.Status, r.Body)
		}
	}
	if r := f.otto.Do(http.MethodPost, edit+"/steps", steps(0, fmt.Sprintf("%d:x", f.ids["otto"]), insertText(1, "x"))); r.Status != http.StatusForbidden {
		t.Errorf("a reader changes the draft: %d %s", r.Status, r.Body)
	}

	// Lena types; Dirk's step on the same version is refused: he catches up and sends it again.
	var res struct {
		Version int `json:"version"`
	}
	f.lena.MustDo(http.MethodPost, edit+"/steps", steps(v, lena, insertText(8, " the agent")), &res)
	if res.Version != v+1 {
		t.Fatalf("version after Lena's step: %d", res.Version)
	}
	if r := f.dirk.Do(http.MethodPost, edit+"/steps", steps(v, dirk, insertText(1, "Please "))); r.Status != http.StatusConflict ||
		r.Code() != "draft.behind" {
		t.Fatalf("a step on an old version: %d %s", r.Status, r.Body)
	}
	var since struct {
		Version int `json:"version"`
		Steps   []struct {
			Version  int    `json:"version"`
			ClientID string `json:"clientId"`
		} `json:"steps"`
	}
	f.dirk.Get(edit+"/steps?since="+fmt.Sprint(v), &since)
	if since.Version != v+1 || len(since.Steps) != 1 || since.Steps[0].ClientID != lena {
		t.Fatalf("steps since %d: %+v", v, since)
	}
	f.dirk.MustDo(http.MethodPost, edit+"/steps", steps(v+1, dirk, insertText(1, "Please ")), &res)
	if res.Version != v+2 {
		t.Fatalf("version after Dirk's step: %d", res.Version)
	}

	// Editors send under their own id; steps must apply and bring nothing documents may not contain.
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{steps(v+2, lena, insertText(1, "x")), "draft.client"}, // Dirk under Lena's id
		{steps(v+2, dirk, insertText(999, "x")), "draft.step"},
		{steps(v+2, dirk, map[string]any{"stepType": "addMark", "from": 1, "to": 4,
			"mark": map[string]any{"type": "link", "attrs": map[string]any{"href": "javascript:alert(1)"}}}), "draft.content"},
		{steps(v+2, dirk, map[string]any{"stepType": "replace", "from": 1, "to": 1, "slice": map[string]any{"content": []any{
			map[string]any{"type": "paragraph"}}}}), "draft.step"}, // a paragraph within a paragraph
		{steps(v+2, dirk, map[string]any{"stepType": "teleport"}), "draft.step"},
	} {
		if r := f.dirk.Do(http.MethodPost, edit+"/steps", c.body); r.Status != http.StatusUnprocessableEntity || r.Code() != c.code {
			t.Errorf("%v: %d %s", c.body, r.Status, r.Body)
		}
	}

	f.dirk.MustDo(http.MethodPut, edit+"/title", map[string]any{"title": "Runbook of the agent", "clientId": dirk}, nil)
	f.lena.Get(edit, &s1)
	if s1.Version != v+2 || textOf(s1.Doc) != "Please Restart the agent" || s1.Title != "Runbook of the agent" || !s1.Changed ||
		len(s1.Contributors) != 2 {
		t.Fatalf("the draft: %+v", s1)
	}
	// Readers see the published version; editors learn of the unpublished changes.
	var view struct {
		Body  map[string]any `json:"body"`
		Draft *struct {
			BaseVersion int `json:"baseVersion"`
		} `json:"draft"`
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid, &view)
	if textOf(view.Body) != "Restart" || view.Draft != nil {
		t.Fatalf("Otto's view: %+v", view)
	}
	f.lena.Get("/api/v1/wiki/pages/"+pid, &view)
	if view.Draft == nil || view.Draft.BaseVersion != 1 {
		t.Fatalf("Lena's view: %+v", view)
	}

	// A body through the API does not overwrite Dirk's unpublished changes, nor does restoring a version.
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Runbook", "body": doc("API"),
		"baseVersion": 1}); r.Status != http.StatusConflict || r.Code() != "page.draft-changed" {
		t.Fatalf("API publish over the draft: %d %s", r.Status, r.Body)
	}
	if r := f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/versions/1/restore", nil); r.Status != http.StatusConflict ||
		r.Code() != "page.draft-changed" {
		t.Fatalf("restore over the draft: %d %s", r.Status, r.Body)
	}

	// The draft is published as of a version: an older one is refused.
	publish := func(version int) *apitest.Response {
		return f.lena.Do(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Runbook of the agent",
			"draftVersion": version, "baseVersion": 1, "message": "Together"})
	}
	if r := publish(v + 1); r.Status != http.StatusConflict || r.Code() != "draft.behind" {
		t.Fatalf("publishing an old draft version: %d %s", r.Status, r.Body)
	}
	if r := publish(v + 2); r.Status != http.StatusOK {
		t.Fatalf("publish: %d %s", r.Status, r.Body)
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid, &view)
	if textOf(view.Body) != "Please Restart the agent" {
		t.Fatalf("published: %+v", view)
	}
	var history struct {
		Items []struct {
			Version int `json:"version"`
			Author  struct {
				Username string `json:"username"`
			} `json:"author"`
			Contributors []struct {
				Username string `json:"username"`
			} `json:"contributors"`
		} `json:"items"`
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid+"/versions", &history)
	if versions := history.Items; len(versions) != 2 || versions[0].Author.Username != "lena" || len(versions[0].Contributors) != 1 ||
		versions[0].Contributors[0].Username != "dirk" || len(versions[1].Contributors) != 0 {
		t.Fatalf("versions: %+v", history)
	}
	f.dirk.Get(edit, &s2)
	if s2.Changed || s2.BaseVersion != 2 || s2.Version != v+2 || len(s2.Contributors) != 0 {
		t.Fatalf("after publishing: %+v", s2)
	}

	// Discarding undoes the unpublished changes for everyone, as one more step.
	f.dirk.MustDo(http.MethodPost, edit+"/steps", steps(v+2, dirk, insertText(1, "Now: ")), nil)
	f.lena.MustDo(http.MethodDelete, "/api/v1/wiki/pages/"+pid+"/draft", nil, nil)
	f.dirk.Get(edit, &s2)
	if textOf(s2.Doc) != "Please Restart the agent" || s2.Changed || s2.Version != v+4 {
		t.Fatalf("after discarding: %+v", s2)
	}

	// Without unpublished changes, a body through the API is published and the draft follows; so does a restore.
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Runbook", "body": doc("Rewritten"),
		"baseVersion": 2}, nil)
	f.dirk.Get(edit, &s2)
	if textOf(s2.Doc) != "Rewritten" || s2.BaseVersion != 3 || s2.Changed || s2.Title != "Runbook" {
		t.Fatalf("after an API publish: %+v", s2)
	}
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/versions/1/restore", nil, nil)
	f.dirk.Get(edit, &s2)
	if textOf(s2.Doc) != "Restart" || s2.BaseVersion != 4 || s2.Changed {
		t.Fatalf("after a restore: %+v", s2)
	}

	// The document is the same when a node reads it from the snapshot and the steps since.
	version := s2.Version
	for i := 0; i < 120; i++ {
		f.dirk.MustDo(http.MethodPost, edit+"/steps", steps(version, dirk, insertText(1, "x")), &res)
		version = res.Version
	}
	f.env.App.Wiki.ForgetDraftDocs()
	f.lena.Get(edit, &s1)
	if s1.Version != version || textOf(s1.Doc) != strings.Repeat("x", 120)+"Restart" {
		t.Fatalf("after reading from the database: version %d, %q", s1.Version, textOf(s1.Doc))
	}
}

// formulasOf lists the formulas of a document as type:latex.
func formulasOf(body map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if t, _ := x["type"].(string); t == "math" || t == "inlineMath" {
				attrs, _ := x["attrs"].(map[string]any)
				latex, _ := attrs["latex"].(string)
				out = append(out, t+":"+latex)
			}
			walk(x["content"])
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(body)
	return out
}

// TestSharedDraftFormulas: formulas come into shared drafts as the editor inserts and changes them, by the rules of
// documents (where they may stand, how long their LaTeX may be); the draft read back from its steps, and the page
// published from it, hold them as they were.
func TestSharedDraftFormulas(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Physics", f.space.HomePageID, "E")
	pid := id(p.ID)
	edit := "/api/v1/wiki/pages/" + pid + "/edit"
	lena := fmt.Sprintf("%d:tab", f.ids["lena"])
	var s editSession
	f.lena.Get(edit, &s)
	v := s.Version
	formula := func(typ, latex string) map[string]any {
		return map[string]any{"type": typ, "attrs": map[string]any{"latex": latex}}
	}
	insert := func(pos int, node map[string]any) map[string]any {
		return map[string]any{"stepType": "replace", "from": pos, "to": pos, "slice": map[string]any{"content": []any{node}}}
	}
	setLatex := func(pos int, latex any) map[string]any {
		return map[string]any{"stepType": "attr", "pos": pos, "attr": "latex", "value": latex}
	}
	tooLong := strings.Repeat("x", docmodel.MaxLatex+1)

	// The draft is a paragraph "E": the formulas go into it and after it, not around it or into each other.
	for _, c := range []struct {
		step map[string]any
		code string
	}{
		{insert(0, formula("inlineMath", "x")), "draft.step"},
		{insert(1, formula("math", "x")), "draft.step"},
		{insert(3, map[string]any{"type": "math", "content": []any{map[string]any{"type": "text", "text": "y"}}}), "draft.content"},
		{insert(2, formula("inlineMath", tooLong)), "draft.content"},
		{insert(3, formula("math", "")), ""},
		{setLatex(3, tooLong), "draft.content"},
		{setLatex(3, 7), "draft.content"},
	} {
		if r := f.lena.Do(http.MethodPost, edit+"/steps", steps(v, lena, c.step)); c.code == "" && r.Status != http.StatusOK ||
			c.code != "" && (r.Status != http.StatusUnprocessableEntity || r.Code() != c.code) {
			t.Errorf("%.100v: %d %.200s", c.step, r.Status, r.Body)
		} else if c.code == "" {
			v++
		}
	}
	// The editor inserts an empty formula and fills it in; LaTeX longer than other texts is kept whole.
	long := `\int_0^1 x\,dx + ` + strings.Repeat(`\alpha+`, 500)
	f.lena.MustDo(http.MethodPost, edit+"/steps", steps(v, lena, setLatex(3, long), insert(2, formula("inlineMath", "=mc^2"))), nil)
	v += 2
	want := []string{"inlineMath:=mc^2", "math:" + long}
	f.env.App.Wiki.ForgetDraftDocs()
	f.lena.Get(edit, &s)
	if got := formulasOf(s.Doc); s.Version != v || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the draft read from its steps: version %d, %q", s.Version, got)
	}
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Physics", "draftVersion": v,
		"baseVersion": 1}, nil)
	var view struct {
		Body map[string]any `json:"body"`
	}
	f.otto.Get("/api/v1/wiki/pages/"+pid, &view)
	if got := formulasOf(view.Body); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("published: %q", got)
	}
	// A body with formulas published through the API becomes the draft.
	body := map[string]any{"type": "doc", "content": []any{formula("math", "a^2+b^2=c^2"),
		map[string]any{"type": "paragraph", "content": []any{formula("inlineMath", `\pi`)}}}}
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Physics", "body": body,
		"baseVersion": 2}, nil)
	f.lena.Get(edit, &s)
	if got := formulasOf(s.Doc); strings.Join(got, "|") != `math:a^2+b^2=c^2|inlineMath:\pi` {
		t.Fatalf("the draft after an API publish: %q", got)
	}
}

type socket struct {
	t  *testing.T
	ws *websocket.Conn
}

func dial(t *testing.T, env *apitest.Env, c *apitest.Client, pageID string, client string, version int, origin string) (*socket, error) {
	t.Helper()
	url := strings.Replace(env.URL, "http://", "ws://", 1) + fmt.Sprintf("/api/v1/wiki/pages/%s/edit/socket?version=%d&client=%s",
		pageID, version, client)
	cfg, err := websocket.NewConfig(url, origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.Cookies() {
		cfg.Header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &socket{t: t, ws: ws}, nil
}

func (s *socket) send(m map[string]any) {
	s.t.Helper()
	if err := websocket.JSON.Send(s.ws, m); err != nil {
		s.t.Fatal(err)
	}
}

// next returns the next message of a type, skipping others.
func (s *socket) next(typ string) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = s.ws.SetReadDeadline(deadline)
		var m map[string]any
		if err := websocket.JSON.Receive(s.ws, &m); err != nil {
			s.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m["t"] == typ {
			return m
		}
	}
	s.t.Fatalf("no %s", typ)
	return nil
}

// TestDraftSockets: editors connected by WebSocket send steps, carets and titles and hear the others', including
// publishing; only editors of the page may connect, and only from Kogge's own pages.
func TestDraftSockets(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Checklist", f.space.HomePageID, "Go")
	pid := id(p.ID)
	f.lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+pid+"/restrictions", map[string]any{"view": []any{},
		"edit": []map[string]any{{"type": "user", "id": f.ids["lena"]}, {"type": "user", "id": f.ids["dirk"]}}}, nil)
	lena, dirk := fmt.Sprintf("%d:a", f.ids["lena"]), fmt.Sprintf("%d:b", f.ids["dirk"])

	var joined editSession
	f.lena.Get("/api/v1/wiki/pages/"+pid+"/edit", &joined)
	v := joined.Version
	if _, err := dial(t, f.env, f.lena, pid, lena, v, "https://elsewhere.example"); err == nil {
		t.Fatal("a WebSocket from another origin was accepted")
	}
	if _, err := dial(t, f.env, f.otto, pid, fmt.Sprintf("%d:c", f.ids["otto"]), v, f.env.URL); err == nil {
		t.Fatal("a reader connected as editor")
	}
	if _, err := dial(t, f.env, f.dirk, pid, lena, v, f.env.URL); err == nil {
		t.Fatal("Dirk connected under Lena's editor id")
	}
	a, err := dial(t, f.env, f.lena, pid, lena, v, f.env.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dial(t, f.env, f.dirk, pid, dirk, v, f.env.URL)
	if err != nil {
		t.Fatal(err)
	}
	if w := a.next("welcome"); w["version"] != float64(v) {
		t.Fatalf("welcome: %v", w)
	}
	b.next("welcome")

	// Lena's step comes back to her (confirming it) and reaches Dirk.
	a.send(map[string]any{"t": "steps", "id": 1, "version": v, "steps": []any{insertText(3, "!")}})
	if ack := a.next("ack"); ack["id"] != float64(1) || ack["version"] != float64(v+1) {
		t.Fatalf("ack: %v", ack)
	}
	for _, s := range []*socket{a, b} {
		m := s.next("steps")
		list, _ := m["steps"].([]any)
		if m["from"] != float64(v) || m["to"] != float64(v+1) || len(list) != 1 || list[0].(map[string]any)["client"] != lena {
			t.Fatalf("steps: %v", m)
		}
	}
	// Dirk's step on the old version is refused.
	b.send(map[string]any{"t": "steps", "id": 7, "version": v, "steps": []any{insertText(1, "?")}})
	if nack := b.next("nack"); nack["id"] != float64(7) || nack["code"] != "draft.behind" {
		t.Fatalf("nack: %v", nack)
	}
	// Carets and titles reach the others.
	b.send(map[string]any{"t": "caret", "version": v + 1, "anchor": 2, "head": 4})
	caret := a.next("caret")
	if caret["client"] != dirk || caret["anchor"] != float64(2) || caret["head"] != float64(4) ||
		caret["user"].(map[string]any)["name"] == "" {
		t.Fatalf("caret: %v", caret)
	}
	b.send(map[string]any{"t": "title", "id": 8, "title": "Checklist for Monday"})
	b.next("ack")
	if title := a.next("title"); title["title"] != "Checklist for Monday" || title["client"] != dirk {
		t.Fatalf("title: %v", title)
	}
	// Who joins later learns where the others are.
	c, err := dial(t, f.env, f.dirk, pid, fmt.Sprintf("%d:c", f.ids["dirk"]), v+1, f.env.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.next("welcome")
	if late := c.next("caret"); late["client"] != dirk {
		t.Fatalf("caret for a late editor: %v", late)
	}
	// Publishing reaches everyone.
	f.lena.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Checklist for Monday",
		"draftVersion": v + 1, "baseVersion": 1}, nil)
	for _, s := range []*socket{a, b} {
		if m := s.next("published"); m["pageVersion"] != float64(2) {
			t.Fatalf("published: %v", m)
		}
	}
	// An editor that leaves is gone for the others.
	_ = b.ws.Close()
	if left := a.next("leave"); left["client"] != dirk {
		t.Fatalf("leave: %v", left)
	}
	// Steps too large for a notification are read from the database.
	big := strings.Repeat("Lorem ipsum ", 1000)
	a.send(map[string]any{"t": "steps", "id": 2, "version": v + 1, "steps": []any{insertText(1, big)}})
	a.next("ack")
	m := c.next("steps")
	raw, _ := json.Marshal(m)
	if m["to"] != float64(v+2) || !strings.Contains(string(raw), "Lorem ipsum Lorem") {
		t.Fatalf("large steps: %.200s", raw)
	}
}

// TestDraftSocketNeedsWriteScope: the socket writes the draft, so an access token opens it only with the write
// scope, although its handshake is a GET.
func TestDraftSocketNeedsWriteScope(t *testing.T) {
	f := setup(t)
	p := f.publishNew(t, f.lena, "Notes", f.space.HomePageID, "Hello")
	pid := id(p.ID)
	var joined editSession
	f.lena.Get("/api/v1/wiki/pages/"+pid+"/edit", &joined)
	open := func(scopes ...string) (*socket, error) {
		var created struct {
			Token string `json:"token"`
		}
		f.lena.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": strings.Join(scopes, " "), "scopes": scopes}, &created)
		url := strings.Replace(f.env.URL, "http://", "ws://", 1) + fmt.Sprintf("/api/v1/wiki/pages/%s/edit/socket?version=%d&client=%d:t",
			pid, joined.Version, f.ids["lena"])
		cfg, err := websocket.NewConfig(url, f.env.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Header.Set("Authorization", "Bearer "+created.Token)
		ws, err := websocket.DialConfig(cfg)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = ws.Close() })
		return &socket{t: t, ws: ws}, nil
	}
	if _, err := open("read"); err == nil {
		t.Fatal("a token with the read scope opened the editor's socket")
	}
	s, err := open("read", "write")
	if err != nil {
		t.Fatal(err)
	}
	s.next("welcome")
}

// TestDraftSocketEndsWithTheSession: an editor's WebSocket does not outlive the editor's session: signing out (as
// a new password or an administrator would) ends it with "bye", like a lost permission.
func TestDraftSocketEndsWithTheSession(t *testing.T) {
	f := setup(t)
	f.env.App.Wiki.SocketRecheck = 100 * time.Millisecond
	p := f.publishNew(t, f.lena, "Notes", f.space.HomePageID, "Hello")
	pid := id(p.ID)
	var joined editSession
	f.lena.Get("/api/v1/wiki/pages/"+pid+"/edit", &joined)
	s, err := dial(t, f.env, f.lena, pid, fmt.Sprintf("%d:a", f.ids["lena"]), joined.Version, f.env.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.next("welcome")
	f.lena.MustDo(http.MethodPost, "/api/v1/auth/logout", nil, nil)
	if bye := s.next("bye"); bye["code"] != "denied" {
		t.Fatalf("bye: %v", bye)
	}
}
