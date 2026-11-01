// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestHistoryQueries: WAS finds issues by the values their fields had (when, set by whom), CHANGED by their changes
// (when, by whom, from and to what); values that never changed count from the issue's creation.
func TestHistoryQueries(t *testing.T) {
	f := setup(t)
	story := f.cfg.typ("story")
	var v10, v20 struct {
		ID int64 `json:"id"`
	}
	f.lead.MustDo(http.MethodPost, "/api/v1/projects/SHOP/versions", map[string]any{"name": "1.0"}, &v10)
	f.lead.MustDo(http.MethodPost, "/api/v1/projects/SHOP/versions", map[string]any{"name": "2.0"}, &v20)
	worked := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": story, "summary": "Worked on"})
	untouched := f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": story, "summary": "Untouched"})

	// SHOP-1: assigned to Dirk by Lena, started and finished by Dirk; 1.0 became 2.0.
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+worked.Key, map[string]any{"fields": map[string]any{"assignee": f.ids["dirk"],
		"fixVersions": []int64{v10.ID}}}, nil)
	var transitions []struct {
		ID int `json:"id"`
		To ref `json:"to"`
	}
	f.dev.Get("/api/v1/issues/"+worked.Key+"/transitions", &transitions)
	move := func(category string) {
		t.Helper()
		f.dev.Get("/api/v1/issues/"+worked.Key+"/transitions", &transitions)
		for _, tr := range transitions {
			if tr.To.Category == category {
				f.dev.MustDo(http.MethodPost, "/api/v1/issues/"+worked.Key+"/transitions", map[string]any{"transitionId": tr.ID}, nil)
				return
			}
		}
		t.Fatalf("no transition to %s: %+v", category, transitions)
	}
	move("inprogress")
	f.lead.MustDo(http.MethodPatch, "/api/v1/issues/"+worked.Key, map[string]any{"fields": map[string]any{"fixVersions": []int64{v20.ID}}}, nil)
	move("done")

	var cfg struct {
		Statuses []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"statuses"`
	}
	f.admin.Get("/api/v1/issue-config", &cfg)
	name := func(category string) string {
		for _, s := range cfg.Statuses {
			if s.Category == category {
				return `"` + s.Name + `"`
			}
		}
		t.Fatalf("no status of %s", category)
		return ""
	}
	todo, progress, done := name("todo"), name("inprogress"), name("done")
	for q, want := range map[string][]string{
		"status WAS " + progress:                                 {worked.Key},
		"status WAS NOT " + progress:                             {untouched.Key},
		"status WAS IN (" + progress + ", " + done + ")":         {worked.Key},
		"status WAS NOT IN (" + progress + ", " + done + ")":     {untouched.Key},
		"status WAS " + todo:                                     {worked.Key, untouched.Key},
		"status WAS " + progress + " BY dirk":                    {worked.Key},
		"status WAS " + progress + " BY lena":                    {},
		"status WAS " + todo + " BY lena":                        {worked.Key, untouched.Key}, // since the creation
		"status WAS " + progress + " AFTER -1h":                  {worked.Key},
		"status WAS " + progress + " BEFORE -1h":                 {},
		"status WAS " + todo + " BEFORE -1h":                     {},
		"status WAS " + done + " DURING (-1d, 1d)":               {worked.Key},
		"status WAS " + done + " ON now()":                       {worked.Key},
		"assignee WAS dirk":                                      {worked.Key},
		"assignee WAS EMPTY":                                     {worked.Key, untouched.Key},
		"assignee WAS dirk BY lena":                              {worked.Key},
		"resolution WAS unresolved":                              {worked.Key, untouched.Key},
		"fixVersion WAS \"1.0\"":                                 {worked.Key},
		"fixVersion WAS \"2.0\" BY lena":                         {worked.Key},
		"fixVersion WAS NOT \"1.0\"":                             {untouched.Key},
		"status CHANGED":                                         {worked.Key},
		"status CHANGED FROM " + todo + " TO " + progress:        {worked.Key},
		"status CHANGED FROM " + done:                            {},
		"status CHANGED BY dirk AFTER -1h":                       {worked.Key},
		"status CHANGED BY lena":                                 {},
		"status CHANGED BEFORE -1h":                              {},
		"assignee CHANGED FROM EMPTY TO dirk":                    {worked.Key},
		"fixVersion CHANGED TO \"2.0\"":                          {worked.Key},
		"fixVersion CHANGED FROM \"1.0\" TO \"2.0\"":             {worked.Key},
		"fixVersion CHANGED FROM \"2.0\"":                        {},
		"project = SHOP AND NOT status CHANGED ORDER BY created": {untouched.Key},
	} {
		got := search(t, f.lead, q)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", q, got, want)
		}
	}

	for q, frag := range map[string]string{
		"summary WAS x":                   "WAS and CHANGED work with",
		"status WAS Nope":                 "does not exist",
		"fixVersion WAS EMPTY":            "IS EMPTY",
		"status CHANGED BY nobody-at-all": "does not exist",
	} {
		r := f.lead.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil)
		if r.Status != http.StatusBadRequest && r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), frag) {
			t.Errorf("%s: %d %s", q, r.Status, r.Body)
		}
	}

	// After an upgrade, the spans of existing issues are derived from their history.
	ctx := context.Background()
	if _, err := f.env.App.Pool.Exec(ctx, "DELETE FROM issue_field_spans; DELETE FROM data_migrations WHERE name = 'issue-field-spans'"); err != nil {
		t.Fatal(err)
	}
	if err := f.env.App.Issues.DeriveSpans(ctx); err != nil {
		t.Fatal(err)
	}
	if got := search(t, f.lead, "status WAS "+progress+" BY dirk"); !slices.Equal(got, []string{worked.Key}) {
		t.Errorf("after deriving the spans: %v", got)
	}
}
