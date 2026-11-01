-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- A webhook sends only what its owner may see: the person who created it or last changed its address. Deliveries
-- remember their subject (an issue, page, repository or run), so that their payload is shown only to people who may
-- see it, and re-checked before it is sent again.

-- +goose Up

ALTER TABLE webhooks ADD COLUMN owner_id bigint REFERENCES users (id) ON DELETE SET NULL;
UPDATE webhooks SET owner_id = created_by;

ALTER TABLE webhook_deliveries
    ADD COLUMN subject_type text CHECK (subject_type IN ('none', 'issue', 'page', 'repository', 'run')),
    ADD COLUMN subject_id bigint;
