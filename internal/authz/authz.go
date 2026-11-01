// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package authz holds the permission catalogue, evaluates global permissions and provides the building blocks
// for module permissions (principal sets and their SQL predicates). Permissions are granted, never denied.
package authz

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Global permissions.
const (
	Admin               = "admin"
	ProjectCreate       = "project.create"
	SpaceCreate         = "space.create"
	SpaceCreatePersonal = "space.create-personal"
	CodeProjectCreate   = "codeproject.create"
	UserBrowse          = "user.browse"
	AgentManage         = "agent.manage"
)

// PermissionInfo documents a permission for the administration.
type PermissionInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// GlobalPermissions is the catalogue of global permissions in display order.
var GlobalPermissions = []PermissionInfo{
	{Admin, "Administer Kogge", "Users, groups, configuration, schemes, settings, audit and imports."},
	{ProjectCreate, "Create projects", "Create issue projects."},
	{SpaceCreate, "Create spaces", "Create team spaces in the wiki."},
	{SpaceCreatePersonal, "Create personal space", "Create one's own personal space."},
	{CodeProjectCreate, "Create code projects", "Create code projects for repositories."},
	{UserBrowse, "Browse users", "See users and groups in pickers and profiles."},
	{AgentManage, "Manage build agents", "Register agents and manage global variables and secrets."},
}

// IsGlobalPermission reports whether key is in the catalogue.
func IsGlobalPermission(key string) bool {
	return slices.ContainsFunc(GlobalPermissions, func(p PermissionInfo) bool { return p.Key == key })
}

// Principal types of grants.
const (
	PrincipalUser     = "user"
	PrincipalGroup    = "group"
	PrincipalLoggedIn = "logged-in"
	PrincipalAnyone   = "anyone"
)

// Set is the set of principals a request acts as: the user, their groups, "logged-in" and "anyone".
type Set struct {
	UserID int64
	Groups []int64
}

// SetOf returns the principal set of a request principal.
func SetOf(p *identity.Principal) Set {
	if !p.Authenticated() {
		return Set{}
	}
	return Set{UserID: p.UserID(), Groups: p.Groups}
}

// Authenticated reports whether the set includes a user.
func (s Set) Authenticated() bool { return s.UserID != 0 }

// Match returns an SQL condition that is true for grants (typeCol, idCol) given to a member of the set.
func (s Set) Match(typeCol, idCol string, args *db.Args) string {
	groups := s.Groups
	if groups == nil {
		groups = []int64{}
	}
	return "(" + typeCol + " = 'anyone' OR (" + typeCol + " = 'logged-in' AND " + args.Add(s.UserID != 0) + ") OR (" +
		typeCol + " = 'user' AND " + idCol + " = " + args.Add(s.UserID) + ") OR (" + typeCol + " = 'group' AND " +
		idCol + " = ANY(" + args.Add(groups) + ")))"
}

// Key identifies the set for caches.
func (s Set) Key() string {
	b := make([]byte, 0, 16+8*len(s.Groups))
	b = strconv.AppendInt(b, s.UserID, 10)
	for _, g := range s.Groups {
		b = append(b, ',')
		b = strconv.AppendInt(b, g, 10)
	}
	return string(b)
}

// Service evaluates global permissions with a short-lived cache. Invalidate is called after any change to
// grants or memberships, on every node.
type Service struct {
	pool    *pgxpool.Pool
	version atomic.Int64
	mu      sync.Mutex
	cache   map[string]cached
}

type cached struct {
	version int64
	at      time.Time
	perms   map[string]bool
}

// NewService creates the service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, cache: map[string]cached{}}
}

// Invalidate drops cached permissions.
func (s *Service) Invalidate() {
	s.version.Add(1)
	s.mu.Lock()
	s.cache = map[string]cached{}
	s.mu.Unlock()
}

// Version changes whenever permissions may have changed (for caches of modules).
func (s *Service) Version() int64 { return s.version.Load() }

// Global returns the global permissions of the principal.
func (s *Service) Global(ctx context.Context, p *identity.Principal) (map[string]bool, error) {
	set := SetOf(p)
	key := set.Key()
	v := s.version.Load()
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && c.version == v && time.Since(c.at) < 30*time.Second {
		s.mu.Unlock()
		return c.perms, nil
	}
	s.mu.Unlock()
	var args db.Args
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT permission FROM global_grants WHERE "+
		set.Match("principal_type", "principal_id", &args), args...)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	perms := map[string]bool{}
	for _, k := range keys {
		perms[k] = true
	}
	s.mu.Lock()
	if len(s.cache) > 10000 {
		s.cache = map[string]cached{}
	}
	s.cache[key] = cached{version: v, at: time.Now(), perms: perms}
	s.mu.Unlock()
	return perms, nil
}

// HasGlobal reports whether the principal has a global permission. Administrators have all global permissions —
// through access tokens only with the admin scope, like administration itself.
func (s *Service) HasGlobal(ctx context.Context, p *identity.Principal, perm string) (bool, error) {
	perms, err := s.Global(ctx, p)
	if err != nil {
		return false, err
	}
	if perms[perm] && perm != Admin {
		return true, nil
	}
	return perms[Admin] && p.AllowsScope(identity.ScopeAdmin), nil
}

// RequireUser returns the principal of the request or 401.
func RequireUser(ctx context.Context) (*identity.Principal, error) {
	p := identity.From(ctx)
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	return p, nil
}

// IsAdmin reports whether the principal administers Kogge. Through access tokens, administration needs the admin
// scope; modules use it for what administrators may do in them (managing permissions, not reading content).
func (s *Service) IsAdmin(ctx context.Context, p *identity.Principal) (bool, error) {
	if !p.Authenticated() || !p.AllowsScope(identity.ScopeAdmin) {
		return false, nil
	}
	return s.HasGlobal(ctx, p, Admin)
}

// RequireGlobal returns the principal if it has the global permission, otherwise 401 or 403. Administration
// through access tokens additionally requires the admin scope.
func (s *Service) RequireGlobal(ctx context.Context, perm string) (*identity.Principal, error) {
	p, err := RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := s.HasGlobal(ctx, p, perm)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Say why when the permission would come from administration through a token without the admin scope.
		if perms, err := s.Global(ctx, p); err == nil && perms[Admin] && !p.AllowsScope(identity.ScopeAdmin) {
			return nil, apperr.Forbidden("auth.token-scope", "Administration through an access token needs the admin scope.")
		}
		return nil, apperr.Forbidden("permission.denied", "You do not have the permission %q.", perm)
	}
	return p, nil
}

// Grant is a global grant.
type Grant struct {
	Permission    string `json:"permission"`
	PrincipalType string `json:"principalType" enum:"user,group,logged-in,anyone"`
	PrincipalID   int64  `json:"principalId,omitempty"`
	PrincipalName string `json:"principalName,omitempty"`
}

// ListGrants returns all global grants with principal names.
func ListGrants(ctx context.Context, q db.Querier) ([]Grant, error) {
	rows, err := q.Query(ctx, `SELECT g.permission, g.principal_type, g.principal_id,
		coalesce(u.display_name, gr.name, '') FROM global_grants g
		LEFT JOIN users u ON g.principal_type = 'user' AND u.id = g.principal_id
		LEFT JOIN groups gr ON g.principal_type = 'group' AND gr.id = g.principal_id
		ORDER BY g.permission, g.principal_type, 4`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Grant, error) {
		var g Grant
		err := row.Scan(&g.Permission, &g.PrincipalType, &g.PrincipalID, &g.PrincipalName)
		return g, err
	})
}

// AddGrant grants a global permission.
func AddGrant(ctx context.Context, q db.Querier, g Grant) error {
	if !IsGlobalPermission(g.Permission) {
		return apperr.Invalidf("permission.unknown", "Unknown permission %s.", g.Permission)
	}
	switch g.PrincipalType {
	case PrincipalUser, PrincipalGroup:
		if g.PrincipalID == 0 {
			return apperr.Invalid("permission.principal", "A user or group is required.")
		}
	case PrincipalLoggedIn, PrincipalAnyone:
		g.PrincipalID = 0
		if g.Permission == Admin {
			return apperr.Invalid("permission.principal", "Administration can only be granted to users and groups.")
		}
	default:
		return apperr.Invalidf("permission.principal", "Unknown principal type %s.", g.PrincipalType)
	}
	_, err := q.Exec(ctx, `INSERT INTO global_grants (permission, principal_type, principal_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, g.Permission, g.PrincipalType, g.PrincipalID)
	if db.IsForeignKeyViolation(err) {
		return apperr.NotFound("permission.principal", "The user or group does not exist.")
	}
	return err
}

// RemoveGrant revokes a global permission. The last administrator grant cannot be removed.
func RemoveGrant(ctx context.Context, q db.Querier, g Grant) error {
	if g.Permission == Admin {
		var others int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM global_grants WHERE permission = 'admin'
			AND NOT (principal_type = $1 AND principal_id = $2)`, g.PrincipalType, g.PrincipalID).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return apperr.Conflict("permission.last-admin", "At least one administrator grant must remain.")
		}
	}
	_, err := q.Exec(ctx, "DELETE FROM global_grants WHERE permission = $1 AND principal_type = $2 AND principal_id = $3",
		g.Permission, g.PrincipalType, g.PrincipalID)
	return err
}
