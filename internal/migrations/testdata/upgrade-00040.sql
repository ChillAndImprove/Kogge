-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Rows added at schema version 40 (00034 to 00040: two-factor authentication, pipeline chains, suggested changes,
-- Markdown sources, shared drafts, field spans). SHOP-6 has the spans the issues module keeps since 00040, and a
-- history in the form of that time (fix versions by id); the older issues have none yet, and the data migration
-- that derives them has not finished.

-- grace signs in with a second factor (the secret is sealed with the master key).
INSERT INTO users (id, username, display_name, email, email_verified, kind, status, password_hash, created_at, updated_at) VALUES
    (107, 'grace', 'Grace Guard', 'grace@example.com', true, 'person', 'active',
     '$argon2id$v=19$m=1024,t=1,p=1$mqP9a5MA0lbTPbucoMSy0Q$A+9n7v7yaSdhm1zcrK0jA0tKwQLYLc9j8FRmTBSNkZE',
     '2026-04-01 08:00:00+00', '2026-04-01 08:00:00+00');
INSERT INTO user_totp (user_id, secret, confirmed_at, last_step, created_at) VALUES
    (107, 'v1:4f73fa16:uj5Fqiox4nXLIoCWR3h9TpcQWWzN5Q1izRJEYiRBt41eb8IVWCq68bosMCniWXA/', '2026-04-01 08:05:00+00', 0,
     '2026-04-01 08:04:00+00');
INSERT INTO user_recovery_codes (user_id, code_hash, used_at) VALUES
    (107, sha256('fixture-recovery-1'::bytea), NULL), (107, sha256('fixture-recovery-2'::bytea), '2026-04-02 08:00:00+00');

-- SHOP-6 with its spans.
INSERT INTO issues (id, project_id, number, key, type_id, status_id, priority_id, summary, description, description_text,
    reporter_id, creator_id, labels, fields, rank, created_at, updated_at, status_changed_at, version) VALUES
    (6, 1, 6, 'SHOP-6', 3, 1, 3, 'Gutscheine', NULL, '', 101, 101, '{}', '{}', 'r', '2026-04-01 09:00:00+00',
     '2026-04-01 10:00:00+00', '2026-04-01 09:00:00+00', 2);
UPDATE issue_projects SET next_number = 7, updated_at = '2026-04-01 09:00:00+00' WHERE id = 1;
INSERT INTO issue_versions (issue_id, version_id, kind) VALUES (6, 2, 'fix');
INSERT INTO issue_changes (issue_id, actor_id, at, items) VALUES
    (6, 101, '2026-04-01 10:00:00+00', '[{"to": [2], "from": [], "field": "fixVersions", "toString": "2.0"}]');
INSERT INTO issue_field_spans (issue_id, field, value_id, start_at, end_at, setter_id) VALUES
    (6, 'status', 1, '2026-04-01 09:00:00+00', NULL, 101),
    (6, 'assignee', NULL, '2026-04-01 09:00:00+00', NULL, 101),
    (6, 'reporter', 101, '2026-04-01 09:00:00+00', NULL, 101),
    (6, 'priority', 3, '2026-04-01 09:00:00+00', NULL, 101),
    (6, 'resolution', NULL, '2026-04-01 09:00:00+00', NULL, 101),
    (6, 'fixVersions', 2, '2026-04-01 10:00:00+00', NULL, 101);
UPDATE issues i SET search_tsv =
    setweight(to_tsvector('german', i.key || ' ' || i.summary), 'A') ||
    setweight(to_tsvector('german', i.description_text), 'B') ||
    setweight(to_tsvector('german', ''), 'C') ||
    setweight(to_tsvector('german', i.environment || ' ' || array_to_string(i.labels, ' ')), 'D')
WHERE i.id = 6;

-- The home page has a second version that bob contributed to, and a shared draft he is writing.
UPDATE pages SET version = 2, updated_by = 101, updated_at = '2026-04-02 09:00:00+00',
    body = '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Willkommen im Betrieb.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Neu: die Rufbereitschaft.", "type": "text"}]}], "version": 1}',
    body_text = E'Willkommen im Betrieb.\nNeu: die Rufbereitschaft.'
WHERE id = 1;
INSERT INTO page_versions (page_id, version, title, body, author_id, created_at, message, minor, contributors) VALUES
    (1, 2, 'Betrieb – Startseite', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Willkommen im Betrieb.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Neu: die Rufbereitschaft.", "type": "text"}]}], "version": 1}',
     101, '2026-04-02 09:00:00+00', 'Rufbereitschaft', false, '{102}');
INSERT INTO page_shared_drafts (page_id, base_version, version, title, doc, doc_version, changed, contributors, updated_at, updated_by) VALUES
    (1, 2, 0, 'Betrieb – Startseite', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Willkommen im Betrieb.", "type": "text"}]}, {"type": "paragraph", "content": [{"text": "Neu: die Rufbereitschaft, ab Mai.", "type": "text"}]}], "version": 1}',
     0, true, '{102}', '2026-04-03 09:00:00+00', 102);

-- Pipelines: legacy's pipeline follows the release pipeline of shop, whose run started it.
INSERT INTO build_followers (upstream_repository_id, upstream_pipeline, repository_id) VALUES (1, 'release', 2);
INSERT INTO runs (id, repository_id, project_id, number, pipeline, pipeline_name, event, branch, commit_sha, commit_subject,
    variables, state, created_at, started_at, finished_at, upstream_run_id, chain_depth) VALUES
    (5, 2, 1, 2, 'ci', 'ci', 'pipeline', 'main', 'ba7925faee16cd1ce32c6b54511fa13c6899bd0d', 'Legacy build', '{}', 'succeeded',
     '2026-03-06 09:01:01+00', '2026-03-06 09:01:05+00', '2026-03-06 09:02:00+00', 4, 1);
INSERT INTO run_jobs (id, run_id, stage_index, stage_name, name, spec, labels, state, attempt, agent_id, exit_code, log_size,
    created_at, queued_at, started_at, finished_at) VALUES
    (5, 5, 0, 'Build', 'test', '{"job": {"Line": 8, "Name": "test", "Only": null, "Cache": null, "Clone": {"LFS": true, "Depth": 50, "Submodules": false}, "Image": "", "Retry": 0, "Steps": [{"Run": "make", "Name": "make", "When": "on-success", "Shell": "", "Timeout": 0, "ContinueOnError": false, "WorkingDirectory": ""}], "Except": null, "Manual": false, "RunsOn": ["shell"], "Reports": {"JUnit": null}, "Timeout": 3600000000000, "Services": null, "Artifacts": null, "Variables": {}, "Environment": "", "AllowFailure": false, "Dependencies": null}, "variables": {}}',
     '{shell}', 'succeeded', 1, 1, 0, 0, '2026-03-06 09:01:01+00', '2026-03-06 09:01:01+00', '2026-03-06 09:01:05+00',
     '2026-03-06 09:02:00+00');

-- The pull request was written in Markdown; alice suggested a change that bob applied.
UPDATE pull_requests SET description_markdown = 'Bitte prüfen.' WHERE id = 1;
INSERT INTO pull_request_comments (id, pull_request_id, author_id, body, body_text, markdown, path, line, side, commit_sha,
    line_text, created_at, updated_at, applied_sha, applied_by, applied_at) VALUES
    (2, 1, 101, '{"type": "doc", "content": [{"type": "codeBlock", "attrs": {"language": "suggestion"}, "content": [{"text": "// Adapter talks to the second provider.", "type": "text"}]}], "version": 1}',
     '// Adapter talks to the second provider.', E'```suggestion\n// Adapter talks to the second provider.\n```', 'adapter.go', 3,
     'new', 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', '// Adapter talks to the second payment provider.',
     '2026-04-04 09:00:00+00', '2026-04-04 09:00:00+00', 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', 102, '2026-04-04 09:30:00+00');
