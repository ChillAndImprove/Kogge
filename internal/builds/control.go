// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// RunPipeline starts pipelines manually at a branch, tag or commit, with variables of the run form.
func (s *Service) RunPipeline(ctx context.Context, p *identity.Principal, key, slug, ref, pipeline string, vars map[string]string) ([]int64, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	for name, v := range vars {
		if !varNamePattern.MatchString(name) || strings.HasPrefix(name, "KOGGE_") || len(v) > 65536 {
			return nil, apperr.Invalidf("run.variable", "Invalid variable %s.", name)
		}
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	// People start runs in the browser (manual); programs with an access token start API runs.
	kind := spec.EventManual
	if p.Method == identity.MethodToken {
		kind = spec.EventAPI
	}
	ev := spec.Event{Kind: kind, Pipeline: pipeline}
	commit, err := s.resolveRunRef(ctx, repo.ID, ref, &ev)
	if err != nil {
		return nil, err
	}
	return s.Trigger(ctx, TriggerInput{RepoID: repo.ID, Event: ev, Commit: commit, ActorID: p.UserID(), Variables: vars})
}

// resolveRunRef finds what the ref of a manual run names: a branch, a tag or a commit. A run gets the branch or tag
// (and with it the protection of that ref) only if the ref is one; a commit runs without a ref.
func (s *Service) resolveRunRef(ctx context.Context, repoID int64, ref string, ev *spec.Event) (string, error) {
	if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		ev.Branch = name
		return s.repos.Resolve(ctx, repoID, ref)
	}
	if name, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		ev.Tag = name
		return s.repos.Resolve(ctx, repoID, ref)
	}
	if commit, err := s.repos.Resolve(ctx, repoID, "refs/heads/"+ref); err == nil {
		ev.Branch = ref
		return commit, nil
	}
	if commit, err := s.repos.Resolve(ctx, repoID, "refs/tags/"+ref); err == nil {
		ev.Tag = ref
		return commit, nil
	}
	return s.repos.Resolve(ctx, repoID, ref)
}

// runForWrite loads a run for a change by a user who can write to its repository.
func (s *Service) runForWrite(ctx context.Context, tx pgx.Tx, p *identity.Principal, runID int64) (*runRow, error) {
	run, err := s.runByID(ctx, tx, runID, true)
	if err != nil {
		return nil, err
	}
	if _, err := s.repoOfRun(ctx, p, run.RepoID, LevelWrite); err != nil {
		return nil, err
	}
	return run, nil
}

// Cancel stops a run: waiting jobs are cancelled, running ones are told to stop.
func (s *Service) Cancel(ctx context.Context, p *identity.Principal, runID int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := s.runForWrite(ctx, tx, p, runID)
		if err != nil {
			return err
		}
		if run.State != RunQueued && run.State != RunRunning {
			return apperr.Conflict("run.finished", "The run has finished.")
		}
		return s.cancelRun(ctx, tx, run)
	})
}

// cancelRun cancels the jobs that have not started and asks the agents of the running ones to stop. The run is
// cancelled right away; its running jobs finish as cancelled when their agents report back.
func (s *Service) cancelRun(ctx context.Context, tx pgx.Tx, run *runRow) error {
	if _, err := tx.Exec(ctx, "UPDATE run_jobs SET cancel_requested = true WHERE run_id = $1 AND state = 'running'", run.ID); err != nil {
		return err
	}
	jobs, err := s.runJobs(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.State == JobCreated || j.State == JobPending || j.State == JobWaiting {
			if err := s.setJobState(ctx, tx, j, JobCancelled, ""); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE runs SET state = 'cancelled', finished_at = coalesce(finished_at, now()) WHERE id = $1", run.ID); err != nil {
		return err
	}
	if err := s.setStatus(ctx, tx, run.RepoID, run.Commit, "kogge/"+run.Pipeline, "cancelled", run.Name, run.ID); err != nil {
		return err
	}
	return s.publish(ctx, tx, RunEvent{Name: "run.cancelled", RunID: run.ID, RepoID: run.RepoID, State: RunCancelled})
}

// autoCancel cancels older unfinished runs of the pipeline on the same branch, when the repository's settings ask
// for it. Runs that deploy at the moment are left alone.
func (s *Service) autoCancel(ctx context.Context, tx pgx.Tx, in TriggerInput, pipeline string, newRunID int64) error {
	if in.Event.Branch == "" || (in.Event.Kind != spec.EventPush && in.Event.Kind != spec.EventPullRequest) {
		return nil
	}
	var on bool
	err := tx.QueryRow(ctx, "SELECT auto_cancel FROM repository_build_settings WHERE repository_id = $1", in.RepoID).Scan(&on)
	if db.IsNoRows(err) || (err == nil && !on) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT "+runColumns+` FROM runs r WHERE repository_id = $1 AND pipeline = $2 AND branch = $3 AND event = $4
		AND id <> $5 AND state IN ('queued', 'running')
		AND NOT EXISTS (SELECT 1 FROM run_jobs j WHERE j.run_id = r.id AND j.environment IS NOT NULL AND j.state = 'running') FOR UPDATE`,
		in.RepoID, pipeline, in.Event.Branch, in.Event.Kind, newRunID)
	if err != nil {
		return err
	}
	older, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return err
	}
	for _, run := range older {
		if err := s.cancelRun(ctx, tx, run); err != nil {
			return err
		}
	}
	return nil
}

// Rerun starts the pipeline of a run again at the same commit.
func (s *Service) Rerun(ctx context.Context, p *identity.Principal, runID int64) (int64, error) {
	run, err := s.runByID(ctx, s.pool, runID, false)
	if err != nil {
		return 0, err
	}
	if _, err := s.repoOfRun(ctx, p, run.RepoID, LevelWrite); err != nil {
		return 0, err
	}
	// A run that followed another one keeps it (its artifacts and variables); included files of other repositories
	// are read at the commits the run read them at.
	ev := spec.Event{Kind: run.Event, Branch: run.Branch, Tag: run.Tag, TargetBranch: run.Target, Pipeline: run.Pipeline}
	ids, err := s.Trigger(ctx, TriggerInput{RepoID: run.RepoID, Event: ev, Commit: run.Commit, ActorID: p.UserID(), PullRequestID: run.PRID, PullRequestNo: run.PRNumber,
		Variables: run.Variables, UpstreamRunID: run.Upstream, ChainDepth: run.ChainDepth, only: run.Pipeline, pins: pinsOf(run.Includes)})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, apperr.Conflict("run.not-triggered", "The pipeline %s does not run for this event any more.", run.Pipeline)
	}
	return ids[0], nil
}

// RetryJob runs a finished job again; the jobs that were skipped or cancelled because of it (those that wait for
// it, also indirectly) run again too.
func (s *Service) RetryJob(ctx context.Context, p *identity.Principal, jobID int64) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1 FOR UPDATE", jobID))
		if db.IsNoRows(err) {
			return jobNotFound()
		}
		if err != nil {
			return err
		}
		run, err := s.runForWrite(ctx, tx, p, j.RunID)
		if err != nil {
			return notFoundAs(err, jobNotFound())
		}
		if !jobFinal(j.State) || j.State == JobSkipped {
			return apperr.Conflict("job.not-finished", "Only finished jobs can be run again.")
		}
		if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = 'created', attempt = attempt + 1, agent_id = NULL, token_hash = NULL, lease_until = NULL,
			failure = '', exit_code = NULL, finished_at = NULL, cancel_requested = false, triggered_by = $2 WHERE id = $1`, jobID, p.UserID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM test_reports WHERE job_id = $1", jobID); err != nil {
			return err
		}
		jobs, err := s.runJobs(ctx, tx, j.RunID)
		if err != nil {
			return err
		}
		g := newGraph(jobs)
		again := []int64{}
		for _, d := range g.dependents(g.byKey[jobKey(j)]) {
			// Jobs that their conditions left out stay skipped: the run's event has not changed.
			if d.State == JobCancelled || d.State == JobSkipped && !skippedByConditions(d.SkipReason) {
				again = append(again, d.ID)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = 'created', finished_at = NULL, skip_reason = '', skip_detail = ''
			WHERE id = ANY($1)`, again); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE runs SET state = 'running', finished_at = NULL WHERE id = $1", run.ID); err != nil {
			return err
		}
		return s.advance(ctx, tx, run.ID)
	})
	return err
}

// Play starts a job that waits for a manual start or for a deployer.
func (s *Service) Play(ctx context.Context, p *identity.Principal, jobID int64) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1 FOR UPDATE", jobID))
		if db.IsNoRows(err) {
			return jobNotFound()
		}
		if err != nil {
			return err
		}
		run, err := s.runForWrite(ctx, tx, p, j.RunID)
		if err != nil {
			return notFoundAs(err, jobNotFound())
		}
		if j.State != JobWaiting || (j.WaitingFor != "manual" && j.WaitingFor != "deployer") {
			return apperr.Conflict("job.not-waiting", "The job does not wait for a start.")
		}
		next, waiting := JobPending, ""
		if j.Environment != "" {
			env, err := scanEnv(tx.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE repository_id = $1 AND name = $2", run.RepoID, j.Environment))
			if err != nil {
				return err
			}
			if len(env.DeployerUsers)+len(env.DeployerGroups) > 0 && !holds(env.DeployerUsers, env.DeployerGroups, p) {
				return apperr.Forbidden("environment.deployer", "You may not deploy to %s.", env.Name)
			}
			if _, err := tx.Exec(ctx, "UPDATE deployments SET created_by = $2 WHERE job_id = $1 AND state = 'waiting'", jobID, p.UserID()); err != nil {
				return err
			}
			if env.Approvals > 0 {
				next, waiting = JobWaiting, "approval"
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = $2, waiting_for = $3, triggered_by = $4,
			queued_at = CASE WHEN $2 = 'pending' THEN now() ELSE queued_at END WHERE id = $1`, jobID, next, waiting, p.UserID()); err != nil {
			return err
		}
		if next == JobPending {
			if err := jobsChanged(ctx, tx); err != nil {
				return err
			}
		}
		if waiting == "approval" {
			j.TriggeredBy = p.UserID()
			if err := s.notifyApprovers(ctx, tx, run, j); err != nil {
				return err
			}
		}
		if run.State == RunSucceeded || run.State == RunFailed {
			if _, err := tx.Exec(ctx, "UPDATE runs SET state = 'running', finished_at = NULL WHERE id = $1", run.ID); err != nil {
				return err
			}
		}
		return s.advance(ctx, tx, run.ID)
	})
	return err
}

// Redeploy runs the job of a finished deployment again: the same commit, its artifacts and its environment, with
// the environment's protections applied to the caller.
func (s *Service) Redeploy(ctx context.Context, p *identity.Principal, deploymentID int64) error {
	var repoID, jobID int64
	var state string
	err := s.pool.QueryRow(ctx, `SELECT e.repository_id, d.job_id, d.state FROM deployments d
		JOIN environments e ON e.id = d.environment_id WHERE d.id = $1`, deploymentID).Scan(&repoID, &jobID, &state)
	if db.IsNoRows(err) {
		return apperr.NotFound("deployment.not-found", "The deployment does not exist.")
	}
	if err != nil {
		return err
	}
	// Only who sees the deployment learns its state; deploying again is running a job.
	l, err := s.repos.Level(ctx, p, repoID)
	if err != nil || l < LevelRead {
		return apperr.NotFound("deployment.not-found", "The deployment does not exist.")
	}
	if l < LevelWrite {
		return apperr.Forbidden("repository.write-required", "You may not run builds of this repository.")
	}
	if state == "waiting" || state == "running" {
		return apperr.Conflict("deployment.active", "The deployment has not finished.")
	}
	return s.RetryJob(ctx, p, jobID)
}

// StartedRun identifies a run that was started.
type StartedRun struct {
	ID     int64 `json:"id"`
	Number int   `json:"number"`
}

func (s *Service) startedRuns(ctx context.Context, ids []int64) ([]StartedRun, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, number FROM runs WHERE id = ANY($1) ORDER BY id", ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StartedRun])
	return nonNil(list), err
}
