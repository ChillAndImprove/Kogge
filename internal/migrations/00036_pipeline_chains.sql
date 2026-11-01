-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Pipelines that run after other pipelines (on.pipeline): a finished run starts the pipelines that follow it, in
-- its repository and in other repositories of its code project.

-- +goose Up

ALTER TABLE runs DROP CONSTRAINT runs_event_check;
ALTER TABLE runs ADD CONSTRAINT runs_event_check CHECK (event IN ('push', 'pull-request', 'schedule', 'manual', 'api', 'pipeline'));

-- The run whose end started this one, and how many runs started each other before it (chains are limited).
ALTER TABLE runs ADD COLUMN upstream_run_id bigint REFERENCES runs (id) ON DELETE SET NULL;
ALTER TABLE runs ADD COLUMN chain_depth smallint NOT NULL DEFAULT 0;
CREATE INDEX runs_upstream ON runs (upstream_run_id) WHERE upstream_run_id IS NOT NULL;

-- Pipelines of a repository's default branch that follow pipelines of other repositories, so that a finished run
-- finds them without reading every pipeline file. The file stays authoritative: it is read again to start them.
CREATE TABLE build_followers (
    upstream_repository_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    upstream_pipeline      text NOT NULL,
    repository_id          bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    PRIMARY KEY (upstream_repository_id, upstream_pipeline, repository_id)
);
CREATE INDEX build_followers_repository ON build_followers (repository_id);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
