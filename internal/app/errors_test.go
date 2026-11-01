// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestErrorsInGerman: errors are worded in the language the client asks for — by operations of the API and by
// the handlers outside it — with their values, and the hints of invalid fields, too; without German, in English.
func TestErrorsInGerman(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	german := []string{"Accept-Language", "de-DE,de;q=0.9,en;q=0.8"}
	detail := func(r *apitest.Response) string {
		t.Helper()
		var p struct {
			Detail string `json:"detail"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		r.Decode(&p)
		if len(p.Errors) > 0 {
			return p.Detail + " | " + p.Errors[0].Message
		}
		return p.Detail
	}
	for _, c := range []struct {
		r    *apitest.Response
		want string
	}{
		{admin.Do(http.MethodGet, "/api/v1/code/repos/NOPE/nothing/search?q=pay", nil, german...),
			"Das Repository NOPE/nothing existiert nicht, oder Sie dürfen es nicht sehen."},
		{admin.Do(http.MethodGet, "/api/v1/code/repos/NOPE/nothing/search?q=pay", nil),
			"The repository NOPE/nothing does not exist or you may not see it."},
		// A handler outside the API description (the agent protocol).
		{admin.Do(http.MethodPost, "/api/agent/v1/jobs/claim", map[string]any{}, append(german, "Authorization", "Bearer nonsense")...),
			"Unbekannter oder deaktivierter Agent."},
		{admin.Do(http.MethodGet, "/api/v1/search?q=project%20%3D%20NOPE", nil, german...),
			"Das Projekt „NOPE“ existiert nicht, oder Sie dürfen es nicht sehen. | Das Projekt „NOPE“ existiert nicht, oder Sie dürfen es nicht sehen."},
	} {
		if got := detail(c.r); got != c.want {
			t.Errorf("%d %q, want %q", c.r.Status, got, c.want)
		}
	}
	// Syntax errors of the issue query language, with what was found.
	if got := detail(admin.Do(http.MethodGet, "/api/v1/search?q=project%20%3D", nil, german...)); got !=
		"Wert erwartet; gefunden: Ende der Abfrage | Wert erwartet; gefunden: Ende der Abfrage" {
		t.Errorf("query syntax: %q", got)
	}
	// The hint of an invalid field is a message of its own.
	var cfg struct {
		IssueTypes []struct {
			ID int64 `json:"id"`
		} `json:"issueTypes"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "GER", "name": "German", "template": "scrum"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "GER", "typeId": cfg.IssueTypes[0].ID, "summary": "Rechnungen"}, nil)
	r := admin.Do(http.MethodPatch, "/api/v1/issues/GER-1", map[string]any{"fields": map[string]any{"duedate": 5}}, german...)
	if got := detail(r); got != "Der Wert von duedate ist ungültig: Datum als JJJJ-MM-TT erwartet | Datum als JJJJ-MM-TT erwartet" {
		t.Errorf("invalid field: %d %q", r.Status, got)
	}
}
