// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
)

// Pipelines that run after other pipelines (on.pipeline). When a run succeeds or fails, the pipelines that follow
// it start: those of its repository at its commit (for the same person, with its artifacts), and those of other
// repositories of its code project at their default branch (their default branch's file says that they follow
// it). The code project is the boundary: whoever may read a code project may read all its repositories, so no
// pipeline learns of runs its readers could not see. Each pipeline follows a run once; a chain of runs is at
// most maxChain long and never runs a pipeline twice.

// maxChain is how many runs may start each other in a row.
const maxChain = 10

// FollowArgs starts the pipelines that follow a finished run.
type FollowArgs struct {
	RunID int64 `json:"run_id"`
}

// Kind implements river.JobArgs.
func (FollowArgs) Kind() string { return "builds.follow" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (FollowArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueBuilds} }

type followWorker struct {
	river.WorkerDefaults[FollowArgs]
	s *Service
}

func (w *followWorker) Work(ctx context.Context, job *river.Job[FollowArgs]) error {
	return w.s.followRun(ctx, job.Args.RunID)
}

// subscribeFollowers starts the following pipelines when runs end.
func (s *Service) subscribeFollowers(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &followWorker{s: s})
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(RunEvent); ok {
			return FollowArgs{RunID: e.RunID}, nil
		}
		return nil, nil
	}, "run."+RunSucceeded, "run."+RunFailed)
}

// followRun starts the pipelines that follow a run that ended.
func (s *Service) followRun(ctx context.Context, runID int64) error {
	run, err := s.runByID(ctx, s.pool, runID, false)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	result := map[string]string{RunSucceeded: spec.ResultSuccess, RunFailed: spec.ResultFailure}[run.State]
	if result == "" {
		return nil // running again (a job was retried), or cancelled
	}
	if run.ChainDepth+1 >= maxChain {
		s.log.Warn("pipelines: a chain of runs ends at its limit", "run", run.ID, "limit", maxChain)
		return nil
	}
	repo, err := s.repos.Repo(ctx, run.RepoID)
	if err != nil {
		return err
	}
	up := spec.Upstream{Repository: repo.ProjectKey + "/" + repo.Slug, Pipeline: run.Pipeline, Result: result, Branch: run.Branch, Tag: run.Tag}

	// The pipelines of the same repository, at the run's commit and for the same person.
	same := up
	same.Same = true
	in := TriggerInput{RepoID: run.RepoID, Commit: run.Commit, ActorID: run.TriggeredBy, UpstreamRunID: run.ID,
		ChainDepth: run.ChainDepth + 1, follow: true,
		Event: spec.Event{Kind: spec.EventPipeline, Branch: run.Branch, Tag: run.Tag, Upstream: &same}}
	if _, err := s.Trigger(ctx, in); err != nil {
		return err
	}

	// The pipelines of other repositories of the code project, at their default branch.
	rows, err := s.pool.Query(ctx, `SELECT repository_id FROM build_followers WHERE upstream_repository_id = $1 AND upstream_pipeline = $2
		AND repository_id <> $1 ORDER BY repository_id`, run.RepoID, run.Pipeline)
	if err != nil {
		return err
	}
	followers, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range followers {
		other, err := s.repos.Repo(ctx, id)
		if err != nil || other.ProjectID != repo.ProjectID {
			continue // gone, or moved to another code project
		}
		head, err := s.repos.Resolve(ctx, id, "refs/heads/"+other.DefaultBranch)
		if err != nil {
			continue // an empty repository
		}
		in := TriggerInput{RepoID: id, Commit: head, UpstreamRunID: run.ID, ChainDepth: run.ChainDepth + 1, follow: true,
			Event: spec.Event{Kind: spec.EventPipeline, Branch: other.DefaultBranch, Upstream: &up}}
		if _, err := s.Trigger(ctx, in); err != nil {
			s.log.Warn("pipelines: a following pipeline did not start", "run", run.ID, "repository", id, "error", err)
		}
	}
	return nil
}

// mayFollow reports whether a pipeline may follow the run of a pipeline event: once per run, and never twice in a
// chain of runs.
func (s *Service) mayFollow(ctx context.Context, tx pgx.Tx, in TriggerInput, pipeline string) (bool, error) {
	var started, loop bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
			SELECT id, repository_id, pipeline, upstream_run_id, 1 AS n FROM runs WHERE id = $1
			UNION ALL
			SELECT r.id, r.repository_id, r.pipeline, r.upstream_run_id, c.n + 1 FROM runs r JOIN chain c ON r.id = c.upstream_run_id
			WHERE c.n <= $4)
		SELECT EXISTS (SELECT 1 FROM runs WHERE upstream_run_id = $1 AND repository_id = $2 AND pipeline = $3),
			EXISTS (SELECT 1 FROM chain WHERE repository_id = $2 AND pipeline = $3)`,
		in.UpstreamRunID, in.RepoID, pipeline, maxChain).Scan(&started, &loop)
	if err != nil {
		return false, err
	}
	if loop {
		s.log.Info("pipelines: a pipeline does not follow its own run", "run", in.UpstreamRunID, "repository", in.RepoID,
			"pipeline", pipeline)
	}
	return !started && !loop, nil
}

// refreshFollowers records which pipelines of other repositories of the code project the default branch's
// pipelines follow.
func (s *Service) refreshFollowers(ctx context.Context, tx pgx.Tx, repo *Repo, f *spec.File) error {
	keep := []string{}
	for _, p := range f.Pipelines {
		for _, t := range p.On.Pipeline {
			if t.Repository == "" {
				continue
			}
			key, slug, _ := strings.Cut(t.Repository, "/")
			up, err := s.repos.RepoByPath(ctx, key, slug)
			if err != nil || up.ID == repo.ID || up.ProjectID != repo.ProjectID {
				continue
			}
			keep = append(keep, strconv.FormatInt(up.ID, 10)+"\n"+t.Pipeline)
			if _, err := tx.Exec(ctx, `INSERT INTO build_followers (upstream_repository_id, upstream_pipeline, repository_id) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`, up.ID, t.Pipeline, repo.ID); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM build_followers WHERE repository_id = $1
		AND NOT upstream_repository_id::text || E'\n' || upstream_pipeline = ANY($2)`, repo.ID, keep)
	return err
}

// upstreamRun is the run whose end started another one, with its repository.
type upstreamRun struct {
	run  *runRow
	repo *Repo
}

// upstreamOf returns the run that a run followed, if it still exists.
func (s *Service) upstreamOf(ctx context.Context, q db.Querier, run *runRow) (*upstreamRun, error) {
	if run.Upstream == 0 {
		return nil, nil
	}
	u, err := s.runByID(ctx, q, run.Upstream, false)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	repo, err := s.repos.Repo(ctx, u.RepoID)
	if err != nil {
		return nil, err
	}
	return &upstreamRun{run: u, repo: repo}, nil
}

// variables describe the run that a job's run followed.
func (u *upstreamRun) variables(base string) map[string]string {
	return map[string]string{
		"KOGGE_UPSTREAM_REPOSITORY": u.repo.ProjectKey + "/" + u.repo.Slug,
		"KOGGE_UPSTREAM_PIPELINE":   u.run.Pipeline,
		"KOGGE_UPSTREAM_RUN_NUMBER": strconv.Itoa(u.run.Number),
		"KOGGE_UPSTREAM_RUN_URL":    base + RunPath(u.repo.ProjectKey, u.repo.Slug, u.run.Number),
		"KOGGE_UPSTREAM_COMMIT":     u.run.Commit,
		"KOGGE_UPSTREAM_BRANCH":     shellSafe(u.run.Branch),
		"KOGGE_UPSTREAM_TAG":        shellSafe(u.run.Tag),
		"KOGGE_UPSTREAM_RESULT":     map[string]string{RunSucceeded: spec.ResultSuccess, RunFailed: spec.ResultFailure}[u.run.State],
	}
}

// runLinks returns the run that a run followed and the runs that followed it, of the repositories that the
// principal may read.
func (s *Service) runLinks(ctx context.Context, p *identity.Principal, run *runRow) (*RunLink, []RunLink, error) {
	repos := map[int64]*Repo{} // nil: not readable
	link := func(r *runRow) *RunLink {
		repo, ok := repos[r.RepoID]
		if !ok {
			if level, err := s.repos.Level(ctx, p, r.RepoID); err == nil && level >= LevelRead {
				repo, _ = s.repos.Repo(ctx, r.RepoID)
			}
			repos[r.RepoID] = repo
		}
		if repo == nil {
			return nil
		}
		return &RunLink{Repository: RunRepo{ProjectKey: repo.ProjectKey, Slug: repo.Slug, Name: repo.Name}, Number: r.Number,
			Pipeline: r.Pipeline, Name: r.Name, State: r.State}
	}
	var up *RunLink
	if run.Upstream != 0 {
		u, err := s.runByID(ctx, s.pool, run.Upstream, false)
		if err != nil && !db.IsNoRows(err) {
			return nil, nil, err
		}
		if err == nil {
			up = link(u)
		}
	}
	rows, err := s.pool.Query(ctx, "SELECT "+runColumns+" FROM runs WHERE upstream_run_id = $1 ORDER BY id", run.ID)
	if err != nil {
		return nil, nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, nil, err
	}
	down := []RunLink{}
	for _, r := range list {
		if l := link(r); l != nil {
			down = append(down, *l)
		}
	}
	return up, down, nil
}

// shellSafe returns a branch or tag name unless it has characters that shells treat specially. Git allows them in
// names, and whoever may push to the repository of the run followed chooses the names, while the run that follows
// may be one of another repository with protected values: a careless script must not run what a name says.
func shellSafe(name string) string {
	if strings.ContainsAny(name, "$`;&|<>()'\"!{}#\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return ""
	}
	return name
}
