// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/metrics"
)

// IssueRow is an issue in search results.
type IssueRow struct {
	ID                int64             `json:"id"`
	Key               string            `json:"key"`
	Project           ProjectRef        `json:"project"`
	Summary           string            `json:"summary"`
	Type              *IssueType        `json:"type"`
	Status            *Status           `json:"status"`
	Priority          *Priority         `json:"priority,omitempty"`
	Resolution        *Resolution       `json:"resolution,omitempty"`
	Assignee          *identity.UserRef `json:"assignee,omitempty"`
	Reporter          *identity.UserRef `json:"reporter,omitempty"`
	ParentKey         string            `json:"parentKey,omitempty"`
	Labels            []string          `json:"labels"`
	DueDate           *string           `json:"dueDate,omitempty"`
	OriginalEstimate  *int64            `json:"originalEstimate,omitempty"`
	RemainingEstimate *int64            `json:"remainingEstimate,omitempty"`
	TimeSpent         int64             `json:"timeSpent"`
	Votes             int               `json:"votes"`
	Fields            map[string]any    `json:"fields"`
	Rank              string            `json:"rank"`
	Created           time.Time         `json:"created"`
	Updated           time.Time         `json:"updated"`
	Resolved          *time.Time        `json:"resolved,omitempty"`
	Components        []Ref             `json:"components"`
	FixVersions       []Ref             `json:"fixVersions"`
	AffectsVersions   []Ref             `json:"affectsVersions"`
	Sprint            *SprintRef        `json:"sprint,omitempty" doc:"The open sprint of the issue"`
	Display           map[string]string `json:"display" doc:"Custom field values as text, by key (cf_<id>)"`
}

// SearchResult is a page of issues.
type SearchResult struct {
	Total  int        `json:"total"`
	Query  string     `json:"query" doc:"The query in canonical form"`
	Issues []IssueRow `json:"issues"`
}

// newCompiler prepares a compiler for the principal.
func (s *Service) newCompiler(ctx context.Context, q db.Querier, p *identity.Principal, a *Access, args *db.Args, alias string) (*compiler, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	tz := st.DefaultTimeZone
	if p.Authenticated() && p.User.TimeZone != "" {
		tz = p.User.TimeZone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return &compiler{s: s, ctx: ctx, q: q, cfg: cfg, a: a, p: p, args: args, loc: loc, lang: st.ContentLanguage,
		now: now(), hpd: st.HoursPerDay, dpw: st.DaysPerWeek, alias: alias}, nil
}

// CompileQuery returns the SQL condition and order for a KQL query (for boards and other modules) with the
// visibility predicate included.
func (s *Service) CompileQuery(ctx context.Context, q db.Querier, p *identity.Principal, query, alias string, args *db.Args) (where, order string, err error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", "", err
	}
	parsed, err := kql.Parse(query)
	if err != nil {
		if pe, ok := err.(*kql.Error); ok {
			return "", "", queryError(pe.Pos, pe.Format, pe.Args...)
		}
		return "", "", err
	}
	c, err := s.newCompiler(ctx, q, p, a, args, alias)
	if err != nil {
		return "", "", err
	}
	where, order, err = c.compile(parsed)
	if err != nil {
		return "", "", err
	}
	return "(" + a.VisibleSQL(alias, args) + " AND " + where + ")", order, nil
}

// Search runs a KQL query and returns a page of issues.
func (s *Service) Search(ctx context.Context, p *identity.Principal, query string, limit, offset int) (*SearchResult, error) {
	defer metrics.Search("issues", time.Now())
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	parsed, err := kql.Parse(query)
	if err != nil {
		if pe, ok := err.(*kql.Error); ok {
			return nil, queryError(pe.Pos, pe.Format, pe.Args...)
		}
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	res := &SearchResult{Query: parsed.String(), Issues: []IssueRow{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 10*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, order, err := s.CompileQuery(ctx, tx, p, query, "i", &args)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issues i WHERE "+where, args...).Scan(&res.Total); err != nil {
			return err
		}
		// The keys of parents the reader may not see stay hidden.
		parentVisible := a.VisibleSQL("pi", &args)
		lim, off := args.Add(limit), args.Add(max(0, offset))
		rows, err := tx.Query(ctx, `SELECT i.id, i.key, i.project_id, p.key, p.name, i.summary, i.type_id, i.status_id,
			i.priority_id, i.resolution_id, i.assignee_id, au.username, au.display_name, i.reporter_id, ru.username,
			ru.display_name, (SELECT key FROM issues pi WHERE pi.id = i.parent_id AND `+parentVisible+`), i.labels,
			to_char(i.due_date, 'YYYY-MM-DD'),
			i.original_estimate, i.remaining_estimate, i.time_spent, i.votes, i.fields, i.rank, i.created_at, i.updated_at,
			i.resolved_at FROM issues i JOIN issue_projects p ON p.id = i.project_id
			LEFT JOIN users au ON au.id = i.assignee_id LEFT JOIN users ru ON ru.id = i.reporter_id
			WHERE `+where+" ORDER BY "+order+" LIMIT "+lim+" OFFSET "+off, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r IssueRow
			var typ, status int64
			var prio, res2, aid, rid *int64
			var au, an, ru, rn, parent *string
			var fields []byte
			if err := rows.Scan(&r.ID, &r.Key, &r.Project.ID, &r.Project.Key, &r.Project.Name, &r.Summary, &typ, &status, &prio,
				&res2, &aid, &au, &an, &rid, &ru, &rn, &parent, &r.Labels, &r.DueDate, &r.OriginalEstimate, &r.RemainingEstimate,
				&r.TimeSpent, &r.Votes, &fields, &r.Rank, &r.Created, &r.Updated, &r.Resolved); err != nil {
				return err
			}
			r.Type, r.Status = cfg.types[typ], cfg.statuses[status]
			if prio != nil {
				r.Priority = cfg.priorities[*prio]
			}
			if res2 != nil {
				r.Resolution = cfg.resolutions[*res2]
			}
			if aid != nil {
				r.Assignee = &identity.UserRef{ID: *aid, Username: *au, DisplayName: *an, Active: true}
			}
			if rid != nil {
				r.Reporter = &identity.UserRef{ID: *rid, Username: *ru, DisplayName: *rn, Active: true}
			}
			if parent != nil {
				r.ParentKey = *parent
			}
			if r.Labels == nil {
				r.Labels = []string{}
			}
			r.Fields = map[string]any{}
			_ = json.Unmarshal(fields, &r.Fields)
			res.Issues = append(res.Issues, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return withExtras(ctx, tx, cfg, res.Issues)
	})
	return res, err
}

// --- saved filters ---

// Share makes a filter visible to others.
type Share struct {
	Type      string `json:"type" enum:"user,group,project,logged-in,anyone"`
	ID        int64  `json:"id,omitempty"`
	ProjectID int64  `json:"projectId,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Filter is a saved query.
type Filter struct {
	ID          int64            `json:"id"`
	Owner       identity.UserRef `json:"owner"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Query       string           `json:"query"`
	Columns     []string         `json:"columns"`
	Shares      []Share          `json:"shares"`
	Favourite   bool             `json:"favourite"`
	CanEdit     bool             `json:"canEdit"`
	Updated     time.Time        `json:"updated"`
}

// sharedSQL returns an SQL condition that is true for the saved filters or dashboards (alias, with owner_id and
// shares) that the principal may see: their own, and those shared with them as a person, with one of their groups,
// with a project in which they see issues, with everyone signed in, or with anyone. The rule is part
// of the queries, so that lists count and page only what the principal sees.
func sharedSQL(alias string, p *identity.Principal, a *Access, args *db.Args) string {
	// Shares hold ids as JSON numbers; they are compared as text, which cannot fail on a malformed share.
	kinds := []string{"v->>'type' = 'anyone'"}
	own := ""
	if p.Authenticated() {
		uid := args.Add(p.UserID())
		own = alias + ".owner_id = " + uid + " OR "
		kinds = append(kinds, "v->>'type' = 'logged-in'", "(v->>'type' = 'user' AND v->>'id' = "+uid+"::text)")
		if len(p.Groups) > 0 {
			kinds = append(kinds, "(v->>'type' = 'group' AND v->>'id' = ANY("+args.Add(p.Groups)+"::bigint[]::text[]))")
		}
	}
	if a.system {
		// Kogge itself sees every project.
		kinds = append(kinds, "v->>'type' = 'project'")
	} else if projects := a.BrowsableProjects(); len(projects) > 0 {
		kinds = append(kinds, "(v->>'type' = 'project' AND v->>'projectId' = ANY("+args.Add(projects)+"::bigint[]::text[]))")
	}
	return "(" + own + "EXISTS (SELECT 1 FROM jsonb_array_elements(" + alias + ".shares) sh(v) WHERE " +
		strings.Join(kinds, " OR ") + "))"
}

// SharedSelection chooses a page of saved filters or of dashboards, the lists of things people share.
type SharedSelection struct {
	Query     string // part of the name or the description
	Owner     string // me: the principal's own; others: those shared with the principal; "": both
	Favourite bool   // only the principal's favourites
	Limit     int
	Offset    int
}

// where returns the condition of the selection on the saved filters or dashboards (alias), whose favourites are
// the rows of the table favourites with the column key.
func (sel SharedSelection) where(alias, favourites, key string, p *identity.Principal, a *Access, args *db.Args) string {
	where := []string{sharedSQL(alias, p, a, args)}
	if q := strings.TrimSpace(sel.Query); q != "" {
		like := args.Add("%" + db.EscapeLike(strings.ToLower(q)) + "%")
		where = append(where, "(lower("+alias+".name) LIKE "+like+" OR lower("+alias+".description) LIKE "+like+")")
	}
	switch sel.Owner {
	case "me":
		where = append(where, alias+".owner_id = "+args.Add(p.UserID()))
	case "others":
		where = append(where, alias+".owner_id <> "+args.Add(p.UserID()))
	}
	if sel.Favourite {
		where = append(where, "EXISTS (SELECT 1 FROM "+favourites+" fav WHERE fav."+key+" = "+alias+".id AND fav.user_id = "+
			args.Add(p.UserID())+")")
	}
	return strings.Join(where, " AND ")
}

// pageSQL returns the LIMIT and OFFSET of a page (at most 500 items).
func pageSQL(limit, offset int, args *db.Args) string {
	return " LIMIT " + args.Add(max(1, min(limit, 500))) + " OFFSET " + args.Add(max(0, offset))
}

// FilterList is a page of saved filters.
type FilterList struct {
	Total int      `json:"total" doc:"Filters that match, on all pages"`
	Items []Filter `json:"items"`
}

// visibleFilters returns the saved filters that match cond (on f, with the values args) and that the principal may
// see, by name.
func (s *Service) visibleFilters(ctx context.Context, p *identity.Principal, cond string, args ...any) ([]Filter, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	all := db.Args(args)
	return s.queryFilters(ctx, p, "("+cond+") AND "+sharedSQL("f", p, a, &all)+" ORDER BY lower(f.name), f.id", &all)
}

// queryFilters reads the saved filters (f) of a condition that includes who may see them, with its order and page.
func (s *Service) queryFilters(ctx context.Context, p *identity.Principal, cond string, args *db.Args) ([]Filter, error) {
	uid := args.Add(p.UserID())
	rows, err := s.pool.Query(ctx, `SELECT f.id, f.owner_id, u.username, u.display_name, f.name, f.description, f.query,
		f.columns, f.shares, f.updated_at, EXISTS (SELECT 1 FROM saved_filter_favourites fav WHERE fav.filter_id = f.id
		AND fav.user_id = `+uid+`) FROM saved_filters f JOIN users u ON u.id = f.owner_id WHERE `+cond, *args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Filter, error) {
		var f Filter
		var shares []byte
		err := row.Scan(&f.ID, &f.Owner.ID, &f.Owner.Username, &f.Owner.DisplayName, &f.Name, &f.Description, &f.Query,
			&f.Columns, &shares, &f.Updated, &f.Favourite)
		_ = json.Unmarshal(shares, &f.Shares)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	admin, _ := s.authz.IsAdmin(ctx, p)
	for i := range list {
		f := &list[i]
		f.CanEdit = f.Owner.ID == p.UserID() || admin
		// Who else sees a filter is for those who may change that to know.
		if f.Shares == nil || !f.CanEdit {
			f.Shares = []Share{}
		}
		if f.Columns == nil {
			f.Columns = []string{}
		}
	}
	if list == nil {
		list = []Filter{}
	}
	return list, s.nameShares(ctx, list)
}

// nameShares names the people and groups of the filters' shares (by their current names).
func (s *Service) nameShares(ctx context.Context, filters []Filter) error {
	var users, groups []int64
	for _, f := range filters {
		for _, sh := range f.Shares {
			switch sh.Type {
			case "user":
				users = append(users, sh.ID)
			case "group":
				groups = append(groups, sh.ID)
			}
		}
	}
	if len(users) == 0 && len(groups) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT 'user', id, display_name FROM users WHERE id = ANY($1)
		UNION ALL SELECT 'group', id, name FROM groups WHERE id = ANY($2)`, nonNil(users), nonNil(groups))
	if err != nil {
		return err
	}
	names := map[Share]string{}
	for rows.Next() {
		var sh Share
		var name string
		if err := rows.Scan(&sh.Type, &sh.ID, &name); err != nil {
			rows.Close()
			return err
		}
		names[sh] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range filters {
		for i, sh := range f.Shares {
			if sh.Type == "user" || sh.Type == "group" {
				f.Shares[i].Name = names[Share{Type: sh.Type, ID: sh.ID}]
			}
		}
	}
	return nil
}

// Filters lists a page of the saved filters the principal may see: their own and those shared with them, by name.
func (s *Service) Filters(ctx context.Context, p *identity.Principal, sel SharedSelection) (*FilterList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	cond := sel.where("f", "saved_filter_favourites", "filter_id", p, a, &args)
	out := &FilterList{}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM saved_filters f WHERE "+cond, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	out.Items, err = s.queryFilters(ctx, p, cond+" ORDER BY lower(f.name), f.id"+pageSQL(sel.Limit, sel.Offset, &args), &args)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetFilter returns a visible filter.
func (s *Service) GetFilter(ctx context.Context, p *identity.Principal, id int64) (*Filter, error) {
	list, err := s.visibleFilters(ctx, p, "f.id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("filter.not-found", "The filter does not exist or is not shared with you.")
	}
	return &list[0], nil
}

func (s *Service) filterByName(ctx context.Context, p *identity.Principal, name string) (*Filter, error) {
	if id, err := strconv.ParseInt(name, 10, 64); err == nil {
		return s.GetFilter(ctx, p, id)
	}
	list, err := s.visibleFilters(ctx, p, "lower(f.name) = lower($1)", name)
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		if f.Owner.ID == p.UserID() {
			return &f, nil
		}
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("filter.not-found", "The filter does not exist.")
	}
	return &list[0], nil
}

// columnKey is the form of the keys of columns: system fields and custom fields (cf_<id>).
var columnKey = regexp.MustCompile(`^(cf_[0-9]{1,18}|[A-Za-z]{1,40})$`)

// FilterInput creates or changes a filter.
type FilterInput struct {
	Name        string   `json:"name" minLength:"1" maxLength:"200"`
	Description string   `json:"description,omitempty" maxLength:"2000"`
	Query       string   `json:"query" maxLength:"10000"`
	Columns     []string `json:"columns,omitempty" maxItems:"40" doc:"The columns of the issue navigator: field keys such as summary or cf_12"`
	Shares      []Share  `json:"shares,omitempty"`
}

// SaveFilter creates (id 0) or updates a filter after validating its query.
func (s *Service) SaveFilter(ctx context.Context, p *identity.Principal, id int64, in FilterInput) (*Filter, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	// Only the owner learns anything about a filter, even whether the new query is valid.
	if id != 0 {
		cur, err := s.GetFilter(ctx, p, id)
		if err != nil {
			return nil, err
		}
		if !cur.CanEdit {
			return nil, apperr.Forbidden("filter.edit-denied", "Only the owner can change this filter.")
		}
	}
	if _, err := s.Search(ctx, p, in.Query, 1, 0); err != nil {
		return nil, err
	}
	if in.Shares == nil {
		in.Shares = []Share{}
	}
	// Names are looked up when filters are read; stored ones would go stale.
	for i := range in.Shares {
		in.Shares[i].Name = ""
	}
	if in.Columns == nil {
		in.Columns = []string{}
	}
	for _, c := range in.Columns {
		if !columnKey.MatchString(c) {
			return nil, apperr.Invalidf("filter.column", "%q is not a column.", c)
		}
	}
	shares, _ := json.Marshal(in.Shares)
	if id == 0 {
		err := s.pool.QueryRow(ctx, `INSERT INTO saved_filters (owner_id, name, description, query, columns, shares)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, p.UserID(), strings.TrimSpace(in.Name), in.Description, in.Query,
			in.Columns, shares).Scan(&id)
		if db.IsUniqueViolation(err, "saved_filters_owner_name_key") {
			return nil, apperr.Conflict("filter.name-taken", "You already have a filter named %s.", in.Name)
		}
		if err != nil {
			return nil, err
		}
	} else {
		_, err := s.pool.Exec(ctx, `UPDATE saved_filters SET name = $2, description = $3, query = $4, columns = $5, shares = $6,
			updated_at = now() WHERE id = $1`, id, strings.TrimSpace(in.Name), in.Description, in.Query, in.Columns, shares)
		if db.IsUniqueViolation(err, "saved_filters_owner_name_key") {
			return nil, apperr.Conflict("filter.name-taken", "You already have a filter named %s.", in.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	return s.GetFilter(ctx, p, id)
}

// DeleteFilter removes a filter (owner or administrator).
func (s *Service) DeleteFilter(ctx context.Context, p *identity.Principal, id int64) error {
	cur, err := s.GetFilter(ctx, p, id)
	if err != nil {
		return err
	}
	if !cur.CanEdit {
		return apperr.Forbidden("filter.edit-denied", "Only the owner can delete this filter.")
	}
	_, err = s.pool.Exec(ctx, "DELETE FROM saved_filters WHERE id = $1", id)
	return err
}

// SetFavourite marks or unmarks a filter as favourite.
func (s *Service) SetFavourite(ctx context.Context, p *identity.Principal, id int64, fav bool) error {
	if _, err := s.GetFilter(ctx, p, id); err != nil {
		return err
	}
	var err error
	if fav {
		_, err = s.pool.Exec(ctx, "INSERT INTO saved_filter_favourites (filter_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, p.UserID())
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM saved_filter_favourites WHERE filter_id = $1 AND user_id = $2", id, p.UserID())
	}
	return err
}
