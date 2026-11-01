-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Indexes for lists and searches that grow with use (tools/scale, concept §19).

-- +goose Up

-- The space directory orders spaces by their latest change: the newest current page of each space. Without this
-- index, the newest pages of all spaces are read until one of the space turns up, which for spaces nobody changed
-- for a while means most pages of the installation.
CREATE INDEX pages_space_updated_idx ON pages (space_id, updated_at DESC) WHERE status = 'current';

-- Issue search matches the unstemmed words of issues (key, summary and description) with to_tsvector('simple', …).
-- PostgreSQL rates that function as cheap, so on installations with a few thousand issues it computes the vector
-- of every issue instead of using the index: half a second for each quick search and each keystroke in issue
-- pickers. The same function with its true cost makes the index the obvious choice at every size; the indexes of
-- migration 00020 are replaced by indexes on it.
-- +goose StatementBegin
CREATE FUNCTION kogge_simple_words(text) RETURNS tsvector
    LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE COST 1000
    AS $$ BEGIN RETURN to_tsvector('simple', $1); END $$;
-- +goose StatementEnd

CREATE INDEX issues_words_idx ON issues USING gin (kogge_simple_words(key || ' ' || summary || ' ' || description_text));
CREATE INDEX issues_summary_words_idx ON issues USING gin (kogge_simple_words(summary));
DROP INDEX issues_simple_text_idx;
DROP INDEX issues_simple_summary_idx;

-- Statistics of the new index expressions: without them the planner guesses that a search matches far fewer issues
-- than it does and combines it with the index of the projects, which made quick searches at 200,000 issues three
-- times slower (51 ms against 27 ms) until autovacuum analyzed the table — on a quiet installation, weeks later.
ANALYZE issues;
