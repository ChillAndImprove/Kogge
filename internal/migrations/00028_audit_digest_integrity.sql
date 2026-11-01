-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- The digest chain shows changed and deleted audit events only if the role Kogge runs as can neither rewrite the
-- digests nor write events into days that are digested already: it may add digests, not change them, and its events
-- carry the server's time.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kogge_app') THEN
        REVOKE UPDATE, DELETE, TRUNCATE ON audit_digests FROM kogge_app;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION audit_events_server_time() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_user = 'kogge_app' THEN
        NEW.at := now();
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_events_server_time BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_server_time();
