// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
)

// FieldSprint is the sprint of an issue: its open sprint, plus the closed sprints it went through.
const FieldSprint = "sprint"

// Sprint states.
const (
	SprintFuture = "future"
	SprintActive = "active"
	SprintClosed = "closed"
)

// Sprint is an iteration of a Scrum board.
type Sprint struct {
	ID        int64      `json:"id"`
	BoardID   int64      `json:"boardId,omitempty"`
	Name      string     `json:"name"`
	Goal      string     `json:"goal"`
	State     string     `json:"state" enum:"future,active,closed"`
	Start     *time.Time `json:"start,omitempty"`
	End       *time.Time `json:"end,omitempty"`
	Completed *time.Time `json:"completed,omitempty"`
	Position  int        `json:"position"`

	projectID int64
}

// SprintRef is the short form of a sprint.
type SprintRef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state" enum:"future,active,closed"`
}

// SprintInput creates or changes a sprint.
type SprintInput struct {
	Name  string     `json:"name,omitempty" maxLength:"100"`
	Goal  string     `json:"goal,omitempty" maxLength:"2000"`
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

// SprintEvent announces sprint changes to boards.
type SprintEvent struct {
	Name      string
	SprintID  int64
	BoardID   int64
	ProjectID int64
}

// EventName implements events.Event.
func (e SprintEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live.
func (e SprintEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "sprint", ID: itoa(e.SprintID), Container: "project:" + itoa(e.ProjectID)}
}

const sprintColumns = `s.id, coalesce(s.board_id, 0), s.name, s.goal, s.state, s.start_at, s.end_at, s.completed_at, s.position,
	coalesce(b.project_id, 0)`

func scanSprint(row pgx.Row) (Sprint, error) {
	var sp Sprint
	err := row.Scan(&sp.ID, &sp.BoardID, &sp.Name, &sp.Goal, &sp.State, &sp.Start, &sp.End, &sp.Completed, &sp.Position, &sp.projectID)
	return sp, err
}

func sprintByID(ctx context.Context, q db.Querier, id int64, forUpdate bool) (*Sprint, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF s"
	}
	sp, err := scanSprint(q.QueryRow(ctx, "SELECT "+sprintColumns+" FROM sprints s LEFT JOIN boards b ON b.id = s.board_id WHERE s.id = $1"+lock, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("sprint.not-found", "The sprint does not exist.")
	}
	return &sp, err
}

func listSprints(ctx context.Context, q db.Querier, where string, args ...any) ([]Sprint, error) {
	rows, err := q.Query(ctx, "SELECT "+sprintColumns+" FROM sprints s LEFT JOIN boards b ON b.id = s.board_id WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Sprint, error) { return scanSprint(row) })
	if out == nil {
		out = []Sprint{}
	}
	return out, err
}

// canManageSprints reports whether the principal manages the sprints of a project.
func (s *Service) canManageSprints(ctx context.Context, p *identity.Principal, projectID int64) (bool, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return false, err
	}
	return projectID != 0 && a.Can(projectID, PermManageSprints), nil
}

// requireSprintManagement checks that the principal manages the sprints of a project. Who cannot see the project
// gets notFound: for them, the board or sprint does not exist.
func (s *Service) requireSprintManagement(ctx context.Context, p *identity.Principal, projectID int64, notFound error) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	switch {
	case projectID != 0 && a.Can(projectID, PermManageSprints):
		return nil
	case projectID == 0 || !a.CanAny(projectID, PermBrowse):
		return notFound
	}
	return apperr.Forbidden("sprint.denied", "You may not manage the sprints of this board.")
}

func (s *Service) requireSprintManager(ctx context.Context, p *identity.Principal, sp *Sprint) error {
	return s.requireSprintManagement(ctx, p, sp.projectID, apperr.NotFound("sprint.not-found", "The sprint does not exist."))
}

// openSprintOf returns the open sprint of an issue (locked), if any.
func openSprintOf(ctx context.Context, tx db.Querier, issueID int64) (*SprintRef, error) {
	var r SprintRef
	err := tx.QueryRow(ctx, `SELECT s.id, s.name, s.state FROM issue_sprints x JOIN sprints s ON s.id = x.sprint_id
		WHERE x.issue_id = $1 AND s.state <> 'closed' LIMIT 1`, issueID).Scan(&r.ID, &r.Name, &r.State)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &r, err
}

// moveToSprint puts an issue into an open sprint (nil = backlog), records the change and bumps the issue.
func moveToSprint(ctx context.Context, tx pgx.Tx, issueID int64, to *Sprint, actorID int64) (bool, error) {
	cur, err := openSprintOf(ctx, tx, issueID)
	if err != nil {
		return false, err
	}
	if (cur == nil && to == nil) || (cur != nil && to != nil && cur.ID == to.ID) {
		return false, nil
	}
	item := ChangeItem{Field: FieldSprint}
	if cur != nil {
		if _, err := tx.Exec(ctx, "DELETE FROM issue_sprints WHERE issue_id = $1 AND sprint_id = $2", issueID, cur.ID); err != nil {
			return false, err
		}
		item.From, item.FromString = cur.ID, cur.Name
	}
	if to != nil {
		if _, err := tx.Exec(ctx, "INSERT INTO issue_sprints (issue_id, sprint_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", issueID, to.ID); err != nil {
			return false, err
		}
		item.To, item.ToString = to.ID, to.Name
	}
	b, _ := json.Marshal([]ChangeItem{item})
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	if _, err := tx.Exec(ctx, "INSERT INTO issue_changes (issue_id, actor_id, items) VALUES ($1, $2, $3)", issueID, actor, b); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, "UPDATE issues SET updated_at = now(), version = version + 1 WHERE id = $1", issueID)
	return true, err
}

// followParentSprint puts a new sub-task into the open sprint of its parent.
func followParentSprint(ctx context.Context, tx pgx.Tx, issueID, actorID int64) error {
	var sprintID int64
	err := tx.QueryRow(ctx, `SELECT x.sprint_id FROM issues i JOIN issue_sprints x ON x.issue_id = i.parent_id
		JOIN sprints s ON s.id = x.sprint_id AND s.state <> 'closed' WHERE i.id = $1 LIMIT 1`, issueID).Scan(&sprintID)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sp, err := sprintByID(ctx, tx, sprintID, false)
	if err != nil {
		return err
	}
	_, err = moveToSprint(ctx, tx, issueID, sp, actorID)
	return err
}

// issueSprints loads the sprint field of an issue: the open sprint and the closed ones.
func issueSprints(ctx context.Context, q db.Querier, issueID int64) (*SprintRef, []SprintRef, error) {
	rows, err := q.Query(ctx, `SELECT s.id, s.name, s.state FROM issue_sprints x JOIN sprints s ON s.id = x.sprint_id
		WHERE x.issue_id = $1 ORDER BY s.completed_at NULLS LAST, s.id`, issueID)
	if err != nil {
		return nil, nil, err
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SprintRef])
	if err != nil {
		return nil, nil, err
	}
	var open *SprintRef
	closed := []SprintRef{}
	for _, r := range refs {
		if r.State == SprintClosed {
			closed = append(closed, r)
		} else {
			open = &r
		}
	}
	return open, closed, nil
}

// --- sprint lifecycle ---

// CreateSprint adds a future sprint to a Scrum board.
func (s *Service) CreateSprint(ctx context.Context, p *identity.Principal, boardID int64, in SprintInput) (*Sprint, error) {
	b, err := s.boardRow(ctx, s.pool, boardID)
	if err != nil {
		return nil, err
	}
	if err := s.requireSprintManagement(ctx, p, b.projectID, apperr.NotFound("board.not-found", "The board does not exist.")); err != nil {
		return nil, err
	}
	if b.Type != BoardScrum {
		return nil, apperr.Invalid("sprint.kanban", "Kanban boards have no sprints.")
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM boards WHERE id = $1 FOR UPDATE", boardID); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM sprints WHERE board_id = $1", boardID).Scan(&n); err != nil {
				return err
			}
			name = b.Project.Key + " Sprint " + strconv.Itoa(n+1)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO sprints (board_id, name, goal, start_at, end_at, position)
			VALUES ($1, $2, $3, $4, $5, coalesce((SELECT max(position) + 1 FROM sprints WHERE board_id = $1), 1)) RETURNING id`,
			boardID, name, in.Goal, in.Start, in.End).Scan(&id); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.created", SprintID: id, BoardID: boardID, ProjectID: b.projectID})
	})
	if err != nil {
		return nil, err
	}
	return sprintByID(ctx, s.pool, id, false)
}

// GetSprint returns a sprint of a board the principal can see.
func (s *Service) GetSprint(ctx context.Context, p *identity.Principal, id int64) (*Sprint, error) {
	sp, err := sprintByID(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if sp.projectID == 0 || !a.CanAny(sp.projectID, PermBrowse) {
		return nil, apperr.NotFound("sprint.not-found", "The sprint does not exist.")
	}
	return sp, nil
}

// ProjectSprints lists the sprints of the boards of a project.
func (s *Service) ProjectSprints(ctx context.Context, p *identity.Principal, key string, open bool) ([]Sprint, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	states := []string{SprintFuture, SprintActive, SprintClosed}
	if open {
		states = states[:2]
	}
	return listSprints(ctx, s.pool, `b.project_id = $1 AND s.state = ANY($2)
		ORDER BY s.state = 'closed', s.state = 'future', s.position, s.completed_at DESC, s.id`, proj.ID, states)
}

// UpdateSprint changes name, goal and dates of a sprint that is not closed.
func (s *Service) UpdateSprint(ctx context.Context, p *identity.Principal, id int64, in SprintInput) (*Sprint, error) {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		sp, err := sprintByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := s.requireSprintManager(ctx, p, sp); err != nil {
			return err
		}
		if sp.State == SprintClosed {
			return apperr.Conflict("sprint.closed", "A completed sprint cannot change.")
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = sp.Name
		}
		if sp.State == SprintActive && (in.Start == nil || in.End == nil) {
			return apperr.Invalid("sprint.dates", "An active sprint needs start and end dates.")
		}
		if in.Start != nil && in.End != nil && !in.End.After(*in.Start) {
			return apperr.Invalid("sprint.dates", "The end must be after the start.")
		}
		if _, err := tx.Exec(ctx, "UPDATE sprints SET name = $2, goal = $3, start_at = $4, end_at = $5 WHERE id = $1",
			id, name, in.Goal, in.Start, in.End); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.updated", SprintID: id, BoardID: sp.BoardID, ProjectID: sp.projectID})
	})
	if err != nil {
		return nil, err
	}
	return sprintByID(ctx, s.pool, id, false)
}

// StartSprint makes a future sprint active and records its scope as the baseline.
func (s *Service) StartSprint(ctx context.Context, p *identity.Principal, id int64, in SprintInput) (*Sprint, error) {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		sp, err := sprintByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := s.requireSprintManager(ctx, p, sp); err != nil {
			return err
		}
		if sp.State != SprintFuture {
			return apperr.Conflict("sprint.state", "Only a future sprint can start.")
		}
		b, err := s.boardRow(ctx, tx, sp.BoardID)
		if err != nil {
			return err
		}
		if !b.Config.ParallelSprints {
			var active bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM sprints WHERE board_id = $1 AND state = 'active')", sp.BoardID).Scan(&active); err != nil {
				return err
			}
			if active {
				return apperr.Conflict("sprint.active-exists", "The board already has an active sprint. Complete it first or allow parallel sprints.")
			}
		}
		start := now()
		if in.Start != nil {
			start = *in.Start
		}
		end := start.Add(14 * 24 * time.Hour)
		if in.End != nil {
			end = *in.End
		} else if sp.End != nil && sp.End.After(start) {
			end = *sp.End
		}
		if !end.After(start) {
			return apperr.Invalid("sprint.dates", "The end must be after the start.")
		}
		name, goal := sp.Name, sp.Goal
		if n := strings.TrimSpace(in.Name); n != "" {
			name = n
		}
		if in.Goal != "" {
			goal = in.Goal
		}
		if _, err := tx.Exec(ctx, "UPDATE sprints SET state = 'active', name = $2, goal = $3, start_at = $4, end_at = $5 WHERE id = $1",
			id, name, goal, start, end); err != nil {
			return err
		}
		est, err := s.estimator(ctx, b)
		if err != nil {
			return err
		}
		done := b.doneStatuses()
		rows, err := tx.Query(ctx, `SELECT i.id, i.status_id, i.original_estimate, i.fields FROM issue_sprints x
			JOIN issues i ON i.id = x.issue_id WHERE x.sprint_id = $1`, id)
		if err != nil {
			return err
		}
		type base struct {
			issue int64
			est   *float64
			done  bool
		}
		var bases []base
		for rows.Next() {
			var iid, status int64
			var orig *int64
			var fields []byte
			if err := rows.Scan(&iid, &status, &orig, &fields); err != nil {
				rows.Close()
				return err
			}
			bases = append(bases, base{iid, est.value(orig, fields), done[status]})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, bs := range bases {
			if _, err := tx.Exec(ctx, `INSERT INTO sprint_baselines (sprint_id, issue_id, estimate, done) VALUES ($1, $2, $3, $4)
				ON CONFLICT (sprint_id, issue_id) DO UPDATE SET estimate = EXCLUDED.estimate, done = EXCLUDED.done`, id, bs.issue, bs.est, bs.done); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "sprint.start", TargetType: "sprint", TargetID: itoa(id),
			Details: map[string]any{"name": name, "issues": len(bases)}}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.started", SprintID: id, BoardID: sp.BoardID, ProjectID: sp.projectID})
	})
	if err != nil {
		return nil, err
	}
	return sprintByID(ctx, s.pool, id, false)
}

// CompleteSprintInput says where unfinished issues go.
type CompleteSprintInput struct {
	MoveTo   string `json:"moveTo" enum:"backlog,sprint,new" doc:"Where unfinished issues go"`
	SprintID int64  `json:"sprintId,omitempty" doc:"Target future sprint when moveTo is sprint"`
}

// CompleteSprintResult summarises a completed sprint.
type CompleteSprintResult struct {
	Sprint    Sprint  `json:"sprint"`
	Completed int     `json:"completed"`
	Moved     int     `json:"moved"`
	MovedTo   *Sprint `json:"movedTo,omitempty"`
}

// CompleteSprint closes an active sprint; unfinished issues move to the backlog or another sprint and keep the
// closed sprint in their sprint history.
func (s *Service) CompleteSprint(ctx context.Context, p *identity.Principal, id int64, in CompleteSprintInput) (*CompleteSprintResult, error) {
	res := &CompleteSprintResult{}
	var events []int64
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		sp, err := sprintByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := s.requireSprintManager(ctx, p, sp); err != nil {
			return err
		}
		if sp.State != SprintActive {
			return apperr.Conflict("sprint.state", "Only an active sprint can be completed.")
		}
		b, err := s.boardRow(ctx, tx, sp.BoardID)
		if err != nil {
			return err
		}
		var target *Sprint
		switch in.MoveTo {
		case "sprint":
			if target, err = sprintByID(ctx, tx, in.SprintID, true); err != nil {
				return err
			}
			if target.State != SprintFuture || target.BoardID != sp.BoardID {
				return apperr.Invalid("sprint.target", "Unfinished issues can only move to a future sprint of the same board.")
			}
		case "new":
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM sprints WHERE board_id = $1", sp.BoardID).Scan(&n); err != nil {
				return err
			}
			var nid int64
			if err := tx.QueryRow(ctx, `INSERT INTO sprints (board_id, name, position) VALUES ($1, $2,
				coalesce((SELECT max(position) + 1 FROM sprints WHERE board_id = $1), 1)) RETURNING id`,
				sp.BoardID, b.Project.Key+" Sprint "+strconv.Itoa(n+1)).Scan(&nid); err != nil {
				return err
			}
			if target, err = sprintByID(ctx, tx, nid, false); err != nil {
				return err
			}
		}
		done := b.doneStatuses()
		rows, err := tx.Query(ctx, `SELECT i.id, i.status_id FROM issue_sprints x JOIN issues i ON i.id = x.issue_id
			WHERE x.sprint_id = $1 ORDER BY i.rank`, id)
		if err != nil {
			return err
		}
		var unfinished []int64
		for rows.Next() {
			var iid, status int64
			if err := rows.Scan(&iid, &status); err != nil {
				rows.Close()
				return err
			}
			if done[status] {
				res.Completed++
			} else {
				unfinished = append(unfinished, iid)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sprints SET state = 'closed', completed_at = now() WHERE id = $1", id); err != nil {
			return err
		}
		for _, iid := range unfinished {
			// The closed sprint stays in the issue's sprint history.
			if target != nil {
				if _, err := tx.Exec(ctx, "INSERT INTO issue_sprints (issue_id, sprint_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", iid, target.ID); err != nil {
					return err
				}
			}
			item := ChangeItem{Field: FieldSprint, From: id, FromString: sp.Name}
			if target != nil {
				item.To, item.ToString = target.ID, target.Name
			}
			b, _ := json.Marshal([]ChangeItem{item})
			if _, err := tx.Exec(ctx, "INSERT INTO issue_changes (issue_id, actor_id, items) VALUES ($1, $2, $3)", iid, p.UserID(), b); err != nil {
				return err
			}
			events = append(events, iid)
		}
		res.Moved = len(unfinished)
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "sprint.complete", TargetType: "sprint", TargetID: itoa(id),
			Details: map[string]any{"name": sp.Name, "completed": res.Completed, "moved": res.Moved}}); err != nil {
			return err
		}
		if target != nil {
			res.MovedTo = target
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.completed", SprintID: id, BoardID: sp.BoardID, ProjectID: sp.projectID})
	})
	if err != nil {
		return nil, err
	}
	sp, err := sprintByID(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	res.Sprint = *sp
	_ = events
	return res, nil
}

// DeleteSprint removes a future or active sprint; its issues return to the backlog.
func (s *Service) DeleteSprint(ctx context.Context, p *identity.Principal, id int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		sp, err := sprintByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := s.requireSprintManager(ctx, p, sp); err != nil {
			return err
		}
		if sp.State == SprintClosed {
			return apperr.Conflict("sprint.closed", "A completed sprint cannot be deleted.")
		}
		rows, err := tx.Query(ctx, "SELECT issue_id FROM issue_sprints WHERE sprint_id = $1", id)
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
		if _, err := tx.Exec(ctx, "DELETE FROM sprints WHERE id = $1", id); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "sprint.delete", TargetType: "sprint", TargetID: itoa(id),
			Details: map[string]any{"name": sp.Name}}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.deleted", SprintID: id, BoardID: sp.BoardID, ProjectID: sp.projectID})
	})
}

// OrderSprints sets the order of the future sprints of a board.
func (s *Service) OrderSprints(ctx context.Context, p *identity.Principal, boardID int64, ids []int64) error {
	b, err := s.boardRow(ctx, s.pool, boardID)
	if err != nil {
		return err
	}
	if err := s.requireSprintManagement(ctx, p, b.projectID, apperr.NotFound("board.not-found", "The board does not exist.")); err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sprints t SET position = o.pos FROM (
			SELECT id, row_number() OVER (ORDER BY array_position($2::bigint[], id) NULLS LAST, position, id) AS pos
			FROM sprints WHERE board_id = $1 AND state = 'future') o WHERE t.id = o.id`, boardID, ids); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, SprintEvent{Name: "sprint.ordered", BoardID: boardID, ProjectID: b.projectID})
	})
}

// MoveIssuesInput moves issues into a sprint or the backlog and optionally ranks them.
type MoveIssuesInput struct {
	Issues []string `json:"issues" minItems:"1" maxItems:"500" doc:"Issue keys"`
	Before string   `json:"before,omitempty" doc:"Rank the issues before this issue"`
	After  string   `json:"after,omitempty" doc:"Rank the issues after this issue"`
}

// MoveToSprint moves issues into an open sprint (sprintID 0 = backlog). Sub-tasks follow their parents.
func (s *Service) MoveToSprint(ctx context.Context, p *identity.Principal, sprintID int64, in MoveIssuesInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	var published []IssueEvent
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var target *Sprint
		if sprintID != 0 {
			if target, err = sprintByID(ctx, tx, sprintID, false); err != nil {
				return err
			}
			if target.State == SprintClosed {
				return apperr.Conflict("sprint.closed", "Issues cannot be added to a completed sprint.")
			}
			if !a.CanAny(target.projectID, PermBrowse) {
				return apperr.NotFound("sprint.not-found", "The sprint does not exist.")
			}
			// Taking issues into a sprint changes it: scheduling in its project too (audit L16).
			if !a.Can(target.projectID, PermSchedule) {
				return apperr.Forbidden("sprint.schedule-denied", "You may not plan issues into the sprints of this board.")
			}
		}
		for _, key := range in.Issues {
			cur, _, err := s.issueFor(ctx, tx, a, key, true)
			if err != nil {
				return err
			}
			if !a.CanIssue(cur.head(), PermSchedule) {
				return apperr.Forbidden("issue.schedule-denied", "You may not schedule %s.", cur.Key)
			}
			// So does taking an issue out of a sprint of another project's board.
			var from int64
			err = tx.QueryRow(ctx, `SELECT b.project_id FROM issue_sprints x JOIN sprints sp ON sp.id = x.sprint_id
				JOIN boards b ON b.id = sp.board_id WHERE x.issue_id = $1 AND sp.state <> 'closed' LIMIT 1`, cur.ID).Scan(&from)
			if err != nil && !db.IsNoRows(err) {
				return err
			}
			if from != 0 && !a.Can(from, PermSchedule) {
				return apperr.Forbidden("sprint.schedule-denied", "You may not take %s out of its sprint.", cur.Key)
			}
			if cur.parentID != 0 {
				var level int
				if err := tx.QueryRow(ctx, "SELECT t.level FROM issues i JOIN issue_types t ON t.id = i.type_id WHERE i.id = $1", cur.ID).Scan(&level); err != nil {
					return err
				}
				if level == -1 {
					return apperr.Invalid("sprint.subtask", "Sub-tasks follow their parent into sprints.")
				}
			}
			ids := []int64{cur.ID}
			rows, err := tx.Query(ctx, "SELECT id FROM issues WHERE parent_id = $1 AND type_id IN (SELECT id FROM issue_types WHERE level = -1)", cur.ID)
			if err != nil {
				return err
			}
			subs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return err
			}
			ids = append(ids, subs...)
			for _, iid := range ids {
				changed, err := moveToSprint(ctx, tx, iid, target, p.UserID())
				if err != nil {
					return err
				}
				if changed && iid == cur.ID {
					published = append(published, IssueEvent{Name: "issue.updated", IssueID: cur.ID, Key: cur.Key, ProjectID: cur.projectID,
						ActorID: p.UserID(), Fields: []string{FieldSprint}})
				}
			}
		}
		if in.Before != "" || in.After != "" {
			if err := s.rankIssues(ctx, tx, a, in.Issues, in.Before, in.After); err != nil {
				return err
			}
		}
		for _, e := range published {
			if err := s.bus.Publish(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// --- KQL ---

// sprintIDs resolves sprint names or ids and the functions openSprints(), closedSprints() and futureSprints().
func (c *compiler) sprintIDs(vals []kql.Value, funcs []kql.Func) ([]int64, error) {
	var ids []int64
	// Only the sprints of boards of projects the reader sees have names for them (audit L18).
	visible := nonNil(c.a.BrowsableProjects())
	for _, v := range vals {
		if n, err := strconv.ParseInt(v.Text, 10, 64); err == nil {
			ids = append(ids, n)
			continue
		}
		rows, err := c.q.Query(c.ctx, `SELECT s.id FROM sprints s JOIN boards b ON b.id = s.board_id
			WHERE lower(s.name) = lower($1) AND b.project_id = ANY($2)`, v.Text, visible)
		if err != nil {
			return nil, err
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			return nil, queryError(v.Pos, "There is no sprint named %q.", v.Text)
		}
		ids = append(ids, found...)
	}
	for _, f := range funcs {
		var states []string
		switch strings.ToLower(f.Name) {
		case "opensprints":
			states = []string{SprintActive, SprintFuture}
		case "closedsprints":
			states = []string{SprintClosed}
		case "futuresprints":
			states = []string{SprintFuture}
		default:
			return nil, queryError(f.Pos, "The function %s() cannot be used with sprint.", f.Name)
		}
		rows, err := c.q.Query(c.ctx, `SELECT s.id FROM sprints s JOIN boards b ON b.id = s.board_id
			WHERE s.state = ANY($1) AND b.project_id = ANY($2)`, states, visible)
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
