// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Information for integrations such as webhooks; these functions do not check permissions.

// PageInfo describes a page or blog post.
type PageInfo struct {
	ID        int64             `json:"id"`
	Title     string            `json:"title"`
	Kind      string            `json:"kind" enum:"page,blog"`
	Status    string            `json:"status"`
	Version   int               `json:"version"`
	SpaceKey  string            `json:"spaceKey"`
	SpaceName string            `json:"spaceName"`
	UpdatedBy *identity.UserRef `json:"updatedBy,omitempty"`
	Updated   time.Time         `json:"updated"`
	Path      string            `json:"-"`
}

// PageInfo returns a page by id.
func (s *Service) PageInfo(ctx context.Context, id int64) (*PageInfo, error) {
	var pg PageInfo
	var by int64
	err := s.pool.QueryRow(ctx, `SELECT p.id, p.title, p.kind, p.status, p.version, sp.key, sp.name, coalesce(p.updated_by, 0), p.updated_at
		FROM pages p JOIN spaces sp ON sp.id = p.space_id WHERE p.id = $1`, id).
		Scan(&pg.ID, &pg.Title, &pg.Kind, &pg.Status, &pg.Version, &pg.SpaceKey, &pg.SpaceName, &by, &pg.Updated)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("page.not-found", "The page does not exist.")
	}
	if err != nil {
		return nil, err
	}
	users, err := identity.GetUserRefs(ctx, s.pool, []int64{by})
	if err != nil {
		return nil, err
	}
	if u, ok := users[by]; ok {
		pg.UpdatedBy = &u
	}
	pg.Path = "/wiki/spaces/" + pg.SpaceKey + "/pages/" + strconv.FormatInt(pg.ID, 10)
	return &pg, nil
}

// UserCanReadPage reports whether an active user can read a page (space permissions and page restrictions), for
// work done on their behalf such as webhooks.
func (s *Service) UserCanReadPage(ctx context.Context, userID, pageID int64) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	p, err := identity.LoadPrincipal(ctx, s.pool, userID, identity.MethodInternal)
	if err != nil {
		if apperr.Is(err, "auth.user-inactive") || db.IsNoRows(err) || apperr.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	pg, err := loadPage(ctx, s.pool, pageID, false)
	if err != nil {
		if db.IsNoRows(err) || apperr.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return false, err
	}
	return s.canView(ctx, s.pool, a, pg)
}

// AdminSpaceID returns the id of a space the principal administers.
func (s *Service) AdminSpaceID(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return 0, err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return 0, err
	}
	if !a.perms[sp.ID][PermAdmin] {
		return 0, apperr.Forbidden("space.admin-required", "Only administrators of the space can do this.")
	}
	return sp.ID, nil
}

// AdminTeamSpaceID is AdminSpaceID for team spaces only. Everybody can have a personal space, so features that act
// on other systems (webhooks) are not available there.
func (s *Service) AdminTeamSpaceID(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	id, err := s.AdminSpaceID(ctx, p, key)
	if err != nil {
		return 0, err
	}
	var typ string
	if err := s.pool.QueryRow(ctx, "SELECT type FROM spaces WHERE id = $1", id).Scan(&typ); err != nil {
		return 0, err
	}
	if typ == "personal" {
		return 0, apperr.Forbidden("space.personal", "Personal spaces have no webhooks.")
	}
	return id, nil
}

// SpaceKeyOf returns the key of a space.
func (s *Service) SpaceKeyOf(ctx context.Context, id int64) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, "SELECT key FROM spaces WHERE id = $1", id).Scan(&key)
	return key, err
}

// LinkIssueInput names the text that became an issue.
type LinkIssueInput struct {
	Text       string `json:"text" minLength:"1" maxLength:"2000" doc:"The selected text"`
	Occurrence int    `json:"occurrence,omitempty" minimum:"0" doc:"Which occurrence of the text in the page (0 = first)"`
	Key        string `json:"key" minLength:"3" maxLength:"30" doc:"The issue created from it"`
}

// LinkIssue replaces text of the current version of a page with an issue node and publishes that as a new version
// (editors of the page).
func (s *Service) LinkIssue(ctx context.Context, p *identity.Principal, id int64, in LinkIssueInput) (*Page, error) {
	pg, err := s.GetPage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if pg.Body == nil || !docmodel.ReplaceText(pg.Body, in.Text, in.Occurrence,
		&docmodel.Node{Type: "issue", Attrs: map[string]any{"key": strings.ToUpper(strings.TrimSpace(in.Key))}}) {
		return nil, apperr.Conflict("page.text-gone", "The text is no longer on the page.")
	}
	return s.Publish(ctx, p, id, PublishInput{Title: pg.Title, Body: pg.Body, BaseVersion: pg.Version, Message: "Linked " + in.Key, Minor: true})
}

// VisiblePageIDs reports which current or archived pages a principal may read (for re-checking notifications).
func (s *Service) VisiblePageIDs(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	where := "p.id = ANY(" + args.Add(parseIDs(ids)) + ") AND p.status IN ('current', 'archived') AND " + a.VisibleSQL("p", &args)
	rows, err := s.pool.Query(ctx, "SELECT p.id FROM pages p WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, id := range found {
		out[strconv.FormatInt(id, 10)] = true
	}
	return out, nil
}

// parseIDs parses decimal ids, leaving out what is none.
func parseIDs(ids []string) []int64 {
	out := []int64{}
	for _, s := range ids {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}
