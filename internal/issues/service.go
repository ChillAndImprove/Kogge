// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/live"
	"kogge.de/kogge/internal/platform/settings"
)

// Service implements the issues module.
type Service struct {
	pool     *pgxpool.Pool
	authz    *authz.Service
	settings *settings.Service
	bus      *events.Bus
	blobs    *blob.Store
	log      *slog.Logger

	access accessCache

	cfgMu      sync.Mutex
	cfgCache   *configSnapshot
	cfgVersion atomic.Int64

	spansReady atomic.Bool // the spans of all issues exist (spans.go)

	mailer *subscriptionMailer // filter subscriptions; nil without e-mail (subscriptions.go)
}

// NewService creates the service.
func NewService(pool *pgxpool.Pool, az *authz.Service, st *settings.Service, bus *events.Bus, blobs *blob.Store,
	logger *slog.Logger) *Service {
	return &Service{pool: pool, authz: az, settings: st, bus: bus, blobs: blobs, log: logger}
}

// Pool returns the database pool.
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

// DropConfig drops the cached configuration (after a change on any node).
func (s *Service) DropConfig() { s.cfgVersion.Add(1) }

// configChanged tells all nodes (at commit) to reload configuration and permissions.
func (s *Service) configChanged(ctx context.Context, tx pgx.Tx) error {
	if err := live.InvalidateCache(ctx, tx, "issues-config"); err != nil {
		return err
	}
	return live.InvalidateCache(ctx, tx, "authz")
}

// permissionsChanged tells all nodes that issue permissions changed (role members, leads, schemes).
func (s *Service) permissionsChanged(ctx context.Context, tx pgx.Tx) error {
	return live.InvalidateCache(ctx, tx, "authz")
}

// afterCommit applies cache invalidation locally right away (other nodes follow on the notification).
func (s *Service) afterCommit() {
	s.DropConfig()
	s.authz.Invalidate()
}

type workflowRecord struct {
	ID          int64
	Name        string
	Description string
	Def         *workflow.Definition
	Version     int
}

type configSnapshot struct {
	version     int64
	loaded      time.Time
	types       map[int64]*IssueType
	typeList    []*IssueType
	statuses    map[int64]*Status
	statusList  []*Status
	priorities  map[int64]*Priority
	prioList    []*Priority
	resolutions map[int64]*Resolution
	resList     []*Resolution
	linkTypes   map[int64]*LinkType
	linkList    []*LinkType
	roles       map[int64]*Role
	roleList    []*Role
	fields      map[int64]*CustomField
	fieldList   []*CustomField
	workflows   map[int64]*workflowRecord
}

func (c *configSnapshot) fieldByName(name string) *CustomField {
	for _, f := range c.fieldList {
		if strings.EqualFold(f.Name, name) {
			return f
		}
	}
	return nil
}

func (c *configSnapshot) statusByName(name string) *Status {
	for _, st := range c.statusList {
		if strings.EqualFold(st.Name, name) {
			return st
		}
	}
	return nil
}

func (c *configSnapshot) defaultResolution() *Resolution {
	for _, r := range c.resList {
		if r.IsDefault {
			return r
		}
	}
	if len(c.resList) > 0 {
		return c.resList[0]
	}
	return nil
}

func (c *configSnapshot) defaultPriority() *Priority {
	for _, p := range c.prioList {
		if p.IsDefault {
			return p
		}
	}
	return nil
}

// config returns the cached configuration, reloading it when it changed.
func (s *Service) config(ctx context.Context) (*configSnapshot, error) {
	v := s.cfgVersion.Load()
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if c := s.cfgCache; c != nil && c.version == v && time.Since(c.loaded) < 5*time.Minute {
		return c, nil
	}
	c := &configSnapshot{version: v, loaded: time.Now(), types: map[int64]*IssueType{}, statuses: map[int64]*Status{},
		priorities: map[int64]*Priority{}, resolutions: map[int64]*Resolution{}, linkTypes: map[int64]*LinkType{},
		roles: map[int64]*Role{}, fields: map[int64]*CustomField{}, workflows: map[int64]*workflowRecord{}}
	q := s.pool
	rows, err := q.Query(ctx, "SELECT id, name, description, icon, color, level, position FROM issue_types ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &IssueType{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Icon, &t.Color, &t.Level, &t.Position); err != nil {
			rows.Close()
			return nil, err
		}
		c.types[t.ID], c.typeList = t, append(c.typeList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, description, category, position FROM statuses ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &Status{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Category, &t.Position); err != nil {
			rows.Close()
			return nil, err
		}
		c.statuses[t.ID], c.statusList = t, append(c.statusList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, description, icon, color, position, is_default FROM priorities ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &Priority{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Icon, &t.Color, &t.Position, &t.IsDefault); err != nil {
			rows.Close()
			return nil, err
		}
		c.priorities[t.ID], c.prioList = t, append(c.prioList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, description, position, is_default FROM resolutions ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &Resolution{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Position, &t.IsDefault); err != nil {
			rows.Close()
			return nil, err
		}
		c.resolutions[t.ID], c.resList = t, append(c.resList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, outward, inward FROM issue_link_types ORDER BY lower(name)")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &LinkType{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Outward, &t.Inward); err != nil {
			rows.Close()
			return nil, err
		}
		c.linkTypes[t.ID], c.linkList = t, append(c.linkList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, description FROM project_roles ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &Role{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description); err != nil {
			rows.Close()
			return nil, err
		}
		c.roles[t.ID], c.roleList = t, append(c.roleList, t)
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT id, name, description, type, options, config, project_ids, issue_type_ids
		FROM custom_fields ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		f := &CustomField{}
		var opts, cfg []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.Type, &opts, &cfg, &f.ProjectIDs, &f.IssueTypeIDs); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(opts, &f.Options)
		_ = json.Unmarshal(cfg, &f.Config)
		if f.Options == nil {
			f.Options = []Option{}
		}
		if f.Config == nil {
			f.Config = map[string]any{}
		}
		f.Key = fieldKey(f.ID)
		c.fields[f.ID], c.fieldList = f, append(c.fieldList, f)
	}
	rows.Close()
	rows, err = q.Query(ctx, "SELECT id, name, description, definition, version FROM workflows")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		w := &workflowRecord{}
		var def []byte
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &def, &w.Version); err != nil {
			rows.Close()
			return nil, err
		}
		if w.Def, err = workflow.Parse(def); err != nil {
			rows.Close()
			return nil, err
		}
		c.workflows[w.ID] = w
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cfgCache = c
	return c, nil
}
