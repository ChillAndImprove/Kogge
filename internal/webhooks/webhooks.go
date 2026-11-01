// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package webhooks delivers events of all modules to other systems: webhooks of the instance, an
// issue project, a space, a code project or a repository subscribe to event types and receive JSON POST requests
// signed with HMAC-SHA256, retried with backoff and logged. Like package links, it uses the modules' services.
package webhooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/wiki"
)

// Scopes of webhooks.
const (
	ScopeInstance    = "instance"
	ScopeProject     = "project" // issue project
	ScopeSpace       = "space"
	ScopeCodeProject = "code-project"
	ScopeRepository  = "repository"
)

// EventType is an event webhooks can subscribe to.
type EventType struct {
	Name   string `json:"name"`
	Module string `json:"module" enum:"issues,wiki,code,builds"`
}

// Catalogue lists the events webhooks receive.
var Catalogue = []EventType{
	{"issue.created", "issues"}, {"issue.updated", "issues"}, {"issue.transitioned", "issues"}, {"issue.commented", "issues"},
	{"issue.worklog", "issues"}, {"issue.linked", "issues"}, {"issue.deleted", "issues"}, {"issue.moved", "issues"},
	{"page.created", "wiki"}, {"page.updated", "wiki"}, {"blog.created", "wiki"}, {"page.commented", "wiki"}, {"page.moved", "wiki"},
	{"page.trashed", "wiki"}, {"page.restored", "wiki"},
	{"repository.pushed", "code"}, {"pull-request.opened", "code"}, {"pull-request.updated", "code"}, {"pull-request.reviewed", "code"},
	{"pull-request.commented", "code"}, {"pull-request.merged", "code"}, {"pull-request.declined", "code"},
	{"pull-request.reopened", "code"},
	{"run.created", "builds"}, {"run.succeeded", "builds"}, {"run.failed", "builds"}, {"run.cancelled", "builds"},
	{"deployment.succeeded", "builds"}, {"deployment.failed", "builds"},
}

// modulesOf returns the modules whose events a scope receives.
func modulesOf(scope string) []string {
	switch scope {
	case ScopeProject:
		return []string{"issues"}
	case ScopeSpace:
		return []string{"wiki"}
	case ScopeCodeProject, ScopeRepository:
		return []string{"code", "builds"}
	}
	return []string{"issues", "wiki", "code", "builds"}
}

func eventModule(name string) string {
	for _, e := range Catalogue {
		if e.Name == name {
			return e.Module
		}
	}
	return ""
}

// Options configure the service.
type Options struct {
	PublicURL *url.URL
	Keyring   *crypto.Keyring
	Version   string
	// AllowLoopback lets webhooks call loopback addresses (development and tests).
	AllowLoopback bool
}

// Service manages webhooks and delivers events.
type Service struct {
	pool     *pgxpool.Pool
	authz    *authz.Service
	settings *settings.Service
	bus      *events.Bus
	issues   *issues.Service
	wiki     *wiki.Service
	code     *code.Service
	builds   *builds.Service
	opt      Options
	log      *slog.Logger
}

// NewService creates the service.
func NewService(pool *pgxpool.Pool, az *authz.Service, st *settings.Service, bus *events.Bus, is *issues.Service, ws *wiki.Service,
	cs *code.Service, bs *builds.Service, opt Options, logger *slog.Logger) *Service {
	return &Service{pool: pool, authz: az, settings: st, bus: bus, issues: is, wiki: ws, code: cs, builds: bs, opt: opt, log: logger}
}

// Webhook is a configured webhook.
type Webhook struct {
	ID       int64     `json:"id"`
	Scope    string    `json:"scope" enum:"instance,project,space,code-project,repository"`
	ScopeKey string    `json:"scopeKey,omitempty" doc:"Key of the project, space or code project; KEY/slug of the repository"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Events   []string  `json:"events" doc:"Empty: all events of the scope"`
	Active   bool      `json:"active"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Last     *Delivery `json:"lastDelivery,omitempty"`
	// Owner is the person who created the webhook or last changed its address: it sends only what they may see.
	Owner *identity.UserRef `json:"owner,omitempty" doc:"The webhook sends only what this person may see"`
	// Secret is returned once, when it was generated.
	Secret string `json:"secret,omitempty" doc:"The signing secret; only in the answer that generated it"`
}

// WebhookInput creates or changes a webhook.
type WebhookInput struct {
	Name   string   `json:"name" minLength:"1" maxLength:"100"`
	URL    string   `json:"url" minLength:"1" maxLength:"2000"`
	Events []string `json:"events" maxItems:"100"`
	Active bool     `json:"active"`
	Secret string   `json:"secret,omitempty" maxLength:"200" doc:"Signing secret; empty: generated (new webhooks) or unchanged"`
}

// scopeOf resolves a scope reference and checks that the principal administers it.
func (s *Service) scopeOf(ctx context.Context, p *identity.Principal, scope, key string) (int64, error) {
	switch scope {
	case ScopeInstance:
		// The principal is not always the request's: deliveries check the webhook's owner.
		ok, err := s.authz.IsAdmin(ctx, p)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, apperr.Forbidden("permission.denied", "Only administrators manage the webhooks of the instance.")
		}
		return 0, nil
	case ScopeProject:
		return s.issues.AdminProjectID(ctx, p, key)
	case ScopeSpace:
		return s.wiki.AdminTeamSpaceID(ctx, p, key)
	case ScopeCodeProject:
		return s.code.AdminProjectID(ctx, p, key)
	case ScopeRepository:
		k, slug, ok := strings.Cut(key, "/")
		if !ok {
			return 0, apperr.Invalid("webhook.scope", "A repository is KEY/slug.")
		}
		r, err := s.code.AdminRepo(ctx, p, k, slug)
		if err != nil {
			return 0, err
		}
		return r.ID, nil
	}
	return 0, apperr.Invalidf("webhook.scope", "Unknown scope %s.", scope)
}

// scopeKey returns the key of a scope (for display and permission checks).
func (s *Service) scopeKey(ctx context.Context, scope string, id int64) (string, error) {
	switch scope {
	case ScopeProject:
		return s.issues.ProjectKeyOf(ctx, id)
	case ScopeSpace:
		return s.wiki.SpaceKeyOf(ctx, id)
	case ScopeCodeProject:
		return s.code.ProjectKeyOf(ctx, id)
	case ScopeRepository:
		r, err := s.code.RepoBasicsByID(ctx, id)
		if err != nil {
			return "", err
		}
		return r.ProjectKey + "/" + r.Slug, nil
	}
	return "", nil
}

const hookColumns = "id, scope, scope_id, name, url, events, active, created_at, updated_at, coalesce(owner_id, 0)"

type hookRow struct {
	Webhook
	scopeID int64
	ownerID int64
}

func scanHook(row pgx.Row) (*hookRow, error) {
	var h hookRow
	err := row.Scan(&h.ID, &h.Scope, &h.scopeID, &h.Name, &h.URL, &h.Events, &h.Active, &h.Created, &h.Updated, &h.ownerID)
	if h.Events == nil {
		h.Events = []string{}
	}
	return &h, err
}

// hookForAdmin loads a webhook its scope's administrator may change.
func (s *Service) hookForAdmin(ctx context.Context, p *identity.Principal, id int64) (*hookRow, error) {
	h, err := scanHook(s.pool.QueryRow(ctx, "SELECT "+hookColumns+" FROM webhooks WHERE id = $1", id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("webhook.not-found", "The webhook does not exist.")
	}
	if err != nil {
		return nil, err
	}
	key, err := s.scopeKey(ctx, h.Scope, h.scopeID)
	if err != nil {
		return nil, apperr.NotFound("webhook.not-found", "The webhook does not exist.")
	}
	if _, err := s.scopeOf(ctx, p, h.Scope, key); err != nil {
		return nil, apperr.NotFound("webhook.not-found", "The webhook does not exist.")
	}
	h.ScopeKey = key
	return h, nil
}

// validate checks a webhook's input for its scope.
func validate(scope string, in *WebhookInput) error {
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return apperr.Invalid("webhook.url", "The address must be an http(s) URL without credentials.")
	}
	in.URL = u.String()
	allowed := modulesOf(scope)
	for _, e := range in.Events {
		if m := eventModule(e); m == "" || !slices.Contains(allowed, m) {
			return apperr.Invalidf("webhook.event", "The event %s is not available here.", e)
		}
	}
	slices.Sort(in.Events)
	in.Events = slices.Compact(in.Events)
	return nil
}

func newSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sealAAD(id int64) string { return fmt.Sprintf("webhook-secret:%d", id) }

// List returns the webhooks of a scope with their latest delivery.
func (s *Service) List(ctx context.Context, p *identity.Principal, scope, key string) ([]Webhook, error) {
	scopeID, err := s.scopeOf(ctx, p, scope, key)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+hookColumns+" FROM webhooks WHERE scope = $1 AND scope_id = $2 ORDER BY lower(name), id", scope, scopeID)
	if err != nil {
		return nil, err
	}
	hooks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*hookRow, error) { return scanHook(r) })
	if err != nil {
		return nil, err
	}
	out := []Webhook{}
	for _, h := range hooks {
		h.ScopeKey = key
		list, err := s.deliveries(ctx, h.ID, 1)
		if err != nil {
			return nil, err
		}
		if len(list) > 0 {
			h.Last = &list[0]
			h.Last.Payload = nil
		}
		h.Owner = userRef(ctx, s, h.ownerID)
		out = append(out, h.Webhook)
	}
	return out, nil
}

// Create adds a webhook; a generated secret is returned once.
func (s *Service) Create(ctx context.Context, p *identity.Principal, scope, key string, in WebhookInput) (*Webhook, error) {
	scopeID, err := s.scopeOf(ctx, p, scope, key)
	if err != nil {
		return nil, err
	}
	if err := validate(scope, &in); err != nil {
		return nil, err
	}
	secret, generated := in.Secret, false
	if secret == "" {
		secret, generated = newSecret(), true
	}
	var h *hookRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		h, err = scanHook(tx.QueryRow(ctx, `INSERT INTO webhooks (scope, scope_id, name, url, secret_sealed, events, active, created_by, owner_id)
			VALUES ($1, $2, $3, $4, '', $5, $6, $7, $7) RETURNING `+hookColumns, scope, scopeID, strings.TrimSpace(in.Name), in.URL,
			in.Events, in.Active, p.UserID()))
		if err != nil {
			return err
		}
		sealed, err := s.opt.Keyring.Seal([]byte(secret), sealAAD(h.ID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE webhooks SET secret_sealed = $2 WHERE id = $1", h.ID, sealed); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "webhook.create", TargetType: "webhook", TargetID: itoa(h.ID),
			Details: map[string]any{"scope": scope, "key": key, "url": in.URL}})
	})
	if err != nil {
		return nil, err
	}
	h.ScopeKey = key
	h.Owner = userRef(ctx, s, h.ownerID)
	if generated {
		h.Secret = secret
	}
	return &h.Webhook, nil
}

// Update changes a webhook; an empty secret keeps the stored one. Whoever changes the address becomes the owner:
// the webhook then sends only what they may see, so that nobody can redirect what others may see to themselves.
func (s *Service) Update(ctx context.Context, p *identity.Principal, id int64, in WebhookInput) (*Webhook, error) {
	h, err := s.hookForAdmin(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err := validate(h.Scope, &in); err != nil {
		return nil, err
	}
	retargeted := in.URL != h.URL
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE webhooks SET name = $2, url = $3, events = $4, active = $5, updated_at = now(),
			owner_id = CASE WHEN $6 THEN $7 ELSE owner_id END WHERE id = $1`,
			id, strings.TrimSpace(in.Name), in.URL, in.Events, in.Active, retargeted, p.UserID()); err != nil {
			return err
		}
		if retargeted {
			// Deliveries waiting for a retry were made for the old address and owner.
			if _, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET state = 'failed', error = 'The address of the webhook changed.'
				WHERE webhook_id = $1 AND state = 'pending'`, id); err != nil {
				return err
			}
		}
		if in.Secret != "" {
			sealed, err := s.opt.Keyring.Seal([]byte(in.Secret), sealAAD(id))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE webhooks SET secret_sealed = $2 WHERE id = $1", id, sealed); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "webhook.update", TargetType: "webhook", TargetID: itoa(id),
			Details: map[string]any{"url": in.URL, "previousUrl": h.URL, "active": in.Active, "secretChanged": in.Secret != "",
				"ownerChanged": retargeted}})
	})
	if err != nil {
		return nil, err
	}
	out, err := scanHook(s.pool.QueryRow(ctx, "SELECT "+hookColumns+" FROM webhooks WHERE id = $1", id))
	if err != nil {
		return nil, err
	}
	out.ScopeKey = h.ScopeKey
	out.Owner = userRef(ctx, s, out.ownerID)
	return &out.Webhook, nil
}

// Delete removes a webhook with its deliveries.
func (s *Service) Delete(ctx context.Context, p *identity.Principal, id int64) error {
	h, err := s.hookForAdmin(ctx, p, id)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM webhooks WHERE id = $1", id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "webhook.delete", TargetType: "webhook", TargetID: itoa(id),
			Details: map[string]any{"scope": h.Scope, "key": h.ScopeKey, "url": h.URL}})
	})
}

func itoa(i int64) string { return fmt.Sprint(i) }
