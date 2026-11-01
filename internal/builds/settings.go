// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// BuildSettings are the pipeline settings of a repository.
type BuildSettings struct {
	AutoCancel      bool `json:"autoCancel" doc:"A new push or pull request run cancels the older unfinished runs of its pipeline on the same branch"`
	MaxParallelJobs int  `json:"maxParallelJobs" minimum:"0" maximum:"1000" doc:"How many jobs of the repository may run at once; 0: no limit"`
}

// RepoSettings returns the pipeline settings of a repository (repository administrators, like their changes).
func (s *Service) RepoSettings(ctx context.Context, p *identity.Principal, key, slug string) (*BuildSettings, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return nil, err
	}
	var v BuildSettings
	err = s.pool.QueryRow(ctx, "SELECT auto_cancel, max_parallel_jobs FROM repository_build_settings WHERE repository_id = $1", repo.ID).
		Scan(&v.AutoCancel, &v.MaxParallelJobs)
	if err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	return &v, nil
}

// SaveRepoSettings changes the pipeline settings of a repository (repository administrators).
func (s *Service) SaveRepoSettings(ctx context.Context, p *identity.Principal, key, slug string, in BuildSettings) error {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return err
	}
	if in.MaxParallelJobs < 0 {
		return apperr.Invalid("build-settings.parallel", "The number of parallel jobs cannot be negative.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO repository_build_settings (repository_id, auto_cancel, max_parallel_jobs) VALUES ($1, $2, $3)
			ON CONFLICT (repository_id) DO UPDATE SET auto_cancel = EXCLUDED.auto_cancel, max_parallel_jobs = EXCLUDED.max_parallel_jobs`,
			repo.ID, in.AutoCancel, in.MaxParallelJobs); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.build-settings", TargetType: "repository",
			TargetID: itoa(repo.ID), Details: map[string]any{"autoCancel": in.AutoCancel, "maxParallelJobs": in.MaxParallelJobs}})
	})
}
