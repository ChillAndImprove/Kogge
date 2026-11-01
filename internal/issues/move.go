// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Moving issues to another project: an issue gets a new key in the target project and keeps what is
// its own — comments, attachments, links, watchers, votes, work logs, history, development and activity — and its
// old key keeps resolving (issue_keys). What the target does not have is mapped (issue types, statuses) or removed
// (fields and values that do not exist there); sub-tasks move with their parent.
//
// An issue leaves its open sprint unless the sprint belongs to a board of the target project: a sprint is the plan of
// the team of its board's project, and an issue that stayed in it would count in its reports while the board (its
// project's issues, by default) no longer shows it. Completed sprints stay in the issue's sprint field, so that their
// reports stay complete.

// MoveInput moves issues to another project.
type MoveInput struct {
	Project  string          `json:"project" minLength:"1" maxLength:"10" doc:"Key of the target project"`
	Types    []TypeMapping   `json:"types,omitempty" maxItems:"100" doc:"The types issues become where the target project lacks theirs"`
	Statuses []StatusMapping `json:"statuses,omitempty" maxItems:"500" doc:"The statuses issues take where the target workflows lack theirs"`
}

// TypeMapping maps an issue type the target project lacks to one of its types (of the same level).
type TypeMapping struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// StatusMapping maps a status that the workflow of an issue type in the target project lacks to one of its statuses.
type StatusMapping struct {
	Type int64 `json:"type" doc:"The issue type in the target project"`
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// MovePlan says what moving issues to a project does and what has to be chosen for it.
type MovePlan struct {
	Project  ProjectRef       `json:"project"`
	Types    []TypeQuestion   `json:"types" doc:"Issue types the target project lacks, with the types they can become"`
	Statuses []StatusQuestion `json:"statuses" doc:"Statuses the target workflows lack, with the statuses they can become"`
	Issues   []MovePlanIssue  `json:"issues" doc:"The issues, followed by the sub-tasks that move with them"`
}

// TypeQuestion asks which type the issues of a type become in the target project.
type TypeQuestion struct {
	From    *IssueType   `json:"from"`
	Choices []*IssueType `json:"choices" doc:"The types of the target project on the same level"`
	Chosen  int64        `json:"chosen,omitempty" doc:"The type chosen in the request, or else the one the plan proposes"`
	Issues  []string     `json:"issues" doc:"Keys of the issues concerned"`
}

// StatusQuestion asks which status the issues of a type and status take in the target project.
type StatusQuestion struct {
	Type    *IssueType `json:"type" doc:"The issue type in the target project"`
	From    *Status    `json:"from"`
	Choices []*Status  `json:"choices" doc:"The statuses of the type's workflow in the target project"`
	Chosen  int64      `json:"chosen,omitempty" doc:"The status chosen in the request, or else the one the plan proposes"`
	Issues  []string   `json:"issues" doc:"Keys of the issues concerned"`
}

// MovePlanIssue is what becomes of an issue.
type MovePlanIssue struct {
	Key        string       `json:"key"`
	Summary    string       `json:"summary,omitempty"`
	Movable    bool         `json:"movable" doc:"The issue can move (once the questions are answered)"`
	Reason     string       `json:"reason,omitempty" doc:"Why it cannot move"`
	WithParent string       `json:"withParent,omitempty" doc:"The sub-task moves with this issue"`
	Subtasks   []string     `json:"subtasks" doc:"The sub-tasks that move with the issue"`
	Changes    []MoveChange `json:"changes" doc:"What changes besides the project and the key"`
}

// MoveChange is a value the move changes or removes.
type MoveChange struct {
	Field   string `json:"field" doc:"Field key, e.g. components or cf_12"`
	Name    string `json:"name,omitempty" doc:"Display name of a custom field"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Removed bool   `json:"removed,omitempty" doc:"The target has no such field or value: it is removed"`
}

// moveMapping is the mapping of a move request by its keys.
type moveMapping struct {
	types    map[int64]int64
	statuses map[[2]int64]int64
}

func mappingOf(in MoveInput) moveMapping {
	m := moveMapping{types: map[int64]int64{}, statuses: map[[2]int64]int64{}}
	for _, t := range in.Types {
		m.types[t.From] = t.To
	}
	for _, st := range in.Statuses {
		m.statuses[[2]int64{st.Type, st.From}] = st.To
	}
	return m
}

// moveTarget is the target project with what moves need to know about it.
type moveTarget struct {
	project    *Project
	types      []projectType
	components map[string]int64 // by lower-case name
	versions   map[string]int64 // by lower-case name
	versionIDs map[int64]string // names by id
	scheme     int64            // the security scheme (0: none)
	level      int64            // its default level (0: none)
	levelName  string
}

// loadMoveTarget loads the target of a move, a project the principal sees.
func (s *Service) loadMoveTarget(ctx context.Context, q db.Querier, a *Access, key string) (*moveTarget, error) {
	proj, err := projectByKey(ctx, q, key)
	if err != nil {
		return nil, err
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	t := &moveTarget{project: proj, components: map[string]int64{}, versions: map[string]int64{}, versionIDs: map[int64]string{}}
	if t.types, err = s.projectIssueTypes(ctx, q, proj.ID); err != nil {
		return nil, err
	}
	comps, err := s.listComponents(ctx, q, proj.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range comps {
		t.components[strings.ToLower(c.Name)] = c.ID
	}
	vs, err := s.listVersions(ctx, q, proj.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		t.versions[strings.ToLower(v.Name)] = v.ID
		t.versionIDs[v.ID] = v.Name
	}
	err = q.QueryRow(ctx, `SELECT coalesce(p.security_scheme_id, 0), coalesce(s.default_level_id, 0), coalesce(l.name, '')
		FROM issue_projects p LEFT JOIN security_schemes s ON s.id = p.security_scheme_id
		LEFT JOIN security_levels l ON l.id = s.default_level_id WHERE p.id = $1`, proj.ID).Scan(&t.scheme, &t.level, &t.levelName)
	return t, err
}

func (t *moveTarget) hasType(id int64) bool {
	return slices.ContainsFunc(t.types, func(pt projectType) bool { return pt.TypeID == id })
}

// typeChoices are the types of the target project that an issue of the type can become (those of its level).
func (t *moveTarget) typeChoices(cfg *configSnapshot, from int64) []*IssueType {
	out := []*IssueType{}
	ft := cfg.types[from]
	for _, pt := range t.types {
		if nt := cfg.types[pt.TypeID]; nt != nil && ft != nil && nt.Level == ft.Level {
			out = append(out, nt)
		}
	}
	return out
}

// moveOutcome is what moving an issue does: worked out for plans, written by moves.
type moveOutcome struct {
	issue  *Issue
	typeID int64 // in the target; 0 while no type is chosen
	status int64 // the status chosen in the target (0: it keeps its status, or none is chosen yet)
	// askType and askStatus tell that the target lacks the type or status (plans ask for them); needType and
	// needStatus that the choice is missing.
	askType, askStatus   bool
	needType, needStatus bool
	change               *fieldChange // the issue's values in the target (besides project, number and key)
	changes              []MoveChange // what plans show
	leave                bool         // the issue leaves its open sprint
	level                *levelChange // the new security level, if it changes
}

// finalLevel is the security level an issue has after the move (nil id: none).
func (o *moveOutcome) finalLevel() levelChange {
	if o.level != nil {
		return *o.level
	}
	if o.issue.securityLevelID == 0 {
		return levelChange{}
	}
	id := o.issue.securityLevelID
	return levelChange{id: &id, name: o.issue.SecurityLevel.Name}
}

type namedID struct {
	id   int64
	name string
}

func namedIDs(ctx context.Context, q db.Querier, sql string, args ...any) ([]namedID, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (namedID, error) {
		var n namedID
		err := r.Scan(&n.id, &n.name)
		return n, err
	})
}

// moveOutcomeOf works out what moving an issue to the target with the mapping does; a sub-task takes parentLevel,
// the security level of its parent after the move. With suggest, the choices the mapping lacks are made as plans
// propose them: the target's first type of the issue's level, the first status of the same category (or the
// workflow's initial one); without, they are missing.
func (s *Service) moveOutcomeOf(ctx context.Context, q db.Querier, cfg *configSnapshot, i *Issue, t *moveTarget, m moveMapping,
	parentLevel *levelChange, suggest bool) (*moveOutcome, error) {
	o := &moveOutcome{issue: i, change: &fieldChange{customs: map[string]any{}}}
	c := o.change

	// The issue type and the status in the target's workflow of the type.
	to := m.types[i.typeID]
	if to == 0 && suggest && !t.hasType(i.typeID) {
		if choices := t.typeChoices(cfg, i.typeID); len(choices) > 0 {
			to = choices[0].ID
		}
	}
	switch {
	case t.hasType(i.typeID):
		o.typeID = i.typeID
	case to != 0:
		if !slices.ContainsFunc(t.typeChoices(cfg, i.typeID), func(nt *IssueType) bool { return nt.ID == to }) {
			return nil, apperr.Invalidf("issue.move-type", "The issue type chosen for %s is not one of %s on its level.", i.Key,
				t.project.Key)
		}
		o.typeID, o.askType = to, true
		c.set("type_id", to)
		c.item(FieldIssueType, i.typeID, to, cfg.types[i.typeID].Name, cfg.types[to].Name)
		o.changes = append(o.changes, MoveChange{Field: FieldIssueType, From: cfg.types[i.typeID].Name, To: cfg.types[to].Name})
	default:
		o.askType, o.needType = true, true
	}
	if o.typeID != 0 {
		wf, err := s.workflowFor(ctx, q, cfg, t.project.ID, o.typeID)
		if err != nil {
			return nil, err
		}
		o.status = m.statuses[[2]int64{o.typeID, i.statusID}]
		if o.status == 0 && suggest && !wf.Def.HasStatus(i.statusID) {
			o.status = wf.Def.Initial
			for _, id := range wf.Def.Statuses {
				if st := cfg.statuses[id]; st != nil && st.Category == cfg.statuses[i.statusID].Category {
					o.status = id
					break
				}
			}
		}
		switch to := o.status; {
		case wf.Def.HasStatus(i.statusID):
			o.status = 0
		case to != 0:
			o.askStatus = true
			if !wf.Def.HasStatus(to) {
				return nil, apperr.Invalidf("issue.move-status", "The status chosen for %s is not part of its workflow in %s.", i.Key,
					t.project.Key)
			}
			from, next := cfg.statuses[i.statusID], cfg.statuses[to]
			c.set("status_id", to)
			c.sets = append(c.sets, "status_changed_at = now()")
			c.item(FieldStatus, i.statusID, to, from.Name, next.Name)
			o.changes = append(o.changes, MoveChange{Field: FieldStatus, From: from.Name, To: next.Name})
			// As a transition to the status would: entering the done category resolves, leaving it reopens.
			if wf.Def.Options.AutoResolution {
				before := len(c.items)
				switch {
				case next.Category == CategoryDone && i.Resolution == nil:
					if r := cfg.defaultResolution(); r != nil {
						id := r.ID
						c.setResolution(cfg, i, &id)
					}
				case next.Category != CategoryDone && i.Resolution != nil:
					c.setResolution(cfg, i, nil)
				}
				if len(c.items) > before {
					it := c.items[len(c.items)-1]
					o.changes = append(o.changes, MoveChange{Field: FieldResolution, From: it.FromString, To: it.ToString,
						Removed: it.ToString == ""})
				}
			}
		default:
			o.askStatus, o.needStatus = true, true
		}
	}

	// Components and versions belong to their project: those the target has under the same name stay.
	comps, err := namedIDs(ctx, q, `SELECT c.id, c.name FROM issue_components ic JOIN components c ON c.id = ic.component_id
		WHERE ic.issue_id = $1 ORDER BY lower(c.name)`, i.ID)
	if err != nil {
		return nil, err
	}
	if len(comps) > 0 {
		kept := []int64{}
		var keptNames, oldNames []string
		for _, cp := range comps {
			oldNames = append(oldNames, cp.name)
			if id, ok := t.components[strings.ToLower(cp.name)]; ok && !slices.Contains(kept, id) {
				kept, keptNames = append(kept, id), append(keptNames, cp.name)
			} else if !ok {
				o.changes = append(o.changes, MoveChange{Field: FieldComponents, From: cp.name, Removed: true})
			}
		}
		slices.Sort(kept)
		c.components = &kept
		if len(keptNames) < len(oldNames) {
			c.item(FieldComponents, nil, nil, strings.Join(oldNames, ", "), strings.Join(keptNames, ", "))
		}
	}
	for _, field := range []string{FieldFixVersions, FieldAffectsVersions} {
		kind := map[string]string{FieldFixVersions: "fix", FieldAffectsVersions: "affects"}[field]
		vs, err := namedIDs(ctx, q, `SELECT v.id, v.name FROM issue_versions iv JOIN versions v ON v.id = iv.version_id
			WHERE iv.issue_id = $1 AND iv.kind = $2 ORDER BY v.position, v.id`, i.ID, kind)
		if err != nil {
			return nil, err
		}
		if len(vs) == 0 {
			continue
		}
		old, kept := []int64{}, []int64{}
		var oldNames, keptNames []string
		for _, v := range vs {
			old, oldNames = append(old, v.id), append(oldNames, v.name)
			if id, ok := t.versions[strings.ToLower(v.name)]; ok && !slices.Contains(kept, id) {
				kept, keptNames = append(kept, id), append(keptNames, v.name)
			} else if !ok {
				o.changes = append(o.changes, MoveChange{Field: field, From: v.name, Removed: true})
			}
		}
		slices.Sort(old)
		slices.Sort(kept)
		if field == FieldFixVersions {
			c.fixVersions = &kept
		} else {
			c.affectsVersions = &kept
		}
		// The ids change even where the names stay: the history records them, so that KQL's WAS follows them.
		c.item(field, old, kept, strings.Join(oldNames, ", "), strings.Join(keptNames, ", "))
	}

	// Custom fields that do not apply to the target project and type are removed; version fields keep the versions
	// that the target has under the same name.
	if o.typeID != 0 {
		var sourceVersions map[int64]string
		if err := s.customFieldsOnMove(ctx, q, cfg, o, t, &sourceVersions); err != nil {
			return nil, err
		}
	}

	// The security level: a sub-task has its parent's. An issue's level stays where the target uses the same
	// scheme, else it becomes the target's default level or none; an issue without a level gets the default level,
	// as new issues of the target do.
	var level *levelChange
	switch {
	case parentLevel != nil:
		level = parentLevel
	case i.securityLevelID != 0 && t.scheme != 0:
		var scheme int64
		if err := q.QueryRow(ctx, "SELECT coalesce(security_scheme_id, 0) FROM issue_projects WHERE id = $1", i.projectID).Scan(&scheme); err != nil {
			return nil, err
		}
		if scheme != t.scheme {
			level = t.defaultLevel()
		}
	case i.securityLevelID != 0 || t.level != 0:
		level = t.defaultLevel()
	}
	if level != nil && !level.is(i.securityLevelID) {
		oldName := ""
		if i.SecurityLevel != nil {
			oldName = i.SecurityLevel.Name
		}
		c.set("security_level_id", optional(level.id))
		c.item(FieldSecurity, nullable(i.securityLevelID), optional(level.id), oldName, level.name)
		o.changes = append(o.changes, MoveChange{Field: FieldSecurity, From: oldName, To: level.name, Removed: level.id == nil})
		o.level = level
	}

	sp, err := openSprintOf(ctx, q, i.ID)
	if err != nil {
		return nil, err
	}
	if sp != nil {
		var boardProject int64
		if err := q.QueryRow(ctx, `SELECT coalesce(b.project_id, 0) FROM sprints s LEFT JOIN boards b ON b.id = s.board_id
			WHERE s.id = $1`, sp.ID).Scan(&boardProject); err != nil {
			return nil, err
		}
		if boardProject != t.project.ID {
			o.leave = true
			o.changes = append(o.changes, MoveChange{Field: FieldSprint, From: sp.Name, Removed: true})
		}
	}
	return o, nil
}

// is reports whether the level is the level with the id (0: none).
func (l *levelChange) is(id int64) bool {
	if l.id == nil {
		return id == 0
	}
	return *l.id == id
}

// defaultLevel is the level issues get that come into the target without one of its scheme.
func (t *moveTarget) defaultLevel() *levelChange {
	if t.level == 0 {
		return &levelChange{}
	}
	id := t.level
	return &levelChange{id: &id, name: t.levelName}
}

// customFieldsOnMove removes the custom field values the target does not have and maps the versions of version
// fields by name; sourceVersions caches the names of the source project's versions.
func (s *Service) customFieldsOnMove(ctx context.Context, q db.Querier, cfg *configSnapshot, o *moveOutcome, t *moveTarget,
	sourceVersions *map[int64]string) error {
	i, c := o.issue, o.change
	keys := make([]string, 0, len(i.Fields))
	for k := range i.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	userName := func(id int64) string {
		refs, err := identity.GetUserRefs(ctx, q, []int64{id})
		if err != nil {
			return ""
		}
		return refs[id].DisplayName
	}
	versions := func() (map[int64]string, error) {
		if *sourceVersions == nil {
			list, err := s.listVersions(ctx, q, i.projectID)
			if err != nil {
				return nil, err
			}
			*sourceVersions = map[int64]string{}
			for _, v := range list {
				(*sourceVersions)[v.ID] = v.Name
			}
		}
		return *sourceVersions, nil
	}
	for _, k := range keys {
		v := i.Fields[k]
		id, ok := parseFieldKey(k)
		f := cfg.fields[id]
		if !ok || v == nil || f == nil {
			continue
		}
		vs, err := versions()
		if err != nil {
			return err
		}
		display := displayCustom(f, v, userName, vs)
		// Raw values (except formatted text) let reports replay the history, as with edits.
		raw := v
		if f.Type == "textarea" {
			raw = nil
		}
		if !fieldApplies(f, t.project.ID, o.typeID) {
			c.removeCustoms = append(c.removeCustoms, k)
			c.items = append(c.items, ChangeItem{Field: k, Name: f.Name, From: raw, FromString: display})
			o.changes = append(o.changes, MoveChange{Field: k, Name: f.Name, From: display, Removed: true})
			continue
		}
		if f.Type != "version" && f.Type != "multiversion" {
			continue
		}
		var kept []int64
		var keptNames []string
		for _, vid := range valueIDs(v) {
			if nid, ok := t.versions[strings.ToLower(vs[vid])]; ok && vs[vid] != "" && !slices.Contains(kept, nid) {
				kept, keptNames = append(kept, nid), append(keptNames, t.versionIDs[nid])
			}
		}
		switch {
		case len(kept) == 0:
			c.removeCustoms = append(c.removeCustoms, k)
			c.items = append(c.items, ChangeItem{Field: k, Name: f.Name, From: raw, FromString: display})
			o.changes = append(o.changes, MoveChange{Field: k, Name: f.Name, From: display, Removed: true})
		case f.Type == "version":
			c.customs[k] = kept[0]
			c.items = append(c.items, ChangeItem{Field: k, Name: f.Name, From: raw, To: kept[0], FromString: display, ToString: keptNames[0]})
		default:
			slices.Sort(kept)
			c.customs[k] = kept
			c.items = append(c.items, ChangeItem{Field: k, Name: f.Name, From: raw, To: kept, FromString: display,
				ToString: strings.Join(keptNames, ", ")})
			if len(kept) < len(valueIDs(v)) {
				o.changes = append(o.changes, MoveChange{Field: k, Name: f.Name, From: display, To: strings.Join(keptNames, ", ")})
			}
		}
	}
	return nil
}

// moveAllowed checks what moving an issue to the target needs besides its sub-tasks: another project, the
// permission to move it, and to create issues in the target.
func moveAllowed(a *Access, i *Issue, t *moveTarget) error {
	if i.projectID == t.project.ID {
		return apperr.Conflict("issue.move-same-project", "%s is in project %s already.", i.Key, t.project.Key)
	}
	if !a.CanIssue(i.head(), PermMove) {
		return apperr.Forbidden("issue.move-denied", "You may not move %s.", i.Key)
	}
	if !a.Can(t.project.ID, PermCreate) {
		return apperr.Forbidden("issue.create-denied", "You may not create issues in %s.", t.project.Key)
	}
	return nil
}

// securityAllowed checks that who moves an issue whose security level the move changes or removes may set it: a
// level decides who sees the issue.
func securityAllowed(a *Access, o *moveOutcome) error {
	if o.level != nil && o.issue.securityLevelID != 0 && !a.CanIssue(o.issue.head(), PermSetSecurity) {
		return apperr.Forbidden("issue.move-security", "Moving %s would change its security level, which you may not set.", o.issue.Key)
	}
	return nil
}

// missingChoice tells which choice the move of an issue still needs.
func missingChoice(cfg *configSnapshot, o *moveOutcome, t *moveTarget) error {
	switch {
	case o.needType:
		return apperr.Invalidf("issue.move-type-missing", "Choose the issue type that issues of the type %s become in %s.",
			cfg.types[o.issue.typeID].Name, t.project.Key)
	case o.needStatus:
		return apperr.Invalidf("issue.move-status-missing", "Choose the status that issues in the status %s take in %s.",
			cfg.statuses[o.issue.statusID].Name, t.project.Key)
	}
	return nil
}

// moveIssue moves an issue (locked by the caller) and its sub-tasks to the target within tx. It returns the moved
// issue and the new keys of its sub-tasks by id; b collects the notifications of a bulk change (nil: they are sent).
func (s *Service) moveIssue(ctx context.Context, tx pgx.Tx, a *Access, p *identity.Principal, cfg *configSnapshot, cur *Issue,
	t *moveTarget, m moveMapping, b *bulkItem) (*Issue, map[int64]string, error) {
	if err := moveAllowed(a, cur, t); err != nil {
		return nil, nil, err
	}
	if cfg.types[cur.typeID].Level == -1 {
		return nil, nil, apperr.Invalid("issue.move-subtask", "A sub-task moves with its parent.")
	}
	o, err := s.moveOutcomeOf(ctx, tx, cfg, cur, t, m, nil, false)
	if err != nil {
		return nil, nil, err
	}
	if err := missingChoice(cfg, o, t); err != nil {
		return nil, nil, err
	}
	if err := securityAllowed(a, o); err != nil {
		return nil, nil, err
	}
	// The sub-tasks go along: each must be one the caller sees and may move.
	subIDs, err := subtaskIDs(ctx, tx, cur.ID)
	if err != nil {
		return nil, nil, err
	}
	level := o.finalLevel()
	var subs []*moveOutcome
	for _, id := range subIDs {
		sub, err := s.loadIssue(ctx, tx, cfg, id, true)
		if err != nil {
			return nil, nil, err
		}
		if !a.visible(sub.head()) || !a.CanIssue(sub.head(), PermMove) {
			return nil, nil, apperr.Forbidden("issue.move-subtasks", "%s has sub-tasks you may not move.", cur.Key)
		}
		so, err := s.moveOutcomeOf(ctx, tx, cfg, sub, t, m, &level, false)
		if err != nil {
			return nil, nil, err
		}
		if err := missingChoice(cfg, so, t); err != nil {
			return nil, nil, err
		}
		subs = append(subs, so)
	}
	uid := p.UserID()
	moved, err := s.applyMove(ctx, tx, cfg, o, t, uid, b)
	if err != nil {
		return nil, nil, err
	}
	keys := map[int64]string{}
	for _, so := range subs {
		sub, err := s.applyMove(ctx, tx, cfg, so, t, uid, b)
		if err != nil {
			return nil, nil, err
		}
		keys[sub.ID] = sub.Key
	}
	return moved, keys, nil
}

// applyMove writes a move: the new project, number and key, the mapped and removed values with their history, the
// old key, the open sprint the issue leaves; and notifies of it.
func (s *Service) applyMove(ctx context.Context, tx pgx.Tx, cfg *configSnapshot, o *moveOutcome, t *moveTarget, actorID int64,
	b *bulkItem) (*Issue, error) {
	i := o.issue
	var number int64
	if err := tx.QueryRow(ctx, `UPDATE issue_projects SET next_number = next_number + 1 WHERE id = $1
		RETURNING next_number - 1`, t.project.ID).Scan(&number); err != nil {
		return nil, err
	}
	key := t.project.Key + "-" + itoa(number)
	// The old key keeps resolving: in addresses, queries and the references of commits, branches and pages.
	if _, err := tx.Exec(ctx, "INSERT INTO issue_keys (key, issue_id) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING", i.Key, i.ID); err != nil {
		return nil, err
	}
	c := o.change
	c.set("project_id", t.project.ID)
	c.set("number", number)
	c.set("key", key)
	c.items = append([]ChangeItem{
		{Field: "project", From: i.projectID, To: t.project.ID, FromString: i.Project.Name, ToString: t.project.Name},
		{Field: "key", FromString: i.Key, ToString: key},
	}, c.items...)
	if err := c.save(ctx, tx, i, actorID, true); err != nil {
		return nil, err
	}
	if o.leave {
		if _, err := moveToSprint(ctx, tx, i.ID, nil, actorID); err != nil {
			return nil, err
		}
	}
	if err := s.refreshSearch(ctx, tx, i.ID); err != nil {
		return nil, err
	}
	moved, err := s.loadIssue(ctx, tx, cfg, i.ID, false)
	if err != nil {
		return nil, err
	}
	if err := s.notifyChange(ctx, tx, b, moved, EventMoved, actorID, "moved", i.Key+" → "+key, nil); err != nil {
		return nil, err
	}
	if err := s.recordActivity(ctx, tx, moved, actorID, "issue.moved", &activityData{FromKey: i.Key}); err != nil {
		return nil, err
	}
	return moved, s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.moved", IssueID: i.ID, Key: key, ProjectID: t.project.ID,
		ActorID: actorID, FromKey: i.Key, FromProjectID: i.projectID})
}

// MoveIssue moves an issue with its sub-tasks to another project and returns it with its new key.
func (s *Service) MoveIssue(ctx context.Context, p *identity.Principal, key string, in MoveInput) (*Issue, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var out *Issue
	var cfg *configSnapshot
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, c, err := s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		cfg = c
		// Access before state: who may not move the issue learns nothing about the target.
		if !a.CanIssue(cur.head(), PermMove) {
			return apperr.Forbidden("issue.move-denied", "You may not move %s.", cur.Key)
		}
		t, err := s.loadMoveTarget(ctx, tx, a, in.Project)
		if err != nil {
			return err
		}
		var subs map[int64]string
		if out, subs, err = s.moveIssue(ctx, tx, a, p, cfg, cur, t, mappingOf(in), nil); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "issue.move", TargetType: "issue", TargetID: out.Key,
			Details: map[string]any{"from": cur.Key, "to": out.Key, "subtasks": len(subs)}})
	})
	if err != nil {
		return nil, err
	}
	return out, s.complete(ctx, s.pool, cfg, a, out)
}

// PlanMove says what moving issues (by key) to a project does and which types and statuses must be chosen for it.
// It tells only about issues the principal sees; lang words why issues cannot move.
func (s *Service) PlanMove(ctx context.Context, p *identity.Principal, keys []string, in MoveInput, lang string) (*MovePlan, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	var plan *MovePlan
	// One snapshot; nothing is written.
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := s.loadMoveTarget(ctx, tx, a, in.Project)
		if err != nil {
			return err
		}
		var list []*Issue
		var missing []string
		for _, k := range keys {
			i, err := s.visibleIssueByKey(ctx, tx, cfg, a, k)
			if err != nil {
				return err
			}
			if i == nil {
				missing = append(missing, k)
			} else if !slices.ContainsFunc(list, func(x *Issue) bool { return x.ID == i.ID }) {
				list = append(list, i)
			}
		}
		if plan, err = s.planMove(ctx, tx, a, cfg, list, t, mappingOf(in), lang); err != nil {
			return err
		}
		for _, k := range missing {
			plan.Issues = append(plan.Issues, MovePlanIssue{Key: k, Subtasks: []string{}, Changes: []MoveChange{},
				Reason: localized(notFoundIssue(k), lang)})
		}
		return nil
	})
	return plan, err
}

// localized words an expected error for a reader of lang.
func localized(err error, lang string) string { return apperr.From(err).Localize(lang).Detail }

// visibleIssueByKey loads an issue by its current or a former key if the principal sees it (nil otherwise).
func (s *Service) visibleIssueByKey(ctx context.Context, q db.Querier, cfg *configSnapshot, a *Access, key string) (*Issue, error) {
	id, err := lookupIssueID(ctx, q, key)
	if apperr.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i, err := s.loadIssue(ctx, q, cfg, id, false)
	if apperr.IsNotFound(err) {
		return nil, nil
	}
	if err != nil || !a.visible(i.head()) {
		return nil, err
	}
	return i, nil
}

// planMove works out the plan for moving issues the principal sees; sub-tasks whose parent is among them are listed
// with it.
func (s *Service) planMove(ctx context.Context, q db.Querier, a *Access, cfg *configSnapshot, list []*Issue, t *moveTarget,
	m moveMapping, lang string) (*MovePlan, error) {
	plan := &MovePlan{Project: t.project.Ref(), Types: []TypeQuestion{}, Statuses: []StatusQuestion{}, Issues: []MovePlanIssue{}}
	selected := map[int64]bool{}
	for _, i := range list {
		selected[i.ID] = true
	}
	types := map[int64]*TypeQuestion{}
	var typeOrder []int64
	statuses := map[[2]int64]*StatusQuestion{}
	var statusOrder [][2]int64
	// The questions are asked whether they were answered or not (the answer, or what the plan proposes, is chosen),
	// so that the answers can still change.
	ask := func(o *moveOutcome) error {
		i := o.issue
		if o.askType {
			qn, ok := types[i.typeID]
			if !ok {
				qn = &TypeQuestion{From: cfg.types[i.typeID], Choices: t.typeChoices(cfg, i.typeID), Chosen: o.typeID, Issues: []string{}}
				types[i.typeID], typeOrder = qn, append(typeOrder, i.typeID)
			}
			qn.Issues = append(qn.Issues, i.Key)
		}
		if o.askStatus && o.typeID != 0 {
			k := [2]int64{o.typeID, i.statusID}
			qn, ok := statuses[k]
			if !ok {
				wf, err := s.workflowFor(ctx, q, cfg, t.project.ID, o.typeID)
				if err != nil {
					return err
				}
				qn = &StatusQuestion{Type: cfg.types[o.typeID], From: cfg.statuses[i.statusID], Chosen: o.status, Choices: []*Status{},
					Issues: []string{}}
				for _, id := range wf.Def.Statuses {
					if st := cfg.statuses[id]; st != nil {
						qn.Choices = append(qn.Choices, st)
					}
				}
				statuses[k], statusOrder = qn, append(statusOrder, k)
			}
			qn.Issues = append(qn.Issues, i.Key)
		}
		return nil
	}
	var subtasks []MovePlanIssue
	for _, i := range list {
		pi := MovePlanIssue{Key: i.Key, Summary: i.Summary, Subtasks: []string{}, Changes: []MoveChange{}}
		if cfg.types[i.typeID].Level == -1 {
			if selected[i.parentID] {
				continue // listed with its parent
			}
			pi.Reason = localized(apperr.Invalid("issue.move-subtask", "A sub-task moves with its parent."), lang)
			plan.Issues = append(plan.Issues, pi)
			continue
		}
		if err := moveAllowed(a, i, t); err != nil {
			pi.Reason = localized(err, lang)
			plan.Issues = append(plan.Issues, pi)
			continue
		}
		o, err := s.moveOutcomeOf(ctx, q, cfg, i, t, m, nil, true)
		if err != nil {
			if !isClientError(err) {
				return nil, err
			}
			pi.Reason = localized(err, lang)
			plan.Issues = append(plan.Issues, pi)
			continue
		}
		if err := ask(o); err != nil {
			return nil, err
		}
		pi.Changes = append(pi.Changes, o.changes...)
		if err := securityAllowed(a, o); err != nil {
			pi.Reason = localized(err, lang)
		}
		level := o.finalLevel()
		subIDs, err := subtaskIDs(ctx, q, i.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range subIDs {
			sub, err := s.loadIssue(ctx, q, cfg, id, false)
			if err != nil {
				return nil, err
			}
			if !a.visible(sub.head()) {
				// Named by nothing the caller sees; the move would stop at it.
				if pi.Reason == "" {
					pi.Reason = localized(apperr.Forbidden("issue.move-subtasks", "%s has sub-tasks you may not move.", i.Key), lang)
				}
				continue
			}
			pi.Subtasks = append(pi.Subtasks, sub.Key)
			si := MovePlanIssue{Key: sub.Key, Summary: sub.Summary, WithParent: i.Key, Subtasks: []string{}, Changes: []MoveChange{}}
			if !a.CanIssue(sub.head(), PermMove) {
				if pi.Reason == "" {
					pi.Reason = localized(apperr.Forbidden("issue.move-subtasks", "%s has sub-tasks you may not move.", i.Key), lang)
				}
				si.Reason = localized(apperr.Forbidden("issue.move-denied", "You may not move %s.", sub.Key), lang)
				subtasks = append(subtasks, si)
				continue
			}
			so, err := s.moveOutcomeOf(ctx, q, cfg, sub, t, m, &level, true)
			if err != nil {
				if !isClientError(err) {
					return nil, err
				}
				si.Reason = localized(err, lang)
				if pi.Reason == "" {
					pi.Reason = si.Reason
				}
				subtasks = append(subtasks, si)
				continue
			}
			if err := ask(so); err != nil {
				return nil, err
			}
			si.Changes = append(si.Changes, so.changes...)
			si.Movable = true
			subtasks = append(subtasks, si)
		}
		pi.Movable = pi.Reason == ""
		plan.Issues = append(plan.Issues, pi)
	}
	plan.Issues = append(plan.Issues, subtasks...)
	for _, id := range typeOrder {
		plan.Types = append(plan.Types, *types[id])
	}
	slices.SortStableFunc(plan.Types, func(x, y TypeQuestion) int { return cmp.Compare(x.From.Position, y.From.Position) })
	for _, k := range statusOrder {
		plan.Statuses = append(plan.Statuses, *statuses[k])
	}
	return plan, nil
}

// isClientError reports whether err is an expected answer: a problem with a status below 500.
func isClientError(err error) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Status < 500
}
