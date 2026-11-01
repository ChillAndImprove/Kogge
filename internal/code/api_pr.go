// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/code/gitdiff"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/api"
)

// RenderedMarkdown is Markdown as comments and descriptions store it: what a preview shows.
type RenderedMarkdown struct {
	Doc *docmodel.Doc `json:"doc"`
}

// MarkdownSource is a document written as Markdown.
type MarkdownSource struct {
	Markdown string `json:"markdown"`
}

const tagPRs = "Pull requests"

type PRPath struct {
	RepoPath
	Number int `path:"number" minimum:"1"`
}

type CommentPath struct {
	ID int64 `path:"id"`
}

func (h *Handler) registerPullRequests(a huma.API) {
	s := h.S

	huma.Register(a, api.Op("list-pull-requests", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/pull-requests", "Pull requests", tagPRs),
		func(ctx context.Context, in *struct {
			RepoPath
			State    string `query:"state" enum:"open,merged,declined,all," default:"open"`
			Author   int64  `query:"author" doc:"User id"`
			Reviewer int64  `query:"reviewer" doc:"User id"`
			Query    string `query:"query" maxLength:"200"`
			Limit    int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Offset   int    `query:"offset" minimum:"0"`
		}) (*body[Paged[PullRequest]], error) {
			list, total, err := s.PullRequests(ctx, principal(ctx), in.Key, in.Slug, PullRequestFilter{State: in.State, Author: in.Author,
				Reviewer: in.Reviewer, Query: in.Query, Limit: in.Limit, Offset: in.Offset})
			return out(Paged[PullRequest]{Items: nonNil(list), Total: total}, err)
		})
	huma.Register(a, api.Op("my-pull-requests", http.MethodGet, "/api/v1/code/pull-requests", "Pull requests of the caller", tagPRs),
		func(ctx context.Context, in *struct {
			Role   string `query:"role" enum:"author,reviewer" default:"author"`
			State  string `query:"state" enum:"open,merged,declined,all," default:"open"`
			Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum number of pull requests; ask for one more than you show to learn whether more follow"`
			Offset int    `query:"offset" minimum:"0" doc:"Number of pull requests to skip, most recently updated first"`
		}) (*body[[]PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			list, err := s.MyPullRequests(ctx, p, in.Role, in.State, in.Limit, in.Offset)
			return out(nonNil(list), err)
		})
	huma.Register(a, api.Op("create-pull-request", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/pull-requests", "Open a pull request", tagPRs),
		func(ctx context.Context, in *struct {
			RepoPath
			Body NewPullRequest
		}) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreatePullRequest(ctx, p, in.Key, in.Slug, in.Body))
		})
	huma.Register(a, api.Op("get-pull-request", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}", "A pull request", tagPRs),
		func(ctx context.Context, in *PRPath) (*body[*PullRequest], error) {
			return out(s.GetPullRequest(ctx, principal(ctx), in.Key, in.Slug, in.Number))
		})
	huma.Register(a, api.Op("update-pull-request", http.MethodPatch, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}",
		"Change a pull request", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			Body PullRequestUpdate
		}) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdatePullRequest(ctx, p, in.Key, in.Slug, in.Number, in.Body))
		})
	huma.Register(a, api.Op("get-pull-request-diff", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/diff",
		"The changes of a pull request", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			DiffQuery
		}) (*body[*gitdiff.Diff], error) {
			return out(s.PullRequestDiff(ctx, principal(ctx), in.Key, in.Slug, in.Number, in.options()))
		})
	huma.Register(a, api.Op("list-pull-request-commits", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/commits",
		"A page of the commits of a pull request, newest first", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			api.Page
		}) (*body[*CommitList], error) {
			return out(s.PullRequestCommits(ctx, principal(ctx), in.Key, in.Slug, in.Number, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("get-pull-request-timeline", http.MethodGet, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/timeline",
		"Events and comments of a pull request", tagPRs),
		func(ctx context.Context, in *PRPath) (*body[[]Activity], error) {
			return out(s.Timeline(ctx, principal(ctx), in.Key, in.Slug, in.Number))
		})
	huma.Register(a, api.Op("render-markdown", http.MethodPost, "/api/v1/code/markdown", "Render Markdown", tagPRs),
		func(ctx context.Context, in *struct {
			Body struct {
				Markdown string `json:"markdown" maxLength:"1000000"`
			}
		}) (*body[RenderedMarkdown], error) {
			if _, err := signedIn(ctx); err != nil {
				return nil, err
			}
			return out(RenderedMarkdown{Doc: docmodel.FromMarkdown(in.Body.Markdown)}, nil)
		})
	huma.Register(a, api.Op("markdown-source", http.MethodPost, "/api/v1/code/markdown/source",
		"A document as Markdown, written as exports write it (for editing a page as Markdown)", tagPRs),
		func(ctx context.Context, in *struct {
			Body struct {
				Doc *docmodel.Doc `json:"doc"`
			}
		}) (*body[MarkdownSource], error) {
			if _, err := signedIn(ctx); err != nil {
				return nil, err
			}
			return out(MarkdownSource{Markdown: docmodel.Markdown(in.Body.Doc)}, nil)
		})
	huma.Register(a, api.Op("add-pull-request-comment", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/comments",
		"Comment on a pull request", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			Body CommentInput
		}) (*body[*PRComment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.AddComment(ctx, p, in.Key, in.Slug, in.Number, in.Body))
		})
	huma.Register(a, api.Op("review-pull-request", http.MethodPut, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/review",
		"Approve, ask for work or withdraw a review", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			Body struct {
				State string `json:"state" enum:"approved,needs-work,unapproved"`
			}
		}) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Review(ctx, p, in.Key, in.Slug, in.Number, in.Body.State))
		})
	huma.Register(a, api.Op("merge-pull-request", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/merge",
		"Merge a pull request", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			Body MergeInput
		}) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Merge(ctx, p, in.Key, in.Slug, in.Number, in.Body))
		})
	huma.Register(a, api.Op("enable-auto-merge", http.MethodPut, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/auto-merge",
		"Merge the pull request when every merge check passes", tagPRs),
		func(ctx context.Context, in *struct {
			PRPath
			Body AutoMergeInput
		}) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.EnableAutoMerge(ctx, p, in.Key, in.Slug, in.Number, in.Body))
		})
	huma.Register(a, api.Op("disable-auto-merge", http.MethodDelete, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/auto-merge",
		"Turn auto-merge off", tagPRs),
		func(ctx context.Context, in *PRPath) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.DisableAutoMerge(ctx, p, in.Key, in.Slug, in.Number))
		})
	huma.Register(a, api.Op("decline-pull-request", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/decline",
		"Decline a pull request", tagPRs),
		func(ctx context.Context, in *PRPath) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Decline(ctx, p, in.Key, in.Slug, in.Number, false))
		})
	huma.Register(a, api.Op("reopen-pull-request", http.MethodPost, "/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/reopen",
		"Reopen a declined pull request", tagPRs),
		func(ctx context.Context, in *PRPath) (*body[*PullRequest], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Decline(ctx, p, in.Key, in.Slug, in.Number, true))
		})
	huma.Register(a, api.Op("update-pull-request-comment", http.MethodPut, "/api/v1/code/pull-request-comments/{id}", "Change a comment", tagPRs),
		func(ctx context.Context, in *struct {
			CommentPath
			Body CommentInput
		}) (*body[*PRComment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateComment(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-pull-request-comment", http.MethodDelete, "/api/v1/code/pull-request-comments/{id}", "Delete a comment", tagPRs),
		func(ctx context.Context, in *CommentPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteComment(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("resolve-task", http.MethodPut, "/api/v1/code/pull-request-comments/{id}/resolved", "Resolve a task", tagPRs),
		func(ctx context.Context, in *CommentPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.ResolveTask(ctx, p, in.ID, true)
		})
	huma.Register(a, api.Op("reopen-task", http.MethodDelete, "/api/v1/code/pull-request-comments/{id}/resolved", "Reopen a task", tagPRs),
		func(ctx context.Context, in *CommentPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.ResolveTask(ctx, p, in.ID, false)
		})
	huma.Register(a, api.Op("apply-suggestion", http.MethodPost, "/api/v1/code/pull-request-comments/{id}/apply",
		"Commit the change a line comment suggests to the source branch", tagPRs),
		func(ctx context.Context, in *CommentPath) (*body[*CommitInfo], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.ApplySuggestion(ctx, p, in.ID))
		})
}
