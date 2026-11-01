// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Widget types of dashboards.
var widgetTypes = []string{"filter-results", "assigned-to-me", "distribution", "matrix", "created-vs-resolved", "sprint-health",
	"activity", "text"}

// Widget is a gadget on a dashboard. Config holds the type's settings, e.g. {"query": "...", "field": "status"}.
type Widget struct {
	ID       string         `json:"id" minLength:"1" maxLength:"40"`
	Type     string         `json:"type" enum:"filter-results,assigned-to-me,distribution,matrix,created-vs-resolved,sprint-health,activity,text"`
	Title    string         `json:"title,omitempty" maxLength:"200"`
	Column   int            `json:"column" minimum:"0" maximum:"2"`
	Position int            `json:"position" minimum:"0"`
	Config   map[string]any `json:"config,omitempty"`
}

// Dashboard is a page of widgets.
type Dashboard struct {
	ID          int64            `json:"id"`
	Owner       identity.UserRef `json:"owner"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Columns     int              `json:"columns" minimum:"1" maximum:"3"`
	Widgets     []Widget         `json:"widgets"`
	Shares      []Share          `json:"shares"`
	Favourite   bool             `json:"favourite"`
	CanEdit     bool             `json:"canEdit"`
	Updated     time.Time        `json:"updated"`
	Version     int64            `json:"version"`
}

// DashboardInput creates or changes a dashboard.
type DashboardInput struct {
	Name        string   `json:"name" minLength:"1" maxLength:"200"`
	Description string   `json:"description,omitempty" maxLength:"2000"`
	Columns     int      `json:"columns,omitempty" minimum:"1" maximum:"3"`
	Widgets     []Widget `json:"widgets,omitempty" maxItems:"30"`
	Shares      []Share  `json:"shares,omitempty"`
}

// visibleDashboards returns the dashboards that match cond (on d, with the values args) and that the principal may
// see.
func (s *Service) visibleDashboards(ctx context.Context, p *identity.Principal, cond string, args ...any) ([]Dashboard, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	all := db.Args(args)
	return s.queryDashboards(ctx, p, "("+cond+") AND "+sharedSQL("d", p, a, &all)+" ORDER BY lower(d.name), d.id", &all)
}

// queryDashboards reads the dashboards (d) of a condition that includes who may see them, with its order and page.
func (s *Service) queryDashboards(ctx context.Context, p *identity.Principal, cond string, args *db.Args) ([]Dashboard, error) {
	uid := args.Add(p.UserID())
	rows, err := s.pool.Query(ctx, `SELECT d.id, d.owner_id, u.username, u.display_name, d.name, d.description, d.columns, d.widgets,
		d.shares, d.updated_at, d.version, EXISTS (SELECT 1 FROM dashboard_favourites f WHERE f.dashboard_id = d.id AND f.user_id = `+uid+`)
		FROM dashboards d JOIN users u ON u.id = d.owner_id WHERE `+cond, *args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Dashboard, error) {
		var d Dashboard
		var widgets, shares []byte
		err := row.Scan(&d.ID, &d.Owner.ID, &d.Owner.Username, &d.Owner.DisplayName, &d.Name, &d.Description, &d.Columns, &widgets,
			&shares, &d.Updated, &d.Version, &d.Favourite)
		_ = json.Unmarshal(widgets, &d.Widgets)
		_ = json.Unmarshal(shares, &d.Shares)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	admin, _ := s.authz.IsAdmin(ctx, p)
	for i := range list {
		d := &list[i]
		d.CanEdit = (d.Owner.ID == p.UserID() && p.Authenticated()) || admin
		if d.Widgets == nil {
			d.Widgets = []Widget{}
		}
		if d.Shares == nil {
			d.Shares = []Share{}
		}
		sort.SliceStable(d.Widgets, func(i, j int) bool {
			if d.Widgets[i].Column != d.Widgets[j].Column {
				return d.Widgets[i].Column < d.Widgets[j].Column
			}
			return d.Widgets[i].Position < d.Widgets[j].Position
		})
	}
	if list == nil {
		list = []Dashboard{}
	}
	return list, nil
}

// DashboardList is a page of dashboards.
type DashboardList struct {
	Total int         `json:"total" doc:"Dashboards that match, on all pages"`
	Items []Dashboard `json:"items"`
}

// Dashboards lists a page of the dashboards the principal may see: their own and those shared with them, by name.
func (s *Service) Dashboards(ctx context.Context, p *identity.Principal, sel SharedSelection) (*DashboardList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	cond := sel.where("d", "dashboard_favourites", "dashboard_id", p, a, &args)
	out := &DashboardList{}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM dashboards d WHERE "+cond, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	out.Items, err = s.queryDashboards(ctx, p, cond+" ORDER BY lower(d.name), d.id"+pageSQL(sel.Limit, sel.Offset, &args), &args)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetDashboard returns a visible dashboard.
func (s *Service) GetDashboard(ctx context.Context, p *identity.Principal, id int64) (*Dashboard, error) {
	list, err := s.visibleDashboards(ctx, p, "d.id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("dashboard.not-found", "The dashboard does not exist or is not shared with you.")
	}
	return &list[0], nil
}

func validateWidgets(ws []Widget, columns int) error {
	ids := map[string]bool{}
	for _, w := range ws {
		if ids[w.ID] {
			return apperr.Invalid("dashboard.widget", "Widget ids must be unique.")
		}
		ids[w.ID] = true
		known := false
		for _, t := range widgetTypes {
			known = known || t == w.Type
		}
		if !known {
			return apperr.Invalidf("dashboard.widget", "Unknown widget type %s.", w.Type)
		}
		if w.Column >= columns {
			return apperr.Invalid("dashboard.widget", "A widget is placed in a column the dashboard does not have.")
		}
	}
	return nil
}

// SaveDashboard creates (id 0) or changes a dashboard (owner or administrator).
func (s *Service) SaveDashboard(ctx context.Context, p *identity.Principal, id, version int64, in DashboardInput) (*Dashboard, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	if in.Columns == 0 {
		in.Columns = 2
	}
	if in.Widgets == nil {
		in.Widgets = []Widget{}
	}
	if in.Shares == nil {
		in.Shares = []Share{}
	}
	if err := validateWidgets(in.Widgets, in.Columns); err != nil {
		return nil, err
	}
	widgets, _ := json.Marshal(in.Widgets)
	shares, _ := json.Marshal(in.Shares)
	name := strings.TrimSpace(in.Name)
	if id == 0 {
		if err := s.pool.QueryRow(ctx, `INSERT INTO dashboards (owner_id, name, description, columns, widgets, shares)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, p.UserID(), name, in.Description, in.Columns, widgets, shares).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO dashboard_favourites (dashboard_id, user_id, position)
			VALUES ($1, $2, coalesce((SELECT max(position) + 1 FROM dashboard_favourites WHERE user_id = $2), 0))`, id, p.UserID()); err != nil {
			return nil, err
		}
		return s.GetDashboard(ctx, p, id)
	}
	cur, err := s.GetDashboard(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !cur.CanEdit {
		return nil, apperr.Forbidden("dashboard.edit-denied", "Only the owner can change this dashboard.")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE dashboards SET name = $2, description = $3, columns = $4, widgets = $5, shares = $6,
		updated_at = now(), version = version + 1 WHERE id = $1 AND ($7 = 0 OR version = $7)`, id, name, in.Description, in.Columns,
		widgets, shares, version)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.PreconditionFailed("dashboard.changed", "The dashboard was changed meanwhile. Reload and repeat your change.")
	}
	return s.GetDashboard(ctx, p, id)
}

// DeleteDashboard removes a dashboard (owner or administrator).
func (s *Service) DeleteDashboard(ctx context.Context, p *identity.Principal, id int64) error {
	cur, err := s.GetDashboard(ctx, p, id)
	if err != nil {
		return err
	}
	if !cur.CanEdit {
		return apperr.Forbidden("dashboard.edit-denied", "Only the owner can delete this dashboard.")
	}
	_, err = s.pool.Exec(ctx, "DELETE FROM dashboards WHERE id = $1", id)
	return err
}

// SetDashboardFavourite adds a dashboard to or removes it from the principal's dashboards.
func (s *Service) SetDashboardFavourite(ctx context.Context, p *identity.Principal, id int64, fav bool) error {
	if _, err := s.GetDashboard(ctx, p, id); err != nil {
		return err
	}
	var err error
	if fav {
		_, err = s.pool.Exec(ctx, `INSERT INTO dashboard_favourites (dashboard_id, user_id, position)
			VALUES ($1, $2, coalesce((SELECT max(position) + 1 FROM dashboard_favourites WHERE user_id = $2), 0)) ON CONFLICT DO NOTHING`, id, p.UserID())
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM dashboard_favourites WHERE dashboard_id = $1 AND user_id = $2", id, p.UserID())
	}
	return err
}

// --- statistics for widgets ---

// DistributionItem is a value of a field with the number of issues having it.
type DistributionItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Color string `json:"color,omitempty"`
	Count int    `json:"count"`
	Query string `json:"query" doc:"KQL clause selecting these issues"`
}

// Distribution counts the issues of a query by a field.
type Distribution struct {
	Field     string             `json:"field"`
	Total     int                `json:"total" doc:"Issues of the query"`
	Items     []DistributionItem `json:"items"`
	Truncated bool               `json:"truncated" doc:"The issues have more than 100 values of the field; items are the 100 most frequent"`
}

// maxDistributionItems limits the values a distribution lists; maxMatrixCells the cells of a matrix.
const (
	maxDistributionItems = 100
	maxMatrixCells       = 5000
)

// statisticFields are the fields issues can be counted by.
var statisticFields = []string{FieldStatus, FieldPriority, FieldAssignee, FieldReporter, FieldIssueType, FieldResolution, "project",
	FieldLabels, FieldComponents, FieldFixVersions, "statusCategory"}

// groupExpr returns SQL to group issues on alias i by a field: a join (for multi-valued fields and names), the
// key and the label. Join aliases end with sfx so that two groupings can be combined.
func (s *Service) groupExpr(cfg *configSnapshot, field, sfx string) (join, key, label string, err error) {
	r := strings.NewReplacer("$", sfx)
	switch field {
	case FieldStatus:
		return "", "i.status_id::text", "", nil
	case "statusCategory":
		return r.Replace("JOIN statuses gs$ ON gs$.id = i.status_id"), r.Replace("gs$.category"), r.Replace("gs$.category"), nil
	case FieldPriority:
		return "", "coalesce(i.priority_id::text, '')", "", nil
	case FieldIssueType:
		return "", "i.type_id::text", "", nil
	case FieldResolution:
		return "", "coalesce(i.resolution_id::text, '')", "", nil
	case FieldAssignee:
		return r.Replace("LEFT JOIN users gu$ ON gu$.id = i.assignee_id"), "coalesce(i.assignee_id::text, '')", r.Replace("coalesce(gu$.display_name, '')"), nil
	case FieldReporter:
		return r.Replace("LEFT JOIN users gu$ ON gu$.id = i.reporter_id"), "coalesce(i.reporter_id::text, '')", r.Replace("coalesce(gu$.display_name, '')"), nil
	case "project":
		return r.Replace("JOIN issue_projects gp$ ON gp$.id = i.project_id"), r.Replace("gp$.key"), r.Replace("gp$.name"), nil
	case FieldLabels:
		return r.Replace("LEFT JOIN LATERAL unnest(CASE WHEN cardinality(i.labels) = 0 THEN ARRAY[''] ELSE i.labels END) gl$(v) ON true"),
			r.Replace("gl$.v"), r.Replace("gl$.v"), nil
	case FieldComponents:
		return r.Replace("LEFT JOIN issue_components gic$ ON gic$.issue_id = i.id LEFT JOIN components gc$ ON gc$.id = gic$.component_id"),
			r.Replace("coalesce(gc$.id::text, '')"), r.Replace("coalesce(gc$.name, '')"), nil
	case FieldFixVersions:
		return r.Replace("LEFT JOIN issue_versions giv$ ON giv$.issue_id = i.id AND giv$.kind = 'fix' LEFT JOIN versions gv$ ON gv$.id = giv$.version_id"),
			r.Replace("coalesce(gv$.id::text, '')"), r.Replace("coalesce(gv$.name, '')"), nil
	}
	if id, ok := parseFieldKey(field); ok {
		if f := cfg.fields[id]; f != nil && (f.Type == "select" || f.Type == "radio") {
			return "", "coalesce(i.fields->>'" + f.Key + "', '')", "", nil
		}
	}
	return "", "", "", apperr.Invalidf("statistics.field", "Issues cannot be counted by %s.", field)
}

func kqlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// describe turns a group key into a label, colour and KQL clause.
func describe(cfg *configSnapshot, field, key, label string) (string, string, string) {
	empty := key == ""
	id := int64(0)
	for _, c := range key {
		if c < '0' || c > '9' {
			id = -1
			break
		}
	}
	if id == 0 && !empty {
		for _, c := range key {
			id = id*10 + int64(c-'0')
		}
	}
	switch field {
	case FieldStatus:
		if st := cfg.statuses[id]; st != nil {
			return st.Name, map[string]string{CategoryTodo: "gray", CategoryInProgress: "blue", CategoryDone: "teal"}[st.Category], "status = " + kqlQuote(st.Name)
		}
	case "statusCategory":
		names := map[string]string{CategoryTodo: "To Do", CategoryInProgress: "In Progress", CategoryDone: "Done"}
		colors := map[string]string{CategoryTodo: "gray", CategoryInProgress: "blue", CategoryDone: "teal"}
		return names[key], colors[key], "statusCategory = " + kqlQuote(names[key])
	case FieldPriority:
		if empty {
			return "", "", "priority is EMPTY"
		}
		if pr := cfg.priorities[id]; pr != nil {
			return pr.Name, pr.Color, "priority = " + kqlQuote(pr.Name)
		}
	case FieldIssueType:
		if t := cfg.types[id]; t != nil {
			return t.Name, t.Color, "issuetype = " + kqlQuote(t.Name)
		}
	case FieldResolution:
		if empty {
			return "", "", "resolution = Unresolved"
		}
		if r := cfg.resolutions[id]; r != nil {
			return r.Name, "", "resolution = " + kqlQuote(r.Name)
		}
	case FieldAssignee, FieldReporter:
		if empty {
			return "", "", field + " is EMPTY"
		}
		return label, "", field + " = " + key
	case "project":
		return label, "", "project = " + key
	case FieldLabels:
		if empty {
			return "", "", "labels is EMPTY"
		}
		return key, "", "labels = " + kqlQuote(key)
	case FieldComponents:
		if empty {
			return "", "", "component is EMPTY"
		}
		return label, "", "component = " + kqlQuote(label)
	case FieldFixVersions:
		if empty {
			return "", "", "fixVersion is EMPTY"
		}
		return label, "", "fixVersion = " + kqlQuote(label)
	default:
		if fid, ok := parseFieldKey(field); ok {
			if f := cfg.fields[fid]; f != nil {
				if empty {
					return "", "", "cf[" + itoa(fid) + "] is EMPTY"
				}
				for _, o := range f.Options {
					if itoa(o.ID) == key {
						return o.Value, "", "cf[" + itoa(fid) + "] = " + kqlQuote(o.Value)
					}
				}
			}
		}
	}
	return label, "", ""
}

// IssueDistribution counts the issues of a query by a field.
func (s *Service) IssueDistribution(ctx context.Context, p *identity.Principal, query, field string) (*Distribution, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	join, key, label, err := s.groupExpr(cfg, field, "")
	if err != nil {
		return nil, err
	}
	if label == "" {
		label = "''"
	}
	out := &Distribution{Field: field, Items: []DistributionItem{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, _, err := s.CompileQuery(ctx, tx, p, query, "i", &args)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issues i WHERE "+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		// One more than listed tells whether there are more.
		rows, err := tx.Query(ctx, "SELECT "+key+", "+label+", count(DISTINCT i.id) FROM issues i "+join+" WHERE "+where+
			" GROUP BY 1, 2 ORDER BY 3 DESC, 2 LIMIT "+args.Add(maxDistributionItems+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Items) == maxDistributionItems {
				out.Truncated = true
				break
			}
			var it DistributionItem
			var lbl string
			if err := rows.Scan(&it.Key, &lbl, &it.Count); err != nil {
				return err
			}
			it.Label, it.Color, it.Query = describe(cfg, field, it.Key, lbl)
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Matrix counts the issues of a query by two fields.
type Matrix struct {
	X         []DistributionItem `json:"x"`
	Y         []DistributionItem `json:"y"`
	Counts    [][]int            `json:"counts" doc:"counts[y][x]"`
	Total     int                `json:"total" doc:"Issues of the query"`
	Truncated bool               `json:"truncated" doc:"The issues have more than 5,000 combinations of the two fields; the matrix shows 5,000 of them"`
}

// IssueMatrix counts the issues of a query by two fields (two-dimensional statistics).
func (s *Service) IssueMatrix(ctx context.Context, p *identity.Principal, query, xField, yField string) (*Matrix, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	xj, xk, xl, err := s.groupExpr(cfg, xField, "x")
	if err != nil {
		return nil, err
	}
	yj, yk, yl, err := s.groupExpr(cfg, yField, "y")
	if err != nil {
		return nil, err
	}
	if xl == "" {
		xl = "''"
	}
	if yl == "" {
		yl = "''"
	}
	out := &Matrix{X: []DistributionItem{}, Y: []DistributionItem{}, Counts: [][]int{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, _, err := s.CompileQuery(ctx, tx, p, query, "i", &args)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issues i WHERE "+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		// One more than shown tells whether there are more.
		rows, err := tx.Query(ctx, "SELECT "+xk+", "+xl+", "+yk+", "+yl+", count(DISTINCT i.id) FROM issues i "+xj+" "+yj+" WHERE "+where+
			" GROUP BY 1, 2, 3, 4 LIMIT "+args.Add(maxMatrixCells+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		xi, yi := map[string]int{}, map[string]int{}
		type cell struct{ x, y, n int }
		var cells []cell
		for rows.Next() {
			if len(cells) == maxMatrixCells {
				out.Truncated = true
				break
			}
			var xKey, xLabel, yKey, yLabel string
			var n int
			if err := rows.Scan(&xKey, &xLabel, &yKey, &yLabel, &n); err != nil {
				return err
			}
			if _, ok := xi[xKey]; !ok {
				xi[xKey] = len(out.X)
				it := DistributionItem{Key: xKey}
				it.Label, it.Color, it.Query = describe(cfg, xField, xKey, xLabel)
				out.X = append(out.X, it)
			}
			if _, ok := yi[yKey]; !ok {
				yi[yKey] = len(out.Y)
				it := DistributionItem{Key: yKey}
				it.Label, it.Color, it.Query = describe(cfg, yField, yKey, yLabel)
				out.Y = append(out.Y, it)
			}
			cells = append(cells, cell{xi[xKey], yi[yKey], n})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Counts = make([][]int, len(out.Y))
		for i := range out.Counts {
			out.Counts[i] = make([]int, len(out.X))
		}
		for _, c := range cells {
			out.Counts[c.y][c.x] += c.n
			out.X[c.x].Count += c.n
			out.Y[c.y].Count += c.n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SprintHealth summarises the active sprints of a board.
type SprintHealth struct {
	Board     ProjectRef         `json:"board" doc:"The board (id and name; key is the project key)"`
	Sprints   []SprintHealthItem `json:"sprints"`
	Truncated bool               `json:"truncated" doc:"The active sprints have more issues than a board shows (2,000); only those are counted"`
}

// SprintHealthItem is the progress of an active sprint.
type SprintHealthItem struct {
	Sprint       Sprint     `json:"sprint"`
	Statistic    Estimation `json:"statistic"`
	Todo         int        `json:"todo"`
	InProgress   int        `json:"inProgress"`
	Done         int        `json:"done"`
	Estimate     float64    `json:"estimate"`
	EstimateDone float64    `json:"estimateDone"`
	TimeElapsed  float64    `json:"timeElapsed" doc:"Share of the sprint's time that has passed (0..1)"`
}

// BoardSprintHealth returns the progress of the board's active sprints.
func (s *Service) BoardSprintHealth(ctx context.Context, p *identity.Principal, boardID int64) (*SprintHealth, error) {
	view, err := s.BoardData(ctx, p, boardID, nil, 0)
	if err != nil {
		return nil, err
	}
	out := &SprintHealth{Board: ProjectRef{ID: view.Board.ID, Key: view.Board.Project.Key, Name: view.Board.Name}, Sprints: []SprintHealthItem{},
		Truncated: view.Truncated}
	for _, sp := range view.Sprints {
		h := SprintHealthItem{Sprint: sp, Statistic: view.Board.Config.Estimation}
		for _, c := range view.Cards {
			if c.SprintID != sp.ID || (c.Type != nil && c.Type.Level == -1) {
				continue
			}
			switch {
			case c.Done:
				h.Done++
			case c.Status != nil && c.Status.Category == CategoryTodo:
				h.Todo++
			default:
				h.InProgress++
			}
			if c.Estimate != nil {
				h.Estimate += *c.Estimate
				if c.Done {
					h.EstimateDone += *c.Estimate
				}
			}
		}
		if sp.Start != nil && sp.End != nil && sp.End.After(*sp.Start) {
			h.TimeElapsed = min(1, max(0, float64(now().Sub(*sp.Start))/float64(sp.End.Sub(*sp.Start))))
		}
		out.Sprints = append(out.Sprints, h)
	}
	return out, nil
}

// ActivityItem is an entry of the activity stream of issues.
type ActivityItem struct {
	At      time.Time         `json:"at"`
	Actor   *identity.UserRef `json:"actor,omitempty"`
	Verb    string            `json:"verb"`
	Title   string            `json:"title"`
	URL     string            `json:"url"`
	Project ProjectRef        `json:"project"`
}

// Activity returns recent changes of issues the principal can see, optionally of one project.
func (s *Service) Activity(ctx context.Context, p *identity.Principal, projectKey string, limit int) ([]ActivityItem, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	var args db.Args
	visible := a.VisibleSQL("i", &args)
	where := "true"
	if projectKey != "" {
		where = "i.project_id = (SELECT project_id FROM issue_project_keys WHERE key = upper(" + args.Add(projectKey) + "))"
	}
	rows, err := s.pool.Query(ctx, `SELECT a.at, a.actor_id, u.username, u.display_name, a.verb, a.title, a.url, pr.id, pr.key, pr.name
		FROM activity a JOIN issues i ON i.id = a.entity_id JOIN issue_projects pr ON pr.id = i.project_id
		LEFT JOIN users u ON u.id = a.actor_id
		WHERE a.entity_type = 'issue' AND a.container_type = 'project' AND `+visible+` AND `+where+`
		ORDER BY a.at DESC, a.id DESC LIMIT `+args.Add(limit), args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ActivityItem, error) {
		var it ActivityItem
		var actor *int64
		var un, dn *string
		err := row.Scan(&it.At, &actor, &un, &dn, &it.Verb, &it.Title, &it.URL, &it.Project.ID, &it.Project.Key, &it.Project.Name)
		if actor != nil {
			it.Actor = &identity.UserRef{ID: *actor, Username: *un, DisplayName: *dn, Active: true}
		}
		return it, err
	})
	if out == nil {
		out = []ActivityItem{}
	}
	return out, err
}
