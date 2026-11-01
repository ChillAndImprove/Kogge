// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/legacy"
)

// Importer loads data of migrated products into the code module. It acts for the system: there are
// no permission checks, and authors and timestamps come from the source. Every entity is recorded in legacy_ids
// under its source id, so loads can be repeated (delta loads before the cut-over).
type Importer struct {
	s      *Service
	tx     pgx.Tx
	source string
}

// NewImporter returns an importer working in a transaction; source identifies the migrated system, e.g.
// "bitbucket:https://bitbucket.example.com".
func (s *Service) NewImporter(tx pgx.Tx, source string) *Importer {
	return &Importer{s: s, tx: tx, source: source}
}

func (im *Importer) ctx() context.Context { return context.Background() }

func (im *Importer) find(typ, id string) (int64, bool, error) {
	return legacy.Lookup(im.ctx(), im.tx, im.source, typ, id)
}

func (im *Importer) record(typ, id, entity string, entityID int64) error {
	return legacy.Record(im.ctx(), im.tx, im.source, typ, id, entity, entityID)
}

// ImportProject is a code project of a migrated system.
type ImportProject struct {
	SrcID, Key, Name, Description string
	CreatorID                     int64
}

// Project ensures a code project: the one recorded for the source id, else one with the key, else a new one.
func (im *Importer) Project(p ImportProject) (int64, error) {
	ctx := im.ctx()
	if id, ok, err := im.find("code-project", p.SrcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(ctx, "UPDATE code_projects SET name = $2, description = $3, updated_at = now() WHERE id = $1", id, p.Name,
				p.Description)
		}
		return id, err
	}
	var id int64
	err := im.tx.QueryRow(ctx, "SELECT id FROM code_projects WHERE upper(key) = upper($1)", p.Key).Scan(&id)
	if db.IsNoRows(err) {
		err = im.tx.QueryRow(ctx, `INSERT INTO code_projects (key, name, description, created_by) VALUES (upper($1), $2, $3, $4)
			RETURNING id`, p.Key, p.Name, p.Description, nullID(p.CreatorID)).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	return id, im.record("code-project", p.SrcID, "code-project", id)
}

// ImportRepository is a repository of a migrated system.
type ImportRepository struct {
	SrcID, Slug, Name, Description, DefaultBranch string
	Archived                                      bool
	CreatorID                                     int64
}

// Repository ensures a repository of a project (with an empty Git repository on disk when it is new) and reports
// whether its history still has to be loaded.
func (im *Importer) Repository(projectID int64, r ImportRepository) (id int64, empty bool, err error) {
	ctx := im.ctx()
	if id, ok, err := im.find("repository", r.SrcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(ctx, "UPDATE repositories SET name = $2, description = $3, archived = $4, updated_at = now() WHERE id = $1",
				id, r.Name, r.Description, r.Archived)
		}
		return id, false, err
	}
	err = im.tx.QueryRow(ctx, "SELECT id FROM repositories WHERE project_id = $1 AND lower(slug) = lower($2)", projectID, r.Slug).Scan(&id)
	if db.IsNoRows(err) {
		settings, _ := json.Marshal(defaultSettings())
		branch := r.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		if err = im.tx.QueryRow(ctx, `INSERT INTO repositories (project_id, slug, name, description, default_branch, archived, settings, created_by)
			VALUES ($1, lower($2), $3, $4, $5, $6, $7, $8) RETURNING id`, projectID, r.Slug, r.Name, r.Description, branch, r.Archived,
			settings, nullID(r.CreatorID)).Scan(&id); err != nil {
			return 0, false, err
		}
		path := im.s.repoPath(id)
		_ = os.RemoveAll(path)
		if _, err := im.s.git.Init(ctx, path, branch); err != nil {
			return 0, false, err
		}
		empty = true
	} else if err != nil {
		return 0, false, err
	}
	return id, empty, im.record("repository", r.SrcID, "repository", id)
}

// Grant adds a permission of a code project (repoID 0) or a repository; holderType is user, group, logged-in or
// anyone.
func (im *Importer) Grant(projectID, repoID int64, permission, holderType string, holderID int64) error {
	if repoID != 0 {
		projectID = 0
	}
	return insertGrants(im.ctx(), im.tx, projectID, repoID, []Grant{{Permission: permission, Holder: Holder{Type: holderType, ID: holderID}}})
}

// BranchRule ensures a branch permission of a code project (repoID 0) or a repository.
func (im *Importer) BranchRule(projectID, repoID int64, srcID, pattern, kind string, exemptUsers, exemptGroups []int64) error {
	if id, ok, err := im.find("branch-permission", srcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(im.ctx(), `UPDATE branch_permissions SET pattern = $2, kind = $3, exempt_users = $4, exempt_groups = $5
				WHERE id = $1`, id, pattern, kind, nonNil(exemptUsers), nonNil(exemptGroups))
		}
		return err
	}
	if repoID != 0 {
		projectID = 0
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO branch_permissions (project_id, repository_id, pattern, kind, exempt_users, exempt_groups)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, nullID(projectID), nullID(repoID), pattern, kind, nonNil(exemptUsers),
		nonNil(exemptGroups)).Scan(&id); err != nil {
		return err
	}
	return im.record("branch-permission", srcID, "branch-permission", id)
}

// ImportPullRequest is a pull request of a migrated system.
type ImportPullRequest struct {
	SrcID       string
	Number      int // kept if free
	Title       string
	Description *docmodel.Doc
	AuthorID    int64
	Source      string // branches
	Target      string
	SourceSHA   string
	TargetSHA   string
	State       string // open, merged or declined
	Draft       bool
	ClosedBy    int64
	Created     time.Time
	Updated     time.Time
	Closed      *time.Time
}

// PullRequest ensures a pull request of a repository. The merge base is computed from the repository, whose history
// must be loaded.
func (im *Importer) PullRequest(repoID int64, pr ImportPullRequest) (int64, error) {
	ctx := im.ctx()
	desc := pr.Description
	if desc == nil {
		desc = &docmodel.Doc{Type: "doc"}
	}
	raw, err := docmodel.Marshal(desc)
	if err != nil {
		return 0, err
	}
	switch pr.State {
	case StateOpen, StateMerged, StateDeclined:
	default:
		return 0, errors.New("unknown pull request state " + pr.State)
	}
	repo := im.s.open(repoID)
	base := pr.TargetSHA
	if mb, err := repo.MergeBase(ctx, pr.SourceSHA, pr.TargetSHA); err == nil && mb != "" {
		base = mb
	}
	conflicted := false
	if pr.State == StateOpen && pr.SourceSHA != "" && pr.TargetSHA != "" {
		if _, conflicts, err := repo.MergeTree(ctx, pr.TargetSHA, pr.SourceSHA); err == nil {
			conflicted = len(conflicts) > 0
		}
	}
	if id, ok, err := im.find("pull-request", pr.SrcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(ctx, `UPDATE pull_requests SET title = $2, description = $3, description_text = $4, state = $5,
				source_sha = $6, target_sha = $7, merge_base = $8, closed_by = $9, closed_at = $10, updated_at = $11, draft = $12,
				conflicted = $13 WHERE id = $1`, id, pr.Title, raw, docmodel.PlainText(desc), pr.State, pr.SourceSHA, pr.TargetSHA, base,
				nullID(pr.ClosedBy), pr.Closed, pr.Updated, pr.Draft && pr.State == StateOpen, conflicted)
		}
		return id, err
	}
	number := pr.Number
	var taken bool
	if err := im.tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pull_requests WHERE repository_id = $1 AND number = $2)", repoID,
		number).Scan(&taken); err != nil {
		return 0, err
	}
	if taken || number <= 0 {
		if err := im.tx.QueryRow(ctx, "SELECT coalesce(max(number), 0) + 1 FROM pull_requests WHERE repository_id = $1", repoID).
			Scan(&number); err != nil {
			return 0, err
		}
	}
	var id int64
	err = im.tx.QueryRow(ctx, `INSERT INTO pull_requests (repository_id, number, title, description, description_text, author_id,
		source_branch, target_branch, source_sha, target_sha, merge_base, state, closed_by, closed_at, created_at, updated_at, draft, conflicted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) RETURNING id`, repoID, number, pr.Title, raw,
		docmodel.PlainText(desc), nullID(pr.AuthorID), pr.Source, pr.Target, pr.SourceSHA, pr.TargetSHA, base, pr.State, nullID(pr.ClosedBy),
		pr.Closed, pr.Created, pr.Updated, pr.Draft && pr.State == StateOpen, conflicted).Scan(&id)
	if db.IsUniqueViolation(err, "pull_requests_open_key") {
		// Another open pull request between the same branches: this one is kept as declined.
		pr.State = StateDeclined
		err = im.tx.QueryRow(ctx, `INSERT INTO pull_requests (repository_id, number, title, description, description_text, author_id,
			source_branch, target_branch, source_sha, target_sha, merge_base, state, closed_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'declined', $14, $12, $13) RETURNING id`, repoID, number, pr.Title, raw,
			docmodel.PlainText(desc), nullID(pr.AuthorID), pr.Source, pr.Target, pr.SourceSHA, pr.TargetSHA, base, pr.Created, pr.Updated,
			pr.Updated).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	if _, err := im.tx.Exec(ctx, `UPDATE repositories SET next_pr_number = greatest(next_pr_number, $2 + 1) WHERE id = $1`, repoID,
		number); err != nil {
		return 0, err
	}
	return id, im.record("pull-request", pr.SrcID, "pull-request", id)
}

// Reviewer sets a reviewer of a pull request; state is unapproved, approved or needs-work.
func (im *Importer) Reviewer(prID, userID int64, state string, at time.Time) error {
	if userID == 0 {
		return nil
	}
	_, err := im.tx.Exec(im.ctx(), `INSERT INTO pull_request_reviewers (pull_request_id, user_id, state, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (pull_request_id, user_id) DO UPDATE SET state = EXCLUDED.state, updated_at = EXCLUDED.updated_at`, prID, userID, state, at)
	return err
}

// Activity records an event of a pull request (opened, reviewed, merged, declined, rescoped), once per source id.
func (im *Importer) Activity(prID int64, srcID string, actorID int64, kind string, data map[string]any, at time.Time) error {
	if _, ok, err := im.find("pull-request-activity", srcID); err != nil || ok {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO pull_request_activities (pull_request_id, actor_id, kind, data, at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, prID, nullID(actorID), kind, data, at).Scan(&id); err != nil {
		return err
	}
	return im.record("pull-request-activity", srcID, "pull-request-activity", id)
}

// ImportComment is a comment on a pull request, possibly on a line of the diff, possibly a reply.
type ImportComment struct {
	SrcID     string
	ParentID  int64 // Kogge id of the comment replied to
	AuthorID  int64
	Body      *docmodel.Doc
	Path      string
	Line      int
	Side      string // old or new, with a line
	CommitSHA string // the source commit the comment was made on; default: the pull request's
	Outdated  bool   // the commented line is not part of the current diff any more
	Task      bool
	Resolved  *time.Time
	Created   time.Time
	Updated   time.Time
}

// Comment ensures a comment of a pull request and returns its id.
func (im *Importer) Comment(prID int64, c ImportComment) (int64, error) {
	ctx := im.ctx()
	body := c.Body
	if body == nil {
		body = &docmodel.Doc{Type: "doc"}
	}
	raw, err := docmodel.Marshal(body)
	if err != nil {
		return 0, err
	}
	if id, ok, err := im.find("pull-request-comment", c.SrcID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(ctx, `UPDATE pull_request_comments SET body = $2, body_text = $3, resolved_at = $4, updated_at = $5,
				edited = $5 > created_at, outdated = outdated OR $6 WHERE id = $1`, id, raw, docmodel.PlainText(body), c.Resolved, c.Updated,
				c.Outdated)
		}
		return id, err
	}
	var path, side, sha, lineText any
	var line any
	if c.Path != "" && c.ParentID == 0 {
		var repoID int64
		var source, base string
		if err := im.tx.QueryRow(ctx, "SELECT repository_id, source_sha, coalesce(merge_base, '') FROM pull_requests WHERE id = $1", prID).
			Scan(&repoID, &source, &base); err != nil {
			return 0, err
		}
		path, sha = c.Path, cmp.Or(c.CommitSHA, source)
		if c.Line > 0 && (c.Side == "old" || c.Side == "new") {
			line, side = c.Line, c.Side
			// The text of the line, as for comments made in Kogge: later pushes mark the comment outdated when it changes.
			rev := sha.(string)
			if c.Side == "old" {
				rev = base
			}
			repo := im.s.open(repoID)
			if info, err := repo.Stat(ctx, rev, c.Path); err == nil {
				if data, err := repo.ReadBlob(ctx, info.SHA, maxFileView); err == nil {
					if lines := strings.Split(string(data), "\n"); c.Line <= len(lines) {
						lineText = lines[c.Line-1]
					}
				}
			}
		}
	}
	var id int64
	if err := im.tx.QueryRow(ctx, `INSERT INTO pull_request_comments (pull_request_id, parent_id, author_id, body, body_text, path, line,
		side, commit_sha, line_text, outdated, task, resolved_at, created_at, updated_at, edited)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15::timestamptz > $14::timestamptz) RETURNING id`, prID, nullID(c.ParentID),
		nullID(c.AuthorID), raw, docmodel.PlainText(body), path, line, side, sha, lineText, c.Outdated && line != nil, c.Task, c.Resolved,
		c.Created, c.Updated).Scan(&id); err != nil {
		return 0, err
	}
	return id, im.record("pull-request-comment", c.SrcID, "pull-request-comment", id)
}

// UpdateSettings changes the pull request settings of a repository (merge checks, strategies, default reviewers).
func (im *Importer) UpdateSettings(repoID int64, update func(*RepoSettings)) error {
	ctx := im.ctx()
	var raw []byte
	if err := im.tx.QueryRow(ctx, "SELECT settings FROM repositories WHERE id = $1 FOR UPDATE", repoID).Scan(&raw); err != nil {
		return err
	}
	st := defaultSettings()
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	update(&st)
	if len(st.MergeStrategies) == 0 {
		st.MergeStrategies = defaultSettings().MergeStrategies
	}
	st.RequiredApprovals = min(max(st.RequiredApprovals, 0), 20)
	st.RequiredBuilds = min(max(st.RequiredBuilds, 0), 20)
	st.DefaultReviewers = nonNil(st.DefaultReviewers)
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = im.tx.Exec(ctx, "UPDATE repositories SET settings = $2, updated_at = now() WHERE id = $1", repoID, raw)
	return err
}

// ImportLFSObject stores a Git LFS object of a migrated repository from a local file, unless the repository has it.
func (s *Service) ImportLFSObject(ctx context.Context, repoID int64, oid, file string) error {
	if !oidPattern.MatchString(oid) {
		return fmt.Errorf("invalid LFS object id %q", oid)
	}
	if s.lfsObjectOf(ctx, repoID, oid) {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	ref, err := s.blobs.Put(ctx, f, 0, "application/octet-stream")
	if err != nil {
		return err
	}
	if ref.SHA256 != oid {
		return fmt.Errorf("%s does not hold the LFS object %s", file, oid)
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.addLFSObject(ctx, tx, repoID, oid, ref.Size, 0)
	})
}

// LoadMirror loads all branches and tags of a local mirror (a bare clone of the migrated repository) into a
// repository and keeps the mirror's other refs (e.g. the commits of pull requests) hidden under refs/imported/.
// Repeated loads bring the changes since: the repository's branches and tags become those of the mirror. The
// changes are recorded like a push with protocol import. It runs outside of a transaction.
func (s *Service) LoadMirror(ctx context.Context, repoID int64, mirror, defaultBranch string) error {
	path := s.repoPath(repoID)
	repo := s.open(repoID)
	if defaultBranch == "" {
		if err := s.pool.QueryRow(ctx, "SELECT default_branch FROM repositories WHERE id = $1", repoID).Scan(&defaultBranch); err != nil {
			return err
		}
	}
	release, err := s.opt.Gate.Enter(ctx, maintenanceWait)
	if err != nil {
		return err
	}
	updates, head, err := func() ([]RefUpdate, string, error) {
		defer release()
		before, err := repo.Refs(ctx, "refs/heads/", "refs/tags/")
		if err != nil {
			return nil, "", err
		}
		var stderr bytes.Buffer
		cmd := s.git.Command(ctx, path, []string{"GIT_ALLOW_PROTOCOL=file"}, "fetch", "--prune", "--no-tags", "--quiet", "--end-of-options", mirror,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "+refs/pull-requests/*:"+importedRefs+"pull-requests/*")
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, "", fmt.Errorf("git could not fetch from %s: %s", mirror, gitMessage(stderr.String()))
		}
		// Refs kept for history are not offered to clients.
		for _, key := range []string{"uploadpack.hideRefs", "receive.hideRefs"} {
			out, _ := s.git.Run(ctx, path, nil, "config", "--get-all", key)
			if !slices.Contains(strings.Fields(string(out)), strings.TrimSuffix(importedRefs, "/")) {
				if _, err := s.git.Run(ctx, path, nil, "config", "--add", key, strings.TrimSuffix(importedRefs, "/")); err != nil {
					return nil, "", err
				}
			}
		}
		after, err := repo.Refs(ctx, "refs/heads/", "refs/tags/")
		if err != nil {
			return nil, "", err
		}
		zero := strings.Repeat("0", 40)
		old := map[string]string{}
		for _, r := range before {
			old[r.Name] = r.SHA
		}
		var updates []RefUpdate
		head := ""
		for _, r := range after {
			if o, ok := old[r.Name]; !ok || o != r.SHA {
				updates = append(updates, RefUpdate{Ref: r.Name, Old: cmp.Or(o, zero), New: r.SHA})
			}
			delete(old, r.Name)
			if b, ok := strings.CutPrefix(r.Name, "refs/heads/"); ok && (head == "" || b == defaultBranch) {
				head = b
			}
		}
		for name, sha := range old {
			updates = append(updates, RefUpdate{Ref: name, Old: sha, New: zero})
		}
		slices.SortFunc(updates, func(a, b RefUpdate) int { return strings.Compare(a.Ref, b.Ref) })
		if head != "" {
			if err := repo.SetHead(ctx, head); err != nil {
				return nil, "", err
			}
		}
		return updates, head, nil
	}()
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, "UPDATE repositories SET default_branch = coalesce(nullif($2, ''), default_branch), size_bytes = $3, updated_at = now() WHERE id = $1",
		repoID, head, repo.Size()); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	_, err = s.recordPush(ctx, &pushContext{repoID: repoID, protocol: "import"}, updates)
	return err
}

// RepoHasHistory reports whether a repository has branches (an earlier load brought its history).
func (s *Service) RepoHasHistory(ctx context.Context, repoID int64) bool {
	empty, err := s.open(repoID).Empty(ctx)
	return err == nil && !empty
}

// RepositoryID finds a repository by project key and slug for a migration.
func (s *Service) RepositoryID(ctx context.Context, key, slug string) (id int64, defaultBranch string, err error) {
	r, err := repoByPath(ctx, s.pool, key, slug)
	if err != nil {
		return 0, "", err
	}
	return r.ID, r.DefaultBranch, nil
}

// migrationSignature is the author of the commits of proposals.
const migrationName, migrationEmail = "Kogge migration", "migration@kogge.invalid"

// Proposal is a file a migration offers as a pull request, e.g. pipelines converted from another build server.
type Proposal struct {
	SrcID       string // identifies the proposal across loads
	Branch      string
	Path        string
	Content     []byte
	Message     string // of the commit
	Title       string
	Description *docmodel.Doc
}

// Propose commits the file on top of the default branch to the proposal's branch and opens a pull request into the
// default branch. Later loads update an open proposal, unless people pushed to its branch; a merged or declined
// proposal is not made again. It returns the pull request's number, or 0 when there is nothing to propose.
func (s *Service) Propose(ctx context.Context, source string, repoID int64, p Proposal) (int, error) {
	file, err := cleanRepoPath(p.Path)
	if err != nil {
		return 0, err
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return 0, err
	}
	var prID int64
	number, state := 0, ""
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, ok, err := legacy.Lookup(ctx, tx, source, "pull-request", p.SrcID)
		if err != nil || !ok {
			return err
		}
		prID = id
		return tx.QueryRow(ctx, "SELECT number, state FROM pull_requests WHERE id = $1", id).Scan(&number, &state)
	}); err != nil {
		return 0, err
	}
	if prID != 0 && state != StateOpen {
		return 0, nil
	}
	repo := s.open(repoID)
	base, err := repo.ResolveBranch(ctx, r.DefaultBranch)
	if err != nil {
		return 0, fmt.Errorf("the default branch %s: %w", r.DefaultBranch, err)
	}
	baseTree, err := repo.TreeOf(ctx, base)
	if err != nil {
		return 0, err
	}
	blob, err := repo.HashObject(ctx, p.Content)
	if err != nil {
		return 0, err
	}
	tree, err := repo.TreeWith(ctx, baseTree, file, blob, "100644")
	if err != nil {
		return 0, err
	}
	if tree == baseTree && prID == 0 {
		return 0, nil // the default branch has the file already
	}
	head, err := repo.ResolveBranch(ctx, p.Branch)
	if err != nil && !errors.Is(err, git.ErrNotFound) {
		return 0, err
	}
	update := true
	if head != "" {
		c, err := repo.Commit(ctx, head)
		if err != nil {
			return 0, err
		}
		headTree, err := repo.TreeOf(ctx, head)
		if err != nil {
			return 0, err
		}
		switch {
		case c.Committer.Email != migrationEmail:
			update = false // people work on the proposal
		case headTree == tree && slices.Equal(c.Parents, []string{base}):
			update = false // unchanged
		}
	}
	if update {
		sig := git.Signature{Name: migrationName, Email: migrationEmail, When: now()}
		sha, err := repo.CommitTree(ctx, tree, []string{base}, p.Message+"\n", sig, sig)
		if err != nil {
			return 0, err
		}
		release, err := s.opt.Gate.Enter(ctx, maintenanceWait)
		if err != nil {
			return 0, err
		}
		err = repo.UpdateRef(ctx, "refs/heads/"+p.Branch, sha, head, "kogge: migration proposal")
		release()
		if err != nil {
			return 0, err
		}
		if _, err := s.recordPush(ctx, &pushContext{repoID: repoID, protocol: "import"},
			[]RefUpdate{{Ref: "refs/heads/" + p.Branch, Old: cmp.Or(head, strings.Repeat("0", len(sha))), New: sha}}); err != nil {
			return 0, err
		}
		head = sha
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		im := s.NewImporter(tx, source)
		at := now()
		id, err := im.PullRequest(repoID, ImportPullRequest{SrcID: p.SrcID, Title: p.Title, Description: p.Description, Source: p.Branch,
			Target: r.DefaultBranch, SourceSHA: head, TargetSHA: base, State: StateOpen, Created: at, Updated: at})
		if err != nil {
			return err
		}
		if prID == 0 {
			if err := im.Activity(id, p.SrcID+":opened", 0, "opened", map[string]any{"source": p.Branch, "target": r.DefaultBranch}, at); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, "SELECT number FROM pull_requests WHERE id = $1", id).Scan(&number)
	})
	return number, err
}

// ImportedRefs returns the branches and tags of a repository with the objects they point to, for the validation
// of a migration, which compares them with the extracted mirror.
func (s *Service) ImportedRefs(ctx context.Context, repoID int64) (map[string]string, error) {
	refs, err := s.open(repoID).Refs(ctx, "refs/heads/", "refs/tags/")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for _, r := range refs {
		out[r.Name] = r.SHA
	}
	return out, nil
}

// ImportedFile returns up to limit bytes of a file on a branch of a repository, for the validation of a
// migration's proposals.
func (s *Service) ImportedFile(ctx context.Context, repoID int64, branch, filePath string, limit int64) ([]byte, error) {
	repo := s.open(repoID)
	commit, err := repo.ResolveBranch(ctx, branch)
	if err != nil {
		return nil, err
	}
	info, err := repo.Stat(ctx, commit, filePath)
	if err != nil {
		return nil, err
	}
	return repo.ReadBlob(ctx, info.SHA, limit)
}
