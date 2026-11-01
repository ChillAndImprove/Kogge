-- SPDX-FileCopyrightText: 2026 The Kogge authors
-- SPDX-License-Identifier: EUPL-1.2

-- Quick search, the search page and activity streams across the products (concept §11.9, §12.3).
--
-- Search as you type matches the words of titles by their beginnings (simple configuration, like the issues'
-- summaries in 00020): pages and pull requests get the indexes for it, and pull requests one for their texts
-- (the search page). Without them every keystroke read all pages or pull requests. Short beginnings of words
-- ("de", "sch") match a large part of all summaries; for people who see a few projects only, the quick search reads
-- each project's issues newest first (issues_project_updated_idx) instead of all the instance's matches.
--
-- Activity streams read what happened from where the products keep it — issues and pages from activity, pushes,
-- pull request events and comments, finished runs and deployments from their tables — newest first and across
-- repositories; the indexes below read them in that order.
--
-- recent_visits holds what each person opened last (the quick search shows it before anything is typed), on every
-- device; the newest 100 per person are kept.

-- +goose Up

CREATE INDEX issues_project_updated_idx ON issues (project_id, updated_at DESC);
CREATE INDEX pages_simple_title_idx ON pages USING gin (to_tsvector('simple', title)) WHERE status = 'current';
CREATE INDEX pull_requests_simple_title_idx ON pull_requests USING gin (to_tsvector('simple', title));
CREATE INDEX pull_requests_simple_text_idx ON pull_requests USING gin (to_tsvector('simple', title || ' ' || description_text));

CREATE INDEX activity_entity_at_idx ON activity (entity_type, at DESC, id DESC);
CREATE INDEX pushes_at_idx ON pushes (pushed_at DESC, id DESC);
CREATE INDEX pull_request_activities_at_idx ON pull_request_activities (at DESC, id DESC);
CREATE INDEX pull_request_comments_threads_idx ON pull_request_comments (created_at DESC, id DESC) WHERE parent_id IS NULL;
CREATE INDEX runs_finished_idx ON runs (finished_at DESC, id DESC) WHERE finished_at IS NOT NULL;
CREATE INDEX runs_repository_finished_idx ON runs (repository_id, finished_at DESC, id DESC) WHERE finished_at IS NOT NULL;
CREATE INDEX deployments_finished_idx ON deployments (finished_at DESC, id DESC) WHERE finished_at IS NOT NULL;

CREATE TABLE recent_visits (
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('issue', 'project', 'page', 'space', 'code-project', 'repository',
                                             'pull-request', 'run')),
    entity_id  bigint NOT NULL,
    visited_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind, entity_id)
);
CREATE INDEX recent_visits_user_idx ON recent_visits (user_id, visited_at DESC);

-- +goose Down
SELECT 1;
