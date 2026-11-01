// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import "context"

// Figures are the module's numbers for the administration's overview.
func (s *Service) Figures(ctx context.Context) (map[string]int64, error) {
	var projects, repos, bytes, open int64
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM code_projects), (SELECT count(*) FROM repositories WHERE NOT archived),
		(SELECT coalesce(sum(size_bytes), 0) FROM repositories), (SELECT count(*) FROM pull_requests WHERE state = 'open')`).
		Scan(&projects, &repos, &bytes, &open)
	return map[string]int64{"codeProjects": projects, "repositories": repos, "repositoryBytes": bytes, "openPullRequests": open}, err
}
