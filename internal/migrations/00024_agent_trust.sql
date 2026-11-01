-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Build agents run code with what they are given, so who decides about them matters: an agent's labels are those it
-- registered with or an administrator set — labels it announces later wait for an administrator; new agents wait for
-- approval unless their registration token approves them; registration tokens can be limited in uses.

-- +goose Up

ALTER TABLE agents
    ADD COLUMN announced_labels text[], -- labels the agent announced last, if they differ from its labels
    ADD COLUMN approved_at timestamptz, -- NULL: waits for an administrator
    ADD COLUMN approved_by bigint REFERENCES users (id) ON DELETE SET NULL;
UPDATE agents SET approved_at = created_at;

ALTER TABLE agent_tokens
    ADD COLUMN max_uses integer NOT NULL DEFAULT 0, -- 0: unlimited
    ADD COLUMN auto_approve boolean NOT NULL DEFAULT false;
