// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// RunSummary is a run in lists.
type RunSummary struct {
	ID          int64             `json:"id"`
	Number      int               `json:"number"`
	Pipeline    string            `json:"pipeline"`
	Name        string            `json:"name"`
	Event       string            `json:"event" enum:"push,pull-request,schedule,manual,api,pipeline"`
	Branch      string            `json:"branch,omitempty"`
	Tag         string            `json:"tag,omitempty"`
	Commit      string            `json:"commit"`
	Subject     string            `json:"subject"`
	PullRequest int               `json:"pullRequest,omitempty" doc:"Number of the pull request"`
	TriggeredBy *identity.UserRef `json:"triggeredBy,omitempty"`
	Trigger     string            `json:"trigger,omitempty" doc:"The pipeline trigger that started the run (API runs)"`
	Repository  *RunRepo          `json:"repository,omitempty" doc:"In lists that span repositories"`
	After       *RunAfter         `json:"after,omitempty" doc:"Pipeline events: the run of the same repository that this run followed"`
	State       string            `json:"state" enum:"queued,running,succeeded,failed,cancelled,error"`
	Created     time.Time         `json:"created"`
	Started     *time.Time        `json:"started,omitempty"`
	Finished    *time.Time        `json:"finished,omitempty"`
}

// JobInfo is a job of a run.
type JobInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state" enum:"created,pending,waiting,running,succeeded,failed,cancelled,skipped"`
	WaitingFor  string `json:"waitingFor,omitempty" enum:"manual,deployer,approval,"`
	Attempt     int    `json:"attempt"`
	Environment string `json:"environment,omitempty"`
	// Matrix names the job whose matrix made this job, with the values it runs with.
	Matrix       string            `json:"matrix,omitempty"`
	MatrixValues map[string]string `json:"matrixValues,omitempty"`
	Manual       bool              `json:"manual"`
	AllowFail    bool              `json:"allowFailure"`
	ExitCode     *int              `json:"exitCode,omitempty"`
	Failure      string            `json:"failure,omitempty"`
	Agent        string            `json:"agent,omitempty"`
	Steps        []string          `json:"steps"`
	Artifacts    []Artifact        `json:"artifacts"`
	Report       *TestReport       `json:"report,omitempty"`
	Deployment   *Deployment       `json:"deployment,omitempty" doc:"The latest deployment of the job"`
	Started      *time.Time        `json:"started,omitempty"`
	Finished     *time.Time        `json:"finished,omitempty"`
	// Needs are the jobs a job waits for when the pipeline file names them (needs); a job without waits for the jobs
	// of the earlier stages.
	Needs     []int64       `json:"needs,omitempty" doc:"The jobs it needs (their ids)"`
	HasNeeds  bool          `json:"hasNeeds,omitempty" doc:"It waits for the jobs it needs only, not for the earlier stages (needs: [] waits for none)"`
	Skip      *JobSkip      `json:"skip,omitempty" doc:"Why a skipped job did not run"`
	Resources *JobResources `json:"resources,omitempty" doc:"What one container of the job asks for (container jobs)"`
}

// JobSkip says why a job was skipped.
type JobSkip struct {
	Reason string `json:"reason" enum:"condition,stage-condition,need-absent,failed,cancelled,need-skipped" doc:"condition, stage-condition: its condition (or its stage's) is false; need-absent: a job it needs does not run for the event; failed, cancelled: a job it waits for failed or was cancelled; need-skipped: a job it needs was skipped"`
	Detail string `json:"detail,omitempty" doc:"The condition, or the job (stage/job)"`
}

// JobResources are the CPUs and memory that one container of a job asks for.
type JobResources struct {
	MilliCPUs int64 `json:"milliCpus,omitempty" doc:"Thousandths of a CPU"`
	Memory    int64 `json:"memory,omitempty" doc:"Bytes"`
	NoAgent   bool  `json:"noAgent,omitempty" doc:"The job waits, and no enabled agent with its labels allows that much"`
}

// Artifact is a file set kept from a job.
type Artifact struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Size    int64      `json:"size"`
	Expires *time.Time `json:"expires,omitempty"`
}

// StageInfo is a stage with its jobs.
type StageInfo struct {
	Name string    `json:"name"`
	Jobs []JobInfo `json:"jobs"`
}

// RunDetail is a run with its stages and jobs.
type RunDetail struct {
	RunSummary
	Error     string            `json:"error,omitempty" doc:"Validation errors of the pipeline file"`
	Variables map[string]string `json:"variables"`
	Stages    []StageInfo       `json:"stages"`
	CanRun    bool              `json:"canRun" doc:"The caller may cancel, re-run and start jobs"`
	Upstream  *RunLink          `json:"upstream,omitempty" doc:"The run whose end started this one"`
	// Runs of other repositories are listed if the caller may read them.
	Downstream []RunLink `json:"downstream" doc:"The runs that this run's end started"`
	File       string    `json:"file,omitempty" doc:"The included file that defines the run's pipeline; empty: the pipeline file"`
	// Includes are the files that the pipeline file included, with the commits the run read them at.
	Includes []spec.Included `json:"includes"`
}

// RunLink names a run that a run followed or that followed it.
type RunLink struct {
	Repository RunRepo `json:"repository"`
	Number     int     `json:"number"`
	Pipeline   string  `json:"pipeline"`
	Name       string  `json:"name" doc:"The name of the pipeline"`
	State      string  `json:"state"`
}

func (s *Service) summaries(ctx context.Context, runs []*runRow) ([]RunSummary, error) {
	ids := []int64{}
	for _, r := range runs {
		ids = append(ids, r.TriggeredBy)
	}
	users, err := identity.GetUserRefs(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	// The runs that runs followed, where they are of the same repository (readable like the run).
	upstream := []int64{}
	for _, r := range runs {
		if r.Upstream != 0 {
			upstream = append(upstream, r.Upstream)
		}
	}
	type after struct {
		repo int64
		RunAfter
	}
	afters := map[int64]after{}
	if len(upstream) > 0 {
		rows, err := s.pool.Query(ctx, "SELECT id, repository_id, number, pipeline_name FROM runs WHERE id = ANY($1)", upstream)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var a after
			if err := rows.Scan(&id, &a.repo, &a.Number, &a.Name); err != nil {
				rows.Close()
				return nil, err
			}
			afters[id] = a
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]RunSummary, 0, len(runs))
	for _, r := range runs {
		rs := RunSummary{ID: r.ID, Number: r.Number, Pipeline: r.Pipeline, Name: r.Name, Event: r.Event, Branch: r.Branch, Tag: r.Tag, Commit: r.Commit,
			Subject: r.Subject, PullRequest: r.PRNumber, Trigger: r.TriggerName, State: r.State, Created: r.Created, Started: r.Started,
			Finished: r.Finished}
		if u, ok := users[r.TriggeredBy]; ok {
			rs.TriggeredBy = &u
		}
		if a, ok := afters[r.Upstream]; ok && a.repo == r.RepoID {
			rs.After = &a.RunAfter
		}
		out = append(out, rs)
	}
	return out, nil
}

// RunAfter names the run of the same repository that a run followed.
type RunAfter struct {
	Number int    `json:"number"`
	Name   string `json:"name" doc:"The name of the pipeline"`
}

// RunRepo names the repository of a run.
type RunRepo struct {
	ProjectKey string `json:"projectKey"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
}

// RecentFilter selects runs of all readable repositories.
type RecentFilter struct {
	State  string
	Mine   bool
	Limit  int
	Offset int
}

// RecentRuns lists the newest runs of the repositories the principal may read.
func (s *Service) RecentRuns(ctx context.Context, p *identity.Principal, f RecentFilter) ([]RunSummary, int, error) {
	all, projects, repos, err := s.repos.Readable(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	var args db.Args
	where := "TRUE"
	if !all {
		where = "(project_id = ANY(" + args.Add(projects) + ") OR repository_id = ANY(" + args.Add(repos) + "))"
	}
	if f.State != "" {
		where += " AND state = " + args.Add(f.State)
	}
	if f.Mine {
		where += " AND triggered_by = " + args.Add(p.UserID())
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+runColumns+" FROM runs WHERE "+where+" ORDER BY created_at DESC, id DESC LIMIT "+strconv.Itoa(pageLimit(f.Limit))+
		" OFFSET "+strconv.Itoa(max(f.Offset, 0)), args...)
	if err != nil {
		return nil, 0, err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, 0, err
	}
	list, err := s.summaries(ctx, runs)
	if err != nil {
		return nil, 0, err
	}
	names := map[int64]*RunRepo{}
	for i, r := range runs {
		if _, ok := names[r.RepoID]; !ok {
			names[r.RepoID] = nil
			if repo, err := s.repos.Repo(ctx, r.RepoID); err == nil {
				names[r.RepoID] = &RunRepo{ProjectKey: repo.ProjectKey, Slug: repo.Slug, Name: repo.Name}
			}
		}
		list[i].Repository = names[r.RepoID]
	}
	return list, total, nil
}

// RunFilter selects runs.
type RunFilter struct {
	Branch   string
	Pipeline string
	State    string
	Commit   string
	Limit    int
	Offset   int
}

// Runs lists the runs of a repository, newest first.
func (s *Service) Runs(ctx context.Context, p *identity.Principal, key, slug string, f RunFilter) ([]RunSummary, int, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, 0, err
	}
	var args db.Args
	where := "repository_id = " + args.Add(repo.ID)
	if f.Branch != "" {
		where += " AND branch = " + args.Add(f.Branch)
	}
	if f.Pipeline != "" {
		where += " AND pipeline = " + args.Add(f.Pipeline)
	}
	if f.State != "" {
		where += " AND state = " + args.Add(f.State)
	}
	if f.Commit != "" {
		where += " AND commit_sha = " + args.Add(f.Commit)
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+runColumns+" FROM runs WHERE "+where+" ORDER BY number DESC LIMIT "+strconv.Itoa(pageLimit(f.Limit))+" OFFSET "+
		strconv.Itoa(max(f.Offset, 0)), args...)
	if err != nil {
		return nil, 0, err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, 0, err
	}
	list, err := s.summaries(ctx, runs)
	return list, total, err
}

// Run returns a run with its stages and jobs.
func (s *Service) Run(ctx context.Context, p *identity.Principal, key, slug string, number int) (*RunDetail, error) {
	repo, level, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	run, err := scanRun(s.pool.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE repository_id = $1 AND number = $2", repo.ID, number))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("run.not-found", "Run %d does not exist.", number)
	}
	if err != nil {
		return nil, err
	}
	list, err := s.summaries(ctx, []*runRow{run})
	if err != nil {
		return nil, err
	}
	d := &RunDetail{RunSummary: list[0], Error: run.Error, Variables: nonNilMap(run.Variables), Stages: []StageInfo{}, CanRun: level >= LevelWrite,
		File: run.File, Includes: nonNil(run.Includes)}
	if d.Upstream, d.Downstream, err = s.runLinks(ctx, p, run); err != nil {
		return nil, err
	}
	jobs, err := s.runJobs(ctx, s.pool, run.ID)
	if err != nil {
		return nil, err
	}
	artifacts := map[int64][]Artifact{}
	rows, err := s.pool.Query(ctx, "SELECT job_id, id, name, size, expires_at FROM artifacts WHERE run_id = $1 ORDER BY id", run.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var jobID int64
		var a Artifact
		if err := rows.Scan(&jobID, &a.ID, &a.Name, &a.Size, &a.Expires); err != nil {
			rows.Close()
			return nil, err
		}
		artifacts[jobID] = append(artifacts[jobID], a)
	}
	rows.Close()
	reports := map[int64]*TestReport{}
	rows, err = s.pool.Query(ctx, `SELECT t.job_id, t.total, t.failed, t.errors, t.skipped, t.duration_ms, t.failures FROM test_reports t
		JOIN run_jobs j ON j.id = t.job_id WHERE j.run_id = $1`, run.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var jobID int64
		var rep TestReport
		var raw []byte
		if err := rows.Scan(&jobID, &rep.Total, &rep.Failed, &rep.Errors, &rep.Skipped, &rep.DurationMs, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(raw, &rep.Failures)
		rep.Failures = nonNil(rep.Failures)
		reports[jobID] = &rep
	}
	rows.Close()
	deployments := map[int64]int64{}
	rows, err = s.pool.Query(ctx, "SELECT job_id, id FROM deployments WHERE run_id = $1 ORDER BY id", run.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var jobID, id int64
		if err := rows.Scan(&jobID, &id); err != nil {
			rows.Close()
			return nil, err
		}
		deployments[jobID] = id
	}
	rows.Close()
	agents := map[int64]string{}
	rows, err = s.pool.Query(ctx, "SELECT a.id, a.name FROM agents a WHERE a.id IN (SELECT agent_id FROM run_jobs WHERE run_id = $1)", run.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		agents[id] = name
	}
	rows.Close()
	// Waiting jobs that agents with their labels could take, if one allowed the resources they ask for.
	noAgent := map[int64]bool{}
	rows, err = s.pool.Query(ctx, `SELECT j.id FROM run_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.run_id = $1 AND j.state = 'pending' AND (j.millicpus > 0 OR j.memory > 0) AND EXISTS (SELECT 1 FROM agents a
		  WHERE a.enabled AND a.approved_at IS NOT NULL AND j.labels <@ a.labels AND (a.project_id IS NULL OR a.project_id = r.project_id))
		AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.enabled AND a.approved_at IS NOT NULL AND j.labels <@ a.labels
		  AND (a.project_id IS NULL OR a.project_id = r.project_id) AND (a.max_millicpus = 0 OR a.max_millicpus >= j.millicpus)
		  AND (a.max_memory = 0 OR a.max_memory >= j.memory))`, run.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		noAgent[id] = true
	}
	rows.Close()
	ids := map[string]int64{}
	for _, j := range jobs {
		ids[jobKey(j)] = j.ID
	}
	stageIndex := -1
	for _, j := range jobs {
		if j.StageIndex != stageIndex {
			d.Stages = append(d.Stages, StageInfo{Name: j.StageName, Jobs: []JobInfo{}})
			stageIndex = j.StageIndex
		}
		ji := JobInfo{ID: j.ID, Name: j.Name, State: j.State, WaitingFor: j.WaitingFor, Attempt: j.Attempt, Environment: j.Environment,
			Matrix: j.Def.Job.Matrix, MatrixValues: j.Def.Job.MatrixValues, Manual: j.Def.Job.Manual, AllowFail: j.Def.Job.AllowFailure,
			ExitCode: j.ExitCode, Failure: j.Failure, Steps: []string{}, Artifacts: nonNil(artifacts[j.ID]), Report: reports[j.ID],
			Started: j.Started, Finished: j.Finished, HasNeeds: j.Def.Job.Needs != nil}
		for _, st := range j.Def.Job.Steps {
			ji.Steps = append(ji.Steps, st.Name)
		}
		for _, n := range j.Def.Job.Needs {
			if id, ok := ids[n.Job]; ok {
				ji.Needs = append(ji.Needs, id)
			}
		}
		if j.State == JobSkipped && j.SkipReason != "" {
			ji.Skip = &JobSkip{Reason: j.SkipReason, Detail: j.SkipDetail}
		}
		if j.MilliCPUs > 0 || j.Memory > 0 {
			ji.Resources = &JobResources{MilliCPUs: j.MilliCPUs, Memory: j.Memory, NoAgent: noAgent[j.ID]}
		}
		if j.AgentID != nil {
			ji.Agent = agents[*j.AgentID]
		}
		if id, ok := deployments[j.ID]; ok {
			if ji.Deployment, err = s.deployment(ctx, p, id); err != nil {
				return nil, err
			}
		}
		d.Stages[len(d.Stages)-1].Jobs = append(d.Stages[len(d.Stages)-1].Jobs, ji)
	}
	return d, nil
}

// LogChunk is a part of a job's log.
type LogChunk struct {
	Text    string `json:"text"`
	Offset  int64  `json:"offset" doc:"Where the text starts"`
	Size    int64  `json:"size" doc:"The log's size so far"`
	Next    int64  `json:"next" doc:"Offset of the next chunk"`
	Done    bool   `json:"done" doc:"The job has finished; the log is complete"`
	Expired bool   `json:"expired,omitempty" doc:"The log was deleted after its retention period"`
}

// jobForRead loads a job whose repository the principal may read.
func (s *Service) jobForRead(ctx context.Context, p *identity.Principal, jobID int64) (*jobRow, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1", jobID))
	if db.IsNoRows(err) {
		return nil, jobNotFound()
	}
	if err != nil {
		return nil, err
	}
	run, err := s.runByID(ctx, s.pool, j.RunID, false)
	if err != nil {
		return nil, notFoundAs(err, jobNotFound())
	}
	if _, err := s.repoOfRun(ctx, p, run.RepoID, LevelRead); err != nil {
		return nil, notFoundAs(err, jobNotFound())
	}
	return j, nil
}

// JobLog returns the log of a job from an offset (at most 1 MiB).
func (s *Service) JobLog(ctx context.Context, p *identity.Principal, jobID, offset int64) (*LogChunk, error) {
	j, err := s.jobForRead(ctx, p, jobID)
	if err != nil {
		return nil, err
	}
	c := &LogChunk{Offset: offset, Next: offset, Size: j.LogSize, Done: jobFinal(j.State), Expired: j.LogExpired}
	if offset < 0 || offset >= j.LogSize || j.LogExpired {
		return c, nil
	}
	text, err := s.readLog(jobID, offset, min(j.LogSize-offset, 1<<20), c.Done)
	if err != nil {
		return nil, err
	}
	c.Text, c.Next = string(text), offset+int64(len(text))
	return c, nil
}

// readLog reads up to n bytes of a log from an offset. Unless the end of a finished log is reached, it stops
// before an incomplete UTF-8 sequence, which the next read returns whole.
func (s *Service) readLog(jobID, offset, n int64, finished bool) ([]byte, error) {
	f, err := os.Open(s.logPath(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:read]
	if !finished || read == int(n) {
		buf = buf[:completeUTF8(buf)]
	}
	return buf, nil
}

// completeUTF8 returns the length of b without a trailing incomplete UTF-8 sequence.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}

// ArtifactFile returns an artifact for download.
func (s *Service) ArtifactFile(ctx context.Context, p *identity.Principal, id int64) (sha, name string, size int64, err error) {
	var runID int64
	err = s.pool.QueryRow(ctx, "SELECT run_id, sha256, name, size FROM artifacts WHERE id = $1", id).Scan(&runID, &sha, &name, &size)
	if db.IsNoRows(err) {
		return "", "", 0, apperr.NotFound("artifact.not-found", "The artifact does not exist.")
	}
	if err != nil {
		return "", "", 0, err
	}
	notFound := apperr.NotFound("artifact.not-found", "The artifact does not exist.")
	run, err := s.runByID(ctx, s.pool, runID, false)
	if err != nil {
		return "", "", 0, notFoundAs(err, notFound)
	}
	if _, err := s.repoOfRun(ctx, p, run.RepoID, LevelRead); err != nil {
		return "", "", 0, notFoundAs(err, notFound)
	}
	return sha, name, size, nil
}

// CommitStatus is the result of a build or an external check for a commit.
type CommitStatus struct {
	Context     string    `json:"context"`
	State       string    `json:"state" enum:"pending,running,success,failure,error,cancelled,skipped" doc:"skipped: the paths filters of the pipeline left the commit out"`
	Description string    `json:"description"`
	URL         string    `json:"url,omitempty"`
	Updated     time.Time `json:"updated"`
}

// Statuses returns the statuses of commits of a repository.
func (s *Service) Statuses(ctx context.Context, p *identity.Principal, key, slug string, shas []string) (map[string][]CommitStatus, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT sha, context, state, description, url, updated_at FROM commit_statuses WHERE repository_id = $1 AND sha = ANY($2)
		ORDER BY context`, repo.ID, shas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]CommitStatus{}
	for rows.Next() {
		var sha string
		var c CommitStatus
		if err := rows.Scan(&sha, &c.Context, &c.State, &c.Description, &c.URL, &c.Updated); err != nil {
			return nil, err
		}
		out[sha] = append(out[sha], c)
	}
	return out, rows.Err()
}

// StatusInput reports a status of an external system.
type StatusInput struct {
	Context     string `json:"context" minLength:"1" maxLength:"100" doc:"e.g. sonar/quality-gate"`
	State       string `json:"state" enum:"pending,running,success,failure,error,cancelled"`
	Description string `json:"description,omitempty" maxLength:"500"`
	URL         string `json:"url,omitempty" maxLength:"2000" format:"uri"`
}

// ReportStatus records a commit status of an external system (write access).
func (s *Service) ReportStatus(ctx context.Context, p *identity.Principal, key, slug, sha string, in StatusInput) error {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelWrite)
	if err != nil {
		return err
	}
	if len(in.Context) >= 6 && in.Context[:6] == "kogge/" {
		return apperr.Invalid("status.context", "Contexts starting with kogge/ are reserved for Kogge's builds.")
	}
	commit, err := s.repos.Resolve(ctx, repo.ID, sha)
	if err != nil {
		return err
	}
	// Merge checks may count it (pull requests that merge when ready look again).
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.setStatus(ctx, tx, repo.ID, commit, in.Context, in.State, in.Description, 0); err != nil {
			return err
		}
		return s.publish(ctx, tx, StatusEvent{RepoID: repo.ID, Commit: commit})
	})
}

// Pipelines lists the pipelines of a ref that can be started by hand, with their run forms.
type PipelineInfo struct {
	ID        string                         `json:"id"`
	Name      string                         `json:"name"`
	Manual    bool                           `json:"manual"`
	Variables map[string]spec.ManualVariable `json:"variables"`
}

// ManualPipelines returns the pipelines of the file at a ref.
func (s *Service) ManualPipelines(ctx context.Context, p *identity.Principal, key, slug, ref string) ([]PipelineInfo, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	commit, err := s.repos.Resolve(ctx, repo.ID, ref)
	if err != nil {
		return nil, err
	}
	data, ok, err := s.repos.ReadFile(ctx, repo.ID, commit, spec.Path)
	if err != nil || !ok {
		return []PipelineInfo{}, err
	}
	// The pipelines of included files count, read with the permissions of who asks.
	f, err := spec.Load(ctx, data, s.readerFor(p, repo, commit))
	var mistakes spec.Errors
	if errors.As(err, &mistakes) {
		return nil, apperr.Invalid("run.pipeline-file", err.Error())
	}
	if err != nil {
		return nil, err
	}
	out := []PipelineInfo{}
	for _, pl := range f.Pipelines {
		info := PipelineInfo{ID: pl.ID, Name: pl.Name, Manual: pl.On.Manual != nil, Variables: map[string]spec.ManualVariable{}}
		if pl.On.Manual != nil {
			info.Variables = pl.On.Manual.Variables
		}
		out = append(out, info)
	}
	return out, nil
}

// RunInfo returns a run with its repository, for integrations such as webhooks (no permission check).
func (s *Service) RunInfo(ctx context.Context, id int64) (*RunSummary, error) {
	run, err := s.runByID(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	list, err := s.summaries(ctx, []*runRow{run})
	if err != nil {
		return nil, err
	}
	if repo, err := s.repos.Repo(ctx, run.RepoID); err == nil {
		list[0].Repository = &RunRepo{ProjectKey: repo.ProjectKey, Slug: repo.Slug, Name: repo.Name}
	}
	return &list[0], nil
}

// LatestRuns returns, per pipeline, the newest run of a repository at one of the commits or on one of the branches
// (the development section of issues; no permission check).
func (s *Service) LatestRuns(ctx context.Context, repoID int64, commits, branches []string) ([]RunSummary, error) {
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT ON (pipeline) "+runColumns+` FROM runs
		WHERE repository_id = $1 AND (commit_sha = ANY($2) OR branch = ANY($3)) ORDER BY pipeline, number DESC`, repoID, nonNil(commits),
		nonNil(branches))
	if err != nil {
		return nil, err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, err
	}
	return s.summaries(ctx, runs)
}

// CurrentDeployment is the latest successful deployment to an environment.
type CurrentDeployment struct {
	Environment string    `json:"environment"`
	RunNumber   int       `json:"runNumber"`
	Commit      string    `json:"commit"`
	Ref         string    `json:"ref"`
	Finished    time.Time `json:"finished"`
}

// CurrentDeployments returns the latest successful deployment per environment of a repository (no permission
// check).
func (s *Service) CurrentDeployments(ctx context.Context, repoID int64) ([]CurrentDeployment, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (e.id) e.name, r.number, d.commit_sha, d.ref, coalesce(d.finished_at, d.created_at)
		FROM deployments d JOIN environments e ON e.id = d.environment_id JOIN runs r ON r.id = d.run_id
		WHERE e.repository_id = $1 AND d.state = 'succeeded' ORDER BY e.id, d.created_at DESC`, repoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CurrentDeployment])
}
