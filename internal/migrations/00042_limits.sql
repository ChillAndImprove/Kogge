-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Limits (Administration → Settings → Limits): the size of a repository's Git LFS objects, kept beside the size of its
-- Git objects for the quota; the quota of single repositories that administrators set; whether a job's log reached
-- its limit and was cut off; the indexes that list the largest repositories and sum up the artifacts of a run.

-- +goose Up

ALTER TABLE repositories ADD COLUMN lfs_bytes bigint NOT NULL DEFAULT 0;
-- NULL: the quota of the settings; 0: no quota; otherwise the quota in bytes.
ALTER TABLE repositories ADD COLUMN quota_bytes bigint CHECK (quota_bytes >= 0);
ALTER TABLE run_jobs ADD COLUMN log_cut boolean NOT NULL DEFAULT false;

UPDATE repositories r SET lfs_bytes = o.bytes
FROM (SELECT repository_id, sum(size) AS bytes FROM lfs_objects GROUP BY repository_id) o
WHERE o.repository_id = r.id;

CREATE INDEX repositories_size_idx ON repositories ((size_bytes + lfs_bytes) DESC, id);
CREATE INDEX artifacts_run_idx ON artifacts (run_id);

-- +goose Down
SELECT 1;
