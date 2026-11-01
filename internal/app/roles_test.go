// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// withUser returns a database URL with other credentials.
func withUser(raw, user, password string) string {
	u, _ := url.Parse(raw)
	u.User = url.UserPassword(user, password)
	return u.String()
}

// TestSeparateDatabaseRoles: an installation that ran as the owner of its database is set up with separate roles
// (kogge admin init-database): the owner migrates, Kogge runs as kogge_app, which cannot change or delete audit
// events — also in tables of later migrations — and Kogge works as kogge_app.
func TestSeparateDatabaseRoles(t *testing.T) {
	ctx := context.Background()
	superURL := pgtest.NewEmpty(t)
	super, err := pgx.Connect(ctx, superURL)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	u, _ := url.Parse(superURL)
	database := strings.TrimPrefix(u.Path, "/")
	owner := "t_owner_" + strings.ToLower(crypto.RandomBase32(8))
	for _, sql := range []string{"CREATE ROLE " + owner + " LOGIN PASSWORD 'Owner-Secret-2026'", "ALTER DATABASE " + database + " OWNER TO " + owner} {
		if _, err := super.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), withUser(strings.Replace(superURL, "/"+database, "/postgres", 1), u.User.Username(), "kogge"))
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+owner)
	})
	ownerURL := withUser(superURL, owner, "Owner-Secret-2026")

	// The installation so far: migrated by the owner, Kogge running as the owner.
	ownerPool, err := db.Open(ctx, ownerURL, "test-owner", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()
	if err := db.Migrate(ctx, ownerPool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if writable, role, err := db.AuditWritable(ctx, ownerPool); err != nil || !writable || role != owner {
		t.Fatalf("the owner's rights on the audit log: %v %s %v", writable, role, err)
	}
	// The role kogge_app belongs to the whole test server and may exist already: here it got nothing so far.
	var exists bool
	if err := super.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kogge_app')").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		for _, sql := range []string{"REVOKE ALL ON ALL TABLES IN SCHEMA public FROM kogge_app",
			"REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM kogge_app", "REVOKE ALL ON SCHEMA public FROM kogge_app",
			"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA public REVOKE ALL ON TABLES FROM kogge_app"} {
			if _, err := super.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Set up as a superuser, twice: it can run again.
	if _, err := db.SetUpRoles(ctx, super, db.RoleSetup{Owner: owner}); err == nil && !exists {
		t.Fatal("kogge_app was set up without a password")
	}
	if _, err := db.SetUpRoles(ctx, super, db.RoleSetup{Owner: owner, AppPassword: "pass word"}); err == nil {
		t.Error("a password with a space was accepted")
	}
	done, err := db.SetUpRoles(ctx, super, db.RoleSetup{Owner: owner, AppPassword: "App-Secret-2026"})
	if err != nil {
		t.Fatalf("set-up: %v", err)
	}
	if _, err := db.SetUpRoles(ctx, super, db.RoleSetup{Owner: owner}); err != nil {
		t.Fatalf("set-up again: %v (%v)", err, done)
	}
	appURL := withUser(superURL, db.AppRole, "App-Secret-2026")
	asApp, err := pgx.Connect(ctx, appURL)
	if err != nil {
		t.Fatalf("connect as kogge_app: %v", err)
	}
	defer asApp.Close(ctx)
	if _, err := asApp.Exec(ctx, "INSERT INTO audit_events (action) VALUES ('test.event')"); err != nil {
		t.Fatalf("kogge_app cannot add audit events: %v", err)
	}
	for _, sql := range []string{"UPDATE audit_events SET action = 'test.forged'", "DELETE FROM audit_events", "TRUNCATE audit_events",
		"UPDATE audit_digests SET digest = '\\x01'", "DELETE FROM audit_digests", "DROP TABLE users", "CREATE TABLE intruder (id int)"} {
		if _, err := asApp.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "permission denied") &&
			!strings.Contains(err.Error(), "must be owner") {
			t.Errorf("%s as kogge_app: %v", sql, err)
		}
	}
	if writable, _, err := db.AuditWritable(ctx, asApp); err != nil || writable {
		t.Errorf("kogge_app's rights on the audit log: %v %v", writable, err)
	}
	// A table of a later migration, created by the owner, is Kogge's to use.
	if _, err := ownerPool.Exec(ctx, "CREATE TABLE later_feature (id bigint GENERATED ALWAYS AS IDENTITY, name text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := asApp.Exec(ctx, "INSERT INTO later_feature (name) VALUES ('ok')"); err != nil {
		t.Errorf("a table of a later migration: %v", err)
	}

	// Kogge serves as kogge_app.
	env := apitest.New(t, func(c *config.Config) { c.DatabaseURL = appURL })
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "ROL", "name": "Roles"}, nil)
	var events []struct {
		Action string `json:"action"`
	}
	admin.Get("/api/v1/admin/audit?action=project.", &events)
	if len(events) != 1 || events[0].Action != "project.create" {
		t.Errorf("audit as kogge_app: %+v", events)
	}
	var role string
	if err := env.App.Pool.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil || role != db.AppRole {
		t.Errorf("Kogge runs as %q (%v)", role, err)
	}
}

// TestRolesScriptCreatesTheSameRoles: the Compose stack's database creates the roles that kogge admin init-database
// creates, with the same attributes (deploy/db/roles.sh).
func TestRolesScriptCreatesTheSameRoles(t *testing.T) {
	script, err := os.ReadFile("../../deploy/db/roles.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`CREATE ROLE ` + db.AppRole + ` LOGIN PASSWORD`),
		regexp.MustCompile(`GRANT CONNECT ON DATABASE "\$POSTGRES_DB" TO ` + db.AppRole),
		regexp.MustCompile(`CREATE ROLE ` + db.MaintRole + ` NOLOGIN`),
	} {
		if !want.Match(script) {
			t.Errorf("deploy/db/roles.sh does not %s", want)
		}
	}
}
