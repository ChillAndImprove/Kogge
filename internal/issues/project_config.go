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
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

type projectType struct {
	TypeID     int64  `json:"issueTypeId"`
	WorkflowID *int64 `json:"workflowId,omitempty" doc:"Override of the project's default workflow"`
	Position   int    `json:"position,omitempty"`
}

func (s *Service) projectIssueTypes(ctx context.Context, q db.Querier, projectID int64) ([]projectType, error) {
	rows, err := q.Query(ctx, `SELECT issue_type_id, workflow_id, position FROM project_issue_types WHERE project_id = $1
		ORDER BY position, issue_type_id`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (projectType, error) {
		var t projectType
		err := row.Scan(&t.TypeID, &t.WorkflowID, &t.Position)
		return t, err
	})
}

// workflowFor returns the workflow of an issue type in a project.
func (s *Service) workflowFor(ctx context.Context, q db.Querier, cfg *configSnapshot, projectID, typeID int64) (*workflowRecord, error) {
	var wf *int64
	var def int64
	err := q.QueryRow(ctx, `SELECT pit.workflow_id, p.default_workflow_id FROM issue_projects p
		LEFT JOIN project_issue_types pit ON pit.project_id = p.id AND pit.issue_type_id = $2 WHERE p.id = $1`,
		projectID, typeID).Scan(&wf, &def)
	if err != nil {
		return nil, err
	}
	id := def
	if wf != nil {
		id = *wf
	}
	w := cfg.workflows[id]
	if w == nil {
		return nil, apperr.Internal(errNoWorkflow)
	}
	return w, nil
}

type configError string

func (e configError) Error() string { return string(e) }

const errNoWorkflow = configError("the project's workflow does not exist")

// checkWorkflowCompatible verifies that the issues of a project (and type; 0 = all types without an override)
// are in statuses of the new workflow.
func (s *Service) checkWorkflowCompatible(ctx context.Context, tx pgx.Tx, projectID, typeID, workflowID int64) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	w := cfg.workflows[workflowID]
	if w == nil {
		return apperr.Invalid("workflow.not-found", "The workflow does not exist.")
	}
	var missing []string
	rows, err := tx.Query(ctx, `SELECT DISTINCT i.status_id FROM issues i
		LEFT JOIN project_issue_types pit ON pit.project_id = i.project_id AND pit.issue_type_id = i.type_id
		WHERE i.project_id = $1 AND (($2 = 0 AND pit.workflow_id IS NULL) OR i.type_id = $2)`, projectID, typeID)
	if err != nil {
		return err
	}
	statusIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range statusIDs {
		if !w.Def.HasStatus(id) {
			if st := cfg.statuses[id]; st != nil {
				missing = append(missing, st.Name)
			}
		}
	}
	if len(missing) > 0 {
		return apperr.Conflict("workflow.incompatible", "Issues are in statuses the workflow does not have: %s. "+
			"Transition or move them first.", strings.Join(missing, ", "))
	}
	return nil
}

// ProjectIssueTypes returns the issue types of a project with their workflows.
func (s *Service) ProjectIssueTypes(ctx context.Context, p *identity.Principal, key string) ([]projectType, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
		return nil, err
	}
	out, err := s.projectIssueTypes(ctx, s.pool, proj.ID)
	if out == nil {
		out = []projectType{}
	}
	return out, err
}

// SetProjectIssueTypes replaces the issue types of a project (and their workflow overrides).
func (s *Service) SetProjectIssueTypes(ctx context.Context, p *identity.Principal, key string, types []projectType) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		if len(types) == 0 {
			return apperr.Invalid("project.issue-types", "A project needs at least one issue type.")
		}
		keep := []int64{}
		for _, t := range types {
			if cfg.types[t.TypeID] == nil {
				return apperr.Invalidf("project.issue-types", "Issue type %d does not exist.", t.TypeID)
			}
			keep = append(keep, t.TypeID)
		}
		var inUse []string
		rows, err := tx.Query(ctx, `SELECT DISTINCT type_id FROM issues WHERE project_id = $1 AND NOT (type_id = ANY($2))`, proj.ID, keep)
		if err != nil {
			return err
		}
		removed, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range removed {
			inUse = append(inUse, cfg.types[id].Name)
		}
		if len(inUse) > 0 {
			return apperr.Conflict("project.issue-type-in-use", "Issues of these types exist: %s.", strings.Join(inUse, ", "))
		}
		for _, t := range types {
			wf := proj.DefaultWorkflowID
			if t.WorkflowID != nil {
				wf = *t.WorkflowID
			}
			if err := s.checkWorkflowCompatible(ctx, tx, proj.ID, t.TypeID, wf); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM project_issue_types WHERE project_id = $1", proj.ID); err != nil {
			return err
		}
		for pos, t := range types {
			if _, err := tx.Exec(ctx, `INSERT INTO project_issue_types (project_id, issue_type_id, workflow_id, position)
				VALUES ($1, $2, $3, $4)`, proj.ID, t.TypeID, t.WorkflowID, pos); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "project.issue-types", TargetType: "project", TargetID: proj.Key})
	})
	if err == nil {
		s.afterCommit()
	}
	return err
}

// --- roles ---

// RoleMember is a user or group in a project role.
type RoleMember struct {
	Type string `json:"type" enum:"user,group"`
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty" readOnly:"true"`
}

// RoleMembers lists a role with its members in a project.
type RoleMembers struct {
	Role    Role         `json:"role"`
	Members []RoleMember `json:"members"`
}

// ProjectRoles returns the members of all roles of a project.
func (s *Service) ProjectRoles(ctx context.Context, p *identity.Principal, key string) ([]RoleMembers, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	perms, _, err := s.ProjectPermissions(ctx, p, proj.ID)
	if err != nil {
		return nil, err
	}
	if len(perms) == 0 {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoleMembers, 0, len(cfg.roleList))
	idx := map[int64]int{}
	for i, r := range cfg.roleList {
		out = append(out, RoleMembers{Role: *r, Members: []RoleMember{}})
		idx[r.ID] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT m.role_id, m.principal_type, m.principal_id, coalesce(u.display_name, g.name, '')
		FROM project_role_members m LEFT JOIN users u ON m.principal_type = 'user' AND u.id = m.principal_id
		LEFT JOIN groups g ON m.principal_type = 'group' AND g.id = m.principal_id WHERE m.project_id = $1
		ORDER BY m.principal_type DESC, 4`, proj.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var roleID int64
		var m RoleMember
		if err := rows.Scan(&roleID, &m.Type, &m.ID, &m.Name); err != nil {
			return nil, err
		}
		if i, ok := idx[roleID]; ok {
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out, rows.Err()
}

// ChangeRoleMember adds or removes a user or group in a project role.
func (s *Service) ChangeRoleMember(ctx context.Context, p *identity.Principal, key string, roleID int64, m RoleMember, add bool) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		if add {
			_, err = tx.Exec(ctx, `INSERT INTO project_role_members (project_id, role_id, principal_type, principal_id)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, proj.ID, roleID, m.Type, m.ID)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM project_role_members WHERE project_id = $1 AND role_id = $2
				AND principal_type = $3 AND principal_id = $4`, proj.ID, roleID, m.Type, m.ID)
		}
		if db.IsForeignKeyViolation(err) {
			return apperr.NotFound("role.not-found", "The role does not exist.")
		}
		if err != nil {
			return err
		}
		if err := s.permissionsChanged(ctx, tx); err != nil {
			return err
		}
		action := "project.role.remove"
		if add {
			action = "project.role.add"
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: action, TargetType: "project", TargetID: proj.Key,
			Details: map[string]any{"role": roleID, "type": m.Type, "id": m.ID}})
	})
	if err == nil {
		s.afterCommit()
	}
	return err
}

// --- components ---

func (s *Service) listComponents(ctx context.Context, q db.Querier, projectID int64) ([]Component, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.project_id, c.name, c.description, c.lead_id, u.username, u.display_name,
		c.default_assignee, c.archived, (SELECT count(*) FROM issue_components ic WHERE ic.component_id = c.id)
		FROM components c LEFT JOIN users u ON u.id = c.lead_id WHERE c.project_id = $1 ORDER BY lower(c.name)`, projectID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Component, error) {
		var c Component
		var leadID *int64
		var lu, ln *string
		err := row.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Description, &leadID, &lu, &ln, &c.DefaultAssignee, &c.Archived, &c.Issues)
		if leadID != nil {
			c.Lead = &identity.UserRef{ID: *leadID, Username: *lu, DisplayName: *ln, Active: true}
		}
		return c, err
	})
	if out == nil {
		out = []Component{}
	}
	return out, err
}

// ComponentInput creates or changes a component.
type ComponentInput struct {
	Name            string `json:"name" minLength:"1" maxLength:"100"`
	Description     string `json:"description,omitempty" maxLength:"2000"`
	LeadID          int64  `json:"leadId,omitempty"`
	DefaultAssignee string `json:"defaultAssignee,omitempty" enum:"none,component-lead,project-lead,"`
	Archived        bool   `json:"archived,omitempty"`
}

// SaveComponent creates (id 0) or updates a component.
func (s *Service) SaveComponent(ctx context.Context, p *identity.Principal, key string, id int64, in ComponentInput) (*Component, error) {
	var out *Component
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		if in.DefaultAssignee == "" {
			in.DefaultAssignee = "none"
		}
		var lead any
		if in.LeadID != 0 {
			lead = in.LeadID
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO components (project_id, name, description, lead_id, default_assignee, archived)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, proj.ID, strings.TrimSpace(in.Name), in.Description, lead,
				in.DefaultAssignee, in.Archived).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE components SET name = $3, description = $4, lead_id = $5, default_assignee = $6,
				archived = $7 WHERE id = $1 AND project_id = $2`, id, proj.ID, strings.TrimSpace(in.Name), in.Description, lead,
				in.DefaultAssignee, in.Archived)
			if err == nil && tag.RowsAffected() == 0 {
				return apperr.NotFound("component.not-found", "The component does not exist.")
			}
		}
		if db.IsUniqueViolation(err, "components_name_key") {
			return apperr.Conflict("component.name-taken", "A component named %s already exists.", in.Name)
		}
		if err != nil {
			return err
		}
		list, err := s.listComponents(ctx, tx, proj.ID)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				out = &list[i]
			}
		}
		return nil
	})
	return out, err
}

// DeleteComponent removes a component from the project and its issues.
func (s *Service) DeleteComponent(ctx context.Context, p *identity.Principal, key string, id int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "DELETE FROM components WHERE id = $1 AND project_id = $2", id, proj.ID)
		return err
	})
}

// --- versions ---

func (s *Service) listVersions(ctx context.Context, q db.Querier, projectID int64) ([]Version, error) {
	rows, err := q.Query(ctx, `SELECT id, project_id, name, description, to_char(start_date, 'YYYY-MM-DD'),
		to_char(release_date, 'YYYY-MM-DD'), released, released_at, archived, position FROM versions WHERE project_id = $1
		ORDER BY position, id`, projectID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
		var v Version
		err := row.Scan(&v.ID, &v.ProjectID, &v.Name, &v.Description, &v.StartDate, &v.ReleaseDate, &v.Released,
			&v.ReleasedAt, &v.Archived, &v.Position)
		return v, err
	})
	if out == nil {
		out = []Version{}
	}
	return out, err
}

// VersionInput creates or changes a version.
type VersionInput struct {
	Name        string  `json:"name" minLength:"1" maxLength:"100"`
	Description string  `json:"description,omitempty" maxLength:"2000"`
	StartDate   *string `json:"startDate,omitempty" format:"date"`
	ReleaseDate *string `json:"releaseDate,omitempty" format:"date"`
	Released    bool    `json:"released,omitempty"`
	Archived    bool    `json:"archived,omitempty"`
}

// SaveVersion creates (id 0) or updates a version.
func (s *Service) SaveVersion(ctx context.Context, p *identity.Principal, key string, id int64, in VersionInput) (*Version, error) {
	var out *Version
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		date := func(v *string) (any, error) {
			if v == nil || *v == "" {
				return nil, nil
			}
			if _, err := time.Parse("2006-01-02", *v); err != nil {
				return nil, apperr.Invalid("version.date", "Dates must be YYYY-MM-DD.")
			}
			return *v, nil
		}
		start, err := date(in.StartDate)
		if err != nil {
			return err
		}
		release, err := date(in.ReleaseDate)
		if err != nil {
			return err
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO versions (project_id, name, description, start_date, release_date, released,
				released_at, archived, position) VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6 THEN now() END, $7,
				coalesce((SELECT max(position) + 1 FROM versions WHERE project_id = $1), 0)) RETURNING id`,
				proj.ID, strings.TrimSpace(in.Name), in.Description, start, release, in.Released, in.Archived).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE versions SET name = $3, description = $4, start_date = $5, release_date = $6,
				released_at = CASE WHEN $7 AND NOT released THEN now() WHEN NOT $7 THEN NULL ELSE released_at END,
				released = $7, archived = $8 WHERE id = $1 AND project_id = $2`, id, proj.ID, strings.TrimSpace(in.Name),
				in.Description, start, release, in.Released, in.Archived)
			if err == nil && tag.RowsAffected() == 0 {
				return apperr.NotFound("version.not-found", "The version does not exist.")
			}
		}
		if db.IsUniqueViolation(err, "versions_name_key") {
			return apperr.Conflict("version.name-taken", "A version named %s already exists.", in.Name)
		}
		if err != nil {
			return err
		}
		list, err := s.listVersions(ctx, tx, proj.ID)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				out = &list[i]
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "version.save", TargetType: "version",
			TargetID: itoa(id), Details: map[string]any{"name": in.Name, "released": in.Released}})
	})
	return out, err
}

// DeleteVersion removes a version; its issues can be moved to another version.
func (s *Service) DeleteVersion(ctx context.Context, p *identity.Principal, key string, id, moveTo int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		// Both versions must belong to the project: the ids come from the request.
		for _, v := range []int64{id, moveTo} {
			var ok bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM versions WHERE id = $1 AND project_id = $2)", v, proj.ID).Scan(&ok); err != nil {
				return err
			}
			if !ok && v != 0 {
				return apperr.NotFound("version.not-found", "The project %s has no version %d.", proj.Key, v)
			}
		}
		if moveTo == id {
			return apperr.Invalid("version.move", "The issues cannot move to the version that is deleted.")
		}
		if moveTo != 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO issue_versions (issue_id, version_id, kind)
				SELECT issue_id, $2, kind FROM issue_versions WHERE version_id = $1 ON CONFLICT DO NOTHING`, id, moveTo); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "DELETE FROM versions WHERE id = $1 AND project_id = $2", id, proj.ID)
		return err
	})
}

// --- field layouts ---

// LayoutItem configures one field in a layout.
type LayoutItem struct {
	Field    string `json:"field"`
	Create   bool   `json:"create"`
	Edit     bool   `json:"edit"`
	View     bool   `json:"view"`
	Required bool   `json:"required"`
}

// defaultLayout shows the common system fields and all applicable custom fields.
func defaultLayout(cfg *configSnapshot, projectID, typeID int64) []LayoutItem {
	out := []LayoutItem{
		{Field: FieldSummary, Create: true, Edit: true, View: true, Required: true},
		{Field: FieldDescription, Create: true, Edit: true, View: true},
		{Field: FieldPriority, Create: true, Edit: true, View: true},
		{Field: FieldAssignee, Create: true, Edit: true, View: true},
		{Field: FieldLabels, Create: true, Edit: true, View: true},
		{Field: FieldComponents, Create: true, Edit: true, View: true},
		{Field: FieldFixVersions, Create: true, Edit: true, View: true},
		{Field: FieldAffectsVersions, Create: false, Edit: true, View: true},
		{Field: FieldDueDate, Create: true, Edit: true, View: true},
		{Field: FieldParent, Create: true, Edit: true, View: true},
		{Field: FieldOriginalEstimate, Create: true, Edit: true, View: true},
		{Field: FieldRemainingEstimate, Create: false, Edit: true, View: true},
		{Field: FieldReporter, Create: false, Edit: true, View: true},
		{Field: FieldSecurity, Create: true, Edit: true, View: true},
		{Field: FieldEnvironment, Create: false, Edit: true, View: false},
	}
	for _, f := range cfg.fieldList {
		if fieldApplies(f, projectID, typeID) {
			out = append(out, LayoutItem{Field: f.Key, Create: true, Edit: true, View: true})
		}
	}
	return out
}

// FieldLayout returns the layout for a project and issue type (0 = default).
func (s *Service) FieldLayout(ctx context.Context, projectID, typeID int64) ([]LayoutItem, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT layout FROM project_field_layouts WHERE project_id = $1 AND issue_type_id IN ($2, 0)
		ORDER BY issue_type_id DESC LIMIT 1`, projectID, typeID).Scan(&raw)
	if db.IsNoRows(err) {
		return defaultLayout(cfg, projectID, typeID), nil
	}
	if err != nil {
		return nil, err
	}
	var items []LayoutItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	// Custom fields added after the layout was saved appear at the end.
	present := map[string]bool{}
	for _, it := range items {
		present[it.Field] = true
	}
	for _, f := range cfg.fieldList {
		if fieldApplies(f, projectID, typeID) && !present[f.Key] {
			items = append(items, LayoutItem{Field: f.Key, Edit: true, View: true})
		}
	}
	return items, nil
}

// SetFieldLayout saves the layout of a project and issue type (0 = default).
func (s *Service) SetFieldLayout(ctx context.Context, p *identity.Principal, key string, typeID int64, items []LayoutItem) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
			return err
		}
		hasSummary := false
		for i := range items {
			if items[i].Field == FieldSummary {
				hasSummary = true
				items[i].Create, items[i].Edit, items[i].View, items[i].Required = true, true, true, true
			}
		}
		if !hasSummary {
			items = append([]LayoutItem{{Field: FieldSummary, Create: true, Edit: true, View: true, Required: true}}, items...)
		}
		b, _ := json.Marshal(items)
		_, err = tx.Exec(ctx, `INSERT INTO project_field_layouts (project_id, issue_type_id, layout) VALUES ($1, $2, $3)
			ON CONFLICT (project_id, issue_type_id) DO UPDATE SET layout = EXCLUDED.layout`, proj.ID, typeID, b)
		return err
	})
}

// ProjectStatuses returns the statuses of the workflows a project uses (project administrators).
func (s *Service) ProjectStatuses(ctx context.Context, p *identity.Principal, key string) ([]Status, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.projectStatuses(ctx, s.pool, cfg, proj.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(list))
	for _, st := range list {
		out = append(out, *st)
	}
	return out, nil
}
