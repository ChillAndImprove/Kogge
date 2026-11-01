// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
)

// Releases: a version shows, for every environment, which of its issues are deployed — those with a
// commit that the environment's current deployment contains.

// VersionDeployment is an environment of a repository with the version's issues it runs.
type VersionDeployment struct {
	Repository  code.RepoRef `json:"repository"`
	Environment string       `json:"environment"`
	RunNumber   int          `json:"runNumber"`
	Ref         string       `json:"ref"`
	Finished    time.Time    `json:"finished"`
	Deployed    []string     `json:"deployed" doc:"Keys of the version's issues the environment runs"`
	Pending     []string     `json:"pending" doc:"Keys of the version's issues with commits in the repository that the environment does not run yet"`
}

// VersionDeployments returns the deployments of a version's issues.
func (s *Service) VersionDeployments(ctx context.Context, p *identity.Principal, projectKey string, versionID int64) ([]VersionDeployment, error) {
	current, err := s.issues.VersionIssueKeys(ctx, p, projectKey, versionID)
	if err != nil {
		return nil, err
	}
	out := []VersionDeployment{}
	if len(current) == 0 || s.builds == nil {
		return out, nil
	}
	// Commits may name an issue by a key it had before it moved: they count for it under its current key.
	keys := make([]string, 0, len(current))
	for k := range current {
		keys = append(keys, k)
	}
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT repository_id, sha, issue_key FROM issue_commits WHERE issue_key = ANY($1)", keys)
	if err != nil {
		return nil, err
	}
	type commitRow struct {
		Repo int64
		SHA  string
		Key  string
	}
	commits, err := pgx.CollectRows(rows, pgx.RowToStructByPos[commitRow])
	if err != nil {
		return nil, err
	}
	for i := range commits {
		commits[i].Key = current[commits[i].Key]
	}
	byRepo := map[int64][]commitRow{}
	var repoIDs []int64
	for _, c := range commits {
		if _, ok := byRepo[c.Repo]; !ok {
			repoIDs = append(repoIDs, c.Repo)
		}
		byRepo[c.Repo] = append(byRepo[c.Repo], c)
	}
	readable, err := s.code.ReadableRepos(ctx, p, repoIDs)
	if err != nil {
		return nil, err
	}
	slices.Sort(repoIDs)
	for _, repoID := range repoIDs {
		if !readable[repoID] {
			continue
		}
		repo, err := s.code.RepoInfo(ctx, repoID)
		if err != nil || repo == nil {
			continue
		}
		deps, err := s.builds.CurrentDeployments(ctx, repoID)
		if err != nil {
			return nil, err
		}
		for _, dep := range deps {
			vd := VersionDeployment{Repository: *repo, Environment: dep.Environment, RunNumber: dep.RunNumber, Ref: dep.Ref, Finished: dep.Finished,
				Deployed: []string{}, Pending: []string{}}
			done := map[string]bool{}
			for _, c := range byRepo[repoID] {
				if !done[c.Key] && s.code.Contains(ctx, repoID, dep.Commit, c.SHA) {
					done[c.Key] = true
				}
			}
			seen := map[string]bool{}
			for _, c := range byRepo[repoID] {
				if seen[c.Key] {
					continue
				}
				seen[c.Key] = true
				if done[c.Key] {
					vd.Deployed = append(vd.Deployed, c.Key)
				} else {
					vd.Pending = append(vd.Pending, c.Key)
				}
			}
			slices.Sort(vd.Deployed)
			slices.Sort(vd.Pending)
			out = append(out, vd)
		}
	}
	return out, nil
}
