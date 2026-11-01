// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
)

// Automation rules: events of code and builds change the issues they name — "pull request opened →
// In Review", "merged → Done". Actions are done as Kogge itself and logged per rule. A rule acts with the access of
// its owner (who last saved it): it reacts only to events of repositories the owner can read, on issues the owner
// may see, while the owner administers the project, and only if who caused the event may see the issue too — naming
// an issue key somewhere else does not drive the issue.

// Triggers of rules.
var Triggers = []string{"branch.created", "commit.pushed", "pull-request.opened", "pull-request.merged", "pull-request.declined",
	"build.failed", "deployment.succeeded"}

// Actions of rules.
var Actions = []string{"transition", "comment", "assign", "fix-version"}

// Rule is an automation rule of a project.
type Rule struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Trigger     string         `json:"trigger" enum:"branch.created,commit.pushed,pull-request.opened,pull-request.merged,pull-request.declined,build.failed,deployment.succeeded"`
	Environment string         `json:"environment,omitempty" doc:"deployment.succeeded: only this environment"`
	IssueTypes  []int64        `json:"issueTypes" doc:"Only issues of these types (empty: all)"`
	Statuses    []int64        `json:"statuses" doc:"Only issues in these statuses (empty: all)"`
	Action      string         `json:"action" enum:"transition,comment,assign,fix-version"`
	Params      map[string]any `json:"params" doc:"transition: {status}; comment: {text}; assign: {to: author|committer|user, user}; fix-version: {version} (0: the next unreleased)"`
	Active      bool           `json:"active"`
	LastRuns    []RuleRun      `json:"lastRuns,omitempty"`
	// Owner is who last saved the rule: it acts with their access.
	Owner *identity.UserRef `json:"owner,omitempty" doc:"The rule acts with this person's access"`
}

// RuleRun is what a rule did to an issue.
type RuleRun struct {
	IssueKey   string    `json:"issueKey"`
	Occurrence string    `json:"occurrence"`
	Result     string    `json:"result" enum:"done,skipped,failed"`
	Message    string    `json:"message,omitempty"`
	At         time.Time `json:"at"`
}

// automationEvent is an occurrence of a trigger with the issues it names.
type automationEvent struct {
	Trigger     string   `json:"trigger"`
	Occurrence  string   `json:"occurrence"`
	Keys        []string `json:"keys"`
	Environment string   `json:"environment,omitempty"`
	RepoID      int64    `json:"repoId"`                // where it happened
	ActorID     int64    `json:"actorId,omitempty"`     // who caused it (pusher, who opened, merged or declined, who started the run)
	AuthorID    int64    `json:"authorId,omitempty"`    // pull request author or pusher
	CommitterID int64    `json:"committerId,omitempty"` // author of the commit
	// Description says what happened, for comments: issue readers may not be able to read the repository, so it
	// names no repository, branch, title or commit message (those are in the issue's development section).
	Description string `json:"description"`
}

// AutomationArgs runs the rules for an event of code or builds.
type AutomationArgs struct {
	Event    string           `json:"event"`
	RepoID   int64            `json:"repoId"`
	PRID     int64            `json:"prId,omitempty"`
	PushID   int64            `json:"pushId,omitempty"`
	RunID    int64            `json:"runId,omitempty"`
	ActorID  int64            `json:"actorId,omitempty"`
	Updates  []code.RefUpdate `json:"updates,omitempty"`
	DeployID int64            `json:"deployId,omitempty"`
	Env      string           `json:"env,omitempty"`
	Commit   string           `json:"commit,omitempty"`
}

// Kind implements river.JobArgs.
func (AutomationArgs) Kind() string { return "links.automation" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (AutomationArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueEvents} }

type automationWorker struct {
	river.WorkerDefaults[AutomationArgs]
	s *Service
}

func (w *automationWorker) Work(ctx context.Context, job *river.Job[AutomationArgs]) error {
	evs, err := w.s.automationEvents(ctx, job.Args)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		ev.RepoID = job.Args.RepoID
		if err := w.s.runRules(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// registerAutomation subscribes the rules engine to the events of code and builds.
func (s *Service) registerAutomation(reg *jobs.Registry, bus *events.Bus) {
	river.AddWorker(reg.Workers, &automationWorker{s: s})
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		switch e := ev.(type) {
		case code.RepoEvent:
			return AutomationArgs{Event: e.Name, RepoID: e.RepoID, PRID: e.PRID, PushID: e.PushID, ActorID: e.ActorID, Updates: e.Updates}, nil
		case builds.RunEvent:
			return AutomationArgs{Event: e.Name, RepoID: e.RepoID, RunID: e.RunID}, nil
		case builds.DeploymentEvent:
			return AutomationArgs{Event: e.Name, RepoID: e.RepoID, RunID: e.RunID, DeployID: e.DeploymentID, Env: e.Environment, Commit: e.Commit}, nil
		}
		return nil, nil
	}, "repository.pushed", "pull-request.opened", "pull-request.merged", "pull-request.declined", "run.failed", "deployment.succeeded")
}

func (s *Service) keysOf(ctx context.Context, texts ...string) ([]string, error) {
	found := docmodel.FindIssueKeys(strings.Join(texts, " "))
	return s.existing(ctx, s.pool, found)
}

func (s *Service) userByEmail(ctx context.Context, email string) int64 {
	users, err := identity.UsersByEmail(ctx, s.pool, []string{email})
	if err != nil {
		return 0
	}
	if u, ok := users[strings.ToLower(email)]; ok && u.Active {
		return u.ID
	}
	return 0
}

// automationEvents turns an event of code or builds into trigger occurrences with the issues they name.
func (s *Service) automationEvents(ctx context.Context, a AutomationArgs) ([]automationEvent, error) {
	var out []automationEvent
	repo, err := s.code.RepoInfo(ctx, a.RepoID)
	if err != nil || repo == nil {
		return nil, nil // the repository is gone
	}
	switch {
	case a.Event == "repository.pushed":
		for _, u := range a.Updates {
			branch, ok := strings.CutPrefix(u.Ref, "refs/heads/")
			if !ok || !u.Created() {
				continue
			}
			keys, err := s.keysOf(ctx, branch)
			if err != nil {
				return nil, err
			}
			if len(keys) > 0 {
				out = append(out, automationEvent{Trigger: "branch.created", Occurrence: fmt.Sprintf("branch:%d:%s", a.RepoID, branch), Keys: keys,
					ActorID: a.ActorID, AuthorID: a.ActorID, Description: "A branch for this issue was created."})
			}
		}
		commits, err := s.code.NewCommits(ctx, a.RepoID, a.Updates, 500)
		if err != nil {
			return nil, err
		}
		for _, c := range commits {
			keys, err := s.keysOf(ctx, c.Message)
			if err != nil {
				return nil, err
			}
			if len(keys) > 0 {
				out = append(out, automationEvent{Trigger: "commit.pushed", Occurrence: "commit:" + c.SHA, Keys: keys, ActorID: a.ActorID,
					AuthorID: a.ActorID, CommitterID: s.userByEmail(ctx, c.Author.Email),
					Description: fmt.Sprintf("Commit %s naming this issue was pushed.", c.SHA[:min(10, len(c.SHA))])})
			}
		}
	case strings.HasPrefix(a.Event, "pull-request."):
		pr, err := s.code.PullRequestInfo(ctx, a.PRID)
		if err != nil {
			if db.IsNoRows(err) || apperr.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		keys, err := s.keysOf(ctx, pr.Title, pr.Source, pr.Description)
		if err != nil {
			return nil, err
		}
		ref, _ := s.code.PullRequestByID(ctx, a.PRID)
		var author int64
		if ref != nil {
			author = ref.AuthorID
		}
		verb := strings.TrimPrefix(a.Event, "pull-request.")
		if len(keys) > 0 {
			out = append(out, automationEvent{Trigger: a.Event, Occurrence: fmt.Sprintf("pull-request:%d:%s", a.PRID, verb), Keys: keys,
				ActorID: a.ActorID, AuthorID: author, Description: fmt.Sprintf("A pull request naming this issue was %s.", verb)})
		}
	case a.Event == "run.failed":
		run, err := s.builds.RunInfo(ctx, a.RunID)
		if err != nil {
			return nil, nil
		}
		var keys []string
		rows, err := s.pool.Query(ctx, "SELECT DISTINCT issue_key FROM issue_commits WHERE repository_id = $1 AND sha = $2", a.RepoID, run.Commit)
		if err != nil {
			return nil, err
		}
		if keys, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, err
		}
		if run.Branch != "" {
			more, err := s.keysOf(ctx, run.Branch)
			if err != nil {
				return nil, err
			}
			keys = append(keys, more...)
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		if len(keys) > 0 {
			var author int64
			if run.TriggeredBy != nil {
				author = run.TriggeredBy.ID
			}
			out = append(out, automationEvent{Trigger: "build.failed", Occurrence: "run:" + strconv.FormatInt(a.RunID, 10), Keys: keys,
				ActorID: author, AuthorID: author, Description: "A build of changes for this issue failed."})
		}
	case a.Event == "deployment.succeeded":
		// The issues of the commits that this deployment brought to the environment.
		var prev string
		err := s.pool.QueryRow(ctx, `SELECT d.commit_sha FROM deployments d JOIN environments e ON e.id = d.environment_id
			WHERE e.repository_id = $1 AND e.name = $2 AND d.state = 'succeeded' AND d.id < $3 ORDER BY d.id DESC LIMIT 1`,
			a.RepoID, a.Env, a.DeployID).Scan(&prev)
		if err != nil && !db.IsNoRows(err) {
			return nil, err
		}
		commits, err := s.code.CommitsBetween(ctx, a.RepoID, prev, a.Commit, 1000)
		if err != nil {
			return nil, err
		}
		var keys []string
		for _, c := range commits {
			k, err := s.keysOf(ctx, c.Message)
			if err != nil {
				return nil, err
			}
			keys = append(keys, k...)
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		if len(keys) > 0 {
			var deployer int64
			if err := s.pool.QueryRow(ctx, "SELECT coalesce(created_by, 0) FROM deployments WHERE id = $1", a.DeployID).Scan(&deployer); err != nil &&
				!db.IsNoRows(err) {
				return nil, err
			}
			out = append(out, automationEvent{Trigger: "deployment.succeeded", Occurrence: "deployment:" + strconv.FormatInt(a.DeployID, 10),
				Keys: keys, Environment: a.Env, ActorID: deployer, AuthorID: deployer,
				Description: fmt.Sprintf("The changes for this issue were deployed to %s.", a.Env)})
		}
	}
	return out, nil
}

const ruleColumns = "id, project_id, name, trigger, environment, issue_types, statuses, action, params, active, coalesce(owner_id, 0)"

type ruleRow struct {
	Rule
	projectID int64
	ownerID   int64
}

func scanRule(row pgx.Row) (*ruleRow, error) {
	var r ruleRow
	var params []byte
	err := row.Scan(&r.ID, &r.projectID, &r.Name, &r.Trigger, &r.Environment, &r.IssueTypes, &r.Statuses, &r.Action, &params, &r.Active,
		&r.ownerID)
	if err != nil {
		return nil, err
	}
	r.Params = map[string]any{}
	_ = json.Unmarshal(params, &r.Params)
	r.IssueTypes, r.Statuses = nonNilIDs(r.IssueTypes), nonNilIDs(r.Statuses)
	return &r, nil
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func paramID(p map[string]any, key string) int64 {
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case string:
		id, _ := strconv.ParseInt(v, 10, 64)
		return id
	}
	return 0
}

func paramString(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// runRules applies the active rules of the issues' projects to an occurrence.
func (s *Service) runRules(ctx context.Context, ev automationEvent) error {
	system := identity.SystemPrincipal()
	for _, key := range ev.Keys {
		issue, err := s.issues.GetIssue(ctx, system, key)
		if err != nil {
			continue // deleted or moved in the meantime
		}
		rows, err := s.pool.Query(ctx, "SELECT "+ruleColumns+" FROM automation_rules WHERE project_id = $1 AND trigger = $2 AND active ORDER BY id",
			issue.Project.ID, ev.Trigger)
		if err != nil {
			return err
		}
		rules, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*ruleRow, error) { return scanRule(r) })
		if err != nil {
			return err
		}
		for _, r := range rules {
			if r.Environment != "" && r.Environment != ev.Environment {
				continue
			}
			// Once per rule, issue and occurrence (jobs may run again).
			tag, err := s.pool.Exec(ctx, `INSERT INTO automation_runs (rule_id, issue_key, occurrence, result) VALUES ($1, $2, $3, 'skipped')
				ON CONFLICT DO NOTHING`, r.ID, key, ev.Occurrence)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			// Conditions are checked against the issue as it is now (an earlier rule may have changed it).
			if cur, err := s.issues.GetIssue(ctx, system, key); err == nil {
				issue = cur
			}
			result, message := "skipped", ""
			if message, err = s.ruleMayAct(ctx, r, issue, ev); err != nil {
				return err
			}
			if message == "" {
				result, message = s.apply(ctx, system, r, issue, ev)
			}
			if _, err := s.pool.Exec(ctx, "UPDATE automation_runs SET result = $4, message = $5 WHERE rule_id = $1 AND issue_key = $2 AND occurrence = $3",
				r.ID, key, ev.Occurrence, result, truncate(message, 1000)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ruleMayAct returns why a rule must not act on an issue for an event, or "" if it may. The rule acts with its
// owner's access: the owner must be active, still administer the project, read the repository the event came from
// and see the issue; and who caused the event must see the issue too.
func (s *Service) ruleMayAct(ctx context.Context, r *ruleRow, issue *issues.Issue, ev automationEvent) (string, error) {
	if r.ownerID == 0 {
		return "The rule has no owner; a project administrator must save it again.", nil
	}
	owner, err := identity.LoadPrincipal(ctx, s.pool, r.ownerID, identity.MethodInternal)
	if err != nil {
		if apperr.Is(err, "auth.user-inactive") || db.IsNoRows(err) || apperr.IsNotFound(err) {
			return "The owner of the rule is no longer active.", nil
		}
		return "", err
	}
	if _, err := s.issues.AdminProjectID(ctx, owner, issue.Project.Key); err != nil {
		var ae *apperr.Error
		if errorsAs(err, &ae) {
			return "The owner of the rule no longer administers the project.", nil
		}
		return "", err
	}
	level, err := s.code.AccessLevel(ctx, owner, ev.RepoID)
	if err != nil && !db.IsNoRows(err) {
		return "", err
	}
	if level < code.LevelRead {
		return "The owner of the rule cannot read the repository the event came from.", nil
	}
	if ok, err := s.issues.UserCanSeeIssue(ctx, r.ownerID, issue.Key); err != nil {
		return "", err
	} else if !ok {
		return "The owner of the rule may not see the issue.", nil
	}
	if ev.ActorID != 0 {
		if ok, err := s.issues.UserCanSeeIssue(ctx, ev.ActorID, issue.Key); err != nil {
			return "", err
		} else if !ok {
			return "Who caused the event may not see the issue.", nil
		}
	}
	return "", nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// apply checks the conditions of a rule and runs its action on the issue.
func (s *Service) apply(ctx context.Context, p *identity.Principal, r *ruleRow, issue *issues.Issue, ev automationEvent) (string, string) {
	if len(r.IssueTypes) > 0 && (issue.Type == nil || !slices.Contains(r.IssueTypes, issue.Type.ID)) {
		return "skipped", "The issue type does not match."
	}
	if len(r.Statuses) > 0 && (issue.Status == nil || !slices.Contains(r.Statuses, issue.Status.ID)) {
		return "skipped", "The status does not match."
	}
	var err error
	switch r.Action {
	case "transition":
		status := paramID(r.Params, "status")
		if issue.Status != nil && issue.Status.ID == status {
			return "skipped", "The issue is in the status already."
		}
		_, err = s.issues.TransitionByStatus(ctx, p, issue.Key, status)
	case "comment":
		text := strings.TrimSpace(paramString(r.Params, "text"))
		if text == "" {
			text = ev.Description
		}
		_, err = s.issues.AddComment(ctx, p, issue.Key, issues.CommentInput{Markdown: text})
	case "assign":
		var user int64
		switch paramString(r.Params, "to") {
		case "author":
			user = ev.AuthorID
		case "committer":
			user = ev.CommitterID
		default:
			user = paramID(r.Params, "user")
		}
		if user == 0 {
			return "skipped", "Nobody to assign: the person is not a Kogge user."
		}
		raw, _ := json.Marshal(user)
		_, err = s.issues.UpdateIssue(ctx, p, issue.Key, 0, map[string]json.RawMessage{issues.FieldAssignee: raw})
	case "fix-version":
		version := paramID(r.Params, "version")
		if version == 0 {
			if version, err = s.issues.NextVersion(ctx, issue.Project.ID); err != nil {
				return "failed", err.Error()
			}
			if version == 0 {
				return "skipped", "The project has no unreleased version."
			}
		}
		ids := []int64{}
		for _, v := range issue.FixVersions {
			if v.ID == version {
				return "skipped", "The issue has the version already."
			}
			ids = append(ids, v.ID)
		}
		raw, _ := json.Marshal(append(ids, version))
		_, err = s.issues.UpdateIssue(ctx, p, issue.Key, 0, map[string]json.RawMessage{issues.FieldFixVersions: raw})
	default:
		return "failed", "Unknown action."
	}
	if err != nil {
		var ae *apperr.Error
		if errorsAs(err, &ae) {
			return "failed", ae.Detail
		}
		return "failed", err.Error()
	}
	return "done", ev.Description
}

// RuleInput creates or changes a rule.
type RuleInput struct {
	Name        string         `json:"name" minLength:"1" maxLength:"100"`
	Trigger     string         `json:"trigger" enum:"branch.created,commit.pushed,pull-request.opened,pull-request.merged,pull-request.declined,build.failed,deployment.succeeded"`
	Environment string         `json:"environment,omitempty" maxLength:"63"`
	IssueTypes  []int64        `json:"issueTypes" maxItems:"50"`
	Statuses    []int64        `json:"statuses" maxItems:"50"`
	Action      string         `json:"action" enum:"transition,comment,assign,fix-version"`
	Params      map[string]any `json:"params"`
	Active      bool           `json:"active"`
}

func validateRule(in *RuleInput) error {
	switch in.Action {
	case "transition":
		if paramID(in.Params, "status") == 0 {
			return apperr.Invalid("automation.status", "Choose the status to move the issue to.")
		}
	case "comment":
		if len(paramString(in.Params, "text")) > 10000 {
			return apperr.Invalid("automation.comment", "The comment is too long.")
		}
	case "assign":
		switch paramString(in.Params, "to") {
		case "author", "committer":
		case "user":
			if paramID(in.Params, "user") == 0 {
				return apperr.Invalid("automation.user", "Choose the user to assign.")
			}
		default:
			return apperr.Invalid("automation.assign", "Assign to the author, the committer or a user.")
		}
	}
	if in.Params == nil {
		in.Params = map[string]any{}
	}
	in.IssueTypes, in.Statuses = nonNilIDs(in.IssueTypes), nonNilIDs(in.Statuses)
	return nil
}

// Rules lists the rules of a project with their latest runs (project administrators).
func (s *Service) Rules(ctx context.Context, p *identity.Principal, key string) ([]Rule, error) {
	projectID, err := s.issues.AdminProjectID(ctx, p, key)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+ruleColumns+" FROM automation_rules WHERE project_id = $1 ORDER BY id", projectID)
	if err != nil {
		return nil, err
	}
	rules, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*ruleRow, error) { return scanRule(r) })
	if err != nil {
		return nil, err
	}
	out := []Rule{}
	for _, r := range rules {
		rows, err := s.pool.Query(ctx, `SELECT issue_key, occurrence, result, message, at FROM automation_runs WHERE rule_id = $1
			ORDER BY at DESC LIMIT 10`, r.ID)
		if err != nil {
			return nil, err
		}
		if r.LastRuns, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RuleRun]); err != nil {
			return nil, err
		}
		r.LastRuns = nonNilRuns(r.LastRuns)
		r.Owner = s.userRef(ctx, r.ownerID)
		out = append(out, r.Rule)
	}
	return out, nil
}

func nonNilRuns(r []RuleRun) []RuleRun {
	if r == nil {
		return []RuleRun{}
	}
	return r
}

// SaveRule creates (id 0) or changes a rule.
func (s *Service) SaveRule(ctx context.Context, p *identity.Principal, key string, id int64, in RuleInput) (*Rule, error) {
	projectID, err := s.issues.AdminProjectID(ctx, p, key)
	if err != nil {
		return nil, err
	}
	if err := validateRule(&in); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(in.Params)
	var r *ruleRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			r, err = scanRule(tx.QueryRow(ctx, `INSERT INTO automation_rules (project_id, name, trigger, environment, issue_types, statuses, action,
				params, active, created_by, owner_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10) RETURNING `+ruleColumns, projectID,
				strings.TrimSpace(in.Name), in.Trigger, in.Environment, in.IssueTypes, in.Statuses, in.Action, params, in.Active, p.UserID()))
		} else {
			// Whoever saves a rule becomes its owner: it acts with their access.
			r, err = scanRule(tx.QueryRow(ctx, `UPDATE automation_rules SET name = $3, trigger = $4, environment = $5, issue_types = $6,
				statuses = $7, action = $8, params = $9, active = $10, owner_id = $11, updated_at = now()
				WHERE id = $1 AND project_id = $2 RETURNING `+ruleColumns,
				id, projectID, strings.TrimSpace(in.Name), in.Trigger, in.Environment, in.IssueTypes, in.Statuses, in.Action, params, in.Active,
				p.UserID()))
			if db.IsNoRows(err) {
				return apperr.NotFound("automation.not-found", "The rule does not exist.")
			}
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "automation.save", TargetType: "project", TargetID: key,
			Details: map[string]any{"rule": r.ID, "trigger": in.Trigger, "action": in.Action}})
	})
	if err != nil {
		return nil, err
	}
	r.LastRuns = []RuleRun{}
	r.Owner = s.userRef(ctx, r.ownerID)
	return &r.Rule, nil
}

func (s *Service) userRef(ctx context.Context, id int64) *identity.UserRef {
	if id == 0 {
		return nil
	}
	refs, err := identity.GetUserRefs(ctx, s.pool, []int64{id})
	if err != nil {
		return nil
	}
	if u, ok := refs[id]; ok {
		return &u
	}
	return nil
}

// DeleteRule removes a rule.
func (s *Service) DeleteRule(ctx context.Context, p *identity.Principal, key string, id int64) error {
	projectID, err := s.issues.AdminProjectID(ctx, p, key)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM automation_rules WHERE id = $1 AND project_id = $2", id, projectID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("automation.not-found", "The rule does not exist.")
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "automation.delete", TargetType: "project", TargetID: key,
			Details: map[string]any{"rule": id}})
	})
}

func errorsAs(err error, target **apperr.Error) bool { return errors.As(err, target) }
