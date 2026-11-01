// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

const (
	confluenceToken = "confluence-test-token"
	cAdmin          = `{"type":"known","username":"admin","userKey":"key-admin","displayName":"Administrator"}`
	cJdoe           = `{"type":"known","username":"jdoe","userKey":"key-jdoe","displayName":"John Doe"}`
	cMmuster        = `{"type":"known","username":"mmuster","userKey":"key-mmuster","displayName":"Max Muster"}`
)

func content(id, typ, status, title string, version int, ancestors []string, position, storage string, labels ...string) string {
	anc := []string{}
	for _, a := range ancestors {
		anc = append(anc, `{"id":"`+a+`","type":"page"}`)
	}
	ls := []string{}
	for _, l := range labels {
		ls = append(ls, `{"prefix":"global","name":"`+l+`"}`)
	}
	body, _ := json.Marshal(storage)
	return `{"id":"` + id + `","type":"` + typ + `","status":"` + status + `","title":"` + title + `",
		"history":{"createdBy":{{JDOE}},"createdDate":"2026-01-05T09:00:00.000+01:00"},
		"version":{"by":{{MMUSTER}},"when":"2026-01-20T09:00:00.000+01:00","number":` + strconv.Itoa(version) + `,"message":"Edited","minorEdit":false},
		"ancestors":[` + strings.Join(anc, ",") + `],"body":{"storage":{"value":` + string(body) + `,"representation":"storage"}},
		"metadata":{"labels":{"results":[` + strings.Join(ls, ",") + `],"size":` + strconv.Itoa(len(ls)) + `}},
		"extensions":{"position":` + position + `}}`
}

// fakeConfluence serves the REST resources the importer reads; content listings come in pages of two.
func fakeConfluence(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	home := `<p>Welcome! See <ac:link><ri:page ri:content-title="Install Guide" /></ac:link> and ask <ac:link><ri:user ri:userkey="key-mmuster" /></ac:link>.</p>` +
		`<ac:structured-macro ac:name="info"><ac:rich-text-body><p>Read this first.</p></ac:rich-text-body></ac:structured-macro>` +
		`<p><ac:image ac:width="300"><ri:attachment ri:filename="logo.png" /></ac:image></p>` +
		// Formulas of the LaTeX Math app.
		`<p>Euler: <ac:structured-macro ac:name="mathinline"><ac:parameter ac:name="body">e^{i\pi}+1=0</ac:parameter></ac:structured-macro></p>` +
		`<ac:structured-macro ac:name="mathblock"><ac:plain-text-body><![CDATA[\sum_{i=1}^n x_i < 1]]></ac:plain-text-body></ac:structured-macro>` +
		`<ac:structured-macro ac:name="gliffy"><ac:parameter ac:name="name">diagram</ac:parameter></ac:structured-macro>`
	install := `<p>Run <ac:inline-comment-marker ac:ref="aaaa-bbbb">the installer</ac:inline-comment-marker> now. ` +
		`<a href="{{BASE}}/pages/viewpage.action?pageId=1003">Plans</a>, <a href="{{BASE}}/display/DOC/Documentation+Home">home</a>, ` +
		`<ac:link><ri:url ri:value="https://jira.example.com/browse/SHOP-1" /></ac:link>.</p>` +
		`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">bash</ac:parameter><ac:plain-text-body><![CDATA[./install.sh --prefix=/opt && echo "<done>"]]></ac:plain-text-body></ac:structured-macro>`
	pages := []string{
		content("1001", "page", "current", "Documentation Home", 3, nil, `"none"`, home, "start", "Bad Label"),
		content("1002", "page", "current", "Install Guide", 1, []string{"1001"}, "1", install, "howto"),
		content("1003", "page", "current", "Secret Plans", 1, []string{"1001"}, "0", "<p>Top secret</p>"),
		// A layout macro Kogge does not know, with more content than an unsupported block keeps.
		content("1004", "page", "current", "Secret Child", 1, []string{"1001", "1003"}, `"none"`, `<p>Also secret</p><ac:structured-macro ac:name="section">`+
			`<ac:rich-text-body><p>`+strings.Repeat("Lorem ipsum dolor sit amet. ", 80)+`</p></ac:rich-text-body></ac:structured-macro>`),
	}
	archived := []string{content("1005", "page", "archived", "Old Stuff", 1, nil, `"none"`, "<p>Old</p>")}
	blogs := []string{content("1006", "blogpost", "current", "Release 1.0", 1, nil, `"none"`, "<p>We released.</p>")}
	// Listings answer two items at a time (or one, if asked for one).
	listing := func(items []string, start, limit int, next string) string {
		if limit != 1 {
			limit = 2
		}
		start = min(start, len(items))
		end := min(start+limit, len(items))
		links := `"base":"{{BASE}}","context":""`
		if end < len(items) {
			links += `,"next":"` + next + `&limit=` + strconv.Itoa(limit) + `&start=` + strconv.Itoa(end) + `"`
		}
		return `{"results":[` + strings.Join(items[start:end], ",") + `],"start":` + strconv.Itoa(start) + `,"limit":` + strconv.Itoa(limit) +
			`,"size":` + strconv.Itoa(end-start) + `,"_links":{` + links + `}}`
	}
	fixed := map[string]string{
		"/rest/api/space/DOC": `{"id":98306,"key":"DOC","name":"Documentation","type":"global","description":{"plain":{"value":"Product docs","representation":"plain"}},
			"homepage":{"id":"1001","title":"Documentation Home"},"_links":{"base":"{{BASE}}","context":""}}`,
		"/rest/api/space/DOC/permissions": `[
			{"operation":{"targetType":"space","operationKey":"read"},"subject":{"type":"group","name":"confluence-users"},"spaceKey":"DOC"},
			{"operation":{"targetType":"page","operationKey":"create"},"subject":{"type":"group","name":"confluence-users"},"spaceKey":"DOC"},
			{"operation":{"targetType":"comment","operationKey":"create"},"subject":{"type":"group","name":"confluence-users"},"spaceKey":"DOC"},
			{"operation":{"targetType":"space","operationKey":"delete_mail"},"subject":{"type":"group","name":"confluence-users"},"spaceKey":"DOC"},
			{"operation":{"targetType":"space","operationKey":"read"},"subject":` + cAdmin + `,"spaceKey":"DOC"},
			{"operation":{"targetType":"space","operationKey":"administer"},"subject":` + cAdmin + `,"spaceKey":"DOC"}]`,
		"/rest/api/group/confluence-users/member": `{"results":[` + cAdmin + `,` + cJdoe + `,` + cMmuster + `],"start":0,"limit":200,"size":3,"_links":{}}`,
		"/rest/api/user": cMmuster,
		"/rest/api/content/1001/child/attachment": `{"results":[{"id":"att3001","type":"attachment","status":"current","title":"logo.png",
			"version":{"by":` + cJdoe + `,"when":"2026-01-06T09:00:00.000+01:00","number":1},"metadata":{"mediaType":"image/png","comment":"Our logo"},
			"extensions":{"mediaType":"image/png","fileSize":8},"_links":{"download":"/download/attachments/1001/logo.png?version=1&api=v2"}}],
			"start":0,"limit":100,"size":1,"_links":{}}`,
		"/rest/api/content/1002/child/comment": `{"results":[
			{"id":"2001","type":"comment","status":"current","title":"Re: Install Guide","history":{"createdBy":` + cJdoe + `,"createdDate":"2026-01-07T09:00:00.000+01:00"},
			 "version":{"by":` + cJdoe + `,"when":"2026-01-07T09:00:00.000+01:00","number":1},"ancestors":[],"body":{"storage":{"value":"<p>Works for me</p>"}},
			 "extensions":{"location":"footer"}},
			{"id":"2002","type":"comment","status":"current","title":"Re: Install Guide","history":{"createdBy":` + cMmuster + `,"createdDate":"2026-01-08T09:00:00.000+01:00"},
			 "version":{"by":` + cMmuster + `,"when":"2026-01-08T09:00:00.000+01:00","number":1},"ancestors":[{"id":"2001","type":"comment"}],
			 "body":{"storage":{"value":"<p>Thanks</p>"}},"extensions":{"location":"footer"}},
			{"id":"2003","type":"comment","status":"current","title":"Re: Install Guide","history":{"createdBy":` + cMmuster + `,"createdDate":"2026-01-09T09:00:00.000+01:00"},
			 "version":{"by":` + cMmuster + `,"when":"2026-01-09T09:00:00.000+01:00","number":1},"ancestors":[],"body":{"storage":{"value":"<p>Which one?</p>"}},
			 "extensions":{"location":"inline","inlineProperties":{"markerRef":"aaaa-bbbb","originalSelection":"the installer"},"resolution":{"status":"resolved"}}}],
			"start":0,"limit":100,"size":3,"_links":{}}`,
		"/rest/api/content/1003/restriction/byOperation": `{"read":{"operation":"read","restrictions":{"user":{"results":[` + cJdoe + `,` + cAdmin + `],"size":2},
			"group":{"results":[],"size":0}}},"update":{"operation":"update","restrictions":{"user":{"results":[],"size":0},"group":{"results":[],"size":0}}}}`,
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+confluenceToken {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		replace := strings.NewReplacer("{{BASE}}", srv.URL, "{{JDOE}}", cJdoe, "{{MMUSTER}}", cMmuster)
		write := func(s string) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(replace.Replace(s)))
		}
		q := r.URL.Query()
		start, _ := strconv.Atoi(q.Get("start"))
		switch {
		case r.URL.Path == "/download/attachments/1001/logo.png":
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
			return
		case r.URL.Path == "/rest/api/content" && q.Get("spaceKey") == "DOC":
			// Like the source's, the next links keep the request's parameters.
			next := "/rest/api/content?spaceKey=DOC&type=" + q.Get("type") + "&status=" + q.Get("status") + "&expand=" + url.QueryEscape(q.Get("expand"))
			limit, _ := strconv.Atoi(q.Get("limit"))
			switch q.Get("type") + "/" + q.Get("status") {
			case "page/current":
				write(listing(pages, start, limit, next))
			case "page/archived":
				write(listing(archived, start, limit, next))
			case "blogpost/current":
				write(listing(blogs, start, limit, next))
			default:
				http.Error(w, "bad listing", http.StatusBadRequest)
			}
			return
		case r.URL.Path == "/rest/api/content/1001" && q.Get("status") == "historical":
			n := q.Get("version")
			write(`{"id":"1001","type":"page","status":"historical","title":"Documentation Home","version":{"by":` + cJdoe +
				`,"when":"2026-01-0` + n + `T09:00:00.000+01:00","number":` + n + `,"message":"Version ` + n + `"},
				"body":{"storage":{"value":"<p>Draft ` + n + `</p>"}}}`)
			return
		}
		if body, ok := fixed[r.URL.Path]; ok {
			write(body)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/rest/api/content/") {
			// Pages without attachments, comments or restrictions.
			switch {
			case strings.HasSuffix(r.URL.Path, "/restriction/byOperation"):
				write(`{"read":{"restrictions":{"user":{"results":[]},"group":{"results":[]}}},"update":{"restrictions":{"user":{"results":[]},"group":{"results":[]}}}}`)
				return
			case strings.Contains(r.URL.Path, "/child/"):
				write(`{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type wikiPage struct {
	ID        int64          `json:"id"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	Version   int            `json:"version"`
	ParentID  int64          `json:"parentId"`
	Labels    []string       `json:"labels"`
	Body      map[string]any `json:"body"`
	Ancestors []struct {
		ID int64 `json:"id"`
	} `json:"ancestors"`
}

func TestConfluenceImport(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	env.CreateUser("mmuster")
	fake := fakeConfluence(t)
	work := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	c := importer.NewConfluence(importer.ConfluenceOptions{URL: fake.URL, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
		JiraURL: "https://jira.example.com", History: true, PageSize: 2}, log)
	if err := c.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"confluence/server.json", "confluence/users.json", "confluence/spaces/DOC/page-current/00000.json", "confluence/spaces/DOC/page-current/00001.json",
		"confluence/content/1001/versions/2.json", "confluence/attachments/att3001", "confluence/groups/confluence-users.json"} {
		if _, err := os.Stat(work + "/" + f); err != nil {
			t.Errorf("not extracted: %s", f)
		}
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Wiki: env.App.Wiki, Log: log}
	rep, err := c.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["spaces"] != 1 || rep.Counts["pages"] != 5 || rep.Counts["blogPosts"] != 1 || rep.Counts["comments"] != 3 ||
		rep.Counts["attachments"] != 1 || rep.Counts["versions"] != 8 {
		t.Errorf("counts = %v", rep.Counts)
	}
	if rep.Unsupported["macro gliffy"] != 1 || rep.Unsupported["space permission space/delete_mail"] != 1 ||
		rep.Unsupported["macro mathblock"] != 0 || rep.Unsupported["macro mathinline"] != 0 {
		t.Errorf("unsupported = %v", rep.Unsupported)
	}
	if strings.Join(rep.UsersMatched, ",") != "admin,mmuster" || strings.Join(rep.Placeholders, ",") != "jdoe" {
		t.Errorf("users: %v / %v", rep.UsersMatched, rep.Placeholders)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), `"bad label"`) {
		t.Errorf("warnings = %v", rep.Warnings)
	}

	var space struct {
		Key        string `json:"key"`
		Name       string `json:"name"`
		HomePageID int64  `json:"homePageId"`
	}
	admin.Get("/api/v1/wiki/spaces/DOC", &space)
	if space.Name != "Documentation" || space.HomePageID == 0 {
		t.Fatalf("space = %+v", space)
	}
	var tree struct {
		Total int `json:"total"`
		Items []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/spaces/DOC/tree?parent=%d", space.HomePageID), &tree)
	if tree.Total != 2 || len(tree.Items) != 2 || tree.Items[0].Title != "Secret Plans" || tree.Items[1].Title != "Install Guide" {
		t.Fatalf("tree = %+v", tree)
	}
	secretID, installID := tree.Items[0].ID, tree.Items[1].ID

	var home wikiPage
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d", space.HomePageID), &home)
	body, _ := json.Marshal(home.Body)
	for _, want := range []string{`/wiki/spaces/DOC/pages/` + strconv.FormatInt(installID, 10), `"type":"mention"`, `"type":"panel"`,
		`"type":"image"`, `"attachment":`, `"type":"unsupported"`, `{"attrs":{"latex":"e^{i\\pi}+1=0"},"type":"inlineMath"}`,
		`{"attrs":{"latex":"\\sum_{i=1}^n x_i \u003c 1"},"type":"math"}`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("home page lacks %s: %s", want, body)
		}
	}
	if home.Version != 3 || strings.Join(home.Labels, ",") != "start" {
		t.Errorf("home = version %d, labels %v", home.Version, home.Labels)
	}
	var versions struct {
		Total int `json:"total"`
		Items []struct {
			Version int    `json:"version"`
			Message string `json:"message"`
		} `json:"items"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d/versions", space.HomePageID), &versions)
	if versions.Total != 3 || len(versions.Items) != 3 {
		t.Errorf("versions = %+v", versions)
	}

	var install wikiPage
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d", installID), &install)
	body, _ = json.Marshal(install.Body)
	for _, want := range []string{`"type":"anchor"`, `"id":"caaaabbbb"`, `/wiki/spaces/DOC/pages/` + strconv.FormatInt(secretID, 10),
		`/wiki/spaces/DOC/pages/` + strconv.FormatInt(space.HomePageID, 10), `"type":"issue"`, `"key":"SHOP-1"`, `"type":"codeBlock"`, `--prefix=/opt`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("install guide lacks %s: %s", want, body)
		}
	}
	var comments []struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parentId"`
		Anchor   string `json:"anchor"`
		Resolved any    `json:"resolved"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d/comments", installID), &comments)
	if len(comments) != 3 {
		t.Fatalf("comments = %+v", comments)
	}
	replies, inline := 0, 0
	for _, cm := range comments {
		if cm.ParentID != 0 {
			replies++
		}
		if cm.Anchor == "caaaabbbb" && cm.Resolved != nil {
			inline++
		}
	}
	if replies != 1 || inline != 1 {
		t.Errorf("comments = %+v", comments)
	}

	// Restricted pages stay restricted, also for their children.
	mm := env.Login("mmuster", apitest.UserPassword)
	if res := mm.Do(http.MethodGet, apitest.Fmt("/api/v1/wiki/pages/%d", secretID), nil); res.Status != http.StatusNotFound {
		t.Errorf("restricted page visible to mmuster: %d", res.Status)
	}
	var child struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/spaces/DOC/tree?parent=%d", secretID), &child)
	if len(child.Items) != 1 {
		t.Fatalf("children of the secret page = %+v", child)
	}
	if res := mm.Do(http.MethodGet, apitest.Fmt("/api/v1/wiki/pages/%d", child.Items[0].ID), nil); res.Status != http.StatusNotFound {
		t.Errorf("child of a restricted page visible to mmuster: %d", res.Status)
	}
	if res := mm.Do(http.MethodGet, apitest.Fmt("/api/v1/wiki/pages/%d", installID), nil); res.Status != http.StatusOK {
		t.Errorf("mmuster cannot read the install guide: %d", res.Status)
	}

	var blog struct {
		Pages []struct {
			Title string `json:"title"`
		} `json:"pages"`
	}
	admin.Get("/api/v1/wiki/spaces/DOC/blog", &blog)
	if len(blog.Pages) != 1 || blog.Pages[0].Title != "Release 1.0" {
		t.Errorf("blog = %+v", blog)
	}
	var found struct {
		Total int `json:"total"`
	}
	admin.Get("/api/v1/wiki/search?q="+apitest.Query("installer"), &found)
	if found.Total < 1 {
		t.Errorf("search found %d pages", found.Total)
	}

	// Old addresses lead to the migrated pages, but not to pages the reader may not see.
	installURL := apitest.Fmt("/wiki/spaces/DOC/pages/%d/", installID)
	for path, want := range map[string]string{
		"/pages/viewpage.action?pageId=1002":   installURL,
		"/display/DOC/Install+Guide":           installURL,
		"/spaces/DOC/pages/1002/Install+Guide": installURL,
		"/x/6gM":                               installURL,
		"/display/DOC":                         "/wiki/spaces/DOC",
		"/display/DOC/2026/01/05/Release+1.0":  "/wiki/spaces/DOC/pages/",
		"/download/attachments/1001/logo.png":  "/api/v1/wiki/attachments/",
	} {
		res := admin.Do(http.MethodGet, path, nil)
		if res.Status != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), want) {
			t.Errorf("GET %s: %d %s, want %s", path, res.Status, res.Header.Get("Location"), want)
		}
	}
	if res := mm.Do(http.MethodGet, "/pages/viewpage.action?pageId=1003", nil); res.Status == http.StatusFound {
		t.Errorf("redirect to a restricted page: %s", res.Header.Get("Location"))
	}
	if res := mm.Do(http.MethodGet, "/display/DOC/Secret+Plans", nil); res.Status == http.StatusFound {
		t.Errorf("redirect to a restricted page by title: %s", res.Header.Get("Location"))
	}

	// A second load changes nothing.
	if _, err := c.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d/comments", installID), &comments)
	admin.Get(apitest.Fmt("/api/v1/wiki/spaces/DOC/tree?parent=%d", space.HomePageID), &tree)
	if len(comments) != 3 || tree.Total != 2 || len(tree.Items) != 2 {
		t.Errorf("second load: %d comments, tree %+v", len(comments), tree)
	}
}
