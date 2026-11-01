// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory locks of the backup protocol. They are cluster-wide, so every node takes part.
const (
	// LockGitWrites is held shared by every change of the repositories on disk (pushes, merges, edits in the
	// browser, branch operations, creating and deleting repositories, maintenance) and exclusively by a backup
	// while it takes its database snapshot and copies the repositories: writes wait, reads continue.
	LockGitWrites int64 = 0x6b6f67676502
	// LockBackup is held by a running backup: one at a time.
	LockBackup int64 = 0x6b6f67676503
	// LockFileDeletes is held shared by jobs that delete files the database refers to (blob and build log
	// collection) and exclusively by a running backup, which waits for running deletions; the jobs skip their run
	// while a backup holds it, so the backup finds every file of its snapshot.
	LockFileDeletes int64 = 0x6b6f67676504
)

// LockAuditForward is held by the node that forwards audit events to a SIEM: one at a time.
const LockAuditForward int64 = 0x6b6f67676505

// ErrPaused is returned by Gate.Enter when a backup kept Git writes paused for longer than the writer may wait.
var ErrPaused = errors.New("git writes are paused while a backup runs")

// Gate lets writers hold LockGitWrites shared. Holders use connections of a small pool of their own, so that
// writers waiting for a backup never take the connections that other work needs.
type Gate struct {
	pool *pgxpool.Pool
}

// NewGate creates a gate with at most maxConns connections, configured like main. Connections are opened on
// demand and closed when idle.
func NewGate(main *pgxpool.Pool, maxConns int32) (*Gate, error) {
	cfg := main.Config()
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = time.Minute
	if name := cfg.ConnConfig.RuntimeParams["application_name"]; name != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = name + "-git-writes"
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	return &Gate{pool: pool}, nil
}

// Close closes the gate's connections.
func (g *Gate) Close() { g.pool.Close() }

// Enter waits at most wait for a running backup and holds LockGitWrites shared until release is called.
// release is safe to call more than once.
func (g *Gate) Enter(ctx context.Context, wait time.Duration) (release func(), err error) {
	actx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tx, err := g.pool.Begin(actx)
	if err != nil {
		if actx.Err() != nil && ctx.Err() == nil {
			return nil, ErrPaused
		}
		return nil, err
	}
	// The server ends the wait itself, so an abandoned writer never waits longer than allowed.
	_, err = tx.Exec(actx, "SELECT set_config('lock_timeout', $1, true)", strconv.FormatInt(max(wait.Milliseconds(), 1), 10))
	if err == nil {
		_, err = tx.Exec(actx, "SELECT pg_advisory_xact_lock_shared($1)", LockGitWrites)
	}
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		var pgErr *pgconn.PgError
		if (errors.As(err, &pgErr) && pgErr.Code == "55P03") || (actx.Err() != nil && ctx.Err() == nil) {
			return nil, ErrPaused
		}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }) }, nil
}

// BackupRunning reports whether a backup is running. Otherwise it holds LockFileDeletes shared until tx ends, so
// that a backup waits until tx has deleted its files.
func BackupRunning(ctx context.Context, tx pgx.Tx) (bool, error) {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock_shared($1)", LockFileDeletes).Scan(&ok); err != nil {
		return false, err
	}
	return !ok, nil
}
