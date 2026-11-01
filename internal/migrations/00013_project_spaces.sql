-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- The wiki space of an issue project (concept §11.6): release notes and retrospectives are written there.

-- +goose Up

CREATE TABLE project_spaces (
    project_id bigint PRIMARY KEY REFERENCES issue_projects (id) ON DELETE CASCADE,
    space_id   bigint NOT NULL REFERENCES spaces (id) ON DELETE CASCADE
);

-- +goose Down
SELECT 1;
