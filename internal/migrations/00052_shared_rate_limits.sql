-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Limits of rates that hold for all nodes of an installation together (concept §13.2, §17.9): the failed password
-- checks of a client address, which would otherwise multiply with the nodes behind a load balancer. A key's token
-- bucket is kept as the time at which it is full again (the theoretical arrival time of the generic cell rate
-- algorithm): a key without a row, or whose row's time has passed, has all its events; the session cleanup deletes
-- such rows.

-- +goose Up

CREATE TABLE rate_limits (
    key     text PRIMARY KEY,
    full_at timestamptz NOT NULL
);
CREATE INDEX rate_limits_full_idx ON rate_limits (full_at);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
