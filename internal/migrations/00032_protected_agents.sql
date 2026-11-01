-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Protected agents take only jobs of refs that a branch permission of the code project protects, so that build
-- hosts with access to production run only reviewed code (concept §10.4).

-- +goose Up

ALTER TABLE agents ADD COLUMN protected boolean NOT NULL DEFAULT false;

-- How the ref of a run was protected when it was created (0 not, 1 by the repository, 2 by the code project); claims
-- check it again.
ALTER TABLE runs ADD COLUMN ref_protection smallint NOT NULL DEFAULT 0;

-- +goose Down
-- Migrations are forward-only.
SELECT 1;
