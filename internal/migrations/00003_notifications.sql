-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- +goose Up
-- The notification inbox. E-mail delivery state lives in the job queue.
CREATE TABLE notifications (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    read_at     timestamptz,
    category    text NOT NULL,
    event       text NOT NULL,
    entity_type text NOT NULL,
    entity_id   text NOT NULL,
    actor_id    bigint REFERENCES users (id),
    title       text NOT NULL,
    summary     text NOT NULL DEFAULT '',
    url         text NOT NULL,
    data        jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

-- Watchers of any entity (issues, pages, spaces, pull requests, repositories).
CREATE TABLE watchers (
    entity_type text NOT NULL,
    entity_id   bigint NOT NULL,
    user_id     bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_type, entity_id, user_id)
);
CREATE INDEX watchers_user_idx ON watchers (user_id, entity_type);

-- Activity streams of projects, spaces, repositories and users.
CREATE TABLE activity (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at             timestamptz NOT NULL DEFAULT now(),
    actor_id       bigint REFERENCES users (id),
    verb           text NOT NULL,
    entity_type    text NOT NULL,
    entity_id      bigint NOT NULL,
    container_type text NOT NULL,
    container_id   bigint NOT NULL,
    title          text NOT NULL,
    url            text NOT NULL,
    data           jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX activity_container_idx ON activity (container_type, container_id, at DESC);
CREATE INDEX activity_actor_idx ON activity (actor_id, at DESC);
CREATE INDEX activity_at_idx ON activity (at DESC);

-- +goose Down
SELECT 1;
