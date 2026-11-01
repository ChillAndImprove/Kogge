// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"time"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Information for integrations such as webhooks; these functions do not check permissions.

// IssueInfo describes an issue.
type IssueInfo struct {
	ID          int64             `json:"id"`
	Key         string            `json:"key"`
	Summary     string            `json:"summary"`
	ProjectKey  string            `json:"projectKey"`
	ProjectName string            `json:"projectName"`
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Priority    string            `json:"priority,omitempty"`
	Resolution  string            `json:"resolution,omitempty"`
	Assignee    *identity.UserRef `json:"assignee,omitempty"`
	Reporter    *identity.UserRef `json:"reporter,omitempty"`
	Labels      []string          `json:"labels"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated"`
	Path        string            `json:"-"`
	// Secured issues have a security level: only some of those who browse the project see them.
	Secured bool `json:"-"`
}

// IssueInfo returns an issue by id.
func (s *Service) IssueInfo(ctx context.Context, id int64) (*IssueInfo, error) {
	var i IssueInfo
	var assignee, reporter int64
	err := s.pool.QueryRow(ctx, `SELECT i.id, i.key, i.summary, p.key, p.name, t.name, st.name, coalesce(pr.name, ''), coalesce(r.name, ''),
		coalesce(i.assignee_id, 0), coalesce(i.reporter_id, 0), i.labels, i.created_at, i.updated_at, i.security_level_id IS NOT NULL
		FROM issues i JOIN issue_projects p ON p.id = i.project_id JOIN issue_types t ON t.id = i.type_id JOIN statuses st ON st.id = i.status_id
		LEFT JOIN priorities pr ON pr.id = i.priority_id LEFT JOIN resolutions r ON r.id = i.resolution_id WHERE i.id = $1`, id).
		Scan(&i.ID, &i.Key, &i.Summary, &i.ProjectKey, &i.ProjectName, &i.Type, &i.Status, &i.Priority, &i.Resolution, &assignee, &reporter,
			&i.Labels, &i.Created, &i.Updated, &i.Secured)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("issue.not-found", "The issue does not exist.")
	}
	if err != nil {
		return nil, err
	}
	users, err := identity.GetUserRefs(ctx, s.pool, []int64{assignee, reporter})
	if err != nil {
		return nil, err
	}
	if u, ok := users[assignee]; ok {
		i.Assignee = &u
	}
	if u, ok := users[reporter]; ok {
		i.Reporter = &u
	}
	if i.Labels == nil {
		i.Labels = []string{}
	}
	i.Path = "/browse/" + i.Key
	return &i, nil
}

// UserCanSeeIssue reports whether a user (active, with their groups) sees an issue, e.g. the owner of a webhook.
func (s *Service) UserCanSeeIssue(ctx context.Context, userID int64, key string) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	p, err := identity.LoadPrincipal(ctx, s.pool, userID, identity.MethodInternal)
	if err != nil {
		if apperr.Is(err, "auth.user-inactive") || db.IsNoRows(err) || apperr.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return false, err
	}
	if _, _, err := s.issueFor(ctx, s.pool, a, key, false); err != nil {
		if apperr.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// StatusNames returns the names of statuses by id.
func (s *Service) StatusNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	rows, err := s.pool.Query(ctx, "SELECT id, name FROM statuses WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// AdminProjectID returns the id of a project the principal administers.
func (s *Service) AdminProjectID(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	proj, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return 0, err
	}
	if err := s.requireProjectAdmin(ctx, p, proj); err != nil {
		return 0, err
	}
	return proj.ID, nil
}

// ProjectKeyOf returns the key of a project.
func (s *Service) ProjectKeyOf(ctx context.Context, id int64) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, "SELECT key FROM issue_projects WHERE id = $1", id).Scan(&key)
	return key, err
}

// NextVersion returns the first unreleased, unarchived version of a project (0 if there is none).
func (s *Service) NextVersion(ctx context.Context, projectID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM versions WHERE project_id = $1 AND NOT released AND NOT archived
		ORDER BY position, id LIMIT 1`, projectID).Scan(&id)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

// VersionIssueKeys returns the keys of the issues the principal may see that have the version as fix version, for
// those who may see the development of the project: every key of such an issue (also the keys it had before it
// moved, by which commits may name it) with its current key.
func (s *Service) VersionIssueKeys(ctx context.Context, p *identity.Principal, projectKey string, versionID int64) (map[string]string, error) {
	proj, err := projectByKey(ctx, s.pool, projectKey)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if !a.CanAny(proj.ID, PermBrowse) {
		return nil, apperr.NotFound("project.not-found", "Project %s does not exist or you may not see it.", projectKey)
	}
	if !a.CanAny(proj.ID, PermViewDevelopment) {
		return nil, apperr.Forbidden("development.denied", "You may not see the development of project %s.", proj.Key)
	}
	var args db.Args
	where := "iv.version_id = " + args.Add(versionID) + " AND iv.kind = 'fix' AND i.project_id = " + args.Add(proj.ID) + " AND " + a.VisibleSQL("i", &args)
	rows, err := s.pool.Query(ctx, `SELECT k.key, k.current FROM (SELECT i.id, i.key FROM issues i JOIN issue_versions iv ON iv.issue_id = i.id
		WHERE `+where+` ORDER BY i.id LIMIT 2000) v CROSS JOIN LATERAL (SELECT v.key, v.key AS current
		UNION ALL SELECT f.key, v.key FROM issue_keys f WHERE f.issue_id = v.id) k`, args...)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var k, current string
		if err := rows.Scan(&k, &current); err != nil {
			rows.Close()
			return nil, err
		}
		out[k] = current
	}
	rows.Close()
	return out, rows.Err()
}

// VisibleIssueKeys reports which issues, by current or former key, a principal sees (for re-checking
// notifications).
func (s *Service) VisibleIssueKeys(ctx context.Context, p *identity.Principal, keys []string) (map[string]bool, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT key, id FROM issues WHERE key = ANY($1)
		UNION ALL SELECT key, issue_id FROM issue_keys WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	byKey := map[string]int64{}
	var ids []int64
	for rows.Next() {
		var k string
		var id int64
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return nil, err
		}
		byKey[k] = id
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	refs, err := s.issueRefs(ctx, s.pool, cfg, a, "i.id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	for _, r := range refs {
		visible[r.ID] = true
	}
	for k, id := range byKey {
		out[k] = visible[id]
	}
	return out, nil
}
