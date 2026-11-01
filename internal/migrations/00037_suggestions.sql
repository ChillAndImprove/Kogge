-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Suggested changes: the commit that applied the change a line comment suggests, who applied it and when.

-- +goose Up

ALTER TABLE pull_request_comments ADD COLUMN applied_sha text;
ALTER TABLE pull_request_comments ADD COLUMN applied_by bigint REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE pull_request_comments ADD COLUMN applied_at timestamptz;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
