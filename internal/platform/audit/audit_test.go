// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// TestAppendOnlyForTheApplicationRole: with separate database roles, the role Kogge runs as can add audit events
// and digests but not change or delete them, and its events carry the server's time — so the digest chain shows
// tampering.
func TestAppendOnlyForTheApplicationRole(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	// The role exists once per database cluster; the grants are those of the migrations.
	if _, err := pool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kogge_app') THEN CREATE ROLE kogge_app NOLOGIN; END IF;
		END $$`); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA public TO kogge_app",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO kogge_app",
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO kogge_app",
		"REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM kogge_app",
		"REVOKE UPDATE, DELETE, TRUNCATE ON audit_digests FROM kogge_app",
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	asApp := func(fn func(tx pgx.Tx) error) error {
		return db.InTx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE kogge_app"); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	// Events and digests can be added; events get the server's time, even when the past is asked for.
	err := asApp(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO audit_events (at, action) VALUES (now() - interval '10 days', 'test.backdated')"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO audit_digests (day, events, digest) VALUES (current_date - 20, 0, '\\x00')")
		return err
	})
	if err != nil {
		t.Fatalf("the application role cannot add: %v", err)
	}
	var at time.Time
	if err := pool.QueryRow(ctx, "SELECT at FROM audit_events WHERE action = 'test.backdated'").Scan(&at); err != nil ||
		time.Since(at) > time.Minute {
		t.Errorf("a backdated event: %v %v", at, err)
	}
	for _, sql := range []string{
		"UPDATE audit_events SET action = 'test.forged'",
		"DELETE FROM audit_events",
		"UPDATE audit_digests SET digest = '\\x01'",
		"DELETE FROM audit_digests",
	} {
		err := asApp(func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql); return err })
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s as the application role: %v", sql, err)
		}
	}
}
