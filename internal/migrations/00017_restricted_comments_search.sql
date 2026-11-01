-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Comments restricted to a role or group no longer count for the full-text search of their issue: their words could
-- be searched by everyone who sees the issue. This recomputes the search text of issues with such comments, with
-- the same weights as the issues module (summary A, description B, comments C, other fields D).

-- +goose Up

UPDATE issues i SET search_tsv =
    setweight(to_tsvector(l.cfg, i.key || ' ' || i.summary), 'A') ||
    setweight(to_tsvector(l.cfg, i.description_text), 'B') ||
    setweight(to_tsvector(l.cfg, coalesce((SELECT string_agg(c.body_text, ' ') FROM issue_comments c
        WHERE c.issue_id = i.id AND c.visibility_type IS NULL), '')), 'C') ||
    setweight(to_tsvector(l.cfg, i.environment || ' ' || array_to_string(i.labels, ' ') || ' ' ||
        coalesce((SELECT string_agg(e.value #>> '{}', ' ') FROM jsonb_each(i.fields) e
            WHERE jsonb_typeof(e.value) = 'string'), '')), 'D')
FROM (SELECT coalesce((SELECT value ->> 'contentLanguage' FROM settings WHERE key = 'instance'), 'german')::regconfig AS cfg) l
WHERE EXISTS (SELECT 1 FROM issue_comments c WHERE c.issue_id = i.id AND c.visibility_type IS NOT NULL);

-- +goose Down
SELECT 1;
