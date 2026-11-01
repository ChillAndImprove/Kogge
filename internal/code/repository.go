// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/live"
)

// Merge strategies of pull requests.
const (
	MergeCommit      = "merge"
	MergeSquash      = "squash"
	MergeFastForward = "fast-forward"
	// MergeRebase puts the source branch's commits onto the target one by one and moves the target to the last.
	MergeRebase = "rebase"
	// MergeRebaseCommit rebases like MergeRebase and adds a merge commit (a semi-linear history).
	MergeRebaseCommit = "rebase-merge"
)

// mergeStrategies are the strategies, in the order the settings offer them.
var mergeStrategies = []string{MergeCommit, MergeSquash, MergeFastForward, MergeRebase, MergeRebaseCommit}

// RepoSettings are the pull request settings of a repository.
type RepoSettings struct {
	MergeStrategies    []string `json:"mergeStrategies" minItems:"1" enum:"merge,squash,fast-forward,rebase,rebase-merge" doc:"Allowed strategies; the first is the default"`
	RequiredApprovals  int      `json:"requiredApprovals" minimum:"0" maximum:"20"`
	NoNeedsWork        bool     `json:"noNeedsWork" doc:"Merging needs no reviewer to have marked the pull request as needing work"`
	TasksResolved      bool     `json:"tasksResolved" doc:"Merging needs all tasks resolved"`
	RequiredBuilds     int      `json:"requiredBuilds" minimum:"0" maximum:"20" doc:"Successful builds of the source commit needed to merge"`
	CodeOwnerApproval  bool     `json:"codeOwnerApproval,omitempty" doc:"Merging needs, for every changed file with code owners, the approval of one of them"`
	ExternalStatuses   bool     `json:"externalStatuses,omitempty" doc:"Statuses of other build systems count as builds; anyone who can write can set them through the API"`
	ResetApprovals     bool     `json:"resetApprovals" doc:"Reset approvals when the source branch changes"`
	DeleteSourceBranch bool     `json:"deleteSourceBranch" doc:"Delete the source branch after merging by default"`
	DefaultReviewers   []int64  `json:"defaultReviewers" doc:"Users added as reviewers to new pull requests"`
}

func defaultSettings() RepoSettings {
	return RepoSettings{MergeStrategies: []string{MergeCommit, MergeSquash, MergeFastForward}, NoNeedsWork: true, TasksResolved: true,
		DefaultReviewers: []int64{}}
}

// Repository is a Git repository.
type Repository struct {
	ID            int64        `json:"id"`
	Project       ProjectRef   `json:"project"`
	Slug          string       `json:"slug"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	DefaultBranch string       `json:"defaultBranch"`
	Archived      bool         `json:"archived"`
	Empty         *bool        `json:"empty,omitempty" doc:"Whether the repository has no branches (only for single repositories)"`
	Size          int64        `json:"size" doc:"Disk usage of its Git objects in bytes"`
	LFSSize       int64        `json:"lfsSize" doc:"Size of its Git LFS objects in bytes"`
	Quota         *int64       `json:"quota,omitempty" doc:"Most its Git and LFS objects may take together, in bytes; 0: no quota (only for single repositories)"`
	CloneHTTP     string       `json:"cloneHttp"`
	CloneSSH      string       `json:"cloneSsh,omitempty"`
	Permission    string       `json:"permission" enum:"none,read,write,admin" doc:"The caller's access"`
	Settings      RepoSettings `json:"settings"`
	Created       time.Time    `json:"created"`
	Updated       time.Time    `json:"updated"`
	Pushed        *time.Time   `json:"pushed,omitempty"`
	Import        *RepoImport  `json:"import,omitempty" doc:"Set for repositories imported from another Git server"`
}

// RepoImport is the state of an import from another Git server.
type RepoImport struct {
	URL      string     `json:"url"`
	State    string     `json:"state" enum:"running,failed,done"`
	Error    string     `json:"error,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

type repoRow struct {
	ID            int64
	ProjectID     int64
	ProjectKey    string
	ProjectName   string
	Slug          string
	Name          string
	Description   string
	DefaultBranch string
	Archived      bool
	Settings      RepoSettings
	Size          int64
	LFSSize       int64
	Quota         *int64 // set by an administrator for this repository; nil: the quota of the settings
	Created       time.Time
	Updated       time.Time
	Pushed        *time.Time
	Import        *RepoImport
}

const repoColumns = `r.id, r.project_id, p.key, p.name, r.slug, r.name, r.description, r.default_branch, r.archived, r.settings,
	r.size_bytes, r.lfs_bytes, r.quota_bytes, r.created_at, r.updated_at, r.pushed_at, coalesce(r.import_url, ''), coalesce(r.import_state, ''), r.import_error,
	r.import_finished_at`

func scanRepo(row pgx.Row) (*repoRow, error) {
	var r repoRow
	var settings []byte
	var imp RepoImport
	if err := row.Scan(&r.ID, &r.ProjectID, &r.ProjectKey, &r.ProjectName, &r.Slug, &r.Name, &r.Description, &r.DefaultBranch,
		&r.Archived, &settings, &r.Size, &r.LFSSize, &r.Quota, &r.Created, &r.Updated, &r.Pushed, &imp.URL, &imp.State, &imp.Error, &imp.Finished); err != nil {
		return nil, err
	}
	if imp.State != "" {
		r.Import = &imp
	}
	r.Settings = defaultSettings()
	_ = json.Unmarshal(settings, &r.Settings)
	if len(r.Settings.MergeStrategies) == 0 {
		r.Settings.MergeStrategies = defaultSettings().MergeStrategies
	}
	r.Settings.DefaultReviewers = nonNil(r.Settings.DefaultReviewers)
	return &r, nil
}

func repoNotFound(key, slug string) error {
	return apperr.NotFound("repository.not-found", "The repository %s/%s does not exist or you may not see it.", key, slug)
}

// repoByPath finds a repository by project key and slug, following redirects of renamed repositories.
func repoByPath(ctx context.Context, q db.Querier, key, slug string) (*repoRow, error) {
	slug = strings.TrimSuffix(slug, ".git")
	r, err := scanRepo(q.QueryRow(ctx, "SELECT "+repoColumns+` FROM repositories r JOIN code_projects p ON p.id = r.project_id
		WHERE upper(p.key) = upper($1) AND lower(r.slug) = lower($2)`, key, slug))
	if db.IsNoRows(err) {
		r, err = scanRepo(q.QueryRow(ctx, "SELECT "+repoColumns+` FROM repository_redirects x JOIN repositories r ON r.id = x.repository_id
			JOIN code_projects p ON p.id = r.project_id WHERE upper(x.project_key) = upper($1) AND lower(x.slug) = lower($2)`, key, slug))
	}
	if db.IsNoRows(err) {
		return nil, repoNotFound(key, slug)
	}
	return r, err
}

func repoByID(ctx context.Context, q db.Querier, id int64) (*repoRow, error) {
	r, err := scanRepo(q.QueryRow(ctx, "SELECT "+repoColumns+" FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE r.id = $1", id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("repository.not-found", "The repository does not exist or you may not see it.")
	}
	return r, err
}

// repo returns a repository the principal may access at the level.
func (s *Service) repo(ctx context.Context, a *Access, key, slug string, level int) (*repoRow, error) {
	r, err := repoByPath(ctx, s.pool, key, slug)
	if err != nil {
		return nil, err
	}
	l := a.Level(r.ProjectID, r.ID)
	if l < LevelRead {
		return nil, repoNotFound(key, slug)
	}
	if l < level {
		switch level {
		case LevelWrite:
			return nil, apperr.Forbidden("repository.write-required", "You may not change this repository.")
		default:
			return nil, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can do this.")
		}
	}
	if level >= LevelWrite && r.Archived {
		return nil, apperr.Conflict("repository.archived", "The repository is archived.")
	}
	return r, nil
}

// CloneURLs returns the HTTPS and SSH addresses of a repository.
func (s *Service) CloneURLs(key, slug string) (string, string) {
	base := strings.TrimRight(s.opt.PublicURL.String(), "/")
	httpURL := base + "/scm/" + strings.ToLower(key) + "/" + slug + ".git"
	if s.opt.SSHPort == 0 {
		return httpURL, ""
	}
	host := s.opt.SSHHost
	if host == "" {
		host = s.opt.PublicURL.Hostname()
	}
	sshURL := "ssh://git@" + host + ":" + strconv.Itoa(s.opt.SSHPort) + "/" + strings.ToLower(key) + "/" + slug + ".git"
	return httpURL, sshURL
}

func (s *Service) toRepository(a *Access, r *repoRow) Repository {
	h, ssh := s.CloneURLs(r.ProjectKey, r.Slug)
	return Repository{ID: r.ID, Project: ProjectRef{ID: r.ProjectID, Key: r.ProjectKey, Name: r.ProjectName}, Slug: r.Slug, Name: r.Name,
		Description: r.Description, DefaultBranch: r.DefaultBranch, Archived: r.Archived, Size: r.Size, LFSSize: r.LFSSize, CloneHTTP: h, CloneSSH: ssh,
		Permission: LevelName(a.Level(r.ProjectID, r.ID)), Settings: r.Settings, Created: r.Created, Updated: r.Updated, Pushed: r.Pushed,
		Import: r.Import}
}

// RepositoryFilter selects a page of repositories.
type RepositoryFilter struct {
	Project string // key of the code project
	Query   string // part of the name, the slug or the project key
	// Level keeps the repositories the principal may access at least at this level (LevelWrite: push to them).
	Level    int
	Archived string // "true": only archived repositories, "false": only the others, "": both
	Sort     string // name (default) or pushed
	Limit    int
	Offset   int
}

// repoOrders are the orders of repository lists; ties are broken by id, so that pages do not overlap.
var repoOrders = map[string]string{
	"name":   "lower(p.key), lower(r.name), r.id",
	"pushed": "r.pushed_at DESC NULLS LAST, r.created_at DESC, r.id DESC",
}

// Repositories lists a page of the repositories the principal can see, of one project or all, with their total.
// Access is part of the query, so that totals and pages count only what the principal may see.
func (s *Service) Repositories(ctx context.Context, p *identity.Principal, f RepositoryFilter) ([]Repository, int, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	var args db.Args
	where := []string{a.LevelSQL("r", f.Level, &args)}
	if f.Project != "" {
		where = append(where, "upper(p.key) = upper("+args.Add(f.Project)+")")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := args.Add("%" + escapeLike(q) + "%")
		where = append(where, "(r.name ILIKE "+like+" OR r.slug ILIKE "+like+" OR p.key ILIKE "+like+")")
	}
	switch f.Archived {
	case "true":
		where = append(where, "r.archived")
	case "false":
		where = append(where, "NOT r.archived")
	}
	const from = " FROM repositories r JOIN code_projects p ON p.id = r.project_id WHERE "
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*)"+from+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order, ok := repoOrders[f.Sort]
	if !ok {
		order = repoOrders["name"]
	}
	limit := args.Add(max(1, min(f.Limit, 500)))
	offset := args.Add(max(0, f.Offset))
	rows, err := s.pool.Query(ctx, "SELECT "+repoColumns+from+cond+" ORDER BY "+order+" LIMIT "+limit+" OFFSET "+offset, args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*repoRow, error) { return scanRepo(row) })
	if err != nil {
		return nil, 0, err
	}
	out := []Repository{}
	for _, r := range list {
		out = append(out, s.toRepository(a, r))
	}
	return out, total, nil
}

// GetRepository returns a repository.
func (s *Service) GetRepository(ctx context.Context, p *identity.Principal, key, slug string) (*Repository, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	out := s.toRepository(a, r)
	empty, err := s.open(r.ID).Empty(ctx)
	if err != nil {
		return nil, err
	}
	out.Empty = &empty
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	quota := quotaOf(st, r)
	out.Quota = &quota
	return &out, nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func validSlug(slug string) bool {
	return slugPattern.MatchString(slug) && !strings.HasSuffix(slug, ".git") && !strings.Contains(slug, "..")
}

// SlugOf derives a slug from a name.
func SlugOf(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteRune('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}

// NewRepository creates a repository.
type NewRepository struct {
	Name          string `json:"name" minLength:"1" maxLength:"100"`
	Slug          string `json:"slug,omitempty" maxLength:"100" doc:"Default: derived from the name"`
	Description   string `json:"description,omitempty" maxLength:"2000"`
	DefaultBranch string `json:"defaultBranch,omitempty" maxLength:"100" default:"main"`
	Readme        bool   `json:"readme,omitempty" doc:"Start with a README file"`
	// Import the branches and tags of another Git server's repository instead (in the background).
	ImportURL      string `json:"importUrl,omitempty" maxLength:"2000" doc:"http(s) address of a Git repository to import"`
	ImportUsername string `json:"importUsername,omitempty" maxLength:"200"`
	ImportPassword string `json:"importPassword,omitempty" maxLength:"2000" doc:"Password or token; kept only until the import ends"`
}

// CreateRepository creates a repository in a code project (project administrators).
func (s *Service) CreateRepository(ctx context.Context, p *identity.Principal, key string, in NewRepository) (*Repository, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return nil, err
	}
	if !a.ProjectAdmin(pr.ID) {
		return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can create repositories.")
	}
	name := strings.TrimSpace(in.Name)
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if slug == "" {
		slug = SlugOf(name)
	}
	if !validSlug(slug) {
		return nil, apperr.Invalid("repository.slug", "The slug may have lower-case letters, digits, '.', '_' and '-'.")
	}
	importURL := strings.TrimSpace(in.ImportURL)
	if importURL != "" {
		if err := validImportURL(importURL); err != nil {
			return nil, err
		}
		in.Readme = false
	}
	branch := strings.TrimSpace(in.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	if !s.git.ValidBranchName(ctx, branch) {
		return nil, apperr.Invalidf("repository.default-branch", "%q is not a valid branch name.", branch)
	}
	var id int64
	settings, _ := json.Marshal(defaultSettings())
	release, err := s.writing(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO repositories (project_id, slug, name, description, default_branch, settings, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, pr.ID, slug, name, in.Description, branch, settings, p.UserID()).Scan(&id)
		if db.IsUniqueViolation(err, "repositories_slug_key") {
			return apperr.Conflict("repository.slug-taken", "The project already has a repository %s.", slug)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM repository_redirects WHERE upper(project_key) = upper($1) AND lower(slug) = $2", pr.Key, slug); err != nil {
			return err
		}
		path := s.repoPath(id)
		_ = os.RemoveAll(path) // a leftover of a failed creation
		repo, err := s.git.Init(ctx, path, branch)
		if err != nil {
			return err
		}
		if importURL != "" {
			if err := s.startImport(ctx, tx, id, importURL, in.ImportUsername, in.ImportPassword); err != nil {
				return err
			}
		}
		if in.Readme {
			if err := s.initialCommit(ctx, repo, p, branch, name, in.Description); err != nil {
				_ = os.RemoveAll(path)
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.create", TargetType: "repository",
			TargetID: pr.Key + "/" + slug})
	})
	if err != nil {
		if id != 0 {
			_ = os.RemoveAll(s.repoPath(id))
		}
		return nil, err
	}
	return s.GetRepository(ctx, p, pr.Key, slug)
}

// initialCommit writes a README as the first commit of a new repository.
func (s *Service) initialCommit(ctx context.Context, repo *git.Repo, p *identity.Principal, branch, name, description string) error {
	content := "# " + name + "\n"
	if description != "" {
		content += "\n" + description + "\n"
	}
	blob, err := s.git.Run(ctx, repo.Path, &git.Opts{Stdin: strings.NewReader(content)}, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	tree, err := s.git.Run(ctx, repo.Path, &git.Opts{Stdin: strings.NewReader("100644 blob " + strings.TrimSpace(string(blob)) + "\tREADME.md\n")}, "mktree")
	if err != nil {
		return err
	}
	sig := git.Signature{Name: p.User.DisplayName, Email: p.User.Email, When: now()}
	if sig.Email == "" {
		sig.Email = p.User.Username + "@users.noreply"
	}
	sha, err := repo.CommitTree(ctx, strings.TrimSpace(string(tree)), nil, "Initial commit\n", sig, sig)
	if err != nil {
		return err
	}
	return repo.UpdateRef(ctx, "refs/heads/"+branch, sha, "", "kogge: initial commit")
}

// RepositoryUpdate holds optional changes of a repository.
type RepositoryUpdate struct {
	Name          *string       `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Slug          *string       `json:"slug,omitempty" minLength:"1" maxLength:"100"`
	Description   *string       `json:"description,omitempty" maxLength:"2000"`
	DefaultBranch *string       `json:"defaultBranch,omitempty" maxLength:"250"`
	Archived      *bool         `json:"archived,omitempty"`
	Project       *string       `json:"project,omitempty" doc:"Move the repository to the code project with this key"`
	Settings      *RepoSettings `json:"settings,omitempty"`
}

// UpdateRepository changes a repository (repository administrators); renames and moves leave redirects.
func (s *Service) UpdateRepository(ctx context.Context, p *identity.Principal, key, slug string, in RepositoryUpdate) (*Repository, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := repoByPath(ctx, s.pool, key, slug)
	if err != nil {
		return nil, err
	}
	if l := a.Level(r.ProjectID, r.ID); l < LevelAdmin {
		if l < LevelRead {
			return nil, repoNotFound(key, slug)
		}
		return nil, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can change it.")
	}
	name, newSlug, desc, branch, archived, projectID, projectKey := r.Name, r.Slug, r.Description, r.DefaultBranch, r.Archived, r.ProjectID, r.ProjectKey
	settings := r.Settings
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Slug != nil {
		newSlug = strings.ToLower(strings.TrimSpace(*in.Slug))
		if !validSlug(newSlug) {
			return nil, apperr.Invalid("repository.slug", "The slug may have lower-case letters, digits, '.', '_' and '-'.")
		}
	}
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Archived != nil {
		archived = *in.Archived
	}
	if in.DefaultBranch != nil && *in.DefaultBranch != branch {
		branch = *in.DefaultBranch
		if _, err := s.open(r.ID).ResolveBranch(ctx, branch); err != nil {
			return nil, apperr.Invalidf("repository.default-branch", "The branch %s does not exist.", branch)
		}
	}
	if in.Settings != nil {
		settings = *in.Settings
		if err := validSettings(&settings); err != nil {
			return nil, err
		}
	}
	if in.Project != nil && !strings.EqualFold(*in.Project, r.ProjectKey) {
		target, err := s.visibleProject(ctx, s.pool, a, *in.Project)
		if err != nil {
			return nil, err
		}
		if !a.ProjectAdmin(target.ID) {
			return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the target project can move repositories into it.")
		}
		// The source project's branch permissions and grants protect the repository; leaving them is up to its
		// administrators, not to an administrator of the repository alone.
		if !a.ProjectAdmin(r.ProjectID) {
			return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the project %s can move its repositories out of it.",
				r.ProjectKey)
		}
		projectID, projectKey = target.ID, target.Key
	}
	raw, _ := json.Marshal(settings)
	if branch != r.DefaultBranch {
		release, err := s.writing(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE repositories SET project_id = $2, slug = $3, name = $4, description = $5, default_branch = $6, archived = $7,
			settings = $8, updated_at = now(), version = version + 1 WHERE id = $1`, r.ID, projectID, newSlug, name, desc, branch, archived, raw)
		if db.IsUniqueViolation(err, "repositories_slug_key") {
			return apperr.Conflict("repository.slug-taken", "The project already has a repository %s.", newSlug)
		}
		if err != nil {
			return err
		}
		if projectID != r.ProjectID || newSlug != r.Slug {
			if _, err := tx.Exec(ctx, `INSERT INTO repository_redirects (project_key, slug, repository_id) VALUES ($1, $2, $3)
				ON CONFLICT (upper(project_key), lower(slug)) DO UPDATE SET repository_id = EXCLUDED.repository_id`, r.ProjectKey, r.Slug, r.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM repository_redirects WHERE upper(project_key) = upper($1) AND lower(slug) = $2", projectKey, newSlug); err != nil {
				return err
			}
		}
		if branch != r.DefaultBranch {
			if err := s.open(r.ID).SetHead(ctx, branch); err != nil {
				return err
			}
		}
		if projectID != r.ProjectID {
			if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
				return err
			}
			// Build runs belong to the project of their repository, also after a move.
			if s.repoMoved != nil {
				if err := s.repoMoved(ctx, tx, r.ID, projectID); err != nil {
					return err
				}
			}
		}
		if err := s.publish(ctx, tx, RepoEvent{Name: "repository.updated", RepoID: r.ID, ActorID: p.UserID()}); err != nil {
			return err
		}
		details := map[string]any{}
		if projectID != r.ProjectID {
			details["movedFrom"], details["movedTo"] = r.ProjectKey, projectKey
		}
		if newSlug != r.Slug {
			details["renamedFrom"] = r.Slug
		}
		if archived != r.Archived {
			details["archived"] = archived
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.update", TargetType: "repository",
			TargetID: projectKey + "/" + newSlug, Details: details})
	})
	if err != nil {
		return nil, err
	}
	if projectID != r.ProjectID {
		s.authz.Invalidate()
	}
	out, err := s.GetRepository(ctx, p, projectKey, newSlug)
	if apperr.IsNotFound(err) {
		// Administrators manage repositories they may not read (for example after moving one into a project they
		// administer): they get the repository's settings, as after any other change.
		moved, lerr := repoByPath(ctx, s.pool, projectKey, newSlug)
		if lerr != nil {
			return nil, lerr
		}
		a, lerr := s.Access(ctx, p)
		if lerr != nil {
			return nil, lerr
		}
		view := s.toRepository(a, moved)
		return &view, nil
	}
	return out, err
}

func validSettings(st *RepoSettings) error {
	if len(st.MergeStrategies) == 0 {
		return apperr.Invalid("repository.merge-strategies", "Allow at least one merge strategy.")
	}
	seen := map[string]bool{}
	for _, m := range st.MergeStrategies {
		if !slices.Contains(mergeStrategies, m) {
			return apperr.Invalidf("repository.merge-strategies", "Unknown merge strategy %s.", m)
		}
		if seen[m] {
			return apperr.Invalidf("repository.merge-strategies", "The strategy %s is listed twice.", m)
		}
		seen[m] = true
	}
	st.DefaultReviewers = nonNil(st.DefaultReviewers)
	slices.Sort(st.DefaultReviewers)
	st.DefaultReviewers = slices.Compact(st.DefaultReviewers)
	return nil
}

// DeleteRepository deletes a repository with its data (repository administrators).
func (s *Service) DeleteRepository(ctx context.Context, p *identity.Principal, key, slug string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return apperr.Forbidden("repository.admin-required", "Only administrators of the repository can delete it.")
	}
	release, err := s.writing(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM repositories WHERE id = $1", r.ID); err != nil {
			return err
		}
		if err := blob.RemoveRefs(ctx, tx, "lfs", r.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.delete", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug})
	})
	if err != nil {
		return err
	}
	if err := os.RemoveAll(s.repoPath(r.ID)); err != nil {
		s.log.Error("removing a deleted repository", "path", s.repoPath(r.ID), "error", err)
	}
	return nil
}

// RepositoryPermissions lists the grants on a repository (repository administrators).
func (s *Service) RepositoryPermissions(ctx context.Context, p *identity.Principal, key, slug string) ([]Grant, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return nil, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can see its permissions.")
	}
	return s.grantsOf(ctx, 0, r.ID)
}

// SetRepositoryPermissions replaces the grants on a repository (in addition to those of its project).
func (s *Service) SetRepositoryPermissions(ctx context.Context, p *identity.Principal, key, slug string, grants []Grant) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return apperr.Forbidden("repository.admin-required", "Only administrators of the repository can change its permissions.")
	}
	if err := validateGrants(grants, false); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM code_permissions WHERE repository_id = $1", r.ID); err != nil {
			return err
		}
		if err := insertGrants(ctx, tx, 0, r.ID, grants); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.permissions", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug, Details: map[string]any{"grants": len(grants)}})
	})
	if err == nil {
		s.authz.Invalidate()
	}
	return err
}

var errNoRepo = errors.New("no repository")
