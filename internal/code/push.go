// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Branch permission kinds.
const (
	RestrictPush    = "restrict-push"
	NoDelete        = "no-delete"
	NoRewrite       = "no-rewrite"
	PullRequestOnly = "pull-request-only"
)

// BranchPermission restricts changes of the branches or tags matching a pattern. Patterns name branches
// ("main", "release/*", "feature/**") or, starting with refs/, any refs ("refs/tags/v*"). "*" matches within a
// path segment, "**" across segments; "**/" also matches no segment at all ("**/hotfix" matches "hotfix").
type BranchPermission struct {
	ID           int64   `json:"id,omitempty"`
	Pattern      string  `json:"pattern" minLength:"1" maxLength:"200"`
	Kind         string  `json:"kind" enum:"restrict-push,no-delete,no-rewrite,pull-request-only"`
	ExemptUsers  []int64 `json:"exemptUsers" doc:"Users the rule does not apply to"`
	ExemptGroups []int64 `json:"exemptGroups" doc:"Groups the rule does not apply to"`
	Inherited    bool    `json:"inherited" readOnly:"true" doc:"Defined on the code project"`
}

var patternCache sync.Map // pattern → *regexp.Regexp

func patternRegexp(pattern string) *regexp.Regexp {
	if re, ok := patternCache.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	var b strings.Builder
	b.WriteString("^")
	// Characters, not bytes: names like "prüfung" are valid branch names.
	p := []rune(pattern)
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+2 < len(p) && p[i+1] == '*' && p[i+2] == '/':
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	patternCache.Store(pattern, re)
	return re
}

// matchRef reports whether a pattern matches a full ref name.
func matchRef(pattern, ref string) bool {
	if strings.HasPrefix(pattern, "refs/") {
		return patternRegexp(pattern).MatchString(ref)
	}
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	return ok && patternRegexp(pattern).MatchString(branch)
}

func (s *Service) branchPermissions(ctx context.Context, q db.Querier, projectID, repoID int64) ([]BranchPermission, error) {
	rows, err := q.Query(ctx, `SELECT id, pattern, kind, exempt_users, exempt_groups, project_id IS NOT NULL FROM branch_permissions
		WHERE repository_id = $1 OR project_id = $2 ORDER BY project_id IS NULL, id`, repoID, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (BranchPermission, error) {
		var b BranchPermission
		err := r.Scan(&b.ID, &b.Pattern, &b.Kind, &b.ExemptUsers, &b.ExemptGroups, &b.Inherited)
		return b, err
	})
}

func exempt(b BranchPermission, p *identity.Principal) bool {
	if p == nil || !p.Authenticated() {
		return false
	}
	if slices.Contains(b.ExemptUsers, p.UserID()) {
		return true
	}
	for _, g := range p.Groups {
		if slices.Contains(b.ExemptGroups, g) {
			return true
		}
	}
	return false
}

// checkPush applies the push policy to the updates of a push and returns the reasons for rejecting it.
func (s *Service) checkPush(ctx context.Context, pc *pushContext, updates []RefUpdate) ([]string, error) {
	r, err := repoByID(ctx, s.pool, pc.repoID)
	if err != nil {
		return nil, err
	}
	if r.Archived {
		return []string{"The repository is archived; it cannot be changed."}, nil
	}
	if r.Import != nil && r.Import.State == "running" && pc.protocol != "import" {
		return []string{"The repository is being imported; it can be changed when the import is done."}, nil
	}
	rules, err := s.branchPermissions(ctx, s.pool, r.ProjectID, r.ID)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, u := range updates {
		short := strings.TrimPrefix(strings.TrimPrefix(u.Ref, "refs/heads/"), "refs/tags/")
		switch {
		case strings.HasPrefix(u.Ref, "refs/heads/"), strings.HasPrefix(u.Ref, "refs/tags/"), strings.HasPrefix(u.Ref, "refs/notes/"):
		default:
			problems = append(problems, fmt.Sprintf("%s: only branches, tags and notes can be pushed.", u.Ref))
			continue
		}
		if u.Deleted() && u.Ref == "refs/heads/"+r.DefaultBranch {
			problems = append(problems, fmt.Sprintf("%s is the default branch; it cannot be deleted.", short))
			continue
		}
		for _, b := range rules {
			if !matchRef(b.Pattern, u.Ref) || exempt(b, pc.principal) {
				continue
			}
			rewrite := !u.Created() && !u.Deleted() && (!u.FastForward || strings.HasPrefix(u.Ref, "refs/tags/"))
			switch {
			case b.Kind == RestrictPush:
				problems = append(problems, fmt.Sprintf("%s: you may not push to it (branch permission %q).", short, b.Pattern))
			case b.Kind == PullRequestOnly:
				problems = append(problems, fmt.Sprintf("%s: changes need a pull request (branch permission %q).", short, b.Pattern))
			case b.Kind == NoDelete && u.Deleted():
				problems = append(problems, fmt.Sprintf("%s cannot be deleted (branch permission %q).", short, b.Pattern))
			case b.Kind == NoRewrite && rewrite:
				problems = append(problems, fmt.Sprintf("%s: history cannot be rewritten (branch permission %q); pull and merge first.",
					short, b.Pattern))
			default:
				continue
			}
			break
		}
	}
	return problems, nil
}

// recordPush records a push and starts its processing; it returns hints for the client. The caller holds the Git
// write lock.
func (s *Service) recordPush(ctx context.Context, pc *pushContext, updates []RefUpdate) ([]string, error) {
	r, err := repoByID(ctx, s.pool, pc.repoID)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(updates)
	var userID int64
	if pc.principal != nil {
		userID = pc.principal.UserID()
	}
	// What the push brought, for the activity streams; they leave out imports and the merges of pull requests.
	var commits []byte
	if pc.protocol != "import" && pc.protocol != "merge" {
		if sum, err := s.summarisePush(ctx, r.ID, updates); err != nil {
			s.log.Warn("reading the commits of a push", "repository", r.ID, "error", err)
		} else {
			commits, _ = json.Marshal(sum)
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var pushID int64
		if err := tx.QueryRow(ctx, `INSERT INTO pushes (repository_id, user_id, deploy_key_id, updates, protocol, commits)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, r.ID, nullID(userID), nullID(pc.deployKeyID), raw, pc.protocol, commits).Scan(&pushID); err != nil {
			return err
		}
		// The objects of the push count at once, for the quota of the next one; the push's job measures the size.
		if _, err := tx.Exec(ctx, "UPDATE repositories SET pushed_at = now(), updated_at = now(), size_bytes = size_bytes + $2 WHERE id = $1",
			r.ID, pc.incoming); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "repository.pushed", RepoID: r.ID, PushID: pushID, ActorID: userID, Updates: updates})
	})
	if err != nil {
		return nil, err
	}
	var messages []string
	for _, u := range updates {
		branch, ok := strings.CutPrefix(u.Ref, "refs/heads/")
		if !ok || !u.Created() || branch == r.DefaultBranch {
			continue
		}
		link := strings.TrimRight(s.opt.PublicURL.String(), "/") + "/code/" + strings.ToLower(r.ProjectKey) + "/" + r.Slug +
			"/pull-requests/new?source=" + url.QueryEscape(branch)
		messages = append(messages, "", "Create a pull request for '"+branch+"':", "  "+link, "")
	}
	return messages, nil
}

// BranchPermissions lists the branch permissions of a repository, including those of its project (repository
// administrators: who may bypass a rule is not for everyone to read).
func (s *Service) BranchPermissions(ctx context.Context, p *identity.Principal, key, slug string) ([]BranchPermission, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelAdmin)
	if err != nil {
		return nil, err
	}
	list, err := s.branchPermissions(ctx, s.pool, r.ProjectID, r.ID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].ExemptUsers, list[i].ExemptGroups = nonNil(list[i].ExemptUsers), nonNil(list[i].ExemptGroups)
	}
	return nonNil(list), nil
}

// SetBranchPermissions replaces the branch permissions of a repository (repository administrators).
func (s *Service) SetBranchPermissions(ctx context.Context, p *identity.Principal, key, slug string, list []BranchPermission) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return apperr.Forbidden("repository.admin-required", "Only administrators of the repository can change its branch permissions.")
	}
	for _, b := range list {
		if strings.TrimSpace(b.Pattern) == "" || strings.ContainsAny(b.Pattern, " \t\n\\") {
			return apperr.Invalidf("branch-permission.pattern", "The pattern %q is not valid.", b.Pattern)
		}
		switch b.Kind {
		case RestrictPush, NoDelete, NoRewrite, PullRequestOnly:
		default:
			return apperr.Invalidf("branch-permission.kind", "Unknown kind %s.", b.Kind)
		}
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM branch_permissions WHERE repository_id = $1", r.ID); err != nil {
			return err
		}
		for _, b := range list {
			if b.Inherited {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO branch_permissions (repository_id, pattern, kind, exempt_users, exempt_groups)
				VALUES ($1, $2, $3, $4, $5)`, r.ID, strings.TrimSpace(b.Pattern), b.Kind, nonNil(b.ExemptUsers), nonNil(b.ExemptGroups)); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.branch-permissions", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug, Details: map[string]any{"rules": len(list)}})
	})
}

// ProjectBranchPermissions lists the branch permissions of a code project (its administrators).
func (s *Service) ProjectBranchPermissions(ctx context.Context, p *identity.Principal, key string) ([]BranchPermission, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return nil, err
	}
	if !a.ProjectAdmin(pr.ID) {
		return nil, apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can see its branch permissions.")
	}
	list, err := s.branchPermissions(ctx, s.pool, pr.ID, 0)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].ExemptUsers, list[i].ExemptGroups, list[i].Inherited = nonNil(list[i].ExemptUsers), nonNil(list[i].ExemptGroups), false
	}
	return nonNil(list), nil
}

// SetProjectBranchPermissions replaces the branch permissions of a code project (project administrators).
func (s *Service) SetProjectBranchPermissions(ctx context.Context, p *identity.Principal, key string, list []BranchPermission) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	pr, err := s.visibleProject(ctx, s.pool, a, key)
	if err != nil {
		return err
	}
	if !a.ProjectAdmin(pr.ID) {
		return apperr.Forbidden("code-project.admin-required", "Only administrators of the code project can change its branch permissions.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM branch_permissions WHERE project_id = $1", pr.ID); err != nil {
			return err
		}
		for _, b := range list {
			switch b.Kind {
			case RestrictPush, NoDelete, NoRewrite, PullRequestOnly:
			default:
				return apperr.Invalidf("branch-permission.kind", "Unknown kind %s.", b.Kind)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO branch_permissions (project_id, pattern, kind, exempt_users, exempt_groups)
				VALUES ($1, $2, $3, $4, $5)`, pr.ID, strings.TrimSpace(b.Pattern), b.Kind, nonNil(b.ExemptUsers), nonNil(b.ExemptGroups)); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "code-project.branch-permissions", TargetType: "code-project",
			TargetID: pr.Key, Details: map[string]any{"rules": len(list)}})
	})
}

// CreateBranch creates a branch at a ref (write access; branch permissions apply).
func (s *Service) CreateBranch(ctx context.Context, p *identity.Principal, key, slug, name, from string) (*Branch, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if !s.git.ValidBranchName(ctx, name) {
		return nil, apperr.Invalidf("branch.name", "%q is not a valid branch name.", name)
	}
	repo := s.open(r.ID)
	sha, err := s.resolve(ctx, repo, r, from)
	if err != nil {
		return nil, err
	}
	update := RefUpdate{Ref: "refs/heads/" + name, Old: strings.Repeat("0", len(sha)), New: sha}
	pc := &pushContext{repoID: r.ID, principal: p, protocol: "web"}
	problems, err := s.checkPush(ctx, pc, []RefUpdate{update})
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, apperr.Forbidden("branch.denied", "%s", strings.Join(problems, " "))
	}
	release, err := s.writing(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := repo.UpdateRef(ctx, update.Ref, sha, "", "kogge: branch created by "+p.User.Username); err != nil {
		if errors.Is(err, git.ErrStale) {
			return nil, apperr.Conflict("branch.exists", "The branch %s already exists.", name)
		}
		return nil, err
	}
	if _, err := s.recordPush(ctx, pc, []RefUpdate{update}); err != nil {
		return nil, err
	}
	c, err := repo.Commit(ctx, sha)
	if err != nil {
		return nil, err
	}
	infos, err := s.commitInfos(ctx, []git.Commit{*c})
	if err != nil {
		return nil, err
	}
	return &Branch{Name: name, Commit: infos[0]}, nil
}

// DeleteBranch deletes a branch (write access; branch permissions apply).
func (s *Service) DeleteBranch(ctx context.Context, p *identity.Principal, key, slug, name string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return err
	}
	repo := s.open(r.ID)
	sha, err := repo.ResolveBranch(ctx, name)
	if err != nil {
		return apperr.NotFound("branch.not-found", "The branch %s does not exist.", name)
	}
	update := RefUpdate{Ref: "refs/heads/" + name, Old: sha, New: strings.Repeat("0", len(sha))}
	pc := &pushContext{repoID: r.ID, principal: p, protocol: "web"}
	problems, err := s.checkPush(ctx, pc, []RefUpdate{update})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return apperr.Forbidden("branch.denied", "%s", strings.Join(problems, " "))
	}
	release, err := s.writing(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := repo.DeleteRef(ctx, update.Ref, sha); err != nil {
		if errors.Is(err, git.ErrStale) {
			return apperr.Conflict("branch.changed", "The branch %s changed; reload and try again.", name)
		}
		return err
	}
	_, err = s.recordPush(ctx, pc, []RefUpdate{update})
	return err
}
