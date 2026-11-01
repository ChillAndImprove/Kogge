// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package admin_test

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"

	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/version"
)

type attention struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
}

type overview struct {
	Version string `json:"version"`
	People  struct {
		Active                 int64 `json:"active"`
		Administrators         int64 `json:"administrators"`
		Local                  int64 `json:"local"`
		TwoFactor              int64 `json:"twoFactor"`
		Sessions               int64 `json:"sessions"`
		FailedSignIns          int64 `json:"failedSignIns24h"`
		AdminsWithoutTwoFactor int64 `json:"adminsWithoutTwoFactor"`
	} `json:"people"`
	Figures    map[string]int64 `json:"figures"`
	LastBackup *string          `json:"lastBackup"`
	Attention  []attention      `json:"attention"`
}

func (o overview) has(key, severity string) bool {
	for _, a := range o.Attention {
		if a.Key == key && a.Severity == severity {
			return true
		}
	}
	return false
}

// TestOverview: administrators see the installation at a glance — accounts and their security, the modules'
// numbers, and what needs attention: backups, administrators without a second factor, e-mail, failed sign-ins.
func TestOverview(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	ctx := context.Background()
	env.CreateUser("anna")
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "OVR", "name": "Overview", "template": "kanban"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "OVC", "name": "Code"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/OVC/repos", map[string]any{"name": "app", "readme": true}, nil)
	env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "anna", "password": "Wrong-Password-9"})

	var o overview
	admin.Get("/api/v1/admin/overview", &o)
	if o.Version == "" || o.People.Active != 2 || o.People.Administrators != 1 || o.People.Local != 2 || o.People.Sessions < 2 ||
		o.People.FailedSignIns != 1 || o.People.AdminsWithoutTwoFactor != 1 {
		t.Errorf("people = %+v", o.People)
	}
	if o.Figures["issueProjects"] != 1 || o.Figures["codeProjects"] != 1 || o.Figures["repositories"] != 1 || o.Figures["pages"] < 0 ||
		o.Figures["runs7d"] != 0 {
		t.Errorf("figures = %v", o.Figures)
	}
	for _, want := range []struct{ key, severity string }{{"no-backup", "warning"}, {"admins-without-two-factor", "warning"},
		{"single-admin", "info"}, {"mail-off", "info"}} {
		if !o.has(want.key, want.severity) {
			t.Errorf("no %s %s in %+v", want.severity, want.key, o.Attention)
		}
	}

	// A backup (kogge admin backup records it), then one that failed.
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO audit_events (action, details) VALUES ('backup.written', '{"bytes": 1234}')`); err != nil {
		t.Fatal(err)
	}
	o = overview{}
	admin.Get("/api/v1/admin/overview", &o)
	if o.LastBackup == nil || o.has("no-backup", "warning") || o.has("backup-failed", "critical") {
		t.Errorf("after a backup: %v %+v", o.LastBackup, o.Attention)
	}
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO audit_events (action, details) VALUES ('backup.failed', '{"error": "disk full"}')`); err != nil {
		t.Fatal(err)
	}
	o = overview{}
	admin.Get("/api/v1/admin/overview", &o)
	// The most urgent comes first.
	if !o.has("backup-failed", "critical") || o.Attention[0].Severity != "critical" {
		t.Errorf("after a failed backup: %+v", o.Attention)
	}

	// Only administrators.
	if r := env.Login("anna", apitest.UserPassword).Do(http.MethodGet, "/api/v1/admin/overview", nil); r.Status != http.StatusForbidden {
		t.Errorf("a user: %d", r.Status)
	}
}

// TestSystem: the system information names the build — version, commit and date — for support requests.
func TestSystem(t *testing.T) {
	v, c, d := version.Version, version.Commit, version.Date
	t.Cleanup(func() { version.Version, version.Commit, version.Date = v, c, d })
	version.Version, version.Commit, version.Date = "1.2.0", "0123456789abcdef0123456789abcdef01234567", "2026-10-20T08:15:00Z"
	env := apitest.New(t)

	var info struct {
		Version   string     `json:"version"`
		Commit    string     `json:"commit"`
		BuildDate *time.Time `json:"buildDate"`
		GoVersion string     `json:"goVersion"`
	}
	env.Admin().Get("/api/v1/admin/system", &info)
	if info.Version != "1.2.0" || info.Commit != version.Commit || info.BuildDate == nil ||
		!info.BuildDate.Equal(time.Date(2026, 10, 20, 8, 15, 0, 0, time.UTC)) || info.GoVersion != runtime.Version() {
		t.Errorf("system = %+v", info)
	}
	if r := env.CreateUser("ben").Do(http.MethodGet, "/api/v1/admin/system", nil); r.Status != http.StatusForbidden {
		t.Errorf("a user: %d", r.Status)
	}
}
