// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/notify"
)

// Notification events of issue projects.
const (
	EventCreated     = "created"
	EventUpdated     = "updated"
	EventAssigned    = "assigned"
	EventTransition  = "transitioned"
	EventResolved    = "resolved"
	EventCommented   = "commented"
	EventCommentEdit = "comment-edited"
	EventWorklog     = "work-logged"
	EventDeleted     = "deleted"
	EventMoved       = "moved"
)

// NotificationEvents lists the events in display order.
var NotificationEvents = []string{EventCreated, EventUpdated, EventAssigned, EventTransition, EventResolved,
	EventCommented, EventCommentEdit, EventWorklog, EventDeleted, EventMoved}

// recipients resolves the notification scheme of the issue's project for an event. Mentioned users are added by
// the caller; recipients who cannot see the issue are dropped.
func (s *Service) recipients(ctx context.Context, tx pgx.Tx, i *Issue, event string) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT e.recipient_type, e.recipient_id FROM notification_scheme_entries e
		JOIN issue_projects p ON p.notification_scheme_id = e.scheme_id WHERE p.id = $1 AND e.event = $2`, i.projectID, event)
	if err != nil {
		return nil, err
	}
	type entry struct {
		typ string
		id  int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.typ, &e.id); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	var out []int64
	add := func(ids ...int64) {
		for _, id := range ids {
			if id != 0 && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	for _, e := range entries {
		switch e.typ {
		case "reporter":
			add(i.reporterID)
		case "assignee":
			add(i.assigneeID)
		case "project-lead":
			add(i.leadID)
		case "user":
			add(e.id)
		case "watchers":
			w, err := notify.Watchers(ctx, tx, "issue", i.ID)
			if err != nil {
				return nil, err
			}
			add(w...)
		case "group":
			ids, err := userIDs(ctx, tx, "SELECT user_id FROM group_members WHERE group_id = $1", e.id)
			if err != nil {
				return nil, err
			}
			add(ids...)
		case "role":
			ids, err := userIDs(ctx, tx, `SELECT m.principal_id FROM project_role_members m WHERE m.project_id = $1
				AND m.role_id = $2 AND m.principal_type = 'user' UNION SELECT gm.user_id FROM project_role_members m
				JOIN group_members gm ON gm.group_id = m.principal_id WHERE m.project_id = $1 AND m.role_id = $2
				AND m.principal_type = 'group'`, i.projectID, e.id)
			if err != nil {
				return nil, err
			}
			add(ids...)
		case "component-lead":
			ids, err := userIDs(ctx, tx, `SELECT c.lead_id FROM issue_components ic JOIN components c ON c.id = ic.component_id
				WHERE ic.issue_id = $1 AND c.lead_id IS NOT NULL`, i.ID)
			if err != nil {
				return nil, err
			}
			add(ids...)
		case "user-field":
			// The people in a user or multi-user field of the issue (one id, or a list of them).
			ids, err := userIDs(ctx, tx, `SELECT DISTINCT v.id::bigint FROM issues i
				JOIN custom_fields f ON f.id = $2 AND f.type IN ('user', 'multiuser')
				CROSS JOIN LATERAL jsonb_array_elements_text(CASE jsonb_typeof(i.fields -> ('cf_' || $2::text))
					WHEN 'array' THEN i.fields -> ('cf_' || $2::text)
					WHEN 'number' THEN jsonb_build_array(i.fields -> ('cf_' || $2::text))
					ELSE '[]'::jsonb END) AS v(id)
				WHERE i.id = $1`, i.ID, e.id)
			if err != nil {
				return nil, err
			}
			add(ids...)
		}
	}
	return out, nil
}

func userIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]int64, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// recipientsWhoSee returns the scheme's recipients of an issue event and the mentioned users, as far as they can
// see the issue.
func (s *Service) recipientsWhoSee(ctx context.Context, tx pgx.Tx, i *Issue, event string, mentioned []int64) ([]int64, error) {
	recipients, err := s.recipients(ctx, tx, i, event)
	if err != nil {
		return nil, err
	}
	for _, m := range mentioned {
		if !slices.Contains(recipients, m) {
			recipients = append(recipients, m)
		}
	}
	visible := recipients[:0]
	for _, uid := range recipients {
		ok, err := s.userCanSee(ctx, tx, uid, i)
		if err != nil {
			return nil, err
		}
		if ok {
			visible = append(visible, uid)
		}
	}
	return visible, nil
}

// notifyIssue writes inbox notifications for an issue event to the scheme's recipients and mentioned users who
// can see the issue.
func (s *Service) notifyIssue(ctx context.Context, tx pgx.Tx, i *Issue, event string, actorID int64, title, summary string,
	mentioned []int64) error {
	visible, err := s.recipientsWhoSee(ctx, tx, i, event, mentioned)
	if err != nil || len(visible) == 0 {
		return err
	}
	var actor *int64
	if actorID != 0 {
		actor = &actorID
	}
	return notify.Create(ctx, tx, visible, notify.Notification{Category: notify.CategoryIssues, Event: "issue." + event,
		EntityType: "issue", EntityID: i.Key, ActorID: actor, Title: i.Key + " " + i.Summary, Summary: summary,
		URL: "/browse/" + i.Key, Data: map[string]any{"title": title}})
}

// bulkItem is an issue a bulk change is working on. Its changes are not notified one by one: who is to hear of
// them is collected for the one summary each recipient gets of the whole bulk change (bulk.go).
type bulkItem struct {
	notify     bool    // the bulk change sends its summary
	recipients []int64 // who hears of the changes of this issue
}

// notifyChange notifies of a change of an issue: right away (b nil), or in the summary of a bulk change.
func (s *Service) notifyChange(ctx context.Context, tx pgx.Tx, b *bulkItem, i *Issue, event string, actorID int64, title,
	summary string, mentioned []int64) error {
	if b == nil {
		return s.notifyIssue(ctx, tx, i, event, actorID, title, summary, mentioned)
	}
	if !b.notify {
		return nil
	}
	visible, err := s.recipientsWhoSee(ctx, tx, i, event, mentioned)
	if err != nil {
		return err
	}
	for _, uid := range visible {
		if uid != actorID && !slices.Contains(b.recipients, uid) {
			b.recipients = append(b.recipients, uid)
		}
	}
	return nil
}

// mentionsOf returns the users mentioned in a document.
func mentionsOf(d *docmodel.Doc) []int64 {
	if d == nil {
		return nil
	}
	return docmodel.References(d).Users
}
