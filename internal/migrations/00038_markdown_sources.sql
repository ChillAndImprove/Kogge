-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- The Markdown that comments and descriptions of pull requests were written in, so that a change starts from the
-- text as it was typed; texts written as documents have none.

-- +goose Up

ALTER TABLE pull_request_comments ADD COLUMN markdown text;
ALTER TABLE pull_requests ADD COLUMN description_markdown text;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
