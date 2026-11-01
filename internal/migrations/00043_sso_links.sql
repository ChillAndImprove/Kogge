-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Single sign-on takes over an existing account (a placeholder of a migration, an account created ahead of the first
-- sign-in) only when that is safe: when the identity provider asserts the account's e-mail address as verified, or
-- when an administrator named the provider's user who may take it over. sso_link is that user name, until the
-- user's first sign-in (or the directory sync) takes the account over.

-- +goose Up
ALTER TABLE users ADD COLUMN sso_link text;
CREATE UNIQUE INDEX users_sso_link_key ON users (lower(sso_link)) WHERE sso_link IS NOT NULL;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
