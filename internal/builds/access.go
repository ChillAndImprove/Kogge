// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
)

// Permissions follow the repository: reading runs needs repo.read, running and cancelling repo.write,
// environments, variables and secrets repo.admin.

// repoFor returns a repository the principal may access at the level.
func (s *Service) repoFor(ctx context.Context, p *identity.Principal, key, slug string, level int) (*Repo, int, error) {
	repo, err := s.repos.RepoByPath(ctx, key, slug)
	if err != nil {
		return nil, LevelNone, err
	}
	l, err := s.repos.Level(ctx, p, repo.ID)
	if err != nil {
		return nil, LevelNone, err
	}
	if l < LevelRead {
		return nil, l, apperr.NotFound("repository.not-found", "The repository %s/%s does not exist or you may not see it.", key, slug)
	}
	if l < level {
		if level >= LevelAdmin {
			return nil, l, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can do this.")
		}
		return nil, l, apperr.Forbidden("repository.write-required", "You may not run builds of this repository.")
	}
	return repo, l, nil
}

// repoOfRun checks access to the repository of a run.
func (s *Service) repoOfRun(ctx context.Context, p *identity.Principal, repoID int64, level int) (*Repo, error) {
	repo, err := s.repos.Repo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	l, err := s.repos.Level(ctx, p, repoID)
	if err != nil {
		return nil, err
	}
	if l < LevelRead {
		return nil, apperr.NotFound("run.not-found", "The run does not exist or you may not see it.")
	}
	if l < level {
		return nil, apperr.Forbidden("repository.write-required", "You may not run builds of this repository.")
	}
	return repo, nil
}

// notFoundAs replaces a not-found error of a run or repository by the not-found error of the object that was asked
// for: for who cannot see a run, its jobs and artifacts do not exist.
func notFoundAs(err, notFound error) error {
	if apperr.IsNotFound(err) {
		return notFound
	}
	return err
}

func jobNotFound() error { return apperr.NotFound("job.not-found", "The job does not exist.") }
