// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package privacy answers the requests of data subjects: it exports what Kogge holds about a
// person (right of access) and anonymises a person (right to erasure). It reads and changes the tables of all
// modules directly, as the backup does: a person's data crosses them.
package privacy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/live"
)

// Export is what Kogge holds about a person: sections of rows, by name.
type Export struct {
	Generated time.Time                   `json:"generated"`
	UserID    int64                       `json:"userId"`
	Sections  map[string][]map[string]any `json:"sections"`
	// Truncated names the sections that hold only their newest entries, with the number of entries there are.
	Truncated map[string]int `json:"truncated" doc:"Sections cut at their limit (the newest entries are kept), with the number of all their entries"`
}

// sectionLimits keep exports of very active people to a size a server can hold in memory: the newest entries of
// these sections. An export says which sections it cut and how many entries there are (Export.Truncated).
var sectionLimits = map[string]int{"notifications": 10000, "mails": 10000, "pageVisits": 1000, "auditEvents": 50000}

// sections are the queries of an export; $1 is the user's id. Texts the person wrote are included; the content
// of others is named only by its key or title. The sections in sectionLimits end with LIMIT $2.
var sections = []struct{ name, sql string }{
	{"profile", `SELECT id, username, display_name, email, email_verified, kind, status, oidc_issuer, oidc_subject,
		password_hash IS NOT NULL AS local_password, sso_link, locale, time_zone, theme, created_at, updated_at, last_login_at
		FROM users WHERE id = $1`},
	{"groups", `SELECT g.name, g.source FROM group_members m JOIN groups g ON g.id = m.group_id WHERE m.user_id = $1
		ORDER BY lower(g.name)`},
	{"globalPermissions", `SELECT permission FROM global_grants WHERE principal_type = 'user' AND principal_id = $1
		ORDER BY permission`},
	{"sessions", `SELECT auth_method, host(ip) AS ip, user_agent, created_at, last_seen_at, expires_at FROM sessions
		WHERE user_id = $1 ORDER BY created_at`},
	{"accessTokens", `SELECT name, hint, scopes, expires_at, last_used_at, created_at FROM access_tokens
		WHERE user_id = $1 ORDER BY created_at`},
	{"sshKeys", `SELECT name, fingerprint, created_at, last_used_at FROM ssh_keys WHERE kind = 'user' AND user_id = $1
		ORDER BY created_at`},
	{"notificationPreferences", `SELECT category, mode FROM notification_preferences WHERE user_id = $1 ORDER BY category`},
	{"notifications", `SELECT created_at, read_at, category, event, entity_type, entity_id, title FROM notifications
		WHERE user_id = $1 ORDER BY id DESC LIMIT $2`},
	{"mails", `SELECT created_at, address, subject, state FROM mail_log WHERE user_id = $1 ORDER BY id DESC LIMIT $2`},
	{"watching", `SELECT entity_type, entity_id, created_at FROM watchers WHERE user_id = $1 ORDER BY created_at`},
	{"issuesReported", `SELECT key, summary, created_at FROM issues WHERE reporter_id = $1 ORDER BY id`},
	{"issuesAssigned", `SELECT key, summary, created_at FROM issues WHERE assignee_id = $1 ORDER BY id`},
	{"issueComments", `SELECT i.key AS issue, c.created_at, c.body_text AS text FROM issue_comments c
		JOIN issues i ON i.id = c.issue_id WHERE c.author_id = $1 ORDER BY c.id`},
	{"worklogs", `SELECT i.key AS issue, w.started_at, w.seconds, w.comment FROM worklogs w JOIN issues i ON i.id = w.issue_id
		WHERE w.author_id = $1 ORDER BY w.id`},
	{"votes", `SELECT i.key AS issue FROM issue_votes v JOIN issues i ON i.id = v.issue_id WHERE v.user_id = $1 ORDER BY i.id`},
	{"filters", `SELECT name, description, query, created_at FROM saved_filters WHERE owner_id = $1 ORDER BY id`},
	{"filterSubscriptions", `SELECT f.name AS filter, g.name AS "group", s.user_id = $1 AS personal, s.created_by = $1 AS set_up,
		s.frequency, s.minute, s.weekdays, s.hours, s.month_day, s.time_zone, s.last_run_at, s.created_at,
		EXISTS (SELECT 1 FROM filter_subscription_mutes m WHERE m.subscription_id = s.id AND m.user_id = $1) AS muted
		FROM filter_subscriptions s JOIN saved_filters f ON f.id = s.filter_id LEFT JOIN groups g ON g.id = s.group_id
		WHERE s.user_id = $1 OR s.created_by = $1 OR EXISTS (SELECT 1 FROM filter_subscription_mutes m
		WHERE m.subscription_id = s.id AND m.user_id = $1) ORDER BY s.id`},
	{"pagesCreated", `SELECT s.key AS space, p.title, p.kind, p.status, p.created_at FROM pages p JOIN spaces s ON s.id = p.space_id
		WHERE p.creator_id = $1 ORDER BY p.id`},
	{"pageComments", `SELECT s.key AS space, p.title AS page, c.created_at, c.body_text AS text FROM page_comments c
		JOIN pages p ON p.id = c.page_id JOIN spaces s ON s.id = p.space_id WHERE c.author_id = $1 ORDER BY c.id`},
	{"pageDrafts", `SELECT p.title AS page, d.title, d.updated_at FROM page_drafts d JOIN pages p ON p.id = d.page_id
		WHERE d.user_id = $1 ORDER BY d.updated_at`},
	{"sharedDrafts", `SELECT p.title AS page, d.title, d.updated_at FROM page_shared_drafts d JOIN pages p ON p.id = d.page_id
		WHERE $1 = ANY(d.contributors) ORDER BY d.updated_at`},
	{"pageVisits", `SELECT p.title AS page, v.visited_at FROM page_visits v JOIN pages p ON p.id = v.page_id
		WHERE v.user_id = $1 ORDER BY v.visited_at DESC LIMIT $2`},
	{"recentVisits", `SELECT kind, entity_id, visited_at FROM recent_visits WHERE user_id = $1 ORDER BY visited_at DESC`},
	{"pullRequests", `SELECT cp.key || '/' || r.slug AS repository, pr.number, pr.title, pr.created_at FROM pull_requests pr
		JOIN repositories r ON r.id = pr.repository_id JOIN code_projects cp ON cp.id = r.project_id
		WHERE pr.author_id = $1 ORDER BY pr.id`},
	{"pullRequestComments", `SELECT cp.key || '/' || r.slug AS repository, pr.number, c.created_at, c.body_text AS text
		FROM pull_request_comments c JOIN pull_requests pr ON pr.id = c.pull_request_id JOIN repositories r ON r.id = pr.repository_id
		JOIN code_projects cp ON cp.id = r.project_id WHERE c.author_id = $1 ORDER BY c.id`},
	{"autoMerges", `SELECT cp.key || '/' || r.slug AS repository, pr.number, am.strategy, am.message, am.created_at
		FROM pull_request_auto_merges am JOIN pull_requests pr ON pr.id = am.pull_request_id JOIN repositories r ON r.id = pr.repository_id
		JOIN code_projects cp ON cp.id = r.project_id WHERE am.enabled_by = $1 ORDER BY am.created_at`},
	{"auditEvents", `SELECT at, action, host(actor_ip) AS ip, target_type, target_id FROM audit_events WHERE actor_id = $1
		ORDER BY id DESC LIMIT $2`},
}

// ExportUser collects what Kogge holds about a person.
func ExportUser(ctx context.Context, pool *pgxpool.Pool, userID int64) (*Export, error) {
	if _, err := identity.GetUser(ctx, pool, userID); err != nil {
		return nil, err
	}
	out := &Export{Generated: time.Now().UTC(), UserID: userID, Sections: map[string][]map[string]any{}, Truncated: map[string]int{}}
	for _, s := range sections {
		args := []any{userID}
		limit, limited := sectionLimits[s.name]
		if limited {
			args = append(args, limit+1) // one more tells that there are more
		}
		rows, err := pool.Query(ctx, s.sql, args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		list, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		if list == nil {
			list = []map[string]any{}
		}
		if limited && len(list) > limit {
			list = list[:limit]
			var total int
			// LIMIT NULL is no limit: the same query counts all entries.
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM ("+s.sql+") AS section", userID, nil).Scan(&total); err != nil {
				return nil, fmt.Errorf("%s: %w", s.name, err)
			}
			out.Truncated[s.name] = total
		}
		out.Sections[s.name] = list
	}
	return out, nil
}

// Pseudonym is the name of an anonymised person.
func Pseudonym(userID int64) string { return "Former user " + strconv.FormatInt(userID, 10) }

// AnonymiseUser replaces a person's name, user name, e-mail address and avatar with a pseudonym, ends their
// sessions, removes their credentials, preferences, inbox and drafts, and keeps the content they created —
// which belongs to the organisation — attributed to the pseudonym. Grants and memberships go; restrictions of
// pages that name the person stay, so that no page becomes visible to more people. It cannot be undone.
func AnonymiseUser(ctx context.Context, pool *pgxpool.Pool, actor *identity.Principal, userID int64) error {
	u, err := identity.GetUser(ctx, pool, userID)
	if err != nil {
		return err
	}
	switch {
	case u.Kind == identity.KindSystem:
		return apperr.Forbidden("user.system", "The system user cannot be anonymised.")
	case u.Status == identity.StatusAnonymised:
		return apperr.Conflict("user.anonymised", "The user is anonymised already.")
	case actor.UserID() == userID:
		return apperr.Conflict("user.anonymise-self", "You cannot anonymise yourself.")
	}
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		// The last administrator stays (as for disabling).
		var admins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM global_grants g JOIN users u ON u.id = g.principal_id
			WHERE g.permission = 'admin' AND g.principal_type = 'user' AND u.status = 'active' AND u.id <> $1`, userID).Scan(&admins); err != nil {
			return err
		}
		var isAdmin bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM global_grants WHERE permission = 'admin'
			AND principal_type = 'user' AND principal_id = $1)`, userID).Scan(&isAdmin); err != nil {
			return err
		}
		if isAdmin && admins == 0 {
			return apperr.Conflict("user.last-admin", "The last administrator cannot be anonymised.")
		}
		name := Pseudonym(userID)
		username := "former-user-" + strconv.FormatInt(userID, 10)
		if _, err := tx.Exec(ctx, `UPDATE users SET username = $2, display_name = $3, email = NULL, email_verified = false,
			status = 'anonymised', oidc_issuer = NULL, oidc_subject = NULL, sso_link = NULL, password_hash = NULL, failed_logins = 0,
			locked_until = NULL, locale = NULL, time_zone = NULL, theme = NULL, avatar_sha256 = NULL, preferences = '{}',
			last_login_at = NULL, updated_at = now() WHERE id = $1`, userID, username, name); err != nil {
			return err
		}
		for _, sql := range []string{
			"DELETE FROM sessions WHERE user_id = $1",
			"DELETE FROM access_tokens WHERE user_id = $1",
			"DELETE FROM ssh_keys WHERE kind = 'user' AND user_id = $1",
			"DELETE FROM user_totp WHERE user_id = $1",
			"DELETE FROM user_recovery_codes WHERE user_id = $1",
			"DELETE FROM group_members WHERE user_id = $1",
			"DELETE FROM global_grants WHERE principal_type = 'user' AND principal_id = $1",
			"DELETE FROM notification_preferences WHERE user_id = $1",
			"DELETE FROM notifications WHERE user_id = $1",
			"DELETE FROM mail_log WHERE user_id = $1",
			"DELETE FROM watchers WHERE user_id = $1",
			"DELETE FROM page_drafts WHERE user_id = $1",
			"DELETE FROM page_presence WHERE user_id = $1",
			"DELETE FROM page_visits WHERE user_id = $1",
			"DELETE FROM recent_visits WHERE user_id = $1",
			"DELETE FROM space_favourites WHERE user_id = $1",
			"DELETE FROM saved_filter_favourites WHERE user_id = $1",
			"DELETE FROM filter_subscriptions WHERE user_id = $1",
			"DELETE FROM filter_subscription_mutes WHERE user_id = $1",
			// Nothing merges in the name of a person who is gone.
			"DELETE FROM pull_request_auto_merges WHERE enabled_by = $1",
			"DELETE FROM dashboard_favourites WHERE user_id = $1",
			"DELETE FROM project_role_members WHERE principal_type = 'user' AND principal_id = $1",
			"DELETE FROM space_permissions WHERE holder_type = 'user' AND holder_id = $1",
			"DELETE FROM code_permissions WHERE holder_type = 'user' AND holder_id = $1",
			"DELETE FROM permission_scheme_grants WHERE holder_type = 'user' AND holder_id = $1",
		} {
			if _, err := tx.Exec(ctx, sql, userID); err != nil {
				return fmt.Errorf("%s: %w", sql, err)
			}
		}
		// A personal space keeps its content under the pseudonym; its key no longer carries the user name.
		if _, err := tx.Exec(ctx, `UPDATE spaces SET name = $2, key = '~FORMER' || id WHERE type = 'personal' AND owner_id = $1`,
			userID, name); err != nil {
			return err
		}
		// Grants and memberships changed: permissions are computed anew on all nodes.
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.anonymise", TargetType: "user",
			TargetID: audit.ID(userID)})
	})
}
