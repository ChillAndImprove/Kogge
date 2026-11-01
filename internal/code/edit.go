// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// Editing in the browser: a change of one file becomes a commit on a branch, with the same branch permissions as
// a push.

// maxEditSize limits files edited in the browser.
const maxEditSize = 2 << 20

// FileChange creates, changes or deletes a file with a commit.
type FileChange struct {
	Branch    string `json:"branch" minLength:"1" maxLength:"250" doc:"Branch the change is made on"`
	Path      string `json:"path" minLength:"1" maxLength:"4096"`
	Content   string `json:"content,omitempty" maxLength:"2097152" doc:"New content (UTF-8 text)"`
	Delete    bool   `json:"delete,omitempty" doc:"Delete the file instead"`
	Message   string `json:"message,omitempty" maxLength:"10000" doc:"Commit message; default: Update <path>"`
	Base      string `json:"base,omitempty" maxLength:"64" doc:"Commit the change was made on; if the branch moved since, the change is refused"`
	NewBranch string `json:"newBranch,omitempty" maxLength:"250" doc:"Commit to this new branch, started from branch"`
}

// cleanRepoPath validates a path inside the repository.
func cleanRepoPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	clean := path.Clean(p)
	if p == "" || clean != p || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", apperr.Invalidf("file.path", "%q is not a valid path.", p)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.ContainsAny(part, "\x00\n\r") {
			return "", apperr.Invalidf("file.path", "%q is not a valid path.", p)
		}
	}
	return clean, nil
}

// CommitFile commits a change of one file (write access; branch permissions apply).
func (s *Service) CommitFile(ctx context.Context, p *identity.Principal, key, slug string, in FileChange) (*CommitInfo, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelWrite)
	if err != nil {
		return nil, err
	}
	if r.Archived {
		return nil, apperr.Conflict("repository.archived", "The repository is archived.")
	}
	file, err := cleanRepoPath(in.Path)
	if err != nil {
		return nil, err
	}
	if len(in.Content) > maxEditSize || !utf8.ValidString(in.Content) {
		return nil, apperr.Invalid("file.content", "Files edited in the browser are UTF-8 text of at most 2 MB.")
	}
	// The limits of pushes apply to the commit, which adds the file to the repository.
	if !in.Delete {
		st, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		size := int64(len(in.Content))
		if err := storage.Check(st, s.repoPath(r.ID), size); err != nil {
			return nil, err
		}
		if limit := settings.MiB(st.MaxGitFileMB); limit > 0 && size > limit {
			return nil, apperr.New(http.StatusRequestEntityTooLarge, "file.too-large",
				"The file has %s; files larger than %s must be stored with Git LFS.", storage.Format(size), storage.Format(limit))
		}
		if quota, used := quotaOf(st, r), r.Size+r.LFSSize; quota > 0 && used+size > quota {
			return nil, quotaExceeded(used, quota)
		}
	}
	repo := s.open(r.ID)
	head, err := repo.ResolveBranch(ctx, in.Branch)
	switch {
	case errors.Is(err, git.ErrNotFound):
		// The first commit of an empty repository goes to its default branch.
		if empty, _ := repo.Empty(ctx); !empty || in.Branch != r.DefaultBranch || in.NewBranch != "" {
			return nil, apperr.NotFound("branch.not-found", "The branch %s does not exist.", in.Branch)
		}
		head = ""
	case err != nil:
		return nil, err
	}
	if in.Base != "" && in.Base != head {
		return nil, apperr.Conflict("file.stale", "The branch %s changed since you opened the file; reload it.", in.Branch)
	}
	target := in.Branch
	if in.NewBranch != "" {
		target = strings.TrimSpace(in.NewBranch)
		if !s.git.ValidBranchName(ctx, target) {
			return nil, apperr.Invalidf("branch.name", "%q is not a valid branch name.", target)
		}
		if _, err := repo.ResolveBranch(ctx, target); err == nil {
			return nil, apperr.Conflict("branch.exists", "The branch %s already exists.", target)
		}
	}

	baseTree := ""
	var parents []string
	if head != "" {
		parents = []string{head}
		if baseTree, err = repo.TreeOf(ctx, head); err != nil {
			return nil, err
		}
	}
	mode := "100644"
	if head != "" {
		if info, err := repo.Stat(ctx, head, file); err == nil {
			if info.Mode == "100755" {
				mode = info.Mode
			}
			if info.Mode == "120000" || info.Mode == "160000" {
				return nil, apperr.Invalid("file.kind", "Links and submodules cannot be edited in the browser.")
			}
		} else if in.Delete {
			return nil, apperr.NotFound("file.not-found", "The file %s does not exist.", file)
		}
	}
	blob := ""
	if !in.Delete {
		if blob, err = repo.HashObject(ctx, []byte(in.Content)); err != nil {
			return nil, err
		}
	}
	tree, err := repo.TreeWith(ctx, baseTree, file, blob, mode)
	if err != nil {
		return nil, err
	}
	if tree == baseTree {
		return nil, apperr.Invalid("file.unchanged", "The file did not change.")
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		verb := "Update"
		if in.Delete {
			verb = "Delete"
		}
		message = verb + " " + file
	}
	sig := git.Signature{Name: p.User.DisplayName, Email: p.User.Email, When: now()}
	if sig.Email == "" {
		sig.Email = p.User.Username + "@users.noreply"
	}
	sha, err := repo.CommitTree(ctx, tree, parents, message+"\n", sig, sig)
	if err != nil {
		return nil, err
	}
	old := head
	if in.NewBranch != "" || head == "" {
		old = strings.Repeat("0", len(sha))
	}
	update := RefUpdate{Ref: "refs/heads/" + target, Old: old, New: sha, FastForward: head != ""}
	pc := &pushContext{repoID: r.ID, principal: p, protocol: "web"}
	problems, err := s.checkPush(ctx, pc, []RefUpdate{update})
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, apperr.Forbidden("file.denied", "%s", strings.Join(problems, " "))
	}
	expected := head
	if in.NewBranch != "" {
		expected = ""
	}
	release, err := s.writing(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := repo.UpdateRef(ctx, update.Ref, sha, expected, "kogge: edited by "+p.User.Username); err != nil {
		if errors.Is(err, git.ErrStale) {
			return nil, apperr.Conflict("file.stale", "The branch %s changed in the meantime; reload the file.", target)
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
	return &infos[0], nil
}
