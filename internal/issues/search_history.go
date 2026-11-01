// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"strconv"
	"strings"
	"time"

	"kogge.de/kogge/internal/issues/kql"
)

// historyFields are the fields WAS and CHANGED know, by their KQL names.
var historyFields = map[string]string{
	"status": FieldStatus, "assignee": FieldAssignee, "reporter": FieldReporter, "priority": FieldPriority,
	"resolution": FieldResolution, "fixversion": FieldFixVersions,
}

// historyValues are the values of a history clause: ids (or, for the versions of CHANGED, names) and the empty value.
type historyValues struct {
	ids   []int64
	names []string
	empty bool
}

// historyValuesOf resolves the values of a WAS clause or a FROM or TO predicate.
func (c *compiler) historyValuesOf(field string, o kql.Operand, names bool) (historyValues, error) {
	vals, funcs, empty := values(o)
	out := historyValues{empty: empty}
	byName := func(what string, list func(string) []int64) error {
		if len(funcs) > 0 {
			return queryError(funcs[0].Pos, "Functions cannot be used with %s.", what)
		}
		for _, v := range vals {
			ids := list(v.Text)
			if len(ids) == 0 {
				return queryError(v.Pos, "The %s %q does not exist.", what, v.Text)
			}
			out.ids = append(out.ids, ids...)
		}
		return nil
	}
	var err error
	switch field {
	case FieldStatus:
		if empty {
			return out, queryError(kql.Pos(0), "The status is never empty.")
		}
		err = byName("status", func(v string) []int64 {
			var ids []int64
			for _, s := range c.cfg.statusList {
				if strings.EqualFold(s.Name, v) || strconv.FormatInt(s.ID, 10) == v {
					ids = append(ids, s.ID)
				}
			}
			return ids
		})
	case FieldPriority:
		err = byName("priority", func(v string) []int64 {
			for _, p := range c.cfg.prioList {
				if strings.EqualFold(p.Name, v) || strconv.FormatInt(p.ID, 10) == v {
					return []int64{p.ID}
				}
			}
			return nil
		})
	case FieldResolution:
		var named []kql.Value
		for _, v := range vals {
			if strings.EqualFold(v.Text, "unresolved") {
				out.empty = true
			} else {
				named = append(named, v)
			}
		}
		vals = named
		err = byName("resolution", func(v string) []int64 {
			for _, r := range c.cfg.resList {
				if strings.EqualFold(r.Name, v) || strconv.FormatInt(r.ID, 10) == v {
					return []int64{r.ID}
				}
			}
			return nil
		})
	case FieldAssignee, FieldReporter:
		out.ids, err = c.users(vals, funcs)
	case FieldFixVersions:
		if names {
			if len(funcs) > 0 {
				return out, queryError(funcs[0].Pos, "FROM and TO of fixVersion take version names.")
			}
			for _, v := range vals {
				out.names = append(out.names, strings.ToLower(strings.TrimSpace(v.Text)))
			}
			return out, nil
		}
		out.ids, err = c.versions(vals, funcs)
	}
	return out, err
}

// window narrows history clauses in time: [lo, hi), nil for open ends.
type window struct{ lo, hi *time.Time }

func (w *window) after(t time.Time) {
	if w.lo == nil || t.After(*w.lo) {
		w.lo = &t
	}
}

func (w *window) before(t time.Time) {
	if w.hi == nil || t.Before(*w.hi) {
		w.hi = &t
	}
}

// history compiles WAS (with the spans of the fields) and CHANGED (with the history).
func (c *compiler) history(h *kql.History) (string, error) {
	field, ok := historyFields[canonicalField(h.Field.Name)]
	if h.Field.CustomID != 0 || !ok {
		return "", queryError(h.Field.Pos, "WAS and CHANGED work with status, assignee, reporter, priority, resolution and fixVersion.")
	}
	var w window
	var by []int64
	hasBy := false
	var from, to *historyValues
	for _, p := range h.Predicates {
		switch p.Kind {
		case "after":
			t, day, err := c.instant(p.Value)
			if err != nil {
				return "", err
			}
			if day {
				t = t.AddDate(0, 0, 1)
			}
			w.after(t)
		case "before":
			t, _, err := c.instant(p.Value)
			if err != nil {
				return "", err
			}
			w.before(t)
		case "on":
			t, _, err := c.instant(p.Value)
			if err != nil {
				return "", err
			}
			n := t.In(c.loc)
			day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, c.loc)
			w.after(day)
			w.before(day.AddDate(0, 0, 1))
		case "during":
			l := p.Value.(kql.List)
			start, _, err := c.instant(l.Items[0])
			if err != nil {
				return "", err
			}
			end, day, err := c.instant(l.Items[1])
			if err != nil {
				return "", err
			}
			if day {
				end = end.AddDate(0, 0, 1)
			}
			w.after(start)
			w.before(end)
		case "by":
			vals, funcs, empty := values(p.Value)
			if empty {
				return "", queryError(p.Pos, "BY needs a user.")
			}
			ids, err := c.users(vals, funcs)
			if err != nil {
				return "", err
			}
			by, hasBy = append(by, ids...), true
		case "from", "to":
			v, err := c.historyValuesOf(field, p.Value, true)
			if err != nil {
				return "", err
			}
			if p.Kind == "from" {
				from = &v
			} else {
				to = &v
			}
		}
	}
	if h.Op == kql.OpChanged {
		return c.changed(field, w, by, hasBy, from, to), nil
	}

	if ready, err := c.s.SpansReady(c.ctx); err != nil {
		return "", err
	} else if !ready {
		return "", queryError(h.Pos, "WAS needs the history of all issues, which is still being prepared after the upgrade; try again in a few minutes.")
	}
	v, err := c.historyValuesOf(field, h.Value, false)
	if err != nil {
		return "", err
	}
	if v.empty && field == FieldFixVersions {
		return "", queryError(h.Pos, "fixVersion WAS EMPTY is not supported; use fixVersion IS EMPTY.")
	}
	var match []string
	if len(v.ids) > 0 {
		match = append(match, "s.value_id = ANY("+c.args.Add(v.ids)+")")
	}
	if v.empty {
		match = append(match, "s.value_id IS NULL")
	}
	if len(match) == 0 {
		match = append(match, "false")
	}
	conds := []string{"s.issue_id = " + c.col("id"), "s.field = " + c.args.Add(field), "(" + strings.Join(match, " OR ") + ")"}
	if w.hi != nil {
		conds = append(conds, "s.start_at < "+c.args.Add(*w.hi))
	}
	if w.lo != nil {
		conds = append(conds, "(s.end_at IS NULL OR s.end_at > "+c.args.Add(*w.lo)+")")
	}
	if hasBy {
		conds = append(conds, "s.setter_id = ANY("+c.args.Add(nonNil(by))+")")
	}
	cond := "EXISTS (SELECT 1 FROM issue_field_spans s WHERE " + strings.Join(conds, " AND ") + ")"
	if h.Op == kql.OpWasNot || h.Op == kql.OpWasNotIn {
		return "NOT " + cond, nil
	}
	return cond, nil
}

// changed compiles CHANGED: a change of the field in the history, by whom, when, from and to what.
func (c *compiler) changed(field string, w window, by []int64, hasBy bool, from, to *historyValues) string {
	conds := []string{"hc.issue_id = " + c.col("id"), "hi->>'field' = " + c.args.Add(field)}
	if w.lo != nil {
		conds = append(conds, "hc.at >= "+c.args.Add(*w.lo))
	}
	if w.hi != nil {
		conds = append(conds, "hc.at < "+c.args.Add(*w.hi))
	}
	if hasBy {
		conds = append(conds, "hc.actor_id = ANY("+c.args.Add(nonNil(by))+")")
	}
	side := func(key string, v *historyValues) string {
		if field == FieldFixVersions {
			// A version left (FROM) or entered (TO) the fix versions; the names of either side, as recorded.
			other := map[string]string{"from": "to", "to": "from"}[key]
			list := func(k string) string {
				return "regexp_split_to_array(lower(coalesce(hi->>'" + k + "String', '')), '\\s*,\\s*')"
			}
			return "EXISTS (SELECT 1 FROM unnest(" + c.args.Add(nonNilStrings(v.names)) + "::text[]) n WHERE n = ANY(" + list(key) +
				") AND NOT n = ANY(" + list(other) + "))"
		}
		var or []string
		if len(v.ids) > 0 {
			or = append(or, "(jsonb_typeof(hi->'"+key+"') = 'number' AND (hi->>'"+key+"')::bigint = ANY("+c.args.Add(v.ids)+"))")
		}
		if v.empty {
			or = append(or, "(hi->'"+key+"' IS NULL OR jsonb_typeof(hi->'"+key+"') = 'null')")
		}
		if len(or) == 0 {
			return "false"
		}
		return "(" + strings.Join(or, " OR ") + ")"
	}
	if from != nil {
		conds = append(conds, side("from", from))
	}
	if to != nil {
		conds = append(conds, side("to", to))
	}
	return "EXISTS (SELECT 1 FROM issue_changes hc CROSS JOIN LATERAL jsonb_array_elements(hc.items) hi WHERE " +
		strings.Join(conds, " AND ") + ")"
}
