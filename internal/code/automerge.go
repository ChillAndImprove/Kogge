// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
)

// Auto-merge ("merge when ready"): someone who may merge a pull request turns it on with a strategy, and Kogge
// merges for them as soon as every merge check passes — the checks of the merge button, looked at again whenever
// something they depend on changes: builds and statuses of the source commit, reviews, tasks, pushes to either
// branch, the pull request itself and the repository's settings (and every ten minutes, for what has no event, such
// as permissions). A job merges, on whichever node runs it, under the locks of a merge by hand and only while
// auto-merge is still on for the source commit it is on for: it never merges twice, nor commits nobody saw.
//
// What ends it, with a notice to who turned it on and the author: a push to the source branch by anyone else (they
// did not see those commits); another target branch; losing the permission to merge (or the account); an archived
// repository, a strategy the repository no longer allows, a branch permission that forbids the merge. Declining
// and merging end it, too. What does not end it: their own pushes (they keep working on it), pushes to the target,
// checks that fail. Conflicts, failed builds, reviewers asking for more work and strategies that cannot merge the
// branches as they are block it: someone has to act, so both hear of it once, and it merges when the checks pass
// again — nobody has to remember to turn it back on, and nothing merges that the person did not agree to.

// AutoMerge is the auto-merge of a pull request.
type AutoMerge struct {
	EnabledBy    *identity.UserRef `json:"enabledBy,omitempty"`
	Strategy     string            `json:"strategy" enum:"merge,squash,fast-forward,rebase,rebase-merge"`
	Message      string            `json:"message,omitempty"`
	DeleteSource *bool             `json:"deleteSource,omitempty"`
	Since        time.Time         `json:"since"`
	Blocked      string            `json:"blocked,omitempty" enum:"conflicts,rebase,fast-forward,builds,needs-work," doc:"What keeps it from merging and needs someone to act: conflicts; rebase (a commit does not apply); fast-forward (the target has other commits); builds (failed); needs-work. Empty while it waits for checks that are pending."`
}

// autoMerge is the stored auto-merge of a pull request.
type autoMerge struct {
	prID         int64
	enabledBy    int64
	strategy     string
	message      string
	deleteSource *bool
	sourceSHA    string // the source commit it is on for: the one it was turned on for, or a later push of theirs
	blocked      string
}

// Reasons why auto-merge ends without being turned off.
const (
	autoMergePushed     = "pushed"     // someone else pushed to the source branch
	autoMergeTarget     = "target"     // the pull request got another target branch
	autoMergePermission = "permission" // who turned it on may no longer merge
	autoMergeArchived   = "archived"
	autoMergeStrategy   = "strategy"   // the repository no longer allows the strategy
	autoMergeRestricted = "restricted" // a branch permission forbids them to change the target
	autoMergeFailed     = "failed"     // the merge failed otherwise
)

// AutoMergeInput turns auto-merge on.
type AutoMergeInput struct {
	Strategy     string `json:"strategy,omitempty" enum:"merge,squash,fast-forward,rebase,rebase-merge," doc:"Default: the repository's first strategy"`
	Message      string `json:"message,omitempty" maxLength:"20000" doc:"As for merging"`
	DeleteSource *bool  `json:"deleteSource,omitempty" doc:"Delete the source branch; default: the repository setting when it merges"`
	Version      int    `json:"version,omitempty" doc:"The version of the pull request the caller saw"`
}

func loadAutoMerge(ctx context.Context, q db.Querier, prID int64, forUpdate bool) (*autoMerge, error) {
	sql := `SELECT pull_request_id, enabled_by, strategy, message, delete_source, source_sha, coalesce(blocked, '')
		FROM pull_request_auto_merges WHERE pull_request_id = $1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var am autoMerge
	err := q.QueryRow(ctx, sql, prID).Scan(&am.prID, &am.enabledBy, &am.strategy, &am.message, &am.deleteSource, &am.sourceSHA, &am.blocked)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &am, err
}

// EnableAutoMerge turns on auto-merge (who may merge): Kogge merges as soon as every merge check passes.
func (s *Service) EnableAutoMerge(ctx context.Context, p *identity.Principal, key, slug string, number int, in AutoMergeInput) (*PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	strategy := in.Strategy
	if strategy == "" {
		strategy = r.Settings.MergeStrategies[0]
	}
	if !slices.Contains(r.Settings.MergeStrategies, strategy) {
		return nil, apperr.Invalidf("pull-request.strategy", "The repository does not allow the strategy %s.", strategy)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr, err := s.loadPR(ctx, tx, r.ID, number, true)
		if err != nil {
			return err
		}
		if pr.State != StateOpen {
			return prClosed(pr.State)
		}
		if in.Version != 0 && in.Version != pr.Version {
			return apperr.Conflict("pull-request.changed", "The pull request changed in the meantime; reload it.")
		}
		// It is on for the source commit the person saw: later pushes keep it only if they are theirs.
		if _, err := tx.Exec(ctx, `INSERT INTO pull_request_auto_merges (pull_request_id, enabled_by, strategy, message, delete_source, source_sha)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (pull_request_id) DO UPDATE SET enabled_by = EXCLUDED.enabled_by,
			strategy = EXCLUDED.strategy, message = EXCLUDED.message, delete_source = EXCLUDED.delete_source, source_sha = EXCLUDED.source_sha,
			blocked = NULL, created_at = now(), updated_at = now()`, pr.ID, p.UserID(), strategy, in.Message, in.DeleteSource, pr.SourceSHA); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "pull-request", pr.ID, p.UserID()); err != nil {
			return err
		}
		if err := s.activity(ctx, tx, pr.ID, p.UserID(), "auto-merge-enabled", map[string]any{"strategy": strategy}); err != nil {
			return err
		}
		if err := s.notifyUsers(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.auto-merge-enabled", p.UserID(),
			[]int64{pr.authorID}); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.auto-merge", RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// DisableAutoMerge turns auto-merge off: who turned it on, the author of the pull request and who can write to the
// repository.
func (s *Service) DisableAutoMerge(ctx context.Context, p *identity.Principal, key, slug string, number int) (*PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr, err := s.loadPR(ctx, tx, r.ID, number, true)
		if err != nil {
			return err
		}
		am, err := loadAutoMerge(ctx, tx, pr.ID, true)
		if err != nil {
			return err
		}
		own := p.UserID() != 0 && (p.UserID() == pr.authorID || am != nil && p.UserID() == am.enabledBy)
		if !own && a.Level(r.ProjectID, r.ID) < LevelWrite {
			return apperr.Forbidden("pull-request.auto-merge-denied",
				"Only who turned auto-merge on, the author and who can write to the repository can turn it off.")
		}
		if am == nil {
			return nil
		}
		return s.endAutoMerge(ctx, tx, r, pr, "user", p.UserID())
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// endAutoMerge ends a pull request's auto-merge (if it is on) and tells who turned it on and the author why: they
// turned it off themselves (reason user), or something else ended it (actor: who caused that, if anyone did).
func (s *Service) endAutoMerge(ctx context.Context, tx pgx.Tx, r *repoRow, pr *prRow, reason string, actor int64) error {
	var by int64
	err := tx.QueryRow(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1 RETURNING enabled_by", pr.ID).Scan(&by)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.activity(ctx, tx, pr.ID, actor, "auto-merge-cancelled", map[string]any{"reason": reason}); err != nil {
		return err
	}
	if err := s.notifyUsers(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.auto-merge-cancelled", actor, []int64{by, pr.authorID}); err != nil {
		return err
	}
	return s.publish(ctx, tx, RepoEvent{Name: "pull-request.auto-merge", RepoID: r.ID, PRID: pr.ID, ActorID: actor})
}

// autoMergePushed follows a push of pusher (0: a deploy key) that moved a pull request's source branch (update u):
// auto-merge stays on only for commits that who turned it on pushed, one push after the other from the commit it was
// on for; a push of anyone else ends it — they did not see those commits. Each push is looked at once, in the job
// of its processing: one that does not start at the commit auto-merge is on for came after another push whose
// processing is still to come (or done it already), and that processing goes on through the pushes recorded after
// its own.
func (s *Service) autoMergePushed(ctx context.Context, tx pgx.Tx, r *repoRow, pr *prRow, pushID, pusher int64, u RefUpdate) error {
	am, err := loadAutoMerge(ctx, tx, pr.ID, true)
	if err != nil || am == nil || u.Old != am.sourceSHA {
		return err
	}
	head := u.New
	if pusher == 0 || pusher != am.enabledBy {
		return s.endAutoMerge(ctx, tx, r, pr, autoMergePushed, pusher)
	}
	filter, _ := json.Marshal([]map[string]string{{"ref": u.Ref}})
	rows, err := tx.Query(ctx, `SELECT coalesce(user_id, 0), updates FROM pushes WHERE id > $1 AND repository_id = $2 AND updates @> $3
		ORDER BY id`, pushID, r.ID, filter)
	if err != nil {
		return err
	}
	type later struct {
		user    int64
		updates []RefUpdate
	}
	var list []later
	for rows.Next() {
		var l later
		var raw []byte
		if err := rows.Scan(&l.user, &raw); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(raw, &l.updates)
		list = append(list, l)
	}
	rows.Close()
	for _, l := range list {
		for _, next := range l.updates {
			if next.Ref != u.Ref || next.Old != head {
				continue
			}
			if l.user == 0 || l.user != am.enabledBy {
				return s.endAutoMerge(ctx, tx, r, pr, autoMergePushed, l.user)
			}
			head = next.New
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE pull_request_auto_merges SET source_sha = $2, updated_at = now() WHERE pull_request_id = $1",
		pr.ID, head); err != nil {
		return err
	}
	// It is on for their newest commit now: look again (also when the pull request was up to date already).
	return s.publish(ctx, tx, RepoEvent{Name: "pull-request.auto-merge", RepoID: r.ID, PRID: pr.ID, ActorID: pusher})
}

// autoMergeEvents are the events after which an auto-merge is looked at again: the pull request, its reviews and
// tasks changed, or its branches moved (which updates it).
var autoMergeEvents = map[string]bool{"pull-request.updated": true, "pull-request.reviewed": true, "pull-request.commented": true,
	"pull-request.auto-merge": true, "pull-request.checks": true}

// autoMergeDue asks for a pull request's auto-merge to be looked at.
type autoMergeDue struct{ prID int64 }

// EventName implements events.Event.
func (autoMergeDue) EventName() string { return "pull-request.auto-merge-due" }

// RecheckAutoMerge looks at a pull request's auto-merge again: something outside the code module changed its checks
// (the builds module recorded the statuses of skipped pipelines, for one).
func (s *Service) RecheckAutoMerge(ctx context.Context, prID int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var repoID int64
		if err := tx.QueryRow(ctx, "SELECT repository_id FROM pull_requests WHERE id = $1", prID).Scan(&repoID); err != nil {
			if db.IsNoRows(err) {
				return nil
			}
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.checks", RepoID: repoID, PRID: prID})
	})
}

// dueAutoMerges asks for the auto-merges of the open pull requests of a repository (0: of all) to be looked at.
func (s *Service) dueAutoMerges(ctx context.Context, repoID int64) error {
	rows, err := s.pool.Query(ctx, `SELECT am.pull_request_id FROM pull_request_auto_merges am JOIN pull_requests pr ON pr.id = am.pull_request_id
		WHERE pr.state = 'open' AND ($1 = 0 OR pr.repository_id = $1) ORDER BY am.pull_request_id`, repoID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		evs := make([]events.Event, len(ids))
		for i, id := range ids {
			evs[i] = autoMergeDue{prID: id}
		}
		return s.bus.Publish(ctx, tx, evs...)
	})
}

// AutoMergeArgs looks at the auto-merge of a pull request: it merges if every check passes.
type AutoMergeArgs struct {
	PRID int64 `json:"prId"`
}

// Kind implements river.JobArgs.
func (AutoMergeArgs) Kind() string { return "code.auto-merge" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (AutoMergeArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueGit} }

type autoMergeWorker struct {
	river.WorkerDefaults[AutoMergeArgs]
	s *Service
}

func (w *autoMergeWorker) Work(ctx context.Context, job *river.Job[AutoMergeArgs]) error {
	return w.s.runAutoMerge(ctx, job.Args.PRID)
}

// AutoMergeRepoArgs looks at the auto-merges of a repository's pull requests (the builds of a commit changed).
type AutoMergeRepoArgs struct {
	RepoID int64 `json:"repoId"`
}

// Kind implements river.JobArgs.
func (AutoMergeRepoArgs) Kind() string { return "code.auto-merge-repository" }

type autoMergeRepoWorker struct {
	river.WorkerDefaults[AutoMergeRepoArgs]
	s *Service
}

func (w *autoMergeRepoWorker) Work(ctx context.Context, job *river.Job[AutoMergeRepoArgs]) error {
	return w.s.dueAutoMerges(ctx, job.Args.RepoID)
}

// AutoMergeSweepArgs looks at all auto-merges: for changes without an event, such as lost permissions.
type AutoMergeSweepArgs struct{}

// Kind implements river.JobArgs.
func (AutoMergeSweepArgs) Kind() string { return "code.auto-merge-sweep" }

type autoMergeSweepWorker struct {
	river.WorkerDefaults[AutoMergeSweepArgs]
	s *Service
}

func (w *autoMergeSweepWorker) Work(ctx context.Context, _ *river.Job[AutoMergeSweepArgs]) error {
	return w.s.dueAutoMerges(ctx, 0)
}

// registerAutoMerge adds the jobs of auto-merge.
func (s *Service) registerAutoMerge(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &autoMergeWorker{s: s})
	river.AddWorker(reg.Workers, &autoMergeRepoWorker{s: s})
	river.AddWorker(reg.Workers, &autoMergeSweepWorker{s: s})
	reg.Every(10*time.Minute, AutoMergeSweepArgs{}, nil)
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(autoMergeDue); ok {
			return AutoMergeArgs{PRID: e.prID}, nil
		}
		return nil, nil
	}, "pull-request.auto-merge-due")
	// New settings (fewer approvals, another strategy) may let pull requests merge, or end their auto-merge.
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(RepoEvent); ok && e.PRID == 0 {
			return AutoMergeRepoArgs{RepoID: e.RepoID}, nil
		}
		return nil, nil
	}, "repository.updated")
}

// runAutoMerge merges a pull request for who turned auto-merge on, if every check passes; otherwise it records what
// it waits for.
func (s *Service) runAutoMerge(ctx context.Context, prID int64) error {
	am, err := loadAutoMerge(ctx, s.pool, prID, false)
	if err != nil || am == nil {
		return err
	}
	var repoID int64
	var number int
	if err := s.pool.QueryRow(ctx, "SELECT repository_id, number FROM pull_requests WHERE id = $1", prID).Scan(&repoID, &number); err != nil {
		if db.IsNoRows(err) {
			return nil
		}
		return err
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return nil // deleted meanwhile
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return err
	}
	// end ends the auto-merge looked at — not one turned on again meanwhile, which its own event looks at.
	end := func(reason string) error {
		return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			cur, err := loadAutoMerge(ctx, tx, prID, true)
			if err != nil || cur == nil || cur.enabledBy != am.enabledBy || cur.strategy != am.strategy || cur.sourceSHA != am.sourceSHA {
				return err
			}
			return s.endAutoMerge(ctx, tx, r, pr, reason, 0)
		})
	}
	if pr.State != StateOpen {
		_, err := s.pool.Exec(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1", prID)
		return err
	}
	// The merge is theirs: with their permissions as they are now.
	p, err := s.identity.PrincipalOf(ctx, am.enabledBy, identity.MethodInternal)
	if err != nil {
		var e *apperr.Error
		if errors.As(err, &e) || db.IsNoRows(err) {
			return end(autoMergePermission)
		}
		return err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	switch {
	case a.Level(r.ProjectID, r.ID) < LevelWrite:
		return end(autoMergePermission)
	case r.Archived:
		return end(autoMergeArchived)
	case !slices.Contains(r.Settings.MergeStrategies, am.strategy):
		return end(autoMergeStrategy)
	case pr.SourceSHA != am.sourceSHA:
		// A push still to be processed: its processing decides whether auto-merge stays on, and looks again.
		return nil
	}
	// What the checks say now, without holding the repository: only a merge that can go through takes it.
	if err := s.completePRs(ctx, a, r, []*prRow{pr}); err != nil {
		return err
	}
	own, err := s.prCodeOwners(ctx, pr)
	if err != nil {
		return err
	}
	var failing []MergeCheck
	for _, c := range s.mergeChecks(ctx, r, pr, own) {
		if !c.OK {
			failing = append(failing, c)
		}
	}
	if len(failing) > 0 {
		return s.autoMergeWaits(ctx, r, pr, am, blockedBy(failing, nil))
	}
	if am.strategy == MergeFastForward {
		if ff, err := s.open(r.ID).IsAncestor(ctx, pr.TargetSHA, pr.SourceSHA); err != nil {
			return err
		} else if !ff {
			c := diverged()
			return s.autoMergeWaits(ctx, r, pr, am, blockedBy(nil, &c))
		}
	}
	err = s.merge(ctx, a, p, r, pr.Number, MergeInput{Strategy: am.strategy, Message: am.message, DeleteSource: am.deleteSource}, am)
	var refusal *mergeRefusal
	var e *apperr.Error
	switch {
	case err == nil, errors.Is(err, errAutoMergeGone):
		return nil
	case errors.As(err, &refusal):
		return s.autoMergeWaits(ctx, r, pr, am, blockedBy(refusal.checks, refusal.strategy))
	case apperr.Is(err, "pull-request.closed"), apperr.Is(err, "pull-request.outdated"), apperr.Is(err, "pull-request.source-gone"),
		apperr.Is(err, "pull-request.target-gone"):
		return nil // merged or declined meanwhile, or a push that its processing looks at
	case apperr.Is(err, "pull-request.target-moved"):
		return err // tried again shortly
	case apperr.Is(err, "code.paused"):
		return river.JobSnooze(time.Minute) // a backup is running
	case apperr.Is(err, "pull-request.branch-restricted"):
		return end(autoMergeRestricted)
	case errors.As(err, &e):
		s.log.Warn("auto-merge failed", "pull_request", prID, "error", err)
		return end(autoMergeFailed)
	}
	return err
}

// blockedBy says what of the failing checks (or the strategy) needs someone to act; "" if they may pass by
// themselves (pending builds, approvals, tasks, code owners, a draft).
func blockedBy(failing []MergeCheck, strategy *StrategyCheck) string {
	reasons := map[string]bool{}
	for _, c := range failing {
		switch {
		case c.Key == "conflicts":
			reasons["conflicts"] = true
		case c.Key == "builds" && c.Reason == "failed":
			reasons["builds"] = true
		case c.Key == "needs-work":
			reasons["needs-work"] = true
		}
	}
	if strategy != nil {
		if strategy.Strategy == MergeFastForward {
			reasons["fast-forward"] = true
		} else {
			reasons["rebase"] = true
		}
	}
	for _, r := range []string{"conflicts", "rebase", "fast-forward", "builds", "needs-work"} {
		if reasons[r] {
			return r
		}
	}
	return ""
}

// autoMergeWaits records why an auto-merge does not merge yet; when something new blocks it, who turned it on and
// the author hear of it.
func (s *Service) autoMergeWaits(ctx context.Context, r *repoRow, pr *prRow, am *autoMerge, blocked string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_request_auto_merges SET blocked = nullif($3, ''), updated_at = now()
			WHERE pull_request_id = $1 AND enabled_by = $2 AND coalesce(blocked, '') <> $3`, pr.ID, am.enabledBy, blocked)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if blocked != "" {
			if err := s.activity(ctx, tx, pr.ID, 0, "auto-merge-blocked", map[string]any{"reason": blocked}); err != nil {
				return err
			}
			if err := s.notifyUsers(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.auto-merge-blocked", 0,
				[]int64{am.enabledBy, pr.authorID}); err != nil {
				return err
			}
		}
		// Only the page: looking at the auto-merge again would change nothing.
		return events.NotifyLive(ctx, tx, RepoEvent{RepoID: r.ID, PRID: pr.ID}.LiveMessage())
	})
}
