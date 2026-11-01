// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

// DevBranch is a branch whose name names the issue.
type DevBranch struct {
	Repository code.RepoRef `json:"repository"`
	Name       string       `json:"name"`
}

// DevCommit is a commit whose message names the issue.
type DevCommit struct {
	Repository code.RepoRef      `json:"repository"`
	SHA        string            `json:"sha"`
	Subject    string            `json:"subject"`
	AuthorName string            `json:"authorName"`
	Author     *identity.UserRef `json:"author,omitempty"`
	When       time.Time         `json:"when"`
}

// CommandResult reports what the commit commands of a commit did to the issue.
type CommandResult struct {
	SHA    string    `json:"sha"`
	Result string    `json:"result"`
	At     time.Time `json:"at"`
}

// DevBuild is the latest run of a pipeline at the issue's commits or branches.
type DevBuild struct {
	Repository code.RepoRef      `json:"repository"`
	Run        builds.RunSummary `json:"run"`
}

// DevDeployment is an environment whose current deployment contains a commit of the issue.
type DevDeployment struct {
	Repository  code.RepoRef `json:"repository"`
	Environment string       `json:"environment"`
	RunNumber   int          `json:"runNumber"`
	Ref         string       `json:"ref"`
	Finished    time.Time    `json:"finished"`
}

// Development is the code of an issue: branches, commits, pull requests, builds and deployments the
// caller may see.
type Development struct {
	Branches []DevBranch `json:"branches" doc:"The newest 100"`
	Commits  []DevCommit `json:"commits" doc:"The newest 200"`
	// The totals count what the caller may see, also beyond the lists.
	BranchesTotal int                `json:"branchesTotal"`
	CommitsTotal  int                `json:"commitsTotal"`
	PullRequests  []code.PullRequest `json:"pullRequests"`
	Commands      []CommandResult    `json:"commands"`
	Builds        []DevBuild         `json:"builds"`
	Deployments   []DevDeployment    `json:"deployments"`
}

// Limits of the development section: the newest branches and commits.
const (
	devBranches = 100
	devCommits  = 200
)

// Development returns the development section of an issue.
func (s *Service) Development(ctx context.Context, p *identity.Principal, key string) (*Development, error) {
	issue, err := s.issues.GetIssue(ctx, p, key)
	if err != nil {
		return nil, err
	}
	if !issue.Permissions.Development {
		return nil, apperr.Forbidden("development.denied", "You may not see the development of %s.", issue.Key)
	}
	// Commits, branches and pull requests may name the issue by a key it had before it moved (or its project's key
	// changed).
	keys, err := s.issues.KeysOf(ctx, s.pool, issue.ID)
	if err != nil {
		return nil, err
	}
	d := &Development{Branches: []DevBranch{}, Commits: []DevCommit{}, PullRequests: []code.PullRequest{}, Commands: []CommandResult{},
		Builds: []DevBuild{}, Deployments: []DevDeployment{}}
	repos := map[int64]*code.RepoRef{}
	repo := func(id int64) *code.RepoRef {
		if r, ok := repos[id]; ok {
			return r
		}
		r, err := s.code.RepoInfo(ctx, id)
		if err != nil {
			r = nil
		}
		repos[id] = r
		return r
	}
	// The repositories of the issue's branches and commits that the caller may read come first: the lists are
	// limited after that, so that branches and commits in hidden repositories do not take the place of visible ones.
	rows, err := s.pool.Query(ctx, `SELECT repository_id FROM issue_branches WHERE issue_key = ANY($1)
		UNION SELECT repository_id FROM issue_commits WHERE issue_key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	readable, err := s.code.ReadableRepos(ctx, p, candidates)
	if err != nil {
		return nil, err
	}
	visible := []int64{}
	for _, id := range candidates {
		if readable[id] {
			visible = append(visible, id)
		}
	}
	// A branch or commit that names the issue by an old key and the current one counts once.
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(DISTINCT (repository_id, branch)) FROM issue_branches WHERE issue_key = ANY($1) AND repository_id = ANY($2)),
		(SELECT count(DISTINCT (repository_id, sha)) FROM issue_commits WHERE issue_key = ANY($1) AND repository_id = ANY($2))`,
		keys, visible).
		Scan(&d.BranchesTotal, &d.CommitsTotal); err != nil {
		return nil, err
	}
	type branchRow struct {
		repo int64
		name string
	}
	rows, err = s.pool.Query(ctx, `SELECT repository_id, branch FROM (SELECT DISTINCT ON (repository_id, branch) repository_id, branch,
		created_at FROM issue_branches WHERE issue_key = ANY($1) AND repository_id = ANY($2)
		ORDER BY repository_id, branch, created_at DESC) b ORDER BY created_at DESC, repository_id, branch LIMIT $3`,
		keys, visible, devBranches)
	if err != nil {
		return nil, err
	}
	branches, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (branchRow, error) {
		var b branchRow
		err := r.Scan(&b.repo, &b.name)
		return b, err
	})
	if err != nil {
		return nil, err
	}
	type commitRow struct {
		DevCommit
		repo     int64
		authorID *int64
	}
	rows, err = s.pool.Query(ctx, `SELECT repository_id, sha, message, author_name, author_id, committed_at FROM (
		SELECT DISTINCT ON (repository_id, sha) repository_id, sha, message, author_name, author_id, committed_at FROM issue_commits
		WHERE issue_key = ANY($1) AND repository_id = ANY($2) ORDER BY repository_id, sha) c
		ORDER BY committed_at DESC, repository_id, sha LIMIT $3`, keys, visible, devCommits)
	if err != nil {
		return nil, err
	}
	commits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (commitRow, error) {
		var c commitRow
		var message string
		err := r.Scan(&c.repo, &c.SHA, &message, &c.AuthorName, &c.authorID, &c.When)
		c.Subject, _, _ = strings.Cut(message, "\n")
		return c, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, "SELECT DISTINCT pull_request_id FROM issue_pull_requests WHERE issue_key = ANY($1) ORDER BY 1", keys)
	if err != nil {
		return nil, err
	}
	prIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, b := range branches {
		if r := repo(b.repo); r != nil && readable[b.repo] {
			d.Branches = append(d.Branches, DevBranch{Repository: *r, Name: b.name})
		}
	}
	userIDs := []int64{}
	for _, c := range commits {
		if c.authorID != nil {
			userIDs = append(userIDs, *c.authorID)
		}
	}
	users, err := identity.GetUserRefs(ctx, s.pool, userIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range commits {
		r := repo(c.repo)
		if r == nil || !readable[c.repo] {
			continue
		}
		c.Repository = *r
		if c.authorID != nil {
			if u, ok := users[*c.authorID]; ok {
				c.Author = &u
			}
		}
		d.Commits = append(d.Commits, c.DevCommit)
	}
	if d.PullRequests, err = s.code.PullRequestsByID(ctx, p, prIDs); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT d.sha, d.result, d.done_at FROM commit_commands_done d WHERE d.issue_key = ANY($1) AND d.result <> ''
		AND EXISTS (SELECT 1 FROM issue_commits c WHERE c.sha = d.sha AND c.issue_key = d.issue_key) ORDER BY d.done_at DESC LIMIT 50`, keys)
	if err != nil {
		return nil, err
	}
	results, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (CommandResult, error) {
		var c CommandResult
		err := r.Scan(&c.SHA, &c.Result, &c.At)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	shown := map[string]bool{}
	for _, c := range d.Commits {
		shown[c.SHA] = true
	}
	for _, c := range results {
		if shown[c.SHA] {
			d.Commands = append(d.Commands, c)
		}
	}
	if s.builds != nil {
		if err := s.addBuilds(ctx, d); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// addBuilds adds the latest runs per pipeline and the environments whose current deployment contains a commit of
// the issue, for the readable repositories already listed.
func (s *Service) addBuilds(ctx context.Context, d *Development) error {
	type repoWork struct {
		ref      code.RepoRef
		commits  []string
		branches []string
	}
	work := map[int64]*repoWork{}
	var order []int64
	get := func(r code.RepoRef) *repoWork {
		if w, ok := work[r.ID]; ok {
			return w
		}
		w := &repoWork{ref: r}
		work[r.ID] = w
		order = append(order, r.ID)
		return w
	}
	for _, c := range d.Commits {
		w := get(c.Repository)
		w.commits = append(w.commits, c.SHA)
	}
	for _, b := range d.Branches {
		w := get(b.Repository)
		w.branches = append(w.branches, b.Name)
	}
	for _, id := range order {
		w := work[id]
		runs, err := s.builds.LatestRuns(ctx, id, w.commits, w.branches)
		if err != nil {
			return err
		}
		for _, r := range runs {
			d.Builds = append(d.Builds, DevBuild{Repository: w.ref, Run: r})
		}
		if len(w.commits) == 0 {
			continue
		}
		deps, err := s.builds.CurrentDeployments(ctx, id)
		if err != nil {
			return err
		}
		for _, dep := range deps {
			for _, c := range w.commits {
				if s.code.Contains(ctx, id, dep.Commit, c) {
					d.Deployments = append(d.Deployments, DevDeployment{Repository: w.ref, Environment: dep.Environment, RunNumber: dep.RunNumber,
						Ref: dep.Ref, Finished: dep.Finished})
					break
				}
			}
		}
	}
	return nil
}

// projectPath addresses an issue project.
type projectPath struct {
	Key string `path:"key"`
}

// rulePath addresses an automation rule of a project.
type rulePath struct {
	Key string `path:"key"`
	ID  int64  `path:"id"`
}

// Handler serves the integration API.
type Handler struct {
	S *Service
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	s := h.S
	h.registerFind(a)
	huma.Register(a, api.Op("get-issue-development", http.MethodGet, "/api/v1/issues/{key}/development",
		"Branches, commits and pull requests of an issue", "Issues"),
		func(ctx context.Context, in *struct {
			Key string `path:"key"`
		}) (*struct{ Body *Development }, error) {
			d, err := s.Development(ctx, identity.From(ctx), in.Key)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *Development }{d}, nil
		})
	huma.Register(a, api.Op("get-project-space", http.MethodGet, "/api/v1/projects/{key}/space", "The wiki space of a project", "Issues"),
		func(ctx context.Context, in *projectPath) (*struct{ Body *ProjectSpace }, error) {
			ps, err := s.LinkedSpace(ctx, identity.From(ctx), in.Key)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *ProjectSpace }{ps}, nil
		})
	huma.Register(a, api.Op("set-project-space", http.MethodPut, "/api/v1/projects/{key}/space", "Link a wiki space to a project", "Issues"),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Space string `json:"space" maxLength:"32" doc:"Key of the space; empty unlinks"`
			}
		}) (*struct{ Body *ProjectSpace }, error) {
			ps, err := s.SetLinkedSpace(ctx, identity.From(ctx), in.Key, in.Body.Space)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *ProjectSpace }{ps}, nil
		})
	huma.Register(a, api.Op("get-version-deployments", http.MethodGet, "/api/v1/projects/{key}/versions/{id}/deployments",
		"Where the issues of a version are deployed", "Issues"),
		func(ctx context.Context, in *rulePath) (*struct{ Body []VersionDeployment }, error) {
			list, err := s.VersionDeployments(ctx, identity.From(ctx), in.Key, in.ID)
			if err != nil {
				return nil, err
			}
			return &struct{ Body []VersionDeployment }{list}, nil
		})
	tag := "Automation"
	huma.Register(a, api.Op("list-automation-rules", http.MethodGet, "/api/v1/projects/{key}/automation", "Automation rules of a project", tag),
		func(ctx context.Context, in *projectPath) (*struct{ Body []Rule }, error) {
			list, err := s.Rules(ctx, identity.From(ctx), in.Key)
			if err != nil {
				return nil, err
			}
			return &struct{ Body []Rule }{list}, nil
		})
	huma.Register(a, api.Op("create-automation-rule", http.MethodPost, "/api/v1/projects/{key}/automation", "Create an automation rule", tag),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body RuleInput
		}) (*struct{ Body *Rule }, error) {
			r, err := s.SaveRule(ctx, identity.From(ctx), in.Key, 0, in.Body)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *Rule }{r}, nil
		})
	huma.Register(a, api.Op("update-automation-rule", http.MethodPut, "/api/v1/projects/{key}/automation/{id}", "Change an automation rule",
		tag),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			ID   int64  `path:"id"`
			Body RuleInput
		}) (*struct{ Body *Rule }, error) {
			r, err := s.SaveRule(ctx, identity.From(ctx), in.Key, in.ID, in.Body)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *Rule }{r}, nil
		})
	huma.Register(a, api.Op("delete-automation-rule", http.MethodDelete, "/api/v1/projects/{key}/automation/{id}", "Delete an automation rule",
		tag),
		func(ctx context.Context, in *rulePath) (*struct{}, error) {
			return nil, s.DeleteRule(ctx, identity.From(ctx), in.Key, in.ID)
		})
}
