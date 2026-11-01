// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/feed"
)

// Hafen's part of an activity stream: pushes with the commits they brought, what happened to pull
// requests (opened, merged, declined, reopened, approved or marked as needing work) and the conversations started
// on them, with the beginning of their text, in the repositories the principal may read. Pushes of imports (history
// from elsewhere) and of merges (the pull request's merge is the entry) are left out.

// prEvents are the pull request activities that make entries; reviews only when they approve or ask for work.
const prEvents = `(pa.kind IN ('opened', 'merged', 'declined', 'reopened')
	OR (pa.kind = 'reviewed' AND pa.data->>'state' IN ('approved', 'needs-work')))`

// ActivityFeed returns the entries of one repository (repoID) or of all the principal may read, newest first.
func (s *Service) ActivityFeed(ctx context.Context, p *identity.Principal, q feed.Query, repoID int64) ([]feed.Entry, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	limit := strconv.Itoa(q.Limit + 1)
	repoCols := "r.id, p.key, r.slug, r.name"
	joins := " JOIN repositories r ON r.id = %s.repository_id JOIN code_projects p ON p.id = r.project_id"
	scope := func(args *db.Args, repoCol string) string {
		cond := a.VisibleSQL("r", args)
		if repoID != 0 {
			cond += " AND " + repoCol + " = " + args.Add(repoID)
		}
		return cond
	}
	mineOfPR := func(args *db.Args) string {
		me := args.Add(p.UserID())
		return "(pr.author_id = " + me + " OR EXISTS (SELECT 1 FROM pull_request_reviewers rv WHERE rv.pull_request_id = pr.id AND rv.user_id = " + me +
			") OR EXISTS (SELECT 1 FROM watchers w WHERE w.entity_type = 'pull-request' AND w.entity_id = pr.id AND w.user_id = " + me + "))"
	}
	type row struct {
		e     feed.Entry
		actor int64
	}
	var rows []row
	collect := func(sql string, args db.Args, scan func(pgx.CollectableRow, *row) error) error {
		res, err := s.pool.Query(ctx, sql, db.CustomPlan(args)...)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(res, func(cr pgx.CollectableRow) (row, error) {
			var r row
			r.e.Product = feed.ProductHafen
			err := scan(cr, &r)
			return r, err
		})
		rows = append(rows, list...)
		return err
	}
	container := func(r *row, key, slug, name string) {
		r.e.Container = feed.Container{Kind: "repository", Key: key + "/" + slug, Name: name, URL: repoWebPath(key, slug)}
	}

	// Pushes.
	var args db.Args
	where := "ps.protocol NOT IN ('import', 'merge') AND " + scope(&args, "ps.repository_id") + " AND " +
		q.Before.After(feed.SourcePush, "ps.pushed_at", "ps.id", &args)
	if q.ActorID != 0 {
		where += " AND ps.user_id = " + args.Add(q.ActorID)
	}
	if q.Mine {
		where += " AND ps.user_id = " + args.Add(p.UserID())
	}
	err = collect(`SELECT ps.id, ps.pushed_at, coalesce(ps.user_id, 0), ps.updates, ps.commits, `+repoCols+` FROM pushes ps`+
		strings.ReplaceAll(joins, "%s", "ps")+` WHERE `+where+` ORDER BY ps.pushed_at DESC, ps.id DESC LIMIT `+limit, args, func(cr pgx.CollectableRow, r *row) error {
		var raw, commits []byte
		var repo int64
		var key, slug, name string
		if err := cr.Scan(&r.e.ID, &r.e.At, &r.actor, &raw, &commits, &repo, &key, &slug, &name); err != nil {
			return err
		}
		var sum pushSummary
		if len(commits) > 0 && json.Unmarshal(commits, &sum) == nil {
			r.e.Commits = &feed.PushCommits{Count: sum.Count, Newest: []feed.PushCommit{}}
			for _, c := range sum.Newest {
				r.e.Commits.Newest = append(r.e.Commits.Newest, feed.PushCommit{SHA: c.SHA, Subject: c.Subject,
					URL: repoWebPath(key, slug) + "/commits/" + c.SHA})
			}
		}
		var updates []RefUpdate
		_ = json.Unmarshal(raw, &updates)
		r.e.Source, r.e.Verb, r.e.Title = feed.SourcePush, "pushed", key+"/"+slug
		container(r, key, slug, name)
		r.e.URL = r.e.Container.URL
		for _, u := range updates {
			change := feed.RefChange{Change: "updated"}
			switch {
			case u.Created():
				change.Change = "created"
			case u.Deleted():
				change.Change = "deleted"
			}
			if b, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
				change.Name = b
			} else if t, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok {
				change.Name, change.Tag = t, true
			} else {
				continue
			}
			if r.e.URL == r.e.Container.URL && change.Change != "deleted" {
				r.e.URL += "/commits?at=" + url.QueryEscape(change.Name)
			}
			r.e.Refs = append(r.e.Refs, change)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Pull requests: their events and the conversations started on them.
	prJoins := " JOIN pull_requests pr ON pr.id = %s.pull_request_id" + strings.ReplaceAll(joins, "%s", "pr")
	scanPR := func(source int, verb func(kind, state string) string) func(pgx.CollectableRow, *row) error {
		return func(cr pgx.CollectableRow, r *row) error {
			var kind, state string
			var number int
			var title, key, slug, name string
			var repo int64
			var text []byte
			if err := cr.Scan(&r.e.ID, &r.e.At, &r.actor, &kind, &state, &text, &number, &title, &repo, &key, &slug, &name); err != nil {
				return err
			}
			r.e.Excerpt = feed.ExcerptOf(text)
			r.e.Source, r.e.Verb, r.e.Title = source, verb(kind, state), "#"+strconv.Itoa(number)+" "+title
			container(r, key, slug, name)
			r.e.URL = r.e.Container.URL + "/pull-requests/" + strconv.Itoa(number)
			return nil
		}
	}
	args = nil
	where = prEvents + " AND " + scope(&args, "pr.repository_id") + " AND " + q.Before.After(feed.SourcePullRequest, "pa.at", "pa.id", &args)
	if q.ActorID != 0 {
		where += " AND pa.actor_id = " + args.Add(q.ActorID)
	}
	if q.Mine {
		where += " AND " + mineOfPR(&args)
	}
	err = collect(`SELECT pa.id, pa.at, coalesce(pa.actor_id, 0), pa.kind, coalesce(pa.data->>'state', ''), NULL::jsonb, pr.number, pr.title, `+repoCols+`
		FROM pull_request_activities pa`+strings.ReplaceAll(prJoins, "%s", "pa")+` WHERE `+where+` ORDER BY pa.at DESC, pa.id DESC LIMIT `+limit, args,
		scanPR(feed.SourcePullRequest, func(kind, state string) string {
			if kind == "reviewed" {
				return "pull-request." + state
			}
			return "pull-request." + kind
		}))
	if err != nil {
		return nil, err
	}
	args = nil
	where = "c.parent_id IS NULL AND " + scope(&args, "pr.repository_id") + " AND " + q.Before.After(feed.SourceComment, "c.created_at", "c.id", &args)
	if q.ActorID != 0 {
		where += " AND c.author_id = " + args.Add(q.ActorID)
	}
	if q.Mine {
		where += " AND " + mineOfPR(&args)
	}
	err = collect(`SELECT c.id, c.created_at, coalesce(c.author_id, 0), '', '', `+feed.FirstBlocks("c.body")+`, pr.number, pr.title, `+repoCols+`
		FROM pull_request_comments c`+strings.ReplaceAll(prJoins, "%s", "c")+` WHERE `+where+` ORDER BY c.created_at DESC, c.id DESC LIMIT `+limit, args,
		scanPR(feed.SourceComment, func(string, string) string { return "pull-request.commented" }))
	if err != nil {
		return nil, err
	}

	ids := []int64{}
	for _, r := range rows {
		if r.actor != 0 {
			ids = append(ids, r.actor)
		}
	}
	users, err := identity.GetUserRefs(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	out := make([]feed.Entry, 0, len(rows))
	for _, r := range rows {
		if u, ok := users[r.actor]; ok {
			r.e.Actor = &u
		}
		out = append(out, r.e)
	}
	return out, nil
}
