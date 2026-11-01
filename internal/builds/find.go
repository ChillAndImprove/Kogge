// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/search"
)

// Helling's part of the quick search: the pipelines of a repository are where its runs are, so the
// quick search offers the pipelines of the repositories it finds (package links) with the state of their newest
// run; runs visited recently are found by id.

// readableSQL restricts runs (alias r) to the repositories the principal may read.
func (s *Service) readableSQL(ctx context.Context, p *identity.Principal, args *db.Args) (string, error) {
	all, projects, repos, err := s.repos.Readable(ctx, p)
	if err != nil {
		return "", err
	}
	if all {
		return "TRUE", nil
	}
	return "(r.project_id = ANY(" + args.Add(nonNil(projects)) + ") OR r.repository_id = ANY(" + args.Add(nonNil(repos)) + "))", nil
}

// NewestRun is the newest run of a repository.
type NewestRun struct {
	Number int
	Name   string
	State  string
}

// NewestRuns returns the newest run of each repository of the ids that the principal may read and that has runs.
func (s *Service) NewestRuns(ctx context.Context, p *identity.Principal, repoIDs []int64) (map[int64]NewestRun, error) {
	out := map[int64]NewestRun{}
	if len(repoIDs) == 0 {
		return out, nil
	}
	args := db.Args{repoIDs}
	readable, err := s.readableSQL(ctx, p, &args)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (r.repository_id) r.repository_id, r.number, r.pipeline_name, r.state FROM runs r
		WHERE r.repository_id = ANY($1) AND `+readable+` ORDER BY r.repository_id, r.created_at DESC, r.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n NewestRun
		if err := rows.Scan(&id, &n.Number, &n.Name, &n.State); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RunHits returns the runs of the ids that the principal may see.
func (s *Service) RunHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	args := db.Args{nonNil(ids)}
	readable, err := s.readableSQL(ctx, p, &args)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+runColumns+" FROM runs r WHERE r.id = ANY($1) AND "+readable, args...)
	if err != nil {
		return nil, err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, err
	}
	repos := map[int64]*Repo{}
	out := make([]search.Hit, 0, len(runs))
	for _, r := range runs {
		repo, ok := repos[r.RepoID]
		if !ok {
			if repo, err = s.repos.Repo(ctx, r.RepoID); err != nil {
				repo = nil // deleted meanwhile
			}
			repos[r.RepoID] = repo
		}
		if repo == nil {
			continue
		}
		created := r.Created
		ref := r.Branch
		if ref == "" {
			ref = r.Tag
		}
		title := r.Name
		if ref != "" {
			title += " · " + ref
		}
		out = append(out, search.Hit{Kind: search.KindRun, ID: r.ID, Key: "#" + strconv.Itoa(r.Number), Title: title,
			Context: repo.ProjectKey + "/" + repo.Slug, URL: runPath(repo, r.Number), State: r.State, Updated: &created})
	}
	return out, nil
}

// PipelinesPath is the address of a repository's pipelines in Helling.
func PipelinesPath(projectKey, slug string) string {
	return "/builds/" + strings.ToLower(projectKey) + "/" + slug
}
