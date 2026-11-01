// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
)

const (
	tagSpaces = "Wiki spaces"
	tagPages  = "Wiki pages"
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

func principal(ctx context.Context) *identity.Principal { return identity.From(ctx) }

type spacePath struct {
	Key string `path:"key" doc:"Space key"`
}

type pagePath struct {
	ID int64 `path:"id" doc:"Page id"`
}

type idPath struct {
	ID int64 `path:"id"`
}

// Register adds the wiki operations.
func (h *Handler) Register(a huma.API) {
	s := h.S

	// Spaces
	huma.Register(a, api.Op("list-spaces", http.MethodGet, "/api/v1/wiki/spaces", "List a page of spaces", tagSpaces),
		func(ctx context.Context, in *struct {
			Query      string `query:"query" maxLength:"200" doc:"Part of the name or key"`
			Category   string `query:"category" maxLength:"100"`
			Favourite  bool   `query:"favourite" doc:"Only the caller's favourites"`
			Archived   bool   `query:"archived" doc:"Archived spaces instead of the others"`
			Type       string `query:"type" enum:"team,personal,"`
			Owner      string `query:"owner" maxLength:"64" doc:"User name of the owner (of personal spaces)"`
			Permission string `query:"permission" enum:"space.view,page.edit,page.delete-own,page.delete,page.archive,comment.add,comment.delete,attachment.add,attachment.delete,page.restrict,space.export,space.admin," doc:"Only spaces in which the caller holds this permission"`
			All        bool   `query:"all" doc:"Administrators: every space, also without access"`
			Sort       string `query:"sort" enum:"name,key,type,updated," doc:"Order by name (default), key, type or the latest change"`
			Order      string `query:"order" enum:"asc,desc," doc:"Default: ascending, the latest change descending"`
			api.Page
		}) (*body[*SpaceList], error) {
			desc := in.Order == "desc" || (in.Order == "" && in.Sort == "updated")
			return out(s.Spaces(ctx, principal(ctx), SpaceFilter{Query: in.Query, Category: in.Category, Favourite: in.Favourite,
				Archived: in.Archived, Type: in.Type, Owner: in.Owner, Permission: in.Permission, All: in.All, Sort: in.Sort, Desc: desc,
				Limit: in.Limit, Offset: in.Offset}))
		})
	huma.Register(a, api.Op("create-space", http.MethodPost, "/api/v1/wiki/spaces", "Create a team space", tagSpaces),
		func(ctx context.Context, in *body[NewSpace]) (*body[*Space], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateSpace(ctx, p, in.Body))
		})
	huma.Register(a, api.Op("create-personal-space", http.MethodPost, "/api/v1/wiki/spaces/personal", "Create my personal space", tagSpaces),
		func(ctx context.Context, _ *struct{}) (*body[*Space], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreatePersonalSpace(ctx, p))
		})
	huma.Register(a, api.Op("get-space", http.MethodGet, "/api/v1/wiki/spaces/{key}", "Get a space", tagSpaces),
		func(ctx context.Context, in *spacePath) (*body[*Space], error) {
			return out(s.GetSpace(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("update-space", http.MethodPatch, "/api/v1/wiki/spaces/{key}", "Change a space", tagSpaces),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body SpaceUpdate
		}) (*body[*Space], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateSpace(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("delete-space", http.MethodDelete, "/api/v1/wiki/spaces/{key}", "Delete a space with all content", tagSpaces),
		func(ctx context.Context, in *spacePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteSpace(ctx, p, in.Key)
		})
	huma.Register(a, api.Op("get-space-permissions", http.MethodGet, "/api/v1/wiki/spaces/{key}/permissions", "Permissions of a space", tagSpaces),
		func(ctx context.Context, in *spacePath) (*body[[]SpaceGrant], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SpacePermissionsOf(ctx, p, in.Key))
		})
	huma.Register(a, api.Op("set-space-permissions", http.MethodPut, "/api/v1/wiki/spaces/{key}/permissions", "Replace the permissions of a space", tagSpaces),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body []SpaceGrant
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetSpacePermissions(ctx, p, in.Key, in.Body)
		})
	spaceToggle := func(set func(ctx context.Context, p *identity.Principal, key string, on bool) error, on bool) func(ctx context.Context, in *spacePath) (*struct{}, error) {
		return func(ctx context.Context, in *spacePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, set(ctx, p, in.Key, on)
		}
	}
	huma.Register(a, api.Op("favourite-space", http.MethodPut, "/api/v1/wiki/spaces/{key}/favourite", "Mark a space as favourite", tagSpaces), spaceToggle(s.SetSpaceFavourite, true))
	huma.Register(a, api.Op("unfavourite-space", http.MethodDelete, "/api/v1/wiki/spaces/{key}/favourite", "Remove a favourite space", tagSpaces), spaceToggle(s.SetSpaceFavourite, false))
	huma.Register(a, api.Op("watch-space", http.MethodPut, "/api/v1/wiki/spaces/{key}/watch", "Watch a space", tagSpaces), spaceToggle(s.WatchSpace, true))
	huma.Register(a, api.Op("unwatch-space", http.MethodDelete, "/api/v1/wiki/spaces/{key}/watch", "Stop watching a space", tagSpaces), spaceToggle(s.WatchSpace, false))
	huma.Register(a, api.Op("get-page-tree", http.MethodGet, "/api/v1/wiki/spaces/{key}/tree", "A page of the child pages for the page tree", tagSpaces),
		func(ctx context.Context, in *struct {
			Key    string `path:"key"`
			Parent int64  `query:"parent" doc:"Parent page; 0 or absent for the top level"`
			// The tree opens a page with more children than other lists show at once, as its rows are short.
			Limit  int `query:"limit" minimum:"1" maximum:"500" default:"100" doc:"Maximum number of child pages"`
			Offset int `query:"offset" minimum:"0" default:"0" doc:"Number of child pages to skip"`
		}) (*body[*ChildPageList], error) {
			return out(s.Tree(ctx, principal(ctx), in.Key, in.Parent, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("list-blog-posts", http.MethodGet, "/api/v1/wiki/spaces/{key}/blog", "A page of the blog posts of a space", tagSpaces),
		func(ctx context.Context, in *struct {
			Key string `path:"key"`
			api.Page
		}) (*body[*SearchResult], error) {
			return out(s.Blog(ctx, principal(ctx), in.Key, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("list-space-trash", http.MethodGet, "/api/v1/wiki/spaces/{key}/trash", "A page of the trashed pages of a space", tagSpaces),
		func(ctx context.Context, in *struct {
			Key string `path:"key" doc:"Space key"`
			api.Page
		}) (*body[*TrashList], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Trash(ctx, p, in.Key, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("create-page", http.MethodPost, "/api/v1/wiki/spaces/{key}/pages", "Start a new page or blog post (as a draft)", tagSpaces),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body NewPage
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreatePage(ctx, p, in.Key, in.Body))
		})
	labels := api.Op("list-space-labels", http.MethodGet, "/api/v1/wiki/labels", "The 200 most used labels", tagSpaces)
	labels.Description = "The labels of the current pages and blog posts the caller may read, the most used first: at most " +
		"the 200 most used ones, without paging."
	huma.Register(a, labels,
		func(ctx context.Context, in *struct {
			Space string `query:"space" doc:"Only the labels of this space"`
		}) (*body[[]LabelCount], error) {
			return out(s.Labels(ctx, principal(ctx), in.Space))
		})

	// Templates
	huma.Register(a, api.Op("list-templates", http.MethodGet, "/api/v1/wiki/templates", "Page templates", tagSpaces),
		func(ctx context.Context, in *struct {
			Space string `query:"space" doc:"Include the templates of this space"`
		}) (*body[[]Template], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Templates(ctx, p, in.Space))
		})
	huma.Register(a, api.Op("create-template", http.MethodPost, "/api/v1/wiki/templates", "Create a template", tagSpaces),
		func(ctx context.Context, in *struct {
			Space string `query:"space" doc:"Space of the template; empty for a global template"`
			Body  TemplateInput
		}) (*body[*Template], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SaveTemplate(ctx, p, in.Space, 0, in.Body))
		})
	huma.Register(a, api.Op("update-template", http.MethodPut, "/api/v1/wiki/templates/{id}", "Change a template", tagSpaces),
		func(ctx context.Context, in *struct {
			ID    int64  `path:"id"`
			Space string `query:"space"`
			Body  TemplateInput
		}) (*body[*Template], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SaveTemplate(ctx, p, in.Space, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-template", http.MethodDelete, "/api/v1/wiki/templates/{id}", "Delete a template", tagSpaces),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteTemplate(ctx, p, in.ID)
		})

	// Pages
	huma.Register(a, api.Op("get-page", http.MethodGet, "/api/v1/wiki/pages/{id}", "Get a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Page], error) {
			return out(s.GetPage(ctx, principal(ctx), in.ID))
		})
	huma.Register(a, api.Op("get-draft", http.MethodGet, "/api/v1/wiki/pages/{id}/draft", "The shared draft of a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Draft], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.GetDraft(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("save-draft", http.MethodPut, "/api/v1/wiki/pages/{id}/draft",
		"Replace the content of the shared draft", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body DraftInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SaveDraft(ctx, p, in.ID, in.Body)
		})
	huma.Register(a, api.Op("discard-draft", http.MethodDelete, "/api/v1/wiki/pages/{id}/draft",
		"Discard the unpublished changes of the shared draft", tagPages),
		func(ctx context.Context, in *pagePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DiscardDraft(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("join-draft", http.MethodGet, "/api/v1/wiki/pages/{id}/edit", "Join the shared draft of a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*EditSession], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.JoinDraft(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("submit-draft-steps", http.MethodPost, "/api/v1/wiki/pages/{id}/edit/steps",
		"Change the shared draft (steps that build on its current version)", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body StepsInput
		}) (*body[*StepsResult], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SubmitSteps(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("list-draft-steps", http.MethodGet, "/api/v1/wiki/pages/{id}/edit/steps",
		"Changes of the shared draft after a version", tagPages),
		func(ctx context.Context, in *struct {
			ID    int64 `path:"id"`
			Since int   `query:"since" minimum:"0" doc:"The draft version the editor has"`
		}) (*body[*DraftSteps], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.DraftStepsSince(ctx, p, in.ID, in.Since))
		})
	huma.Register(a, api.Op("set-draft-title", http.MethodPut, "/api/v1/wiki/pages/{id}/edit/title",
		"Change the title of the shared draft", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body DraftTitleInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetDraftTitle(ctx, p, in.ID, in.Body)
		})
	huma.Register(a, api.Op("send-draft-caret", http.MethodPost, "/api/v1/wiki/pages/{id}/edit/caret",
		"Tell the other editors where my selection is", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body CaretInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DraftCaret(ctx, p, in.ID, in.Body)
		})
	huma.Register(a, api.Op("delete-earlier-draft", http.MethodDelete, "/api/v1/wiki/pages/{id}/edit/earlier-draft",
		"Delete my personal draft from before drafts were shared", tagPages),
		func(ctx context.Context, in *pagePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DropEarlierDraft(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("publish-page", http.MethodPost, "/api/v1/wiki/pages/{id}/publish", "Publish a new version", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body PublishInput
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Publish(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("link-page-issue", http.MethodPost, "/api/v1/wiki/pages/{id}/link-issue",
		"Replace selected text with the issue created from it", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body LinkIssueInput
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.LinkIssue(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("list-page-versions", http.MethodGet, "/api/v1/wiki/pages/{id}/versions", "A page of the versions of a page, newest first", tagPages),
		func(ctx context.Context, in *struct {
			ID int64 `path:"id" doc:"Page id"`
			api.Page
		}) (*body[*PageVersionList], error) {
			return out(s.Versions(ctx, principal(ctx), in.ID, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("get-page-version", http.MethodGet, "/api/v1/wiki/pages/{id}/versions/{version}", "A version of a page", tagPages),
		func(ctx context.Context, in *struct {
			ID      int64 `path:"id"`
			Version int   `path:"version"`
		}) (*body[*PageVersion], error) {
			return out(s.GetVersion(ctx, principal(ctx), in.ID, in.Version))
		})
	huma.Register(a, api.Op("restore-page-version", http.MethodPost, "/api/v1/wiki/pages/{id}/versions/{version}/restore", "Restore a version", tagPages),
		func(ctx context.Context, in *struct {
			ID      int64 `path:"id"`
			Version int   `path:"version"`
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.RestoreVersion(ctx, p, in.ID, in.Version))
		})
	huma.Register(a, api.Op("move-page", http.MethodPost, "/api/v1/wiki/pages/{id}/move", "Move a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body MoveInput
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.MovePage(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("copy-page", http.MethodPost, "/api/v1/wiki/pages/{id}/copy", "Copy a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body CopyInput
		}) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CopyPage(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("trash-page", http.MethodDelete, "/api/v1/wiki/pages/{id}", "Move a page to the trash", tagPages),
		func(ctx context.Context, in *pagePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.TrashPage(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("restore-page", http.MethodPost, "/api/v1/wiki/pages/{id}/restore", "Restore a page from the trash", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.RestorePage(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("purge-page", http.MethodDelete, "/api/v1/wiki/pages/{id}/purge", "Delete a trashed page for good", tagPages),
		func(ctx context.Context, in *pagePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.PurgePage(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("archive-page", http.MethodPut, "/api/v1/wiki/pages/{id}/archived", "Archive a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SetArchived(ctx, p, in.ID, true))
		})
	huma.Register(a, api.Op("unarchive-page", http.MethodDelete, "/api/v1/wiki/pages/{id}/archived", "Bring a page back from the archive", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Page], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SetArchived(ctx, p, in.ID, false))
		})
	huma.Register(a, api.Op("set-page-labels", http.MethodPut, "/api/v1/wiki/pages/{id}/labels", "Replace the labels of a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body []string
		}) (*body[[]string], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SetLabels(ctx, p, in.ID, in.Body))
		})
	pageToggle := func(set func(ctx context.Context, p *identity.Principal, id int64, on bool) error, on bool) func(ctx context.Context, in *pagePath) (*struct{}, error) {
		return func(ctx context.Context, in *pagePath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, set(ctx, p, in.ID, on)
		}
	}
	huma.Register(a, api.Op("like-page", http.MethodPut, "/api/v1/wiki/pages/{id}/like", "Like a page", tagPages), pageToggle(s.SetLike, true))
	huma.Register(a, api.Op("unlike-page", http.MethodDelete, "/api/v1/wiki/pages/{id}/like", "Remove the like", tagPages), pageToggle(s.SetLike, false))
	huma.Register(a, api.Op("watch-page", http.MethodPut, "/api/v1/wiki/pages/{id}/watch", "Watch a page", tagPages), pageToggle(s.WatchPage, true))
	huma.Register(a, api.Op("unwatch-page", http.MethodDelete, "/api/v1/wiki/pages/{id}/watch", "Stop watching a page", tagPages), pageToggle(s.WatchPage, false))
	huma.Register(a, api.Op("get-page-restrictions", http.MethodGet, "/api/v1/wiki/pages/{id}/restrictions", "Restrictions of a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[*Restrictions], error) {
			return out(s.GetRestrictions(ctx, principal(ctx), in.ID))
		})
	huma.Register(a, api.Op("set-page-restrictions", http.MethodPut, "/api/v1/wiki/pages/{id}/restrictions", "Replace the restrictions of a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body RestrictionsInput
		}) (*body[*Restrictions], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SetRestrictions(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("page-presence", http.MethodPost, "/api/v1/wiki/pages/{id}/presence", "Report presence and see who else has the page open", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body struct {
				Editing bool `json:"editing"`
			}
		}) (*body[[]PresenceEntry], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Presence(ctx, p, in.ID, in.Body.Editing))
		})

	// Comments
	huma.Register(a, api.Op("list-page-comments", http.MethodGet, "/api/v1/wiki/pages/{id}/comments", "Comments of a page", tagPages),
		func(ctx context.Context, in *pagePath) (*body[[]Comment], error) {
			return out(s.Comments(ctx, principal(ctx), in.ID))
		})
	huma.Register(a, api.Op("add-page-comment", http.MethodPost, "/api/v1/wiki/pages/{id}/comments", "Comment on a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body CommentInput
		}) (*body[*Comment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.AddComment(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("add-inline-comment", http.MethodPost, "/api/v1/wiki/pages/{id}/inline-comments", "Comment on text of a page", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body InlineCommentInput
		}) (*body[*Comment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.AddInlineComment(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("update-page-comment", http.MethodPut, "/api/v1/wiki/comments/{id}", "Change a comment", tagPages),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body CommentInput
		}) (*body[*Comment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateComment(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-page-comment", http.MethodDelete, "/api/v1/wiki/comments/{id}", "Delete a comment", tagPages),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteComment(ctx, p, in.ID)
		})
	resolve := func(on bool) func(ctx context.Context, in *idPath) (*body[*Comment], error) {
		return func(ctx context.Context, in *idPath) (*body[*Comment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.ResolveComment(ctx, p, in.ID, on))
		}
	}
	huma.Register(a, api.Op("resolve-comment", http.MethodPut, "/api/v1/wiki/comments/{id}/resolved", "Resolve an inline comment", tagPages), resolve(true))
	huma.Register(a, api.Op("reopen-comment", http.MethodDelete, "/api/v1/wiki/comments/{id}/resolved", "Reopen an inline comment", tagPages), resolve(false))
	likeComment := func(on bool) func(ctx context.Context, in *idPath) (*struct{}, error) {
		return func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.LikeComment(ctx, p, in.ID, on)
		}
	}
	huma.Register(a, api.Op("like-comment", http.MethodPut, "/api/v1/wiki/comments/{id}/like", "Like a comment", tagPages), likeComment(true))
	huma.Register(a, api.Op("unlike-comment", http.MethodDelete, "/api/v1/wiki/comments/{id}/like", "Remove the like of a comment", tagPages), likeComment(false))

	// Attachments
	huma.Register(a, api.Op("list-page-attachments", http.MethodGet, "/api/v1/wiki/pages/{id}/attachments", "Attachments of a page", tagPages),
		func(ctx context.Context, in *struct {
			ID  int64 `path:"id"`
			All bool  `query:"all" doc:"Include older versions"`
		}) (*body[[]Attachment], error) {
			return out(s.Attachments(ctx, principal(ctx), in.ID, in.All))
		})
	huma.Register(a, api.Op("delete-page-attachment", http.MethodDelete, "/api/v1/wiki/attachments/{id}", "Delete an attachment (all versions)", tagPages),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteAttachment(ctx, p, in.ID)
		})

	// Search and lists
	huma.Register(a, api.Op("search-pages", http.MethodGet, "/api/v1/wiki/search", "Search pages and blog posts", tagPages),
		func(ctx context.Context, in *struct {
			Query         string `query:"q"`
			Space         string `query:"space"`
			Label         string `query:"label"`
			Kind          string `query:"kind" enum:"page,blog,"`
			Sort          string `query:"sort" enum:"relevance,updated," doc:"relevance (with q; the default) or updated: the most recently updated first"`
			UpdatedWithin int    `query:"updatedWithin" minimum:"0" maximum:"36500" doc:"Only pages updated within as many days (0: any time)"`
			Facets        bool   `query:"facets" doc:"Also count the matches by space and by kind"`
			api.Page
		}) (*body[*SearchResult], error) {
			return out(s.Search(ctx, principal(ctx), SearchInput{Query: in.Query, Space: in.Space, Label: in.Label, Kind: in.Kind,
				Sort: in.Sort, UpdatedWithin: in.UpdatedWithin, Facets: in.Facets, Limit: in.Limit, Offset: in.Offset}))
		})
	huma.Register(a, api.Op("recent-pages", http.MethodGet, "/api/v1/wiki/recent", "Recently viewed or updated pages", tagPages),
		func(ctx context.Context, in *struct {
			Mode  string `query:"mode" enum:"viewed,updated" default:"updated"`
			Space string `query:"space"`
			Limit int    `query:"limit" minimum:"1" maximum:"100" default:"20"`
		}) (*body[[]PageSummary], error) {
			return out(s.Recent(ctx, principal(ctx), in.Mode, in.Space, in.Limit))
		})
	huma.Register(a, api.Op("issue-mentions", http.MethodGet, "/api/v1/wiki/mentions", "Pages that mention an issue", tagPages),
		func(ctx context.Context, in *struct {
			Issue string `query:"issue" required:"true" doc:"Issue key"`
		}) (*body[[]PageSummary], error) {
			return out(s.Mentions(ctx, principal(ctx), in.Issue))
		})
	h.registerExports(a)
}
