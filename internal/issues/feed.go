// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/feed"
)

// activityData is what a change of an issue was, as its activity records it (activity.data): the statuses of a
// transition, the fields of an edit, the comment written, the key before a move. Comments are named, not copied,
// so that the stream shows their text as it is: edited, or gone with the comment.
type activityData struct {
	From    *feed.IssueStatus   `json:"from,omitempty"`
	To      *feed.IssueStatus   `json:"to,omitempty"`
	Fields  []feed.ChangedField `json:"fields,omitempty"`
	Comment int64               `json:"comment,omitempty"`
	FromKey string              `json:"fromKey,omitempty"`
}

// statusOf returns a status as the activity records it.
func statusOf(st *Status) *feed.IssueStatus {
	if st == nil {
		return nil
	}
	return &feed.IssueStatus{Name: st.Name, Category: st.Category}
}

// changedFields returns the fields of an edit by their keys, with the names of custom fields.
func changedFields(cfg *configSnapshot, keys []string) []feed.ChangedField {
	out := make([]feed.ChangedField, 0, len(keys))
	for _, k := range keys {
		f := feed.ChangedField{Key: k}
		if id, ok := parseFieldKey(k); ok {
			if cf := cfg.fields[id]; cf != nil {
				f.Name = cf.Name
			}
		}
		out = append(out, f)
	}
	return out
}

// ActivityFeed returns Kontor's part of an activity stream: what happened to the issues the
// principal may see, of one project (projectID) or all, newest first. Changes of deleted issues are not shown.
// Comments show their text only while they exist and are not restricted to a role or group.
func (s *Service) ActivityFeed(ctx context.Context, p *identity.Principal, q feed.Query, projectID int64) ([]feed.Entry, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := "a.entity_type = 'issue' AND " + a.VisibleSQL("i", &args) + " AND " + q.Before.After(feed.SourceActivity, "a.at", "a.id", &args)
	if projectID != 0 {
		where += " AND a.container_type = 'project' AND a.container_id = " + args.Add(projectID)
	}
	if q.ActorID != 0 {
		where += " AND a.actor_id = " + args.Add(q.ActorID)
	}
	if q.Mine {
		me := args.Add(p.UserID())
		where += " AND (i.reporter_id = " + me + " OR i.assignee_id = " + me + " OR EXISTS (SELECT 1 FROM watchers w WHERE w.entity_type = 'issue' " +
			"AND w.entity_id = i.id AND w.user_id = " + me + "))"
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.at, a.actor_id, u.username, u.display_name, u.status = 'active', a.verb, a.title, a.url,
			pr.key, pr.name, a.data,
			(SELECT `+feed.FirstBlocks("c.body")+` FROM issue_comments c WHERE a.verb = 'issue.commented' AND a.data ? 'comment'
				AND c.id = (a.data->>'comment')::bigint AND c.issue_id = i.id AND c.visibility_type IS NULL)
		FROM activity a JOIN issues i ON i.id = a.entity_id JOIN issue_projects pr ON pr.id = i.project_id LEFT JOIN users u ON u.id = a.actor_id
		WHERE `+where+` ORDER BY a.at DESC, a.id DESC LIMIT `+strconv.Itoa(q.Limit+1), db.CustomPlan(args)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (feed.Entry, error) {
		e := feed.Entry{Product: feed.ProductKontor, Source: feed.SourceActivity, Container: feed.Container{Kind: "project"}}
		var actor *int64
		var username, name *string
		var active *bool
		var raw, comment []byte
		err := row.Scan(&e.ID, &e.At, &actor, &username, &name, &active, &e.Verb, &e.Title, &e.URL, &e.Container.Key, &e.Container.Name,
			&raw, &comment)
		if actor != nil && username != nil {
			e.Actor = &identity.UserRef{ID: *actor, Username: *username, DisplayName: *name, Active: active != nil && *active}
		}
		e.Container.URL = "/projects/" + e.Container.Key
		var d activityData
		if json.Unmarshal(raw, &d) == nil {
			if d.From != nil && d.To != nil {
				e.Status = &feed.StatusChange{From: *d.From, To: *d.To}
			}
			e.Fields, e.FromKey = d.Fields, d.FromKey
		}
		e.Excerpt = feed.ExcerptOf(comment)
		return e, err
	})
}
