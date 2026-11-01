// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// withExtras adds to search results what the columns of the issue navigator show beyond an issue's own row: its
// components and versions, its open sprint and its custom field values as text. It reads in batches, a query per
// kind for all rows.
func withExtras(ctx context.Context, q db.Querier, cfg *configSnapshot, rows []IssueRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int64, len(rows))
	index := make(map[int64]int, len(rows))
	for i := range rows {
		r := &rows[i]
		ids[i], index[r.ID] = r.ID, i
		r.Components, r.FixVersions, r.AffectsVersions, r.Display = []Ref{}, []Ref{}, []Ref{}, map[string]string{}
	}

	refs, err := q.Query(ctx, `SELECT x.issue_id, 'component', c.id, c.name FROM issue_components x JOIN components c ON c.id = x.component_id
		WHERE x.issue_id = ANY($1)
		UNION ALL SELECT x.issue_id, x.kind, v.id, v.name FROM issue_versions x JOIN versions v ON v.id = x.version_id
		WHERE x.issue_id = ANY($1)
		UNION ALL SELECT x.issue_id, 'sprint', s.id, s.name || '|' || s.state FROM issue_sprints x JOIN sprints s ON s.id = x.sprint_id
		WHERE x.issue_id = ANY($1) AND s.state <> 'closed'
		ORDER BY 1, 2, 4`, ids)
	if err != nil {
		return err
	}
	for refs.Next() {
		var issueID int64
		var kind string
		var ref Ref
		if err := refs.Scan(&issueID, &kind, &ref.ID, &ref.Name); err != nil {
			refs.Close()
			return err
		}
		r := &rows[index[issueID]]
		switch kind {
		case "component":
			r.Components = append(r.Components, ref)
		case "fix":
			r.FixVersions = append(r.FixVersions, ref)
		case "affects":
			r.AffectsVersions = append(r.AffectsVersions, ref)
		case "sprint":
			name, state := splitLast(ref.Name, '|')
			r.Sprint = &SprintRef{ID: ref.ID, Name: name, State: state}
		}
	}
	refs.Close()
	if err := refs.Err(); err != nil {
		return err
	}

	// Custom fields: the users, versions and groups they name, then their values as text.
	var userIDs, versionIDs, groupIDs []int64
	for _, r := range rows {
		for key, v := range r.Fields {
			id, ok := parseFieldKey(key)
			if f := cfg.fields[id]; ok && f != nil {
				switch f.Type {
				case "user", "multiuser":
					userIDs = append(userIDs, valueIDs(v)...)
				case "version", "multiversion":
					versionIDs = append(versionIDs, valueIDs(v)...)
				case "group":
					groupIDs = append(groupIDs, valueIDs(v)...)
				}
			}
		}
	}
	users := map[int64]identity.UserRef{}
	if len(userIDs) > 0 {
		if users, err = identity.GetUserRefs(ctx, q, userIDs); err != nil {
			return err
		}
	}
	names := func(sql string, ids []int64) (map[int64]string, error) {
		out := map[int64]string{}
		if len(ids) == 0 {
			return out, nil
		}
		rows, err := q.Query(ctx, sql, ids)
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
	versions, err := names("SELECT id, name FROM versions WHERE id = ANY($1)", versionIDs)
	if err != nil {
		return err
	}
	groups, err := names("SELECT id, name FROM groups WHERE id = ANY($1)", groupIDs)
	if err != nil {
		return err
	}
	userName := func(id int64) string { return users[id].DisplayName }
	for i := range rows {
		r := &rows[i]
		for key, v := range r.Fields {
			id, ok := parseFieldKey(key)
			f := cfg.fields[id]
			if !ok || f == nil || v == nil {
				continue
			}
			if f.Type == "group" {
				if gid, ok := toInt64(v); ok {
					r.Display[key] = groups[gid]
				}
				continue
			}
			if f.Type == "textarea" {
				// Rich text: its beginning.
				raw, _ := json.Marshal(v)
				var d docmodel.Doc
				if json.Unmarshal(raw, &d) == nil {
					r.Display[key] = docmodel.Summary(&d, 200)
				}
				continue
			}
			r.Display[key] = displayCustom(f, v, userName, versions)
		}
	}
	return nil
}

// splitLast splits a text at the last separator.
func splitLast(s string, sep byte) (string, string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == sep {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
