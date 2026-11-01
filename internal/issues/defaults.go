// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/platform/db"
)

type texts map[string][2]string // key → {en, de}

var defaultTexts = texts{
	"type.epic": {"Epic", "Epic"}, "type.story": {"Story", "Story"}, "type.task": {"Task", "Aufgabe"},
	"type.bug": {"Bug", "Fehler"}, "type.subtask": {"Sub-task", "Unteraufgabe"},
	"status.todo": {"To Do", "Offen"}, "status.progress": {"In Progress", "In Arbeit"},
	"status.review": {"In Review", "In Prüfung"}, "status.done": {"Done", "Erledigt"},
	"prio.highest": {"Highest", "Höchste"}, "prio.high": {"High", "Hoch"}, "prio.medium": {"Medium", "Mittel"},
	"prio.low": {"Low", "Niedrig"}, "prio.lowest": {"Lowest", "Niedrigste"},
	"res.done": {"Done", "Erledigt"}, "res.wontdo": {"Won't do", "Wird nicht erledigt"},
	"res.duplicate": {"Duplicate", "Duplikat"}, "res.cannot": {"Cannot reproduce", "Nicht reproduzierbar"},
	"link.blocks": {"Blocks", "Blockiert"}, "link.blocks.out": {"blocks", "blockiert"},
	"link.blocks.in": {"is blocked by", "wird blockiert von"},
	"link.duplicate": {"Duplicate", "Duplikat"}, "link.duplicate.out": {"duplicates", "dupliziert"},
	"link.duplicate.in": {"is duplicated by", "wird dupliziert von"},
	"link.relates":      {"Relates", "Bezieht sich"}, "link.relates.out": {"relates to", "bezieht sich auf"},
	"link.relates.in": {"relates to", "bezieht sich auf"},
	"link.causes":     {"Causes", "Verursacht"}, "link.causes.out": {"causes", "verursacht"},
	"link.causes.in": {"is caused by", "wird verursacht von"},
	"role.admins":    {"Administrators", "Administratoren"}, "role.devs": {"Developers", "Entwickler"},
	"role.users":   {"Users", "Benutzer"},
	"field.points": {"Story Points", "Story Points"}, "field.start": {"Start date", "Startdatum"},
	"wf.simple": {"Simple workflow", "Einfacher Workflow"}, "wf.software": {"Software workflow", "Software-Workflow"},
	"tr.start": {"Start progress", "Bearbeitung beginnen"}, "tr.stop": {"Stop progress", "Bearbeitung beenden"},
	"tr.review": {"Request review", "Prüfung anfordern"}, "tr.changes": {"Request changes", "Änderungen anfordern"},
	"tr.done": {"Done", "Erledigen"}, "tr.reopen": {"Reopen", "Wieder öffnen"},
	"board.name":           {"%s board", "%s-Board"},
	"board.quick.mine":     {"Only my issues", "Nur meine Vorgänge"},
	"board.quick.recent":   {"Recently updated", "Kürzlich geändert"},
	"scheme.permissions":   {"Default permission scheme", "Standard-Berechtigungsschema"},
	"scheme.notifications": {"Default notification scheme", "Standard-Benachrichtigungsschema"},
}

func (t texts) get(key, lang string) string {
	v := t[key]
	if lang == "de" {
		return v[1]
	}
	return v[0]
}

// EnsureDefaults creates the initial configuration (issue types, statuses, priorities, resolutions, link types,
// roles, workflows and schemes) in the instance's language, once.
func (s *Service) EnsureDefaults(ctx context.Context) error {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	lang := st.DefaultLocale
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7366100)"); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issue_types").Scan(&n); err != nil || n > 0 {
			return err
		}
		t := func(k string) string { return defaultTexts.get(k, lang) }
		ids := map[string]int64{}
		insert := func(key, sql string, args ...any) error {
			var id int64
			if err := tx.QueryRow(ctx, sql+" RETURNING id", args...).Scan(&id); err != nil {
				return err
			}
			ids[key] = id
			return nil
		}
		steps := []struct {
			key, sql string
			args     []any
		}{
			{"type.epic", "INSERT INTO issue_types (name, icon, color, level, position) VALUES ($1, 'epic', 'violet', 1, 1)", []any{t("type.epic")}},
			{"type.story", "INSERT INTO issue_types (name, icon, color, level, position) VALUES ($1, 'story', 'green', 0, 2)", []any{t("type.story")}},
			{"type.task", "INSERT INTO issue_types (name, icon, color, level, position) VALUES ($1, 'task', 'blue', 0, 3)", []any{t("type.task")}},
			{"type.bug", "INSERT INTO issue_types (name, icon, color, level, position) VALUES ($1, 'bug', 'red', 0, 4)", []any{t("type.bug")}},
			{"type.subtask", "INSERT INTO issue_types (name, icon, color, level, position) VALUES ($1, 'subtask', 'teal', -1, 5)", []any{t("type.subtask")}},
			{"status.todo", "INSERT INTO statuses (name, category, position) VALUES ($1, 'todo', 1)", []any{t("status.todo")}},
			{"status.progress", "INSERT INTO statuses (name, category, position) VALUES ($1, 'inprogress', 2)", []any{t("status.progress")}},
			{"status.review", "INSERT INTO statuses (name, category, position) VALUES ($1, 'inprogress', 3)", []any{t("status.review")}},
			{"status.done", "INSERT INTO statuses (name, category, position) VALUES ($1, 'done', 4)", []any{t("status.done")}},
			{"prio.highest", "INSERT INTO priorities (name, icon, color, position) VALUES ($1, 'highest', 'red', 1)", []any{t("prio.highest")}},
			{"prio.high", "INSERT INTO priorities (name, icon, color, position) VALUES ($1, 'high', 'orange', 2)", []any{t("prio.high")}},
			{"prio.medium", "INSERT INTO priorities (name, icon, color, position, is_default) VALUES ($1, 'medium', 'yellow', 3, true)", []any{t("prio.medium")}},
			{"prio.low", "INSERT INTO priorities (name, icon, color, position) VALUES ($1, 'low', 'blue', 4)", []any{t("prio.low")}},
			{"prio.lowest", "INSERT INTO priorities (name, icon, color, position) VALUES ($1, 'lowest', 'gray', 5)", []any{t("prio.lowest")}},
			{"res.done", "INSERT INTO resolutions (name, position, is_default) VALUES ($1, 1, true)", []any{t("res.done")}},
			{"res.wontdo", "INSERT INTO resolutions (name, position) VALUES ($1, 2)", []any{t("res.wontdo")}},
			{"res.duplicate", "INSERT INTO resolutions (name, position) VALUES ($1, 3)", []any{t("res.duplicate")}},
			{"res.cannot", "INSERT INTO resolutions (name, position) VALUES ($1, 4)", []any{t("res.cannot")}},
			{"role.admins", "INSERT INTO project_roles (name) VALUES ($1)", []any{t("role.admins")}},
			{"role.devs", "INSERT INTO project_roles (name) VALUES ($1)", []any{t("role.devs")}},
			{"role.users", "INSERT INTO project_roles (name) VALUES ($1)", []any{t("role.users")}},
			{"field.points", "INSERT INTO custom_fields (name, type) VALUES ($1, 'number')", []any{t("field.points")}},
			{"field.start", "INSERT INTO custom_fields (name, type) VALUES ($1, 'date')", []any{t("field.start")}},
		}
		for _, l := range []string{"blocks", "duplicate", "relates", "causes"} {
			steps = append(steps, struct {
				key, sql string
				args     []any
			}{"link." + l, "INSERT INTO issue_link_types (name, outward, inward) VALUES ($1, $2, $3)",
				[]any{t("link." + l), t("link." + l + ".out"), t("link." + l + ".in")}})
		}
		for _, step := range steps {
			if err := insert(step.key, step.sql, step.args...); err != nil {
				return err
			}
		}

		todo, prog, rev, done := ids["status.todo"], ids["status.progress"], ids["status.review"], ids["status.done"]
		simple := workflow.Simple([]int64{todo, prog, done}, map[int64]string{todo: t("status.todo"), prog: t("status.progress"), done: t("status.done")})
		software := &workflow.Definition{Statuses: []int64{todo, prog, rev, done}, Initial: todo,
			Options: workflow.Options{AutoResolution: true}, Transitions: []workflow.Transition{
				{ID: 11, Name: t("tr.start"), From: []int64{todo}, To: prog},
				{ID: 21, Name: t("tr.stop"), From: []int64{prog}, To: todo},
				{ID: 31, Name: t("tr.review"), From: []int64{prog}, To: rev},
				{ID: 41, Name: t("tr.changes"), From: []int64{rev}, To: prog},
				{ID: 51, Name: t("tr.done"), FromAny: true, To: done},
				{ID: 61, Name: t("tr.reopen"), From: []int64{done}, To: todo},
			}}
		for key, def := range map[string]*workflow.Definition{"wf.simple": simple, "wf.software": software} {
			b, _ := json.Marshal(def)
			if err := insert(key, "INSERT INTO workflows (name, definition) VALUES ($1, $2)", t(key), b); err != nil {
				return err
			}
		}

		if err := insert("scheme.permissions", "INSERT INTO permission_schemes (name) VALUES ($1)", t("scheme.permissions")); err != nil {
			return err
		}
		users := []string{PermBrowse, PermCreate, PermCommentAdd, PermCommentEditOwn, PermCommentDeleteOwn, PermAttachCreate,
			PermAttachDeleteOwn, PermViewWatchers, PermViewDevelopment, PermLink}
		// Developers change many issues at once too: bulk changes check each issue's own permissions as well.
		devs := append(append([]string{}, users...), PermEdit, PermTransition, PermSchedule, PermAssign, PermAssignable,
			PermResolve, PermMove, PermBulkChange, PermManageSprints, PermManageWatchers, PermWorklogAdd, PermWorklogEditOwn,
			PermWorklogDeleteOwn)
		grant := func(role string, perms []string) error {
			for _, p := range perms {
				if _, err := tx.Exec(ctx, `INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id)
					VALUES ($1, $2, 'role', $3) ON CONFLICT DO NOTHING`, ids["scheme.permissions"], p, ids[role]); err != nil {
					return err
				}
			}
			return nil
		}
		if err := grant("role.users", users); err != nil {
			return err
		}
		if err := grant("role.devs", devs); err != nil {
			return err
		}
		if err := grant("role.admins", ProjectPermissions); err != nil {
			return err
		}

		if err := insert("scheme.notifications", "INSERT INTO notification_schemes (name) VALUES ($1)", t("scheme.notifications")); err != nil {
			return err
		}
		for _, ev := range NotificationEvents {
			for _, r := range []string{"reporter", "assignee", "watchers"} {
				if ev == EventCreated && r == "watchers" {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type)
					VALUES ($1, $2, $3)`, ids["scheme.notifications"], ev, r); err != nil {
					return err
				}
			}
		}
		// Remember the built-in objects for project templates (their names are localised).
		b, _ := json.Marshal(ids)
		_, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('issues.defaults', $1)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, b)
		return err
	})
}

// defaultIDs returns the ids of the built-in configuration objects by key (e.g. "wf.simple").
func defaultIDs(ctx context.Context, q db.Querier) (map[string]int64, error) {
	var raw []byte
	err := q.QueryRow(ctx, "SELECT value FROM settings WHERE key = 'issues.defaults'").Scan(&raw)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	return ids, json.Unmarshal(raw, &ids)
}

// Templates for new projects.
var projectTemplates = map[string]struct {
	typ      string
	workflow string // "simple" or "software"
	types    []string
	board    string // board created with the project, if any
}{
	"scrum":    {"software", "software", []string{"epic", "story", "task", "bug", "subtask"}, BoardScrum},
	"kanban":   {"software", "simple", []string{"epic", "story", "task", "bug", "subtask"}, BoardKanban},
	"bugs":     {"software", "software", []string{"bug", "task", "subtask"}, BoardKanban},
	"business": {"business", "simple", []string{"task", "subtask"}, ""},
}
