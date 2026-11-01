-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Jobs that received protected values (protected refs, gated deployments) keep their caches apart from those of
-- other jobs, so that a branch cannot plant files into the workspace of a protected job.

-- +goose Up

ALTER TABLE run_jobs ADD COLUMN protected boolean NOT NULL DEFAULT false;
ALTER TABLE build_caches ADD COLUMN protected boolean NOT NULL DEFAULT false;
ALTER TABLE build_caches DROP CONSTRAINT build_caches_pkey;
ALTER TABLE build_caches ADD PRIMARY KEY (repository_id, protected, key);

-- +goose Down
SELECT 1;
