// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// PRComment is a comment on a pull request, a file or a line of its diff; tasks are comments to resolve.
type PRComment struct {
	ID         int64             `json:"id"`
	ParentID   int64             `json:"parentId,omitempty"`
	Author     *identity.UserRef `json:"author,omitempty"`
	Body       *docmodel.Doc     `json:"body"`
	Markdown   string            `json:"markdown,omitempty" doc:"The text as Markdown, to change it (comments the caller may edit)"`
	Path       string            `json:"path,omitempty"`
	Line       int               `json:"line,omitempty"`
	Side       string            `json:"side,omitempty" enum:"old,new,"`
	CommitSHA  string            `json:"commitSha,omitempty"`
	LineText   string            `json:"lineText,omitempty" doc:"The commented line when the comment was made"`
	Outdated   bool              `json:"outdated"`
	Task       bool              `json:"task"`
	Resolved   *time.Time        `json:"resolved,omitempty"`
	ResolvedBy *identity.UserRef `json:"resolvedBy,omitempty"`
	Created    time.Time         `json:"created"`
	Updated    time.Time         `json:"updated"`
	Edited     bool              `json:"edited"`
	CanEdit    bool              `json:"canEdit"`
	CanResolve bool              `json:"canResolve" doc:"The caller may resolve and reopen the task"`
	Suggestion *Suggestion       `json:"suggestion,omitempty" doc:"Line comments on the new side: the change they suggest"`
	CanApply   bool              `json:"canApply" doc:"The caller may apply the suggestion (while the pull request is open)"`
}

// Activity is an entry of a pull request's timeline: an event or a comment.
type Activity struct {
	ID      int64             `json:"id"`
	Kind    string            `json:"kind" doc:"opened, updated, rescoped, reviewed, merged, declined, reopened, commented, auto-merge-enabled, auto-merge-cancelled (data.reason: user, pushed, target, permission, archived, strategy, restricted, failed) or auto-merge-blocked (data.reason as AutoMerge.blocked)"`
	Actor   *identity.UserRef `json:"actor,omitempty"`
	At      time.Time         `json:"at"`
	Data    map[string]any    `json:"data"`
	Comment *PRComment        `json:"comment,omitempty"`
}

// Timeline returns the events and the comments of a pull request, oldest first.
func (s *Service) Timeline(ctx context.Context, p *identity.Principal, key, slug string, number int) ([]Activity, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, err
	}
	out := []Activity{}
	userIDs := []int64{}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, coalesce(actor_id, 0), at, data FROM pull_request_activities WHERE pull_request_id = $1
		ORDER BY at, id`, pr.ID)
	if err != nil {
		return nil, err
	}
	actors := map[int]int64{}
	for rows.Next() {
		var act Activity
		var actor int64
		var raw []byte
		if err := rows.Scan(&act.ID, &act.Kind, &actor, &act.At, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		act.Data = map[string]any{}
		_ = json.Unmarshal(raw, &act.Data)
		actors[len(out)] = actor
		userIDs = append(userIDs, actor)
		out = append(out, act)
	}
	rows.Close()
	comments, err := s.prComments(ctx, s.pool, a, r, pr.ID, 0)
	if err != nil {
		return nil, err
	}
	users, err := identity.GetUserRefs(ctx, s.pool, userIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if u, ok := users[actors[i]]; ok {
			out[i].Actor = &u
		}
	}
	for i := range comments {
		c := comments[i]
		out = append(out, Activity{ID: -c.ID, Kind: "commented", Actor: c.Author, At: c.Created, Data: map[string]any{}, Comment: &c})
	}
	// Stable order by time; comments after events of the same instant.
	sortActivities(out)
	return out, nil
}

func sortActivities(list []Activity) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].At.Before(list[j-1].At); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func (s *Service) prComments(ctx context.Context, q db.Querier, a *Access, r *repoRow, prID, id int64) ([]PRComment, error) {
	where := "pull_request_id = $1"
	args := []any{prID}
	if id != 0 {
		where += " AND id = $2"
		args = append(args, id)
	}
	rows, err := q.Query(ctx, `SELECT id, coalesce(parent_id, 0), coalesce(author_id, 0), body, coalesce(path, ''), coalesce(line, 0),
		coalesce(side, ''), coalesce(commit_sha, ''), coalesce(line_text, ''), outdated, task, resolved_at, coalesce(resolved_by, 0), created_at,
		updated_at, edited, coalesce(applied_sha, ''), coalesce(applied_by, 0), applied_at, coalesce(markdown, '') FROM pull_request_comments WHERE `+where+
		" ORDER BY created_at, id", args...)
	if err != nil {
		return nil, err
	}
	type row struct {
		c                           PRComment
		author, resolvedBy, applier int64
		appliedSHA                  string
		appliedAt                   *time.Time
		markdown                    string
	}
	var list []row
	ids := []int64{}
	for rows.Next() {
		var x row
		var raw []byte
		if err := rows.Scan(&x.c.ID, &x.c.ParentID, &x.author, &raw, &x.c.Path, &x.c.Line, &x.c.Side, &x.c.CommitSHA, &x.c.LineText,
			&x.c.Outdated, &x.c.Task, &x.c.Resolved, &x.resolvedBy, &x.c.Created, &x.c.Updated, &x.c.Edited, &x.appliedSHA, &x.applier,
			&x.appliedAt, &x.markdown); err != nil {
			rows.Close()
			return nil, err
		}
		x.c.Body = &docmodel.Doc{}
		if json.Unmarshal(raw, x.c.Body) != nil {
			x.c.Body = docmodel.Empty()
		}
		ids = append(ids, x.author, x.resolvedBy, x.applier)
		list = append(list, x)
	}
	rows.Close()
	users, err := identity.GetUserRefs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	admin := a.Level(r.ProjectID, r.ID) >= LevelAdmin
	var prAuthor int64
	if err := q.QueryRow(ctx, "SELECT coalesce(author_id, 0) FROM pull_requests WHERE id = $1", prID).Scan(&prAuthor); err != nil {
		return nil, err
	}
	settles := a.UserID() != 0 && (prAuthor == a.UserID() || a.Level(r.ProjectID, r.ID) >= LevelWrite)
	out := []PRComment{}
	for _, x := range list {
		if u, ok := users[x.author]; ok {
			x.c.Author = &u
		}
		if u, ok := users[x.resolvedBy]; ok {
			x.c.ResolvedBy = &u
		}
		own := a.UserID() != 0 && x.author == a.UserID()
		x.c.CanEdit = own || a.UserID() != 0 && admin
		if x.c.CanEdit {
			if x.c.Markdown = x.markdown; x.c.Markdown == "" {
				x.c.Markdown = docmodel.Markdown(x.c.Body)
			}
		}
		x.c.CanResolve = x.c.Task && (own || settles)
		if lines, ok := suggestionOf(x.c.Body); ok && x.c.ParentID == 0 && x.c.Line > 0 && x.c.Side == "new" {
			x.c.Suggestion = &Suggestion{Lines: lines}
			if x.appliedSHA != "" {
				x.c.Suggestion.Applied = &AppliedSuggestion{Commit: x.appliedSHA, At: *x.appliedAt}
				if u, ok := users[x.applier]; ok {
					x.c.Suggestion.Applied.By = &u
				}
			}
			x.c.CanApply = x.c.Suggestion.Applied == nil && !x.c.Outdated && a.UserID() != 0 && a.Level(r.ProjectID, r.ID) >= LevelWrite
		}
		out = append(out, x.c)
	}
	return out, nil
}

// CommentInput adds or changes a comment.
type CommentInput struct {
	Body     *docmodel.Doc `json:"body,omitempty"`
	Markdown string        `json:"markdown,omitempty" doc:"Alternative to body"`
	ParentID int64         `json:"parentId,omitempty" doc:"Reply to this comment"`
	Path     string        `json:"path,omitempty" doc:"File of a file or line comment"`
	Line     int           `json:"line,omitempty" minimum:"0" doc:"Line of a line comment"`
	Side     string        `json:"side,omitempty" enum:"old,new," doc:"Side of the diff the line is on"`
	Task     bool          `json:"task,omitempty" doc:"The comment is a task to resolve before merging"`
}

// AddComment comments on a pull request.
func (s *Service) AddComment(ctx context.Context, p *identity.Principal, key, slug string, number int, in CommentInput) (*PRComment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	body, err := docOf(in.Body, in.Markdown)
	if err != nil {
		return nil, err
	}
	if len(body.Content) == 0 {
		return nil, apperr.Invalid("comment.empty", "The comment is empty.")
	}
	raw, _ := docmodel.Marshal(body)
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr, err := s.loadPR(ctx, tx, r.ID, number, false)
		if err != nil {
			return err
		}
		var path, side, sha, lineText any
		var line any
		if in.ParentID != 0 {
			var parentPR int64
			if err := tx.QueryRow(ctx, "SELECT pull_request_id FROM pull_request_comments WHERE id = $1", in.ParentID).Scan(&parentPR); err != nil ||
				parentPR != pr.ID {
				return apperr.Invalid("comment.parent", "The comment to reply to does not exist.")
			}
		} else if in.Path != "" {
			path, sha = in.Path, pr.SourceSHA
			if in.Line > 0 {
				if in.Side != "old" && in.Side != "new" {
					return apperr.Invalid("comment.side", "Line comments need the side old or new.")
				}
				line, side = in.Line, in.Side
				rev := pr.SourceSHA
				if in.Side == "old" {
					rev = pr.MergeBase
				}
				repo := s.open(r.ID)
				if info, err := repo.Stat(ctx, rev, in.Path); err == nil {
					if data, err := repo.ReadBlob(ctx, info.SHA, maxFileView); err == nil {
						lines := strings.Split(string(data), "\n")
						if in.Line <= len(lines) {
							lineText = lines[in.Line-1]
						}
					}
				}
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pull_request_comments (pull_request_id, parent_id, author_id, body, body_text, path, line, side,
			commit_sha, line_text, task, markdown) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`, pr.ID,
			nullID(in.ParentID), p.UserID(), raw, docmodel.PlainText(body), path, line, side, sha, lineText, in.Task,
			markdownSource(in.Body, in.Markdown)).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE pull_requests SET updated_at = now() WHERE id = $1", pr.ID); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "pull-request", pr.ID, p.UserID()); err != nil {
			return err
		}
		mentioned := docmodel.References(body).Users
		if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.commented", p.UserID(), mentioned); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.commented", RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, err
	}
	list, err := s.prComments(ctx, s.pool, a, r, pr.ID, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// commentOf loads a comment with its repository for a change.
func (s *Service) commentOf(ctx context.Context, tx pgx.Tx, a *Access, id int64) (*repoRow, int64, int64, error) {
	var repoID, prID, author int64
	err := tx.QueryRow(ctx, `SELECT pr.repository_id, c.pull_request_id, coalesce(c.author_id, 0) FROM pull_request_comments c
		JOIN pull_requests pr ON pr.id = c.pull_request_id WHERE c.id = $1 FOR UPDATE OF c`, id).Scan(&repoID, &prID, &author)
	if db.IsNoRows(err) {
		return nil, 0, 0, apperr.NotFound("comment.not-found", "The comment does not exist.")
	}
	if err != nil {
		return nil, 0, 0, err
	}
	r, err := repoByID(ctx, tx, repoID)
	if err != nil {
		return nil, 0, 0, err
	}
	if a.Level(r.ProjectID, r.ID) < LevelRead {
		return nil, 0, 0, apperr.NotFound("comment.not-found", "The comment does not exist.")
	}
	return r, prID, author, nil
}

// UpdateComment changes the text of a comment (its author or repository administrators) or turns it into a task.
func (s *Service) UpdateComment(ctx context.Context, p *identity.Principal, id int64, in CommentInput) (*PRComment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var r *repoRow
	var prID int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var author int64
		if r, prID, author, err = s.commentOf(ctx, tx, a, id); err != nil {
			return err
		}
		if author != p.UserID() && a.Level(r.ProjectID, r.ID) < LevelAdmin {
			return apperr.Forbidden("comment.edit-denied", "Only the author can change the comment.")
		}
		body, err := docOf(in.Body, in.Markdown)
		if err != nil {
			return err
		}
		raw, _ := docmodel.Marshal(body)
		_, err = tx.Exec(ctx, `UPDATE pull_request_comments SET body = $2, body_text = $3, task = $4, markdown = $5, edited = true,
			updated_at = now() WHERE id = $1`, id, raw, docmodel.PlainText(body), in.Task, markdownSource(in.Body, in.Markdown))
		if err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.commented", RepoID: r.ID, PRID: prID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	list, err := s.prComments(ctx, s.pool, a, r, prID, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// DeleteComment deletes a comment without replies (its author or repository administrators).
func (s *Service) DeleteComment(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, prID, author, err := s.commentOf(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if author != p.UserID() && a.Level(r.ProjectID, r.ID) < LevelAdmin {
			return apperr.Forbidden("comment.delete-denied", "Only the author can delete the comment.")
		}
		var replies bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pull_request_comments WHERE parent_id = $1)", id).Scan(&replies); err != nil {
			return err
		}
		if replies {
			return apperr.Conflict("comment.has-replies", "Comments with replies cannot be deleted.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM pull_request_comments WHERE id = $1", id); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.commented", RepoID: r.ID, PRID: prID, ActorID: p.UserID()})
	})
}

// ResolveTask resolves or reopens a task: its author, the author of the pull request and who can write to the
// repository. Readers comment and approve, but settling the tasks a merge check waits for is not theirs.
func (s *Service) ResolveTask(ctx context.Context, p *identity.Principal, id int64, resolved bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if p.UserID() == 0 {
		return apperr.Unauthorized("auth.required", "Please sign in.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, prID, author, err := s.commentOf(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if author != p.UserID() && a.Level(r.ProjectID, r.ID) < LevelWrite {
			var prAuthor int64
			if err := tx.QueryRow(ctx, "SELECT coalesce(author_id, 0) FROM pull_requests WHERE id = $1", prID).Scan(&prAuthor); err != nil {
				return err
			}
			if prAuthor != p.UserID() {
				return apperr.Forbidden("task.resolve-denied", "Only the authors of the task and the pull request and who can write to the repository resolve tasks.")
			}
		}
		var by any
		at := "NULL"
		if resolved {
			by, at = p.UserID(), "now()"
		}
		if _, err := tx.Exec(ctx, "UPDATE pull_request_comments SET resolved_at = "+at+", resolved_by = $2 WHERE id = $1 AND task", id, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE pull_requests SET updated_at = now(), version = version + 1 WHERE id = $1", prID); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.updated", RepoID: r.ID, PRID: prID, ActorID: p.UserID()})
	})
}
