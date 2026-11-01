-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- An installation at schema version 9 (migrations 00001 to 00009: platform, identity, issues, agile, wiki, code,
-- LFS and builds), as Kogge wrote it then: the issue configuration and the page templates are the rows its
-- defaults created (German, the instance's language); the other rows are written in the form of the code of that
-- time (e.g. history items name fix versions by their names, the search text includes restricted comments). Some
-- rows have what later migrations repair: a sub-task without the security level of its parent (00021), a run that
-- kept the code project of its moved repository (00025), a group created by a migration (00027), personal drafts
-- that become shared drafts (00039).

-- The instance settings with the keys of that time; later keys take their defaults.
INSERT INTO settings (key, value, updated_by) VALUES ('instance', '{"name": "Werft", "defaultLocale": "de",
    "defaultTimeZone": "Europe/Berlin", "contentLanguage": "german", "allowAnonymous": false, "allowExternalImages": false,
    "maxAttachmentMb": 100, "hoursPerDay": 8, "daysPerWeek": 5, "auditRetentionDays": 730, "trashRetentionDays": 0,
    "buildLogRetentionDays": 0, "maxJobTimeoutMinutes": 1440, "announcement": ""}', 101);

-- Users (password Test-User-Secret-1, hashed with Argon2id), groups and the administrator.
INSERT INTO users (id, username, display_name, email, email_verified, kind, status, password_hash, locale, time_zone,
    created_at, updated_at, last_login_at) VALUES
    (101, 'alice', 'Alice Adler', 'alice@example.com', true, 'person', 'active',
     '$argon2id$v=19$m=1024,t=1,p=1$mqP9a5MA0lbTPbucoMSy0Q$A+9n7v7yaSdhm1zcrK0jA0tKwQLYLc9j8FRmTBSNkZE', 'de', 'Europe/Berlin',
     '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00', '2026-01-07 15:00:00+00'),
    (102, 'bob', 'Bob Builder', 'bob@example.com', true, 'person', 'active',
     '$argon2id$v=19$m=1024,t=1,p=1$mqP9a5MA0lbTPbucoMSy0Q$A+9n7v7yaSdhm1zcrK0jA0tKwQLYLc9j8FRmTBSNkZE', 'en', NULL,
     '2026-01-05 08:10:00+00', '2026-01-05 08:10:00+00', '2026-01-07 15:30:00+00'),
    (103, 'carol', 'Carol Clerk', 'carol@example.com', false, 'person', 'active',
     '$argon2id$v=19$m=1024,t=1,p=1$mqP9a5MA0lbTPbucoMSy0Q$A+9n7v7yaSdhm1zcrK0jA0tKwQLYLc9j8FRmTBSNkZE', NULL, NULL,
     '2026-01-05 08:20:00+00', '2026-01-05 08:20:00+00', NULL),
    (104, 'dave', 'Dave Departed', 'dave@example.com', true, 'person', 'disabled', NULL, NULL, NULL,
     '2026-01-05 08:30:00+00', '2026-01-06 08:30:00+00', NULL);
INSERT INTO groups (id, name, description, source, created_at) VALUES
    (1, 'Entwicklung', 'Die Entwicklerinnen und Entwickler', 'local', '2026-01-05 08:40:00+00'),
    (2, 'Altbestand', 'Created by a migration.', 'local', '2026-01-05 08:41:00+00');
INSERT INTO group_members (group_id, user_id) VALUES (1, 102), (2, 103);
INSERT INTO global_grants (permission, principal_type, principal_id) VALUES ('admin', 'user', 101);
INSERT INTO access_tokens (id, user_id, name, token_hash, hint, scopes, expires_at, created_at) VALUES
    (1, 102, 'CI', sha256('kgp_fixture-token-of-bob'::bytea), 'kgp_fixt…', '{read,write}', NULL, '2026-01-05 09:00:00+00');
INSERT INTO audit_events (at, actor_id, actor_ip, action, target_type, target_id, details) VALUES
    ('2026-01-05 09:00:00+00', 101, '10.0.0.5', 'auth.login', 'user', '101', '{"method": "password"}'),
    ('2026-01-05 09:30:00+00', 101, '10.0.0.5', 'project.create', 'project', 'SHOP', '{}'),
    ('2026-01-05 09:40:00+00', 101, '10.0.0.5', 'group.member.add', 'group', '1', '{"user": 102}');

-- The issue configuration that Kogge's defaults created (internal/issues/defaults.go), and its page templates.
INSERT INTO custom_fields (id, name, description, type, options, config, project_ids, issue_type_ids, created_at) VALUES (1, 'Story Points', '', 'number', '[]', '{}', '{}', '{}', '2026-01-05 08:00:00+00');
INSERT INTO custom_fields (id, name, description, type, options, config, project_ids, issue_type_ids, created_at) VALUES (2, 'Startdatum', '', 'date', '[]', '{}', '{}', '{}', '2026-01-05 08:00:00+00');
INSERT INTO issue_link_types (id, name, outward, inward) VALUES (1, 'Blockiert', 'blockiert', 'wird blockiert von');
INSERT INTO issue_link_types (id, name, outward, inward) VALUES (2, 'Duplikat', 'dupliziert', 'wird dupliziert von');
INSERT INTO issue_link_types (id, name, outward, inward) VALUES (3, 'Bezieht sich', 'bezieht sich auf', 'bezieht sich auf');
INSERT INTO issue_link_types (id, name, outward, inward) VALUES (4, 'Verursacht', 'verursacht', 'wird verursacht von');
INSERT INTO issue_types (id, name, description, icon, color, level, "position", created_at) VALUES (1, 'Epic', '', 'epic', 'violet', 1, 1, '2026-01-05 08:00:00+00');
INSERT INTO issue_types (id, name, description, icon, color, level, "position", created_at) VALUES (2, 'Story', '', 'story', 'green', 0, 2, '2026-01-05 08:00:00+00');
INSERT INTO issue_types (id, name, description, icon, color, level, "position", created_at) VALUES (3, 'Aufgabe', '', 'task', 'blue', 0, 3, '2026-01-05 08:00:00+00');
INSERT INTO issue_types (id, name, description, icon, color, level, "position", created_at) VALUES (4, 'Fehler', '', 'bug', 'red', 0, 4, '2026-01-05 08:00:00+00');
INSERT INTO issue_types (id, name, description, icon, color, level, "position", created_at) VALUES (5, 'Unteraufgabe', '', 'subtask', 'teal', -1, 5, '2026-01-05 08:00:00+00');
INSERT INTO notification_schemes (id, name, description) VALUES (1, 'Standard-Benachrichtigungsschema', '');
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'created', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'created', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'updated', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'updated', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'updated', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'assigned', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'assigned', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'assigned', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'transitioned', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'transitioned', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'transitioned', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'resolved', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'resolved', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'resolved', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'commented', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'commented', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'commented', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'comment-edited', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'comment-edited', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'comment-edited', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'work-logged', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'work-logged', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'work-logged', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'deleted', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'deleted', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'deleted', 'watchers', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'moved', 'reporter', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'moved', 'assignee', 0);
INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id) VALUES (1, 'moved', 'watchers', 0);
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (1, NULL, 'Besprechungsnotizen', 'Tagesordnung, Teilnehmende, Entscheidungen und Aufgaben', 'page', '{"type": "doc", "content": [{"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Datum", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "{{datum}}", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Teilnehmende", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph", "content": [{"text": "{{teilnehmende}}", "type": "text"}]}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Tagesordnung", "type": "text"}]}, {"type": "orderedList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Notizen", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Entscheidungen", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Aufgaben", "type": "text"}]}, {"type": "taskList", "content": [{"type": "taskItem", "attrs": {"checked": false}, "content": [{"type": "paragraph"}]}]}], "version": 1}', '{meeting-notes}', 'meeting-notes', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (2, NULL, 'Entscheidung', 'Eine Entscheidung mit Hintergrund und Optionen festhalten', 'page', '{"type": "doc", "content": [{"type": "table", "content": [{"type": "tableRow", "content": [{"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Status", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Verantwortlich", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Fällig", "type": "text"}]}]}]}, {"type": "tableRow", "content": [{"type": "tableCell", "content": [{"type": "paragraph", "content": [{"text": "Offen", "type": "text"}]}]}, {"type": "tableCell", "content": [{"type": "paragraph", "content": [{"text": "{{verantwortlich}}", "type": "text"}]}]}, {"type": "tableCell", "content": [{"type": "paragraph", "content": [{"text": "{{faellig}}", "type": "text"}]}]}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Hintergrund", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Optionen", "type": "text"}]}, {"type": "table", "content": [{"type": "tableRow", "content": [{"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Option", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Vorteile", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Nachteile", "type": "text"}]}]}]}, {"type": "tableRow", "content": [{"type": "tableCell", "content": [{"type": "paragraph"}]}, {"type": "tableCell", "content": [{"type": "paragraph"}]}, {"type": "tableCell", "content": [{"type": "paragraph"}]}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Entscheidung", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Folgen", "type": "text"}]}], "version": 1}', '{decision}', 'decision', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (3, NULL, 'Produktanforderungen', 'Ziele, User Stories und offene Fragen eines Features', 'page', '{"type": "doc", "content": [{"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Ziele", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Hintergrund und strategische Einordnung", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Anforderungen", "type": "text"}]}, {"type": "table", "content": [{"type": "tableRow", "content": [{"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "#", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "User Story", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Wichtigkeit", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Hinweise", "type": "text"}]}]}]}, {"type": "tableRow", "content": [{"type": "tableCell", "content": [{"type": "paragraph", "content": [{"text": "1", "type": "text"}]}]}, {"type": "tableCell", "content": [{"type": "paragraph"}]}, {"type": "tableCell", "content": [{"type": "paragraph", "content": [{"text": "Muss", "type": "text"}]}]}, {"type": "tableCell", "content": [{"type": "paragraph"}]}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Bedienung und Gestaltung", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Fragen", "type": "text"}]}, {"type": "table", "content": [{"type": "tableRow", "content": [{"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Frage", "type": "text"}]}]}, {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"text": "Ergebnis", "type": "text"}]}]}]}, {"type": "tableRow", "content": [{"type": "tableCell", "content": [{"type": "paragraph"}]}, {"type": "tableCell", "content": [{"type": "paragraph"}]}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Nicht im Umfang", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}], "version": 1}', '{requirements}', 'requirements', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (4, NULL, 'Anleitung', 'Schritt-für-Schritt-Anleitung', 'page', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Beschreiben Sie in einem Satz, wobei diese Anleitung hilft.", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Schritte", "type": "text"}]}, {"type": "orderedList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}, {"type": "listItem", "content": [{"type": "paragraph"}]}, {"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Verwandte Artikel", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}], "version": 1}', '{how-to}', 'how-to', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (5, NULL, 'Retrospektive', 'Was lief gut, was verbessern wir, Maßnahmen', 'page', '{"type": "doc", "content": [{"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Sprint", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "{{sprint}}", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Was lief gut", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Was können wir verbessern", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Maßnahmen", "type": "text"}]}, {"type": "taskList", "content": [{"type": "taskItem", "attrs": {"checked": false}, "content": [{"type": "paragraph"}]}]}], "version": 1}', '{retrospective}', 'retrospective', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (6, NULL, 'Fehlerbehebung', 'Ein Problem, seine Ursache und die Lösung', 'page', '{"type": "doc", "content": [{"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Problem", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Ursache", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Lösung", "type": "text"}]}, {"type": "orderedList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}], "version": 1}', '{troubleshooting}', 'troubleshooting', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO page_templates (id, space_id, name, description, kind, body, labels, builtin, created_by, created_at, updated_at) VALUES (7, NULL, 'Release Notes', 'Was eine Version Neues bringt', 'page', '{"type": "doc", "content": [{"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Version {{version}}", "type": "text"}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Höhepunkte", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Änderungen", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}, {"type": "heading", "attrs": {"level": 2}, "content": [{"text": "Bekannte Probleme", "type": "text"}]}, {"type": "bulletList", "content": [{"type": "listItem", "content": [{"type": "paragraph"}]}]}], "version": 1}', '{release-notes}', 'release-notes', NULL, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO permission_schemes (id, name, description, created_at) VALUES (1, 'Standard-Berechtigungsschema', '', '2026-01-05 08:00:00+00');
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.browse', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.create', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.add', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.edit-own', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.delete-own', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.create', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.delete-own', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'watchers.view', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.view-development', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.link', 'role', 3);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.browse', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.create', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.add', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.edit-own', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.delete-own', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.create', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.delete-own', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'watchers.view', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.view-development', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.link', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.edit', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.transition', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.schedule', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.assign', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.assignable', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.resolve', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.move', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.manage-sprints', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'watchers.manage', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.add', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.edit-own', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.delete-own', 'role', 2);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.browse', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.admin', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.manage-sprints', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'project.view-development', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.create', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.edit', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.transition', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.schedule', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.move', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.assign', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.assignable', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.resolve', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.modify-reporter', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.delete', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.link', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'issue.set-security', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'watchers.view', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'watchers.manage', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.add', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.edit-all', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.edit-own', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.delete-all', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'comment.delete-own', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.create', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.delete-all', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'attachment.delete-own', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.add', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.edit-all', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.edit-own', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.delete-all', 'role', 1);
INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES (1, 'worklog.delete-own', 'role', 1);
INSERT INTO priorities (id, name, description, icon, color, "position", is_default) VALUES (1, 'Höchste', '', 'highest', 'red', 1, false);
INSERT INTO priorities (id, name, description, icon, color, "position", is_default) VALUES (2, 'Hoch', '', 'high', 'orange', 2, false);
INSERT INTO priorities (id, name, description, icon, color, "position", is_default) VALUES (3, 'Mittel', '', 'medium', 'yellow', 3, true);
INSERT INTO priorities (id, name, description, icon, color, "position", is_default) VALUES (4, 'Niedrig', '', 'low', 'blue', 4, false);
INSERT INTO priorities (id, name, description, icon, color, "position", is_default) VALUES (5, 'Niedrigste', '', 'lowest', 'gray', 5, false);
INSERT INTO project_roles (id, name, description) VALUES (1, 'Administratoren', '');
INSERT INTO project_roles (id, name, description) VALUES (2, 'Entwickler', '');
INSERT INTO project_roles (id, name, description) VALUES (3, 'Benutzer', '');
INSERT INTO resolutions (id, name, description, "position", is_default) VALUES (1, 'Erledigt', '', 1, true);
INSERT INTO resolutions (id, name, description, "position", is_default) VALUES (2, 'Wird nicht erledigt', '', 2, false);
INSERT INTO resolutions (id, name, description, "position", is_default) VALUES (3, 'Duplikat', '', 3, false);
INSERT INTO resolutions (id, name, description, "position", is_default) VALUES (4, 'Nicht reproduzierbar', '', 4, false);
INSERT INTO statuses (id, name, description, category, "position", created_at) VALUES (1, 'Offen', '', 'todo', 1, '2026-01-05 08:00:00+00');
INSERT INTO statuses (id, name, description, category, "position", created_at) VALUES (2, 'In Arbeit', '', 'inprogress', 2, '2026-01-05 08:00:00+00');
INSERT INTO statuses (id, name, description, category, "position", created_at) VALUES (3, 'In Prüfung', '', 'inprogress', 3, '2026-01-05 08:00:00+00');
INSERT INTO statuses (id, name, description, category, "position", created_at) VALUES (4, 'Erledigt', '', 'done', 4, '2026-01-05 08:00:00+00');
INSERT INTO workflows (id, name, description, definition, version, created_at, updated_at) VALUES (1, 'Einfacher Workflow', '', '{"initial": 1, "options": {"autoResolution": true}, "statuses": [1, 2, 4], "transitions": [{"id": 10, "to": 1, "from": "any", "name": "Offen"}, {"id": 20, "to": 2, "from": "any", "name": "In Arbeit"}, {"id": 30, "to": 4, "from": "any", "name": "Erledigt"}]}', 1, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');
INSERT INTO workflows (id, name, description, definition, version, created_at, updated_at) VALUES (2, 'Software-Workflow', '', '{"initial": 1, "options": {"autoResolution": true}, "statuses": [1, 2, 3, 4], "transitions": [{"id": 11, "to": 2, "from": [1], "name": "Bearbeitung beginnen"}, {"id": 21, "to": 1, "from": [2], "name": "Bearbeitung beenden"}, {"id": 31, "to": 3, "from": [2], "name": "Prüfung anfordern"}, {"id": 41, "to": 2, "from": [3], "name": "Änderungen anfordern"}, {"id": 51, "to": 4, "from": "any", "name": "Erledigen"}, {"id": 61, "to": 1, "from": [4], "name": "Wieder öffnen"}]}', 1, '2026-01-05 08:00:00+00', '2026-01-05 08:00:00+00');

INSERT INTO settings (key, value) VALUES ('issues.defaults', '{"prio.low": 4, "res.done": 1, "type.bug": 4, "prio.high": 2,
    "role.devs": 2, "type.epic": 1, "type.task": 3, "wf.simple": 1, "res.cannot": 4, "res.wontdo": 2, "role.users": 3, "type.story": 2,
    "field.start": 2, "link.blocks": 1, "link.causes": 4, "prio.lowest": 5, "prio.medium": 3, "role.admins": 1, "status.done": 4,
    "status.todo": 1, "wf.software": 2, "field.points": 1, "link.relates": 3, "prio.highest": 1, "type.subtask": 5, "res.duplicate": 3,
    "status.review": 3, "link.duplicate": 2, "status.progress": 2, "scheme.permissions": 1, "scheme.notifications": 1}');

-- The project SHOP (template scrum) with a security scheme: alice administers it, the group Entwicklung develops,
-- carol only browses.
INSERT INTO security_schemes (id, name, description) VALUES (1, 'Vertraulich', 'Fehler, die nicht alle sehen sollen');
INSERT INTO security_levels (id, scheme_id, name, description, position) VALUES (1, 1, 'Nur Team', '', 0);
INSERT INTO security_level_members (level_id, holder_type, holder_id) VALUES (1, 'role', 1), (1, 'role', 2);
INSERT INTO issue_projects (id, key, name, description, type, lead_id, permission_scheme_id, notification_scheme_id,
    security_scheme_id, default_workflow_id, next_number, created_at, updated_at) VALUES
    (1, 'SHOP', 'Shop', 'Der Webshop', 'software', 101, 1, 1, 1, 2, 4, '2026-01-05 09:30:00+00', '2026-01-05 09:30:00+00');
INSERT INTO issue_project_keys (key, project_id) VALUES ('SHOP', 1);
INSERT INTO project_issue_types (project_id, issue_type_id, workflow_id, position) VALUES
    (1, 1, NULL, 0), (1, 2, NULL, 1), (1, 3, NULL, 2), (1, 4, NULL, 3), (1, 5, NULL, 4);
INSERT INTO project_role_members (project_id, role_id, principal_type, principal_id) VALUES
    (1, 1, 'user', 101), (1, 2, 'user', 101), (1, 2, 'group', 1), (1, 3, 'user', 103);
INSERT INTO components (id, project_id, name, description, lead_id) VALUES (1, 1, 'Kasse', 'Bezahlen und Belege', 101);
INSERT INTO versions (id, project_id, name, description, start_date, release_date, released, released_at, position) VALUES
    (1, 1, '1.0', '', '2026-01-05', '2026-01-09', true, '2026-01-09 12:00:00+00', 0),
    (2, 1, '2.0', '', '2026-01-12', '2026-02-06', false, NULL, 1);

-- SHOP-1 went from Offen to In Arbeit to Erledigt, with fix versions named in its history; SHOP-2 is secured; its
-- sub-task SHOP-3 was created without the level (00021 gives it its parent's).
INSERT INTO issues (id, project_id, number, key, type_id, status_id, priority_id, resolution_id, summary, description,
    description_text, reporter_id, creator_id, assignee_id, parent_id, security_level_id, original_estimate,
    remaining_estimate, time_spent, labels, fields, rank, votes, created_at, updated_at, resolved_at, status_changed_at, version) VALUES
    (1, 1, 1, 'SHOP-1', 3, 4, 3, 1, 'Zweiten Zahlungsanbieter anbinden',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "The checkout needs a second payment provider.", "type": "text"}]}], "version": 1}',
     'The checkout needs a second payment provider.', 101, 101, 102, NULL, NULL, 14400, 7200, 7200, '{payments}', '{"cf_1": 5}',
     'V', 1, '2026-01-05 10:00:00+00', '2026-01-07 16:00:00+00', '2026-01-07 16:00:00+00', '2026-01-07 16:00:00+00', 6),
    (2, 1, 2, 'SHOP-2', 4, 1, 2, NULL, 'Zahlungen schlagen nachts fehl', NULL, '', 102, 102, NULL, NULL, 1, NULL, NULL, 0,
     '{}', '{}', 'k', 0, '2026-01-06 09:00:00+00', '2026-01-06 09:00:00+00', NULL, '2026-01-06 09:00:00+00', 1),
    (3, 1, 3, 'SHOP-3', 5, 1, 3, NULL, 'Protokolle des Anbieters prüfen', NULL, '', 102, 102, 102, 2, NULL, NULL, NULL, 0,
     '{}', '{}', 'n', 0, '2026-01-06 09:30:00+00', '2026-01-06 09:30:00+00', NULL, '2026-01-06 09:30:00+00', 1);
INSERT INTO issue_components (issue_id, component_id) VALUES (1, 1);
INSERT INTO issue_versions (issue_id, version_id, kind) VALUES (1, 1, 'fix'), (1, 2, 'fix'), (2, 1, 'affects');
INSERT INTO issue_changes (issue_id, actor_id, at, items) VALUES
    (1, 101, '2026-01-05 10:05:00+00', '[{"to": 102, "field": "assignee", "toString": "Bob Builder"}, {"field": "fixVersions", "toString": "1.0"}, {"field": "labels", "toString": "payments"}]'),
    (1, 101, '2026-01-05 10:06:00+00', '[{"to": 1, "field": "sprint", "toString": "Sprint 1"}]'),
    (1, 102, '2026-01-06 08:00:00+00', '[{"to": 2, "from": 1, "field": "status", "toString": "In Arbeit", "fromString": "Offen"}]'),
    (1, 101, '2026-01-06 12:00:00+00', '[{"field": "fixVersions", "toString": "1.0, 2.0", "fromString": "1.0"}]'),
    (1, 102, '2026-01-07 16:00:00+00', '[{"to": 4, "from": 2, "field": "status", "toString": "Erledigt", "fromString": "In Arbeit"}, {"to": 1, "field": "resolution", "toString": "Erledigt"}]'),
    (2, 102, '2026-01-06 09:05:00+00', '[{"to": 2, "field": "sprint", "toString": "Sprint 2"}]');
INSERT INTO issue_comments (id, issue_id, author_id, body, body_text, visibility_type, visibility_id, created_at, updated_at) VALUES
    (1, 1, 102, '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Angefangen mit dem Adapter.", "type": "text"}]}], "version": 1}',
     'Angefangen mit dem Adapter.', NULL, NULL, '2026-01-06 08:05:00+00', '2026-01-06 08:05:00+00'),
    (2, 1, 101, '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Nur für das Team: der alte Anbieter heißt zanzibar.", "type": "text"}]}], "version": 1}',
     'Nur für das Team: der alte Anbieter heißt zanzibar.', 'role', 2, '2026-01-06 09:00:00+00', '2026-01-06 09:00:00+00'),
    (3, 2, 102, '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Seit dem Update.", "type": "text"}]}], "version": 1}',
     'Seit dem Update.', NULL, NULL, '2026-01-06 09:10:00+00', '2026-01-06 09:10:00+00');
INSERT INTO worklogs (id, issue_id, author_id, started_at, seconds, comment, created_at, updated_at) VALUES
    (1, 1, 102, '2026-01-06 08:00:00+00', 7200, 'Adapter', '2026-01-06 10:00:00+00', '2026-01-06 10:00:00+00');
INSERT INTO issue_links (id, link_type_id, source_id, target_id, created_by, created_at) VALUES (1, 1, 1, 2, 102, '2026-01-06 09:20:00+00');
INSERT INTO issue_votes (issue_id, user_id) VALUES (1, 103);
INSERT INTO watchers (entity_type, entity_id, user_id, created_at) VALUES
    ('issue', 1, 101, '2026-01-05 10:00:00+00'), ('issue', 1, 102, '2026-01-05 10:05:00+00'), ('issue', 2, 102, '2026-01-06 09:00:00+00');
INSERT INTO notifications (user_id, created_at, read_at, category, event, entity_type, entity_id, actor_id, title, summary, url, data) VALUES
    (102, '2026-01-05 10:05:00+00', NULL, 'issues', 'issue.assigned', 'issue', 'SHOP-1', 101, 'SHOP-1 Zweiten Zahlungsanbieter anbinden',
     'Alice Adler hat Ihnen den Vorgang zugewiesen.', '/issues/SHOP-1', '{}');
INSERT INTO activity (at, actor_id, verb, entity_type, entity_id, container_type, container_id, title, url) VALUES
    ('2026-01-05 10:00:00+00', 101, 'created', 'issue', 1, 'project', 1, 'SHOP-1 Zweiten Zahlungsanbieter anbinden', '/issues/SHOP-1');
INSERT INTO saved_filters (id, owner_id, name, description, query, columns, shares, created_at, updated_at) VALUES
    (1, 101, 'Offene Fehler', '', 'project = SHOP AND type = Fehler AND resolution IS EMPTY ORDER BY created DESC', '{}', '[]',
     '2026-01-05 11:00:00+00', '2026-01-05 11:00:00+00');
INSERT INTO saved_filter_favourites (filter_id, user_id) VALUES (1, 101);

-- The project's board with a closed sprint (SHOP-1) and the active one (SHOP-2), and a dashboard.
INSERT INTO boards (id, project_id, name, type, query, config, owner_id, created_at, updated_at) VALUES
    (1, 1, 'SHOP-Board', 'scrum', 'project = SHOP ORDER BY rank', '{"card": {"colors": "issuetype", "fields": []}, "columns": [{"name": "Offen", "statuses": [1]}, {"name": "In Arbeit", "statuses": [2]}, {"name": "In Prüfung", "statuses": [3]}, {"name": "Erledigt", "statuses": [4]}], "swimlanes": {"type": "none"}, "estimation": {"type": "field", "field": "cf_1"}, "quickFilters": [{"id": 1, "name": "Nur meine Vorgänge", "query": "assignee = currentUser()"}, {"id": 2, "name": "Kürzlich geändert", "query": "updated >= -1d"}]}',
     101, '2026-01-05 09:30:00+00', '2026-01-05 09:30:00+00');
INSERT INTO sprints (id, board_id, name, goal, state, start_at, end_at, completed_at, position, created_at) VALUES
    (1, 1, 'Sprint 1', 'Bezahlen', 'closed', '2026-01-05 09:00:00+00', '2026-01-19 09:00:00+00', '2026-01-08 09:00:00+00', 0, '2026-01-05 09:35:00+00'),
    (2, 1, 'Sprint 2', '', 'active', '2026-01-08 09:30:00+00', '2026-01-22 09:30:00+00', NULL, 1, '2026-01-05 09:36:00+00');
INSERT INTO issue_sprints (issue_id, sprint_id) VALUES (1, 1), (2, 2);
INSERT INTO sprint_baselines (sprint_id, issue_id, estimate, done) VALUES (1, 1, 5, true), (2, 2, NULL, false);
INSERT INTO dashboards (id, owner_id, name, description, columns, widgets, shares, created_at, updated_at) VALUES
    (1, 101, 'Überblick', '', 2, '[]', '[]', '2026-01-05 11:30:00+00', '2026-01-05 11:30:00+00');

-- The space DOCS: the home page, Runbook (two versions, three personal drafts: alice's and bob's of the current
-- version, carol's of the first), a page in the trash, and a page only alice may see.
INSERT INTO spaces (id, key, name, description, type, created_by, created_at, updated_at) VALUES
    (1, 'DOCS', 'Betrieb', 'Wie der Shop läuft', 'team', 101, '2026-01-05 11:00:00+00', '2026-01-05 11:00:00+00');
INSERT INTO space_permissions (space_id, permission, holder_type, holder_id) VALUES
    (1, 'space.view', 'logged-in', 0), (1, 'page.edit', 'logged-in', 0), (1, 'page.delete-own', 'logged-in', 0),
    (1, 'comment.add', 'logged-in', 0), (1, 'attachment.add', 'logged-in', 0), (1, 'space.export', 'logged-in', 0),
    (1, 'space.admin', 'user', 101), (1, 'space.view', 'user', 101), (1, 'page.edit', 'user', 101), (1, 'page.delete', 'user', 101),
    (1, 'page.delete-own', 'user', 101), (1, 'page.archive', 'user', 101), (1, 'page.restrict', 'user', 101),
    (1, 'comment.add', 'user', 101), (1, 'comment.delete', 'user', 101), (1, 'attachment.add', 'user', 101),
    (1, 'attachment.delete', 'user', 101), (1, 'space.export', 'user', 101);
INSERT INTO pages (id, space_id, parent_id, kind, status, title, body, body_text, rank, version, creator_id, created_at,
    updated_by, updated_at, published_at, trashed_at, trashed_by, view_restricted_by) VALUES
    (1, 1, NULL, 'page', 'current', 'Betrieb – Startseite',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Willkommen im Betrieb.", "type": "text"}]}], "version": 1}',
     'Willkommen im Betrieb.', '', 1, 101, '2026-01-05 11:00:00+00', 101, '2026-01-05 11:00:00+00', '2026-01-05 11:00:00+00', NULL, NULL, '{}'),
    (2, 1, 1, 'page', 'current', 'Runbook',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Call SHOP-1''s assignee first.", "type": "text"}]}], "version": 1}',
     E'Restart the shop with care.\nCall SHOP-1''s assignee first.', 'V', 2, 101, '2026-01-05 11:10:00+00', 101,
     '2026-01-06 14:00:00+00', '2026-01-05 11:10:00+00', NULL, NULL, '{}'),
    (3, 1, 1, 'page', 'trashed', 'Altes Konzept',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Überholt.", "type": "text"}]}], "version": 1}',
     'Überholt.', 'k', 1, 101, '2026-01-05 11:20:00+00', 101, '2026-01-08 10:00:00+00', '2026-01-05 11:20:00+00',
     '2026-01-08 10:00:00+00', 101, '{}'),
    (4, 1, 1, 'page', 'current', 'Zugangsdaten',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Nur für alice.", "type": "text"}]}], "version": 1}',
     'Nur für alice.', 'n', 1, 101, '2026-01-05 11:30:00+00', 101, '2026-01-05 11:30:00+00', '2026-01-05 11:30:00+00', NULL, NULL, '{4}');
UPDATE spaces SET home_page_id = 1 WHERE id = 1;
INSERT INTO page_restrictions (page_id, kind, holder_type, holder_id) VALUES (4, 'view', 'user', 101), (4, 'edit', 'user', 101);
INSERT INTO page_versions (page_id, version, title, body, author_id, created_at, message, minor) VALUES
    (1, 1, 'Betrieb – Startseite', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Willkommen im Betrieb.", "type": "text"}]}], "version": 1}',
     101, '2026-01-05 11:00:00+00', '', false),
    (2, 1, 'Runbook', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care.", "type": "text"}]}], "version": 1}',
     101, '2026-01-05 11:10:00+00', 'First version', false),
    (2, 2, 'Runbook', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Call SHOP-1''s assignee first.", "type": "text"}]}], "version": 1}',
     101, '2026-01-06 14:00:00+00', 'Who to call', false),
    (3, 1, 'Altes Konzept', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Überholt.", "type": "text"}]}], "version": 1}',
     101, '2026-01-05 11:20:00+00', '', false),
    (4, 1, 'Zugangsdaten', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Nur für alice.", "type": "text"}]}], "version": 1}',
     101, '2026-01-05 11:30:00+00', '', false);
INSERT INTO page_drafts (page_id, user_id, title, body, base_version, updated_at) VALUES
    (2, 101, 'Runbook', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Call SHOP-1''s assignee first.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Alice: and the provider.", "type": "text"}]}], "version": 1}',
     2, '2026-01-09 11:00:00+00'),
    (2, 102, 'Runbook', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Call SHOP-1''s assignee first.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Bob: check the logs.", "type": "text"}]}], "version": 1}',
     2, '2026-01-09 10:00:00+00'),
    (2, 103, 'Runbook (Carol)', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Restart the shop with care, Carol says.", "type": "text"}]}], "version": 1}',
     1, '2026-01-08 09:00:00+00');
INSERT INTO page_comments (id, page_id, parent_id, author_id, body, body_text, created_at, updated_at) VALUES
    (1, 2, NULL, 103, '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Danke!", "type": "text"}]}], "version": 1}',
     'Danke!', '2026-01-06 15:00:00+00', '2026-01-06 15:00:00+00');
INSERT INTO page_labels (page_id, label) VALUES (2, 'betrieb');
INSERT INTO page_likes (page_id, user_id, created_at) VALUES (2, 102, '2026-01-06 15:10:00+00');
INSERT INTO page_issue_refs (page_id, issue_key) VALUES (2, 'SHOP-1');
INSERT INTO page_visits (user_id, page_id, visited_at) VALUES (102, 2, '2026-01-09 10:00:00+00');
INSERT INTO watchers (entity_type, entity_id, user_id, created_at) VALUES ('page', 2, 101, '2026-01-05 11:10:00+00');

-- Code: the repository shop with a pull request of bob, and legacy, which moved from the code project OLD to PLAT
-- (its run kept OLD, 00025 corrects that). carol may read OLD only.
INSERT INTO code_projects (id, key, name, description, created_by, created_at, updated_at) VALUES
    (1, 'PLAT', 'Plattform', '', 101, '2026-01-05 12:00:00+00', '2026-01-05 12:00:00+00'),
    (2, 'OLD', 'Altlasten', '', 101, '2026-01-04 12:00:00+00', '2026-01-04 12:00:00+00');
INSERT INTO repositories (id, project_id, slug, name, description, default_branch, settings, size_bytes, next_pr_number,
    pushed_at, created_by, created_at, updated_at) VALUES
    (1, 1, 'shop', 'shop', 'Der Webshop', 'main', '{}', 2048, 2, '2026-01-06 11:00:00+00', 101, '2026-01-05 12:00:00+00', '2026-01-06 11:00:00+00'),
    (2, 1, 'legacy', 'legacy', '', 'main', '{}', 1024, 1, '2026-01-04 10:00:00+00', 101, '2026-01-04 12:00:00+00', '2026-01-05 12:30:00+00');
INSERT INTO repository_redirects (project_key, slug, repository_id) VALUES ('OLD', 'legacy', 2);
INSERT INTO code_permissions (project_id, repository_id, permission, holder_type, holder_id) VALUES
    (1, NULL, 'codeproject.admin', 'user', 101), (1, NULL, 'repo.write', 'group', 1), (2, NULL, 'codeproject.admin', 'user', 101),
    (2, NULL, 'repo.read', 'user', 103);
INSERT INTO ssh_keys (id, kind, user_id, repository_id, name, public_key, fingerprint, write, created_by, created_at) VALUES
    (1, 'user', 102, NULL, 'bob@laptop', 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPnqMXNeVgNelptQkdO8IkHkRccr2lMbK/U/kPgKTuwE',
     'SHA256:NqRVVI6Y/5RhjJ65+xKwHZu5jcB71ypSJHwCi6FG+fA', false, 102, '2026-01-05 12:10:00+00');
INSERT INTO branch_permissions (id, project_id, repository_id, pattern, kind, exempt_users, created_at) VALUES
    (1, NULL, 1, 'main', 'no-rewrite', '{}', '2026-01-05 12:05:00+00');
INSERT INTO pushes (id, repository_id, user_id, updates, pushed_at) VALUES
    (1, 1, 101, '[{"new": "bb4378685a62737ca0e66461a4d5b5ba9bc98bde", "old": "0000000000000000000000000000000000000000", "ref": "refs/heads/main", "fastForward": true}]',
     '2026-01-05 12:01:00+00'),
    (2, 1, 102, '[{"new": "b1a580b7116917a5b73fbd6a4b8b9e117f971def", "old": "0000000000000000000000000000000000000000", "ref": "refs/heads/feature/SHOP-1", "fastForward": true}]',
     '2026-01-06 11:00:00+00');
INSERT INTO pull_requests (id, repository_id, number, title, description, description_text, author_id, source_branch,
    target_branch, source_sha, target_sha, merge_base, state, created_at, updated_at) VALUES
    (1, 1, 1, 'SHOP-1 Adapter für den zweiten Anbieter',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Bitte prüfen.", "type": "text"}]}], "version": 1}',
     'Bitte prüfen.', 102, 'feature/SHOP-1', 'main', 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde',
     'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'open', '2026-01-06 11:05:00+00', '2026-01-06 11:30:00+00');
INSERT INTO pull_request_reviewers (pull_request_id, user_id, state, reviewed_sha, updated_at) VALUES
    (1, 101, 'approved', 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', '2026-01-06 11:30:00+00');
INSERT INTO pull_request_comments (id, pull_request_id, author_id, body, body_text, path, line, side, commit_sha, line_text,
    created_at, updated_at) VALUES
    (1, 1, 101, '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Schön.", "type": "text"}]}], "version": 1}',
     'Schön.', 'adapter.go', 3, 'new', 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', '// Adapter talks to the second payment provider.',
     '2026-01-06 11:20:00+00', '2026-01-06 11:20:00+00');
INSERT INTO pull_request_activities (id, pull_request_id, actor_id, kind, data, at) VALUES
    (1, 1, 102, 'opened', '{"source": "feature/SHOP-1", "target": "main"}', '2026-01-06 11:05:00+00'),
    (2, 1, 101, 'reviewed', '{"state": "approved"}', '2026-01-06 11:30:00+00');
INSERT INTO issue_commits (issue_key, repository_id, sha, message, author_name, author_email, author_id, committed_at) VALUES
    ('SHOP-1', 1, 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', 'SHOP-1 Adapter for the second provider', 'Bob Builder',
     'bob@example.com', 102, '2026-01-06 11:00:00+00');
INSERT INTO issue_branches (issue_key, repository_id, branch, created_at) VALUES ('SHOP-1', 1, 'feature/SHOP-1', '2026-01-06 11:00:00+00');
INSERT INTO issue_pull_requests (issue_key, pull_request_id) VALUES ('SHOP-1', 1);

-- Builds: an agent, a run of shop with a test report, and the run of legacy from before its move.
INSERT INTO agent_tokens (id, token_hash, hint, project_id, description, created_by, created_at, uses) VALUES
    (1, sha256('kgr_fixture-registration'::bytea), 'kgr_fixt…', NULL, 'Build-Hosts', 101, '2026-01-05 12:20:00+00', 1);
INSERT INTO agents (id, name, project_id, secret_hash, labels, os, arch, version, enabled, created_at, last_seen_at, last_ip) VALUES
    (1, 'build-01', NULL, sha256('kga_fixture-agent-1'::bytea), '{shell,linux,amd64}', 'linux', 'amd64', 'dev', true,
     '2026-01-05 12:21:00+00', '2026-01-07 16:00:00+00', '10.0.0.9');
INSERT INTO runs (id, repository_id, project_id, number, pipeline, pipeline_name, event, branch, commit_sha, commit_subject,
    triggered_by, variables, state, created_at, started_at, finished_at) VALUES
    (1, 1, 1, 1, 'ci', 'ci', 'push', 'main', 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'Initial commit', 101, '{}', 'succeeded',
     '2026-01-05 12:01:00+00', '2026-01-05 12:21:30+00', '2026-01-05 12:22:00+00'),
    (2, 2, 2, 1, 'ci', 'ci', 'push', 'main', 'ba7925faee16cd1ce32c6b54511fa13c6899bd0d', 'Legacy build', 101, '{}', 'failed',
     '2026-01-04 10:00:00+00', '2026-01-05 12:21:40+00', '2026-01-05 12:21:50+00');
INSERT INTO run_jobs (id, run_id, stage_index, stage_name, name, spec, labels, state, attempt, agent_id, exit_code, failure,
    log_size, created_at, started_at, finished_at) VALUES
    (1, 1, 0, 'Build', 'test', '{"job": {"Line": 8, "Name": "test", "Only": null, "Cache": null, "Clone": {"LFS": true, "Depth": 50, "Submodules": false}, "Image": "", "Retry": 0, "Steps": [{"Run": "go test ./...", "Name": "go test ./...", "When": "on-success", "Shell": "", "Timeout": 0, "ContinueOnError": false, "WorkingDirectory": ""}], "Except": null, "Manual": false, "RunsOn": ["shell"], "Reports": {"JUnit": null}, "Timeout": 3600000000000, "Services": null, "Artifacts": null, "Variables": {}, "Environment": "", "AllowFailure": false, "Dependencies": null}, "variables": {}}',
     '{shell}', 'succeeded', 1, 1, 0, '', 26, '2026-01-05 12:01:00+00', '2026-01-05 12:21:30+00', '2026-01-05 12:22:00+00'),
    (2, 2, 0, 'Build', 'test', '{"job": {"Line": 8, "Name": "test", "Only": null, "Cache": null, "Clone": {"LFS": true, "Depth": 50, "Submodules": false}, "Image": "", "Retry": 0, "Steps": [{"Run": "make", "Name": "make", "When": "on-success", "Shell": "", "Timeout": 0, "ContinueOnError": false, "WorkingDirectory": ""}], "Except": null, "Manual": false, "RunsOn": ["shell"], "Reports": {"JUnit": null}, "Timeout": 3600000000000, "Services": null, "Artifacts": null, "Variables": {}, "Environment": "", "AllowFailure": false, "Dependencies": null}, "variables": {}}',
     '{shell}', 'failed', 1, 1, 2, 'exit status 2', 0, '2026-01-04 10:00:00+00', '2026-01-05 12:21:40+00', '2026-01-05 12:21:50+00');
INSERT INTO test_reports (job_id, total, failed, errors, skipped, duration_ms, failures, created_at) VALUES
    (1, 3, 0, 0, 1, 1200, '[]', '2026-01-05 12:22:00+00');
INSERT INTO commit_statuses (id, repository_id, sha, context, state, description, url, run_id, updated_at) VALUES
    (1, 1, 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'kogge/ci', 'success', 'ci', 'http://kogge.example.com/code/plat/shop/builds/1', 1,
     '2026-01-05 12:22:00+00');
INSERT INTO build_variables (id, scope, scope_id, name, value, sealed, secret, protected, updated_by, updated_at) VALUES
    (1, 'repository', 1, 'DEPLOY_TOKEN', '', 'v1:4f73fa16:NxtqlmzknUg6vdtikOBFsEurrK07EA90uAL5+3/9W+eomtAx//REGzII3fQ=', true, true, 101,
     '2026-01-05 12:30:00+00'),
    (2, 'repository', 1, 'GREETING', 'moin', NULL, false, false, 101, '2026-01-05 12:30:00+00');
INSERT INTO environments (id, repository_id, name, description, refs, approvals, approver_users, created_at) VALUES
    (1, 1, 'production', 'Der Live-Shop', '{main}', 1, '{101}', '2026-01-05 12:35:00+00');
INSERT INTO repository_build_settings (repository_id, auto_cancel, max_parallel_jobs) VALUES (1, true, 2);
INSERT INTO build_schedules (repository_id, pipeline, branch, cron) VALUES (1, 'nightly', 'main', '0 2 * * *');

-- The search texts as the code of that time computed them: restricted comments were part of the issue's.
UPDATE issues i SET search_tsv =
    setweight(to_tsvector('german', i.key || ' ' || i.summary), 'A') ||
    setweight(to_tsvector('german', i.description_text), 'B') ||
    setweight(to_tsvector('german', coalesce((SELECT string_agg(c.body_text, ' ') FROM issue_comments c WHERE c.issue_id = i.id), '')), 'C') ||
    setweight(to_tsvector('german', i.environment || ' ' || array_to_string(i.labels, ' ') || ' ' ||
        coalesce((SELECT string_agg(e.value #>> '{}', ' ') FROM jsonb_each(i.fields) e WHERE jsonb_typeof(e.value) = 'string'), '')), 'D');
UPDATE pages p SET search_tsv =
    setweight(to_tsvector('german', p.title), 'A') ||
    setweight(to_tsvector('german', p.body_text), 'B') ||
    setweight(to_tsvector('german', coalesce((SELECT string_agg(label, ' ') FROM page_labels WHERE page_id = p.id), '')), 'A') ||
    setweight(to_tsvector('german', coalesce((SELECT string_agg(c.body_text, ' ') FROM page_comments c WHERE c.page_id = p.id), '')), 'C');
