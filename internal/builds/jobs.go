// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/jobs"
)

// Periodic work of the module: jobs whose agent was lost or whose time ran out, scheduled pipelines, and the
// retention of artifacts, caches and logs.

const (
	timeoutGrace    = 2 * time.Minute     // agents stop timed-out jobs themselves; the server steps in after this
	scheduleCatchUp = 10 * time.Minute    // missed schedule minutes (a busy or restarted leader) run late, older ones never
	cacheRetention  = 30 * 24 * time.Hour // caches unused for this long are deleted
	keptDeployments = 10                  // artifacts of the latest successful deployments per environment never expire
	orphanLogsEvery = 24 * time.Hour      // log files of deleted jobs are removed this often
)

// ReaperArgs fails running jobs whose agent stopped sending heartbeats or which ran past their timeout.
type ReaperArgs struct{}

// Kind implements river.JobArgs.
func (ReaperArgs) Kind() string { return "builds.reaper" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ReaperArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueBuilds} }

type reaperWorker struct {
	river.WorkerDefaults[ReaperArgs]
	s *Service
}

func (w *reaperWorker) Work(ctx context.Context, _ *river.Job[ReaperArgs]) error {
	return w.s.reap(ctx)
}

// ScheduleArgs starts the pipelines whose schedule is due.
type ScheduleArgs struct{}

// Kind implements river.JobArgs.
func (ScheduleArgs) Kind() string { return "builds.schedule" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ScheduleArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueBuilds} }

type scheduleWorker struct {
	river.WorkerDefaults[ScheduleArgs]
	s *Service
}

func (w *scheduleWorker) Work(ctx context.Context, _ *river.Job[ScheduleArgs]) error {
	return w.s.runSchedules(ctx)
}

// RetentionArgs deletes expired artifacts, unused caches and old logs.
type RetentionArgs struct{}

// Kind implements river.JobArgs.
func (RetentionArgs) Kind() string { return "builds.retention" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RetentionArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueBuilds} }

type retentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	s *Service
}

func (w *retentionWorker) Timeout(*river.Job[RetentionArgs]) time.Duration { return time.Hour }

func (w *retentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	return w.s.retain(ctx)
}

// Register adds the module's jobs and event subscriptions.
func (s *Service) Register(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &reaperWorker{s: s})
	river.AddWorker(reg.Workers, &scheduleWorker{s: s})
	river.AddWorker(reg.Workers, &retentionWorker{s: s})
	s.subscribeFollowers(reg)
	reg.Every(30*time.Second, ReaperArgs{}, nil)
	reg.Every(time.Minute, ScheduleArgs{}, nil)
	reg.Every(time.Hour, RetentionArgs{}, nil)
}

// reap finishes the running jobs that were lost.
func (s *Service) reap(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id FROM run_jobs WHERE state = 'running'
		AND (lease_until < now() OR deadline_at < now() - $1::interval) ORDER BY id LIMIT 500`, interval(timeoutGrace))
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		reaped := ""
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			// Checked again under the lock: a heartbeat may have arrived in the meantime.
			var lost, late bool
			var timeout string
			err := tx.QueryRow(ctx, `SELECT lease_until < now(), coalesce(deadline_at < now() - $2::interval, false),
				coalesce(to_char(deadline_at - started_at, 'HH24:MI:SS'), '') FROM run_jobs WHERE id = $1 AND state = 'running' FOR UPDATE`,
				id, interval(timeoutGrace)).Scan(&lost, &late, &timeout)
			if db.IsNoRows(err) || (err == nil && !lost && !late) {
				return nil
			}
			if err != nil {
				return err
			}
			reaped = "The agent stopped responding."
			if late {
				reaped = "The job ran longer than its time limit of " + timeout + "."
			}
			return s.finishJob(ctx, tx, id, JobFailed, nil, reaped)
		})
		if err != nil {
			return err
		}
		if reaped != "" {
			s.log.Info("pipeline job failed by the server", "job", id, "reason", reaped)
		}
	}
	return nil
}

// runSchedules starts the scheduled pipelines whose cron expression matched a minute since their last run.
// Expressions are evaluated in the instance time zone.
func (s *Service) runSchedules(ctx context.Context) error {
	loc := time.UTC
	if st, err := s.settings.Get(ctx); err == nil {
		if l, err := time.LoadLocation(st.DefaultTimeZone); err == nil {
			loc = l
		}
	}
	current := now().Truncate(time.Minute)
	type schedule struct {
		RepoID   int64
		Pipeline string
		Branch   string
		Cron     string
		Last     *time.Time
	}
	rows, err := s.pool.Query(ctx, "SELECT repository_id, pipeline, branch, cron, last_run_at FROM build_schedules")
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[schedule])
	if err != nil {
		return err
	}
	for _, sc := range list {
		c, err := spec.ParseCron(sc.Cron)
		if err != nil {
			continue
		}
		if !due(c, sc.Last, current, loc) {
			continue
		}
		// Only one node starts the run: whoever moves last_run_at.
		tag, err := s.pool.Exec(ctx, `UPDATE build_schedules SET last_run_at = $5 WHERE repository_id = $1 AND pipeline = $2 AND branch = $3 AND cron = $4
			AND (last_run_at IS NULL OR last_run_at < $5)`, sc.RepoID, sc.Pipeline, sc.Branch, sc.Cron, current)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		commit, err := s.repos.Resolve(ctx, sc.RepoID, "refs/heads/"+sc.Branch)
		if err != nil {
			s.log.Warn("scheduled pipeline: branch not found", "repository", sc.RepoID, "branch", sc.Branch, "error", err)
			continue
		}
		ev := spec.Event{Kind: spec.EventSchedule, Branch: sc.Branch, Pipeline: sc.Pipeline}
		if _, err := s.Trigger(ctx, TriggerInput{RepoID: sc.RepoID, Event: ev, Commit: commit, only: sc.Pipeline}); err != nil {
			s.log.Warn("scheduled pipeline failed to start", "repository", sc.RepoID, "pipeline", sc.Pipeline, "error", err)
		}
	}
	return nil
}

// due reports whether the expression matched a minute after the last run, up to the current minute.
func due(c *spec.Cron, last *time.Time, current time.Time, loc *time.Location) bool {
	from := current.Add(-scheduleCatchUp)
	if last != nil && last.After(from) {
		from = last.Truncate(time.Minute)
	}
	for t := from.Add(time.Minute); !t.After(current); t = t.Add(time.Minute) {
		if c.Matches(t.In(loc)) {
			return true
		}
	}
	return false
}

// retain applies the retention of artifacts, caches and logs.
func (s *Service) retain(ctx context.Context) error {
	// Expired artifacts, except those of runs among the latest successful deployments of an environment (they
	// are needed to deploy again).
	if _, err := s.pool.Exec(ctx, `WITH kept AS (
			SELECT run_id FROM (SELECT run_id, row_number() OVER (PARTITION BY environment_id ORDER BY created_at DESC) AS n
				FROM deployments WHERE state = 'succeeded') d WHERE n <= $1),
		gone AS (DELETE FROM artifacts a WHERE a.expires_at < now() AND a.run_id NOT IN (SELECT run_id FROM kept) RETURNING a.id)
		DELETE FROM blob_refs WHERE owner_type = 'artifact' AND owner_id IN (SELECT id FROM gone)`, keptDeployments); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `WITH gone AS (DELETE FROM build_caches WHERE used_at < now() - $1::interval RETURNING id)
		DELETE FROM blob_refs WHERE owner_type = 'build-cache' AND owner_id IN (SELECT id FROM gone)`, interval(cacheRetention)); err != nil {
		return err
	}
	// References of artifacts and caches deleted with their run or repository.
	if _, err := s.pool.Exec(ctx, `DELETE FROM blob_refs r WHERE r.owner_type = 'artifact'
		AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.id = r.owner_id)`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM blob_refs r WHERE r.owner_type = 'build-cache'
		AND NOT EXISTS (SELECT 1 FROM build_caches c WHERE c.id = r.owner_id)`); err != nil {
		return err
	}
	if err := s.expireLogs(ctx); err != nil {
		return err
	}
	return s.removeOrphanLogs(ctx)
}

// expireLogs deletes the logs of jobs that finished before the retention period. It waits for the next run while
// a backup copies the logs.
func (s *Service) expireLogs(ctx context.Context) error {
	st, err := s.settings.Get(ctx)
	if err != nil || st.BuildLogRetentionDays <= 0 {
		return err
	}
	for more := true; more; {
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			more = false
			if running, err := db.BackupRunning(ctx, tx); err != nil || running {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id FROM run_jobs WHERE finished_at < now() - make_interval(days => $1) AND NOT log_expired
				ORDER BY id LIMIT 1000`, st.BuildLogRetentionDays)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil || len(ids) == 0 {
				return err
			}
			for _, id := range ids {
				if err := os.Remove(s.logPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "UPDATE run_jobs SET log_expired = true WHERE id = ANY($1)", ids); err != nil {
				return err
			}
			more = len(ids) == 1000
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// removeOrphanLogs deletes the log files of jobs that no longer exist (their run or repository was deleted), once
// a day, but not while a backup copies the logs.
func (s *Service) removeOrphanLogs(ctx context.Context) error {
	marker := filepath.Join(s.opt.DataDir, "logs", ".swept")
	if fi, err := os.Stat(marker); err == nil && now().Sub(fi.ModTime()) < orphanLogsEvery {
		return nil
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if running, err := db.BackupRunning(ctx, tx); err != nil || running {
			return err
		}
		return s.sweepLogs(ctx, tx, marker)
	})
}

func (s *Service) sweepLogs(ctx context.Context, tx pgx.Tx, marker string) error {
	for bucket := range 100 {
		dir := filepath.Join(s.opt.DataDir, "logs", strconv.Itoa(bucket))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var ids []int64
		for _, e := range entries {
			if id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".log"), 10, 64); err == nil && strings.HasSuffix(e.Name(), ".log") {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		rows, err := tx.Query(ctx, "SELECT id FROM run_jobs WHERE id = ANY($1)", ids)
		if err != nil {
			return err
		}
		existing, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		known := make(map[int64]bool, len(existing))
		for _, id := range existing {
			known[id] = true
		}
		for _, id := range ids {
			if !known[id] {
				_ = os.Remove(s.logPath(id))
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o750); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o640)
}

// interval formats a duration for a PostgreSQL interval parameter.
func interval(d time.Duration) string { return fmt.Sprintf("%d seconds", int64(d.Seconds())) }
