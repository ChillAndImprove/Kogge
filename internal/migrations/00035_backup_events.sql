-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Backups record their outcome in the audit log (backup.written, backup.failed); the administration's overview
-- reads the latest — this index finds it without reading the log.

-- +goose Up

CREATE INDEX audit_events_backup_idx ON audit_events (id) WHERE action IN ('backup.written', 'backup.failed');

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
