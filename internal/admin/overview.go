// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package admin

import (
	"context"
	"slices"
	"time"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/storage"
)

// The administration's overview: whether the installation is healthy, how it is used, how accounts are secured,
// and what needs an administrator's attention. The modules report their own numbers (Figures).

// Figures returns a module's numbers for the overview, by name.
type Figures func(ctx context.Context) (map[string]int64, error)

// Overview is the state of the installation at a glance.
type Overview struct {
	Version          string     `json:"version"`
	StartedAt        time.Time  `json:"startedAt"`
	DataFreeBytes    uint64     `json:"dataFreeBytes"`
	DataTotalBytes   uint64     `json:"dataTotalBytes"`
	DataReserveBytes uint64     `json:"dataReserveBytes" doc:"Free space the settings keep; below it, pushes and uploads are refused"`
	DatabaseBytes    int64      `json:"databaseBytes"`
	Mail             bool       `json:"mail" doc:"E-mail is configured"`
	SSO              bool       `json:"sso" doc:"Single sign-on is configured"`
	LastBackup       *time.Time `json:"lastBackup,omitempty" doc:"When the last backup was written (kogge admin backup)"`
	LastBackupSize   int64      `json:"lastBackupBytes,omitempty"`
	Jobs             JobCounts  `json:"jobs"`
	People           People     `json:"people"`
	// Figures are the modules' numbers: issueProjects, issues, spaces, pages, repositories, runs7d, …
	Figures   map[string]int64 `json:"figures"`
	Attention []Attention      `json:"attention" doc:"What needs an administrator, the most urgent first"`
}

// JobCounts counts the background jobs.
type JobCounts struct {
	Waiting      int64 `json:"waiting" doc:"Jobs that wait to run or to be retried"`
	Failed24h    int64 `json:"failed24h" doc:"Jobs given up in the last 24 hours"`
	OldestWaitMs int64 `json:"oldestWaitMs" doc:"How long the oldest waiting job has waited"`
}

// People counts the accounts and their security.
type People struct {
	Active         int64 `json:"active"`
	Disabled       int64 `json:"disabled"`
	ActiveLast30d  int64 `json:"activeLast30d" doc:"Signed in during the last 30 days"`
	Administrators int64 `json:"administrators"`
	Local          int64 `json:"local" doc:"Active accounts with a local password"`
	TwoFactor      int64 `json:"twoFactor" doc:"Of those, with two-factor authentication"`
	Locked         int64 `json:"locked" doc:"Locked after wrong passwords"`
	Sessions       int64 `json:"sessions" doc:"Browser sessions now"`
	FailedSignIns  int64 `json:"failedSignIns24h" doc:"Failed sign-ins in the last 24 hours"`
	// AdminsWithoutTwoFactor are administrators with a local password but no second factor.
	AdminsWithoutTwoFactor int64 `json:"adminsWithoutTwoFactor"`
	TwoFactorRequired      bool  `json:"twoFactorRequired" doc:"Local accounts must use two-factor authentication (a setting)"`
}

// Attention is something an administrator should look at; the interface words it by its key.
type Attention struct {
	Key      string     `json:"key" enum:"storage-low,disk-low,audit-forwarding-failing,backup-failed,jobs-failed,no-backup,backup-old,admins-without-two-factor,single-admin,mail-off,agents-awaiting-approval,accounts-locked"`
	Severity string     `json:"severity" enum:"critical,warning,info"`
	Count    int64      `json:"count,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
}

// backupAge is how old the last backup may be before the overview asks for one.
const backupAge = 7 * 24 * time.Hour

func (h *Handler) overview(ctx context.Context, _ *struct{}) (*struct{ Body Overview }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	st, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	disk := storage.Of(st, h.Config.DataDir)
	free, total := disk.Free, disk.Total
	o := Overview{Version: h.Version, StartedAt: h.Started, DataFreeBytes: free, DataTotalBytes: total, DataReserveBytes: disk.Reserve,
		Mail: h.Config.SMTP.Enabled(), SSO: h.Config.OIDC.Enabled(), Figures: map[string]int64{}, Attention: []Attention{}}
	p := &o.People
	p.TwoFactorRequired = st.RequireTwoFactor
	err = h.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),
		count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'disabled'),
		count(*) FILTER (WHERE status = 'active' AND last_login_at > now() - interval '30 days'),
		count(*) FILTER (WHERE status = 'active' AND password_hash IS NOT NULL),
		count(*) FILTER (WHERE status = 'active' AND password_hash IS NOT NULL
			AND EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = users.id AND t.confirmed_at IS NOT NULL)),
		count(*) FILTER (WHERE status = 'active' AND locked_until > now())
		FROM users WHERE kind = 'person'`).Scan(&o.DatabaseBytes, &p.Active, &p.Disabled, &p.ActiveLast30d, &p.Local, &p.TwoFactor, &p.Locked)
	if err != nil {
		return nil, err
	}
	err = h.Pool.QueryRow(ctx, `SELECT count(DISTINCT u.id), count(DISTINCT u.id) FILTER (WHERE u.password_hash IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL))
		FROM global_grants g JOIN users u ON u.id = g.principal_id
		WHERE g.permission = 'admin' AND g.principal_type = 'user' AND u.status = 'active' AND u.kind = 'person'`).
		Scan(&p.Administrators, &p.AdminsWithoutTwoFactor)
	if err != nil {
		return nil, err
	}
	if err := h.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sessions WHERE expires_at > now() AND idle_expires_at > now()),
		(SELECT count(*) FROM audit_events WHERE action = 'auth.login.failed' AND at > now() - interval '24 hours')`).
		Scan(&p.Sessions, &p.FailedSignIns); err != nil {
		return nil, err
	}
	if err := h.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state IN ('available', 'retryable', 'scheduled') AND scheduled_at <= now()),
		count(*) FILTER (WHERE state = 'discarded' AND finalized_at > now() - interval '24 hours'),
		coalesce(extract(epoch FROM now() - min(scheduled_at) FILTER (WHERE state IN ('available', 'retryable') AND scheduled_at <= now())) * 1000, 0)::bigint
		FROM river_job`).Scan(&o.Jobs.Waiting, &o.Jobs.Failed24h, &o.Jobs.OldestWaitMs); err != nil {
		return nil, err
	}
	var backupBytes *int64
	if err := h.Pool.QueryRow(ctx, `SELECT at, (details->>'bytes')::bigint FROM audit_events WHERE action = 'backup.written'
		ORDER BY id DESC LIMIT 1`).Scan(&o.LastBackup, &backupBytes); err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	if backupBytes != nil {
		o.LastBackupSize = *backupBytes
	}
	// Whether the latest attempt failed.
	var lastFailed *time.Time
	var lastAction string
	var lastAt time.Time
	if err := h.Pool.QueryRow(ctx, `SELECT action, at FROM audit_events WHERE action IN ('backup.written', 'backup.failed')
		ORDER BY id DESC LIMIT 1`).Scan(&lastAction, &lastAt); err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	if lastAction == "backup.failed" {
		lastFailed = &lastAt
	}
	for _, f := range h.Figures {
		m, err := f(ctx)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			o.Figures[k] = v
		}
	}

	// What needs attention, the most urgent first.
	add := func(key, severity string, count int64, since *time.Time) {
		o.Attention = append(o.Attention, Attention{Key: key, Severity: severity, Count: count, Since: since})
	}
	// Below the reserve, Kogge refuses pushes and uploads; before that, the disk fills up.
	if disk.Low() {
		add("storage-low", "critical", 0, nil)
	} else if total > 0 && (free < total/20 || free < 2<<30) {
		add("disk-low", "critical", 0, nil)
	} else if total > 0 && (free < total/10 || free < 10<<30) {
		add("disk-low", "warning", 0, nil)
	}
	if h.Config.AuditForward.Enabled() {
		if st, err := audit.Forwarding(ctx, h.Pool); err == nil && st.Error != "" {
			add("audit-forwarding-failing", "critical", st.Waiting, st.FailingSince)
		}
	}
	if o.Jobs.Failed24h > 0 {
		add("jobs-failed", "warning", o.Jobs.Failed24h, nil)
	}
	switch {
	case lastFailed != nil:
		add("backup-failed", "critical", 0, lastFailed)
	case o.LastBackup == nil:
		add("no-backup", "warning", 0, nil)
	case time.Since(*o.LastBackup) > backupAge:
		add("backup-old", "warning", 0, o.LastBackup)
	}
	if p.AdminsWithoutTwoFactor > 0 {
		add("admins-without-two-factor", "warning", p.AdminsWithoutTwoFactor, nil)
	}
	if p.Administrators == 1 {
		add("single-admin", "info", 1, nil)
	}
	if !o.Mail {
		add("mail-off", "info", 0, nil)
	}
	if n := o.Figures["agentsAwaitingApproval"]; n > 0 {
		add("agents-awaiting-approval", "info", n, nil)
	}
	if p.Locked > 0 {
		add("accounts-locked", "info", p.Locked, nil)
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	slices.SortStableFunc(o.Attention, func(a, b Attention) int { return rank[a.Severity] - rank[b.Severity] })
	return &struct{ Body Overview }{o}, nil
}
