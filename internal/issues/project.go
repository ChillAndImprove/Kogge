// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
)

var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,9}$`)

// ValidProjectKey reports whether k can be a project key.
func ValidProjectKey(k string) bool { return projectKeyPattern.MatchString(k) }

func (s *Service) userCanSee(ctx context.Context, q db.Querier, userID int64, i *Issue) (bool, error) {
	var status string
	var groups []int64
	if err := q.QueryRow(ctx, `SELECT status, ARRAY(SELECT group_id FROM group_members WHERE user_id = $1) FROM users
		WHERE id = $1`, userID).Scan(&status, &groups); err != nil {
		if db.IsNoRows(err) {
			return false, nil
		}
		return false, err
	}
	if status != identity.StatusActive {
		return false, nil
	}
	a, err := s.Access(ctx, &identity.Principal{User: &identity.User{ID: userID}, Groups: groups, Method: identity.MethodInternal})
	if err != nil {
		return false, err
	}
	return a.visible(i.head()), nil
}

func (i *Issue) head() issueHead {
	return issueHead{id: i.ID, projectID: i.projectID, reporterID: i.reporterID, assigneeID: i.assigneeID,
		securityLevelID: i.securityLevelID}
}

const projectColumns = `p.id, p.key, p.name, p.description, p.type, p.category, p.lead_id, lu.username, lu.display_name,
	lu.status = 'active', p.default_assignee, p.permission_scheme_id, p.notification_scheme_id, p.security_scheme_id,
	p.default_workflow_id, p.archived, p.created_at, p.version, p.next_number - 1`

func scanProject(row pgx.Row) (*Project, error) {
	p := &Project{}
	var leadID *int64
	var leadUser, leadName *string
	var leadActive *bool
	err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Type, &p.Category, &leadID, &leadUser, &leadName,
		&leadActive, &p.DefaultAssignee, &p.PermissionSchemeID, &p.NotificationSchemeID, &p.SecuritySchemeID,
		&p.DefaultWorkflowID, &p.Archived, &p.CreatedAt, &p.Version, &p.Issues)
	if err != nil {
		return nil, err
	}
	if leadID != nil {
		p.Lead = &identity.UserRef{ID: *leadID, Username: *leadUser, DisplayName: *leadName, Active: *leadActive}
	}
	return p, nil
}

const projectFrom = " FROM issue_projects p LEFT JOIN users lu ON lu.id = p.lead_id "

// projectByKey loads a project by its current or a former key.
func projectByKey(ctx context.Context, q db.Querier, key string) (*Project, error) {
	p, err := scanProject(q.QueryRow(ctx, "SELECT "+projectColumns+projectFrom+
		"WHERE p.id = (SELECT project_id FROM issue_project_keys WHERE key = upper($1))", key))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	return p, err
}

func projectByID(ctx context.Context, q db.Querier, id int64) (*Project, error) {
	p, err := scanProject(q.QueryRow(ctx, "SELECT "+projectColumns+projectFrom+"WHERE p.id = $1", id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("project.not-found", "Project %d does not exist.", id)
	}
	return p, err
}

// ProjectView is a project with the caller's permissions and its configuration for forms.
type ProjectView struct {
	*Project
	Permissions []string     `json:"permissions" doc:"Project permissions of the caller"`
	IssueTypes  []*IssueType `json:"issueTypes"`
	Components  []Component  `json:"components"`
	Versions    []Version    `json:"versions"`
}

// ListProjects returns the projects the principal can browse (and, for project creators and administrators,
// archived ones on request).
func (s *Service) ListProjects(ctx context.Context, p *identity.Principal, query string, archived bool) ([]*Project, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	ids := a.BrowsableProjects()
	if admin, _ := s.authz.IsAdmin(ctx, p); admin {
		ids = nil // administrators see all projects (not their issues)
	}
	var args db.Args
	where := []string{"(" + args.Add(ids == nil) + " OR p.id = ANY(" + args.Add(nonNil(ids)) + "))"}
	if !archived {
		where = append(where, "NOT p.archived")
	}
	if q := strings.TrimSpace(query); q != "" {
		pat := args.Add("%" + db.EscapeLike(strings.ToLower(q)) + "%")
		where = append(where, "(lower(p.name) LIKE "+pat+" OR lower(p.key) LIKE "+pat+")")
	}
	rows, err := s.pool.Query(ctx, "SELECT "+projectColumns+projectFrom+"WHERE "+strings.Join(where, " AND ")+
		" ORDER BY lower(p.name)", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Project, error) { return scanProject(row) })
}

// ProjectPermissions returns the principal's permissions in a project (sorted).
func (s *Service) ProjectPermissions(ctx context.Context, p *identity.Principal, projectID int64) ([]string, *Access, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	perms := []string{}
	for _, perm := range ProjectPermissions {
		if a.CanAny(projectID, perm) {
			perms = append(perms, perm)
		}
	}
	if !slices.Contains(perms, PermAdmin) {
		if admin, _ := s.authz.IsAdmin(ctx, p); admin {
			perms = append(perms, PermAdmin)
		}
	}
	return perms, a, nil
}

// GetProject returns a project the principal can browse (administrators can always see the configuration).
func (s *Service) GetProject(ctx context.Context, p *identity.Principal, key string) (*ProjectView, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	perms, _, err := s.ProjectPermissions(ctx, p, proj.ID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(perms, PermBrowse) && !slices.Contains(perms, PermAdmin) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", key)
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	v := &ProjectView{Project: proj, Permissions: perms, IssueTypes: []*IssueType{}}
	types, err := s.projectIssueTypes(ctx, s.pool, proj.ID)
	if err != nil {
		return nil, err
	}
	for _, pt := range types {
		if t := cfg.types[pt.TypeID]; t != nil {
			v.IssueTypes = append(v.IssueTypes, t)
		}
	}
	if v.Components, err = s.listComponents(ctx, s.pool, proj.ID); err != nil {
		return nil, err
	}
	if v.Versions, err = s.listVersions(ctx, s.pool, proj.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// requireProjectAdmin checks project administration (or global administration). For who cannot see the project, it
// does not exist.
func (s *Service) requireProjectAdmin(ctx context.Context, p *identity.Principal, proj *Project) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if a.Can(proj.ID, PermAdmin) {
		return nil
	}
	if ok, _ := s.authz.IsAdmin(ctx, p); ok {
		return nil
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", proj.Key)
	}
	return apperr.Forbidden("project.admin-required", "Administering project %s needs the project administration permission.", proj.Key)
}

// NewProject describes a project to create.
type NewProject struct {
	Key         string `json:"key" pattern:"^[A-Z][A-Z0-9_]{1,9}$" doc:"2–10 upper-case letters, digits or _"`
	Name        string `json:"name" minLength:"1" maxLength:"100"`
	Description string `json:"description,omitempty" maxLength:"2000"`
	Template    string `json:"template,omitempty" enum:"scrum,kanban,bugs,business" default:"scrum"`
	LeadID      int64  `json:"leadId,omitempty" doc:"Default: the creator"`
	Category    string `json:"category,omitempty" maxLength:"100"`
}

// CreateProject creates a project from a template. The creator becomes lead and project administrator.
func (s *Service) CreateProject(ctx context.Context, p *identity.Principal, in NewProject) (*Project, error) {
	if _, err := s.authz.RequireGlobal(ctx, authz.ProjectCreate); err != nil {
		return nil, err
	}
	in.Key = strings.ToUpper(strings.TrimSpace(in.Key))
	in.Name = strings.TrimSpace(in.Name)
	if !ValidProjectKey(in.Key) {
		return nil, apperr.Invalid("project.key", "The key must start with a letter and have 2–10 upper-case letters, digits or _.")
	}
	tpl, ok := projectTemplates[in.Template]
	if !ok {
		tpl = projectTemplates["scrum"]
	}
	if in.LeadID == 0 {
		in.LeadID = p.UserID()
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var proj *Project
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ids, err := defaultIDs(ctx, tx)
		if err != nil {
			return apperr.Internal(err)
		}
		var taken bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM issue_project_keys WHERE key = $1)", in.Key).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return apperr.Conflict("project.key-taken", "The key %s is already in use.", in.Key)
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO issue_projects (key, name, description, type, category, lead_id,
			permission_scheme_id, notification_scheme_id, default_workflow_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`, in.Key, in.Name, in.Description, tpl.typ, in.Category,
			in.LeadID, ids["scheme.permissions"], ids["scheme.notifications"], ids["wf."+tpl.workflow]).Scan(&id)
		if db.IsUniqueViolation(err, "issue_projects_name_key") {
			return apperr.Conflict("project.name-taken", "A project named %s already exists.", in.Name)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO issue_project_keys (key, project_id) VALUES ($1, $2)", in.Key, id); err != nil {
			return err
		}
		for pos, t := range tpl.types {
			if _, err := tx.Exec(ctx, `INSERT INTO project_issue_types (project_id, issue_type_id, position) VALUES ($1, $2, $3)`,
				id, ids["type."+t], pos); err != nil {
				return err
			}
		}
		for _, uid := range []int64{p.UserID(), in.LeadID} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_role_members (project_id, role_id, principal_type, principal_id)
				VALUES ($1, $2, 'user', $3) ON CONFLICT DO NOTHING`, id, ids["role.admins"], uid); err != nil {
				return err
			}
		}
		if proj, err = projectByID(ctx, tx, id); err != nil {
			return err
		}
		if tpl.board != "" {
			name := fmt.Sprintf(defaultTexts.get("board.name", st.DefaultLocale), proj.Key)
			if _, err := s.createBoard(ctx, tx, cfg, p.UserID(), proj, name, tpl.board, "", nil); err != nil {
				return err
			}
		}
		if err := s.permissionsChanged(ctx, tx); err != nil {
			return err
		}
		if err := s.bus.Publish(ctx, tx, ProjectEvent{Name: "project.created", ProjectID: id}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "project.create", TargetType: "project",
			TargetID: in.Key, Details: map[string]any{"name": in.Name, "template": in.Template}})
	})
	if err == nil {
		s.afterCommit()
	}
	return proj, err
}

// ProjectUpdate holds optional project changes.
type ProjectUpdate struct {
	Key                  *string `json:"key,omitempty" pattern:"^[A-Z][A-Z0-9_]{1,9}$"`
	Name                 *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Description          *string `json:"description,omitempty" maxLength:"2000"`
	Category             *string `json:"category,omitempty" maxLength:"100"`
	LeadID               *int64  `json:"leadId,omitempty"`
	DefaultAssignee      *string `json:"defaultAssignee,omitempty" enum:"unassigned,lead"`
	PermissionSchemeID   *int64  `json:"permissionSchemeId,omitempty"`
	NotificationSchemeID *int64  `json:"notificationSchemeId,omitempty" doc:"0 removes the scheme"`
	SecuritySchemeID     *int64  `json:"securitySchemeId,omitempty" doc:"0 removes the scheme"`
	DefaultWorkflowID    *int64  `json:"defaultWorkflowId,omitempty"`
	Archived             *bool   `json:"archived,omitempty"`
}

// UpdateProject changes a project. Changing the key renames all issue keys; old keys keep resolving.
func (s *Service) UpdateProject(ctx context.Context, p *identity.Principal, key string, u ProjectUpdate) (*Project, error) {
	var proj *Project
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.requireProjectAdmin(ctx, p, cur); err != nil {
			return err
		}
		schemeChange := u.PermissionSchemeID != nil || u.SecuritySchemeID != nil || u.LeadID != nil
		if u.PermissionSchemeID != nil || u.NotificationSchemeID != nil || u.SecuritySchemeID != nil {
			if ok, _ := s.authz.IsAdmin(ctx, p); !ok {
				return apperr.Forbidden("project.schemes", "Only administrators can change the schemes of a project.")
			}
		}
		var args db.Args
		var sets []string
		if u.Name != nil {
			sets = append(sets, "name = "+args.Add(strings.TrimSpace(*u.Name)))
		}
		if u.Description != nil {
			sets = append(sets, "description = "+args.Add(*u.Description))
		}
		if u.Category != nil {
			sets = append(sets, "category = "+args.Add(*u.Category))
		}
		if u.LeadID != nil {
			sets = append(sets, "lead_id = NULLIF("+args.Add(*u.LeadID)+", 0)")
		}
		if u.DefaultAssignee != nil {
			sets = append(sets, "default_assignee = "+args.Add(*u.DefaultAssignee))
		}
		if u.PermissionSchemeID != nil {
			sets = append(sets, "permission_scheme_id = "+args.Add(*u.PermissionSchemeID))
		}
		if u.NotificationSchemeID != nil {
			sets = append(sets, "notification_scheme_id = NULLIF("+args.Add(*u.NotificationSchemeID)+", 0)")
		}
		if u.SecuritySchemeID != nil {
			sets = append(sets, "security_scheme_id = NULLIF("+args.Add(*u.SecuritySchemeID)+", 0)")
		}
		if u.DefaultWorkflowID != nil {
			if err := s.checkWorkflowCompatible(ctx, tx, cur.ID, 0, *u.DefaultWorkflowID); err != nil {
				return err
			}
			sets = append(sets, "default_workflow_id = "+args.Add(*u.DefaultWorkflowID))
		}
		if u.Archived != nil {
			sets = append(sets, "archived = "+args.Add(*u.Archived))
		}
		if u.Key != nil && *u.Key != cur.Key {
			newKey := strings.ToUpper(*u.Key)
			var owner int64
			err := tx.QueryRow(ctx, "SELECT project_id FROM issue_project_keys WHERE key = $1", newKey).Scan(&owner)
			if err == nil && owner != cur.ID {
				return apperr.Conflict("project.key-taken", "The key %s is already in use.", newKey)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO issue_project_keys (key, project_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				newKey, cur.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO issue_keys (key, issue_id) SELECT key, id FROM issues WHERE project_id = $1
				ON CONFLICT DO NOTHING`, cur.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE issues SET key = $2 || '-' || number WHERE project_id = $1", cur.ID, newKey); err != nil {
				return err
			}
			sets = append(sets, "key = "+args.Add(newKey))
		}
		if len(sets) > 0 {
			sets = append(sets, "updated_at = now()", "version = version + 1")
			_, err := tx.Exec(ctx, "UPDATE issue_projects SET "+strings.Join(sets, ", ")+" WHERE id = "+args.Add(cur.ID), args...)
			if db.IsUniqueViolation(err, "issue_projects_name_key") {
				return apperr.Conflict("project.name-taken", "A project with this name already exists.")
			}
			if db.IsForeignKeyViolation(err) {
				return apperr.Invalid("project.reference", "A referenced user, scheme or workflow does not exist.")
			}
			if err != nil {
				return err
			}
		}
		if proj, err = projectByID(ctx, tx, cur.ID); err != nil {
			return err
		}
		if schemeChange || u.Archived != nil {
			if err := s.permissionsChanged(ctx, tx); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "project.update", TargetType: "project",
			TargetID: proj.Key, Details: map[string]any{"fields": len(sets)}})
	})
	if err == nil {
		s.afterCommit()
	}
	return proj, err
}

// DeleteProject removes a project with all issues (administrators only).
func (s *Service) DeleteProject(ctx context.Context, key string) error {
	actor, err := s.authz.RequireGlobal(ctx, authz.Admin)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		proj, err := projectByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		var n int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issues WHERE project_id = $1", proj.ID).Scan(&n); err != nil {
			return err
		}
		for _, sql := range []string{
			"DELETE FROM watchers WHERE entity_type = 'issue' AND entity_id IN (SELECT id FROM issues WHERE project_id = $1)",
			"DELETE FROM blob_refs WHERE owner_type = 'issue-attachment' AND owner_id IN (SELECT a.id FROM issue_attachments a JOIN issues i ON i.id = a.issue_id WHERE i.project_id = $1)",
			"UPDATE issues SET parent_id = NULL WHERE parent_id IN (SELECT id FROM issues WHERE project_id = $1)",
			"DELETE FROM issues WHERE project_id = $1",
			"DELETE FROM issue_projects WHERE id = $1",
		} {
			if _, err := tx.Exec(ctx, sql, proj.ID); err != nil {
				return err
			}
		}
		if err := s.permissionsChanged(ctx, tx); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "project.delete", TargetType: "project",
			TargetID: proj.Key, Details: map[string]any{"name": proj.Name, "issues": n}})
	})
	if err == nil {
		s.afterCommit()
	}
	return err
}

// ProjectEvent is published for project lifecycle changes.
type ProjectEvent struct {
	Name      string
	ProjectID int64
}

// EventName implements events.Event.
func (e ProjectEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live.
func (e ProjectEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "project", ID: itoa(e.ProjectID), Container: "project:" + itoa(e.ProjectID)}
}
