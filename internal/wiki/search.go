// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/search"
)

// PageSummary is a page in lists and search results.
type PageSummary struct {
	ID      int64    `json:"id"`
	Title   string   `json:"title"`
	Kind    string   `json:"kind" enum:"page,blog"`
	Space   SpaceRef `json:"space"`
	Excerpt string   `json:"excerpt" doc:"The beginning of the page as clauses (headings, paragraphs, list items, table rows; no code), cut at a word with …; search results: the text around the matches, with matches between \u0001 and \u0002"`
	// Path is set in search results.
	Path      []string          `json:"path,omitempty" doc:"Search results: the titles of the pages above it, below the space's home page"`
	Labels    []string          `json:"labels"`
	UpdatedBy *identity.UserRef `json:"updatedBy,omitempty"`
	Updated   time.Time         `json:"updated"`
	Published *time.Time        `json:"published,omitempty"`
	URL       string            `json:"url"`
}

// excerptLength is how many characters the excerpts of lists have: two lines of a list of pages.
const excerptLength = 240

// excerptBlocks and excerptMacro select what the excerpt of a page is made from (docmodel.Excerpt): its first
// blocks, which hold more text than the excerpt shows, and an excerpt macro near the top of the tree, which
// replaces them. Bodies are not read whole for a list.
const (
	excerptBlocks = "jsonb_path_query_array(p.body, '$.content[0 to 11]')"
	excerptMacro  = `jsonb_path_query_first(p.body, '$.content[*].**{0 to 4} ? (@.type == "macro" && @.attrs.name == "excerpt")')`
)

type summaryQuery struct {
	where []string
	args  db.Args
	// order ends with a unique column, so that the pages of a list do not overlap.
	order    string
	limit    int
	offset   int
	headline string // search query for excerpts
	paths    bool   // with the paths of the pages
}

func (s *Service) summaries(ctx context.Context, a *Access, q summaryQuery) ([]PageSummary, int, error) {
	visible := a.VisibleSQL("p", &q.args)
	where := append([]string{visible}, q.where...)
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM pages p WHERE "+cond, q.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	excerpt := excerptBlocks + ", " + excerptMacro + ", NULL::text, NULL::text, NULL::text"
	if q.headline != "" {
		st, err := s.settings.Get(ctx)
		if err != nil {
			return nil, 0, err
		}
		excerpt = "NULL::jsonb, NULL::jsonb, ts_headline(" + q.args.Add(st.ContentLanguage) + "::regconfig, p.body_text, websearch_to_tsquery(" +
			q.args.Add(st.ContentLanguage) + "::regconfig, " + q.args.Add(q.headline) + "), " + q.args.Add(search.HeadlineOptions) + "), " +
			search.HeadlineEnds("p.body_text")
	}
	// A page is as long as asked, up to the API's maximum: a limit is never replaced by a shorter one without
	// saying so.
	if q.limit <= 0 {
		q.limit = 25
	}
	// The ids of the page come first, in their order; excerpts and labels are read for them only. In one query
	// with the order, the offset and the limit, they were read for every matching page (for a blog of 2,000
	// posts, 90 ms instead of 5 ms per page of the list).
	ids := "ARRAY(SELECT p.id FROM pages p WHERE " + cond + " ORDER BY " + q.order + " LIMIT " + q.args.Add(pageLimit(q.limit)) +
		" OFFSET " + q.args.Add(max(q.offset, 0)) + ")"
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.title, p.kind, sp.id, sp.key, sp.name, `+excerpt+`,
		array(SELECT label FROM page_labels l WHERE l.page_id = p.id ORDER BY label), p.updated_by, u.username, u.display_name, p.updated_at,
		p.published_at FROM unnest(`+ids+`) WITH ORDINALITY AS page(id, n) JOIN pages p ON p.id = page.id
		JOIN spaces sp ON sp.id = p.space_id LEFT JOIN users u ON u.id = p.updated_by ORDER BY page.n`, q.args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PageSummary, error) {
		var r PageSummary
		var uid *int64
		var un, dn *string
		var blocks, macro []byte
		var headline, head, tail *string
		err := row.Scan(&r.ID, &r.Title, &r.Kind, &r.Space.ID, &r.Space.Key, &r.Space.Name, &blocks, &macro, &headline, &head, &tail,
			&r.Labels, &uid, &un, &dn, &r.Updated, &r.Published)
		if uid != nil {
			r.UpdatedBy = &identity.UserRef{ID: *uid, Username: *un, DisplayName: *dn, Active: true}
		}
		if r.Labels == nil {
			r.Labels = []string{}
		}
		if headline != nil {
			r.Excerpt = search.Headline(*headline, deref(head), deref(tail))
		} else {
			r.Excerpt = excerptOf(blocks, macro, excerptLength)
		}
		r.URL = pageURL(r.Space.Key, r.ID, r.Title)
		return r, err
	})
	if err != nil {
		return nil, 0, err
	}
	if q.paths && len(out) > 0 {
		ids := make([]int64, 0, len(out))
		for _, r := range out {
			ids = append(ids, r.ID)
		}
		paths, err := s.pagePaths(ctx, a, ids)
		if err != nil {
			return nil, 0, err
		}
		for i := range out {
			out[i].Path = paths[out[i].ID]
		}
	}
	return nonNil(out), total, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// excerptOf returns the excerpt of a page from its first blocks and its excerpt macro (JSON, as excerptBlocks
// and excerptMacro select them).
func excerptOf(blocks, macro []byte, max int) string {
	d := &docmodel.Doc{Type: "doc"}
	_ = json.Unmarshal(blocks, &d.Content)
	var m docmodel.Node
	if len(macro) > 0 && json.Unmarshal(macro, &m) == nil && m.Type == "macro" {
		d.Content = append(d.Content, &m)
	}
	return docmodel.Excerpt(d, max)
}

// pagePaths returns the titles of the pages above pages (that the principal may read), from the top down, without
// the home pages of their spaces.
func (s *Service) pagePaths(ctx context.Context, a *Access, ids []int64) (map[int64][]string, error) {
	args := db.Args{ids}
	rows, err := s.pool.Query(ctx, `WITH RECURSIVE up (page, id, depth) AS (
			SELECT p.id, p.parent_id, 1 FROM pages p WHERE p.id = ANY($1) AND p.kind = 'page'
			UNION ALL
			SELECT up.page, p.parent_id, up.depth + 1 FROM up JOIN pages p ON p.id = up.id WHERE up.depth < 50)
		SELECT up.page, p.title FROM up JOIN pages p ON p.id = up.id JOIN spaces sp ON sp.id = p.space_id
		WHERE p.id IS DISTINCT FROM sp.home_page_id AND p.status IN ('current', 'archived') AND `+a.VisibleSQL("p", &args)+`
		ORDER BY up.page, up.depth DESC`, args...)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for rows.Next() {
		var page int64
		var title string
		if err := rows.Scan(&page, &title); err != nil {
			rows.Close()
			return nil, err
		}
		out[page] = append(out[page], title)
	}
	return out, rows.Err()
}

// SearchInput searches pages and blog posts.
type SearchInput struct {
	Query string
	Space string
	Label string
	Kind  string
	// Sort is relevance (with a query; the default) or updated (the most recently updated first).
	Sort string
	// UpdatedWithin keeps the pages updated within as many days (0: any).
	UpdatedWithin int
	// Facets asks for the counts of the matches by space and kind.
	Facets bool
	Limit  int
	Offset int
}

// SearchResult is a page of search results.
type SearchResult struct {
	Total  int           `json:"total"`
	Pages  []PageSummary `json:"pages"`
	Facets *SearchFacets `json:"facets,omitempty" doc:"With facets: how many matches there are in each space and of each kind"`
}

// SearchFacets count the matches of a search by space (whatever the kind asked for) and by kind (in the space
// asked for), so that each filter shows what choosing it would find.
type SearchFacets struct {
	Spaces []SpaceFacet `json:"spaces" doc:"The spaces with matches, the most first"`
	Kinds  []KindFacet  `json:"kinds"`
}

// SpaceFacet is a space with the number of matches in it.
type SpaceFacet struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// KindFacet is a kind of page with the number of matches of it.
type KindFacet struct {
	Kind  string `json:"kind" enum:"page,blog"`
	Count int    `json:"count"`
}

// Search finds current pages and blog posts by their text, title, labels and comments.
func (s *Service) Search(ctx context.Context, p *identity.Principal, in SearchInput) (*SearchResult, error) {
	defer metrics.Search("wiki", time.Now())
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Imports change many pages at the same time; the id breaks ties.
	q := summaryQuery{where: []string{"p.status = 'current'"}, order: "p.updated_at DESC, p.id DESC", limit: in.Limit, offset: in.Offset,
		paths: true}
	if in.Query != "" {
		ts := "websearch_to_tsquery(" + q.args.Add(st.ContentLanguage) + "::regconfig, " + q.args.Add(in.Query) + ")"
		like := "%" + db.EscapeLike(strings.ToLower(in.Query)) + "%"
		q.where = append(q.where, "(p.search_tsv @@ "+ts+" OR lower(p.title) LIKE "+q.args.Add(like)+")")
		if in.Sort != "updated" {
			q.order = "ts_rank(p.search_tsv, " + ts + ") DESC, p.updated_at DESC, p.id DESC"
		}
		q.headline = in.Query
	}
	if in.Label != "" {
		q.where = append(q.where, "EXISTS (SELECT 1 FROM page_labels l WHERE l.page_id = p.id AND l.label = "+q.args.Add(strings.ToLower(in.Label))+")")
	}
	if in.UpdatedWithin > 0 {
		q.where = append(q.where, "p.updated_at >= now() - make_interval(days => "+q.args.Add(in.UpdatedWithin)+")")
	}
	// The facets count what the other conditions find, without the space and the kind.
	facetWhere, facetArgs := append([]string{}, q.where...), append(db.Args{}, q.args...)
	var spaceID int64
	if in.Space != "" {
		sp, err := s.visibleSpace(ctx, a, in.Space)
		if err != nil {
			return nil, err
		}
		spaceID = sp.ID
		q.where = append(q.where, "p.space_id = "+q.args.Add(sp.ID))
	}
	if in.Kind != "" {
		q.where = append(q.where, "p.kind = "+q.args.Add(in.Kind))
	}
	list, total, err := s.summaries(ctx, a, q)
	if err != nil {
		return nil, err
	}
	out := &SearchResult{Total: total, Pages: list}
	if in.Facets {
		if out.Facets, err = s.searchFacets(ctx, a, facetWhere, facetArgs, spaceID, in.Kind); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// searchFacets counts the matches of the conditions by space (of the kind, if one is given) and by kind (in the
// space, if one is given).
func (s *Service) searchFacets(ctx context.Context, a *Access, where []string, args db.Args, spaceID int64, kind string) (*SearchFacets, error) {
	where = append([]string{a.VisibleSQL("p", &args)}, where...)
	rows, err := s.pool.Query(ctx, `SELECT p.kind, sp.id, sp.key, sp.name, count(*) FROM pages p JOIN spaces sp ON sp.id = p.space_id
		WHERE `+strings.Join(where, " AND ")+` GROUP BY p.kind, sp.id, sp.key, sp.name`, args...)
	if err != nil {
		return nil, err
	}
	out := &SearchFacets{Spaces: []SpaceFacet{}, Kinds: []KindFacet{}}
	spaces := map[int64]int{}
	kinds := map[string]int{}
	for rows.Next() {
		var k, key, name string
		var id int64
		var n int
		if err := rows.Scan(&k, &id, &key, &name, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if kind == "" || k == kind {
			if i, ok := spaces[id]; ok {
				out.Spaces[i].Count += n
			} else {
				spaces[id] = len(out.Spaces)
				out.Spaces = append(out.Spaces, SpaceFacet{Key: key, Name: name, Count: n})
			}
		}
		if spaceID == 0 || id == spaceID {
			kinds[k] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out.Spaces, func(x, y SpaceFacet) int {
		if c := cmp.Compare(y.Count, x.Count); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	for _, k := range []string{"page", "blog"} {
		out.Kinds = append(out.Kinds, KindFacet{Kind: k, Count: kinds[k]})
	}
	return out, nil
}

// Recent lists pages the principal visited recently (mode viewed) or recently updated pages (mode updated).
func (s *Service) Recent(ctx context.Context, p *identity.Principal, mode, spaceKey string, limit int) ([]PageSummary, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	q := summaryQuery{where: []string{"p.status = 'current'"}, order: "p.updated_at DESC, p.id DESC", limit: limit}
	if spaceKey != "" {
		sp, err := s.visibleSpace(ctx, a, spaceKey)
		if err != nil {
			return nil, err
		}
		q.where = append(q.where, "p.space_id = "+q.args.Add(sp.ID))
	}
	if mode == "viewed" {
		if a.userID == 0 {
			return []PageSummary{}, nil
		}
		uid := q.args.Add(a.userID)
		q.where = append(q.where, "EXISTS (SELECT 1 FROM page_visits v WHERE v.page_id = p.id AND v.user_id = "+uid+")")
		q.order = "(SELECT v.visited_at FROM page_visits v WHERE v.page_id = p.id AND v.user_id = " + uid + ") DESC, p.id DESC"
	}
	list, _, err := s.summaries(ctx, a, q)
	return list, err
}

// Blog lists the blog posts of a space, newest first.
func (s *Service) Blog(ctx context.Context, p *identity.Principal, spaceKey string, limit, offset int) (*SearchResult, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, spaceKey)
	if err != nil {
		return nil, err
	}
	q := summaryQuery{where: []string{"p.status = 'current'", "p.kind = 'blog'"}, order: "p.published_at DESC NULLS LAST, p.id DESC",
		limit: limit, offset: offset}
	q.where = append(q.where, "p.space_id = "+q.args.Add(sp.ID))
	list, total, err := s.summaries(ctx, a, q)
	if err != nil {
		return nil, err
	}
	return &SearchResult{Total: total, Pages: list}, nil
}

// Mentions lists the current pages that mention an issue, by any of its keys.
func (s *Service) Mentions(ctx context.Context, p *identity.Principal, issueKey string) ([]PageSummary, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	keys := []string{strings.ToUpper(issueKey)}
	if s.issueKeys != nil {
		if keys, err = s.issueKeys(ctx, p, issueKey); err != nil {
			return nil, err
		}
	}
	q := summaryQuery{where: []string{"p.status = 'current'"}, order: "p.updated_at DESC, p.id DESC", limit: 100}
	q.where = append(q.where, "EXISTS (SELECT 1 FROM page_issue_refs r WHERE r.page_id = p.id AND r.issue_key = ANY("+q.args.Add(keys)+"))")
	list, _, err := s.summaries(ctx, a, q)
	return list, err
}

// LabelCount is a label with the number of visible pages using it.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count" doc:"Current pages and blog posts with the label that the caller may read"`
}

// Labels lists the 200 most used labels, optionally of a space.
func (s *Service) Labels(ctx context.Context, p *identity.Principal, spaceKey string) ([]LabelCount, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := a.VisibleSQL("p", &args) + " AND p.status = 'current'"
	if spaceKey != "" {
		sp, err := s.visibleSpace(ctx, a, spaceKey)
		if err != nil {
			return nil, err
		}
		where += " AND p.space_id = " + args.Add(sp.ID)
	}
	rows, err := s.pool.Query(ctx, "SELECT l.label, count(*) FROM page_labels l JOIN pages p ON p.id = l.page_id WHERE "+where+
		" GROUP BY l.label ORDER BY 2 DESC, 1 LIMIT 200", args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[LabelCount])
	return nonNil(out), err
}

// Presence lists who else has a page open; the call records the caller (heartbeat).
func (s *Service) Presence(ctx context.Context, p *identity.Principal, id int64, editing bool) ([]PresenceEntry, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.readablePage(ctx, s.pool, a, id, false); err != nil {
		return nil, err
	}
	if a.userID == 0 {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO page_presence (page_id, user_id, editing) VALUES ($1, $2, $3)
		ON CONFLICT (page_id, user_id) DO UPDATE SET editing = EXCLUDED.editing, seen_at = now()`, id, a.userID, editing); err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM page_presence WHERE seen_at < now() - interval '5 minutes'"); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.username, u.display_name, pp.editing FROM page_presence pp JOIN users u ON u.id = pp.user_id
		WHERE pp.page_id = $1 AND pp.user_id <> $2 AND pp.seen_at > now() - interval '60 seconds' ORDER BY u.display_name`, id, a.userID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PresenceEntry, error) {
		var e PresenceEntry
		e.User.Active = true
		err := row.Scan(&e.User.ID, &e.User.Username, &e.User.DisplayName, &e.Editing)
		return e, err
	})
	return nonNil(out), err
}

// PresenceEntry is a user who has the page open.
type PresenceEntry struct {
	User    identity.UserRef `json:"user"`
	Editing bool             `json:"editing"`
}
