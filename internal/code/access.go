// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Permissions on code projects and repositories.
const (
	PermRead         = "repo.read"
	PermWrite        = "repo.write"
	PermAdmin        = "repo.admin"
	PermProjectAdmin = "codeproject.admin"
)

// Levels of repository access; a higher level includes the lower ones.
const (
	LevelNone = iota
	LevelRead
	LevelWrite
	LevelAdmin
)

// LevelName names a level for the API.
func LevelName(l int) string {
	switch l {
	case LevelRead:
		return "read"
	case LevelWrite:
		return "write"
	case LevelAdmin:
		return "admin"
	}
	return "none"
}

// levelNamed is the level LevelName names (LevelNone for others).
func levelNamed(name string) int {
	for _, l := range []int{LevelRead, LevelWrite, LevelAdmin} {
		if LevelName(l) == name {
			return l
		}
	}
	return LevelNone
}

func levelOf(perm string) int {
	switch perm {
	case PermRead:
		return LevelRead
	case PermWrite:
		return LevelWrite
	case PermAdmin, PermProjectAdmin:
		return LevelAdmin
	}
	return LevelNone
}

// Access holds what a principal may do in code projects and repositories.
type Access struct {
	userID       int64
	groups       []int64
	admin        bool // global administrator
	deployRepo   int64
	projects     map[int64]int
	projectAdmin map[int64]bool
	repos        map[int64]int
}

// UserID returns the principal's user id (0 for anonymous users and deploy keys).
func (a *Access) UserID() int64 { return a.userID }

// Level returns the access to a repository of a project. Administrators of Kogge have no access of their own to
// content: they grant it themselves (audited) through the code project's permissions, which they manage.
func (a *Access) Level(projectID, repoID int64) int {
	return max(a.projects[projectID], a.repos[repoID])
}

// ProjectAdmin reports whether the principal administers a code project (its settings and permissions, not its
// content).
func (a *Access) ProjectAdmin(projectID int64) bool { return a.admin || a.projectAdmin[projectID] }

// ProjectLevel returns the access granted on a code project (for all its repositories).
func (a *Access) ProjectLevel(projectID int64) int {
	return a.projects[projectID]
}

// VisibleSQL restricts repositories (alias r) to those the principal may read.
func (a *Access) VisibleSQL(alias string, args *db.Args) string {
	return a.LevelSQL(alias, LevelRead, args)
}

// LevelSQL restricts repositories (alias r) to those the principal may access at least at the level, through a
// grant on their project or on themselves (as Level computes it).
func (a *Access) LevelSQL(alias string, level int, args *db.Args) string {
	level = max(level, LevelRead)
	var projects, repos []int64
	for id, l := range a.projects {
		if l >= level {
			projects = append(projects, id)
		}
	}
	for id, l := range a.repos {
		if l >= level {
			repos = append(repos, id)
		}
	}
	return "(" + alias + ".project_id = ANY(" + args.Add(nonNil(projects)) + ") OR " + alias + ".id = ANY(" + args.Add(nonNil(repos)) + "))"
}

// ProjectAdminSQL restricts code projects (alias p) to those the principal administers (as ProjectAdmin decides).
func (a *Access) ProjectAdminSQL(alias string, args *db.Args) string {
	if a.admin {
		return "TRUE"
	}
	ids := []int64{}
	for id, ok := range a.projectAdmin {
		if ok {
			ids = append(ids, id)
		}
	}
	return alias + ".id = ANY(" + args.Add(ids) + ")"
}

// ProjectVisibleSQL restricts code projects (alias p) to those with a readable repository or a grant;
// administrators see all, to manage them.
func (a *Access) ProjectVisibleSQL(alias string, args *db.Args) string {
	if a.admin {
		return "TRUE"
	}
	var projects, repos []int64
	for id, l := range a.projects {
		if l >= LevelRead {
			projects = append(projects, id)
		}
	}
	for id, l := range a.repos {
		if l >= LevelRead {
			repos = append(repos, id)
		}
	}
	return "(" + alias + ".id = ANY(" + args.Add(nonNil(projects)) + ") OR EXISTS (SELECT 1 FROM repositories rr WHERE rr.project_id = " +
		alias + ".id AND rr.id = ANY(" + args.Add(nonNil(repos)) + ")))"
}

type accessEntry struct {
	version int64
	at      time.Time
	access  *Access
}

type accessCache struct {
	mu      sync.Mutex
	entries map[string]accessEntry
}

// Access computes (and briefly caches) the principal's code permissions.
func (s *Service) Access(ctx context.Context, p *identity.Principal) (*Access, error) {
	set := authz.SetOf(p)
	anyone := true
	if !set.Authenticated() {
		st, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		anyone = st.AllowAnonymous
	}
	key := set.Key() + "|" + strconv.FormatBool(anyone) + "|" + strconv.FormatBool(p.AllowsScope(identity.ScopeAdmin))
	version := s.authz.Version()
	s.access.mu.Lock()
	if e, ok := s.access.entries[key]; ok && e.version == version && time.Since(e.at) < 30*time.Second {
		s.access.mu.Unlock()
		return e.access, nil
	}
	s.access.mu.Unlock()

	groups := set.Groups
	if groups == nil {
		groups = []int64{}
	}
	a := &Access{userID: set.UserID, groups: groups, projects: map[int64]int{}, projectAdmin: map[int64]bool{}, repos: map[int64]int{}}
	if set.Authenticated() {
		a.admin, _ = s.authz.IsAdmin(ctx, p)
	}
	rows, err := s.pool.Query(ctx, `SELECT coalesce(project_id, 0), coalesce(repository_id, 0), permission FROM code_permissions
		WHERE (holder_type = 'anyone' AND $3)
		   OR (holder_type = 'logged-in' AND $1 <> 0)
		   OR (holder_type = 'user' AND holder_id = $1 AND $1 <> 0)
		   OR (holder_type = 'group' AND holder_id = ANY($2))`, set.UserID, groups, anyone)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var projectID, repoID int64
		var perm string
		if err := rows.Scan(&projectID, &repoID, &perm); err != nil {
			rows.Close()
			return nil, err
		}
		l := levelOf(perm)
		if projectID != 0 {
			a.projects[projectID] = max(a.projects[projectID], l)
			if perm == PermProjectAdmin {
				a.projectAdmin[projectID] = true
			}
		} else {
			a.repos[repoID] = max(a.repos[repoID], l)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Through access tokens, administering code projects and repositories (permissions, deploy keys, settings) needs
	// the admin scope, like all administration (audit L4): other tokens write at most.
	if !p.AllowsScope(identity.ScopeAdmin) {
		for id, l := range a.projects {
			a.projects[id] = min(l, LevelWrite)
		}
		for id, l := range a.repos {
			a.repos[id] = min(l, LevelWrite)
		}
		clear(a.projectAdmin)
	}
	s.access.mu.Lock()
	if s.access.entries == nil || len(s.access.entries) > 10000 {
		s.access.entries = map[string]accessEntry{}
	}
	s.access.entries[key] = accessEntry{version: version, at: time.Now(), access: a}
	s.access.mu.Unlock()
	return a, nil
}

// deployAccess is the access of a deploy key: one repository, read or write.
func deployAccess(repoID int64, write bool) *Access {
	l := LevelRead
	if write {
		l = LevelWrite
	}
	return &Access{deployRepo: repoID, projects: map[int64]int{}, projectAdmin: map[int64]bool{}, repos: map[int64]int{repoID: l}}
}

// Invalidate drops cached access (after permission changes on any node).
func (s *Service) Invalidate() {
	s.access.mu.Lock()
	s.access.entries = nil
	s.access.mu.Unlock()
}

// RepoVisibility tells the live hub whether a user may receive messages about a repository ("repo:<id>").
func (s *Service) RepoVisibility(ctx context.Context, userID int64, groups []int64, container string) bool {
	id, err := strconv.ParseInt(strings.TrimPrefix(container, "repo:"), 10, 64)
	if err != nil {
		return false
	}
	p := &identity.Principal{Groups: groups, Method: identity.MethodInternal}
	if userID != 0 {
		p.User = &identity.User{ID: userID}
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return false
	}
	var projectID int64
	if err := s.pool.QueryRow(ctx, "SELECT project_id FROM repositories WHERE id = $1", id).Scan(&projectID); err != nil {
		return false
	}
	return a.Level(projectID, id) >= LevelRead
}
