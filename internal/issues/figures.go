// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import "context"

// Figures are the module's numbers for the administration's overview.
func (s *Service) Figures(ctx context.Context) (map[string]int64, error) {
	var projects, issues, created, sprints int64
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM issue_projects WHERE NOT archived), (SELECT count(*) FROM issues),
		(SELECT count(*) FROM issues WHERE created_at > now() - interval '30 days'), (SELECT count(*) FROM sprints WHERE state = 'active')`).
		Scan(&projects, &issues, &created, &sprints)
	return map[string]int64{"issueProjects": projects, "issues": issues, "issuesCreated30d": created, "activeSprints": sprints}, err
}
