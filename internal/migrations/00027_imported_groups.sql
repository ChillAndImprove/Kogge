-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Groups that migrations created: later migrations add their members to them, but never to Kogge's own groups,
-- which may hold permissions the members of a source group must not get.

-- +goose Up

ALTER TABLE groups ADD COLUMN imported boolean NOT NULL DEFAULT false;
UPDATE groups SET imported = true WHERE description = 'Created by a migration.';
