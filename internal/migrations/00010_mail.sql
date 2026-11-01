-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- E-mail delivery of notifications (concept §12.4).

-- +goose Up

-- pending: to be mailed (soon, or with the next digest); sent; skipped (e-mail off for the category, no verified
-- address, e-mail not configured, or too many failed attempts).
ALTER TABLE notifications ADD COLUMN mail_state text NOT NULL DEFAULT 'skipped'
    CHECK (mail_state IN ('pending', 'sent', 'skipped'));
ALTER TABLE notifications ADD COLUMN mail_tries smallint NOT NULL DEFAULT 0;
CREATE INDEX notifications_mail_idx ON notifications (user_id, created_at) WHERE mail_state = 'pending';

-- Per user and category: immediately, hourly or daily digest, or off. Without a row: immediately.
CREATE TABLE notification_preferences (
    user_id  bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    category text NOT NULL CHECK (category IN ('issues', 'wiki', 'code', 'builds', 'system')),
    mode     text NOT NULL CHECK (mode IN ('immediately', 'hourly', 'daily', 'off')),
    PRIMARY KEY (user_id, category)
);

-- Sent and failed e-mails, for support (kept 30 days).
CREATE TABLE mail_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint REFERENCES users (id) ON DELETE SET NULL,
    address    text NOT NULL,
    subject    text NOT NULL,
    state      text NOT NULL CHECK (state IN ('sent', 'failed')),
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_log_created_idx ON mail_log (created_at);

-- +goose Down
SELECT 1;
