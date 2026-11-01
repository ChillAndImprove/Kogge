-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Git LFS objects of repositories; the content lives in the blob store under its SHA-256 (the LFS object id).

-- +goose Up

CREATE TABLE lfs_objects (
    repository_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    oid           text NOT NULL,
    size          bigint NOT NULL,
    created_by    bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repository_id, oid)
);

-- +goose Down
SELECT 1;
