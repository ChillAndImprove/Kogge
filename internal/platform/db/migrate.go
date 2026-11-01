// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"kogge.de/kogge/internal/migrations"
)

// migrationLock is the advisory lock key that serialises concurrent migration runs ("kogge-migrate").
const migrationLock int64 = 0x6b6f67676501

// Versions are the schema versions of Kogge's migrations and of River's (the job queue).
type Versions struct {
	Schema int64 `json:"schema"`
	Jobs   int   `json:"jobs"`
}

// Migrate applies River's and Kogge's migrations. Concurrent calls wait for each other.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return MigrateTo(ctx, pool, logger, Versions{})
}

// MigrateTo applies the migrations up to the given versions; zero means all. A restore uses it to recreate the
// schema of a backup before loading its data, then migrates the rest.
func MigrateTo(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, to Versions) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock)
	}()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: logger})
	if err != nil {
		return fmt.Errorf("job queue migrations: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{TargetVersion: to.Jobs})
	if err != nil {
		return fmt.Errorf("job queue migrations: %w", err)
	}
	for _, v := range res.Versions {
		logger.Info("applied job queue migration", "version", v.Version)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	var results []*goose.MigrationResult
	if to.Schema > 0 {
		results, err = p.UpTo(ctx, to.Schema)
	} else {
		results, err = p.Up(ctx)
	}
	for _, r := range results {
		logger.Info("applied migration", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	return nil
}

func provider(sqlDB *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithDisableGlobalRegistry(true), goose.WithTableName("kogge_schema_version"))
}

// CurrentVersions returns the versions the database is migrated to.
func CurrentVersions(ctx context.Context, pool *pgxpool.Pool) (Versions, error) {
	var v Versions
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return v, err
	}
	existing, err := migrator.ExistingVersions(ctx)
	if err != nil {
		return v, err
	}
	for _, m := range existing {
		v.Jobs = max(v.Jobs, m.Version)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return v, err
	}
	v.Schema, err = p.GetDBVersion(ctx)
	return v, err
}

// LatestVersions returns the versions this build migrates to.
func LatestVersions(pool *pgxpool.Pool) (Versions, error) {
	var v Versions
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return v, err
	}
	for _, m := range migrator.AllVersions() {
		v.Jobs = max(v.Jobs, m.Version)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return v, err
	}
	for _, s := range p.ListSources() {
		v.Schema = max(v.Schema, s.Version)
	}
	return v, nil
}

// PendingMigrations reports whether migrations are missing (readiness check).
func PendingMigrations(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return false, err
	}
	return p.HasPending(ctx)
}
