-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Build runs belong to the project of their repository. Runs of repositories moved to another project kept the old
-- one, so people of the old project saw them in the list of runs.

-- +goose Up

UPDATE runs r SET project_id = repo.project_id
FROM repositories repo
WHERE repo.id = r.repository_id AND r.project_id <> repo.project_id;
