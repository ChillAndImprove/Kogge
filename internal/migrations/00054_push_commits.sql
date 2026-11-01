-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- What a push brought (concept §11.9): how many commits it added and the newest of them, so that activity streams
-- say "pushed 4 commits to main" and name them. Pushes recorded before have none (NULL); streams show them as
-- before.

-- +goose Up

ALTER TABLE pushes ADD COLUMN commits jsonb;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
