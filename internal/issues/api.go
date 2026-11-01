// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

// Handler serves the issues API.
type Handler struct {
	S *Service
}

func principal(ctx context.Context) *identity.Principal { return identity.From(ctx) }

func signedIn(ctx context.Context) (*identity.Principal, error) { return authz.RequireUser(ctx) }

type keyPath struct {
	Key string `path:"key" doc:"Project or issue key"`
}

type idPath struct {
	ID int64 `path:"id"`
}

type body[T any] struct {
	Body T
}

// sharedListInput selects a page of saved filters or dashboards.
type sharedListInput struct {
	Query     string `query:"query" maxLength:"200" doc:"Part of the name or the description"`
	Owner     string `query:"owner" enum:"me,others," doc:"Only the caller's own (me) or only those others share with the caller (others)"`
	Favourite bool   `query:"favourite" doc:"Only the caller's favourites"`
	api.Page
}

func (in *sharedListInput) selection() SharedSelection {
	return SharedSelection{Query: in.Query, Owner: in.Owner, Favourite: in.Favourite, Limit: in.Limit, Offset: in.Offset}
}

func out[T any](v T, err error) (*body[T], error) {
	if err != nil {
		return nil, err
	}
	return &body[T]{Body: v}, nil
}

type issueOutput struct {
	ETag string `header:"ETag"`
	Body *Issue
}

func issueOut(i *Issue, err error) (*issueOutput, error) {
	if err != nil {
		return nil, err
	}
	return &issueOutput{ETag: api.ETag(i.Version), Body: i}, nil
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	const tp, ti, tc, ts = "Projects", "Issues", "Issue configuration", "Search"
	s := h.S

	h.registerAgile(a)
	h.registerDashboards(a)
	h.registerSubscriptions(a)
	h.registerBulk(a)

	// Projects
	huma.Register(a, api.Op("list-projects", http.MethodGet, "/api/v1/projects", "List projects", tp),
		func(ctx context.Context, in *struct {
			Query    string `query:"query" maxLength:"100"`
			Archived bool   `query:"archived"`
		}) (*body[[]*Project], error) {
			list, err := s.ListProjects(ctx, principal(ctx), in.Query, in.Archived)
			if list == nil {
				list = []*Project{}
			}
			return out(list, err)
		})
	huma.Register(a, api.Op("create-project", http.MethodPost, "/api/v1/projects", "Create a project", tp),
		func(ctx context.Context, in *body[NewProject]) (*body[*Project], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateProject(ctx, p, in.Body))
		})
	huma.Register(a, api.Op("get-project", http.MethodGet, "/api/v1/projects/{key}", "Get a project", tp),
		func(ctx context.Context, in *keyPath) (*body[*ProjectView], error) {
			return out(s.GetProject(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("update-project", http.MethodPatch, "/api/v1/projects/{key}", "Change a project", tp),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body ProjectUpdate
		}) (*body[*Project], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateProject(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("delete-project", http.MethodDelete, "/api/v1/projects/{key}", "Delete a project with all issues", tp),
		func(ctx context.Context, in *keyPath) (*struct{}, error) { return nil, s.DeleteProject(ctx, in.Key) })
	huma.Register(a, api.Op("list-project-roles", http.MethodGet, "/api/v1/projects/{key}/roles", "Project role members", tp),
		func(ctx context.Context, in *keyPath) (*body[[]RoleMembers], error) {
			return out(s.ProjectRoles(ctx, principal(ctx), in.Key))
		})
	type roleMemberInput struct {
		Key    string `path:"key"`
		RoleID int64  `path:"roleId"`
		Body   RoleMember
	}
	huma.Register(a, api.Op("add-project-role-member", http.MethodPost, "/api/v1/projects/{key}/roles/{roleId}/members",
		"Add a user or group to a project role", tp), func(ctx context.Context, in *roleMemberInput) (*struct{}, error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return nil, s.ChangeRoleMember(ctx, p, in.Key, in.RoleID, in.Body, true)
	})
	huma.Register(a, api.Op("remove-project-role-member", http.MethodDelete, "/api/v1/projects/{key}/roles/{roleId}/members",
		"Remove a user or group from a project role", tp), func(ctx context.Context, in *struct {
		Key    string `path:"key"`
		RoleID int64  `path:"roleId"`
		Type   string `query:"type" enum:"user,group" required:"true"`
		ID     int64  `query:"id" required:"true"`
	}) (*struct{}, error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return nil, s.ChangeRoleMember(ctx, p, in.Key, in.RoleID, RoleMember{Type: in.Type, ID: in.ID}, false)
	})
	huma.Register(a, api.Op("get-project-issue-types", http.MethodGet, "/api/v1/projects/{key}/issue-types",
		"Issue types and workflows of a project", tp), func(ctx context.Context, in *keyPath) (*body[[]projectType], error) {
		return out(s.ProjectIssueTypes(ctx, principal(ctx), in.Key))
	})
	huma.Register(a, api.Op("get-project-statuses", http.MethodGet, "/api/v1/projects/{key}/statuses",
		"Statuses of the workflows of a project", tp), func(ctx context.Context, in *keyPath) (*body[[]Status], error) {
		return out(s.ProjectStatuses(ctx, principal(ctx), in.Key))
	})
	huma.Register(a, api.Op("set-project-issue-types", http.MethodPut, "/api/v1/projects/{key}/issue-types",
		"Set the issue types and workflows of a project", tp), func(ctx context.Context, in *struct {
		Key  string `path:"key"`
		Body []projectType
	}) (*struct{}, error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return nil, s.SetProjectIssueTypes(ctx, p, in.Key, in.Body)
	})
	huma.Register(a, api.Op("get-field-layout", http.MethodGet, "/api/v1/projects/{key}/field-layouts/{typeId}",
		"Field layout of a project and issue type (0 = default)", tp), func(ctx context.Context, in *struct {
		Key    string `path:"key"`
		TypeID int64  `path:"typeId"`
	}) (*body[[]LayoutItem], error) {
		pv, err := s.GetProject(ctx, principal(ctx), in.Key)
		if err != nil {
			return nil, err
		}
		return out(s.FieldLayout(ctx, pv.ID, in.TypeID))
	})
	huma.Register(a, api.Op("set-field-layout", http.MethodPut, "/api/v1/projects/{key}/field-layouts/{typeId}",
		"Set the field layout of a project and issue type", tp), func(ctx context.Context, in *struct {
		Key    string `path:"key"`
		TypeID int64  `path:"typeId"`
		Body   []LayoutItem
	}) (*struct{}, error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return nil, s.SetFieldLayout(ctx, p, in.Key, in.TypeID, in.Body)
	})
	huma.Register(a, api.Op("list-security-levels", http.MethodGet, "/api/v1/projects/{key}/security-levels",
		"Security levels the caller may give issues of a project", tp), func(ctx context.Context, in *struct {
		Key string `path:"key"`
	}) (*body[*SecurityLevelChoices], error) {
		return out(s.SecurityLevels(ctx, principal(ctx), in.Key))
	})
	huma.Register(a, api.Op("list-assignable-users", http.MethodGet, "/api/v1/projects/{key}/assignable",
		"Users who can be assigned in a project", tp), func(ctx context.Context, in *struct {
		Key   string `path:"key"`
		Query string `query:"query" maxLength:"100"`
	}) (*body[[]identity.UserRef], error) {
		return out(s.AssignableUsers(ctx, principal(ctx), in.Key, in.Query, 20))
	})
	type componentInput struct {
		Key  string `path:"key"`
		ID   int64  `path:"id"`
		Body ComponentInput
	}
	huma.Register(a, api.Op("create-component", http.MethodPost, "/api/v1/projects/{key}/components", "Create a component", tp),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body ComponentInput
		}) (*body[*Component], error) {
			return out(s.SaveComponent(ctx, principal(ctx), in.Key, 0, in.Body))
		})
	huma.Register(a, api.Op("update-component", http.MethodPut, "/api/v1/projects/{key}/components/{id}", "Change a component", tp),
		func(ctx context.Context, in *componentInput) (*body[*Component], error) {
			return out(s.SaveComponent(ctx, principal(ctx), in.Key, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-component", http.MethodDelete, "/api/v1/projects/{key}/components/{id}", "Delete a component", tp),
		func(ctx context.Context, in *struct {
			Key string `path:"key"`
			ID  int64  `path:"id"`
		}) (*struct{}, error) {
			return nil, s.DeleteComponent(ctx, principal(ctx), in.Key, in.ID)
		})
	huma.Register(a, api.Op("create-version", http.MethodPost, "/api/v1/projects/{key}/versions", "Create a version", tp),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body VersionInput
		}) (*body[*Version], error) {
			return out(s.SaveVersion(ctx, principal(ctx), in.Key, 0, in.Body))
		})
	huma.Register(a, api.Op("update-version", http.MethodPut, "/api/v1/projects/{key}/versions/{id}", "Change or release a version", tp),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			ID   int64  `path:"id"`
			Body VersionInput
		}) (*body[*Version], error) {
			return out(s.SaveVersion(ctx, principal(ctx), in.Key, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-version", http.MethodDelete, "/api/v1/projects/{key}/versions/{id}", "Delete a version", tp),
		func(ctx context.Context, in *struct {
			Key    string `path:"key"`
			ID     int64  `path:"id"`
			MoveTo int64  `query:"moveTo" doc:"Version that takes over the issues"`
		}) (*struct{}, error) {
			return nil, s.DeleteVersion(ctx, principal(ctx), in.Key, in.ID, in.MoveTo)
		})

	// Configuration
	huma.Register(a, api.Op("get-issue-config", http.MethodGet, "/api/v1/issue-config", "Issue configuration", tc),
		func(ctx context.Context, _ *struct{}) (*body[*Config], error) {
			if _, err := signedIn(ctx); err != nil {
				return nil, err
			}
			return out(s.Configuration(ctx))
		})
	registerConfigOps(a, s)
	huma.Register(a, api.Op("list-workflows", http.MethodGet, "/api/v1/issue-config/workflows", "List workflows", tc),
		func(ctx context.Context, _ *struct{}) (*body[[]WorkflowInfo], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Workflows(ctx, p))
		})
	huma.Register(a, api.Op("list-permission-schemes", http.MethodGet, "/api/v1/issue-config/permission-schemes", "List permission schemes", tc),
		func(ctx context.Context, _ *struct{}) (*body[[]PermissionScheme], error) {
			return out(s.PermissionSchemes(ctx))
		})
	huma.Register(a, api.Op("list-security-schemes", http.MethodGet, "/api/v1/issue-config/security-schemes", "List security schemes", tc),
		func(ctx context.Context, _ *struct{}) (*body[[]SecurityScheme], error) {
			return out(s.SecuritySchemes(ctx))
		})
	huma.Register(a, api.Op("list-notification-schemes", http.MethodGet, "/api/v1/issue-config/notification-schemes", "List notification schemes", tc),
		func(ctx context.Context, _ *struct{}) (*body[[]NotificationScheme], error) {
			return out(s.NotificationSchemes(ctx))
		})

	// Issues
	huma.Register(a, api.Op("create-issue", http.MethodPost, "/api/v1/issues", "Create an issue", ti),
		func(ctx context.Context, in *body[NewIssue]) (*issueOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return issueOut(s.CreateIssue(ctx, p, in.Body))
		})
	huma.Register(a, api.Op("get-issue", http.MethodGet, "/api/v1/issues/{key}", "Get an issue", ti),
		func(ctx context.Context, in *struct {
			Key      string `path:"key" doc:"Project or issue key"`
			Markdown bool   `query:"markdown" doc:"Also give the description as Markdown (descriptionMarkdown)"`
		}) (*issueOutput, error) {
			i, err := s.GetIssue(ctx, principal(ctx), in.Key)
			if err == nil && in.Markdown && i.Description != nil {
				i.Markdown = docmodel.Markdown(i.Description)
			}
			return issueOut(i, err)
		})
	huma.Register(a, api.Op("update-issue", http.MethodPatch, "/api/v1/issues/{key}", "Change fields of an issue", ti),
		func(ctx context.Context, in *struct {
			Key     string `path:"key"`
			IfMatch string `header:"If-Match" doc:"ETag of the version the change is based on"`
			Body    struct {
				Fields   map[string]json.RawMessage `json:"fields,omitempty"`
				Markdown *string                    `json:"descriptionMarkdown,omitempty" doc:"Alternative to fields.description: the description as Markdown; empty removes it"`
			}
		}) (*issueOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			v, _, err := api.IfMatch(in.IfMatch)
			if err != nil {
				return nil, err
			}
			fields := in.Body.Fields
			if md := in.Body.Markdown; md != nil {
				if _, both := fields[FieldDescription]; both {
					return nil, apperr.Invalid("issue.description-twice", "Give the description either as a document or as Markdown.")
				}
				fields = maps.Clone(fields)
				if fields == nil {
					fields = map[string]json.RawMessage{}
				}
				fields[FieldDescription] = json.RawMessage("null")
				if strings.TrimSpace(*md) != "" {
					fields[FieldDescription], _ = json.Marshal(docmodel.FromMarkdown(*md))
				}
			}
			return issueOut(s.UpdateIssue(ctx, p, in.Key, v, fields))
		})
	huma.Register(a, api.Op("delete-issue", http.MethodDelete, "/api/v1/issues/{key}", "Delete an issue", ti),
		func(ctx context.Context, in *keyPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteIssue(ctx, p, in.Key)
		})
	huma.Register(a, api.Op("list-transitions", http.MethodGet, "/api/v1/issues/{key}/transitions", "Available transitions", ti),
		func(ctx context.Context, in *keyPath) (*body[[]AvailableTransition], error) {
			return out(s.Transitions(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("transition-issue", http.MethodPost, "/api/v1/issues/{key}/transitions", "Run a transition", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body TransitionInput
		}) (*issueOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return issueOut(s.Transition(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("list-comments", http.MethodGet, "/api/v1/issues/{key}/comments", "Comments of an issue", ti),
		func(ctx context.Context, in *keyPath) (*body[[]Comment], error) {
			return out(s.Comments(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("add-comment", http.MethodPost, "/api/v1/issues/{key}/comments", "Add a comment", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body CommentInput
		}) (*body[*Comment], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.AddComment(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("update-comment", http.MethodPut, "/api/v1/issues/{key}/comments/{id}", "Change a comment", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			ID   int64  `path:"id"`
			Body CommentInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.UpdateComment(ctx, p, in.Key, in.ID, in.Body)
		})
	huma.Register(a, api.Op("delete-comment", http.MethodDelete, "/api/v1/issues/{key}/comments/{id}", "Delete a comment", ti),
		func(ctx context.Context, in *struct {
			Key string `path:"key"`
			ID  int64  `path:"id"`
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteComment(ctx, p, in.Key, in.ID)
		})
	huma.Register(a, api.Op("get-issue-history", http.MethodGet, "/api/v1/issues/{key}/history", "Change history", ti),
		func(ctx context.Context, in *keyPath) (*body[[]Change], error) {
			return out(s.History(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("list-worklogs", http.MethodGet, "/api/v1/issues/{key}/worklogs", "Work logs", ti),
		func(ctx context.Context, in *keyPath) (*body[[]Worklog], error) {
			return out(s.Worklogs(ctx, principal(ctx), in.Key))
		})
	huma.Register(a, api.Op("add-worklog", http.MethodPost, "/api/v1/issues/{key}/worklogs", "Log work", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body WorklogInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.AddWorklog(ctx, p, in.Key, in.Body)
		})
	huma.Register(a, api.Op("delete-worklog", http.MethodDelete, "/api/v1/issues/{key}/worklogs/{id}", "Delete a work log", ti),
		func(ctx context.Context, in *struct {
			Key string `path:"key"`
			ID  int64  `path:"id"`
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteWorklog(ctx, p, in.Key, in.ID)
		})
	huma.Register(a, api.Op("add-issue-link", http.MethodPost, "/api/v1/issues/{key}/links", "Link to another issue", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body LinkInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.AddLink(ctx, p, in.Key, in.Body)
		})
	huma.Register(a, api.Op("add-issue-subtask", http.MethodPost, "/api/v1/issues/{key}/subtasks", "Make an existing issue a sub-task", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body SubtaskInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.AddSubtask(ctx, p, in.Key, in.Body)
		})
	huma.Register(a, api.Op("delete-issue-link", http.MethodDelete, "/api/v1/issue-links/{id}", "Remove a link", ti),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteLink(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("delete-issue-attachment", http.MethodDelete, "/api/v1/issue-attachments/{id}", "Delete an attachment", ti),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteAttachment(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("list-watchers", http.MethodGet, "/api/v1/issues/{key}/watchers", "Watchers of an issue", ti),
		func(ctx context.Context, in *keyPath) (*body[[]identity.UserRef], error) {
			return out(s.WatcherList(ctx, principal(ctx), in.Key))
		})
	watch := func(add bool) func(ctx context.Context, in *struct {
		Key    string `path:"key"`
		UserID int64  `query:"userId" doc:"Default: the caller"`
	}) (*struct{}, error) {
		return func(ctx context.Context, in *struct {
			Key    string `path:"key"`
			UserID int64  `query:"userId" doc:"Default: the caller"`
		}) (*struct{}, error) {
			return nil, s.SetWatching(ctx, principal(ctx), in.Key, in.UserID, add)
		}
	}
	huma.Register(a, api.Op("watch-issue", http.MethodPut, "/api/v1/issues/{key}/watchers", "Watch an issue", ti), watch(true))
	huma.Register(a, api.Op("unwatch-issue", http.MethodDelete, "/api/v1/issues/{key}/watchers", "Stop watching an issue", ti), watch(false))
	huma.Register(a, api.Op("vote-issue", http.MethodPut, "/api/v1/issues/{key}/vote", "Vote for an issue", ti),
		func(ctx context.Context, in *keyPath) (*struct{}, error) {
			return nil, s.SetVote(ctx, principal(ctx), in.Key, true)
		})
	huma.Register(a, api.Op("unvote-issue", http.MethodDelete, "/api/v1/issues/{key}/vote", "Remove the vote", ti),
		func(ctx context.Context, in *keyPath) (*struct{}, error) {
			return nil, s.SetVote(ctx, principal(ctx), in.Key, false)
		})

	// Search and filters
	huma.Register(a, api.Op("search-issues", http.MethodGet, "/api/v1/search", "Search issues with KQL", ts),
		func(ctx context.Context, in *struct {
			Q      string `query:"q" maxLength:"10000" doc:"KQL query"`
			Limit  int    `query:"limit" minimum:"1" maximum:"1000" default:"50"`
			Offset int    `query:"offset" minimum:"0"`
		}) (*body[*SearchResult], error) {
			return out(s.Search(ctx, principal(ctx), in.Q, in.Limit, in.Offset))
		})
	huma.Register(a, api.Op("list-filters", http.MethodGet, "/api/v1/filters", "List a page of the saved filters visible to the caller", ts),
		func(ctx context.Context, in *sharedListInput) (*body[*FilterList], error) {
			return out(s.Filters(ctx, principal(ctx), in.selection()))
		})
	huma.Register(a, api.Op("create-filter", http.MethodPost, "/api/v1/filters", "Save a filter", ts),
		func(ctx context.Context, in *body[FilterInput]) (*body[*Filter], error) {
			return out(s.SaveFilter(ctx, principal(ctx), 0, in.Body))
		})
	huma.Register(a, api.Op("get-filter", http.MethodGet, "/api/v1/filters/{id}", "Get a filter", ts),
		func(ctx context.Context, in *idPath) (*body[*Filter], error) {
			return out(s.GetFilter(ctx, principal(ctx), in.ID))
		})
	huma.Register(a, api.Op("update-filter", http.MethodPut, "/api/v1/filters/{id}", "Change a filter", ts),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body FilterInput
		}) (*body[*Filter], error) {
			return out(s.SaveFilter(ctx, principal(ctx), in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-filter", http.MethodDelete, "/api/v1/filters/{id}", "Delete a filter", ts),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			return nil, s.DeleteFilter(ctx, principal(ctx), in.ID)
		})
	huma.Register(a, api.Op("favourite-filter", http.MethodPut, "/api/v1/filters/{id}/favourite", "Mark a filter as favourite", ts),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			return nil, s.SetFavourite(ctx, principal(ctx), in.ID, true)
		})
	huma.Register(a, api.Op("unfavourite-filter", http.MethodDelete, "/api/v1/filters/{id}/favourite", "Remove a favourite", ts),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			return nil, s.SetFavourite(ctx, principal(ctx), in.ID, false)
		})
}
