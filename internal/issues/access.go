// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Access holds what a principal may do in issue projects: permissions granted unconditionally, permissions
// granted only as reporter or assignee of an issue, and security level memberships.
type Access struct {
	system   bool // Kogge itself (automation rules): every permission, every issue
	userID   int64
	all      map[int64]map[string]bool
	reporter map[int64]map[string]bool
	assignee map[int64]map[string]bool
	archived map[int64]bool
	levels   map[[2]int64]levelGrant
	secured  map[int64]bool // projects with a security scheme
}

type levelGrant struct{ all, reporter, assignee bool }

// readPermissions stay available in archived projects.
var readPermissions = map[string]bool{PermBrowse: true, PermViewDevelopment: true, PermViewWatchers: true}

// UserID returns the principal's user id (0 for anonymous).
func (a *Access) UserID() int64 { return a.userID }

// Can reports whether the permission is granted in the project independent of a particular issue.
func (a *Access) Can(projectID int64, perm string) bool {
	if a.system {
		return true
	}
	if a.archived[projectID] && !readPermissions[perm] {
		return false
	}
	return a.all[projectID][perm]
}

// CanAny reports whether the permission is granted in the project at least for some issues (e.g. as reporter).
func (a *Access) CanAny(projectID int64, perm string) bool {
	if a.system {
		return true
	}
	if a.archived[projectID] && !readPermissions[perm] {
		return false
	}
	return a.all[projectID][perm] || (a.userID != 0 && (a.reporter[projectID][perm] || a.assignee[projectID][perm]))
}

// issueHead is what permission checks need to know about an issue.
type issueHead struct {
	id, projectID, reporterID, assigneeID, securityLevelID int64
}

func (a *Access) holds(projectID int64, perm string, h issueHead) bool {
	if a.system {
		return true
	}
	if a.archived[projectID] && !readPermissions[perm] {
		return false
	}
	if a.all[projectID][perm] {
		return true
	}
	if a.userID == 0 {
		return false
	}
	return (a.reporter[projectID][perm] && h.reporterID == a.userID) ||
		(a.assignee[projectID][perm] && h.assigneeID == a.userID)
}

// Visible reports whether the principal may see the issue (browse permission and security level).
func (a *Access) visible(h issueHead) bool {
	if a.system {
		return true
	}
	if !a.holds(h.projectID, PermBrowse, h) {
		return false
	}
	if h.securityLevelID == 0 {
		return true
	}
	g := a.levels[[2]int64{h.projectID, h.securityLevelID}]
	return g.all || (a.userID != 0 && ((g.reporter && h.reporterID == a.userID) || (g.assignee && h.assigneeID == a.userID)))
}

// CanIssue reports whether the permission is granted for the issue (which must be visible).
func (a *Access) CanIssue(h issueHead, perm string) bool {
	return a.visible(h) && a.holds(h.projectID, perm, h)
}

// ProjectsWith returns the projects in which the principal holds a permission for all issues.
func (a *Access) ProjectsWith(perm string) []int64 {
	out := []int64{}
	for p := range a.all {
		if a.Can(p, perm) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// BrowsableProjects returns the projects in which the principal can see at least some issues.
func (a *Access) BrowsableProjects() []int64 {
	seen := map[int64]bool{}
	for p, perms := range a.all {
		if perms[PermBrowse] {
			seen[p] = true
		}
	}
	if a.userID != 0 {
		for _, m := range []map[int64]map[string]bool{a.reporter, a.assignee} {
			for p, perms := range m {
				if perms[PermBrowse] {
					seen[p] = true
				}
			}
		}
	}
	out := make([]int64, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// VisibleSQL returns an SQL condition that restricts the issues aliased as alias to those the principal may see.
func (a *Access) VisibleSQL(alias string, args *db.Args) string {
	if a.system {
		return "true"
	}
	var all, rep, asg []int64
	for p, perms := range a.all {
		if perms[PermBrowse] {
			all = append(all, p)
		}
	}
	if a.userID != 0 {
		for p, perms := range a.reporter {
			if perms[PermBrowse] {
				rep = append(rep, p)
			}
		}
		for p, perms := range a.assignee {
			if perms[PermBrowse] {
				asg = append(asg, p)
			}
		}
	}
	if len(all)+len(rep)+len(asg) == 0 {
		return "false"
	}
	// The user id is bound only if it is used (PostgreSQL cannot type unused parameters).
	uidParam := ""
	uid := func() string {
		if uidParam == "" {
			uidParam = args.Add(a.userID)
		}
		return uidParam
	}
	browse := "(" + alias + ".project_id = ANY(" + args.Add(nonNil(all)) + ")"
	if len(rep) > 0 {
		browse += " OR (" + alias + ".project_id = ANY(" + args.Add(rep) + ") AND " + alias + ".reporter_id = " + uid() + ")"
	}
	if len(asg) > 0 {
		browse += " OR (" + alias + ".project_id = ANY(" + args.Add(asg) + ") AND " + alias + ".assignee_id = " + uid() + ")"
	}
	browse += ")"

	var lp, ll, rp, rl, ap, al []int64
	for k, g := range a.levels {
		if g.all {
			lp, ll = append(lp, k[0]), append(ll, k[1])
		}
		if g.reporter && a.userID != 0 {
			rp, rl = append(rp, k[0]), append(rl, k[1])
		}
		if g.assignee && a.userID != 0 {
			ap, al = append(ap, k[0]), append(al, k[1])
		}
	}
	pair := "(" + alias + ".project_id, " + alias + ".security_level_id)"
	level := "(" + alias + ".security_level_id IS NULL"
	if len(lp) > 0 {
		level += " OR " + pair + " IN (SELECT * FROM unnest(" + args.Add(lp) + "::bigint[], " + args.Add(ll) + "::bigint[]))"
	}
	if len(rp) > 0 {
		level += " OR (" + pair + " IN (SELECT * FROM unnest(" + args.Add(rp) + "::bigint[], " + args.Add(rl) +
			"::bigint[])) AND " + alias + ".reporter_id = " + uid() + ")"
	}
	if len(ap) > 0 {
		level += " OR (" + pair + " IN (SELECT * FROM unnest(" + args.Add(ap) + "::bigint[], " + args.Add(al) +
			"::bigint[])) AND " + alias + ".assignee_id = " + uid() + ")"
	}
	level += ")"
	return "(" + browse + " AND " + level + ")"
}

func nonNil(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

type accessCache struct {
	mu      sync.Mutex
	entries map[string]accessEntry
}

type accessEntry struct {
	version int64
	at      time.Time
	access  *Access
}

// Access computes (and briefly caches) the principal's access to issue projects.
func (s *Service) Access(ctx context.Context, p *identity.Principal) (*Access, error) {
	// Kogge's own principal exists only inside the server (it cannot sign in).
	if p != nil && p.User != nil && p.User.ID == identity.SystemUserID && p.User.Kind == identity.KindSystem && p.Method == identity.MethodInternal {
		return &Access{system: true, userID: p.User.ID}, nil
	}
	set := authz.SetOf(p)
	if !set.Authenticated() {
		st, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		if !st.AllowAnonymous {
			return &Access{}, nil
		}
	}
	key := set.Key() + "|" + strconv.FormatBool(p.AllowsScope(identity.ScopeAdmin))
	version := s.authz.Version()
	s.access.mu.Lock()
	if e, ok := s.access.entries[key]; ok && e.version == version && time.Since(e.at) < 30*time.Second {
		s.access.mu.Unlock()
		return e.access, nil
	}
	s.access.mu.Unlock()

	a := &Access{userID: set.UserID, all: map[int64]map[string]bool{}, reporter: map[int64]map[string]bool{},
		assignee: map[int64]map[string]bool{}, archived: map[int64]bool{}, levels: map[[2]int64]levelGrant{}, secured: map[int64]bool{}}
	groups := set.Groups
	if groups == nil {
		groups = []int64{}
	}
	roleMember := `EXISTS (SELECT 1 FROM project_role_members rm WHERE rm.project_id = p.id AND rm.role_id = %s.holder_id
		AND ((rm.principal_type = 'user' AND rm.principal_id = $1) OR (rm.principal_type = 'group' AND rm.principal_id = ANY($2))))`
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.archived, g.permission, g.holder_type
		FROM issue_projects p JOIN permission_scheme_grants g ON g.scheme_id = p.permission_scheme_id
		WHERE g.holder_type = 'anyone'
		   OR (g.holder_type IN ('logged-in', 'reporter', 'assignee') AND $1 <> 0)
		   OR (g.holder_type = 'user' AND g.holder_id = $1 AND $1 <> 0)
		   OR (g.holder_type = 'group' AND g.holder_id = ANY($2))
		   OR (g.holder_type = 'project-lead' AND p.lead_id = $1)
		   OR (g.holder_type = 'role' AND `+fmt.Sprintf(roleMember, "g")+`)`, set.UserID, groups)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pid int64
		var archived bool
		var perm, holder string
		if err := rows.Scan(&pid, &archived, &perm, &holder); err != nil {
			rows.Close()
			return nil, err
		}
		a.archived[pid] = archived
		target := a.all
		switch holder {
		case "reporter":
			target = a.reporter
		case "assignee":
			target = a.assignee
		}
		// Through access tokens, administering a project needs the admin scope (like all administration).
		if perm == PermAdmin && !p.AllowsScope(identity.ScopeAdmin) {
			continue
		}
		if target[pid] == nil {
			target[pid] = map[string]bool{}
		}
		target[pid][perm] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, "SELECT id FROM issue_projects WHERE security_scheme_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	secured, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range secured {
		a.secured[id] = true
	}

	rows, err = s.pool.Query(ctx, `SELECT p.id, l.id, m.holder_type
		FROM issue_projects p JOIN security_levels l ON l.scheme_id = p.security_scheme_id
		JOIN security_level_members m ON m.level_id = l.id
		WHERE (m.holder_type IN ('reporter', 'assignee') AND $1 <> 0)
		   OR (m.holder_type = 'user' AND m.holder_id = $1 AND $1 <> 0)
		   OR (m.holder_type = 'group' AND m.holder_id = ANY($2))
		   OR (m.holder_type = 'project-lead' AND p.lead_id = $1)
		   OR (m.holder_type = 'role' AND `+fmt.Sprintf(roleMember, "m")+`)`, set.UserID, groups)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pid, lid int64
		var holder string
		if err := rows.Scan(&pid, &lid, &holder); err != nil {
			rows.Close()
			return nil, err
		}
		k := [2]int64{pid, lid}
		g := a.levels[k]
		switch holder {
		case "reporter":
			g.reporter = true
		case "assignee":
			g.assignee = true
		default:
			g.all = true
		}
		a.levels[k] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.access.mu.Lock()
	if s.access.entries == nil || len(s.access.entries) > 5000 {
		s.access.entries = map[string]accessEntry{}
	}
	s.access.entries[key] = accessEntry{version: version, at: time.Now(), access: a}
	s.access.mu.Unlock()
	return a, nil
}
