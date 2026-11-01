// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import "context"

// Figures are the module's numbers for the administration's overview.
func (s *Service) Figures(ctx context.Context) (map[string]int64, error) {
	var runs, failed, online, awaiting, waiting int64
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runs WHERE created_at > now() - interval '7 days'),
		(SELECT count(*) FROM runs WHERE created_at > now() - interval '7 days' AND state IN ('failed', 'error')),
		(SELECT count(*) FROM agents WHERE enabled AND approved_at IS NOT NULL AND last_seen_at > now() - interval '1 minute'),
		(SELECT count(*) FROM agents WHERE enabled AND approved_at IS NULL),
		(SELECT count(*) FROM run_jobs WHERE state = 'pending')`).Scan(&runs, &failed, &online, &awaiting, &waiting)
	return map[string]int64{"runs7d": runs, "failedRuns7d": failed, "agentsOnline": online, "agentsAwaitingApproval": awaiting,
		"jobsWaiting": waiting}, err
}
