// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
)

// The space of repositories in the administration: the largest repositories, and the quotas of single repositories.
// Administrators of Kogge see the names and sizes of all repositories, not their content.

// RepoStorage is what a repository takes on the data volume.
type RepoStorage struct {
	ID      int64      `json:"id"`
	Project ProjectRef `json:"project"`
	Slug    string     `json:"slug"`
	Name    string     `json:"name"`
	Size    int64      `json:"size" doc:"Disk usage of its Git objects in bytes"`
	LFSSize int64      `json:"lfsSize" doc:"Size of its Git LFS objects in bytes"`
	Quota   int64      `json:"quota" doc:"Most its Git and LFS objects may take together, in bytes; 0: no quota"`
	QuotaMB *int       `json:"quotaMb,omitempty" doc:"The quota set for this repository in MiB (0: none); without it, the quota of the settings applies"`
	Pushed  *time.Time `json:"pushed,omitempty"`
}

func toStorage(st settings.Instance, r *repoRow) RepoStorage {
	out := RepoStorage{ID: r.ID, Project: ProjectRef{ID: r.ProjectID, Key: r.ProjectKey, Name: r.ProjectName}, Slug: r.Slug, Name: r.Name,
		Size: r.Size, LFSSize: r.LFSSize, Quota: quotaOf(st, r), Pushed: r.Pushed}
	if r.Quota != nil {
		mb := int(*r.Quota >> 20)
		out.QuotaMB = &mb
	}
	return out
}

// RepositorySizes lists the repositories by the space they take, the largest first (administrators of Kogge).
func (s *Service) RepositorySizes(ctx context.Context, limit, offset int) (*Paged[RepoStorage], error) {
	if _, err := s.authz.RequireGlobal(ctx, authz.Admin); err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+repoColumns+` FROM repositories r JOIN code_projects p ON p.id = r.project_id
		ORDER BY r.size_bytes + r.lfs_bytes DESC, r.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*repoRow, error) { return scanRepo(row) })
	if err != nil {
		return nil, err
	}
	out := &Paged[RepoStorage]{Items: []RepoStorage{}}
	for _, r := range list {
		out.Items = append(out.Items, toStorage(st, r))
	}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM repositories").Scan(&out.Total); err != nil {
		return nil, err
	}
	return out, nil
}

// QuotaInput sets the quota of a repository.
type QuotaInput struct {
	QuotaMB *int `json:"quotaMb,omitempty" minimum:"0" maximum:"1048576" doc:"The quota in MiB (0: none); without it, the quota of the settings applies"`
}

// SetRepositoryQuota gives a repository a quota of its own, or the one of the settings again (administrators of
// Kogge).
func (s *Service) SetRepositoryQuota(ctx context.Context, id int64, in QuotaInput) (*RepoStorage, error) {
	actor, err := s.authz.RequireGlobal(ctx, authz.Admin)
	if err != nil {
		return nil, err
	}
	var quota any
	details := map[string]any{"quotaMb": nil}
	if in.QuotaMB != nil {
		quota, details["quotaMb"] = settings.MiB(*in.QuotaMB), *in.QuotaMB
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := repoByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE repositories SET quota_bytes = $2 WHERE id = $1", id, quota); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "repository.quota", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug, Details: details})
	})
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	r, err := repoByID(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	out := toStorage(st, r)
	return &out, nil
}
