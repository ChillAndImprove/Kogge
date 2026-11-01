// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/search"
)

// Hafen's part of the quick search and the search page: code projects and repositories by
// key, path and name, pull requests by number and title (and, on the search page, by their descriptions). Package
// links merges them with the other products. The queries apply the principal's access to repositories.

// prNumber is the number of a pull request typed into a search: "12" or "#12".
var prNumber = regexp.MustCompile(`^#?([1-9][0-9]{0,8})$`)

func repoWebPath(projectKey, slug string) string {
	return "/code/" + strings.ToLower(projectKey) + "/" + slug
}

const codeProjectHitColumns = "p.id, p.key, p.name, p.updated_at"

func (s *Service) collectCodeProjectHits(ctx context.Context, sql string, args db.Args, ranked bool) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindCodeProject, Updated: new(time.Time)}
		dest := []any{&h.ID, &h.Key, &h.Title, h.Updated}
		if ranked {
			dest = append(dest, &h.Tier, &h.Score)
		}
		err := row.Scan(dest...)
		h.URL = "/code/" + strings.ToLower(h.Key)
		return h, err
	})
}

// SearchCodeProjects finds the code projects the principal may see by key and name (with r.Text also by
// description).
func (s *Service) SearchCodeProjects(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
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
	conds := []string{"upper(p.key) = " + key, "upper(p.key) LIKE " + keyStart, "lower(p.name) LIKE " + contains}
	words := "FALSE"
	if prefix := r.Query.Prefix(); prefix != "" {
		words = "to_tsvector('simple', p.name) @@ to_tsquery('simple', " + args.Add(prefix) + ")"
		conds = append(conds, words)
	}
	if r.Text {
		conds = append(conds, "lower(p.description) LIKE "+contains)
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND " + a.ProjectVisibleSQL("p", &args)
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM code_projects p WHERE "+where, args...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	lower, start := args.Add(r.Query.Lower()), args.Add(r.Query.StartsWith())
	tier := `CASE WHEN upper(p.key) = ` + key + ` OR lower(p.name) = ` + lower + ` THEN 0
		WHEN upper(p.key) LIKE ` + keyStart + ` OR lower(p.name) LIKE ` + start + ` THEN 1 WHEN ` + words + ` THEN 2 ELSE 3 END`
	sql := `SELECT ` + codeProjectHitColumns + `, ` + tier + `, similarity(lower(p.name), ` + lower + `)::float8 FROM code_projects p WHERE ` +
		where + ` ORDER BY 5, 6 DESC, lower(p.name), p.id LIMIT ` + strconv.Itoa(r.LimitOr(5, 100)) + ` OFFSET ` + strconv.Itoa(max(r.Offset, 0))
	if out.Hits, err = s.collectCodeProjectHits(ctx, sql, args, true); err != nil {
		return nil, err
	}
	return out, nil
}

// CodeProjectHits returns the code projects of the ids that the principal may see.
func (s *Service) CodeProjectHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	return s.collectCodeProjectHits(ctx, "SELECT "+codeProjectHitColumns+" FROM code_projects p WHERE p.id = ANY($1) AND "+
		a.ProjectVisibleSQL("p", &args), args, false)
}

const repoHitColumns = "r.id, p.key, r.slug, r.name, p.name, r.archived, r.updated_at"

func (s *Service) collectRepoHits(ctx context.Context, sql string, args db.Args, ranked bool) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindRepository, Updated: new(time.Time)}
		var key, slug string
		dest := []any{&h.ID, &key, &slug, &h.Title, &h.Context, &h.Archived, h.Updated}
		if ranked {
			dest = append(dest, &h.Tier, &h.Score)
		}
		err := row.Scan(dest...)
		h.Key, h.URL = key+"/"+slug, repoWebPath(key, slug)
		return h, err
	})
}

// SearchRepositories finds the repositories the principal may read: by path (KEY/slug), slug and name, all of a
// code project by its key, and with r.Text also by description.
func (s *Service) SearchRepositories(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
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
	start, contains := args.Add(r.Query.StartsWith()), args.Add(r.Query.Contains())
	key := args.Add(strings.ToUpper(r.Query.Text))
	path := "lower(p.key || '/' || r.slug)"
	conds := []string{"lower(r.name) LIKE " + contains, "lower(r.slug) LIKE " + contains, path + " LIKE " + start, "upper(p.key) = " + key}
	words := "FALSE"
	if prefix := r.Query.Prefix(); prefix != "" {
		words = "to_tsvector('simple', r.name || ' ' || r.slug) @@ to_tsquery('simple', " + args.Add(prefix) + ")"
		conds = append(conds, words)
	}
	if r.Text {
		conds = append(conds, "lower(r.description) LIKE "+contains)
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND " + a.VisibleSQL("r", &args)
	from := " FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE "
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*)"+from+where, args...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	lower := args.Add(r.Query.Lower())
	tier := `CASE WHEN lower(r.slug) = ` + lower + ` OR lower(r.name) = ` + lower + ` OR ` + path + ` = ` + lower + ` THEN 0
		WHEN lower(r.slug) LIKE ` + start + ` OR lower(r.name) LIKE ` + start + ` OR ` + path + ` LIKE ` + start + ` THEN 1
		WHEN ` + words + ` OR upper(p.key) = ` + key + ` THEN 2 ELSE 3 END`
	sql := `SELECT ` + repoHitColumns + `, ` + tier + `, similarity(lower(r.name), ` + lower + `)::float8` + from + where +
		` ORDER BY 8, r.archived, 9 DESC, r.updated_at DESC, r.id LIMIT ` + strconv.Itoa(r.LimitOr(5, 100)) + ` OFFSET ` + strconv.Itoa(max(r.Offset, 0))
	if out.Hits, err = s.collectRepoHits(ctx, sql, args, true); err != nil {
		return nil, err
	}
	return out, nil
}

// RepositoryHits returns the repositories of the ids that the principal may read.
func (s *Service) RepositoryHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	return s.collectRepoHits(ctx, "SELECT "+repoHitColumns+" FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE r.id = ANY($1) AND "+
		a.VisibleSQL("r", &args), args, false)
}

const prHitColumns = "pr.id, pr.number, pr.title, pr.state, pr.draft, pr.updated_at, p.key, r.slug, r.archived"

func (s *Service) collectPRHits(ctx context.Context, sql string, args db.Args, ranked bool, tier int) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, db.CustomPlan(args)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindPullRequest, Updated: new(time.Time), Tier: tier}
		var number int
		var draft bool
		var key, slug string
		dest := []any{&h.ID, &number, &h.Title, &h.State, &draft, h.Updated, &key, &slug, &h.Archived}
		if ranked {
			dest = append(dest, &h.Tier, &h.Score)
		}
		err := row.Scan(dest...)
		if draft && h.State == StateOpen {
			h.State = "draft"
		}
		h.Key, h.Context = "#"+strconv.Itoa(number), key+"/"+slug
		h.URL = repoWebPath(key, slug) + "/pull-requests/" + strconv.Itoa(number)
		return h, err
	})
}

// SearchPullRequests finds the pull requests of repositories the principal may read: by number (12 or #12) and the
// words of their titles, and with r.Text (the search page) also by their descriptions.
func (s *Service) SearchPullRequests(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if r.Query.Empty() {
		return out, nil
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if !r.Text {
		if out.Hits, err = s.prTitleHits(ctx, a, r.Query, r.LimitOr(5, 50)); err != nil {
			return nil, err
		}
		return out, nil
	}
	// The search page: the number and the titles that match first, ranked as the quick search ranks them, then the
	// other pull requests whose descriptions match, newest first.
	titles, err := s.prTitleHits(ctx, a, r.Query, search.Candidates)
	if err != nil {
		return nil, err
	}
	var args db.Args
	var conds []string
	if m := prNumber.FindStringSubmatch(r.Query.Text); m != nil {
		n, _ := strconv.Atoi(m[1])
		conds = append(conds, "pr.number = "+args.Add(n))
	}
	prefix := r.Query.Prefix()
	if prefix != "" {
		conds = append(conds, "to_tsvector('simple', pr.title || ' ' || pr.description_text) @@ to_tsquery('simple', "+args.Add(prefix)+")")
	}
	if len(conds) == 0 {
		return out, nil
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND " + a.VisibleSQL("r", &args)
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*)"+prFrom+" WHERE "+where, db.CustomPlan(args)...).Scan(&out.Total); err != nil {
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
		texts, err := s.collectPRHits(ctx, `SELECT `+prHitColumns+prFrom+` WHERE `+where+` AND NOT (pr.id = ANY(`+args.Add(ids)+`))
			ORDER BY pr.updated_at DESC, pr.id DESC LIMIT `+strconv.Itoa(textLimit)+` OFFSET `+strconv.Itoa(textOffset), args, false, search.TierText)
		if err != nil {
			return nil, err
		}
		out.Hits = append(out.Hits, texts...)
	}
	if len(out.Hits) > 0 && prefix != "" {
		ids := make([]int64, 0, len(out.Hits))
		for _, h := range out.Hits {
			ids = append(ids, h.ID)
		}
		rows, err := s.pool.Query(ctx, `SELECT id, ts_headline('simple', description_text, to_tsquery('simple', $1), $2),
			`+search.HeadlineEnds("description_text")+` FROM pull_requests WHERE id = ANY($3)`, prefix, search.HeadlineOptions, ids)
		if err != nil {
			return nil, err
		}
		excerpts, err := search.CollectExcerpts(rows)
		if err != nil {
			return nil, err
		}
		if rows, err = s.pool.Query(ctx, `SELECT pr.id, u.id, u.username, u.display_name, u.status = 'active' FROM pull_requests pr
			JOIN users u ON u.id = pr.author_id WHERE pr.id = ANY($1)`, ids); err != nil {
			return nil, err
		}
		authors, err := search.CollectPeople(rows)
		if err != nil {
			return nil, err
		}
		for i := range out.Hits {
			out.Hits[i].Excerpt, out.Hits[i].Person = excerpts[out.Hits[i].ID], authors[out.Hits[i].ID]
		}
	}
	return out, nil
}

// prTitleHits finds pull requests by number and by the words of their titles (the candidates are the most
// recently updated ones whose titles match): the best limit of them.
func (s *Service) prTitleHits(ctx context.Context, a *Access, q search.Query, limit int) ([]search.Hit, error) {
	hits := []search.Hit{}
	if m := prNumber.FindStringSubmatch(q.Text); m != nil {
		n, _ := strconv.Atoi(m[1])
		var args db.Args
		numbered, err := s.collectPRHits(ctx, `SELECT `+prHitColumns+`, 0, 1.0::float8`+prFrom+` WHERE pr.number = `+args.Add(n)+` AND `+
			a.VisibleSQL("r", &args)+` ORDER BY pr.updated_at DESC LIMIT `+strconv.Itoa(limit), args, true, 0)
		if err != nil {
			return nil, err
		}
		hits = append(hits, numbered...)
	}
	if prefix := q.Prefix(); prefix != "" {
		var args db.Args
		lower := args.Add(q.Lower())
		sql := `SELECT ` + prHitColumns + `,
				CASE WHEN lower(pr.title) = ` + lower + ` THEN 0 WHEN lower(pr.title) LIKE ` + args.Add(q.StartsWith()) + ` THEN 1 ELSE 2 END,
				similarity(lower(pr.title), ` + lower + `)::float8
			FROM (SELECT pr.id, pr.number, pr.title, pr.state, pr.draft, pr.updated_at, pr.repository_id FROM pull_requests pr
				JOIN repositories r ON r.id = pr.repository_id
				WHERE to_tsvector('simple', pr.title) @@ to_tsquery('simple', ` + args.Add(prefix) + `) AND ` + a.VisibleSQL("r", &args) + `
				ORDER BY pr.updated_at DESC LIMIT ` + strconv.Itoa(search.Candidates) + `) pr
			JOIN repositories r ON r.id = pr.repository_id JOIN code_projects p ON p.id = r.project_id
			ORDER BY 10, 11 DESC, pr.updated_at DESC LIMIT ` + strconv.Itoa(limit)
		found, err := s.collectPRHits(ctx, sql, args, true, 0)
		if err != nil {
			return nil, err
		}
		seen := map[int64]bool{}
		for _, h := range hits {
			seen[h.ID] = true
		}
		for _, h := range found {
			if !seen[h.ID] {
				hits = append(hits, h)
			}
		}
	}
	search.Sort(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// PullRequestHits returns the pull requests of the ids that the principal may see.
func (s *Service) PullRequestHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	return s.collectPRHits(ctx, "SELECT "+prHitColumns+prFrom+" WHERE pr.id = ANY($1) AND "+a.VisibleSQL("r", &args), args, false, 0)
}
