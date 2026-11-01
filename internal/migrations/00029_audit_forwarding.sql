-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Forwarding of audit events to a SIEM (concept §13.7). Events are forwarded in the order of the transactions that
-- wrote them, and only once no transaction that might still add an earlier one is running: ids alone would skip
-- the event of a transaction that commits after a later one. xact is that transaction; the trigger sets it, so
-- that it cannot be chosen. Events written before have the transaction of this migration.

-- +goose Up

ALTER TABLE audit_events ADD COLUMN xact xid8 NOT NULL DEFAULT pg_current_xact_id();
CREATE INDEX audit_events_xact_idx ON audit_events (xact, id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_events_server_time() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_user = 'kogge_app' THEN
        NEW.at := now();
    END IF;
    NEW.xact := pg_current_xact_id();
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- The position of the forwarding: the last event delivered, or a position before the events still to come.
CREATE TABLE audit_forwarding (
    id            smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    target        text NOT NULL,
    xact          xid8 NOT NULL,
    event_id      bigint NOT NULL,
    forwarded     bigint NOT NULL DEFAULT 0,
    forwarded_at  timestamptz,
    error         text,
    error_at      timestamptz,
    failing_since timestamptz,
    started_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
