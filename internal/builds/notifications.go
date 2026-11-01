// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/db"
)

// Notifications: failed runs, and runs that succeed again after a failure, reach whoever started
// them; deployments that wait for approval reach the approvers.

// RunPath is the address of a run page in Helling, relative to the server (as notifications store addresses).
func RunPath(projectKey, slug string, number int) string {
	return "/builds/" + strings.ToLower(projectKey) + "/" + slug + "/runs/" + strconv.Itoa(number)
}

func runPath(repo *Repo, number int) string { return RunPath(repo.ProjectKey, repo.Slug, number) }

// readers keeps the users who may read the repository.
func (s *Service) readers(ctx context.Context, repoID int64, users []int64) []int64 {
	var out []int64
	for _, uid := range users {
		p, err := s.identity.PrincipalOf(ctx, uid, identity.MethodInternal)
		if err != nil {
			continue
		}
		if l, err := s.repos.Level(ctx, p, repoID); err == nil && l >= LevelRead {
			out = append(out, uid)
		}
	}
	return out
}

// shortRef drops refs/heads/ and refs/tags/.
func shortRef(ref string) string {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return b
	}
	return strings.TrimPrefix(ref, "refs/tags/")
}

// notifyRunFinished tells the user who started a run that it failed, or that it succeeded after the previous run
// of its pipeline on the same branch or tag had failed.
func (s *Service) notifyRunFinished(ctx context.Context, tx pgx.Tx, run *runRow, state string) error {
	if run.TriggeredBy == 0 || (state != RunFailed && state != RunSucceeded) {
		return nil
	}
	event := "run.failed"
	if state == RunSucceeded {
		var prev string
		err := tx.QueryRow(ctx, `SELECT state FROM runs WHERE repository_id = $1 AND pipeline = $2 AND coalesce(branch, '') = $3
			AND coalesce(tag, '') = $4 AND number < $5 AND state IN ('succeeded', 'failed') ORDER BY number DESC LIMIT 1`,
			run.RepoID, run.Pipeline, run.Branch, run.Tag, run.Number).Scan(&prev)
		if db.IsNoRows(err) || (err == nil && prev != RunFailed) {
			return nil
		}
		if err != nil {
			return err
		}
		event = "run.fixed"
	}
	repo, err := s.repos.Repo(ctx, run.RepoID)
	if err != nil {
		return err
	}
	recipients := s.readers(ctx, run.RepoID, []int64{run.TriggeredBy})
	if len(recipients) == 0 {
		return nil
	}
	ref := run.Branch
	if run.Tag != "" {
		ref = run.Tag
	}
	return notify.Create(ctx, tx, recipients, notify.Notification{Category: notify.CategoryBuilds, Event: event, EntityType: "run",
		EntityID: itoa(run.ID), Title: fmt.Sprintf("%s/%s #%d %s", repo.ProjectKey, repo.Slug, run.Number, run.Name), Summary: run.Subject,
		URL: runPath(repo, run.Number), Data: map[string]any{"repository": repo.ProjectKey + "/" + repo.Slug, "ref": ref, "state": state}})
}

// notifyApprovers asks the approvers of the job's environment to decide on its deployment.
func (s *Service) notifyApprovers(ctx context.Context, tx pgx.Tx, run *runRow, j *jobRow) error {
	env, err := scanEnv(tx.QueryRow(ctx, "SELECT "+envColumns+" FROM environments WHERE repository_id = $1 AND name = $2", run.RepoID, j.Environment))
	if err != nil {
		return err
	}
	var depID int64
	err = tx.QueryRow(ctx, "SELECT id FROM deployments WHERE job_id = $1 AND state = 'waiting' ORDER BY id DESC LIMIT 1", j.ID).Scan(&depID)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	users, err := identity.UsersOf(ctx, tx, env.ApproverUsers, env.ApproverGroups)
	if err != nil {
		return err
	}
	recipients := s.readers(ctx, run.RepoID, users)
	if len(recipients) == 0 {
		return nil
	}
	repo, err := s.repos.Repo(ctx, run.RepoID)
	if err != nil {
		return err
	}
	// The person who started the deployment cannot approve it and is not asked to.
	var actor *int64
	by := run.TriggeredBy
	if j.TriggeredBy != 0 {
		by = j.TriggeredBy
	}
	if by != 0 {
		actor = &by
	}
	return notify.Create(ctx, tx, recipients, notify.Notification{Category: notify.CategoryBuilds, Event: "deployment.approval",
		EntityType: "deployment", EntityID: itoa(depID), ActorID: actor,
		Title:   fmt.Sprintf("%s → %s: %s/%s #%d", shortRef(run.ref()), env.Name, repo.ProjectKey, repo.Slug, run.Number),
		Summary: run.Subject, URL: runPath(repo, run.Number) + "?job=" + itoa(j.ID),
		Data: map[string]any{"repository": repo.ProjectKey + "/" + repo.Slug, "environment": env.Name}})
}

// VisibleRunIDs reports which runs a principal may see (for re-checking notifications).
func (s *Service) VisibleRunIDs(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error) {
	return s.visibleBy(ctx, p, ids, "SELECT id, repository_id FROM runs WHERE id = ANY($1)")
}

// VisibleDeploymentIDs reports which deployments a principal may see (for re-checking notifications).
func (s *Service) VisibleDeploymentIDs(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error) {
	return s.visibleBy(ctx, p, ids, `SELECT d.id, e.repository_id FROM deployments d JOIN environments e ON e.id = d.environment_id
		WHERE d.id = ANY($1)`)
}

// visibleBy checks objects by the repositories that query (id, repository id) finds for them.
func (s *Service) visibleBy(ctx context.Context, p *identity.Principal, ids []string, query string) (map[string]bool, error) {
	nums := []int64{}
	for _, id := range ids {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			nums = append(nums, n)
		}
	}
	rows, err := s.pool.Query(ctx, query, nums)
	if err != nil {
		return nil, err
	}
	repoOf := map[int64]int64{}
	for rows.Next() {
		var id, repo int64
		if err := rows.Scan(&id, &repo); err != nil {
			rows.Close()
			return nil, err
		}
		repoOf[id] = repo
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	level := map[int64]int{}
	out := map[string]bool{}
	for id, repo := range repoOf {
		l, ok := level[repo]
		if !ok {
			if l, err = s.repos.Level(ctx, p, repo); err != nil {
				return nil, err
			}
			level[repo] = l
		}
		out[strconv.FormatInt(id, 10)] = l >= LevelRead
	}
	return out, nil
}
