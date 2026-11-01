// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// language is what an installation shows of its language: its settings and the names of its default configuration.
type language struct {
	locale, content string
	issueTypes      []string
	statuses        []string
	roles           []string
	templates       []string
}

func languageOf(t *testing.T, env *apitest.Env) language {
	t.Helper()
	admin := env.Admin()
	var st struct {
		DefaultLocale   string `json:"defaultLocale"`
		ContentLanguage string `json:"contentLanguage"`
	}
	admin.Get("/api/v1/admin/settings", &st)
	type named struct {
		Name string `json:"name"`
	}
	var cfg struct {
		IssueTypes []named `json:"issueTypes"`
		Statuses   []named `json:"statuses"`
		Roles      []named `json:"roles"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	var templates []named
	admin.Get("/api/v1/wiki/templates", &templates)
	names := func(list []named) []string {
		out := []string{}
		for _, n := range list {
			out = append(out, n.Name)
		}
		return out
	}
	return language{locale: st.DefaultLocale, content: st.ContentLanguage, issueTypes: names(cfg.IssueTypes),
		statuses: names(cfg.Statuses), roles: names(cfg.Roles), templates: names(templates)}
}

func (l language) has(t *testing.T, what string, list []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(list, w) {
			t.Errorf("%s: no %q in %v", what, w, list)
		}
	}
}

// TestLanguageOfANewInstallation: KOGGE_DEFAULT_LOCALE gives a new installation its language — the default of its
// users, the language of its content, and that of its default configuration (issue types, statuses, project roles,
// page templates); without it, a new installation is German. Later starts ignore it, with a note in the log.
func TestLanguageOfANewInstallation(t *testing.T) {
	english := apitest.New(t, func(c *config.Config) { c.DefaultLocale = "en" })
	en := languageOf(t, english)
	if en.locale != "en" || en.content != "english" {
		t.Errorf("English settings: %+v", en)
	}
	en.has(t, "issue types", en.issueTypes, "Task", "Bug", "Sub-task")
	en.has(t, "statuses", en.statuses, "To Do", "In Progress", "Done")
	en.has(t, "roles", en.roles, "Administrators", "Developers", "Users")
	en.has(t, "templates", en.templates, "How-to article", "Retrospective")

	german := apitest.New(t)
	de := languageOf(t, german)
	if de.locale != "de" || de.content != "german" {
		t.Errorf("German settings: %+v", de)
	}
	de.has(t, "issue types", de.issueTypes, "Aufgabe", "Fehler", "Unteraufgabe")
	de.has(t, "statuses", de.statuses, "Offen", "In Arbeit", "Erledigt")
	de.has(t, "roles", de.roles, "Administratoren", "Entwickler", "Benutzer")
	de.has(t, "templates", de.templates, "Anleitung", "Retrospektive")

	// A later start with KOGGE_DEFAULT_LOCALE=en changes nothing of the German installation, and says so.
	var logs bytes.Buffer
	cfg := *german.App.Cfg
	cfg.DefaultLocale = "en"
	later, err := app.New(context.Background(), &cfg, slog.New(slog.NewTextHandler(&logs, nil)),
		app.Options{Argon2: &crypto.Argon2Params{Memory: 1024, Time: 1, Threads: 1}})
	if err != nil {
		t.Fatal(err)
	}
	later.Close()
	if !strings.Contains(logs.String(), "KOGGE_DEFAULT_LOCALE applies to new installations only") {
		t.Errorf("no note in the log:\n%s", logs.String())
	}
	if again := languageOf(t, german); again.locale != "de" || !slices.Contains(again.issueTypes, "Aufgabe") ||
		slices.Contains(again.issueTypes, "Task") {
		t.Errorf("after a start with en: %+v", again)
	}
}
