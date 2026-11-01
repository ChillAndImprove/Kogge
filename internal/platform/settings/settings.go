// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package settings stores instance settings edited in the administration. Values are cached per process and
// refreshed after changes on any node (live message "settings").
package settings

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/db"
)

// Instance holds the instance settings with their defaults.
type Instance struct {
	Name                string `json:"name" doc:"Name of the installation, shown in the header and e-mails"`
	DefaultLocale       string `json:"defaultLocale" enum:"de,en" doc:"Language for new users"`
	DefaultTimeZone     string `json:"defaultTimeZone" doc:"IANA time zone for new users and schedules"`
	ContentLanguage     string `json:"contentLanguage" enum:"german,english,simple" doc:"Language of the content for full-text search"`
	AllowAnonymous      bool   `json:"allowAnonymous" doc:"Allow anonymous access to content granted to 'anyone'"`
	AllowExternalImages bool   `json:"allowExternalImages" doc:"Allow images from other servers in documents"`
	RequireTwoFactor    bool   `json:"requireTwoFactor,omitempty" doc:"Local accounts sign in with a second factor: who signs in with a password and has none sets it up first"`

	// Limits: what one user with ordinary access may store, so that nobody fills the data volume (sizes in MiB; 0
	// switches off the limits whose documentation says so). Changes have to name the limits (no omitempty): a client
	// that does not know one cannot switch it off unawares.
	MaxAttachmentMB     int `json:"maxAttachmentMb" minimum:"1" maximum:"10240" doc:"Maximum size of an attachment (issues and pages) in MiB"`
	MaxLFSObjectMB      int `json:"maxLfsObjectMb,omitempty" minimum:"1" maximum:"102400" doc:"Maximum size of a Git LFS object in MiB"`
	MaxPushMB           int `json:"maxPushMb" minimum:"1" maximum:"1048576" doc:"Maximum size of one push over HTTPS or SSH (the pack git sends) in MiB"`
	RepositoryQuotaMB   int `json:"repositoryQuotaMb" minimum:"0" maximum:"1048576" doc:"Maximum size of a repository, its Git and LFS objects together, in MiB; 0 = no quota. Administrators can give single repositories another quota."`
	MaxGitFileMB        int `json:"maxGitFileMb" minimum:"0" maximum:"1048576" doc:"Maximum size of a file pushed without Git LFS in MiB; 0 = no limit"`
	MaxArtifactMB       int `json:"maxArtifactMb" minimum:"1" maximum:"1048576" doc:"Maximum size of one artifact of a pipeline job in MiB"`
	MaxRunArtifactsMB   int `json:"maxRunArtifactsMb" minimum:"0" maximum:"1048576" doc:"Maximum size of all artifacts of a pipeline run together in MiB; 0 = no limit"`
	MaxCacheMB          int `json:"maxCacheMb" minimum:"1" maximum:"1048576" doc:"Maximum size of a pipeline cache in MiB"`
	MaxJobLogMB         int `json:"maxJobLogMb" minimum:"1" maximum:"10240" doc:"Maximum size of the log of a pipeline job in MiB; the rest is cut off, the job goes on"`
	MinFreeSpaceMB      int `json:"minFreeSpaceMb" minimum:"0" maximum:"1048576" doc:"Free space the file system of the data directory keeps, in MiB: below it, pushes and uploads are refused. The larger of this and minFreeSpacePercent applies; both 0 = no check."`
	MinFreeSpacePercent int `json:"minFreeSpacePercent" minimum:"0" maximum:"50" doc:"Free space the file system of the data directory keeps, in percent of its size"`

	HoursPerDay           int    `json:"hoursPerDay" minimum:"1" maximum:"24" doc:"Working hours per day for time tracking"`
	DaysPerWeek           int    `json:"daysPerWeek" minimum:"1" maximum:"7" doc:"Working days per week for time tracking"`
	AuditRetentionDays    int    `json:"auditRetentionDays" minimum:"30" doc:"Days audit events are kept"`
	TrashRetentionDays    int    `json:"trashRetentionDays" minimum:"0" doc:"Days deleted pages stay in the trash (0 = forever)"`
	BuildLogRetentionDays int    `json:"buildLogRetentionDays" minimum:"0" doc:"Days the logs of pipeline jobs are kept (0 = forever)"`
	MaxJobTimeoutMinutes  int    `json:"maxJobTimeoutMinutes" minimum:"1" maximum:"1440" doc:"Longest time a pipeline job may run, whatever its timeout says"`
	MaxTokenDays          int    `json:"maxTokenDays" minimum:"0" maximum:"3650" doc:"Longest lifetime of new personal access tokens in days (0 = no limit)"`
	WebhookTargets        string `json:"webhookTargets" maxLength:"4000" doc:"Hosts, addresses and networks (CIDR) that webhooks and repository imports may reach, one per line; public for all public addresses. Empty: public addresses only. Loopback, link-local and metadata addresses never."`
	Announcement          string `json:"announcement" maxLength:"500" doc:"Banner text shown to all users"`
}

// Defaults are used for settings that were never saved. The limits are far above what normal work needs (a push of a
// large repository's whole history, release builds with their binaries, a cache of dependencies); they stop what
// would fill the disk.
func Defaults() Instance {
	return Instance{
		Name: "Kogge", DefaultLocale: "de", DefaultTimeZone: "Europe/Berlin", ContentLanguage: "german",
		MaxAttachmentMB: 100, MaxLFSObjectMB: 5120, MaxPushMB: 2048, MaxArtifactMB: 1024, MaxRunArtifactsMB: 10240,
		MaxCacheMB: 4096, MaxJobLogMB: 64, MinFreeSpaceMB: 2048, MinFreeSpacePercent: 2,
		HoursPerDay: 8, DaysPerWeek: 5, AuditRetentionDays: 730, TrashRetentionDays: 0,
		BuildLogRetentionDays: 0, MaxJobTimeoutMinutes: 1440, MaxTokenDays: 365,
	}
}

// MiB converts a size setting to bytes.
func MiB(n int) int64 { return int64(n) << 20 }

// Limits returns the limit settings by their names in the API (for the audit log of changes).
func (i Instance) Limits() map[string]int {
	return map[string]int{"maxAttachmentMb": i.MaxAttachmentMB, "maxLfsObjectMb": i.MaxLFSObjectMB, "maxPushMb": i.MaxPushMB,
		"repositoryQuotaMb": i.RepositoryQuotaMB, "maxGitFileMb": i.MaxGitFileMB, "maxArtifactMb": i.MaxArtifactMB,
		"maxRunArtifactsMb": i.MaxRunArtifactsMB, "maxCacheMb": i.MaxCacheMB, "maxJobLogMb": i.MaxJobLogMB,
		"minFreeSpaceMb": i.MinFreeSpaceMB, "minFreeSpacePercent": i.MinFreeSpacePercent}
}

const key = "instance"

// Service reads and writes settings.
type Service struct {
	pool    *pgxpool.Pool
	mu      sync.RWMutex
	cached  *Instance
	expires time.Time
	// version changes with every invalidation: settings read before one are not kept, so that a change that
	// commits while another request reads the old settings is not hidden for the cache's lifetime.
	version atomic.Int64
}

// New creates the service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Get returns the current settings.
func (s *Service) Get(ctx context.Context) (Instance, error) {
	s.mu.RLock()
	if s.cached != nil && time.Now().Before(s.expires) {
		v := *s.cached
		s.mu.RUnlock()
		return v, nil
	}
	s.mu.RUnlock()
	version := s.version.Load()
	v := Defaults()
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&raw)
	if err != nil && !db.IsNoRows(err) {
		return v, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return Defaults(), err
		}
	}
	s.mu.Lock()
	if s.version.Load() == version {
		s.cached, s.expires = &v, time.Now().Add(30*time.Second)
	}
	s.mu.Unlock()
	return v, nil
}

// Save stores the settings within tx.
func (s *Service) Save(ctx context.Context, tx pgx.Tx, v Instance, actorID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		key, v, actorID)
	if err == nil {
		s.Invalidate()
	}
	return err
}

// Invalidate drops the cache (after a change on any node).
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.version.Add(1)
	s.cached = nil
	s.mu.Unlock()
}
