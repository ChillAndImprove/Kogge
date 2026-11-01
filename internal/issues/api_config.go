// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/issues/workflow"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

const tagConfig = "Issue configuration"

// IssueTypeInput creates or changes an issue type.
type IssueTypeInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	Description string `json:"description,omitempty" maxLength:"1000"`
	Icon        string `json:"icon" enum:"epic,story,task,bug,subtask"`
	Color       string `json:"color" minLength:"1" maxLength:"20" doc:"A colour name of the theme, e.g. blue"`
	Level       int    `json:"level" enum:"-1,0,1" doc:"1 = epic, 0 = standard, -1 = sub-task"`
}

// StatusInput creates or changes a status.
type StatusInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	Description string `json:"description,omitempty" maxLength:"1000"`
	Category    string `json:"category" enum:"todo,inprogress,done"`
}

// PriorityInput creates or changes a priority.
type PriorityInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	Description string `json:"description,omitempty" maxLength:"1000"`
	Icon        string `json:"icon" enum:"highest,high,medium,low,lowest"`
	Color       string `json:"color" minLength:"1" maxLength:"20"`
	IsDefault   bool   `json:"isDefault,omitempty"`
}

// ResolutionInput creates or changes a resolution.
type ResolutionInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	Description string `json:"description,omitempty" maxLength:"1000"`
	IsDefault   bool   `json:"isDefault,omitempty"`
}

// LinkTypeInput creates or changes a link type.
type LinkTypeInput struct {
	Name    string `json:"name" minLength:"1" maxLength:"60"`
	Outward string `json:"outward" minLength:"1" maxLength:"60" doc:"e.g. blocks"`
	Inward  string `json:"inward" minLength:"1" maxLength:"60" doc:"e.g. is blocked by"`
}

// RoleInput creates or changes a project role.
type RoleInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	Description string `json:"description,omitempty" maxLength:"1000"`
}

// WorkflowInput creates or replaces a workflow.
type WorkflowInput struct {
	Name        string              `json:"name" minLength:"1" maxLength:"100"`
	Description string              `json:"description,omitempty" maxLength:"1000"`
	Definition  workflow.Definition `json:"definition"`
}

// PermissionSchemeInput creates or replaces a permission scheme.
type PermissionSchemeInput struct {
	Name        string  `json:"name" minLength:"1" maxLength:"100"`
	Description string  `json:"description,omitempty" maxLength:"1000"`
	Grants      []Grant `json:"grants"`
}

// SecuritySchemeInput creates or replaces an issue security scheme.
type SecuritySchemeInput struct {
	Name           string               `json:"name" minLength:"1" maxLength:"100"`
	Description    string               `json:"description,omitempty" maxLength:"1000"`
	DefaultLevelID *int64               `json:"defaultLevelId,omitempty"`
	Levels         []SecurityLevelInput `json:"levels"`
}

// SecurityLevelInput is a level of a security scheme; levels with an id keep it.
type SecurityLevelInput struct {
	ID          int64         `json:"id,omitempty" doc:"Id of an existing level, or a negative temporary id of a new level that defaultLevelId may reference"`
	Name        string        `json:"name" minLength:"1" maxLength:"100"`
	Description string        `json:"description,omitempty" maxLength:"1000"`
	Members     []LevelMember `json:"members"`
}

// NotificationSchemeInput creates or replaces a notification scheme.
type NotificationSchemeInput struct {
	Name        string              `json:"name" minLength:"1" maxLength:"100"`
	Description string              `json:"description,omitempty" maxLength:"1000"`
	Entries     []NotificationEntry `json:"entries"`
}

// WorkflowTransition documents workflow.Transition, whose "from" is a list of status ids or "any".
type WorkflowTransition struct {
	ID            int             `json:"id"`
	Name          string          `json:"name"`
	From          transitionFrom  `json:"from"`
	To            int64           `json:"to"`
	Screen        []string        `json:"screen,omitempty" doc:"Fields asked for when the transition runs, e.g. resolution"`
	Conditions    []workflow.Rule `json:"conditions,omitempty"`
	Validators    []workflow.Rule `json:"validators,omitempty"`
	PostFunctions []workflow.Rule `json:"postFunctions,omitempty"`
}

type transitionFrom struct{}

// Schema implements huma.SchemaProvider.
func (transitionFrom) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Description: `Status ids the transition is available from, or "any" for a global transition`,
		OneOf: []*huma.Schema{
			{Type: huma.TypeArray, Items: &huma.Schema{Type: huma.TypeInteger, Format: "int64"}},
			{Type: huma.TypeString, Enum: []any{"any"}},
		},
	}
}

// Kinds whose objects have an explicit order.
var orderedKinds = map[string]string{
	"issue-types": "issue_types",
	"statuses":    "statuses",
	"priorities":  "priorities",
	"resolutions": "resolutions",
}

type savedID struct {
	ID int64 `json:"id"`
}

// registerConfig adds the create, update and delete operations of one kind of configuration object.
func registerConfig[T any](a huma.API, s *Service, kind, name string, save func(ctx context.Context, id int64, v T) (int64, error)) {
	saved := func(id int64, err error) (*body[savedID], error) { return out(savedID{ID: id}, err) }
	huma.Register(a, api.Op("create-"+kind, http.MethodPost, "/api/v1/issue-config/"+kind, "Create "+name, tagConfig),
		func(ctx context.Context, in *body[T]) (*body[savedID], error) {
			return saved(save(ctx, 0, in.Body))
		})
	huma.Register(a, api.Op("update-"+kind, http.MethodPut, "/api/v1/issue-config/"+kind+"/{id}", "Change "+name, tagConfig),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body T
		}) (*body[savedID], error) {
			return saved(save(ctx, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-"+kind, http.MethodDelete, "/api/v1/issue-config/"+kind+"/{id}", "Delete "+name, tagConfig),
		func(ctx context.Context, in *idPath) (*struct{}, error) { return nil, s.DeleteConfig(ctx, kind, in.ID) })
	if _, ok := orderedKinds[kind]; ok {
		huma.Register(a, api.Op("order-"+kind, http.MethodPut, "/api/v1/issue-config/"+kind+"-order", "Reorder "+kind, tagConfig),
			func(ctx context.Context, in *body[[]int64]) (*struct{}, error) {
				return nil, s.OrderConfig(ctx, kind, in.Body)
			})
	}
}

func registerConfigOps(a huma.API, s *Service) {
	a.OpenAPI().Components.Schemas.RegisterTypeAlias(reflect.TypeFor[workflow.Transition](), reflect.TypeFor[WorkflowTransition]())
	registerConfig(a, s, "issue-types", "an issue type", func(ctx context.Context, id int64, v IssueTypeInput) (int64, error) {
		return s.SaveIssueType(ctx, id, IssueType{Name: v.Name, Description: v.Description, Icon: v.Icon, Color: v.Color, Level: v.Level})
	})
	registerConfig(a, s, "statuses", "a status", func(ctx context.Context, id int64, v StatusInput) (int64, error) {
		return s.SaveStatus(ctx, id, Status{Name: v.Name, Description: v.Description, Category: v.Category})
	})
	registerConfig(a, s, "priorities", "a priority", func(ctx context.Context, id int64, v PriorityInput) (int64, error) {
		return s.SavePriority(ctx, id, Priority{Name: v.Name, Description: v.Description, Icon: v.Icon, Color: v.Color, IsDefault: v.IsDefault})
	})
	registerConfig(a, s, "resolutions", "a resolution", func(ctx context.Context, id int64, v ResolutionInput) (int64, error) {
		return s.SaveResolution(ctx, id, Resolution{Name: v.Name, Description: v.Description, IsDefault: v.IsDefault})
	})
	registerConfig(a, s, "link-types", "a link type", func(ctx context.Context, id int64, v LinkTypeInput) (int64, error) {
		return s.SaveLinkType(ctx, id, LinkType{Name: v.Name, Outward: v.Outward, Inward: v.Inward})
	})
	registerConfig(a, s, "roles", "a project role", func(ctx context.Context, id int64, v RoleInput) (int64, error) {
		return s.SaveRole(ctx, id, Role{Name: v.Name, Description: v.Description})
	})
	registerConfig(a, s, "fields", "a custom field", s.SaveCustomField)
	registerConfig(a, s, "workflows", "a workflow", func(ctx context.Context, id int64, v WorkflowInput) (int64, error) {
		if len(v.Definition.Statuses) == 0 {
			return 0, apperr.Invalid("workflow.definition", "The workflow needs at least one status.")
		}
		return s.SaveWorkflow(ctx, id, v.Name, v.Description, &v.Definition)
	})
	registerConfig(a, s, "permission-schemes", "a permission scheme", func(ctx context.Context, id int64, v PermissionSchemeInput) (int64, error) {
		return s.SavePermissionScheme(ctx, id, v.Name, v.Description, v.Grants)
	})
	registerConfig(a, s, "security-schemes", "a security scheme", func(ctx context.Context, id int64, v SecuritySchemeInput) (int64, error) {
		levels := make([]SecurityLevel, len(v.Levels))
		for i, l := range v.Levels {
			levels[i] = SecurityLevel{ID: l.ID, Name: l.Name, Description: l.Description, Members: l.Members}
		}
		return s.SaveSecurityScheme(ctx, id, SecurityScheme{Name: v.Name, Description: v.Description, DefaultLevelID: v.DefaultLevelID, Levels: levels})
	})
	registerConfig(a, s, "notification-schemes", "a notification scheme", func(ctx context.Context, id int64, v NotificationSchemeInput) (int64, error) {
		return s.SaveNotificationScheme(ctx, id, NotificationScheme{Name: v.Name, Description: v.Description, Entries: v.Entries})
	})
}
