// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/legacy"
	"kogge.de/kogge/internal/platform/rank"
)

// Importer loads data of migrated products into the issues module. It acts for the system: there
// are no permission checks, and authors and timestamps come from the source. Every entity is recorded in
// legacy_ids under its source id, so loads can be repeated (delta loads before the cut-over).
type Importer struct {
	s      *Service
	tx     pgx.Tx
	source string
}

// NewImporter returns an importer working in a transaction. source identifies the migrated system, e.g.
// "jira:https://jira.example.com".
func (s *Service) NewImporter(tx pgx.Tx, source string) *Importer {
	return &Importer{s: s, tx: tx, source: source}
}

func (im *Importer) ctx() context.Context { return context.Background() }

func (im *Importer) find(typ, id string) (int64, bool, error) {
	return legacy.Lookup(im.ctx(), im.tx, im.source, typ, id)
}

func (im *Importer) record(typ, id, entity string, entityID int64) error {
	return legacy.Record(im.ctx(), im.tx, im.source, typ, id, entity, entityID)
}

// ensure returns the entity recorded for a source id, or finds one by name, or creates one.
func (im *Importer) ensure(typ, srcID, entity string, byName func() (int64, error), create func() (int64, error)) (int64, error) {
	if id, ok, err := im.find(typ, srcID); err != nil || ok {
		return id, err
	}
	id, err := byName()
	if err != nil && !db.IsNoRows(err) {
		return 0, err
	}
	if id == 0 {
		if id, err = create(); err != nil {
			return 0, err
		}
	}
	return id, im.record(typ, srcID, entity, id)
}

func (im *Importer) changed() error {
	return im.s.configChanged(im.ctx(), im.tx)
}

// IssueType maps an issue type by name (creating it if needed).
func (im *Importer) IssueType(srcID, name, description string, subtask bool) (int64, error) {
	level := 0
	if subtask {
		level = -1
	}
	lname := strings.ToLower(name)
	icon := "task"
	switch {
	case subtask:
		icon = "subtask"
	case strings.Contains(lname, "epic"):
		icon, level = "epic", 1
	case strings.Contains(lname, "bug") || strings.Contains(lname, "fehler") || strings.Contains(lname, "defect"):
		icon = "bug"
	case strings.Contains(lname, "story"):
		icon = "story"
	}
	color := map[string]string{"epic": "orange", "story": "teal", "bug": "red", "task": "indigo", "subtask": "gray"}[icon]
	return im.ensure("issuetype", srcID, "issue-type", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM issue_types WHERE lower(name) = lower($1)", name).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), `INSERT INTO issue_types (name, description, icon, color, level, position)
			VALUES ($1, $2, $3, $4, $5, coalesce((SELECT max(position) + 1 FROM issue_types), 1)) RETURNING id`,
			name, description, icon, color, level).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
}

// Status maps a status by name; category is todo, inprogress or done.
func (im *Importer) Status(srcID, name, category string) (int64, error) {
	if category != CategoryTodo && category != CategoryInProgress && category != CategoryDone {
		category = CategoryTodo
	}
	return im.ensure("status", srcID, "status", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM statuses WHERE lower(name) = lower($1)", name).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), `INSERT INTO statuses (name, category, position)
			VALUES ($1, $2, coalesce((SELECT max(position) + 1 FROM statuses), 1)) RETURNING id`, name, category).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
}

// Priority maps a priority by name.
func (im *Importer) Priority(srcID, name string) (int64, error) {
	return im.ensure("priority", srcID, "priority", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM priorities WHERE lower(name) = lower($1)", name).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), `INSERT INTO priorities (name, icon, color, position)
			VALUES ($1, 'medium', 'gray', coalesce((SELECT max(position) + 1 FROM priorities), 1)) RETURNING id`, name).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
}

// Resolution maps a resolution by name.
func (im *Importer) Resolution(srcID, name string) (int64, error) {
	return im.ensure("resolution", srcID, "resolution", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM resolutions WHERE lower(name) = lower($1)", name).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), `INSERT INTO resolutions (name, position)
			VALUES ($1, coalesce((SELECT max(position) + 1 FROM resolutions), 1)) RETURNING id`, name).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
}

// LinkType maps a link type by name.
func (im *Importer) LinkType(srcID, name, outward, inward string) (int64, error) {
	return im.ensure("linktype", srcID, "link-type", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM issue_link_types WHERE lower(name) = lower($1)", name).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "INSERT INTO issue_link_types (name, outward, inward) VALUES ($1, $2, $3) RETURNING id",
			name, outward, inward).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
}

// CustomField maps a custom field by name and type and returns its key (cf_<id>) and options by value.
func (im *Importer) CustomField(srcID, name, typ string, options []string) (string, map[string]int64, error) {
	id, err := im.ensure("field", srcID, "field", func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), "SELECT id FROM custom_fields WHERE lower(name) = lower($1) AND type = $2", name, typ).Scan(&id)
		return id, err
	}, func() (int64, error) {
		var id int64
		err := im.tx.QueryRow(im.ctx(), `INSERT INTO custom_fields (name, type, options, config, project_ids, issue_type_ids)
			VALUES ($1, $2, '[]', '{}', '{}', '{}') RETURNING id`, name, typ).Scan(&id)
		if err == nil {
			err = im.changed()
		}
		return id, err
	})
	if err != nil {
		return "", nil, err
	}
	// Merge options by value.
	var raw []byte
	if err := im.tx.QueryRow(im.ctx(), "SELECT options FROM custom_fields WHERE id = $1", id).Scan(&raw); err != nil {
		return "", nil, err
	}
	var existing []Option
	_ = json.Unmarshal(raw, &existing)
	byValue := map[string]int64{}
	next := int64(1)
	for _, o := range existing {
		byValue[o.Value] = o.ID
		next = max(next, o.ID+1)
	}
	added := false
	for _, v := range options {
		if _, ok := byValue[v]; !ok && v != "" {
			existing = append(existing, Option{ID: next, Value: v})
			byValue[v] = next
			next++
			added = true
		}
	}
	if added {
		b, _ := json.Marshal(existing)
		if _, err := im.tx.Exec(im.ctx(), "UPDATE custom_fields SET options = $2 WHERE id = $1", id, b); err != nil {
			return "", nil, err
		}
		if err := im.changed(); err != nil {
			return "", nil, err
		}
	}
	return fieldKey(id), byValue, nil
}

// ImportProject describes a project to load.
type ImportProject struct {
	SourceID    string
	Key         string
	Name        string
	Description string
	Category    string
	LeadID      int64
	Business    bool
	IssueTypes  []int64
	Statuses    []int64 // of the project's workflow, the first todo status is the initial one
	Board       string  // scrum, kanban or empty
}

type statusInfo struct {
	name, category string
}

// statusInfos reads statuses in the import transaction (new statuses are not in the configuration cache yet).
func (im *Importer) statusInfos(ids []int64) (map[int64]statusInfo, error) {
	rows, err := im.tx.Query(im.ctx(), "SELECT id, name, category FROM statuses WHERE id = ANY($1)", nonNil(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]statusInfo{}
	for rows.Next() {
		var id int64
		var si statusInfo
		if err := rows.Scan(&id, &si.name, &si.category); err != nil {
			return nil, err
		}
		out[id] = si
	}
	return out, rows.Err()
}

// Project creates or updates a project with a workflow of its statuses (every status reachable from any other,
// since workflows of the source system are not available through its API).
func (im *Importer) Project(p ImportProject) (int64, error) {
	ctx := im.ctx()
	statuses := []int64{}
	for _, st := range p.Statuses {
		if !slices.Contains(statuses, st) {
			statuses = append(statuses, st)
		}
	}
	infos, err := im.statusInfos(statuses)
	if err != nil {
		return 0, err
	}
	order := map[string]int{CategoryTodo: 0, CategoryInProgress: 1, CategoryDone: 2}
	slices.SortStableFunc(statuses, func(a, b int64) int { return order[infos[a].category] - order[infos[b].category] })
	initial := int64(0)
	if len(statuses) > 0 {
		initial = statuses[0]
	}
	def := &workflow.Definition{Statuses: statuses, Initial: initial, Options: workflow.Options{AutoResolution: true}}
	for i, st := range statuses {
		def.Transitions = append(def.Transitions, workflow.Transition{ID: i + 1, Name: infos[st].name, FromAny: true, To: st})
	}
	defRaw, _ := json.Marshal(def)
	if id, ok, err := im.find("project", p.SourceID); err != nil {
		return 0, err
	} else if ok {
		_, err := im.tx.Exec(ctx, "UPDATE issue_projects SET name = $2, description = $3, category = $4, lead_id = $5 WHERE id = $1",
			id, p.Name, p.Description, p.Category, nullable(p.LeadID))
		if err != nil {
			return 0, err
		}
		if _, err := im.tx.Exec(ctx, `UPDATE workflows SET definition = $2, updated_at = now(), version = version + 1
			WHERE id = (SELECT default_workflow_id FROM issue_projects WHERE id = $1)`, id, defRaw); err != nil {
			return 0, err
		}
		return id, im.changed()
	}
	ids, err := defaultIDs(ctx, im.tx)
	if err != nil {
		return 0, err
	}
	var wf int64
	if err := im.tx.QueryRow(ctx, "INSERT INTO workflows (name, description, definition) VALUES ($1, $2, $3) RETURNING id",
		p.Key+" (imported)", "Created by the import of "+p.Key+".", defRaw).Scan(&wf); err != nil {
		return 0, err
	}
	typ := "software"
	if p.Business {
		typ = "business"
	}
	var id int64
	err = im.tx.QueryRow(ctx, `INSERT INTO issue_projects (key, name, description, type, category, lead_id, permission_scheme_id,
		notification_scheme_id, default_workflow_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		p.Key, p.Name, p.Description, typ, p.Category, nullable(p.LeadID), ids["scheme.permissions"], ids["scheme.notifications"], wf).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("project %s: %w", p.Key, err)
	}
	if _, err := im.tx.Exec(ctx, "INSERT INTO issue_project_keys (key, project_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", p.Key, id); err != nil {
		return 0, err
	}
	for pos, t := range p.IssueTypes {
		if _, err := im.tx.Exec(ctx, `INSERT INTO project_issue_types (project_id, issue_type_id, position) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, id, t, pos); err != nil {
			return 0, err
		}
	}
	if p.LeadID != 0 {
		if _, err := im.tx.Exec(ctx, `INSERT INTO project_role_members (project_id, role_id, principal_type, principal_id)
			VALUES ($1, $2, 'user', $3) ON CONFLICT DO NOTHING`, id, ids["role.admins"], p.LeadID); err != nil {
			return 0, err
		}
	}
	if err := im.record("project", p.SourceID, "project", id); err != nil {
		return 0, err
	}
	if err := im.s.permissionsChanged(ctx, im.tx); err != nil {
		return 0, err
	}
	if err := im.changed(); err != nil {
		return 0, err
	}
	if p.Board != "" {
		c := BoardConfig{Swimlanes: Swimlanes{Type: "none"}, QuickFilters: []NamedQuery{{ID: 1, Name: "Mine", Query: "assignee = currentUser()"}},
			Card: CardLayout{Fields: []string{}, Colors: "issuetype"}, Estimation: Estimation{Type: "count"}}
		for _, st := range statuses {
			c.Columns = append(c.Columns, BoardColumn{Name: infos[st].name, Statuses: []int64{st}})
		}
		var points int64
		if err := im.tx.QueryRow(ctx, "SELECT id FROM custom_fields WHERE type = 'number' AND lower(name) = 'story points' LIMIT 1").Scan(&points); err == nil && p.Board == BoardScrum {
			c.Estimation = Estimation{Type: "field", Field: fieldKey(points)}
		}
		if p.Board == BoardKanban {
			c.HideDoneAfterDays = 14
		}
		raw, _ := json.Marshal(c)
		var bid int64
		if err := im.tx.QueryRow(ctx, `INSERT INTO boards (project_id, name, type, query, config, owner_id) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`, id, p.Key+" board", p.Board, "project = "+p.Key+" ORDER BY rank", raw, nullable(p.LeadID)).Scan(&bid); err != nil {
			return 0, err
		}
		if err := im.record("project-board", p.SourceID, "board", bid); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// SecurityLevel maps an issue security level. The members of a level are not available through the source's API,
// so a new level is visible to the project's Administrators role only, until an administrator adds members. The
// level belongs to the scheme "<KEY> (imported)", which is assigned to the project unless it has a scheme already.
func (im *Importer) SecurityLevel(projectID int64, srcID, name, description string) (int64, bool, error) {
	ctx := im.ctx()
	if id, ok, err := im.find("security-level", srcID); err != nil || ok {
		return id, false, err
	}
	var key string
	var scheme *int64
	if err := im.tx.QueryRow(ctx, "SELECT key, security_scheme_id FROM issue_projects WHERE id = $1", projectID).Scan(&key, &scheme); err != nil {
		return 0, false, err
	}
	if scheme == nil {
		var id int64
		err := im.tx.QueryRow(ctx, `INSERT INTO security_schemes (name, description) VALUES ($1, $2)
			ON CONFLICT (lower(name)) DO UPDATE SET description = EXCLUDED.description RETURNING id`,
			key+" (imported)", "Created by the import of "+key+".").Scan(&id)
		if err != nil {
			return 0, false, err
		}
		if _, err := im.tx.Exec(ctx, "UPDATE issue_projects SET security_scheme_id = $2 WHERE id = $1", projectID, id); err != nil {
			return 0, false, err
		}
		scheme = &id
	}
	var id int64
	err := im.tx.QueryRow(ctx, "SELECT id FROM security_levels WHERE scheme_id = $1 AND lower(name) = lower($2)", *scheme, name).Scan(&id)
	created := false
	if db.IsNoRows(err) {
		err = im.tx.QueryRow(ctx, `INSERT INTO security_levels (scheme_id, name, description, position)
			VALUES ($1, $2, $3, coalesce((SELECT max(position) + 1 FROM security_levels WHERE scheme_id = $1), 1)) RETURNING id`,
			*scheme, name, description).Scan(&id)
		if err != nil {
			return 0, false, err
		}
		ids, err := defaultIDs(ctx, im.tx)
		if err != nil {
			return 0, false, err
		}
		if _, err := im.tx.Exec(ctx, "INSERT INTO security_level_members (level_id, holder_type, holder_id) VALUES ($1, 'role', $2)",
			id, ids["role.admins"]); err != nil {
			return 0, false, err
		}
		created = true
	}
	if err != nil {
		return 0, false, err
	}
	if err := im.s.permissionsChanged(ctx, im.tx); err != nil {
		return 0, false, err
	}
	if err := im.changed(); err != nil {
		return 0, false, err
	}
	return id, created, im.record("security-level", srcID, "security-level", id)
}

// ProjectRole maps a project role by name. The usual role names of the source map to the built-in roles, whose
// names depend on the installation's language.
func (im *Importer) ProjectRole(name, description string) (int64, error) {
	ids, err := defaultIDs(im.ctx(), im.tx)
	if err != nil {
		return 0, err
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "administrators", "administratoren":
		return ids["role.admins"], nil
	case "developers", "entwickler":
		return ids["role.devs"], nil
	case "users", "benutzer":
		return ids["role.users"], nil
	}
	var id int64
	err = im.tx.QueryRow(im.ctx(), "SELECT id FROM project_roles WHERE lower(name) = lower($1)", name).Scan(&id)
	if db.IsNoRows(err) {
		err = im.tx.QueryRow(im.ctx(), "INSERT INTO project_roles (name, description) VALUES ($1, $2) RETURNING id", name, description).Scan(&id)
		if err == nil {
			err = im.changed()
		}
	}
	return id, err
}

// RoleMember adds a user or a group to a project role.
func (im *Importer) RoleMember(projectID, roleID int64, principalType string, principalID int64) error {
	_, err := im.tx.Exec(im.ctx(), `INSERT INTO project_role_members (project_id, role_id, principal_type, principal_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, projectID, roleID, principalType, principalID)
	if err == nil {
		err = im.s.permissionsChanged(im.ctx(), im.tx)
	}
	return err
}

// SchemeGrant grants a permission of a permission scheme.
type SchemeGrant struct {
	Permission string
	HolderType string // role, group, user, reporter, assignee, project-lead, logged-in or anyone
	HolderID   int64
}

// PermissionScheme creates or replaces a permission scheme of the source and assigns it to a project.
func (im *Importer) PermissionScheme(projectID int64, srcID, name, description string, grants []SchemeGrant) (int64, error) {
	ctx := im.ctx()
	id, ok, err := im.find("permission-scheme", srcID)
	if err != nil {
		return 0, err
	}
	if !ok {
		var taken bool
		if err := im.tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM permission_schemes WHERE lower(name) = lower($1))", name).Scan(&taken); err != nil {
			return 0, err
		}
		if taken {
			name += " (imported)"
		}
		err := im.tx.QueryRow(ctx, `INSERT INTO permission_schemes (name, description) VALUES ($1, $2)
			ON CONFLICT (lower(name)) DO UPDATE SET description = EXCLUDED.description RETURNING id`, name, description).Scan(&id)
		if err != nil {
			return 0, err
		}
		if err := im.record("permission-scheme", srcID, "permission-scheme", id); err != nil {
			return 0, err
		}
	}
	if _, err := im.tx.Exec(ctx, "DELETE FROM permission_scheme_grants WHERE scheme_id = $1", id); err != nil {
		return 0, err
	}
	for _, g := range grants {
		if !slices.Contains(ProjectPermissions, g.Permission) {
			return 0, fmt.Errorf("unknown permission %s", g.Permission)
		}
		if _, err := im.tx.Exec(ctx, `INSERT INTO permission_scheme_grants (scheme_id, permission, holder_type, holder_id) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, id, g.Permission, g.HolderType, g.HolderID); err != nil {
			return 0, err
		}
	}
	if _, err := im.tx.Exec(ctx, "UPDATE issue_projects SET permission_scheme_id = $2 WHERE id = $1", projectID, id); err != nil {
		return 0, err
	}
	if err := im.s.permissionsChanged(ctx, im.tx); err != nil {
		return 0, err
	}
	return id, im.changed()
}

// Done applies cache invalidation locally after the import transaction committed.
func (im *Importer) Done() { im.s.afterCommit() }

// Component maps a component of a project.
func (im *Importer) Component(projectID int64, srcID, name, description string, leadID int64) (int64, error) {
	if id, ok, err := im.find("component", srcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(im.ctx(), "UPDATE components SET name = $2, description = $3, lead_id = $4 WHERE id = $1", id, name, description, nullable(leadID))
		}
		return id, err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), "INSERT INTO components (project_id, name, description, lead_id) VALUES ($1, $2, $3, $4) RETURNING id",
		projectID, name, description, nullable(leadID)).Scan(&id); err != nil {
		return 0, err
	}
	return id, im.record("component", srcID, "component", id)
}

// Version maps a version of a project.
func (im *Importer) Version(projectID int64, srcID, name, description string, start, release *string, released, archived bool, position int) (int64, error) {
	if id, ok, err := im.find("version", srcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(im.ctx(), `UPDATE versions SET name = $2, description = $3, start_date = $4, release_date = $5, released = $6,
				archived = $7, position = $8 WHERE id = $1`, id, name, description, start, release, released, archived, position)
		}
		return id, err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO versions (project_id, name, description, start_date, release_date, released, archived, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, projectID, name, description, start, release, released, archived, position).Scan(&id); err != nil {
		return 0, err
	}
	return id, im.record("version", srcID, "version", id)
}

// ImportIssue is an issue to load.
type ImportIssue struct {
	SourceID          string
	Key               string
	ProjectID         int64
	TypeID            int64
	StatusID          int64
	PriorityID        int64
	ResolutionID      int64
	Summary           string
	Description       *docmodel.Doc
	Environment       string
	ReporterID        int64
	CreatorID         int64
	AssigneeID        int64
	Created           time.Time
	Updated           time.Time
	Resolved          *time.Time
	DueDate           *string
	Labels            []string
	Components        []int64
	FixVersions       []int64
	AffectsVersions   []int64
	OriginalEstimate  *int64
	RemainingEstimate *int64
	TimeSpent         int64
	Fields            map[string]any
	Rank              string
	SecurityLevelID   int64
}

// Issue creates or updates an issue with its original key, authors and dates.
func (im *Importer) Issue(in ImportIssue) (int64, error) {
	ctx := im.ctx()
	number, err := strconv.ParseInt(in.Key[strings.LastIndex(in.Key, "-")+1:], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("issue key %s: %w", in.Key, err)
	}
	if in.Description == nil {
		in.Description = docmodel.Empty()
	}
	desc, _ := docmodel.Marshal(in.Description)
	fields, _ := json.Marshal(nonNilMap(in.Fields))
	labels := nonNilStrings(in.Labels)
	creator := in.CreatorID
	if creator == 0 {
		creator = in.ReporterID
	}
	if creator == 0 {
		creator = 1 // the system user
	}
	if in.Rank == "" {
		var maxRank *string
		if err := im.tx.QueryRow(ctx, "SELECT max(rank) FROM issues").Scan(&maxRank); err != nil {
			return 0, err
		}
		in.Rank = rank.After("")
		if maxRank != nil {
			in.Rank = rank.After(*maxRank)
		}
	}
	id, ok, err := im.find("issue", in.SourceID)
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = im.tx.Exec(ctx, `UPDATE issues SET type_id = $2, status_id = $3, priority_id = $4, resolution_id = $5, summary = $6,
			description = $7, description_text = $8, environment = $9, reporter_id = $10, assignee_id = $11, created_at = $12, updated_at = $13,
			resolved_at = $14, due_date = $15, labels = $16, original_estimate = $17, remaining_estimate = $18, time_spent = $19, fields = $20,
			security_level_id = $21, version = version + 1 WHERE id = $1`, id, in.TypeID, in.StatusID, nullable(in.PriorityID),
			nullable(in.ResolutionID), in.Summary, desc, docmodel.PlainText(in.Description), in.Environment, nullable(in.ReporterID),
			nullable(in.AssigneeID), in.Created, in.Updated, in.Resolved, in.DueDate, labels, in.OriginalEstimate, in.RemainingEstimate,
			in.TimeSpent, fields, nullable(in.SecurityLevelID))
	} else {
		err = im.tx.QueryRow(ctx, `INSERT INTO issues (project_id, number, key, type_id, status_id, priority_id, resolution_id, summary,
			description, description_text, environment, reporter_id, creator_id, assignee_id, created_at, updated_at, resolved_at,
			status_changed_at, due_date, labels, original_estimate, remaining_estimate, time_spent, fields, rank, security_level_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $16, $18, $19, $20, $21, $22, $23, $24, $25)
			RETURNING id`, in.ProjectID, number, in.Key, in.TypeID, in.StatusID, nullable(in.PriorityID), nullable(in.ResolutionID),
			in.Summary, desc, docmodel.PlainText(in.Description), in.Environment, nullable(in.ReporterID), creator, nullable(in.AssigneeID),
			in.Created, in.Updated, in.Resolved, in.DueDate, labels, in.OriginalEstimate, in.RemainingEstimate, in.TimeSpent, fields, in.Rank,
			nullable(in.SecurityLevelID)).Scan(&id)
		if err == nil {
			err = im.record("issue", in.SourceID, "issue", id)
		}
		if err == nil {
			err = im.record("issue-key", in.Key, "issue", id)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("issue %s: %w", in.Key, err)
	}
	if _, err := im.tx.Exec(ctx, "DELETE FROM issue_components WHERE issue_id = $1", id); err != nil {
		return 0, err
	}
	if _, err := im.tx.Exec(ctx, "INSERT INTO issue_components (issue_id, component_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING",
		id, nonNil(in.Components)); err != nil {
		return 0, err
	}
	if _, err := im.tx.Exec(ctx, "DELETE FROM issue_versions WHERE issue_id = $1", id); err != nil {
		return 0, err
	}
	for kind, ids := range map[string][]int64{"fix": in.FixVersions, "affects": in.AffectsVersions} {
		if _, err := im.tx.Exec(ctx, "INSERT INTO issue_versions (issue_id, version_id, kind) SELECT $1, unnest($2::bigint[]), $3 ON CONFLICT DO NOTHING",
			id, nonNil(ids), kind); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// SetDescription replaces the description of an imported issue (after its attachments were loaded, so that the
// converted text can show them).
func (im *Importer) SetDescription(issueID int64, d *docmodel.Doc) error {
	if d == nil {
		d = docmodel.Empty()
	}
	raw, _ := docmodel.Marshal(d)
	_, err := im.tx.Exec(im.ctx(), "UPDATE issues SET description = $2, description_text = $3 WHERE id = $1", issueID, raw, docmodel.PlainText(d))
	return err
}

// IssueByKey returns the id of an imported (or existing) issue by its key.
func (im *Importer) IssueByKey(key string) (int64, bool, error) {
	var id int64
	err := im.tx.QueryRow(im.ctx(), "SELECT id FROM issues WHERE key = upper($1)", key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// SetParent links a sub-task or an issue of an epic to its parent.
func (im *Importer) SetParent(issueID, parentID int64) error {
	_, err := im.tx.Exec(im.ctx(), "UPDATE issues SET parent_id = $2 WHERE id = $1 AND id <> $2", issueID, nullable(parentID))
	return err
}

// Visibility restricts a comment to a project role or a group.
type Visibility struct {
	Type string // role or group
	ID   int64
}

// CommentVisibility resolves a restriction of the source system by the name of a project role or a group. When
// neither exists, it returns the Administrators role and false, so that the comment stays hidden from others.
func (im *Importer) CommentVisibility(typ, name string) (*Visibility, bool, error) {
	var id int64
	var err error
	switch typ {
	case "role":
		err = im.tx.QueryRow(im.ctx(), "SELECT id FROM project_roles WHERE lower(name) = lower($1)", name).Scan(&id)
	case "group":
		err = im.tx.QueryRow(im.ctx(), "SELECT id FROM groups WHERE lower(name) = lower($1)", name).Scan(&id)
	default:
		err = pgx.ErrNoRows
	}
	if err == nil {
		return &Visibility{Type: typ, ID: id}, true, nil
	}
	if !db.IsNoRows(err) {
		return nil, false, err
	}
	ids, err := defaultIDs(im.ctx(), im.tx)
	if err != nil {
		return nil, false, err
	}
	return &Visibility{Type: "role", ID: ids["role.admins"]}, false, nil
}

// Comment adds or updates a comment.
func (im *Importer) Comment(issueID int64, srcID string, authorID int64, created, updated time.Time, body *docmodel.Doc, vis *Visibility) error {
	raw, _ := docmodel.Marshal(body)
	var vt, vi any
	if vis != nil {
		vt, vi = vis.Type, vis.ID
	}
	if id, ok, err := im.find("comment", srcID); err != nil {
		return err
	} else if ok {
		_, err := im.tx.Exec(im.ctx(), `UPDATE issue_comments SET body = $2, body_text = $3, updated_at = $4, visibility_type = $5, visibility_id = $6
			WHERE id = $1`, id, raw, docmodel.PlainText(body), updated, vt, vi)
		return err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO issue_comments (issue_id, author_id, body, body_text, visibility_type, visibility_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, issueID, nullable(authorID), raw, docmodel.PlainText(body), vt, vi, created, updated).Scan(&id); err != nil {
		return err
	}
	return im.record("comment", srcID, "issue-comment", id)
}

// Worklog adds a work log entry.
func (im *Importer) Worklog(issueID int64, srcID string, authorID int64, started time.Time, seconds int64, comment string, created time.Time) error {
	if seconds <= 0 {
		return nil
	}
	if _, ok, err := im.find("worklog", srcID); err != nil || ok {
		return err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO worklogs (issue_id, author_id, started_at, seconds, comment, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id`, issueID, nullable(authorID), started, seconds, comment, created).Scan(&id); err != nil {
		return err
	}
	return im.record("worklog", srcID, "worklog", id)
}

// Attachment stores a file of an issue (the caller streams the content).
func (im *Importer) Attachment(ctx context.Context, issueID int64, srcID, filename, contentType string, authorID int64, created time.Time, r io.Reader) error {
	if _, ok, err := im.find("attachment", srcID); err != nil || ok {
		return err
	}
	ref, err := im.s.blobs.Put(ctx, r, 0, contentType)
	if err != nil {
		return err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO issue_attachments (issue_id, filename, content_type, size, sha256, author_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, issueID, filename, contentType, ref.Size, ref.SHA256, nullable(authorID), created).Scan(&id); err != nil {
		return err
	}
	if err := blob.AddRef(im.ctx(), im.tx, ref.SHA256, "issue-attachment", id); err != nil {
		return err
	}
	return im.record("attachment", srcID, "issue-attachment", id)
}

// AttachmentID returns the Kogge id of an imported attachment.
func (im *Importer) AttachmentID(srcID string) (int64, bool, error) {
	return im.find("attachment", srcID)
}

// Link links two issues.
func (im *Importer) Link(typeID, sourceID, targetID, creatorID int64, created time.Time) error {
	if sourceID == targetID {
		return nil
	}
	_, err := im.tx.Exec(im.ctx(), `INSERT INTO issue_links (link_type_id, source_id, target_id, created_by, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, typeID, sourceID, targetID, nullable(creatorID), created)
	return err
}

// History adds a history entry (status changes etc.), keeping reports working for migrated data.
// Spans derives the spans of an issue's fields (for KQL's WAS) once its history is imported.
func (im *Importer) Spans(issueID int64) error { return rebuildSpans(im.ctx(), im.tx, issueID) }

func (im *Importer) History(issueID int64, srcID string, actorID int64, at time.Time, items []ChangeItem) error {
	if len(items) == 0 {
		return nil
	}
	if _, ok, err := im.find("change", srcID); err != nil || ok {
		return err
	}
	b, _ := json.Marshal(items)
	var id int64
	if err := im.tx.QueryRow(im.ctx(), "INSERT INTO issue_changes (issue_id, actor_id, at, items) VALUES ($1, $2, $3, $4) RETURNING id",
		issueID, nullable(actorID), at, b).Scan(&id); err != nil {
		return err
	}
	return im.record("change", srcID, "issue-change", id)
}

// Watchers adds watchers of an issue.
func (im *Importer) Watchers(issueID int64, users []int64) error {
	_, err := im.tx.Exec(im.ctx(), `INSERT INTO watchers (entity_type, entity_id, user_id) SELECT 'issue', $1, unnest($2::bigint[])
		ON CONFLICT DO NOTHING`, issueID, nonNil(users))
	return err
}

// Votes sets the vote count of an issue (voters are not available for every source).
func (im *Importer) Votes(issueID int64, votes int) error {
	_, err := im.tx.Exec(im.ctx(), "UPDATE issues SET votes = $2 WHERE id = $1", issueID, votes)
	return err
}

// Sprint maps a sprint of a board.
func (im *Importer) Sprint(boardID int64, srcID, name, goal, state string, start, end, completed *time.Time, position int) (int64, error) {
	if state != SprintActive && state != SprintClosed {
		state = SprintFuture
	}
	if id, ok, err := im.find("sprint", srcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(im.ctx(), `UPDATE sprints SET name = $2, goal = $3, state = $4, start_at = $5, end_at = $6, completed_at = $7
				WHERE id = $1`, id, name, goal, state, start, end, completed)
		}
		return id, err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO sprints (board_id, name, goal, state, start_at, end_at, completed_at, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, nullable(boardID), name, goal, state, start, end, completed, position).Scan(&id); err != nil {
		return 0, err
	}
	return id, im.record("sprint", srcID, "sprint", id)
}

// IssueSprints sets the sprints of an issue: the closed ones it was part of and the current one.
func (im *Importer) IssueSprints(issueID int64, sprintIDs []int64) error {
	if _, err := im.tx.Exec(im.ctx(), "DELETE FROM issue_sprints WHERE issue_id = $1 AND sprint_id <> ALL($2)", issueID, nonNil(sprintIDs)); err != nil {
		return err
	}
	_, err := im.tx.Exec(im.ctx(), "INSERT INTO issue_sprints (issue_id, sprint_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING",
		issueID, nonNil(sprintIDs))
	return err
}

// ImportBoard describes a board of the source system.
type ImportBoard struct {
	SourceID  string
	ProjectID int64
	Name      string
	Type      string // scrum or kanban
	Query     string
	Config    BoardConfig
	OwnerID   int64
}

// Board creates or updates a board. into names an existing board that takes the source board (the board created
// with an imported project), 0 creates a new one on the first load.
func (im *Importer) Board(in ImportBoard, into int64) (int64, error) {
	ctx := im.ctx()
	if in.Type != BoardScrum {
		in.Type = BoardKanban
	}
	if in.Config.QuickFilters == nil {
		in.Config.QuickFilters = []NamedQuery{}
	}
	if in.Config.Card.Fields == nil {
		in.Config.Card.Fields = []string{}
	}
	if in.Config.Card.Colors == "" {
		in.Config.Card.Colors = "issuetype"
	}
	if in.Config.Swimlanes.Type == "" {
		in.Config.Swimlanes.Type = "none"
	}
	if in.Config.Estimation.Type == "" {
		in.Config.Estimation.Type = "count"
	}
	raw, err := json.Marshal(in.Config)
	if err != nil {
		return 0, err
	}
	id, ok, err := im.find("board", in.SourceID)
	if err != nil {
		return 0, err
	}
	if !ok && into != 0 {
		id, ok = into, true
	}
	if ok {
		_, err = im.tx.Exec(ctx, "UPDATE boards SET name = $2, type = $3, query = $4, config = $5, updated_at = now(), version = version + 1 WHERE id = $1",
			id, in.Name, in.Type, in.Query, raw)
	} else {
		err = im.tx.QueryRow(ctx, `INSERT INTO boards (project_id, name, type, query, config, owner_id) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			nullable(in.ProjectID), in.Name, in.Type, in.Query, raw, nullable(in.OwnerID)).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	return id, im.record("board", in.SourceID, "board", id)
}

// MapBoard records a board of the source system as an existing board (when its configuration is not available).
func (im *Importer) MapBoard(srcID string, boardID int64) error {
	if _, ok, err := im.find("board", srcID); err != nil || ok {
		return err
	}
	return im.record("board", srcID, "board", boardID)
}

// FieldKeyOf returns the key (cf_<id>) of a custom field migrated from a source field.
func (im *Importer) FieldKeyOf(srcID string) (string, bool, error) {
	id, ok, err := im.find("field", srcID)
	if err != nil || !ok {
		return "", ok, err
	}
	return fieldKey(id), true, nil
}

// BoardOf returns the board created with an imported project.
func (im *Importer) BoardOf(projectSourceID string) (int64, bool, error) {
	return im.find("project-board", projectSourceID)
}

// Finish brings a project up to date after a load: the next issue number, search texts.
func (im *Importer) Finish(ctx context.Context, projectID int64) error {
	if _, err := im.tx.Exec(ctx, "UPDATE issue_projects SET next_number = greatest(next_number, coalesce((SELECT max(number) + 1 FROM issues WHERE project_id = $1), 1)) WHERE id = $1", projectID); err != nil {
		return err
	}
	rows, err := im.tx.Query(ctx, "SELECT id FROM issues WHERE project_id = $1", projectID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := im.s.refreshSearch(ctx, im.tx, id); err != nil {
			return err
		}
	}
	return nil
}

// ProjectByKey returns a project's id.
func (im *Importer) ProjectByKey(key string) (int64, bool, error) {
	var id int64
	err := im.tx.QueryRow(im.ctx(), "SELECT project_id FROM issue_project_keys WHERE key = upper($1)", key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, false, nil
	}
	return id, err == nil, err
}
