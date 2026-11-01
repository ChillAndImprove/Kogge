// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

type applyMode int

const (
	modeCreate applyMode = iota
	modeEdit
	modeTransition
)

// fieldChange collects the effect of a set of field values on an issue.
type fieldChange struct {
	sets            []string
	args            db.Args
	items           []ChangeItem
	components      *[]int64
	fixVersions     *[]int64
	affectsVersions *[]int64
	customs         map[string]any
	removeCustoms   []string
	mentions        []int64
	sprintSet       bool
	sprint          *Sprint
	// sprintChanged tells (after save) that the issue went into another sprint or the backlog; the sprint writes its
	// own history entry.
	sprintChanged bool
	// newParent is the parent the change sets (0: none), if it changes the parent.
	newParent *int64
	// subtaskLevel is the security level the sub-tasks take on when the level of their parent changes.
	subtaskLevel *levelChange
}

// levelChange is a new security level (nil: none) with its name.
type levelChange struct {
	id   *int64
	name string
}

func (c *fieldChange) set(col string, v any) {
	c.sets = append(c.sets, col+" = "+c.args.Add(v))
}

func (c *fieldChange) item(field string, from, to any, fromS, toS string) {
	c.items = append(c.items, ChangeItem{Field: field, From: from, To: to, FromString: fromS, ToString: toS})
}

func (c *fieldChange) fieldNames() []string {
	out := make([]string, 0, len(c.items))
	for _, it := range c.items {
		out = append(out, it.Field)
	}
	return out
}

// save writes the change to the issue (and, with history, a history entry).
func (c *fieldChange) save(ctx context.Context, tx pgx.Tx, cur *Issue, actorID int64, history bool) error {
	sets := append([]string{}, c.sets...)
	args := append(db.Args{}, c.args...)
	if len(c.customs) > 0 || len(c.removeCustoms) > 0 {
		add := map[string]any{}
		for k, v := range c.customs {
			add[k] = v
		}
		b, _ := json.Marshal(add)
		sets = append(sets, "fields = (fields - "+args.Add(nonNilStrings(c.removeCustoms))+"::text[]) || "+args.Add(string(b))+"::jsonb")
	}
	if len(sets) > 0 || c.components != nil || c.fixVersions != nil || c.affectsVersions != nil {
		sets = append(sets, "updated_at = now()", "version = version + 1")
		if _, err := tx.Exec(ctx, "UPDATE issues SET "+strings.Join(sets, ", ")+" WHERE id = "+args.Add(cur.ID), args...); err != nil {
			return err
		}
	}
	if c.components != nil {
		if _, err := tx.Exec(ctx, "DELETE FROM issue_components WHERE issue_id = $1", cur.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO issue_components (issue_id, component_id) SELECT $1, unnest($2::bigint[])", cur.ID, *c.components); err != nil {
			return err
		}
	}
	for kind, ids := range map[string]*[]int64{"fix": c.fixVersions, "affects": c.affectsVersions} {
		if ids == nil {
			continue
		}
		if _, err := tx.Exec(ctx, "DELETE FROM issue_versions WHERE issue_id = $1 AND kind = $2", cur.ID, kind); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO issue_versions (issue_id, version_id, kind) SELECT $1, unnest($2::bigint[]), $3",
			cur.ID, *ids, kind); err != nil {
			return err
		}
	}
	if c.sprintSet {
		// Writes its own history entry, also on creation, so that reports see when the issue joined.
		changed, err := moveToSprint(ctx, tx, cur.ID, c.sprint, actorID)
		if err != nil {
			return err
		}
		c.sprintChanged = changed
		rows, err := tx.Query(ctx, "SELECT id FROM issues WHERE parent_id = $1 AND type_id IN (SELECT id FROM issue_types WHERE level = -1)", cur.ID)
		if err != nil {
			return err
		}
		subs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range subs {
			if _, err := moveToSprint(ctx, tx, id, c.sprint, actorID); err != nil {
				return err
			}
		}
	}
	if c.subtaskLevel != nil {
		if err := followParentLevel(ctx, tx, cur, *c.subtaskLevel, actorID, history); err != nil {
			return err
		}
	}
	if history && len(c.items) > 0 {
		b, _ := json.Marshal(c.items)
		var actor any
		if actorID != 0 {
			actor = actorID
		}
		if _, err := tx.Exec(ctx, "INSERT INTO issue_changes (issue_id, actor_id, items) VALUES ($1, $2, $3)", cur.ID, actor, b); err != nil {
			return err
		}
	}
	// The spans of the fields (for WAS) follow the history; a new issue starts its spans.
	if !history || touchesSpans(c.items) {
		return rebuildSpans(ctx, tx, cur.ID)
	}
	return nil
}

// securityRules applies what follows from security levels: a sub-task always has the level of its parent (its
// visibility must not differ from it), and a new issue without a chosen level gets the default level of the
// project's security scheme.
func securityRules(ctx context.Context, tx pgx.Tx, cfg *configSnapshot, cur *Issue, fields map[string]json.RawMessage,
	mode applyMode, c *fieldChange) error {
	_, chosen := fields[FieldSecurity]
	var level *int64
	switch {
	case cfg.types[cur.typeID].Level == -1:
		if chosen {
			return invalidField(FieldSecurity, "a sub-task has the security level of its parent", nil)
		}
		parentID := cur.parentID
		if c.newParent != nil {
			parentID = *c.newParent
		}
		if parentID == 0 {
			return nil
		}
		if err := tx.QueryRow(ctx, "SELECT security_level_id FROM issues WHERE id = $1", parentID).Scan(&level); err != nil {
			return err
		}
	case mode == modeCreate && !chosen:
		err := tx.QueryRow(ctx, `SELECT s.default_level_id FROM issue_projects p JOIN security_schemes s ON s.id = p.security_scheme_id
			WHERE p.id = $1`, cur.projectID).Scan(&level)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
	default:
		return nil
	}
	if (level == nil && cur.securityLevelID == 0) || (level != nil && *level == cur.securityLevelID) {
		return nil
	}
	oldName, name, err := levelNames(ctx, tx, cur.securityLevelID, level)
	if err != nil {
		return err
	}
	c.set("security_level_id", optional(level))
	c.item(FieldSecurity, nullable(cur.securityLevelID), optional(level), oldName, name)
	return nil
}

// levelNames returns the names of two security levels (0 or nil: none).
func levelNames(ctx context.Context, q db.Querier, from int64, to *int64) (string, string, error) {
	var fromName, toName string
	if from != 0 {
		if err := q.QueryRow(ctx, "SELECT name FROM security_levels WHERE id = $1", from).Scan(&fromName); err != nil {
			return "", "", err
		}
	}
	if to != nil {
		if err := q.QueryRow(ctx, "SELECT name FROM security_levels WHERE id = $1", *to).Scan(&toName); err != nil {
			return "", "", err
		}
	}
	return fromName, toName, nil
}

// followParentLevel gives the sub-tasks of an issue its new security level, with an entry in their history.
func followParentLevel(ctx context.Context, tx pgx.Tx, parent *Issue, level levelChange, actorID int64, history bool) error {
	rows, err := tx.Query(ctx, `UPDATE issues s SET security_level_id = $1, updated_at = now(), version = s.version + 1
		FROM issues old WHERE old.id = s.id AND s.parent_id = $2 AND s.type_id IN (SELECT id FROM issue_types WHERE level = -1)
		AND s.security_level_id IS DISTINCT FROM $1 RETURNING s.id, old.security_level_id`, optional(level.id), parent.ID)
	if err != nil {
		return err
	}
	type changed struct {
		id   int64
		from *int64
	}
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (changed, error) {
		var c changed
		err := r.Scan(&c.id, &c.from)
		return c, err
	})
	if err != nil || !history {
		return err
	}
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	for _, sub := range subs {
		var from int64
		if sub.from != nil {
			from = *sub.from
		}
		fromName, _, err := levelNames(ctx, tx, from, nil)
		if err != nil {
			return err
		}
		b, _ := json.Marshal([]ChangeItem{{Field: FieldSecurity, From: nullable(from), To: optional(level.id),
			FromString: fromName, ToString: level.name}})
		if _, err := tx.Exec(ctx, "INSERT INTO issue_changes (issue_id, actor_id, items) VALUES ($1, $2, $3)", sub.id, actor, b); err != nil {
			return err
		}
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func decodeOptionalID(raw json.RawMessage, key string) (*int64, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return &n, nil
			}
		}
		return nil, invalidField(key, "expected an id or null", string(raw))
	}
	return &id, nil
}

func decodeIDs(raw json.RawMessage, key string) ([]int64, error) {
	if string(raw) == "null" {
		return []int64{}, nil
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, invalidField(key, "expected a list of ids", string(raw))
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func optional[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

// applyFields validates field values against permissions, the field layout and the configuration and returns
// the resulting change. screen restricts transition screens to their fields.
func (s *Service) applyFields(ctx context.Context, tx pgx.Tx, cfg *configSnapshot, hoursPerDay, daysPerWeek int, a *Access,
	cur *Issue, fields map[string]json.RawMessage, mode applyMode, screen []string) (*fieldChange, error) {
	c := &fieldChange{customs: map[string]any{}}
	h := cur.head()
	layout, err := s.FieldLayout(ctx, cur.projectID, cur.typeID)
	if err != nil {
		return nil, err
	}
	inLayout := func(key string) bool {
		if mode == modeTransition {
			return slices.Contains(screen, key)
		}
		for _, it := range layout {
			if it.Field == key {
				return (mode == modeCreate && it.Create) || (mode == modeEdit && it.Edit)
			}
		}
		return false
	}
	can := func(perm string) bool {
		if mode == modeCreate {
			return a.Can(cur.projectID, perm) || perm == PermEdit
		}
		if mode == modeTransition && perm == PermEdit {
			return true // fields on a transition screen come with the transition
		}
		return a.CanIssue(h, perm)
	}
	need := func(perm, key string) error {
		if !can(perm) {
			return apperr.Forbidden("issue.field-denied", "You may not change %s of %s.", key, cur.Key)
		}
		return nil
	}

	var versions map[int64]string
	projectVersions := func() (map[int64]string, error) {
		if versions != nil {
			return versions, nil
		}
		versions = map[int64]string{}
		list, err := s.listVersions(ctx, tx, cur.projectID)
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			versions[v.ID] = v.Name
		}
		return versions, nil
	}
	userName := func(id int64) string {
		if id == 0 {
			return ""
		}
		refs, err := identity.GetUserRefs(ctx, tx, []int64{id})
		if err != nil {
			return ""
		}
		return refs[id].DisplayName
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := fields[key]
		if key == FieldStatus {
			return nil, apperr.Invalid("issue.status", "The status changes only through transitions.")
		}
		if _, isCustom := parseFieldKey(key); !isCustom {
			if _, ok := systemField(key); !ok {
				return nil, apperr.Invalidf("issue.field-unknown", "Unknown field %s.", key).WithFields(
					apperr.FieldError{Location: "body.fields." + key})
			}
		}
		if !inLayout(key) && !(mode == modeCreate && (key == FieldParent || key == FieldDescription)) && key != FieldSprint {
			return nil, apperr.Invalidf("issue.field-not-on-screen", "The field %s cannot be set here.", key).WithFields(
				apperr.FieldError{Location: "body.fields." + key})
		}
		switch key {
		case FieldSummary:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			var v string
			if err := json.Unmarshal(raw, &v); err != nil || strings.TrimSpace(v) == "" || len(v) > 255 {
				return nil, invalidField(key, "1 to 255 characters", nil)
			}
			v = strings.TrimSpace(v)
			if v != cur.Summary {
				c.set("summary", v)
				c.item(key, nil, nil, cur.Summary, v)
			}
		case FieldDescription:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			var d *docmodel.Doc
			if string(raw) != "null" {
				if d, err = docmodel.Parse(raw, docmodel.Options{}); err != nil {
					return nil, invalidField(key, err.Error(), nil)
				}
				if len(d.Content) == 0 {
					d = nil
				}
			}
			oldText := docmodel.PlainText(cur.Description)
			newText := docmodel.PlainText(d)
			oldJSON, _ := json.Marshal(cur.Description)
			newJSON, _ := json.Marshal(d)
			if string(oldJSON) != string(newJSON) {
				if d == nil {
					c.sets = append(c.sets, "description = NULL")
				} else {
					c.set("description", d)
				}
				c.set("description_text", newText)
				c.item(key, nil, nil, truncate(oldText, 500), truncate(newText, 500))
				old := mentionsOf(cur.Description)
				for _, m := range mentionsOf(d) {
					if !slices.Contains(old, m) {
						c.mentions = append(c.mentions, m)
					}
				}
			}
		case FieldEnvironment:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			var v string
			if err := json.Unmarshal(raw, &v); err != nil && string(raw) != "null" {
				return nil, invalidField(key, "expected a string", nil)
			}
			if v != cur.Environment {
				c.set("environment", v)
				c.item(key, nil, nil, cur.Environment, v)
			}
		case FieldIssueType:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil || id == nil {
				return nil, invalidField(key, "an issue type is required", nil)
			}
			if *id == cur.typeID {
				continue
			}
			nt, ot := cfg.types[*id], cfg.types[cur.typeID]
			types, err := s.projectIssueTypes(ctx, tx, cur.projectID)
			if err != nil {
				return nil, err
			}
			if nt == nil || !slices.ContainsFunc(types, func(t projectType) bool { return t.TypeID == *id }) {
				return nil, invalidField(key, "the issue type is not available in this project", *id)
			}
			if nt.Level != ot.Level {
				return nil, invalidField(key, "the new type must be on the same hierarchy level", *id)
			}
			wf, err := s.workflowFor(ctx, tx, cfg, cur.projectID, *id)
			if err != nil {
				return nil, err
			}
			if !wf.Def.HasStatus(cur.statusID) {
				return nil, invalidField(key, "the issue's status does not exist in the new type's workflow", *id)
			}
			c.set("type_id", *id)
			c.item(key, cur.typeID, *id, ot.Name, nt.Name)
		case FieldPriority:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil {
				return nil, err
			}
			if id != nil && cfg.priorities[*id] == nil {
				return nil, invalidField(key, "unknown priority", *id)
			}
			old := cur.Priority
			if (old == nil) != (id == nil) || (old != nil && id != nil && old.ID != *id) {
				c.set("priority_id", optional(id))
				var fromS, toS string
				var from any
				if old != nil {
					fromS, from = old.Name, old.ID
				}
				if id != nil {
					toS = cfg.priorities[*id].Name
				}
				c.item(key, from, optional(id), fromS, toS)
			}
		case FieldResolution:
			if err := need(PermResolve, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil {
				return nil, err
			}
			if id != nil && cfg.resolutions[*id] == nil {
				return nil, invalidField(key, "unknown resolution", *id)
			}
			c.setResolution(cfg, cur, id)
		case FieldAssignee:
			if err := need(PermAssign, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil {
				return nil, err
			}
			if id != nil && *id == cur.assigneeID || id == nil && cur.assigneeID == 0 {
				continue
			}
			if id != nil {
				ok, err := s.assignable(ctx, tx, *id, cur.projectID)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, invalidField(key, "the user cannot be assigned in this project", *id)
				}
			}
			c.set("assignee_id", optional(id))
			to := int64(0)
			if id != nil {
				to = *id
			}
			c.item(key, nullable(cur.assigneeID), optional(id), userName(cur.assigneeID), userName(to))
		case FieldReporter:
			if err := need(PermModifyReporter, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil || id == nil {
				return nil, invalidField(key, "a reporter is required", nil)
			}
			if *id != cur.reporterID {
				c.set("reporter_id", *id)
				c.item(key, nullable(cur.reporterID), *id, userName(cur.reporterID), userName(*id))
			}
		case FieldLabels:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			var ls []string
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &ls); err != nil {
					return nil, invalidField(key, "expected a list of labels", nil)
				}
			}
			ls, err := normaliseLabels(key, ls)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(ls, cur.Labels) {
				c.set("labels", ls)
				c.item(key, nil, nil, strings.Join(cur.Labels, " "), strings.Join(ls, " "))
			}
		case FieldComponents:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			ids, err := decodeIDs(raw, key)
			if err != nil {
				return nil, err
			}
			comps, err := s.listComponents(ctx, tx, cur.projectID)
			if err != nil {
				return nil, err
			}
			names := map[int64]string{}
			for _, cp := range comps {
				names[cp.ID] = cp.Name
			}
			var newNames []string
			for _, id := range ids {
				n, ok := names[id]
				if !ok {
					return nil, invalidField(key, "the component does not belong to the project", id)
				}
				newNames = append(newNames, n)
			}
			old, err := userIDs(ctx, tx, "SELECT component_id FROM issue_components WHERE issue_id = $1 ORDER BY component_id", cur.ID)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(old, ids) {
				var oldNames []string
				for _, id := range old {
					oldNames = append(oldNames, names[id])
				}
				c.components = &ids
				c.item(key, nil, nil, strings.Join(oldNames, ", "), strings.Join(newNames, ", "))
			}
		case FieldFixVersions, FieldAffectsVersions:
			perm, kind := PermResolve, "fix"
			if key == FieldAffectsVersions {
				perm, kind = PermEdit, "affects"
			}
			if err := need(perm, key); err != nil {
				return nil, err
			}
			ids, err := decodeIDs(raw, key)
			if err != nil {
				return nil, err
			}
			vs, err := projectVersions()
			if err != nil {
				return nil, err
			}
			var newNames []string
			for _, id := range ids {
				n, ok := vs[id]
				if !ok {
					return nil, invalidField(key, "the version does not belong to the project", id)
				}
				newNames = append(newNames, n)
			}
			old, err := userIDs(ctx, tx, "SELECT version_id FROM issue_versions WHERE issue_id = $1 AND kind = $2 ORDER BY version_id", cur.ID, kind)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(old, ids) {
				var oldNames []string
				for _, id := range old {
					oldNames = append(oldNames, vs[id])
				}
				if kind == "fix" {
					c.fixVersions = &ids
				} else {
					c.affectsVersions = &ids
				}
				// The ids, so that the history (and KQL's WAS) follows versions that are renamed later.
				c.item(key, nonNil(old), nonNil(ids), strings.Join(oldNames, ", "), strings.Join(newNames, ", "))
			}
		case FieldDueDate:
			if err := need(PermSchedule, key); err != nil {
				return nil, err
			}
			var v *string
			if string(raw) != "null" {
				var sv string
				if err := json.Unmarshal(raw, &sv); err != nil {
					return nil, invalidField(key, "expected a date as YYYY-MM-DD", nil)
				}
				if sv != "" {
					d, err := parseDate(key, sv)
					if err != nil {
						return nil, err
					}
					v = &d
				}
			}
			old := ""
			if cur.DueDate != nil {
				old = *cur.DueDate
			}
			nv := ""
			if v != nil {
				nv = *v
			}
			if old != nv {
				c.set("due_date", optional(v))
				c.item(key, nil, nil, old, nv)
			}
		case FieldParent:
			if mode != modeCreate {
				if err := need(PermEdit, key); err != nil {
					return nil, err
				}
			}
			if err := s.applyParent(ctx, tx, cfg, a, cur, raw, c); err != nil {
				return nil, err
			}
		case FieldOriginalEstimate, FieldRemainingEstimate:
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			var v *int64
			if string(raw) != "null" {
				var n int64
				if err := json.Unmarshal(raw, &n); err != nil {
					var sv string
					if json.Unmarshal(raw, &sv) != nil {
						return nil, invalidField(key, "expected seconds or a duration such as 2h 30m", nil)
					}
					if n, err = ParseDuration(sv, hoursPerDay, daysPerWeek); err != nil {
						return nil, invalidField(key, err.Error(), sv)
					}
				}
				if n < 0 {
					return nil, invalidField(key, "must not be negative", n)
				}
				v = &n
			}
			col, old := "original_estimate", cur.OriginalEstimate
			if key == FieldRemainingEstimate {
				col, old = "remaining_estimate", cur.RemainingEstimate
			}
			if (old == nil) != (v == nil) || (old != nil && v != nil && *old != *v) {
				c.set(col, optional(v))
				c.item(key, optional(old), optional(v), durationString(old, hoursPerDay, daysPerWeek), durationString(v, hoursPerDay, daysPerWeek))
				if key == FieldOriginalEstimate && cur.RemainingEstimate == nil && fields[FieldRemainingEstimate] == nil && v != nil {
					c.set("remaining_estimate", *v)
				}
			}
		case FieldSprint:
			if mode == modeTransition {
				return nil, invalidField(key, "the sprint cannot change in a transition", nil)
			}
			if err := need(PermSchedule, key); err != nil {
				return nil, err
			}
			if cfg.types[cur.typeID] != nil && cfg.types[cur.typeID].Level == -1 {
				return nil, invalidField(key, "sub-tasks follow their parent into sprints", nil)
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil {
				return nil, err
			}
			c.sprintSet = true
			if id != nil {
				sp, err := sprintByID(ctx, tx, *id, false)
				if err != nil {
					return nil, invalidField(key, "the sprint does not exist", *id)
				}
				if sp.State == SprintClosed {
					return nil, invalidField(key, "the sprint is completed", *id)
				}
				if !a.CanAny(sp.projectID, PermBrowse) {
					return nil, invalidField(key, "the sprint does not exist", *id)
				}
				// Taking an issue into a sprint changes the sprint: scheduling in its project too (audit L16).
				if !a.Can(sp.projectID, PermSchedule) {
					return nil, apperr.Forbidden("sprint.schedule-denied", "You may not plan issues into the sprints of this board.")
				}
				c.sprint = sp
			}
			// So does taking it out of a sprint of another project's board.
			if mode == modeEdit {
				var from, fromSprint int64
				err := tx.QueryRow(ctx, `SELECT b.project_id, sp.id FROM issue_sprints x JOIN sprints sp ON sp.id = x.sprint_id
					JOIN boards b ON b.id = sp.board_id WHERE x.issue_id = $1 AND sp.state <> 'closed' LIMIT 1`, cur.ID).Scan(&from, &fromSprint)
				if err != nil && !db.IsNoRows(err) {
					return nil, err
				}
				if from != 0 && (id == nil || *id != fromSprint) && !a.Can(from, PermSchedule) {
					return nil, apperr.Forbidden("sprint.schedule-denied", "You may not take %s out of its sprint.", cur.Key)
				}
			}
		case FieldSecurity:
			if err := need(PermSetSecurity, key); err != nil {
				return nil, err
			}
			id, err := decodeOptionalID(raw, key)
			if err != nil {
				return nil, err
			}
			var name string
			if id != nil {
				if err := tx.QueryRow(ctx, `SELECT l.name FROM security_levels l JOIN issue_projects p ON p.security_scheme_id = l.scheme_id
					WHERE p.id = $1 AND l.id = $2`, cur.projectID, *id).Scan(&name); err != nil {
					if db.IsNoRows(err) {
						return nil, invalidField(key, "the level does not belong to the project's security scheme", *id)
					}
					return nil, err
				}
				g := a.levels[[2]int64{cur.projectID, *id}]
				if !g.all && !g.reporter && !g.assignee {
					return nil, invalidField(key, "you are not a member of this security level", *id)
				}
			}
			oldName := ""
			if cur.SecurityLevel != nil {
				oldName = cur.SecurityLevel.Name
			}
			if (id == nil) != (cur.securityLevelID == 0) || (id != nil && *id != cur.securityLevelID) {
				c.set("security_level_id", optional(id))
				c.item(key, nullable(cur.securityLevelID), optional(id), oldName, name)
				c.subtaskLevel = &levelChange{id: id, name: name}
			}
		default:
			id, _ := parseFieldKey(key)
			f := cfg.fields[id]
			if f == nil || !fieldApplies(f, cur.projectID, cur.typeID) {
				return nil, apperr.Invalidf("issue.field-unknown", "The field %s is not available for this issue.", key).WithFields(
					apperr.FieldError{Location: "body.fields." + key})
			}
			if err := need(PermEdit, key); err != nil {
				return nil, err
			}
			vs, err := projectVersions()
			if err != nil {
				return nil, err
			}
			v, err := customValue(f, raw, vs)
			if err != nil {
				return nil, err
			}
			oldV := cur.Fields[key]
			oldJSON, _ := json.Marshal(oldV)
			newJSON, _ := json.Marshal(v)
			if string(oldJSON) == string(newJSON) {
				continue
			}
			if v == nil {
				c.removeCustoms = append(c.removeCustoms, key)
			} else {
				c.customs[key] = v
			}
			var normalisedOld any
			_ = json.Unmarshal(oldJSON, &normalisedOld)
			// Raw values (except formatted text) let reports replay the history, e.g. story points.
			rawOld, rawNew := normalisedOld, toAny(v)
			if f.Type == "textarea" {
				rawOld, rawNew = nil, nil
			}
			c.item(key, rawOld, rawNew, displayCustom(f, normalisedOld, userName, vs), displayCustom(f, toAny(v), userName, vs))
			if d, ok := v.(*docmodel.Doc); ok {
				c.mentions = append(c.mentions, mentionsOf(d)...)
			}
		}
	}
	if err := securityRules(ctx, tx, cfg, cur, fields, mode, c); err != nil {
		return nil, err
	}
	if mode == modeCreate {
		for _, it := range layout {
			if !it.Required || !it.Create || it.Field == FieldSummary {
				continue
			}
			if raw, ok := fields[it.Field]; !ok || string(raw) == "null" || string(raw) == `""` || string(raw) == "[]" {
				return nil, apperr.Invalidf("issue.field-required", "The field %s is required.", it.Field).WithFields(
					apperr.FieldError{Location: "body.fields." + it.Field, Message: "required"})
			}
		}
	}
	return c, nil
}

func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func durationString(v *int64, hpd, dpw int) string {
	if v == nil {
		return ""
	}
	return FormatDuration(*v, hpd, dpw)
}

// setResolution sets or clears the resolution with its date.
func (c *fieldChange) setResolution(cfg *configSnapshot, cur *Issue, id *int64) {
	old := cur.Resolution
	if (old == nil) == (id == nil) && (old == nil || old.ID == *id) {
		return
	}
	if id == nil {
		c.sets = append(c.sets, "resolution_id = NULL", "resolved_at = NULL")
	} else {
		c.set("resolution_id", *id)
		if old == nil {
			c.sets = append(c.sets, "resolved_at = now()")
		}
	}
	var fromS, toS string
	var from any
	if old != nil {
		fromS, from = old.Name, old.ID
	}
	if id != nil {
		toS = cfg.resolutions[*id].Name
	}
	c.item(FieldResolution, from, optional(id), fromS, toS)
}

// applyParent validates and sets the parent: sub-tasks under standard issues of the same project, standard
// issues under epics; no cycles.
func (s *Service) applyParent(ctx context.Context, tx pgx.Tx, cfg *configSnapshot, a *Access, cur *Issue, raw json.RawMessage, c *fieldChange) error {
	typ := cfg.types[cur.typeID]
	var key string
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &key); err != nil {
			return invalidField(FieldParent, "expected an issue key", nil)
		}
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	oldKey := ""
	if cur.parentID != 0 {
		if err := tx.QueryRow(ctx, "SELECT key FROM issues WHERE id = $1", cur.parentID).Scan(&oldKey); err != nil {
			return err
		}
	}
	if key == "" {
		if typ.Level == -1 {
			return invalidField(FieldParent, "a sub-task needs a parent", nil)
		}
		if cur.parentID != 0 {
			c.sets = append(c.sets, "parent_id = NULL")
			c.item(FieldParent, cur.parentID, nil, oldKey, "")
			none := int64(0)
			c.newParent = &none
		}
		return nil
	}
	if typ.Level == 1 {
		return invalidField(FieldParent, "epics cannot have a parent", key)
	}
	pid, err := lookupIssueID(ctx, tx, key)
	if err != nil {
		return invalidField(FieldParent, "the parent issue does not exist", key)
	}
	parent, err := s.loadIssue(ctx, tx, cfg, pid, false)
	if err != nil {
		return err
	}
	if !a.visible(parent.head()) {
		return invalidField(FieldParent, "the parent issue does not exist", key)
	}
	if parent.ID == cur.ID {
		return invalidField(FieldParent, "an issue cannot be its own parent", key)
	}
	pt := cfg.types[parent.typeID]
	switch typ.Level {
	case -1:
		if pt.Level != 0 || parent.projectID != cur.projectID {
			return invalidField(FieldParent, "a sub-task's parent must be a standard issue of the same project", key)
		}
	case 0:
		if pt.Level != 1 {
			return invalidField(FieldParent, "the parent of a standard issue must be an epic", key)
		}
	}
	if parent.ID != cur.parentID {
		c.set("parent_id", parent.ID)
		c.item(FieldParent, nullable(cur.parentID), parent.ID, oldKey, parent.Key)
		c.newParent = &parent.ID
	}
	return nil
}

// assignableSQL returns an SQL condition that is true for the users (alias.id) to whom the permission scheme of the
// project grants issue.assignable: by name, through one of their groups, through a project role they hold
// themselves or through a group, as project lead, or as everyone signed in. It decides from the grants that
// Access.Can reads (archived projects assign no one), so that the assignee picker offers exactly the users an issue
// can be assigned to, and it reads the grants once per statement, not once per user.
func assignableSQL(alias string, projectID int64, args *db.Args) string {
	pid, perm := args.Add(projectID), args.Add(PermAssignable)
	grants := `SELECT g.holder_type, g.holder_id, p.lead_id FROM issue_projects p
		JOIN permission_scheme_grants g ON g.scheme_id = p.permission_scheme_id
		WHERE p.id = ` + pid + ` AND NOT p.archived AND g.permission = ` + perm
	return `(EXISTS (SELECT 1 FROM (` + grants + `) g WHERE g.holder_type IN ('anyone', 'logged-in'))
		OR ` + alias + `.id IN (WITH g AS (` + grants + `)
			SELECT holder_id FROM g WHERE holder_type = 'user'
			UNION ALL SELECT lead_id FROM g WHERE holder_type = 'project-lead' AND lead_id IS NOT NULL
			UNION ALL SELECT m.user_id FROM g JOIN group_members m ON m.group_id = g.holder_id WHERE g.holder_type = 'group'
			UNION ALL SELECT r.principal_id FROM g JOIN project_role_members r ON r.project_id = ` + pid + `
				AND r.role_id = g.holder_id AND r.principal_type = 'user' WHERE g.holder_type = 'role'
			UNION ALL SELECT m.user_id FROM g JOIN project_role_members r ON r.project_id = ` + pid + `
				AND r.role_id = g.holder_id AND r.principal_type = 'group' JOIN group_members m ON m.group_id = r.principal_id
				WHERE g.holder_type = 'role'))`
}

// assignable reports whether the user may be assignee in the project: an active user whom the project's scheme
// grants issue.assignable. The picker (AssignableUsers) uses the same condition, so that it offers no one who
// cannot be assigned.
func (s *Service) assignable(ctx context.Context, q db.Querier, userID, projectID int64) (bool, error) {
	var args db.Args
	uid := args.Add(userID)
	cond := assignableSQL("u", projectID, &args)
	var ok bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users u WHERE u.id = "+uid+" AND u.status = 'active' AND "+cond+")",
		args...).Scan(&ok)
	return ok, err
}

// AssignableUsers lists up to limit users who may be assigned in a project, matching query. Who may be assigned
// is part of the query, so that the list is full even when most people of the instance match the query but cannot
// be assigned in the project.
func (s *Service) AssignableUsers(ctx context.Context, p *identity.Principal, key, query string, limit int) ([]identity.UserRef, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	// The picker is for who assigns issues; it finds people by e-mail only for who may browse users (audit L13).
	if !a.Can(proj.ID, PermAssign) {
		return nil, apperr.Forbidden("issue.assign-denied", "You may not assign the issues of project %s.", proj.Key)
	}
	browse, err := s.authz.HasGlobal(ctx, p, authz.UserBrowse)
	if err != nil {
		return nil, err
	}
	users, _, err := identity.ListUsers(ctx, s.pool, identity.UserFilter{Query: query, ByName: !browse, Status: identity.StatusActive,
		Limit: limit, Where: func(args *db.Args) string { return assignableSQL("u", proj.ID, args) }})
	if err != nil {
		return nil, err
	}
	out := make([]identity.UserRef, 0, len(users))
	for _, u := range users {
		out = append(out, u.Ref())
	}
	return out, nil
}

// SecurityLevelChoice is a security level of a project's scheme.
type SecurityLevelChoice struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SecurityLevelChoices are the security levels the caller may give the issues of a project.
type SecurityLevelChoices struct {
	CanSet  bool                  `json:"canSet" doc:"The caller may set security levels in the project"`
	Default *SecurityLevelChoice  `json:"default,omitempty" doc:"The level of new issues for which none is chosen"`
	Levels  []SecurityLevelChoice `json:"levels" doc:"The levels the caller may choose: those whose members include them"`
}

// SecurityLevels lists the security levels the caller may choose in a project, for the create form and the
// issue view.
func (s *Service) SecurityLevels(ctx context.Context, p *identity.Principal, key string) (*SecurityLevelChoices, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	out := &SecurityLevelChoices{CanSet: a.Can(proj.ID, PermSetSecurity), Levels: []SecurityLevelChoice{}}
	rows, err := s.pool.Query(ctx, `SELECT l.id, l.name, l.description, l.id = s.default_level_id
		FROM issue_projects p JOIN security_schemes s ON s.id = p.security_scheme_id JOIN security_levels l ON l.scheme_id = s.id
		WHERE p.id = $1 ORDER BY l.position, l.id`, proj.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l SecurityLevelChoice
		var isDefault bool
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &isDefault); err != nil {
			return nil, err
		}
		if isDefault {
			d := l
			out.Default = &d
		}
		// As when a level is set (applyFields): members, and reporter or assignee holders for issues of theirs.
		if g := a.levels[[2]int64{proj.ID, l.ID}]; out.CanSet && (g.all || g.reporter || g.assignee) {
			out.Levels = append(out.Levels, l)
		}
	}
	return out, rows.Err()
}
