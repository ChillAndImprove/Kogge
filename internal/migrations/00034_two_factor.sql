-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Two-factor authentication of local accounts (concept §13.1): a TOTP secret (sealed with the keyring) and
-- recovery codes (hashed, each usable once).

-- +goose Up

CREATE TABLE user_totp (
    user_id      bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret       text NOT NULL,
    confirmed_at timestamptz,               -- NULL while it is being set up
    last_step    bigint NOT NULL DEFAULT 0, -- the last time step whose code was accepted: codes count once
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_recovery_codes (
    user_id   bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    used_at   timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
