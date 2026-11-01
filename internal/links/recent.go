// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/search"
)

// Recently visited: before anything is typed, the quick search shows what the person opened last,
// on whichever device. The web application records a visit when the page of an issue, a page, a repository …
// opens; reading them shows only what the person may still see, with its current title.

// recentKept is how many visits are kept per person.
const recentKept = 100

// VisitKinds are the kinds of things whose visits are kept; pages include blog posts.
var VisitKinds = []string{search.KindIssue, search.KindProject, search.KindPage, search.KindSpace, search.KindCodeProject,
	search.KindRepository, search.KindPullRequest, search.KindRun}

// RecordVisit remembers that the principal opened something; the oldest visits beyond recentKept are forgotten.
// It does not check that the principal may see it: reading the visits does.
func (s *Service) RecordVisit(ctx context.Context, p *identity.Principal, kind string, id int64) error {
	if !slices.Contains(VisitKinds, kind) || id <= 0 {
		return apperr.Invalidf("visit.invalid", "%s %d is nothing whose visits are kept.", kind, id)
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO recent_visits (user_id, kind, entity_id) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, kind, entity_id) DO UPDATE SET visited_at = now()`, p.UserID(), kind, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM recent_visits WHERE user_id = $1 AND visited_at < (SELECT visited_at FROM recent_visits
			WHERE user_id = $1 ORDER BY visited_at DESC OFFSET $2 LIMIT 1)`, p.UserID(), recentKept-1)
		return err
	})
}

// Recent returns what the principal opened last and may still see, the last visited first.
func (s *Service) Recent(ctx context.Context, p *identity.Principal, limit int) ([]search.Hit, error) {
	type visit struct {
		kind string
		id   int64
	}
	rows, err := s.pool.Query(ctx, "SELECT kind, entity_id FROM recent_visits WHERE user_id = $1 ORDER BY visited_at DESC, kind, entity_id LIMIT $2",
		p.UserID(), recentKept)
	if err != nil {
		return nil, err
	}
	visits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (visit, error) {
		var v visit
		err := row.Scan(&v.kind, &v.id)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	ids := map[string][]int64{}
	for _, v := range visits {
		ids[v.kind] = append(ids[v.kind], v.id)
	}
	resolve := map[string]func(context.Context, *identity.Principal, []int64) ([]search.Hit, error){
		search.KindIssue:       s.issues.IssueHits,
		search.KindProject:     s.issues.ProjectHits,
		search.KindPage:        s.wiki.PageHits,
		search.KindSpace:       s.wiki.SpaceHits,
		search.KindCodeProject: s.code.CodeProjectHits,
		search.KindRepository:  s.code.RepositoryHits,
		search.KindPullRequest: s.code.PullRequestHits,
	}
	if s.builds != nil {
		resolve[search.KindRun] = s.builds.RunHits
	}
	found := map[visit]search.Hit{}
	for kind, list := range ids {
		fn, ok := resolve[kind]
		if !ok {
			continue
		}
		hits, err := fn(ctx, p, list)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			found[visit{kind, h.ID}] = h
		}
	}
	out := []search.Hit{}
	for _, v := range visits {
		if h, ok := found[v]; ok && len(out) < limit {
			out = append(out, h)
		}
	}
	return out, nil
}
