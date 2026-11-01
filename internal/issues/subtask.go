// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// SubtaskInput names an existing issue that becomes a sub-task.
type SubtaskInput struct {
	Issue string `json:"issue" minLength:"1" maxLength:"30" doc:"Key of the issue that becomes a sub-task"`
}

// AddSubtask makes an existing issue a sub-task of the issue key. A standard issue without sub-tasks of its own
// takes the first sub-task type of the project and leaves its epic; a sub-task of another issue changes its parent.
// Like every sub-task it then has the security level of its parent and follows it into its sprint.
func (s *Service) AddSubtask(ctx context.Context, p *identity.Principal, key string, in SubtaskInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	uid := p.UserID()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		parent, cfg, err := s.issueFor(ctx, tx, a, key, false)
		if err != nil {
			return err
		}
		child, _, err := s.issueFor(ctx, tx, a, in.Issue, true)
		if err != nil {
			return err
		}
		if !a.CanIssue(child.head(), PermEdit) {
			return apperr.Forbidden("issue.edit-denied", "You may not edit %s.", child.Key)
		}
		typ := cfg.types[child.typeID]
		switch {
		case child.ID == parent.ID:
			return apperr.Invalid("issue.subtask-self", "An issue cannot be its own sub-task.")
		case cfg.types[parent.typeID].Level != 0:
			return apperr.Invalid("issue.subtask-parent", "Only standard issues have sub-tasks.")
		case typ.Level == 1:
			return apperr.Invalid("issue.subtask-epic", "An epic cannot become a sub-task.")
		case child.projectID != parent.projectID:
			return apperr.Invalidf("issue.subtask-project", "%s belongs to another project; a sub-task belongs to an issue of its own project.", child.Key)
		case typ.Level == -1 && child.parentID == parent.ID:
			return nil
		}

		c := &fieldChange{}
		if typ.Level == 0 {
			subs, err := subtaskIDs(ctx, tx, child.ID)
			if err != nil {
				return err
			}
			if len(subs) > 0 {
				return apperr.Invalidf("issue.subtask-children", "%s has sub-tasks of its own and cannot become a sub-task.", child.Key)
			}
			types, err := s.projectIssueTypes(ctx, tx, child.projectID)
			if err != nil {
				return err
			}
			var sub *IssueType
			for _, pt := range types {
				if t := cfg.types[pt.TypeID]; t != nil && t.Level == -1 {
					sub = t
					break
				}
			}
			if sub == nil {
				return apperr.Invalid("issue.subtask-type", "The project has no issue type for sub-tasks.")
			}
			wf, err := s.workflowFor(ctx, tx, cfg, child.projectID, sub.ID)
			if err != nil {
				return err
			}
			if !wf.Def.HasStatus(child.statusID) {
				return apperr.Invalidf("issue.subtask-status", "%s cannot become a sub-task: the workflow of sub-tasks lacks its status.", child.Key)
			}
			c.set("type_id", sub.ID)
			c.item(FieldIssueType, child.typeID, sub.ID, typ.Name, sub.Name)
		}
		oldKey := ""
		if child.parentID != 0 {
			if err := tx.QueryRow(ctx, "SELECT key FROM issues WHERE id = $1", child.parentID).Scan(&oldKey); err != nil {
				return err
			}
		}
		c.set("parent_id", parent.ID)
		c.item(FieldParent, nullable(child.parentID), parent.ID, oldKey, parent.Key)

		// The parent's security level: who may not set levels cannot change who sees the issue this way either.
		if child.securityLevelID != parent.securityLevelID {
			if !a.CanIssue(child.head(), PermSetSecurity) {
				return apperr.Forbidden("issue.field-denied", "You may not change %s of %s.", FieldSecurity, child.Key)
			}
			var level *int64
			if parent.securityLevelID != 0 {
				level = &parent.securityLevelID
			}
			oldName, name, err := levelNames(ctx, tx, child.securityLevelID, level)
			if err != nil {
				return err
			}
			c.set("security_level_id", optional(level))
			c.item(FieldSecurity, nullable(child.securityLevelID), optional(level), oldName, name)
		}

		// The parent's sprint: taking the issue out of its own sprint is planning, as in an edit of the sprint field.
		from, err := openSprintOf(ctx, tx, child.ID)
		if err != nil {
			return err
		}
		to, err := openSprintOf(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		var target *Sprint
		if to != nil {
			if target, err = sprintByID(ctx, tx, to.ID, false); err != nil {
				return err
			}
		}
		if from != nil && (to == nil || to.ID != from.ID) {
			sp, err := sprintByID(ctx, tx, from.ID, false)
			if err != nil {
				return err
			}
			if !a.Can(sp.projectID, PermSchedule) {
				return apperr.Forbidden("sprint.schedule-denied", "You may not take %s out of its sprint.", child.Key)
			}
		}

		if err := c.save(ctx, tx, child, uid, true); err != nil {
			return err
		}
		moved, err := moveToSprint(ctx, tx, child.ID, target, uid)
		if err != nil {
			return err
		}
		if err := s.refreshSearch(ctx, tx, child.ID); err != nil {
			return err
		}
		out, err := s.loadIssue(ctx, tx, cfg, child.ID, false)
		if err != nil {
			return err
		}
		summary, names := changeSummary(c.items), c.fieldNames()
		if moved {
			names = append(names, FieldSprint)
		}
		if err := s.notifyChange(ctx, tx, nil, out, EventUpdated, uid, "updated", summary, nil); err != nil {
			return err
		}
		if err := s.recordActivity(ctx, tx, out, uid, "issue.updated", &activityData{Fields: changedFields(cfg, names)}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.updated", IssueID: out.ID, Key: out.Key, ProjectID: out.projectID,
			ActorID: uid, Fields: names})
	})
}
