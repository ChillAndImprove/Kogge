// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Merging pull requests without a working tree: by a merge commit, a squash, a fast-forward, or a
// rebase of the source commits onto the target (with or without a merge commit). Besides the merge checks, some
// strategies need something of the branches: a fast-forward a target without commits of its own, a rebase commits
// that apply one after another. People merge with Merge; auto-merge merges for the person who turned it on.

// maxRebaseCommits is the most commits a rebase merge applies: each one is a merge of its own, made while the merge
// holds the repository (some milliseconds each on a small repository, more on a large one).
const maxRebaseCommits = 250

// MergeInput selects how to merge.
type MergeInput struct {
	Strategy     string `json:"strategy,omitempty" enum:"merge,squash,fast-forward,rebase,rebase-merge," doc:"Default: the repository's first strategy"`
	Message      string `json:"message,omitempty" maxLength:"20000" doc:"Message of the merge commit (merge, rebase-merge) or of the squashed commit; default: generated. Rebases keep the messages of the commits."`
	DeleteSource *bool  `json:"deleteSource,omitempty" doc:"Delete the source branch; default: the repository setting"`
	Version      int    `json:"version,omitempty"`
}

// StrategyCheck says whether a strategy can merge the pull request as the branches are now, besides the merge
// checks (single open pull requests, for those who may merge).
type StrategyCheck struct {
	Strategy string   `json:"strategy" enum:"merge,squash,fast-forward,rebase,rebase-merge"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason" enum:"ok,diverged,conflicted,too-many" doc:"diverged: the target has commits the source does not have (fast-forward); conflicted: a commit of the source does not apply to the target (rebase); too-many: more commits than a rebase applies"`
	Commit   string   `json:"commit,omitempty" doc:"conflicted: the commit that does not apply"`
	Subject  string   `json:"subject,omitempty" doc:"conflicted: the subject of the commit"`
	Paths    []string `json:"paths,omitempty" doc:"conflicted: the files it conflicts in"`
	Limit    int      `json:"limit,omitempty" doc:"too-many: the most commits a rebase applies"`
	Message  string   `json:"message"`

	err *apperr.Error // the refusal of a merge with the strategy
}

// MergeMessages are the commit messages a merge writes unless it is given one (merge and rebase-merge write the
// merge message, squash the squash message).
type MergeMessages struct {
	Merge  string `json:"merge"`
	Squash string `json:"squash"`
}

// strategyOK is a strategy that can merge.
func strategyOK(strategy string) StrategyCheck {
	return StrategyCheck{Strategy: strategy, OK: true, Reason: "ok", Message: "The strategy can merge the pull request."}
}

func diverged() StrategyCheck {
	e := apperr.Conflict("pull-request.no-fast-forward", "The target branch has commits the source branch does not have; "+
		"merge or rebase first, or choose another strategy.")
	return StrategyCheck{Strategy: MergeFastForward, Reason: "diverged", Message: e.Detail, err: e}
}

// rebaseRefused is the check of a rebase that does not go through; strategy is set by who uses it.
func rebaseRefused(target string, conflict *git.ReplayConflict, tooMany bool) StrategyCheck {
	if tooMany {
		e := apperr.Conflict("pull-request.rebase-too-long", "The source branch has more than %d commits to rebase; rebase it yourself, "+
			"or choose another strategy.", maxRebaseCommits)
		return StrategyCheck{Reason: "too-many", Limit: maxRebaseCommits, Message: e.Detail, err: e}
	}
	paths := conflict.Paths
	shown := strings.Join(paths[:min(len(paths), 5)], ", ")
	if len(paths) > 5 {
		shown += ", …"
	}
	short := conflict.Commit.SHA[:min(len(conflict.Commit.SHA), 10)]
	e := apperr.Conflict("pull-request.rebase-conflict", "The commit %s (%s) cannot be rebased onto %s: it conflicts in %s. "+
		"Rebase the source branch yourself, or choose another strategy.", short, conflict.Commit.Subject(), target, shown)
	return StrategyCheck{Reason: "conflicted", Commit: conflict.Commit.SHA, Subject: conflict.Commit.Subject(), Paths: paths,
		Message: e.Detail, err: e}
}

// rebaseCache keeps what rebasing a source commit onto a target commit gives: the objects never change, so the
// answer never does either. Pages of pull requests ask for it whenever they are shown.
type rebaseCache struct {
	mu    sync.Mutex
	items map[string]*StrategyCheck // nil: the rebase goes through
	order []string
}

const rebaseCacheSize = 512

// rebaseKey identifies a rebase: the commits, and the target branch, which a refusal names.
func rebaseKey(repoID int64, target, targetSHA, sourceSHA string) string {
	return itoa(repoID) + ":" + targetSHA + ":" + sourceSHA + ":" + target
}

func (c *rebaseCache) get(key string) (*StrategyCheck, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *rebaseCache) put(key string, v *StrategyCheck) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*StrategyCheck{}
	}
	if _, ok := c.items[key]; !ok {
		c.order = append(c.order, key)
		if len(c.order) > rebaseCacheSize {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.items[key] = v
}

// rebase applies the source's commits to the target and returns the new tip (the target, if every change is in
// it already) and the number of commits made; a rebase that does not go through returns its check. A merge makes
// its commits anew (signed by who merges, now); only a refusal known already saves it the work.
func (s *Service) rebase(ctx context.Context, repo *git.Repo, repoID int64, target, targetSHA, sourceSHA string,
	committer git.Signature) (string, int, *StrategyCheck, error) {
	key := rebaseKey(repoID, target, targetSHA, sourceSHA)
	if c, ok := s.rebases.get(key); ok && c != nil {
		return "", 0, c, nil
	}
	shas, complete, err := repo.RebaseCommits(ctx, targetSHA, sourceSHA, maxRebaseCommits)
	if err != nil {
		return "", 0, nil, err
	}
	if !complete {
		c := rebaseRefused(target, nil, true)
		s.rebases.put(key, &c)
		return "", 0, &c, nil
	}
	commits, err := repo.ReadCommits(ctx, shas)
	if err != nil {
		return "", 0, nil, err
	}
	tip, made, err := repo.Replay(ctx, targetSHA, commits, committer)
	var conflict *git.ReplayConflict
	if errors.As(err, &conflict) {
		c := rebaseRefused(target, conflict, false)
		s.rebases.put(key, &c)
		return "", 0, &c, nil
	}
	if err != nil {
		return "", 0, nil, err
	}
	s.rebases.put(key, nil)
	return tip, made, nil, nil
}

// rebaseProbe signs the commits of rebases that only find out whether one goes through.
var rebaseProbe = git.Signature{Name: "Kogge", Email: "kogge@localhost"}

// rebaseCheck says whether the source's commits apply to the target one by one (nil: they do). It rebases once per
// pair of commits (and node): the answer cannot change, and pages of pull requests ask for it on every view.
func (s *Service) rebaseCheck(ctx context.Context, repo *git.Repo, repoID int64, target, targetSHA, sourceSHA string) (*StrategyCheck, error) {
	if c, ok := s.rebases.get(rebaseKey(repoID, target, targetSHA, sourceSHA)); ok {
		return c, nil
	}
	probe := rebaseProbe
	probe.When = now()
	_, _, c, err := s.rebase(ctx, repo, repoID, target, targetSHA, sourceSHA, probe)
	return c, err
}

// strategyChecks says for each strategy of the repository whether it can merge the pull request now.
func (s *Service) strategyChecks(ctx context.Context, r *repoRow, pr *prRow) ([]StrategyCheck, error) {
	repo := s.open(r.ID)
	out := []StrategyCheck{}
	var rebased *StrategyCheck // the rebase, done once for both rebasing strategies
	done := false
	for _, strategy := range r.Settings.MergeStrategies {
		switch strategy {
		case MergeFastForward:
			ff, err := repo.IsAncestor(ctx, pr.TargetSHA, pr.SourceSHA)
			if err != nil {
				return nil, err
			}
			if ff {
				out = append(out, strategyOK(strategy))
			} else {
				out = append(out, diverged())
			}
		case MergeRebase, MergeRebaseCommit:
			if !done {
				check, err := s.rebaseCheck(ctx, repo, r.ID, pr.Target, pr.TargetSHA, pr.SourceSHA)
				if err != nil {
					return nil, err
				}
				rebased, done = check, true
			}
			if rebased == nil {
				out = append(out, strategyOK(strategy))
			} else {
				c := *rebased
				c.Strategy = strategy
				out = append(out, c)
			}
		default:
			out = append(out, strategyOK(strategy))
		}
	}
	return out, nil
}

// mergeMessage is the message of a merge commit.
func mergeMessage(pr *prRow) string {
	message := fmt.Sprintf("Merge pull request #%d from %s into %s\n\n%s\n", pr.Number, pr.Source, pr.Target, pr.Title)
	if text := strings.TrimSpace(docmodel.PlainText(pr.Description)); text != "" {
		message += "\n" + text + "\n"
	}
	return message
}

// squashMessage is the message of a squashed commit: the title, the subjects of the commits, and the other authors
// of the commits as co-authors (the squash's author is the pull request's).
func (s *Service) squashMessage(ctx context.Context, repo *git.Repo, pr *prRow, sourceSHA, targetSHA string, author git.Signature) (string, error) {
	commits, err := repo.Log(ctx, git.LogOptions{Rev: sourceSHA, Exclude: []string{targetSHA}, Limit: 100, Reverse: true})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (#%d)\n\n", pr.Title, pr.Number)
	var coauthors []string
	seen := map[string]bool{strings.ToLower(author.Email): true}
	for _, c := range commits {
		b.WriteString("* " + c.Subject() + "\n")
		if email := strings.ToLower(c.Author.Email); email != "" && !seen[email] {
			seen[email] = true
			coauthors = append(coauthors, "Co-authored-by: "+c.Author.Name+" <"+c.Author.Email+">")
		}
	}
	if len(coauthors) > 0 {
		b.WriteString("\n" + strings.Join(coauthors, "\n") + "\n")
	}
	return b.String(), nil
}

// signature is how a user appears in commits Kogge writes for them.
func signature(u *identity.User) git.Signature {
	sig := git.Signature{Name: u.DisplayName, Email: u.Email, When: now()}
	if sig.Email == "" {
		sig.Email = u.Username + "@users.noreply"
	}
	return sig
}

// squashAuthor is the author of a squash: the pull request's author, if Kogge knows their address.
func squashAuthor(ctx context.Context, q db.Querier, pr *prRow, merger git.Signature) git.Signature {
	if pr.authorID != 0 {
		if u, err := identity.GetUser(ctx, q, pr.authorID); err == nil && u.Email != "" {
			return git.Signature{Name: u.DisplayName, Email: u.Email, When: now()}
		}
	}
	return merger
}

// mergeMessages are the messages a merge of the pull request by viewer writes unless given one.
func (s *Service) mergeMessages(ctx context.Context, r *repoRow, pr *prRow, viewer git.Signature) (*MergeMessages, error) {
	m := &MergeMessages{Merge: mergeMessage(pr)}
	if slices.Contains(r.Settings.MergeStrategies, MergeSquash) {
		author := squashAuthor(ctx, s.pool, pr, viewer)
		squash, err := s.squashMessage(ctx, s.open(r.ID), pr, pr.SourceSHA, pr.TargetSHA, author)
		if err != nil {
			return nil, err
		}
		m.Squash = squash
	}
	return m, nil
}

// Merge merges a pull request.
func (s *Service) Merge(ctx context.Context, p *identity.Principal, key, slug string, number int, in MergeInput) (*PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	if err := s.merge(ctx, a, p, r, number, in, nil); err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// mergeRefusal is why the pull request cannot be merged now: the merge checks that fail, or what the strategy needs.
// Clients get it as the error it wraps.
type mergeRefusal struct {
	checks   []MergeCheck   // the merge checks that fail
	strategy *StrategyCheck // the strategy's own condition (when the merge checks pass)
	err      *apperr.Error
}

func (m *mergeRefusal) Error() string { return m.err.Error() }
func (m *mergeRefusal) Unwrap() error { return m.err }

// errAutoMergeGone ends a merge by auto-merge that was turned off or changed meanwhile.
var errAutoMergeGone = errors.New("auto-merge is off or changed")

// merge merges a pull request of a repository the principal may write to. With auto, auto-merge merges for the
// person who turned it on: only while it is still on for the same source commit.
func (s *Service) merge(ctx context.Context, a *Access, p *identity.Principal, r *repoRow, number int, in MergeInput, auto *autoMerge) error {
	strategy := in.Strategy
	if strategy == "" {
		strategy = r.Settings.MergeStrategies[0]
	}
	if !slices.Contains(r.Settings.MergeStrategies, strategy) {
		return apperr.Invalidf("pull-request.strategy", "The repository does not allow the strategy %s.", strategy)
	}
	deleteSource := r.Settings.DeleteSourceBranch
	if in.DeleteSource != nil {
		deleteSource = *in.DeleteSource
	}
	repo := s.open(r.ID)
	release, err := s.writing(ctx)
	if err != nil {
		return err
	}
	defer release()
	var updates []RefUpdate
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		unlock, err := s.lock(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		defer unlock()
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
		if auto != nil {
			var by int64
			var source string
			err := tx.QueryRow(ctx, "SELECT enabled_by, source_sha FROM pull_request_auto_merges WHERE pull_request_id = $1 FOR UPDATE",
				pr.ID).Scan(&by, &source)
			if db.IsNoRows(err) || err == nil && (by != auto.enabledBy || source != auto.sourceSHA || source != pr.SourceSHA) {
				return errAutoMergeGone
			}
			if err != nil {
				return err
			}
		}
		// The branches as they are now (a push may have been processed later).
		sourceSHA, err := repo.ResolveBranch(ctx, pr.Source)
		if err != nil {
			return apperr.Conflict("pull-request.source-gone", "The source branch %s does not exist any more.", pr.Source)
		}
		targetSHA, err := repo.ResolveBranch(ctx, pr.Target)
		if err != nil {
			return apperr.Conflict("pull-request.target-gone", "The target branch %s does not exist any more.", pr.Target)
		}
		if sourceSHA != pr.SourceSHA {
			return apperr.Conflict("pull-request.outdated", "The source branch changed; reload the pull request and review the new commits.")
		}
		pr.TargetSHA = targetSHA
		if err := s.completePRs(ctx, a, r, []*prRow{pr}); err != nil {
			return err
		}
		tree, conflicts, err := repo.MergeTree(ctx, targetSHA, sourceSHA)
		if err != nil {
			return err
		}
		pr.Conflicted = len(conflicts) > 0
		own, err := s.prCodeOwners(ctx, pr)
		if err != nil {
			return err
		}
		var failing []MergeCheck
		var reasons apperr.Messages
		for _, c := range s.mergeChecks(ctx, r, pr, own) {
			if !c.OK {
				failing = append(failing, c)
				reasons = append(reasons, c.text...)
			}
		}
		if len(failing) > 0 {
			return &mergeRefusal{checks: failing, err: apperr.Conflict("pull-request.checks", "The pull request cannot be merged: %s", reasons)}
		}
		// Restrictions of the target branch: pull-request-only allows merges, restrict-push does not.
		rules, err := s.branchPermissions(ctx, tx, r.ProjectID, r.ID)
		if err != nil {
			return err
		}
		for _, b := range rules {
			if b.Kind == RestrictPush && matchRef(b.Pattern, "refs/heads/"+pr.Target) && !exempt(b, p) {
				return apperr.Forbidden("pull-request.branch-restricted", "You may not change %s (branch permission %q).", pr.Target, b.Pattern)
			}
		}
		me := signature(p.User)
		mergeSHA, made := "", 0
		switch strategy {
		case MergeFastForward:
			ff, err := repo.IsAncestor(ctx, targetSHA, sourceSHA)
			if err != nil {
				return err
			}
			if !ff {
				c := diverged()
				return &mergeRefusal{strategy: &c, err: c.err}
			}
			mergeSHA = sourceSHA
		case MergeSquash:
			author := squashAuthor(ctx, tx, pr, me)
			message := in.Message
			if message == "" {
				if message, err = s.squashMessage(ctx, repo, pr, sourceSHA, targetSHA, author); err != nil {
					return err
				}
			}
			if mergeSHA, err = repo.CommitTree(ctx, tree, []string{targetSHA}, message, author, me); err != nil {
				return err
			}
		case MergeRebase, MergeRebaseCommit:
			tip, n, check, err := s.rebase(ctx, repo, r.ID, pr.Target, targetSHA, sourceSHA, me)
			if err != nil {
				return err
			}
			if check != nil {
				c := *check
				c.Strategy = strategy
				return &mergeRefusal{strategy: &c, err: c.err}
			}
			mergeSHA, made = tip, n
			if strategy == MergeRebaseCommit && tip != targetSHA {
				message := in.Message
				if message == "" {
					message = mergeMessage(pr)
				}
				rebasedTree, err := repo.TreeOf(ctx, tip)
				if err != nil {
					return err
				}
				// The rebased commits on a side line, joined by a merge commit: a history that stays linear.
				if mergeSHA, err = repo.CommitTree(ctx, rebasedTree, []string{targetSHA, tip}, message, me, me); err != nil {
					return err
				}
			}
		default:
			message := in.Message
			if message == "" {
				message = mergeMessage(pr)
			}
			if mergeSHA, err = repo.CommitTree(ctx, tree, []string{targetSHA, sourceSHA}, message, me, me); err != nil {
				return err
			}
		}
		// A rebase whose commits are all in the target already leaves it as it is.
		if mergeSHA != targetSHA {
			if err := repo.UpdateRef(ctx, "refs/heads/"+pr.Target, mergeSHA, targetSHA, "kogge: merge pull request #"+strconv.Itoa(pr.Number)); err != nil {
				if errors.Is(err, git.ErrStale) {
					return apperr.Conflict("pull-request.target-moved", "The target branch changed during the merge; try again.")
				}
				return err
			}
			updates = append(updates, RefUpdate{Ref: "refs/heads/" + pr.Target, Old: targetSHA, New: mergeSHA, FastForward: true})
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'merged', merge_sha = $2, target_sha = $3, closed_by = $4, closed_at = now(),
			updated_at = now(), version = version + 1 WHERE id = $1`, pr.ID, mergeSHA, targetSHA, p.UserID()); err != nil {
			return err
		}
		// A merge ends the pull request's auto-merge, whoever merges.
		if _, err := tx.Exec(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1", pr.ID); err != nil {
			return err
		}
		data := map[string]any{"strategy": strategy, "commit": mergeSHA}
		if strategy == MergeRebase || strategy == MergeRebaseCommit {
			data["commits"] = made
		}
		if auto != nil {
			data["auto"] = true
		}
		if err := s.activity(ctx, tx, pr.ID, p.UserID(), "merged", data); err != nil {
			return err
		}
		if deleteSource && pr.Source != r.DefaultBranch {
			del := RefUpdate{Ref: "refs/heads/" + pr.Source, Old: sourceSHA, New: strings.Repeat("0", len(sourceSHA))}
			if problems, err := s.checkPush(ctx, &pushContext{repoID: r.ID, principal: p}, []RefUpdate{del}); err == nil && len(problems) == 0 {
				if err := repo.DeleteRef(ctx, del.Ref, sourceSHA); err == nil {
					updates = append(updates, del)
				}
			}
		}
		if auto != nil {
			// Nobody clicked: everyone who follows the pull request hears of it, also the one who turned auto-merge on.
			if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.auto-merged", 0, []int64{auto.enabledBy, pr.authorID}); err != nil {
				return err
			}
		} else if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.merged", p.UserID(), nil); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.merged", RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return err
	}
	// The merge changed the target like a push: record it, so that it is processed the same way.
	if len(updates) > 0 {
		if _, err := s.recordPush(ctx, &pushContext{repoID: r.ID, principal: p, protocol: "merge"}, updates); err != nil {
			s.log.Error("recording a merge", "repository", r.ID, "error", err)
		}
	}
	return nil
}
