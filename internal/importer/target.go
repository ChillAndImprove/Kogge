// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"kogge.de/kogge/internal/platform/db"
)

// What the migration reads of the target installation besides the modules' services: the places behind ids it
// recorded (for redirects), and what exists already or was loaded (for the analysis and the validation). SQL only;
// permissions are checked by the callers through the modules' services.

// repositoryPath returns the project key and slug of a repository.
func repositoryPath(ctx context.Context, q db.Querier, id int64) (key, slug string, err error) {
	err = q.QueryRow(ctx, "SELECT p.key, r.slug FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE r.id = $1", id).
		Scan(&key, &slug)
	return key, slug, err
}

// environmentRepository returns the repository of a pipeline environment.
func environmentRepository(ctx context.Context, q db.Querier, id int64) (int64, error) {
	var repo int64
	err := q.QueryRow(ctx, "SELECT repository_id FROM environments WHERE id = $1", id).Scan(&repo)
	return repo, err
}

// codeProjectKey returns the key of a code project.
func codeProjectKey(ctx context.Context, q db.Querier, id int64) (string, error) {
	var key string
	err := q.QueryRow(ctx, "SELECT key FROM code_projects WHERE id = $1", id).Scan(&key)
	return key, err
}

// codeProjectID returns the id of a code project by key (0 if there is none).
func codeProjectID(ctx context.Context, q db.Querier, key string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT id FROM code_projects WHERE upper(key) = upper($1)", key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

// repositoryID returns the id of a repository of a code project by slug (0 if there is none).
func repositoryID(ctx context.Context, q db.Querier, projectID int64, slug string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT id FROM repositories WHERE project_id = $1 AND lower(slug) = lower($2)", projectID, slug).Scan(&id)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

// configTables are the configuration of issues whose entries the migration matches by name.
var configTables = map[string]string{"issue-type": "issue_types", "status": "statuses", "priority": "priorities",
	"resolution": "resolutions", "link-type": "issue_link_types"}

// configNames returns the lower-case names of a configuration of issues (see configTables).
func configNames(ctx context.Context, q db.Querier, kind string) (map[string]bool, error) {
	table, ok := configTables[kind]
	if !ok {
		return nil, nil
	}
	return names(ctx, q, "SELECT lower(name) FROM "+table)
}

// customFieldNames returns the custom fields by lower-case name and type ("name\x00type").
func customFieldNames(ctx context.Context, q db.Querier) (map[string]bool, error) {
	rows, err := q.Query(ctx, "SELECT lower(name), type FROM custom_fields")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out[name+"\x00"+typ] = true
	}
	return out, rows.Err()
}

func names(ctx context.Context, q db.Querier, sql string) (map[string]bool, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// issueProjectID returns the project of issues that has a key, as its key or as an earlier one (0 if none).
func issueProjectID(ctx context.Context, q db.Querier, key string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM issue_projects WHERE upper(key) = upper($1)
		UNION ALL SELECT project_id FROM issue_project_keys WHERE upper(key) = upper($1) LIMIT 1`, key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

// spaceID returns the space with a key (0 if none).
func spaceID(ctx context.Context, q db.Querier, key string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT id FROM spaces WHERE upper(key) = upper($1)", key).Scan(&id)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

// personalSpaceOf returns the key of the personal space of a user ("" if none).
func personalSpaceOf(ctx context.Context, q db.Querier, userID int64) (string, error) {
	var key string
	err := q.QueryRow(ctx, "SELECT key FROM spaces WHERE type = 'personal' AND owner_id = $1", userID).Scan(&key)
	if db.IsNoRows(err) {
		return "", nil
	}
	return key, err
}

// entityTables are the tables of the entities a load records in legacy_ids, by entity type.
var entityTables = map[string]string{"project": "issue_projects", "issue": "issues", "issue-comment": "issue_comments",
	"worklog": "worklogs", "issue-attachment": "issue_attachments", "sprint": "sprints", "board": "boards", "version": "versions",
	"component": "components", "issue-change": "issue_changes", "space": "spaces", "page": "pages", "page-comment": "page_comments",
	"page-attachment": "page_attachments", "code-project": "code_projects", "repository": "repositories", "pull-request": "pull_requests",
	"pull-request-comment": "pull_request_comments", "branch-permission": "branch_permissions", "environment": "environments"}

// importedIDs returns the entities of a type that a load of the source recorded and that exist, by source id.
func importedIDs(ctx context.Context, q db.Querier, source, sourceType, entityType string) (map[string]int64, error) {
	table, ok := entityTables[entityType]
	if !ok {
		return nil, fmt.Errorf("no table for entities of the type %s", entityType)
	}
	rows, err := q.Query(ctx, "SELECT l.source_id, l.entity_id FROM legacy_ids l JOIN "+table+` e ON e.id = l.entity_id
		WHERE l.source = $1 AND l.source_type = $2 AND l.entity_type = $3`, source, sourceType, entityType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var src string
		var id int64
		if err := rows.Scan(&src, &id); err != nil {
			return nil, err
		}
		out[src] = id
	}
	return out, rows.Err()
}

// importedIssueKeys returns the keys of the issues a load of the source recorded, by source id.
func importedIssueKeys(ctx context.Context, q db.Querier, source string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT l.source_id, e.key FROM legacy_ids l JOIN issues e ON e.id = l.entity_id
		WHERE l.source = $1 AND l.source_type = 'issue' AND l.entity_type = 'issue'`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var src, key string
		if err := rows.Scan(&src, &key); err != nil {
			return nil, err
		}
		out[src] = key
	}
	return out, rows.Err()
}

// storedFile is what Kogge stores of an attachment.
type storedFile struct {
	id     int64
	sha256 string
	size   int64
}

// importedFiles returns the attachments a load of the source stored (entity type issue-attachment or
// page-attachment), by source id.
func importedFiles(ctx context.Context, q db.Querier, source, entityType string) (map[string]storedFile, error) {
	table := map[string]string{"issue-attachment": "issue_attachments", "page-attachment": "page_attachments"}[entityType]
	if table == "" {
		return nil, fmt.Errorf("no attachments of the type %s", entityType)
	}
	rows, err := q.Query(ctx, "SELECT l.source_id, e.id, e.sha256, e.size FROM legacy_ids l JOIN "+table+` e ON e.id = l.entity_id
		WHERE l.source = $1 AND l.source_type = 'attachment' AND l.entity_type = $2`, source, entityType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedFile{}
	for rows.Next() {
		var src string
		var f storedFile
		if err := rows.Scan(&src, &f.id, &f.sha256, &f.size); err != nil {
			return nil, err
		}
		out[src] = f
	}
	return out, rows.Err()
}

// docQueries read the documents of the entities a load of the source recorded: a label and the document.
var docQueries = map[string]string{
	"issue": `SELECT e.key, e.description FROM legacy_ids l JOIN issues e ON e.id = l.entity_id
		WHERE l.source = $1 AND l.source_type = 'issue' AND l.entity_type = 'issue' AND e.description IS NOT NULL`,
	"issue-comment": `SELECT i.key, c.body FROM legacy_ids l JOIN issue_comments c ON c.id = l.entity_id JOIN issues i ON i.id = c.issue_id
		WHERE l.source = $1 AND l.source_type = 'comment' AND l.entity_type = 'issue-comment'`,
	"page": `SELECT s.key || ': ' || p.title, p.body FROM legacy_ids l JOIN pages p ON p.id = l.entity_id JOIN spaces s ON s.id = p.space_id
		WHERE l.source = $1 AND l.source_type = 'page' AND l.entity_type = 'page'`,
	"page-comment": `SELECT s.key || ': ' || p.title, c.body FROM legacy_ids l JOIN page_comments c ON c.id = l.entity_id
		JOIN pages p ON p.id = c.page_id JOIN spaces s ON s.id = p.space_id
		WHERE l.source = $1 AND l.source_type = 'comment' AND l.entity_type = 'page-comment'`,
	"pull-request": `SELECT cp.key || '/' || r.slug || '#' || pr.number, pr.description FROM legacy_ids l
		JOIN pull_requests pr ON pr.id = l.entity_id JOIN repositories r ON r.id = pr.repository_id JOIN code_projects cp ON cp.id = r.project_id
		WHERE l.source = $1 AND l.source_type = 'pull-request' AND l.entity_type = 'pull-request'`,
	"pull-request-comment": `SELECT cp.key || '/' || r.slug || '#' || pr.number, c.body FROM legacy_ids l
		JOIN pull_request_comments c ON c.id = l.entity_id JOIN pull_requests pr ON pr.id = c.pull_request_id
		JOIN repositories r ON r.id = pr.repository_id JOIN code_projects cp ON cp.id = r.project_id
		WHERE l.source = $1 AND l.source_type = 'pull-request-comment' AND l.entity_type = 'pull-request-comment'`,
}

// eachImportedDoc calls fn with the documents of the entities of a kind (see docQueries) a load of the source
// recorded, one at a time.
func eachImportedDoc(ctx context.Context, q db.Querier, source, kind string, fn func(label string, raw []byte) error) error {
	sql, ok := docQueries[kind]
	if !ok {
		return fmt.Errorf("no documents of the kind %s", kind)
	}
	rows, err := q.Query(ctx, sql, source)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var raw []byte
		if err := rows.Scan(&label, &raw); err != nil {
			return err
		}
		if err := fn(label, raw); err != nil {
			return err
		}
	}
	return rows.Err()
}

// existingIDs returns which of the ids exist in a table of entities (see entityTables) or of users.
func existingIDs(ctx context.Context, q db.Querier, table string, ids []int64) (map[int64]bool, error) {
	if !slices.Contains(slices.Collect(maps.Values(entityTables)), table) && table != "users" {
		return nil, fmt.Errorf("no table %s", table)
	}
	out := map[int64]bool{}
	for start := 0; start < len(ids); start += 10000 {
		rows, err := q.Query(ctx, "SELECT id FROM "+table+" WHERE id = ANY($1)", ids[start:min(start+10000, len(ids))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// existingIssueKeys returns which of the issue keys exist.
func existingIssueKeys(ctx context.Context, q db.Querier, keys []string) (map[string]bool, error) {
	return existingStrings(ctx, q, "SELECT key FROM issues WHERE key = ANY($1)", keys)
}

// existingRepositories returns which repositories ("key/slug", lower case) exist.
func existingRepositories(ctx context.Context, q db.Querier, paths []string) (map[string]bool, error) {
	return existingStrings(ctx, q, `SELECT lower(p.key) || '/' || lower(r.slug) FROM repositories r JOIN code_projects p ON p.id = r.project_id
		WHERE lower(p.key) || '/' || lower(r.slug) = ANY($1)`, paths)
}

func existingStrings(ctx context.Context, q db.Querier, sql string, values []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(values); start += 10000 {
		rows, err := q.Query(ctx, sql, values[start:min(start+10000, len(values))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return nil, err
			}
			out[s] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pageSpaces returns the space keys of pages.
func pageSpaces(ctx context.Context, q db.Querier, ids []int64) (map[int64]string, error) {
	rows, err := q.Query(ctx, "SELECT p.id, s.key FROM pages p JOIN spaces s ON s.id = p.space_id WHERE p.id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, rows.Err()
}

// variableNames returns the names of the pipeline variables of a scope (instance, repository or environment).
func variableNames(ctx context.Context, q db.Querier, scope string, scopeID int64) (map[string]bool, error) {
	rows, err := q.Query(ctx, "SELECT name FROM build_variables WHERE scope = $1 AND scope_id = $2", scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// pullRequestState returns the number and state of a pull request.
func pullRequestState(ctx context.Context, q db.Querier, id int64) (int, string, error) {
	var number int
	var state string
	err := q.QueryRow(ctx, "SELECT number, state FROM pull_requests WHERE id = $1", id).Scan(&number, &state)
	return number, state, err
}

// lfsObjectsOf returns the LFS objects of a repository.
func lfsObjectsOf(ctx context.Context, q db.Querier, repoID int64) (map[string]bool, error) {
	rows, err := q.Query(ctx, "SELECT oid FROM lfs_objects WHERE repository_id = $1", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			return nil, err
		}
		out[oid] = true
	}
	return out, rows.Err()
}

// isAdministrator reports whether a user has Kogge's administrator permission (directly or by a group).
func isAdministrator(ctx context.Context, q db.Querier, userID int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM global_grants g WHERE g.permission = 'admin' AND (
		(g.principal_type = 'user' AND g.principal_id = $1) OR
		(g.principal_type = 'group' AND g.principal_id IN (SELECT group_id FROM group_members WHERE user_id = $1))))`, userID).Scan(&ok)
	return ok, err
}

// issueFacts is what a validation compares of an issue.
type issueFacts struct {
	key                         string
	reporter, creator, assignee string
	created                     time.Time
	comments, changes, worklogs int
	commentAuthors              []string
	description                 []byte
}

func issueFactsOf(ctx context.Context, q db.Querier, id int64) (*issueFacts, error) {
	f := &issueFacts{}
	err := q.QueryRow(ctx, `SELECT i.key, coalesce(r.username, ''), coalesce(c.username, ''), coalesce(a.username, ''), i.created_at,
		(SELECT count(*) FROM issue_comments WHERE issue_id = i.id), (SELECT count(*) FROM issue_changes WHERE issue_id = i.id),
		(SELECT count(*) FROM worklogs WHERE issue_id = i.id), coalesce((SELECT array_agg(coalesce(u.username, '') ORDER BY ic.created_at, ic.id)
			FROM issue_comments ic LEFT JOIN users u ON u.id = ic.author_id WHERE ic.issue_id = i.id), '{}'), coalesce(i.description, 'null')
		FROM issues i LEFT JOIN users r ON r.id = i.reporter_id LEFT JOIN users c ON c.id = i.creator_id LEFT JOIN users a ON a.id = i.assignee_id
		WHERE i.id = $1`, id).Scan(&f.key, &f.reporter, &f.creator, &f.assignee, &f.created, &f.comments, &f.changes, &f.worklogs,
		&f.commentAuthors, &f.description)
	return f, err
}

// pageFacts is what a validation compares of a page.
type pageFacts struct {
	space, title       string
	creator, updatedBy string
	created            time.Time
	version, versions  int
	comments           int
	body               []byte
}

func pageFactsOf(ctx context.Context, q db.Querier, id int64) (*pageFacts, error) {
	f := &pageFacts{}
	err := q.QueryRow(ctx, `SELECT s.key, p.title, coalesce(c.username, ''), coalesce(u.username, ''), p.created_at, p.version,
		(SELECT count(*) FROM page_versions WHERE page_id = p.id), (SELECT count(*) FROM page_comments WHERE page_id = p.id), p.body
		FROM pages p JOIN spaces s ON s.id = p.space_id LEFT JOIN users c ON c.id = p.creator_id LEFT JOIN users u ON u.id = p.updated_by
		WHERE p.id = $1`, id).Scan(&f.space, &f.title, &f.creator, &f.updatedBy, &f.created, &f.version, &f.versions, &f.comments, &f.body)
	return f, err
}

// pullRequestFacts is what a validation compares of a pull request.
type pullRequestFacts struct {
	number   int
	author   string
	state    string
	created  time.Time
	comments int
}

func pullRequestFactsOf(ctx context.Context, q db.Querier, id int64) (*pullRequestFacts, error) {
	f := &pullRequestFacts{}
	err := q.QueryRow(ctx, `SELECT pr.number, coalesce(u.username, ''), pr.state, pr.created_at,
		(SELECT count(*) FROM pull_request_comments WHERE pull_request_id = pr.id)
		FROM pull_requests pr LEFT JOIN users u ON u.id = pr.author_id WHERE pr.id = $1`, id).
		Scan(&f.number, &f.author, &f.state, &f.created, &f.comments)
	return f, err
}
