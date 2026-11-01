// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// QueryError is a KQL error with its position, returned as a 400 problem.
func queryError(pos kql.Pos, format string, args ...any) error {
	e := apperr.BadRequest("search.query", format, args...)
	e.Errors = []apperr.FieldError{{Location: "q:" + strconv.Itoa(int(pos)), Message: e.Detail}}
	return e
}

type compiler struct {
	s     *Service
	ctx   context.Context
	q     db.Querier
	cfg   *configSnapshot
	a     *Access
	p     *identity.Principal
	args  *db.Args
	loc   *time.Location
	lang  string
	now   time.Time
	hpd   int
	dpw   int
	depth int
	alias string
}

var fieldAliases = map[string]string{
	"issuekey": "key", "id": "key", "issue": "key", "type": "issuetype", "category": "statuscategory",
	"label": "labels", "components": "component", "fixversions": "fixversion", "affectsversion": "affectedversion",
	"version": "affectedversion", "epic link": "parent", "epic": "parent", "createddate": "created",
	"updateddate": "updated", "resolutiondate": "resolved", "duedate": "due", "watchers": "watcher",
	"savedfilter": "filter", "request": "filter", "originalestimate": "originalestimate",
}

func canonicalField(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if a, ok := fieldAliases[n]; ok {
		return a
	}
	return n
}

// compile translates a parsed query into an SQL condition and ORDER BY clause for the alias i.
func (c *compiler) compile(q *kql.Query) (where, order string, err error) {
	where = "true"
	if q.Where != nil {
		if where, err = c.expr(q.Where); err != nil {
			return "", "", err
		}
	}
	order, err = c.orderBy(q.OrderBy)
	return where, order, err
}

func (c *compiler) expr(e kql.Expr) (string, error) {
	switch x := e.(type) {
	case *kql.And:
		l, err := c.expr(x.Left)
		if err != nil {
			return "", err
		}
		r, err := c.expr(x.Right)
		if err != nil {
			return "", err
		}
		return "(" + l + " AND " + r + ")", nil
	case *kql.Or:
		l, err := c.expr(x.Left)
		if err != nil {
			return "", err
		}
		r, err := c.expr(x.Right)
		if err != nil {
			return "", err
		}
		return "(" + l + " OR " + r + ")", nil
	case *kql.Not:
		inner, err := c.expr(x.X)
		if err != nil {
			return "", err
		}
		return "NOT coalesce(" + inner + ", false)", nil
	case *kql.Clause:
		return c.clause(x)
	case *kql.History:
		return c.history(x)
	}
	return "", fmt.Errorf("unknown expression")
}

func (c *compiler) col(name string) string { return c.alias + "." + name }

// values flattens an operand into values and functions.
func values(o kql.Operand) ([]kql.Value, []kql.Func, bool) {
	switch x := o.(type) {
	case kql.Value:
		return []kql.Value{x}, nil, false
	case kql.Func:
		return nil, []kql.Func{x}, false
	case kql.List:
		var vs []kql.Value
		var fs []kql.Func
		empty := false
		for _, it := range x.Items {
			v, f, e := values(it)
			vs, fs, empty = append(vs, v...), append(fs, f...), empty || e
		}
		return vs, fs, empty
	case kql.Empty:
		return nil, nil, true
	}
	return nil, nil, false
}

func negated(op kql.Op) bool { return op == kql.OpNe || op == kql.OpNotIn || op == kql.OpIsNot }

// idClause builds the common shape for fields stored as a nullable id column: = != in, not in, is (not) empty.
func (c *compiler) idClause(cl *kql.Clause, column string, ids []int64, includeEmpty bool) string {
	match := column + " = ANY(" + c.args.Add(nonNil(ids)) + ")"
	if includeEmpty {
		match = "(" + match + " OR " + column + " IS NULL)"
	}
	if negated(cl.Op) {
		// != and NOT IN never match empty values unless EMPTY is in the list.
		if includeEmpty {
			return "NOT " + match
		}
		return "(" + column + " IS NOT NULL AND NOT " + match + ")"
	}
	return match
}

func (c *compiler) checkOps(cl *kql.Clause, allowed ...kql.Op) error {
	for _, op := range allowed {
		if cl.Op == op {
			return nil
		}
	}
	return queryError(cl.Pos, "The operator %s cannot be used with the field %s.", strings.ToUpper(string(cl.Op)), cl.Field.String())
}

var listOps = []kql.Op{kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn, kql.OpIs, kql.OpIsNot}

func (c *compiler) clause(cl *kql.Clause) (string, error) {
	if cl.Field.CustomID != 0 {
		f := c.cfg.fields[cl.Field.CustomID]
		if f == nil {
			return "", queryError(cl.Pos, "The custom field cf[%d] does not exist.", cl.Field.CustomID)
		}
		return c.customClause(cl, f)
	}
	name := canonicalField(cl.Field.Name)
	vals, funcs, empty := values(cl.Value)
	switch name {
	case "project":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		var ids []int64
		for _, v := range vals {
			var id int64
			err := c.q.QueryRow(c.ctx, `SELECT project_id FROM issue_project_keys WHERE key = upper($1)
				UNION ALL SELECT id FROM issue_projects WHERE lower(name) = lower($1) OR id::text = $1 LIMIT 1`, v.Text).Scan(&id)
			if err != nil || !c.a.CanAny(id, PermBrowse) {
				return "", queryError(v.Pos, "The project %q does not exist or you may not see it.", v.Text)
			}
			ids = append(ids, id)
		}
		if len(funcs) > 0 {
			return "", queryError(funcs[0].Pos, "Functions cannot be used with project.")
		}
		return c.idClause(cl, c.col("project_id"), ids, false), nil
	case "key":
		return c.keyClause(cl, vals, funcs, empty)
	case "issuetype":
		return c.namedClause(cl, vals, funcs, empty, c.col("type_id"), "issue type", func(v string) []int64 {
			var out []int64
			for _, t := range c.cfg.typeList {
				if strings.EqualFold(t.Name, v) || strconv.FormatInt(t.ID, 10) == v {
					out = append(out, t.ID)
				}
			}
			return out
		})
	case "status":
		return c.namedClause(cl, vals, funcs, empty, c.col("status_id"), "status", func(v string) []int64 {
			var out []int64
			for _, t := range c.cfg.statusList {
				if strings.EqualFold(t.Name, v) || strconv.FormatInt(t.ID, 10) == v {
					out = append(out, t.ID)
				}
			}
			return out
		})
	case "statuscategory":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		var ids []int64
		for _, v := range vals {
			cat := map[string]string{"to do": CategoryTodo, "todo": CategoryTodo, "new": CategoryTodo, "offen": CategoryTodo,
				"in progress": CategoryInProgress, "inprogress": CategoryInProgress, "indeterminate": CategoryInProgress,
				"in arbeit": CategoryInProgress, "done": CategoryDone, "complete": CategoryDone, "erledigt": CategoryDone}[strings.ToLower(v.Text)]
			if cat == "" {
				return "", queryError(v.Pos, "Unknown status category %q (To Do, In Progress, Done).", v.Text)
			}
			for _, st := range c.cfg.statusList {
				if st.Category == cat {
					ids = append(ids, st.ID)
				}
			}
		}
		return c.idClause(cl, c.col("status_id"), ids, false), nil
	case "priority":
		return c.orderedClause(cl, vals, empty, c.col("priority_id"), "priorities", "priority", func(v string) (int64, int, bool) {
			for _, p := range c.cfg.prioList {
				if strings.EqualFold(p.Name, v) || strconv.FormatInt(p.ID, 10) == v {
					return p.ID, p.Position, true
				}
			}
			return 0, 0, false
		})
	case "resolution":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		var ids []int64
		for _, v := range vals {
			if strings.EqualFold(v.Text, "unresolved") {
				empty = true
				continue
			}
			found := false
			for _, r := range c.cfg.resList {
				if strings.EqualFold(r.Name, v.Text) || strconv.FormatInt(r.ID, 10) == v.Text {
					ids, found = append(ids, r.ID), true
				}
			}
			if !found {
				return "", queryError(v.Pos, "The resolution %q does not exist.", v.Text)
			}
		}
		return c.idClause(cl, c.col("resolution_id"), ids, empty), nil
	case "assignee", "reporter", "creator":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		ids, err := c.users(vals, funcs)
		if err != nil {
			return "", err
		}
		return c.idClause(cl, c.col(name+"_id"), ids, empty), nil
	case "summary", "description", "environment", "comment", "text":
		return c.textClause(cl, name)
	case "labels":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		var ls []string
		for _, v := range vals {
			ls = append(ls, v.Text)
		}
		col := c.col("labels")
		match := col + " && " + c.args.Add(nonNilStrings(ls)) + "::text[]"
		if empty {
			match = "(" + match + " OR cardinality(" + col + ") = 0)"
		}
		if negated(cl.Op) {
			if empty {
				return "NOT " + match, nil
			}
			return "(cardinality(" + col + ") > 0 AND NOT " + match + ")", nil
		}
		return match, nil
	case "component":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		var ids []int64
		visible := nonNil(c.a.BrowsableProjects())
		for _, v := range vals {
			rows, err := c.q.Query(c.ctx, `SELECT id FROM components WHERE (lower(name) = lower($1) OR id::text = $1)
				AND project_id = ANY($2)`, v.Text, visible)
			if err != nil {
				return "", err
			}
			found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return "", err
			}
			if len(found) == 0 {
				return "", queryError(v.Pos, "The component %q does not exist or you may not see it.", v.Text)
			}
			ids = append(ids, found...)
		}
		return c.existsClause(cl, "issue_components x", "x.issue_id = "+c.col("id"), "x.component_id", ids, empty), nil
	case "fixversion", "affectedversion":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		kind := "fix"
		if name == "affectedversion" {
			kind = "affects"
		}
		ids, err := c.versions(vals, funcs)
		if err != nil {
			return "", err
		}
		return c.existsClause(cl, "issue_versions x", "x.issue_id = "+c.col("id")+" AND x.kind = '"+kind+"'", "x.version_id", ids, empty), nil
	case "parent":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		var ids []int64
		for _, v := range vals {
			id, err := c.visibleIssue(v.Pos, v.Text)
			if err != nil {
				return "", err
			}
			ids = append(ids, id)
		}
		return c.idClause(cl, c.col("parent_id"), ids, empty), nil
	case "created", "updated", "resolved":
		col := map[string]string{"created": "created_at", "updated": "updated_at", "resolved": "resolved_at"}[name]
		return c.timeClause(cl, c.col(col), false)
	case "due":
		return c.timeClause(cl, c.col("due_date"), true)
	case "originalestimate", "remainingestimate", "timespent":
		col := map[string]string{"originalestimate": "original_estimate", "remainingestimate": "remaining_estimate", "timespent": "time_spent"}[name]
		return c.durationClause(cl, c.col(col))
	case "votes":
		return c.numberClause(cl, c.col("votes"))
	case "watcher", "voter":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		ids, err := c.users(vals, funcs)
		if err != nil {
			return "", err
		}
		table, cond := "watchers x", "x.entity_type = 'issue' AND x.entity_id = "+c.col("id")
		if name == "voter" {
			table, cond = "issue_votes x", "x.issue_id = "+c.col("id")
		}
		match := c.existsClause(cl, table, cond, "x.user_id", ids, false)
		// Who watches and votes is for those who may see watchers (watchers.view); one's own watching and voting
		// is always one's own business.
		if slices.ContainsFunc(ids, func(id int64) bool { return id != c.p.UserID() || id == 0 }) {
			match = "(" + match + " AND " + c.col("project_id") + " = ANY(" + c.args.Add(nonNil(c.a.ProjectsWith(PermViewWatchers))) + "))"
		}
		return match, nil
	case "level":
		if err := c.checkOps(cl, listOps...); err != nil {
			return "", err
		}
		var ids []int64
		for _, v := range vals {
			rows, err := c.q.Query(c.ctx, "SELECT id FROM security_levels WHERE lower(name) = lower($1) OR id::text = $1", v.Text)
			if err != nil {
				return "", err
			}
			found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return "", err
			}
			if len(found) == 0 {
				return "", queryError(v.Pos, "The security level %q does not exist.", v.Text)
			}
			ids = append(ids, found...)
		}
		return c.idClause(cl, c.col("security_level_id"), ids, empty), nil
	case "attachments":
		if err := c.checkOps(cl, kql.OpIs, kql.OpIsNot); err != nil {
			return "", err
		}
		exists := "EXISTS (SELECT 1 FROM issue_attachments x WHERE x.issue_id = " + c.col("id") + ")"
		if cl.Op == kql.OpIs {
			return "NOT " + exists, nil
		}
		return exists, nil
	case "filter":
		return c.filterClause(cl, vals)
	case "sprint":
		return c.sprintClause(cl, vals, funcs, empty)
	case "rank":
		return "", queryError(cl.Pos, "rank can only be used in ORDER BY.")
	}
	if f := c.cfg.fieldByName(cl.Field.Name); f != nil {
		return c.customClause(cl, f)
	}
	return "", queryError(cl.Field.Pos, "The field %q does not exist.", cl.Field.Name)
}

// namedClause resolves names to ids for configuration fields.
func (c *compiler) namedClause(cl *kql.Clause, vals []kql.Value, funcs []kql.Func, empty bool, column, what string,
	resolve func(string) []int64) (string, error) {
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
		return "", err
	}
	if len(funcs) > 0 || empty {
		return "", queryError(cl.Pos, "The %s cannot be empty and takes no functions.", what)
	}
	var ids []int64
	for _, v := range vals {
		found := resolve(v.Text)
		if len(found) == 0 {
			return "", queryError(v.Pos, "The %s %q does not exist.", what, v.Text)
		}
		ids = append(ids, found...)
	}
	return c.idClause(cl, column, ids, false), nil
}

// orderedClause supports comparisons by position (priorities: position 1 is the highest).
func (c *compiler) orderedClause(cl *kql.Clause, vals []kql.Value, empty bool, column, table, what string,
	resolve func(string) (int64, int, bool)) (string, error) {
	switch cl.Op {
	case kql.OpGt, kql.OpGe, kql.OpLt, kql.OpLe:
		if len(vals) != 1 {
			return "", queryError(cl.Pos, "Compare the %s with one value.", what)
		}
		_, pos, ok := resolve(vals[0].Text)
		if !ok {
			return "", queryError(vals[0].Pos, "The %s %q does not exist.", what, vals[0].Text)
		}
		cmp := map[kql.Op]string{kql.OpGt: "<", kql.OpGe: "<=", kql.OpLt: ">", kql.OpLe: ">="}[cl.Op]
		return column + " IN (SELECT id FROM " + table + " WHERE position " + cmp + " " + c.args.Add(pos) + ")", nil
	}
	if err := c.checkOps(cl, listOps...); err != nil {
		return "", err
	}
	var ids []int64
	for _, v := range vals {
		id, _, ok := resolve(v.Text)
		if !ok {
			return "", queryError(v.Pos, "The %s %q does not exist.", what, v.Text)
		}
		ids = append(ids, id)
	}
	return c.idClause(cl, column, ids, empty), nil
}

func (c *compiler) existsClause(cl *kql.Clause, table, join, column string, ids []int64, empty bool) string {
	any := "EXISTS (SELECT 1 FROM " + table + " WHERE " + join + ")"
	if cl.Op == kql.OpIs {
		return "NOT " + any
	}
	if cl.Op == kql.OpIsNot {
		return any
	}
	// Bound only when used: an unused parameter has no type PostgreSQL could infer.
	match := "EXISTS (SELECT 1 FROM " + table + " WHERE " + join + " AND " + column + " = ANY(" + c.args.Add(nonNil(ids)) + "))"
	if empty {
		match = "(" + match + " OR NOT " + any + ")"
	}
	if negated(cl.Op) {
		if empty {
			return "NOT " + match
		}
		return "(" + any + " AND NOT " + match + ")"
	}
	return match
}

// users resolves user names, e-mail addresses, display names, currentUser() and membersOf(). Naming other users
// needs user.browse, like the user directory.
func (c *compiler) users(vals []kql.Value, funcs []kql.Func) ([]int64, error) {
	var ids []int64
	if len(vals) > 0 || slices.ContainsFunc(funcs, func(f kql.Func) bool { return !strings.EqualFold(f.Name, "currentuser") }) {
		if ok, err := c.s.authz.HasGlobal(c.ctx, c.p, authz.UserBrowse); err != nil {
			return nil, err
		} else if !ok {
			pos := cl0(vals, funcs)
			return nil, queryError(pos, "Searching for other users needs the permission to browse users.")
		}
	}
	for _, v := range vals {
		var id int64
		err := c.q.QueryRow(c.ctx, `SELECT id FROM users WHERE status <> 'anonymised' AND (lower(username) = lower($1)
			OR lower(email) = lower($1) OR id::text = $1) ORDER BY id LIMIT 1`, v.Text).Scan(&id)
		if err != nil {
			if !db.IsNoRows(err) {
				return nil, err
			}
			err = c.q.QueryRow(c.ctx, "SELECT id FROM users WHERE lower(display_name) = lower($1) ORDER BY id LIMIT 1", v.Text).Scan(&id)
			if err != nil {
				return nil, queryError(v.Pos, "The user %q does not exist.", v.Text)
			}
		}
		ids = append(ids, id)
	}
	for _, f := range funcs {
		switch strings.ToLower(f.Name) {
		case "currentuser":
			if !c.p.Authenticated() {
				return nil, queryError(f.Pos, "currentUser() needs a signed-in user.")
			}
			ids = append(ids, c.p.UserID())
		case "membersof":
			if len(f.Args) != 1 {
				return nil, queryError(f.Pos, "membersOf() needs a group name.")
			}
			rows, err := c.q.Query(c.ctx, `SELECT m.user_id FROM group_members m JOIN groups g ON g.id = m.group_id
				WHERE lower(g.name) = lower($1)`, f.Args[0].Text)
			if err != nil {
				return nil, err
			}
			members, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return nil, err
			}
			ids = append(ids, members...)
		default:
			return nil, queryError(f.Pos, "The function %s() cannot be used with user fields.", f.Name)
		}
	}
	return ids, nil
}

// cl0 is the position of the first value or function of a clause.
func cl0(vals []kql.Value, funcs []kql.Func) kql.Pos {
	if len(vals) > 0 {
		return vals[0].Pos
	}
	return funcs[0].Pos
}

// versions resolves version names (in any visible project) and version functions.
func (c *compiler) versions(vals []kql.Value, funcs []kql.Func) ([]int64, error) {
	var ids []int64
	visible := nonNil(c.a.BrowsableProjects())
	for _, v := range vals {
		rows, err := c.q.Query(c.ctx, `SELECT id FROM versions WHERE (lower(name) = lower($1) OR id::text = $1)
			AND project_id = ANY($2)`, v.Text, visible)
		if err != nil {
			return nil, err
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			return nil, queryError(v.Pos, "The version %q does not exist.", v.Text)
		}
		ids = append(ids, found...)
	}
	for _, f := range funcs {
		projects := visible
		if len(f.Args) > 0 {
			var pid int64
			if err := c.q.QueryRow(c.ctx, "SELECT project_id FROM issue_project_keys WHERE key = upper($1)", f.Args[0].Text).Scan(&pid); err != nil ||
				!c.a.CanAny(pid, PermBrowse) {
				return nil, queryError(f.Args[0].Pos, "The project %q does not exist or you may not see it.", f.Args[0].Text)
			}
			projects = []int64{pid}
		}
		var sql string
		switch strings.ToLower(f.Name) {
		case "releasedversions":
			sql = "SELECT id FROM versions WHERE released AND project_id = ANY($1)"
		case "unreleasedversions":
			sql = "SELECT id FROM versions WHERE NOT released AND NOT archived AND project_id = ANY($1)"
		case "latestreleasedversion":
			sql = `SELECT DISTINCT ON (project_id) id FROM versions WHERE released AND project_id = ANY($1)
				ORDER BY project_id, release_date DESC NULLS LAST, position DESC`
		case "earliestunreleasedversion":
			sql = `SELECT DISTINCT ON (project_id) id FROM versions WHERE NOT released AND NOT archived AND project_id = ANY($1)
				ORDER BY project_id, release_date NULLS LAST, position`
		default:
			return nil, queryError(f.Pos, "The function %s() cannot be used with versions.", f.Name)
		}
		rows, err := c.q.Query(c.ctx, sql, projects)
		if err != nil {
			return nil, err
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		ids = append(ids, found...)
	}
	return ids, nil
}

var issueKeyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]{1,9})-([0-9]+)$`)

func (c *compiler) keyClause(cl *kql.Clause, vals []kql.Value, funcs []kql.Func, empty bool) (string, error) {
	if empty {
		return "", queryError(cl.Pos, "Issue keys are never empty.")
	}
	switch cl.Op {
	case kql.OpGt, kql.OpGe, kql.OpLt, kql.OpLe:
		if len(vals) != 1 {
			return "", queryError(cl.Pos, "Compare the key with one issue key.")
		}
		m := issueKeyRe.FindStringSubmatch(vals[0].Text)
		if m == nil {
			return "", queryError(vals[0].Pos, "%q is not an issue key.", vals[0].Text)
		}
		var pid int64
		if err := c.q.QueryRow(c.ctx, "SELECT project_id FROM issue_project_keys WHERE key = upper($1)", m[1]).Scan(&pid); err != nil ||
			!c.a.CanAny(pid, PermBrowse) {
			return "", queryError(vals[0].Pos, "The project %q does not exist or you may not see it.", m[1])
		}
		n, _ := strconv.ParseInt(m[2], 10, 64)
		return "(" + c.col("project_id") + " = " + c.args.Add(pid) + " AND " + c.col("number") + " " + string(cl.Op) + " " + c.args.Add(n) + ")", nil
	}
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
		return "", err
	}
	var parts []string
	var ids []int64
	for _, v := range vals {
		id, err := lookupIssueID(c.ctx, c.q, v.Text)
		if err != nil {
			id = -1 // unknown keys simply match nothing
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 || len(funcs) == 0 {
		parts = append(parts, c.col("id")+" = ANY("+c.args.Add(nonNil(ids))+")")
	}
	for _, f := range funcs {
		sub, err := c.issueFunction(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, c.col("id")+" IN ("+sub+")")
	}
	match := "(" + strings.Join(parts, " OR ") + ")"
	if negated(cl.Op) {
		return "NOT " + match, nil
	}
	return match, nil
}

// issueFunction returns a subquery of issue ids for the issue functions.
// visibleIssue resolves the key of an issue the reader may see; hidden issues are answered like missing ones, so that
// queries tell nothing about them or their relations.
func (c *compiler) visibleIssue(pos kql.Pos, key string) (int64, error) {
	id, err := lookupIssueID(c.ctx, c.q, key)
	if err == nil {
		var refs []IssueRef
		if refs, err = c.s.issueRefs(c.ctx, c.q, c.cfg, c.a, "i.id = $1", id); err == nil && len(refs) == 0 {
			err = apperr.NotFound("issue.not-found", "")
		}
	}
	if apperr.IsNotFound(err) {
		return 0, queryError(pos, "The issue %s does not exist or you may not see it.", key)
	}
	return id, err
}

func (c *compiler) issueFunction(f kql.Func) (string, error) {
	switch strings.ToLower(f.Name) {
	case "watchedissues":
		return "SELECT entity_id FROM watchers WHERE entity_type = 'issue' AND user_id = " + c.args.Add(c.p.UserID()), nil
	case "votedissues":
		return "SELECT issue_id FROM issue_votes WHERE user_id = " + c.args.Add(c.p.UserID()), nil
	case "linkedissues":
		if len(f.Args) < 1 {
			return "", queryError(f.Pos, "linkedIssues() needs an issue key.")
		}
		id, err := c.visibleIssue(f.Args[0].Pos, f.Args[0].Text)
		if err != nil {
			return "", err
		}
		p := c.args.Add(id)
		if len(f.Args) > 1 {
			desc := c.args.Add(strings.ToLower(f.Args[1].Text))
			return "SELECT l.target_id FROM issue_links l JOIN issue_link_types t ON t.id = l.link_type_id WHERE l.source_id = " + p +
				" AND (lower(t.outward) = " + desc + " OR lower(t.name) = " + desc + ") UNION SELECT l.source_id FROM issue_links l " +
				"JOIN issue_link_types t ON t.id = l.link_type_id WHERE l.target_id = " + p + " AND (lower(t.inward) = " + desc +
				" OR lower(t.name) = " + desc + ")", nil
		}
		return "SELECT target_id FROM issue_links WHERE source_id = " + p + " UNION SELECT source_id FROM issue_links WHERE target_id = " + p, nil
	case "childissuesof":
		if len(f.Args) != 1 {
			return "", queryError(f.Pos, "childIssuesOf() needs an issue key.")
		}
		id, err := c.visibleIssue(f.Args[0].Pos, f.Args[0].Text)
		if err != nil {
			return "", err
		}
		return "WITH RECURSIVE tree AS (SELECT id FROM issues WHERE parent_id = " + c.args.Add(id) +
			" UNION SELECT i2.id FROM issues i2 JOIN tree ON i2.parent_id = tree.id) SELECT id FROM tree", nil
	}
	return "", queryError(f.Pos, "The function %s() cannot be used with issue keys.", f.Name)
}

// tsquery builds a text query: words must all occur (stemmed), "phrases" match in order, word* matches prefixes.
func (c *compiler) tsquery(pos kql.Pos, text string) (string, error) {
	cfgParam := ""
	cfg := func() string {
		if cfgParam == "" {
			cfgParam = c.args.Add(c.lang) + "::regconfig"
		}
		return cfgParam
	}
	var parts []string
	rest := strings.TrimSpace(text)
	for rest != "" {
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				end = len(rest) - 1
			}
			phrase := rest[1 : end+1]
			rest = strings.TrimSpace(rest[min(end+2, len(rest)):])
			if strings.TrimSpace(phrase) != "" {
				parts = append(parts, "phraseto_tsquery("+cfg()+", "+c.args.Add(phrase)+")")
			}
			continue
		}
		word := rest
		if i := strings.IndexAny(rest, " \t\""); i >= 0 {
			word, rest = rest[:i], strings.TrimSpace(rest[i:])
		} else {
			rest = ""
		}
		if strings.HasSuffix(word, "*") {
			stem := strings.Map(func(r rune) rune {
				if r == '\'' || r == '\\' || r == ':' || r == '&' || r == '|' || r == '!' || r == '(' || r == ')' || r == '*' || r == '<' || r == '>' {
					return -1
				}
				return r
			}, word)
			if stem != "" {
				parts = append(parts, "to_tsquery('simple', "+c.args.Add(strings.ToLower(stem)+":*")+")")
			}
			continue
		}
		parts = append(parts, "plainto_tsquery("+cfg()+", "+c.args.Add(word)+")")
	}
	if len(parts) == 0 {
		return "", queryError(pos, "Enter at least one word to search for.")
	}
	return "(" + strings.Join(parts, " && ") + ")", nil
}

func (c *compiler) textClause(cl *kql.Clause, name string) (string, error) {
	if cl.Op == kql.OpIs || cl.Op == kql.OpIsNot {
		col := map[string]string{"description": "description_text", "environment": "environment"}[name]
		if col == "" {
			return "", queryError(cl.Pos, "%s cannot be compared with EMPTY.", name)
		}
		cond := c.col(col) + " = ''"
		if cl.Op == kql.OpIsNot {
			cond = c.col(col) + " <> ''"
		}
		return cond, nil
	}
	if err := c.checkOps(cl, kql.OpLike, kql.OpNotLike); err != nil {
		return "", err
	}
	v, ok := cl.Value.(kql.Value)
	if !ok {
		return "", queryError(cl.Pos, "Text fields are searched with a text value.")
	}
	q, err := c.tsquery(v.Pos, v.Text)
	if err != nil {
		return "", err
	}
	// The simple configuration covers prefix queries; the stemmed vector covers words. kogge_simple_words is
	// to_tsvector('simple', …) with its true cost (migration 00053), so that its indexes are used at every size.
	vec := c.col("search_tsv")
	weight := map[string]string{"summary": "{a}", "description": "{b}", "comment": "{c}", "environment": "{d}"}[name]
	match := "(" + vec + " @@ " + q + " OR kogge_simple_words(" + c.col("key") + " || ' ' || " + c.col("summary") + " || ' ' || " +
		c.col("description_text") + ") @@ " + q + ")"
	if weight != "" {
		match = "(" + vec + " @@ " + q + " AND ts_filter(" + vec + ", '" + weight + "') @@ " + q + ")"
		if name == "summary" {
			match = "(" + match + " OR kogge_simple_words(" + c.col("summary") + ") @@ " + q + ")"
		}
	}
	if cl.Op == kql.OpNotLike {
		return "NOT " + match, nil
	}
	return match, nil
}

// instant resolves a date value or date function; dateOnly tells whether the value denotes a whole day.
func (c *compiler) instant(o kql.Operand) (t time.Time, dateOnly bool, err error) {
	switch x := o.(type) {
	case kql.Value:
		s := strings.TrimSpace(x.Text)
		if d, ok := parseRelative(s, c.hpd, c.dpw); ok {
			return c.now.Add(d), false, nil
		}
		for _, layout := range []string{"2006-01-02 15:04", "2006/01/02 15:04", "2006-01-02T15:04:05Z07:00"} {
			if t, err := time.ParseInLocation(layout, s, c.loc); err == nil {
				return t, false, nil
			}
		}
		for _, layout := range []string{"2006-01-02", "2006/01/02", "02.01.2006"} {
			if t, err := time.ParseInLocation(layout, s, c.loc); err == nil {
				return t, true, nil
			}
		}
		return time.Time{}, false, queryError(x.Pos, "%q is not a date; use 2026-10-06, 2026/10/06 14:30 or a relative date such as -2w.", x.Text)
	case kql.Func:
		var inc time.Duration
		if len(x.Args) > 0 {
			d, ok := parseRelative(x.Args[0].Text, c.hpd, c.dpw)
			if !ok {
				return time.Time{}, false, queryError(x.Args[0].Pos, "%q is not an increment such as -1d.", x.Args[0].Text)
			}
			inc = d
		}
		n := c.now.In(c.loc)
		day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, c.loc)
		weekStart := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		monthStart := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, c.loc)
		yearStart := time.Date(n.Year(), 1, 1, 0, 0, 0, 0, c.loc)
		var base time.Time
		switch strings.ToLower(x.Name) {
		case "now":
			base = c.now
		case "startofday":
			base = day
		case "endofday":
			base = day.AddDate(0, 0, 1).Add(-time.Second)
		case "startofweek":
			base = weekStart
		case "endofweek":
			base = weekStart.AddDate(0, 0, 7).Add(-time.Second)
		case "startofmonth":
			base = monthStart
		case "endofmonth":
			base = monthStart.AddDate(0, 1, 0).Add(-time.Second)
		case "startofyear":
			base = yearStart
		case "endofyear":
			base = yearStart.AddDate(1, 0, 0).Add(-time.Second)
		default:
			return time.Time{}, false, queryError(x.Pos, "The function %s() cannot be used with dates.", x.Name)
		}
		return base.Add(inc), false, nil
	}
	return time.Time{}, false, queryError(0, "Expected a date.")
}

// parseRelative parses -1d, 2w, "-1w 2d", 4h, -30m (w = 7 calendar days, d = 24 hours).
func parseRelative(s string, _, _ int) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	sign := time.Duration(1)
	switch s[0] {
	case '-':
		sign, s = -1, s[1:]
	case '+':
		s = s[1:]
	}
	var total time.Duration
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return 0, false
	}
	for _, p := range parts {
		p = strings.TrimPrefix(p, "-")
		if len(p) < 2 {
			return 0, false
		}
		n, err := strconv.Atoi(p[:len(p)-1])
		if err != nil || n < 0 {
			return 0, false
		}
		switch p[len(p)-1] {
		case 'w':
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'd':
			total += time.Duration(n) * 24 * time.Hour
		case 'h':
			total += time.Duration(n) * time.Hour
		case 'm':
			total += time.Duration(n) * time.Minute
		default:
			return 0, false
		}
	}
	return sign * total, true
}

func (c *compiler) timeClause(cl *kql.Clause, column string, dateColumn bool) (string, error) {
	if _, isEmpty := cl.Value.(kql.Empty); isEmpty {
		if cl.Op == kql.OpIs || cl.Op == kql.OpEq {
			return column + " IS NULL", nil
		}
		return column + " IS NOT NULL", nil
	}
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpGt, kql.OpGe, kql.OpLt, kql.OpLe); err != nil {
		return "", err
	}
	t, dateOnly, err := c.instant(cl.Value)
	if err != nil {
		return "", err
	}
	if dateColumn {
		d := t.In(c.loc).Format("2006-01-02")
		return column + " " + string(cl.Op) + " " + c.args.Add(d) + "::date", nil
	}
	if dateOnly {
		// The day from its start to the start of the next; each bound only where it is used (PostgreSQL cannot type
		// an unused parameter).
		start := func() string { return c.args.Add(t) }
		end := func() string { return c.args.Add(t.AddDate(0, 0, 1)) }
		switch cl.Op {
		case kql.OpEq:
			return "(" + column + " >= " + start() + " AND " + column + " < " + end() + ")", nil
		case kql.OpNe:
			return "(" + column + " < " + start() + " OR " + column + " >= " + end() + ")", nil
		case kql.OpGt:
			return column + " >= " + end(), nil
		case kql.OpGe:
			return column + " >= " + start(), nil
		case kql.OpLt:
			return column + " < " + start(), nil
		case kql.OpLe:
			return column + " < " + end(), nil
		}
	}
	return column + " " + string(cl.Op) + " " + c.args.Add(t), nil
}

func (c *compiler) durationClause(cl *kql.Clause, column string) (string, error) {
	if _, isEmpty := cl.Value.(kql.Empty); isEmpty {
		if cl.Op == kql.OpIs || cl.Op == kql.OpEq {
			return "(" + column + " IS NULL OR " + column + " = 0)", nil
		}
		return "(" + column + " IS NOT NULL AND " + column + " <> 0)", nil
	}
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpGt, kql.OpGe, kql.OpLt, kql.OpLe); err != nil {
		return "", err
	}
	v, ok := cl.Value.(kql.Value)
	if !ok {
		return "", queryError(cl.Pos, "Expected a duration such as 2h.")
	}
	secs, err := ParseDuration(v.Text, c.hpd, c.dpw)
	if err != nil {
		return "", queryError(v.Pos, "%q is not a duration such as 1d 4h.", v.Text)
	}
	return column + " " + string(cl.Op) + " " + c.args.Add(secs), nil
}

func (c *compiler) numberClause(cl *kql.Clause, column string) (string, error) {
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpGt, kql.OpGe, kql.OpLt, kql.OpLe); err != nil {
		return "", err
	}
	v, ok := cl.Value.(kql.Value)
	if !ok {
		return "", queryError(cl.Pos, "Expected a number.")
	}
	n, err := strconv.ParseFloat(v.Text, 64)
	if err != nil {
		return "", queryError(v.Pos, "%q is not a number.", v.Text)
	}
	return column + " " + string(cl.Op) + " " + c.args.Add(n), nil
}

func (c *compiler) filterClause(cl *kql.Clause, vals []kql.Value) (string, error) {
	if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
		return "", err
	}
	if c.depth >= 3 {
		return "", queryError(cl.Pos, "Filters may nest at most three levels deep.")
	}
	var parts []string
	for _, v := range vals {
		f, err := c.s.filterByName(c.ctx, c.p, v.Text)
		if err != nil {
			return "", queryError(v.Pos, "The filter %q does not exist or is not shared with you.", v.Text)
		}
		q, err := kql.Parse(f.Query)
		if err != nil {
			return "", queryError(v.Pos, "The filter %q contains an invalid query.", v.Text)
		}
		sub := *c
		sub.depth++
		where := "true"
		if q.Where != nil {
			if where, err = sub.expr(q.Where); err != nil {
				return "", err
			}
		}
		parts = append(parts, where)
	}
	match := "(" + strings.Join(parts, " OR ") + ")"
	if negated(cl.Op) {
		return "NOT coalesce(" + match + ", false)", nil
	}
	return match, nil
}

// sprintClause is completed by the agile module (sprints); without sprints, only EMPTY can match.
func (c *compiler) sprintClause(cl *kql.Clause, vals []kql.Value, funcs []kql.Func, empty bool) (string, error) {
	if err := c.checkOps(cl, listOps...); err != nil {
		return "", err
	}
	ids, err := c.sprintIDs(vals, funcs)
	if err != nil {
		return "", err
	}
	return c.existsClause(cl, "issue_sprints x", "x.issue_id = "+c.col("id"), "x.sprint_id", ids, empty), nil
}

func (c *compiler) customClause(cl *kql.Clause, f *CustomField) (string, error) {
	key := c.args.Add(f.Key)
	val := c.col("fields") + "->>" + key
	present := c.col("fields") + " ? " + key
	if _, isEmpty := cl.Value.(kql.Empty); isEmpty || cl.Op == kql.OpIs || cl.Op == kql.OpIsNot {
		if cl.Op == kql.OpIs || cl.Op == kql.OpEq {
			return "NOT (" + present + ")", nil
		}
		return "(" + present + ")", nil
	}
	vals, _, empty := values(cl.Value)
	switch f.Type {
	case "text", "url", "textarea":
		if err := c.checkOps(cl, kql.OpLike, kql.OpNotLike); err != nil {
			return "", err
		}
		v, ok := cl.Value.(kql.Value)
		if !ok {
			return "", queryError(cl.Pos, "Expected a text value.")
		}
		q, err := c.tsquery(v.Pos, v.Text)
		if err != nil {
			return "", err
		}
		match := "to_tsvector(" + c.args.Add(c.lang) + "::regconfig, " + c.col("fields") + "->" + key + ") @@ " + q
		if cl.Op == kql.OpNotLike {
			return "NOT coalesce(" + match + ", false)", nil
		}
		return match, nil
	case "number":
		return c.numberClause(cl, "("+val+")::numeric")
	case "date":
		return c.timeClause(cl, "("+val+")::date", true)
	case "datetime":
		return c.timeClause(cl, "("+val+")::timestamptz", false)
	case "select", "radio", "multiselect", "checkboxes", "cascading":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		var ids []string
		for _, v := range vals {
			found := false
			for _, o := range f.Options {
				if strings.EqualFold(o.Value, v.Text) || strconv.FormatInt(o.ID, 10) == v.Text {
					ids, found = append(ids, strconv.FormatInt(o.ID, 10)), true
				}
			}
			if !found {
				return "", queryError(v.Pos, "The option %q does not exist for %s.", v.Text, f.Name)
			}
		}
		arr := c.args.Add(nonNilStrings(ids)) + "::text[]"
		var match string
		switch f.Type {
		case "select", "radio":
			match = val + " = ANY(" + arr + ")"
		case "cascading":
			match = "(" + c.col("fields") + "->" + key + "->>'parent' = ANY(" + arr + ") OR " + c.col("fields") + "->" + key + "->>'child' = ANY(" + arr + "))"
		default:
			match = "EXISTS (SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(" + c.col("fields") + "->" + key +
				") = 'array' THEN " + c.col("fields") + "->" + key + " ELSE '[]' END) e WHERE e = ANY(" + arr + "))"
		}
		return c.customMatch(cl, match, present, empty), nil
	case "user", "multiuser":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		_, funcs, _ := values(cl.Value)
		uids, err := c.users(vals, funcs)
		if err != nil {
			return "", err
		}
		strs := make([]string, len(uids))
		for i, id := range uids {
			strs[i] = strconv.FormatInt(id, 10)
		}
		arr := c.args.Add(nonNilStrings(strs)) + "::text[]"
		match := val + " = ANY(" + arr + ")"
		if f.Type == "multiuser" {
			match = "EXISTS (SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(" + c.col("fields") + "->" + key +
				") = 'array' THEN " + c.col("fields") + "->" + key + " ELSE '[]' END) e WHERE e = ANY(" + arr + "))"
		}
		return c.customMatch(cl, match, present, empty), nil
	case "labels":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		var ls []string
		for _, v := range vals {
			ls = append(ls, v.Text)
		}
		match := c.col("fields") + "->" + key + " ?| " + c.args.Add(nonNilStrings(ls)) + "::text[]"
		return c.customMatch(cl, match, present, empty), nil
	case "version", "multiversion":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		_, funcs, _ := values(cl.Value)
		vids, err := c.versions(vals, funcs)
		if err != nil {
			return "", err
		}
		strs := make([]string, len(vids))
		for i, id := range vids {
			strs[i] = strconv.FormatInt(id, 10)
		}
		arr := c.args.Add(nonNilStrings(strs)) + "::text[]"
		match := val + " = ANY(" + arr + ")"
		if f.Type == "multiversion" {
			match = "EXISTS (SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(" + c.col("fields") + "->" + key +
				") = 'array' THEN " + c.col("fields") + "->" + key + " ELSE '[]' END) e WHERE e = ANY(" + arr + "))"
		}
		return c.customMatch(cl, match, present, empty), nil
	case "group":
		if err := c.checkOps(cl, kql.OpEq, kql.OpNe, kql.OpIn, kql.OpNotIn); err != nil {
			return "", err
		}
		var strs []string
		for _, v := range vals {
			var id int64
			if err := c.q.QueryRow(c.ctx, "SELECT id FROM groups WHERE lower(name) = lower($1) OR id::text = $1", v.Text).Scan(&id); err != nil {
				return "", queryError(v.Pos, "The group %q does not exist.", v.Text)
			}
			strs = append(strs, strconv.FormatInt(id, 10))
		}
		return c.customMatch(cl, val+" = ANY("+c.args.Add(nonNilStrings(strs))+"::text[])", present, empty), nil
	}
	return "", queryError(cl.Pos, "The field %s cannot be searched.", f.Name)
}

func (c *compiler) customMatch(cl *kql.Clause, match, present string, empty bool) string {
	if empty {
		match = "(" + match + " OR NOT (" + present + "))"
	}
	if negated(cl.Op) {
		if empty {
			return "NOT coalesce(" + match + ", false)"
		}
		return "(" + present + " AND NOT coalesce(" + match + ", false))"
	}
	return match
}

// orderBy maps ORDER BY items to SQL; ids break ties.
func (c *compiler) orderBy(items []kql.Order) (string, error) {
	if len(items) == 0 {
		return c.col("created_at") + " DESC, " + c.col("id") + " DESC", nil
	}
	var parts []string
	for _, o := range items {
		dir := " ASC NULLS LAST"
		if o.Desc {
			dir = " DESC NULLS LAST"
		}
		var expr string
		if o.Field.CustomID != 0 || c.cfg.fieldByName(o.Field.Name) != nil && canonicalField(o.Field.Name) != "parent" {
			f := c.cfg.fields[o.Field.CustomID]
			if f == nil {
				f = c.cfg.fieldByName(o.Field.Name)
			}
			if f == nil {
				return "", queryError(o.Field.Pos, "The field %s does not exist.", o.Field.String())
			}
			key := c.args.Add(f.Key)
			switch f.Type {
			case "number":
				expr = "(" + c.col("fields") + "->>" + key + ")::numeric"
			case "date":
				expr = "(" + c.col("fields") + "->>" + key + ")::date"
			default:
				expr = c.col("fields") + "->>" + key
			}
		} else {
			switch canonicalField(o.Field.Name) {
			case "key":
				expr = "(SELECT key FROM issue_projects WHERE id = " + c.col("project_id") + "), " + c.col("number")
				if o.Desc {
					expr = "(SELECT key FROM issue_projects WHERE id = " + c.col("project_id") + ") DESC, " + c.col("number")
				}
			case "summary":
				expr = "lower(" + c.col("summary") + ")"
			case "project":
				expr = "(SELECT lower(name) FROM issue_projects WHERE id = " + c.col("project_id") + ")"
			case "issuetype":
				expr = "(SELECT position FROM issue_types WHERE id = " + c.col("type_id") + ")"
			case "status":
				expr = "(SELECT position FROM statuses WHERE id = " + c.col("status_id") + ")"
			case "priority":
				// DESC lists the highest priority (position 1) first.
				expr = "(SELECT -position FROM priorities WHERE id = " + c.col("priority_id") + ")"
			case "resolution":
				expr = "(SELECT position FROM resolutions WHERE id = " + c.col("resolution_id") + ")"
			case "assignee", "reporter", "creator":
				expr = "(SELECT lower(display_name) FROM users WHERE id = " + c.col(canonicalField(o.Field.Name)+"_id") + ")"
			case "created":
				expr = c.col("created_at")
			case "updated":
				expr = c.col("updated_at")
			case "resolved":
				expr = c.col("resolved_at")
			case "due":
				expr = c.col("due_date")
			case "originalestimate":
				expr = c.col("original_estimate")
			case "remainingestimate":
				expr = c.col("remaining_estimate")
			case "timespent":
				expr = c.col("time_spent")
			case "votes":
				expr = c.col("votes")
			case "rank":
				expr = c.col("rank")
			case "parent":
				expr = "(SELECT key FROM issues p2 WHERE p2.id = " + c.col("parent_id") + ")"
			default:
				return "", queryError(o.Field.Pos, "Issues cannot be ordered by %s.", o.Field.String())
			}
		}
		if canonicalField(o.Field.Name) == "key" {
			parts = append(parts, expr+dir)
			continue
		}
		parts = append(parts, expr+dir)
	}
	parts = append(parts, c.col("id"))
	return strings.Join(parts, ", "), nil
}
