// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/feed"
)

// ActivityFeed returns Helling's part of an activity stream: finished runs and deployments (that
// succeeded or failed) of one repository (repoID) or of all the principal may read, newest first.
func (s *Service) ActivityFeed(ctx context.Context, p *identity.Principal, q feed.Query, repoID int64) ([]feed.Entry, error) {
	limit := strconv.Itoa(q.Limit + 1)
	type row struct {
		e      feed.Entry
		repo   int64
		actor  int64
		number int
	}
	var rows []row
	collect := func(sql string, args db.Args, scan func(pgx.CollectableRow, *row) error) error {
		res, err := s.pool.Query(ctx, sql, db.CustomPlan(args)...)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(res, func(cr pgx.CollectableRow) (row, error) {
			r := row{e: feed.Entry{Product: feed.ProductHelling}}
			err := scan(cr, &r)
			return r, err
		})
		rows = append(rows, list...)
		return err
	}

	var args db.Args
	readable, err := s.readableSQL(ctx, p, &args)
	if err != nil {
		return nil, err
	}
	where := "r.finished_at IS NOT NULL AND " + readable + " AND " + q.Before.After(feed.SourceRun, "r.finished_at", "r.id", &args)
	if repoID != 0 {
		where += " AND r.repository_id = " + args.Add(repoID)
	}
	if q.ActorID != 0 {
		where += " AND r.triggered_by = " + args.Add(q.ActorID)
	}
	if q.Mine {
		where += " AND r.triggered_by = " + args.Add(p.UserID())
	}
	err = collect(`SELECT r.id, r.finished_at, r.repository_id, coalesce(r.triggered_by, 0), r.state, r.number, r.pipeline_name,
			coalesce(r.branch, r.tag, '') FROM runs r WHERE `+where+` ORDER BY r.finished_at DESC, r.id DESC LIMIT `+limit, args,
		func(cr pgx.CollectableRow, r *row) error {
			var state, name string
			if err := cr.Scan(&r.e.ID, &r.e.At, &r.repo, &r.actor, &state, &r.number, &name, &r.e.Detail); err != nil {
				return err
			}
			r.e.Source, r.e.Verb, r.e.Title = feed.SourceRun, "run."+state, "#"+strconv.Itoa(r.number)+" "+name
			r.e.Pipeline, r.e.Number = name, r.number
			return nil
		})
	if err != nil {
		return nil, err
	}

	args = nil
	if readable, err = s.readableSQL(ctx, p, &args); err != nil {
		return nil, err
	}
	where = "d.finished_at IS NOT NULL AND d.state IN ('succeeded', 'failed') AND " + readable + " AND " +
		q.Before.After(feed.SourceDeployment, "d.finished_at", "d.id", &args)
	if repoID != 0 {
		where += " AND e.repository_id = " + args.Add(repoID)
	}
	if q.ActorID != 0 {
		where += " AND d.created_by = " + args.Add(q.ActorID)
	}
	if q.Mine {
		me := args.Add(p.UserID())
		where += " AND (d.created_by = " + me + " OR r.triggered_by = " + me + ")"
	}
	err = collect(`SELECT d.id, d.finished_at, r.repository_id, coalesce(d.created_by, 0), d.state, r.number, r.pipeline_name, e.name
		FROM deployments d JOIN environments e ON e.id = d.environment_id JOIN runs r ON r.id = d.run_id
		WHERE `+where+` ORDER BY d.finished_at DESC, d.id DESC LIMIT `+limit, args,
		func(cr pgx.CollectableRow, r *row) error {
			var state, name string
			if err := cr.Scan(&r.e.ID, &r.e.At, &r.repo, &r.actor, &state, &r.number, &name, &r.e.Detail); err != nil {
				return err
			}
			r.e.Source, r.e.Verb, r.e.Title = feed.SourceDeployment, "deployment."+state, "#"+strconv.Itoa(r.number)+" "+name
			r.e.Pipeline, r.e.Number = name, r.number
			return nil
		})
	if err != nil {
		return nil, err
	}

	ids := []int64{}
	for _, r := range rows {
		if r.actor != 0 {
			ids = append(ids, r.actor)
		}
	}
	users, err := identity.GetUserRefs(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	repos := map[int64]*Repo{}
	out := make([]feed.Entry, 0, len(rows))
	for _, r := range rows {
		repo, ok := repos[r.repo]
		if !ok {
			if repo, err = s.repos.Repo(ctx, r.repo); err != nil {
				repo = nil // deleted meanwhile
			}
			repos[r.repo] = repo
		}
		if repo == nil {
			continue
		}
		r.e.URL = runPath(repo, r.number)
		r.e.Container = feed.Container{Kind: "repository", Key: repo.ProjectKey + "/" + repo.Slug, Name: repo.Name,
			URL: PipelinesPath(repo.ProjectKey, repo.Slug)}
		if u, ok := users[r.actor]; ok {
			r.e.Actor = &u
		}
		out = append(out, r.e)
	}
	return out, nil
}
