// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"strings"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/feed"
)

// Activity streams: what happened recently in what the principal may see — issues, pages, pushes
// and pull requests, runs and deployments — on the start page and per project, space, repository and person. Each
// product reads its own sources with its permissions in the SQL (so a stream pages only through what the principal
// may see); this merges them, newest first.

// ActivityFilter selects a stream. Project, Space and Repository name one container (and with it the products);
// Product keeps one product.
type ActivityFilter struct {
	Product    string
	Project    string // key of an issue project
	Space      string // key of a space
	Repository string // KEY/slug
	User       string // what this person did
	Mine       bool   // what the principal watches or is involved in
	Before     string // the key of the last entry read
	Limit      int
}

// ActivityPage is a part of a stream.
type ActivityPage struct {
	Entries []feed.Entry `json:"entries"`
	Next    string       `json:"next,omitempty" doc:"Pass as before to read the entries that follow; empty at the end of the stream"`
}

// activitySource is a product's part of a stream.
type activitySource func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error)

// Activity returns entries of a stream, newest first.
func (s *Service) Activity(ctx context.Context, p *identity.Principal, f ActivityFilter) (*ActivityPage, error) {
	q := feed.Query{Limit: f.Limit, Mine: f.Mine}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
	if f.Before != "" {
		c, err := feed.ParseCursor(f.Before)
		if err != nil {
			return nil, apperr.Invalid("activity.before", "Read on from the key of an entry of the stream.")
		}
		q.Before = &c
	}
	containers := 0
	for _, c := range []string{f.Project, f.Space, f.Repository} {
		if c != "" {
			containers++
		}
	}
	if containers > 1 {
		return nil, apperr.Invalid("activity.container", "Ask for the activity of one project, space or repository at a time.")
	}
	if f.User != "" {
		if _, err := s.authz.RequireGlobal(ctx, authz.UserBrowse); err != nil {
			return nil, err
		}
		u, err := identity.GetUserByUsername(ctx, s.pool, f.User)
		if err != nil {
			return nil, err
		}
		q.ActorID = u.ID
	}
	sources := map[string]activitySource{}
	switch {
	case f.Project != "":
		proj, err := s.issues.GetProject(ctx, p, f.Project)
		if err != nil {
			return nil, err
		}
		sources[ProductKontor] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.issues.ActivityFeed(ctx, p, q, proj.ID)
		}
	case f.Space != "":
		sp, err := s.wiki.GetSpace(ctx, p, f.Space)
		if err != nil {
			return nil, err
		}
		sources[ProductLogbuch] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.wiki.ActivityFeed(ctx, p, q, sp.ID)
		}
	case f.Repository != "":
		key, slug, _ := strings.Cut(f.Repository, "/")
		repo, err := s.code.GetRepository(ctx, p, key, slug)
		if err != nil {
			return nil, err
		}
		sources[ProductHafen] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.code.ActivityFeed(ctx, p, q, repo.ID)
		}
		if s.builds != nil {
			sources[ProductHelling] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
				return s.builds.ActivityFeed(ctx, p, q, repo.ID)
			}
		}
	default:
		sources[ProductKontor] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.issues.ActivityFeed(ctx, p, q, 0)
		}
		sources[ProductLogbuch] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.wiki.ActivityFeed(ctx, p, q, 0)
		}
		sources[ProductHafen] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
			return s.code.ActivityFeed(ctx, p, q, 0)
		}
		if s.builds != nil {
			sources[ProductHelling] = func(ctx context.Context, p *identity.Principal, q feed.Query) ([]feed.Entry, error) {
				return s.builds.ActivityFeed(ctx, p, q, 0)
			}
		}
	}
	if f.Product != "" {
		for pr := range sources {
			if pr != f.Product {
				delete(sources, pr)
			}
		}
	}
	// The products are read at once as far as the node's search slots are free, the others one after another
	// (runInSlots): the stream needs all of them.
	type read struct {
		at  int
		src activitySource
	}
	var reads []read
	for i, pr := range productOrder {
		if src, ok := sources[pr]; ok {
			reads = append(reads, read{i, src})
		}
	}
	lists := make([][]feed.Entry, len(productOrder))
	errs := make([]error, len(productOrder))
	s.runInSlots(len(reads), func(k int) {
		r := reads[k]
		lists[r.at], errs[r.at] = r.src(ctx, p, q)
	})
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	entries, more := feed.Merge(lists, q.Limit)
	out := &ActivityPage{Entries: entries}
	if more {
		out.Next = entries[len(entries)-1].Key
	}
	return out, nil
}
