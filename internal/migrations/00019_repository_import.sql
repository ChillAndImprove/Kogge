-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Repositories imported from another Git server. The import runs in the background; its credentials are kept,
-- sealed with the master key, only until it ends.

-- +goose Up

ALTER TABLE repositories
    ADD COLUMN import_url text,
    ADD COLUMN import_username text,
    ADD COLUMN import_secret text,
    ADD COLUMN import_state text CHECK (import_state IN ('running', 'failed', 'done')),
    ADD COLUMN import_error text NOT NULL DEFAULT '',
    ADD COLUMN import_finished_at timestamptz;

-- +goose Down
SELECT 1;
