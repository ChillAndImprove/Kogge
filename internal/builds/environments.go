// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Environments are configured in Kogge, not in the pipeline file, so a branch cannot weaken its protections.

// Environment is a deployment target of a repository.
type Environment struct {
	ID             int64              `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Refs           []string           `json:"refs" doc:"Allowed branches and tags (patterns like main, release/*, refs/tags/v*); empty: all"`
	DeployerUsers  []int64            `json:"deployerUsers" doc:"Users who may deploy; empty with no groups: everyone who can write"`
	DeployerGroups []int64            `json:"deployerGroups"`
	Approvals      int                `json:"approvals" minimum:"0" maximum:"10" doc:"Approvals needed before a deployment starts"`
	ApproverUsers  []int64            `json:"approverUsers"`
	ApproverGroups []int64            `json:"approverGroups"`
	Users          []identity.UserRef `json:"users" doc:"The deployers and approvers named above"`
	Groups         []NamedGroup       `json:"groups" doc:"The groups named above"`
	Current        *Deployment        `json:"current,omitempty" doc:"The latest successful deployment"`
	Last           *Deployment        `json:"last,omitempty" doc:"The latest deployment"`
	Created        time.Time          `json:"created"`
}

// NamedGroup is a group with its name.
type NamedGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Deployment is a job that deployed (or tries to deploy) a commit to an environment.
type Deployment struct {
	ID          int64             `json:"id"`
	Environment string            `json:"environment"`
	RunID       int64             `json:"runId"`
	RunNumber   int               `json:"runNumber"`
	JobID       int64             `json:"jobId"`
	Commit      string            `json:"commit"`
	Ref         string            `json:"ref"`
	State       string            `json:"state" enum:"waiting,running,succeeded,failed,cancelled"`
	CreatedBy   *identity.UserRef `json:"createdBy,omitempty"`
	Created     time.Time         `json:"created"`
	Finished    *time.Time        `json:"finished,omitempty"`
	Approvals   []Approval        `json:"approvals"`
	Required    int               `json:"required" doc:"Approvals the environment requires"`
	CanApprove  bool              `json:"canApprove"`
}

// Approval is a decision about a deployment.
type Approval struct {
	User     identity.UserRef `json:"user"`
	Approved bool             `json:"approved"`
	Comment  string           `json:"comment,omitempty"`
	At       time.Time        `json:"at"`
}

var envNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type envRow struct {
	Environment
	repoID int64
}

func scanEnv(row pgx.Row) (*envRow, error) {
	var e envRow
	err := row.Scan(&e.ID, &e.repoID, &e.Name, &e.Description, &e.Refs, &e.DeployerUsers, &e.DeployerGroups, &e.Approvals, &e.ApproverUsers,
		&e.ApproverGroups, &e.Created)
	return &e, err
}

const envColumns = `id, repository_id, name, description, refs, deployer_users, deployer_groups, approvals, approver_users, approver_groups, created_at`

// gated reports whether deployments to the environment need a person: a deployer or approvals. Only then do they
// receive protected values from refs that are not protected.
func (e *Environment) gated() bool {
	return len(e.DeployerUsers)+len(e.DeployerGroups) > 0 || e.Approvals > 0
}

// refAllowed matches a run's ref against the environment's patterns.
func refAllowed(patterns []string, ref string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.HasPrefix(p, "refs/") {
			if spec.MatchGlob(p, ref) {
				return true
			}
		} else if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && spec.MatchGlob(p, b) {
			return true
		}
	}
	return false
}

func holds(users, groups []int64, p *identity.Principal) bool {
	if p == nil || !p.Authenticated() {
		return false
	}
	if slices.Contains(users, p.UserID()) {
		return true
	}
	for _, g := range p.Groups {
		if slices.Contains(groups, g) {
			return true
		}
	}
	return false
}

// prepareDeployment checks a deployment job when its stage starts. It returns what the job waits for (a deployer
// or approvals) and records the deployment.
func (s *Service) prepareDeployment(ctx context.Context, tx pgx.Tx, run *runRow, j *jobRow) (string, error) {
	env, err := scanEnv(tx.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE repository_id = $1 AND name = $2", run.RepoID, j.Environment))
	if db.IsNoRows(err) {
		// Environments named in a pipeline file exist without protections until they are configured.
		env, err = scanEnv(tx.QueryRow(ctx, `INSERT INTO environments (repository_id, name) VALUES ($1, $2)
			ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING `+envColumns, run.RepoID, j.Environment))
	}
	if err != nil {
		return "", err
	}
	if !refAllowed(env.Refs, run.ref()) {
		j.State, j.Failure = JobFailed, "The environment "+env.Name+" does not allow deployments of "+run.ref()+"."
		return "", nil
	}
	// Who lets the deployment through: who started the job by hand, else who started the run. Runs of pull requests
	// run code that anyone writing to the source branch may have put there, so a deployer always starts their
	// deployments by hand.
	actor := run.TriggeredBy
	if run.Event == spec.EventPullRequest {
		actor = 0
	}
	if j.TriggeredBy != 0 {
		actor = j.TriggeredBy
	}
	waiting := ""
	if len(env.DeployerUsers)+len(env.DeployerGroups) > 0 {
		var p *identity.Principal
		if actor != 0 {
			p, _ = s.identity.PrincipalOf(ctx, actor, identity.MethodInternal)
		}
		if !holds(env.DeployerUsers, env.DeployerGroups, p) {
			waiting = "deployer"
		}
	}
	if env.Approvals > 0 && waiting == "" {
		waiting = "approval"
	}
	// Every attempt of the job is a deployment of its own; earlier ones stay as history.
	_, err = tx.Exec(ctx, `INSERT INTO deployments (environment_id, run_id, job_id, commit_sha, ref, state, created_by)
		SELECT $1, $2, $3, $4, $5, 'waiting', $6 WHERE NOT EXISTS (SELECT 1 FROM deployments WHERE job_id = $3 AND state IN ('waiting', 'running'))`,
		env.ID, run.ID, j.ID, run.Commit, run.ref(), nullID(actor))
	return waiting, err
}

// Environments lists the environments of a repository with their latest deployments.
func (s *Service) Environments(ctx context.Context, p *identity.Principal, key, slug string) ([]Environment, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+envColumns+" FROM environments WHERE repository_id = $1 ORDER BY name", repo.ID)
	if err != nil {
		return nil, err
	}
	envs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*envRow, error) { return scanEnv(r) })
	if err != nil {
		return nil, err
	}
	out := []Environment{}
	for _, e := range envs {
		deps, err := s.deployments(ctx, p, e.ID, 1, 0, "")
		if err != nil {
			return nil, err
		}
		if len(deps) > 0 {
			e.Last = &deps[0]
		}
		ok, err := s.deployments(ctx, p, e.ID, 1, 0, "succeeded")
		if err != nil {
			return nil, err
		}
		if len(ok) > 0 {
			e.Current = &ok[0]
		}
		e.Refs, e.DeployerUsers, e.DeployerGroups = nonNil(e.Refs), nonNil(e.DeployerUsers), nonNil(e.DeployerGroups)
		e.ApproverUsers, e.ApproverGroups = nonNil(e.ApproverUsers), nonNil(e.ApproverGroups)
		users, err := identity.GetUserRefs(ctx, s.pool, append(append([]int64{}, e.DeployerUsers...), e.ApproverUsers...))
		if err != nil {
			return nil, err
		}
		e.Users = []identity.UserRef{}
		for _, u := range users {
			e.Users = append(e.Users, u)
		}
		groups, err := identity.GroupNames(ctx, s.pool, append(append([]int64{}, e.DeployerGroups...), e.ApproverGroups...))
		if err != nil {
			return nil, err
		}
		e.Groups = []NamedGroup{}
		for id, name := range groups {
			e.Groups = append(e.Groups, NamedGroup{ID: id, Name: name})
		}
		out = append(out, e.Environment)
	}
	return out, nil
}

// Deployments lists a page of the deployments of an environment, newest first, with their number.
func (s *Service) Deployments(ctx context.Context, p *identity.Principal, key, slug, env string, limit, offset int) ([]Deployment, int, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return nil, 0, err
	}
	var id int64
	if err := s.pool.QueryRow(ctx, "SELECT id FROM environments WHERE repository_id = $1 AND name = $2", repo.ID, env).Scan(&id); err != nil {
		if db.IsNoRows(err) {
			return nil, 0, apperr.NotFound("environment.not-found", "The environment %s does not exist.", env)
		}
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE environment_id = $1", id).Scan(&total); err != nil {
		return nil, 0, err
	}
	list, err := s.deployments(ctx, p, id, pageLimit(limit), max(offset, 0), "")
	return list, total, err
}

func (s *Service) deployments(ctx context.Context, p *identity.Principal, envID int64, limit, offset int, state string) ([]Deployment, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id, e.name, d.run_id, r.number, d.job_id, d.commit_sha, d.ref, d.state, coalesce(d.created_by, 0),
		d.created_at, d.finished_at FROM deployments d JOIN environments e ON e.id = d.environment_id JOIN runs r ON r.id = d.run_id
		WHERE d.environment_id = $1 AND ($3 = '' OR d.state = $3) ORDER BY d.created_at DESC, d.id DESC LIMIT $2 OFFSET $4`,
		envID, limit, state, offset)
	if err != nil {
		return nil, err
	}
	return s.collectDeployments(ctx, p, envID, rows)
}

// deployment returns one deployment (the caller checked the access).
func (s *Service) deployment(ctx context.Context, p *identity.Principal, id int64) (*Deployment, error) {
	var envID int64
	if err := s.pool.QueryRow(ctx, "SELECT environment_id FROM deployments WHERE id = $1", id).Scan(&envID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT d.id, e.name, d.run_id, r.number, d.job_id, d.commit_sha, d.ref, d.state, coalesce(d.created_by, 0),
		d.created_at, d.finished_at FROM deployments d JOIN environments e ON e.id = d.environment_id JOIN runs r ON r.id = d.run_id
		WHERE d.id = $1`, id)
	if err != nil {
		return nil, err
	}
	list, err := s.collectDeployments(ctx, p, envID, rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Service) collectDeployments(ctx context.Context, p *identity.Principal, envID int64, rows pgx.Rows) ([]Deployment, error) {
	type depRow struct {
		Deployment
		by int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (depRow, error) {
		var d depRow
		err := r.Scan(&d.ID, &d.Environment, &d.RunID, &d.RunNumber, &d.JobID, &d.Commit, &d.Ref, &d.State, &d.by, &d.Created, &d.Finished)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	env, err := scanEnv(s.pool.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE id = $1", envID))
	if err != nil {
		return nil, err
	}
	// The approvals and the people of a page of deployments, in one query each.
	ids, by := make([]int64, len(list)), make([]int64, len(list))
	for i, d := range list {
		ids[i], by[i] = d.ID, d.by
	}
	approvals, err := s.approvals(ctx, ids)
	if err != nil {
		return nil, err
	}
	users, err := identity.GetUserRefs(ctx, s.pool, by)
	if err != nil {
		return nil, err
	}
	out := []Deployment{}
	for _, d := range list {
		if u, ok := users[d.by]; ok {
			d.CreatedBy = &u
		}
		d.Approvals = nonNil(approvals[d.ID])
		d.Required = env.Approvals
		d.CanApprove = d.State == "waiting" && s.mayApprove(env, p, d.Approvals, d.by)
		out = append(out, d.Deployment)
	}
	return out, nil
}

// approvals returns the decisions on deployments, in the order they were made.
func (s *Service) approvals(ctx context.Context, deploymentIDs []int64) (map[int64][]Approval, error) {
	rows, err := s.pool.Query(ctx, `SELECT a.deployment_id, u.id, u.username, u.display_name, u.status = 'active', a.approved, a.comment, a.at
		FROM deployment_approvals a JOIN users u ON u.id = a.user_id WHERE a.deployment_id = ANY($1) ORDER BY a.at, a.user_id`, deploymentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Approval{}
	for rows.Next() {
		var id int64
		var a Approval
		if err := rows.Scan(&id, &a.User.ID, &a.User.Username, &a.User.DisplayName, &a.User.Active, &a.Approved, &a.Comment, &a.At); err != nil {
			return nil, err
		}
		out[id] = append(out[id], a)
	}
	return out, rows.Err()
}

// mayApprove tells whether p may decide on a deployment: an approver of the environment who neither started the
// deployment (four eyes) nor decided before.
func (s *Service) mayApprove(env *envRow, p *identity.Principal, approvals []Approval, startedBy int64) bool {
	if p == nil || !p.Authenticated() || env.Approvals == 0 || p.UserID() == startedBy {
		return false
	}
	for _, a := range approvals {
		if a.User.ID == p.UserID() {
			return false
		}
	}
	return holds(env.ApproverUsers, env.ApproverGroups, p)
}

// EnvironmentInput configures an environment.
type EnvironmentInput struct {
	Name           string   `json:"name,omitempty" maxLength:"63" doc:"Taken from the path"`
	Description    string   `json:"description,omitempty" maxLength:"2000"`
	Refs           []string `json:"refs"`
	DeployerUsers  []int64  `json:"deployerUsers"`
	DeployerGroups []int64  `json:"deployerGroups"`
	Approvals      int      `json:"approvals" minimum:"0" maximum:"10"`
	ApproverUsers  []int64  `json:"approverUsers"`
	ApproverGroups []int64  `json:"approverGroups"`
}

// SaveEnvironment creates or changes an environment (repository administrators).
func (s *Service) SaveEnvironment(ctx context.Context, p *identity.Principal, key, slug string, in EnvironmentInput) error {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return err
	}
	if !envNamePattern.MatchString(in.Name) {
		return apperr.Invalid("environment.name", "The name may have lower-case letters, digits, - and _.")
	}
	if in.Approvals > 0 && len(in.ApproverUsers)+len(in.ApproverGroups) == 0 {
		return apperr.Invalid("environment.approvers", "Name the users or groups who approve deployments.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO environments (repository_id, name, description, refs, deployer_users, deployer_groups, approvals,
			approver_users, approver_groups) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (repository_id, name) DO UPDATE SET
			description = EXCLUDED.description, refs = EXCLUDED.refs, deployer_users = EXCLUDED.deployer_users, deployer_groups = EXCLUDED.deployer_groups,
			approvals = EXCLUDED.approvals, approver_users = EXCLUDED.approver_users, approver_groups = EXCLUDED.approver_groups`, repo.ID, in.Name,
			in.Description, nonNil(in.Refs), nonNil(in.DeployerUsers), nonNil(in.DeployerGroups), in.Approvals, nonNil(in.ApproverUsers),
			nonNil(in.ApproverGroups)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "environment.save", TargetType: "repository",
			TargetID: repo.ProjectKey + "/" + repo.Slug, Details: map[string]any{"environment": in.Name}})
	})
}

// DeleteEnvironment removes an environment with its deployment history.
func (s *Service) DeleteEnvironment(ctx context.Context, p *identity.Principal, key, slug, name string) error {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM environments WHERE repository_id = $1 AND name = $2", repo.ID, name)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.NotFound("environment.not-found", "The environment %s does not exist.", name)
	}
	return err
}

// Approve approves or rejects a waiting deployment; with enough approvals the job starts, a rejection fails it.
func (s *Service) Approve(ctx context.Context, p *identity.Principal, deploymentID int64, approved bool, comment string) error {
	var repoID, envID, jobID int64
	var state string
	var startedBy int64
	if err := s.pool.QueryRow(ctx, `SELECT e.repository_id, e.id, d.job_id, d.state, coalesce(d.created_by, 0) FROM deployments d
		JOIN environments e ON e.id = d.environment_id WHERE d.id = $1`, deploymentID).Scan(&repoID, &envID, &jobID, &state, &startedBy); err != nil {
		if db.IsNoRows(err) {
			return apperr.NotFound("deployment.not-found", "The deployment does not exist.")
		}
		return err
	}
	if l, err := s.repos.Level(ctx, p, repoID); err != nil || l < LevelRead {
		return apperr.NotFound("deployment.not-found", "The deployment does not exist.")
	}
	env, err := scanEnv(s.pool.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE id = $1", envID))
	if err != nil {
		return err
	}
	decided, err := s.approvals(ctx, []int64{deploymentID})
	if err != nil {
		return err
	}
	if state != "waiting" || !s.mayApprove(env, p, decided[deploymentID], startedBy) {
		return apperr.Forbidden("deployment.approve-denied", "You cannot approve this deployment.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO deployment_approvals (deployment_id, user_id, approved, comment) VALUES ($1, $2, $3, $4)
			ON CONFLICT (deployment_id, user_id) DO NOTHING`, deploymentID, p.UserID(), approved, comment); err != nil {
			return err
		}
		j, err := scanJob(tx.QueryRow(ctx, "SELECT "+jobColumns+" FROM run_jobs WHERE id = $1 FOR UPDATE", jobID))
		if err != nil {
			return err
		}
		if j.State != JobWaiting || j.WaitingFor != "approval" {
			return nil
		}
		if !approved {
			if err := s.setJobState(ctx, tx, j, JobFailed, "The deployment was rejected by "+p.User.DisplayName+"."); err != nil {
				return err
			}
			return s.advance(ctx, tx, j.RunID)
		}
		var yes int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM deployment_approvals WHERE deployment_id = $1 AND approved", deploymentID).Scan(&yes); err != nil {
			return err
		}
		if yes >= env.Approvals {
			if _, err := tx.Exec(ctx, "UPDATE run_jobs SET state = 'pending', waiting_for = '', queued_at = now() WHERE id = $1", jobID); err != nil {
				return err
			}
			if err := jobsChanged(ctx, tx); err != nil {
				return err
			}
		}
		return s.advance(ctx, tx, j.RunID)
	})
	return err
}
