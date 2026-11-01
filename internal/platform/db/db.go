// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package db provides the PostgreSQL connection pool, transactions and error helpers. Stores receive a Querier,
// which is either the pool or a transaction, so the caller decides the transaction boundaries.
package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is implemented by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// DefaultMaxConns is the size of a pool unless its URL sets pool_max_conns or its caller another one: requests, the
// job worker (up to 69 jobs at once) and the listener of live updates share it, and pgx's own default (the number of
// CPUs, 4 on the smallest documented server) would let them queue for connections. PostgreSQL's max_connections must
// cover the pools of all nodes.
const DefaultMaxConns = 25

// Open creates a connection pool and verifies the connection. maxConns 0 means pool_max_conns of the URL, or
// DefaultMaxConns.
func Open(ctx context.Context, url, applicationName string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = applicationName
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	} else if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns = DefaultMaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// Beginner starts transactions (*pgxpool.Pool, or pgx.Tx for nested savepoints).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn in a transaction and commits if fn returns nil. A panic rolls back and is re-raised.
func InTx(ctx context.Context, b Beginner, fn func(tx pgx.Tx) error) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PrepareSearch readies a transaction for searches and reports: statements end after timeout, every statement is
// planned for its parameters, and none starts parallel workers.
//
// Searches filter by arrays of what the reader may see (projects, spaces). The generic plan, which PostgreSQL uses
// for a prepared statement from its sixth execution on, cannot know how much such an array selects; for readers
// who may see much, it builds a bitmap of the whole table before each search (three times slower at 200,000
// issues, tools/scale). Parallel workers shorten a search on an idle server, but with several people searching
// they compete for the same processors: at 200,000 issues and four searches at a time, the 95th percentile of
// quick searches was two to four times longer with them.
func PrepareSearch(ctx context.Context, tx pgx.Tx, timeout time.Duration) error {
	_, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', $1, true), set_config('plan_cache_mode', 'force_custom_plan', true),
		set_config('max_parallel_workers_per_gather', '0', true)`, strconv.FormatInt(timeout.Milliseconds(), 10))
	return err
}

// IsNoRows reports whether err means that a query returned no rows.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation reports whether err is a unique violation, optionally of the named constraint or index.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return constraint == "" || pgErr.ConstraintName == constraint
	}
	return false
}

// IsForeignKeyViolation reports whether err is a foreign key violation.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// IsSerializationFailure reports whether a transaction should be retried.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// EscapeLike escapes the LIKE wildcards in s, for use with ESCAPE '\'.
func EscapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '%', '_':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
