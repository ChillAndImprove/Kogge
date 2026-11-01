// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Config is the issue configuration visible to all signed-in users (for forms and the query builder).
type Config struct {
	IssueTypes  []*IssueType  `json:"issueTypes"`
	Statuses    []*Status     `json:"statuses"`
	Priorities  []*Priority   `json:"priorities"`
	Resolutions []*Resolution `json:"resolutions"`
	LinkTypes   []*LinkType   `json:"linkTypes"`
	Roles       []*Role       `json:"roles"`
	Fields      []FieldInfo   `json:"fields"`
	Permissions []string      `json:"permissions" doc:"The project permission catalogue"`
	Events      []string      `json:"events" doc:"Notification events"`
	// TimeTracking holds the units of durations such as 1d or 1w.
	TimeTracking TimeTracking `json:"timeTracking"`
}

// TimeTracking holds the units of the instance's durations.
type TimeTracking struct {
	HoursPerDay int `json:"hoursPerDay" doc:"Working hours per day: 1d"`
	DaysPerWeek int `json:"daysPerWeek" doc:"Working days per week: 1w"`
}

// Configuration returns the issue configuration.
func (s *Service) Configuration(ctx context.Context) (*Config, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := &Config{TimeTracking: TimeTracking{HoursPerDay: st.HoursPerDay, DaysPerWeek: st.DaysPerWeek},
		IssueTypes: nonNilPtrs(cfg.typeList), Statuses: nonNilPtrs(cfg.statusList), Priorities: nonNilPtrs(cfg.prioList),
		Resolutions: nonNilPtrs(cfg.resList), LinkTypes: nonNilPtrs(cfg.linkList), Roles: nonNilPtrs(cfg.roleList),
		Fields: append([]FieldInfo{}, systemFields...), Permissions: ProjectPermissions, Events: NotificationEvents}
	for _, f := range cfg.fieldList {
		out.Fields = append(out.Fields, FieldInfo{Key: f.Key, Name: f.Name, Type: f.Type, Custom: true, Editable: true,
			Orderable:   f.Type == "number" || f.Type == "date" || f.Type == "datetime" || f.Type == "text" || f.Type == "select",
			Description: f.Description, Options: f.Options, ProjectIDs: f.ProjectIDs, IssueTypeIDs: f.IssueTypeIDs})
	}
	return out, nil
}

func nonNilPtrs[T any](s []*T) []*T {
	if s == nil {
		return []*T{}
	}
	return s
}

func (s *Service) admin(ctx context.Context) (*identity.Principal, error) {
	return s.authz.RequireGlobal(ctx, authz.Admin)
}

// configTx runs an administrative configuration change and invalidates caches on all nodes.
func (s *Service) configTx(ctx context.Context, action, target string, fn func(tx pgx.Tx) error) error {
	actor, err := s.admin(ctx)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := s.configChanged(ctx, tx); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: action, TargetType: "issue-config", TargetID: target})
	})
	if err == nil {
		s.afterCommit()
	}
	return err
}

// uniqueErr turns the violation of a unique name into the message (with %s for the name).
func uniqueErr(err error, message, name string) error {
	if db.IsUniqueViolation(err, "") {
		if name == "" {
			return apperr.Conflict("config.name-taken", "An object with this name already exists.")
		}
		return apperr.Conflict("config.name-taken", message, name)
	}
	if db.IsForeignKeyViolation(err) {
		return apperr.Conflict("config.in-use", "It is still in use.")
	}
	return err
}

// SaveIssueType creates (id 0) or updates an issue type.
func (s *Service) SaveIssueType(ctx context.Context, id int64, t IssueType) (int64, error) {
	if t.Level < -1 || t.Level > 1 {
		return 0, apperr.Invalid("issue-type.level", "The level must be 1 (epic), 0 (standard) or -1 (sub-task).")
	}
	err := s.configTx(ctx, "issue-type.save", t.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO issue_types (name, description, icon, color, level, position) VALUES ($1, $2, $3, $4, $5,
				coalesce((SELECT max(position) + 1 FROM issue_types), 1)) RETURNING id`, t.Name, t.Description, t.Icon, t.Color, t.Level).Scan(&id)
		} else {
			var used bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM issues WHERE type_id = $1) AND (SELECT level FROM issue_types WHERE id = $1) <> $2",
				id, t.Level).Scan(&used); err != nil {
				return err
			}
			if used {
				return apperr.Conflict("issue-type.level-in-use", "The level of a type with issues cannot change.")
			}
			_, err = tx.Exec(ctx, "UPDATE issue_types SET name = $2, description = $3, icon = $4, color = $5, level = $6 WHERE id = $1",
				id, t.Name, t.Description, t.Icon, t.Color, t.Level)
		}
		return uniqueErr(err, "An issue type named %s already exists.", t.Name)
	})
	return id, err
}

// SaveStatus creates (id 0) or updates a status.
func (s *Service) SaveStatus(ctx context.Context, id int64, st Status) (int64, error) {
	err := s.configTx(ctx, "status.save", st.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO statuses (name, description, category, position) VALUES ($1, $2, $3,
				coalesce((SELECT max(position) + 1 FROM statuses), 1)) RETURNING id`, st.Name, st.Description, st.Category).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE statuses SET name = $2, description = $3, category = $4 WHERE id = $1", id, st.Name, st.Description, st.Category)
		}
		return uniqueErr(err, "A status named %s already exists.", st.Name)
	})
	return id, err
}

// SavePriority creates (id 0) or updates a priority.
func (s *Service) SavePriority(ctx context.Context, id int64, pr Priority) (int64, error) {
	err := s.configTx(ctx, "priority.save", pr.Name, func(tx pgx.Tx) error {
		var err error
		if pr.IsDefault {
			if _, err := tx.Exec(ctx, "UPDATE priorities SET is_default = false"); err != nil {
				return err
			}
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO priorities (name, description, icon, color, is_default, position) VALUES ($1, $2, $3, $4, $5,
				coalesce((SELECT max(position) + 1 FROM priorities), 1)) RETURNING id`, pr.Name, pr.Description, pr.Icon, pr.Color, pr.IsDefault).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE priorities SET name = $2, description = $3, icon = $4, color = $5, is_default = $6 WHERE id = $1",
				id, pr.Name, pr.Description, pr.Icon, pr.Color, pr.IsDefault)
		}
		return uniqueErr(err, "A priority named %s already exists.", pr.Name)
	})
	return id, err
}

// SaveResolution creates (id 0) or updates a resolution.
func (s *Service) SaveResolution(ctx context.Context, id int64, r Resolution) (int64, error) {
	err := s.configTx(ctx, "resolution.save", r.Name, func(tx pgx.Tx) error {
		var err error
		if r.IsDefault {
			if _, err := tx.Exec(ctx, "UPDATE resolutions SET is_default = false"); err != nil {
				return err
			}
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO resolutions (name, description, is_default, position) VALUES ($1, $2, $3,
				coalesce((SELECT max(position) + 1 FROM resolutions), 1)) RETURNING id`, r.Name, r.Description, r.IsDefault).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE resolutions SET name = $2, description = $3, is_default = $4 WHERE id = $1", id, r.Name, r.Description, r.IsDefault)
		}
		return uniqueErr(err, "A resolution named %s already exists.", r.Name)
	})
	return id, err
}

// SaveLinkType creates (id 0) or updates a link type.
func (s *Service) SaveLinkType(ctx context.Context, id int64, l LinkType) (int64, error) {
	err := s.configTx(ctx, "link-type.save", l.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO issue_link_types (name, outward, inward) VALUES ($1, $2, $3) RETURNING id", l.Name, l.Outward, l.Inward).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE issue_link_types SET name = $2, outward = $3, inward = $4 WHERE id = $1", id, l.Name, l.Outward, l.Inward)
		}
		return uniqueErr(err, "A link type named %s already exists.", l.Name)
	})
	return id, err
}

// SaveRole creates (id 0) or updates a project role.
func (s *Service) SaveRole(ctx context.Context, id int64, r Role) (int64, error) {
	err := s.configTx(ctx, "role.save", r.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO project_roles (name, description) VALUES ($1, $2) RETURNING id", r.Name, r.Description).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE project_roles SET name = $2, description = $3 WHERE id = $1", id, r.Name, r.Description)
		}
		return uniqueErr(err, "A role named %s already exists.", r.Name)
	})
	return id, err
}

// DeleteConfig removes a configuration object that is not in use.
func (s *Service) DeleteConfig(ctx context.Context, kind string, id int64) error {
	inUse := map[string][]string{
		"issue-types":          {"SELECT EXISTS (SELECT 1 FROM issues WHERE type_id = $1) OR EXISTS (SELECT 1 FROM project_issue_types WHERE issue_type_id = $1)", "DELETE FROM issue_types WHERE id = $1"},
		"statuses":             {"SELECT EXISTS (SELECT 1 FROM issues WHERE status_id = $1) OR EXISTS (SELECT 1 FROM workflows WHERE definition->'statuses' @> to_jsonb($1::bigint))", "DELETE FROM statuses WHERE id = $1"},
		"priorities":           {"SELECT EXISTS (SELECT 1 FROM issues WHERE priority_id = $1)", "DELETE FROM priorities WHERE id = $1"},
		"resolutions":          {"SELECT EXISTS (SELECT 1 FROM issues WHERE resolution_id = $1)", "DELETE FROM resolutions WHERE id = $1"},
		"link-types":           {"SELECT false", "DELETE FROM issue_link_types WHERE id = $1"},
		"roles":                {"SELECT EXISTS (SELECT 1 FROM permission_scheme_grants WHERE holder_type = 'role' AND holder_id = $1)", "DELETE FROM project_roles WHERE id = $1"},
		"fields":               {"SELECT false", "DELETE FROM custom_fields WHERE id = $1"},
		"workflows":            {"SELECT EXISTS (SELECT 1 FROM issue_projects WHERE default_workflow_id = $1) OR EXISTS (SELECT 1 FROM project_issue_types WHERE workflow_id = $1)", "DELETE FROM workflows WHERE id = $1"},
		"permission-schemes":   {"SELECT EXISTS (SELECT 1 FROM issue_projects WHERE permission_scheme_id = $1)", "DELETE FROM permission_schemes WHERE id = $1"},
		"notification-schemes": {"SELECT EXISTS (SELECT 1 FROM issue_projects WHERE notification_scheme_id = $1)", "DELETE FROM notification_schemes WHERE id = $1"},
		"security-schemes":     {"SELECT EXISTS (SELECT 1 FROM issue_projects WHERE security_scheme_id = $1)", "DELETE FROM security_schemes WHERE id = $1"},
	}
	sqls, ok := inUse[kind]
	if !ok {
		return apperr.NotFound("config.not-found", "Unknown configuration type %s.", kind)
	}
	return s.configTx(ctx, kind+".delete", itoa(id), func(tx pgx.Tx) error {
		var used bool
		if err := tx.QueryRow(ctx, sqls[0], id).Scan(&used); err != nil {
			return err
		}
		if used {
			return apperr.Conflict("config.in-use", "It is still in use and cannot be deleted.")
		}
		if kind == "fields" {
			if _, err := tx.Exec(ctx, "UPDATE issues SET fields = fields - $1 WHERE fields ? $1", fieldKey(id)); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, sqls[1], id)
		if err == nil && tag.RowsAffected() == 0 {
			return apperr.NotFound("config.not-found", "It does not exist.")
		}
		return uniqueErr(err, "An object with this name already exists.", "")
	})
}

// CustomFieldInput creates or changes a custom field.
type CustomFieldInput struct {
	Name         string         `json:"name" minLength:"1" maxLength:"100"`
	Description  string         `json:"description,omitempty" maxLength:"1000"`
	Type         string         `json:"type" enum:"text,textarea,number,date,datetime,select,multiselect,checkboxes,radio,cascading,user,multiuser,group,labels,url,version,multiversion"`
	Options      []Option       `json:"options,omitempty" doc:"Choices; new options have id 0 or a negative temporary id that child options of a cascading field reference as parentId"`
	Config       map[string]any `json:"config,omitempty"`
	ProjectIDs   []int64        `json:"projectIds,omitempty"`
	IssueTypeIDs []int64        `json:"issueTypeIds,omitempty"`
}

// SaveCustomField creates (id 0) or updates a custom field. Option ids are assigned to new options; existing
// options keep their ids, removed options are kept disabled if they are in use.
func (s *Service) SaveCustomField(ctx context.Context, id int64, in CustomFieldInput) (int64, error) {
	err := s.configTx(ctx, "field.save", in.Name, func(tx pgx.Tx) error {
		var existing []Option
		if id != 0 {
			var raw []byte
			var typ string
			if err := tx.QueryRow(ctx, "SELECT type, options FROM custom_fields WHERE id = $1", id).Scan(&typ, &raw); err != nil {
				if db.IsNoRows(err) {
					return apperr.NotFound("field.not-found", "The field does not exist.")
				}
				return err
			}
			if typ != in.Type {
				return apperr.Conflict("field.type", "The type of a field cannot change.")
			}
			_ = json.Unmarshal(raw, &existing)
		}
		next := int64(1)
		for _, o := range existing {
			next = max(next, o.ID+1)
		}
		var opts []Option
		remap := map[int64]int64{} // temporary ids of new options -> assigned ids
		for _, o := range in.Options {
			o.Value = strings.TrimSpace(o.Value)
			if o.Value == "" {
				continue
			}
			if o.ID <= 0 || !slices.ContainsFunc(existing, func(e Option) bool { return e.ID == o.ID }) {
				if o.ID != 0 {
					remap[o.ID] = next
				}
				o.ID = next
				next++
			}
			opts = append(opts, o)
		}
		for i := range opts {
			if p, ok := remap[opts[i].ParentID]; ok {
				opts[i].ParentID = p
			}
			if in.Type != "cascading" {
				opts[i].ParentID = 0
			} else if p := opts[i].ParentID; p != 0 && !slices.ContainsFunc(opts, func(o Option) bool { return o.ID == p && o.ParentID == 0 }) {
				return apperr.Invalidf("field.option-parent", "The parent of the option %s does not exist.", opts[i].Value)
			}
		}
		for _, e := range existing {
			if !slices.ContainsFunc(opts, func(o Option) bool { return o.ID == e.ID }) {
				e.Disabled = true
				opts = append(opts, e)
			}
		}
		if opts == nil {
			opts = []Option{}
		}
		if in.Config == nil {
			in.Config = map[string]any{}
		}
		ob, _ := json.Marshal(opts)
		cb, _ := json.Marshal(in.Config)
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO custom_fields (name, description, type, options, config, project_ids, issue_type_ids)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, in.Name, in.Description, in.Type, ob, cb, nonNil(in.ProjectIDs),
				nonNil(in.IssueTypeIDs)).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE custom_fields SET name = $2, description = $3, options = $4, config = $5, project_ids = $6,
				issue_type_ids = $7 WHERE id = $1`, id, in.Name, in.Description, ob, cb, nonNil(in.ProjectIDs), nonNil(in.IssueTypeIDs))
		}
		return uniqueErr(err, "A field named %s already exists.", in.Name)
	})
	return id, err
}

// WorkflowInfo is a workflow with its definition.
type WorkflowInfo struct {
	ID          int64                `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Definition  *workflow.Definition `json:"definition"`
	Projects    []string             `json:"projects" doc:"Keys of the projects using the workflow that the caller can see"`
}

// Workflows lists the workflows with the projects that use them.
func (s *Service) Workflows(ctx context.Context, p *identity.Principal) ([]WorkflowInfo, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	// The projects that use a workflow are those the caller sees; administrators, who change workflows for all
	// projects, see all of them.
	admin, err := s.authz.IsAdmin(ctx, p)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT w.id, coalesce(array_agg(DISTINCT p.key) FILTER (WHERE p.key IS NOT NULL
			AND ($1 OR p.id = ANY($2))), '{}')
		FROM workflows w LEFT JOIN project_issue_types pit ON pit.workflow_id = w.id
		LEFT JOIN issue_projects p ON p.default_workflow_id = w.id OR p.id = pit.project_id AND pit.workflow_id = w.id
		GROUP BY w.id`, admin, a.BrowsableProjects())
	if err != nil {
		return nil, err
	}
	usage := map[int64][]string{}
	for rows.Next() {
		var id int64
		var keys []string
		if err := rows.Scan(&id, &keys); err != nil {
			rows.Close()
			return nil, err
		}
		usage[id] = keys
	}
	rows.Close()
	out := []WorkflowInfo{}
	for _, w := range cfg.workflows {
		ps := usage[w.ID]
		if ps == nil {
			ps = []string{}
		}
		out = append(out, WorkflowInfo{ID: w.ID, Name: w.Name, Description: w.Description, Definition: w.Def, Projects: ps})
	}
	slices.SortFunc(out, func(a, b WorkflowInfo) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// SaveWorkflow creates (id 0) or updates a workflow after validating it. Removing statuses that issues of
// projects using the workflow are in is refused.
func (s *Service) SaveWorkflow(ctx context.Context, id int64, name, description string, def *workflow.Definition) (int64, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return 0, err
	}
	if problems := def.Validate(func(sid int64) bool { return cfg.statuses[sid] != nil }); len(problems) > 0 {
		fields := make([]apperr.FieldError, len(problems))
		for i, p := range problems {
			fields[i] = apperr.FieldError{Location: "body.definition." + p.Path, Message: p.Message}
		}
		return 0, apperr.Invalid("workflow.invalid", "The workflow is not valid.", fields...)
	}
	b, _ := json.Marshal(def)
	err = s.configTx(ctx, "workflow.save", name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO workflows (name, description, definition) VALUES ($1, $2, $3) RETURNING id", name, description, b).Scan(&id)
			return uniqueErr(err, "A workflow named %s already exists.", name)
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT i.status_id FROM issues i JOIN issue_projects p ON p.id = i.project_id
			LEFT JOIN project_issue_types pit ON pit.project_id = i.project_id AND pit.issue_type_id = i.type_id
			WHERE coalesce(pit.workflow_id, p.default_workflow_id) = $1`, id)
		if err != nil {
			return err
		}
		used, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		var missing []string
		for _, sid := range used {
			if !def.HasStatus(sid) {
				missing = append(missing, cfg.statuses[sid].Name)
			}
		}
		if len(missing) > 0 {
			return apperr.Conflict("workflow.status-in-use", "Issues are in statuses that the new version removes: %s.", strings.Join(missing, ", "))
		}
		tag, err := tx.Exec(ctx, "UPDATE workflows SET name = $2, description = $3, definition = $4, version = version + 1, updated_at = now() WHERE id = $1",
			id, name, description, b)
		if err == nil && tag.RowsAffected() == 0 {
			return apperr.NotFound("workflow.not-found", "The workflow does not exist.")
		}
		return uniqueErr(err, "A workflow named %s already exists.", name)
	})
	return id, err
}

// --- permission schemes ---

// PermissionSchemes lists the schemes with their grants.
func (s *Service) PermissionSchemes(ctx context.Context) ([]PermissionScheme, error) {
	if _, err := s.admin(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.name, s.description, (SELECT count(*) FROM issue_projects p WHERE p.permission_scheme_id = s.id)
		FROM permission_schemes s ORDER BY lower(s.name)`)
	if err != nil {
		return nil, err
	}
	schemes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PermissionScheme, error) {
		var ps PermissionScheme
		err := row.Scan(&ps.ID, &ps.Name, &ps.Description, &ps.Projects)
		ps.Grants = []Grant{}
		return ps, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT g.scheme_id, g.permission, g.holder_type, g.holder_id,
		coalesce(r.name, gr.name, u.display_name, '') FROM permission_scheme_grants g
		LEFT JOIN project_roles r ON g.holder_type = 'role' AND r.id = g.holder_id
		LEFT JOIN groups gr ON g.holder_type = 'group' AND gr.id = g.holder_id
		LEFT JOIN users u ON g.holder_type = 'user' AND u.id = g.holder_id ORDER BY g.permission, g.holder_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[int64]int{}
	for i, sc := range schemes {
		idx[sc.ID] = i
	}
	for rows.Next() {
		var sid int64
		var g Grant
		if err := rows.Scan(&sid, &g.Permission, &g.HolderType, &g.HolderID, &g.HolderName); err != nil {
			return nil, err
		}
		if i, ok := idx[sid]; ok {
			schemes[i].Grants = append(schemes[i].Grants, g)
		}
	}
	if schemes == nil {
		schemes = []PermissionScheme{}
	}
	return schemes, rows.Err()
}

// SavePermissionScheme creates (id 0) or replaces a scheme with its grants.
func (s *Service) SavePermissionScheme(ctx context.Context, id int64, name, description string, grants []Grant) (int64, error) {
	for _, g := range grants {
		if !slices.Contains(ProjectPermissions, g.Permission) {
			return 0, apperr.Invalidf("permission.unknown", "Unknown permission %s.", g.Permission)
		}
	}
	err := s.configTx(ctx, "permission-scheme.save", name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO permission_schemes (name, description) VALUES ($1, $2) RETURNING id", name, description).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE permission_schemes SET name = $2, description = $3 WHERE id = $1", id, name, description)
		}
		if err != nil {
			return uniqueErr(err, "A permission scheme named %s already exists.", name)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM permission_scheme_grants WHERE scheme_id = $1", id); err != nil {
			return err
		}
		for _, g := range grants {
			switch g.HolderType {
			case "logged-in", "anyone", "reporter", "assignee", "project-lead":
				g.HolderID = 0
			}
			if _, err := tx.Exec(ctx, `INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, g.Permission, g.HolderType, g.HolderID); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// --- security schemes ---

// SecuritySchemes lists the security schemes with levels and members.
func (s *Service) SecuritySchemes(ctx context.Context) ([]SecurityScheme, error) {
	if _, err := s.admin(ctx); err != nil {
		return nil, err
	}
	return s.securitySchemes(ctx, s.pool)
}

func (s *Service) securitySchemes(ctx context.Context, q db.Querier) ([]SecurityScheme, error) {
	rows, err := q.Query(ctx, "SELECT id, name, description, default_level_id FROM security_schemes ORDER BY lower(name)")
	if err != nil {
		return nil, err
	}
	schemes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SecurityScheme, error) {
		var sc SecurityScheme
		err := row.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.DefaultLevelID)
		sc.Levels = []SecurityLevel{}
		return sc, err
	})
	if err != nil {
		return nil, err
	}
	for i := range schemes {
		rows, err := q.Query(ctx, "SELECT id, name, description FROM security_levels WHERE scheme_id = $1 ORDER BY position, id", schemes[i].ID)
		if err != nil {
			return nil, err
		}
		levels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SecurityLevel, error) {
			var l SecurityLevel
			err := row.Scan(&l.ID, &l.Name, &l.Description)
			l.Members = []LevelMember{}
			return l, err
		})
		if err != nil {
			return nil, err
		}
		for j := range levels {
			rows, err := q.Query(ctx, `SELECT m.holder_type, m.holder_id, coalesce(r.name, g.name, u.display_name, '')
				FROM security_level_members m LEFT JOIN project_roles r ON m.holder_type = 'role' AND r.id = m.holder_id
				LEFT JOIN groups g ON m.holder_type = 'group' AND g.id = m.holder_id
				LEFT JOIN users u ON m.holder_type = 'user' AND u.id = m.holder_id WHERE m.level_id = $1`, levels[j].ID)
			if err != nil {
				return nil, err
			}
			members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LevelMember, error) {
				var m LevelMember
				err := row.Scan(&m.HolderType, &m.HolderID, &m.HolderName)
				return m, err
			})
			if err != nil {
				return nil, err
			}
			if members != nil {
				levels[j].Members = members
			}
		}
		if levels != nil {
			schemes[i].Levels = levels
		}
	}
	if schemes == nil {
		schemes = []SecurityScheme{}
	}
	return schemes, nil
}

// SaveSecurityScheme creates (id 0) or replaces a security scheme with its levels (levels keep their ids when
// given; levels in use cannot be removed).
func (s *Service) SaveSecurityScheme(ctx context.Context, id int64, in SecurityScheme) (int64, error) {
	err := s.configTx(ctx, "security-scheme.save", in.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO security_schemes (name, description) VALUES ($1, $2) RETURNING id", in.Name, in.Description).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE security_schemes SET name = $2, description = $3 WHERE id = $1", id, in.Name, in.Description)
		}
		if err != nil {
			return uniqueErr(err, "A security scheme named %s already exists.", in.Name)
		}
		var keep []int64
		var defaultLevel any
		for pos, l := range in.Levels {
			lid := l.ID // negative ids are temporary ids of new levels
			if lid > 0 {
				tag, err := tx.Exec(ctx, "UPDATE security_levels SET name = $3, description = $4, position = $5 WHERE id = $1 AND scheme_id = $2",
					lid, id, l.Name, l.Description, pos)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					lid = 0
				}
			}
			if lid <= 0 {
				if err := tx.QueryRow(ctx, "INSERT INTO security_levels (scheme_id, name, description, position) VALUES ($1, $2, $3, $4) RETURNING id",
					id, l.Name, l.Description, pos).Scan(&lid); err != nil {
					return err
				}
			}
			keep = append(keep, lid)
			if in.DefaultLevelID != nil && *in.DefaultLevelID == l.ID && l.ID != 0 {
				defaultLevel = lid
			}
			if _, err := tx.Exec(ctx, "DELETE FROM security_level_members WHERE level_id = $1", lid); err != nil {
				return err
			}
			for _, m := range l.Members {
				if m.HolderType == "reporter" || m.HolderType == "assignee" || m.HolderType == "project-lead" {
					m.HolderID = 0
				}
				if _, err := tx.Exec(ctx, `INSERT INTO security_level_members (level_id, holder_type, holder_id) VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`, lid, m.HolderType, m.HolderID); err != nil {
					return err
				}
			}
		}
		var used bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM issues WHERE security_level_id IN (SELECT id FROM security_levels WHERE scheme_id = $1 AND NOT (id = ANY($2))))",
			id, nonNil(keep)).Scan(&used); err != nil {
			return err
		}
		if used {
			return apperr.Conflict("security-level.in-use", "Issues use a level that would be removed.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM security_levels WHERE scheme_id = $1 AND NOT (id = ANY($2))", id, nonNil(keep)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE security_schemes SET default_level_id = $2 WHERE id = $1", id, defaultLevel)
		return err
	})
	return id, err
}

// --- notification schemes ---

// NotificationScheme maps events to recipients.
type NotificationScheme struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Entries     []NotificationEntry `json:"entries"`
}

// NotificationEntry is one recipient of an event.
type NotificationEntry struct {
	Event         string `json:"event"`
	RecipientType string `json:"recipientType" enum:"reporter,assignee,watchers,project-lead,component-lead,user,group,role,user-field"`
	RecipientID   int64  `json:"recipientId,omitempty"`
}

// NotificationSchemes lists the notification schemes.
func (s *Service) NotificationSchemes(ctx context.Context) ([]NotificationScheme, error) {
	if _, err := s.admin(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT id, name, description FROM notification_schemes ORDER BY lower(name)")
	if err != nil {
		return nil, err
	}
	schemes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (NotificationScheme, error) {
		var n NotificationScheme
		err := row.Scan(&n.ID, &n.Name, &n.Description)
		n.Entries = []NotificationEntry{}
		return n, err
	})
	if err != nil {
		return nil, err
	}
	for i := range schemes {
		rows, err := s.pool.Query(ctx, "SELECT event, recipient_type, recipient_id FROM notification_scheme_entries WHERE scheme_id = $1", schemes[i].ID)
		if err != nil {
			return nil, err
		}
		entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[NotificationEntry])
		if err != nil {
			return nil, err
		}
		if entries != nil {
			schemes[i].Entries = entries
		}
	}
	if schemes == nil {
		schemes = []NotificationScheme{}
	}
	return schemes, nil
}

// SaveNotificationScheme creates (id 0) or replaces a notification scheme.
func (s *Service) SaveNotificationScheme(ctx context.Context, id int64, in NotificationScheme) (int64, error) {
	err := s.configTx(ctx, "notification-scheme.save", in.Name, func(tx pgx.Tx) error {
		var err error
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO notification_schemes (name, description) VALUES ($1, $2) RETURNING id", in.Name, in.Description).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE notification_schemes SET name = $2, description = $3 WHERE id = $1", id, in.Name, in.Description)
		}
		if err != nil {
			return uniqueErr(err, "A notification scheme named %s already exists.", in.Name)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM notification_scheme_entries WHERE scheme_id = $1", id); err != nil {
			return err
		}
		for _, e := range in.Entries {
			if !slices.Contains(NotificationEvents, e.Event) {
				return apperr.Invalidf("notification.event", "Unknown event %s.", e.Event)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO notification_scheme_entries (scheme_id, event, recipient_type, recipient_id)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, e.Event, e.RecipientType, e.RecipientID); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// OrderConfig sets the order of issue types, statuses, priorities or resolutions (priorities sort issues by
// it). Objects missing from ids keep their relative order after the listed ones.
func (s *Service) OrderConfig(ctx context.Context, kind string, ids []int64) error {
	table, ok := orderedKinds[kind]
	if !ok {
		return apperr.NotFound("config.not-found", "Unknown configuration type %s.", kind)
	}
	return s.configTx(ctx, kind+".order", "", func(tx pgx.Tx) error {
		// table comes from a fixed map, never from the request.
		_, err := tx.Exec(ctx, `UPDATE `+table+` t SET position = o.pos FROM (
			SELECT id, row_number() OVER (ORDER BY array_position($1::bigint[], id) NULLS LAST, position, id) AS pos FROM `+table+`
		) o WHERE t.id = o.id`, ids)
		return err
	})
}
