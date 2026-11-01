// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import "context"

// Figures are the module's numbers for the administration's overview.
func (s *Service) Figures(ctx context.Context) (map[string]int64, error) {
	var spaces, pages, edits int64
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM spaces WHERE NOT archived AND type = 'team'),
		(SELECT count(*) FROM pages WHERE status = 'current'), (SELECT count(*) FROM page_versions WHERE created_at > now() - interval '30 days')`).
		Scan(&spaces, &pages, &edits)
	return map[string]int64{"spaces": spaces, "pages": pages, "pageEdits30d": edits}, err
}
