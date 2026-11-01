// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/feed"
)

// ActivityFeed returns Logbuch's part of an activity stream: pages and blog posts created,
// published anew and commented on, of one space (spaceID) or all, newest first. Only pages the principal may read
// now count, so pages in the trash and pages restricted since are left out. New versions show the author's note on
// what changed, comments the beginning of their text (while they exist).
func (s *Service) ActivityFeed(ctx context.Context, p *identity.Principal, q feed.Query, spaceID int64) ([]feed.Entry, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := "a.entity_type = 'page' AND pg.status IN ('current', 'archived') AND " + a.VisibleSQL("pg", &args) + " AND " +
		q.Before.After(feed.SourceActivity, "a.at", "a.id", &args)
	if spaceID != 0 {
		where += " AND a.container_type = 'space' AND a.container_id = " + args.Add(spaceID)
	}
	if q.ActorID != 0 {
		where += " AND a.actor_id = " + args.Add(q.ActorID)
	}
	if q.Mine {
		me := args.Add(p.UserID())
		where += " AND (pg.creator_id = " + me + " OR EXISTS (SELECT 1 FROM watchers w WHERE w.user_id = " + me +
			" AND ((w.entity_type = 'page' AND w.entity_id = pg.id) OR (w.entity_type = 'space' AND w.entity_id = pg.space_id))))"
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.at, a.actor_id, u.username, u.display_name, u.status = 'active', a.verb, a.title, a.url,
			sp.key, sp.name,
			(SELECT v.message FROM page_versions v WHERE a.verb = 'page.updated' AND a.data ? 'version'
				AND v.page_id = pg.id AND v.version = (a.data->>'version')::int),
			(SELECT `+feed.FirstBlocks("c.body")+` FROM page_comments c WHERE a.verb = 'page.commented' AND a.data ? 'comment'
				AND c.id = (a.data->>'comment')::bigint AND c.page_id = pg.id)
		FROM activity a JOIN pages pg ON pg.id = a.entity_id JOIN spaces sp ON sp.id = pg.space_id LEFT JOIN users u ON u.id = a.actor_id
		WHERE `+where+` ORDER BY a.at DESC, a.id DESC LIMIT `+strconv.Itoa(q.Limit+1), db.CustomPlan(args)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (feed.Entry, error) {
		e := feed.Entry{Product: feed.ProductLogbuch, Source: feed.SourceActivity, Container: feed.Container{Kind: "space"}}
		var actor *int64
		var username, name *string
		var active *bool
		var note *string
		var comment []byte
		err := row.Scan(&e.ID, &e.At, &actor, &username, &name, &active, &e.Verb, &e.Title, &e.URL, &e.Container.Key, &e.Container.Name,
			&note, &comment)
		if actor != nil && username != nil {
			e.Actor = &identity.UserRef{ID: *actor, Username: *username, DisplayName: *name, Active: active != nil && *active}
		}
		e.Container.URL = "/wiki/spaces/" + e.Container.Key
		if note != nil {
			e.Excerpt = docmodel.Cut(strings.Join(strings.Fields(*note), " "), feed.ExcerptLength)
		} else {
			e.Excerpt = feed.ExcerptOf(comment)
		}
		return e, err
	})
}
