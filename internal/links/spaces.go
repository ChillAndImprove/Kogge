// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/wiki"
)

// The wiki space of an issue project: sprints create retrospectives and versions release notes there.

// ProjectSpace is the space linked to a project; Space is nil when there is none or the caller may not see it.
type ProjectSpace struct {
	Space *wiki.SpaceRef `json:"space,omitempty"`
}

// LinkedSpace returns the space of a project (readers of the project).
func (s *Service) LinkedSpace(ctx context.Context, p *identity.Principal, key string) (*ProjectSpace, error) {
	proj, err := s.issues.GetProject(ctx, p, key)
	if err != nil {
		return nil, err
	}
	var spaceKey string
	err = s.pool.QueryRow(ctx, "SELECT sp.key FROM project_spaces ps JOIN spaces sp ON sp.id = ps.space_id WHERE ps.project_id = $1", proj.ID).
		Scan(&spaceKey)
	if db.IsNoRows(err) {
		return &ProjectSpace{}, nil
	}
	if err != nil {
		return nil, err
	}
	sp, err := s.wiki.GetSpace(ctx, p, spaceKey)
	if err != nil {
		return &ProjectSpace{}, nil // not visible to the caller
	}
	return &ProjectSpace{Space: &wiki.SpaceRef{ID: sp.ID, Key: sp.Key, Name: sp.Name}}, nil
}

// SetLinkedSpace links a space to a project (project administrators who can see the space); an empty key unlinks.
func (s *Service) SetLinkedSpace(ctx context.Context, p *identity.Principal, key, spaceKey string) (*ProjectSpace, error) {
	projectID, err := s.issues.AdminProjectID(ctx, p, key)
	if err != nil {
		return nil, err
	}
	spaceKey = strings.TrimSpace(spaceKey)
	// The link and its record commit together.
	record := func(tx pgx.Tx, space string) error {
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "project.space", TargetType: "project", TargetID: key,
			Details: map[string]any{"space": space}})
	}
	if spaceKey == "" {
		return &ProjectSpace{}, db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "DELETE FROM project_spaces WHERE project_id = $1", projectID); err != nil {
				return err
			}
			return record(tx, "")
		})
	}
	sp, err := s.wiki.GetSpace(ctx, p, spaceKey)
	if err != nil {
		return nil, apperr.Invalidf("project.space", "The space %s does not exist or you may not see it.", spaceKey)
	}
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO project_spaces (project_id, space_id) VALUES ($1, $2)
			ON CONFLICT (project_id) DO UPDATE SET space_id = EXCLUDED.space_id`, projectID, sp.ID); err != nil {
			return err
		}
		return record(tx, sp.Key)
	}); err != nil {
		return nil, err
	}
	return &ProjectSpace{Space: &wiki.SpaceRef{ID: sp.ID, Key: sp.Key, Name: sp.Name}}, nil
}
