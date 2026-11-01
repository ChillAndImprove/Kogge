-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Pipelines as graphs (needs), conditions (if), included files and resource limits of container jobs.

-- +goose Up

-- Why a job was skipped: by its condition or its stage's, because a job it needs does not run (decided when the run
-- is created), or because a job it waits for failed, was cancelled or was skipped. Detail: the condition or the job
-- (stage/job).
ALTER TABLE run_jobs ADD COLUMN skip_reason text NOT NULL DEFAULT '';
ALTER TABLE run_jobs ADD COLUMN skip_detail text NOT NULL DEFAULT '';

-- What one container of the job asks for (the most of the job's container and its services); claims hand a job only
-- to agents that allow that much. 0: nothing.
ALTER TABLE run_jobs ADD COLUMN millicpus bigint NOT NULL DEFAULT 0;
ALTER TABLE run_jobs ADD COLUMN memory bigint NOT NULL DEFAULT 0;

-- The included file that defines the run's pipeline ('' for the pipeline file itself) and the files the pipeline file
-- included, with the commits they were read at: running the run again reads them at the same commits.
ALTER TABLE runs ADD COLUMN pipeline_file text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN includes jsonb NOT NULL DEFAULT '[]';

-- The most an agent allows one container (announced by the agent; 0: no limit), for telling why a job waits.
ALTER TABLE agents ADD COLUMN max_millicpus bigint NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN max_memory bigint NOT NULL DEFAULT 0;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
