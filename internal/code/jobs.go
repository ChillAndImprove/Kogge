// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
)

// PushArgs processes a push after it was recorded: the repository size and the pull requests of the changed
// branches. Other modules learn about the push from the repository.pushed event.
type PushArgs struct {
	RepoID int64 `json:"repoId"`
	PushID int64 `json:"pushId"`
}

// Kind implements river.JobArgs.
func (PushArgs) Kind() string { return "code.push" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (PushArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueGit} }

type pushWorker struct {
	river.WorkerDefaults[PushArgs]
	s *Service
}

func (w *pushWorker) Work(ctx context.Context, job *river.Job[PushArgs]) error {
	return w.s.processPush(ctx, job.Args.RepoID, job.Args.PushID)
}

// MaintenanceArgs runs git maintenance on the repositories changed since the last run.
type MaintenanceArgs struct{}

// maintenanceTimeout bounds the maintenance of one repository: repacking a large one takes a while.
const maintenanceTimeout = time.Hour

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "code.maintenance" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (MaintenanceArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueGit} }

type maintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	s *Service
}

func (w *maintenanceWorker) Timeout(*river.Job[MaintenanceArgs]) time.Duration { return 6 * time.Hour }

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	rows, err := w.s.pool.Query(ctx, "SELECT id FROM repositories WHERE pushed_at > now() - interval '25 hours'")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		// Maintenance repacks and removes objects; a backup copying the repositories must not see half of it.
		release, err := w.s.opt.Gate.Enter(ctx, maintenanceWait)
		if err != nil {
			return err
		}
		// Repositories never pack on their own (gc.auto=0: nothing repacks while a backup copies them), so this
		// runs gc itself rather than its --auto heuristics, which gc.auto=0 switches off: it packs loose objects,
		// writes the commit-graph and bitmaps, packs refs, and prunes what has been unreachable for two weeks.
		_, err = w.s.git.Run(ctx, w.s.repoPath(id), &git.Opts{Timeout: maintenanceTimeout},
			"maintenance", "run", "--task=gc", "--quiet")
		release()
		if err != nil {
			w.s.log.Warn("git maintenance failed", "repository", id, "error", err)
			continue
		}
		size := w.s.open(id).Size()
		if _, err := w.s.pool.Exec(ctx, "UPDATE repositories SET size_bytes = $2 WHERE id = $1", id, size); err != nil {
			return err
		}
	}
	return nil
}

// Register adds the module's jobs and event subscriptions.
func (s *Service) Register(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &pushWorker{s: s})
	river.AddWorker(reg.Workers, &maintenanceWorker{s: s})
	river.AddWorker(reg.Workers, &cleanupWorker{s: s})
	river.AddWorker(reg.Workers, &importWorker{s: s})
	s.registerAutoMerge(reg)
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(cleanupDue); ok {
			return CleanupArgs{RepoID: e.repoID}, nil
		}
		return nil, nil
	}, "repository.cleanup-due")
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(importRequested); ok {
			return ImportArgs{RepoID: e.repoID}, nil
		}
		return nil, nil
	}, "repository.import-requested")
	reg.Every(24*time.Hour, MaintenanceArgs{}, nil)
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(RepoEvent); ok && e.PushID != 0 {
			return PushArgs{RepoID: e.RepoID, PushID: e.PushID}, nil
		}
		return nil, nil
	}, "repository.pushed")
}

// processPush updates the repository after a push.
func (s *Service) processPush(ctx context.Context, repoID, pushID int64) error {
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return nil // deleted in the meantime
	}
	size := s.open(repoID).Size()
	if _, err := s.pool.Exec(ctx, "UPDATE repositories SET size_bytes = $2 WHERE id = $1", repoID, size); err != nil {
		return err
	}
	// Over its quota, a repository takes only pushes that bring nothing, which delete branches and tags to make room:
	// the room is made now rather than by the maintenance two weeks later.
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if quota := quotaOf(st, r); quota > 0 && size+r.LFSSize > quota {
		if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.bus.Publish(ctx, tx, cleanupDue{repoID: repoID}) }); err != nil {
			return err
		}
	}
	return s.rescope(ctx, repoID, pushID)
}

// rescope updates the open pull requests whose branches changed (pull requests follow in rescopePullRequests).
func (s *Service) rescope(ctx context.Context, repoID, pushID int64) error {
	return s.rescopePullRequests(ctx, repoID, pushID)
}
