-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- Instance settings edited in the administration (start-up configuration comes from the environment).
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by bigint
);

-- Append-only audit log (the application role has INSERT and SELECT only, see the grants below).
CREATE TABLE audit_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor_id    bigint,
    actor_ip    inet,
    action      text NOT NULL,
    target_type text,
    target_id   text,
    details     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_at_idx ON audit_events (at);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, at);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id);
CREATE INDEX audit_events_action_idx ON audit_events (action, at);

-- Daily SHA-256 chain over the audit events, for tamper evidence.
CREATE TABLE audit_digests (
    day        date PRIMARY KEY,
    events     integer NOT NULL,
    digest     bytea NOT NULL,
    previous   bytea,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Content-addressed files on the data volume. touched_at protects re-uploaded blobs from garbage collection.
CREATE TABLE blobs (
    sha256       text PRIMARY KEY,
    size         bigint NOT NULL,
    content_type text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    touched_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE blob_refs (
    sha256     text NOT NULL REFERENCES blobs (sha256),
    owner_type text NOT NULL,
    owner_id   bigint NOT NULL,
    PRIMARY KEY (sha256, owner_type, owner_id)
);
CREATE INDEX blob_refs_owner_idx ON blob_refs (owner_type, owner_id);

-- Ids of migrated entities in their source system (importers).
CREATE TABLE legacy_ids (
    source      text NOT NULL,
    source_type text NOT NULL,
    source_id   text NOT NULL,
    entity_type text NOT NULL,
    entity_id   bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source, source_type, source_id)
);
CREATE INDEX legacy_ids_entity_idx ON legacy_ids (entity_type, entity_id);

-- Grants for installations with separate database roles (see deploy/db/roles.sql). The application role may
-- read and write data but never change or delete audit events.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kogge_app') THEN
        GRANT USAGE ON SCHEMA public TO kogge_app;
        GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO kogge_app;
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO kogge_app;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO kogge_app;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO kogge_app;
        REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM kogge_app;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kogge_maint') THEN
        GRANT USAGE ON SCHEMA public TO kogge_maint;
        GRANT SELECT, DELETE ON audit_events TO kogge_maint;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
