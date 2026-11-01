// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db_test

import (
	"context"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// TestAbandonedQueriesStop: a query whose context ends — a request stopped at its time limit — stops in the
// database too, not only in Kogge (pgx cancels it as it gives the connection up), so that the time limits of
// requests also end their work in the database.
func TestAbandonedQueriesStop(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := pool.Exec(short, "SELECT pg_sleep(120) /* abandoned */"); err == nil {
		t.Fatal("the query outlived its context")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var running int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND query LIKE 'SELECT pg_sleep(120) /* abandoned */%' AND state = 'active'`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		if running == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the database still runs the abandoned query")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestPoolSize: pools have DefaultMaxConns connections unless the database URL or the caller sets their size.
func TestPoolSize(t *testing.T) {
	url := pgtest.New(t)
	ctx := context.Background()
	for _, c := range []struct {
		url  string
		max  int32
		want int32
	}{
		{url, 0, db.DefaultMaxConns},
		{url + "&pool_max_conns=7", 0, 7},
		{url, 3, 3},
	} {
		pool, err := db.Open(ctx, c.url, "kogge-test", c.max)
		if err != nil {
			t.Fatal(err)
		}
		if got := pool.Config().MaxConns; got != c.want {
			t.Errorf("Open(%q, %d): %d connections, want %d", c.url, c.max, got, c.want)
		}
		pool.Close()
	}
}
