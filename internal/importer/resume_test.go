// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

// quiet is the logger of the tests: warnings and errors only.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// flaky stands in front of a fake source, counts the requests and fails those that break returns true for
// (with an answer that is not tried again), like a source that goes away in the middle of an extraction.
type flaky struct {
	*httptest.Server
	mu       sync.Mutex
	requests map[string]int
	broken   func(key string) bool
	key      func(r *http.Request) string
	status   int // of the failing answers (400 if not set)
}

func newFlaky(t *testing.T, upstream string, key func(r *http.Request) string) *flaky {
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	f := &flaky{requests: map[string]int{}, key: key}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := f.key(r)
		f.mu.Lock()
		f.requests[k]++
		fail := f.broken != nil && f.broken(k)
		status := cmp.Or(f.status, http.StatusBadRequest)
		f.mu.Unlock()
		if fail {
			http.Error(w, `{"errorMessages":["the source went away"]}`, status)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *flaky) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[k]
}

func (f *flaky) reset(broken func(string) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests, f.broken = map[string]int{}, broken
}

func extractionState(t *testing.T, work, product string) importer.ExtractionState {
	t.Helper()
	var st importer.ExtractionState
	raw, err := os.ReadFile(filepath.Join(work, product, "extraction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestJiraExtractionResumes(t *testing.T) {
	fake := fakeJira(t)
	src := newFlaky(t, fake.URL, func(r *http.Request) string {
		if r.URL.Path == "/rest/api/2/search" {
			return r.URL.Path + "?startAt=" + r.URL.Query().Get("startAt")
		}
		return r.URL.Path
	})
	src.reset(func(k string) bool { return k == "/rest/api/2/search?startAt=2" })
	work := t.TempDir()
	ctx := context.Background()
	j := importer.NewJira(importer.JiraOptions{URL: src.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
	if err := j.Extract(ctx); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("the interrupted extraction: %v", err)
	}
	if st := extractionState(t, work, "jira"); st.Finished != nil {
		t.Fatalf("the interrupted extraction is marked as finished: %+v", st)
	}
	if src.count("/secure/attachment/10500/screenshot.png") != 1 {
		t.Fatalf("the attachment of the first page was not downloaded: %v", src.requests)
	}

	// The next run resumes after the first page: neither the configuration nor the first page nor its attachment
	// are read again.
	src.reset(nil)
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int{"/rest/api/2/serverInfo": 0, "/rest/api/2/search?startAt=0": 0, "/secure/attachment/10500/screenshot.png": 0,
		"/rest/api/2/search?startAt=2": 1, "/rest/api/2/issue/SHOP-3/comment": 1} {
		if got := src.count(k); got != want {
			t.Errorf("%s was requested %d times, want %d", k, got, want)
		}
	}
	st := extractionState(t, work, "jira")
	if st.Finished == nil || len(st.Resumed) != 1 {
		t.Errorf("state after resuming: %+v", st)
	}

	// The extraction is complete: the load has every issue.
	env := apitest.New(t)
	env.CreateUser("mmuster")
	rep, err := j.Load(ctx, importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["issues"] != 4 || rep.Counts["attachments"] != 1 || rep.Counts["comments"] != 5 {
		t.Errorf("counts after a resumed extraction: %v", rep.Counts)
	}

	// After a finished extraction, the next one (a delta before the cut-over) reads everything again but keeps the
	// attachment that was downloaded completely.
	src.reset(nil)
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	if src.count("/rest/api/2/serverInfo") != 1 || src.count("/rest/api/2/search?startAt=0") != 1 ||
		src.count("/secure/attachment/10500/screenshot.png") != 0 {
		t.Errorf("a new extraction: %v", src.requests)
	}
	// A partial download (of a run that was killed) is started again; --restart begins afresh.
	if err := os.WriteFile(filepath.Join(work, "jira/attachments/10500"), []byte("\x89PN"), 0o640); err != nil {
		t.Fatal(err)
	}
	restarted := importer.NewJira(importer.JiraOptions{URL: src.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work,
		Restart: true}, quiet())
	src.reset(nil)
	if err := restarted.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	if src.count("/secure/attachment/10500/screenshot.png") != 1 {
		t.Errorf("the incomplete attachment was not downloaded again: %v", src.requests)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "jira/attachments/10500")); string(b) != "\x89PNG\r\n\x1a\n" {
		t.Errorf("the attachment: %q", b)
	}
}

// TestJiraBrokenIssues: a page of issues that the source cannot answer (an issue whose fields or renderer fail) is
// read issue by issue; an issue whose rendered fields fail is read without them, one that fails anyway is left out
// and noted, and the extraction goes on.
func TestJiraBrokenIssues(t *testing.T) {
	fake := fakeJira(t)
	src := newFlaky(t, fake.URL, func(r *http.Request) string {
		q := r.URL.Query()
		if r.URL.Path == "/rest/api/2/search" {
			return fmt.Sprintf("search %s/%s %s", q.Get("startAt"), q.Get("maxResults"), q.Get("expand"))
		}
		return r.URL.Path
	})
	src.status = http.StatusInternalServerError
	src.reset(func(k string) bool {
		switch {
		case k == "search 2/100 renderedFields,changelog": // the second page: SHOP-3 and SHOP-4
			return true
		case strings.HasPrefix(k, "search 2/1 ") && k != "search 2/1 ": // SHOP-3 fails, but its key can be read
			return true
		case k == "search 3/1 renderedFields,changelog": // SHOP-4 cannot be rendered
			return true
		}
		return false
	})
	work := t.TempDir()
	ctx := context.Background()
	// Pages of 100 issues: the fake answers two at a time, so the extraction asks from 0 and from 2.
	j := importer.NewJira(importer.JiraOptions{URL: src.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
	j.WithoutPauses()
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	if src.count("search 3/1 changelog") != 1 || src.count("search 2/1 ") != 1 {
		t.Errorf("requests: %v", src.requests)
	}
	var page struct {
		Issues []struct {
			Key            string         `json:"key"`
			RenderedFields map[string]any `json:"renderedFields"`
		} `json:"issues"`
	}
	raw, err := os.ReadFile(filepath.Join(work, "jira/projects/SHOP/issues/page-00001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Issues) != 1 || page.Issues[0].Key != "SHOP-4" {
		t.Fatalf("the page read issue by issue: %s %v", raw, err)
	}
	a, err := j.Analyse(ctx, nil, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Notes) != 2 || a.Notes[0].Kind != "issue" || a.Notes[0].Name != "SHOP-3" || decision(a, "issues-unreadable") == nil {
		t.Errorf("notes: %+v", a.Notes)
	}
	if st := extractionState(t, work, "jira"); st.Finished == nil {
		t.Error("the extraction did not finish")
	}
}

func TestConfluenceExtractionResumes(t *testing.T) {
	fake := fakeConfluence(t)
	src := newFlaky(t, fake.URL, func(r *http.Request) string {
		q := r.URL.Query()
		if r.URL.Path == "/rest/api/content" {
			return r.URL.Path + "?" + q.Get("type") + "/" + q.Get("status") + "@" + q.Get("start")
		}
		return r.URL.Path
	})
	src.reset(func(k string) bool { return k == "/rest/api/content?page/current@2" })
	work := t.TempDir()
	ctx := context.Background()
	c := importer.NewConfluence(importer.ConfluenceOptions{URL: src.URL, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
		History: true, PageSize: 2}, quiet())
	if err := c.Extract(ctx); err == nil {
		t.Fatal("the interrupted extraction did not fail")
	}
	if src.count("/download/attachments/1001/logo.png") != 1 || src.count("/rest/api/content/1001/child/comment") != 1 {
		t.Fatalf("the first chunk was not extracted: %v", src.requests)
	}
	src.reset(nil)
	if err := c.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int{"/rest/api/space/DOC": 0, "/rest/api/content?page/current@": 0, "/download/attachments/1001/logo.png": 0,
		"/rest/api/content/1001/child/comment": 0, "/rest/api/content?page/current@2": 1, "/rest/api/content/1003/restriction/byOperation": 1} {
		if got := src.count(k); got != want {
			t.Errorf("%s was requested %d times, want %d", k, got, want)
		}
	}
	env := apitest.New(t)
	env.CreateUser("mmuster")
	rep, err := c.Load(ctx, importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Wiki: env.App.Wiki, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["pages"] != 5 || rep.Counts["blogPosts"] != 1 || rep.Counts["attachments"] != 1 || rep.Counts["comments"] != 3 {
		t.Errorf("counts after a resumed extraction: %v", rep.Counts)
	}
}

// confluenceExpand is what the extraction of Confluence asks the listings of a space to expand.
const confluenceExpand = "body.storage,version,ancestors,history,metadata.labels,extensions.position"

// TestConfluenceBrokenPages: a chunk of a listing that the source cannot answer (a page whose content or history
// fails there) is read page by page; a page that fails also so is left out and noted, and the extraction goes on.
func TestConfluenceBrokenPages(t *testing.T) {
	fake := fakeConfluence(t)
	src := newFlaky(t, fake.URL, func(r *http.Request) string {
		q := r.URL.Query()
		if r.URL.Path == "/rest/api/content" {
			return fmt.Sprintf("%s/%s %s/%s %s", q.Get("type"), q.Get("status"), q.Get("start"), q.Get("limit"), q.Get("expand"))
		}
		return r.URL.Path
	})
	src.status = http.StatusInternalServerError
	src.reset(func(k string) bool {
		// The second chunk of pages: Secret Plans and Secret Child, which fails, but whose title can be read.
		return k == "page/current 2/2 "+confluenceExpand || k == "page/current 3/1 "+confluenceExpand
	})
	work := t.TempDir()
	ctx := context.Background()
	c := importer.NewConfluence(importer.ConfluenceOptions{URL: src.URL, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
		PageSize: 2}, quiet())
	c.WithoutPauses()
	if err := c.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	// The broken page is tried a few times only: the chunk's request has shown that the error persists.
	if src.count("page/current 2/1 "+confluenceExpand) != 1 || src.count("page/current 3/1 "+confluenceExpand) != 3 ||
		src.count("page/current 3/1 ") != 1 {
		t.Errorf("requests: %v", src.requests)
	}
	var chunk []struct {
		ID string `json:"id"`
	}
	raw, err := os.ReadFile(filepath.Join(work, "confluence/spaces/DOC/page-current/00001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &chunk); err != nil || len(chunk) != 1 || chunk[0].ID != "1003" {
		t.Fatalf("the chunk read page by page: %s %v", raw, err)
	}
	if st := extractionState(t, work, "confluence"); st.Finished == nil {
		t.Error("the extraction did not finish")
	}
	a, err := c.Analyse(ctx, nil, importer.English)
	if err != nil {
		t.Fatal(err)
	}
	if decision(a, "pages-unreadable") == nil || len(a.Notes) != 1 || a.Notes[0].ID != "1004" || a.Notes[0].Name != "Secret Child" {
		t.Errorf("notes: %+v", a.Notes)
	}
	env := apitest.New(t)
	rep, err := c.Load(ctx, importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Wiki: env.App.Wiki, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["pages"] != 4 || len(rep.Failures) != 0 {
		t.Errorf("the load without the broken page: %v %+v", rep.Counts, rep.Failures)
	}
}

// TestFailingSourceStopsExtraction: when not an item fails but the source — it answers nothing any more, or no
// item of a page at all —, the extraction stops, to resume later, instead of leaving items out as unreadable.
func TestFailingSourceStopsExtraction(t *testing.T) {
	ctx := context.Background()
	beyond := func(prefix string) func(string) bool { // a request of the listing from its third item on
		return func(k string) bool {
			n, err := strconv.Atoi(strings.TrimPrefix(k, prefix))
			return strings.HasPrefix(k, prefix) && err == nil && n >= 2
		}
	}
	type extraction interface{ Extract(context.Context) error }
	products := []struct {
		name    string
		fake    func(*testing.T) *httptest.Server
		key     func(*http.Request) string
		listing func(string) bool
		new     func(url, work string) extraction
	}{
		{"jira", fakeJira, func(r *http.Request) string {
			if r.URL.Path == "/rest/api/2/search" {
				return "search " + r.URL.Query().Get("startAt")
			}
			return r.URL.Path
		}, beyond("search "), func(url, work string) extraction {
			j := importer.NewJira(importer.JiraOptions{URL: url, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
			j.WithoutPauses()
			return j
		}},
		{"confluence", fakeConfluence, func(r *http.Request) string {
			if r.URL.Path == "/rest/api/content" {
				return "content " + r.URL.Query().Get("type") + " " + r.URL.Query().Get("start")
			}
			return r.URL.Path
		}, beyond("content page "), func(url, work string) extraction {
			c := importer.NewConfluence(importer.ConfluenceOptions{URL: url, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
				PageSize: 2}, quiet())
			c.WithoutPauses()
			return c
		}},
	}
	for _, p := range products {
		for _, whole := range []bool{true, false} {
			src := newFlaky(t, p.fake(t).URL, p.key)
			src.status = http.StatusServiceUnavailable
			down := false
			src.reset(func(k string) bool {
				if !whole {
					return p.listing(k) // only the listing fails, from its second page on
				}
				down = down || p.listing(k) // the source goes down when the second page is asked for
				return down
			})
			work := t.TempDir()
			x := p.new(src.URL, work)
			if err := x.Extract(ctx); err == nil || !strings.Contains(err.Error(), "503") {
				t.Fatalf("%s (the whole source failing: %v): the extraction did not stop: %v", p.name, whole, err)
			}
			notes, err := os.ReadFile(filepath.Join(work, p.name, "extraction-notes.log"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if strings.Contains(string(notes), `"kind":"issue"`) || strings.Contains(string(notes), `"kind":"page"`) {
				t.Errorf("%s (the whole source failing: %v): items were noted as unreadable: %s", p.name, whole, notes)
			}
			src.reset(nil)
			if err := x.Extract(ctx); err != nil {
				t.Fatal(err)
			}
			if st := extractionState(t, work, p.name); st.Finished == nil || len(st.Resumed) != 1 {
				t.Errorf("%s: state after resuming: %+v", p.name, st)
			}
		}
	}
}

// TestSourceGoesDownWhileReadingOneByOne: when the source goes down while a page is read item by item, the items
// it no longer answers are not taken for broken ones: the extraction stops and resumes later.
func TestSourceGoesDownWhileReadingOneByOne(t *testing.T) {
	ctx := context.Background()
	type extraction interface{ Extract(context.Context) error }
	products := []struct {
		name         string
		fake         func(*testing.T) *httptest.Server
		key          func(*http.Request) string
		page, second string // the request of the page that fails, and of its second item, from which on the source is down
		new          func(url, work string) extraction
	}{
		{"jira", fakeJira, func(r *http.Request) string {
			q := r.URL.Query()
			if r.URL.Path == "/rest/api/2/search" {
				return fmt.Sprintf("search %s/%s %s", q.Get("startAt"), q.Get("maxResults"), q.Get("expand"))
			}
			return r.URL.Path
		}, "search 2/100 renderedFields,changelog", "search 3/1 renderedFields,changelog", func(url, work string) extraction {
			j := importer.NewJira(importer.JiraOptions{URL: url, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
			j.WithoutPauses()
			return j
		}},
		{"confluence", fakeConfluence, func(r *http.Request) string {
			q := r.URL.Query()
			if r.URL.Path == "/rest/api/content" {
				return fmt.Sprintf("%s/%s %s/%s %s", q.Get("type"), q.Get("status"), q.Get("start"), q.Get("limit"), q.Get("expand"))
			}
			return r.URL.Path
		}, "page/current 2/2 " + confluenceExpand, "page/current 3/1 " + confluenceExpand, func(url, work string) extraction {
			c := importer.NewConfluence(importer.ConfluenceOptions{URL: url, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
				PageSize: 2}, quiet())
			c.WithoutPauses()
			return c
		}},
	}
	for _, p := range products {
		src := newFlaky(t, p.fake(t).URL, p.key)
		src.status = http.StatusServiceUnavailable
		down := false
		src.reset(func(k string) bool {
			down = down || k == p.second
			return down || k == p.page
		})
		work := t.TempDir()
		x := p.new(src.URL, work)
		if err := x.Extract(ctx); err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("%s: the extraction did not stop: %v", p.name, err)
		}
		src.reset(nil)
		if err := x.Extract(ctx); err != nil {
			t.Fatal(err)
		}
		notes, err := os.ReadFile(filepath.Join(work, p.name, "extraction-notes.log"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(notes), `"kind":"issue"`) || strings.Contains(string(notes), `"kind":"page"`) {
			t.Errorf("%s: items were noted as unreadable: %s", p.name, notes)
		}
	}
}
