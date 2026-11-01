// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/live"
)

// Space is a wiki space.
type Space struct {
	ID          int64             `json:"id"`
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        string            `json:"type" enum:"team,personal"`
	Owner       *identity.UserRef `json:"owner,omitempty"`
	Categories  []string          `json:"categories"`
	HomePageID  int64             `json:"homePageId,omitempty"`
	Archived    bool              `json:"archived"`
	Favourite   bool              `json:"favourite"`
	Watching    bool              `json:"watching"`
	Permissions []string          `json:"permissions" doc:"Space permissions of the caller"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated"`
	// LastUpdated is when something in the space changed last, as far as the caller may see it.
	LastUpdated *time.Time `json:"lastUpdated,omitempty" doc:"In the directory: the latest change of the space or of a page or blog post the caller may read"`
	Version     int64      `json:"version"`
}

var spaceKeyPattern = regexp.MustCompile(`^[A-Z0-9]{1,32}$`)

const spaceColumns = `s.id, s.key, s.name, s.description, s.type, s.owner_id, ou.username, ou.display_name, s.categories,
	coalesce(s.home_page_id, 0), s.archived, s.created_at, s.updated_at, s.version`

// scanSpace reads the spaceColumns and then the extra columns of a query.
func scanSpace(row pgx.Row, extra ...any) (*Space, error) {
	var sp Space
	var owner *int64
	var on, od *string
	if err := row.Scan(append([]any{&sp.ID, &sp.Key, &sp.Name, &sp.Description, &sp.Type, &owner, &on, &od, &sp.Categories,
		&sp.HomePageID, &sp.Archived, &sp.Created, &sp.Updated, &sp.Version}, extra...)...); err != nil {
		return nil, err
	}
	if owner != nil {
		sp.Owner = &identity.UserRef{ID: *owner, Username: *on, DisplayName: *od, Active: true}
	}
	if sp.Categories == nil {
		sp.Categories = []string{}
	}
	return &sp, nil
}

func spaceByKey(ctx context.Context, q db.Querier, key string) (*Space, error) {
	sp, err := scanSpace(q.QueryRow(ctx, "SELECT "+spaceColumns+" FROM spaces s LEFT JOIN users ou ON ou.id = s.owner_id WHERE upper(s.key) = upper($1)", key))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("space.not-found", "The space %s does not exist or you may not see it.", key)
	}
	return sp, err
}

func spaceByID(ctx context.Context, q db.Querier, id int64) (*Space, error) {
	sp, err := scanSpace(q.QueryRow(ctx, "SELECT "+spaceColumns+" FROM spaces s LEFT JOIN users ou ON ou.id = s.owner_id WHERE s.id = $1", id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("space.not-found", "The space does not exist or you may not see it.")
	}
	return sp, err
}

func (s *Service) completeSpace(ctx context.Context, a *Access, sp *Space) error {
	list := []Space{*sp}
	if err := s.completeSpaces(ctx, a, list); err != nil {
		return err
	}
	*sp = list[0]
	return nil
}

// completeSpaces adds the caller's permissions, favourites and watches to spaces, with one query for all of them.
func (s *Service) completeSpaces(ctx context.Context, a *Access, list []Space) error {
	ids := make([]int64, len(list))
	at := make(map[int64]int, len(list))
	for i := range list {
		sp := &list[i]
		sp.Permissions = []string{}
		for _, perm := range SpacePermissions {
			if a.Can(sp.ID, perm) {
				sp.Permissions = append(sp.Permissions, perm)
			}
		}
		ids[i], at[sp.ID] = sp.ID, i
	}
	if a.userID == 0 || len(list) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id, EXISTS (SELECT 1 FROM space_favourites f WHERE f.space_id = s.id AND f.user_id = $2),
		EXISTS (SELECT 1 FROM watchers w WHERE w.entity_type = 'space' AND w.entity_id = s.id AND w.user_id = $2)
		FROM unnest($1::bigint[]) AS s(id)`, ids, a.userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var favourite, watching bool
		if err := rows.Scan(&id, &favourite, &watching); err != nil {
			return err
		}
		list[at[id]].Favourite, list[at[id]].Watching = favourite, watching
	}
	return rows.Err()
}

// visibleSpace returns a space the principal may view (global administrators see every space to repair grants).
func (s *Service) visibleSpace(ctx context.Context, a *Access, key string) (*Space, error) {
	sp, err := spaceByKey(ctx, s.pool, key)
	if err != nil {
		return nil, err
	}
	if !a.Can(sp.ID, PermView) && !a.admin {
		return nil, apperr.NotFound("space.not-found", "The space %s does not exist or you may not see it.", key)
	}
	return sp, s.completeSpace(ctx, a, sp)
}

// SpaceFilter selects a page of spaces in the directory.
type SpaceFilter struct {
	Query     string
	Category  string
	Favourite bool
	Archived  bool
	Type      string
	Owner     string // user name of the owner of personal spaces
	// Permission keeps the spaces in which the principal also holds this permission (pickers of spaces to write in).
	Permission string
	All        bool   // administrators: every space
	Sort       string // name (default), key, type or updated
	Desc       bool
	Limit      int
	Offset     int
}

// SpaceList is a page of the space directory.
type SpaceList struct {
	Total int     `json:"total" doc:"Spaces that match, on all pages"`
	Items []Space `json:"items"`
}

// spaceOrders are the orders of the directory; ties are broken by id, so that pages do not overlap.
var spaceOrders = map[string]string{
	"name":    "lower(s.name) %[1]s, s.id %[1]s",
	"key":     "s.key %[1]s, s.id %[1]s",
	"type":    "s.type %[1]s, lower(s.name) %[1]s, s.id %[1]s",
	"updated": "last_updated %[1]s, s.id %[1]s",
}

// Spaces lists a page of the spaces the principal may see. Access is part of the query, so that totals and pages
// count only what the principal may see.
func (s *Service) Spaces(ctx context.Context, p *identity.Principal, f SpaceFilter) (*SpaceList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := []string{"s.archived = " + args.Add(f.Archived)}
	if !(f.All && a.admin) {
		where = append(where, "s.id = ANY("+args.Add(a.Spaces(PermView))+")")
	}
	if f.Permission != "" {
		where = append(where, "s.id = ANY("+args.Add(a.Spaces(f.Permission))+")")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := args.Add("%" + db.EscapeLike(strings.ToLower(q)) + "%")
		where = append(where, "(lower(s.name) LIKE "+like+" OR lower(s.key) LIKE "+like+")")
	}
	if f.Category != "" {
		where = append(where, args.Add(strings.ToLower(f.Category))+" = ANY(s.categories)")
	}
	if f.Type != "" {
		where = append(where, "s.type = "+args.Add(f.Type))
	}
	if f.Owner != "" {
		where = append(where, "s.owner_id = (SELECT u.id FROM users u WHERE lower(u.username) = lower("+args.Add(f.Owner)+"))")
	}
	if f.Favourite {
		where = append(where, "EXISTS (SELECT 1 FROM space_favourites sf WHERE sf.space_id = s.id AND sf.user_id = "+args.Add(a.userID)+")")
	}
	cond := strings.Join(where, " AND ")
	out := &SpaceList{Items: []Space{}}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM spaces s WHERE "+cond, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	order, ok := spaceOrders[f.Sort]
	if !ok {
		order = spaceOrders["name"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	// The latest change counts pages and blog posts the principal may read, so that hidden pages do not show
	// through it.
	lastUpdated := "greatest(s.updated_at, (SELECT max(p.updated_at) FROM pages p WHERE p.space_id = s.id AND p.status = 'current' AND " +
		a.VisibleSQL("p", &args) + "))"
	limit := args.Add(max(1, min(f.Limit, 500)))
	offset := args.Add(max(0, f.Offset))
	rows, err := s.pool.Query(ctx, "SELECT "+spaceColumns+", "+lastUpdated+" AS last_updated FROM spaces s LEFT JOIN users ou ON ou.id = s.owner_id WHERE "+
		cond+" ORDER BY "+fmt.Sprintf(order, dir)+" LIMIT "+limit+" OFFSET "+offset, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var last time.Time
		sp, err := scanSpace(rows, &last)
		if err != nil {
			return nil, err
		}
		sp.LastUpdated = &last
		out.Items = append(out.Items, *sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, s.completeSpaces(ctx, a, out.Items)
}

// GetSpace returns a space the principal may see.
func (s *Service) GetSpace(ctx context.Context, p *identity.Principal, key string) (*Space, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.visibleSpace(ctx, a, key)
}

// NewSpace creates a team space.
type NewSpace struct {
	Key         string   `json:"key" pattern:"^[A-Za-z0-9]{1,32}$" doc:"Letters and digits, up to 32"`
	Name        string   `json:"name" minLength:"1" maxLength:"200"`
	Description string   `json:"description,omitempty" maxLength:"2000"`
	Categories  []string `json:"categories,omitempty" maxItems:"20"`
	// Open grants logged-in users view, edit, comment and attach (otherwise only the creator has access).
	Open *bool `json:"open,omitempty" doc:"Let all signed-in users read and edit (default true)"`
}

func normaliseCategories(in []string) []string {
	out := []string{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// CreateSpace creates a team space with a home page; the creator administers it.
func (s *Service) CreateSpace(ctx context.Context, p *identity.Principal, in NewSpace) (*Space, error) {
	if _, err := s.authz.RequireGlobal(ctx, authz.SpaceCreate); err != nil {
		return nil, err
	}
	key := strings.ToUpper(strings.TrimSpace(in.Key))
	if !spaceKeyPattern.MatchString(key) {
		return nil, apperr.Invalid("space.key", "The key must have 1–32 letters or digits.")
	}
	open := in.Open == nil || *in.Open
	var id int64
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.createSpace(ctx, tx, p.UserID(), key, strings.TrimSpace(in.Name), in.Description, "team", normaliseCategories(in.Categories))
		if err != nil {
			return err
		}
		grants := []grant{}
		for _, perm := range SpacePermissions {
			grants = append(grants, grant{perm, "user", p.UserID()})
		}
		if open {
			for _, perm := range []string{PermView, PermEdit, PermDeleteOwn, PermComment, PermAttach, PermExport} {
				grants = append(grants, grant{perm, "logged-in", 0})
			}
		}
		if err := insertGrants(ctx, tx, id, grants); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "space", id, p.UserID()); err != nil {
			return err
		}
		// The grants of the new space reach the caches of every node, so that its creator (and with an open space
		// everyone) finds it whichever node answers.
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "space.create", TargetType: "space", TargetID: key,
			Details: map[string]any{"name": in.Name, "open": open}})
	})
	if err != nil {
		return nil, err
	}
	s.authz.Invalidate()
	return s.GetSpace(ctx, p, key)
}

// CreatePersonalSpace creates the principal's personal space (key ~USERNAME).
func (s *Service) CreatePersonalSpace(ctx context.Context, p *identity.Principal) (*Space, error) {
	if _, err := s.authz.RequireGlobal(ctx, authz.SpaceCreatePersonal); err != nil {
		return nil, err
	}
	key := strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, p.User.Username))
	if key == "" {
		key = "U" + itoa(p.UserID())
	}
	key = "~" + key
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM spaces WHERE owner_id = $1 AND type = 'personal')", p.UserID()).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, apperr.Conflict("space.personal-exists", "You already have a personal space.")
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := s.createSpace(ctx, tx, p.UserID(), key, p.User.DisplayName, "", "personal", []string{})
		if err != nil {
			return err
		}
		grants := []grant{}
		for _, perm := range SpacePermissions {
			grants = append(grants, grant{perm, "user", p.UserID()})
		}
		grants = append(grants, grant{PermView, "logged-in", 0}, grant{PermComment, "logged-in", 0})
		if err := insertGrants(ctx, tx, id, grants); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "space", id, p.UserID()); err != nil {
			return err
		}
		return live.InvalidateCache(ctx, tx, "authz")
	})
	if err != nil {
		return nil, err
	}
	s.authz.Invalidate()
	return s.GetSpace(ctx, p, key)
}

func (s *Service) createSpace(ctx context.Context, tx pgx.Tx, uid int64, key, name, description, typ string, categories []string) (int64, error) {
	var owner any
	if typ == "personal" {
		owner = uid
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO spaces (key, name, description, type, owner_id, categories, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, key, name, description, typ, owner, categories, uid).Scan(&id)
	if db.IsUniqueViolation(err, "spaces_key_key") {
		return 0, apperr.Conflict("space.key-taken", "The key %s is already in use.", key)
	}
	if err != nil {
		return 0, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	home := defaultTexts.get("space.home", st.DefaultLocale)
	body := docmodel.Paragraphs(defaultTexts.get("space.welcome", st.DefaultLocale))
	pid, err := s.insertPage(ctx, tx, id, 0, "page", "current", name+" – "+home, body, uid, "")
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO page_versions (page_id, version, title, body, author_id) SELECT id, 1, title, body, creator_id FROM pages WHERE id = $1", pid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "UPDATE pages SET version = 1, published_at = now() WHERE id = $1", pid); err != nil {
		return 0, err
	}
	if err := s.refreshSearch(ctx, tx, pid); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, "UPDATE spaces SET home_page_id = $2 WHERE id = $1", id, pid)
	return id, err
}

type grant struct {
	perm, holderType string
	holderID         int64
}

func insertGrants(ctx context.Context, tx pgx.Tx, spaceID int64, gs []grant) error {
	for _, g := range gs {
		if _, err := tx.Exec(ctx, `INSERT INTO space_permissions (space_id, permission, holder_type, holder_id) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, spaceID, g.perm, g.holderType, g.holderID); err != nil {
			return err
		}
	}
	return nil
}

// SpaceUpdate holds optional changes of a space.
type SpaceUpdate struct {
	Name        *string  `json:"name,omitempty" minLength:"1" maxLength:"200"`
	Description *string  `json:"description,omitempty" maxLength:"2000"`
	Categories  []string `json:"categories,omitempty" maxItems:"20"`
	HomePageID  *int64   `json:"homePageId,omitempty"`
	Archived    *bool    `json:"archived,omitempty"`
}

// UpdateSpace changes a space (space administrators).
func (s *Service) UpdateSpace(ctx context.Context, p *identity.Principal, key string, in SpaceUpdate) (*Space, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return nil, err
	}
	if !a.perms[sp.ID][PermAdmin] {
		return nil, apperr.Forbidden("space.admin-required", "Only administrators of the space can change it.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args db.Args
		sets := []string{"updated_at = now()", "version = version + 1"}
		if in.Name != nil {
			sets = append(sets, "name = "+args.Add(strings.TrimSpace(*in.Name)))
		}
		if in.Description != nil {
			sets = append(sets, "description = "+args.Add(*in.Description))
		}
		if in.Categories != nil {
			sets = append(sets, "categories = "+args.Add(normaliseCategories(in.Categories)))
		}
		if in.HomePageID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pages WHERE id = $1 AND space_id = $2 AND kind = 'page' AND status = 'current')",
				*in.HomePageID, sp.ID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return apperr.Invalid("space.home", "The home page must be a current page of the space.")
			}
			sets = append(sets, "home_page_id = "+args.Add(*in.HomePageID))
		}
		if in.Archived != nil {
			sets = append(sets, "archived = "+args.Add(*in.Archived))
		}
		if _, err := tx.Exec(ctx, "UPDATE spaces SET "+strings.Join(sets, ", ")+" WHERE id = "+args.Add(sp.ID), args...); err != nil {
			return err
		}
		if in.Archived != nil {
			if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "space.update", TargetType: "space", TargetID: sp.Key}); err != nil {
			return err
		}
		return s.publish(ctx, tx, SpaceEvent{Name: "space.updated", SpaceID: sp.ID})
	})
	if err != nil {
		return nil, err
	}
	if in.Archived != nil {
		s.authz.Invalidate()
	}
	return s.GetSpace(ctx, p, key)
}

// DeleteSpace removes a space with all its content (space administrators who are also global administrators).
func (s *Service) DeleteSpace(ctx context.Context, p *identity.Principal, key string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return err
	}
	if !a.admin && !(sp.Type == "personal" && sp.Owner != nil && sp.Owner.ID == p.UserID()) {
		return apperr.Forbidden("space.delete-denied", "Only administrators can delete spaces.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM blob_refs WHERE owner_type = 'page-attachment' AND owner_id IN
			(SELECT a.id FROM page_attachments a JOIN pages pg ON pg.id = a.page_id WHERE pg.space_id = $1)`, sp.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE spaces SET home_page_id = NULL WHERE id = $1", sp.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM pages WHERE space_id = $1", sp.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM spaces WHERE id = $1", sp.ID); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "space.delete", TargetType: "space", TargetID: sp.Key,
			Details: map[string]any{"name": sp.Name}})
	})
	if err == nil {
		s.authz.Invalidate()
	}
	return err
}

// SpaceGrant is a permission granted in a space.
type SpaceGrant struct {
	Permission string `json:"permission"`
	Holder     Holder `json:"holder"`
}

// SpacePermissionsOf lists the grants of a space (space and global administrators).
func (s *Service) SpacePermissionsOf(ctx context.Context, p *identity.Principal, key string) ([]SpaceGrant, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return nil, err
	}
	if !a.perms[sp.ID][PermAdmin] && !a.admin {
		return nil, apperr.Forbidden("space.admin-required", "Only administrators of the space can see its permissions.")
	}
	rows, err := s.pool.Query(ctx, `SELECT g.permission, g.holder_type, g.holder_id, coalesce(u.display_name, gr.name, '')
		FROM space_permissions g LEFT JOIN users u ON g.holder_type = 'user' AND u.id = g.holder_id
		LEFT JOIN groups gr ON g.holder_type = 'group' AND gr.id = g.holder_id
		WHERE g.space_id = $1 ORDER BY g.holder_type, g.holder_id, g.permission`, sp.ID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SpaceGrant, error) {
		var g SpaceGrant
		err := row.Scan(&g.Permission, &g.Holder.Type, &g.Holder.ID, &g.Holder.Name)
		return g, err
	})
	return nonNil(out), err
}

// SetSpacePermissions replaces the grants of a space. Space administrators keep at least one administrator.
func (s *Service) SetSpacePermissions(ctx context.Context, p *identity.Principal, key string, grants []SpaceGrant) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return err
	}
	if !a.perms[sp.ID][PermAdmin] && !a.admin {
		return apperr.Forbidden("space.admin-required", "Only administrators of the space can change its permissions.")
	}
	hasAdmin := false
	for _, g := range grants {
		if !slices.Contains(SpacePermissions, g.Permission) {
			return apperr.Invalidf("space.permission", "Unknown permission %s.", g.Permission)
		}
		switch g.Holder.Type {
		case "user", "group":
			if g.Holder.ID <= 0 {
				return apperr.Invalid("space.holder", "Users and groups need an id.")
			}
		case "logged-in", "anyone":
			if g.Permission == PermAdmin {
				return apperr.Invalid("space.holder", "Space administration can only be granted to users and groups.")
			}
		default:
			return apperr.Invalidf("space.holder", "Unknown holder type %s.", g.Holder.Type)
		}
		hasAdmin = hasAdmin || g.Permission == PermAdmin
	}
	if !hasAdmin {
		return apperr.Invalid("space.no-admin", "At least one user or group must administer the space.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM space_permissions WHERE space_id = $1", sp.ID); err != nil {
			return err
		}
		gs := make([]grant, 0, len(grants))
		for _, g := range grants {
			id := g.Holder.ID
			if g.Holder.Type == "logged-in" || g.Holder.Type == "anyone" {
				id = 0
			}
			gs = append(gs, grant{g.Permission, g.Holder.Type, id})
		}
		if err := insertGrants(ctx, tx, sp.ID, gs); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "space.permissions", TargetType: "space", TargetID: sp.Key,
			Details: map[string]any{"grants": len(grants)}})
	})
	if err == nil {
		s.authz.Invalidate()
	}
	return err
}

// SetSpaceFavourite marks or unmarks a space as favourite.
func (s *Service) SetSpaceFavourite(ctx context.Context, p *identity.Principal, key string, on bool) error {
	sp, err := s.GetSpace(ctx, p, key)
	if err != nil {
		return err
	}
	if on {
		_, err = s.pool.Exec(ctx, "INSERT INTO space_favourites (space_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", sp.ID, p.UserID())
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM space_favourites WHERE space_id = $1 AND user_id = $2", sp.ID, p.UserID())
	}
	return err
}

// WatchSpace starts or stops watching a whole space.
func (s *Service) WatchSpace(ctx context.Context, p *identity.Principal, key string, on bool) error {
	sp, err := s.GetSpace(ctx, p, key)
	if err != nil {
		return err
	}
	if on {
		return notify.Watch(ctx, s.pool, "space", sp.ID, p.UserID())
	}
	return notify.Unwatch(ctx, s.pool, "space", sp.ID, p.UserID())
}

// PageVisibility is the live-update visibility check for pages ("page:<id>"): view restrictions, drafts and the
// trash apply as when reading the page.
func (s *Service) PageVisibility(ctx context.Context, userID int64, groups []int64, container string) bool {
	id, ok := strings.CutPrefix(container, "page:")
	if !ok {
		return false
	}
	pageID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false
	}
	a, err := s.Access(ctx, &identity.Principal{User: &identity.User{ID: userID}, Groups: groups, Method: identity.MethodInternal})
	if err != nil {
		return false
	}
	_, err = s.readablePage(ctx, s.pool, a, pageID, false)
	return err == nil
}

// SpaceVisibility is the live-update visibility check for space containers.
func (s *Service) SpaceVisibility(ctx context.Context, userID int64, groups []int64, container string) bool {
	id, ok := strings.CutPrefix(container, "space:")
	if !ok {
		return false
	}
	a, err := s.Access(ctx, &identity.Principal{User: &identity.User{ID: userID}, Groups: groups, Method: identity.MethodInternal})
	if err != nil {
		return false
	}
	for _, sid := range a.Spaces(PermView) {
		if itoa(sid) == id {
			return true
		}
	}
	return false
}
