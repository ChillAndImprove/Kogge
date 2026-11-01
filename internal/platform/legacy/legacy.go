// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package legacy maps entities of migrated products to Kogge entities: every imported entity is
// recorded with its source id, so imports can be repeated and old links redirected.
package legacy

import (
	"context"

	"kogge.de/kogge/internal/platform/db"
)

// Lookup returns the Kogge entity for a source entity.
func Lookup(ctx context.Context, q db.Querier, source, sourceType, sourceID string) (int64, bool, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT entity_id FROM legacy_ids WHERE source = $1 AND source_type = $2 AND source_id = $3",
		source, sourceType, sourceID).Scan(&id)
	if db.IsNoRows(err) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Record stores (or updates) the mapping of a source entity.
func Record(ctx context.Context, q db.Querier, source, sourceType, sourceID, entityType string, entityID int64) error {
	_, err := q.Exec(ctx, `INSERT INTO legacy_ids (source, source_type, source_id, entity_type, entity_id) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source, source_type, source_id) DO UPDATE SET entity_type = EXCLUDED.entity_type, entity_id = EXCLUDED.entity_id`,
		source, sourceType, sourceID, entityType, entityID)
	return err
}

// Find returns the Kogge entity of a source id in any source (for redirects of old links).
func Find(ctx context.Context, q db.Querier, sourceType, sourceID string) (int64, bool, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT entity_id FROM legacy_ids WHERE source_type = $1 AND source_id = $2 ORDER BY created_at DESC LIMIT 1",
		sourceType, sourceID).Scan(&id)
	if db.IsNoRows(err) {
		return 0, false, nil
	}
	return id, err == nil, err
}
