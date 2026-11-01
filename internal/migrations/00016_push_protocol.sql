-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- How a push came in (http, ssh, web, merge): commit commands do not run for merges Kogge made, whose messages
-- are built from texts of pull requests.

-- +goose Up

ALTER TABLE pushes ADD COLUMN protocol text NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
