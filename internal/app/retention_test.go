// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestRetentionAndAuditIntegrity: the retention purges the trash and old audit events as the settings say, and
// the digest chain shows changed and deleted events — but not those the retention purged.
func TestRetentionAndAuditIntegrity(t *testing.T) {
	env := apitest.New(t)
	ctx := context.Background()
	admin := env.Admin()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.App.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args river.JobArgs) {
		t.Helper()
		if _, err := env.App.Jobs.Insert(ctx, args, nil); err != nil {
			t.Fatal(err)
		}
		env.WorkJobs()
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := env.App.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Audit events on four days: 400, 100, 2 and 1 days ago.
	for _, daysAgo := range []int{400, 100, 2, 1} {
		for i := 0; i < 3; i++ {
			exec("INSERT INTO audit_events (at, action) VALUES ($1, 'test.event')", time.Now().UTC().AddDate(0, 0, -daysAgo))
		}
	}
	run(app.AuditDigestArgs{})
	days, findings, err := audit.Verify(ctx, env.App.Pool, 730)
	if err != nil || len(findings) != 0 || days < 4 {
		t.Fatalf("verify: %d %v %v", days, findings, err)
	}

	// A changed and a deleted event show.
	exec("UPDATE audit_events SET action = 'test.forged' WHERE id = (SELECT min(id) FROM audit_events WHERE action = 'test.event' AND at > now() - interval '3 days')")
	exec("DELETE FROM audit_events WHERE id = (SELECT max(id) FROM audit_events WHERE action = 'test.event' AND at > now() - interval '30 hours')")
	if _, findings, _ := audit.Verify(ctx, env.App.Pool, 730); len(findings) != 2 {
		t.Errorf("tampering: %+v", findings)
	}

	// A trashed page, old enough for a retention of 30 days.
	admin.Post("/api/v1/wiki/spaces", map[string]any{"key": "RET", "name": "Retention"}, nil)
	var pg struct {
		ID int64 `json:"id"`
	}
	admin.Post("/api/v1/wiki/spaces/RET/pages", map[string]any{"title": "Old"}, &pg)
	admin.Post(apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), map[string]any{"title": "Old", "baseVersion": 0,
		"body": map[string]any{"type": "doc", "content": []any{}}}, nil)
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/wiki/pages/%d", pg.ID), nil, nil)
	exec("UPDATE pages SET trashed_at = now() - interval '40 days' WHERE id = $1", pg.ID)

	// The default retention (audit 730 days, trash forever) keeps everything.
	run(app.RetentionArgs{})
	if n := count("SELECT count(*) FROM audit_events WHERE action LIKE 'test.%'"); n != 11 {
		t.Fatalf("events after the default retention: %d", n)
	}
	if n := count("SELECT count(*) FROM pages WHERE id = $1", pg.ID); n != 1 {
		t.Fatal("the default retention purged the trash")
	}
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	st["auditRetentionDays"], st["trashRetentionDays"] = 365, 30
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	run(app.RetentionArgs{})
	if n := count("SELECT count(*) FROM river_job WHERE kind = 'platform.retention' AND state <> 'completed'"); n != 0 {
		t.Fatal("the retention failed")
	}
	if n := count("SELECT count(*) FROM audit_events WHERE action LIKE 'test.%'"); n != 8 {
		t.Errorf("events after a retention of 365 days: %d", n)
	}
	if n := count("SELECT count(*) FROM pages WHERE id = $1", pg.ID); n != 0 {
		t.Error("the trash was not purged")
	}
	if n := count("SELECT count(*) FROM audit_events WHERE action = 'page.purge' AND details->>'retention' = 'true'"); n != 1 {
		t.Errorf("purge audited %d times", n)
	}
	// The purged day is no finding; the tampering still is.
	if _, findings, _ := audit.Verify(ctx, env.App.Pool, 365); len(findings) != 2 {
		t.Errorf("after the purge: %+v", findings)
	}
}
