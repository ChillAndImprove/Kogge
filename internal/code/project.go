// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/live"
)

// Project is a code project: a group of repositories with common permissions.
type Project struct {
	ID           int64     `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Repositories int       `json:"repositories" doc:"Number of repositories the caller can see"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
	Permission   string    `json:"permission" enum:"none,read,write,admin" doc:"The caller's access to all repositories of the project"`
	CanAdmin     bool      `json:"canAdmin"`
}

// ProjectRef identifies a code project.
type ProjectRef struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}$`)

func projectNotFound(key string) error {
	return apperr.NotFound("code-project.not-found", "The code project %s does not exist or you may not see it.", key)
}

type projectRow struct {
	ID          int64
	Key         string
	Name        string
	Description string
	Created     time.Time
	Updated     time.Time
}

func projectByKey(ctx context.Context, q db.Querier, key string) (*projectRow, error) {
	var p projectRow
	err := q.QueryRow(ctx, "SELECT id, key, name, description, created_at, updated_at FROM code_projects WHERE upper(key) = upper($1)", key).
		Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Created, &p.Updated)
	if db.IsNoRows(err) {
		return nil, projectNotFound(key)
	}
	return &p, err
}

func (s *Service) visibleProject(ctx context.Context, q db.Querier, a *Access, key string) (*projectRow, error) {
	p, err := projectByKey(ctx, q, key)
	if err != nil {
		return nil, err
	}
	if a.ProjectLevel(p.ID) >= LevelRead || a.ProjectAdmin(p.ID) {
		return p, nil
	}
	var visible bool
	var args db.Args
	args.Add(p.ID)
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM repositories r WHERE r.project_id = $1 AND "+a.VisibleSQL("r", &args)+")",
		args...).Scan(&visible); err != nil {
		return nil, err
	}
	if !visible {
		return nil, projectNotFound(key)
	}
	return p, nil
}

// projectView is a code project as the principal sees it, with the number of repositories they may see in it.
func projectView(a *Access, p *projectRow, repositories int) Project {
	return Project{ID: p.ID, Key: p.Key, Name: p.Name, Description: p.Description, Repositories: repositories, Created: p.Created,
		Updated: p.Updated, Permission: LevelName(a.ProjectLevel(p.ID)), CanAdmin: a.ProjectAdmin(p.ID)}
}

func (s *Service) toProject(ctx context.Context, a *Access, p *projectRow) (*Project, error) {
	counts, err := s.repositoryCounts(ctx, a, []int64{p.ID})
	if err != nil {
		return nil, err
	}
	out := projectView(a, p, counts[p.ID])
	return &out, nil
}

// repositoryCounts counts the repositories the principal may see in each of the code projects, in one query.
func (s *Service) repositoryCounts(ctx context.Context, a *Access, projectIDs []int64) (map[int64]int, error) {
	counts := map[int64]int{}
	if len(projectIDs) == 0 {
		return counts, nil
	}
	var args db.Args
	ids := args.Add(projectIDs)
	rows, err := s.pool.Query(ctx, "SELECT r.project_id, count(*) FROM repositories r WHERE r.project_id = ANY("+ids+") AND "+
		a.VisibleSQL("r", &args)+" GROUP BY r.project_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

// ProjectFilter selects a page of code projects.
type ProjectFilter struct {
	Query string // part of the key or name
	// Admin keeps the projects the principal administers: those they may create repositories in or move them to.
	Admin  bool
	Limit  int
	Offset int
}

// Projects lists a page of the code projects the principal can see, ordered by name, with their total. Access is
// part of the query, so that totals and pages count only what the principal may see.
func (s *Service) Projects(ctx context.Context, p *identity.Principal, f ProjectFilter) ([]Project, int, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	var args db.Args
	where := []string{a.ProjectVisibleSQL("p", &args)}
	if f.Admin {
		where = append(where, a.ProjectAdminSQL("p", &args))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := args.Add("%" + escapeLike(q) + "%")
		where = append(where, "(p.key ILIKE "+like+" OR p.name ILIKE "+like+")")
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM code_projects p WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := args.Add(max(1, min(f.Limit, 500)))
	offset := args.Add(max(0, f.Offset))
	rows, err := s.pool.Query(ctx, "SELECT p.id, p.key, p.name, p.description, p.created_at, p.updated_at FROM code_projects p WHERE "+
		cond+" ORDER BY lower(p.name), p.id LIMIT "+limit+" OFFSET "+offset, args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (projectRow, error) {
		var p projectRow
		err := r.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Created, &p.Updated)
		return p, err
	})
	if err != nil {
		return nil, 0, err
	}
	ids := make([]int64, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	counts, err := s.repositoryCounts(ctx, a, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Project, 0, len(list))
	for i := range list {
		out = append(out, projectView(a, &list[i], counts[list[i].ID]))
	}
	return out, total, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// GetProject returns a code project.
func (s *Service) GetProject(ctx context.Context, p *identity.Principal, key string) (*Project, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return nil, err
	}
	return s.toProject(ctx, a, pr)
}

// NewProject creates a code project.
type NewProject struct {
	Key         string `json:"key" minLength:"1" maxLength:"32" doc:"Upper-case letters, digits and _; starts with a letter"`
	Name        string `json:"name" minLength:"1" maxLength:"100"`
	Description string `json:"description,omitempty" maxLength:"2000"`
	Visibility  string `json:"visibility,omitempty" enum:"private,internal,public" default:"private" doc:"internal: all signed-in users can read; public: also anonymous users (if the instance allows them)"`
}

// CreateProject creates a code project; the creator administers it.
func (s *Service) CreateProject(ctx context.Context, p *identity.Principal, in NewProject) (*Project, error) {
	if _, err := s.authz.RequireGlobal(ctx, authz.CodeProjectCreate); err != nil {
		return nil, err
	}
	key := strings.ToUpper(strings.TrimSpace(in.Key))
	if !projectKeyPattern.MatchString(key) {
		return nil, apperr.Invalid("code-project.key", "The key must start with a letter and have up to 32 upper-case letters, digits or _.")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, apperr.Invalid("code-project.name", "The name must not be empty.")
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, "INSERT INTO code_projects (key, name, description, created_by) VALUES ($1, $2, $3, $4) RETURNING id",
			key, name, in.Description, p.UserID()).Scan(&id)
		if db.IsUniqueViolation(err, "code_projects_key_key") {
			return apperr.Conflict("code-project.key-taken", "The key %s is already in use.", key)
		}
		if db.IsUniqueViolation(err, "code_projects_name_key") {
			return apperr.Conflict("code-project.name-taken", "A code project named %s already exists.", name)
		}
		if err != nil {
			return err
		}
		grants := []Grant{{Permission: PermProjectAdmin, Holder: Holder{Type: "user", ID: p.UserID()}}}
		switch in.Visibility {
		case "internal":
			grants = append(grants, Grant{Permission: PermRead, Holder: Holder{Type: "logged-in"}})
		case "public":
			grants = append(grants, Grant{Permission: PermRead, Holder: Holder{Type: "anyone"}})
		}
		if err := insertGrants(ctx, tx, id, 0, grants); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "code-project.create", TargetType: "code-project", TargetID: key,
			Details: map[string]any{"name": name, "visibility": in.Visibility}})
	})
	if err != nil {
		return nil, err
	}
	s.authz.Invalidate()
	return s.GetProject(ctx, p, key)
}

// ProjectUpdate holds optional changes of a code project.
type ProjectUpdate struct {
	Name        *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Description *string `json:"description,omitempty" maxLength:"2000"`
}

// UpdateProject changes a code project (project administrators).
func (s *Service) UpdateProject(ctx context.Context, p *identity.Principal, key string, in ProjectUpdate) (*Project, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return nil, err
	}
	if !a.ProjectAdmin(pr.ID) {
		return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can change it.")
	}
	name, desc := pr.Name, pr.Description
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		desc = *in.Description
	}
	_, err = s.pool.Exec(ctx, "UPDATE code_projects SET name = $2, description = $3, updated_at = now(), version = version + 1 WHERE id = $1",
		pr.ID, name, desc)
	if db.IsUniqueViolation(err, "code_projects_name_key") {
		return nil, apperr.Conflict("code-project.name-taken", "A code project named %s already exists.", name)
	}
	if err != nil {
		return nil, err
	}
	return s.GetProject(ctx, p, key)
}

// DeleteProject deletes an empty code project.
func (s *Service) DeleteProject(ctx context.Context, p *identity.Principal, key string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return err
	}
	if !a.ProjectAdmin(pr.ID) {
		return apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can delete it.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var repos int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM repositories WHERE project_id = $1", pr.ID).Scan(&repos); err != nil {
			return err
		}
		if repos > 0 {
			return apperr.Conflict("code-project.not-empty", "Delete or move the %d repositories of the project first.", repos)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM code_projects WHERE id = $1", pr.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "code-project.delete", TargetType: "code-project", TargetID: pr.Key})
	})
}

// Holder is a user, a group, the signed-in users or anyone.
type Holder struct {
	Type string `json:"type" enum:"user,group,logged-in,anyone"`
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name,omitempty" readOnly:"true"`
}

// Grant is a permission granted on a code project or repository.
type Grant struct {
	Permission string `json:"permission" enum:"repo.read,repo.write,repo.admin,codeproject.admin"`
	Holder     Holder `json:"holder"`
}

func validateGrants(grants []Grant, project bool) error {
	for _, g := range grants {
		if g.Permission == PermProjectAdmin && !project {
			return apperr.Invalid("code.permission", "Project administration is granted on code projects.")
		}
		if levelOf(g.Permission) == LevelNone {
			return apperr.Invalidf("code.permission", "Unknown permission %s.", g.Permission)
		}
		switch g.Holder.Type {
		case "user", "group":
			if g.Holder.ID <= 0 {
				return apperr.Invalid("code.holder", "Users and groups need an id.")
			}
		case "logged-in", "anyone":
			if g.Permission != PermRead {
				return apperr.Invalid("code.holder", "All signed-in users or anyone can only be granted read access.")
			}
		default:
			return apperr.Invalidf("code.holder", "Unknown holder %s.", g.Holder.Type)
		}
	}
	return nil
}

func insertGrants(ctx context.Context, tx pgx.Tx, projectID, repoID int64, grants []Grant) error {
	for _, g := range grants {
		id := g.Holder.ID
		if g.Holder.Type == "logged-in" || g.Holder.Type == "anyone" {
			id = 0
		}
		if _, err := tx.Exec(ctx, `INSERT INTO code_permissions (project_id, repository_id, permission, holder_type, holder_id)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, nullID(projectID), nullID(repoID), g.Permission, g.Holder.Type, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) grantsOf(ctx context.Context, projectID, repoID int64) ([]Grant, error) {
	column, id := "project_id", projectID
	if repoID != 0 {
		column, id = "repository_id", repoID
	}
	rows, err := s.pool.Query(ctx, `SELECT g.permission, g.holder_type, g.holder_id, coalesce(u.display_name, gr.name, '')
		FROM code_permissions g LEFT JOIN users u ON g.holder_type = 'user' AND u.id = g.holder_id
		LEFT JOIN groups gr ON g.holder_type = 'group' AND gr.id = g.holder_id
		WHERE g.`+column+` = $1 ORDER BY g.permission DESC, g.holder_type, 4`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) {
		var g Grant
		err := r.Scan(&g.Permission, &g.Holder.Type, &g.Holder.ID, &g.Holder.Name)
		return g, err
	})
}

// ProjectPermissions lists the grants of a code project (project administrators).
func (s *Service) ProjectPermissions(ctx context.Context, p *identity.Principal, key string) ([]Grant, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return nil, err
	}
	if !a.ProjectAdmin(pr.ID) {
		return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can see its permissions.")
	}
	return s.grantsOf(ctx, pr.ID, 0)
}

// SetProjectPermissions replaces the grants of a code project; one user or group must keep administering it.
func (s *Service) SetProjectPermissions(ctx context.Context, p *identity.Principal, key string, grants []Grant) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return err
	}
	if !a.ProjectAdmin(pr.ID) {
		return apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can change its permissions.")
	}
	if err := validateGrants(grants, true); err != nil {
		return err
	}
	if !slices.ContainsFunc(grants, func(g Grant) bool { return g.Permission == PermProjectAdmin }) {
		return apperr.Invalid("code-project.no-admin", "At least one user or group must administer the code project.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM code_permissions WHERE project_id = $1", pr.ID); err != nil {
			return err
		}
		if err := insertGrants(ctx, tx, pr.ID, 0, grants); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "code-project.permissions", TargetType: "code-project",
			TargetID: pr.Key, Details: map[string]any{"grants": len(grants)}})
	})
	if err == nil {
		s.authz.Invalidate()
	}
	return err
}
