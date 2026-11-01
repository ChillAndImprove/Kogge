// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/platform/audit"
)

// Retention: trashed pages and audit events are kept as long as the settings say. Build logs and
// artifacts have their own jobs in the builds module, sessions and login states theirs.

// bulkChangeRetention is how long the results of finished bulk changes of issues are kept.
const bulkChangeRetention = 30 * 24 * time.Hour

// RetentionArgs purges what is older than its retention.
type RetentionArgs struct{}

// Kind implements river.JobArgs.
func (RetentionArgs) Kind() string { return "platform.retention" }

type retentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	app *App
}

// appendOnlyNoted makes the note that the audit log is purged by the operator appear once per process.
var appendOnlyNoted sync.Once

func (w *retentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	return retention(ctx, w.app)
}

func retention(ctx context.Context, a *App) error {
	st, err := a.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if st.TrashRetentionDays > 0 {
		n, err := a.Wiki.PurgeTrash(ctx, time.Duration(st.TrashRetentionDays)*24*time.Hour)
		if err != nil {
			return err
		}
		if n > 0 {
			a.Log.Info("purged pages from the trash", "pages", n, "retention_days", st.TrashRetentionDays)
		}
	}
	// The results of bulk changes are for checking them soon after; the audit log keeps what they were.
	if n, err := a.Issues.PurgeBulkChanges(ctx, bulkChangeRetention); err != nil {
		return err
	} else if n > 0 {
		a.Log.Info("purged bulk changes", "changes", n)
	}
	n, err := audit.Purge(ctx, a.Pool, st.AuditRetentionDays)
	switch {
	case errors.Is(err, audit.ErrAppendOnly):
		// The role Kogge runs as cannot change the audit log; the operator purges it with the owner's role.
		appendOnlyNoted.Do(func() {
			a.Log.Info("the audit log is append-only for Kogge's database role; run `kogge admin purge-audit` with the owner's database URL to apply its retention")
		})
	case err != nil:
		return err
	case n > 0:
		a.Log.Info("purged audit events", "events", n, "retention_days", st.AuditRetentionDays)
	}
	return nil
}
