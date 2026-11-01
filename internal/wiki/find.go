// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/search"
)

// Logbuch's part of the quick search and the search page: pages and blog posts by title
// (and, on the search page, by their texts) and spaces by key and name. Package links merges them with the other
// products. The queries apply the principal's access, view restrictions included.

type pageHitRow struct {
	id                  int64
	title, kind         string
	spaceKey, spaceName string
	updated             time.Time
	spaceArchived       bool
	tier                int
	score               float64
}

const pageHitColumns = "p.id, p.title, p.kind, sp.key, sp.name, p.updated_at, sp.archived"

func (s *Service) collectPageHits(ctx context.Context, sql string, args db.Args, ranked bool, tier int) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, db.CustomPlan(args)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := pageHitRow{tier: tier}
		dest := []any{&h.id, &h.title, &h.kind, &h.spaceKey, &h.spaceName, &h.updated, &h.spaceArchived}
		if ranked {
			dest = append(dest, &h.tier, &h.score)
		}
		if err := row.Scan(dest...); err != nil {
			return search.Hit{}, err
		}
		kind := search.KindPage
		if h.kind == "blog" {
			kind = search.KindBlog
		}
		updated := h.updated
		return search.Hit{Kind: kind, ID: h.id, Title: h.title, Context: h.spaceName, URL: pageURL(h.spaceKey, h.id, h.title),
			Updated: &updated, Archived: h.spaceArchived, Tier: h.tier, Score: h.score}, nil
	})
}

// SearchPages finds the current pages and blog posts the principal may read (kind page or blog, or both when
// empty): by the words of their titles, and with r.Text (the search page) by their texts, labels and comments.
func (s *Service) SearchPages(ctx context.Context, p *identity.Principal, kind string, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if r.Query.Empty() {
		return out, nil
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if !r.Text {
		if out.Hits, err = s.pageTitleHits(ctx, a, kind, r.Query, r.LimitOr(5, 50)); err != nil {
			return nil, err
		}
		return out, nil
	}
	// The search page: the titles that match first, ranked as the quick search ranks them, then the other pages
	// whose texts match, newest first.
	titles, err := s.pageTitleHits(ctx, a, kind, r.Query, search.Candidates)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := s.pageScope(a, kind, &args)
	lang := args.Add(st.ContentLanguage) + "::regconfig"
	conds := []string{"p.search_tsv @@ websearch_to_tsquery(" + lang + ", " + args.Add(r.Query.Text) + ")"}
	if prefix := r.Query.Prefix(); prefix != "" {
		conds = append(conds, "to_tsvector('simple', p.title) @@ to_tsquery('simple', "+args.Add(prefix)+")")
	}
	where += " AND (" + strings.Join(conds, " OR ") + ")"
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM pages p WHERE "+where, db.CustomPlan(args)...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	from, to, textOffset, textLimit := search.Split(len(titles), max(r.Offset, 0), r.LimitOr(25, 100))
	out.Hits = append(out.Hits, titles[from:to]...)
	if textLimit > 0 {
		ids := make([]int64, 0, len(titles))
		for _, h := range titles {
			ids = append(ids, h.ID)
		}
		texts, err := s.collectPageHits(ctx, `SELECT `+pageHitColumns+` FROM pages p JOIN spaces sp ON sp.id = p.space_id
			WHERE `+where+` AND NOT (p.id = ANY(`+args.Add(ids)+`))
			ORDER BY p.updated_at DESC, p.id DESC LIMIT `+strconv.Itoa(textLimit)+` OFFSET `+strconv.Itoa(textOffset), args, false, search.TierText)
		if err != nil {
			return nil, err
		}
		out.Hits = append(out.Hits, texts...)
	}
	if len(out.Hits) > 0 {
		ids := make([]int64, 0, len(out.Hits))
		for _, h := range out.Hits {
			ids = append(ids, h.ID)
		}
		rows, err := s.pool.Query(ctx, `SELECT id, ts_headline($1::regconfig, body_text, websearch_to_tsquery($1::regconfig, $2), $3),
			`+search.HeadlineEnds("body_text")+` FROM pages WHERE id = ANY($4)`, st.ContentLanguage, r.Query.Text, search.HeadlineOptions, ids)
		if err != nil {
			return nil, err
		}
		excerpts, err := search.CollectExcerpts(rows)
		if err != nil {
			return nil, err
		}
		if rows, err = s.pool.Query(ctx, `SELECT p.id, u.id, u.username, u.display_name, u.status = 'active' FROM pages p
			JOIN users u ON u.id = p.updated_by WHERE p.id = ANY($1)`, ids); err != nil {
			return nil, err
		}
		editors, err := search.CollectPeople(rows)
		if err != nil {
			return nil, err
		}
		paths, err := s.pagePaths(ctx, a, ids)
		if err != nil {
			return nil, err
		}
		for i := range out.Hits {
			h := &out.Hits[i]
			h.Excerpt, h.Person = excerpts[h.ID], editors[h.ID]
			if path := paths[h.ID]; len(path) > 0 {
				h.Context = strings.Join(append([]string{h.Context}, path...), " › ")
			}
		}
	}
	return out, nil
}

// pageScope restricts pages (alias p) to the current ones the principal may read, of a kind (or both).
func (s *Service) pageScope(a *Access, kind string, args *db.Args) string {
	where := "p.status = 'current' AND " + a.VisibleSQL("p", args)
	if kind != "" {
		where += " AND p.kind = " + args.Add(kind)
	}
	return where
}

// pageTitleHits finds pages by the words of their titles: as for issues, the most recently updated pages whose
// titles match are the candidates, ranked by the whole title, its beginning and how like the text the title is.
func (s *Service) pageTitleHits(ctx context.Context, a *Access, kind string, q search.Query, limit int) ([]search.Hit, error) {
	prefix := q.Prefix()
	if prefix == "" {
		return []search.Hit{}, nil
	}
	var args db.Args
	where := s.pageScope(a, kind, &args)
	lower := args.Add(q.Lower())
	sql := `SELECT ` + pageHitColumns + `,
			CASE WHEN lower(p.title) = ` + lower + ` THEN 0 WHEN lower(p.title) LIKE ` + args.Add(q.StartsWith()) + ` THEN 1 ELSE 2 END,
			similarity(lower(p.title), ` + lower + `)::float8
		FROM (SELECT p.id, p.title, p.kind, p.space_id, p.updated_at FROM pages p
			WHERE to_tsvector('simple', p.title) @@ to_tsquery('simple', ` + args.Add(prefix) + `) AND ` + where + `
			ORDER BY p.updated_at DESC LIMIT ` + strconv.Itoa(search.Candidates) + `) p
		JOIN spaces sp ON sp.id = p.space_id
		ORDER BY 8, 9 DESC, p.updated_at DESC LIMIT ` + strconv.Itoa(limit)
	hits, err := s.collectPageHits(ctx, sql, args, true, 0)
	if err != nil {
		return nil, err
	}
	search.Sort(hits)
	return hits, nil
}

// PageHits returns the current pages and blog posts of the ids that the principal may read.
func (s *Service) PageHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	return s.collectPageHits(ctx, `SELECT `+pageHitColumns+` FROM pages p JOIN spaces sp ON sp.id = p.space_id
		WHERE p.id = ANY($1) AND p.status = 'current' AND `+a.VisibleSQL("p", &args), args, false, 0)
}

const spaceHitColumns = "s.id, s.key, s.name, s.archived, s.updated_at"

func (s *Service) collectSpaceHits(ctx context.Context, sql string, args db.Args, ranked bool) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindSpace, Updated: new(time.Time)}
		dest := []any{&h.ID, &h.Key, &h.Title, &h.Archived, h.Updated}
		if ranked {
			dest = append(dest, &h.Tier, &h.Score)
		}
		err := row.Scan(dest...)
		h.URL = "/wiki/spaces/" + h.Key
		return h, err
	})
}

// SearchSpaces finds the spaces the principal may see by key and name (with r.Text also by description).
func (s *Service) SearchSpaces(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if r.Query.Empty() {
		return out, nil
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	// The count binds the parameters of the condition only (PostgreSQL cannot type unused ones); the ranking's
	// follow.
	var args db.Args
	upper := strings.ToUpper(r.Query.Text)
	key, keyStart, contains := args.Add(upper), args.Add(db.EscapeLike(upper)+"%"), args.Add(r.Query.Contains())
	conds := []string{"upper(s.key) = " + key, "upper(s.key) LIKE " + keyStart, "lower(s.name) LIKE " + contains}
	words := "FALSE"
	if prefix := r.Query.Prefix(); prefix != "" {
		words = "to_tsvector('simple', s.name) @@ to_tsquery('simple', " + args.Add(prefix) + ")"
		conds = append(conds, words)
	}
	if r.Text {
		conds = append(conds, "lower(s.description) LIKE "+contains)
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND s.id = ANY(" + args.Add(a.Spaces(PermView)) + ")"
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM spaces s WHERE "+where, args...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	lower, start := args.Add(r.Query.Lower()), args.Add(r.Query.StartsWith())
	tier := `CASE WHEN upper(s.key) = ` + key + ` OR lower(s.name) = ` + lower + ` THEN 0
		WHEN upper(s.key) LIKE ` + keyStart + ` OR lower(s.name) LIKE ` + start + ` THEN 1 WHEN ` + words + ` THEN 2 ELSE 3 END`
	sql := `SELECT ` + spaceHitColumns + `, ` + tier + `, similarity(lower(s.name), ` + lower + `)::float8 FROM spaces s WHERE ` + where +
		` ORDER BY 6, s.archived, 7 DESC, lower(s.name), s.id LIMIT ` + strconv.Itoa(r.LimitOr(5, 100)) + ` OFFSET ` + strconv.Itoa(max(r.Offset, 0))
	if out.Hits, err = s.collectSpaceHits(ctx, sql, args, true); err != nil {
		return nil, err
	}
	return out, nil
}

// SpaceHits returns the spaces of the ids that the principal may see.
func (s *Service) SpaceHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.collectSpaceHits(ctx, "SELECT "+spaceHitColumns+" FROM spaces s WHERE s.id = ANY($1) AND s.id = ANY($2)",
		db.Args{nonNil(ids), a.Spaces(PermView)}, false)
}
