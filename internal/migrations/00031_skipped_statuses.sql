-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Pipelines whose paths filters leave a commit out record the state skipped for it, so that merge checks count
-- them as done (concept §10.2).

-- +goose Up

ALTER TABLE commit_statuses DROP CONSTRAINT commit_statuses_state_check;
ALTER TABLE commit_statuses ADD CONSTRAINT commit_statuses_state_check
    CHECK (state IN ('pending', 'running', 'success', 'failure', 'error', 'cancelled', 'skipped'));

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
