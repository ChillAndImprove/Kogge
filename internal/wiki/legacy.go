// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Addresses for redirects of links into a migrated wiki. They only resolve what the principal may
// see, so that redirects do not reveal restricted pages.

// PageAddress returns the address of a page.
func (s *Service) PageAddress(ctx context.Context, p *identity.Principal, id int64) (string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", err
	}
	pg, err := s.readablePage(ctx, s.pool, a, id, false)
	if err != nil {
		return "", err
	}
	if pg.Status != StatusCurrent && pg.Status != StatusArchived {
		return "", notFoundPage()
	}
	sp, err := spaceByID(ctx, s.pool, pg.SpaceID)
	if err != nil {
		return "", err
	}
	return pageURL(sp.Key, pg.ID, pg.Title), nil
}

// PageAddressByTitle returns the address of the page (or newest blog post) with a title in a space.
func (s *Service) PageAddressByTitle(ctx context.Context, p *identity.Principal, spaceKey, title string, blog bool) (string, error) {
	kind := "page"
	if blog {
		kind = "blog"
	}
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT p.id FROM pages p JOIN spaces s ON s.id = p.space_id
		WHERE upper(s.key) = upper($1) AND lower(p.title) = lower($2) AND p.kind = $3 AND p.status IN ('current', 'archived')
		ORDER BY p.published_at DESC NULLS LAST LIMIT 1`, spaceKey, title, kind).Scan(&id)
	if db.IsNoRows(err) {
		return "", notFoundPage()
	}
	if err != nil {
		return "", err
	}
	return s.PageAddress(ctx, p, id)
}

// SpaceAddress returns the address of a space.
func (s *Service) SpaceAddress(ctx context.Context, p *identity.Principal, key string) (string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", err
	}
	sp, err := s.visibleSpace(ctx, a, key)
	if err != nil {
		return "", err
	}
	return "/wiki/spaces/" + sp.Key, nil
}

// AttachmentAddress returns the download address of the newest version of a page's attachment.
func (s *Service) AttachmentAddress(ctx context.Context, p *identity.Principal, pageID int64, filename string) (string, error) {
	if _, err := s.PageAddress(ctx, p, pageID); err != nil {
		return "", err
	}
	var id int64
	err := s.pool.QueryRow(ctx, "SELECT id FROM page_attachments WHERE page_id = $1 AND filename = $2 ORDER BY version DESC LIMIT 1",
		pageID, filename).Scan(&id)
	if db.IsNoRows(err) {
		return "", notFoundPage()
	}
	if err != nil {
		return "", err
	}
	return attachmentURL(id), nil
}
