// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Autocompletion of KQL in the query box: the catalogue tells the browser's tokenizer which fields
// there are, which operators each takes and which functions and keywords exist; the suggestions are the values of a
// field that start with what was typed, of what the caller may see — no names of projects, versions, sprints or
// people they may not browse.

// KQLCatalogue describes KQL for autocompletion.
type KQLCatalogue struct {
	Fields    []KQLField    `json:"fields"`
	Functions []KQLFunction `json:"functions"`
	Keywords  []string      `json:"keywords" doc:"Keywords and the predicates of WAS and CHANGED"`
}

// KQLField is a field of KQL.
type KQLField struct {
	Name      string   `json:"name" doc:"The name as written in queries"`
	Aliases   []string `json:"aliases"`
	Custom    bool     `json:"custom"`
	ID        int64    `json:"id,omitempty" doc:"Custom fields: the id of cf[id]"`
	Kind      string   `json:"kind" doc:"What its values are: project, key, issue, issuetype, status, statuscategory, priority, resolution, user, group, text, labels, component, version, sprint, date, datetime, duration, number, option, securitylevel, filter or none"`
	Operators []string `json:"operators" doc:"=, !=, >, >=, <, <=, ~, !~, in, not in, is, is not"`
	History   bool     `json:"history" doc:"The field takes WAS and CHANGED"`
	Orderable bool     `json:"orderable" doc:"ORDER BY takes the field"`
	Values    bool     `json:"values" doc:"The suggestions endpoint suggests values of the field"`
}

// KQLFunction is a function of KQL.
type KQLFunction struct {
	Name  string   `json:"name"`
	Arg   string   `json:"arg,omitempty" doc:"What its argument is: group, issue, project or increment (such as -1d); optional for increments and projects"`
	Kinds []string `json:"kinds" doc:"The kinds of fields it is used with"`
	List  bool     `json:"list" doc:"It returns several values: used with IN"`
}

// KQLSuggestion is a value for the query box.
type KQLSuggestion struct {
	Value  string `json:"value" doc:"The value (unquoted)"`
	Label  string `json:"label,omitempty" doc:"What it stands for, e.g. a person's or project's name"`
	Detail string `json:"detail,omitempty" doc:"Where it is from, e.g. the project of a version"`
}

// Operator sets of the catalogue, as the compiler takes them (search.go; a test keeps them in step).
var (
	opsEq      = []string{"=", "!=", "in", "not in"}
	opsList    = []string{"=", "!=", "in", "not in", "is", "is not"}
	opsCompare = []string{"=", "!=", ">", ">=", "<", "<=", "is", "is not"}
	opsText    = []string{"~", "!~"}
)

// kqlFields are the system fields of KQL in the order the query box offers them.
var kqlFields = []KQLField{
	{Name: "project", Kind: "project", Operators: opsEq, Orderable: true, Values: true},
	{Name: "issuetype", Aliases: []string{"type"}, Kind: "issuetype", Operators: opsEq, Orderable: true, Values: true},
	{Name: "status", Kind: "status", Operators: opsEq, History: true, Orderable: true, Values: true},
	{Name: "assignee", Kind: "user", Operators: opsList, History: true, Orderable: true, Values: true},
	{Name: "reporter", Kind: "user", Operators: opsList, History: true, Orderable: true, Values: true},
	{Name: "priority", Kind: "priority", Operators: []string{"=", "!=", ">", ">=", "<", "<=", "in", "not in", "is", "is not"},
		History: true, Orderable: true, Values: true},
	{Name: "resolution", Kind: "resolution", Operators: opsList, History: true, Orderable: true, Values: true},
	{Name: "statusCategory", Aliases: []string{"category"}, Kind: "statuscategory", Operators: opsEq, Values: true},
	{Name: "summary", Kind: "text", Operators: opsText, Orderable: true},
	{Name: "text", Kind: "text", Operators: opsText},
	{Name: "description", Kind: "text", Operators: []string{"~", "!~", "is", "is not"}},
	{Name: "comment", Kind: "text", Operators: opsText},
	{Name: "environment", Kind: "text", Operators: []string{"~", "!~", "is", "is not"}},
	{Name: "key", Aliases: []string{"issuekey", "id", "issue"}, Kind: "key",
		Operators: []string{"=", "!=", "in", "not in", ">", ">=", "<", "<="}, Orderable: true, Values: true},
	{Name: "labels", Aliases: []string{"label"}, Kind: "labels", Operators: opsList, Values: true},
	{Name: "component", Aliases: []string{"components"}, Kind: "component", Operators: opsList, Values: true},
	{Name: "fixVersion", Aliases: []string{"fixVersions"}, Kind: "version", Operators: opsList, History: true, Values: true},
	{Name: "affectedVersion", Aliases: []string{"affectsVersion", "version"}, Kind: "version", Operators: opsList, Values: true},
	{Name: "sprint", Kind: "sprint", Operators: opsList, Values: true},
	{Name: "parent", Aliases: []string{"Epic Link", "epic"}, Kind: "issue", Operators: opsList, Orderable: true, Values: true},
	{Name: "creator", Kind: "user", Operators: opsList, Orderable: true, Values: true},
	{Name: "created", Aliases: []string{"createdDate"}, Kind: "datetime", Operators: opsCompare, Orderable: true},
	{Name: "updated", Aliases: []string{"updatedDate"}, Kind: "datetime", Operators: opsCompare, Orderable: true},
	{Name: "resolved", Aliases: []string{"resolutionDate"}, Kind: "datetime", Operators: opsCompare, Orderable: true},
	{Name: "due", Aliases: []string{"duedate"}, Kind: "date", Operators: opsCompare, Orderable: true},
	{Name: "originalEstimate", Kind: "duration", Operators: opsCompare, Orderable: true},
	{Name: "remainingEstimate", Kind: "duration", Operators: opsCompare, Orderable: true},
	{Name: "timeSpent", Kind: "duration", Operators: opsCompare, Orderable: true},
	{Name: "votes", Kind: "number", Operators: []string{"=", "!=", ">", ">=", "<", "<="}, Orderable: true},
	{Name: "watcher", Aliases: []string{"watchers"}, Kind: "user", Operators: opsEq, Values: true},
	{Name: "voter", Kind: "user", Operators: opsEq, Values: true},
	{Name: "level", Kind: "securitylevel", Operators: opsList, Values: true},
	{Name: "attachments", Kind: "none", Operators: []string{"is", "is not"}},
	{Name: "filter", Aliases: []string{"savedFilter", "request"}, Kind: "filter", Operators: opsEq, Values: true},
	{Name: "rank", Kind: "none", Operators: []string{}, Orderable: true},
}

var dateKinds = []string{"date", "datetime"}

// kqlFunctions are the functions of KQL.
var kqlFunctions = []KQLFunction{
	{Name: "currentUser", Kinds: []string{"user"}},
	{Name: "membersOf", Arg: "group", Kinds: []string{"user"}, List: true},
	{Name: "now", Kinds: dateKinds},
	{Name: "startOfDay", Arg: "increment", Kinds: dateKinds},
	{Name: "endOfDay", Arg: "increment", Kinds: dateKinds},
	{Name: "startOfWeek", Arg: "increment", Kinds: dateKinds},
	{Name: "endOfWeek", Arg: "increment", Kinds: dateKinds},
	{Name: "startOfMonth", Arg: "increment", Kinds: dateKinds},
	{Name: "endOfMonth", Arg: "increment", Kinds: dateKinds},
	{Name: "startOfYear", Arg: "increment", Kinds: dateKinds},
	{Name: "endOfYear", Arg: "increment", Kinds: dateKinds},
	{Name: "openSprints", Kinds: []string{"sprint"}, List: true},
	{Name: "closedSprints", Kinds: []string{"sprint"}, List: true},
	{Name: "futureSprints", Kinds: []string{"sprint"}, List: true},
	{Name: "releasedVersions", Arg: "project", Kinds: []string{"version"}, List: true},
	{Name: "unreleasedVersions", Arg: "project", Kinds: []string{"version"}, List: true},
	{Name: "latestReleasedVersion", Arg: "project", Kinds: []string{"version"}},
	{Name: "earliestUnreleasedVersion", Arg: "project", Kinds: []string{"version"}},
	{Name: "linkedIssues", Arg: "issue", Kinds: []string{"key"}, List: true},
	{Name: "watchedIssues", Kinds: []string{"key"}, List: true},
	{Name: "votedIssues", Kinds: []string{"key"}, List: true},
	{Name: "childIssuesOf", Arg: "issue", Kinds: []string{"key"}, List: true},
}

var kqlKeywords = []string{"AND", "OR", "NOT", "IN", "IS", "EMPTY", "NULL", "ORDER BY", "ASC", "DESC", "WAS", "CHANGED",
	"AFTER", "BEFORE", "ON", "DURING", "BY", "FROM", "TO"}

// customKind returns the kind and operators of a custom field by its type.
func customKind(f *CustomField) (string, []string, bool) {
	switch f.Type {
	case "text", "url", "textarea":
		return "text", []string{"~", "!~", "is", "is not"}, false
	case "number":
		return "number", opsCompare, false
	case "date", "datetime":
		return f.Type, opsCompare, false
	case "select", "radio", "multiselect", "checkboxes", "cascading":
		return "option", opsList, true
	case "user", "multiuser":
		return "user", opsList, true
	case "group":
		return "group", opsList, true
	case "labels":
		return "labels", opsList, true
	case "version", "multiversion":
		return "version", opsList, true
	}
	return "none", []string{"is", "is not"}, false
}

// KQLCatalogue returns the fields, functions and keywords of KQL; custom fields for signed-in users (as the issue
// configuration), those of projects they browse.
func (s *Service) KQLCatalogue(ctx context.Context, p *identity.Principal) (*KQLCatalogue, error) {
	out := &KQLCatalogue{Fields: append([]KQLField{}, kqlFields...), Functions: kqlFunctions, Keywords: kqlKeywords}
	for k := range out.Fields {
		if out.Fields[k].Aliases == nil {
			out.Fields[k].Aliases = []string{}
		}
	}
	if !p.Authenticated() {
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
	browsable := a.BrowsableProjects()
	for _, f := range cfg.fieldList {
		if len(f.ProjectIDs) > 0 && !slices.ContainsFunc(f.ProjectIDs, func(id int64) bool { return slices.Contains(browsable, id) }) {
			continue
		}
		kind, ops, values := customKind(f)
		out.Fields = append(out.Fields, KQLField{Name: f.Name, Aliases: []string{}, Custom: true, ID: f.ID, Kind: kind, Operators: ops,
			Orderable: f.Type == "number" || f.Type == "date" || f.Type == "datetime" || f.Type == "text" || f.Type == "select",
			Values:    values})
	}
	return out, nil
}

var customRef = regexp.MustCompile(`^(?i)cf\[(\d+)\]$`)

// kqlField finds a field by a name as written in a query (system names and aliases, custom names, cf[id]).
func kqlField(cfg *configSnapshot, name string) (*KQLField, *CustomField) {
	name = strings.TrimSpace(name)
	if m := customRef.FindStringSubmatch(name); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if f := cfg.fields[id]; f != nil {
			return nil, f
		}
		return nil, nil
	}
	canonical := canonicalField(name)
	for k := range kqlFields {
		if canonicalField(kqlFields[k].Name) == canonical {
			return &kqlFields[k], nil
		}
	}
	return nil, cfg.fieldByName(name)
}

// kqlFunction finds a function by its name (case-insensitive).
func kqlFunction(name string) *KQLFunction {
	for k := range kqlFunctions {
		if strings.EqualFold(kqlFunctions[k].Name, strings.TrimSpace(name)) {
			return &kqlFunctions[k]
		}
	}
	return nil
}

// suggestionLimit is how many values the query box shows.
const suggestionLimit = 20

// KQLSuggestions returns the values of a field that start with the prefix (or contain it, for names), of what the
// principal sees.
func (s *Service) KQLSuggestions(ctx context.Context, p *identity.Principal, field, prefix string) ([]KQLSuggestion, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	out := []KQLSuggestion{}
	projects := a.BrowsableProjects()
	// Who browses no project sees no values: not even of the configuration.
	if len(projects) == 0 {
		return out, nil
	}
	sys, custom := kqlField(cfg, field)
	// The argument of a function, by the function's name: the group of membersOf(), the issue of linkedIssues(), the
	// project of the version functions.
	if fn := kqlFunction(field); fn != nil && fn.Arg != "" && fn.Arg != "increment" {
		sys, custom = &KQLField{Kind: map[string]string{"group": "group", "issue": "key", "project": "project"}[fn.Arg]}, nil
	}
	if custom != nil {
		if !p.Authenticated() || len(custom.ProjectIDs) > 0 && !slices.ContainsFunc(custom.ProjectIDs, func(id int64) bool {
			return slices.Contains(projects, id)
		}) {
			return out, nil
		}
	}
	prefix = strings.TrimSpace(prefix)
	lower := strings.ToLower(prefix)
	matches := func(texts ...string) bool {
		for _, t := range texts {
			if strings.Contains(strings.ToLower(t), lower) {
				return true
			}
		}
		return false
	}
	add := func(value, label, detail string) bool {
		if slices.ContainsFunc(out, func(x KQLSuggestion) bool { return x.Value == value && x.Detail == detail }) {
			return true
		}
		out = append(out, KQLSuggestion{Value: value, Label: label, Detail: detail})
		return len(out) < suggestionLimit
	}
	like := "%" + db.EscapeLike(lower) + "%"
	kind := ""
	switch {
	case sys != nil:
		kind = sys.Kind
	case custom != nil:
		kind, _, _ = customKind(custom)
	default:
		return out, nil
	}
	switch kind {
	case "project":
		rows, err := s.pool.Query(ctx, `SELECT key, name FROM issue_projects WHERE id = ANY($1) AND (lower(key) LIKE $2 OR lower(name) LIKE $2)
			ORDER BY lower(key) LIKE $3 DESC, lower(name) LIMIT $4`, projects, like, db.EscapeLike(lower)+"%", suggestionLimit)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (KQLSuggestion, error) {
			var sg KQLSuggestion
			err := r.Scan(&sg.Value, &sg.Label)
			return sg, err
		})
		if err != nil {
			return nil, err
		}
		return append(out, list...), nil
	case "issuetype":
		rows, err := s.pool.Query(ctx, "SELECT DISTINCT issue_type_id FROM project_issue_types WHERE project_id = ANY($1)", projects)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		for _, t := range cfg.typeList {
			if slices.Contains(ids, t.ID) && matches(t.Name) && !add(t.Name, "", "") {
				break
			}
		}
	case "status":
		// The statuses of the workflows the browsable projects use, in one query however many projects there are.
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT coalesce(pit.workflow_id, p.default_workflow_id) FROM issue_projects p
			LEFT JOIN project_issue_types pit ON pit.project_id = p.id WHERE p.id = ANY($1)`, projects)
		if err != nil {
			return nil, err
		}
		workflows, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		used := map[int64]bool{}
		for _, id := range workflows {
			if wf := cfg.workflows[id]; wf != nil {
				for _, sid := range wf.Def.Statuses {
					used[sid] = true
				}
			}
		}
		for _, st := range cfg.statusList {
			if used[st.ID] && matches(st.Name) && !add(st.Name, "", "") {
				break
			}
		}
	case "statuscategory":
		for _, c := range []string{"To Do", "In Progress", "Done"} {
			if matches(c) {
				add(c, "", "")
			}
		}
	case "priority":
		for _, pr := range cfg.prioList {
			if matches(pr.Name) && !add(pr.Name, "", "") {
				break
			}
		}
	case "resolution":
		if matches("Unresolved") {
			add("Unresolved", "", "")
		}
		for _, r := range cfg.resList {
			if matches(r.Name) && !add(r.Name, "", "") {
				break
			}
		}
	case "user":
		// Naming other users needs the permission to browse users, as in queries; currentUser() is a function.
		browse, err := s.authz.HasGlobal(ctx, p, authz.UserBrowse)
		if err != nil || !browse {
			return out, err
		}
		users, _, err := identity.ListUsers(ctx, s.pool, identity.UserFilter{Query: prefix, ByName: true, Status: identity.StatusActive,
			Limit: suggestionLimit})
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			add(u.Username, u.DisplayName, "")
		}
	case "group":
		browse, err := s.authz.HasGlobal(ctx, p, authz.UserBrowse)
		if err != nil || !browse {
			return out, err
		}
		groups, _, err := identity.ListGroups(ctx, s.pool, prefix, suggestionLimit, 0)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			add(g.Name, "", "")
		}
	case "labels":
		column := "i.labels"
		cond := "cardinality(i.labels) > 0"
		var args db.Args
		if custom != nil {
			key := args.Add(custom.Key)
			column = "ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(i.fields->" + key + ") = 'array' THEN i.fields->" + key +
				" ELSE '[]' END))"
			cond = "i.fields ? " + key
		}
		// The labels of the issues the caller sees that changed last: what people use now, at a bounded cost.
		rows, err := s.pool.Query(ctx, `SELECT l FROM (SELECT DISTINCT unnest(labels) AS l FROM (SELECT `+column+` AS labels FROM issues i
			WHERE `+cond+` AND `+a.VisibleSQL("i", &args)+` ORDER BY i.updated_at DESC LIMIT 5000) r) x WHERE lower(l) LIKE `+
			args.Add(like)+` ORDER BY lower(l) LIMIT `+args.Add(suggestionLimit), args...)
		if err != nil {
			return nil, err
		}
		labels, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, l := range labels {
			add(l, "", "")
		}
	case "component":
		rows, err := s.pool.Query(ctx, `SELECT c.name, p.key FROM components c JOIN issue_projects p ON p.id = c.project_id
			WHERE c.project_id = ANY($1) AND lower(c.name) LIKE $2 AND NOT c.archived ORDER BY lower(c.name), p.key LIMIT $3`,
			projects, like, suggestionLimit)
		if err != nil {
			return nil, err
		}
		if err := s.collectSuggestions(rows, add); err != nil {
			return nil, err
		}
	case "version":
		rows, err := s.pool.Query(ctx, `SELECT v.name, p.key FROM versions v JOIN issue_projects p ON p.id = v.project_id
			WHERE v.project_id = ANY($1) AND lower(v.name) LIKE $2 AND NOT v.archived
			ORDER BY v.released, lower(v.name), p.key LIMIT $3`, projects, like, suggestionLimit)
		if err != nil {
			return nil, err
		}
		if err := s.collectSuggestions(rows, add); err != nil {
			return nil, err
		}
	case "sprint":
		// The sprints of boards of projects the caller sees (as KQL resolves them, audit L18); open ones first.
		rows, err := s.pool.Query(ctx, `SELECT s.name, b.name FROM sprints s JOIN boards b ON b.id = s.board_id
			WHERE b.project_id = ANY($1) AND lower(s.name) LIKE $2 ORDER BY s.state = 'closed', s.id DESC LIMIT $3`,
			projects, like, suggestionLimit)
		if err != nil {
			return nil, err
		}
		if err := s.collectSuggestions(rows, add); err != nil {
			return nil, err
		}
	case "key", "issue":
		var args db.Args
		where := "(i.key LIKE " + args.Add(strings.ToUpper(db.EscapeLike(prefix))+"%") + " OR lower(i.summary) LIKE " + args.Add(like) + ")"
		if kind == "issue" {
			where += " AND i.type_id IN (SELECT id FROM issue_types WHERE level >= 0)"
		}
		rows, err := s.pool.Query(ctx, "SELECT i.key, i.summary, '' FROM issues i WHERE "+where+" AND "+a.VisibleSQL("i", &args)+
			" ORDER BY i.updated_at DESC LIMIT "+args.Add(10), args...)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (KQLSuggestion, error) {
			var sg KQLSuggestion
			err := r.Scan(&sg.Value, &sg.Label, &sg.Detail)
			return sg, err
		})
		if err != nil {
			return nil, err
		}
		return append(out, list...), nil
	case "securitylevel":
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT l.name, '' FROM security_levels l JOIN issue_projects p ON p.security_scheme_id = l.scheme_id
			WHERE p.id = ANY($1) AND lower(l.name) LIKE $2 ORDER BY 1 LIMIT $3`, projects, like, suggestionLimit)
		if err != nil {
			return nil, err
		}
		if err := s.collectSuggestions(rows, add); err != nil {
			return nil, err
		}
	case "filter":
		filters, err := s.Filters(ctx, p, SharedSelection{Query: prefix, Limit: suggestionLimit})
		if err != nil {
			return nil, err
		}
		for _, f := range filters.Items {
			if !add(f.Name, "", f.Owner.DisplayName) {
				break
			}
		}
	case "option":
		for _, o := range custom.Options {
			if !o.Disabled && matches(o.Value) && !add(o.Value, "", "") {
				break
			}
		}
	}
	return out, nil
}

// collectSuggestions adds rows of value and detail.
func (s *Service) collectSuggestions(rows pgx.Rows, add func(value, label, detail string) bool) error {
	defer rows.Close()
	for rows.Next() {
		var value, detail string
		if err := rows.Scan(&value, &detail); err != nil {
			return err
		}
		if !add(value, "", detail) {
			break
		}
	}
	return rows.Err()
}
