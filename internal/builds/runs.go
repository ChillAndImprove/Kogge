// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Run states.
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunError     = "error"
)

// Job states.
const (
	JobCreated   = "created"
	JobPending   = "pending"
	JobWaiting   = "waiting"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
	JobSkipped   = "skipped"
)

func jobFinal(state string) bool {
	return state == JobSucceeded || state == JobFailed || state == JobCancelled || state == JobSkipped
}

// jobDef is what a job row stores: the job of the file with the variables of the file and the pipeline.
type jobDef struct {
	Job       spec.Job          `json:"job"`
	Variables map[string]string `json:"variables"`
}

// TriggerInput starts the pipelines of a repository.
type TriggerInput struct {
	RepoID        int64
	Event         spec.Event
	Commit        string
	ActorID       int64
	PullRequestID int64
	PullRequestNo int               // number of the pull request in its repository
	Variables     map[string]string // manual runs
	// ChangesBase is the commit the changes of a push or pull request count from, for paths filters; without it
	// the changes are unknown and paths filters let every change through.
	ChangesBase string
	// TriggerName is the description of the pipeline trigger that started an API run.
	TriggerName string
	// UpstreamRunID is the run whose end started a pipeline event; ChainDepth counts the runs that started each
	// other before it.
	UpstreamRunID int64
	ChainDepth    int
	projectID     int64
	only          string // re-runs: only this pipeline
	follow        bool   // a run's end starts the pipelines that follow it, each once
	// pins are the commits at which a run read the files of other repositories that its pipeline file includes: a
	// run again reads them at the same commits.
	pins map[string]string
}

// Trigger reads the pipeline file at the commit (with the files it includes) and creates a run for every pipeline
// the event starts. Without a pipeline file nothing happens; an invalid file produces a run in state error.
func (s *Service) Trigger(ctx context.Context, in TriggerInput) ([]int64, error) {
	if s.repos == nil {
		return nil, errors.New("builds: no repositories")
	}
	repo, err := s.repos.Repo(ctx, in.RepoID)
	if err != nil {
		return nil, err
	}
	in.projectID = repo.ProjectID
	data, ok, err := s.repos.ReadFile(ctx, in.RepoID, in.Commit, spec.Path)
	if err != nil {
		return nil, err
	}
	if !ok {
		if in.Event.Kind == spec.EventManual || in.Event.Kind == spec.EventAPI {
			return nil, apperr.Invalidf("run.no-pipelines", "The commit has no %s.", spec.Path)
		}
		return nil, nil
	}
	message := s.repos.CommitMessage(ctx, in.RepoID, in.Commit)
	subject, _, _ := strings.Cut(message, "\n")
	reader := &includeReader{s: s, repo: repo, commit: in.Commit, person: in.ActorID, pins: in.pins}
	if in.Event.Tag == "" {
		reader.branch = in.Event.Branch
	}
	f, perr := spec.Load(ctx, data, reader)
	var mistakes spec.Errors
	if perr != nil && !errors.As(perr, &mistakes) {
		return nil, perr // reading failed: the job that triggers runs tries again
	}
	if perr != nil && in.Event.Kind == spec.EventPipeline {
		s.log.Info("pipelines: a following pipeline does not start: the pipeline file is not valid", "repository", in.RepoID,
			"commit", in.Commit, "error", perr)
		return nil, nil // the push of the file reported it
	}
	if perr == nil && in.ChangesBase != "" && f.UsesPaths() {
		files, known, err := s.repos.ChangedFiles(ctx, in.RepoID, in.ChangesBase, in.Commit)
		if err != nil {
			// Not knowing what changed starts the pipelines rather than leaving a change unbuilt.
			s.log.Warn("pipelines: the changed files are unknown", "repository", in.RepoID, "commit", in.Commit, "error", err)
		} else if known {
			in.Event.Changes, in.Event.ChangesKnown = files, true
		}
	}
	var ids []int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7002, $1::int)", in.RepoID); err != nil {
			return err
		}
		if perr != nil {
			if done, err := s.builtForPR(ctx, tx, in, "pipelines"); err != nil || done {
				return err
			}
			id, err := s.insertRun(ctx, tx, in, newRun{pipeline: "pipelines", name: spec.Path, subject: subject, state: RunError, message: perr.Error()})
			if err != nil {
				return err
			}
			ids = append(ids, id)
			return s.setStatus(ctx, tx, in.RepoID, in.Commit, "kogge/pipelines", "error", "The pipeline file is not valid.", id)
		}
		for _, p := range f.Triggered(in.Event) {
			if in.only != "" && p.ID != in.only {
				continue
			}
			if done, err := s.builtForPR(ctx, tx, in, p.ID); err != nil {
				return err
			} else if done {
				continue
			}
			if in.follow {
				if ok, err := s.mayFollow(ctx, tx, in, p.ID); err != nil {
					return err
				} else if !ok {
					continue
				}
			}
			id, err := s.createRun(ctx, tx, in, f, p, subject, message)
			if err != nil {
				return err
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
		if in.only == "" {
			for _, p := range f.SkippedByPaths(in.Event) {
				if err := s.skippedStatus(ctx, tx, in.RepoID, in.Commit, p); err != nil {
					return err
				}
			}
		}
		if in.Event.Kind == spec.EventPush && in.Event.Branch != "" {
			if in.Event.Branch == repo.DefaultBranch {
				if err := s.refreshFollowers(ctx, tx, repo, f); err != nil {
					return err
				}
			}
			return s.refreshSchedules(ctx, tx, in.RepoID, in.Event.Branch, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if (in.Event.Kind == spec.EventManual || in.Event.Kind == spec.EventAPI) && len(ids) == 0 && perr == nil {
		return nil, apperr.Invalid("run.no-pipelines", "No pipeline can be started this way at this commit.")
	}
	return ids, nil
}

// builtForPR reports whether a pull request event already ran the pipeline at the commit (title edits and moves of
// the target branch announce the pull request again).
func (s *Service) builtForPR(ctx context.Context, tx pgx.Tx, in TriggerInput, pipeline string) (bool, error) {
	if in.Event.Kind != spec.EventPullRequest || in.PullRequestID == 0 || in.only != "" {
		return false, nil
	}
	var done bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE repository_id = $1 AND pull_request_id = $2 AND commit_sha = $3 AND pipeline = $4
		AND event = 'pull-request')`, in.RepoID, in.PullRequestID, in.Commit, pipeline).Scan(&done)
	return done, err
}

// newRun is what a new run records besides its trigger.
type newRun struct {
	pipeline, name, subject string
	state, message          string
	file                    string          // the included file that defines the pipeline
	includes                []spec.Included // the files the pipeline file included
}

func (s *Service) insertRun(ctx context.Context, tx pgx.Tx, in TriggerInput, r newRun) (int64, error) {
	pipeline, name, subject, state, message := r.pipeline, r.name, r.subject, r.state, r.message
	var number int
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(number), 0) + 1 FROM runs WHERE repository_id = $1", in.RepoID).Scan(&number); err != nil {
		return 0, err
	}
	vars, _ := json.Marshal(nonNilMap(in.Variables))
	includes, _ := json.Marshal(nonNil(r.includes))
	var branch, tag, target any
	if in.Event.Branch != "" {
		branch = in.Event.Branch
	}
	if in.Event.Tag != "" {
		tag = in.Event.Tag
	}
	if in.Event.TargetBranch != "" {
		target = in.Event.TargetBranch
	}
	var finished any
	if state == RunError {
		finished = now()
	}
	// Protected agents take jobs of protected refs only (checked again when they claim one).
	protection := ProtectionNone
	if ref := (&runRow{Branch: in.Event.Branch, Tag: in.Event.Tag}).ref(); ref != "refs/heads/" {
		protection = s.repos.Protection(ctx, in.RepoID, ref)
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO runs (repository_id, project_id, number, pipeline, pipeline_name, event, branch, tag, target_branch,
		commit_sha, commit_subject, pull_request_id, pull_request_number, triggered_by, variables, state, error, finished_at, ref_protection,
		trigger_name, upstream_run_id, chain_depth, pipeline_file, includes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		$15, $16, $17, $18, $19, $20, $21, $22, $23, $24) RETURNING id`, in.RepoID, in.projectID, number, pipeline, name, in.Event.Kind, branch, tag,
		target, in.Commit, subject, nullID(in.PullRequestID), nullID(int64(in.PullRequestNo)), nullID(in.ActorID), vars, state, message, finished,
		int(protection), nullString(in.TriggerName), nullID(in.UpstreamRunID), in.ChainDepth, r.file, includes).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, s.publish(ctx, tx, RunEvent{Name: "run.created", RunID: id, RepoID: in.RepoID, State: state})
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *Service) createRun(ctx context.Context, tx pgx.Tx, in TriggerInput, f *spec.File, p *spec.Pipeline, subject, message string) (int64, error) {
	form := map[string]spec.ManualVariable{}
	if p.On.Manual != nil {
		form = p.On.Manual.Variables
	}
	manual := in.Event.Kind == spec.EventManual || in.Event.Kind == spec.EventAPI
	vars := in.Variables
	if manual {
		// The values of the run form, with its defaults for missing values; they decide conditions, too.
		vars = map[string]string{}
		for name, mv := range form {
			switch v, given := in.Variables[name]; {
			case v != "":
				vars[name] = v
			case mv.Default != "":
				vars[name] = mv.Default
			case given:
				vars[name] = "" // an empty value given on purpose overrides what the file says
			}
		}
	}
	planned := p.Plan(f, in.Event, message, vars)
	if len(planned) == 0 {
		return 0, nil
	}
	if manual {
		// The run form: only its variables (they override configured values, also those of environments), and values
		// from the options only.
		for k := range in.Variables {
			if _, ok := form[k]; !ok {
				return 0, apperr.Invalidf("run.variable", "The pipeline %s has no variable %s in its run form.", p.Name, k)
			}
		}
		for name, mv := range form {
			if v := in.Variables[name]; v != "" && len(mv.Options) > 0 && !slices.Contains(mv.Options, v) {
				return 0, apperr.Invalidf("run.variable", "%s must be one of %s.", name, strings.Join(mv.Options, ", "))
			}
		}
		in.Variables = vars
	}
	id, err := s.insertRun(ctx, tx, in, newRun{pipeline: p.ID, name: p.Name, subject: subject, state: RunQueued, file: p.File, includes: f.Includes})
	if err != nil {
		return 0, err
	}
	if err := s.autoCancel(ctx, tx, in, p.ID, id); err != nil {
		return 0, err
	}
	fileVars := map[string]string{}
	for k, v := range f.Variables {
		fileVars[k] = v
	}
	for k, v := range p.Variables {
		fileVars[k] = v
	}
	for _, j := range planned {
		def, _ := json.Marshal(jobDef{Job: *j.Job, Variables: fileVars})
		var env any
		if j.Job.Environment != "" {
			env = j.Job.Environment
		}
		// Jobs that their conditions skip are shown with the reason; the others wait to be started.
		state, finished := JobCreated, any(nil)
		if j.Skip != "" {
			state, finished = JobSkipped, now()
		}
		req := j.Job.Requests()
		if _, err := tx.Exec(ctx, `INSERT INTO run_jobs (run_id, stage_index, stage_name, name, spec, labels, state, environment, skip_reason,
			skip_detail, millicpus, memory, finished_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, id, j.Stage, j.StageName,
			j.Job.Name, def, j.Job.Labels(), state, env, j.Skip, j.Detail, req.MilliCPUs, req.Memory, finished); err != nil {
			return 0, err
		}
	}
	if err := s.setStatus(ctx, tx, in.RepoID, in.Commit, "kogge/"+p.ID, "pending", p.Name, id); err != nil {
		return 0, err
	}
	return id, s.advance(ctx, tx, id)
}

type jobRow struct {
	ID          int64
	RunID       int64
	StageIndex  int
	StageName   string
	Name        string
	Def         jobDef
	State       string
	WaitingFor  string
	Attempt     int
	Environment string
	ExitCode    *int
	Failure     string
	LogSize     int64
	LogExpired  bool
	TriggeredBy int64 // who started this attempt (manual start, retry); 0: the run's trigger
	AgentID     *int64
	Created     time.Time
	Started     *time.Time
	Finished    *time.Time
	// SkipReason says why a skipped job did not run (spec.Skip…, Skip…; empty for jobs skipped before reasons were
	// kept); SkipDetail names the condition or the job.
	SkipReason string
	SkipDetail string
	// MilliCPUs and Memory are what one container of the job asks for.
	MilliCPUs int64
	Memory    int64
}

const jobColumns = `id, run_id, stage_index, stage_name, name, spec, state, waiting_for, attempt, coalesce(environment, ''), exit_code,
	failure, log_size, log_expired, coalesce(triggered_by, 0), agent_id, created_at, started_at, finished_at, skip_reason, skip_detail, millicpus,
	memory`

func scanJob(row pgx.Row) (*jobRow, error) {
	var j jobRow
	var def []byte
	if err := row.Scan(&j.ID, &j.RunID, &j.StageIndex, &j.StageName, &j.Name, &def, &j.State, &j.WaitingFor, &j.Attempt, &j.Environment,
		&j.ExitCode, &j.Failure, &j.LogSize, &j.LogExpired, &j.TriggeredBy, &j.AgentID, &j.Created, &j.Started, &j.Finished, &j.SkipReason,
		&j.SkipDetail, &j.MilliCPUs, &j.Memory); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(def, &j.Def)
	return &j, nil
}

func (s *Service) runJobs(ctx context.Context, q db.Querier, runID int64) ([]*jobRow, error) {
	rows, err := q.Query(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE run_id = $1 ORDER BY stage_index, id", runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*jobRow, error) { return scanJob(r) })
}

type runRow struct {
	ID          int64
	RepoID      int64
	Number      int
	Pipeline    string
	Name        string
	Event       string
	Branch      string
	Tag         string
	Target      string // target branch of pull request runs
	Commit      string
	Subject     string
	PRID        int64
	PRNumber    int
	TriggeredBy int64
	TriggerName string // the pipeline trigger that started an API run
	Variables   map[string]string
	State       string
	Error       string
	Created     time.Time
	Started     *time.Time
	Finished    *time.Time
	Upstream    int64 // the run whose end started this one
	ChainDepth  int
	// File is the included file that defines the pipeline; Includes are the files the pipeline file included.
	File     string
	Includes []spec.Included
}

const runColumns = `id, repository_id, number, pipeline, pipeline_name, event, coalesce(branch, ''), coalesce(tag, ''), coalesce(target_branch, ''), commit_sha,
	commit_subject, coalesce(pull_request_id, 0), coalesce(pull_request_number, 0), coalesce(triggered_by, 0), coalesce(trigger_name, ''), variables,
	state, error, created_at, started_at, finished_at, coalesce(upstream_run_id, 0), chain_depth, pipeline_file, includes`

func scanRun(row pgx.Row) (*runRow, error) {
	var r runRow
	var vars, includes []byte
	if err := row.Scan(&r.ID, &r.RepoID, &r.Number, &r.Pipeline, &r.Name, &r.Event, &r.Branch, &r.Tag, &r.Target, &r.Commit, &r.Subject, &r.PRID, &r.PRNumber,
		&r.TriggeredBy, &r.TriggerName, &vars, &r.State, &r.Error, &r.Created, &r.Started, &r.Finished, &r.Upstream, &r.ChainDepth, &r.File,
		&includes); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(vars, &r.Variables)
	_ = json.Unmarshal(includes, &r.Includes)
	return &r, nil
}

func (s *Service) runByID(ctx context.Context, q db.Querier, id int64, forUpdate bool) (*runRow, error) {
	sql := "SELECT " + runColumns + " FROM runs WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	r, err := scanRun(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("run.not-found", "The run does not exist or you may not see it.")
	}
	return r, err
}

func (r *runRow) ref() string {
	if r.Tag != "" {
		return "refs/tags/" + r.Tag
	}
	return "refs/heads/" + r.Branch
}

// activate makes a created job pending, or waiting for a manual start, a deployer or approvals.
func (s *Service) activate(ctx context.Context, tx pgx.Tx, run *runRow, j *jobRow) error {
	if j.Def.Job.Manual {
		j.State, j.WaitingFor = JobWaiting, "manual"
	} else {
		j.State, j.WaitingFor = JobPending, ""
	}
	if j.Environment != "" {
		waiting, err := s.prepareDeployment(ctx, tx, run, j)
		if err != nil {
			return err
		}
		if waiting != "" && j.State != JobFailed {
			if j.WaitingFor == "" {
				j.WaitingFor = waiting
			}
			j.State = JobWaiting
		}
	}
	if j.State == JobFailed {
		return s.setJobState(ctx, tx, j, JobFailed, j.Failure)
	}
	if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = $2, waiting_for = $3,
		queued_at = CASE WHEN $2 = 'pending' THEN now() ELSE queued_at END WHERE id = $1`, j.ID, j.State, j.WaitingFor); err != nil {
		return err
	}
	if j.State == JobPending {
		if err := jobsChanged(ctx, tx); err != nil {
			return err
		}
	}
	if j.State == JobWaiting && j.WaitingFor == "approval" {
		return s.notifyApprovers(ctx, tx, run, j)
	}
	return nil
}

func (s *Service) setJobState(ctx context.Context, tx pgx.Tx, j *jobRow, state, failure string) error {
	j.State = state
	var finished any
	if jobFinal(state) {
		finished = now()
	}
	if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = $2, failure = CASE WHEN $3 = '' THEN failure ELSE $3 END, finished_at = coalesce($4, finished_at),
		token_hash = CASE WHEN $5 THEN NULL ELSE token_hash END WHERE id = $1`, j.ID, state, failure, finished, jobFinal(state)); err != nil {
		return err
	}
	// The streams of the job's log end with it, at whichever node they are.
	if err := jobChanged(ctx, tx, j.ID, jobFinal(state)); err != nil {
		return err
	}
	if j.Environment != "" && jobFinal(state) {
		dstate := map[string]string{JobSucceeded: "succeeded", JobFailed: "failed", JobCancelled: "cancelled", JobSkipped: "cancelled"}[state]
		rows, err := tx.Query(ctx, `UPDATE deployments d SET state = $2, finished_at = now() FROM environments e
			WHERE d.job_id = $1 AND d.state NOT IN ('succeeded', 'failed', 'cancelled') AND e.id = d.environment_id
			RETURNING d.id, d.run_id, e.repository_id, e.name, d.commit_sha`, j.ID, dstate)
		if err != nil {
			return err
		}
		evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeploymentEvent, error) {
			e := DeploymentEvent{Name: "deployment." + dstate}
			err := r.Scan(&e.DeploymentID, &e.RunID, &e.RepoID, &e.Environment, &e.Commit)
			return e, err
		})
		if err != nil {
			return err
		}
		for _, e := range evs {
			if err := s.publish(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// finishJob records the result of a job (from its agent, or when its agent was lost) and advances the run.
func (s *Service) finishJob(ctx context.Context, tx pgx.Tx, jobID int64, state string, exitCode *int, failure string) error {
	j, err := scanJob(tx.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1 FOR UPDATE", jobID))
	if err != nil {
		return err
	}
	if j.State != JobRunning {
		return nil
	}
	// Its agent takes the next job, and other agents the jobs it held back (a limit of the repository, a
	// deployment to its environment).
	if err := jobsChanged(ctx, tx); err != nil {
		return err
	}
	var cancel bool
	if err := tx.QueryRow(ctx, "SELECT cancel_requested FROM run_jobs WHERE id = $1", jobID).Scan(&cancel); err != nil {
		return err
	}
	if cancel {
		state = JobCancelled
	}
	if _, err := tx.Exec(ctx, "UPDATE run_jobs SET exit_code = $2 WHERE id = $1", jobID, exitCode); err != nil {
		return err
	}
	if state == JobFailed && j.Attempt <= j.Def.Job.Retry {
		// Retry: the same job again, with its log continued and a new test report.
		if _, err := tx.Exec(ctx, `UPDATE run_jobs SET state = 'pending', attempt = attempt + 1, agent_id = NULL, token_hash = NULL, lease_until = NULL,
			failure = '', queued_at = now() WHERE id = $1`, jobID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM test_reports WHERE job_id = $1", jobID); err != nil {
			return err
		}
		// The next attempt gets its secrets anew.
		if err := jobChanged(ctx, tx, jobID, true); err != nil {
			return err
		}
		return s.advance(ctx, tx, j.RunID)
	}
	if err := s.setJobState(ctx, tx, j, state, failure); err != nil {
		return err
	}
	return s.advance(ctx, tx, j.RunID)
}

// setStatus records the status of a commit for a context.
func (s *Service) setStatus(ctx context.Context, q db.Querier, repoID int64, sha, context, state, description string, runID int64) error {
	url := ""
	if runID != 0 && s.opt.PublicURL != nil {
		if r, err := s.repos.Repo(ctx, repoID); err == nil {
			var number int
			_ = q.QueryRow(ctx, "SELECT number FROM runs WHERE id = $1", runID).Scan(&number)
			url = strings.TrimRight(s.opt.PublicURL.String(), "/") + RunPath(r.ProjectKey, r.Slug, number)
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO commit_statuses (repository_id, sha, context, state, description, url, run_id) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (repository_id, sha, context) DO UPDATE SET state = EXCLUDED.state, description = EXCLUDED.description, url = EXCLUDED.url,
		run_id = EXCLUDED.run_id, updated_at = now()`, repoID, sha, context, state, description, url, nullID(runID))
	return err
}

// skippedStatus records that the paths filters of a pipeline left a commit out — unless a run of the pipeline set
// its status already: another event (a push or a pull request) can start it at the same commit.
func (s *Service) skippedStatus(ctx context.Context, tx pgx.Tx, repoID int64, sha string, p *spec.Pipeline) error {
	_, err := tx.Exec(ctx, `INSERT INTO commit_statuses (repository_id, sha, context, state, description) VALUES ($1, $2, $3, 'skipped', $4)
		ON CONFLICT (repository_id, sha, context) DO NOTHING`, repoID, sha, "kogge/"+p.ID, p.Name)
	return err
}

// StatusCount counts the statuses of a commit (for merge checks).
type StatusCount struct {
	Successful, Skipped, Failed, Running int
}

// StatusCounts counts the statuses of a commit: those of Kogge's builds (contexts kogge/…) and, with external, those
// that other systems set through the API. Pending builds count as running; skipped ones are pipelines whose paths
// filters left the commit out.
func (s *Service) StatusCounts(ctx context.Context, repoID int64, sha string, external bool) (StatusCount, error) {
	var c StatusCount
	err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state = 'success'), count(*) FILTER (WHERE state = 'skipped'),
		count(*) FILTER (WHERE state IN ('failure', 'error')), count(*) FILTER (WHERE state IN ('pending', 'running')) FROM commit_statuses
		WHERE repository_id = $1 AND sha = $2 AND ($3 OR context LIKE 'kogge/%')`, repoID, sha, external).
		Scan(&c.Successful, &c.Skipped, &c.Failed, &c.Running)
	return c, err
}

// refreshSchedules records the schedules of a branch's pipelines.
func (s *Service) refreshSchedules(ctx context.Context, tx pgx.Tx, repoID int64, branch string, f *spec.File) error {
	// Schedules that stay keep their last run; new ones start with the next matching minute.
	keep := []string{}
	for _, p := range f.Pipelines {
		for _, sc := range p.On.Schedule {
			b := sc.Branch
			if b == "" {
				repo, err := s.repos.Repo(ctx, repoID)
				if err != nil {
					return err
				}
				b = repo.DefaultBranch
			}
			if b != branch {
				continue
			}
			keep = append(keep, p.ID+"\n"+sc.Expr)
			if _, err := tx.Exec(ctx, `INSERT INTO build_schedules (repository_id, pipeline, branch, cron, last_run_at) VALUES ($1, $2, $3, $4, now())
				ON CONFLICT DO NOTHING`, repoID, p.ID, b, sc.Expr); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM build_schedules WHERE repository_id = $1 AND branch = $2 AND NOT pipeline || E'\\n' || cron = ANY($3)",
		repoID, branch, keep)
	return err
}

// RepositoryMoved gives the runs of a repository its new code project, in the transaction of the move: runs are
// listed, and agents of a project claim jobs, by the run's project.
func (s *Service) RepositoryMoved(ctx context.Context, tx pgx.Tx, repoID, projectID int64) error {
	_, err := tx.Exec(ctx, "UPDATE runs SET project_id = $2 WHERE repository_id = $1 AND project_id <> $2", repoID, projectID)
	return err
}
