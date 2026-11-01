-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Sub-tasks have the security level of their parent; before, a sub-task of a secured issue could be created without
-- one and was visible to everyone browsing the project. Such sub-tasks take on their parent's level. Sub-tasks
-- whose level differs from their parent's keep it: changing it could widen who sees them.

-- +goose Up

UPDATE issues s SET security_level_id = p.security_level_id, version = s.version + 1
FROM issues p
WHERE s.parent_id = p.id
  AND s.type_id IN (SELECT id FROM issue_types WHERE level = -1)
  AND s.security_level_id IS NULL
  AND p.security_level_id IS NOT NULL;
