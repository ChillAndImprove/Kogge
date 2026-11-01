-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- The values the fields of an issue had over time (for KQL's WAS): one span per value, from the issue's history
-- and its current values, kept by the issues module whenever the history grows. Fix versions are sets: a span per
-- version. value_id NULL is the empty value (unassigned, unresolved).

-- +goose Up

CREATE TABLE issue_field_spans (
    issue_id  bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    field     text NOT NULL,
    value_id  bigint,
    start_at  timestamptz NOT NULL,
    end_at    timestamptz,
    setter_id bigint REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX issue_field_spans_issue_idx ON issue_field_spans (issue_id, field);
CREATE INDEX issue_field_spans_value_idx ON issue_field_spans (field, value_id);

-- One-time work on existing data that runs in the background after an upgrade (e.g. deriving the spans above).
CREATE TABLE data_migrations (
    name    text PRIMARY KEY,
    done_at timestamptz NOT NULL DEFAULT now()
);

-- Without issues there is nothing to derive.
INSERT INTO data_migrations (name) SELECT 'issue-field-spans' WHERE NOT EXISTS (SELECT 1 FROM issues);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
