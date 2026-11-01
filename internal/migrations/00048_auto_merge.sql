-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Auto-merge of pull requests: who turned it on, how to merge, and the source commit it is on for (the one they saw,
-- or a later push of theirs). blocked says what keeps it from merging and needs someone to act.

-- +goose Up

CREATE TABLE pull_request_auto_merges (
    pull_request_id bigint PRIMARY KEY REFERENCES pull_requests (id) ON DELETE CASCADE,
    enabled_by      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    strategy        text NOT NULL,
    message         text NOT NULL DEFAULT '',
    delete_source   boolean,
    source_sha      text NOT NULL,
    blocked         text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pull_request_auto_merges_user_idx ON pull_request_auto_merges (enabled_by);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
