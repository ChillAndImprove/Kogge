// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
)

// The jobs of a run form a graph: a job with needs waits for the jobs it needs and starts as soon
// as they succeeded (or may fail); a job without needs waits for all jobs of the earlier stages, as before. Jobs
// that nobody started by hand do not hold up later stages — but a job that needs one waits for it. A job whose
// needed job failed, was cancelled or was skipped is skipped, and so is a job without needs after a failure in an
// earlier stage; jobs that do not depend on the failure go on. Advancing a run looks at every job each time, in an
// order where each job comes after the jobs it waits for, so one pass settles the run.

// Reasons why jobs were skipped while their run ran (those of spec.Plan are decided when it is created).
const (
	SkipFailed      = "failed"       // a job it waits for failed; detail: that job (stage/job)
	SkipCancelled   = "cancelled"    // a job it waits for was cancelled
	SkipNeedSkipped = "need-skipped" // a job it needs was skipped because something failed
)

// skippedByConditions reports whether a skip reason says that the job does not run for the event (decided when the
// run was created), rather than because something failed.
func skippedByConditions(reason string) bool {
	return reason == spec.SkipCondition || reason == spec.SkipStageCondition || reason == spec.SkipNeedAbsent
}

// graph holds the jobs of a run with what they wait for.
type graph struct {
	stages [][]*jobRow // the jobs by stage, each stage in an order where jobs follow those of the stage they need
	byKey  map[string]*jobRow
	// cyclic are jobs whose needs could not be ordered (pipeline files with cycles are refused; this is a guard).
	cyclic map[int64]bool
}

func jobKey(j *jobRow) string { return spec.Key(j.StageName, j.Name) }

func newGraph(jobs []*jobRow) *graph {
	g := &graph{byKey: map[string]*jobRow{}, cyclic: map[int64]bool{}}
	var stage []*jobRow
	flush := func() {
		if len(stage) > 0 {
			g.stages = append(g.stages, g.order(stage))
			stage = nil
		}
	}
	for _, j := range jobs { // ordered by stage
		g.byKey[jobKey(j)] = j
	}
	for i, j := range jobs {
		if i > 0 && j.StageIndex != jobs[i-1].StageIndex {
			flush()
		}
		stage = append(stage, j)
	}
	flush()
	return g
}

// order sorts the jobs of a stage so that each comes after the jobs of the stage that it needs.
func (g *graph) order(stage []*jobRow) []*jobRow {
	in := map[int64]bool{}
	for _, j := range stage {
		in[j.ID] = true
	}
	var out []*jobRow
	done := map[int64]bool{}
	for len(out) < len(stage) {
		progress := false
		for _, j := range stage {
			if done[j.ID] {
				continue
			}
			ready := true
			for _, n := range j.Def.Job.Needs {
				if d := g.byKey[n.Job]; d != nil && in[d.ID] && !done[d.ID] {
					ready = false
				}
			}
			if ready {
				out, done[j.ID], progress = append(out, j), true, true
			}
		}
		if !progress {
			for _, j := range stage {
				if !done[j.ID] {
					g.cyclic[j.ID], done[j.ID] = true, true
					out = append(out, j)
				}
			}
		}
	}
	return out
}

// jobs returns all jobs in the graph's order.
func (g *graph) jobs() []*jobRow {
	var out []*jobRow
	for _, st := range g.stages {
		out = append(out, st...)
	}
	return out
}

// How far a job is from starting.
type readiness int

const (
	ready      readiness = iota
	blocked              // it waits for jobs that run without anyone starting them
	manualOnly           // it waits for a job that somebody has to start by hand
	doomed               // a job it waits for failed, was cancelled or was skipped: it is skipped
)

// earlierStages sums up the jobs of the stages before one, for the jobs without needs.
type earlierStages struct {
	failed, cancelled *jobRow // the first that failed (and may not) or was cancelled
	running           bool    // one has not finished and does not wait for a manual start
	manual            bool    // one waits for a job that somebody has to start by hand
}

func (e *earlierStages) add(stage []*jobRow, manual map[int64]bool) {
	for _, j := range stage {
		switch j.State {
		case JobFailed:
			if !j.Def.Job.AllowFailure && e.failed == nil {
				e.failed = j
			}
		case JobCancelled:
			if e.cancelled == nil {
				e.cancelled = j
			}
		case JobSucceeded, JobSkipped:
		case JobWaiting:
			// Jobs nobody started yet do not hold up later stages.
			if j.WaitingFor != "manual" {
				e.running = true
			}
		case JobCreated:
			if manual[j.ID] {
				e.manual = true
			} else {
				e.running = true
			}
		default:
			e.running = true
		}
	}
}

// readiness tells how far a job is from starting, and for a doomed job why.
func (g *graph) readiness(j *jobRow, before earlierStages, manual map[int64]bool) (readiness, string, string) {
	needs := j.Def.Job.Needs
	if needs == nil {
		switch {
		case before.failed != nil:
			return doomed, SkipFailed, jobKey(before.failed)
		case before.cancelled != nil:
			return doomed, SkipCancelled, jobKey(before.cancelled)
		case before.manual:
			return manualOnly, "", ""
		case before.running:
			return blocked, "", ""
		}
		return ready, "", ""
	}
	state := ready
	for _, n := range needs {
		d := g.byKey[n.Job]
		if d == nil || g.cyclic[d.ID] {
			if n.Optional {
				continue
			}
			return doomed, spec.SkipNeedAbsent, n.Job
		}
		switch d.State {
		case JobSucceeded:
		case JobFailed:
			if !d.Def.Job.AllowFailure {
				return doomed, SkipFailed, n.Job
			}
		case JobCancelled:
			return doomed, SkipCancelled, n.Job
		case JobSkipped:
			switch {
			case skippedByConditions(d.SkipReason) && n.Optional:
			case skippedByConditions(d.SkipReason):
				return doomed, spec.SkipNeedAbsent, n.Job
			default:
				return doomed, SkipNeedSkipped, n.Job
			}
		case JobWaiting:
			if d.WaitingFor == "manual" {
				state = manualOnly
			} else {
				state = max(state, blocked)
			}
		case JobCreated:
			if manual[d.ID] {
				state = manualOnly
			} else {
				state = max(state, blocked)
			}
		default:
			state = max(state, blocked)
		}
	}
	return state, "", ""
}

// waitsFor reports whether job d waits for job x.
func waitsFor(d, x *jobRow) bool {
	if d.Def.Job.Needs == nil {
		return x.StageIndex < d.StageIndex
	}
	k := jobKey(x)
	return slices.ContainsFunc(d.Def.Job.Needs, func(n spec.Need) bool { return n.Job == k })
}

// dependents returns the jobs that wait for a job, also indirectly.
func (g *graph) dependents(x *jobRow) []*jobRow {
	all := g.jobs()
	seen := map[int64]bool{x.ID: true}
	var out []*jobRow
	var visit func(x *jobRow)
	visit = func(x *jobRow) {
		for _, d := range all {
			if !seen[d.ID] && waitsFor(d, x) {
				seen[d.ID] = true
				out = append(out, d)
				visit(d)
			}
		}
	}
	visit(x)
	return out
}

// advance moves a run forward: it starts the jobs whose needs (or earlier stages) are done, skips those that cannot
// run any more, and derives the run's state from its jobs.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, runID int64) error {
	run, err := s.runByID(ctx, tx, runID, true)
	if err != nil {
		return err
	}
	if run.State == RunError {
		return nil
	}
	jobs, err := s.runJobs(ctx, tx, runID)
	if err != nil {
		return err
	}
	g := newGraph(jobs)
	cancelled := run.State == RunCancelled
	manual := map[int64]bool{} // jobs that wait for a job somebody has to start by hand
	var before earlierStages
	for _, stage := range g.stages {
		sum := before
		for _, j := range stage {
			if j.State != JobCreated && j.State != JobPending && j.State != JobWaiting {
				continue
			}
			if cancelled {
				if err := s.setJobState(ctx, tx, j, JobSkipped, ""); err != nil {
					return err
				}
				continue
			}
			switch st, reason, detail := g.readiness(j, sum, manual); st {
			case doomed:
				if err := s.skipJob(ctx, tx, j, reason, detail); err != nil {
					return err
				}
			case ready:
				if j.State == JobCreated {
					if err := s.activate(ctx, tx, run, j); err != nil {
						return err
					}
				}
			case manualOnly:
				if j.State == JobCreated {
					manual[j.ID] = true
				}
			}
		}
		before.add(stage, manual)
	}
	// The run's state.
	active, failed := false, false
	for _, j := range jobs {
		switch {
		case j.State == JobRunning, j.State == JobPending, j.State == JobWaiting && j.WaitingFor != "manual":
			active = true
		case j.State == JobCreated && !manual[j.ID] && !cancelled:
			active = true
		case j.State == JobFailed && !j.Def.Job.AllowFailure, j.State == JobCancelled:
			failed = true
		}
	}
	state := RunSucceeded
	switch {
	case cancelled:
		state = RunCancelled
	case active:
		state = RunRunning
		// Jobs that their conditions skipped when the run was created are no progress.
		if run.State == RunQueued && !slices.ContainsFunc(jobs, func(j *jobRow) bool {
			return j.State == JobRunning || jobFinal(j.State) && !skippedByConditions(j.SkipReason)
		}) {
			state = RunQueued
		}
	case failed:
		state = RunFailed
	}
	if state == run.State {
		return nil
	}
	var started, finished any
	if state != RunQueued && run.Started == nil {
		started = now()
	}
	if state == RunSucceeded || state == RunFailed || state == RunCancelled {
		finished = now()
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET state = $2, started_at = coalesce(started_at, $3), finished_at = $4 WHERE id = $1`,
		runID, state, started, finished); err != nil {
		return err
	}
	status := map[string]string{RunQueued: "pending", RunRunning: "running", RunSucceeded: "success", RunFailed: "failure",
		RunCancelled: "cancelled"}[state]
	if err := s.setStatus(ctx, tx, run.RepoID, run.Commit, "kogge/"+run.Pipeline, status, run.Name, run.ID); err != nil {
		return err
	}
	if err := s.notifyRunFinished(ctx, tx, run, state); err != nil {
		return err
	}
	return s.publish(ctx, tx, RunEvent{Name: "run." + state, RunID: runID, RepoID: run.RepoID, State: state})
}

// skipJob skips a job that cannot run any more, with the reason.
func (s *Service) skipJob(ctx context.Context, tx pgx.Tx, j *jobRow, reason, detail string) error {
	if err := s.setJobState(ctx, tx, j, JobSkipped, ""); err != nil {
		return err
	}
	j.SkipReason, j.SkipDetail = reason, detail
	_, err := tx.Exec(ctx, "UPDATE run_jobs SET skip_reason = $2, skip_detail = $3 WHERE id = $1", j.ID, reason, detail)
	return err
}
