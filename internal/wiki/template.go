// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

type texts map[string][2]string // key → {en, de}

func (t texts) get(key, lang string) string {
	v := t[key]
	if lang == "de" {
		return v[1]
	}
	return v[0]
}

var defaultTexts = texts{
	"space.home":    {"Home", "Startseite"},
	"space.welcome": {"Welcome to this space. Edit this page to describe what the space is about.", "Willkommen in diesem Bereich. Bearbeiten Sie diese Seite, um den Bereich vorzustellen."},
	"page.copyOf":   {"Copy of %s", "Kopie von %s"},
}

// builtinTemplates are created once in the instance language. Bodies are Markdown; {{name}}
// marks variables the author fills in.
var builtinTemplates = []struct {
	key    string
	kind   string
	name   [2]string
	desc   [2]string
	labels []string
	body   [2]string
}{
	{"meeting-notes", "page", [2]string{"Meeting notes", "Besprechungsnotizen"},
		[2]string{"Agenda, attendees, decisions and action items", "Tagesordnung, Teilnehmende, Entscheidungen und Aufgaben"},
		[]string{"meeting-notes"},
		[2]string{"## Date\n\n{{date}}\n\n## Attendees\n\n- {{attendees}}\n\n## Agenda\n\n1. \n\n## Notes\n\n\n\n## Decisions\n\n- \n\n## Action items\n\n- [ ] \n",
			"## Datum\n\n{{datum}}\n\n## Teilnehmende\n\n- {{teilnehmende}}\n\n## Tagesordnung\n\n1. \n\n## Notizen\n\n\n\n## Entscheidungen\n\n- \n\n## Aufgaben\n\n- [ ] \n"}},
	{"decision", "page", [2]string{"Decision", "Entscheidung"},
		[2]string{"Record a decision with its background and options", "Eine Entscheidung mit Hintergrund und Optionen festhalten"},
		[]string{"decision"},
		[2]string{"| Status | Owner | Due |\n|---|---|---|\n| Open | {{owner}} | {{due}} |\n\n## Background\n\n\n\n## Options\n\n| Option | Pros | Cons |\n|---|---|---|\n|  |  |  |\n\n## Decision\n\n\n\n## Consequences\n\n",
			"| Status | Verantwortlich | Fällig |\n|---|---|---|\n| Offen | {{verantwortlich}} | {{faellig}} |\n\n## Hintergrund\n\n\n\n## Optionen\n\n| Option | Vorteile | Nachteile |\n|---|---|---|\n|  |  |  |\n\n## Entscheidung\n\n\n\n## Folgen\n\n"}},
	{"requirements", "page", [2]string{"Product requirements", "Produktanforderungen"},
		[2]string{"Goals, user stories and open questions of a feature", "Ziele, User Stories und offene Fragen eines Features"},
		[]string{"requirements"},
		[2]string{"## Goals\n\n- \n\n## Background and strategic fit\n\n\n\n## Requirements\n\n| # | User story | Importance | Notes |\n|---|---|---|---|\n| 1 |  | Must have |  |\n\n## User interaction and design\n\n\n\n## Questions\n\n| Question | Outcome |\n|---|---|\n|  |  |\n\n## Not doing\n\n- \n",
			"## Ziele\n\n- \n\n## Hintergrund und strategische Einordnung\n\n\n\n## Anforderungen\n\n| # | User Story | Wichtigkeit | Hinweise |\n|---|---|---|---|\n| 1 |  | Muss |  |\n\n## Bedienung und Gestaltung\n\n\n\n## Fragen\n\n| Frage | Ergebnis |\n|---|---|\n|  |  |\n\n## Nicht im Umfang\n\n- \n"}},
	{"how-to", "page", [2]string{"How-to article", "Anleitung"},
		[2]string{"Step-by-step instructions", "Schritt-für-Schritt-Anleitung"},
		[]string{"how-to"},
		[2]string{"Describe in one sentence what this article helps with.\n\n## Steps\n\n1. \n2. \n3. \n\n## Related articles\n\n- \n",
			"Beschreiben Sie in einem Satz, wobei diese Anleitung hilft.\n\n## Schritte\n\n1. \n2. \n3. \n\n## Verwandte Artikel\n\n- \n"}},
	{"retrospective", "page", [2]string{"Retrospective", "Retrospektive"},
		[2]string{"What went well, what to improve, actions", "Was lief gut, was verbessern wir, Maßnahmen"},
		[]string{"retrospective"},
		[2]string{"## Sprint\n\n{{sprint}}\n\n## What went well\n\n- \n\n## What could be improved\n\n- \n\n## Actions\n\n- [ ] \n",
			"## Sprint\n\n{{sprint}}\n\n## Was lief gut\n\n- \n\n## Was können wir verbessern\n\n- \n\n## Maßnahmen\n\n- [ ] \n"}},
	{"troubleshooting", "page", [2]string{"Troubleshooting article", "Fehlerbehebung"},
		[2]string{"A problem, its cause and the solution", "Ein Problem, seine Ursache und die Lösung"},
		[]string{"troubleshooting"},
		[2]string{"## Problem\n\n\n\n## Cause\n\n\n\n## Solution\n\n1. \n",
			"## Problem\n\n\n\n## Ursache\n\n\n\n## Lösung\n\n1. \n"}},
	{"release-notes", "page", [2]string{"Release notes", "Release Notes"},
		[2]string{"What is new in a version", "Was eine Version Neues bringt"},
		[]string{"release-notes"},
		[2]string{"## Version {{version}}\n\n## Highlights\n\n- \n\n## Changes\n\n- \n\n## Known issues\n\n- \n",
			"## Version {{version}}\n\n## Höhepunkte\n\n- \n\n## Änderungen\n\n- \n\n## Bekannte Probleme\n\n- \n"}},
}

// EnsureDefaults creates the built-in templates in the instance's language, once.
func (s *Service) EnsureDefaults(ctx context.Context) error {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	i := 0
	if st.DefaultLocale == "de" {
		i = 1
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7366200)"); err != nil {
			return err
		}
		for _, t := range builtinTemplates {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM page_templates WHERE builtin = $1)", t.key).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			raw, _ := docmodel.Marshal(docmodel.FromMarkdown(t.body[i]))
			if _, err := tx.Exec(ctx, `INSERT INTO page_templates (name, description, kind, body, labels, builtin) VALUES ($1, $2, $3, $4, $5, $6)`,
				t.name[i], t.desc[i], t.kind, raw, t.labels, t.key); err != nil {
				return err
			}
		}
		return nil
	})
}

// Template is a page template, global or of a space.
type Template struct {
	ID          int64         `json:"id"`
	SpaceKey    string        `json:"spaceKey,omitempty" doc:"Empty for global templates"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Kind        string        `json:"kind" enum:"page,blog"`
	Body        *docmodel.Doc `json:"body"`
	Labels      []string      `json:"labels"`
	Builtin     bool          `json:"builtin"`
	Updated     time.Time     `json:"updated"`
	CanEdit     bool          `json:"canEdit"`
}

func (s *Service) scanTemplates(ctx context.Context, a *Access, where string, args ...any) ([]Template, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id, coalesce(s.key, ''), coalesce(t.space_id, 0), t.name, t.description, t.kind, t.body, t.labels,
		t.builtin IS NOT NULL, t.updated_at FROM page_templates t LEFT JOIN spaces s ON s.id = t.space_id WHERE `+where+
		` ORDER BY t.space_id NULLS FIRST, lower(t.name)`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Template, error) {
		var t Template
		var spaceID int64
		var body []byte
		if err := row.Scan(&t.ID, &t.SpaceKey, &spaceID, &t.Name, &t.Description, &t.Kind, &body, &t.Labels, &t.Builtin, &t.Updated); err != nil {
			return t, err
		}
		t.Body = &docmodel.Doc{}
		if t.Labels == nil {
			t.Labels = []string{}
		}
		if spaceID == 0 {
			t.CanEdit = a.admin
		} else {
			t.CanEdit = a.Can(spaceID, PermAdmin)
		}
		return t, json.Unmarshal(body, t.Body)
	})
	return nonNil(out), err
}

// Templates lists the global templates and those of a space (if given).
func (s *Service) Templates(ctx context.Context, p *identity.Principal, spaceKey string) ([]Template, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if spaceKey == "" {
		return s.scanTemplates(ctx, a, "t.space_id IS NULL")
	}
	sp, err := s.visibleSpace(ctx, a, spaceKey)
	if err != nil {
		return nil, err
	}
	// Templates are content: administrators who see the space to repair its grants do not read them (audit L21).
	if !a.Can(sp.ID, PermView) {
		return s.scanTemplates(ctx, a, "t.space_id IS NULL")
	}
	return s.scanTemplates(ctx, a, "(t.space_id IS NULL OR t.space_id = $1)", sp.ID)
}

func (s *Service) template(ctx context.Context, a *Access, spaceID, id int64) (*Template, error) {
	list, err := s.scanTemplates(ctx, a, "t.id = $1 AND (t.space_id IS NULL OR t.space_id = $2)", id, spaceID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("template.not-found", "The template does not exist.")
	}
	return &list[0], nil
}

// TemplateInput creates or changes a template.
type TemplateInput struct {
	Name        string        `json:"name" minLength:"1" maxLength:"200"`
	Description string        `json:"description,omitempty" maxLength:"1000"`
	Kind        string        `json:"kind,omitempty" enum:"page,blog" default:"page"`
	Body        *docmodel.Doc `json:"body"`
	Labels      []string      `json:"labels,omitempty"`
}

// SaveTemplate creates (id 0) or changes a template of a space (space administrators) or a global one (spaceKey
// empty; global administrators).
func (s *Service) SaveTemplate(ctx context.Context, p *identity.Principal, spaceKey string, id int64, in TemplateInput) (*Template, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var spaceID any
	if spaceKey != "" {
		sp, err := s.visibleSpace(ctx, a, spaceKey)
		if err != nil {
			return nil, err
		}
		if !a.Can(sp.ID, PermAdmin) {
			return nil, apperr.Forbidden("space.admin-required", "Only administrators of the space manage its templates.")
		}
		spaceID = sp.ID
	} else if _, err := s.authz.RequireGlobal(ctx, authz.Admin); err != nil {
		return nil, err
	}
	opts, err := s.docOptions(ctx)
	if err != nil {
		return nil, err
	}
	body, err := validateDoc(in.Body, opts)
	if err != nil {
		return nil, err
	}
	labels := []string{}
	for _, l := range in.Labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if !validLabel(l) {
			return nil, apperr.Invalidf("template.label", "Labels are lower case without spaces: %s", l)
		}
		labels = append(labels, l)
	}
	kind := in.Kind
	if kind == "" {
		kind = "page"
	}
	raw, _ := docmodel.Marshal(body)
	if id == 0 {
		err = s.pool.QueryRow(ctx, `INSERT INTO page_templates (space_id, name, description, kind, body, labels, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, spaceID, strings.TrimSpace(in.Name), in.Description, kind, raw, labels, a.userID).Scan(&id)
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = s.pool.Exec(ctx, `UPDATE page_templates SET name = $3, description = $4, kind = $5, body = $6, labels = $7, updated_at = now()
			WHERE id = $1 AND space_id IS NOT DISTINCT FROM $2`, id, spaceID, strings.TrimSpace(in.Name), in.Description, kind, raw, labels)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, apperr.NotFound("template.not-found", "The template does not exist.")
		}
	}
	if err != nil {
		return nil, err
	}
	list, err := s.scanTemplates(ctx, a, "t.id = $1", id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// DeleteTemplate removes a template.
func (s *Service) DeleteTemplate(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	var spaceID *int64
	err = s.pool.QueryRow(ctx, "SELECT space_id FROM page_templates WHERE id = $1", id).Scan(&spaceID)
	if db.IsNoRows(err) {
		return apperr.NotFound("template.not-found", "The template does not exist.")
	}
	if err != nil {
		return err
	}
	if spaceID != nil && !a.Can(*spaceID, PermView) {
		return apperr.NotFound("template.not-found", "The template does not exist.")
	}
	if (spaceID == nil && !a.admin) || (spaceID != nil && !a.Can(*spaceID, PermAdmin)) {
		return apperr.Forbidden("template.denied", "You may not delete this template.")
	}
	_, err = s.pool.Exec(ctx, "DELETE FROM page_templates WHERE id = $1", id)
	return err
}
