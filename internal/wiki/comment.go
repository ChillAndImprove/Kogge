// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Comment is a page comment; inline comments refer to an anchor in the page.
type Comment struct {
	ID         int64             `json:"id"`
	ParentID   int64             `json:"parentId,omitempty"`
	Author     *identity.UserRef `json:"author,omitempty"`
	Body       *docmodel.Doc     `json:"body"`
	Created    time.Time         `json:"created"`
	Updated    time.Time         `json:"updated"`
	Edited     bool              `json:"edited"`
	Anchor     string            `json:"anchor,omitempty" doc:"Inline comments: id of the anchor mark in the page"`
	AnchorText string            `json:"anchorText,omitempty" doc:"Inline comments: the commented text"`
	Detached   bool              `json:"detached" doc:"Inline comments whose text was deleted from the page"`
	Resolved   *time.Time        `json:"resolved,omitempty"`
	ResolvedBy *identity.UserRef `json:"resolvedBy,omitempty"`
	Likes      int               `json:"likes"`
	Liked      bool              `json:"liked"`
	CanEdit    bool              `json:"canEdit"`
	CanDelete  bool              `json:"canDelete"`
}

// Comments lists the comments of a page in thread order (oldest first).
func (s *Service) Comments(ctx context.Context, p *identity.Principal, pageID int64) ([]Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.readablePage(ctx, s.pool, a, pageID, false)
	if err != nil {
		return nil, err
	}
	anchors := docmodel.Anchors(pg.Body)
	rows, err := s.pool.Query(ctx, `SELECT c.id, coalesce(c.parent_id, 0), c.author_id, u.username, u.display_name, c.body, c.created_at,
		c.updated_at, c.edited, coalesce(c.anchor, ''), coalesce(c.anchor_text, ''), c.resolved_at, ru.id, ru.username, ru.display_name,
		(SELECT count(*) FROM comment_likes l WHERE l.comment_id = c.id),
		EXISTS (SELECT 1 FROM comment_likes l WHERE l.comment_id = c.id AND l.user_id = $2)
		FROM page_comments c LEFT JOIN users u ON u.id = c.author_id LEFT JOIN users ru ON ru.id = c.resolved_by
		WHERE c.page_id = $1 ORDER BY c.created_at, c.id`, pageID, a.userID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Comment, error) {
		var c Comment
		var aid, rid *int64
		var un, dn, run, rdn *string
		var body []byte
		if err := row.Scan(&c.ID, &c.ParentID, &aid, &un, &dn, &body, &c.Created, &c.Updated, &c.Edited, &c.Anchor, &c.AnchorText,
			&c.Resolved, &rid, &run, &rdn, &c.Likes, &c.Liked); err != nil {
			return c, err
		}
		if aid != nil {
			c.Author = &identity.UserRef{ID: *aid, Username: *un, DisplayName: *dn, Active: true}
		}
		if rid != nil {
			c.ResolvedBy = &identity.UserRef{ID: *rid, Username: *run, DisplayName: *rdn, Active: true}
		}
		c.Body = &docmodel.Doc{}
		if err := json.Unmarshal(body, c.Body); err != nil {
			return c, err
		}
		if c.Anchor != "" {
			_, ok := anchors[c.Anchor]
			c.Detached = !ok
		}
		own := aid != nil && *aid == a.userID && a.userID != 0
		c.CanEdit = own
		c.CanDelete = own || a.Can(pg.SpaceID, PermCommentDelete)
		return c, nil
	})
	return nonNil(out), err
}

// CommentInput adds or changes a comment.
type CommentInput struct {
	Body     *docmodel.Doc `json:"body"`
	ParentID int64         `json:"parentId,omitempty" doc:"Reply to this comment"`
}

func (s *Service) commentable(ctx context.Context, q db.Querier, a *Access, pageID int64, forUpdate bool) (*pageRow, error) {
	pg, err := s.readablePage(ctx, q, a, pageID, forUpdate)
	if err != nil {
		return nil, err
	}
	if !a.Can(pg.SpaceID, PermComment) || pg.Status != StatusCurrent {
		return nil, apperr.Forbidden("comment.denied", "You may not comment on this page.")
	}
	return pg, nil
}

func (s *Service) insertComment(ctx context.Context, tx pgx.Tx, a *Access, pg *pageRow, body *docmodel.Doc, parentID int64, anchor, anchorText string) (int64, error) {
	raw, _ := docmodel.Marshal(body)
	if parentID != 0 {
		var ok bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM page_comments WHERE id = $1 AND page_id = $2)", parentID, pg.ID).Scan(&ok); err != nil {
			return 0, err
		}
		if !ok {
			return 0, apperr.Invalid("comment.parent", "The comment to reply to does not exist.")
		}
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO page_comments (page_id, parent_id, author_id, body, body_text, anchor, anchor_text)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, '')) RETURNING id`, pg.ID, nullID(parentID), a.userID, raw,
		docmodel.PlainText(body), anchor, anchorText).Scan(&id); err != nil {
		return 0, err
	}
	if err := s.refreshSearch(ctx, tx, pg.ID); err != nil {
		return 0, err
	}
	if err := notify.Watch(ctx, tx, "page", pg.ID, a.userID); err != nil {
		return 0, err
	}
	sp, err := spaceByID(ctx, tx, pg.SpaceID)
	if err != nil {
		return 0, err
	}
	url := pageURL(sp.Key, pg.ID, pg.Title) + "#comment-" + itoa(id)
	mentioned := docmodel.References(body).Users
	if parentID != 0 {
		var parentAuthor *int64
		if err := tx.QueryRow(ctx, "SELECT author_id FROM page_comments WHERE id = $1", parentID).Scan(&parentAuthor); err != nil {
			return 0, err
		}
		if parentAuthor != nil {
			mentioned = append(mentioned, *parentAuthor)
		}
	}
	if err := s.notifyPage(ctx, tx, pg, sp, pg.Title, url, "page.commented", docmodel.Summary(body, 300), mentioned); err != nil {
		return 0, err
	}
	// The activity names the comment: the stream shows its text as it is, edited or gone.
	actor := a.userID
	if err := notify.RecordActivity(ctx, tx, notify.Activity{ActorID: &actor, Verb: "page.commented", EntityType: "page", EntityID: pg.ID,
		ContainerType: "space", ContainerID: pg.SpaceID, Title: pg.Title, URL: url, Data: map[string]any{"comment": id}}); err != nil {
		return 0, err
	}
	return id, s.publish(ctx, tx, PageEvent{Name: "page.commented", PageID: pg.ID, SpaceID: pg.SpaceID, ActorID: a.userID})
}

func (s *Service) commentBody(ctx context.Context, body *docmodel.Doc) (*docmodel.Doc, error) {
	opts, err := s.docOptions(ctx)
	if err != nil {
		return nil, err
	}
	if body == nil || len(body.Content) == 0 {
		return nil, apperr.Invalid("comment.empty", "The comment is empty.")
	}
	return validateDoc(body, opts)
}

// AddComment adds a comment or a reply to a page.
func (s *Service) AddComment(ctx context.Context, p *identity.Principal, pageID int64, in CommentInput) (*Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	body, err := s.commentBody(ctx, in.Body)
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.commentable(ctx, tx, a, pageID, false)
		if err != nil {
			return err
		}
		id, err = s.insertComment(ctx, tx, a, pg, body, in.ParentID, "", "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.comment(ctx, p, pageID, id)
}

func (s *Service) comment(ctx context.Context, p *identity.Principal, pageID, id int64) (*Comment, error) {
	list, err := s.Comments(ctx, p, pageID)
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, apperr.NotFound("comment.not-found", "The comment does not exist.")
}

// InlineCommentInput comments on a piece of text of the page.
type InlineCommentInput struct {
	Body       *docmodel.Doc `json:"body"`
	Text       string        `json:"text" minLength:"1" maxLength:"2000" doc:"The selected text"`
	Occurrence int           `json:"occurrence,omitempty" minimum:"0" doc:"Which occurrence of the text in the page (0 = first)"`
}

func newAnchorID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "c" + hex.EncodeToString(b)
}

// AddInlineComment marks the selected text with an anchor and comments on it. Marking does not create a new
// version; the anchor moves with the text when the page is edited later.
func (s *Service) AddInlineComment(ctx context.Context, p *identity.Principal, pageID int64, in InlineCommentInput) (*Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	body, err := s.commentBody(ctx, in.Body)
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.commentable(ctx, tx, a, pageID, true)
		if err != nil {
			return err
		}
		anchor := newAnchorID()
		if !docmodel.AddAnchor(pg.Body, anchor, in.Text, in.Occurrence) {
			return apperr.Invalid("comment.anchor", "The selected text was not found in the current version of the page.")
		}
		raw, _ := docmodel.Marshal(pg.Body)
		if _, err := tx.Exec(ctx, "UPDATE pages SET body = $2 WHERE id = $1", pg.ID, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE page_versions SET body = $3 WHERE page_id = $1 AND version = $2", pg.ID, pg.Version, raw); err != nil {
			return err
		}
		id, err = s.insertComment(ctx, tx, a, pg, body, 0, anchor, in.Text)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.comment(ctx, p, pageID, id)
}

type commentRow struct {
	pageID   int64
	authorID int64
	anchor   string
}

func (s *Service) loadComment(ctx context.Context, q db.Querier, a *Access, id int64) (*commentRow, *pageRow, error) {
	var c commentRow
	var author *int64
	err := q.QueryRow(ctx, "SELECT page_id, author_id, coalesce(anchor, '') FROM page_comments WHERE id = $1", id).Scan(&c.pageID, &author, &c.anchor)
	if db.IsNoRows(err) {
		return nil, nil, apperr.NotFound("comment.not-found", "The comment does not exist.")
	}
	if err != nil {
		return nil, nil, err
	}
	if author != nil {
		c.authorID = *author
	}
	pg, err := s.readablePage(ctx, q, a, c.pageID, false)
	if apperr.IsNotFound(err) {
		// The comments of pages one cannot read do not exist for one.
		return nil, nil, apperr.NotFound("comment.not-found", "The comment does not exist.")
	}
	if err != nil {
		return nil, nil, err
	}
	return &c, pg, nil
}

// UpdateComment changes the author's own comment.
func (s *Service) UpdateComment(ctx context.Context, p *identity.Principal, id int64, in CommentInput) (*Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	body, err := s.commentBody(ctx, in.Body)
	if err != nil {
		return nil, err
	}
	c, pg, err := s.loadComment(ctx, s.pool, a, id)
	if err != nil {
		return nil, err
	}
	if c.authorID != a.userID || a.userID == 0 {
		return nil, apperr.Forbidden("comment.edit-denied", "Only the author can change a comment.")
	}
	raw, _ := docmodel.Marshal(body)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE page_comments SET body = $2, body_text = $3, edited = true, updated_at = now() WHERE id = $1",
			id, raw, docmodel.PlainText(body)); err != nil {
			return err
		}
		if err := s.refreshSearch(ctx, tx, pg.ID); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.commented", PageID: pg.ID, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	return s.comment(ctx, p, pg.ID, id)
}

// DeleteComment removes a comment with its replies (the author, or comment.delete); an inline comment's anchor
// is removed from the page. Authors delete their comments with replies only if the replies are their own too:
// the replies of others are not theirs to remove (audit L24).
func (s *Service) DeleteComment(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, pg, err := s.loadComment(ctx, tx, a, id)
		if err != nil {
			return err
		}
		moderator := a.Can(pg.SpaceID, PermCommentDelete)
		if !(c.authorID == a.userID && a.userID != 0) && !moderator {
			return apperr.Forbidden("comment.delete-denied", "You may not delete this comment.")
		}
		if !moderator {
			var others bool
			if err := tx.QueryRow(ctx, `WITH RECURSIVE thread AS (SELECT id, author_id FROM page_comments WHERE parent_id = $1
					UNION ALL SELECT c.id, c.author_id FROM page_comments c JOIN thread t ON c.parent_id = t.id)
				SELECT EXISTS (SELECT 1 FROM thread WHERE author_id IS DISTINCT FROM $2)`, id, a.userID).Scan(&others); err != nil {
				return err
			}
			if others {
				return apperr.Conflict("comment.has-replies", "Others replied to the comment; only who may delete all comments removes it with the replies.")
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM page_comments WHERE id = $1", id); err != nil {
			return err
		}
		if c.anchor != "" {
			locked, err := loadPage(ctx, tx, pg.ID, true)
			if err != nil {
				return err
			}
			if docmodel.RemoveAnchor(locked.Body, c.anchor) {
				raw, _ := docmodel.Marshal(locked.Body)
				if _, err := tx.Exec(ctx, "UPDATE pages SET body = $2 WHERE id = $1", pg.ID, raw); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "UPDATE page_versions SET body = $3 WHERE page_id = $1 AND version = $2", pg.ID, locked.Version, raw); err != nil {
					return err
				}
			}
		}
		if err := s.refreshSearch(ctx, tx, pg.ID); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.commented", PageID: pg.ID, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
}

// ResolveComment resolves or reopens an inline comment.
func (s *Service) ResolveComment(ctx context.Context, p *identity.Principal, id int64, resolved bool) (*Comment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	c, pg, err := s.loadComment(ctx, s.pool, a, id)
	if err != nil {
		return nil, err
	}
	if !a.Can(pg.SpaceID, PermComment) {
		return nil, apperr.Forbidden("comment.denied", "You may not resolve comments on this page.")
	}
	if c.anchor == "" {
		return nil, apperr.Invalid("comment.not-inline", "Only inline comments can be resolved.")
	}
	if resolved {
		_, err = s.pool.Exec(ctx, "UPDATE page_comments SET resolved_at = now(), resolved_by = $2 WHERE id = $1", id, a.userID)
	} else {
		_, err = s.pool.Exec(ctx, "UPDATE page_comments SET resolved_at = NULL, resolved_by = NULL WHERE id = $1", id)
	}
	if err != nil {
		return nil, err
	}
	return s.comment(ctx, p, pg.ID, id)
}

// LikeComment likes or unlikes a comment.
func (s *Service) LikeComment(ctx context.Context, p *identity.Principal, id int64, on bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, _, err := s.loadComment(ctx, s.pool, a, id); err != nil {
		return err
	}
	if on {
		_, err = s.pool.Exec(ctx, "INSERT INTO comment_likes (comment_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, a.userID)
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM comment_likes WHERE comment_id = $1 AND user_id = $2", id, a.userID)
	}
	return err
}
