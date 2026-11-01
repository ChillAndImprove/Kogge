// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/search"
)

// PersonPath is the address of a person's page in the web application.
func PersonPath(username string) string { return "/people/" + username }

// SearchUsers finds active people (and service accounts) by user name and display name, for the quick search and
// the search page; the caller checks that the principal may browse users. E-mail addresses are not searched: who
// may see a person need not learn their address.
func SearchUsers(ctx context.Context, q db.Querier, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if r.Query.Empty() {
		return out, nil
	}
	var args db.Args
	where := "u.status = 'active' AND u.kind <> 'system' AND lower(u.username || ' ' || u.display_name) LIKE " + args.Add(r.Query.Contains())
	if r.Count {
		if err := q.QueryRow(ctx, "SELECT count(*) FROM users u WHERE "+where, args...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	lower, start := args.Add(r.Query.Lower()), args.Add(r.Query.StartsWith())
	tier := `CASE WHEN lower(u.username) = ` + lower + ` OR lower(u.display_name) = ` + lower + ` THEN 0
		WHEN lower(u.username) LIKE ` + start + ` OR lower(u.display_name) LIKE ` + start + ` THEN 1
		WHEN lower(u.display_name) LIKE ` + args.Add("% "+r.Query.StartsWith()) + ` THEN 2 ELSE 3 END`
	rows, err := q.Query(ctx, `SELECT u.id, u.username, u.display_name, `+tier+`, similarity(lower(u.display_name), `+lower+`)::float8
		FROM users u WHERE `+where+` ORDER BY 4, 5 DESC, lower(u.display_name), u.id LIMIT `+strconv.Itoa(r.LimitOr(5, 100))+
		` OFFSET `+strconv.Itoa(max(r.Offset, 0)), args...)
	if err != nil {
		return nil, err
	}
	if out.Hits, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindUser}
		err := row.Scan(&h.ID, &h.Key, &h.Title, &h.Tier, &h.Score)
		h.URL = PersonPath(h.Key)
		return h, err
	}); err != nil {
		return nil, err
	}
	return out, nil
}
