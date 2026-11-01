-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Text search matches stemmed words (search_tsv) and, for prefixes and words the stemmer changes, the words as they
-- are (simple configuration); page search also matches parts of titles. Without indexes for the latter, every
-- search read all issues or pages (found by the scale tests, concept §19).

-- +goose Up

CREATE INDEX issues_simple_text_idx ON issues USING gin (to_tsvector('simple', key || ' ' || summary || ' ' || description_text));
CREATE INDEX issues_simple_summary_idx ON issues USING gin (to_tsvector('simple', summary));
CREATE INDEX pages_title_trgm_idx ON pages USING gin (lower(title) gin_trgm_ops);
