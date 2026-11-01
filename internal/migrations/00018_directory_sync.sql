-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- The directory sync disables users that the directory no longer has or has disabled, and enables them again when
-- they come back; users that administrators disabled stay disabled.

-- +goose Up

ALTER TABLE users ADD COLUMN disabled_by_directory boolean NOT NULL DEFAULT false;

-- +goose Down
SELECT 1;
