// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

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

// Kontor's part of the quick search and the search page: issues by key and summary (and,
// on the search page, by their texts) and projects by key and name. Package links merges them with the other
// products. Every query applies the principal's access, so that nothing they may not see is found.

// searchKey is an issue key typed into a search: the project's key and a number.
var searchKey = regexp.MustCompile(`^([A-Z][A-Z0-9_]{1,9})-([1-9][0-9]{0,9})$`)

// keyDigits is how many digits the quick search adds to the number of a key: "KOG-12" also finds KOG-120 to
// KOG-12999, the issues whose keys it begins.
const keyDigits = 3

// walkProjects is up to how many projects the quick search reads the matches of short words project by project.
const walkProjects = 10

// issueHitRow is an issue found, as read.
type issueHitRow struct {
	id, typeID, statusID int64
	key, summary         string
	project              string
	archived             bool
	updated              time.Time
	tier                 int
	score                float64
}

const issueHitColumns = "i.id, i.key, i.summary, i.type_id, i.status_id, i.updated_at, pr.name, pr.archived"

func (s *Service) collectIssueHits(ctx context.Context, sql string, args db.Args, ranked bool) ([]issueHitRow, error) {
	rows, err := s.pool.Query(ctx, sql, db.CustomPlan(args)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (issueHitRow, error) {
		var h issueHitRow
		dest := []any{&h.id, &h.key, &h.summary, &h.typeID, &h.statusID, &h.updated, &h.project, &h.archived}
		if ranked {
			dest = append(dest, &h.tier, &h.score)
		}
		err := row.Scan(dest...)
		return h, err
	})
}

func issueHit(cfg *configSnapshot, h issueHitRow) search.Hit {
	updated := h.updated
	hit := search.Hit{Kind: search.KindIssue, ID: h.id, Key: h.key, Title: h.summary, Context: h.project, URL: "/browse/" + h.key,
		Updated: &updated, Archived: h.archived, Tier: h.tier, Score: h.score}
	if st := cfg.statuses[h.statusID]; st != nil {
		hit.Status = &search.Status{Name: st.Name, Category: st.Category}
	}
	if t := cfg.types[h.typeID]; t != nil {
		hit.Type = &search.Type{Name: t.Name, Icon: t.Icon, Color: t.Color}
	}
	return hit
}

// SearchIssues finds the issues the principal may see: the issue of a key typed in full, the issues whose keys it
// begins and the issues whose summaries match the words; with r.Text (the search page) also the issues whose
// texts match them, a page of them at a time.
func (s *Service) SearchIssues(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if r.Query.Empty() {
		return out, nil
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.Text {
		return s.searchIssueTexts(ctx, cfg, a, r)
	}
	if out.Hits, err = s.issueTitleHits(ctx, cfg, a, r.Query, r.LimitOr(5, 50), true); err != nil {
		return nil, err
	}
	return out, nil
}

// issueTitleHits finds issues by key — the exact key first and, with begun, the keys it begins — and by the words
// of their summaries: the best limit of them.
func (s *Service) issueTitleHits(ctx context.Context, cfg *configSnapshot, a *Access, q search.Query, limit int, begun bool) ([]search.Hit, error) {
	var found []issueHitRow
	if m := searchKey.FindStringSubmatch(strings.ToUpper(q.Text)); m != nil {
		keys, err := s.issuesByKey(ctx, a, m[0], m[1], m[2], limit, begun)
		if err != nil {
			return nil, err
		}
		found = keys
	}
	if prefix := q.Prefix(); prefix != "" {
		// The candidates are the most recently updated issues whose summaries match the words, so that words that
		// begin many summaries cost no more than rare ones. Among them the whole summary and its beginning come
		// first, then the summaries most like the text.
		var args db.Args
		lower, start := args.Add(q.Lower()), args.Add(q.StartsWith())
		match := "kogge_simple_words(i.summary) @@ to_tsquery('simple', " + args.Add(prefix) + ") AND " + a.VisibleSQL("i", &args)
		n := strconv.Itoa(search.Candidates)
		candidates := `SELECT i.id, i.key, i.summary, i.type_id, i.status_id, i.updated_at, i.project_id FROM issues i
			WHERE ` + match + ` ORDER BY i.updated_at DESC LIMIT ` + n
		if projects := a.BrowsableProjects(); !a.system && len(projects) <= walkProjects && q.ShortWords() {
			// Words of up to three letters begin a large part of all summaries. Who sees a few projects only finds
			// their newest matches fastest project by project: the index of the words would read the matches of
			// the whole instance first (the scale tests at size M). Every beginning of a word is in the summary,
			// which is cheaper to check than its words, so that rare beginnings cost little more than common ones.
			contains := ""
			for _, w := range q.PrefixWords() {
				contains += " AND lower(i.summary) LIKE '%' || lower(" + args.Add(w) + ") || '%'"
			}
			candidates = `SELECT c.* FROM unnest(` + args.Add(projects) + `::bigint[]) AS vp(id) CROSS JOIN LATERAL (
					SELECT i.id, i.key, i.summary, i.type_id, i.status_id, i.updated_at, i.project_id FROM issues i
					WHERE i.project_id = vp.id` + contains + ` AND ` + match + ` ORDER BY i.updated_at DESC LIMIT ` + n + `) c
				ORDER BY c.updated_at DESC LIMIT ` + n
		}
		sql := `SELECT ` + issueHitColumns + `,
				CASE WHEN lower(i.summary) = ` + lower + ` THEN 0 WHEN lower(i.summary) LIKE ` + start + ` THEN 1 ELSE 2 END,
				similarity(lower(i.summary), ` + lower + `)::float8
			FROM (` + candidates + `) i JOIN issue_projects pr ON pr.id = i.project_id
			ORDER BY 9, 10 DESC, i.updated_at DESC LIMIT ` + strconv.Itoa(limit)
		words, err := s.collectIssueHits(ctx, sql, args, true)
		if err != nil {
			return nil, err
		}
		found = append(found, words...)
	}
	hits := []search.Hit{}
	seen := map[int64]bool{}
	for _, h := range found {
		if !seen[h.id] {
			seen[h.id] = true
			hits = append(hits, issueHit(cfg, h))
		}
	}
	search.Sort(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// issuesByKey finds the issue of a key (also a former key of a moved issue) and, with begun, after it the issues
// of the project whose numbers begin with the key's number.
func (s *Service) issuesByKey(ctx context.Context, a *Access, key, projectKey, number string, limit int, begun bool) ([]issueHitRow, error) {
	n, err := strconv.ParseInt(number, 10, 64)
	if err != nil {
		return nil, nil
	}
	var args db.Args
	k := args.Add(key)
	sql := `(SELECT ` + issueHitColumns + `, 0, 1.0::float8 FROM issues i JOIN issue_projects pr ON pr.id = i.project_id
		WHERE i.id IN (SELECT id FROM issues WHERE key = ` + k + ` UNION SELECT issue_id FROM issue_keys WHERE key = ` + k + `)
		AND ` + a.VisibleSQL("i", &args) + `)`
	if begun {
		var ranges []string
		for d, f := 1, int64(10); d <= keyDigits; d, f = d+1, f*10 {
			ranges = append(ranges, "i.number BETWEEN "+args.Add(n*f)+" AND "+args.Add((n+1)*f-1))
		}
		sql += ` UNION ALL (SELECT ` + issueHitColumns + `, 1, -i.number::float8 FROM issues i JOIN issue_projects pr ON pr.id = i.project_id
			WHERE i.project_id = (SELECT project_id FROM issue_project_keys WHERE key = ` + args.Add(projectKey) + `)
			AND (` + strings.Join(ranges, " OR ") + `) AND ` + a.VisibleSQL("i", &args) + ` ORDER BY i.number LIMIT ` + strconv.Itoa(limit) + `)`
	}
	return s.collectIssueHits(ctx, sql, args, true)
}

// searchIssueTexts is the search page's search of issues: the issue of the key and the issues whose summaries
// match come first, ranked as the quick search ranks them; the other issues whose texts match (summary,
// description, comments, other fields; stemmed, or by the beginnings of words) follow, newest first.
func (s *Service) searchIssueTexts(ctx context.Context, cfg *configSnapshot, a *Access, r search.Request) (*search.Result, error) {
	titles, err := s.issueTitleHits(ctx, cfg, a, r.Query, search.Candidates, false)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var args db.Args
	lang := args.Add(st.ContentLanguage) + "::regconfig"
	conds := []string{"i.search_tsv @@ websearch_to_tsquery(" + lang + ", " + args.Add(r.Query.Text) + ")"}
	if m := searchKey.FindStringSubmatch(strings.ToUpper(r.Query.Text)); m != nil {
		conds = append(conds, "i.key = "+args.Add(m[0]))
	}
	if p := r.Query.Prefix(); p != "" {
		conds = append(conds, "kogge_simple_words(i.key || ' ' || i.summary || ' ' || i.description_text) @@ to_tsquery('simple', "+args.Add(p)+")")
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND " + a.VisibleSQL("i", &args)
	out := &search.Result{Hits: []search.Hit{}}
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM issues i WHERE "+where, db.CustomPlan(args)...).Scan(&out.Total); err != nil {
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
		rows, err := s.collectIssueHits(ctx, `SELECT `+issueHitColumns+` FROM issues i JOIN issue_projects pr ON pr.id = i.project_id
			WHERE `+where+` AND NOT (i.id = ANY(`+args.Add(ids)+`))
			ORDER BY i.updated_at DESC, i.id DESC LIMIT `+strconv.Itoa(textLimit)+` OFFSET `+strconv.Itoa(textOffset), args, false)
		if err != nil {
			return nil, err
		}
		for _, h := range rows {
			h.tier = search.TierText
			out.Hits = append(out.Hits, issueHit(cfg, h))
		}
	}
	return out, s.issueExcerpts(ctx, st.ContentLanguage, r.Query, out.Hits)
}

// issueExcerpts sets the excerpts of hits, the parts of their descriptions that match, and their assignees.
func (s *Service) issueExcerpts(ctx context.Context, lang string, q search.Query, hits []search.Hit) error {
	if len(hits) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, ts_headline($1::regconfig, description_text, websearch_to_tsquery($1::regconfig, $2), $3),
		`+search.HeadlineEnds("description_text")+` FROM issues WHERE id = ANY($4)`, lang, q.Text, search.HeadlineOptions, ids)
	if err != nil {
		return err
	}
	excerpts, err := search.CollectExcerpts(rows)
	if err != nil {
		return err
	}
	if rows, err = s.pool.Query(ctx, `SELECT i.id, u.id, u.username, u.display_name, u.status = 'active' FROM issues i
		JOIN users u ON u.id = i.assignee_id WHERE i.id = ANY($1)`, ids); err != nil {
		return err
	}
	assignees, err := search.CollectPeople(rows)
	if err != nil {
		return err
	}
	for i := range hits {
		hits[i].Excerpt, hits[i].Person = excerpts[hits[i].ID], assignees[hits[i].ID]
	}
	return nil
}

// IssueHits returns the issues of the ids that the principal may see (the recently visited ones of the quick
// search).
func (s *Service) IssueHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	found, err := s.collectIssueHits(ctx, `SELECT `+issueHitColumns+` FROM issues i JOIN issue_projects pr ON pr.id = i.project_id
		WHERE i.id = ANY($1) AND `+a.VisibleSQL("i", &args), args, false)
	if err != nil {
		return nil, err
	}
	out := make([]search.Hit, 0, len(found))
	for _, h := range found {
		out = append(out, issueHit(cfg, h))
	}
	return out, nil
}

// visibleProjectsSQL restricts projects (alias p) to those the principal finds in the list of projects: those in
// which they can see issues, and for administrators all.
func (s *Service) visibleProjectsSQL(ctx context.Context, p *identity.Principal, a *Access, args *db.Args) string {
	if admin, _ := s.authz.IsAdmin(ctx, p); admin {
		return "TRUE"
	}
	return "p.id = ANY(" + args.Add(a.BrowsableProjects()) + ")"
}

const projectHitColumns = "p.id, p.key, p.name, p.archived, p.updated_at"

func (s *Service) collectProjectHits(ctx context.Context, sql string, args db.Args, ranked bool) ([]search.Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (search.Hit, error) {
		h := search.Hit{Kind: search.KindProject, Updated: new(time.Time)}
		dest := []any{&h.ID, &h.Key, &h.Title, &h.Archived, h.Updated}
		if ranked {
			dest = append(dest, &h.Tier, &h.Score)
		}
		err := row.Scan(dest...)
		h.URL = "/projects/" + h.Key
		return h, err
	})
}

// SearchProjects finds the projects the principal may see by key and name (with r.Text also by description).
func (s *Service) SearchProjects(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
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
	conds := []string{"p.key = " + key, "p.key LIKE " + keyStart, "lower(p.name) LIKE " + contains}
	words := "FALSE"
	if prefix := r.Query.Prefix(); prefix != "" {
		words = "to_tsvector('simple', p.name) @@ to_tsquery('simple', " + args.Add(prefix) + ")"
		conds = append(conds, words)
	}
	if r.Text {
		st, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		lang := args.Add(st.ContentLanguage) + "::regconfig"
		conds = append(conds, "to_tsvector("+lang+", p.description) @@ websearch_to_tsquery("+lang+", "+args.Add(r.Query.Text)+")")
	}
	where := "(" + strings.Join(conds, " OR ") + ") AND " + s.visibleProjectsSQL(ctx, p, a, &args)
	if r.Count {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM issue_projects p WHERE "+where, args...).Scan(&out.Total); err != nil {
			return nil, err
		}
	}
	lower, start := args.Add(r.Query.Lower()), args.Add(r.Query.StartsWith())
	tier := `CASE WHEN p.key = ` + key + ` OR lower(p.name) = ` + lower + ` THEN 0
		WHEN p.key LIKE ` + keyStart + ` OR lower(p.name) LIKE ` + start + ` THEN 1 WHEN ` + words + ` THEN 2 ELSE 3 END`
	sql := `SELECT ` + projectHitColumns + `, ` + tier + `, similarity(lower(p.name), ` + lower + `)::float8 FROM issue_projects p WHERE ` + where +
		` ORDER BY 6, p.archived, 7 DESC, lower(p.name), p.id LIMIT ` + strconv.Itoa(r.LimitOr(5, 100)) + ` OFFSET ` + strconv.Itoa(max(r.Offset, 0))
	if out.Hits, err = s.collectProjectHits(ctx, sql, args, true); err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectHits returns the projects of the ids that the principal may see.
func (s *Service) ProjectHits(ctx context.Context, p *identity.Principal, ids []int64) ([]search.Hit, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	args := db.Args{nonNil(ids)}
	return s.collectProjectHits(ctx, "SELECT "+projectHitColumns+" FROM issue_projects p WHERE p.id = ANY($1) AND "+
		s.visibleProjectsSQL(ctx, p, a, &args), args, false)
}
