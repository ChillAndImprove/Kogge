// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Space permissions.
const (
	PermView          = "space.view"
	PermEdit          = "page.edit"
	PermDeleteOwn     = "page.delete-own"
	PermDelete        = "page.delete"
	PermArchive       = "page.archive"
	PermComment       = "comment.add"
	PermCommentDelete = "comment.delete"
	PermAttach        = "attachment.add"
	PermAttachDelete  = "attachment.delete"
	PermRestrict      = "page.restrict"
	PermExport        = "space.export"
	PermAdmin         = "space.admin"
)

// SpacePermissions lists the space permissions in display order.
var SpacePermissions = []string{PermView, PermEdit, PermDeleteOwn, PermDelete, PermArchive, PermComment, PermCommentDelete,
	PermAttach, PermAttachDelete, PermRestrict, PermExport, PermAdmin}

// readPermissions remain in archived spaces.
var readPermissions = map[string]bool{PermView: true, PermExport: true, PermAdmin: true}

// Access holds what a principal may do in spaces.
type Access struct {
	userID   int64
	groups   []int64
	perms    map[int64]map[string]bool
	archived map[int64]bool
	admin    bool // global administrator
}

// UserID returns the principal's user id (0 for anonymous users).
func (a *Access) UserID() int64 { return a.userID }

// Can reports whether the principal holds a permission in a space. Space administrators hold all permissions;
// archived spaces are read-only.
func (a *Access) Can(spaceID int64, perm string) bool {
	if a.archived[spaceID] && !readPermissions[perm] {
		return false
	}
	p := a.perms[spaceID]
	return p[perm] || p[PermAdmin]
}

// Spaces returns the spaces in which the principal holds a permission.
func (a *Access) Spaces(perm string) []int64 {
	out := []int64{}
	for id := range a.perms {
		if a.Can(id, perm) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// passes reports whether the principal is among the holders of a restriction.
func (a *Access) passes(holders []Holder) bool {
	for _, h := range holders {
		if (h.Type == "user" && h.ID == a.userID && a.userID != 0) || (h.Type == "group" && slices.Contains(a.groups, h.ID)) {
			return true
		}
	}
	return false
}

// VisibleSQL restricts pages aliased as alias to those the principal may read: the space grants space.view and
// the principal passes the view restrictions of the page and its ancestors (space administrators pass all).
func (a *Access) VisibleSQL(alias string, args *db.Args) string {
	view, admin := []int64{}, []int64{}
	for id := range a.perms {
		if a.Can(id, PermView) {
			view = append(view, id)
			if a.Can(id, PermAdmin) {
				admin = append(admin, id)
			}
		}
	}
	if len(view) == 0 {
		return "false"
	}
	groups := a.groups
	if groups == nil {
		groups = []int64{}
	}
	pass := "NOT EXISTS (SELECT 1 FROM unnest(" + alias + ".view_restricted_by) vr(page_id) WHERE NOT EXISTS (SELECT 1 FROM page_restrictions r " +
		"WHERE r.page_id = vr.page_id AND r.kind = 'view' AND ((r.holder_type = 'user' AND r.holder_id = " + args.Add(a.userID) +
		" AND " + args.Add(a.userID != 0) + ") OR (r.holder_type = 'group' AND r.holder_id = ANY(" + args.Add(groups) + ")))))"
	cond := "(" + alias + ".space_id = ANY(" + args.Add(view) + ") AND (cardinality(" + alias + ".view_restricted_by) = 0 OR " + pass
	if len(admin) > 0 {
		cond += " OR " + alias + ".space_id = ANY(" + args.Add(admin) + ")"
	}
	return cond + "))"
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

// Access computes (and briefly caches) the principal's space permissions.
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
	a := &Access{userID: set.UserID, groups: groups, perms: map[int64]map[string]bool{}, archived: map[int64]bool{}}
	if set.Authenticated() {
		a.admin, _ = s.authz.IsAdmin(ctx, p)
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.archived, g.permission FROM spaces s JOIN space_permissions g ON g.space_id = s.id
		WHERE (g.holder_type = 'anyone' AND $3)
		   OR (g.holder_type = 'logged-in' AND $1 <> 0)
		   OR (g.holder_type = 'user' AND g.holder_id = $1 AND $1 <> 0)
		   OR (g.holder_type = 'group' AND g.holder_id = ANY($2))`, set.UserID, groups, anyone)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var archived bool
		var perm string
		if err := rows.Scan(&id, &archived, &perm); err != nil {
			rows.Close()
			return nil, err
		}
		a.archived[id] = archived
		if a.perms[id] == nil {
			a.perms[id] = map[string]bool{}
		}
		a.perms[id][perm] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Through access tokens, administering a space needs the admin scope, like all administration: other tokens
	// keep what space administration includes, except administering.
	if !p.AllowsScope(identity.ScopeAdmin) {
		for _, perms := range a.perms {
			if perms[PermAdmin] {
				for _, perm := range SpacePermissions {
					perms[perm] = true
				}
				delete(perms, PermAdmin)
			}
		}
	}
	s.access.mu.Lock()
	if s.access.entries == nil || len(s.access.entries) > 10000 {
		s.access.entries = map[string]accessEntry{}
	}
	s.access.entries[key] = accessEntry{version: version, at: time.Now(), access: a}
	s.access.mu.Unlock()
	return a, nil
}

// Invalidate drops cached access (after permission changes on any node).
func (s *Service) Invalidate() {
	s.access.mu.Lock()
	s.access.entries = nil
	s.access.mu.Unlock()
}

// Holder is a user or group in a page restriction or a space grant.
type Holder struct {
	Type string `json:"type" enum:"user,group,logged-in,anyone"`
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name,omitempty" readOnly:"true"`
}

// canView checks the view restrictions of a page (with its ancestors) for the principal.
func (s *Service) canView(ctx context.Context, q db.Querier, a *Access, pg *pageRow) (bool, error) {
	if !a.Can(pg.SpaceID, PermView) {
		return false, nil
	}
	if len(pg.ViewRestrictedBy) == 0 || a.Can(pg.SpaceID, PermAdmin) {
		return true, nil
	}
	rows, err := q.Query(ctx, "SELECT page_id, holder_type, holder_id FROM page_restrictions WHERE kind = 'view' AND page_id = ANY($1)", pg.ViewRestrictedBy)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	sets := map[int64][]Holder{}
	for rows.Next() {
		var pid int64
		var h Holder
		if err := rows.Scan(&pid, &h.Type, &h.ID); err != nil {
			return false, err
		}
		sets[pid] = append(sets[pid], h)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, pid := range pg.ViewRestrictedBy {
		if !a.passes(sets[pid]) {
			return false, nil
		}
	}
	return true, nil
}

// canEdit checks page.edit and the edit restrictions of a page itself (not inherited).
func (s *Service) canEdit(ctx context.Context, q db.Querier, a *Access, pg *pageRow) (bool, error) {
	if !a.Can(pg.SpaceID, PermEdit) {
		return false, nil
	}
	return s.passesEditRestrictions(ctx, q, a, pg)
}

// passesEditRestrictions reports whether the principal passes the edit restrictions of a page (space administrators
// pass all). Every change of a page needs it besides its permission: editing, trashing, archiving, attachments.
func (s *Service) passesEditRestrictions(ctx context.Context, q db.Querier, a *Access, pg *pageRow) (bool, error) {
	if a.Can(pg.SpaceID, PermAdmin) {
		return true, nil
	}
	rows, err := q.Query(ctx, "SELECT holder_type, holder_id FROM page_restrictions WHERE kind = 'edit' AND page_id = $1", pg.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var hs []Holder
	for rows.Next() {
		var h Holder
		if err := rows.Scan(&h.Type, &h.ID); err != nil {
			return false, err
		}
		hs = append(hs, h)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return len(hs) == 0 || a.passes(hs), nil
}

func notFoundPage() error {
	return apperr.NotFound("page.not-found", "The page does not exist or you may not see it.")
}

func validLabel(l string) bool {
	if l == "" || len(l) > 100 {
		return false
	}
	return !strings.ContainsAny(l, " \t\r\n,;") && strings.ToLower(l) == l
}
