// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// --- comments ---

// commentVisible reports whether a principal can see a comment restricted to a role or group.
func (s *Service) commentVisible(ctx context.Context, q db.Querier, p *identity.Principal, projectID int64, typ string, id int64) (bool, error) {
	switch typ {
	case "":
		return true, nil
	case "group":
		return p.InGroup(id), nil
	case "role":
		var ok bool
		err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_role_members WHERE project_id = $1 AND role_id = $2
			AND ((principal_type = 'user' AND principal_id = $3) OR (principal_type = 'group' AND principal_id = ANY($4))))`,
			projectID, id, p.UserID(), nonNil(p.Groups)).Scan(&ok)
		return ok, err
	}
	return false, nil
}

// commentFor loads a comment of an issue for a change; comments restricted to a role or group the principal is not in
// do not exist for them.
func (s *Service) commentFor(ctx context.Context, tx pgx.Tx, p *identity.Principal, i *Issue, id int64) (author *int64, visibility string, err error) {
	var visID int64
	err = tx.QueryRow(ctx, `SELECT author_id, coalesce(visibility_type, ''), coalesce(visibility_id, 0) FROM issue_comments
		WHERE id = $1 AND issue_id = $2`, id, i.ID).Scan(&author, &visibility, &visID)
	if err == nil {
		var ok bool
		if ok, err = s.commentVisible(ctx, tx, p, i.projectID, visibility, visID); err == nil && !ok {
			err = pgx.ErrNoRows
		}
	}
	if db.IsNoRows(err) {
		return nil, "", apperr.NotFound("comment.not-found", "The comment does not exist.")
	}
	return author, visibility, err
}

// Comments lists the comments of an issue the principal may see.
func (s *Service) Comments(ctx context.Context, p *identity.Principal, key string) ([]Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, _, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.author_id, u.username, u.display_name, u.status = 'active', c.body,
		coalesce(c.visibility_type, ''), coalesce(c.visibility_id, 0), coalesce(r.name, g.name, ''), c.created_at,
		c.updated_at, c.version > 1 FROM issue_comments c LEFT JOIN users u ON u.id = c.author_id
		LEFT JOIN project_roles r ON c.visibility_type = 'role' AND r.id = c.visibility_id
		LEFT JOIN groups g ON c.visibility_type = 'group' AND g.id = c.visibility_id
		WHERE c.issue_id = $1 ORDER BY c.created_at, c.id`, i.ID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Comment, error) {
		var c Comment
		var authorID *int64
		var au, an *string
		var active *bool
		var body []byte
		err := row.Scan(&c.ID, &authorID, &au, &an, &active, &body, &c.VisibilityType, &c.VisibilityID, &c.VisibilityName,
			&c.Created, &c.Updated, &c.Edited)
		if authorID != nil {
			c.Author = &identity.UserRef{ID: *authorID, Username: *au, DisplayName: *an, Active: *active}
		}
		c.Body = &docmodel.Doc{}
		_ = json.Unmarshal(body, c.Body)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	out := []Comment{}
	h := i.head()
	for _, c := range all {
		ok, err := s.commentVisible(ctx, s.pool, p, i.projectID, c.VisibilityType, c.VisibilityID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		own := c.Author != nil && c.Author.ID == p.UserID()
		c.CanEdit = a.CanIssue(h, PermCommentEditAll) || (own && a.CanIssue(h, PermCommentEditOwn))
		c.CanDelete = a.CanIssue(h, PermCommentDeleteAll) || (own && a.CanIssue(h, PermCommentDeleteOwn))
		out = append(out, c)
	}
	return out, nil
}

// CommentInput adds or changes a comment.
type CommentInput struct {
	Body           *docmodel.Doc `json:"body,omitempty"`
	Markdown       string        `json:"markdown,omitempty" doc:"Alternative to body"`
	VisibilityType string        `json:"visibilityType,omitempty" enum:"role,group,"`
	VisibilityID   int64         `json:"visibilityId,omitempty"`
}

func (in CommentInput) doc() (*docmodel.Doc, error) {
	d := in.Body
	if d == nil && in.Markdown != "" {
		d = docmodel.FromMarkdown(in.Markdown)
	}
	if d == nil {
		return nil, apperr.Invalid("comment.body", "The comment is empty.")
	}
	if err := docmodel.Validate(d, docmodel.Options{}); err != nil {
		return nil, apperr.Invalid("comment.body", err.Error())
	}
	if strings.TrimSpace(docmodel.PlainText(d)) == "" && len(docmodel.References(d).Attachments) == 0 {
		return nil, apperr.Invalid("comment.body", "The comment is empty.")
	}
	return d, nil
}

// AddComment adds a comment to an issue.
func (s *Service) AddComment(ctx context.Context, p *identity.Principal, key string, in CommentInput) (*Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	d, err := in.doc()
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		if !a.CanIssue(i.head(), PermCommentAdd) {
			return apperr.Forbidden("comment.denied", "You may not comment on %s.", i.Key)
		}
		if in.VisibilityType != "" {
			ok, err := s.commentVisible(ctx, tx, p, i.projectID, in.VisibilityType, in.VisibilityID)
			if err != nil {
				return err
			}
			if !ok {
				return apperr.Invalid("comment.visibility", "You can restrict comments only to roles and groups you belong to.")
			}
		}
		if err := s.insertComment(ctx, tx, i, p.UserID(), d, in.VisibilityType, in.VisibilityID, &id); err != nil {
			return err
		}
		return s.afterComment(ctx, tx, i, p.UserID(), d, id, EventCommented, in.VisibilityType == "")
	})
	if err != nil {
		return nil, err
	}
	list, err := s.Comments(ctx, p, key)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, apperr.Internal(nil)
}

func (s *Service) insertComment(ctx context.Context, tx pgx.Tx, i *Issue, authorID int64, d *docmodel.Doc, vt string, vid int64, id *int64) error {
	var visType, visID any
	if vt != "" {
		visType, visID = vt, vid
	}
	var author any
	if authorID != 0 {
		author = authorID
	}
	if err := tx.QueryRow(ctx, `INSERT INTO issue_comments (issue_id, author_id, body, body_text, visibility_type, visibility_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, i.ID, author, d, docmodel.PlainText(d), visType, visID).Scan(id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE issues SET updated_at = now() WHERE id = $1", i.ID)
	return err
}

// afterComment follows a new or edited comment (event): the search, watching, notifications, and for a new one the
// activity, which shows the comment's text as it is when it is read.
func (s *Service) afterComment(ctx context.Context, tx pgx.Tx, i *Issue, uid int64, d *docmodel.Doc, commentID int64, event string, public bool) error {
	if err := s.refreshSearch(ctx, tx, i.ID); err != nil {
		return err
	}
	if uid != 0 {
		if err := notify.Watch(ctx, tx, "issue", i.ID, uid); err != nil {
			return err
		}
	}
	if public {
		if err := s.notifyIssue(ctx, tx, i, event, uid, "commented", docmodel.Summary(d, 300), mentionsOf(d)); err != nil {
			return err
		}
	}
	if event != EventCommentEdit {
		if err := s.recordActivity(ctx, tx, i, uid, "issue.commented", &activityData{Comment: commentID}); err != nil {
			return err
		}
	}
	return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.commented", IssueID: i.ID, Key: i.Key, ProjectID: i.projectID, ActorID: uid})
}

// UpdateComment changes a comment's text.
func (s *Service) UpdateComment(ctx context.Context, p *identity.Principal, key string, id int64, in CommentInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	d, err := in.doc()
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		author, visibility, err := s.commentFor(ctx, tx, p, i, id)
		if err != nil {
			return err
		}
		own := author != nil && *author == p.UserID()
		if !a.CanIssue(i.head(), PermCommentEditAll) && !(own && a.CanIssue(i.head(), PermCommentEditOwn)) {
			return apperr.Forbidden("comment.edit-denied", "You may not edit this comment.")
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_comments SET body = $2, body_text = $3, updated_at = now(), updated_by = $4,
			version = version + 1 WHERE id = $1`, id, d, docmodel.PlainText(d), p.UserID()); err != nil {
			return err
		}
		// Restricted comments do not go out to all watchers.
		return s.afterComment(ctx, tx, i, p.UserID(), d, id, EventCommentEdit, visibility == "")
	})
}

// DeleteComment removes a comment.
func (s *Service) DeleteComment(ctx context.Context, p *identity.Principal, key string, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		author, _, err := s.commentFor(ctx, tx, p, i, id)
		if err != nil {
			return err
		}
		own := author != nil && *author == p.UserID()
		if !a.CanIssue(i.head(), PermCommentDeleteAll) && !(own && a.CanIssue(i.head(), PermCommentDeleteOwn)) {
			return apperr.Forbidden("comment.delete-denied", "You may not delete this comment.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM issue_comments WHERE id = $1", id); err != nil {
			return err
		}
		return s.refreshSearch(ctx, tx, i.ID)
	})
}

// --- history ---

// History returns the change history of an issue.
func (s *Service) History(ctx context.Context, p *identity.Principal, key string) ([]Change, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, cfg, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.actor_id, u.username, u.display_name, c.at, c.items FROM issue_changes c
		LEFT JOIN users u ON u.id = c.actor_id WHERE c.issue_id = $1 ORDER BY c.at, c.id`, i.ID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Change, error) {
		var c Change
		var actor *int64
		var au, an *string
		var items []byte
		err := row.Scan(&c.ID, &actor, &au, &an, &c.At, &items)
		if actor != nil {
			c.Actor = &identity.UserRef{ID: *actor, Username: *au, DisplayName: *an, Active: true}
		}
		_ = json.Unmarshal(items, &c.Items)
		for j, it := range c.Items {
			if id, ok := parseFieldKey(it.Field); ok {
				if f := cfg.fields[id]; f != nil {
					c.Items[j].Name = f.Name
				}
			}
		}
		return c, err
	})
	if err != nil {
		return nil, err
	}
	return s.withoutHiddenIssues(ctx, cfg, a, out)
}

// keyAtEnd matches the issue key the text of a link's history entry ends with.
var keyAtEnd = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*-[0-9]+$`)

// withoutHiddenIssues leaves the issues the reader may not see out of a history (audit L7): links to them
// disappear, parents become blank. Links name the linked issue by id (older entries only by key).
func (s *Service) withoutHiddenIssues(ctx context.Context, cfg *configSnapshot, a *Access, changes []Change) ([]Change, error) {
	var ids []int64
	var keys []string
	for _, c := range changes {
		for _, it := range c.Items {
			switch it.Field {
			case FieldParent:
				ids = append(ids, anyID(it.From), anyID(it.To))
			case "link":
				if id := anyID(it.To); id != 0 {
					ids = append(ids, id)
				} else if k := keyAtEnd.FindString(it.ToString); k != "" {
					keys = append(keys, k)
				}
			}
		}
	}
	out := []Change{}
	if len(ids)+len(keys) == 0 {
		return append(out, changes...), nil
	}
	refs, err := s.issueRefs(ctx, s.pool, cfg, a, "i.id = ANY($1) OR i.key = ANY($2) OR i.id IN (SELECT issue_id FROM issue_keys WHERE key = ANY($2))",
		ids, keys)
	if err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	for _, r := range refs {
		visible[r.ID] = true
	}
	visibleKeys := map[string]bool{}
	if len(keys) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT key, id FROM issues WHERE key = ANY($1)
			UNION ALL SELECT key, issue_id FROM issue_keys WHERE key = ANY($1)`, keys)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var id int64
			if err := rows.Scan(&k, &id); err != nil {
				rows.Close()
				return nil, err
			}
			visibleKeys[k] = visible[id]
		}
		rows.Close()
	}
	for _, c := range changes {
		items := []ChangeItem{}
		for _, it := range c.Items {
			switch it.Field {
			case FieldParent:
				if id := anyID(it.From); id != 0 && !visible[id] {
					it.From, it.FromString = nil, ""
				}
				if id := anyID(it.To); id != 0 && !visible[id] {
					it.To, it.ToString = nil, ""
				}
				if it.FromString == "" && it.ToString == "" {
					continue
				}
			case "link":
				if id := anyID(it.To); id != 0 && !visible[id] || id == 0 && !visibleKeys[keyAtEnd.FindString(it.ToString)] {
					continue
				}
			}
			items = append(items, it)
		}
		if len(items) > 0 {
			c.Items = items
			out = append(out, c)
		}
	}
	return out, nil
}

// anyID returns the id a history value holds (numbers decode as float64), or 0.
func anyID(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		id, _ := n.Int64()
		return id
	}
	return 0
}

// --- worklogs ---

// Worklogs lists the time logged on an issue.
func (s *Service) Worklogs(ctx context.Context, p *identity.Principal, key string) ([]Worklog, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, _, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT w.id, w.author_id, u.username, u.display_name, w.started_at, w.seconds, w.comment,
		w.created_at FROM worklogs w LEFT JOIN users u ON u.id = w.author_id WHERE w.issue_id = $1 ORDER BY w.started_at, w.id`, i.ID)
	if err != nil {
		return nil, err
	}
	h := i.head()
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Worklog, error) {
		var w Worklog
		var author *int64
		var au, an *string
		err := row.Scan(&w.ID, &author, &au, &an, &w.Started, &w.Seconds, &w.Comment, &w.Created)
		own := false
		if author != nil {
			w.Author = &identity.UserRef{ID: *author, Username: *au, DisplayName: *an, Active: true}
			own = *author == p.UserID()
		}
		w.CanEdit = a.CanIssue(h, PermWorklogEditAll) || (own && a.CanIssue(h, PermWorklogEditOwn))
		w.CanDelete = a.CanIssue(h, PermWorklogDeleteAll) || (own && a.CanIssue(h, PermWorklogDeleteOwn))
		return w, err
	})
	if out == nil {
		out = []Worklog{}
	}
	return out, err
}

// WorklogInput logs time.
type WorklogInput struct {
	Started   time.Time `json:"started,omitzero" doc:"Default: now"`
	TimeSpent string    `json:"timeSpent" doc:"Duration such as 1h 30m"`
	Comment   string    `json:"comment,omitempty" maxLength:"4000"`
	Remaining *string   `json:"remaining,omitempty" doc:"New remaining estimate; default: reduce by the time spent"`
}

// AddWorklog logs time on an issue and adjusts the remaining estimate.
func (s *Service) AddWorklog(ctx context.Context, p *identity.Principal, key string, in WorklogInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	secs, err := ParseDuration(in.TimeSpent, st.HoursPerDay, st.DaysPerWeek)
	if err != nil || secs <= 0 {
		return apperr.Invalid("worklog.time", "Enter the time spent, e.g. 1h 30m.")
	}
	var remaining *int64
	if in.Remaining != nil {
		r, err := ParseDuration(*in.Remaining, st.HoursPerDay, st.DaysPerWeek)
		if err != nil {
			return apperr.Invalid("worklog.remaining", "Enter the remaining estimate, e.g. 2h.")
		}
		remaining = &r
	}
	if in.Started.IsZero() {
		in.Started = time.Now()
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		if !a.CanIssue(i.head(), PermWorklogAdd) {
			return apperr.Forbidden("worklog.denied", "You may not log work on %s.", i.Key)
		}
		return s.logWork(ctx, tx, i, p.UserID(), in.Started, secs, in.Comment, remaining)
	})
}

func (s *Service) logWork(ctx context.Context, tx pgx.Tx, i *Issue, uid int64, started time.Time, secs int64, comment string, remaining *int64) error {
	var author any
	if uid != 0 {
		author = uid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO worklogs (issue_id, author_id, started_at, seconds, comment) VALUES ($1, $2, $3, $4, $5)`,
		i.ID, author, started, secs, comment); err != nil {
		return err
	}
	rem := "GREATEST(coalesce(remaining_estimate, 0) - $2, 0)"
	args := []any{i.ID, secs}
	if remaining != nil {
		rem = "$3"
		args = append(args, *remaining)
	}
	if _, err := tx.Exec(ctx, `UPDATE issues SET time_spent = time_spent + $2, remaining_estimate = CASE
		WHEN remaining_estimate IS NULL AND `+boolSQL(remaining == nil)+` THEN NULL ELSE `+rem+` END,
		updated_at = now(), version = version + 1 WHERE id = $1`, args...); err != nil {
		return err
	}
	if err := s.notifyIssue(ctx, tx, i, EventWorklog, uid, "work logged", comment, nil); err != nil {
		return err
	}
	return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.worklog", IssueID: i.ID, Key: i.Key, ProjectID: i.projectID, ActorID: uid})
}

func boolSQL(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// DeleteWorklog removes logged time (and gives it back to the remaining estimate).
func (s *Service) DeleteWorklog(ctx context.Context, p *identity.Principal, key string, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		var author *int64
		var secs int64
		if err := tx.QueryRow(ctx, "SELECT author_id, seconds FROM worklogs WHERE id = $1 AND issue_id = $2", id, i.ID).Scan(&author, &secs); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound("worklog.not-found", "The work log does not exist.")
			}
			return err
		}
		own := author != nil && *author == p.UserID()
		if !a.CanIssue(i.head(), PermWorklogDeleteAll) && !(own && a.CanIssue(i.head(), PermWorklogDeleteOwn)) {
			return apperr.Forbidden("worklog.delete-denied", "You may not delete this work log.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM worklogs WHERE id = $1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE issues SET time_spent = GREATEST(time_spent - $2, 0),
			remaining_estimate = CASE WHEN remaining_estimate IS NULL THEN NULL ELSE remaining_estimate + $2 END,
			updated_at = now(), version = version + 1 WHERE id = $1`, i.ID, secs)
		return err
	})
}

// --- links ---

func (s *Service) issueLinks(ctx context.Context, q db.Querier, cfg *configSnapshot, a *Access, issueID int64) ([]IssueLink, error) {
	rows, err := q.Query(ctx, `SELECT l.id, l.link_type_id, 'outward', l.target_id FROM issue_links l WHERE l.source_id = $1
		UNION ALL SELECT l.id, l.link_type_id, 'inward', l.source_id FROM issue_links l WHERE l.target_id = $1`, issueID)
	if err != nil {
		return nil, err
	}
	type rawLink struct {
		id, typ, other int64
		dir            string
	}
	var raws []rawLink
	for rows.Next() {
		var r rawLink
		if err := rows.Scan(&r.id, &r.typ, &r.dir, &r.other); err != nil {
			rows.Close()
			return nil, err
		}
		raws = append(raws, r)
	}
	rows.Close()
	out := []IssueLink{}
	if len(raws) == 0 {
		return out, nil
	}
	others := make([]int64, len(raws))
	for i, r := range raws {
		others[i] = r.other
	}
	refs, err := s.issueRefs(ctx, q, cfg, a, "i.id = ANY($1)", others)
	if err != nil {
		return nil, err
	}
	byID := map[int64]IssueRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	for _, r := range raws {
		ref, ok := byID[r.other]
		lt := cfg.linkTypes[r.typ]
		if !ok || lt == nil {
			continue // linked issues the reader may not see are hidden
		}
		label := lt.Outward
		if r.dir == "inward" {
			label = lt.Inward
		}
		out = append(out, IssueLink{ID: r.id, Type: *lt, Direction: r.dir, Label: label, Issue: ref})
	}
	return out, nil
}

// LinkInput links two issues.
type LinkInput struct {
	TypeID    int64  `json:"typeId"`
	Direction string `json:"direction,omitempty" enum:"outward,inward" default:"outward" doc:"outward: this issue <outward label> the other"`
	Issue     string `json:"issue" doc:"Key of the other issue"`
}

// AddLink links an issue to another issue.
func (s *Service) AddLink(ctx context.Context, p *identity.Principal, key string, in LinkInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, cfg, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		if !a.CanIssue(i.head(), PermLink) {
			return apperr.Forbidden("link.denied", "You may not link %s.", i.Key)
		}
		other, _, err := s.issueFor(ctx, tx, a, in.Issue, false)
		if err != nil {
			return err
		}
		lt := cfg.linkTypes[in.TypeID]
		if lt == nil {
			return apperr.Invalid("link.type", "Unknown link type.")
		}
		if other.ID == i.ID {
			return apperr.Invalid("link.self", "An issue cannot be linked to itself.")
		}
		src, dst := i, other
		if in.Direction == "inward" {
			src, dst = other, i
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_links (link_type_id, source_id, target_id, created_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, lt.ID, src.ID, dst.ID, p.UserID()); err != nil {
			return err
		}
		c := &fieldChange{}
		c.item("link", nil, dst.ID, "", lt.Outward+" "+dst.Key)
		if err := c.save(ctx, tx, src, p.UserID(), true); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.linked", IssueID: i.ID, Key: i.Key, ProjectID: i.projectID, ActorID: p.UserID()})
	})
}

// DeleteLink removes a link.
func (s *Service) DeleteLink(ctx context.Context, p *identity.Principal, linkID int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var src int64
		if err := tx.QueryRow(ctx, "SELECT source_id FROM issue_links WHERE id = $1", linkID).Scan(&src); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound("link.not-found", "The link does not exist.")
			}
			return err
		}
		i, err := s.loadIssue(ctx, tx, cfg, src, false)
		if err != nil {
			return err
		}
		if !a.visible(i.head()) {
			return apperr.NotFound("link.not-found", "The link does not exist.")
		}
		if !a.CanIssue(i.head(), PermLink) {
			return apperr.Forbidden("link.denied", "You may not change the links of %s.", i.Key)
		}
		_, err = tx.Exec(ctx, "DELETE FROM issue_links WHERE id = $1", linkID)
		return err
	})
}

// --- attachments ---

func (s *Service) attachments(ctx context.Context, q db.Querier, issueID int64) ([]Attachment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.filename, a.content_type, a.size, a.author_id, u.username, u.display_name,
		a.created_at FROM issue_attachments a LEFT JOIN users u ON u.id = a.author_id WHERE a.issue_id = $1 ORDER BY a.created_at, a.id`, issueID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attachment, error) {
		var at Attachment
		var author *int64
		var au, an *string
		err := row.Scan(&at.ID, &at.Filename, &at.ContentType, &at.Size, &author, &au, &an, &at.CreatedAt)
		if author != nil {
			at.Author = &identity.UserRef{ID: *author, Username: *au, DisplayName: *an, Active: true}
		}
		at.URL = "/api/v1/issue-attachments/" + itoa(at.ID)
		return at, err
	})
	if out == nil {
		out = []Attachment{}
	}
	return out, err
}

// attachable returns the issue if the principal may attach files to it.
func (s *Service) attachable(ctx context.Context, a *Access, key string) (*Issue, error) {
	i, _, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	if !a.CanIssue(i.head(), PermAttachCreate) {
		return nil, apperr.Forbidden("attachment.denied", "You may not attach files to %s.", i.Key)
	}
	return i, nil
}

// UploadLimit checks, before an upload is read, that the principal may attach files to the issue and that the server
// has room for n more bytes (0: unknown); it returns the largest file.
func (s *Service) UploadLimit(ctx context.Context, p *identity.Principal, key string, n int64) (int64, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return 0, err
	}
	if _, err := s.attachable(ctx, a, key); err != nil {
		return 0, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	if err := storage.Check(st, s.blobs.Dir(), n); err != nil {
		return 0, err
	}
	return settings.MiB(st.MaxAttachmentMB), nil
}

func attachmentTooLarge(limit int64) error {
	return apperr.New(http.StatusRequestEntityTooLarge, "attachment.too-large", "The file is larger than %s.", storage.Format(limit))
}

// AddAttachment stores a file on an issue.
func (s *Service) AddAttachment(ctx context.Context, p *identity.Principal, key, filename, contentType string, r io.Reader) (*Attachment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.attachable(ctx, a, key)
	if err != nil {
		return nil, err
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "file"
	}
	if contentType == "" || contentType == "application/octet-stream" {
		if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
			contentType = ct
		} else {
			contentType = "application/octet-stream"
		}
	}
	ref, err := s.blobs.Put(ctx, r, settings.MiB(st.MaxAttachmentMB), contentType)
	if errors.Is(err, blob.ErrTooLarge) {
		return nil, attachmentTooLarge(settings.MiB(st.MaxAttachmentMB))
	}
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO issue_attachments (issue_id, filename, content_type, size, sha256, author_id)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, i.ID, filename, contentType, ref.Size, ref.SHA256, p.UserID()).Scan(&id); err != nil {
			return err
		}
		if err := blob.AddRef(ctx, tx, ref.SHA256, "issue-attachment", id); err != nil {
			return err
		}
		c := &fieldChange{}
		c.item("attachment", nil, nil, "", filename)
		if err := c.save(ctx, tx, i, p.UserID(), true); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.updated", IssueID: i.ID, Key: i.Key, ProjectID: i.projectID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	list, err := s.attachments(ctx, s.pool, i.ID)
	if err != nil {
		return nil, err
	}
	for k := range list {
		if list[k].ID == id {
			return &list[k], nil
		}
	}
	return nil, apperr.Internal(nil)
}

// AttachmentFile returns an attachment's metadata and blob hash if the principal may see its issue.
func (s *Service) AttachmentFile(ctx context.Context, p *identity.Principal, id int64) (*Attachment, string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, "", err
	}
	var issueID int64
	var sha string
	at := &Attachment{ID: id}
	err = s.pool.QueryRow(ctx, "SELECT issue_id, filename, content_type, size, sha256, created_at FROM issue_attachments WHERE id = $1", id).
		Scan(&issueID, &at.Filename, &at.ContentType, &at.Size, &sha, &at.CreatedAt)
	if db.IsNoRows(err) {
		return nil, "", apperr.NotFound("attachment.not-found", "The attachment does not exist.")
	}
	if err != nil {
		return nil, "", err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, "", err
	}
	i, err := s.loadIssue(ctx, s.pool, cfg, issueID, false)
	if err != nil || !a.visible(i.head()) {
		return nil, "", apperr.NotFound("attachment.not-found", "The attachment does not exist.")
	}
	return at, sha, nil
}

// DeleteAttachment removes an attachment.
func (s *Service) DeleteAttachment(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var issueID int64
		var author *int64
		var name string
		if err := tx.QueryRow(ctx, "SELECT issue_id, author_id, filename FROM issue_attachments WHERE id = $1", id).Scan(&issueID, &author, &name); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound("attachment.not-found", "The attachment does not exist.")
			}
			return err
		}
		i, err := s.loadIssue(ctx, tx, cfg, issueID, false)
		if err != nil {
			return err
		}
		if !a.visible(i.head()) {
			return apperr.NotFound("attachment.not-found", "The attachment does not exist.")
		}
		own := author != nil && *author == p.UserID()
		if !a.CanIssue(i.head(), PermAttachDeleteAll) && !(own && a.CanIssue(i.head(), PermAttachDeleteOwn)) {
			return apperr.Forbidden("attachment.delete-denied", "You may not delete this attachment.")
		}
		if err := blob.RemoveRefs(ctx, tx, "issue-attachment", id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM issue_attachments WHERE id = $1", id); err != nil {
			return err
		}
		c := &fieldChange{}
		c.item("attachment", nil, nil, name, "")
		return c.save(ctx, tx, i, p.UserID(), true)
	})
}

// --- watching and voting ---

// SetWatching adds or removes the principal (or, with manage permission, another user) as watcher.
func (s *Service) SetWatching(ctx context.Context, p *identity.Principal, key string, userID int64, watch bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if !p.Authenticated() {
		return apperr.Unauthorized("auth.required", "Please sign in.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		if userID == 0 {
			userID = p.UserID()
		}
		if userID != p.UserID() {
			if !a.CanIssue(i.head(), PermManageWatchers) {
				return apperr.Forbidden("watchers.denied", "You may not change the watchers of %s.", i.Key)
			}
			if ok, err := s.userCanSee(ctx, tx, userID, i); err != nil || !ok {
				if err != nil {
					return err
				}
				return apperr.Invalid("watchers.cannot-see", "The user cannot see this issue.")
			}
		}
		if watch {
			return notify.Watch(ctx, tx, "issue", i.ID, userID)
		}
		return notify.Unwatch(ctx, tx, "issue", i.ID, userID)
	})
}

// WatcherList returns the watchers of an issue.
func (s *Service) WatcherList(ctx context.Context, p *identity.Principal, key string) ([]identity.UserRef, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, _, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	if !a.CanIssue(i.head(), PermViewWatchers) {
		return nil, apperr.Forbidden("watchers.denied", "You may not see the watchers of %s.", i.Key)
	}
	ids, err := notify.Watchers(ctx, s.pool, "issue", i.ID)
	if err != nil {
		return nil, err
	}
	refs, err := identity.GetUserRefs(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	out := []identity.UserRef{}
	for _, id := range ids {
		if r, ok := refs[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// SetVote adds or removes the principal's vote.
func (s *Service) SetVote(ctx context.Context, p *identity.Principal, key string, vote bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if !p.Authenticated() {
		return apperr.Unauthorized("auth.required", "Please sign in.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		i, _, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		if i.reporterID == p.UserID() {
			return apperr.Invalid("vote.own", "You cannot vote for an issue you reported.")
		}
		var tag interface{ RowsAffected() int64 }
		if vote {
			tag, err = tx.Exec(ctx, "INSERT INTO issue_votes (issue_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", i.ID, p.UserID())
		} else {
			tag, err = tx.Exec(ctx, "DELETE FROM issue_votes WHERE issue_id = $1 AND user_id = $2", i.ID, p.UserID())
		}
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE issues SET votes = (SELECT count(*) FROM issue_votes WHERE issue_id = $1) WHERE id = $1", i.ID)
		return err
	})
}
