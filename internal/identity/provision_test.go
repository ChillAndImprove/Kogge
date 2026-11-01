// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// Users of a migration become placeholders, which their owners take over at the first sign-in.
func TestPlaceholderTakeover(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		id, matched, err := ImportUser(ctx, tx, "JDoe", "John Doe", "john@example.org")
		if err != nil || matched {
			t.Fatalf("import: %d %v %v", id, matched, err)
		}
		if again, matched, err := ImportUser(ctx, tx, "jdoe", "", ""); err != nil || matched || again != id {
			t.Errorf("repeated import: %d %v %v", again, matched, err)
		}
		u, err := GetUser(ctx, tx, id)
		if err != nil || u.Kind != KindPlaceholder || u.Status != StatusDisabled {
			t.Fatalf("placeholder = %+v, %v", u, err)
		}
		u, created, err := (&Service{}).provision(ctx, tx, &Claims{Issuer: "https://sso.example.org", Subject: "s-1", PreferredUsername: "jdoe",
			Name: "John Doe", Email: "john@example.org", EmailVerified: true})
		if err != nil || created || u.ID != id || u.Kind != KindPerson || u.Status != StatusActive {
			t.Fatalf("takeover = %+v, created %v, %v", u, created, err)
		}
		if again, matched, err := ImportUser(ctx, tx, "john.doe", "", "JOHN@example.org"); err != nil || !matched || again != id {
			t.Errorf("match by e-mail: %d %v %v", again, matched, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMigrationsDoNotGrantKoggeAccess: users and groups of migrated products do not slip into Kogge's own accounts
// and groups (audit L6) — no merging with Kogge's groups of the same name, which may hold permissions, and no
// match by an e-mail address that is not verified.
func TestMigrationsDoNotGrantKoggeAccess(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		var own int64
		if err := tx.QueryRow(ctx, "INSERT INTO groups (name) VALUES ('Administrators') RETURNING id").Scan(&own); err != nil {
			return err
		}
		id, name, err := ImportGroup(ctx, tx, "administrators")
		if err != nil || id == own || name != "administrators (migrated)" {
			t.Errorf("source group merged into Kogge's group: %d %q %v", id, name, err)
		}
		again, name, err := ImportGroup(ctx, tx, "Administrators")
		if err != nil || again != id || name != "administrators (migrated)" {
			t.Errorf("a later migration did not reuse the migrated group: %d %q %v", again, name, err)
		}
		if fresh, name, err := ImportGroup(ctx, tx, "Testers"); err != nil || name != "Testers" || fresh == id {
			t.Errorf("new group: %d %q %v", fresh, name, err)
		}

		// E-mail addresses: only verified ones match a Kogge user.
		var unverified, verified int64
		if err := tx.QueryRow(ctx, `INSERT INTO users (username, display_name, email, email_verified) VALUES
			('carla', 'Carla', 'ceo@example.org', false) RETURNING id`).Scan(&unverified); err != nil {
			return err
		}
		if uid, matched, err := ImportUser(ctx, tx, "boss", "The CEO", "ceo@example.org"); err != nil || matched || uid == unverified {
			t.Errorf("matched by an unverified address: %d %v %v", uid, matched, err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO users (username, display_name, email, email_verified) VALUES
			('dana', 'Dana', 'dana@example.org', true) RETURNING id`).Scan(&verified); err != nil {
			return err
		}
		if uid, matched, err := ImportUser(ctx, tx, "dschmidt", "Dana Schmidt", "dana@example.org"); err != nil || !matched || uid != verified {
			t.Errorf("not matched by a verified address: %d %v %v", uid, matched, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
