// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/rank"
)

// Board types.
const (
	BoardScrum  = "scrum"
	BoardKanban = "kanban"
)

// BoardColumn maps statuses to a column of a board.
type BoardColumn struct {
	Name     string  `json:"name" minLength:"1" maxLength:"60"`
	Statuses []int64 `json:"statuses"`
	Min      *int    `json:"min,omitempty" minimum:"0" doc:"Minimum number of issues (work-in-progress limit)"`
	Max      *int    `json:"max,omitempty" minimum:"0" doc:"Maximum number of issues (work-in-progress limit)"`
}

// NamedQuery is a named KQL clause, e.g. a quick filter or a swimlane.
type NamedQuery struct {
	ID    int    `json:"id"`
	Name  string `json:"name" minLength:"1" maxLength:"100"`
	Query string `json:"query" minLength:"1" maxLength:"2000"`
	Color string `json:"color,omitempty" maxLength:"20"`
}

// Swimlanes groups the cards of a board in rows.
type Swimlanes struct {
	Type    string       `json:"type" enum:"none,assignee,epic,parent,queries"`
	Queries []NamedQuery `json:"queries,omitempty"`
}

// CardLayout says what cards show.
type CardLayout struct {
	Fields       []string     `json:"fields" maxItems:"3" doc:"Up to three extra fields"`
	Colors       string       `json:"colors" enum:"none,issuetype,priority,queries"`
	ColorQueries []NamedQuery `json:"colorQueries,omitempty"`
}

// Estimation is the statistic boards and reports sum up.
type Estimation struct {
	Type  string `json:"type" enum:"field,originalEstimate,count"`
	Field string `json:"field,omitempty" doc:"A number field (cf_<id>) when the type is field"`
}

// BoardConfig is the definition of a board.
type BoardConfig struct {
	Columns           []BoardColumn `json:"columns" minItems:"1" maxItems:"30"`
	Swimlanes         Swimlanes     `json:"swimlanes"`
	QuickFilters      []NamedQuery  `json:"quickFilters"`
	Card              CardLayout    `json:"card"`
	Estimation        Estimation    `json:"estimation"`
	SubQuery          string        `json:"subQuery,omitempty" maxLength:"2000" doc:"Kanban: additional condition, e.g. fixVersion in unreleasedVersions() OR fixVersion is EMPTY"`
	HideDoneAfterDays int           `json:"hideDoneAfterDays,omitempty" minimum:"0" doc:"Kanban: hide issues of the last column not updated for this many days (0 = show all)"`
	ParallelSprints   bool          `json:"parallelSprints,omitempty" doc:"Scrum: allow several active sprints"`
}

// Board shows the issues of a query in columns.
type Board struct {
	ID               int64             `json:"id"`
	Name             string            `json:"name"`
	Type             string            `json:"type" enum:"scrum,kanban"`
	Project          ProjectRef        `json:"project"`
	Query            string            `json:"query"`
	Config           BoardConfig       `json:"config"`
	Owner            *identity.UserRef `json:"owner,omitempty"`
	Updated          time.Time         `json:"updated"`
	Version          int64             `json:"version"`
	CanAdmin         bool              `json:"canAdmin"`
	CanManageSprints bool              `json:"canManageSprints"`

	projectID int64
}

// BoardInput creates or changes a board.
type BoardInput struct {
	Name    string       `json:"name" minLength:"1" maxLength:"100"`
	Type    string       `json:"type,omitempty" enum:"scrum,kanban" doc:"Only when creating"`
	Project string       `json:"project,omitempty" doc:"Project key (only when creating)"`
	Query   string       `json:"query,omitempty" maxLength:"10000" doc:"KQL; defaults to the project's issues ordered by rank"`
	Config  *BoardConfig `json:"config,omitempty" doc:"Defaults to one column per status of the project's workflows"`
}

// BoardEvent announces board changes.
type BoardEvent struct {
	Name      string
	BoardID   int64
	ProjectID int64
}

// EventName implements events.Event.
func (e BoardEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live.
func (e BoardEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "board", ID: itoa(e.BoardID), Container: "project:" + itoa(e.ProjectID)}
}

const boardColumns = `b.id, b.name, b.type, b.project_id, p.key, p.name, b.query, b.config, b.owner_id, u.username, u.display_name,
	b.updated_at, b.version`

func scanBoard(row pgx.Row) (*Board, error) {
	var b Board
	var raw []byte
	var owner *int64
	var on, od *string
	if err := row.Scan(&b.ID, &b.Name, &b.Type, &b.Project.ID, &b.Project.Key, &b.Project.Name, &b.Query, &raw, &owner, &on, &od,
		&b.Updated, &b.Version); err != nil {
		return nil, err
	}
	b.projectID = b.Project.ID
	if owner != nil {
		b.Owner = &identity.UserRef{ID: *owner, Username: *on, DisplayName: *od, Active: true}
	}
	if err := json.Unmarshal(raw, &b.Config); err != nil {
		return nil, err
	}
	b.Config.normalise()
	return &b, nil
}

func (c *BoardConfig) normalise() {
	if c.Columns == nil {
		c.Columns = []BoardColumn{}
	}
	for i := range c.Columns {
		if c.Columns[i].Statuses == nil {
			c.Columns[i].Statuses = []int64{}
		}
	}
	if c.Swimlanes.Type == "" {
		c.Swimlanes.Type = "none"
	}
	if c.QuickFilters == nil {
		c.QuickFilters = []NamedQuery{}
	}
	if c.Card.Fields == nil {
		c.Card.Fields = []string{}
	}
	if c.Card.Colors == "" {
		c.Card.Colors = "issuetype"
	}
	if c.Estimation.Type == "" {
		c.Estimation.Type = "count"
	}
}

func (s *Service) boardRow(ctx context.Context, q db.Querier, id int64) (*Board, error) {
	b, err := scanBoard(q.QueryRow(ctx, "SELECT "+boardColumns+` FROM boards b JOIN issue_projects p ON p.id = b.project_id
		LEFT JOIN users u ON u.id = b.owner_id WHERE b.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("board.not-found", "The board does not exist.")
	}
	return b, err
}

// doneStatuses are the statuses of the last column: issues there count as done on the board.
func (b *Board) doneStatuses() map[int64]bool {
	out := map[int64]bool{}
	if n := len(b.Config.Columns); n > 0 {
		for _, id := range b.Config.Columns[n-1].Statuses {
			out[id] = true
		}
	}
	return out
}

// columnOf returns the column of a status, or -1.
func (b *Board) columnOf(status int64) int {
	for i, c := range b.Config.Columns {
		if slices.Contains(c.Statuses, status) {
			return i
		}
	}
	return -1
}

func (b *Board) mappedStatuses() []int64 {
	out := []int64{}
	for _, c := range b.Config.Columns {
		out = append(out, c.Statuses...)
	}
	return out
}

// estimator computes the board's estimation statistic of an issue.
type estimator struct {
	kind  string
	field string
}

func (s *Service) estimator(_ context.Context, b *Board) (estimator, error) {
	return estimator{kind: b.Config.Estimation.Type, field: b.Config.Estimation.Field}, nil
}

// value returns the statistic from an issue's original estimate (seconds) and custom fields (JSON).
func (e estimator) value(orig *int64, fields []byte) *float64 {
	switch e.kind {
	case "count":
		one := 1.0
		return &one
	case "originalEstimate":
		if orig == nil {
			return nil
		}
		v := float64(*orig)
		return &v
	case "field":
		var m map[string]any
		if json.Unmarshal(fields, &m) != nil {
			return nil
		}
		if n, ok := m[e.field].(float64); ok {
			return &n
		}
	}
	return nil
}

// fromHistory interprets a history value of the statistic's field (nil for other fields).
func (e estimator) tracks(field string) bool {
	return (e.kind == "field" && field == e.field) || (e.kind == "originalEstimate" && field == FieldOriginalEstimate)
}

// --- board definitions ---

// projectStatuses returns the statuses of the workflows a project uses, ordered by category and position.
func (s *Service) projectStatuses(ctx context.Context, q db.Querier, cfg *configSnapshot, projectID int64) ([]*Status, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT coalesce(pit.workflow_id, p.default_workflow_id) FROM issue_projects p
		LEFT JOIN project_issue_types pit ON pit.project_id = p.id WHERE p.id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	wfs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []*Status
	for _, id := range wfs {
		wf := cfg.workflows[id]
		if wf == nil {
			continue
		}
		for _, sid := range wf.Def.Statuses {
			if st := cfg.statuses[sid]; st != nil && !seen[sid] {
				seen[sid] = true
				out = append(out, st)
			}
		}
	}
	order := map[string]int{CategoryTodo: 0, CategoryInProgress: 1, CategoryDone: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Category] != order[out[j].Category] {
			return order[out[i].Category] < order[out[j].Category]
		}
		return out[i].Position < out[j].Position
	})
	return out, nil
}

// defaultBoardConfig has one column per status of the project's workflows.
func (s *Service) defaultBoardConfig(ctx context.Context, q db.Querier, cfg *configSnapshot, projectID int64, typ string) (BoardConfig, error) {
	statuses, err := s.projectStatuses(ctx, q, cfg, projectID)
	if err != nil {
		return BoardConfig{}, err
	}
	lang := "en"
	if st, err := s.settings.Get(ctx); err == nil {
		lang = st.DefaultLocale
	}
	c := BoardConfig{Swimlanes: Swimlanes{Type: "none"}, QuickFilters: []NamedQuery{
		{ID: 1, Name: defaultTexts.get("board.quick.mine", lang), Query: "assignee = currentUser()"},
		{ID: 2, Name: defaultTexts.get("board.quick.recent", lang), Query: "updated >= -1d"},
	}, Card: CardLayout{Fields: []string{}, Colors: "issuetype"}, Estimation: Estimation{Type: "count"}}
	for _, st := range statuses {
		c.Columns = append(c.Columns, BoardColumn{Name: st.Name, Statuses: []int64{st.ID}})
	}
	if len(c.Columns) == 0 {
		c.Columns = []BoardColumn{}
	}
	if typ == BoardScrum {
		for _, f := range cfg.fieldList {
			if f.Type == "number" && strings.EqualFold(f.Name, "Story Points") {
				c.Estimation = Estimation{Type: "field", Field: f.Key}
				break
			}
		}
	} else {
		c.HideDoneAfterDays = 14
	}
	return c, nil
}

// validateBoard checks the query, the columns and the named queries of a board definition.
func (s *Service) validateBoard(ctx context.Context, p *identity.Principal, cfg *configSnapshot, query string, c *BoardConfig) error {
	if _, err := s.Search(ctx, p, query, 1, 0); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for i, col := range c.Columns {
		if strings.TrimSpace(col.Name) == "" {
			return apperr.Invalid("board.column", "Every column needs a name.")
		}
		for _, st := range col.Statuses {
			if cfg.statuses[st] == nil {
				return apperr.Invalidf("board.column", "Column %s maps an unknown status.", col.Name)
			}
			if seen[st] {
				return apperr.Invalid("board.column", "A status can be mapped to one column only.")
			}
			seen[st] = true
		}
		if col.Min != nil && col.Max != nil && *col.Min > *col.Max {
			return apperr.Invalidf("board.column", "The minimum of column %s is above its maximum.", col.Name)
		}
		c.Columns[i].Name = strings.TrimSpace(col.Name)
	}
	check := func(list []NamedQuery, code, what string) error {
		ids := map[int]bool{}
		for _, nq := range list {
			if ids[nq.ID] || nq.ID <= 0 {
				return apperr.Invalidf("board."+code, "The %s need unique positive ids.", what)
			}
			ids[nq.ID] = true
			if _, err := kql.Parse(nq.Query); err != nil {
				return apperr.Invalidf("board."+code, "The query of %s is not valid: %v", nq.Name, err)
			}
		}
		return nil
	}
	if err := check(c.QuickFilters, "quick-filters", "quick filters"); err != nil {
		return err
	}
	if err := check(c.Swimlanes.Queries, "swimlanes", "swimlanes"); err != nil {
		return err
	}
	if err := check(c.Card.ColorQueries, "card-colours", "card colours"); err != nil {
		return err
	}
	if c.SubQuery != "" {
		if _, err := kql.Parse(c.SubQuery); err != nil {
			return apperr.Invalidf("board.sub-query", "The additional condition is not valid: %v", err)
		}
	}
	if c.Estimation.Type == "field" {
		id, ok := parseFieldKey(c.Estimation.Field)
		if f := cfg.fields[id]; !ok || f == nil || f.Type != "number" {
			return apperr.Invalid("board.estimation", "The estimation field must be a number field.")
		}
	}
	return nil
}

// boardAdmin reports whether the principal administers a board: project administrators, and its owner while the
// project is not archived (archived projects are read-only, audit L15).
func boardAdmin(a *Access, p *identity.Principal, b *Board) bool {
	owner := b.Owner != nil && p.Authenticated() && b.Owner.ID == p.UserID() && !a.archived[b.projectID]
	return a.Can(b.projectID, PermAdmin) || owner
}

func (s *Service) boardPermissions(ctx context.Context, p *identity.Principal, b *Board) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if !a.CanAny(b.projectID, PermBrowse) {
		return apperr.NotFound("board.not-found", "The board does not exist.")
	}
	b.CanAdmin = boardAdmin(a, p, b)
	b.CanManageSprints = b.Type == BoardScrum && a.Can(b.projectID, PermManageSprints)
	return nil
}

// BoardFilter selects a page of boards.
type BoardFilter struct {
	Project string // key of the project
	Query   string // part of the name of the board or of the key or name of its project
	Type    string // scrum or kanban
	Limit   int
	Offset  int
}

// BoardList is a page of boards.
type BoardList struct {
	Total int     `json:"total" doc:"Boards that match, on all pages"`
	Items []Board `json:"items"`
}

// Boards lists a page of the boards the principal can see, optionally of one project, by name. A board is visible
// with its project, so the projects the principal browses are part of the query and totals and pages count only
// visible boards.
func (s *Service) Boards(ctx context.Context, p *identity.Principal, f BoardFilter) (*BoardList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := []string{"b.project_id = ANY(" + args.Add(nonNil(a.BrowsableProjects())) + ")"}
	if a.system {
		where = []string{"true"}
	}
	if f.Project != "" {
		where = append(where, "b.project_id = (SELECT project_id FROM issue_project_keys WHERE key = upper("+args.Add(f.Project)+"))")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := args.Add("%" + db.EscapeLike(strings.ToLower(q)) + "%")
		where = append(where, "(lower(b.name) LIKE "+like+" OR lower(p.key) LIKE "+like+" OR lower(p.name) LIKE "+like+")")
	}
	if f.Type != "" {
		where = append(where, "b.type = "+args.Add(f.Type))
	}
	cond := strings.Join(where, " AND ")
	out := &BoardList{Items: []Board{}}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM boards b JOIN issue_projects p ON p.id = b.project_id WHERE "+cond,
		args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+boardColumns+` FROM boards b JOIN issue_projects p ON p.id = b.project_id
		LEFT JOIN users u ON u.id = b.owner_id WHERE `+cond+" ORDER BY lower(b.name), b.id"+pageSQL(f.Limit, f.Offset, &args), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, err
		}
		b.CanAdmin = boardAdmin(a, p, b)
		b.CanManageSprints = b.Type == BoardScrum && a.Can(b.projectID, PermManageSprints)
		out.Items = append(out.Items, *b)
	}
	return out, rows.Err()
}

// GetBoard returns a board the principal can see.
func (s *Service) GetBoard(ctx context.Context, p *identity.Principal, id int64) (*Board, error) {
	b, err := s.boardRow(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	return b, s.boardPermissions(ctx, p, b)
}

func (s *Service) createBoard(ctx context.Context, tx pgx.Tx, cfg *configSnapshot, ownerID int64, proj *Project, name, typ, query string, c *BoardConfig) (int64, error) {
	if query == "" {
		query = "project = " + proj.Key + " ORDER BY rank"
	}
	if c == nil {
		def, err := s.defaultBoardConfig(ctx, tx, cfg, proj.ID, typ)
		if err != nil {
			return 0, err
		}
		c = &def
	}
	raw, _ := json.Marshal(c)
	var owner any
	if ownerID != 0 {
		owner = ownerID
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO boards (project_id, name, type, query, config, owner_id) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, proj.ID, name, typ, query, raw, owner).Scan(&id)
	return id, err
}

// CreateBoard adds a board to a project (project administrators).
func (s *Service) CreateBoard(ctx context.Context, p *identity.Principal, in BoardInput) (*Board, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := projectByKey(ctx, s.pool, in.Project)
	if err != nil {
		return nil, err
	}
	if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
		return nil, err
	}
	if in.Type != BoardScrum && in.Type != BoardKanban {
		return nil, apperr.Invalid("board.type", "The type must be scrum or kanban.")
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		query = "project = " + proj.Key + " ORDER BY rank"
	}
	if in.Config != nil {
		in.Config.normalise()
		if err := s.validateBoard(ctx, p, cfg, query, in.Config); err != nil {
			return nil, err
		}
	} else if err := s.validateBoard(ctx, p, cfg, query, &BoardConfig{}); err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if id, err = s.createBoard(ctx, tx, cfg, p.UserID(), proj, strings.TrimSpace(in.Name), in.Type, query, in.Config); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "board.create", TargetType: "board", TargetID: itoa(id),
			Details: map[string]any{"name": in.Name, "project": proj.Key}}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, BoardEvent{Name: "board.created", BoardID: id, ProjectID: proj.ID})
	})
	if err != nil {
		return nil, err
	}
	return s.GetBoard(ctx, p, id)
}

// UpdateBoard changes name, query and definition of a board (project administrators and the owner).
func (s *Service) UpdateBoard(ctx context.Context, p *identity.Principal, id, version int64, in BoardInput) (*Board, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	b, err := s.GetBoard(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !b.CanAdmin {
		return nil, apperr.Forbidden("board.denied", "Only administrators of the project can change this board.")
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		query = b.Query
	}
	c := b.Config
	if in.Config != nil {
		c = *in.Config
		c.normalise()
	}
	if err := s.validateBoard(ctx, p, cfg, query, &c); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(c)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE boards SET name = $2, query = $3, config = $4, updated_at = now(), version = version + 1
			WHERE id = $1 AND ($5 = 0 OR version = $5)`, id, strings.TrimSpace(in.Name), query, raw, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.PreconditionFailed("board.changed", "The board was changed meanwhile. Reload and repeat your change.")
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "board.update", TargetType: "board", TargetID: itoa(id),
			Details: map[string]any{"name": in.Name}}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, BoardEvent{Name: "board.updated", BoardID: id, ProjectID: b.projectID})
	})
	if err != nil {
		return nil, err
	}
	return s.GetBoard(ctx, p, id)
}

// DeleteBoard removes a board; its sprints stay in the history of their issues.
func (s *Service) DeleteBoard(ctx context.Context, p *identity.Principal, id int64) error {
	b, err := s.GetBoard(ctx, p, id)
	if err != nil {
		return err
	}
	if !b.CanAdmin {
		return apperr.Forbidden("board.denied", "Only administrators of the project can delete this board.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Open sprints of the board end: their issues return to the backlog.
		rows, err := tx.Query(ctx, `SELECT x.issue_id FROM issue_sprints x JOIN sprints s ON s.id = x.sprint_id
			WHERE s.board_id = $1 AND s.state <> 'closed'`, id)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, iid := range ids {
			if _, err := moveToSprint(ctx, tx, iid, nil, p.UserID()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM sprints WHERE board_id = $1 AND state <> 'closed'", id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM boards WHERE id = $1", id); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "board.delete", TargetType: "board", TargetID: itoa(id),
			Details: map[string]any{"name": b.Name}}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, BoardEvent{Name: "board.deleted", BoardID: id, ProjectID: b.projectID})
	})
}

// --- ranking ---

// RankInput orders issues relative to another issue.
type RankInput struct {
	Issues []string `json:"issues" minItems:"1" maxItems:"500" doc:"Issue keys, in their new order"`
	Before string   `json:"before,omitempty" doc:"Put the issues directly before this issue"`
	After  string   `json:"after,omitempty" doc:"Put the issues directly after this issue"`
}

// RankIssues changes the rank (the global order of issues on boards and backlogs).
func (s *Service) RankIssues(ctx context.Context, p *identity.Principal, in RankInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.rankIssues(ctx, tx, a, in.Issues, in.Before, in.After)
	})
}

func (s *Service) rankIssues(ctx context.Context, tx pgx.Tx, a *Access, keys []string, before, after string) error {
	if (before == "") == (after == "") {
		return apperr.Invalid("rank.anchor", "Give either before or after.")
	}
	anchorKey := before
	if anchorKey == "" {
		anchorKey = after
	}
	anchor, _, err := s.issueFor(ctx, tx, a, anchorKey, false)
	if err != nil {
		return err
	}
	moving := map[int64]bool{}
	var issues []*Issue
	for _, k := range keys {
		cur, _, err := s.issueFor(ctx, tx, a, k, true)
		if err != nil {
			return err
		}
		if !a.CanIssue(cur.head(), PermSchedule) {
			return apperr.Forbidden("issue.schedule-denied", "You may not rank %s.", cur.Key)
		}
		if cur.ID == anchor.ID {
			return apperr.Invalid("rank.anchor", "An issue cannot be ranked relative to itself.")
		}
		if !moving[cur.ID] {
			moving[cur.ID] = true
			issues = append(issues, cur)
		}
	}
	// Neighbours of the anchor, skipping the issues that move.
	ids := make([]int64, 0, len(moving))
	for id := range moving {
		ids = append(ids, id)
	}
	var lo, hi string
	if before != "" {
		hi = anchor.Rank
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(rank), '') FROM issues WHERE rank < $1 AND NOT (id = ANY($2))`, anchor.Rank, ids).Scan(&lo); err != nil {
			return err
		}
	} else {
		lo = anchor.Rank
		if err := tx.QueryRow(ctx, `SELECT coalesce(min(rank), '') FROM issues WHERE rank > $1 AND NOT (id = ANY($2))`, anchor.Rank, ids).Scan(&hi); err != nil {
			return err
		}
	}
	for _, cur := range issues {
		var r string
		switch {
		case hi == "":
			r = rank.After(lo)
		case lo == "":
			r = rank.Before(hi)
		default:
			if r, err = rank.Between(lo, hi); err != nil {
				return apperr.Internal(err)
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE issues SET rank = $2 WHERE id = $1", cur.ID, r); err != nil {
			return err
		}
		lo = r
	}
	projects := map[int64]bool{}
	for _, cur := range issues {
		if !projects[cur.projectID] {
			projects[cur.projectID] = true
			if err := s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.ranked", IssueID: cur.ID, Key: cur.Key, ProjectID: cur.projectID}); err != nil {
				return err
			}
		}
	}
	return nil
}
