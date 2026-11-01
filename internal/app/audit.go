// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// digestAudit writes the digests of all finished days that have none yet, oldest first.
func digestAudit(ctx context.Context, a *App) error {
	var first *time.Time
	if err := a.Pool.QueryRow(ctx, `SELECT min(at) FROM audit_events
		WHERE at >= coalesce((SELECT max(day) + 1 FROM audit_digests), '-infinity'::date)`).Scan(&first); err != nil {
		return err
	}
	if first == nil {
		return nil
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for day := first.UTC().Truncate(24 * time.Hour); day.Before(today); day = day.Add(24 * time.Hour) {
		if err := db.InTx(ctx, a.Pool, func(tx pgx.Tx) error { return audit.Digest(ctx, tx, day) }); err != nil {
			return err
		}
	}
	return nil
}
