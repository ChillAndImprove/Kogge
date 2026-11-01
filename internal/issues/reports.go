// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Reports are computed from issue data and history: the state of an issue at a point in time is
// its current state with later changes undone.

// ReportIssue is an issue in a report.
type ReportIssue struct {
	Key      string     `json:"key"`
	Summary  string     `json:"summary"`
	Type     *IssueType `json:"type"`
	Status   *Status    `json:"status"`
	Estimate *float64   `json:"estimate,omitempty"`
	Added    bool       `json:"added" doc:"Added after the sprint started"`
}

// SprintReport lists what a sprint achieved.
type SprintReport struct {
	Sprint       Sprint        `json:"sprint"`
	Statistic    Estimation    `json:"statistic"`
	Committed    float64       `json:"committed" doc:"Estimate at the start"`
	Completed    []ReportIssue `json:"completed"`
	NotCompleted []ReportIssue `json:"notCompleted"`
	Removed      []ReportIssue `json:"removed"`
}

// BurndownPoint is a change of the remaining estimate.
type BurndownPoint struct {
	At        time.Time `json:"at"`
	Remaining float64   `json:"remaining"`
	Event     string    `json:"event" enum:"start,added,removed,estimate,completed,reopened,now,end"`
	Key       string    `json:"key,omitempty"`
	Delta     float64   `json:"delta"`
}

// Burndown is the remaining estimate of a sprint over time.
type Burndown struct {
	Sprint    Sprint          `json:"sprint"`
	Statistic Estimation      `json:"statistic"`
	Points    []BurndownPoint `json:"points"`
}

// VelocityEntry is the commitment and the result of a sprint.
type VelocityEntry struct {
	Sprint    SprintRef `json:"sprint"`
	Committed float64   `json:"committed"`
	Completed float64   `json:"completed"`
}

// Velocity compares the last sprints.
type Velocity struct {
	Statistic Estimation      `json:"statistic"`
	Sprints   []VelocityEntry `json:"sprints"`
}

// FlowDay is the number of issues per column at the end of a day.
type FlowDay struct {
	Date   string `json:"date" format:"date"`
	Counts []int  `json:"counts"`
}

// CumulativeFlow is the number of issues per column over time.
type CumulativeFlow struct {
	Columns   []string  `json:"columns"`
	Days      []FlowDay `json:"days"`
	Truncated bool      `json:"truncated" doc:"The board has more than 10,000 issues; the report counts the 10,000 oldest"`
}

// maxFlowIssues limits the issues the cumulative flow replays.
const maxFlowIssues = 10000

// CreatedResolvedPeriod counts issues created and resolved in a period.
type CreatedResolvedPeriod struct {
	Start    string `json:"start" format:"date"`
	Created  int    `json:"created"`
	Resolved int    `json:"resolved"`
}

// CreatedResolved compares created and resolved issues of a query.
type CreatedResolved struct {
	Periods []CreatedResolvedPeriod `json:"periods"`
}

// --- history replay ---

type histChange struct {
	at    time.Time
	field string
	from  any
	to    any
}

type replayIssue struct {
	id      int64
	key     string
	summary string
	typ     int64
	status  int64
	est     *float64
	member  bool // in the sprint
	changes []histChange
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

func asFloat(v any) *float64 {
	if n, ok := v.(float64); ok {
		return &n
	}
	return nil
}

// loadReplay loads issues with their current state and the history of status, sprint and estimate.
func (s *Service) loadReplay(ctx context.Context, q db.Querier, est estimator, ids []int64, sprintID int64) (map[int64]*replayIssue, error) {
	out := map[int64]*replayIssue{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT i.id, i.key, i.summary, i.type_id, i.status_id, i.original_estimate, i.fields,
		EXISTS (SELECT 1 FROM issue_sprints x WHERE x.issue_id = i.id AND x.sprint_id = $2) FROM issues i WHERE i.id = ANY($1)`, ids, sprintID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r replayIssue
		var orig *int64
		var fields []byte
		if err := rows.Scan(&r.id, &r.key, &r.summary, &r.typ, &r.status, &orig, &fields, &r.member); err != nil {
			rows.Close()
			return nil, err
		}
		r.est = est.value(orig, fields)
		out[r.id] = &r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, "SELECT issue_id, at, items FROM issue_changes WHERE issue_id = ANY($1) ORDER BY at, id", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var at time.Time
		var raw []byte
		if err := rows.Scan(&id, &at, &raw); err != nil {
			return nil, err
		}
		r := out[id]
		if r == nil {
			continue
		}
		var items []ChangeItem
		if json.Unmarshal(raw, &items) != nil {
			continue
		}
		for _, it := range items {
			if it.Field == FieldStatus || it.Field == FieldSprint || est.tracks(it.Field) {
				r.changes = append(r.changes, histChange{at: at, field: it.Field, from: it.From, to: it.To})
			}
		}
	}
	return out, rows.Err()
}

// stateAt returns the issue's state just before the changes after t (undoing them from the current state).
func (r *replayIssue) stateAt(t time.Time, est estimator, sprintID int64) replayIssue {
	st := *r
	for i := len(r.changes) - 1; i >= 0; i-- {
		c := r.changes[i]
		if !c.at.After(t) {
			break
		}
		st.undo(c, est, sprintID)
	}
	return st
}

func (st *replayIssue) undo(c histChange, est estimator, sprintID int64) {
	switch {
	case c.field == FieldStatus:
		if v, ok := asInt(c.from); ok {
			st.status = v
		}
	case c.field == FieldSprint:
		if v, ok := asInt(c.to); ok && v == sprintID {
			st.member = false
		}
		if v, ok := asInt(c.from); ok && v == sprintID {
			st.member = true
		}
	case est.kind != "count" && est.tracks(c.field):
		st.est = asFloat(c.from)
	}
}

func (st *replayIssue) apply(c histChange, est estimator, sprintID int64) {
	switch {
	case c.field == FieldStatus:
		if v, ok := asInt(c.to); ok {
			st.status = v
		}
	case c.field == FieldSprint:
		if v, ok := asInt(c.from); ok && v == sprintID {
			st.member = false
		}
		if v, ok := asInt(c.to); ok && v == sprintID {
			st.member = true
		}
	case est.kind != "count" && est.tracks(c.field):
		st.est = asFloat(c.to)
	}
}

func estOf(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// sprintReplay prepares the replay of a sprint: its board, window and issues.
func (s *Service) sprintReplay(ctx context.Context, p *identity.Principal, sprintID int64) (*Sprint, *Board, estimator, map[int64]*replayIssue, time.Time, time.Time, error) {
	var zero time.Time
	sp, err := s.GetSprint(ctx, p, sprintID)
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	if sp.State == SprintFuture || sp.Start == nil {
		return nil, nil, estimator{}, nil, zero, zero, apperr.Invalid("sprint.not-started", "The sprint has not started yet.")
	}
	b, err := s.GetBoard(ctx, p, sp.BoardID)
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	est, _ := s.estimator(ctx, b)
	start := *sp.Start
	end := now()
	if sp.Completed != nil {
		end = *sp.Completed
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	var args db.Args
	visible := a.VisibleSQL("i", &args)
	sid := args.Add(sprintID)
	marker := args.Add(`[{"field":"sprint","from":` + itoa(sprintID) + `}]`)
	marker2 := args.Add(`[{"field":"sprint","to":` + itoa(sprintID) + `}]`)
	rows, err := s.pool.Query(ctx, `SELECT i.id FROM issues i WHERE `+visible+` AND (
		EXISTS (SELECT 1 FROM issue_sprints x WHERE x.issue_id = i.id AND x.sprint_id = `+sid+`)
		OR EXISTS (SELECT 1 FROM sprint_baselines sb WHERE sb.issue_id = i.id AND sb.sprint_id = `+sid+`)
		OR EXISTS (SELECT 1 FROM issue_changes c WHERE c.issue_id = i.id AND (c.items @> `+marker+`::jsonb OR c.items @> `+marker2+`::jsonb)))`, args...)
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	issues, err := s.loadReplay(ctx, s.pool, est, ids, sprintID)
	if err != nil {
		return nil, nil, estimator{}, nil, zero, zero, err
	}
	return sp, b, est, issues, start, end, nil
}

// changesBetween returns the changes of all issues in (from, to), ordered by time.
func changesBetween(issues map[int64]*replayIssue, from, to time.Time) []struct {
	issue *replayIssue
	c     histChange
} {
	var out []struct {
		issue *replayIssue
		c     histChange
	}
	for _, r := range issues {
		for _, c := range r.changes {
			if c.at.After(from) && c.at.Before(to) {
				out = append(out, struct {
					issue *replayIssue
					c     histChange
				}{r, c})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].c.at.Before(out[j].c.at) })
	return out
}

// SprintBurndown returns the remaining estimate of a sprint over time.
func (s *Service) SprintBurndown(ctx context.Context, p *identity.Principal, sprintID int64) (*Burndown, error) {
	sp, b, est, issues, start, end, err := s.sprintReplay(ctx, p, sprintID)
	if err != nil {
		return nil, err
	}
	done := b.doneStatuses()
	states := map[int64]*replayIssue{}
	remaining := 0.0
	for id, r := range issues {
		st := r.stateAt(start, est, sprintID)
		states[id] = &st
		if st.member && !done[st.status] {
			remaining += estOf(st.est)
		}
	}
	out := &Burndown{Sprint: *sp, Statistic: b.Config.Estimation, Points: []BurndownPoint{{At: start, Remaining: remaining, Event: "start"}}}
	for _, ev := range changesBetween(issues, start, end) {
		st := states[ev.issue.id]
		before := 0.0
		if st.member && !done[st.status] {
			before = estOf(st.est)
		}
		wasMember, wasDone := st.member, done[st.status]
		st.apply(ev.c, est, sprintID)
		after := 0.0
		if st.member && !done[st.status] {
			after = estOf(st.est)
		}
		if before == after && wasMember == st.member && wasDone == done[st.status] {
			continue
		}
		event := "estimate"
		switch {
		case !wasMember && st.member:
			event = "added"
		case wasMember && !st.member:
			event = "removed"
		case !wasDone && done[st.status]:
			event = "completed"
		case wasDone && !done[st.status]:
			event = "reopened"
		}
		if !wasMember && !st.member {
			continue
		}
		remaining += after - before
		out.Points = append(out.Points, BurndownPoint{At: ev.c.at, Remaining: remaining, Event: event, Key: st.key, Delta: after - before})
	}
	last := "now"
	if sp.Completed != nil {
		last = "end"
	}
	out.Points = append(out.Points, BurndownPoint{At: end, Remaining: remaining, Event: last})
	return out, nil
}

// GetSprintReport lists completed, unfinished and removed issues of a sprint.
func (s *Service) GetSprintReport(ctx context.Context, p *identity.Principal, sprintID int64) (*SprintReport, error) {
	sp, b, est, issues, start, end, err := s.sprintReplay(ctx, p, sprintID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	done := b.doneStatuses()
	rep := &SprintReport{Sprint: *sp, Statistic: b.Config.Estimation, Completed: []ReportIssue{}, NotCompleted: []ReportIssue{}, Removed: []ReportIssue{}}
	var baseline map[int64]*float64
	rows, err := s.pool.Query(ctx, "SELECT issue_id, estimate FROM sprint_baselines WHERE sprint_id = $1", sprintID)
	if err != nil {
		return nil, err
	}
	baseline = map[int64]*float64{}
	for rows.Next() {
		var id int64
		var e *float64
		if err := rows.Scan(&id, &e); err != nil {
			rows.Close()
			return nil, err
		}
		baseline[id] = e
	}
	rows.Close()
	ids := make([]int64, 0, len(issues))
	for id := range issues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return issues[ids[i]].key < issues[ids[j]].key })
	for _, id := range ids {
		r := issues[id]
		atStart := r.stateAt(start, est, sprintID)
		atEnd := r.stateAt(end.Add(-time.Microsecond), est, sprintID)
		if e, ok := baseline[id]; ok {
			rep.Committed += estOf(e)
		} else if len(baseline) == 0 && atStart.member {
			rep.Committed += estOf(atStart.est)
		}
		ri := ReportIssue{Key: r.key, Summary: r.summary, Type: cfg.types[r.typ], Status: cfg.statuses[atEnd.status], Estimate: atEnd.est,
			Added: !atStart.member}
		if _, inBaseline := baseline[id]; inBaseline {
			ri.Added = false
		}
		switch {
		case atEnd.member && done[atEnd.status]:
			rep.Completed = append(rep.Completed, ri)
		case atEnd.member:
			rep.NotCompleted = append(rep.NotCompleted, ri)
		default:
			// Was it in the sprint at any time during it?
			was := atStart.member
			st := atStart
			for _, c := range r.changes {
				if c.at.After(start) && c.at.Before(end) {
					st.apply(c, est, sprintID)
					was = was || st.member
				}
			}
			if was {
				rep.Removed = append(rep.Removed, ri)
			}
		}
	}
	return rep, nil
}

// BoardVelocity compares commitment and result of the last closed sprints of a board.
func (s *Service) BoardVelocity(ctx context.Context, p *identity.Principal, boardID int64, count int) (*Velocity, error) {
	b, err := s.GetBoard(ctx, p, boardID)
	if err != nil {
		return nil, err
	}
	if count <= 0 || count > 20 {
		count = 7
	}
	sprints, err := listSprints(ctx, s.pool, "s.board_id = $1 AND s.state = 'closed' ORDER BY s.completed_at DESC LIMIT $2", boardID, count)
	if err != nil {
		return nil, err
	}
	out := &Velocity{Statistic: b.Config.Estimation, Sprints: []VelocityEntry{}}
	for i := len(sprints) - 1; i >= 0; i-- {
		rep, err := s.GetSprintReport(ctx, p, sprints[i].ID)
		if err != nil {
			return nil, err
		}
		e := VelocityEntry{Sprint: SprintRef{ID: sprints[i].ID, Name: sprints[i].Name, State: sprints[i].State}, Committed: rep.Committed}
		for _, ri := range rep.Completed {
			e.Completed += estOf(ri.Estimate)
		}
		out.Sprints = append(out.Sprints, e)
	}
	return out, nil
}

// BoardCumulativeFlow counts the board's issues per column at the end of each of the last days.
func (s *Service) BoardCumulativeFlow(ctx context.Context, p *identity.Principal, boardID int64, days int) (*CumulativeFlow, error) {
	b, err := s.GetBoard(ctx, p, boardID)
	if err != nil {
		return nil, err
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	est := estimator{kind: "count"}
	var ids []int64
	var created map[int64]time.Time
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, err := s.boardWhere(ctx, tx, p, b, nil, &args)
		if err != nil {
			return err
		}
		// One more than counted tells whether the board has more.
		rows, err := tx.Query(ctx, "SELECT i.id, i.created_at FROM issues i WHERE "+where+" ORDER BY i.id LIMIT "+
			args.Add(maxFlowIssues+1), args...)
		if err != nil {
			return err
		}
		created = map[int64]time.Time{}
		for rows.Next() {
			var id int64
			var c time.Time
			if err := rows.Scan(&id, &c); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			created[id] = c
		}
		rows.Close()
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	truncated := len(ids) > maxFlowIssues
	if truncated {
		delete(created, ids[maxFlowIssues])
		ids = ids[:maxFlowIssues]
	}
	issues, err := s.loadReplay(ctx, s.pool, est, ids, 0)
	if err != nil {
		return nil, err
	}
	loc := s.userLocation(ctx, p)
	out := &CumulativeFlow{Columns: []string{}, Days: []FlowDay{}, Truncated: truncated}
	for _, c := range b.Config.Columns {
		out.Columns = append(out.Columns, c.Name)
	}
	today := time.Now().In(loc)
	day0 := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	for d := 0; d < days; d++ {
		dayStart := day0.AddDate(0, 0, d)
		endOfDay := dayStart.AddDate(0, 0, 1)
		counts := make([]int, len(b.Config.Columns))
		for id, r := range issues {
			if created[id].After(endOfDay) {
				continue
			}
			st := r.stateAt(endOfDay, est, 0)
			if col := b.columnOf(st.status); col >= 0 {
				counts[col]++
			}
		}
		out.Days = append(out.Days, FlowDay{Date: dayStart.Format("2006-01-02"), Counts: counts})
	}
	return out, nil
}

func (s *Service) userLocation(ctx context.Context, p *identity.Principal) *time.Location {
	tz := ""
	if p.Authenticated() {
		tz = p.User.TimeZone
	}
	if tz == "" {
		if st, err := s.settings.Get(ctx); err == nil {
			tz = st.DefaultTimeZone
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// CreatedVsResolved counts issues of a query created and resolved per day or week.
func (s *Service) CreatedVsResolved(ctx context.Context, p *identity.Principal, query string, days int, period string) (*CreatedResolved, error) {
	if days <= 0 || days > 730 {
		days = 30
	}
	step := 1
	if period == "week" {
		step = 7
	}
	loc := s.userLocation(ctx, p)
	today := time.Now().In(loc)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	if step == 7 {
		// Weeks start on Monday.
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
	}
	out := &CreatedResolved{Periods: []CreatedResolvedPeriod{}}
	for t := start; !t.After(today); t = t.AddDate(0, 0, step) {
		out.Periods = append(out.Periods, CreatedResolvedPeriod{Start: t.Format("2006-01-02")})
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.PrepareSearch(ctx, tx, 15*time.Second); err != nil {
			return err
		}
		var args db.Args
		where, _, err := s.CompileQuery(ctx, tx, p, query, "i", &args)
		if err != nil {
			return err
		}
		from := args.Add(start)
		rows, err := tx.Query(ctx, "SELECT i.created_at, i.resolved_at FROM issues i WHERE "+where+" AND (i.created_at >= "+from+
			" OR i.resolved_at >= "+from+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := func(t time.Time) int {
			t = t.In(loc)
			d := int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Sub(start).Hours()/24+0.5) / step
			if d < 0 || d >= len(out.Periods) {
				return -1
			}
			return d
		}
		for rows.Next() {
			var c time.Time
			var r *time.Time
			if err := rows.Scan(&c, &r); err != nil {
				return err
			}
			if i := index(c); i >= 0 {
				out.Periods[i].Created++
			}
			if r != nil {
				if i := index(*r); i >= 0 {
					out.Periods[i].Resolved++
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
