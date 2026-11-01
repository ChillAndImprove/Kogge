-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Rows added at schema version 13 (00010 to 00013: e-mail, webhooks, automation, the space of a project) to those
-- of version 9. The webhook and the automation rule have no owner yet: 00022 and 00023 make their creator it.

INSERT INTO notification_preferences (user_id, category, mode) VALUES (102, 'issues', 'daily'), (102, 'builds', 'off');
INSERT INTO notifications (user_id, created_at, read_at, category, event, entity_type, entity_id, actor_id, title, summary, url,
    data, mail_state, mail_tries) VALUES
    (101, '2026-01-12 09:00:00+00', NULL, 'code', 'pull-request.opened', 'pull-request', '1', 102,
     'SHOP-1 Adapter für den zweiten Anbieter', 'Bob Builder hat einen Pull-Request geöffnet.', '/code/PLAT/shop/pull-requests/1',
     '{}', 'sent', 1);
INSERT INTO mail_log (user_id, address, subject, state, error, created_at) VALUES
    (101, 'alice@example.com', '[Werft] SHOP-1 Adapter für den zweiten Anbieter', 'sent', '', '2026-01-12 09:00:05+00');

INSERT INTO webhooks (id, scope, scope_id, name, url, secret_sealed, events, active, created_by, created_at, updated_at) VALUES
    (1, 'repository', 1, 'CI-Spiegel', 'https://hooks.example.com/kogge',
     'v1:4f73fa16:SgmvChb68EIhIvYpeUE5g6zwV070X+eykU1VOPGdMnEfTnKOhFNwTir/htY8aI2lg/Y=', '{}', true, 101,
     '2026-01-12 10:00:00+00', '2026-01-12 10:00:00+00');
INSERT INTO webhook_deliveries (id, webhook_id, event_id, event, payload, state, attempts, response_status, response_excerpt,
    duration_ms, created_at, delivered_at) VALUES
    (1, 1, '6f1c2b8e-0d4a-4c47-9a1f-3b2e5d6c7a80', 'repository.pushed',
     '{"event": "repository.pushed", "repository": {"key": "PLAT", "slug": "shop"}, "ref": "refs/heads/main"}',
     'succeeded', 1, 200, 'ok', 42, '2026-01-12 10:05:00+00', '2026-01-12 10:05:01+00');

INSERT INTO automation_rules (id, project_id, name, trigger, environment, issue_types, statuses, action, params, active,
    created_by, created_at, updated_at) VALUES
    (1, 1, 'Erledigt beim Merge', 'pull-request.merged', '', '{}', '{}', 'transition', '{"status": 4}', true, 101,
     '2026-01-12 11:00:00+00', '2026-01-12 11:00:00+00');
INSERT INTO automation_runs (rule_id, issue_key, occurrence, result, message, at) VALUES
    (1, 'SHOP-1', 'pull-request:1', 'skipped', 'The issue is in the target status already.', '2026-01-12 11:30:00+00');

-- Release notes and retrospectives of SHOP go to the space DOCS.
INSERT INTO project_spaces (project_id, space_id) VALUES (1, 1);
