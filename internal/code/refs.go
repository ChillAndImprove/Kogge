// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Information for the integration with issues (package links): what a push brought, what a pull request is
// about, and who may see it.

// Push is a recorded push.
type Push struct {
	ID       int64
	RepoID   int64
	UserID   int64 // 0 for deploy keys
	Protocol string
	Updates  []RefUpdate
}

// Merge reports whether Kogge made the push by merging a pull request.
func (p *Push) Merge() bool { return p.Protocol == "merge" }

// Imported reports whether the push brought the history of a repository imported from another Git server.
func (p *Push) Imported() bool { return p.Protocol == "import" }

// PushOf returns a recorded push.
func (s *Service) PushOf(ctx context.Context, id int64) (*Push, error) {
	p := &Push{ID: id}
	var raw []byte
	var user *int64
	if err := s.pool.QueryRow(ctx, "SELECT repository_id, user_id, updates, protocol FROM pushes WHERE id = $1", id).Scan(&p.RepoID, &user,
		&raw, &p.Protocol); err != nil {
		return nil, err
	}
	if user != nil {
		p.UserID = *user
	}
	_ = json.Unmarshal(raw, &p.Updates)
	return p, nil
}

// NewCommits returns the commits a push added to the repository (oldest first, at most limit): for updated refs
// the commits between the old and the new value, for new refs those not reachable from refs that existed before.
func (s *Service) NewCommits(ctx context.Context, repoID int64, updates []RefUpdate, limit int) ([]git.Commit, error) {
	repo := s.open(repoID)
	seen := map[string]bool{}
	var out []git.Commit
	add := func(args []string) error {
		raw, err := s.git.Run(ctx, repo.Path, nil, args...)
		if err != nil {
			return err
		}
		for _, c := range git.ParseLog(raw) {
			if !seen[c.SHA] && len(out) < limit {
				seen[c.SHA] = true
				out = append(out, c)
			}
		}
		return nil
	}
	base := []string{"log", "--reverse", "--max-count=" + itoaInt(limit), git.LogFormat}
	for _, spec := range revSpecs(updates) {
		if err := add(append(append([]string{}, base...), spec...)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// revSpecs returns the revision arguments of the commits that ref updates add to a repository (after the refs were
// updated), one range per list: the new branches and tags together — the other new refs must not hide their shared
// commits — without what the refs that existed before reach, and each updated branch or tag from its old value to
// its new one.
func revSpecs(updates []RefUpdate) [][]string {
	var specs [][]string
	var tips, branchExcl, tagExcl []string
	for _, u := range updates {
		if u.Deleted() || !u.Created() {
			continue
		}
		if b, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
			branchExcl = append(branchExcl, "--exclude="+b)
		} else if t, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok {
			tagExcl = append(tagExcl, "--exclude="+t)
		} else {
			continue
		}
		tips = append(tips, u.New)
	}
	if len(tips) > 0 {
		spec := append([]string{"--not"}, branchExcl...)
		spec = append(append(spec, "--branches"), tagExcl...)
		spec = append(spec, "--tags", "--not", "--end-of-options")
		specs = append(specs, append(append(spec, tips...), "--"))
	}
	for _, u := range updates {
		if u.Deleted() || u.Created() || (!strings.HasPrefix(u.Ref, "refs/heads/") && !strings.HasPrefix(u.Ref, "refs/tags/")) {
			continue
		}
		specs = append(specs, []string{"--end-of-options", u.New, "^" + u.Old, "--"})
	}
	return specs
}

// pushSummary is what a push brought, as pushes.commits records it for the activity streams.
type pushSummary struct {
	Count  int            `json:"count"`
	Newest []pushedCommit `json:"newest"`
}

// pushedCommit is a commit a push brought.
type pushedCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// newestPushed is how many of the newest commits of a push its activity names.
const newestPushed = 3

// pushSummaryTimeout bounds reading the commits of a push, which the push waits for.
const pushSummaryTimeout = 20 * time.Second

// summarisePush returns how many commits ref updates added to a repository and the newest of them. It runs while
// the push holds the Git write lock, so that no other push changes the refs it compares with. A push that updates
// several refs with the same new commits counts them for each.
func (s *Service) summarisePush(ctx context.Context, repoID int64, updates []RefUpdate) (*pushSummary, error) {
	repo := s.open(repoID)
	opts := &git.Opts{Timeout: pushSummaryTimeout}
	out := &pushSummary{Newest: []pushedCommit{}}
	for _, spec := range revSpecs(updates) {
		raw, err := s.git.Run(ctx, repo.Path, opts, append([]string{"rev-list", "--count"}, spec...)...)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, err
		}
		out.Count += n
		if n == 0 || len(out.Newest) >= newestPushed {
			continue
		}
		raw, err = s.git.Run(ctx, repo.Path, opts, append([]string{"log", "--max-count=" + itoaInt(newestPushed-len(out.Newest)),
			git.LogFormat}, spec...)...)
		if err != nil {
			return nil, err
		}
		for _, c := range git.ParseLog(raw) {
			out.Newest = append(out.Newest, pushedCommit{SHA: c.SHA, Subject: c.Subject()})
		}
	}
	return out, nil
}

func itoaInt(i int) string { return itoa(int64(i)) }

// RepoInfo returns the reference of a repository.
func (s *Service) RepoInfo(ctx context.Context, id int64) (*RepoRef, error) {
	r, err := repoByID(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	return &RepoRef{ID: r.ID, ProjectKey: r.ProjectKey, Slug: r.Slug, Name: r.Name}, nil
}

// VisiblePullRequestIDs reports which pull requests a principal may see (for re-checking notifications).
func (s *Service) VisiblePullRequestIDs(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error) {
	nums := []int64{}
	for _, id := range ids {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			nums = append(nums, n)
		}
	}
	rows, err := s.pool.Query(ctx, "SELECT id, repository_id FROM pull_requests WHERE id = ANY($1)", nums)
	if err != nil {
		return nil, err
	}
	repoOf := map[int64]int64{}
	var repos []int64
	for rows.Next() {
		var id, repo int64
		if err := rows.Scan(&id, &repo); err != nil {
			rows.Close()
			return nil, err
		}
		repoOf[id] = repo
		repos = append(repos, repo)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	readable, err := s.ReadableRepos(ctx, p, repos)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, repo := range repoOf {
		out[strconv.FormatInt(id, 10)] = readable[repo]
	}
	return out, nil
}

// ReadableRepos returns which of the repositories the principal may read.
func (s *Service) ReadableRepos(ctx context.Context, p *identity.Principal, ids []int64) (map[int64]bool, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, "SELECT id, project_id FROM repositories WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, project int64
		if err := rows.Scan(&id, &project); err != nil {
			return nil, err
		}
		out[id] = a.Level(project, id) >= LevelRead
	}
	return out, rows.Err()
}

// PRInfo is what the integration needs to know about a pull request.
type PRInfo struct {
	ID          int64
	RepoID      int64
	Number      int
	Title       string
	Source      string
	Description string
	State       string
}

// PullRequestInfo returns a pull request for indexing.
func (s *Service) PullRequestInfo(ctx context.Context, id int64) (*PRInfo, error) {
	var pr PRInfo
	err := s.pool.QueryRow(ctx, `SELECT id, repository_id, number, title, source_branch, description_text, state FROM pull_requests WHERE id = $1`, id).
		Scan(&pr.ID, &pr.RepoID, &pr.Number, &pr.Title, &pr.Source, &pr.Description, &pr.State)
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// PullRequestsByID returns pull requests (summaries) the principal may see, e.g. for the development section of
// an issue.
func (s *Service) PullRequestsByID(ctx context.Context, p *identity.Principal, ids []int64) ([]PullRequest, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []PullRequest{}, nil
	}
	rows, err := s.pool.Query(ctx, "SELECT "+prColumns+prFrom+" WHERE pr.id = ANY($1) ORDER BY pr.updated_at DESC", ids)
	if err != nil {
		return nil, err
	}
	var list []*prRow
	for rows.Next() {
		pr, err := scanPR(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, pr)
	}
	rows.Close()
	visible := []*prRow{}
	for _, pr := range list {
		r, err := repoByID(ctx, s.pool, pr.repoID)
		if err != nil {
			continue
		}
		if a.Level(r.ProjectID, r.ID) >= LevelRead {
			if err := s.completePRs(ctx, a, r, []*prRow{pr}); err != nil {
				return nil, err
			}
			visible = append(visible, pr)
		}
	}
	out := []PullRequest{}
	for _, pr := range visible {
		pr.Description = nil
		out = append(out, pr.PullRequest)
	}
	return out, nil
}

// RepoBasics describes a repository for other modules.
type RepoBasics struct {
	ID            int64
	ProjectID     int64
	ProjectKey    string
	Slug          string
	Name          string
	DefaultBranch string
	CloneURL      string
}

func (s *Service) basics(r *repoRow) *RepoBasics {
	h, _ := s.CloneURLs(r.ProjectKey, r.Slug)
	return &RepoBasics{ID: r.ID, ProjectID: r.ProjectID, ProjectKey: r.ProjectKey, Slug: r.Slug, Name: r.Name, DefaultBranch: r.DefaultBranch,
		CloneURL: h}
}

// RepoBasicsByID returns a repository by id.
func (s *Service) RepoBasicsByID(ctx context.Context, id int64) (*RepoBasics, error) {
	r, err := repoByID(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	return s.basics(r), nil
}

// RepoBasicsByPath returns a repository by project key and slug (no permission check).
func (s *Service) RepoBasicsByPath(ctx context.Context, key, slug string) (*RepoBasics, error) {
	r, err := repoByPath(ctx, s.pool, key, slug)
	if err != nil {
		return nil, err
	}
	return s.basics(r), nil
}

// AccessLevel returns the principal's access to a repository (LevelNone … LevelAdmin).
func (s *Service) AccessLevel(ctx context.Context, p *identity.Principal, repoID int64) (int, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return LevelNone, err
	}
	var projectID int64
	if err := s.pool.QueryRow(ctx, "SELECT project_id FROM repositories WHERE id = $1", repoID).Scan(&projectID); err != nil {
		return LevelNone, err
	}
	return a.Level(projectID, repoID), nil
}

// ProjectForVariables resolves a code project whose variables the principal manages: its administrators through
// its permissions. Global administrators, who manage the permissions, do not read what the project holds without
// granting it to themselves (audit L31); who cannot see the project gets the same 404 as for one that does not exist.
func (s *Service) ProjectForVariables(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return 0, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return 0, err
	}
	if !a.projectAdmin[pr.ID] {
		return 0, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project manage its variables.")
	}
	return pr.ID, nil
}

// RootNames lists the names of the files and directories at the top of a commit's tree, for the builds module,
// which recognises the kind of a project by them.
func (s *Service) RootNames(ctx context.Context, repoID int64, commit string) ([]string, error) {
	entries, err := s.open(repoID).Tree(ctx, commit, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names, nil
}

// ReadFileAt returns a file at a commit (false if it does not exist), at most 1 MiB.
func (s *Service) ReadFileAt(ctx context.Context, repoID int64, commit, path string) ([]byte, bool, error) {
	repo := s.open(repoID)
	info, err := repo.Stat(ctx, commit, path)
	if errors.Is(err, git.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Size > 1<<20 {
		return nil, false, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	data, err := repo.ReadBlob(ctx, info.SHA, 1<<20)
	return data, err == nil, err
}

// maxChangedFiles bounds the files of a change that paths filters of pipelines look at: a larger change counts as
// one that touches everything.
const maxChangedFiles = 10000

// ChangedFiles lists the files that differ between two commits, for paths filters of pipelines; known is false when
// they are too many.
func (s *Service) ChangedFiles(ctx context.Context, repoID int64, base, head string) (files []string, known bool, err error) {
	return s.open(repoID).ChangedPaths(ctx, base, head, maxChangedFiles)
}

// ChangeBase returns the commit the changes of a pushed branch count from, for paths filters of pipelines: the
// branch's commit before the push or, for a new branch, the commit where it left the default branch. For a new
// default branch, and a branch with a history of its own, every file is new (the empty tree); "" means unknown.
func (s *Service) ChangeBase(ctx context.Context, repoID int64, u RefUpdate) string {
	if !u.Created() {
		return u.Old
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return ""
	}
	if u.Ref == "refs/heads/"+r.DefaultBranch {
		return git.EmptyTree
	}
	repo := s.open(repoID)
	tip, err := repo.ResolveBranch(ctx, r.DefaultBranch)
	if errors.Is(err, git.ErrNotFound) {
		return git.EmptyTree
	}
	if err != nil {
		return ""
	}
	base, err := repo.MergeBase(ctx, tip, u.New)
	switch {
	case err != nil:
		return ""
	case base == "":
		return git.EmptyTree
	}
	return base
}

// ResolveRef returns the commit of a branch, tag or commit id.
func (s *Service) ResolveRef(ctx context.Context, repoID int64, ref string) (string, error) {
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return "", err
	}
	return s.resolve(ctx, s.open(repoID), r, ref)
}

// CommitSubject returns the first line of a commit's message.
func (s *Service) CommitSubject(ctx context.Context, repoID int64, sha string) string {
	c, err := s.open(repoID).Commit(ctx, sha)
	if err != nil {
		return ""
	}
	return c.Subject()
}

// CommitMessage returns a commit's message.
func (s *Service) CommitMessage(ctx context.Context, repoID int64, sha string) string {
	c, err := s.open(repoID).Commit(ctx, sha)
	if err != nil {
		return ""
	}
	return c.Message
}

// Pusher returns the user whose push (or edit, or merge) last moved a branch to a commit; 0 if none is recorded or
// a deploy key pushed. Pipelines that start without a person — on a schedule, after a pipeline of another
// repository — read other repositories with the permissions of whoever put the commit there.
func (s *Service) Pusher(ctx context.Context, repoID int64, ref, sha string) (int64, error) {
	want, err := json.Marshal([]map[string]string{{"ref": ref, "new": sha}})
	if err != nil {
		return 0, err
	}
	var user *int64
	err = s.pool.QueryRow(ctx, `SELECT user_id FROM pushes WHERE repository_id = $1 AND updates @> $2::jsonb
		ORDER BY pushed_at DESC, id DESC LIMIT 1`, repoID, want).Scan(&user)
	if db.IsNoRows(err) || err == nil && user == nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return *user, nil
}

// Protection levels of refs, for protected build values: who decides what a pipeline at the ref does.
const (
	// ProtectionNone: everyone who can write may change the ref.
	ProtectionNone = iota
	// ProtectionRepository: a branch permission of the repository decides who may change it; the repository's
	// administrators control it.
	ProtectionRepository
	// ProtectionProject: a branch permission of the code project decides who may change it.
	ProtectionProject
)

// RefProtection returns how a ref is protected: by a branch permission that decides who may change it, of the code
// project or only of the repository. Rules that only prevent deletions or rewrites leave it open to everyone who
// can write.
func (s *Service) RefProtection(ctx context.Context, repoID int64, ref string) int {
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return ProtectionNone
	}
	rules, err := s.branchPermissions(ctx, s.pool, r.ProjectID, r.ID)
	if err != nil {
		return ProtectionNone
	}
	level := ProtectionNone
	for _, b := range rules {
		if (b.Kind == RestrictPush || b.Kind == PullRequestOnly) && matchRef(b.Pattern, ref) {
			if b.Inherited {
				return ProtectionProject
			}
			level = ProtectionRepository
		}
	}
	return level
}

// RepoMoved is told, in the transaction of the move, that a repository moved to another code project; set by the
// application with the builds module.
type RepoMoved func(ctx context.Context, tx pgx.Tx, repoID, projectID int64) error

// SetRepoMoved lets build runs follow their repository to another project.
func (s *Service) SetRepoMoved(f RepoMoved) { s.repoMoved = f }

// JobAuth checks a build job's token for Git access; set by the application with the builds module.
type JobAuth func(ctx context.Context, token string) (repoID int64, ok bool)

// SetJobAuth lets build jobs clone their repository with their job token.
func (s *Service) SetJobAuth(f JobAuth) { s.jobAuth = f }

// ProjectIDByKey returns the id of a code project (no permission check).
func (s *Service) ProjectIDByKey(ctx context.Context, key string) (int64, error) {
	p, err := projectByKey(ctx, s.pool, key)
	if err != nil {
		return 0, err
	}
	return p.ID, nil
}

// PullRequestRef describes a pull request for other modules.
type PullRequestRef struct {
	ID        int64
	RepoID    int64
	Number    int
	Title     string
	Source    string
	Target    string
	SourceSHA string
	MergeBase string // where the source branch left the target branch ("" for unrelated histories)
	State     string
	Draft     bool
	AuthorID  int64
}

// PullRequestByID returns a pull request (no permission check).
func (s *Service) PullRequestByID(ctx context.Context, id int64) (*PullRequestRef, error) {
	pr := &PullRequestRef{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT repository_id, number, title, source_branch, target_branch, source_sha, coalesce(merge_base, ''), state,
		draft, coalesce(author_id, 0) FROM pull_requests WHERE id = $1`, id).Scan(&pr.RepoID, &pr.Number, &pr.Title, &pr.Source, &pr.Target,
		&pr.SourceSHA, &pr.MergeBase, &pr.State, &pr.Draft, &pr.AuthorID)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("pull-request.not-found", "The pull request does not exist.")
	}
	return pr, err
}

// ReadableScope returns what the principal may read: everything, or the repositories of some code projects and
// some single repositories (for lists that span repositories, such as recent pipeline runs).
func (s *Service) ReadableScope(ctx context.Context, p *identity.Principal) (all bool, projects, repos []int64, err error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return false, nil, nil, err
	}
	projects, repos = []int64{}, []int64{}
	for id, l := range a.projects {
		if l >= LevelRead {
			projects = append(projects, id)
		}
	}
	for id, l := range a.repos {
		if l >= LevelRead {
			repos = append(repos, id)
		}
	}
	return false, projects, repos, nil
}

// AdminRepo returns a repository the principal administers.
func (s *Service) AdminRepo(ctx context.Context, p *identity.Principal, key, slug string) (*RepoBasics, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelAdmin)
	if err != nil {
		return nil, err
	}
	return s.basics(r), nil
}

// AdminProjectID returns the id of a code project the principal administers; who cannot see the project gets the
// same 404 as for one that does not exist.
func (s *Service) AdminProjectID(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return 0, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return 0, err
	}
	if !a.ProjectAdmin(pr.ID) {
		return 0, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can do this.")
	}
	return pr.ID, nil
}

// ProjectKeyOf returns the key of a code project.
func (s *Service) ProjectKeyOf(ctx context.Context, id int64) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, "SELECT key FROM code_projects WHERE id = $1", id).Scan(&key)
	return key, err
}

// Contains reports whether commit is tip or one of its ancestors (deployments containing a commit).
func (s *Service) Contains(ctx context.Context, repoID int64, tip, commit string) bool {
	if tip == commit {
		return true
	}
	ok, err := s.open(repoID).IsAncestor(ctx, commit, tip)
	return err == nil && ok
}

// CommitsBetween returns the commits reachable from to but not from from (newest first).
func (s *Service) CommitsBetween(ctx context.Context, repoID int64, from, to string, limit int) ([]git.Commit, error) {
	o := git.LogOptions{Rev: to, Limit: limit}
	if from != "" {
		o.Exclude = []string{from}
	}
	return s.open(repoID).Log(ctx, o)
}
