// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
)

// Pipeline files include files: of their own repository at the same commit, and of other
// repositories of the same code project at a ref. What an included file says becomes part of the run and is seen
// by everybody who may read the run's repository — so a run never reads a repository that the person it is for may
// not read. That person is who caused it: who pushed, opened the pull request or pushed its source, started it by
// hand, through the API or a trigger (the trigger's creator), or runs it again. A run without a person (a schedule,
// a pipeline that follows one of another repository) reads with the permissions of whoever pushed its commit to its
// branch: they put the include there and could have copied the file themselves. Pushes of deploy keys have no
// person; their runs cannot include files of other repositories. The code project is the boundary in any case,
// as for pipelines that follow others: no file of another code project is read.

// includeReader reads the files that a pipeline file includes, for a run (or a check of the file).
type includeReader struct {
	s      *Service
	repo   *Repo  // the pipeline's repository
	commit string // the commit of the run
	branch string // the run's branch, for finding who pushed its commit
	person int64  // whose permissions apply (0: who pushed the commit)
	// principal is the person, once known (a check of a file: who checks it).
	principal *identity.Principal
	looked    bool
	// pins are the commits at which a run read files of other repositories (repository and ref → commit): a run
	// again reads the same.
	pins map[string]string
	// nobody tells why files of other repositories cannot be read when no person's permissions apply.
	nobody string
}

// Why files of other repositories are not read without a person.
const (
	nobodyForRun = "files of other repositories are read with the permissions of the person a run is for — who started it, or " +
		"pushed its commit — and this run has none who is active (a deploy key pushed the commit, or the person's account is not active)"
	nobodySignedIn = "files of other repositories are read with the permissions of whoever looks at the file: sign in to read them"
)

func pinKey(repository, ref string) string { return repository + "\x00" + ref }

// pins returns the commits at which the files of other repositories were read.
func pinsOf(includes []spec.Included) map[string]string {
	pins := map[string]string{}
	for _, inc := range includes {
		if inc.Repository != "" && inc.Commit != "" {
			pins[pinKey(inc.Repository, inc.Ref)] = inc.Commit
		}
	}
	return pins
}

// who returns the person whose permissions apply, or nil.
func (r *includeReader) who(ctx context.Context) (*identity.Principal, error) {
	if r.looked {
		return r.principal, nil
	}
	r.looked = true
	id := r.person
	if id == 0 && r.branch != "" {
		var err error
		if id, err = r.s.repos.Pusher(ctx, r.repo.ID, "refs/heads/"+r.branch, r.commit); err != nil {
			return nil, err
		}
	}
	if id == 0 {
		return nil, nil
	}
	p, err := r.s.identity.PrincipalOf(ctx, id, identity.MethodInternal)
	var gone *apperr.Error
	if errors.As(err, &gone) {
		return nil, nil // deleted or no longer active: nobody's permissions apply
	}
	if err != nil {
		return nil, err
	}
	r.principal = p
	return p, nil
}

// Read implements spec.Reader.
func (r *includeReader) Read(ctx context.Context, repository, ref, path string) ([]byte, string, error) {
	if repository == "" {
		data, ok, err := r.s.repos.ReadFile(ctx, r.repo.ID, r.commit, path)
		if err != nil {
			return nil, "", &spec.IncludeError{Message: "the file cannot be read: " + err.Error()}
		}
		if !ok {
			return nil, "", &spec.IncludeError{Message: "the file does not exist at the commit of the run"}
		}
		return data, r.commit, nil
	}
	p, err := r.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if p == nil {
		if r.nobody == "" {
			return nil, "", &spec.IncludeError{Message: nobodyForRun}
		}
		return nil, "", &spec.IncludeError{Message: r.nobody}
	}
	hidden := &spec.IncludeError{Message: fmt.Sprintf("the repository %s does not exist, or %s may not read it", repository, p.User.Username)}
	key, slug, _ := strings.Cut(repository, "/")
	other, err := r.s.repos.RepoByPath(ctx, key, slug)
	if apperr.IsNotFound(err) {
		return nil, "", hidden
	}
	if err != nil {
		return nil, "", err
	}
	level, err := r.s.repos.Level(ctx, p, other.ID)
	if err != nil {
		return nil, "", err
	}
	if level < LevelRead {
		return nil, "", hidden
	}
	if other.ProjectID != r.repo.ProjectID {
		return nil, "", &spec.IncludeError{Message: fmt.Sprintf("only files of repositories of the code project %s can be included",
			r.repo.ProjectKey)}
	}
	commit, pinned := r.pins[pinKey(repository, ref)]
	if !pinned {
		name := ref
		if name == "" {
			name = "refs/heads/" + other.DefaultBranch
		}
		if commit, err = r.s.repos.Resolve(ctx, other.ID, name); err != nil {
			return nil, "", &spec.IncludeError{Message: fmt.Sprintf("%s has no branch, tag or commit %s", repository, displayRef(ref, other))}
		}
	}
	data, ok, err := r.s.repos.ReadFile(ctx, other.ID, commit, path)
	if err != nil {
		return nil, "", &spec.IncludeError{Message: "the file cannot be read: " + err.Error()}
	}
	if !ok {
		return nil, "", &spec.IncludeError{Message: fmt.Sprintf("the file does not exist at %s", displayRef(ref, other))}
	}
	return data, commit, nil
}

func displayRef(ref string, repo *Repo) string {
	if ref == "" {
		return repo.DefaultBranch
	}
	return ref
}

// readerFor reads included files for a person who looks at a pipeline file (its check, its run forms): with that
// person's permissions.
func (s *Service) readerFor(p *identity.Principal, repo *Repo, commit string) *includeReader {
	r := &includeReader{s: s, repo: repo, commit: commit, looked: true, nobody: nobodySignedIn}
	if p != nil && p.Authenticated() {
		r.principal = p
	}
	return r
}
