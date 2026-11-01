// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Bulk changes: edit, transition, move, delete, watch or stop watching many issues at once. A bulk
// change needs the project permission issue.bulk-change and, for every issue, the permission of its action — both
// checked for each issue when the job gets to it, not only when the issues were chosen. It runs as a job (bulk_run.go)
// that records a result per issue and tells the browser of its progress.
//
// Every issue gets the history entries, events (webhooks) and activity of a single change. Notifications differ:
// whoever would hear of the change of an issue gets one summary of the whole bulk change instead of a notification
// (and an e-mail) per issue — hundreds of e-mails would bury the one that matters. Administrators of all the issues'
// projects may leave the summary out, as for silent clean-ups; webhooks still receive every change, since the systems
// behind them keep copies of the issues. Automation rules react to code and builds, not to changes of issues, so a
// bulk change triggers none, as single changes trigger none.

// Limits of bulk changes.
const (
	bulkMax     = 1000 // issues per bulk change
	bulkPending = 3    // bulk changes a person may have waiting or running at a time
)

// Actions of bulk changes.
const (
	BulkEdit       = "edit"
	BulkTransition = "transition"
	BulkMove       = "move"
	BulkDelete     = "delete"
	BulkWatch      = "watch"
	BulkUnwatch    = "unwatch"
)

// LabelsChange adds, removes or replaces labels.
type LabelsChange struct {
	Mode   string   `json:"mode" enum:"add,remove,replace"`
	Values []string `json:"values" maxItems:"100"`
}

// IDsChange adds, removes or replaces the values of a field with several values (components, versions).
type IDsChange struct {
	Mode string  `json:"mode" enum:"add,remove,replace"`
	IDs  []int64 `json:"ids" maxItems:"100"`
}

// BulkEditInput sets fields of the issues.
type BulkEditInput struct {
	Fields          map[string]json.RawMessage `json:"fields,omitempty" doc:"Values that replace those of the issues (system field keys or cf_<id>); null clears"`
	Labels          *LabelsChange              `json:"labels,omitempty"`
	Components      *IDsChange                 `json:"components,omitempty"`
	FixVersions     *IDsChange                 `json:"fixVersions,omitempty"`
	AffectsVersions *IDsChange                 `json:"affectsVersions,omitempty"`
}

// BulkTransitionInput moves the issues to a status, each by the transition its workflow has to it.
type BulkTransitionInput struct {
	Status  int64                      `json:"status" doc:"The status the issues go to"`
	Fields  map[string]json.RawMessage `json:"fields,omitempty" doc:"Values for the transitions' screens: each issue takes those its transition asks for"`
	Comment *docmodel.Doc              `json:"comment,omitempty" doc:"A comment for each issue"`
}

// BulkInput starts a bulk change.
type BulkInput struct {
	Issues     []string             `json:"issues,omitempty" maxItems:"1000" doc:"Keys of the issues"`
	Query      string               `json:"query,omitempty" maxLength:"10000" doc:"Instead of keys: a KQL query, whose first 1000 results are the issues"`
	Action     string               `json:"action" enum:"edit,transition,move,delete,watch,unwatch"`
	Edit       *BulkEditInput       `json:"edit,omitempty"`
	Transition *BulkTransitionInput `json:"transition,omitempty"`
	Move       *MoveInput           `json:"move,omitempty"`
	Confirm    int                  `json:"confirm,omitempty" doc:"delete: the number of issues, as typed by the user to confirm"`
	Notify     *bool                `json:"notify,omitempty" doc:"Send the summary notification (default); only administrators of all the issues' projects may leave it out"`
}

// bulkParams is what a bulk change does, as stored.
type bulkParams struct {
	Edit       *BulkEditInput       `json:"edit,omitempty"`
	Transition *BulkTransitionInput `json:"transition,omitempty"`
	Move       *MoveInput           `json:"move,omitempty"`
}

// BulkChange is a bulk change with its progress and, for whoever may see them, the results per issue.
type BulkChange struct {
	ID         int64                `json:"id"`
	Action     string               `json:"action" enum:"edit,transition,move,delete,watch,unwatch"`
	State      string               `json:"state" enum:"queued,running,done,failed"`
	Actor      *identity.UserRef    `json:"actor,omitempty"`
	Mine       bool                 `json:"mine" doc:"The caller started the bulk change"`
	Total      int                  `json:"total"`
	Processed  int                  `json:"processed"`
	Done       int                  `json:"done"`
	Skipped    int                  `json:"skipped"`
	Failed     int                  `json:"failed"`
	Notify     bool                 `json:"notify"`
	Edit       *BulkEditInput       `json:"edit,omitempty"`
	Transition *BulkTransitionInput `json:"transition,omitempty"`
	Move       *MoveInput           `json:"move,omitempty"`
	Target     *ProjectRef          `json:"target,omitempty" doc:"move: the target project"`
	Status     *Status              `json:"status,omitempty" doc:"transition: the target status"`
	Error      string               `json:"error,omitempty"`
	Created    time.Time            `json:"created"`
	Started    *time.Time           `json:"started,omitempty"`
	Finished   *time.Time           `json:"finished,omitempty"`
	Items      []BulkItemResult     `json:"items" doc:"The issues: all for who started it, those changed that one sees for others"`
}

// BulkItemResult is what became of an issue of a bulk change.
type BulkItemResult struct {
	Key     string `json:"key" doc:"The key the issue had when it was chosen"`
	NewKey  string `json:"newKey,omitempty" doc:"move: its new key"`
	Summary string `json:"summary,omitempty"`
	Result  string `json:"result" enum:"pending,done,skipped,failed"`
	Code    string `json:"code,omitempty" doc:"The error code of skipped and failed issues"`
	Message string `json:"message,omitempty" doc:"Why it was skipped or failed"`
}

// BulkCheckInput asks what a bulk change would do.
type BulkCheckInput struct {
	Issues []string   `json:"issues,omitempty" maxItems:"1000" doc:"Keys of the issues"`
	Query  string     `json:"query,omitempty" maxLength:"10000" doc:"Instead of keys: a KQL query, whose first 1000 results are the issues"`
	Action string     `json:"action" enum:"edit,transition,move,delete,watch,unwatch"`
	Move   *MoveInput `json:"move,omitempty" doc:"move: the target project and the choices made so far"`
}

// BulkCheck is what a bulk change would do with the chosen issues. The job checks every issue again when it gets to it.
type BulkCheck struct {
	Total     int              `json:"total" doc:"The issues chosen (a query's results: all it finds)"`
	Truncated bool             `json:"truncated" doc:"The query finds more issues than a bulk change takes"`
	Issues    []BulkCheckIssue `json:"issues"`
	CanMute   bool             `json:"canMute" doc:"The caller may leave out the summary notification"`
	Fields    []BulkField      `json:"fields" doc:"edit: the fields, and the issues they do not apply to"`
	Statuses  []BulkStatus     `json:"statuses" doc:"transition: the statuses issues can go to"`
	Move      *MovePlan        `json:"move,omitempty" doc:"move: the plan"`
}

// BulkCheckIssue is a chosen issue.
type BulkCheckIssue struct {
	Key       string      `json:"key"`
	Summary   string      `json:"summary,omitempty"`
	Project   *ProjectRef `json:"project,omitempty"`
	Type      *IssueType  `json:"type,omitempty"`
	Status    *Status     `json:"status,omitempty"`
	Permitted bool        `json:"permitted" doc:"The caller may change the issue so"`
	Reason    string      `json:"reason,omitempty" doc:"Why not"`
	Subtasks  int         `json:"subtasks,omitempty" doc:"delete: sub-tasks deleted with the issue"`
}

// BulkField is a field a bulk edit can set.
type BulkField struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Custom  bool          `json:"custom"`
	Applies bool          `json:"applies" doc:"The field can be set on every issue the caller may change"`
	Missing []BulkMissing `json:"missing" doc:"The issues it does not apply to"`
}

// BulkMissing is an issue a field does not apply to.
type BulkMissing struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// BulkStatus is a status issues of a bulk transition can go to.
type BulkStatus struct {
	Status          *Status               `json:"status"`
	Issues          []BulkIssueTransition `json:"issues" doc:"The issues that have a transition to it"`
	Screen          []string              `json:"screen" doc:"The fields the transitions ask for"`
	CommentRequired bool                  `json:"commentRequired" doc:"A transition needs a comment"`
}

// BulkIssueTransition is the transition an issue would take.
type BulkIssueTransition struct {
	Key        string `json:"key"`
	Transition int    `json:"transition"`
	Name       string `json:"name"`
}

// bulkFieldKeys are the system fields a bulk edit sets (custom fields follow): those people change for many issues
// at once. Texts of their own (summary, description) and the type (a move within the project) are not among them.
var bulkFieldKeys = []string{FieldAssignee, FieldReporter, FieldPriority, FieldLabels, FieldComponents, FieldFixVersions,
	FieldAffectsVersions, FieldDueDate, FieldSprint, FieldParent, FieldSecurity}

// projectScoped are the fields whose values belong to a project: a bulk edit sets them for the issues of one project.
// Epics may be parents across projects, but are chosen among those of the issues' project, as in the issue view.
var projectScoped = []string{FieldComponents, FieldFixVersions, FieldAffectsVersions, FieldSprint, FieldSecurity, FieldParent}

// fieldPermission is the permission that changing a field needs (as applyFields decides).
func fieldPermission(key string) string {
	switch key {
	case FieldResolution, FieldFixVersions:
		return PermResolve
	case FieldAssignee:
		return PermAssign
	case FieldReporter:
		return PermModifyReporter
	case FieldDueDate, FieldSprint:
		return PermSchedule
	case FieldSecurity:
		return PermSetSecurity
	}
	return PermEdit
}

// bulkSelect loads the issues of a bulk change that the principal sees — by key (current or former), or the first
// results of a query — and returns the keys of the others; total counts what the query finds.
func (s *Service) bulkSelect(ctx context.Context, tx pgx.Tx, p *identity.Principal, a *Access, cfg *configSnapshot, keys []string,
	query string) (list []*Issue, missing []string, total int, err error) {
	if strings.TrimSpace(query) != "" {
		if len(keys) > 0 {
			return nil, nil, 0, apperr.Invalid("bulk.selection", "Choose the issues either by key or by query.")
		}
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'"); err != nil {
			return nil, nil, 0, err
		}
		var args db.Args
		where, order, err := s.CompileQuery(ctx, tx, p, query, "i", &args)
		if err != nil {
			return nil, nil, 0, err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM issues i WHERE "+where, args...).Scan(&total); err != nil {
			return nil, nil, 0, err
		}
		rows, err := tx.Query(ctx, "SELECT "+issueColumns+issueFrom+"WHERE "+where+" ORDER BY "+order+" LIMIT "+itoa(bulkMax), args...)
		if err != nil {
			return nil, nil, 0, err
		}
		for rows.Next() {
			i, err := scanIssue(rows, cfg)
			if err != nil {
				rows.Close()
				return nil, nil, 0, err
			}
			list = append(list, i)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, 0, err
		}
		_, err = tx.Exec(ctx, "SET LOCAL statement_timeout TO DEFAULT")
		return list, nil, total, err
	}
	var wanted []string
	for _, k := range keys {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k != "" && !slices.Contains(wanted, k) {
			wanted = append(wanted, k)
		}
	}
	if len(wanted) > bulkMax {
		return nil, nil, 0, apperr.Invalidf("bulk.too-many", "A bulk change takes at most %d issues.", bulkMax)
	}
	rows, err := tx.Query(ctx, "SELECT "+issueColumns+`, k.key FROM (SELECT key, id FROM issues WHERE key = ANY($1)
		UNION SELECT key, issue_id FROM issue_keys WHERE key = ANY($1)) k JOIN issues i ON i.id = k.id
		JOIN issue_projects p ON p.id = i.project_id LEFT JOIN security_levels sl ON sl.id = i.security_level_id`, wanted)
	if err != nil {
		return nil, nil, 0, err
	}
	byKey := map[string]*Issue{}
	for rows.Next() {
		var named string
		r := &issueRow{}
		if err := rows.Scan(append(r.targets(), &named)...); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		byKey[named] = r.issue(cfg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	for _, k := range wanted {
		i := byKey[k]
		switch {
		case i == nil || !a.visible(i.head()):
			missing = append(missing, k)
		case !slices.ContainsFunc(list, func(x *Issue) bool { return x.ID == i.ID }):
			list = append(list, i)
		}
	}
	return list, missing, len(wanted), nil
}

// bulkAllowed checks the bulk change permission for an issue.
func bulkAllowed(a *Access, i *Issue) error {
	if !a.CanIssue(i.head(), PermBulkChange) {
		return apperr.Forbidden("bulk.issue-denied", "You may not change %s in bulk.", i.Key)
	}
	return nil
}

// canMute reports whether the principal administers the projects of all the issues.
func canMute(a *Access, list []*Issue) bool {
	for _, i := range list {
		if !a.Can(i.projectID, PermAdmin) {
			return false
		}
	}
	return len(list) > 0
}

// CheckBulkChange says what a bulk change would do with the issues: which the caller may change, and for edits the
// fields that apply to all of them, for transitions the statuses they can go to, for moves the plan. lang words the
// reasons.
func (s *Service) CheckBulkChange(ctx context.Context, p *identity.Principal, in BulkCheckInput, lang string) (*BulkCheck, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	out := &BulkCheck{Issues: []BulkCheckIssue{}, Fields: []BulkField{}, Statuses: []BulkStatus{}}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		list, missing, total, err := s.bulkSelect(ctx, tx, p, a, cfg, in.Issues, in.Query)
		if err != nil {
			return err
		}
		out.Total, out.Truncated = total, total > len(list)+len(missing)
		out.CanMute = canMute(a, list)
		var permitted []*Issue
		entries := map[int64]int{} // issue id → index in out.Issues
		for _, i := range list {
			e := BulkCheckIssue{Key: i.Key, Summary: i.Summary, Project: &ProjectRef{ID: i.projectID, Key: i.Project.Key, Name: i.Project.Name},
				Type: cfg.types[i.typeID], Status: cfg.statuses[i.statusID]}
			err := bulkAllowed(a, i)
			if err == nil {
				err = s.bulkActionAllowed(ctx, tx, a, cfg, in.Action, i, &e)
			}
			if err != nil {
				if !isClientError(err) {
					return err
				}
				e.Reason = localized(err, lang)
			} else {
				e.Permitted = true
				permitted = append(permitted, i)
			}
			entries[i.ID] = len(out.Issues)
			out.Issues = append(out.Issues, e)
		}
		for _, k := range missing {
			out.Issues = append(out.Issues, BulkCheckIssue{Key: k, Reason: localized(notFoundIssue(k), lang)})
		}
		switch in.Action {
		case BulkEdit:
			out.Fields, err = s.bulkFields(ctx, tx, a, cfg, permitted, lang)
		case BulkTransition:
			out.Statuses, err = s.bulkStatuses(ctx, tx, a, p, cfg, permitted)
			for _, i := range permitted {
				if !slices.ContainsFunc(out.Statuses, func(st BulkStatus) bool {
					return slices.ContainsFunc(st.Issues, func(t BulkIssueTransition) bool { return t.Key == i.Key })
				}) {
					e := &out.Issues[entries[i.ID]]
					e.Permitted, e.Reason = false, localized(apperr.Conflict("bulk.no-transitions",
						"%s has no transition that you may run now.", i.Key), lang)
				}
			}
		case BulkMove:
			if in.Move == nil || strings.TrimSpace(in.Move.Project) == "" {
				return apperr.Invalid("bulk.move-target", "Choose the project to move the issues to.")
			}
			t, err := s.loadMoveTarget(ctx, tx, a, in.Move.Project)
			if err != nil {
				return err
			}
			if out.Move, err = s.planMove(ctx, tx, a, cfg, permitted, t, mappingOf(*in.Move), lang); err != nil {
				return err
			}
			// Issues the plan finds unmovable are not permitted either.
			for _, pi := range out.Move.Issues {
				if pi.Movable || pi.WithParent != "" {
					continue
				}
				for k := range out.Issues {
					if out.Issues[k].Key == pi.Key && out.Issues[k].Permitted {
						out.Issues[k].Permitted, out.Issues[k].Reason = false, pi.Reason
					}
				}
			}
		}
		return err
	})
	return out, err
}

// bulkActionAllowed checks the permission an action needs on an issue (as far as it does not depend on the values).
func (s *Service) bulkActionAllowed(ctx context.Context, tx pgx.Tx, a *Access, cfg *configSnapshot, action string, i *Issue,
	e *BulkCheckIssue) error {
	switch action {
	case BulkDelete:
		if !a.CanIssue(i.head(), PermDelete) {
			return apperr.Forbidden("issue.delete-denied", "You may not delete %s.", i.Key)
		}
		subs, err := subtaskIDs(ctx, tx, i.ID)
		if err != nil {
			return err
		}
		for _, id := range subs {
			sub, err := s.loadIssue(ctx, tx, cfg, id, false)
			if err != nil {
				return err
			}
			if !a.visible(sub.head()) || !a.CanIssue(sub.head(), PermDelete) {
				return apperr.Forbidden("issue.delete-subtasks", "%s has sub-tasks you may not delete.", i.Key)
			}
		}
		e.Subtasks = len(subs)
	case BulkMove:
		if !a.CanIssue(i.head(), PermMove) {
			return apperr.Forbidden("issue.move-denied", "You may not move %s.", i.Key)
		}
	case BulkTransition:
		if !a.CanIssue(i.head(), PermTransition) {
			return apperr.Forbidden("bulk.transition-denied", "You may not transition %s.", i.Key)
		}
	case BulkEdit, BulkWatch, BulkUnwatch:
	default:
		return apperr.Invalidf("bulk.action", "Unknown action %s.", action)
	}
	return nil
}

// bulkFields says for each field a bulk edit can set which of the issues it does not apply to: fields not on the
// edit screen of an issue's type, custom fields of other projects or types, fields the caller may not change, and
// fields of project values (components, versions, sprints, security levels) for issues of several projects.
func (s *Service) bulkFields(ctx context.Context, q db.Querier, a *Access, cfg *configSnapshot, list []*Issue, lang string) ([]BulkField, error) {
	type layoutKey struct{ project, typ int64 }
	layouts := map[layoutKey][]LayoutItem{}
	projects := map[int64]bool{}
	for _, i := range list {
		projects[i.projectID] = true
		k := layoutKey{i.projectID, i.typeID}
		if _, ok := layouts[k]; !ok {
			l, err := s.FieldLayout(ctx, i.projectID, i.typeID)
			if err != nil {
				return nil, err
			}
			layouts[k] = l
		}
	}
	var defs []BulkField
	for _, key := range bulkFieldKeys {
		f, _ := systemField(key)
		defs = append(defs, BulkField{Key: key, Name: f.Name, Type: f.Type})
	}
	for _, f := range cfg.fieldList {
		defs = append(defs, BulkField{Key: f.Key, Name: f.Name, Type: f.Type, Custom: true})
	}
	out := []BulkField{}
	for _, d := range defs {
		d.Missing = []BulkMissing{}
		scoped := slices.Contains(projectScoped, d.Key) || d.Custom && (d.Type == "version" || d.Type == "multiversion")
		for _, i := range list {
			typ := cfg.types[i.typeID]
			onScreen := d.Key == FieldSprint // the sprint is no field of layouts
			for _, it := range layouts[layoutKey{i.projectID, i.typeID}] {
				if it.Field == d.Key {
					onScreen = it.Edit
				}
			}
			id, _ := parseFieldKey(d.Key)
			// The reasons appear under the field: they need not name it.
			why := ""
			switch {
			case d.Custom && !fieldApplies(cfg.fields[id], i.projectID, i.typeID):
				why = apperr.Text(lang, "The field does not exist for issues of the type %s in %s.", typ.Name, i.Project.Key)
			case !onScreen:
				why = apperr.Text(lang, "Not on the edit screen of issues of the type %s in %s.", typ.Name, i.Project.Key)
			case !a.CanIssue(i.head(), fieldPermission(d.Key)):
				why = apperr.Text(lang, "You may not change this field of %s.", i.Key)
			case d.Key == FieldSprint && typ.Level == -1:
				why = apperr.Text(lang, "Sub-tasks follow their parent into sprints.")
			case d.Key == FieldParent && typ.Level != 0:
				why = apperr.Text(lang, "Only standard issues get another parent here.")
			case d.Key == FieldSecurity && typ.Level == -1:
				why = apperr.Text(lang, "A sub-task has the security level of its parent.")
			case scoped && len(projects) > 1:
				why = apperr.Text(lang, "Its values belong to a project; the issues are in several projects.")
			}
			if why != "" {
				d.Missing = append(d.Missing, BulkMissing{Key: i.Key, Reason: why})
			}
		}
		d.Applies = len(list) > 0 && len(d.Missing) == 0
		out = append(out, d)
	}
	return out, nil
}

// bulkStatuses lists the statuses the issues can go to, each with the transition each issue would take (the first
// one its workflow has to the status that the caller may run now).
func (s *Service) bulkStatuses(ctx context.Context, q db.Querier, a *Access, p *identity.Principal, cfg *configSnapshot,
	list []*Issue) ([]BulkStatus, error) {
	out := []BulkStatus{}
	index := map[int64]int{}
	for _, i := range list {
		ts, err := s.availableTransitions(ctx, q, a, p, cfg, i)
		if err != nil {
			return nil, err
		}
		seen := map[int64]bool{}
		for _, t := range ts {
			if t.To == nil || seen[t.To.ID] {
				continue
			}
			seen[t.To.ID] = true
			k, ok := index[t.To.ID]
			if !ok {
				k, index[t.To.ID] = len(out), len(out)
				out = append(out, BulkStatus{Status: t.To, Issues: []BulkIssueTransition{}, Screen: []string{}})
			}
			st := &out[k]
			st.Issues = append(st.Issues, BulkIssueTransition{Key: i.Key, Transition: t.ID, Name: t.Name})
			for _, f := range t.Screen {
				if !slices.Contains(st.Screen, f) {
					st.Screen = append(st.Screen, f)
				}
			}
			st.CommentRequired = st.CommentRequired || t.CommentRequired
		}
	}
	slices.SortStableFunc(out, func(x, y BulkStatus) int { return x.Status.Position - y.Status.Position })
	return out, nil
}

// validate checks the input of a bulk change before it is queued (each issue is checked when the job gets to it).
func (in *BulkInput) validate(cfg *configSnapshot) error {
	switch in.Action {
	case BulkEdit:
		e := in.Edit
		if e == nil || len(e.Fields) == 0 && e.Labels == nil && e.Components == nil && e.FixVersions == nil && e.AffectsVersions == nil {
			return apperr.Invalid("bulk.edit-empty", "Choose at least one field to change.")
		}
		for key := range e.Fields {
			id, custom := parseFieldKey(key)
			if custom && cfg.fields[id] == nil || !custom && !slices.Contains(bulkFieldKeys, key) {
				return apperr.Invalidf("bulk.edit-field", "The field %s cannot be changed in bulk.", key).WithFields(
					apperr.FieldError{Location: "body.edit.fields." + key})
			}
		}
		for _, key := range []string{FieldLabels, FieldComponents, FieldFixVersions, FieldAffectsVersions} {
			if _, ok := e.Fields[key]; ok {
				return apperr.Invalidf("bulk.edit-field", "The field %s cannot be changed in bulk.", key).WithFields(
					apperr.FieldError{Location: "body.edit.fields." + key, Message: "use " + key + " with a mode"})
			}
		}
	case BulkTransition:
		if in.Transition == nil || cfg.statuses[in.Transition.Status] == nil {
			return apperr.Invalid("bulk.transition-status", "Choose the status the issues go to.")
		}
		if c := in.Transition.Comment; c != nil && docmodel.PlainText(c) != "" {
			if err := docmodel.Validate(c, docmodel.Options{}); err != nil {
				return apperr.Invalid("comment.body", err.Error())
			}
		}
	case BulkMove:
		if in.Move == nil || strings.TrimSpace(in.Move.Project) == "" {
			return apperr.Invalid("bulk.move-target", "Choose the project to move the issues to.")
		}
	case BulkDelete, BulkWatch, BulkUnwatch:
	default:
		return apperr.Invalidf("bulk.action", "Unknown action %s.", in.Action)
	}
	return nil
}

// StartBulkChange queues a bulk change of the issues the principal sees, with the bulk change permission in the
// projects of at least one of them. lang words the results.
func (s *Service) StartBulkChange(ctx context.Context, p *identity.Principal, in BulkInput, lang string) (*BulkChange, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		list, missing, _, err := s.bulkSelect(ctx, tx, p, a, cfg, in.Issues, in.Query)
		if err != nil {
			return err
		}
		// Access before state: who sees none of the issues learns nothing more.
		if len(list) == 0 {
			if strings.TrimSpace(in.Query) != "" {
				return apperr.Invalid("bulk.empty", "The query finds no issues.")
			}
			return apperr.NotFound("bulk.not-found", "None of the issues exists or you may see it.")
		}
		if !slices.ContainsFunc(list, func(i *Issue) bool { return bulkAllowed(a, i) == nil }) {
			return apperr.Forbidden("bulk.denied", "You may not change these issues in bulk.")
		}
		if err := in.validate(cfg); err != nil {
			return err
		}
		switch in.Action {
		case BulkMove:
			if _, err := s.loadMoveTarget(ctx, tx, a, in.Move.Project); err != nil {
				return err
			}
		case BulkDelete:
			if in.Confirm != len(list) {
				return apperr.Invalidf("bulk.confirm", "To delete the issues, confirm their number: %d.", len(list))
			}
		}
		notifyAll := in.Notify == nil || *in.Notify
		if !notifyAll && !canMute(a, list) {
			return apperr.Forbidden("bulk.mute-denied", "Only administrators of all the issues' projects may leave out the notifications.")
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM issue_bulk_changes WHERE actor_id = $1 AND state IN ('queued', 'running')`,
			p.UserID()).Scan(&pending); err != nil {
			return err
		}
		if pending >= bulkPending {
			return apperr.TooManyRequests("bulk.busy", "You have %d bulk changes waiting or running; start the next one when one of them is done.",
				pending)
		}
		params, err := json.Marshal(bulkParams{Edit: in.Edit, Transition: in.Transition, Move: in.Move})
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO issue_bulk_changes (actor_id, action, params, notify, lang, admin_scope, total)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, p.UserID(), in.Action, params, notifyAll, lang,
			p.AllowsScope(identity.ScopeAdmin), len(list)+len(missing)).Scan(&id); err != nil {
			return err
		}
		var issueIDs []int64
		var keys []string
		for _, i := range list {
			issueIDs, keys = append(issueIDs, i.ID), append(keys, i.Key)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_bulk_items (change_id, position, issue_id, issue_key)
			SELECT $1, n, issue_id, issue_key FROM unnest($2::bigint[], $3::text[]) WITH ORDINALITY AS u(issue_id, issue_key, n)`,
			id, issueIDs, keys); err != nil {
			return err
		}
		// Keys of issues that do not exist or are hidden are skipped right away.
		for k, key := range missing {
			msg := localized(notFoundIssue(key), lang)
			if _, err := tx.Exec(ctx, `INSERT INTO issue_bulk_items (change_id, position, issue_key, result, code, message)
				VALUES ($1, $2, $3, 'skipped', 'issue.not-found', $4)`, id, len(list)+k+1, key, msg); err != nil {
				return err
			}
		}
		if len(missing) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE issue_bulk_changes SET processed = $2 WHERE id = $1", id, len(missing)); err != nil {
				return err
			}
		}
		// One audit event with the scope of the whole bulk change; each issue keeps its history.
		projects := []string{}
		for _, i := range list {
			if !slices.Contains(projects, i.Project.Key) {
				projects = append(projects, i.Project.Key)
			}
		}
		details := map[string]any{"action": in.Action, "issues": len(list), "keys": keys, "projects": projects, "notify": notifyAll}
		switch {
		case in.Edit != nil:
			var fields []string
			for k := range in.Edit.Fields {
				fields = append(fields, k)
			}
			for k, set := range map[string]bool{FieldLabels: in.Edit.Labels != nil, FieldComponents: in.Edit.Components != nil,
				FieldFixVersions: in.Edit.FixVersions != nil, FieldAffectsVersions: in.Edit.AffectsVersions != nil} {
				if set {
					fields = append(fields, k)
				}
			}
			slices.Sort(fields)
			details["fields"] = fields
		case in.Transition != nil:
			details["status"] = cfg.statuses[in.Transition.Status].Name
		case in.Move != nil:
			details["target"] = strings.ToUpper(in.Move.Project)
		}
		if strings.TrimSpace(in.Query) != "" {
			details["query"] = in.Query
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "issue.bulk-change", TargetType: "bulk-change",
			TargetID: audit.ID(id), Details: details}); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, bulkRequested{ID: id})
	})
	if err != nil {
		return nil, err
	}
	return s.GetBulkChange(ctx, p, id)
}

// bulkRequested queues the job of a bulk change.
type bulkRequested struct{ ID int64 }

// EventName implements events.Event.
func (bulkRequested) EventName() string { return "issue.bulk-requested" }

const bulkColumns = `c.id, c.actor_id, c.action, c.params, c.notify, c.lang, c.admin_scope, c.state, c.total, c.processed, c.error,
	c.created_at, c.started_at, c.finished_at`

// bulkRow is a bulk change as stored.
type bulkRow struct {
	BulkChange
	actorID    int64
	params     bulkParams
	lang       string
	adminScope bool
}

func scanBulk(row pgx.Row) (*bulkRow, error) {
	b := &bulkRow{}
	var params []byte
	if err := row.Scan(&b.ID, &b.actorID, &b.Action, &params, &b.Notify, &b.lang, &b.adminScope, &b.State, &b.Total, &b.Processed,
		&b.Error, &b.Created, &b.Started, &b.Finished); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(params, &b.params); err != nil {
		return nil, err
	}
	b.Edit, b.Transition, b.Move = b.params.Edit, b.params.Transition, b.params.Move
	return b, nil
}

func bulkByID(ctx context.Context, q db.Querier, id int64, forUpdate bool) (*bulkRow, error) {
	sql := "SELECT " + bulkColumns + " FROM issue_bulk_changes c WHERE c.id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	b, err := scanBulk(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("bulk.not-found", "The bulk change does not exist.")
	}
	return b, err
}

// GetBulkChange returns a bulk change: with all its issues for who started it; for others who see issues it changed,
// with those issues only (as its summary notification links here). Others get 404.
func (s *Service) GetBulkChange(ctx context.Context, p *identity.Principal, id int64) (*BulkChange, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	b, err := bulkByID(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	b.Mine = b.actorID == p.UserID()
	rows, err := s.pool.Query(ctx, `SELECT b.issue_id, b.issue_key, coalesce(b.new_key, ''), coalesce(i.summary, ''), b.result, b.code,
		b.message FROM issue_bulk_items b LEFT JOIN issues i ON i.id = b.issue_id WHERE b.change_id = $1 ORDER BY b.position`, id)
	if err != nil {
		return nil, err
	}
	type itemRow struct {
		issueID *int64
		item    BulkItemResult
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (itemRow, error) {
		var it itemRow
		err := r.Scan(&it.issueID, &it.item.Key, &it.item.NewKey, &it.item.Summary, &it.item.Result, &it.item.Code, &it.item.Message)
		return it, err
	})
	if err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	if !b.Mine {
		a, err := s.Access(ctx, p)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, it := range items {
			if it.issueID != nil && it.item.Result == "done" {
				ids = append(ids, *it.issueID)
			}
		}
		refs, err := s.issueRefs(ctx, s.pool, cfg, a, "i.id = ANY($1)", nonNil(ids))
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			visible[r.ID] = true
		}
		if len(visible) == 0 {
			return nil, apperr.NotFound("bulk.not-found", "The bulk change does not exist.")
		}
	}
	b.Items = []BulkItemResult{}
	for _, it := range items {
		if !b.Mine {
			// Others learn what was changed of what they see, not what the person could not do.
			if it.issueID != nil && visible[*it.issueID] {
				b.Items = append(b.Items, it.item)
			}
			continue
		}
		switch it.item.Result {
		case "done":
			b.Done++
		case "skipped":
			b.Skipped++
		case "failed":
			b.Failed++
		}
		b.Items = append(b.Items, it.item)
	}
	if !b.Mine {
		b.Total, b.Processed, b.Done, b.Error = len(b.Items), len(b.Items), len(b.Items), ""
	}
	refs, err := identity.GetUserRefs(ctx, s.pool, []int64{b.actorID})
	if err != nil {
		return nil, err
	}
	if r, ok := refs[b.actorID]; ok {
		b.Actor = &r
	}
	if b.Move != nil {
		if proj, err := projectByKey(ctx, s.pool, b.Move.Project); err == nil {
			ref := proj.Ref()
			b.Target = &ref
		}
	}
	if b.Transition != nil {
		b.Status = cfg.statuses[b.Transition.Status]
	}
	return &b.BulkChange, nil
}

// BulkChanges lists the caller's bulk changes of the last 30 days, newest first (without their issues).
func (s *Service) BulkChanges(ctx context.Context, p *identity.Principal) ([]BulkChange, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+bulkColumns+`, (SELECT count(*) FILTER (WHERE result = 'done') FROM issue_bulk_items
		WHERE change_id = c.id), (SELECT count(*) FILTER (WHERE result = 'skipped') FROM issue_bulk_items WHERE change_id = c.id),
		(SELECT count(*) FILTER (WHERE result = 'failed') FROM issue_bulk_items WHERE change_id = c.id)
		FROM issue_bulk_changes c WHERE c.actor_id = $1 AND c.created_at > now() - interval '30 days' ORDER BY c.id DESC LIMIT 50`,
		p.UserID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BulkChange{}
	for rows.Next() {
		b := &bulkRow{}
		var params []byte
		if err := rows.Scan(&b.ID, &b.actorID, &b.Action, &params, &b.Notify, &b.lang, &b.adminScope, &b.State, &b.Total, &b.Processed,
			&b.Error, &b.Created, &b.Started, &b.Finished, &b.Done, &b.Skipped, &b.Failed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(params, &b.params); err != nil {
			return nil, err
		}
		b.Edit, b.Transition, b.Move = b.params.Edit, b.params.Transition, b.params.Move
		if b.Transition != nil {
			b.Status = cfg.statuses[b.Transition.Status]
		}
		b.Mine, b.Items = true, []BulkItemResult{}
		out = append(out, b.BulkChange)
	}
	return out, rows.Err()
}

// VisibleBulkChanges reports which bulk changes (by id) a principal may still see, for re-checking their summary
// notifications: who started one, and who sees an issue it changed.
func (s *Service) VisibleBulkChanges(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	var nums []int64
	for _, id := range ids {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 || !p.Authenticated() {
		return out, nil
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.actor_id = $2, coalesce(array_agg(b.issue_id) FILTER (WHERE b.issue_id IS NOT NULL), '{}')
		FROM issue_bulk_changes c LEFT JOIN issue_bulk_items b ON b.change_id = c.id AND b.result = 'done'
		WHERE c.id = ANY($1) GROUP BY c.id`, nums, p.UserID())
	if err != nil {
		return nil, err
	}
	issuesOf := map[int64][]int64{}
	var all []int64
	for rows.Next() {
		var id int64
		var mine bool
		var issueIDs []int64
		if err := rows.Scan(&id, &mine, &issueIDs); err != nil {
			rows.Close()
			return nil, err
		}
		if mine {
			out[strconv.FormatInt(id, 10)] = true
			continue
		}
		issuesOf[id] = issueIDs
		all = append(all, issueIDs...)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(all) == 0 {
		return out, err
	}
	refs, err := s.issueRefs(ctx, s.pool, cfg, a, "i.id = ANY($1)", all)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, r := range refs {
		seen[r.ID] = true
	}
	for id, issueIDs := range issuesOf {
		out[strconv.FormatInt(id, 10)] = slices.ContainsFunc(issueIDs, func(i int64) bool { return seen[i] })
	}
	return out, nil
}

// PurgeBulkChanges deletes the bulk changes that ended before the cutoff, with their results.
func (s *Service) PurgeBulkChanges(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM issue_bulk_changes WHERE state IN ('done', 'failed') AND finished_at < $1", time.Now().Add(-age))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
