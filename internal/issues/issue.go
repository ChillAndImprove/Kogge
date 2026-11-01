// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/rank"
	"kogge.de/kogge/internal/platform/settings"
)

const issueColumns = `i.id, i.key, i.project_id, p.key, p.name, p.lead_id, i.type_id, i.status_id, i.priority_id,
	i.resolution_id, i.summary, i.description, i.environment, i.reporter_id, i.creator_id, i.assignee_id, i.parent_id,
	i.security_level_id, sl.name, to_char(i.due_date, 'YYYY-MM-DD'), i.original_estimate, i.remaining_estimate,
	i.time_spent, i.labels, i.fields, i.rank, i.votes, i.created_at, i.updated_at, i.resolved_at, i.version`

const issueFrom = ` FROM issues i JOIN issue_projects p ON p.id = i.project_id
	LEFT JOIN security_levels sl ON sl.id = i.security_level_id `

type issueRow struct {
	Issue
	priorityID, resolutionID, creatorID *int64
	reporter, assignee, parent, level   *int64
	levelName                           *string
	description                         []byte
	fields                              []byte
	leadIDPtr                           *int64
}

func scanIssue(row pgx.Row, cfg *configSnapshot) (*Issue, error) {
	r := &issueRow{}
	if err := row.Scan(r.targets()...); err != nil {
		return nil, err
	}
	return r.issue(cfg), nil
}

// targets are the scan targets of issueColumns.
func (r *issueRow) targets() []any {
	i := &r.Issue
	return []any{&i.ID, &i.Key, &i.projectID, &i.Project.Key, &i.Project.Name, &r.leadIDPtr, &i.typeID, &i.statusID,
		&r.priorityID, &r.resolutionID, &i.Summary, &r.description, &i.Environment, &r.reporter, &r.creatorID, &r.assignee,
		&r.parent, &r.level, &r.levelName, &i.DueDate, &i.OriginalEstimate, &i.RemainingEstimate, &i.TimeSpent, &i.Labels,
		&r.fields, &i.Rank, &i.Votes, &i.Created, &i.Updated, &i.Resolved, &i.Version}
}

// issue completes the scanned issue with the configuration.
func (r *issueRow) issue(cfg *configSnapshot) *Issue {
	i := &r.Issue
	i.Project.ID = i.projectID
	i.Type, i.Status = cfg.types[i.typeID], cfg.statuses[i.statusID]
	if r.priorityID != nil {
		i.Priority = cfg.priorities[*r.priorityID]
	}
	if r.resolutionID != nil {
		i.Resolution = cfg.resolutions[*r.resolutionID]
	}
	if r.reporter != nil {
		i.reporterID = *r.reporter
	}
	if r.assignee != nil {
		i.assigneeID = *r.assignee
	}
	if r.parent != nil {
		i.parentID = *r.parent
	}
	if r.level != nil {
		i.securityLevelID = *r.level
		i.SecurityLevel = &Ref{ID: *r.level, Name: *r.levelName}
	}
	if r.leadIDPtr != nil {
		i.leadID = *r.leadIDPtr
	}
	if r.creatorID != nil {
		i.Creator = &identity.UserRef{ID: *r.creatorID}
	}
	if len(r.description) > 0 {
		var d docmodel.Doc
		if err := json.Unmarshal(r.description, &d); err == nil {
			i.Description = &d
		}
	}
	i.Fields = map[string]any{}
	_ = json.Unmarshal(r.fields, &i.Fields)
	if i.Labels == nil {
		i.Labels = []string{}
	}
	return i
}

// lookupIssueID resolves a current or former issue key (or a numeric id).
func lookupIssueID(ctx context.Context, q db.Querier, key string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM issues WHERE key = upper($1)
		UNION ALL SELECT issue_id FROM issue_keys WHERE key = upper($1) LIMIT 1`, key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, apperr.NotFound("issue.not-found", "Issue %s does not exist or you may not see it.", key)
	}
	return id, err
}

func (s *Service) loadIssue(ctx context.Context, q db.Querier, cfg *configSnapshot, id int64, forUpdate bool) (*Issue, error) {
	sql := "SELECT " + issueColumns + issueFrom + "WHERE i.id = $1"
	if forUpdate {
		sql += " FOR UPDATE OF i"
	}
	i, err := scanIssue(q.QueryRow(ctx, sql, id), cfg)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("issue.not-found", "Issue %d does not exist.", id)
	}
	return i, err
}

func notFoundIssue(key string) error {
	return apperr.NotFound("issue.not-found", "Issue %s does not exist or you may not see it.", key)
}

// issueFor loads an issue the principal can see.
func (s *Service) issueFor(ctx context.Context, q db.Querier, a *Access, key string, forUpdate bool) (*Issue, *configSnapshot, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, nil, err
	}
	id, err := lookupIssueID(ctx, q, key)
	if err != nil {
		return nil, nil, err
	}
	i, err := s.loadIssue(ctx, q, cfg, id, forUpdate)
	if err != nil {
		return nil, nil, err
	}
	if !a.visible(i.head()) {
		return nil, nil, notFoundIssue(key)
	}
	return i, cfg, nil
}

func (s *Service) permissionsFor(a *Access, i *Issue) IssuePermissions {
	h := i.head()
	return IssuePermissions{
		Edit: a.CanIssue(h, PermEdit), Transition: a.CanIssue(h, PermTransition), Assign: a.CanIssue(h, PermAssign),
		Delete: a.CanIssue(h, PermDelete), Comment: a.CanIssue(h, PermCommentAdd), Attach: a.CanIssue(h, PermAttachCreate),
		Link: a.CanIssue(h, PermLink), LogWork: a.CanIssue(h, PermWorklogAdd), Move: a.CanIssue(h, PermMove),
		Watchers: a.CanIssue(h, PermViewWatchers), Security: a.CanIssue(h, PermSetSecurity), Schedule: a.CanIssue(h, PermSchedule),
		Development: a.CanIssue(h, PermViewDevelopment), ModifyReporter: a.CanIssue(h, PermModifyReporter),
	}
}

// GetIssue returns an issue with all details the principal may see.
func (s *Service) GetIssue(ctx context.Context, p *identity.Principal, key string) (*Issue, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, cfg, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	return i, s.complete(ctx, s.pool, cfg, a, i)
}

// complete fills the derived parts of an issue: people, parent, components, versions, sub-tasks, links,
// attachments, watching and voting.
func (s *Service) complete(ctx context.Context, q db.Querier, cfg *configSnapshot, a *Access, i *Issue) error {
	ids := []int64{i.reporterID, i.assigneeID}
	if i.Creator != nil {
		ids = append(ids, i.Creator.ID)
	}
	refs, err := identity.GetUserRefs(ctx, q, ids)
	if err != nil {
		return err
	}
	if r, ok := refs[i.reporterID]; ok {
		i.Reporter = &r
	}
	if r, ok := refs[i.assigneeID]; ok {
		i.Assignee = &r
	}
	if i.Creator != nil {
		if r, ok := refs[i.Creator.ID]; ok {
			i.Creator = &r
		}
	}
	if i.FieldUsers, i.FieldGroups, err = fieldRefs(ctx, q, cfg, i.Fields); err != nil {
		return err
	}
	if i.parentID != 0 {
		refs, err := s.issueRefs(ctx, q, cfg, a, "i.id = $1", i.parentID)
		if err != nil {
			return err
		}
		if len(refs) == 1 {
			i.Parent = &refs[0]
		}
	}
	if i.Subtasks, err = s.issueRefs(ctx, q, cfg, a, "i.parent_id = $1 ORDER BY i.rank LIMIT 500", i.ID); err != nil {
		return err
	}
	i.Components, i.FixVersions, i.AffectsVersions = []Ref{}, []Ref{}, []Ref{}
	rows, err := q.Query(ctx, `SELECT c.id, c.name FROM issue_components ic JOIN components c ON c.id = ic.component_id
		WHERE ic.issue_id = $1 ORDER BY lower(c.name)`, i.ID)
	if err != nil {
		return err
	}
	if i.Components, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Ref]); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT v.id, v.name, iv.kind FROM issue_versions iv JOIN versions v ON v.id = iv.version_id
		WHERE iv.issue_id = $1 ORDER BY v.position`, i.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r Ref
		var kind string
		if err := rows.Scan(&r.ID, &r.Name, &kind); err != nil {
			rows.Close()
			return err
		}
		if kind == "fix" {
			i.FixVersions = append(i.FixVersions, r)
		} else {
			i.AffectsVersions = append(i.AffectsVersions, r)
		}
	}
	rows.Close()
	if i.Links, err = s.issueLinks(ctx, q, cfg, a, i.ID); err != nil {
		return err
	}
	if i.Sprint, i.ClosedSprints, err = issueSprints(ctx, q, i.ID); err != nil {
		return err
	}
	if i.Attachments, err = s.attachments(ctx, q, i.ID); err != nil {
		return err
	}
	uid := a.UserID()
	// As DeleteAttachment decides.
	deleteAll, deleteOwn := a.CanIssue(i.head(), PermAttachDeleteAll), a.CanIssue(i.head(), PermAttachDeleteOwn)
	for k := range i.Attachments {
		own := uid != 0 && i.Attachments[k].Author != nil && i.Attachments[k].Author.ID == uid
		i.Attachments[k].CanDelete = deleteAll || (own && deleteOwn)
	}
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM watchers WHERE entity_type = 'issue' AND entity_id = $1),
		EXISTS (SELECT 1 FROM watchers WHERE entity_type = 'issue' AND entity_id = $1 AND user_id = $2),
		EXISTS (SELECT 1 FROM issue_votes WHERE issue_id = $1 AND user_id = $2)`, i.ID, uid).
		Scan(&i.Watchers, &i.Watching, &i.Voted); err != nil {
		return err
	}
	i.Permissions = s.permissionsFor(a, i)
	if !i.Permissions.Watchers {
		i.Watchers = 0
	}
	return nil
}

// fieldRefs returns the users and groups that the values of user, multiuser and group fields refer to, so that
// clients can show their names.
func fieldRefs(ctx context.Context, q db.Querier, cfg *configSnapshot, fields map[string]any) ([]identity.UserRef, []Ref, error) {
	var userIDs, groupIDs []int64
	for _, f := range cfg.fieldList {
		v, ok := fields[f.Key]
		if !ok || v == nil {
			continue
		}
		switch f.Type {
		case "user", "multiuser":
			userIDs = append(userIDs, valueIDs(v)...)
		case "group":
			groupIDs = append(groupIDs, valueIDs(v)...)
		}
	}
	var users []identity.UserRef
	if len(userIDs) > 0 {
		refs, err := identity.GetUserRefs(ctx, q, userIDs)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range refs {
			users = append(users, r)
		}
		slices.SortFunc(users, func(a, b identity.UserRef) int { return cmp.Compare(a.ID, b.ID) })
	}
	var groups []Ref
	if len(groupIDs) > 0 {
		names, err := identity.GroupNames(ctx, q, groupIDs)
		if err != nil {
			return nil, nil, err
		}
		for id, name := range names {
			groups = append(groups, Ref{ID: id, Name: name})
		}
		slices.SortFunc(groups, func(a, b Ref) int { return cmp.Compare(a.ID, b.ID) })
	}
	return users, groups, nil
}

// issueRefs loads short forms of visible issues matching a condition on alias i.
func (s *Service) issueRefs(ctx context.Context, q db.Querier, cfg *configSnapshot, a *Access, cond string, args ...any) ([]IssueRef, error) {
	all := db.Args(args)
	sql := `SELECT i.id, i.key, i.summary, i.type_id, i.status_id, i.priority_id, i.assignee_id, u.username, u.display_name
		FROM issues i LEFT JOIN users u ON u.id = i.assignee_id WHERE `
	// The visibility predicate must come before ORDER BY/LIMIT in cond.
	vis := a.VisibleSQL("i", &all)
	if idx := strings.Index(strings.ToUpper(cond), " ORDER BY"); idx >= 0 {
		sql += "(" + cond[:idx] + ") AND " + vis + cond[idx:]
	} else {
		sql += "(" + cond + ") AND " + vis
	}
	rows, err := q.Query(ctx, sql, all...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (IssueRef, error) {
		var r IssueRef
		var typ, status int64
		var prio, assignee *int64
		var au, an *string
		if err := row.Scan(&r.ID, &r.Key, &r.Summary, &typ, &status, &prio, &assignee, &au, &an); err != nil {
			return r, err
		}
		r.Type, r.Status = cfg.types[typ], cfg.statuses[status]
		if prio != nil {
			r.Priority = cfg.priorities[*prio]
		}
		if assignee != nil {
			r.Assignee = &identity.UserRef{ID: *assignee, Username: *au, DisplayName: *an, Active: true}
		}
		return r, nil
	})
	if out == nil {
		out = []IssueRef{}
	}
	return out, err
}

// NewIssue describes an issue to create.
type NewIssue struct {
	Project     string                     `json:"project" doc:"Project key"`
	TypeID      int64                      `json:"typeId"`
	Summary     string                     `json:"summary" minLength:"1" maxLength:"255"`
	Description *docmodel.Doc              `json:"description,omitempty"`
	Markdown    string                     `json:"descriptionMarkdown,omitempty" doc:"Alternative to description"`
	Parent      string                     `json:"parent,omitempty" doc:"Key of the parent issue"`
	Fields      map[string]json.RawMessage `json:"fields,omitempty" doc:"Other fields by key (system or cf_<id>)"`
}

// CreateIssue creates an issue.
func (s *Service) CreateIssue(ctx context.Context, p *identity.Principal, in NewIssue) (*Issue, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var created *Issue
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, in.Project)
		if err != nil {
			return err
		}
		if !a.Can(proj.ID, PermCreate) {
			if a.CanAny(proj.ID, PermBrowse) {
				return apperr.Forbidden("issue.create-denied", "You may not create issues in %s.", proj.Key)
			}
			return apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", in.Project)
		}
		types, err := s.projectIssueTypes(ctx, tx, proj.ID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(types, func(t projectType) bool { return t.TypeID == in.TypeID }) {
			return apperr.Invalid("issue.type", "The issue type is not available in this project.",
				apperr.FieldError{Location: "body.typeId", Value: in.TypeID})
		}
		typ := cfg.types[in.TypeID]
		wf, err := s.workflowFor(ctx, tx, cfg, proj.ID, in.TypeID)
		if err != nil {
			return err
		}
		var number int64
		if err := tx.QueryRow(ctx, `UPDATE issue_projects SET next_number = next_number + 1 WHERE id = $1
			RETURNING next_number - 1`, proj.ID).Scan(&number); err != nil {
			return err
		}
		var maxRank *string
		if err := tx.QueryRow(ctx, "SELECT max(rank) FROM issues").Scan(&maxRank); err != nil {
			return err
		}
		rk := rank.After("")
		if maxRank != nil {
			rk = rank.After(*maxRank)
		}
		key := proj.Key + "-" + itoa(number)
		uid := p.UserID()
		var assignee any
		if proj.DefaultAssignee == "lead" && proj.Lead != nil {
			assignee = proj.Lead.ID
		}
		var prio any
		if dp := cfg.defaultPriority(); dp != nil {
			prio = dp.ID
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO issues (project_id, number, key, type_id, status_id, priority_id, summary,
			reporter_id, creator_id, assignee_id, rank) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9, $10) RETURNING id`,
			proj.ID, number, key, in.TypeID, wf.Def.Initial, prio, strings.TrimSpace(in.Summary), uid, assignee, rk).Scan(&id); err != nil {
			return err
		}
		cur, err := s.loadIssue(ctx, tx, cfg, id, true)
		if err != nil {
			return err
		}
		fields := map[string]json.RawMessage{}
		for k, v := range in.Fields {
			fields[k] = v
		}
		if in.Description != nil {
			b, _ := json.Marshal(in.Description)
			fields[FieldDescription] = b
		} else if in.Markdown != "" {
			b, _ := json.Marshal(docmodel.FromMarkdown(in.Markdown))
			fields[FieldDescription] = b
		}
		if in.Parent != "" {
			b, _ := json.Marshal(in.Parent)
			fields[FieldParent] = b
		} else if typ.Level == -1 {
			return apperr.Invalid("issue.parent-required", "A sub-task needs a parent issue.")
		}
		ch, err := s.applyFields(ctx, tx, cfg, st.HoursPerDay, st.DaysPerWeek, a, cur, fields, modeCreate, nil)
		if err != nil {
			return err
		}
		if err := ch.save(ctx, tx, cur, uid, false); err != nil {
			return err
		}
		if typ.Level == -1 {
			if err := followParentSprint(ctx, tx, id, uid); err != nil {
				return err
			}
		}
		if err := s.refreshSearch(ctx, tx, id); err != nil {
			return err
		}
		created, err = s.loadIssue(ctx, tx, cfg, id, false)
		if err != nil {
			return err
		}
		for _, w := range []int64{uid, created.assigneeID} {
			if w != 0 {
				if err := notify.Watch(ctx, tx, "issue", id, w); err != nil {
					return err
				}
			}
		}
		if err := s.notifyIssue(ctx, tx, created, EventCreated, uid, "created", docmodel.Summary(created.Description, 300),
			ch.mentions); err != nil {
			return err
		}
		if err := s.recordActivity(ctx, tx, created, uid, "issue.created", nil); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.created", IssueID: id, Key: key, ProjectID: proj.ID, ActorID: uid})
	})
	if err != nil {
		return nil, err
	}
	return created, s.complete(ctx, s.pool, cfg, a, created)
}

// UpdateIssue changes fields of an issue. version (from If-Match) guards against lost updates.
func (s *Service) UpdateIssue(ctx context.Context, p *identity.Principal, key string, version int64, fields map[string]json.RawMessage) (*Issue, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var out *Issue
	var cfg *configSnapshot
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur *Issue
		var err error
		cur, cfg, err = s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		if version != 0 && version != cur.Version {
			return apperr.PreconditionFailed("issue.stale", "The issue was changed by someone else. Reload it and try again.")
		}
		out, _, err = s.editIssue(ctx, tx, a, p, cfg, st, cur, fields, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, s.complete(ctx, s.pool, cfg, a, out)
}

// editIssue changes fields of an issue (locked by the caller) within tx and reports whether anything changed; b
// collects the notifications of a bulk change (nil: they are sent).
func (s *Service) editIssue(ctx context.Context, tx pgx.Tx, a *Access, p *identity.Principal, cfg *configSnapshot,
	st settings.Instance, cur *Issue, fields map[string]json.RawMessage, b *bulkItem) (*Issue, bool, error) {
	ch, err := s.applyFields(ctx, tx, cfg, st.HoursPerDay, st.DaysPerWeek, a, cur, fields, modeEdit, nil)
	if err != nil {
		return nil, false, err
	}
	// The sprint is no item of the change: it writes its own history when the issue goes to another one.
	if len(ch.items) == 0 && !ch.sprintSet {
		return cur, false, nil
	}
	uid := p.UserID()
	if err := ch.save(ctx, tx, cur, uid, true); err != nil {
		return nil, false, err
	}
	if len(ch.items) == 0 && !ch.sprintChanged {
		return cur, false, nil
	}
	if err := s.refreshSearch(ctx, tx, cur.ID); err != nil {
		return nil, false, err
	}
	out, err := s.loadIssue(ctx, tx, cfg, cur.ID, false)
	if err != nil {
		return nil, false, err
	}
	event := EventUpdated
	if len(ch.items) == 1 && ch.items[0].Field == FieldAssignee && !ch.sprintChanged {
		event = EventAssigned
		if out.assigneeID != 0 {
			if err := notify.Watch(ctx, tx, "issue", out.ID, out.assigneeID); err != nil {
				return nil, false, err
			}
		}
	}
	summary, names := changeSummary(ch.items), ch.fieldNames()
	if ch.sprintChanged {
		name := "—"
		if ch.sprint != nil {
			name = ch.sprint.Name
		}
		summary = strings.TrimPrefix(summary+"; "+FieldSprint+": "+name, "; ")
		names = append(names, FieldSprint)
	}
	if err := s.notifyChange(ctx, tx, b, out, event, uid, "updated", summary, ch.mentions); err != nil {
		return nil, false, err
	}
	if err := s.recordActivity(ctx, tx, out, uid, "issue.updated", &activityData{Fields: changedFields(cfg, names)}); err != nil {
		return nil, false, err
	}
	return out, true, s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.updated", IssueID: out.ID, Key: out.Key, ProjectID: out.projectID,
		ActorID: uid, Fields: names})
}

func changeSummary(items []ChangeItem) string {
	var parts []string
	for _, it := range items {
		if it.Field == FieldDescription {
			parts = append(parts, "description")
			continue
		}
		v := it.ToString
		if v == "" {
			v = "—"
		}
		parts = append(parts, it.Field+": "+v)
	}
	return strings.Join(parts, "; ")
}

// DeleteIssue removes an issue and its sub-tasks.
func (s *Service) DeleteIssue(ctx context.Context, p *identity.Principal, key string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, cfg, err := s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		_, err = s.deleteIssue(ctx, tx, a, p, cfg, cur, nil)
		return err
	})
}

// subtaskIDs returns the sub-tasks of an issue.
func subtaskIDs(ctx context.Context, q db.Querier, issueID int64) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT id FROM issues WHERE parent_id = $1 AND type_id IN (SELECT id FROM issue_types WHERE level = -1)
		ORDER BY id`, issueID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// deleteIssue removes an issue (locked by the caller) and its sub-tasks within tx and returns the sub-tasks; b
// collects the notifications of a bulk change (nil: they are sent).
func (s *Service) deleteIssue(ctx context.Context, tx pgx.Tx, a *Access, p *identity.Principal, cfg *configSnapshot, cur *Issue,
	b *bulkItem) ([]int64, error) {
	if !a.CanIssue(cur.head(), PermDelete) {
		return nil, apperr.Forbidden("issue.delete-denied", "You may not delete %s.", cur.Key)
	}
	// The sub-tasks go too: each must be one the caller sees and may delete.
	subs, err := subtaskIDs(ctx, tx, cur.ID)
	if err != nil {
		return nil, err
	}
	for _, id := range subs {
		sub, err := s.loadIssue(ctx, tx, cfg, id, true)
		if err != nil {
			return nil, err
		}
		if !a.visible(sub.head()) || !a.CanIssue(sub.head(), PermDelete) {
			return nil, apperr.Forbidden("issue.delete-subtasks", "%s has sub-tasks you may not delete.", cur.Key)
		}
	}
	uid := p.UserID()
	if err := s.notifyChange(ctx, tx, b, cur, EventDeleted, uid, "deleted", "", nil); err != nil {
		return nil, err
	}
	ids := append([]int64{cur.ID}, subs...)
	// Each statement gets the values it uses: PostgreSQL cannot type a parameter a statement does not use.
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE issues SET parent_id = NULL WHERE parent_id = ANY($1) AND NOT (id = ANY($1))", []any{ids}},
		{"DELETE FROM watchers WHERE entity_type = 'issue' AND entity_id = ANY($1)", []any{ids}},
		{"DELETE FROM blob_refs WHERE owner_type = 'issue-attachment' AND owner_id IN (SELECT id FROM issue_attachments WHERE issue_id = ANY($1))",
			[]any{ids}},
		{"DELETE FROM issues WHERE id = ANY($1) AND id <> $2", []any{ids, cur.ID}}, // the sub-tasks, before their parent
		{"DELETE FROM issues WHERE id = $1", []any{cur.ID}},
	} {
		if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
			return nil, err
		}
	}
	if err := s.recordActivity(ctx, tx, cur, uid, "issue.deleted", nil); err != nil {
		return nil, err
	}
	return subs, s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.deleted", IssueID: cur.ID, Key: cur.Key, ProjectID: cur.projectID,
		ActorID: uid})
}

// refreshSearch rebuilds the weighted text vector of an issue (keys and summary A, description B, comments C, rest
// D); former keys of moved issues are found too. Comments restricted to a role or group are left out: everyone who
// sees the issue can search it.
func (s *Service) refreshSearch(ctx context.Context, tx pgx.Tx, id int64) error {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issues i SET search_tsv =
		setweight(to_tsvector($2::regconfig, i.key || ' ' || coalesce((SELECT string_agg(k.key, ' ') FROM issue_keys k
			WHERE k.issue_id = i.id), '') || ' ' || i.summary), 'A') ||
		setweight(to_tsvector($2::regconfig, i.description_text), 'B') ||
		setweight(to_tsvector($2::regconfig, coalesce((SELECT string_agg(c.body_text, ' ') FROM issue_comments c
			WHERE c.issue_id = i.id AND c.visibility_type IS NULL), '')), 'C') ||
		setweight(to_tsvector($2::regconfig, i.environment || ' ' || array_to_string(i.labels, ' ') || ' ' ||
			coalesce((SELECT string_agg(e.value #>> '{}', ' ') FROM jsonb_each(i.fields) e
				WHERE jsonb_typeof(e.value) = 'string'), '')), 'D')
		WHERE i.id = $1`, id, st.ContentLanguage)
	return err
}

// recordActivity appends to the activity of the issue's project; data is what the change was (activityData).
func (s *Service) recordActivity(ctx context.Context, tx pgx.Tx, i *Issue, actorID int64, verb string, data *activityData) error {
	var actor *int64
	if actorID != 0 {
		actor = &actorID
	}
	var d map[string]any
	if data != nil {
		raw, _ := json.Marshal(data)
		_ = json.Unmarshal(raw, &d)
	}
	return notify.RecordActivity(ctx, tx, notify.Activity{ActorID: actor, Verb: verb, EntityType: "issue", EntityID: i.ID,
		ContainerType: "project", ContainerID: i.projectID, Title: i.Key + " " + i.Summary, URL: "/browse/" + i.Key, Data: d})
}

// IssueEvent is published for issue changes.
type IssueEvent struct {
	Name      string
	IssueID   int64
	Key       string
	ProjectID int64
	ActorID   int64
	Fields    []string
	From, To  int64 // statuses of transitions
	// FromKey and FromProjectID are the key and project of a moved issue before the move.
	FromKey       string
	FromProjectID int64
}

// EventName implements events.Event.
func (e IssueEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live. The container names the issue, so that issues with a security level reach
// only those who see them.
func (e IssueEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "issue", ID: e.Key, Container: "issue:" + itoa(e.ProjectID) + ":" + itoa(e.IssueID)}
}

// IssueVisibility is the live-update visibility check for project ("project:<id>") and issue
// ("issue:<project>:<id>") containers. The issue is read only in projects with a security scheme.
func (s *Service) IssueVisibility(ctx context.Context, userID int64, groups []int64, container string) bool {
	kind, rest, _ := strings.Cut(container, ":")
	project, issue, _ := strings.Cut(rest, ":")
	pid, err := strconv.ParseInt(project, 10, 64)
	if err != nil || (kind != "project" && kind != "issue") {
		return false
	}
	a, err := s.Access(ctx, &identity.Principal{User: &identity.User{ID: userID}, Groups: groups, Method: identity.MethodInternal})
	if err != nil || !a.CanAny(pid, PermBrowse) {
		return false
	}
	if kind == "project" || !a.secured[pid] {
		return true
	}
	id, err := strconv.ParseInt(issue, 10, 64)
	if err != nil {
		return false
	}
	h := issueHead{id: id, projectID: pid}
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(reporter_id, 0), coalesce(assignee_id, 0), coalesce(security_level_id, 0)
		FROM issues WHERE id = $1 AND project_id = $2`, id, pid).Scan(&h.reporterID, &h.assigneeID, &h.securityLevelID); err != nil {
		return false
	}
	return a.visible(h)
}

// now is replaceable in tests.
var now = time.Now

// ExistingProjectKeys returns which of the keys are project keys (current or former), for issue keys found in
// commits, branches and pull requests.
func (s *Service) ExistingProjectKeys(ctx context.Context, q db.Querier, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT key FROM issue_project_keys WHERE key = ANY($1)", keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// DevelopmentChanged tells browsers showing the issues that their development information (commits, branches,
// pull requests) changed. The messages go to the issues' containers, so that only who sees an issue hears of it
// (audit L11). The keys may be former keys of the issues.
func (s *Service) DevelopmentChanged(ctx context.Context, tx pgx.Tx, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id, key, project_id FROM issues WHERE key = ANY($1)
		OR id IN (SELECT issue_id FROM issue_keys WHERE key = ANY($1))`, keys)
	if err != nil {
		return err
	}
	type ref struct {
		id      int64
		key     string
		project int64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.key, &r.project); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	rows.Close()
	for _, r := range refs {
		m := IssueEvent{Key: r.key, IssueID: r.id, ProjectID: r.project}.LiveMessage()
		if err := events.NotifyLive(ctx, tx, m); err != nil {
			return err
		}
	}
	return nil
}

// KeysOf returns all keys of an issue, the current one first: commits, branches, pull requests and pages may name it
// by a key it had before it moved to another project or its project's key changed. It does not check permissions.
func (s *Service) KeysOf(ctx context.Context, q db.Querier, issueID int64) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT key FROM (SELECT key, 0 AS o FROM issues WHERE id = $1
		UNION ALL SELECT key, 1 FROM issue_keys WHERE issue_id = $1) k ORDER BY o, key`, issueID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// VisibleIssueKeysOf returns all keys of the issue with a key (current or former) if the principal sees it, the
// current one first; otherwise just the key, so that what names it by that key is found and nothing more.
func (s *Service) VisibleIssueKeysOf(ctx context.Context, p *identity.Principal, key string) ([]string, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.visibleIssueByKey(ctx, s.pool, cfg, a, key)
	if err != nil || i == nil {
		return []string{key}, err
	}
	keys, err := s.KeysOf(ctx, s.pool, i.ID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(keys, key) {
		keys = append(keys, key)
	}
	return keys, nil
}

// CurrentKey returns the current key of the issue with a (current or former) key that the principal sees.
func (s *Service) CurrentKey(ctx context.Context, p *identity.Principal, key string) (string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", err
	}
	i, _, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return "", err
	}
	return i.Key, nil
}
