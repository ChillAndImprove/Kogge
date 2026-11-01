// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

const (
	tagProjects = "Code projects"
	tagRepos    = "Repositories"
	tagBrowse   = "Repository content"
)

type body[T any] struct {
	Body T
}

func out[T any](v T, err error) (*body[T], error) {
	if err != nil {
		return nil, err
	}
	return &body[T]{Body: v}, nil
}

func signedIn(ctx context.Context) (*identity.Principal, error) { return authz.RequireUser(ctx) }

// highlightSlots bounds the snippets that a node highlights at a time: highlighting costs processor time (chroma's
// lexers are regular expressions that backtrack, for up to 250 ms a match), and a page asks for all its code blocks
// at once. A request waits a few seconds for its turn; then it hears that the server is busy, and the code block
// stays plain.
var (
	highlightSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)/2))
	highlightWait  = 5 * time.Second
)

const highlightRetryAfter = "5"

// highlightTurn waits for a turn to highlight, at most highlightWait; release ends the turn.
func highlightTurn(ctx context.Context) (release func(), err error) {
	wait := time.NewTimer(highlightWait)
	defer wait.Stop()
	select {
	case highlightSlots <- struct{}{}:
		return func() { <-highlightSlots }, nil
	case <-wait.C:
		return nil, huma.ErrorWithHeaders(apperr.New(http.StatusServiceUnavailable, "code.highlight-busy",
			"Many code blocks are being highlighted at the moment; try again shortly."), http.Header{"Retry-After": {highlightRetryAfter}})
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func principal(ctx context.Context) *identity.Principal { return identity.From(ctx) }

type ProjectPath struct {
	Key string `path:"key" doc:"Code project key"`
}

type RepoPath struct {
	Key  string `path:"key" doc:"Code project key"`
	Slug string `path:"slug" doc:"Repository slug"`
}

type RefQuery struct {
	RepoPath
	Ref  string `query:"ref" doc:"Branch, tag or commit; default: the default branch"`
	Path string `query:"path" doc:"Path in the repository"`
}

type DiffQuery struct {
	IgnoreWhitespace bool   `query:"ignoreWhitespace"`
	Context          int    `query:"context" minimum:"0" maximum:"1000"`
	Path             string `query:"path" doc:"Only this file or directory"`
}

func (q DiffQuery) options() DiffOptions {
	return DiffOptions{IgnoreWhitespace: q.IgnoreWhitespace, Context: q.Context, Path: q.Path}
}

// Paged is a page of a list with the total count.
type Paged[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// Register adds the code operations.
// Highlighted is a snippet split into lines of tokens.
type Highlighted struct {
	Lines    [][]Token `json:"lines"`
	Language string    `json:"language,omitempty" doc:"The language recognised; empty when none was"`
}

func (h *Handler) Register(a huma.API) {
	s := h.S

	// Highlighting of snippets, the code blocks of documents as they are shown and edited: a function of its input,
	// for signed-in users (the web application shows documents to them only), a few at a time on each node.
	huma.Register(a, api.Op("highlight-snippet", http.MethodPost, "/api/v1/code/highlight", "Highlight a snippet of code",
		tagProjects), func(ctx context.Context, in *struct {
		Body struct {
			Language string `json:"language,omitempty" maxLength:"40" doc:"Name or alias of the language, e.g. go, ts, bash"`
			Text     string `json:"text" maxLength:"65536"`
		}
	}) (*struct{ Body Highlighted }, error) {
		if _, err := signedIn(ctx); err != nil {
			return nil, err
		}
		release, err := highlightTurn(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		lines, language := HighlightSnippet(in.Body.Language, in.Body.Text)
		if lines == nil {
			lines = [][]Token{}
		}
		for i := range lines {
			if lines[i] == nil {
				lines[i] = []Token{}
			}
		}
		return &struct{ Body Highlighted }{Highlighted{Lines: lines, Language: language}}, nil
	})

	// Code projects
	huma.Register(a, api.Op("list-code-projects", http.MethodGet, "/api/v1/code/projects", "List a page of code projects", tagProjects),
		func(ctx context.Context, in *struct {
			Query string `query:"query" maxLength:"100" doc:"Part of the key or name"`
			Admin bool   `query:"admin" doc:"Only code projects the caller administers, e.g. to create repositories in them"`
			api.Page
		}) (*body[Paged[Project]], error) {
			list, total, err := s.Projects(ctx, principal(ctx), ProjectFilter{Query: in.Query, Admin: in.Admin, Limit: in.Limit,
				Offset: in.Offset})
			return out(Paged[Project]{Items: nonNil(list), Total: total}, err)
		})
	huma.Register(a, api.Op("create-code-project", http.MethodPost, "/api/v1/code/projects", "Create a code project", tagProjects),
		func(ctx context.Context, in *body[NewProject]) (*body[*Project], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateProject(ctx, p, in.Body))
		})
	huma.Register(a, api.Op("get-code-project", http.MethodGet, "/api/v1/code/projects/{key}", "Get a code project", tagProjects),
		func(ctx context.Context, in *ProjectPath) (*body[*Project], error) {
			return out(s.GetProject(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("update-code-project", http.MethodPatch, "/api/v1/code/projects/{key}", "Change a code project", tagProjects),
		func(ctx context.Context, in *struct {
			ProjectPath
			Body ProjectUpdate
		}) (*body[*Project], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateProject(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("delete-code-project", http.MethodDelete, "/api/v1/code/projects/{key}", "Delete an empty code project", tagProjects),
		func(ctx context.Context, in *ProjectPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteProject(ctx, p, in.Key)
		})
	huma.Register(a, api.Op("get-code-project-permissions", http.MethodGet, "/api/v1/code/projects/{key}/permissions",
		"Permissions of a code project", tagProjects),
		func(ctx context.Context, in *ProjectPath) (*body[[]Grant], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			grants, err := s.ProjectPermissions(ctx, p, in.Key)
			return out(nonNil(grants), err)
		})
	huma.Register(a, api.Op("set-code-project-permissions", http.MethodPut, "/api/v1/code/projects/{key}/permissions",
		"Replace the permissions of a code project", tagProjects),
		func(ctx context.Context, in *struct {
			ProjectPath
			Body []Grant
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetProjectPermissions(ctx, p, in.Key, in.Body)
		})
	huma.Register(a, api.Op("get-code-project-branch-permissions", http.MethodGet, "/api/v1/code/projects/{key}/branch-permissions",
		"Branch permissions of a code project", tagProjects),
		func(ctx context.Context, in *ProjectPath) (*body[[]BranchPermission], error) {
			return out(s.ProjectBranchPermissions(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("set-code-project-branch-permissions", http.MethodPut, "/api/v1/code/projects/{key}/branch-permissions",
		"Replace the branch permissions of a code project", tagProjects),
		func(ctx context.Context, in *struct {
			ProjectPath
			Body []BranchPermission
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetProjectBranchPermissions(ctx, p, in.Key, in.Body)
		})

	// Repositories
	huma.Register(a, api.Op("list-repositories", http.MethodGet, "/api/v1/code/repos", "Find a page of repositories", tagRepos),
		func(ctx context.Context, in *struct {
			Project    string `query:"project" maxLength:"32" doc:"Only repositories of this code project"`
			Query      string `query:"query" maxLength:"100" doc:"Part of the name, the address name (slug) or the project key"`
			Permission string `query:"permission" enum:"read,write,admin," doc:"Only repositories the caller may access at least at this level, e.g. write to push to them"`
			Archived   string `query:"archived" enum:"true,false," doc:"true: only archived repositories; false: only those not archived; default: both"`
			Sort       string `query:"sort" enum:"name,pushed," doc:"By project key and name (default), or the latest push first (repositories never pushed last, newest first)"`
			api.Page
		}) (*body[Paged[Repository]], error) {
			list, total, err := s.Repositories(ctx, principal(ctx), RepositoryFilter{Project: in.Project, Query: in.Query,
				Level: levelNamed(in.Permission), Archived: in.Archived, Sort: in.Sort, Limit: in.Limit, Offset: in.Offset})
			return out(Paged[Repository]{Items: nonNil(list), Total: total}, err)
		})
	huma.Register(a, api.Op("create-repository", http.MethodPost, "/api/v1/code/projects/{key}/repos", "Create a repository", tagRepos),
		func(ctx context.Context, in *struct {
			ProjectPath
			Body NewRepository
		}) (*body[*Repository], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateRepository(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("get-repository", http.MethodGet, "/api/v1/code/repos/{key}/{slug}", "Get a repository", tagRepos),
		func(ctx context.Context, in *RepoPath) (*body[*Repository], error) {
			return out(s.GetRepository(ctx, principal(ctx), in.Key, in.Slug))
		})
	huma.Register(a, api.Op("update-repository", http.MethodPatch, "/api/v1/code/repos/{key}/{slug}", "Change a repository", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			Body RepositoryUpdate
		}) (*body[*Repository], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateRepository(ctx, p, in.Key, in.Slug, in.Body))
		})
	huma.Register(a, api.Op("delete-repository", http.MethodDelete, "/api/v1/code/repos/{key}/{slug}", "Delete a repository", tagRepos),
		func(ctx context.Context, in *RepoPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteRepository(ctx, p, in.Key, in.Slug)
		})
	huma.Register(a, api.Op("list-repository-sizes", http.MethodGet, "/api/v1/admin/repositories",
		"Repositories by the space they take, the largest first (administrators)", tagRepos),
		func(ctx context.Context, in *struct {
			Limit  int `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Offset int `query:"offset" minimum:"0" default:"0"`
		}) (*body[*Paged[RepoStorage]], error) {
			return out(s.RepositorySizes(ctx, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("set-repository-quota", http.MethodPut, "/api/v1/admin/repositories/{id}/quota",
		"Give a repository a quota of its own, or the quota of the settings again (administrators)", tagRepos),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body QuotaInput
		}) (*body[*RepoStorage], error) {
			return out(s.SetRepositoryQuota(ctx, in.ID, in.Body))
		})
	huma.Register(a, api.Op("retry-repository-import", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/import",
		"Start a failed import again", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			Body RetryImportInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.RetryImport(ctx, p, in.Key, in.Slug, in.Body)
		})
	huma.Register(a, api.Op("get-repository-permissions", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/permissions",
		"Permissions granted on a repository", tagRepos),
		func(ctx context.Context, in *RepoPath) (*body[[]Grant], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			grants, err := s.RepositoryPermissions(ctx, p, in.Key, in.Slug)
			return out(nonNil(grants), err)
		})
	huma.Register(a, api.Op("set-repository-permissions", http.MethodPut, "/api/v1/code/repos/{key}/{slug}/permissions",
		"Replace the permissions granted on a repository", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			Body []Grant
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetRepositoryPermissions(ctx, p, in.Key, in.Slug, in.Body)
		})
	huma.Register(a, api.Op("get-branch-permissions", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/branch-permissions",
		"Branch permissions of a repository (with those of its project)", tagRepos),
		func(ctx context.Context, in *RepoPath) (*body[[]BranchPermission], error) {
			return out(s.BranchPermissions(ctx, principal(ctx), in.Key, in.Slug))
		})
	huma.Register(a, api.Op("set-branch-permissions", http.MethodPut, "/api/v1/code/repos/{key}/{slug}/branch-permissions",
		"Replace the branch permissions of a repository", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			Body []BranchPermission
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetBranchPermissions(ctx, p, in.Key, in.Slug, in.Body)
		})
	huma.Register(a, api.Op("list-deploy-keys", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/deploy-keys", "Deploy keys", tagRepos),
		func(ctx context.Context, in *RepoPath) (*body[[]DeployKey], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.DeployKeys(ctx, p, in.Key, in.Slug))
		})
	huma.Register(a, api.Op("add-deploy-key", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/deploy-keys", "Add a deploy key", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			Body struct {
				Name  string `json:"name,omitempty" maxLength:"100"`
				Key   string `json:"key" minLength:"1" maxLength:"16000"`
				Write bool   `json:"write,omitempty"`
			}
		}) (*body[*DeployKey], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.AddDeployKey(ctx, p, in.Key, in.Slug, in.Body.Name, in.Body.Key, in.Body.Write))
		})
	huma.Register(a, api.Op("delete-deploy-key", http.MethodDelete, "/api/v1/code/repos/{key}/{slug}/deploy-keys/{id}",
		"Remove a deploy key", tagRepos),
		func(ctx context.Context, in *struct {
			RepoPath
			ID int64 `path:"id"`
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteDeployKey(ctx, p, in.Key, in.Slug, in.ID)
		})

	// Content
	huma.Register(a, api.Op("list-branches", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/branches", "Branches", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Filter string `query:"filter" maxLength:"200"`
			Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Offset int    `query:"offset" minimum:"0"`
		}) (*body[Paged[Branch]], error) {
			list, total, err := s.Branches(ctx, principal(ctx), in.Key, in.Slug, in.Filter, in.Limit, in.Offset)
			return out(Paged[Branch]{Items: nonNil(list), Total: total}, err)
		})
	huma.Register(a, api.Op("create-branch", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/branches", "Create a branch", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Body struct {
				Name string `json:"name" minLength:"1" maxLength:"250"`
				From string `json:"from,omitempty" doc:"Branch, tag or commit; default: the default branch"`
			}
		}) (*body[*Branch], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateBranch(ctx, p, in.Key, in.Slug, in.Body.Name, in.Body.From))
		})
	huma.Register(a, api.Op("delete-branch", http.MethodDelete, "/api/v1/code/repos/{key}/{slug}/branches", "Delete a branch", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Name string `query:"name" required:"true"`
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteBranch(ctx, p, in.Key, in.Slug, in.Name)
		})
	huma.Register(a, api.Op("list-tags", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/tags", "Tags", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Filter string `query:"filter" maxLength:"200"`
			Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Offset int    `query:"offset" minimum:"0"`
		}) (*body[Paged[Tag]], error) {
			list, total, err := s.Tags(ctx, principal(ctx), in.Key, in.Slug, in.Filter, in.Limit, in.Offset)
			return out(Paged[Tag]{Items: nonNil(list), Total: total}, err)
		})
	huma.Register(a, api.Op("get-tree", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/tree", "A directory", tagBrowse),
		func(ctx context.Context, in *RefQuery) (*body[*Tree], error) {
			return out(s.Tree(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Path))
		})
	huma.Register(a, api.Op("get-file", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/file", "A file for display", tagBrowse),
		func(ctx context.Context, in *RefQuery) (*body[*File], error) {
			return out(s.File(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Path))
		})
	huma.Register(a, api.Op("commit-file", http.MethodPut, "/api/v1/code/repos/{key}/{slug}/files", "Create, change or delete a file with a commit",
		tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Body FileChange
		}) (*body[*CommitInfo], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CommitFile(ctx, p, in.Key, in.Slug, in.Body))
		})
	huma.Register(a, api.Op("get-blame", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/blame", "Who changed the lines of a file", tagBrowse),
		func(ctx context.Context, in *RefQuery) (*body[*Blame], error) {
			return out(s.Blame(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Path))
		})
	huma.Register(a, api.Op("get-code-owners", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/code-owners",
		"The CODEOWNERS file of a branch, tag or commit as Kogge reads it; with a path, its owners", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Ref  string `query:"ref" maxLength:"255" doc:"Branch, tag or commit; default: the default branch"`
			Path string `query:"path" maxLength:"4096" doc:"A file whose owners to return"`
		}) (*body[*CodeOwnersFile], error) {
			return out(s.CodeOwners(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Path))
		})
	huma.Register(a, api.Op("search-code", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/search",
		"Search the files of a repository at a branch, tag or commit", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			Query         string `query:"q" maxLength:"200" doc:"Text to find, or a Perl-compatible regular expression with regex"`
			Ref           string `query:"ref" maxLength:"255" doc:"Branch, tag or commit; default: the default branch"`
			Regex         bool   `query:"regex"`
			CaseSensitive bool   `query:"case"`
			Path          string `query:"path" maxLength:"4096" doc:"Only files below this path"`
		}) (*body[*SearchResult], error) {
			return out(s.SearchCode(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Query, in.Regex, in.CaseSensitive, in.Path))
		})
	huma.Register(a, api.Op("list-commits", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/commits", "Commits", tagBrowse),
		func(ctx context.Context, in *struct {
			RefQuery
			Exclude string `query:"exclude" doc:"Leave out commits of this ref (e.g. the default branch)"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"30"`
			Offset  int    `query:"offset" minimum:"0"`
		}) (*body[*CommitList], error) {
			return out(s.Commits(ctx, principal(ctx), in.Key, in.Slug, in.Ref, in.Path, in.Exclude, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("get-commit", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/commits/{sha}", "A commit with its changes", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			SHA string `path:"sha"`
			DiffQuery
		}) (*body[*CommitDetail], error) {
			return out(s.Commit(ctx, principal(ctx), in.Key, in.Slug, in.SHA, in.options()))
		})
	huma.Register(a, api.Op("compare-refs", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/compare", "Compare two refs", tagBrowse),
		func(ctx context.Context, in *struct {
			RepoPath
			From string `query:"from" required:"true"`
			To   string `query:"to" required:"true"`
			DiffQuery
		}) (*body[*Comparison], error) {
			return out(s.Compare(ctx, principal(ctx), in.Key, in.Slug, in.From, in.To, in.options()))
		})

	h.registerPullRequests(a)
}
