// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
)

// AvailableTransition is a transition the principal can run now.
type AvailableTransition struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	To     *Status  `json:"to"`
	Screen []string `json:"screen" doc:"Fields asked for when running the transition"`
	// CommentRequired tells the client to ask for a comment.
	CommentRequired bool `json:"commentRequired"`
}

// conditionsMet evaluates a transition's conditions for the principal.
func (s *Service) conditionsMet(ctx context.Context, q db.Querier, a *Access, p *identity.Principal, i *Issue, t workflow.Transition) (bool, error) {
	h := i.head()
	if !a.CanIssue(h, PermTransition) {
		return false, nil
	}
	for _, c := range t.Conditions {
		switch c.Type {
		case workflow.CondPermission:
			if !a.CanIssue(h, c.Permission) {
				return false, nil
			}
		case workflow.CondAssigneeOnly:
			if i.assigneeID == 0 || i.assigneeID != p.UserID() {
				return false, nil
			}
		case workflow.CondReporterOnly:
			if i.reporterID == 0 || i.reporterID != p.UserID() {
				return false, nil
			}
		case workflow.CondGroup:
			if !p.InGroup(c.Group) {
				return false, nil
			}
		case workflow.CondRole:
			ok, err := s.commentVisible(ctx, q, p, i.projectID, "role", c.Role)
			if err != nil || !ok {
				return false, err
			}
		case workflow.CondSubtasksDone:
			var open bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issues c JOIN statuses st ON st.id = c.status_id
				JOIN issue_types it ON it.id = c.type_id WHERE c.parent_id = $1 AND it.level = -1 AND st.category <> 'done')`,
				i.ID).Scan(&open); err != nil || open {
				return false, err
			}
		}
	}
	return true, nil
}

// Transitions lists the transitions the principal can run on an issue.
func (s *Service) Transitions(ctx context.Context, p *identity.Principal, key string) ([]AvailableTransition, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	i, cfg, err := s.issueFor(ctx, s.pool, a, key, false)
	if err != nil {
		return nil, err
	}
	return s.availableTransitions(ctx, s.pool, a, p, cfg, i)
}

// availableTransitions lists the transitions the principal can run on an issue now.
func (s *Service) availableTransitions(ctx context.Context, q db.Querier, a *Access, p *identity.Principal, cfg *configSnapshot,
	i *Issue) ([]AvailableTransition, error) {
	wf, err := s.workflowFor(ctx, q, cfg, i.projectID, i.typeID)
	if err != nil {
		return nil, err
	}
	out := []AvailableTransition{}
	for _, t := range wf.Def.From(i.statusID) {
		ok, err := s.conditionsMet(ctx, q, a, p, i, t)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		at := AvailableTransition{ID: t.ID, Name: t.Name, To: cfg.statuses[t.To], Screen: transitionScreen(t)}
		for _, v := range t.Validators {
			if v.Type == workflow.ValCommentRequired {
				at.CommentRequired = true
			}
		}
		out = append(out, at)
	}
	return out, nil
}

// TransitionInput runs a transition.
type TransitionInput struct {
	TransitionID int                        `json:"transitionId"`
	Fields       map[string]json.RawMessage `json:"fields,omitempty" doc:"Values for the transition's screen"`
	Comment      *docmodel.Doc              `json:"comment,omitempty"`
}

// Transition runs a workflow transition with its validators and post-functions.
func (s *Service) Transition(ctx context.Context, p *identity.Principal, key string, in TransitionInput) (*Issue, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	var out *Issue
	var cfg *configSnapshot
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var i *Issue
		var err error
		i, cfg, err = s.issueFor(ctx, tx, a, key, true)
		if err != nil {
			return err
		}
		out, err = s.transitionIssue(ctx, tx, a, p, cfg, st, i, in, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, s.complete(ctx, s.pool, cfg, a, out)
}

// transitionIssue runs a transition on an issue (locked by the caller) within tx; b collects the notifications of a
// bulk change (nil: they are sent).
func (s *Service) transitionIssue(ctx context.Context, tx pgx.Tx, a *Access, p *identity.Principal, cfg *configSnapshot,
	st settings.Instance, i *Issue, in TransitionInput, b *bulkItem) (*Issue, error) {
	if err := s.complete(ctx, tx, cfg, a, i); err != nil {
		return nil, err
	}
	wf, err := s.workflowFor(ctx, tx, cfg, i.projectID, i.typeID)
	if err != nil {
		return nil, err
	}
	t, ok := wf.Def.Transition(in.TransitionID)
	if !ok || !(t.FromAny || slices.Contains(t.From, i.statusID)) || (t.FromAny && t.To == i.statusID) {
		return nil, apperr.Conflict("issue.transition-not-available", "This transition is not available in the issue's status.")
	}
	allowed, err := s.conditionsMet(ctx, tx, a, p, i, t)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, apperr.Forbidden("issue.transition-not-allowed", "You may not run the transition %q.", t.Name)
	}
	// A transition's comment is a comment (audit L10).
	if in.Comment != nil && docmodel.PlainText(in.Comment) != "" && !a.CanIssue(i.head(), PermCommentAdd) {
		return nil, apperr.Forbidden("comment.denied", "You may not comment on %s.", i.Key)
	}
	screen := transitionScreen(t)
	ch, err := s.applyFields(ctx, tx, cfg, st.HoursPerDay, st.DaysPerWeek, a, i, in.Fields, modeTransition, screen)
	if err != nil {
		return nil, err
	}
	// Validators see the values after the screen.
	for _, v := range t.Validators {
		switch v.Type {
		case workflow.ValCommentRequired:
			if in.Comment == nil || docmodel.PlainText(in.Comment) == "" {
				return nil, apperr.Invalid("issue.comment-required", "This transition needs a comment.")
			}
		case workflow.ValFieldRequired:
			if raw, given := in.Fields[v.Field]; given {
				if string(raw) == "null" || string(raw) == `""` || string(raw) == "[]" {
					return nil, apperr.Invalidf("issue.field-required", "The field %s is required for this transition.", v.Field)
				}
			} else if !fieldHasValue(i, v.Field) {
				return nil, apperr.Invalidf("issue.field-required", "The field %s is required for this transition.", v.Field)
			}
		case workflow.ValPermission:
			if !a.CanIssue(i.head(), v.Permission) {
				return nil, apperr.Forbidden("issue.transition-not-allowed", "You may not run the transition %q.", t.Name)
			}
		}
	}
	from, to := cfg.statuses[i.statusID], cfg.statuses[t.To]
	ch.set("status_id", t.To)
	ch.sets = append(ch.sets, "status_changed_at = now()")
	ch.items = append([]ChangeItem{{Field: FieldStatus, From: i.statusID, To: t.To, FromString: from.Name, ToString: to.Name}}, ch.items...)

	resolutionGiven := in.Fields[FieldResolution] != nil
	for _, pf := range t.PostFunctions {
		switch pf.Type {
		case workflow.PostSetResolution:
			if !resolutionGiven && cfg.resolutions[pf.Resolution] != nil {
				id := pf.Resolution
				ch.setResolution(cfg, i, &id)
				resolutionGiven = true
			}
		case workflow.PostClearResolution:
			ch.setResolution(cfg, i, nil)
			resolutionGiven = true
		case workflow.PostAssignCurrent, workflow.PostAssignReporter, workflow.PostAssignLead, workflow.PostUnassign:
			var target int64
			switch pf.Type {
			case workflow.PostAssignCurrent:
				target = p.UserID()
			case workflow.PostAssignReporter:
				target = i.reporterID
			case workflow.PostAssignLead:
				target = i.leadID
			}
			if target != i.assigneeID {
				ch.set("assignee_id", nullable(target))
				ch.item(FieldAssignee, nullable(i.assigneeID), nullable(target), "", "")
			}
		case workflow.PostSetField:
			raw, _ := json.Marshal(pf.Value)
			extra, err := s.applyFields(ctx, tx, cfg, st.HoursPerDay, st.DaysPerWeek, a, i,
				map[string]json.RawMessage{pf.Field: raw}, modeTransition, []string{pf.Field})
			if err != nil {
				return nil, err
			}
			ch.sets = append(ch.sets, rebase(extra.sets, len(ch.args))...)
			ch.args = append(ch.args, extra.args...)
			ch.items = append(ch.items, extra.items...)
			for k, v := range extra.customs {
				ch.customs[k] = v
			}
		case workflow.PostAddComment:
			if pf.Text != "" {
				var cid int64
				if err := s.insertComment(ctx, tx, i, p.UserID(), docmodel.Paragraphs(pf.Text), "", 0, &cid); err != nil {
					return nil, err
				}
			}
		}
	}
	if wf.Def.Options.AutoResolution && !resolutionGiven {
		switch {
		case to.Category == CategoryDone && i.Resolution == nil:
			if r := cfg.defaultResolution(); r != nil {
				id := r.ID
				ch.setResolution(cfg, i, &id)
			}
		case to.Category != CategoryDone && i.Resolution != nil:
			ch.setResolution(cfg, i, nil)
		}
	}
	uid := p.UserID()
	if err := ch.save(ctx, tx, i, uid, true); err != nil {
		return nil, err
	}
	if in.Comment != nil && docmodel.PlainText(in.Comment) != "" {
		if err := docmodel.Validate(in.Comment, docmodel.Options{}); err != nil {
			return nil, apperr.Invalid("comment.body", err.Error())
		}
		var cid int64
		if err := s.insertComment(ctx, tx, i, uid, in.Comment, "", 0, &cid); err != nil {
			return nil, err
		}
	}
	if err := s.refreshSearch(ctx, tx, i.ID); err != nil {
		return nil, err
	}
	out, err := s.loadIssue(ctx, tx, cfg, i.ID, false)
	if err != nil {
		return nil, err
	}
	event := EventTransition
	if to.Category == CategoryDone && from.Category != CategoryDone {
		event = EventResolved
	}
	// Who moves an issue on hears of it from then on; not so for the hundreds of issues of a bulk change.
	if b == nil {
		if err := notify.Watch(ctx, tx, "issue", i.ID, uid); err != nil {
			return nil, err
		}
	}
	if err := s.notifyChange(ctx, tx, b, out, event, uid, t.Name, from.Name+" → "+to.Name, mentionsOf(in.Comment)); err != nil {
		return nil, err
	}
	if err := s.recordActivity(ctx, tx, out, uid, "issue.transitioned", &activityData{From: statusOf(from), To: statusOf(to)}); err != nil {
		return nil, err
	}
	return out, s.bus.Publish(ctx, tx, IssueEvent{Name: "issue.transitioned", IssueID: i.ID, Key: i.Key, ProjectID: i.projectID,
		ActorID: uid, From: from.ID, To: to.ID})
}

// transitionScreen returns the fields a transition asks for: its screen and the fields its validators require.
func transitionScreen(t workflow.Transition) []string {
	screen := append([]string{}, t.Screen...)
	for _, v := range t.Validators {
		if v.Type == workflow.ValFieldRequired && !slices.Contains(screen, v.Field) {
			screen = append(screen, v.Field)
		}
	}
	return screen
}

// rebase renumbers $n placeholders of SQL fragments by offset (when merging two argument lists).
func rebase(sets []string, offset int) []string {
	out := make([]string, len(sets))
	for k, s := range sets {
		var b []byte
		for j := 0; j < len(s); j++ {
			if s[j] == '$' {
				n, e := 0, j+1
				for e < len(s) && s[e] >= '0' && s[e] <= '9' {
					n = n*10 + int(s[e]-'0')
					e++
				}
				if e > j+1 {
					b = append(b, '$')
					b = append(b, itoa(int64(n+offset))...)
					j = e - 1
					continue
				}
			}
			b = append(b, s[j])
		}
		out[k] = string(b)
	}
	return out
}

func fieldHasValue(i *Issue, field string) bool {
	switch field {
	case FieldResolution:
		return i.Resolution != nil
	case FieldAssignee:
		return i.assigneeID != 0
	case FieldPriority:
		return i.Priority != nil
	case FieldDueDate:
		return i.DueDate != nil
	case FieldDescription:
		return i.Description != nil
	case FieldLabels:
		return len(i.Labels) > 0
	case FieldFixVersions:
		return len(i.FixVersions) > 0
	case FieldComponents:
		return len(i.Components) > 0
	}
	v, ok := i.Fields[field]
	return ok && v != nil
}

// TransitionByStatus runs the first available transition that leads to a status (boards, automation).
func (s *Service) TransitionByStatus(ctx context.Context, p *identity.Principal, key string, statusID int64) (*Issue, error) {
	ts, err := s.Transitions(ctx, p, key)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.To != nil && t.To.ID == statusID && len(t.Screen) == 0 && !t.CommentRequired {
			return s.Transition(ctx, p, key, TransitionInput{TransitionID: t.ID})
		}
	}
	return nil, apperr.Conflict("issue.no-transition", "No transition without a screen leads to this status.")
}

var _ = pgx.ErrNoRows
