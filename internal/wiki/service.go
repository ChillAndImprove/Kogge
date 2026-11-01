// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package wiki implements spaces, pages and blog posts with drafts, versions, restrictions, comments,
// attachments, labels and templates.
package wiki

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/settings"
)

// Service implements the wiki module.
type Service struct {
	pool     *pgxpool.Pool
	authz    *authz.Service
	settings *settings.Service
	bus      *events.Bus
	blobs    *blob.Store
	log      *slog.Logger

	access accessCache
	drafts draftDocs // the documents of shared drafts in use on this node
	hub    *draftHub
	public *url.URL    // the instance's address, where links of exports lead
	macros MacroSource // issues, code and builds for the output of macros in exports

	// SocketRecheck is how often the WebSocket of an editor checks again that the editor is still signed in and
	// may still edit (a minute; tests shorten it before connecting).
	SocketRecheck time.Duration
	issueKeys     IssueKeys
}

// IssueKeys returns all keys of the issue with a key, as far as the principal sees the issue: pages name issues by
// the key they had when the page was written, also after the issue moved to another project.
type IssueKeys func(ctx context.Context, p *identity.Principal, key string) ([]string, error)

// SetIssueKeys connects the issues module (the former keys of issues).
func (s *Service) SetIssueKeys(f IssueKeys) { s.issueKeys = f }

// NewService creates the service.
func NewService(pool *pgxpool.Pool, az *authz.Service, st *settings.Service, bus *events.Bus, blobs *blob.Store,
	logger *slog.Logger) *Service {
	s := &Service{pool: pool, authz: az, settings: st, bus: bus, blobs: blobs, log: logger, hub: newDraftHub(logger),
		SocketRecheck: socketRecheck}
	s.hub.fetch = func(pageID int64, after, upTo int) ([]DraftStep, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.stepsBetween(ctx, s.pool, pageID, after, upTo)
	}
	return s
}

// now is the clock (tests may replace it).
var now = time.Now

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// maxPageLimit is the largest page of a list the API hands out (the maximum of api.Page).
const maxPageLimit = 500

// pageLimit keeps the limit of a page of a list between 1 and maxPageLimit. The API checks the limits it
// receives; this guards callers in the code.
func pageLimit(limit int) int { return max(1, min(limit, maxPageLimit)) }

// PageEvent announces changes of pages to live clients and other modules.
type PageEvent struct {
	Name    string
	PageID  int64
	SpaceID int64
	ActorID int64
}

// EventName implements events.Event.
func (e PageEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live. Pages are their own containers, so that only who may read a page (view
// restrictions included) hears of its changes.
func (e PageEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "page", ID: itoa(e.PageID), Container: "page:" + itoa(e.PageID)}
}

// SpaceEvent announces changes of spaces.
type SpaceEvent struct {
	Name    string
	SpaceID int64
}

// EventName implements events.Event.
func (e SpaceEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live.
func (e SpaceEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "space", ID: itoa(e.SpaceID), Container: "space:" + itoa(e.SpaceID)}
}

func (s *Service) publish(ctx context.Context, tx pgx.Tx, e events.Event) error {
	return s.bus.Publish(ctx, tx, e)
}
