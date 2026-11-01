-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- An automation rule acts with the access of its owner, the project administrator who last saved it: it reacts only
-- to events of repositories its owner can read, on issues its owner may see, and only while its owner administers
-- the project.

-- +goose Up

ALTER TABLE automation_rules ADD COLUMN owner_id bigint REFERENCES users (id) ON DELETE SET NULL;
UPDATE automation_rules SET owner_id = created_by;
