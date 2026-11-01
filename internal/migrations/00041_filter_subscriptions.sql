-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Filter subscriptions (concept §7.6): the results of a saved filter by e-mail, on a schedule, to a person or to the
-- members of a group — each with what they may see.

-- +goose Up

CREATE TABLE filter_subscriptions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    filter_id   bigint NOT NULL REFERENCES saved_filters (id) ON DELETE CASCADE,
    user_id     bigint REFERENCES users (id) ON DELETE CASCADE,
    group_id    bigint REFERENCES groups (id) ON DELETE CASCADE,
    created_by  bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    frequency   text NOT NULL CHECK (frequency IN ('hourly', 'daily', 'monthly')),
    minute      integer NOT NULL CHECK (minute BETWEEN 0 AND 1439), -- of the hour (hourly) or of the day
    weekdays    integer[] NOT NULL DEFAULT '{}',                    -- hourly and daily: ISO days (1 = Monday)
    hours       integer[] NOT NULL DEFAULT '{}',                    -- hourly: the hours of the day, all when empty
    month_day   integer CHECK (month_day BETWEEN 1 AND 31),         -- monthly: the last day of shorter months
    send_empty  boolean NOT NULL DEFAULT false,
    time_zone   text NOT NULL,
    next_run_at timestamptz NOT NULL,
    last_run_at timestamptz,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((user_id IS NULL) <> (group_id IS NULL))
);
CREATE INDEX filter_subscriptions_due_idx ON filter_subscriptions (next_run_at);
CREATE INDEX filter_subscriptions_filter_idx ON filter_subscriptions (filter_id);
CREATE INDEX filter_subscriptions_user_idx ON filter_subscriptions (user_id);
CREATE INDEX filter_subscriptions_group_idx ON filter_subscriptions (group_id);
CREATE INDEX filter_subscriptions_creator_idx ON filter_subscriptions (created_by);

-- Members of a group who do not want the e-mails of its subscription.
CREATE TABLE filter_subscription_mutes (
    subscription_id bigint NOT NULL REFERENCES filter_subscriptions (id) ON DELETE CASCADE,
    user_id         bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (subscription_id, user_id)
);

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
