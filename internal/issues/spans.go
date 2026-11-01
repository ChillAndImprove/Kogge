// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
)

// Spans: the values the fields of an issue had over time, for KQL's WAS. They are derived from the
// issue's history and current values — rebuilt for an issue whenever its history grows — rather than kept step by
// step, so that they always agree with the history, whichever way it was written (by Kogge, or imported in the
// form of the system it came from).

// spanFields are the fields WAS knows; fix versions are sets (a span per version).
var spanFields = []string{FieldStatus, FieldAssignee, FieldReporter, FieldPriority, FieldResolution, FieldFixVersions}

type span struct {
	field  string
	value  *int64
	start  time.Time
	end    *time.Time
	setter *int64
}

type historyEntry struct {
	at    time.Time
	actor *int64
	items []ChangeItem
}

// idOf reads a single id of a history item (a JSON number), nil for none.
func idOf(v any) *int64 {
	switch x := v.(type) {
	case float64:
		id := int64(x)
		return &id
	case int64:
		return &x
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return &n
		}
	}
	return nil
}

// versionSet reads the fix versions of one side of a history item: ids as Kogge records them, or, in older
// entries, the names (mapped with the project's versions); single for entries in the form of other systems, where
// an item adds or removes one version.
func versionSet(ids any, names string, byName map[string]int64) (set []int64, single bool) {
	switch x := ids.(type) {
	case []any:
		for _, v := range x {
			if id := idOf(v); id != nil {
				set = append(set, *id)
			}
		}
		return sortedSet(set), false
	case float64, int64, json.Number:
		return []int64{*idOf(x)}, true
	}
	for _, n := range strings.Split(names, ",") {
		if id, ok := byName[strings.ToLower(strings.TrimSpace(n))]; ok {
			set = append(set, id)
		}
	}
	return sortedSet(set), false
}

func sortedSet(s []int64) []int64 {
	slices.Sort(s)
	return slices.Compact(s)
}

// fieldSpans derives the spans of a field with one value: the value before its first change is the issue's first
// value (set by its creator); without changes, the current one.
func fieldSpans(field string, created time.Time, creator, current *int64, history []historyEntry) []span {
	var out []span
	value, start, setter := current, created, creator
	first := true
	for _, e := range history {
		for _, it := range e.items {
			if it.Field != field {
				continue
			}
			if first {
				value, first = idOf(it.From), false
			}
			at := e.at
			out = append(out, span{field: field, value: value, start: start, end: &at, setter: setter})
			value, start, setter = idOf(it.To), e.at, e.actor
		}
	}
	return append(out, span{field: field, value: value, start: start, setter: setter})
}

// versionSpans derives the spans of the fix versions, a span per version and stay in the set: the sets before
// every change are found back from the current set, then the changes are followed forward.
func versionSpans(created time.Time, creator *int64, current []int64, history []historyEntry, byName map[string]int64) []span {
	type change struct {
		at       time.Time
		actor    *int64
		from, to []int64
		single   bool // adds or removes one version (history in the form of other systems)
		fromNone bool
	}
	var changes []change
	for _, e := range history {
		for _, it := range e.items {
			if it.Field != FieldFixVersions {
				continue
			}
			from, single := versionSet(it.From, it.FromString, byName)
			to, single2 := versionSet(it.To, it.ToString, byName)
			changes = append(changes, change{at: e.at, actor: e.actor, from: from, to: to, single: single || single2,
				fromNone: it.From == nil && it.FromString == ""})
		}
	}
	set := sortedSet(slices.Clone(current))
	before := make([][]int64, len(changes)+1) // before[k]: the set before change k; before[len]: the current set
	before[len(changes)] = set
	for k := len(changes) - 1; k >= 0; k-- {
		c := changes[k]
		if c.single {
			next := slices.DeleteFunc(slices.Clone(set), func(id int64) bool { return slices.Contains(c.to, id) })
			if !c.fromNone {
				next = append(next, c.from...)
			}
			set = sortedSet(next)
		} else {
			set = c.from
		}
		before[k] = set
	}
	var out []span
	open := map[int64]span{}
	enter := func(ids []int64, at time.Time, setter *int64) {
		for _, id := range ids {
			if _, ok := open[id]; !ok {
				v := id
				open[id] = span{field: FieldFixVersions, value: &v, start: at, setter: setter}
			}
		}
	}
	leave := func(keep []int64, at time.Time) {
		for _, id := range slices.Sorted(maps.Keys(open)) {
			if !slices.Contains(keep, id) {
				sp, end := open[id], at
				sp.end = &end
				out = append(out, sp)
				delete(open, id)
			}
		}
	}
	enter(before[0], created, creator)
	for k, c := range changes {
		after := before[k+1]
		if !c.single {
			after = c.to
		}
		leave(after, c.at)
		enter(after, c.at, c.actor)
	}
	for _, id := range slices.Sorted(maps.Keys(open)) {
		out = append(out, open[id])
	}
	return out
}

// rebuildSpans derives the spans of an issue from its history and current values (in the caller's transaction).
func rebuildSpans(ctx context.Context, q db.Querier, issueID int64) error {
	var created time.Time
	var creator, projectID int64
	var status, assignee, reporter, priority, resolution *int64
	if err := q.QueryRow(ctx, `SELECT created_at, creator_id, project_id, status_id, assignee_id, reporter_id, priority_id, resolution_id
		FROM issues WHERE id = $1`, issueID).Scan(&created, &creator, &projectID, &status, &assignee, &reporter, &priority, &resolution); err != nil {
		return err
	}
	rows, err := q.Query(ctx, "SELECT at, actor_id, items FROM issue_changes WHERE issue_id = $1 ORDER BY at, id", issueID)
	if err != nil {
		return err
	}
	var history []historyEntry
	for rows.Next() {
		var e historyEntry
		var raw []byte
		if err := rows.Scan(&e.at, &e.actor, &raw); err != nil {
			rows.Close()
			return err
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if dec.Decode(&e.items) == nil {
			history = append(history, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var spans []span
	for field, current := range map[string]*int64{FieldStatus: status, FieldAssignee: assignee, FieldReporter: reporter,
		FieldPriority: priority, FieldResolution: resolution} {
		spans = append(spans, fieldSpans(field, created, &creator, current, history)...)
	}
	vrows, err := q.Query(ctx, "SELECT id, lower(name) FROM versions WHERE project_id = $1", projectID)
	if err != nil {
		return err
	}
	byName := map[string]int64{}
	for vrows.Next() {
		var id int64
		var name string
		if err := vrows.Scan(&id, &name); err != nil {
			vrows.Close()
			return err
		}
		byName[name] = id
	}
	vrows.Close()
	var current []int64
	if err := q.QueryRow(ctx, "SELECT coalesce(array_agg(version_id), '{}') FROM issue_versions WHERE issue_id = $1 AND kind = 'fix'",
		issueID).Scan(&current); err != nil {
		return err
	}
	spans = append(spans, versionSpans(created, &creator, current, history, byName)...)

	if _, err := q.Exec(ctx, "DELETE FROM issue_field_spans WHERE issue_id = $1", issueID); err != nil {
		return err
	}
	fields := make([]string, len(spans))
	values := make([]*int64, len(spans))
	starts := make([]time.Time, len(spans))
	ends := make([]*time.Time, len(spans))
	setters := make([]*int64, len(spans))
	for i, sp := range spans {
		fields[i], values[i], starts[i], ends[i], setters[i] = sp.field, sp.value, sp.start, sp.end, sp.setter
	}
	// Versions may have gone meanwhile (and with them their spans).
	_, err = q.Exec(ctx, `INSERT INTO issue_field_spans (issue_id, field, value_id, start_at, end_at, setter_id)
		SELECT $1, f, v, s, e, u FROM unnest($2::text[], $3::bigint[], $4::timestamptz[], $5::timestamptz[], $6::bigint[]) AS t(f, v, s, e, u)
		WHERE f <> $7 OR EXISTS (SELECT 1 FROM versions WHERE id = v)`, issueID, fields, values, starts, ends, setters, FieldFixVersions)
	if err != nil {
		return fmt.Errorf("spans of issue %d: %w", issueID, err)
	}
	return nil
}

// touchesSpans reports whether a change concerns a field with spans.
func touchesSpans(items []ChangeItem) bool {
	for _, it := range items {
		if slices.Contains(spanFields, it.Field) {
			return true
		}
	}
	return false
}

const spansMigration = "issue-field-spans"

// SpansReady reports whether WAS can use the spans of all issues (after the upgrade that added them, a background
// run derives them for the existing issues).
func (s *Service) SpansReady(ctx context.Context) (bool, error) {
	if s.spansReady.Load() {
		return true, nil
	}
	var done bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM data_migrations WHERE name = $1)", spansMigration).Scan(&done); err != nil {
		return false, err
	}
	if done {
		s.spansReady.Store(true)
	}
	return done, nil
}

// DeriveSpans derives the spans of the issues that have none, in batches, and records when all are done. Only one
// node does it at a time (an advisory lock); after an interruption it goes on where it stopped.
func (s *Service) DeriveSpans(ctx context.Context) error {
	var done bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM data_migrations WHERE name = $1)", spansMigration).Scan(&done); err != nil || done {
		return err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", spansMigration).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext($1))", spansMigration)
	}()
	after, total := int64(0), 0
	for {
		rows, err := conn.Query(ctx, `SELECT id FROM issues i WHERE id > $1
			AND NOT EXISTS (SELECT 1 FROM issue_field_spans s WHERE s.issue_id = i.id) ORDER BY id LIMIT 200`, after)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			for _, id := range ids {
				if err := rebuildSpans(ctx, tx, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after, total = ids[len(ids)-1], total+len(ids)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO data_migrations (name) VALUES ($1) ON CONFLICT DO NOTHING", spansMigration); err != nil {
		return err
	}
	s.spansReady.Store(true)
	s.log.Info("issue field spans derived for WAS queries", "issues", total)
	return nil
}
