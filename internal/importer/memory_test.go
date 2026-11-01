// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

// The load reads big work directories piece by piece: its memory must not grow with the number of issues and
// pages. The test generates work directories of two sizes, loads them and compares the peak heap. With
// KOGGE_IMPORT_ISSUES and KOGGE_IMPORT_PAGES it loads one work directory of that size instead and reports its
// peak heap and throughput (e.g. 100000 and 50000: that takes about an hour, and a few GB of database);
// KOGGE_IMPORT_FIXTURE_DIR puts the generated work directory elsewhere than the system's temporary directory.

func writeJSON(t testing.TB, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

// generateJira writes a work directory with one project of n issues, in pages of 100 like the extraction; the
// descriptions and first comments have about 100 bytes per size.
func generateJira(t testing.TB, dir string, n, size int) {
	j := filepath.Join(dir, "jira")
	writeJSON(t, filepath.Join(j, "serverInfo.json"), map[string]any{"baseUrl": "https://jira.example.com", "version": "9.12.0"})
	writeJSON(t, filepath.Join(j, "issuetypes.json"), []any{map[string]any{"id": "1", "name": "Task"}})
	writeJSON(t, filepath.Join(j, "statuses.json"), []any{map[string]any{"id": "1", "name": "Open", "statusCategory": map[string]any{"key": "new"}},
		map[string]any{"id": "3", "name": "Done", "statusCategory": map[string]any{"key": "done"}}})
	writeJSON(t, filepath.Join(j, "priorities.json"), []any{map[string]any{"id": "3", "name": "Medium"}})
	writeJSON(t, filepath.Join(j, "resolutions.json"), []any{})
	writeJSON(t, filepath.Join(j, "linktypes.json"), map[string]any{"issueLinkTypes": []any{}})
	writeJSON(t, filepath.Join(j, "fields.json"), []any{})
	p := filepath.Join(j, "projects", "BIG")
	writeJSON(t, filepath.Join(p, "project.json"), map[string]any{"id": "20000", "key": "BIG", "name": "Big", "projectTypeKey": "business",
		"issueTypes": []any{map[string]any{"id": "1", "name": "Task"}}})
	writeJSON(t, filepath.Join(p, "statuses.json"), []any{map[string]any{"id": "1", "statuses": []any{map[string]any{"id": "1"}, map[string]any{"id": "3"}}}})
	writeJSON(t, filepath.Join(p, "roles.json"), []any{})
	writeJSON(t, filepath.Join(p, "boards.json"), map[string]any{"values": []any{}})
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2*size)
	for page := 0; page*100 < n; page++ {
		var list []any
		for i := page * 100; i < min(n, (page+1)*100); i++ {
			user := map[string]any{"name": "user" + strconv.Itoa(i%50), "displayName": "User " + strconv.Itoa(i%50), "active": true}
			created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000-0700")
			list = append(list, map[string]any{"id": strconv.Itoa(100000 + i), "key": "BIG-" + strconv.Itoa(i+1),
				"fields": map[string]any{"summary": "Issue " + strconv.Itoa(i+1), "issuetype": map[string]any{"id": "1"},
					"status": map[string]any{"id": "1"}, "priority": map[string]any{"id": "3"}, "reporter": user, "creator": user,
					"created": created, "updated": created, "description": text,
					"comment": map[string]any{"total": 2, "comments": []any{
						map[string]any{"id": strconv.Itoa(200000 + 2*i), "author": user, "body": text, "created": created},
						map[string]any{"id": strconv.Itoa(200001 + 2*i), "author": user, "body": "Done.", "created": created}}},
					"worklog": map[string]any{"total": 0, "worklogs": []any{}}, "watches": map[string]any{"watchCount": 0}},
				"renderedFields": map[string]any{"description": "<p>" + text + "</p>"},
				"changelog": map[string]any{"histories": []any{map[string]any{"id": strconv.Itoa(300000 + i), "author": user, "created": created,
					"items": []any{map[string]any{"field": "status", "fieldtype": "jira", "from": "1", "fromString": "Open", "to": "3",
						"toString": "Done"}}}}}})
		}
		writeJSON(t, filepath.Join(p, "issues", fmt.Sprintf("page-%05d.json", page)), map[string]any{"startAt": page * 100, "maxResults": 100,
			"total": n, "issues": list})
	}
}

// generateConfluence writes a work directory with one space of n pages (a tree, ten children each), in chunks of
// 50 like the extraction; each page has about size KB of content. The pages have no attachments, comments or
// restrictions: the load reads their (missing) files all the same.
func generateConfluence(t testing.TB, dir string, n, size int) {
	c := filepath.Join(dir, "confluence")
	writeJSON(t, filepath.Join(c, "server.json"), map[string]any{"baseUrl": "https://wiki.example.com"})
	writeJSON(t, filepath.Join(c, "users.json"), map[string]any{})
	s := filepath.Join(c, "spaces", "BIG")
	writeJSON(t, filepath.Join(s, "space.json"), map[string]any{"key": "BIG", "name": "Big", "type": "global"})
	writeJSON(t, filepath.Join(s, "permissions.json"), []any{map[string]any{"operation": map[string]any{"targetType": "space", "operationKey": "read"},
		"subject": map[string]any{"type": "group", "name": "big-readers"}}})
	writeJSON(t, filepath.Join(c, "groups", "big-readers.json"), []any{})
	body := strings.Repeat("<p>Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt.</p>", 9*size) +
		`<ac:structured-macro ac:name="info"><ac:rich-text-body><p>Note</p></ac:rich-text-body></ac:structured-macro>`
	for chunk := 0; chunk*50 < n; chunk++ {
		var list []any
		for i := chunk * 50; i < min(n, (chunk+1)*50); i++ {
			id := strconv.Itoa(500000 + i)
			var ancestors []any
			for a := i; a > 0; {
				a = (a - 1) / 10
				ancestors = append([]any{map[string]any{"id": strconv.Itoa(500000 + a), "type": "page"}}, ancestors...)
			}
			user := map[string]any{"type": "known", "username": "user" + strconv.Itoa(i%50), "userKey": "key" + strconv.Itoa(i%50)}
			list = append(list, map[string]any{"id": id, "type": "page", "status": "current", "title": "Page " + strconv.Itoa(i),
				"history":   map[string]any{"createdBy": user, "createdDate": "2025-01-01T00:00:00.000Z"},
				"version":   map[string]any{"by": user, "when": "2025-01-02T00:00:00.000Z", "number": 1},
				"ancestors": ancestors, "body": map[string]any{"storage": map[string]any{"value": body + "<p>" + id + "</p>"}}})
		}
		writeJSON(t, filepath.Join(s, "page-current", fmt.Sprintf("%05d.json", chunk)), list)
	}
}

// peakHeap runs fn and returns the highest heap in use above the one before (sampled).
func peakHeap(fn func()) uint64 {
	old := debug.SetGCPercent(10) // a tight heap: samples are close to what is alive
	defer debug.SetGCPercent(old)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak uint64
	var mu sync.Mutex
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				var s runtime.MemStats
				runtime.ReadMemStats(&s)
				mu.Lock()
				if s.HeapAlloc > base && s.HeapAlloc-base > peak {
					peak = s.HeapAlloc - base
				}
				mu.Unlock()
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return peak
}

type loadRun struct {
	issues, pages      int
	jiraPeak, wikiPeak uint64
	jiraTime, wikiTime time.Duration
}

func measureLoad(t *testing.T, issues, pages, size int) loadRun {
	env := apitest.New(t)
	ctx := context.Background()
	run := loadRun{issues: issues, pages: pages}
	dir := t.TempDir()
	if base := os.Getenv("KOGGE_IMPORT_FIXTURE_DIR"); base != "" {
		// Big work directories go where there is room for them; they are removed after the test.
		var err error
		if err = os.MkdirAll(base, 0o750); err == nil {
			dir, err = os.MkdirTemp(base, "load-")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	generateJira(t, dir, issues, size)
	j := importer.NewJira(importer.JiraOptions{Workdir: dir}, quiet())
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Wiki: env.App.Wiki, Log: quiet()}
	started := time.Now()
	run.jiraPeak = peakHeap(func() {
		rep, err := j.Load(ctx, loader)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Counts["issues"] != issues || rep.Counts["comments"] != 2*issues {
			t.Fatalf("counts: %v", rep.Counts)
		}
	})
	run.jiraTime = time.Since(started)
	generateConfluence(t, dir, pages, size)
	c := importer.NewConfluence(importer.ConfluenceOptions{Workdir: dir}, quiet())
	started = time.Now()
	run.wikiPeak = peakHeap(func() {
		rep, err := c.Load(ctx, loader)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Counts["pages"] != pages {
			t.Fatalf("counts: %v", rep.Counts)
		}
	})
	run.wikiTime = time.Since(started)
	t.Logf("%d issues: peak heap %d MiB, %s (%.0f issues/s); %d pages: peak heap %d MiB, %s (%.0f pages/s)", issues, run.jiraPeak>>20,
		run.jiraTime.Round(time.Second), float64(issues)/run.jiraTime.Seconds(), pages, run.wikiPeak>>20, run.wikiTime.Round(time.Second),
		float64(pages)/run.wikiTime.Seconds())
	return run
}

func TestLoadMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("generates and loads thousands of issues and pages")
	}
	issues, _ := strconv.Atoi(os.Getenv("KOGGE_IMPORT_ISSUES"))
	pages, _ := strconv.Atoi(os.Getenv("KOGGE_IMPORT_PAGES"))
	if issues > 0 && pages > 0 {
		run := measureLoad(t, issues, pages, 2)
		// A load holds a page of the extraction, the maps of ids and the order of the pages of a space.
		if run.jiraPeak > 256<<20 || run.wikiPeak > 256<<20 {
			t.Errorf("peak heap: %d MiB for the issues, %d MiB for the pages", run.jiraPeak>>20, run.wikiPeak>>20)
		}
		return
	}
	small := measureLoad(t, 250, 125, 10)
	large := measureLoad(t, 1000, 500, 10)
	// Four times the issues and pages need about the same memory: what grows is only the maps of ids (a few
	// dozen bytes per entity), well below the slack. Holding the pages' bodies (10 KB each) would not be.
	const slack = 24 << 20
	if large.jiraPeak > small.jiraPeak*3/2+slack {
		t.Errorf("the load of 4x the issues needs %d MiB, of 1x %d MiB", large.jiraPeak>>20, small.jiraPeak>>20)
	}
	if large.wikiPeak > small.wikiPeak*3/2+slack {
		t.Errorf("the load of 4x the pages needs %d MiB, of 1x %d MiB", large.wikiPeak>>20, small.wikiPeak>>20)
	}
}
