-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Shared drafts (concept §8.3): everyone editing a page edits one draft together, live. The server applies the
-- editors' changes (steps) one after the other to the draft's document and keeps a snapshot of it with the steps
-- since, for editors that catch up; who changed the draft since it was last published are its contributors.

-- +goose Up

CREATE TABLE page_shared_drafts (
    page_id      bigint PRIMARY KEY REFERENCES pages (id) ON DELETE CASCADE,
    base_version integer NOT NULL,           -- the page version the draft builds on
    version      integer NOT NULL DEFAULT 0, -- steps applied to the draft
    title        text NOT NULL,
    doc          jsonb NOT NULL,             -- the document at doc_version
    doc_version  integer NOT NULL DEFAULT 0,
    changed      boolean NOT NULL DEFAULT false,
    contributors bigint[] NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   bigint REFERENCES users (id) ON DELETE SET NULL
);

CREATE TABLE page_draft_steps (
    page_id    bigint NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    version    integer NOT NULL, -- the draft's version after the step
    step       jsonb NOT NULL,
    client_id  text NOT NULL,
    user_id    bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, version)
);

-- Who contributed to a version besides who published it.
ALTER TABLE page_versions ADD COLUMN contributors bigint[] NOT NULL DEFAULT '{}';

-- Personal drafts that build on the current version of their page become its shared draft (the newest one, where
-- several people had one); the others stay with their authors, who find them in the editor.
INSERT INTO page_shared_drafts (page_id, base_version, title, doc, changed, contributors, updated_at, updated_by)
SELECT DISTINCT ON (d.page_id) d.page_id, d.base_version, d.title, d.body, d.body <> p.body OR d.title <> p.title,
    ARRAY[d.user_id], d.updated_at, d.user_id
FROM page_drafts d JOIN pages p ON p.id = d.page_id
WHERE d.base_version = p.version
ORDER BY d.page_id, d.updated_at DESC;

DELETE FROM page_drafts d USING page_shared_drafts s WHERE s.page_id = d.page_id AND s.updated_by = d.user_id;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
