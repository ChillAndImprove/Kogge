-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- When a build job became pending: how long jobs wait for an agent is a metric (concept §17.7).

-- +goose Up

ALTER TABLE run_jobs ADD COLUMN queued_at timestamptz;

-- +goose Down
SELECT 1;
