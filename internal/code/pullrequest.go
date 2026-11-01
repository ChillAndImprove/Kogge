// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/code/gitdiff"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Pull request states and review states.
const (
	StateOpen     = "open"
	StateMerged   = "merged"
	StateDeclined = "declined"

	ReviewUnapproved = "unapproved"
	ReviewApproved   = "approved"
	ReviewNeedsWork  = "needs-work"
)

// RepoRef identifies a repository.
type RepoRef struct {
	ID         int64  `json:"id"`
	ProjectKey string `json:"projectKey"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
}

// Reviewer is a reviewer of a pull request with the review state.
type Reviewer struct {
	User      identity.UserRef `json:"user"`
	State     string           `json:"state" enum:"unapproved,approved,needs-work"`
	CodeOwner bool             `json:"codeOwner,omitempty" doc:"Owns changed files (single pull requests)"`
}

// MergeCheck is a condition for merging. Clients word it from its key, reason and values; the message words it in
// English (also for the error of a refused merge).
type MergeCheck struct {
	Key    string `json:"key" enum:"draft,conflicts,approvals,needs-work,tasks,builds,code-owners"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason" enum:"draft,clean,conflicted,count,none,some,resolved,open,passed,missing,running,failed,unavailable,approved,waiting,unknown" doc:"The state of the check — draft: draft; conflicts: clean, conflicted; approvals: count; needs-work: none, some; tasks: resolved, open; builds: passed, missing, running, failed, unavailable; code-owners: none, approved, waiting, unknown"`
	// Values are the numbers of the reason: approved and required (approvals); count (needs-work, tasks, and the
	// files waiting for code owners); done, required, successful, skipped, running and failed (builds; done is
	// successful and skipped).
	Values  map[string]int `json:"values,omitempty"`
	Message string         `json:"message"`

	text apperr.Messages // the message, to be translated in errors
}

// say sets the message of a check (sentences, translated where errors name the check).
func (c MergeCheck) say(m ...apperr.Message) MergeCheck {
	c.text = m
	c.Message = c.text.String()
	return c
}

// plural chooses the message for a number: one for 1, other (with the number) for the rest.
func plural(n int, one, other apperr.Message) apperr.Message {
	if n == 1 {
		return one
	}
	return other
}

// PullRequestPermissions say what the caller may do.
type PullRequestPermissions struct {
	Edit    bool `json:"edit"`
	Merge   bool `json:"merge" doc:"Merge, and turn auto-merge on"`
	Decline bool `json:"decline"`
	Review  bool `json:"review"`
	Comment bool `json:"comment"`
	// DisableAutoMerge: who turned it on, the author and who can write.
	DisableAutoMerge bool `json:"disableAutoMerge" doc:"Turn auto-merge off (who turned it on, the author and who can write)"`
}

// PullRequest is a request to merge a source branch into a target branch.
type PullRequest struct {
	ID             int64                  `json:"id"`
	Number         int                    `json:"number"`
	Repository     RepoRef                `json:"repository"`
	Title          string                 `json:"title"`
	Description    *docmodel.Doc          `json:"description"`
	DescriptionMD  string                 `json:"descriptionMarkdown,omitempty" doc:"The description as Markdown, to change it (single pull requests the caller may edit)"`
	Author         *identity.UserRef      `json:"author,omitempty"`
	Source         string                 `json:"source"`
	Target         string                 `json:"target"`
	SourceSHA      string                 `json:"sourceSha"`
	TargetSHA      string                 `json:"targetSha"`
	MergeBase      string                 `json:"mergeBase"`
	State          string                 `json:"state" enum:"open,merged,declined"`
	Draft          bool                   `json:"draft"`
	Conflicted     bool                   `json:"conflicted"`
	MergeSHA       string                 `json:"mergeSha,omitempty"`
	ClosedBy       *identity.UserRef      `json:"closedBy,omitempty"`
	Closed         *time.Time             `json:"closed,omitempty"`
	Created        time.Time              `json:"created"`
	Updated        time.Time              `json:"updated"`
	Version        int                    `json:"version"`
	Reviewers      []Reviewer             `json:"reviewers"`
	Comments       int                    `json:"comments"`
	OpenTasks      int                    `json:"openTasks"`
	ResolvedTasks  int                    `json:"resolvedTasks"`
	Checks         []MergeCheck           `json:"checks,omitempty" doc:"Merge checks (single pull requests)"`
	Strategies     []StrategyCheck        `json:"strategies,omitempty" doc:"Whether each strategy of the repository can merge the branches as they are (single open pull requests the caller may merge)"`
	MergeMessages  *MergeMessages         `json:"mergeMessages,omitempty" doc:"The commit messages a merge writes unless given one (single open pull requests the caller may merge)"`
	AutoMerge      *AutoMerge             `json:"autoMerge,omitempty" doc:"Set while auto-merge is on: the pull request merges when every merge check passes"`
	CodeOwners     []*OwnedFiles          `json:"codeOwners,omitempty" doc:"The changed files by their code owners (single open pull requests)"`
	CodeOwnersFile string                 `json:"codeOwnersFile,omitempty" doc:"The target's CODEOWNERS file (single open pull requests)"`
	Permissions    PullRequestPermissions `json:"permissions"`
}

type prRow struct {
	PullRequest
	repoID   int64
	authorID int64
	autoBy   int64 // who turned auto-merge on
}

const prColumns = `pr.id, pr.number, pr.repository_id, p.key, r.slug, r.name, pr.title, pr.description, coalesce(pr.author_id, 0),
	pr.source_branch, pr.target_branch, pr.source_sha, pr.target_sha, pr.merge_base, pr.state, pr.draft, pr.conflicted,
	coalesce(pr.merge_sha, ''), pr.closed_by, pr.closed_at, pr.created_at, pr.updated_at, pr.version,
	(SELECT count(*) FROM pull_request_comments c WHERE c.pull_request_id = pr.id),
	(SELECT count(*) FROM pull_request_comments c WHERE c.pull_request_id = pr.id AND c.task AND c.resolved_at IS NULL),
	(SELECT count(*) FROM pull_request_comments c WHERE c.pull_request_id = pr.id AND c.task AND c.resolved_at IS NOT NULL),
	coalesce(am.enabled_by, 0), coalesce(am.strategy, ''), coalesce(am.message, ''), am.delete_source, am.created_at, coalesce(am.blocked, '')`

const prFrom = ` FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id JOIN code_projects p ON p.id = r.project_id
	LEFT JOIN pull_request_auto_merges am ON am.pull_request_id = pr.id`

func scanPR(row pgx.Row) (*prRow, error) {
	var pr prRow
	var desc []byte
	var closedBy *int64
	var auto AutoMerge
	var since *time.Time
	if err := row.Scan(&pr.ID, &pr.Number, &pr.repoID, &pr.Repository.ProjectKey, &pr.Repository.Slug, &pr.Repository.Name, &pr.Title,
		&desc, &pr.authorID, &pr.Source, &pr.Target, &pr.SourceSHA, &pr.TargetSHA, &pr.MergeBase, &pr.State, &pr.Draft, &pr.Conflicted,
		&pr.MergeSHA, &closedBy, &pr.Closed, &pr.Created, &pr.Updated, &pr.Version, &pr.Comments, &pr.OpenTasks, &pr.ResolvedTasks,
		&pr.autoBy, &auto.Strategy, &auto.Message, &auto.DeleteSource, &since, &auto.Blocked); err != nil {
		return nil, err
	}
	pr.Repository.ID = pr.repoID
	pr.Description = &docmodel.Doc{}
	if json.Unmarshal(desc, pr.Description) != nil {
		pr.Description = docmodel.Empty()
	}
	if closedBy != nil {
		pr.ClosedBy = &identity.UserRef{ID: *closedBy}
	}
	if since != nil {
		auto.Since = *since
		pr.AutoMerge = &auto
	}
	return &pr, nil
}

func prNotFound(number int) error {
	return apperr.NotFound("pull-request.not-found", "Pull request #%d does not exist or you may not see it.", number)
}

// complete adds users, reviewers and permissions.
func (s *Service) completePRs(ctx context.Context, a *Access, r *repoRow, list []*prRow) error {
	ids := []int64{}
	prIDs := []int64{}
	for _, pr := range list {
		ids = append(ids, pr.authorID, pr.autoBy)
		if pr.ClosedBy != nil {
			ids = append(ids, pr.ClosedBy.ID)
		}
		prIDs = append(prIDs, pr.ID)
	}
	reviewers := map[int64][]Reviewer{}
	rows, err := s.pool.Query(ctx, `SELECT pull_request_id, user_id, state FROM pull_request_reviewers WHERE pull_request_id = ANY($1)
		ORDER BY updated_at`, prIDs)
	if err != nil {
		return err
	}
	type rv struct {
		pr, user int64
		state    string
	}
	var rvs []rv
	for rows.Next() {
		var x rv
		if err := rows.Scan(&x.pr, &x.user, &x.state); err != nil {
			rows.Close()
			return err
		}
		rvs = append(rvs, x)
		ids = append(ids, x.user)
	}
	rows.Close()
	users, err := identity.GetUserRefs(ctx, s.pool, ids)
	if err != nil {
		return err
	}
	for _, x := range rvs {
		reviewers[x.pr] = append(reviewers[x.pr], Reviewer{User: users[x.user], State: x.state})
	}
	level := LevelNone
	if r != nil {
		level = a.Level(r.ProjectID, r.ID)
	}
	for _, pr := range list {
		if u, ok := users[pr.authorID]; ok {
			pr.Author = &u
		}
		if pr.ClosedBy != nil {
			u := users[pr.ClosedBy.ID]
			pr.ClosedBy = &u
		}
		if u, ok := users[pr.autoBy]; ok && pr.AutoMerge != nil {
			pr.AutoMerge.EnabledBy = &u
		}
		pr.Reviewers = nonNil(reviewers[pr.ID])
		l := level
		if r == nil {
			l = LevelRead
		}
		own := pr.authorID != 0 && pr.authorID == a.UserID()
		open := pr.State == StateOpen
		pr.Permissions = PullRequestPermissions{
			Edit:    open && (own || l >= LevelAdmin),
			Merge:   open && l >= LevelWrite,
			Decline: open && (own || l >= LevelWrite),
			Review:  open && a.UserID() != 0 && !own && l >= LevelRead,
			Comment: a.UserID() != 0 && l >= LevelRead,
			DisableAutoMerge: open && pr.AutoMerge != nil && a.UserID() != 0 &&
				(own || pr.autoBy == a.UserID() || l >= LevelWrite),
		}
	}
	return nil
}

// loadPR returns a pull request of a repository the principal may read.
func (s *Service) loadPR(ctx context.Context, q db.Querier, repoID int64, number int, forUpdate bool) (*prRow, error) {
	sql := "SELECT " + prColumns + prFrom + " WHERE pr.repository_id = $1 AND pr.number = $2"
	if forUpdate {
		sql += " FOR UPDATE OF pr"
	}
	pr, err := scanPR(q.QueryRow(ctx, sql, repoID, number))
	if db.IsNoRows(err) {
		return nil, prNotFound(number)
	}
	return pr, err
}

// PullRequestFilter selects pull requests.
type PullRequestFilter struct {
	State    string // open (default), merged, declined or all
	Author   int64
	Reviewer int64
	Query    string
	Limit    int
	Offset   int
}

// PullRequests lists the pull requests of a repository.
func (s *Service) PullRequests(ctx context.Context, p *identity.Principal, key, slug string, f PullRequestFilter) ([]PullRequest, int, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, 0, err
	}
	var args db.Args
	where := "pr.repository_id = " + args.Add(r.ID)
	return s.listPRs(ctx, a, r, where, &args, f)
}

// MyPullRequests lists a page of the pull requests of all repositories where the principal is the author or a
// reviewer, most recently updated first.
func (s *Service) MyPullRequests(ctx context.Context, p *identity.Principal, role, state string, limit, offset int) ([]PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.UserID() == 0 {
		return []PullRequest{}, nil
	}
	var args db.Args
	where := a.VisibleSQL("r", &args)
	f := PullRequestFilter{State: state, Limit: limit, Offset: offset}
	switch role {
	case "reviewer":
		f.Reviewer = p.UserID()
	default:
		f.Author = p.UserID()
	}
	list, _, err := s.listPRs(ctx, a, nil, where, &args, f)
	return list, err
}

func (s *Service) listPRs(ctx context.Context, a *Access, r *repoRow, where string, args *db.Args, f PullRequestFilter) ([]PullRequest, int, error) {
	switch f.State {
	case "", StateOpen:
		where += " AND pr.state = 'open'"
	case StateMerged, StateDeclined:
		where += " AND pr.state = " + args.Add(f.State)
	case "all":
	default:
		return nil, 0, apperr.Invalidf("pull-request.state", "Unknown state %s.", f.State)
	}
	if f.Author != 0 {
		where += " AND pr.author_id = " + args.Add(f.Author)
	}
	if f.Reviewer != 0 {
		where += " AND EXISTS (SELECT 1 FROM pull_request_reviewers rv WHERE rv.pull_request_id = pr.id AND rv.user_id = " + args.Add(f.Reviewer) + ")"
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := args.Add("%" + escapeLike(q) + "%")
		where += " AND (pr.title ILIKE " + like + " OR pr.source_branch ILIKE " + like + " OR pr.number::text = " + args.Add(strings.TrimPrefix(q, "#")) + ")"
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*)"+prFrom+" WHERE "+where, *args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Pull requests updated at the same time are ordered by id, so that pages do not overlap.
	rows, err := s.pool.Query(ctx, "SELECT "+prColumns+prFrom+" WHERE "+where+" ORDER BY pr.updated_at DESC, pr.id DESC LIMIT "+
		strconv.Itoa(limit)+" OFFSET "+strconv.Itoa(max(f.Offset, 0)), *args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*prRow, error) { return scanPR(row) })
	if err != nil {
		return nil, 0, err
	}
	if err := s.completePRs(ctx, a, r, list); err != nil {
		return nil, 0, err
	}
	out := []PullRequest{}
	for _, pr := range list {
		pr.Description = nil
		out = append(out, pr.PullRequest)
	}
	return out, total, nil
}

// prClosed is the error of a change that needs an open pull request.
func prClosed(state string) *apperr.Error {
	if state == StateMerged {
		return apperr.Conflict("pull-request.closed", "The pull request is merged.")
	}
	return apperr.Conflict("pull-request.closed", "The pull request is declined.")
}

// GetPullRequest returns a pull request with its merge checks.
func (s *Service) GetPullRequest(ctx context.Context, p *identity.Principal, key, slug string, number int) (*PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, err
	}
	if err := s.completePRs(ctx, a, r, []*prRow{pr}); err != nil {
		return nil, err
	}
	if pr.Permissions.Edit {
		if err := s.pool.QueryRow(ctx, "SELECT coalesce(description_markdown, '') FROM pull_requests WHERE id = $1", pr.ID).
			Scan(&pr.DescriptionMD); err != nil {
			return nil, err
		}
		if pr.DescriptionMD == "" {
			pr.DescriptionMD = docmodel.Markdown(pr.Description)
		}
	}
	if pr.State == StateOpen {
		own, err := s.prCodeOwners(ctx, pr)
		if err != nil {
			return nil, err
		}
		if own != nil {
			pr.CodeOwners, pr.CodeOwnersFile = nonNil(own.owned), own.path
		}
		pr.Checks = s.mergeChecks(ctx, r, pr, own)
		// What merging would do and whether each strategy can: for those who may merge.
		if pr.Permissions.Merge {
			if pr.Strategies, err = s.strategyChecks(ctx, r, pr); err != nil {
				return nil, err
			}
			if pr.MergeMessages, err = s.mergeMessages(ctx, r, pr, signature(p.User)); err != nil {
				return nil, err
			}
		}
	}
	return &pr.PullRequest, nil
}

// prCodeOwners returns the code owners of an open pull request's changes, with their approvals; nil if the target
// has no CODEOWNERS file.
func (s *Service) prCodeOwners(ctx context.Context, pr *prRow) (*prOwners, error) {
	own, err := s.ownedFiles(ctx, pr.repoID, pr.TargetSHA, pr.MergeBase, pr.SourceSHA)
	if err != nil || own == nil {
		return own, err
	}
	return own, own.review(ctx, s.pool, pr)
}

// mergeChecks evaluates the repository's conditions for merging.
func (s *Service) mergeChecks(ctx context.Context, r *repoRow, pr *prRow, own *prOwners) []MergeCheck {
	st := r.Settings
	var checks []MergeCheck
	approved, needsWork := 0, 0
	for _, rv := range pr.Reviewers {
		switch rv.State {
		case ReviewApproved:
			approved++
		case ReviewNeedsWork:
			needsWork++
		}
	}
	if pr.Draft {
		checks = append(checks, MergeCheck{Key: "draft", Reason: "draft"}.say(apperr.Msg("The pull request is a draft.")))
	}
	if pr.Conflicted {
		checks = append(checks, MergeCheck{Key: "conflicts", Reason: "conflicted"}.say(
			apperr.Msg("The source and target branches have conflicts; merge the target into the source branch first.")))
	} else {
		checks = append(checks, MergeCheck{Key: "conflicts", OK: true, Reason: "clean"}.say(apperr.Msg("No conflicts.")))
	}
	if st.RequiredApprovals > 0 {
		checks = append(checks, MergeCheck{Key: "approvals", OK: approved >= st.RequiredApprovals, Reason: "count",
			Values: map[string]int{"approved": approved, "required": st.RequiredApprovals}}.say(plural(st.RequiredApprovals,
			apperr.Msg("%d of 1 required approval.", approved), apperr.Msg("%d of %d required approvals.", approved, st.RequiredApprovals))))
	}
	if st.NoNeedsWork {
		c := MergeCheck{Key: "needs-work", OK: true, Reason: "none"}.say(apperr.Msg("No reviewer asks for more work."))
		if needsWork > 0 {
			c = MergeCheck{Key: "needs-work", Reason: "some", Values: map[string]int{"count": needsWork}}.say(plural(needsWork,
				apperr.Msg("1 reviewer asks for more work."), apperr.Msg("%d reviewers ask for more work.", needsWork)))
		}
		checks = append(checks, c)
	}
	if st.TasksResolved {
		c := MergeCheck{Key: "tasks", OK: true, Reason: "resolved"}.say(apperr.Msg("All tasks are resolved."))
		if pr.OpenTasks > 0 {
			c = MergeCheck{Key: "tasks", Reason: "open", Values: map[string]int{"count": pr.OpenTasks}}.say(plural(pr.OpenTasks,
				apperr.Msg("1 open task."), apperr.Msg("%d open tasks.", pr.OpenTasks)))
		}
		checks = append(checks, c)
	}
	if st.CodeOwnerApproval {
		checks = append(checks, codeOwnersCheck(own))
	}
	if st.RequiredBuilds > 0 {
		checks = append(checks, s.buildsCheck(ctx, r.ID, pr.SourceSHA, st.RequiredBuilds, st.ExternalStatuses))
	}
	return checks
}

// BuildCounts counts the builds of a commit by their result. Skipped builds are pipelines whose paths filters left
// the commit out.
type BuildCounts struct {
	Successful, Skipped, Failed, Running int
}

// BuildStatus counts the builds of a commit (provided by the builds module through the application).
type BuildStatus func(ctx context.Context, repoID int64, sha string, external bool) (BuildCounts, error)

// SetBuildStatus connects the build results (the builds module is wired in by the application).
func (s *Service) SetBuildStatus(f BuildStatus) { s.buildStatus = f }

// buildsCheck counts Kogge's builds of the source commit, and the statuses of other build systems if the
// repository says so (anyone who can write can set those). Running builds hold the merge back: a build that
// fails later would come too late. Pipelines that their paths filters skipped count as done: the pipeline file
// says that the change does not need them.
func (s *Service) buildsCheck(ctx context.Context, repoID int64, sha string, required int, external bool) MergeCheck {
	check := MergeCheck{Key: "builds", Reason: "unavailable"}
	if s.buildStatus == nil {
		return check.say(apperr.Msg("No build results are available."))
	}
	c, err := s.buildStatus(ctx, repoID, sha, external)
	if err != nil {
		return check.say(apperr.Msg("The build results could not be read."))
	}
	done := c.Successful + c.Skipped
	check.Values = map[string]int{"done": done, "required": required, "successful": c.Successful, "skipped": c.Skipped, "running": c.Running,
		"failed": c.Failed}
	var skipped []apperr.Message
	if c.Skipped > 0 {
		skipped = append(skipped, plural(c.Skipped, apperr.Msg("1 pipeline skipped: no changed file matches its paths."),
			apperr.Msg("%d pipelines skipped: no changed file matches their paths.", c.Skipped)))
	}
	switch {
	case c.Failed > 0:
		check.Reason = "failed"
		return check.say(plural(c.Failed, apperr.Msg("1 build of the source commit failed."),
			apperr.Msg("%d builds of the source commit failed.", c.Failed)))
	case c.Running > 0:
		check.Reason = "running"
		return check.say(append([]apperr.Message{apperr.Msg("%d of %d required builds succeeded; %d still running.", done, required,
			c.Running)}, skipped...)...)
	case done < required:
		check.Reason = "missing"
		return check.say(append([]apperr.Message{apperr.Msg("%d of %d required builds succeeded.", done, required)}, skipped...)...)
	case c.Successful == 0 && c.Skipped > 0:
		check.OK, check.Reason = true, "passed"
		return check.say(skipped...)
	default:
		check.OK, check.Reason = true, "passed"
		return check.say(append([]apperr.Message{plural(c.Successful, apperr.Msg("1 successful build."),
			apperr.Msg("%d successful builds.", c.Successful))}, skipped...)...)
	}
}

// NewPullRequest opens a pull request.
type NewPullRequest struct {
	Title       string        `json:"title" minLength:"1" maxLength:"255"`
	Description *docmodel.Doc `json:"description,omitempty"`
	Markdown    string        `json:"descriptionMarkdown,omitempty" doc:"Alternative to description"`
	Source      string        `json:"source" minLength:"1" doc:"Source branch"`
	Target      string        `json:"target,omitempty" doc:"Target branch; default: the default branch"`
	Reviewers   []int64       `json:"reviewers,omitempty" doc:"User ids"`
	Draft       bool          `json:"draft,omitempty"`
}

// markdownSource is the Markdown a text was written in, to be stored with it; nil (NULL) for texts given as documents.
func markdownSource(d *docmodel.Doc, markdown string) any {
	if d == nil && markdown != "" {
		return markdown
	}
	return nil
}

func docOf(d *docmodel.Doc, markdown string) (*docmodel.Doc, error) {
	if d == nil && markdown != "" {
		d = docmodel.FromMarkdown(markdown)
	}
	if d == nil {
		return docmodel.Empty(), nil
	}
	if err := docmodel.Validate(d, docmodel.Options{}); err != nil {
		return nil, apperr.Invalidf("pull-request.description", "The text is not valid: %v", err)
	}
	return d, nil
}

// CreatePullRequest opens a pull request (write access is not needed; reading the repository is).
func (s *Service) CreatePullRequest(ctx context.Context, p *identity.Principal, key, slug string, in NewPullRequest) (*PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	target := in.Target
	if target == "" {
		target = r.DefaultBranch
	}
	if target == in.Source {
		return nil, apperr.Invalid("pull-request.branches", "The source and target branches must differ.")
	}
	repo := s.open(r.ID)
	sourceSHA, err := repo.ResolveBranch(ctx, in.Source)
	if err != nil {
		return nil, apperr.Invalidf("pull-request.source", "The branch %s does not exist.", in.Source)
	}
	targetSHA, err := repo.ResolveBranch(ctx, target)
	if err != nil {
		return nil, apperr.Invalidf("pull-request.target", "The branch %s does not exist.", target)
	}
	if merged, err := repo.IsAncestor(ctx, sourceSHA, targetSHA); err != nil {
		return nil, err
	} else if merged {
		return nil, apperr.Invalidf("pull-request.nothing", "The branch %s has no changes that are not in %s.", in.Source, target)
	}
	base, err := repo.MergeBase(ctx, sourceSHA, targetSHA)
	if err != nil {
		return nil, err
	}
	_, conflicts, err := repo.MergeTree(ctx, targetSHA, sourceSHA)
	if err != nil {
		return nil, err
	}
	desc, err := docOf(in.Description, in.Markdown)
	if err != nil {
		return nil, err
	}
	raw, _ := docmodel.Marshal(desc)
	reviewers := append(slices.Clone(in.Reviewers), r.Settings.DefaultReviewers...)
	own, err := s.ownedFiles(ctx, r.ID, targetSHA, base, sourceSHA)
	if err != nil {
		return nil, err
	}
	var number int
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "UPDATE repositories SET next_pr_number = next_pr_number + 1 WHERE id = $1 RETURNING next_pr_number - 1",
			r.ID).Scan(&number); err != nil {
			return err
		}
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO pull_requests (repository_id, number, title, description, description_text, author_id,
			source_branch, target_branch, source_sha, target_sha, merge_base, draft, conflicted, description_markdown)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`, r.ID, number, strings.TrimSpace(in.Title), raw,
			docmodel.PlainText(desc), p.UserID(), in.Source, target, sourceSHA, targetSHA, base, in.Draft, len(conflicts) > 0,
			markdownSource(in.Description, in.Markdown)).Scan(&id)
		if db.IsUniqueViolation(err, "pull_requests_open_key") {
			return apperr.Conflict("pull-request.exists", "There is already an open pull request from %s to %s.", in.Source, target)
		}
		if err != nil {
			return err
		}
		if err := s.addReviewers(ctx, tx, a, r, id, p.UserID(), reviewers); err != nil {
			return err
		}
		var owners []int64
		if !in.Draft { // drafts ask their owners once they are ready
			if owners, err = s.addCodeOwners(ctx, tx, r, id, p.UserID(), own); err != nil {
				return err
			}
		}
		if err := s.activity(ctx, tx, id, p.UserID(), "opened", map[string]any{"source": in.Source, "target": target}); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "pull-request", id, p.UserID()); err != nil {
			return err
		}
		if err := s.notifyPR(ctx, tx, r, id, number, strings.TrimSpace(in.Title), "pull-request.opened", p.UserID(),
			append(reviewers, owners...)); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.opened", RepoID: r.ID, PRID: id, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// addReviewers adds reviewers who can read the repository (not the author).
func (s *Service) addReviewers(ctx context.Context, tx pgx.Tx, a *Access, r *repoRow, prID, authorID int64, ids []int64) error {
	for _, uid := range ids {
		if uid == authorID || uid <= 0 {
			continue
		}
		rp, err := s.identity.PrincipalOf(ctx, uid, identity.MethodInternal)
		if err != nil {
			continue
		}
		ra, err := s.Access(ctx, rp)
		if err != nil {
			return err
		}
		if ra.Level(r.ProjectID, r.ID) < LevelRead {
			// By id, not by name: the caller may not see who the id is (audit L28).
			return apperr.Invalid("pull-request.reviewer", "A chosen reviewer cannot read the repository.",
				apperr.FieldError{Location: "body.reviewers", Message: "cannot read the repository", Value: uid})
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pull_request_reviewers (pull_request_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			prID, uid); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "pull-request", prID, uid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) activity(ctx context.Context, tx pgx.Tx, prID, actorID int64, kind string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	_, err := tx.Exec(ctx, "INSERT INTO pull_request_activities (pull_request_id, actor_id, kind, data) VALUES ($1, $2, $3, $4)",
		prID, nullID(actorID), kind, data)
	return err
}

func (s *Service) prURL(r *repoRow, number int) string {
	return "/code/" + strings.ToLower(r.ProjectKey) + "/" + r.Slug + "/pull-requests/" + strconv.Itoa(number)
}

// notifyPR informs the watchers of a pull request (and extra users) who can read the repository.
func (s *Service) notifyPR(ctx context.Context, tx pgx.Tx, r *repoRow, prID int64, number int, title, event string, actorID int64,
	extra []int64) error {
	recipients, err := notify.Watchers(ctx, tx, "pull-request", prID)
	if err != nil {
		return err
	}
	for _, u := range extra {
		if !slices.Contains(recipients, u) {
			recipients = append(recipients, u)
		}
	}
	return s.notifyUsers(ctx, tx, r, prID, number, title, event, actorID, recipients)
}

// notifyUsers tells people of a pull request who can read its repository.
func (s *Service) notifyUsers(ctx context.Context, tx pgx.Tx, r *repoRow, prID int64, number int, title, event string, actorID int64,
	recipients []int64) error {
	visible := []int64{}
	for _, uid := range recipients {
		rp, err := s.identity.PrincipalOf(ctx, uid, identity.MethodInternal)
		if err != nil {
			continue
		}
		ra, err := s.Access(ctx, rp)
		if err != nil {
			return err
		}
		if ra.Level(r.ProjectID, r.ID) >= LevelRead {
			visible = append(visible, uid)
		}
	}
	if len(visible) == 0 {
		return nil
	}
	var actor *int64
	if actorID != 0 {
		actor = &actorID
	}
	return notify.Create(ctx, tx, visible, notify.Notification{Category: notify.CategoryCode, Event: event, EntityType: "pull-request",
		EntityID: itoa(prID), ActorID: actor, Title: fmt.Sprintf("#%d %s", number, title), URL: s.prURL(r, number),
		Data: map[string]any{"repository": r.ProjectKey + "/" + r.Slug}})
}

// PullRequestUpdate changes a pull request.
type PullRequestUpdate struct {
	Title       *string       `json:"title,omitempty" minLength:"1" maxLength:"255"`
	Description *docmodel.Doc `json:"description,omitempty"`
	Markdown    *string       `json:"descriptionMarkdown,omitempty"`
	Target      *string       `json:"target,omitempty"`
	Reviewers   []int64       `json:"reviewers,omitempty" doc:"Replaces the reviewers"`
	Draft       *bool         `json:"draft,omitempty"`
	Version     int           `json:"version,omitempty" doc:"The version the change is based on"`
}

// UpdatePullRequest changes title, description, target, reviewers or the draft state.
func (s *Service) UpdatePullRequest(ctx context.Context, p *identity.Principal, key, slug string, number int, in PullRequestUpdate) (*PullRequest, error) {
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
		if err := s.completePRs(ctx, a, r, []*prRow{pr}); err != nil {
			return err
		}
		if !pr.Permissions.Edit {
			return apperr.Forbidden("pull-request.edit-denied", "Only the author and administrators can change the pull request.")
		}
		if in.Version != 0 && in.Version != pr.Version {
			return apperr.Conflict("pull-request.changed", "The pull request changed in the meantime; reload it.")
		}
		changes := map[string]any{}
		title, raw, target, draft := pr.Title, []byte(nil), pr.Target, pr.Draft
		if in.Title != nil && strings.TrimSpace(*in.Title) != pr.Title {
			title = strings.TrimSpace(*in.Title)
			changes["title"] = title
		}
		desc := pr.Description
		var source any // the description's Markdown: unchanged unless the description is
		if in.Description != nil || in.Markdown != nil {
			md := ""
			if in.Markdown != nil {
				md = *in.Markdown
			}
			if desc, err = docOf(in.Description, md); err != nil {
				return err
			}
			source = markdownSource(in.Description, md)
			changes["description"] = true
		}
		raw, _ = docmodel.Marshal(desc)
		if in.Draft != nil && *in.Draft != pr.Draft {
			draft = *in.Draft
			changes["draft"] = draft
		}
		targetSHA, base, conflicted := pr.TargetSHA, pr.MergeBase, pr.Conflicted
		if in.Target != nil && *in.Target != pr.Target {
			target = *in.Target
			repo := s.open(r.ID)
			if targetSHA, err = repo.ResolveBranch(ctx, target); err != nil || target == pr.Source {
				return apperr.Invalidf("pull-request.target", "The branch %s cannot be the target.", target)
			}
			if base, err = repo.MergeBase(ctx, pr.SourceSHA, targetSHA); err != nil {
				return err
			}
			_, conflicts, err := repo.MergeTree(ctx, targetSHA, pr.SourceSHA)
			if err != nil {
				return err
			}
			conflicted = len(conflicts) > 0
			changes["target"] = target
		}
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET title = $2, description = $3, description_text = $4, target_branch = $5, target_sha = $6,
			merge_base = $7, conflicted = $8, draft = $9, updated_at = now(), version = version + 1,
			description_markdown = CASE WHEN $10 THEN $11 ELSE description_markdown END WHERE id = $1`, pr.ID, title, raw,
			docmodel.PlainText(desc), target, targetSHA, base, conflicted, draft, changes["description"] != nil, source)
		if db.IsUniqueViolation(err, "pull_requests_open_key") {
			return apperr.Conflict("pull-request.exists", "There is already an open pull request from %s to %s.", pr.Source, target)
		}
		if err != nil {
			return err
		}
		if changes["target"] != nil {
			// Approvals were given for the changes against the old target: the new one changes what is merged.
			tag, err := tx.Exec(ctx, `UPDATE pull_request_reviewers SET state = 'unapproved', updated_at = now()
				WHERE pull_request_id = $1 AND state = 'approved'`, pr.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				changes["approvalsReset"] = true
			}
			// So does auto-merge, which was turned on for the old target.
			if err := s.endAutoMerge(ctx, tx, r, pr, autoMergeTarget, p.UserID()); err != nil {
				return err
			}
		}
		if in.Reviewers != nil {
			current := []int64{}
			var removed []int64
			for _, rv := range pr.Reviewers {
				current = append(current, rv.User.ID)
				if slices.Contains(in.Reviewers, rv.User.ID) {
					continue
				}
				// A reviewer who asks for more work is not removed by the author: that would get around the review.
				if rv.State == ReviewNeedsWork && a.Level(r.ProjectID, r.ID) < LevelAdmin {
					return apperr.Forbidden("pull-request.reviewer-veto",
						"%s asks for more work; only administrators of the repository can remove them as reviewer.", rv.User.DisplayName)
				}
				removed = append(removed, rv.User.ID)
			}
			if len(removed) > 0 {
				changes["reviewersRemoved"] = removed
			}
			if _, err := tx.Exec(ctx, "DELETE FROM pull_request_reviewers WHERE pull_request_id = $1 AND user_id <> ALL($2)", pr.ID,
				nonNil(in.Reviewers)); err != nil {
				return err
			}
			var added []int64
			for _, u := range in.Reviewers {
				if !slices.Contains(current, u) {
					added = append(added, u)
				}
			}
			if err := s.addReviewers(ctx, tx, a, r, pr.ID, pr.authorID, added); err != nil {
				return err
			}
			if len(added) > 0 {
				if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, title, "pull-request.reviewer-added", p.UserID(), added); err != nil {
					return err
				}
				changes["reviewersAdded"] = added
			}
		}
		// Ready for review, or another target (with its own CODEOWNERS): the owners are asked.
		if ready, ok := changes["draft"]; !draft && (ok && ready == false || changes["target"] != nil) {
			own, err := s.ownedFiles(ctx, r.ID, targetSHA, base, pr.SourceSHA)
			if err != nil {
				return err
			}
			owners, err := s.addCodeOwners(ctx, tx, r, pr.ID, pr.authorID, own)
			if err != nil {
				return err
			}
			if err := s.notifyUsers(ctx, tx, r, pr.ID, pr.Number, title, "pull-request.code-owner", p.UserID(), owners); err != nil {
				return err
			}
		}
		if len(changes) > 0 {
			if err := s.activity(ctx, tx, pr.ID, p.UserID(), "updated", changes); err != nil {
				return err
			}
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.updated", RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// Review sets the caller's review of a pull request; reviewing adds the caller as a reviewer.
func (s *Service) Review(ctx context.Context, p *identity.Principal, key, slug string, number int, state string) (*PullRequest, error) {
	if state != ReviewApproved && state != ReviewNeedsWork && state != ReviewUnapproved {
		return nil, apperr.Invalidf("pull-request.review", "Unknown review state %s.", state)
	}
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
		if pr.State != StateOpen {
			return prClosed(pr.State)
		}
		if pr.authorID == p.UserID() {
			return apperr.Forbidden("pull-request.own", "Authors cannot review their own pull requests.")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pull_request_reviewers (pull_request_id, user_id, state, reviewed_sha, updated_at)
			VALUES ($1, $2, $3, $4, now()) ON CONFLICT (pull_request_id, user_id) DO UPDATE SET state = EXCLUDED.state,
			reviewed_sha = EXCLUDED.reviewed_sha, updated_at = now()`, pr.ID, p.UserID(), state, pr.SourceSHA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE pull_requests SET updated_at = now(), version = version + 1 WHERE id = $1", pr.ID); err != nil {
			return err
		}
		if err := notify.Watch(ctx, tx, "pull-request", pr.ID, p.UserID()); err != nil {
			return err
		}
		if err := s.activity(ctx, tx, pr.ID, p.UserID(), "reviewed", map[string]any{"state": state}); err != nil {
			return err
		}
		if state != ReviewUnapproved {
			if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request."+state, p.UserID(), nil); err != nil {
				return err
			}
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.reviewed", RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// Decline closes a pull request without merging; Reopen opens it again.
func (s *Service) Decline(ctx context.Context, p *identity.Principal, key, slug string, number int, reopen bool) (*PullRequest, error) {
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
		own := pr.authorID == p.UserID()
		if !own && a.Level(r.ProjectID, r.ID) < LevelWrite {
			return apperr.Forbidden("pull-request.decline-denied", "Only the author and users who can write to the repository can do this.")
		}
		if reopen {
			if pr.State != StateDeclined {
				return apperr.Conflict("pull-request.not-declined", "Only declined pull requests can be reopened.")
			}
			repo := s.open(r.ID)
			sourceSHA, err := repo.ResolveBranch(ctx, pr.Source)
			if err != nil {
				return apperr.Conflict("pull-request.source-gone", "The source branch %s does not exist any more.", pr.Source)
			}
			targetSHA, err := repo.ResolveBranch(ctx, pr.Target)
			if err != nil {
				return apperr.Conflict("pull-request.target-gone", "The target branch %s does not exist any more.", pr.Target)
			}
			base, _ := repo.MergeBase(ctx, sourceSHA, targetSHA)
			_, conflicts, err := repo.MergeTree(ctx, targetSHA, sourceSHA)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE pull_requests SET state = 'open', closed_at = NULL, closed_by = NULL, source_sha = $2, target_sha = $3,
				merge_base = $4, conflicted = $5, updated_at = now(), version = version + 1 WHERE id = $1`, pr.ID, sourceSHA, targetSHA, base,
				len(conflicts) > 0)
			if db.IsUniqueViolation(err, "pull_requests_open_key") {
				return apperr.Conflict("pull-request.exists", "There is already an open pull request from %s to %s.", pr.Source, pr.Target)
			}
			if err != nil {
				return err
			}
		} else {
			if pr.State != StateOpen {
				return prClosed(pr.State)
			}
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'declined', closed_at = now(), closed_by = $2, updated_at = now(),
				version = version + 1 WHERE id = $1`, pr.ID, p.UserID()); err != nil {
				return err
			}
			// Declining ends auto-merge; who turned it on watches the pull request and hears of the decline.
			if _, err := tx.Exec(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1", pr.ID); err != nil {
				return err
			}
		}
		kind := map[bool]string{true: "reopened", false: "declined"}[reopen]
		if err := s.activity(ctx, tx, pr.ID, p.UserID(), kind, nil); err != nil {
			return err
		}
		if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request."+kind, p.UserID(), nil); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request." + kind, RepoID: r.ID, PRID: pr.ID, ActorID: p.UserID()})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPullRequest(ctx, p, key, slug, number)
}

// PullRequestDiff returns the changes of a pull request (since the merge base).
func (s *Service) PullRequestDiff(ctx context.Context, p *identity.Principal, key, slug string, number int, o DiffOptions) (*gitdiff.Diff, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, err
	}
	from, to := pr.MergeBase, pr.SourceSHA
	if from == "" {
		from = git.EmptyTree
	}
	if pr.State == StateMerged && pr.MergeSHA != "" && pr.MergeSHA != pr.SourceSHA {
		// Squash merges: the source commits are still there; show their changes.
		to = pr.SourceSHA
	}
	return s.diff(ctx, repo, from, to, o)
}

// PullRequestCommits lists a page of the commits of a pull request, newest first; More says whether more follow.
func (s *Service) PullRequestCommits(ctx context.Context, p *identity.Principal, key, slug string, number, limit, offset int) (*CommitList, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, err
	}
	exclude := []string{pr.TargetSHA}
	if pr.MergeBase != "" {
		exclude = []string{pr.MergeBase}
	}
	// One commit more than the page tells whether another page follows.
	limit = max(1, min(limit, 500))
	list, err := repo.Log(ctx, git.LogOptions{Rev: pr.SourceSHA, Exclude: exclude, Skip: max(offset, 0), Limit: limit + 1})
	if err != nil {
		return nil, err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	infos, err := s.commitInfos(ctx, list)
	if err != nil {
		return nil, err
	}
	return &CommitList{Commits: nonNil(infos), More: more}, nil
}

// rescopePullRequests updates the open pull requests of the branches a push changed: new commits, approvals,
// outdated comments, conflicts; pull requests whose source was merged by a push are marked merged.
func (s *Service) rescopePullRequests(ctx context.Context, repoID, pushID int64) error {
	var raw []byte
	var pusher *int64
	var protocol string
	if err := s.pool.QueryRow(ctx, "SELECT updates, user_id, protocol FROM pushes WHERE id = $1", pushID).Scan(&raw, &pusher,
		&protocol); err != nil {
		if db.IsNoRows(err) {
			return nil
		}
		return err
	}
	if protocol == "import" {
		return nil // migrations bring the state of their pull requests themselves
	}
	var updates []RefUpdate
	_ = json.Unmarshal(raw, &updates)
	var branches []string
	for _, u := range updates {
		if b, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
			branches = append(branches, b)
		}
	}
	if len(branches) == 0 {
		return nil
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT number FROM pull_requests WHERE repository_id = $1 AND state = 'open'
		AND (source_branch = ANY($2) OR target_branch = ANY($2))`, repoID, branches)
	if err != nil {
		return err
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return err
	}
	actor := int64(0)
	if pusher != nil {
		actor = *pusher
	}
	for _, n := range numbers {
		if err := s.rescopeOne(ctx, r, n, pushID, actor, updates); err != nil {
			return err
		}
	}
	return nil
}

// rescopeOne updates an open pull request after the push pushID of actor (0: a deploy key) with the updates.
func (s *Service) rescopeOne(ctx context.Context, r *repoRow, number int, pushID, actor int64, updates []RefUpdate) error {
	repo := s.open(r.ID)
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr, err := s.loadPR(ctx, tx, r.ID, number, true)
		if err != nil || pr.State != StateOpen {
			return nil
		}
		// The push's move of the source branch, for auto-merge: also when a later push was processed first.
		for _, u := range updates {
			if u.Ref == "refs/heads/"+pr.Source && !u.Deleted() {
				if err := s.autoMergePushed(ctx, tx, r, pr, pushID, actor, u); err != nil {
					return err
				}
			}
		}
		sourceSHA, srcErr := repo.ResolveBranch(ctx, pr.Source)
		targetSHA, tgtErr := repo.ResolveBranch(ctx, pr.Target)
		if srcErr != nil || tgtErr != nil {
			gone := pr.Source
			if srcErr == nil {
				gone = pr.Target
			}
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'declined', closed_at = now(), updated_at = now(), version = version + 1
				WHERE id = $1`, pr.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1", pr.ID); err != nil {
				return err
			}
			if err := s.activity(ctx, tx, pr.ID, actor, "declined", map[string]any{"branchDeleted": gone}); err != nil {
				return err
			}
			return s.publish(ctx, tx, RepoEvent{Name: "pull-request.declined", RepoID: r.ID, PRID: pr.ID, ActorID: actor})
		}
		if sourceSHA == pr.SourceSHA && targetSHA == pr.TargetSHA {
			return nil
		}
		// Merged outside of Kogge: the source is part of the target now.
		if merged, err := repo.IsAncestor(ctx, sourceSHA, targetSHA); err == nil && merged {
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'merged', merge_sha = $2, source_sha = $3, target_sha = $2,
				closed_by = $4, closed_at = now(), updated_at = now(), version = version + 1 WHERE id = $1`, pr.ID, targetSHA, sourceSHA,
				nullID(actor)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM pull_request_auto_merges WHERE pull_request_id = $1", pr.ID); err != nil {
				return err
			}
			if err := s.activity(ctx, tx, pr.ID, actor, "merged", map[string]any{"remote": true, "commit": targetSHA}); err != nil {
				return err
			}
			if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.merged", actor, nil); err != nil {
				return err
			}
			return s.publish(ctx, tx, RepoEvent{Name: "pull-request.merged", RepoID: r.ID, PRID: pr.ID, ActorID: actor})
		}
		base, err := repo.MergeBase(ctx, sourceSHA, targetSHA)
		if err != nil {
			return err
		}
		_, conflicts, err := repo.MergeTree(ctx, targetSHA, sourceSHA)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET source_sha = $2, target_sha = $3, merge_base = $4, conflicted = $5, updated_at = now(),
			version = version + 1 WHERE id = $1`, pr.ID, sourceSHA, targetSHA, base, len(conflicts) > 0); err != nil {
			return err
		}
		// The changes, or the owners on the target, may be others now.
		if !pr.Draft {
			own, err := s.ownedFiles(ctx, r.ID, targetSHA, base, sourceSHA)
			if err != nil {
				return err
			}
			if _, err := s.addCodeOwners(ctx, tx, r, pr.ID, pr.authorID, own); err != nil {
				return err
			}
		}
		if sourceSHA != pr.SourceSHA {
			added, _ := repo.Log(ctx, git.LogOptions{Rev: sourceSHA, Exclude: []string{pr.SourceSHA, targetSHA}, Limit: 100})
			removed, _ := repo.Log(ctx, git.LogOptions{Rev: pr.SourceSHA, Exclude: []string{sourceSHA, targetSHA}, Limit: 100})
			summary := func(list []git.Commit) []map[string]string {
				out := []map[string]string{}
				for _, c := range list {
					out = append(out, map[string]string{"sha": c.SHA, "subject": c.Subject()})
				}
				return out
			}
			if err := s.activity(ctx, tx, pr.ID, actor, "rescoped", map[string]any{"added": summary(added), "removed": summary(removed),
				"from": pr.SourceSHA, "to": sourceSHA}); err != nil {
				return err
			}
			if r.Settings.ResetApprovals {
				if _, err := tx.Exec(ctx, "UPDATE pull_request_reviewers SET state = 'unapproved', updated_at = now() WHERE pull_request_id = $1 AND state = 'approved'",
					pr.ID); err != nil {
					return err
				}
			}
			if err := s.markOutdated(ctx, tx, repo, pr.ID, sourceSHA); err != nil {
				return err
			}
			if err := s.notifyPR(ctx, tx, r, pr.ID, pr.Number, pr.Title, "pull-request.rescoped", actor, nil); err != nil {
				return err
			}
		}
		return s.publish(ctx, tx, RepoEvent{Name: "pull-request.updated", RepoID: r.ID, PRID: pr.ID, ActorID: actor})
	})
}

// markOutdated marks line comments whose line changed in the new source commit.
func (s *Service) markOutdated(ctx context.Context, tx pgx.Tx, repo *git.Repo, prID int64, sourceSHA string) error {
	rows, err := tx.Query(ctx, `SELECT id, path, line, coalesce(line_text, '') FROM pull_request_comments
		WHERE pull_request_id = $1 AND path IS NOT NULL AND line IS NOT NULL AND side = 'new' AND NOT outdated`, prID)
	if err != nil {
		return err
	}
	type lc struct {
		id   int64
		path string
		line int
		text string
	}
	var list []lc
	for rows.Next() {
		var c lc
		if err := rows.Scan(&c.id, &c.path, &c.line, &c.text); err != nil {
			rows.Close()
			return err
		}
		list = append(list, c)
	}
	rows.Close()
	files := map[string][]string{}
	for _, c := range list {
		lines, ok := files[c.path]
		if !ok {
			if info, err := repo.Stat(ctx, sourceSHA, c.path); err == nil {
				if data, err := repo.ReadBlob(ctx, info.SHA, maxFileView); err == nil {
					lines = strings.Split(string(data), "\n")
				}
			}
			files[c.path] = lines
		}
		if c.line < 1 || c.line > len(lines) || lines[c.line-1] != c.text {
			if _, err := tx.Exec(ctx, "UPDATE pull_request_comments SET outdated = true WHERE id = $1", c.id); err != nil {
				return err
			}
		}
	}
	return nil
}
