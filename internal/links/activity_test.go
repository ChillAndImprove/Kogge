// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type changedField struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// detailEntry is an entry of a stream with what the change was.
type detailEntry struct {
	Verb   string `json:"verb"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Status *struct {
		From struct{ Name, Category string } `json:"from"`
		To   struct{ Name, Category string } `json:"to"`
	} `json:"status"`
	Fields  []changedField `json:"fields"`
	Excerpt string         `json:"excerpt"`
	Commits *struct {
		Count  int `json:"count"`
		Newest []struct {
			SHA     string `json:"sha"`
			Subject string `json:"subject"`
			URL     string `json:"url"`
		} `json:"newest"`
	} `json:"commits"`
	Refs []struct {
		Name string `json:"name"`
	} `json:"refs"`
	Pipeline string `json:"pipeline"`
	Number   int    `json:"number"`
}

func stream(t *testing.T, c *apitest.Client, query string) []detailEntry {
	t.Helper()
	var page struct {
		Entries []detailEntry `json:"entries"`
	}
	c.Get("/api/v1/activity-stream?limit=100&"+query, &page)
	return page.Entries
}

func find(entries []detailEntry, verb, title string) []detailEntry {
	var out []detailEntry
	for _, e := range entries {
		if e.Verb == verb && strings.Contains(e.Title, title) {
			out = append(out, e)
		}
	}
	return out
}

// TestActivityDetails: the entries of activity streams say what a change was — the statuses of a transition, the
// fields of an edit, the beginning of a comment as it is now (for who may read it), the author's note on a new
// version, the commits of a push, the pipeline and number of a run.
func TestActivityDetails(t *testing.T) {
	w := newSearchWorld(t)
	lena, eve := w.lena, w.eve

	// Kontor: OPEN-1 starts, gets labels, and comments: for everybody (edited later), with code, for the
	// administrators of the project only, and one that is deleted.
	var transitions []struct {
		ID int `json:"id"`
		To struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"to"`
	}
	lena.Get("/api/v1/issues/OPEN-1/transitions", &transitions)
	start := -1
	var started string
	for _, tr := range transitions {
		if tr.To.Category == "inprogress" {
			start, started = tr.ID, tr.To.Name
		}
	}
	if start < 0 {
		t.Fatalf("no transition into progress: %+v", transitions)
	}
	lena.Post("/api/v1/issues/OPEN-1/transitions", map[string]any{"transitionId": start}, nil)
	lena.MustDo(http.MethodPatch, "/api/v1/issues/OPEN-1", map[string]any{"fields": map[string]any{"labels": []string{"airship"},
		"environment": "Hangar 2"}}, nil)
	var public, deleted struct {
		ID int64 `json:"id"`
	}
	lena.Post("/api/v1/issues/OPEN-1/comments", map[string]any{"markdown": "First draft of the flight plan"}, &public)
	lena.MustDo(http.MethodPut, apitest.Fmt("/api/v1/issues/OPEN-1/comments/%d", public.ID),
		map[string]any{"markdown": "The **flight plan** is ready."}, nil)
	lena.Post("/api/v1/issues/OPEN-2/comments", map[string]any{"markdown": "## Checklist\n\n- Fuel\n- Crew\n\n```\nlaunch()\n```"}, nil)
	var roles []struct {
		Role struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"role"`
	}
	lena.Get("/api/v1/projects/OPEN/roles", &roles)
	var admins int64
	for _, r := range roles {
		if r.Role.Name == "Administrators" || r.Role.Name == "Administratoren" {
			admins = r.Role.ID
		}
	}
	lena.Post("/api/v1/issues/OPEN-3/comments", map[string]any{"markdown": "Only for the crew", "visibilityType": "role",
		"visibilityId": admins}, nil)
	lena.Post("/api/v1/issues/OPEN-5/comments", map[string]any{"markdown": "Posted by mistake"}, &deleted)
	lena.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/issues/OPEN-5/comments/%d", deleted.ID), nil, nil)

	kontor := stream(t, lena, "project=OPEN")
	if moves := find(kontor, "issue.transitioned", "OPEN-1 "); len(moves) != 1 || moves[0].Status == nil ||
		moves[0].Status.To.Name != started || moves[0].Status.To.Category != "inprogress" || moves[0].Status.From.Category != "todo" {
		t.Errorf("the transition: %+v", moves)
	}
	if edits := find(kontor, "issue.updated", "OPEN-1 "); len(edits) != 1 || len(edits[0].Fields) != 2 ||
		!slices.Contains(edits[0].Fields, changedField{Key: "labels"}) || !slices.Contains(edits[0].Fields, changedField{Key: "environment"}) {
		t.Errorf("the edit: %+v", edits)
	}
	// The comment shows its text as it is now; editing it adds no entry.
	if c := find(kontor, "issue.commented", "OPEN-1 "); len(c) != 1 || c[0].Excerpt != "The flight plan is ready." {
		t.Errorf("the edited comment: %+v", c)
	}
	if c := find(kontor, "issue.commented", "OPEN-2 "); len(c) != 1 || c[0].Excerpt != "Checklist · Fuel · Crew" {
		t.Errorf("the comment with code: %+v", c)
	}
	// A comment for a role or group shows no text in streams, which everybody who sees the issue reads.
	if c := find(kontor, "issue.commented", "OPEN-3 "); len(c) != 1 || c[0].Excerpt != "" {
		t.Errorf("lena's restricted comment: %+v", c)
	}
	if c := find(kontor, "issue.commented", "OPEN-5 "); len(c) != 1 || c[0].Excerpt != "" {
		t.Errorf("the deleted comment: %+v", c)
	}
	r := eve.MustDo(http.MethodGet, "/api/v1/activity-stream?limit=100&project=OPEN", nil, nil)
	if strings.Contains(string(r.Body), "crew") {
		t.Errorf("eve reads the restricted comment: %s", r.Body)
	}
	if c := find(stream(t, eve, "project=OPEN"), "issue.commented", "OPEN-3 "); len(c) != 1 || c[0].Excerpt != "" {
		t.Errorf("eve's view of the restricted comment: %+v", c)
	}

	// Logbuch: a new version with a note, and a comment.
	page := apitest.Fmt("/api/v1/wiki/pages/%d", w.ids["public page"])
	lena.Post(page+"/publish", map[string]any{"title": "Zeppelin public page", "baseVersion": 1,
		"message": "Added   the mooring times", "body": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "Notes about the flight and the mooring."}}}}}}, nil)
	lena.Post(page+"/comments", map[string]any{"body": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph",
		"content": []any{map[string]any{"type": "text", "text": "Which mast do we use?"}}}}}}, nil)
	logbuch := stream(t, eve, "space=OPEN")
	if u := find(logbuch, "page.updated", "Zeppelin public page"); len(u) != 1 || u[0].Excerpt != "Added the mooring times" {
		t.Errorf("the new version: %+v", u)
	}
	if c := find(logbuch, "page.commented", "Zeppelin public page"); len(c) != 1 || c[0].Excerpt != "Which mast do we use?" {
		t.Errorf("the page comment: %+v", c)
	}

	// Hafen: a commit in the browser, on a new branch, and a comment on the pull request.
	repo := "/api/v1/code/repos/OPNC/zeppelin-public"
	lena.MustDo(http.MethodPut, repo+"/files", map[string]any{"branch": "main", "newBranch": "mast", "path": "MAST.md",
		"content": "# Mast\n", "message": "Choose the mast\n\nThe northern one is free."}, nil)
	lena.Post(repo+"/pull-requests/1/comments", map[string]any{"markdown": "Merged **at last**."}, nil)
	w.env.WorkJobs()
	hafen := stream(t, eve, "repository=OPNC/zeppelin-public")
	var mast *detailEntry
	for i, e := range hafen {
		if e.Verb == "pushed" && len(e.Refs) == 1 && e.Refs[0].Name == "mast" {
			mast = &hafen[i]
		}
	}
	if mast == nil || mast.Commits == nil || mast.Commits.Count != 1 || len(mast.Commits.Newest) != 1 ||
		mast.Commits.Newest[0].Subject != "Choose the mast" || !strings.HasPrefix(mast.Commits.Newest[0].URL, "/code/opnc/zeppelin-public/commits/") ||
		!strings.HasSuffix(mast.Commits.Newest[0].URL, mast.Commits.Newest[0].SHA) {
		t.Errorf("the push of the new branch: %+v", mast)
	}
	if c := find(hafen, "pull-request.commented", "#1 "); len(c) != 1 || c[0].Excerpt != "Merged at last." {
		t.Errorf("the comment on the pull request: %+v", c)
	}
	// Helling: runs name their pipeline and number.
	runs := 0
	for _, e := range hafen {
		if strings.HasPrefix(e.Verb, "run.") {
			runs++
			if e.Number == 0 || !strings.HasPrefix(e.Title, apitest.Fmt("#%d ", e.Number)) || e.Pipeline == "" || !strings.HasSuffix(e.Title, e.Pipeline) {
				t.Errorf("a run: %+v", e)
			}
		}
	}
	if runs == 0 {
		t.Errorf("no runs: %+v", hafen)
	}
}
