// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// maxCards limits the cards of a board or backlog.
const maxCards = 2000

// EpicRef is the epic of a card.
type EpicRef struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// CardField is an extra field shown on a card.
type CardField struct {
	Field string `json:"field"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Card is an issue on a board or in a backlog.
type Card struct {
	ID        int64             `json:"id"`
	Key       string            `json:"key"`
	Summary   string            `json:"summary"`
	Type      *IssueType        `json:"type"`
	Status    *Status           `json:"status"`
	Priority  *Priority         `json:"priority,omitempty"`
	Assignee  *identity.UserRef `json:"assignee,omitempty"`
	ParentKey string            `json:"parentKey,omitempty"`
	Epic      *EpicRef          `json:"epic,omitempty"`
	Labels    []string          `json:"labels"`
	Estimate  *float64          `json:"estimate,omitempty" doc:"The board's estimation statistic"`
	Extra     []CardField       `json:"extra"`
	Subtasks  int               `json:"subtasks"`
	SubsDone  int               `json:"subtasksDone"`
	SprintID  int64             `json:"sprintId,omitempty"`
	Column    int               `json:"column" doc:"Index of the board column, -1 if the status is not mapped"`
	Lane      int               `json:"lane" doc:"Index of the swimlane query that matches first, -1 for none"`
	Color     string            `json:"color,omitempty" doc:"Colour of the first matching colour query"`
	Done      bool              `json:"done" doc:"In the last column"`
	Rank      string            `json:"rank"`
	Updated   time.Time         `json:"updated"`
}

// BoardView is the content of a board: the cards of its columns.
type BoardView struct {
	Board     Board    `json:"board"`
	Sprints   []Sprint `json:"sprints" doc:"Active sprints (Scrum)"`
	Cards     []Card   `json:"cards"`
	Truncated bool     `json:"truncated"`
}

// SprintIssues is a sprint with its cards in the backlog.
type SprintIssues struct {
	Sprint Sprint `json:"sprint"`
	Cards  []Card `json:"cards"`
}

// EpicSummary is an epic with the progress of its issues.
type EpicSummary struct {
	ID           int64   `json:"id"`
	Key          string  `json:"key"`
	Summary      string  `json:"summary"`
	Status       *Status `json:"status"`
	Issues       int     `json:"issues"`
	IssuesDone   int     `json:"issuesDone"`
	Estimate     float64 `json:"estimate"`
	EstimateDone float64 `json:"estimateDone"`
}

// BacklogView is the planning view of a Scrum board.
type BacklogView struct {
	Board     Board          `json:"board"`
	Sprints   []SprintIssues `json:"sprints" doc:"Active and future sprints"`
	Backlog   []Card         `json:"backlog"`
	Epics     []EpicSummary  `json:"epics"`
	Versions  []Version      `json:"versions" doc:"Unreleased versions of the board's project"`
	Truncated bool           `json:"truncated"`
}

// compileCondition compiles the condition of a query (its ORDER BY is ignored) without the visibility predicate.
func (s *Service) compileCondition(ctx context.Context, q db.Querier, p *identity.Principal, a *Access, query, alias string, args *db.Args) (string, error) {
	parsed, err := kql.Parse(query)
	if err != nil {
		if pe, ok := err.(*kql.Error); ok {
			return "", queryError(pe.Pos, pe.Format, pe.Args...)
		}
		return "", err
	}
	c, err := s.newCompiler(ctx, q, p, a, args, alias)
	if err != nil {
		return "", err
	}
	where, _, err := c.compile(parsed)
	return where, err
}

// boardWhere is the condition selecting the board's issues with the chosen quick filters.
func (s *Service) boardWhere(ctx context.Context, q db.Querier, p *identity.Principal, b *Board, quick []int, args *db.Args) (string, error) {
	where, _, err := s.CompileQuery(ctx, q, p, b.Query, "i", args)
	if err != nil {
		return "", err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", err
	}
	parts := []string{where}
	if b.Type == BoardKanban && strings.TrimSpace(b.Config.SubQuery) != "" {
		w, err := s.compileCondition(ctx, q, p, a, b.Config.SubQuery, "i", args)
		if err != nil {
			return "", err
		}
		parts = append(parts, "("+w+")")
	}
	for _, id := range quick {
		found := false
		for _, f := range b.Config.QuickFilters {
			if f.ID == id {
				w, err := s.compileCondition(ctx, q, p, a, f.Query, "i", args)
				if err != nil {
					return "", err
				}
				parts = append(parts, "("+w+")")
				found = true
			}
		}
		if !found {
			return "", apperr.Invalid("board.quick-filter", "The quick filter does not exist.")
		}
	}
	return strings.Join(parts, " AND "), nil
}

// cards loads the cards matching a condition, ordered by rank.
func (s *Service) cards(ctx context.Context, q db.Querier, p *identity.Principal, b *Board, cfg *configSnapshot, where string, args *db.Args) ([]Card, bool, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, false, err
	}
	// Swimlane and colour queries become boolean columns.
	var extraCols []string
	lanes := []NamedQuery{}
	if b.Config.Swimlanes.Type == "queries" {
		lanes = b.Config.Swimlanes.Queries
	}
	colors := []NamedQuery{}
	if b.Config.Card.Colors == "queries" {
		colors = b.Config.Card.ColorQueries
	}
	for _, nq := range append(append([]NamedQuery{}, lanes...), colors...) {
		w, err := s.compileCondition(ctx, q, p, a, nq.Query, "i", args)
		if err != nil {
			return nil, false, err
		}
		extraCols = append(extraCols, "coalesce(("+w+"), false)")
	}
	extra := ""
	if len(extraCols) > 0 {
		extra = ", " + strings.Join(extraCols, ", ")
	}
	wantComponents, wantVersions := false, false
	for _, f := range b.Config.Card.Fields {
		wantComponents = wantComponents || f == FieldComponents
		wantVersions = wantVersions || f == FieldFixVersions
	}
	sub := ", '{}'::text[], '{}'::text[]"
	if wantComponents || wantVersions {
		sub = `, array(SELECT c.name FROM issue_components ic JOIN components c ON c.id = ic.component_id WHERE ic.issue_id = i.id ORDER BY lower(c.name)),
			array(SELECT v.name FROM issue_versions iv JOIN versions v ON v.id = iv.version_id WHERE iv.issue_id = i.id AND iv.kind = 'fix' ORDER BY v.position)`
	}
	// Parents, epics and sub-tasks the reader may not see are neither named nor counted.
	epicVisible, parentVisible := a.VisibleSQL("ei", args), a.VisibleSQL("pi", args)
	subsVisible, doneVisible := a.VisibleSQL("s", args), a.VisibleSQL("s", args)
	lim := args.Add(maxCards + 1)
	rows, err := q.Query(ctx, `SELECT i.id, i.key, i.summary, i.type_id, i.status_id, i.priority_id, i.assignee_id, au.username,
		au.display_name, CASE WHEN `+parentVisible+` THEN pi.key END, ei.id, ei.key, ei.summary, `+epicVisible+`, i.labels, i.fields,
		i.original_estimate, i.remaining_estimate, i.time_spent, to_char(i.due_date, 'YYYY-MM-DD'), i.reporter_id, i.created_at,
		i.updated_at, i.rank,
		(SELECT count(*) FROM issues s WHERE s.parent_id = i.id AND s.type_id IN (SELECT id FROM issue_types WHERE level = -1)
			AND `+subsVisible+`),
		(SELECT count(*) FROM issues s JOIN statuses st ON st.id = s.status_id WHERE s.parent_id = i.id AND st.category = 'done'
			AND s.type_id IN (SELECT id FROM issue_types WHERE level = -1) AND `+doneVisible+`),
		(SELECT x.sprint_id FROM issue_sprints x JOIN sprints sp ON sp.id = x.sprint_id WHERE x.issue_id = i.id AND sp.state <> 'closed' LIMIT 1)`+
		sub+extra+`
		FROM issues i LEFT JOIN users au ON au.id = i.assignee_id
		LEFT JOIN issues pi ON pi.id = i.parent_id LEFT JOIN issue_types pt ON pt.id = pi.type_id
		LEFT JOIN issues ei ON ei.id = CASE WHEN pt.level = 1 THEN pi.id ELSE pi.parent_id END
		WHERE `+where+" ORDER BY i.rank, i.id LIMIT "+lim, *args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	est, _ := s.estimator(ctx, b)
	done := b.doneStatuses()
	type pending struct {
		card       Card
		fields     []byte
		orig, rem  *int64
		spent      int64
		due        *string
		reporter   *int64
		created    time.Time
		components []string
		versions   []string
	}
	var list []pending
	userIDs := map[int64]bool{}
	for rows.Next() {
		var c Card
		var typ, status int64
		var prio, aid, eid, reporter, sprint *int64
		var au, an, parentKey, ekey, esum, due *string
		var evisible *bool
		var fields []byte
		var orig, rem *int64
		var spent int64
		var created time.Time
		var comps, vers []string
		flags := make([]bool, len(extraCols))
		dest := []any{&c.ID, &c.Key, &c.Summary, &typ, &status, &prio, &aid, &au, &an, &parentKey, &eid, &ekey, &esum, &evisible,
			&c.Labels, &fields, &orig, &rem, &spent, &due, &reporter, &created, &c.Updated, &c.Rank, &c.Subtasks, &c.SubsDone, &sprint,
			&comps, &vers}
		for i := range flags {
			dest = append(dest, &flags[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		c.Type, c.Status = cfg.types[typ], cfg.statuses[status]
		if prio != nil {
			c.Priority = cfg.priorities[*prio]
		}
		if aid != nil {
			c.Assignee = &identity.UserRef{ID: *aid, Username: *au, DisplayName: *an, Active: true}
		}
		if parentKey != nil {
			c.ParentKey = *parentKey
		}
		if eid != nil && evisible != nil && *evisible {
			c.Epic = &EpicRef{ID: *eid, Key: *ekey, Summary: *esum}
		}
		if c.Labels == nil {
			c.Labels = []string{}
		}
		if sprint != nil {
			c.SprintID = *sprint
		}
		c.Estimate = est.value(orig, fields)
		c.Column = b.columnOf(status)
		c.Done = done[status]
		c.Lane = -1
		for i := range lanes {
			if flags[i] {
				c.Lane = i
				break
			}
		}
		for i, cq := range colors {
			if flags[len(lanes)+i] {
				c.Color = cq.Color
				break
			}
		}
		if reporter != nil {
			userIDs[*reporter] = true
		}
		list = append(list, pending{card: c, fields: fields, orig: orig, rem: rem, spent: spent, due: due, reporter: reporter,
			created: created, components: comps, versions: vers})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(list) > maxCards
	if truncated {
		list = list[:maxCards]
	}
	// Extra fields.
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, false, err
	}
	var users map[int64]identity.UserRef
	var versions map[int64]string
	if len(b.Config.Card.Fields) > 0 {
		for _, pc := range list {
			var m map[string]any
			_ = json.Unmarshal(pc.fields, &m)
			for _, key := range b.Config.Card.Fields {
				if id, ok := parseFieldKey(key); ok {
					if f := cfg.fields[id]; f != nil && (f.Type == "user" || f.Type == "multiuser") {
						switch v := m[key].(type) {
						case float64:
							userIDs[int64(v)] = true
						case []any:
							for _, x := range v {
								if n, ok := x.(float64); ok {
									userIDs[int64(n)] = true
								}
							}
						}
					}
				}
			}
		}
		ids := make([]int64, 0, len(userIDs))
		for id := range userIDs {
			ids = append(ids, id)
		}
		if users, err = identity.GetUserRefs(ctx, q, ids); err != nil {
			return nil, false, err
		}
		vs, err := s.listVersions(ctx, q, b.projectID)
		if err != nil {
			return nil, false, err
		}
		versions = map[int64]string{}
		for _, v := range vs {
			versions[v.ID] = v.Name
		}
	}
	userName := func(id int64) string { return users[id].DisplayName }
	out := make([]Card, 0, len(list))
	for _, pc := range list {
		c := pc.card
		c.Extra = []CardField{}
		var m map[string]any
		_ = json.Unmarshal(pc.fields, &m)
		for _, key := range b.Config.Card.Fields {
			cf := CardField{Field: key}
			if id, ok := parseFieldKey(key); ok {
				f := cfg.fields[id]
				if f == nil {
					continue
				}
				cf.Name = f.Name
				cf.Value = displayCustom(f, m[key], userName, versions)
			} else {
				if sf, ok := systemField(key); ok {
					cf.Name = sf.Name
				}
				switch key {
				case FieldDueDate:
					if pc.due != nil {
						cf.Value = *pc.due
					}
				case FieldLabels:
					cf.Value = strings.Join(c.Labels, ", ")
				case FieldComponents:
					cf.Value = strings.Join(pc.components, ", ")
				case FieldFixVersions:
					cf.Value = strings.Join(pc.versions, ", ")
				case FieldReporter:
					if pc.reporter != nil {
						cf.Value = userName(*pc.reporter)
					}
				case FieldOriginalEstimate:
					cf.Value = durationString(pc.orig, st.HoursPerDay, st.DaysPerWeek)
				case FieldRemainingEstimate:
					cf.Value = durationString(pc.rem, st.HoursPerDay, st.DaysPerWeek)
				case "timeSpent":
					spent := pc.spent
					cf.Value = durationString(&spent, st.HoursPerDay, st.DaysPerWeek)
				case "created":
					cf.Value = pc.created.Format(time.RFC3339)
				case "updated":
					cf.Value = c.Updated.Format(time.RFC3339)
				default:
					continue
				}
			}
			c.Extra = append(c.Extra, cf)
		}
		out = append(out, c)
	}
	return out, truncated, nil
}

func (s *Service) activeSprints(ctx context.Context, q db.Querier, boardID int64) ([]Sprint, error) {
	return listSprints(ctx, q, "s.board_id = $1 AND s.state = 'active' ORDER BY s.start_at, s.id", boardID)
}

// BoardData returns the cards of a board: for Scrum boards those of the active sprints, for Kanban boards all
// matching issues except long finished ones.
func (s *Service) BoardData(ctx context.Context, p *identity.Principal, id int64, quick []int, sprintID int64) (*BoardView, error) {
	b, err := s.GetBoard(ctx, p, id)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	view := &BoardView{Board: *b, Sprints: []Sprint{}, Cards: []Card{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, err := s.boardWhere(ctx, tx, p, b, quick, &args)
		if err != nil {
			return err
		}
		where += " AND i.status_id = ANY(" + args.Add(nonNil(b.mappedStatuses())) + ")"
		if b.Type == BoardScrum {
			if view.Sprints, err = s.activeSprints(ctx, tx, b.ID); err != nil {
				return err
			}
			ids := []int64{}
			for _, sp := range view.Sprints {
				if sprintID == 0 || sp.ID == sprintID {
					ids = append(ids, sp.ID)
				}
			}
			if len(ids) == 0 {
				return nil
			}
			where += " AND EXISTS (SELECT 1 FROM issue_sprints x WHERE x.issue_id = i.id AND x.sprint_id = ANY(" + args.Add(ids) + "))"
		} else if b.Config.HideDoneAfterDays > 0 {
			done := []int64{}
			for st := range b.doneStatuses() {
				done = append(done, st)
			}
			where += " AND NOT (i.status_id = ANY(" + args.Add(done) + ") AND i.updated_at < now() - make_interval(days => " +
				args.Add(b.Config.HideDoneAfterDays) + "))"
		}
		view.Cards, view.Truncated, err = s.cards(ctx, tx, p, b, cfg, where, &args)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// Backlog returns the sprints and the backlog of a Scrum board.
func (s *Service) Backlog(ctx context.Context, p *identity.Principal, id int64, quick []int, epic string) (*BacklogView, error) {
	b, err := s.GetBoard(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if b.Type != BoardScrum {
		return nil, apperr.Invalid("board.kanban", "Kanban boards have no backlog with sprints.")
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	view := &BacklogView{Board: *b, Sprints: []SprintIssues{}, Backlog: []Card{}, Epics: []EpicSummary{}, Versions: []Version{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, err := s.boardWhere(ctx, tx, p, b, quick, &args)
		if err != nil {
			return err
		}
		// Sub-tasks follow their parents and epics have their own panel.
		where += " AND i.type_id IN (SELECT id FROM issue_types WHERE level = 0)"
		if epic != "" {
			if strings.EqualFold(epic, "none") {
				where += " AND (pi.id IS NULL OR pt.level <> 1)"
			} else {
				where += " AND ei.key = upper(" + args.Add(epic) + ")"
			}
		}
		sprints, err := listSprints(ctx, tx, "s.board_id = $1 AND s.state <> 'closed' ORDER BY s.state = 'future', s.position, s.id", b.ID)
		if err != nil {
			return err
		}
		all, truncated, err := s.cards(ctx, tx, p, b, cfg, where+` AND (i.status_id <> ALL(`+args.Add(nonNil(keys(b.doneStatuses())))+`)
			OR EXISTS (SELECT 1 FROM issue_sprints x JOIN sprints sp ON sp.id = x.sprint_id WHERE x.issue_id = i.id AND sp.state <> 'closed'))`, &args)
		if err != nil {
			return err
		}
		view.Truncated = truncated
		bySprint := map[int64][]Card{}
		for _, c := range all {
			if c.SprintID != 0 {
				bySprint[c.SprintID] = append(bySprint[c.SprintID], c)
			} else {
				view.Backlog = append(view.Backlog, c)
			}
		}
		for _, sp := range sprints {
			cards := bySprint[sp.ID]
			if cards == nil {
				cards = []Card{}
			}
			view.Sprints = append(view.Sprints, SprintIssues{Sprint: sp, Cards: cards})
		}
		// Epics of the board's project with their progress.
		var eargs db.Args
		visible := a.VisibleSQL("e", &eargs)
		est, _ := s.estimator(ctx, b)
		rows, err := tx.Query(ctx, `SELECT e.id, e.key, e.summary, e.status_id, c.id, c.status_id, c.original_estimate, c.fields
			FROM issues e JOIN issue_types t ON t.id = e.type_id AND t.level = 1
			LEFT JOIN issues c ON c.parent_id = e.id
			WHERE e.project_id = `+eargs.Add(b.projectID)+` AND e.resolution_id IS NULL AND `+visible+` ORDER BY e.rank, e.id`, eargs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		idx := map[int64]int{}
		done := b.doneStatuses()
		for rows.Next() {
			var eid, estatus int64
			var key, summary string
			var cid, cstatus, orig *int64
			var fields []byte
			if err := rows.Scan(&eid, &key, &summary, &estatus, &cid, &cstatus, &orig, &fields); err != nil {
				return err
			}
			i, ok := idx[eid]
			if !ok {
				i = len(view.Epics)
				idx[eid] = i
				view.Epics = append(view.Epics, EpicSummary{ID: eid, Key: key, Summary: summary, Status: cfg.statuses[estatus]})
			}
			if cid == nil {
				continue
			}
			e := &view.Epics[i]
			e.Issues++
			v := est.value(orig, fields)
			isDone := done[*cstatus] || (cfg.statuses[*cstatus] != nil && cfg.statuses[*cstatus].Category == CategoryDone)
			if isDone {
				e.IssuesDone++
			}
			if v != nil {
				e.Estimate += *v
				if isDone {
					e.EstimateDone += *v
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		vs, err := s.listVersions(ctx, tx, b.projectID)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if !v.Released && !v.Archived {
				view.Versions = append(view.Versions, v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

func keys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
