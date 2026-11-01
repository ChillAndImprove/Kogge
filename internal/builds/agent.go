// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// The agent protocol: agents connect outbound and long-poll for work at /api/agent/v1. They
// authenticate with their secret; everything about a job uses the job's token, valid while the job runs.

// The sizes of logs, artifacts and caches are limits of the settings (Administration → Settings → Limits).
const (
	leaseDuration = protocol.Lease
	pollDuration  = protocol.PollTimeout
	maxLogChunk   = protocol.MaxLogChunk
	maxReportSize = 16 << 20
)

// The messages of the protocol.
type (
	RegisterRequest = protocol.RegisterRequest
	ArtifactRef     = protocol.ArtifactRef
	JobPayload      = protocol.JobPayload
	CompleteRequest = protocol.CompleteRequest
)

// AgentHandler serves the agent protocol.
type AgentHandler struct {
	S *Service
}

// Routes registers the protocol.
func (h *AgentHandler) Routes(r chi.Router) {
	r.Post("/api/agent/v1/register", h.register)
	r.Post("/api/agent/v1/jobs/claim", h.claim)
	r.Post("/api/agent/v1/jobs/{id}/heartbeat", h.heartbeat)
	r.Post("/api/agent/v1/jobs/{id}/log", h.appendLog)
	r.Post("/api/agent/v1/jobs/{id}/complete", h.complete)
	r.Put("/api/agent/v1/jobs/{id}/artifacts/{name}", h.uploadArtifact)
	r.Get("/api/agent/v1/jobs/{id}/artifacts/{artifact}", h.downloadArtifact)
	r.Get("/api/agent/v1/jobs/{id}/caches/{key}", h.downloadCache)
	r.Put("/api/agent/v1/jobs/{id}/caches/{key}", h.uploadCache)
	r.Post("/api/agent/v1/jobs/{id}/reports/junit", h.uploadReport)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func bearer(r *http.Request) string {
	t, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return t
}

func (h *AgentHandler) register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Name == "" {
		httpx.WriteProblem(w, r, apperr.BadRequest("agent.request", "Invalid registration request."))
		return
	}
	s := h.S
	invalid := apperr.Unauthorized("agent.token", "The registration token is invalid, expired or used up.")
	secret, hash := crypto.NewToken(crypto.PrefixAgentSecret)
	// The labels it registers with are the agent's; later changes wait for an administrator (see claim).
	labels := normalizeLabels(append(req.Labels, req.OS, req.Arch))
	var id int64
	err := db.InTx(r.Context(), s.pool, func(tx pgx.Tx) error {
		// One use of the token, if it has uses left; the row lock orders concurrent registrations.
		var tokenID int64
		var project *int64
		var autoApprove bool
		err := tx.QueryRow(r.Context(), `UPDATE agent_tokens SET uses = uses + 1 WHERE token_hash = $1
			AND (expires_at IS NULL OR expires_at > now()) AND (max_uses = 0 OR uses < max_uses)
			RETURNING id, project_id, auto_approve`, crypto.HashToken(req.Token)).Scan(&tokenID, &project, &autoApprove)
		if db.IsNoRows(err) {
			return invalid
		}
		if err != nil {
			return err
		}
		var approved any
		if autoApprove {
			approved = now()
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO agents (name, project_id, secret_hash, labels, os, arch, version, last_seen_at, last_ip,
			approved_at) VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8, $9) RETURNING id`, truncate(req.Name, 100), project, hash, labels, req.OS,
			req.Arch, truncate(req.Version, 40), httpx.ClientIP(r.Context()).String(), approved).Scan(&id); err != nil {
			return err
		}
		return audit.Record(r.Context(), tx, audit.Entry{Action: "agent.register", TargetType: "agent", TargetID: itoa(id),
			Details: map[string]any{"name": truncate(req.Name, 100), "token": tokenID, "labels": labels, "approved": autoApprove}})
	})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.RegisterResponse{ID: id, Secret: secret})
}

// sameLabels reports whether two label lists hold the same labels.
func sameLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, l := range a {
		if !containsString(b, l) {
			return false
		}
	}
	return true
}

func normalizeLabels(in []string) []string {
	out := []string{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && len(l) <= 64 && !containsString(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type agentRow struct {
	ID        int64
	ProjectID *int64
	Labels    []string
	Approved  bool
	Protected bool // only jobs of refs that the code project protects
	// Limits are the most it allows one container, as it announced with its claim (0: no limit).
	Limits protocol.Limits
}

func (s *Service) agentOf(ctx context.Context, secret string) (*agentRow, error) {
	var a agentRow
	err := s.pool.QueryRow(ctx, "SELECT id, project_id, labels, approved_at IS NOT NULL, protected FROM agents WHERE secret_hash = $1 AND enabled",
		crypto.HashToken(secret)).Scan(&a.ID, &a.ProjectID, &a.Labels, &a.Approved, &a.Protected)
	if err != nil {
		return nil, apperr.Unauthorized("agent.secret", "Unknown or disabled agent.")
	}
	return &a, nil
}

// AgentStateHeader tells an agent without work why: it waits for an administrator to approve it.
const AgentStateHeader = "X-Kogge-Agent-State"

// claimJob hands the oldest matching pending job to the agent. Jobs that deploy wait while their environment has a
// running deployment; repositories may limit how many of their jobs run at once. Protected agents take jobs of refs
// that a branch permission of the code project protects; agents with limits take jobs that ask for no more CPUs and
// memory for one container than they allow.
func (s *Service) claimJob(ctx context.Context, a *agentRow) (*JobPayload, error) {
	var payload *JobPayload
	// What may not start now is passed over: single jobs, and the waiting jobs of a repository that runs as many as
	// it may or of an environment that deploys, all together, so that they do not keep agents from the jobs of
	// others. Never nil: ANY(NULL) matches nothing.
	skip, skipRepos := []int64{}, []int64{}
	skipEnvRepos, skipEnvs := []int64{}, []string{}
	for attempt := 0; attempt < 32 && payload == nil; attempt++ {
		retry := false
		var waited *float64 // since the job became pending
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			var jobID, runID, repoID int64
			var env, ref string
			err := tx.QueryRow(ctx, `SELECT j.id, r.id, r.repository_id, coalesce(j.environment, ''),
				CASE WHEN r.tag IS NOT NULL THEN 'refs/tags/' || r.tag ELSE 'refs/heads/' || coalesce(r.branch, '') END
				FROM run_jobs j JOIN runs r ON r.id = j.run_id
				WHERE j.state = 'pending' AND j.labels <@ $1::text[] AND ($2::bigint IS NULL OR r.project_id = $2) AND NOT j.id = ANY($3::bigint[])
				  AND (NOT $4 OR r.ref_protection >= $5) AND NOT r.repository_id = ANY($6::bigint[])
				  AND NOT EXISTS (SELECT FROM unnest($7::bigint[], $8::text[]) AS b(repo, env)
				    WHERE b.repo = r.repository_id AND b.env = j.environment)
				  AND ($9::bigint = 0 OR j.millicpus <= $9) AND ($10::bigint = 0 OR j.memory <= $10)
				ORDER BY j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1`, a.Labels, a.ProjectID, skip, a.Protected, int(ProtectionProject),
				skipRepos, skipEnvRepos, skipEnvs, a.Limits.MilliCPUs, a.Limits.Memory).
				Scan(&jobID, &runID, &repoID, &env, &ref)
			if db.IsNoRows(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if a.Protected {
				// The branch permissions may have changed since the run was created.
				if now := s.repos.Protection(ctx, repoID, ref); now < ProtectionProject {
					skip, retry = append(skip, jobID), true
					_, err := tx.Exec(ctx, "UPDATE runs SET ref_protection = $2 WHERE id = $1", runID, int(now))
					return err
				}
			}
			// The checks lock the environment or the repository's settings, so concurrent claims see each other.
			switch blocked, err := s.mayStart(ctx, tx, repoID, env); {
			case err != nil:
				return err
			case blocked == blockedByRepository:
				skipRepos, retry = append(skipRepos, repoID), true
				return nil
			case blocked == blockedByEnvironment:
				skipEnvRepos, skipEnvs, retry = append(skipEnvRepos, repoID), append(skipEnvs, env), true
				return nil
			}
			token, hash := crypto.NewToken(crypto.PrefixJobToken)
			if err := tx.QueryRow(ctx, `UPDATE run_jobs SET state = 'running', agent_id = $2, token_hash = $3, lease_until = now() + $4::interval,
				started_at = coalesce(started_at, now()), cancel_requested = false WHERE id = $1
				RETURNING extract(epoch FROM now() - queued_at)::float8`, jobID, a.ID, hash,
				fmt.Sprintf("%d seconds", int(leaseDuration.Seconds()))).Scan(&waited); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE deployments SET state = 'running' WHERE job_id = $1 AND state = 'waiting'", jobID); err != nil {
				return err
			}
			j, err := scanJob(tx.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1", jobID))
			if err != nil {
				return err
			}
			j.Def.Job.Timeout = s.jobTimeout(ctx, j)
			if _, err := tx.Exec(ctx, "UPDATE run_jobs SET deadline_at = now() + $2::interval WHERE id = $1", jobID,
				fmt.Sprintf("%d seconds", int(j.Def.Job.Timeout.Seconds()))); err != nil {
				return err
			}
			run, err := s.runByID(ctx, tx, j.RunID, false)
			if err != nil {
				return err
			}
			if payload, err = s.payload(ctx, tx, run, j, token); err != nil {
				return err
			}
			return s.advance(ctx, tx, run.ID)
		})
		if err == nil && payload != nil && waited != nil {
			metrics.BuildWait(time.Duration(*waited * float64(time.Second)))
		}
		if err != nil || !retry {
			return payload, err
		}
	}
	return payload, nil
}

// jobTimeout is the job's timeout, limited by the instance's maximum.
func (s *Service) jobTimeout(ctx context.Context, j *jobRow) time.Duration {
	t := j.Def.Job.Timeout
	if t <= 0 {
		t = spec.DefaultTimeout
	}
	if st, err := s.settings.Get(ctx); err == nil && st.MaxJobTimeoutMinutes > 0 {
		t = min(t, time.Duration(st.MaxJobTimeoutMinutes)*time.Minute)
	}
	return t
}

// What keeps a job from starting now (see mayStart).
const (
	blockedByEnvironment = "environment" // a deployment to the job's environment runs
	blockedByRepository  = "repository"  // the repository runs as many jobs as it may
)

// mayStart reports what keeps a job of the repository (deploying to env, if not empty) from starting now, or "".
func (s *Service) mayStart(ctx context.Context, tx pgx.Tx, repoID int64, env string) (string, error) {
	if env != "" {
		var envID int64
		err := tx.QueryRow(ctx, "SELECT id FROM environments WHERE repository_id = $1 AND name = $2 FOR UPDATE", repoID, env).Scan(&envID)
		if err != nil && !db.IsNoRows(err) {
			return "", err
		}
		var busy bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM deployments WHERE environment_id = $1 AND state = 'running')", envID).Scan(&busy); err != nil {
			return "", err
		}
		if busy {
			return blockedByEnvironment, nil
		}
	}
	var limit int
	err := tx.QueryRow(ctx, "SELECT max_parallel_jobs FROM repository_build_settings WHERE repository_id = $1 FOR UPDATE", repoID).Scan(&limit)
	if db.IsNoRows(err) || (err == nil && limit <= 0) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var running int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM run_jobs j JOIN runs r ON r.id = j.run_id WHERE r.repository_id = $1 AND j.state = 'running'`,
		repoID).Scan(&running); err != nil {
		return "", err
	}
	if running >= limit {
		return blockedByRepository, nil
	}
	return "", nil
}

func (s *Service) payload(ctx context.Context, tx pgx.Tx, run *runRow, j *jobRow, token string) (*JobPayload, error) {
	repo, err := s.repos.Repo(ctx, run.RepoID)
	if err != nil {
		return nil, err
	}
	var envID int64
	gated := false
	if j.Environment != "" {
		env, err := scanEnv(tx.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE repository_id = $1 AND name = $2", run.RepoID, j.Environment))
		if err != nil && !db.IsNoRows(err) {
			return nil, err
		}
		if err == nil {
			envID, gated = env.ID, env.gated()
		}
	}
	// Protected values reach protected refs (only maintainers can change their pipeline files) and deployments that
	// people let through: naming an environment in a pipeline file is not enough. The repository's administrators
	// control its own branch permissions and environments, so these unlock only the repository's protected values.
	protection := s.repos.Protection(ctx, run.RepoID, run.ref())
	if gated && protection < ProtectionRepository {
		protection = ProtectionRepository
	}
	// The job's caches belong to the same side (see uploadCache).
	if _, err := tx.Exec(ctx, "UPDATE run_jobs SET protected = $2 WHERE id = $1", j.ID, protection >= ProtectionRepository); err != nil {
		return nil, err
	}
	configured, envVars, secrets, err := s.resolveVariables(ctx, tx, repo, envID, protection)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(s.opt.PublicURL.String(), "/")
	runURL := base + RunPath(repo.ProjectKey, repo.Slug, run.Number)
	// Precedence (later wins): instance, code project, repository, file, pipeline, job, environment, manual run;
	// the variables Kogge provides cannot be overridden.
	vars := map[string]string{}
	for _, m := range []map[string]string{configured, j.Def.Variables, j.Def.Job.Variables, envVars, run.Variables} {
		for k, v := range m {
			vars[k] = v
		}
	}
	kogge := map[string]string{"CI": "true", "KOGGE_CI": "true", "KOGGE_SERVER_URL": base, "KOGGE_PROJECT": repo.ProjectKey,
		"KOGGE_REPOSITORY": repo.ProjectKey + "/" + repo.Slug, "KOGGE_COMMIT": run.Commit, "KOGGE_BRANCH": run.Branch, "KOGGE_TAG": run.Tag,
		"KOGGE_PIPELINE": run.Pipeline, "KOGGE_RUN_NUMBER": strconv.Itoa(run.Number), "KOGGE_RUN_URL": runURL, "KOGGE_STAGE": j.StageName,
		"KOGGE_JOB": j.Name, "KOGGE_TRIGGER": run.Event, "KOGGE_ENVIRONMENT": j.Environment}
	if run.PRNumber != 0 {
		kogge["KOGGE_PULL_REQUEST"] = strconv.Itoa(run.PRNumber)
		kogge["KOGGE_PULL_REQUEST_TARGET"] = run.Target
	}
	up, err := s.upstreamOf(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	if up != nil {
		for k, v := range up.variables(base) {
			kogge[k] = v
		}
	}
	for k, v := range kogge {
		vars[k] = v
	}
	p := &JobPayload{ID: j.ID, Token: token, Attempt: j.Attempt, RunID: run.ID, RunNumber: run.Number, Pipeline: run.Pipeline, Stage: j.StageName,
		CloneURL: repo.CloneURL, Commit: run.Commit, Branch: run.Branch, Tag: run.Tag, Job: j.Def.Job, Variables: vars, Mask: nonNil(secrets),
		Artifacts: []ArtifactRef{}, LogOffset: j.LogSize, ServerURL: base, Repository: repo.ProjectKey + "/" + repo.Slug}
	// Artifacts of the named dependencies, else of the jobs it needs (those it wants the artifacts of), else of the
	// earlier stages; before them those of the run of the same repository that this run followed, for the jobs whose
	// dependencies do not choose.
	var upstreamID int64
	if up != nil && up.run.RepoID == run.RepoID {
		upstreamID = up.run.ID
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.name, j.stage_name, j.name, j.stage_index, a.size, a.run_id FROM artifacts a
		JOIN run_jobs j ON j.id = a.job_id WHERE (a.run_id = $1 AND j.id <> $2) OR a.run_id = $3 ORDER BY a.run_id = $1, a.id`,
		run.ID, j.ID, upstreamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps, needs := j.Def.Job.Dependencies, j.Def.Job.Needs
	for rows.Next() {
		var a ArtifactRef
		var stage, job string
		var stageIndex int
		var runID int64
		if err := rows.Scan(&a.ID, &a.Name, &stage, &job, &stageIndex, &a.Size, &runID); err != nil {
			return nil, err
		}
		if runID != run.ID {
			if deps == nil {
				a.Job = fmt.Sprintf("%s #%d: %s/%s", up.run.Name, up.run.Number, stage, job)
				p.Artifacts = append(p.Artifacts, a)
			}
			continue
		}
		a.Job = spec.Key(stage, job)
		var take bool
		switch {
		case deps != nil && needs != nil: // the parser names the jobs as stage/job
			take = containsString(deps, a.Job)
		case deps != nil:
			take = stageIndex < j.StageIndex && (containsString(deps, a.Job) || containsString(deps, job))
		case needs != nil:
			take = slices.ContainsFunc(needs, func(n spec.Need) bool { return n.Job == a.Job && n.Artifacts })
		default:
			take = stageIndex < j.StageIndex
		}
		if take {
			p.Artifacts = append(p.Artifacts, a)
		}
	}
	return p, rows.Err()
}

func (h *AgentHandler) claim(w http.ResponseWriter, r *http.Request) {
	s := h.S
	a, err := s.agentOf(r.Context(), bearer(r))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	_, _ = s.pool.Exec(r.Context(), "UPDATE agents SET last_seen_at = now(), last_ip = $2 WHERE id = $1", a.ID, httpx.ClientIP(r.Context()).String())
	var hello protocol.ClaimRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&hello); err == nil && hello.Version != "" {
		// Labels decide which jobs an agent gets, so an agent cannot change its own: different labels are kept for an
		// administrator to accept (see SetAgentLabels).
		announced := normalizeLabels(append(hello.Labels, hello.OS, hello.Arch))
		var differs any
		if !sameLabels(announced, a.Labels) {
			differs = announced
		}
		_, _ = s.pool.Exec(r.Context(), `UPDATE agents SET announced_labels = $2, version = $3 WHERE id = $1
			AND (announced_labels IS DISTINCT FROM $2::text[] OR version <> $3)`, a.ID, differs, truncate(hello.Version, 40))
		// The limits protect the agent's own host, so an agent sets them itself; they are kept for telling why a job
		// waits.
		if hello.Limits != nil {
			a.Limits = protocol.Limits{MilliCPUs: max(hello.Limits.MilliCPUs, 0), Memory: max(hello.Limits.Memory, 0)}
		}
		_, _ = s.pool.Exec(r.Context(), `UPDATE agents SET max_millicpus = $2, max_memory = $3 WHERE id = $1
			AND (max_millicpus <> $2 OR max_memory <> $3)`, a.ID, a.Limits.MilliCPUs, a.Limits.Memory)
	}
	if !a.Approved {
		// No work until an administrator approves it; the agent asks again after a while.
		w.Header().Set(AgentStateHeader, "awaiting-approval")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	deadline := time.NewTimer(pollDuration)
	defer deadline.Stop()
	for {
		wait := s.wake.wait()
		p, err := s.claimJob(r.Context(), a)
		if err != nil {
			httpx.WriteProblem(w, r, err)
			return
		}
		if p != nil {
			writeJSON(w, http.StatusOK, p)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-s.stopping:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-wait:
		case <-time.After(s.ClaimRecheck):
		}
	}
}

// jobOf authenticates a request of a running job.
func (s *Service) jobOf(r *http.Request) (*jobRow, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return nil, apperr.NotFound("job.not-found", "The job does not exist.")
	}
	j, err := scanJob(s.pool.QueryRow(r.Context(), "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1 AND token_hash = $2 AND state = 'running'",
		id, crypto.HashToken(bearer(r))))
	if err != nil {
		return nil, apperr.Unauthorized("job.token", "The job token is invalid, or the job is not running.")
	}
	return j, nil
}

// JobRepo returns the repository of a running job for its token (Git access of build jobs).
func (s *Service) JobRepo(ctx context.Context, token string) (int64, bool) {
	var repoID int64
	err := s.pool.QueryRow(ctx, `SELECT r.repository_id FROM run_jobs j JOIN runs r ON r.id = j.run_id WHERE j.token_hash = $1 AND j.state = 'running'`,
		crypto.HashToken(token)).Scan(&repoID)
	return repoID, err == nil
}

func (h *AgentHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	j, err := h.S.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	var cancel bool
	if err := h.S.pool.QueryRow(r.Context(), `UPDATE run_jobs SET lease_until = now() + $2::interval WHERE id = $1 RETURNING cancel_requested`,
		j.ID, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds()))).Scan(&cancel); err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	// Busy agents do not poll for work; their jobs' heartbeats show they are alive.
	_, _ = h.S.pool.Exec(r.Context(), "UPDATE agents SET last_seen_at = now() WHERE id = $1 AND last_seen_at < now() - interval '20 seconds'", j.AgentID)
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{Cancel: cancel})
}

func (h *AgentHandler) complete(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	var req CompleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpx.WriteProblem(w, r, apperr.BadRequest("job.request", "Invalid request."))
		return
	}
	state := JobFailed
	switch req.State {
	case JobSucceeded:
		state = JobSucceeded
	case JobCancelled:
		state = JobCancelled
	}
	err = db.InTx(r.Context(), s.pool, func(tx pgx.Tx) error {
		return s.finishJob(r.Context(), tx, j.ID, state, req.ExitCode, truncate(req.Failure, 2000))
	})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// jobMasks keeps the secret values of running jobs, for masking their logs. Every node that receives a job's log
// keeps them; the notification that the job ended drops them on every node (Notified).
type jobMasks struct {
	mu   sync.Mutex
	jobs map[int64]jobMask
}

type jobMask struct {
	attempt int
	values  []string
}

// maskedJobs bounds the jobs whose secrets a node keeps (should notifications of their end be lost).
const maskedJobs = 10000

func (m *jobMasks) get(jobID int64, attempt int) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[jobID]
	return e.values, ok && e.attempt == attempt
}

func (m *jobMasks) put(jobID int64, attempt int, values []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs == nil || len(m.jobs) >= maskedJobs {
		m.jobs = map[int64]jobMask{}
	}
	m.jobs[jobID] = jobMask{attempt: attempt, values: values}
}

func (m *jobMasks) drop(jobID int64) {
	m.mu.Lock()
	delete(m.jobs, jobID)
	m.mu.Unlock()
}

func (s *Service) maskOf(ctx context.Context, j *jobRow) []string {
	if list, ok := s.masks.get(j.ID, j.Attempt); ok {
		return list
	}
	var list []string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := s.runByID(ctx, tx, j.RunID, false)
		if err != nil {
			return err
		}
		repo, err := s.repos.Repo(ctx, run.RepoID)
		if err != nil {
			return err
		}
		var envID int64
		if j.Environment != "" {
			_ = tx.QueryRow(ctx, "SELECT id FROM environments WHERE repository_id = $1 AND name = $2", run.RepoID, j.Environment).Scan(&envID)
		}
		// Every secret the job could have received is masked.
		_, _, list, err = s.resolveVariables(ctx, tx, repo, envID, ProtectionProject)
		return err
	})
	list = maskForms(list)
	if err == nil {
		// Kept only when complete: the next chunk asks again (the agent masks the secrets it got, too).
		s.masks.put(j.ID, j.Attempt, list)
	}
	return list
}

// maskForms returns the texts to mask for secrets: each secret and its encoded forms (protocol.MaskForms), longest
// first, so that replacing a shorter one does not leave the rest of a longer one behind.
func maskForms(secrets []string) []string {
	var out []string
	for _, sec := range secrets {
		for _, f := range protocol.MaskForms(strings.TrimSpace(sec)) {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// maskSecrets replaces the texts of maskForms in data.
func maskSecrets(data []byte, forms []string) []byte {
	text := string(data)
	for _, f := range forms {
		text = strings.ReplaceAll(text, f, "****")
	}
	return []byte(text)
}

func (h *AgentHandler) appendLog(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.BadRequest("log.offset", "The offset is missing."))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxLogChunk+1))
	if err != nil || len(data) > maxLogChunk {
		httpx.WriteProblem(w, r, apperr.New(http.StatusRequestEntityTooLarge, "log.chunk", "Log chunks may have at most 1 MiB."))
		return
	}
	size, err := s.appendLogChunk(r.Context(), j, offset, maskSecrets(data, s.maskOf(r.Context(), j)))
	if errors.Is(err, errLogOffset) {
		writeJSON(w, http.StatusConflict, protocol.LogResponse{Size: size})
		return
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.LogResponse{Size: size})
}

var errLogOffset = errors.New("log offset mismatch")

// appendLogChunk appends to a job's log if the offset is the current size (so retried chunks never duplicate). A log
// that reaches the limit of the settings is cut off with a line that says so; the job goes on, and what it logs
// after is dropped.
func (s *Service) appendLogChunk(ctx context.Context, j *jobRow, offset int64, data []byte) (int64, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	limit := settings.MiB(st.MaxJobLogMB)
	var size int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cut bool
		if err := tx.QueryRow(ctx, "SELECT log_size, log_cut FROM run_jobs WHERE id = $1 FOR UPDATE", j.ID).Scan(&size, &cut); err != nil {
			return err
		}
		if offset != size {
			return errLogOffset
		}
		if cut || size >= limit {
			return nil // cut off already, or the limit was lowered
		}
		if size+int64(len(data)) > limit {
			data, cut = cutLog(data, limit-size), true
		}
		path := s.logPath(j.ID)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		if _, err := f.WriteAt(data, size); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		size += int64(len(data))
		if _, err := tx.Exec(ctx, "UPDATE run_jobs SET log_size = $2, log_cut = $3 WHERE id = $1", j.ID, size, cut); err != nil {
			return err
		}
		// The streams of the log, at whichever node they are, read on: the file is closed, so another node that opens
		// it after the commit sees what was written (also on NFS, which keeps close-to-open consistency).
		return jobChanged(ctx, tx, j.ID, false)
	})
	return size, err
}

// cutLog keeps of a chunk what fits into room bytes with the line that says the log was cut off: the chunk up to its
// last line end that leaves room for that line. The log ends at its limit, or a little after it where the line does
// not fit any more; the job records that its log was cut off, and later chunks are dropped.
func cutLog(data []byte, room int64) []byte {
	marker := []byte("\n[Kogge: the log reached its limit (Administration → Settings → Limits); the rest of it was not kept. The job goes on.]\n")
	keep := room - int64(len(marker))
	if keep < 0 {
		keep = 0
	}
	if keep > int64(len(data)) {
		keep = int64(len(data))
	}
	kept := data[:keep]
	if i := bytes.LastIndexByte(kept, '\n'); i >= 0 {
		kept = kept[:i]
	} else {
		kept = kept[:0]
	}
	return append(kept[:len(kept):len(kept)], marker...)
}

func (h *AgentHandler) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	name := chi.URLParam(r, "name")
	var expiry time.Duration = spec.DefaultExpiry
	found := false
	for _, a := range j.Def.Job.Artifacts {
		if a.Name == name {
			expiry, found = a.ExpireIn, true
		}
	}
	if !found {
		httpx.WriteProblem(w, r, apperr.Invalidf("artifact.name", "The job defines no artifact %s.", name))
		return
	}
	st, err := s.settings.Get(r.Context())
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	// The limits refuse an upload by its announced length before it is sent (agents ask with Expect: 100-continue),
	// and stop a longer one while it is read.
	maxSize, runLimit := settings.MiB(st.MaxArtifactMB), settings.MiB(st.MaxRunArtifactsMB)
	if r.ContentLength > maxSize {
		httpx.WriteProblem(w, r, artifactTooLarge(maxSize))
		return
	}
	if err := storage.Check(st, s.blobs.Dir(), r.ContentLength); err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	if runLimit > 0 && r.ContentLength > 0 {
		if err := s.checkRunArtifacts(r.Context(), s.pool, j, name, r.ContentLength, runLimit); err != nil {
			httpx.WriteProblem(w, r, err)
			return
		}
	}
	ref, err := s.blobs.Put(r.Context(), r.Body, maxSize, "application/zip")
	if errors.Is(err, blob.ErrTooLarge) {
		err = artifactTooLarge(maxSize)
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	err = db.InTx(r.Context(), s.pool, func(tx pgx.Tx) error {
		// Uploads of the jobs of a run come one after another here, so that together they keep to the limit.
		if _, err := tx.Exec(r.Context(), "SELECT 1 FROM runs WHERE id = $1 FOR UPDATE", j.RunID); err != nil {
			return err
		}
		if runLimit > 0 {
			if err := s.checkRunArtifacts(r.Context(), tx, j, name, ref.Size, runLimit); err != nil {
				return err
			}
		}
		var id int64
		if err := tx.QueryRow(r.Context(), `INSERT INTO artifacts (run_id, job_id, name, sha256, size, expires_at) VALUES ($1, $2, $3, $4, $5, now() + $6::interval)
			ON CONFLICT (job_id, name) DO UPDATE SET sha256 = EXCLUDED.sha256, size = EXCLUDED.size, expires_at = EXCLUDED.expires_at RETURNING id`,
			j.RunID, j.ID, name, ref.SHA256, ref.Size, fmt.Sprintf("%d seconds", int64(expiry.Seconds()))).Scan(&id); err != nil {
			return err
		}
		if err := blob.RemoveRefs(r.Context(), tx, "artifact", id); err != nil {
			return err
		}
		return blob.AddRef(r.Context(), tx, ref.SHA256, "artifact", id)
	})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func artifactTooLarge(limit int64) error {
	return apperr.New(http.StatusRequestEntityTooLarge, "artifact.too-large", "The artifact is larger than the %s this installation allows.",
		storage.Format(limit))
}

// checkRunArtifacts refuses an artifact of size bytes that would take the artifacts of the job's run over the limit
// (an artifact of the same name of the job counts only once: it is replaced).
func (s *Service) checkRunArtifacts(ctx context.Context, q db.Querier, j *jobRow, name string, size, limit int64) error {
	var total int64
	if err := q.QueryRow(ctx, "SELECT coalesce(sum(size), 0) FROM artifacts WHERE run_id = $1 AND NOT (job_id = $2 AND name = $3)",
		j.RunID, j.ID, name).Scan(&total); err != nil {
		return err
	}
	if total+size > limit {
		return apperr.New(http.StatusRequestEntityTooLarge, "artifact.run-too-large",
			"The artifacts of the run would take %s, more than the %s a run may keep (the run has %s already).",
			storage.Format(total+size), storage.Format(limit), storage.Format(total))
	}
	return nil
}

func (h *AgentHandler) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "artifact"), 10, 64)
	var sha string
	var size int64
	// The job's run's artifacts, and those of the run of the same repository that it followed.
	if err := s.pool.QueryRow(r.Context(), `SELECT a.sha256, a.size FROM artifacts a WHERE a.id = $1 AND (a.run_id = $2 OR a.run_id = (
			SELECT u.id FROM runs d JOIN runs u ON u.id = d.upstream_run_id AND u.repository_id = d.repository_id WHERE d.id = $2))`,
		id, j.RunID).Scan(&sha, &size); err != nil {
		httpx.WriteProblem(w, r, apperr.NotFound("artifact.not-found", "The artifact does not exist."))
		return
	}
	serveBlob(w, r, s, sha, size, "application/zip", "")
}

func serveBlob(w http.ResponseWriter, r *http.Request, s *Service, sha string, size int64, contentType, filename string) {
	f, err := s.blobs.Open(sha)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.NotFound("artifact.gone", "The file is not available any more."))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if filename != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	}
	_, _ = io.Copy(w, f)
}

func cacheKeyValid(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (h *AgentHandler) downloadCache(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	var sha string
	var size int64
	err = s.pool.QueryRow(r.Context(), `UPDATE build_caches c SET used_at = now() FROM run_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.id = $1 AND c.repository_id = r.repository_id AND c.protected = j.protected AND c.key = $2
		RETURNING c.sha256, c.size`, j.ID, chi.URLParam(r, "key")).Scan(&sha, &size)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	serveBlob(w, r, s, sha, size, "application/gzip", "")
}

func (h *AgentHandler) uploadCache(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	key := chi.URLParam(r, "key")
	if !cacheKeyValid(key) {
		httpx.WriteProblem(w, r, apperr.Invalid("cache.key", "Invalid cache key."))
		return
	}
	st, err := s.settings.Get(r.Context())
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	maxSize := settings.MiB(st.MaxCacheMB)
	if r.ContentLength > maxSize {
		httpx.WriteProblem(w, r, cacheTooLarge(maxSize))
		return
	}
	if err := storage.Check(st, s.blobs.Dir(), r.ContentLength); err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	ref, err := s.blobs.Put(r.Context(), r.Body, maxSize, "application/gzip")
	if errors.Is(err, blob.ErrTooLarge) {
		err = cacheTooLarge(maxSize)
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	err = db.InTx(r.Context(), s.pool, func(tx pgx.Tx) error {
		// Protected jobs (protected refs, gated deployments) and the others have caches of their own: a branch must
		// not plant files into a protected job's workspace, nor read what protected jobs cached.
		var owner int64
		if err := tx.QueryRow(r.Context(), `INSERT INTO build_caches (repository_id, protected, key, sha256, size)
			SELECT r.repository_id, j.protected, $2, $3, $4 FROM run_jobs j JOIN runs r ON r.id = j.run_id WHERE j.id = $1
			ON CONFLICT (repository_id, protected, key) DO UPDATE SET sha256 = EXCLUDED.sha256, size = EXCLUDED.size,
				updated_at = now(), used_at = now()
			RETURNING id`, j.ID, key, ref.SHA256, ref.Size).Scan(&owner); err != nil {
			return err
		}
		if err := blob.RemoveRefs(r.Context(), tx, "build-cache", owner); err != nil {
			return err
		}
		return blob.AddRef(r.Context(), tx, ref.SHA256, "build-cache", owner)
	})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func cacheTooLarge(limit int64) error {
	return apperr.New(http.StatusRequestEntityTooLarge, "cache.too-large", "The cache is larger than the %s this installation allows.",
		storage.Format(limit))
}

func (h *AgentHandler) uploadReport(w http.ResponseWriter, r *http.Request) {
	s := h.S
	j, err := s.jobOf(r)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxReportSize+1))
	if err != nil || len(data) > maxReportSize {
		httpx.WriteProblem(w, r, apperr.New(http.StatusRequestEntityTooLarge, "report.size", "Reports may have at most 16 MiB."))
		return
	}
	rep, err := ParseJUnit(data)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.Invalidf("report.junit", "The report is not JUnit XML: %v", err))
		return
	}
	// Reports are shown like logs, so the job's secrets are masked in them like in logs (audit L34).
	rep.mask(s.maskOf(r.Context(), j))
	if err := s.addReport(r.Context(), j.ID, rep); err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
