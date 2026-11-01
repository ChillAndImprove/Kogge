-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Rows added at schema version 24 (00014 to 00024: queue times and protected jobs and caches, push protocols, the
-- directory sync, imported repositories, owners of webhooks and automation rules, approved agents). The older rows
-- went through 00017 and 00021 already.

-- erin came from single sign-on; the directory sync disabled her.
INSERT INTO users (id, username, display_name, email, email_verified, kind, status, oidc_issuer, oidc_subject,
    disabled_by_directory, created_at, updated_at, last_login_at) VALUES
    (105, 'erin', 'Erin Export', 'erin@example.com', true, 'person', 'disabled', 'https://sso.example.com/realms/kogge',
     '5f0e9a52-8a77-4c34-b6f6-7e0f3d2a1c11', true, '2026-02-01 08:00:00+00', '2026-02-09 03:00:00+00', '2026-02-02 08:00:00+00');

-- SHOP-4 is in progress with the fix version 2.0 (named in its history); SHOP-5 is a sub-task of the secured SHOP-2,
-- created with its parent's level.
INSERT INTO issues (id, project_id, number, key, type_id, status_id, priority_id, summary, description, description_text,
    reporter_id, creator_id, assignee_id, parent_id, security_level_id, labels, fields, rank, created_at, updated_at,
    status_changed_at, version) VALUES
    (4, 1, 4, 'SHOP-4', 2, 2, 3, 'Belege als PDF',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Kundinnen wollen ihre Belege herunterladen.", "type": "text"}]}], "version": 1}',
     'Kundinnen wollen ihre Belege herunterladen.', 101, 101, 102, NULL, NULL, '{}', '{"cf_1": 3}', 'q', '2026-02-02 09:00:00+00',
     '2026-02-02 10:30:00+00', '2026-02-02 10:00:00+00', 3),
    (5, 1, 5, 'SHOP-5', 5, 1, 3, 'Zeitzonen der Zahlungen prüfen', NULL, '', 102, 102, NULL, 2, 1, '{}', '{}', 'p',
     '2026-02-03 09:00:00+00', '2026-02-03 09:00:00+00', '2026-02-03 09:00:00+00', 1);
UPDATE issue_projects SET next_number = 6, updated_at = '2026-02-03 09:00:00+00' WHERE id = 1;
INSERT INTO issue_versions (issue_id, version_id, kind) VALUES (4, 2, 'fix');
INSERT INTO issue_changes (issue_id, actor_id, at, items) VALUES
    (4, 102, '2026-02-02 10:00:00+00', '[{"to": 2, "from": 1, "field": "status", "toString": "In Arbeit", "fromString": "Offen"}]'),
    (4, 101, '2026-02-02 10:30:00+00', '[{"field": "fixVersions", "toString": "2.0"}]');
UPDATE issues i SET search_tsv =
    setweight(to_tsvector('german', i.key || ' ' || i.summary), 'A') ||
    setweight(to_tsvector('german', i.description_text), 'B') ||
    setweight(to_tsvector('german', coalesce((SELECT string_agg(c.body_text, ' ') FROM issue_comments c
        WHERE c.issue_id = i.id AND c.visibility_type IS NULL), '')), 'C') ||
    setweight(to_tsvector('german', i.environment || ' ' || array_to_string(i.labels, ' ') || ' ' ||
        coalesce((SELECT string_agg(e.value #>> '{}', ' ') FROM jsonb_each(i.fields) e WHERE jsonb_typeof(e.value) = 'string'), '')), 'D')
WHERE i.id IN (4, 5);

-- An imported repository, a push over SSH, and a run whose job received protected values.
INSERT INTO repositories (id, project_id, slug, name, description, default_branch, settings, size_bytes, next_pr_number,
    created_by, created_at, updated_at, import_url, import_username, import_state, import_finished_at) VALUES
    (3, 1, 'mirror', 'mirror', 'Aus dem alten Git-Server', 'main', '{}', 0, 1, 101, '2026-02-04 09:00:00+00', '2026-02-04 09:01:00+00',
     'https://git.example.com/team/mirror.git', 'svc-kogge', 'done', '2026-02-04 09:01:00+00');
INSERT INTO pushes (id, repository_id, user_id, updates, pushed_at, protocol) VALUES
    (3, 1, 102, '[{"new": "b1a580b7116917a5b73fbd6a4b8b9e117f971def", "old": "b1a580b7116917a5b73fbd6a4b8b9e117f971def", "ref": "refs/heads/feature/SHOP-1", "fastForward": true}]',
     '2026-02-05 09:00:00+00', 'ssh');
INSERT INTO runs (id, repository_id, project_id, number, pipeline, pipeline_name, event, branch, commit_sha, commit_subject,
    triggered_by, variables, state, created_at, started_at, finished_at) VALUES
    (3, 1, 1, 2, 'ci', 'ci', 'manual', 'main', 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'Initial commit', 101, '{}', 'succeeded',
     '2026-02-05 10:00:00+00', '2026-02-05 10:00:10+00', '2026-02-05 10:01:00+00');
INSERT INTO run_jobs (id, run_id, stage_index, stage_name, name, spec, labels, state, attempt, agent_id, exit_code, log_size,
    created_at, queued_at, started_at, finished_at, protected) VALUES
    (3, 3, 0, 'Build', 'test', '{"job": {"Line": 8, "Name": "test", "Only": null, "Cache": null, "Clone": {"LFS": true, "Depth": 50, "Submodules": false}, "Image": "", "Retry": 0, "Steps": [{"Run": "go test ./...", "Name": "go test ./...", "When": "on-success", "Shell": "", "Timeout": 0, "ContinueOnError": false, "WorkingDirectory": ""}], "Except": null, "Manual": false, "RunsOn": ["shell"], "Reports": {"JUnit": null}, "Timeout": 3600000000000, "Services": null, "Artifacts": null, "Variables": {}, "Environment": "", "AllowFailure": false, "Dependencies": null}, "variables": {}}',
     '{shell}', 'succeeded', 1, 1, 0, 0, '2026-02-05 10:00:00+00', '2026-02-05 10:00:00+00', '2026-02-05 10:00:10+00',
     '2026-02-05 10:01:00+00', true);

-- A second agent registered with a token that is limited in uses; it waits for an administrator, and announced a
-- label it was not given.
INSERT INTO agent_tokens (id, token_hash, hint, project_id, description, created_by, created_at, uses, max_uses, auto_approve) VALUES
    (2, sha256('kgr_fixture-registration-2'::bytea), 'kgr_fixt…', 1, 'ARM-Hosts', 101, '2026-02-06 09:00:00+00', 1, 3, false);
INSERT INTO agents (id, name, project_id, secret_hash, labels, os, arch, version, enabled, created_at, last_seen_at, last_ip,
    announced_labels, approved_at) VALUES
    (2, 'build-02', 1, sha256('kga_fixture-agent-2'::bytea), '{shell,linux,arm64}', 'linux', 'arm64', 'dev', true,
     '2026-02-06 09:05:00+00', '2026-02-06 09:30:00+00', '10.0.0.10', '{shell,linux,arm64,gpu}', NULL);

-- A webhook of the project, with its owner.
INSERT INTO webhooks (id, scope, scope_id, name, url, secret_sealed, events, active, created_by, owner_id, created_at, updated_at) VALUES
    (2, 'project', 1, 'Chat', 'https://chat.example.com/hooks/kogge',
     'v1:4f73fa16:OdiDGZCd/J67TdAU0kgAYEVUK8G9umgUTdq9BJZeGfV4tiH4mw1dDjoQoVCgWBqc5/+mYQ==', '{issue.created}', true, 101, 101,
     '2026-02-07 09:00:00+00', '2026-02-07 09:00:00+00');
