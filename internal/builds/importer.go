// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// ImportEnvironment ensures an environment of a repository for a migration. New environments have
// no protections, which are decided after the migration; existing ones are kept as they are.
func (s *Service) ImportEnvironment(ctx context.Context, repoID int64, name, description string) (int64, error) {
	if !envNamePattern.MatchString(name) {
		return 0, fmt.Errorf("%q is not a valid environment name", name)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO environments (repository_id, name, description) VALUES ($1, $2, $3)
		ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, repoID, name, description).Scan(&id)
	return id, err
}

// ImportVariable sets a plain variable of a scope for a migration. Secrets and variables people changed in Kogge
// are kept; it reports whether the variable has the migrated value.
func (s *Service) ImportVariable(ctx context.Context, scope string, scopeID int64, name, value string) (bool, error) {
	if !varNamePattern.MatchString(name) || strings.HasPrefix(name, "KOGGE_") || name == "CI" {
		return false, fmt.Errorf("%q is not a valid variable name", name)
	}
	set := false
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var old *string
		err := tx.QueryRow(ctx, `INSERT INTO build_variables (scope, scope_id, name, value) VALUES ($1, $2, $3, $4)
			ON CONFLICT (scope, scope_id, name) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
			WHERE build_variables.updated_by IS NULL AND NOT build_variables.secret
			RETURNING (SELECT value FROM build_variables WHERE scope = $1 AND scope_id = $2 AND name = $3)`, scope, scopeID, name, value).Scan(&old)
		if db.IsNoRows(err) {
			return nil // kept
		}
		if err != nil {
			return err
		}
		set = true
		if old != nil && *old == value {
			return nil
		}
		return audit.Record(ctx, tx, audit.Entry{Action: "build.variable.set", TargetType: scope, TargetID: itoa(scopeID),
			Details: map[string]any{"name": name, "secret": false, "migration": true}})
	})
	return set, err
}
