// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package links connects the modules: issue keys in commits, branches and pull requests, the
// development section of issues and commit commands. Modules never import each other; this package uses their
// services and events.
package links

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/wiki"
)

// Service links issues with code.
type Service struct {
	pool     *pgxpool.Pool
	issues   *issues.Service
	code     *code.Service
	builds   *builds.Service
	wiki     *wiki.Service
	identity *identity.Service
	authz    *authz.Service
	log      *slog.Logger
	// slots bounds the database work that the searches and activity streams of all requests do at once on this
	// node (searchSlots): a product search or activity source holds a slot while it runs.
	slots chan struct{}
}

// SetBuilds connects the builds module (builds and deployments of issues).
func (s *Service) SetBuilds(b *builds.Service) { s.builds = b }

// SetWiki connects the wiki module (the spaces of projects).
func (s *Service) SetWiki(w *wiki.Service) { s.wiki = w }

// SetAuthz connects the global permissions (who may find people).
func (s *Service) SetAuthz(az *authz.Service) { s.authz = az }

// NewService creates the service.
func NewService(pool *pgxpool.Pool, is *issues.Service, cs *code.Service, id *identity.Service, logger *slog.Logger) *Service {
	return &Service{pool: pool, issues: is, code: cs, identity: id, log: logger,
		slots: make(chan struct{}, searchSlots(pool))}
}

// PushArgs indexes a push: its commits and branches.
type PushArgs struct {
	RepoID int64 `json:"repoId"`
	PushID int64 `json:"pushId"`
}

// Kind implements river.JobArgs.
func (PushArgs) Kind() string { return "links.push" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (PushArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueGit} }

// PullRequestArgs indexes the issue keys of a pull request.
type PullRequestArgs struct {
	PRID int64 `json:"prId"`
}

// Kind implements river.JobArgs.
func (PullRequestArgs) Kind() string { return "links.pull-request" }

type pushWorker struct {
	river.WorkerDefaults[PushArgs]
	s *Service
}

func (w *pushWorker) Work(ctx context.Context, job *river.Job[PushArgs]) error {
	return w.s.indexPush(ctx, job.Args.RepoID, job.Args.PushID)
}

type prWorker struct {
	river.WorkerDefaults[PullRequestArgs]
	s *Service
}

func (w *prWorker) Work(ctx context.Context, job *river.Job[PullRequestArgs]) error {
	return w.s.indexPullRequest(ctx, job.Args.PRID)
}

// Register adds the jobs and subscribes to the events of the modules.
func (s *Service) Register(reg *jobs.Registry, bus *events.Bus) {
	s.registerAutomation(reg, bus)
	river.AddWorker(reg.Workers, &pushWorker{s: s})
	river.AddWorker(reg.Workers, &prWorker{s: s})
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(code.RepoEvent); ok && e.PushID != 0 {
			return PushArgs{RepoID: e.RepoID, PushID: e.PushID}, nil
		}
		return nil, nil
	}, "repository.pushed")
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(code.RepoEvent); ok && e.PRID != 0 {
			return PullRequestArgs{PRID: e.PRID}, nil
		}
		return nil, nil
	}, "pull-request.opened", "pull-request.updated", "pull-request.merged", "pull-request.declined", "pull-request.reopened")
}

// existing keeps the issue keys whose project exists.
func (s *Service) existing(ctx context.Context, q db.Querier, keys []string) ([]string, error) {
	projects := []string{}
	for _, k := range keys {
		if p, _, ok := strings.Cut(k, "-"); ok && !slices.Contains(projects, p) {
			projects = append(projects, p)
		}
	}
	known, err := s.issues.ExistingProjectKeys(ctx, q, projects)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		if p, _, _ := strings.Cut(k, "-"); known[p] {
			out = append(out, k)
		}
	}
	return out, nil
}

// indexPush records the issue keys of the commits and branches of a push and runs commit commands.
func (s *Service) indexPush(ctx context.Context, repoID, pushID int64) error {
	push, err := s.code.PushOf(ctx, pushID)
	if err != nil {
		if db.IsNoRows(err) {
			return nil
		}
		return err
	}
	commits, err := s.code.NewCommits(ctx, repoID, push.Updates, 1000)
	if err != nil {
		return err
	}
	var touched []string
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, u := range push.Updates {
			branch, ok := strings.CutPrefix(u.Ref, "refs/heads/")
			if !ok {
				continue
			}
			if u.Deleted() {
				var keys []string
				rows, err := tx.Query(ctx, "DELETE FROM issue_branches WHERE repository_id = $1 AND branch = $2 RETURNING issue_key", repoID, branch)
				if err != nil {
					return err
				}
				if keys, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
					return err
				}
				touched = append(touched, keys...)
				continue
			}
			if !u.Created() {
				continue
			}
			keys, err := s.existing(ctx, tx, docmodel.FindIssueKeys(branch))
			if err != nil {
				return err
			}
			for _, k := range keys {
				if _, err := tx.Exec(ctx, `INSERT INTO issue_branches (issue_key, repository_id, branch) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
					k, repoID, branch); err != nil {
					return err
				}
			}
			touched = append(touched, keys...)
		}
		emails := []string{}
		for _, c := range commits {
			emails = append(emails, c.Author.Email)
		}
		users, err := identity.UsersByEmail(ctx, tx, emails)
		if err != nil {
			return err
		}
		for _, c := range commits {
			keys, err := s.existing(ctx, tx, docmodel.FindIssueKeys(c.Message))
			if err != nil {
				return err
			}
			var author any
			if u, ok := users[strings.ToLower(c.Author.Email)]; ok {
				author = u.ID
			}
			for _, k := range keys {
				if _, err := tx.Exec(ctx, `INSERT INTO issue_commits (issue_key, repository_id, sha, message, author_name, author_email, author_id,
					committed_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`, k, repoID, c.SHA, c.Message, c.Author.Name,
					c.Author.Email, author, c.Committer.When); err != nil {
					return err
				}
			}
			touched = append(touched, keys...)
		}
		slices.Sort(touched)
		touched = slices.Compact(touched)
		return s.issues.DevelopmentChanged(ctx, tx, touched)
	})
	if err != nil {
		return err
	}
	// Merges by Kogge carry texts of pull requests in their messages, and the merged commits ran their commands
	// when they were pushed; imported history ran its commands elsewhere.
	if push.Merge() || push.Imported() {
		return nil
	}
	for _, c := range commits {
		if err := s.runCommands(ctx, c, push.UserID); err != nil {
			s.log.Warn("commit commands failed", "commit", c.SHA, "error", err)
		}
	}
	return nil
}

// runCommands runs the commit commands of a commit once per issue, as the user who pushed it, and only if the commit
// is theirs (its author's e-mail address is a verified address of the pusher): anyone can write any author into a
// commit.
func (s *Service) runCommands(ctx context.Context, c git.Commit, pusherID int64) error {
	cmds := ParseCommands(c.Message)
	if len(cmds) == 0 {
		return nil
	}
	users, err := identity.UsersByEmail(ctx, s.pool, []string{c.Author.Email})
	if err != nil {
		return err
	}
	var p *identity.Principal
	skipped := "skipped: the author's e-mail address " + c.Author.Email + " belongs to no verified user"
	switch u, ok := users[strings.ToLower(c.Author.Email)]; {
	case pusherID == 0:
		skipped = "skipped: pushed with a deploy key"
	case ok && u.ID != pusherID:
		skipped = "skipped: the commit of " + c.Author.Email + " was pushed by someone else"
	case ok && u.Active:
		if p, err = s.identity.PrincipalOf(ctx, u.ID, identity.MethodInternal); err != nil {
			p = nil
		}
	}
	byKey := map[string][]Command{}
	var keys []string
	for _, cmd := range cmds {
		for _, k := range cmd.Keys {
			if _, ok := byKey[k]; !ok {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], cmd)
		}
	}
	for _, key := range keys {
		// Once per commit and issue, also when the commit appears in other repositories.
		tag, err := s.pool.Exec(ctx, "INSERT INTO commit_commands_done (sha, issue_key) VALUES ($1, $2) ON CONFLICT DO NOTHING", c.SHA, key)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		var results []string
		if p == nil {
			results = append(results, skipped)
		} else {
			for _, cmd := range byKey[key] {
				if err := s.runCommand(ctx, p, key, cmd, c); err != nil {
					results = append(results, "#"+cmd.Name+": "+err.Error())
				} else {
					results = append(results, "#"+cmd.Name+": done")
				}
			}
		}
		if _, err := s.pool.Exec(ctx, "UPDATE commit_commands_done SET result = $3 WHERE sha = $1 AND issue_key = $2", c.SHA, key,
			strings.Join(results, "; ")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runCommand(ctx context.Context, p *identity.Principal, key string, cmd Command, c git.Commit) error {
	switch cmd.Name {
	case "comment":
		if strings.TrimSpace(cmd.Args) == "" {
			return fmt.Errorf("the comment is empty")
		}
		_, err := s.issues.AddComment(ctx, p, key, issues.CommentInput{Markdown: cmd.Args})
		return err
	case "time":
		spent, comment := splitTime(cmd.Args)
		if spent == "" {
			return fmt.Errorf("no duration such as 1h 30m")
		}
		started := c.Author.When
		if started.IsZero() {
			started = time.Now()
		}
		return s.issues.AddWorklog(ctx, p, key, issues.WorklogInput{TimeSpent: spent, Comment: comment, Started: started})
	default:
		available, err := s.issues.Transitions(ctx, p, key)
		if err != nil {
			return err
		}
		for _, t := range available {
			if strings.ReplaceAll(strings.ToLower(strings.TrimSpace(t.Name)), " ", "-") == cmd.Name {
				_, err := s.issues.Transition(ctx, p, key, issues.TransitionInput{TransitionID: t.ID})
				return err
			}
		}
		return fmt.Errorf("no transition %q for the issue", cmd.Name)
	}
}

// indexPullRequest records the issue keys in a pull request's title, source branch and description.
func (s *Service) indexPullRequest(ctx context.Context, prID int64) error {
	pr, err := s.code.PullRequestInfo(ctx, prID)
	if err != nil {
		if db.IsNoRows(err) {
			return nil
		}
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		found := docmodel.FindIssueKeys(pr.Title + " " + pr.Source + " " + pr.Description)
		keys, err := s.existing(ctx, tx, found)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "DELETE FROM issue_pull_requests WHERE pull_request_id = $1 RETURNING issue_key", prID)
		if err != nil {
			return err
		}
		old, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, k := range keys {
			if _, err := tx.Exec(ctx, "INSERT INTO issue_pull_requests (issue_key, pull_request_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
				k, prID); err != nil {
				return err
			}
		}
		touched := append(old, keys...)
		slices.Sort(touched)
		return s.issues.DevelopmentChanged(ctx, tx, slices.Compact(touched))
	})
}
