-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Rows added at schema version 33 (00025 to 00033: run projects, restricted pages in the trash with their children,
-- imported groups, the audit chain and its forwarding, pipeline templates, skipped statuses, protected agents and
-- refs, pipeline triggers).

-- A placeholder for a person of a migrated system, and a group that a migration created.
INSERT INTO users (id, username, display_name, email, kind, status, created_at, updated_at) VALUES
    (106, 'frank', 'Frank Fremd', NULL, 'placeholder', 'active', '2026-03-01 08:00:00+00', '2026-03-01 08:00:00+00');
INSERT INTO groups (id, name, description, source, created_at, imported) VALUES
    (3, 'Lieferanten', 'Aus der alten Ablage', 'local', '2026-03-01 08:00:00+00', true);
INSERT INTO group_members (group_id, user_id) VALUES (3, 106);
INSERT INTO audit_events (at, actor_id, actor_ip, action, target_type, target_id, details) VALUES
    ('2026-03-01 09:00:00+00', 101, '10.0.0.5', 'pipeline.trigger.create', 'repository', '1', '{"description": "Release"}');

-- The restricted page Interna went to the trash with its child.
INSERT INTO pages (id, space_id, parent_id, kind, status, title, body, body_text, rank, version, creator_id, created_at,
    updated_by, updated_at, published_at, trashed_at, trashed_by, view_restricted_by, trash_root_id) VALUES
    (5, 1, 1, 'page', 'trashed', 'Interna',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Intern.", "type": "text"}]}], "version": 1}',
     'Intern.', 'p', 1, 101, '2026-03-02 09:00:00+00', 101, '2026-03-03 09:00:00+00', '2026-03-02 09:00:00+00',
     '2026-03-03 09:00:00+00', 101, '{5}', NULL),
    (6, 1, 5, 'page', 'trashed', 'Interna – Schlüssel',
     '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Im Tresor.", "type": "text"}]}], "version": 1}',
     'Im Tresor.', 'V', 1, 101, '2026-03-02 09:10:00+00', 101, '2026-03-03 09:00:00+00', '2026-03-02 09:10:00+00',
     '2026-03-03 09:00:00+00', 101, '{5}', 5);
INSERT INTO page_restrictions (page_id, kind, holder_type, holder_id) VALUES (5, 'view', 'user', 101);
INSERT INTO page_versions (page_id, version, title, body, author_id, created_at, message, minor) VALUES
    (5, 1, 'Interna', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Intern.", "type": "text"}]}], "version": 1}',
     101, '2026-03-02 09:00:00+00', '', false),
    (6, 1, 'Interna – Schlüssel', '{"type": "doc", "content": [{"type": "paragraph", "content": [{"text": "Im Tresor.", "type": "text"}]}], "version": 1}',
     101, '2026-03-02 09:10:00+00', '', false);

-- Builds: a pipeline template, a protected agent, a trigger whose run deployed to production, and a skipped status.
INSERT INTO pipeline_templates (id, name, description, content, created_by, updated_by, created_at, updated_at) VALUES
    (1, 'Go-Dienst', 'Baut und testet einen Go-Dienst', E'version: 1\npipelines:\n  ci:\n    on: { push: { branches: ["**"] } }\n    stages:\n      - name: Build\n        jobs:\n          - name: test\n            runs-on: [shell]\n            steps:\n              - run: go test ./...\n',
     101, 101, '2026-03-04 09:00:00+00', '2026-03-04 09:00:00+00');
INSERT INTO agents (id, name, project_id, secret_hash, labels, os, arch, version, enabled, created_at, last_seen_at, last_ip,
    approved_at, approved_by, protected) VALUES
    (3, 'deploy-01', NULL, sha256('kga_fixture-agent-3'::bytea), '{shell,linux,amd64,deploy}', 'linux', 'amd64', 'dev', true,
     '2026-03-05 09:00:00+00', '2026-03-06 09:01:00+00', '10.0.0.11', '2026-03-05 09:10:00+00', 101, true);
INSERT INTO pipeline_triggers (id, repository_id, description, token_hash, token_hint, created_by, created_at, last_used_at) VALUES
    (1, 1, 'Release', sha256('kgt_fixture-trigger'::bytea), 'kgt_fixt…', 101, '2026-03-01 09:00:00+00', '2026-03-06 09:00:00+00');
INSERT INTO runs (id, repository_id, project_id, number, pipeline, pipeline_name, event, branch, commit_sha, commit_subject,
    triggered_by, variables, state, created_at, started_at, finished_at, ref_protection, trigger_name) VALUES
    (4, 1, 1, 3, 'release', 'Release', 'api', 'main', 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'Initial commit', 101, '{}',
     'succeeded', '2026-03-06 09:00:00+00', '2026-03-06 09:00:05+00', '2026-03-06 09:01:00+00', 1, 'Release');
INSERT INTO run_jobs (id, run_id, stage_index, stage_name, name, spec, labels, state, attempt, agent_id, exit_code, log_size,
    environment, created_at, queued_at, started_at, finished_at, protected) VALUES
    (4, 4, 0, 'Deploy', 'deploy', '{"job": {"Line": 9, "Name": "deploy", "Only": null, "Cache": null, "Clone": {"LFS": true, "Depth": 50, "Submodules": false}, "Image": "", "Retry": 0, "Steps": [{"Run": "./deploy.sh", "Name": "./deploy.sh", "When": "on-success", "Shell": "", "Timeout": 0, "ContinueOnError": false, "WorkingDirectory": ""}], "Except": null, "Manual": false, "RunsOn": ["deploy"], "Reports": {"JUnit": null}, "Timeout": 3600000000000, "Services": null, "Artifacts": null, "Variables": {}, "Environment": "production", "AllowFailure": false, "Dependencies": null}, "variables": {}}',
     '{deploy}', 'succeeded', 1, 3, 0, 0, 'production', '2026-03-06 09:00:00+00', '2026-03-06 09:00:00+00', '2026-03-06 09:00:05+00',
     '2026-03-06 09:01:00+00', true);
INSERT INTO deployments (id, environment_id, run_id, job_id, commit_sha, ref, state, created_by, created_at, finished_at) VALUES
    (1, 1, 4, 4, 'bb4378685a62737ca0e66461a4d5b5ba9bc98bde', 'refs/heads/main', 'succeeded', 101, '2026-03-06 09:00:00+00',
     '2026-03-06 09:01:00+00');
INSERT INTO deployment_approvals (deployment_id, user_id, approved, comment, at) VALUES (1, 101, true, 'Los.', '2026-03-06 09:00:03+00');
INSERT INTO commit_statuses (id, repository_id, sha, context, state, description, url, run_id, updated_at) VALUES
    (2, 1, 'b1a580b7116917a5b73fbd6a4b8b9e117f971def', 'kogge/docs', 'skipped', 'docs', '', NULL, '2026-03-06 10:00:00+00');
