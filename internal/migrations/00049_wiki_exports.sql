-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Exports of spaces and page trees (concept §8.9): a job writes them for the person who asked for them, with
-- what that person may see, as an HTML site or Markdown files in a ZIP archive, or as one PDF. The file is a blob
-- (owner wiki-export) until the export expires; the blob collection removes it after its grace period.

-- +goose Up

CREATE TABLE wiki_exports (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    space_id     bigint NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    page_id      bigint REFERENCES pages (id) ON DELETE CASCADE, -- the root of a page tree; the whole space without
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Exports asked for with an access token without the admin scope see what such a token sees.
    admin_scope  boolean NOT NULL,
    format       text NOT NULL CHECK (format IN ('html', 'markdown', 'pdf')),
    paper        text NOT NULL DEFAULT 'a4' CHECK (paper IN ('a4', 'letter')),
    state        text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    pages_done   integer NOT NULL DEFAULT 0,
    pages_total  integer NOT NULL DEFAULT 0,
    error        text NOT NULL DEFAULT '',
    filename     text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    sha256       text,
    size         bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    expires_at   timestamptz NOT NULL,
    CHECK ((state = 'done') = (sha256 IS NOT NULL))
);
CREATE INDEX wiki_exports_user_idx ON wiki_exports (user_id, created_at DESC);
CREATE INDEX wiki_exports_expires_idx ON wiki_exports (expires_at);
CREATE INDEX wiki_exports_space_idx ON wiki_exports (space_id);
CREATE INDEX wiki_exports_page_idx ON wiki_exports (page_id);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
