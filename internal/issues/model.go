// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package issues implements issue projects, their configuration, issues, KQL search, boards and sprints.
package issues

import (
	"time"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
)

// Project permissions.
const (
	PermBrowse           = "project.browse"
	PermAdmin            = "project.admin"
	PermManageSprints    = "project.manage-sprints"
	PermViewDevelopment  = "project.view-development"
	PermCreate           = "issue.create"
	PermEdit             = "issue.edit"
	PermTransition       = "issue.transition"
	PermSchedule         = "issue.schedule"
	PermMove             = "issue.move"
	PermBulkChange       = "issue.bulk-change"
	PermAssign           = "issue.assign"
	PermAssignable       = "issue.assignable"
	PermResolve          = "issue.resolve"
	PermModifyReporter   = "issue.modify-reporter"
	PermDelete           = "issue.delete"
	PermLink             = "issue.link"
	PermSetSecurity      = "issue.set-security"
	PermViewWatchers     = "watchers.view"
	PermManageWatchers   = "watchers.manage"
	PermCommentAdd       = "comment.add"
	PermCommentEditAll   = "comment.edit-all"
	PermCommentEditOwn   = "comment.edit-own"
	PermCommentDeleteAll = "comment.delete-all"
	PermCommentDeleteOwn = "comment.delete-own"
	PermAttachCreate     = "attachment.create"
	PermAttachDeleteAll  = "attachment.delete-all"
	PermAttachDeleteOwn  = "attachment.delete-own"
	PermWorklogAdd       = "worklog.add"
	PermWorklogEditAll   = "worklog.edit-all"
	PermWorklogEditOwn   = "worklog.edit-own"
	PermWorklogDeleteAll = "worklog.delete-all"
	PermWorklogDeleteOwn = "worklog.delete-own"
)

// ProjectPermissions is the catalogue in display order.
var ProjectPermissions = []string{PermBrowse, PermAdmin, PermManageSprints, PermViewDevelopment, PermCreate,
	PermEdit, PermTransition, PermSchedule, PermMove, PermBulkChange, PermAssign, PermAssignable, PermResolve,
	PermModifyReporter, PermDelete, PermLink, PermSetSecurity, PermViewWatchers, PermManageWatchers, PermCommentAdd,
	PermCommentEditAll, PermCommentEditOwn, PermCommentDeleteAll, PermCommentDeleteOwn, PermAttachCreate,
	PermAttachDeleteAll, PermAttachDeleteOwn, PermWorklogAdd, PermWorklogEditAll, PermWorklogEditOwn,
	PermWorklogDeleteAll, PermWorklogDeleteOwn}

// Status categories.
const (
	CategoryTodo       = "todo"
	CategoryInProgress = "inprogress"
	CategoryDone       = "done"
)

// IssueType is a configuration object.
type IssueType struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Level       int    `json:"level" doc:"1 = epic, 0 = standard, -1 = sub-task"`
	Position    int    `json:"position"`
}

// Status is a workflow status.
type Status struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category" enum:"todo,inprogress,done"`
	Position    int    `json:"position"`
}

// Priority orders issues by importance (position 1 is the highest).
type Priority struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Position    int    `json:"position"`
	IsDefault   bool   `json:"isDefault"`
}

// Resolution says how an issue was finished.
type Resolution struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Position    int    `json:"position"`
	IsDefault   bool   `json:"isDefault"`
}

// LinkType relates two issues.
type LinkType struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Outward string `json:"outward" doc:"e.g. blocks"`
	Inward  string `json:"inward" doc:"e.g. is blocked by"`
}

// Role is a project role definition.
type Role struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Option is a choice of a select-like custom field.
type Option struct {
	ID       int64  `json:"id"`
	Value    string `json:"value"`
	ParentID int64  `json:"parentId,omitempty" doc:"Parent option of a cascading select"`
	Disabled bool   `json:"disabled,omitempty"`
}

// CustomField is a field definition.
type CustomField struct {
	ID           int64          `json:"id"`
	Key          string         `json:"key" doc:"cf_<id>"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Type         string         `json:"type"`
	Options      []Option       `json:"options"`
	Config       map[string]any `json:"config"`
	ProjectIDs   []int64        `json:"projectIds" doc:"Projects the field applies to; empty = all"`
	IssueTypeIDs []int64        `json:"issueTypeIds" doc:"Issue types the field applies to; empty = all"`
}

// Grant gives a permission to a holder in a permission scheme.
type Grant struct {
	Permission string `json:"permission"`
	HolderType string `json:"holderType" enum:"role,group,user,reporter,assignee,project-lead,logged-in,anyone"`
	HolderID   int64  `json:"holderId,omitempty"`
	HolderName string `json:"holderName,omitempty"`
}

// PermissionScheme is a shareable set of grants.
type PermissionScheme struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Grants      []Grant `json:"grants"`
	Projects    int     `json:"projects" doc:"Number of projects using the scheme"`
}

// SecurityLevel restricts issues to its members.
type SecurityLevel struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Members     []LevelMember `json:"members"`
}

// LevelMember is a member of a security level.
type LevelMember struct {
	HolderType string `json:"holderType" enum:"role,group,user,reporter,assignee,project-lead"`
	HolderID   int64  `json:"holderId,omitempty"`
	HolderName string `json:"holderName,omitempty"`
}

// SecurityScheme is a set of security levels.
type SecurityScheme struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	DefaultLevelID *int64          `json:"defaultLevelId,omitempty"`
	Levels         []SecurityLevel `json:"levels"`
}

// Ref is the short form of a named object.
type Ref struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ProjectRef is the short form of a project.
type ProjectRef struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Project is an issue project.
type Project struct {
	ID                   int64             `json:"id"`
	Key                  string            `json:"key"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	Type                 string            `json:"type" enum:"software,business"`
	Category             string            `json:"category"`
	Lead                 *identity.UserRef `json:"lead,omitempty"`
	DefaultAssignee      string            `json:"defaultAssignee" enum:"unassigned,lead"`
	PermissionSchemeID   int64             `json:"permissionSchemeId"`
	NotificationSchemeID *int64            `json:"notificationSchemeId,omitempty"`
	SecuritySchemeID     *int64            `json:"securitySchemeId,omitempty"`
	DefaultWorkflowID    int64             `json:"defaultWorkflowId"`
	Archived             bool              `json:"archived"`
	CreatedAt            time.Time         `json:"createdAt"`
	Version              int64             `json:"version"`
	Issues               int64             `json:"issues"`
}

// Ref returns the short form.
func (p *Project) Ref() ProjectRef { return ProjectRef{ID: p.ID, Key: p.Key, Name: p.Name} }

// Component groups issues of a project.
type Component struct {
	ID              int64             `json:"id"`
	ProjectID       int64             `json:"projectId"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Lead            *identity.UserRef `json:"lead,omitempty"`
	DefaultAssignee string            `json:"defaultAssignee" enum:"none,component-lead,project-lead"`
	Archived        bool              `json:"archived"`
	Issues          int               `json:"issues"`
}

// Version is a release of a project.
type Version struct {
	ID          int64      `json:"id"`
	ProjectID   int64      `json:"projectId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	StartDate   *string    `json:"startDate,omitempty" format:"date"`
	ReleaseDate *string    `json:"releaseDate,omitempty" format:"date"`
	Released    bool       `json:"released"`
	ReleasedAt  *time.Time `json:"releasedAt,omitempty"`
	Archived    bool       `json:"archived"`
	Position    int        `json:"position"`
}

// IssueRef is the short form of an issue.
type IssueRef struct {
	ID       int64             `json:"id"`
	Key      string            `json:"key"`
	Summary  string            `json:"summary"`
	Type     *IssueType        `json:"type"`
	Status   *Status           `json:"status"`
	Priority *Priority         `json:"priority,omitempty"`
	Assignee *identity.UserRef `json:"assignee,omitempty"`
}

// IssueLink is a link as seen from one issue.
type IssueLink struct {
	ID        int64    `json:"id"`
	Type      LinkType `json:"type"`
	Direction string   `json:"direction" enum:"outward,inward"`
	Label     string   `json:"label" doc:"The link type's description for this direction"`
	Issue     IssueRef `json:"issue"`
}

// Attachment is a file of an issue.
type Attachment struct {
	ID          int64             `json:"id"`
	Filename    string            `json:"filename"`
	ContentType string            `json:"contentType"`
	Size        int64             `json:"size"`
	Author      *identity.UserRef `json:"author,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	URL         string            `json:"url"`
	CanDelete   bool              `json:"canDelete" doc:"The caller may delete the attachment"`
}

// IssuePermissions tells clients what the user may do with an issue.
type IssuePermissions struct {
	Edit       bool `json:"edit"`
	Transition bool `json:"transition"`
	Assign     bool `json:"assign"`
	Delete     bool `json:"delete"`
	Comment    bool `json:"comment"`
	Attach     bool `json:"attach"`
	Link       bool `json:"link"`
	LogWork    bool `json:"logWork"`
	Move       bool `json:"move"`
	Watchers   bool `json:"watchers"`
	Security   bool `json:"security"`
	Schedule   bool `json:"schedule" doc:"Rank the issue and plan it into sprints"`
	// ModifyReporter is issue.modify-reporter: change who reported the issue.
	ModifyReporter bool `json:"modifyReporter" doc:"Change the reporter"`
	// Development is project.view-development: branches, commits, pull requests, builds and deployments.
	Development bool `json:"development" doc:"See the development of the issue"`
}

// Issue is an issue with all fields.
type Issue struct {
	ID                int64              `json:"id"`
	Key               string             `json:"key"`
	Project           ProjectRef         `json:"project"`
	Type              *IssueType         `json:"type"`
	Status            *Status            `json:"status"`
	Priority          *Priority          `json:"priority,omitempty"`
	Resolution        *Resolution        `json:"resolution,omitempty"`
	Summary           string             `json:"summary"`
	Description       *docmodel.Doc      `json:"description,omitempty"`
	Markdown          string             `json:"descriptionMarkdown,omitempty" doc:"The description as Markdown, when asked for"`
	Environment       string             `json:"environment"`
	Reporter          *identity.UserRef  `json:"reporter,omitempty"`
	Creator           *identity.UserRef  `json:"creator,omitempty"`
	Assignee          *identity.UserRef  `json:"assignee,omitempty"`
	Parent            *IssueRef          `json:"parent,omitempty"`
	SecurityLevel     *Ref               `json:"securityLevel,omitempty"`
	DueDate           *string            `json:"dueDate,omitempty" format:"date"`
	OriginalEstimate  *int64             `json:"originalEstimate,omitempty" doc:"Seconds"`
	RemainingEstimate *int64             `json:"remainingEstimate,omitempty" doc:"Seconds"`
	TimeSpent         int64              `json:"timeSpent" doc:"Seconds"`
	Labels            []string           `json:"labels"`
	Components        []Ref              `json:"components"`
	FixVersions       []Ref              `json:"fixVersions"`
	AffectsVersions   []Ref              `json:"affectsVersions"`
	Fields            map[string]any     `json:"fields" doc:"Custom field values by key (cf_<id>)"`
	FieldUsers        []identity.UserRef `json:"fieldUsers,omitempty" doc:"The users that user fields refer to"`
	FieldGroups       []Ref              `json:"fieldGroups,omitempty" doc:"The groups that group fields refer to"`
	Rank              string             `json:"rank"`
	Votes             int                `json:"votes"`
	Voted             bool               `json:"voted"`
	Watching          bool               `json:"watching"`
	Watchers          int                `json:"watchers"`
	Sprint            *SprintRef         `json:"sprint,omitempty" doc:"The open sprint of the issue"`
	ClosedSprints     []SprintRef        `json:"closedSprints" doc:"Completed sprints the issue went through"`
	Subtasks          []IssueRef         `json:"subtasks"`
	Links             []IssueLink        `json:"links"`
	Attachments       []Attachment       `json:"attachments"`
	Created           time.Time          `json:"created"`
	Updated           time.Time          `json:"updated"`
	Resolved          *time.Time         `json:"resolved,omitempty"`
	Version           int64              `json:"version"`
	Permissions       IssuePermissions   `json:"permissions"`

	projectID       int64
	typeID          int64
	statusID        int64
	reporterID      int64
	assigneeID      int64
	securityLevelID int64
	parentID        int64
	leadID          int64
}

// ChangeItem is one field change in the history.
type ChangeItem struct {
	Field      string `json:"field" doc:"Field key, e.g. status or cf_12"`
	Name       string `json:"name,omitempty" doc:"Display name of a custom field"`
	From       any    `json:"from,omitempty"`
	FromString string `json:"fromString,omitempty"`
	To         any    `json:"to,omitempty"`
	ToString   string `json:"toString,omitempty"`
}

// Change is an entry of the history.
type Change struct {
	ID    int64             `json:"id"`
	Actor *identity.UserRef `json:"actor,omitempty"`
	At    time.Time         `json:"at"`
	Items []ChangeItem      `json:"items"`
}

// Comment is a comment on an issue.
type Comment struct {
	ID             int64             `json:"id"`
	Author         *identity.UserRef `json:"author,omitempty"`
	Body           *docmodel.Doc     `json:"body"`
	VisibilityType string            `json:"visibilityType,omitempty" enum:"role,group,"`
	VisibilityID   int64             `json:"visibilityId,omitempty"`
	VisibilityName string            `json:"visibilityName,omitempty"`
	Created        time.Time         `json:"created"`
	Updated        time.Time         `json:"updated"`
	Edited         bool              `json:"edited"`
	CanEdit        bool              `json:"canEdit"`
	CanDelete      bool              `json:"canDelete"`
}

// Worklog is time logged on an issue.
type Worklog struct {
	ID        int64             `json:"id"`
	Author    *identity.UserRef `json:"author,omitempty"`
	Started   time.Time         `json:"started"`
	Seconds   int64             `json:"seconds"`
	Comment   string            `json:"comment"`
	Created   time.Time         `json:"created"`
	CanEdit   bool              `json:"canEdit"`
	CanDelete bool              `json:"canDelete"`
}
