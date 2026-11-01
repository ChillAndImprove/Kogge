-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- A page that restricts who may see it takes its child pages along to the trash: moving them up a level, as other
-- deleted pages do, would show them to everyone the page kept out. They come back and are purged with it.

-- +goose Up

ALTER TABLE pages ADD COLUMN trash_root_id bigint REFERENCES pages (id) ON DELETE CASCADE;
