// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package notify manages watchers, the notification inbox and activity streams. Modules call Notify within
// their transactions; delivery by e-mail is scheduled as jobs.
package notify

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
)

// Categories of notifications (users choose e-mail delivery per category).
const (
	CategoryIssues = "issues"
	CategoryWiki   = "wiki"
	CategoryCode   = "code"
	CategoryBuilds = "builds"
	CategorySystem = "system"
)

// Notification is an inbox entry.
type Notification struct {
	ID         int64          `json:"id"`
	UserID     int64          `json:"-"`
	CreatedAt  time.Time      `json:"createdAt"`
	ReadAt     *time.Time     `json:"readAt,omitempty"`
	Category   string         `json:"category"`
	Event      string         `json:"event"`
	EntityType string         `json:"entityType"`
	EntityID   string         `json:"entityId"`
	ActorID    *int64         `json:"actorId,omitempty"`
	ActorName  *string        `json:"actorName,omitempty"`
	Title      string         `json:"title"`
	Summary    string         `json:"summary"`
	URL        string         `json:"url"`
	Data       map[string]any `json:"data"`
	// Hidden: the user may no longer see what the notification is about, so it says nothing about it.
	Hidden bool `json:"hidden,omitempty"`
}

// mailEnabled is set when e-mail is configured (SetMailEnabled).
var mailEnabled atomic.Bool

// SetMailEnabled tells whether new notifications are to be mailed.
func SetMailEnabled(on bool) { mailEnabled.Store(on) }

// Create writes inbox entries for the recipients (except the actor) and sends a live message to each.
func Create(ctx context.Context, tx pgx.Tx, recipients []int64, n Notification) error {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	seen := map[int64]bool{}
	for _, uid := range recipients {
		if uid == 0 || seen[uid] || (n.ActorID != nil && uid == *n.ActorID) {
			continue
		}
		seen[uid] = true
		// E-mail follows unless the recipient turned it off for the category or has no verified address.
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, category, event, entity_type, entity_id, actor_id,
			title, summary, url, data, mail_state) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			CASE WHEN $11 AND coalesce((SELECT mode FROM notification_preferences WHERE user_id = $1 AND category = $2), 'immediately') <> 'off'
				AND EXISTS (SELECT 1 FROM users WHERE id = $1 AND status = 'active' AND email_verified AND coalesce(email, '') <> '')
			THEN 'pending' ELSE 'skipped' END)`, uid, n.Category, n.Event,
			n.EntityType, n.EntityID, n.ActorID, n.Title, n.Summary, n.URL, n.Data, mailEnabled.Load()); err != nil {
			return err
		}
		if err := events.NotifyLive(ctx, tx, events.LiveMessage{Type: "notification", User: uid}); err != nil {
			return err
		}
	}
	return nil
}

// List returns a user's notifications, newest first.
func List(ctx context.Context, q db.Querier, userID int64, unreadOnly bool, before int64, limit int) ([]Notification, error) {
	rows, err := q.Query(ctx, `SELECT n.id, n.created_at, n.read_at, n.category, n.event, n.entity_type, n.entity_id,
		n.actor_id, u.display_name, n.title, n.summary, n.url, n.data FROM notifications n
		LEFT JOIN users u ON u.id = n.actor_id
		WHERE n.user_id = $1 AND (NOT $2 OR n.read_at IS NULL) AND ($3 = 0 OR n.id < $3)
		ORDER BY n.id DESC LIMIT $4`, userID, unreadOnly, before, max(1, min(limit, 200)))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Notification, error) {
		var n Notification
		err := row.Scan(&n.ID, &n.CreatedAt, &n.ReadAt, &n.Category, &n.Event, &n.EntityType, &n.EntityID, &n.ActorID,
			&n.ActorName, &n.Title, &n.Summary, &n.URL, &n.Data)
		return n, err
	})
}

// Unread counts a user's unread notifications.
func Unread(ctx context.Context, q db.Querier, userID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL", userID).Scan(&n)
	return n, err
}

// MarkRead marks notifications (or all, if ids is empty) as read.
func MarkRead(ctx context.Context, q db.Querier, userID int64, ids []int64) error {
	if len(ids) == 0 {
		_, err := q.Exec(ctx, "UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL", userID)
		return err
	}
	_, err := q.Exec(ctx, "UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL",
		userID, ids)
	return err
}

// Watch adds a watcher.
func Watch(ctx context.Context, q db.Querier, entityType string, entityID, userID int64) error {
	_, err := q.Exec(ctx, "INSERT INTO watchers (entity_type, entity_id, user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
		entityType, entityID, userID)
	return err
}

// Unwatch removes a watcher.
func Unwatch(ctx context.Context, q db.Querier, entityType string, entityID, userID int64) error {
	_, err := q.Exec(ctx, "DELETE FROM watchers WHERE entity_type = $1 AND entity_id = $2 AND user_id = $3",
		entityType, entityID, userID)
	return err
}

// Watchers returns the ids of the users watching an entity.
func Watchers(ctx context.Context, q db.Querier, entityType string, entityID int64) ([]int64, error) {
	rows, err := q.Query(ctx, "SELECT user_id FROM watchers WHERE entity_type = $1 AND entity_id = $2", entityType, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// IsWatching reports whether a user watches an entity.
func IsWatching(ctx context.Context, q db.Querier, entityType string, entityID, userID int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM watchers WHERE entity_type = $1 AND entity_id = $2 AND user_id = $3)",
		entityType, entityID, userID).Scan(&ok)
	return ok, err
}

// Activity is an entry of an activity stream.
type Activity struct {
	ID            int64          `json:"id"`
	At            time.Time      `json:"at"`
	ActorID       *int64         `json:"actorId,omitempty"`
	ActorName     *string        `json:"actorName,omitempty"`
	Verb          string         `json:"verb"`
	EntityType    string         `json:"entityType"`
	EntityID      int64          `json:"entityId"`
	ContainerType string         `json:"containerType"`
	ContainerID   int64          `json:"containerId"`
	Title         string         `json:"title"`
	URL           string         `json:"url"`
	Data          map[string]any `json:"data"`
}

// RecordActivity appends to the activity stream of a container.
func RecordActivity(ctx context.Context, q db.Querier, a Activity) error {
	if a.Data == nil {
		a.Data = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO activity (actor_id, verb, entity_type, entity_id, container_type, container_id,
		title, url, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, a.ActorID, a.Verb, a.EntityType, a.EntityID,
		a.ContainerType, a.ContainerID, a.Title, a.URL, a.Data)
	return err
}

// UserModes returns a user's e-mail mode per category (immediately where nothing was chosen).
func UserModes(ctx context.Context, q db.Querier, userID int64) (map[string]string, error) {
	out := map[string]string{}
	for _, c := range Categories {
		out[c] = "immediately"
	}
	rows, err := q.Query(ctx, "SELECT category, mode FROM notification_preferences WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, m string
		if err := rows.Scan(&c, &m); err != nil {
			return nil, err
		}
		out[c] = m
	}
	return out, rows.Err()
}

// SetUserModes stores e-mail modes of a user. Pending e-mails of categories turned off are not sent.
func SetUserModes(ctx context.Context, q db.Querier, userID int64, prefs []Preference) error {
	for _, p := range prefs {
		if _, err := q.Exec(ctx, `INSERT INTO notification_preferences (user_id, category, mode) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, category) DO UPDATE SET mode = EXCLUDED.mode`, userID, p.Category, p.Mode); err != nil {
			return err
		}
		if p.Mode == "off" {
			if _, err := q.Exec(ctx, "UPDATE notifications SET mail_state = 'skipped' WHERE user_id = $1 AND category = $2 AND mail_state = 'pending'",
				userID, p.Category); err != nil {
				return err
			}
		}
	}
	return nil
}
