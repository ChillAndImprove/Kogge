// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// TestGate: writers hold the Git write lock together; a backup holding it exclusively makes them wait, and they
// give up after their wait.
func TestGate(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	gate, err := db.NewGate(pool, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()

	a, err := gate.Enter(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := gate.Enter(ctx, time.Second)
	if err != nil {
		t.Fatalf("writers exclude each other: %v", err)
	}
	a()
	a() // releasing twice is harmless
	b()

	backup, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close(ctx)
	if _, err := backup.Exec(ctx, "SELECT pg_advisory_lock($1)", db.LockGitWrites); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := gate.Enter(ctx, 200*time.Millisecond); !errors.Is(err, db.ErrPaused) {
		t.Fatalf("enter during a backup: %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 5*time.Second {
		t.Errorf("waited %s", d)
	}

	entered := make(chan error, 1)
	go func() {
		release, err := gate.Enter(ctx, 10*time.Second)
		if err == nil {
			release()
		}
		entered <- err
	}()
	select {
	case err := <-entered:
		t.Fatalf("a writer did not wait for the backup: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := backup.Exec(ctx, "SELECT pg_advisory_unlock($1)", db.LockGitWrites); err != nil {
		t.Fatal(err)
	}
	if err := <-entered; err != nil {
		t.Fatalf("writer after the backup: %v", err)
	}

	// Collectors skip their run while a backup runs.
	running := func() bool {
		var r bool
		if err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
			var err error
			r, err = db.BackupRunning(ctx, tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if running() {
		t.Fatal("backup running without a backup")
	}
	if _, err := backup.Exec(ctx, "SELECT pg_advisory_lock($1)", db.LockFileDeletes); err != nil {
		t.Fatal(err)
	}
	if !running() {
		t.Fatal("the running backup was not noticed")
	}
}
