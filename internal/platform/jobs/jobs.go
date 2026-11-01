// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package jobs wraps River, the PostgreSQL job queue. Modules register workers and periodic jobs;
// services enqueue jobs inside their transactions with InsertTx.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"kogge.de/kogge/internal/platform/metrics"
)

// Queues by kind of work, so slow imports never block notifications.
const (
	QueueDefault = river.QueueDefault
	QueueEvents  = "events"
	QueueMail    = "mail"
	QueueGit     = "git"
	QueueImport  = "import"
	QueueBuilds  = "builds"
	QueueExport  = "export"
)

// Registry collects workers and periodic jobs from all modules before the client starts.
type Registry struct {
	Workers  *river.Workers
	Periodic []*river.PeriodicJob
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{Workers: river.NewWorkers()} }

// Every registers a periodic job that runs on the leader at the interval.
func (r *Registry) Every(interval time.Duration, args river.JobArgs, opts *river.InsertOpts) {
	r.Periodic = append(r.Periodic, river.NewPeriodicJob(river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) { return args, opts }, &river.PeriodicJobOpts{ID: args.Kind()}))
}

// Client is the River client. Processes without the worker role use it to insert jobs only.
type Client = river.Client[pgx.Tx]

// NewClient creates the client. With work set, it also executes jobs; configure adjusts the configuration (tests
// fetch jobs more often).
func NewClient(pool *pgxpool.Pool, reg *Registry, work bool, logger *slog.Logger, configure ...func(*river.Config)) (*Client, error) {
	cfg := &river.Config{
		Logger:                      logger,
		CompletedJobRetentionPeriod: 24 * time.Hour,
		CancelledJobRetentionPeriod: 7 * 24 * time.Hour,
		DiscardedJobRetentionPeriod: 30 * 24 * time.Hour,
		MaxAttempts:                 10,
		JobTimeout:                  5 * time.Minute,
		Workers:                     reg.Workers,
		Middleware:                  []rivertype.Middleware{metrics.Jobs()},
	}
	if work {
		cfg.Queues = map[string]river.QueueConfig{
			QueueDefault: {MaxWorkers: 20},
			QueueEvents:  {MaxWorkers: 20},
			QueueMail:    {MaxWorkers: 5},
			QueueGit:     {MaxWorkers: 10},
			QueueImport:  {MaxWorkers: 2},
			QueueBuilds:  {MaxWorkers: 10},
			QueueExport:  {MaxWorkers: 2},
		}
		cfg.PeriodicJobs = reg.Periodic
	}
	for _, c := range configure {
		c(cfg)
	}
	return river.NewClient(riverpgxv5.New(pool), cfg)
}

// Start starts job execution if the client works.
func Start(ctx context.Context, c *Client) error { return c.Start(ctx) }
