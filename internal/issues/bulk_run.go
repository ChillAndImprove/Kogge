// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/settings"
)

// The job of a bulk change: one transaction per issue, so that an issue that cannot be changed leaves the others
// changed and its result recorded. A job that ends early (a restart of the server, a lost connection) runs again and
// goes on with the issues still pending.

// BulkArgs runs a bulk change.
type BulkArgs struct {
	ID int64 `json:"id"`
}

// Kind implements river.JobArgs.
func (BulkArgs) Kind() string { return "issues.bulk-change" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (BulkArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueDefault, MaxAttempts: 5}
}

type bulkWorker struct {
	river.WorkerDefaults[BulkArgs]
	s *Service
}

// Timeout allows for a thousand moves with their sub-tasks.
func (w *bulkWorker) Timeout(*river.Job[BulkArgs]) time.Duration { return time.Hour }

func (w *bulkWorker) Work(ctx context.Context, job *river.Job[BulkArgs]) error {
	err := w.s.RunBulkChange(ctx, job.Args.ID)
	if err != nil && job.Attempt >= job.MaxAttempts {
		w.s.log.Error("bulk change failed", "id", job.Args.ID, "error", err)
		return w.s.endBulkChange(context.WithoutCancel(ctx), job.Args.ID, "failed",
			apperr.New(http.StatusInternalServerError, "bulk.stopped", "The bulk change stopped after an internal error."))
	}
	return err
}

// Register adds the jobs of the issues module.
func (s *Service) Register(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &bulkWorker{s: s})
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(bulkRequested); ok {
			return BulkArgs{ID: e.ID}, nil
		}
		return nil, nil
	}, bulkRequested{}.EventName())
}

// bulkLive tells the browser of who started a bulk change of its progress.
func bulkLive(ctx context.Context, tx pgx.Tx, b *bulkRow) error {
	return events.NotifyLive(ctx, tx, events.LiveMessage{Type: "bulk-change", ID: itoa(b.ID), User: b.actorID})
}

// bulkRun is what the items of a running bulk change share.
type bulkRun struct {
	b      *bulkRow
	p      *identity.Principal
	cfg    *configSnapshot
	st     settings.Instance
	target *moveTarget // move
}

// RunBulkChange works through the pending issues of a bulk change: first the issues, then the sub-tasks among them
// (those whose parent was moved or deleted went with it), and sends the summary notifications at the end.
func (s *Service) RunBulkChange(ctx context.Context, id int64) error {
	b, err := bulkByID(ctx, s.pool, id, false)
	if err != nil {
		if apperr.IsNotFound(err) {
			return nil // purged
		}
		return err
	}
	if b.State == "done" || b.State == "failed" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE issue_bulk_changes SET state = 'running', started_at = coalesce(started_at, now())
		WHERE id = $1`, id); err != nil {
		return err
	}
	// The job acts as who started the bulk change, with their permissions now; through an access token without the
	// admin scope, without administration.
	p, err := identity.LoadPrincipal(ctx, s.pool, b.actorID, identity.MethodInternal)
	if apperr.Is(err, "auth.user-inactive") {
		return s.endBulkChange(ctx, id, "failed", apperr.Forbidden("bulk.actor-inactive",
			"The person who started the bulk change is no longer active."))
	}
	if err != nil {
		return err
	}
	if !b.adminScope {
		p.Method, p.Scopes = identity.MethodToken, []string{identity.ScopeWrite}
	}
	run := &bulkRun{b: b, p: p}
	if run.cfg, err = s.config(ctx); err != nil {
		return err
	}
	if run.st, err = s.settings.Get(ctx); err != nil {
		return err
	}
	if b.Action == BulkMove {
		a, err := s.Access(ctx, p)
		if err != nil {
			return err
		}
		if run.target, err = s.loadMoveTarget(ctx, s.pool, a, b.params.Move.Project); err != nil {
			if !isClientError(err) {
				return err
			}
			return s.endBulkChange(ctx, id, "failed", err)
		}
	}
	type pending struct {
		position int
		issueID  int64
		key      string
	}
	rows, err := s.pool.Query(ctx, `SELECT b.position, coalesce(b.issue_id, 0), b.issue_key FROM issue_bulk_items b
		LEFT JOIN issues i ON i.id = b.issue_id LEFT JOIN issue_types t ON t.id = i.type_id
		WHERE b.change_id = $1 AND b.result = 'pending' ORDER BY coalesce(t.level, 0) = -1, b.position`, id)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var it pending
		err := r.Scan(&it.position, &it.issueID, &it.key)
		return it, err
	})
	if err != nil {
		return err
	}
	last := time.Time{}
	for n, it := range items {
		// The browser learns of the progress a few times a second at most.
		tell := n == len(items)-1 || time.Since(last) > 500*time.Millisecond
		if err := s.runBulkItem(ctx, run, it.position, it.issueID, it.key, tell); err != nil {
			return err
		}
		if tell {
			last = time.Now()
		}
	}
	if err := s.sendBulkSummaries(ctx, b); err != nil {
		return err
	}
	return s.endBulkChange(ctx, id, "done", nil)
}

// endBulkChange records the end of a bulk change; after a failure (reason), the issues still pending are skipped.
func (s *Service) endBulkChange(ctx context.Context, id int64, state string, reason error) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := bulkByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		message := ""
		if reason != nil {
			message = localized(reason, b.lang)
			if _, err := tx.Exec(ctx, `UPDATE issue_bulk_items SET result = 'skipped', code = 'bulk.stopped', message = $2
				WHERE change_id = $1 AND result = 'pending'`, id, message); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_bulk_changes SET state = $2, error = $3, finished_at = now(),
			processed = (SELECT count(*) FROM issue_bulk_items WHERE change_id = $1 AND result <> 'pending') WHERE id = $1`,
			id, state, message); err != nil {
			return err
		}
		return bulkLive(ctx, tx, b)
	})
}

// bulkOutcome classifies the error of an issue: what the person may not do, or what does not apply to the issue,
// is skipped; invalid values and everything unexpected failed.
func bulkOutcome(err error) string {
	var e *apperr.Error
	if !errors.As(err, &e) {
		return "failed"
	}
	switch {
	case e.Status == 403, e.Status == 404, e.Status == 409:
		return "skipped"
	case e.Code == "issue.field-not-on-screen", e.Code == "issue.field-unknown", e.Code == "issue.move-subtask":
		return "skipped"
	}
	return "failed"
}

// runBulkItem changes one issue of a bulk change in its own transaction and records the result; tell sends the
// browser of who started it the progress.
func (s *Service) runBulkItem(ctx context.Context, run *bulkRun, position int, issueID int64, key string, tell bool) error {
	b := run.b
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var result string
		if err := tx.QueryRow(ctx, "SELECT result FROM issue_bulk_items WHERE change_id = $1 AND position = $2 FOR UPDATE",
			b.ID, position).Scan(&result); err != nil {
			return err
		}
		if result != "pending" {
			return nil // done with its parent, or by an earlier run of the job
		}
		item := &bulkItem{notify: b.Notify && b.Action != BulkDelete}
		var newKey, note string
		// The change of the issue runs in a savepoint: if it fails, its partial writes go and its result stays.
		actionErr := db.InTx(ctx, tx, func(sp pgx.Tx) error {
			var err error
			newKey, note, err = s.bulkApply(ctx, sp, run, issueID, key, item)
			return err
		})
		code, message := "", note
		result = "done"
		if actionErr != nil {
			if ctx.Err() != nil {
				return ctx.Err() // the job ends; the issue stays pending for the next run
			}
			result = bulkOutcome(actionErr)
			e := apperr.From(actionErr)
			if e.Status >= 500 {
				s.log.Error("bulk change of an issue failed", "bulk", b.ID, "issue", key, "error", actionErr)
			}
			code, message = e.Code, e.Localize(b.lang).Detail
			item.recipients = nil
		}
		var nk any
		if newKey != "" {
			nk = newKey
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_bulk_items SET result = $3, code = $4, message = $5, new_key = $6, recipients = $7
			WHERE change_id = $1 AND position = $2`, b.ID, position, result, code, message, nk, nonNil(item.recipients)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE issue_bulk_changes SET processed = processed + 1 WHERE id = $1", b.ID); err != nil {
			return err
		}
		if tell {
			return bulkLive(ctx, tx, b)
		}
		return nil
	})
}

// bulkApply changes one issue as the bulk change says and returns its new key (moves) and a note for the result.
func (s *Service) bulkApply(ctx context.Context, tx pgx.Tx, run *bulkRun, issueID int64, key string, item *bulkItem) (string, string, error) {
	b, p, cfg := run.b, run.p, run.cfg
	a, err := s.Access(ctx, p)
	if err != nil {
		return "", "", err
	}
	i, err := s.loadIssue(ctx, tx, cfg, issueID, true)
	if apperr.IsNotFound(err) {
		return "", "", apperr.NotFound("bulk.issue-gone", "%s no longer exists.", key)
	}
	if err != nil {
		return "", "", err
	}
	if !a.visible(i.head()) {
		return "", "", notFoundIssue(key)
	}
	if err := bulkAllowed(a, i); err != nil {
		return "", "", err
	}
	unchanged := apperr.Conflict("bulk.unchanged", "%s needed no change.", i.Key)
	switch b.Action {
	case BulkEdit:
		fields, err := s.bulkEditFields(ctx, tx, i, b.params.Edit)
		if err != nil {
			return "", "", err
		}
		if len(fields) == 0 {
			return "", "", unchanged
		}
		_, changed, err := s.editIssue(ctx, tx, a, p, cfg, run.st, i, fields, item)
		if err == nil && !changed {
			err = unchanged
		}
		return "", "", err
	case BulkTransition:
		in := b.params.Transition
		ts, err := s.availableTransitions(ctx, tx, a, p, cfg, i)
		if err != nil {
			return "", "", err
		}
		k := slices.IndexFunc(ts, func(t AvailableTransition) bool { return t.To != nil && t.To.ID == in.Status })
		if k < 0 {
			if i.statusID == in.Status {
				return "", "", unchanged
			}
			return "", "", apperr.Conflict("bulk.no-transition", "%s has no transition to %s that you may run now.", i.Key,
				cfg.statuses[in.Status].Name)
		}
		// Each issue takes the values its transition's screen asks for.
		fields := map[string]json.RawMessage{}
		for _, f := range ts[k].Screen {
			if v, ok := in.Fields[f]; ok {
				fields[f] = v
			}
		}
		_, err = s.transitionIssue(ctx, tx, a, p, cfg, run.st, i, TransitionInput{TransitionID: ts[k].ID, Fields: fields,
			Comment: in.Comment}, item)
		return "", "", err
	case BulkMove:
		moved, subs, err := s.moveIssue(ctx, tx, a, p, cfg, i, run.target, mappingOf(*b.params.Move), item)
		if err != nil {
			return "", "", err
		}
		for id, k := range subs {
			if err := markWithParent(ctx, tx, b, id, k, apperr.Text(b.lang, "Moved with its parent %s.", moved.Key)); err != nil {
				return "", "", err
			}
		}
		note := ""
		if len(subs) > 0 {
			note = apperr.Text(b.lang, "With %d sub-tasks.", len(subs))
		}
		return moved.Key, note, nil
	case BulkDelete:
		subs, err := s.deleteIssue(ctx, tx, a, p, cfg, i, item)
		if err != nil {
			return "", "", err
		}
		for _, id := range subs {
			if err := markWithParent(ctx, tx, b, id, "", apperr.Text(b.lang, "Deleted with its parent %s.", i.Key)); err != nil {
				return "", "", err
			}
		}
		note := ""
		if len(subs) > 0 {
			note = apperr.Text(b.lang, "With %d sub-tasks.", len(subs))
		}
		return "", note, nil
	case BulkWatch, BulkUnwatch:
		watching, err := notify.IsWatching(ctx, tx, "issue", i.ID, p.UserID())
		if err != nil {
			return "", "", err
		}
		if watching == (b.Action == BulkWatch) {
			return "", "", unchanged
		}
		if b.Action == BulkWatch {
			return "", "", notify.Watch(ctx, tx, "issue", i.ID, p.UserID())
		}
		return "", "", notify.Unwatch(ctx, tx, "issue", i.ID, p.UserID())
	}
	return "", "", apperr.Invalidf("bulk.action", "Unknown action %s.", b.Action)
}

// markWithParent records that a chosen sub-task went with its parent.
func markWithParent(ctx context.Context, tx pgx.Tx, b *bulkRow, issueID int64, newKey, note string) error {
	var nk any
	if newKey != "" {
		nk = newKey
	}
	tag, err := tx.Exec(ctx, `UPDATE issue_bulk_items SET result = 'done', message = $3, new_key = $4 WHERE change_id = $1
		AND issue_id = $2 AND result = 'pending'`, b.ID, issueID, note, nk)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE issue_bulk_changes SET processed = processed + $2 WHERE id = $1", b.ID, tag.RowsAffected())
	return err
}

// bulkEditFields works out the values a bulk edit gives an issue: the fields as given, and labels, components and
// versions added to, removed from or replacing the issue's.
func (s *Service) bulkEditFields(ctx context.Context, tx pgx.Tx, i *Issue, e *BulkEditInput) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	for k, v := range e.Fields {
		fields[k] = v
	}
	if e.Labels != nil {
		next := applyValues(e.Labels.Mode, i.Labels, e.Labels.Values)
		if !slices.Equal(next, i.Labels) {
			raw, _ := json.Marshal(next)
			fields[FieldLabels] = raw
		}
	}
	for key, change := range map[string]*IDsChange{FieldComponents: e.Components, FieldFixVersions: e.FixVersions,
		FieldAffectsVersions: e.AffectsVersions} {
		if change == nil {
			continue
		}
		sql := "SELECT component_id FROM issue_components WHERE issue_id = $1 ORDER BY component_id"
		args := []any{i.ID}
		if key != FieldComponents {
			sql = "SELECT version_id FROM issue_versions WHERE issue_id = $1 AND kind = $2 ORDER BY version_id"
			args = append(args, map[string]string{FieldFixVersions: "fix", FieldAffectsVersions: "affects"}[key])
		}
		cur, err := userIDs(ctx, tx, sql, args...)
		if err != nil {
			return nil, err
		}
		cur = nonNil(cur)
		next := applyValues(change.Mode, cur, change.IDs)
		slices.Sort(next)
		if !slices.Equal(next, cur) {
			raw, _ := json.Marshal(next)
			fields[key] = raw
		}
	}
	return fields, nil
}

// applyValues adds values to, removes them from or replaces the current ones, keeping the order of the current
// values.
func applyValues[T comparable](mode string, current, values []T) []T {
	out := []T{}
	switch mode {
	case "replace":
		for _, v := range values {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	case "remove":
		for _, v := range current {
			if !slices.Contains(values, v) {
				out = append(out, v)
			}
		}
	default: // add
		out = append(out, current...)
		for _, v := range values {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// sendBulkSummaries sends each recipient one notification of the issues of the bulk change they would have heard
// of, listing those they see. Each issue's recipients were chosen when it changed, by the scheme of its project and
// with what they could see then; the notifications are re-checked when they are shown and mailed.
func (s *Service) sendBulkSummaries(ctx context.Context, b *bulkRow) error {
	if !b.Notify || b.Action == BulkDelete || b.Action == BulkWatch || b.Action == BulkUnwatch {
		return nil
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Once per bulk change, also when the job runs again.
		tag, err := tx.Exec(ctx, "UPDATE issue_bulk_changes SET summary_sent = true WHERE id = $1 AND NOT summary_sent", b.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		type line struct {
			key, summary string
		}
		rows, err := tx.Query(ctx, `SELECT r.uid, array_agg(coalesce(i.key, b.issue_key) ORDER BY b.position),
			array_agg(coalesce(i.summary, '') ORDER BY b.position)
			FROM issue_bulk_items b CROSS JOIN LATERAL unnest(b.recipients) AS r(uid) LEFT JOIN issues i ON i.id = b.issue_id
			WHERE b.change_id = $1 AND b.result = 'done' GROUP BY r.uid ORDER BY r.uid`, b.ID)
		if err != nil {
			return err
		}
		type recipient struct {
			id    int64
			lines []line
		}
		var list []recipient
		for rows.Next() {
			var r recipient
			var keys, summaries []string
			if err := rows.Scan(&r.id, &keys, &summaries); err != nil {
				rows.Close()
				return err
			}
			for k := range keys {
				r.lines = append(r.lines, line{keys[k], summaries[k]})
			}
			list = append(list, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		change, err := s.describeBulk(ctx, tx, b)
		if err != nil {
			return err
		}
		for _, r := range list {
			keys := make([]string, 0, len(r.lines))
			for _, l := range r.lines {
				keys = append(keys, l.key)
			}
			title := strings.Join(keys[:min(3, len(keys))], ", ")
			if len(keys) > 3 {
				title += fmt.Sprintf(", … (%d)", len(keys))
			}
			var summary strings.Builder
			summary.WriteString(change)
			for k, l := range r.lines {
				if k == 20 {
					summary.WriteString("\n…")
					break
				}
				summary.WriteString("\n" + l.key + " " + l.summary)
			}
			actor := b.actorID
			if err := notify.Create(ctx, tx, []int64{r.id}, notify.Notification{Category: notify.CategoryIssues,
				Event: "issue.bulk-" + bulkVerb(b.Action), EntityType: "issue-bulk", EntityID: itoa(b.ID), ActorID: &actor,
				Title: title, Summary: summary.String(), URL: "/issues/bulk/" + itoa(b.ID),
				Data: map[string]any{"count": len(keys), "action": b.Action}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// bulkVerb names an action as its notification event does.
func bulkVerb(action string) string {
	return map[string]string{BulkEdit: "edited", BulkTransition: "transitioned", BulkMove: "moved"}[action]
}

// describeBulk says what a bulk change does, in the words of the history (field: value).
func (s *Service) describeBulk(ctx context.Context, q db.Querier, b *bulkRow) (string, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return "", err
	}
	switch b.Action {
	case BulkTransition:
		if st := cfg.statuses[b.params.Transition.Status]; st != nil {
			return "status: " + st.Name, nil
		}
	case BulkMove:
		if proj, err := projectByKey(ctx, q, b.params.Move.Project); err == nil {
			return "project: " + proj.Name + " (" + proj.Key + ")", nil
		}
	case BulkEdit:
		e := b.params.Edit
		var parts []string
		userName := func(id int64) string {
			refs, err := identity.GetUserRefs(ctx, q, []int64{id})
			if err != nil {
				return ""
			}
			return refs[id].DisplayName
		}
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			raw := e.Fields[k]
			var v any
			_ = json.Unmarshal(raw, &v)
			text := "—"
			if v != nil {
				switch k {
				case FieldPriority:
					if p := cfg.priorities[anyID(v)]; p != nil {
						text = p.Name
					}
				case FieldAssignee, FieldReporter:
					text = userName(anyID(v))
				case FieldSprint:
					if sp, err := sprintByID(ctx, q, anyID(v), false); err == nil {
						text = sp.Name
					}
				case FieldSecurity:
					var name string
					if err := q.QueryRow(ctx, "SELECT name FROM security_levels WHERE id = $1", anyID(v)).Scan(&name); err == nil {
						text = name
					}
				default:
					text = fmt.Sprint(v)
					if id, ok := parseFieldKey(k); ok && cfg.fields[id] != nil {
						text = displayCustom(cfg.fields[id], v, userName, map[int64]string{})
						k = cfg.fields[id].Name
					}
				}
			}
			parts = append(parts, k+": "+text)
		}
		sign := map[string]string{"add": "+", "remove": "−", "replace": ""}
		if e.Labels != nil {
			parts = append(parts, FieldLabels+": "+sign[e.Labels.Mode]+strings.Join(e.Labels.Values, " "+sign[e.Labels.Mode]))
		}
		names := func(sql string, ids []int64) []string {
			rows, err := q.Query(ctx, sql, ids)
			if err != nil {
				return nil
			}
			out, _ := pgx.CollectRows(rows, pgx.RowTo[string])
			return out
		}
		if c := e.Components; c != nil {
			parts = append(parts, FieldComponents+": "+sign[c.Mode]+strings.Join(names(
				"SELECT name FROM components WHERE id = ANY($1) ORDER BY lower(name)", c.IDs), " "+sign[c.Mode]))
		}
		for k, c := range map[string]*IDsChange{FieldFixVersions: e.FixVersions, FieldAffectsVersions: e.AffectsVersions} {
			if c != nil {
				parts = append(parts, k+": "+sign[c.Mode]+strings.Join(names(
					"SELECT name FROM versions WHERE id = ANY($1) ORDER BY position, id", c.IDs), " "+sign[c.Mode]))
			}
		}
		return strings.Join(parts, "; "), nil
	}
	return "", nil
}
