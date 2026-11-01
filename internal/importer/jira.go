// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/issues/kql"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/rank"
)

// --- Jira REST v2 shapes (only what the import uses) ---

type jUser struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	Active       *bool  `json:"active"`
}

// inactive reports whether the source says the user is no longer active.
func (u *jUser) inactive() bool { return u.Active != nil && !*u.Active }

type jRef struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type jIssueType struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Subtask     bool   `json:"subtask"`
}

type jStatus struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

type jLinkType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

type jField struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema struct {
		Type   string `json:"type"`
		Items  string `json:"items"`
		Custom string `json:"custom"`
	} `json:"schema"`
}

type jProject struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ProjectTypeKey  string `json:"projectTypeKey"`
	ProjectCategory *struct {
		Name string `json:"name"`
	} `json:"projectCategory"`
	Lead       *jUser `json:"lead"`
	Components []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Lead        *jUser `json:"lead"`
	} `json:"components"`
	Versions []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		StartDate   string `json:"startDate"`
		ReleaseDate string `json:"releaseDate"`
		Released    bool   `json:"released"`
		Archived    bool   `json:"archived"`
	} `json:"versions"`
	IssueTypes []jIssueType `json:"issueTypes"`
}

type jProjectStatuses []struct {
	ID       string    `json:"id"`
	Statuses []jStatus `json:"statuses"`
}

type jComment struct {
	ID           string `json:"id"`
	Author       *jUser `json:"author"`
	Body         string `json:"body"`
	RenderedBody string `json:"renderedBody"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
	Visibility   *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"visibility"`
}

type jWorklog struct {
	ID               string `json:"id"`
	Author           *jUser `json:"author"`
	Comment          string `json:"comment"`
	Started          string `json:"started"`
	Created          string `json:"created"`
	TimeSpentSeconds int64  `json:"timeSpentSeconds"`
}

type jAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Author   *jUser `json:"author"`
	Created  string `json:"created"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Content  string `json:"content"`
}

type jIssue struct {
	ID             string                     `json:"id"`
	Key            string                     `json:"key"`
	Fields         map[string]json.RawMessage `json:"fields"`
	RenderedFields map[string]json.RawMessage `json:"renderedFields"`
	Changelog      *jChangelog                `json:"changelog"`
}

// jChangelog is the history of an issue; total counts all entries where the answer lists only some.
type jChangelog struct {
	Total     int        `json:"total"`
	Histories []jHistory `json:"histories"`
}

type jHistory struct {
	ID      string        `json:"id"`
	Author  *jUser        `json:"author"`
	Created string        `json:"created"`
	Items   []jChangeItem `json:"items"`
}

type jChangeItem struct {
	Field      string  `json:"field"`
	FieldType  string  `json:"fieldtype"`
	FieldID    string  `json:"fieldId"`
	From       *string `json:"from"`
	FromString *string `json:"fromString"`
	To         *string `json:"to"`
	ToString   *string `json:"toString"`
}

type jSearch struct {
	StartAt    int      `json:"startAt"`
	MaxResults int      `json:"maxResults"`
	Total      int      `json:"total"`
	Issues     []jIssue `json:"issues"`
}

type jSprint struct {
	ID            int64  `json:"id"`
	State         string `json:"state"`
	Name          string `json:"name"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	CompleteDate  string `json:"completeDate"`
	Goal          string `json:"goal"`
	OriginBoardID int64  `json:"originBoardId"`
	RapidViewID   int64  `json:"rapidViewId"`
}

// jBoardDetails is the configuration of a board with its filter and quick filters.
type jBoardDetails struct {
	Configuration *struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Filter struct {
			ID string `json:"id"`
		} `json:"filter"`
		SubQuery *struct {
			Query string `json:"query"`
		} `json:"subQuery"`
		ColumnConfig struct {
			Columns []struct {
				Name     string `json:"name"`
				Statuses []struct {
					ID string `json:"id"`
				} `json:"statuses"`
				Min *int `json:"min"`
				Max *int `json:"max"`
			} `json:"columns"`
			ConstraintType string `json:"constraintType"`
		} `json:"columnConfig"`
		Estimation *struct {
			Type  string `json:"type"`
			Field *struct {
				FieldID string `json:"fieldId"`
			} `json:"field"`
		} `json:"estimation"`
	} `json:"configuration"`
	Filter *struct {
		JQL string `json:"jql"`
	} `json:"filter"`
	QuickFilters *jPage[struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		JQL  string `json:"jql"`
	}] `json:"quickFilters"`
}

type jBoards struct {
	Values []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"values"`
}

type jPage[T any] struct {
	Values []T  `json:"values"`
	IsLast bool `json:"isLast"`
}

type jRole struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Actors      []struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"actors"`
}

type jPermissionScheme struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Permissions []struct {
		Permission string `json:"permission"`
		Holder     struct {
			Type        string `json:"type"`
			Parameter   string `json:"parameter"`
			ProjectRole *struct {
				Name string `json:"name"`
			} `json:"projectRole"`
			User *jUser `json:"user"`
		} `json:"holder"`
	} `json:"permissions"`
}

// jiraPermissions maps the permission keys of Jira to Kogge's project permissions.
var jiraPermissions = map[string]string{
	"ADMINISTER_PROJECTS": issues.PermAdmin, "BROWSE_PROJECTS": issues.PermBrowse, "MANAGE_SPRINTS_PERMISSION": issues.PermManageSprints,
	"VIEW_DEV_TOOLS": issues.PermViewDevelopment, "CREATE_ISSUES": issues.PermCreate, "EDIT_ISSUES": issues.PermEdit,
	"TRANSITION_ISSUES": issues.PermTransition, "SCHEDULE_ISSUES": issues.PermSchedule, "MOVE_ISSUES": issues.PermMove,
	"ASSIGN_ISSUES": issues.PermAssign, "ASSIGNABLE_USER": issues.PermAssignable, "RESOLVE_ISSUES": issues.PermResolve,
	"MODIFY_REPORTER": issues.PermModifyReporter, "DELETE_ISSUES": issues.PermDelete, "LINK_ISSUES": issues.PermLink,
	"SET_ISSUE_SECURITY": issues.PermSetSecurity, "VIEW_VOTERS_AND_WATCHERS": issues.PermViewWatchers,
	"MANAGE_WATCHERS": issues.PermManageWatchers, "ADD_COMMENTS": issues.PermCommentAdd, "EDIT_ALL_COMMENTS": issues.PermCommentEditAll,
	"EDIT_OWN_COMMENTS": issues.PermCommentEditOwn, "DELETE_ALL_COMMENTS": issues.PermCommentDeleteAll,
	"DELETE_OWN_COMMENTS": issues.PermCommentDeleteOwn, "CREATE_ATTACHMENTS": issues.PermAttachCreate,
	"DELETE_ALL_ATTACHMENTS": issues.PermAttachDeleteAll, "DELETE_OWN_ATTACHMENTS": issues.PermAttachDeleteOwn,
	"WORK_ON_ISSUES": issues.PermWorklogAdd, "EDIT_ALL_WORKLOGS": issues.PermWorklogEditAll, "EDIT_OWN_WORKLOGS": issues.PermWorklogEditOwn,
	"DELETE_ALL_WORKLOGS": issues.PermWorklogDeleteAll, "DELETE_OWN_WORKLOGS": issues.PermWorklogDeleteOwn,
	// Closing is a transition in Kogge.
	"CLOSE_ISSUES": issues.PermTransition,
}

// JiraOptions select what to migrate.
type JiraOptions struct {
	URL      string
	Token    string
	Projects []string
	Workdir  string
	// SkipAttachments leaves attachment files out of the extraction (for trial runs).
	SkipAttachments bool
	// Concurrency is how many requests for the details of issues and for attachment files run at a time
	// (default 4).
	Concurrency int
	// Restart begins the extraction afresh instead of resuming an interrupted one.
	Restart bool
}

// Jira migrates projects from Jira Data Center.
type Jira struct {
	opt    JiraOptions
	client *Client
	work   Workdir
	log    *slog.Logger
}

// NewJira prepares a Jira migration.
func NewJira(opt JiraOptions, log *slog.Logger) *Jira {
	return &Jira{opt: opt, client: NewClient(opt.URL, opt.Token, log), work: Workdir{Root: opt.Workdir}, log: log}
}

// selected returns the project keys given in the options.
func (j *Jira) selected() []string {
	var keys []string
	for _, k := range j.opt.Projects {
		if k = strings.ToUpper(strings.TrimSpace(k)); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// fieldMapping says how a Jira custom field becomes a Kogge field.
type fieldMapping struct {
	key     string
	name    string
	typ     string
	options map[string]int64 // option ids by value, completed while loading
}

var jiraFieldTypes = map[string]string{
	"com.atlassian.jira.plugin.system.customfieldtypes:float":           "number",
	"com.atlassian.jira.plugin.system.customfieldtypes:textfield":       "text",
	"com.atlassian.jira.plugin.system.customfieldtypes:textarea":        "textarea",
	"com.atlassian.jira.plugin.system.customfieldtypes:url":             "url",
	"com.atlassian.jira.plugin.system.customfieldtypes:datepicker":      "date",
	"com.atlassian.jira.plugin.system.customfieldtypes:datetime":        "datetime",
	"com.atlassian.jira.plugin.system.customfieldtypes:select":          "select",
	"com.atlassian.jira.plugin.system.customfieldtypes:radiobuttons":    "radio",
	"com.atlassian.jira.plugin.system.customfieldtypes:multiselect":     "multiselect",
	"com.atlassian.jira.plugin.system.customfieldtypes:multicheckboxes": "checkboxes",
	"com.atlassian.jira.plugin.system.customfieldtypes:labels":          "labels",
	"com.atlassian.jira.plugin.system.customfieldtypes:userpicker":      "user",
	"com.atlassian.jira.plugin.system.customfieldtypes:multiuserpicker": "multiuser",
}

var sprintPattern = regexp.MustCompile(`(\w+)=([^,\]]*)`)

// parseSprints reads the sprint field, which older Jira versions return as strings.
func parseSprints(raw json.RawMessage) []jSprint {
	var objs []jSprint
	if json.Unmarshal(raw, &objs) == nil && len(objs) > 0 && objs[0].ID != 0 {
		return objs
	}
	var strs []string
	if json.Unmarshal(raw, &strs) != nil {
		return nil
	}
	var out []jSprint
	for _, s := range strs {
		var sp jSprint
		for _, m := range sprintPattern.FindAllStringSubmatch(s, -1) {
			if m[2] == "<null>" {
				m[2] = ""
			}
			switch m[1] {
			case "id":
				sp.ID, _ = strconv.ParseInt(m[2], 10, 64)
			case "state":
				sp.State = strings.ToLower(m[2])
			case "name":
				sp.Name = m[2]
			case "startDate":
				sp.StartDate = m[2]
			case "endDate":
				sp.EndDate = m[2]
			case "completeDate":
				sp.CompleteDate = m[2]
			case "goal":
				sp.Goal = m[2]
			case "rapidViewId":
				sp.RapidViewID, _ = strconv.ParseInt(m[2], 10, 64)
			}
		}
		if sp.ID != 0 {
			out = append(out, sp)
		}
	}
	return out
}

func sprintState(s string) string {
	switch strings.ToLower(s) {
	case "active":
		return issues.SprintActive
	case "closed":
		return issues.SprintClosed
	}
	return issues.SprintFuture
}

func categoryOf(key string) string {
	switch key {
	case "indeterminate":
		return issues.CategoryInProgress
	case "done":
		return issues.CategoryDone
	}
	return issues.CategoryTodo
}

// Load reads the work directory and loads the projects. Loads can be repeated: entities loaded before are updated
// (delta loads before the cut-over).
func (j *Jira) Load(ctx context.Context, l Loader) (*Report, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := j.work.Read(&server, "jira", "serverInfo.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Jira extraction", j.work.Root)
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, j.opt.URL), "/")
	ld := &jiraLoad{Jira: j, l: l, source: "jira:" + base, base: base, types: map[string]int64{}, statuses: map[string]int64{},
		priorities: map[string]int64{}, resolutions: map[string]int64{}, linkTypes: map[string]int64{}, custom: map[string]*fieldMapping{},
		customByName: map[string]*fieldMapping{}, groups: map[string]int64{}, jr: &journal{}}
	ld.rep = newReport(ld.source)
	ld.us = newUsers(ld.rep, ld.jr)
	ld.f = l.failures(ld.rep, ld.jr)
	if err := ld.config(ctx); err != nil {
		return ld.rep, fmt.Errorf("configuration: %w", err)
	}
	keys := j.selected()
	if len(keys) == 0 {
		keys = j.work.List("jira", "projects")
	}
	var err error
	for _, key := range keys {
		j.log.Info("loading project", "project", key)
		sum, perr := ld.project(ctx, key)
		if perr != nil {
			// A project that cannot be created (its key is taken, say) is reported; the others are loaded.
			if err = ld.f.record("project", key, key, perr); err != nil {
				break
			}
		}
		if sum == nil {
			continue
		}
		ld.rep.Projects = append(ld.rep.Projects, *sum)
		ld.rep.Counts["projects"]++
		ld.rep.Counts["issues"] += sum.Issues
		ld.rep.Counts["comments"] += sum.Comments
		ld.rep.Counts["attachments"] += sum.Attachments
		ld.rep.Counts["worklogs"] += sum.Worklogs
		ld.rep.Counts["sprints"] += sum.Sprints
	}
	ld.rep.Counts["users"] = len(ld.rep.UsersMatched) + len(ld.rep.Placeholders)
	if werr := ld.rep.Write(j.work, "jira-report.json"); err == nil {
		err = werr
	}
	return ld.rep, err
}

// jiraLoad is the state of one load.
type jiraLoad struct {
	*Jira
	l      Loader
	source string
	base   string
	rep    *Report
	us     *users
	jr     *journal
	f      *failures

	types, statuses, priorities, resolutions, linkTypes map[string]int64
	custom                                              map[string]*fieldMapping // by Jira field id
	customByName                                        map[string]*fieldMapping // by lower-case name, for old change logs
	sprintField, epicField                              string
	groups                                              map[string]int64
	// The size of the attachment files loaded, and the time it took (for estimates of later loads).
	attachmentBytes int64
	attachmentTime  time.Duration
}

// group maps a group with its extracted members.
func (ld *jiraLoad) group(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	if id, ok := ld.groups[strings.ToLower(name)]; ok {
		return id, nil
	}
	id, kname, err := identity.ImportGroup(ctx, tx, name)
	if err != nil {
		return 0, err
	}
	ld.rep.groupRenamed(name, kname)
	var members []jUser
	if _, err := ld.work.Read(&members, "jira", "groups", url.PathEscape(name)+".json"); err != nil {
		return 0, err
	}
	for i := range members {
		if members[i].inactive() {
			continue // who left does not join; a reused user name would inherit the group otherwise (audit L6)
		}
		uid, err := ld.us.get(ctx, tx, &members[i])
		if err != nil {
			return 0, err
		}
		if err := identity.AddMember(ctx, tx, id, uid); err != nil {
			return 0, err
		}
	}
	ld.groups[strings.ToLower(name)] = id
	ld.rep.Counts["groups"]++
	ld.jr.add(func() {
		delete(ld.groups, strings.ToLower(name))
		ld.rep.Counts["groups"]--
	})
	return id, nil
}

// access loads the project roles and the permission scheme of a project.
func (pl *projectLoad) access(ctx context.Context, tx pgx.Tx, im *issues.Importer) error {
	var roles []jRole
	if _, err := pl.work.Read(&roles, "jira", "projects", pl.key, "roles.json"); err != nil {
		return err
	}
	for _, r := range roles {
		rid, err := im.ProjectRole(r.Name, r.Description)
		if err != nil {
			return err
		}
		for _, a := range r.Actors {
			switch a.Type {
			case "atlassian-user-role-actor":
				uid, err := pl.us.get(ctx, tx, &jUser{Name: a.Name, DisplayName: a.DisplayName})
				if err == nil {
					err = im.RoleMember(pl.projectID, rid, "user", uid)
				}
				if err != nil {
					return err
				}
			case "atlassian-group-role-actor":
				gid, err := pl.group(ctx, tx, a.Name)
				if err == nil {
					err = im.RoleMember(pl.projectID, rid, "group", gid)
				}
				if err != nil {
					return err
				}
			}
		}
	}
	var ps jPermissionScheme
	if ok, err := pl.work.Read(&ps, "jira", "projects", pl.key, "permissionscheme.json"); err != nil {
		return err
	} else if !ok {
		pl.rep.warn("%s: the permission scheme was not extracted; the project uses Kogge's default scheme", pl.key)
		return nil
	}
	var grants []issues.SchemeGrant
	for _, p := range ps.Permissions {
		perm, ok := jiraPermissions[p.Permission]
		if !ok {
			pl.rep.Unsupported["permission "+p.Permission]++
			continue
		}
		g := issues.SchemeGrant{Permission: perm}
		var err error
		switch h := p.Holder; h.Type {
		case "projectRole":
			name := h.Parameter
			if h.ProjectRole != nil {
				name = h.ProjectRole.Name
			}
			g.HolderType = "role"
			g.HolderID, err = im.ProjectRole(name, "")
		case "group":
			if h.Parameter == "" {
				g.HolderType = "anyone"
			} else {
				g.HolderType = "group"
				g.HolderID, err = pl.group(ctx, tx, h.Parameter)
			}
		case "user":
			u := h.User
			if u == nil {
				u = &jUser{Name: h.Parameter}
			}
			g.HolderType = "user"
			g.HolderID, err = pl.us.get(ctx, tx, u)
		case "reporter", "assignee":
			g.HolderType = h.Type
		case "projectLead":
			g.HolderType = "project-lead"
		case "applicationRole":
			g.HolderType = "logged-in"
		case "anyone":
			g.HolderType = "anyone"
		default:
			pl.rep.Unsupported["permission holder "+h.Type]++
			continue
		}
		if err != nil {
			return err
		}
		grants = append(grants, g)
	}
	// Bulk changes are a global permission in Jira, which is not migrated. In Kogge they are a project permission
	// that the default schemes grant to whoever may edit issues (a bulk change checks each issue's own permissions
	// as well): so do the migrated schemes.
	var bulk []issues.SchemeGrant
	for _, g := range grants {
		if g.Permission == issues.PermEdit {
			g.Permission = issues.PermBulkChange
			bulk = append(bulk, g)
		}
	}
	grants = append(grants, bulk...)
	_, err := im.PermissionScheme(pl.projectID, strconv.FormatInt(ps.ID, 10), ps.Name, ps.Description, grants)
	return err
}

// inTx runs fn in a transaction and applies cache invalidation locally after the commit. In-memory state follows
// the transaction's outcome.
func (ld *jiraLoad) inTx(ctx context.Context, fn func(tx pgx.Tx, im *issues.Importer) error) error {
	var im *issues.Importer
	err := ld.f.inTx(ctx, ld.l.Pool, func(tx pgx.Tx) error {
		im = ld.l.Issues.NewImporter(tx, ld.source)
		return fn(tx, im)
	})
	if err == nil {
		im.Done()
	}
	return err
}

// eachIssue runs fn for every issue of the extracted pages, a page per transaction and an issue per savepoint:
// an issue that fails is recorded, and the others of its page are loaded.
func (pl *projectLoad) eachIssue(ctx context.Context, what string, fn func(tx pgx.Tx, im *issues.Importer, is jIssue, i int) error) error {
	pages := pl.work.List("jira", "projects", pl.key, "issues")
	total := 0
	if len(pages) > 0 {
		var first struct {
			Total int `json:"total"`
		}
		if _, err := pl.work.Read(&first, "jira", "projects", pl.key, "issues", pages[0]); err != nil {
			return err
		}
		total = first.Total
	}
	p := newProgress(pl.log, what+" of "+pl.key, total)
	defer p.finish()
	n := 0
	for _, name := range pages {
		var page jSearch
		if _, err := pl.work.Read(&page, "jira", "projects", pl.key, "issues", name); err != nil {
			return err
		}
		err := pl.inTx(ctx, func(tx pgx.Tx, _ *issues.Importer) error {
			for _, is := range page.Issues {
				i := n
				n++
				ok, err := pl.f.entity(ctx, tx, "issue", is.ID, is.Key, func(sp pgx.Tx) error {
					return fn(sp, pl.l.Issues.NewImporter(sp, pl.source), is, i)
				})
				if err != nil {
					return err
				}
				if ok {
					p.add(1)
				} else {
					p.fail()
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// config loads issue types, statuses, priorities, resolutions, link types and custom fields.
func (ld *jiraLoad) config(ctx context.Context) error {
	var types []jIssueType
	var statuses []jStatus
	var prios, resolutions []jRef
	var links struct {
		IssueLinkTypes []jLinkType `json:"issueLinkTypes"`
	}
	var fields []jField
	for _, f := range []struct {
		v    any
		name string
	}{{&types, "issuetypes.json"}, {&statuses, "statuses.json"}, {&prios, "priorities.json"}, {&resolutions, "resolutions.json"},
		{&links, "linktypes.json"}, {&fields, "fields.json"}} {
		if _, err := ld.work.Read(f.v, "jira", f.name); err != nil {
			return err
		}
	}
	return ld.inTx(ctx, func(_ pgx.Tx, im *issues.Importer) error {
		var err error
		for _, t := range types {
			if ld.types[t.ID], err = im.IssueType(t.ID, t.Name, t.Description, t.Subtask); err != nil {
				return err
			}
		}
		for _, s := range statuses {
			if ld.statuses[s.ID], err = im.Status(s.ID, s.Name, categoryOf(s.StatusCategory.Key)); err != nil {
				return err
			}
		}
		for _, p := range prios {
			if ld.priorities[p.ID], err = im.Priority(p.ID, p.Name); err != nil {
				return err
			}
		}
		for _, r := range resolutions {
			if ld.resolutions[r.ID], err = im.Resolution(r.ID, r.Name); err != nil {
				return err
			}
		}
		for _, lt := range links.IssueLinkTypes {
			if ld.linkTypes[lt.ID], err = im.LinkType(lt.ID, lt.Name, lt.Outward, lt.Inward); err != nil {
				return err
			}
		}
		for _, f := range fields {
			if !f.Custom {
				continue
			}
			kind := f.Schema.Custom
			switch {
			case strings.HasSuffix(kind, ":gh-sprint"):
				ld.sprintField = f.ID
			case strings.HasSuffix(kind, ":gh-epic-link"):
				ld.epicField = f.ID
			case strings.HasPrefix(kind, "com.pyxis.greenhopper.jira:"):
				// Rank, epic name, colour and status: Kogge keeps the order and epics in the issues themselves.
			default:
				typ, ok := jiraFieldTypes[kind]
				if !ok {
					ld.rep.Unsupported["custom field "+f.Name+" ("+kind+")"]++
					continue
				}
				key, opts, err := im.CustomField(f.ID, f.Name, typ, nil)
				if err != nil {
					return err
				}
				m := &fieldMapping{key: key, name: f.Name, typ: typ, options: opts}
				ld.custom[f.ID] = m
				ld.customByName[strings.ToLower(f.Name)] = m
			}
		}
		return nil
	})
}

// projectLoad is the state of loading one project.
type projectLoad struct {
	*jiraLoad
	key        string
	projectID  int64
	boardID    int64
	components map[string]int64
	versions   map[string]int64
	sprints    map[int64]int64 // by Jira sprint id
	boards     map[int64]int64 // by Jira board id
	levels     map[string]int64
	issues     map[string]int64 // by key
	sum        *ProjectSummary
}

func (ld *jiraLoad) project(ctx context.Context, key string) (*ProjectSummary, error) {
	pl := &projectLoad{jiraLoad: ld, key: key, components: map[string]int64{}, versions: map[string]int64{}, sprints: map[int64]int64{},
		boards: map[int64]int64{},
		levels: map[string]int64{}, issues: map[string]int64{}, sum: &ProjectSummary{Key: key}}
	var p jProject
	if ok, err := ld.work.Read(&p, "jira", "projects", key, "project.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.New("the project was not extracted")
	}
	var ps jProjectStatuses
	if _, err := ld.work.Read(&ps, "jira", "projects", key, "statuses.json"); err != nil {
		return nil, err
	}
	var boards jBoards
	if _, err := ld.work.Read(&boards, "jira", "projects", key, "boards.json"); err != nil {
		return nil, err
	}
	err := ld.inTx(ctx, func(tx pgx.Tx, im *issues.Importer) error {
		lead, err := ld.us.get(ctx, tx, p.Lead)
		if err != nil {
			return err
		}
		var types, statuses []int64
		for _, t := range p.IssueTypes {
			if id := ld.types[t.ID]; id != 0 && !slices.Contains(types, id) {
				types = append(types, id)
			}
		}
		for _, t := range ps {
			for _, s := range t.Statuses {
				if id := ld.statuses[s.ID]; id != 0 {
					statuses = append(statuses, id)
				}
			}
		}
		board := ""
		if p.ProjectTypeKey == "software" || len(boards.Values) > 0 {
			board = issues.BoardKanban
		}
		for _, b := range boards.Values {
			if b.Type == "scrum" {
				board = issues.BoardScrum
			}
		}
		category := ""
		if p.ProjectCategory != nil {
			category = p.ProjectCategory.Name
		}
		pl.projectID, err = im.Project(issues.ImportProject{SourceID: p.ID, Key: p.Key, Name: p.Name, Description: p.Description,
			Category: category, LeadID: lead, Business: p.ProjectTypeKey == "business", IssueTypes: types, Statuses: statuses, Board: board})
		if err != nil {
			return err
		}
		if pl.boardID, _, err = im.BoardOf(p.ID); err != nil {
			return err
		}
		if err := pl.access(ctx, tx, im); err != nil {
			return err
		}
		for _, c := range p.Components {
			cl, err := ld.us.get(ctx, tx, c.Lead)
			if err != nil {
				return err
			}
			if pl.components[c.ID], err = im.Component(pl.projectID, c.ID, c.Name, c.Description, cl); err != nil {
				return err
			}
		}
		for i, v := range p.Versions {
			if pl.versions[v.ID], err = im.Version(pl.projectID, v.ID, v.Name, v.Description, nonEmpty(v.StartDate), nonEmpty(v.ReleaseDate),
				v.Released, v.Archived, i+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Boards: the first one takes the board created with the project.
	if err := ld.inTx(ctx, func(_ pgx.Tx, im *issues.Importer) error { return pl.loadBoards(im, boards) }); err != nil {
		return nil, err
	}

	// Sprints of the Scrum boards, in the order of the source.
	var sprints []jSprint
	for _, name := range ld.work.List("jira", "projects", key, "sprints") {
		var page jPage[jSprint]
		if _, err := ld.work.Read(&page, "jira", "projects", key, "sprints", name); err != nil {
			return nil, err
		}
		sprints = append(sprints, page.Values...)
	}
	if err := ld.inTx(ctx, func(_ pgx.Tx, im *issues.Importer) error { return pl.loadSprints(im, sprints) }); err != nil {
		return nil, err
	}

	// Issues, page by page. The extraction is ordered by rank; the ranks are spread evenly over the key space.
	var ranks []string
	last := ""
	nextRank := func(i int) string {
		if i < len(ranks) {
			return ranks[i]
		}
		last = rank.After(cmp.Or(last, ranks[len(ranks)-1]))
		return last
	}
	if pages := ld.work.List("jira", "projects", key, "issues"); len(pages) > 0 {
		var first jSearch
		if _, err := ld.work.Read(&first, "jira", "projects", key, "issues", pages[0]); err != nil {
			return nil, err
		}
		ranks = rank.Spread(max(first.Total, len(first.Issues), 1))
	}
	started := time.Now()
	bytes0, spent0 := ld.attachmentBytes, ld.attachmentTime
	err = pl.eachIssue(ctx, "issues", func(tx pgx.Tx, im *issues.Importer, is jIssue, i int) error {
		id, err := pl.issue(ctx, tx, im, is, nextRank(i))
		if err != nil {
			return err
		}
		pl.issues[is.Key] = id
		pl.sum.Issues++
		ld.jr.add(func() {
			delete(pl.issues, is.Key)
			pl.sum.Issues--
		})
		return nil
	})
	if err != nil {
		return pl.sum, err
	}
	spent := ld.attachmentTime - spent0
	ld.rep.time("issues", pl.sum.Issues, 0, time.Since(started)-spent)
	ld.rep.time("attachments", 0, ld.attachmentBytes-bytes0, spent)

	// Second pass, now that every issue exists: parents, links, comments, work logs, watchers and history.
	started = time.Now()
	err = pl.eachIssue(ctx, "comments and history", func(tx pgx.Tx, im *issues.Importer, is jIssue, _ int) error {
		if _, ok := pl.issues[is.Key]; !ok {
			return nil // the issue itself failed
		}
		return pl.details(ctx, tx, im, is)
	})
	if err != nil {
		return pl.sum, err
	}
	ld.rep.time("issue-details", pl.sum.Issues, 0, time.Since(started))
	err = ld.inTx(ctx, func(_ pgx.Tx, im *issues.Importer) error { return im.Finish(ctx, pl.projectID) })
	return pl.sum, err
}

// loadBoards loads the boards of a project with their columns, filters, quick filters and estimation.
func (pl *projectLoad) loadBoards(im *issues.Importer, boards jBoards) error {
	into := pl.boardID
	for _, b := range boards.Values {
		var d jBoardDetails
		if _, err := pl.work.Read(&d, "jira", "projects", pl.key, "boards", fmt.Sprintf("board-%d.json", b.ID)); err != nil {
			return err
		}
		if d.Configuration == nil || len(d.Configuration.ColumnConfig.Columns) == 0 {
			// Without its configuration, the board is represented by the project's board.
			if pl.boardID != 0 {
				if err := im.MapBoard(strconv.FormatInt(b.ID, 10), pl.boardID); err != nil {
					return err
				}
				pl.boards[b.ID] = pl.boardID
			}
			pl.rep.warn("%s: the configuration of board %q was not extracted; it is represented by the project's board", pl.key, b.Name)
			continue
		}
		in := issues.ImportBoard{SourceID: strconv.FormatInt(b.ID, 10), ProjectID: pl.projectID, Name: cmp.Or(d.Configuration.Name, b.Name),
			Type: d.Configuration.Type, Query: "project = " + pl.key + " ORDER BY rank"}
		if d.Filter != nil {
			if q, ok := pl.kql(im, d.Filter.JQL); ok {
				in.Query = q
			} else {
				pl.rep.warn("%s: the filter of board %q could not be translated (%s); the board shows the project", pl.key, in.Name, d.Filter.JQL)
			}
		}
		seen := map[int64]bool{}
		limits := strings.HasPrefix(d.Configuration.ColumnConfig.ConstraintType, "issueCount")
		for _, c := range d.Configuration.ColumnConfig.Columns {
			col := issues.BoardColumn{Name: c.Name, Statuses: []int64{}}
			for _, st := range c.Statuses {
				if id := pl.statuses[st.ID]; id != 0 && !seen[id] {
					col.Statuses = append(col.Statuses, id)
					seen[id] = true
				}
			}
			if limits {
				col.Min, col.Max = c.Min, c.Max
			}
			in.Config.Columns = append(in.Config.Columns, col)
		}
		if e := d.Configuration.Estimation; e != nil && e.Type == "field" && e.Field != nil {
			switch e.Field.FieldID {
			case "timeoriginalestimate":
				in.Config.Estimation = issues.Estimation{Type: "originalEstimate"}
			default:
				if m, ok := pl.custom[e.Field.FieldID]; ok && m.typ == "number" {
					in.Config.Estimation = issues.Estimation{Type: "field", Field: m.key}
				}
			}
		}
		if sq := d.Configuration.SubQuery; sq != nil && sq.Query != "" {
			if q, ok := pl.kql(im, sq.Query); ok {
				in.Config.SubQuery = q
			} else {
				pl.rep.warn("%s: the additional condition of board %q could not be translated (%s)", pl.key, in.Name, sq.Query)
			}
		}
		if d.QuickFilters != nil {
			for _, f := range d.QuickFilters.Values {
				if q, ok := pl.kql(im, f.JQL); ok {
					in.Config.QuickFilters = append(in.Config.QuickFilters, issues.NamedQuery{ID: len(in.Config.QuickFilters) + 1, Name: f.Name, Query: q})
				} else {
					pl.rep.warn("%s: the quick filter %q of board %q could not be translated (%s)", pl.key, f.Name, in.Name, f.JQL)
				}
			}
		}
		if in.Type == issues.BoardKanban {
			in.Config.HideDoneAfterDays = 14
		}
		id, err := im.Board(in, into)
		if err != nil {
			return fmt.Errorf("board %q: %w", in.Name, err)
		}
		pl.boards[b.ID] = id
		if into == pl.boardID {
			into = 0
		}
		pl.rep.Counts["boards"]++
	}
	return nil
}

var cfPattern = regexp.MustCompile(`(?i)\bcf\[(\d+)\]`)

// kql translates a query of the source into KQL: custom fields referenced by id get their Kogge ids; the rest of
// the syntax is shared. The result is checked with the KQL parser.
func (pl *projectLoad) kql(im *issues.Importer, jql string) (string, bool) {
	ok := true
	q := cfPattern.ReplaceAllStringFunc(jql, func(m string) string {
		id := cfPattern.FindStringSubmatch(m)[1]
		key, found, err := im.FieldKeyOf("customfield_" + id)
		if err != nil || !found {
			ok = false
			return m
		}
		return "cf[" + strings.TrimPrefix(key, "cf_") + "]"
	})
	if !ok {
		return "", false
	}
	if _, err := kql.Parse(q); err != nil {
		return "", false
	}
	return q, true
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// loadSprints maps sprints that were not mapped yet.
func (pl *projectLoad) loadSprints(im *issues.Importer, list []jSprint) error {
	for _, s := range list {
		if _, done := pl.sprints[s.ID]; done || s.ID == 0 {
			continue
		}
		board := pl.boardID
		if b, ok := pl.boards[cmp.Or(s.OriginBoardID, s.RapidViewID)]; ok {
			board = b
		}
		id, err := im.Sprint(board, strconv.FormatInt(s.ID, 10), s.Name, s.Goal, sprintState(s.State), parseTime(s.StartDate),
			parseTime(s.EndDate), parseTime(s.CompleteDate), len(pl.sprints)+1)
		if err != nil {
			return err
		}
		pl.sprints[s.ID] = id
		pl.sum.Sprints++
		pl.jr.add(func() {
			delete(pl.sprints, s.ID)
			pl.sum.Sprints--
		})
	}
	return nil
}

func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func refID(raw json.RawMessage) string {
	var r jRef
	if json.Unmarshal(raw, &r) == nil {
		return r.ID
	}
	return ""
}

func refIDs(raw json.RawMessage) []string {
	var rs []jRef
	_ = json.Unmarshal(raw, &rs)
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func int64Ptr(raw json.RawMessage) *int64 {
	var n *int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return nil
}

type attachmentRef struct {
	id       int64
	url      string
	filename string
}

// resolver resolves references in rich text: users, issue keys and the attachments of the issue.
func (pl *projectLoad) resolver(ctx context.Context, q db.Querier, attachments map[string]attachmentRef) docmodel.ImportResolver {
	return docmodel.ImportResolver{
		User: func(ref string) (int64, string) {
			id, err := pl.us.get(ctx, q, &jUser{Name: ref, DisplayName: ref})
			if err != nil || id == 0 {
				return 0, ""
			}
			return id, pl.us.label(ctx, q, id)
		},
		Link: func(href string) (string, string, int64, string) {
			rest, ok := strings.CutPrefix(href, pl.base)
			if !ok && strings.HasPrefix(href, "/") {
				rest, ok = href, true
			}
			if !ok {
				return "", "", 0, ""
			}
			if k, ok := strings.CutPrefix(rest, "/browse/"); ok && docmodel.IsIssueKey(k) {
				return "", k, 0, ""
			}
			for _, prefix := range []string{"/secure/attachment/", "/secure/thumbnail/"} {
				if a, ok := strings.CutPrefix(rest, prefix); ok {
					if ref, ok := attachments[strings.Split(a, "/")[0]]; ok {
						return ref.url, "", 0, ""
					}
				}
			}
			return "", "", 0, ""
		},
		Attachment: func(name, _, _ string) (int64, string) {
			for _, a := range attachments {
				if a.filename == name {
					return a.id, a.url
				}
			}
			return 0, ""
		},
	}
}

// attachments returns the attachments of an issue that were loaded.
func (pl *projectLoad) attachments(im *issues.Importer, f map[string]json.RawMessage) map[string]attachmentRef {
	var atts []jAttachment
	_ = json.Unmarshal(f["attachment"], &atts)
	refs := map[string]attachmentRef{}
	for _, a := range atts {
		if id, ok, _ := im.AttachmentID(a.ID); ok {
			refs[a.ID] = attachmentRef{id: id, url: "/api/v1/issue-attachments/" + strconv.FormatInt(id, 10), filename: a.Filename}
		}
	}
	return refs
}

// issue loads an issue with its fields, attachments, description and sprints.
func (pl *projectLoad) issue(ctx context.Context, tx pgx.Tx, im *issues.Importer, is jIssue, rk string) (int64, error) {
	f := is.Fields
	var reporter, creator, assignee *jUser
	_ = json.Unmarshal(f["reporter"], &reporter)
	_ = json.Unmarshal(f["creator"], &creator)
	_ = json.Unmarshal(f["assignee"], &assignee)
	var ids [3]int64
	for i, u := range []*jUser{reporter, creator, assignee} {
		id, err := pl.us.get(ctx, tx, u)
		if err != nil {
			return 0, err
		}
		ids[i] = id
	}
	created := timeOr(rawString(f["created"]), time.Now())
	in := issues.ImportIssue{SourceID: is.ID, Key: is.Key, ProjectID: pl.projectID, TypeID: pl.types[refID(f["issuetype"])],
		StatusID: pl.statuses[refID(f["status"])], PriorityID: pl.priorities[refID(f["priority"])],
		ResolutionID: pl.resolutions[refID(f["resolution"])], Summary: rawString(f["summary"]), Environment: rawString(f["environment"]),
		ReporterID: ids[0], CreatorID: ids[1], AssigneeID: ids[2], Created: created, Updated: timeOr(rawString(f["updated"]), created),
		Resolved: parseTime(rawString(f["resolutiondate"])), DueDate: nonEmpty(rawString(f["duedate"])),
		OriginalEstimate: int64Ptr(f["timeoriginalestimate"]), RemainingEstimate: int64Ptr(f["timeestimate"]), Rank: rk, Fields: map[string]any{}}
	if in.TypeID == 0 || in.StatusID == 0 {
		return 0, errors.New("the issue type or status was not extracted")
	}
	if spent := int64Ptr(f["timespent"]); spent != nil {
		in.TimeSpent = *spent
	}
	_ = json.Unmarshal(f["labels"], &in.Labels)
	for _, id := range refIDs(f["components"]) {
		if v := pl.components[id]; v != 0 {
			in.Components = append(in.Components, v)
		}
	}
	for _, id := range refIDs(f["fixVersions"]) {
		if v := pl.versions[id]; v != 0 {
			in.FixVersions = append(in.FixVersions, v)
		}
	}
	for _, id := range refIDs(f["versions"]) {
		if v := pl.versions[id]; v != 0 {
			in.AffectsVersions = append(in.AffectsVersions, v)
		}
	}
	// Issue security: restricted issues must stay restricted.
	var sec *struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(f["security"], &sec)
	if sec != nil && sec.ID != "" {
		level, ok := pl.levels[sec.ID]
		if !ok {
			var created bool
			var err error
			if level, created, err = im.SecurityLevel(pl.projectID, sec.ID, sec.Name, sec.Description); err != nil {
				return 0, err
			}
			if created {
				pl.rep.warn("%s: the security level %q was created for the project's Administrators role only; add its members in the "+
					"project's security scheme", pl.key, sec.Name)
			}
			pl.levels[sec.ID] = level
			pl.jr.add(func() { delete(pl.levels, sec.ID) })
		}
		in.SecurityLevelID = level
	}
	textResolver := pl.resolver(ctx, tx, nil)
	for fid, m := range pl.custom {
		raw, ok := f[fid]
		if !ok || string(raw) == "null" {
			continue
		}
		v, err := pl.customValue(ctx, tx, im, fid, m, raw, rawString(is.RenderedFields[fid]), textResolver)
		if err != nil {
			pl.rep.warn("%s: the value of %s was not migrated: %v", is.Key, m.name, err)
			continue
		}
		if v != nil {
			in.Fields[m.key] = v
		}
	}
	id, err := im.Issue(in)
	if err != nil {
		return 0, err
	}

	// Attachments before the description, so that it can show them.
	var atts []jAttachment
	_ = json.Unmarshal(f["attachment"], &atts)
	for _, a := range atts {
		if _, ok, err := im.AttachmentID(a.ID); err != nil {
			return 0, err
		} else if ok {
			continue
		}
		r, err := os.Open(pl.work.Path("jira", "attachments", a.ID))
		if err != nil {
			pl.rep.warn("%s: the attachment %s was not extracted", is.Key, a.Filename)
			continue
		}
		started := time.Now()
		author, err := pl.us.get(ctx, tx, a.Author)
		if err == nil {
			err = im.Attachment(ctx, id, a.ID, a.Filename, cmp.Or(a.MimeType, "application/octet-stream"), author, timeOr(a.Created, created), r)
		}
		if st, serr := r.Stat(); serr == nil {
			pl.attachmentBytes += st.Size()
		}
		pl.attachmentTime += time.Since(started)
		r.Close()
		if err != nil {
			return 0, fmt.Errorf("attachment %s (%s): %w", a.Filename, a.ID, err)
		}
	}
	if html := rawString(is.RenderedFields["description"]); html != "" {
		err = im.SetDescription(id, docmodel.FromHTML(html, pl.resolver(ctx, tx, pl.attachments(im, f))))
	} else if text := rawString(f["description"]); text != "" {
		err = im.SetDescription(id, docmodel.Paragraphs(text))
	}
	if err != nil {
		return 0, err
	}
	var votes struct {
		Votes int `json:"votes"`
	}
	_ = json.Unmarshal(f["votes"], &votes)
	if err := im.Votes(id, votes.Votes); err != nil {
		return 0, err
	}
	// Sprints (also of other boards: they are mapped on first sight).
	if pl.sprintField != "" {
		list := parseSprints(f[pl.sprintField])
		if err := pl.loadSprints(im, list); err != nil {
			return 0, err
		}
		var sprintIDs []int64
		for _, s := range list {
			sprintIDs = append(sprintIDs, pl.sprints[s.ID])
		}
		if err := im.IssueSprints(id, sprintIDs); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// customValue converts the value of a custom field.
func (pl *projectLoad) customValue(ctx context.Context, tx pgx.Tx, im *issues.Importer, fid string, m *fieldMapping, raw json.RawMessage,
	rendered string, r docmodel.ImportResolver) (any, error) {
	option := func(values ...string) ([]int64, error) {
		var missing []string
		for _, v := range values {
			if _, ok := m.options[v]; !ok && v != "" {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			_, opts, err := im.CustomField(fid, m.name, m.typ, missing)
			if err != nil {
				return nil, err
			}
			prev := m.options
			m.options = opts
			pl.jr.add(func() { m.options = prev })
		}
		ids := []int64{}
		for _, v := range values {
			if id := m.options[v]; id != 0 {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	switch m.typ {
	case "number":
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		return n, nil
	case "text", "url":
		return rawString(raw), nil
	case "textarea":
		if rendered != "" {
			return docmodel.FromHTML(rendered, r), nil
		}
		return docmodel.Paragraphs(rawString(raw)), nil
	case "date":
		if s := rawString(raw); len(s) >= 10 {
			return s[:10], nil
		}
	case "datetime":
		if t := parseTime(rawString(raw)); t != nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	case "labels":
		var ls []string
		err := json.Unmarshal(raw, &ls)
		return ls, err
	case "select", "radio":
		var o struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &o); err != nil || o.Value == "" {
			return nil, err
		}
		ids, err := option(o.Value)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		return ids[0], nil
	case "multiselect", "checkboxes":
		var os []struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &os); err != nil {
			return nil, err
		}
		values := make([]string, 0, len(os))
		for _, o := range os {
			values = append(values, o.Value)
		}
		ids, err := option(values...)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		return ids, nil
	case "user":
		var u jUser
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, err
		}
		id, err := pl.us.get(ctx, tx, &u)
		if err != nil || id == 0 {
			return nil, err
		}
		return id, nil
	case "multiuser":
		var list []jUser
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		ids := []int64{}
		for i := range list {
			id, err := pl.us.get(ctx, tx, &list[i])
			if err != nil {
				return nil, err
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	return nil, nil
}

// details loads what refers to other issues or follows the issue: parent, links, comments, work logs, watchers and
// the history.
func (pl *projectLoad) details(ctx context.Context, tx pgx.Tx, im *issues.Importer, is jIssue) error {
	f := is.Fields
	id := pl.issues[is.Key]
	byKey := func(key string) (int64, bool) {
		if id, ok := pl.issues[key]; ok {
			return id, true
		}
		id, ok, _ := im.IssueByKey(key)
		return id, ok
	}
	// The parent of a sub-task, or the epic of an issue.
	var parent struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(f["parent"], &parent)
	if parent.Key == "" && pl.epicField != "" {
		parent.Key = rawString(f[pl.epicField])
	}
	if parent.Key != "" {
		if pid, ok := byKey(parent.Key); ok {
			if err := im.SetParent(id, pid); err != nil {
				return err
			}
		} else {
			pl.rep.warn("%s: the parent %s was not migrated", is.Key, parent.Key)
		}
	}
	// Links: every link is listed on both issues; the outward side creates it.
	var links []struct {
		Type struct {
			ID string `json:"id"`
		} `json:"type"`
		OutwardIssue *struct {
			Key string `json:"key"`
		} `json:"outwardIssue"`
	}
	_ = json.Unmarshal(f["issuelinks"], &links)
	for _, lk := range links {
		if lk.OutwardIssue == nil {
			continue
		}
		target, ok := byKey(lk.OutwardIssue.Key)
		if !ok || pl.linkTypes[lk.Type.ID] == 0 {
			pl.rep.warn("%s: the link to %s was skipped, the issue was not migrated", is.Key, lk.OutwardIssue.Key)
			continue
		}
		if err := im.Link(pl.linkTypes[lk.Type.ID], id, target, 0, timeOr(rawString(f["updated"]), time.Now())); err != nil {
			return err
		}
	}
	refs := pl.attachments(im, f)
	pl.sum.Attachments += len(refs)
	resolver := pl.resolver(ctx, tx, refs)
	// Comments: rendered HTML where available.
	var comments struct {
		Comments []jComment `json:"comments"`
	}
	if ok, err := pl.work.Read(&comments, "jira", "projects", pl.key, "comments", is.Key+".json"); err != nil {
		return err
	} else if !ok {
		_ = json.Unmarshal(f["comment"], &comments)
	}
	var rendered struct {
		Comments []jComment `json:"comments"`
	}
	renderedBody := map[string]string{}
	if json.Unmarshal(is.RenderedFields["comment"], &rendered) == nil {
		for _, c := range rendered.Comments {
			renderedBody[c.ID] = c.Body
		}
	}
	for _, c := range comments.Comments {
		author, err := pl.us.get(ctx, tx, c.Author)
		if err != nil {
			return err
		}
		body := docmodel.Paragraphs(c.Body)
		if html := cmp.Or(c.RenderedBody, renderedBody[c.ID]); html != "" {
			body = docmodel.FromHTML(html, resolver)
		}
		var vis *issues.Visibility
		if c.Visibility != nil {
			v, found, err := im.CommentVisibility(c.Visibility.Type, c.Visibility.Value)
			if err != nil {
				return err
			}
			if !found {
				pl.rep.warn("%s: a comment is restricted to the %s %q, which does not exist in Kogge; it is visible to the Administrators role",
					is.Key, c.Visibility.Type, c.Visibility.Value)
			}
			vis = v
		}
		created := timeOr(c.Created, time.Now())
		if err := im.Comment(id, c.ID, author, created, timeOr(c.Updated, created), body, vis); err != nil {
			return err
		}
		pl.sum.Comments++
	}
	// Work logs.
	var worklogs struct {
		Worklogs []jWorklog `json:"worklogs"`
	}
	if ok, err := pl.work.Read(&worklogs, "jira", "projects", pl.key, "worklogs", is.Key+".json"); err != nil {
		return err
	} else if !ok {
		_ = json.Unmarshal(f["worklog"], &worklogs)
	}
	for _, w := range worklogs.Worklogs {
		author, err := pl.us.get(ctx, tx, w.Author)
		if err != nil {
			return err
		}
		started := timeOr(w.Started, time.Now())
		if err := im.Worklog(id, w.ID, author, started, w.TimeSpentSeconds, w.Comment, timeOr(w.Created, started)); err != nil {
			return err
		}
		pl.sum.Worklogs++
	}
	// Watchers.
	var watchers struct {
		Watchers []jUser `json:"watchers"`
	}
	if ok, err := pl.work.Read(&watchers, "jira", "projects", pl.key, "watchers", is.Key+".json"); err != nil {
		return err
	} else if ok {
		var ids []int64
		for i := range watchers.Watchers {
			uid, err := pl.us.get(ctx, tx, &watchers.Watchers[i])
			if err != nil {
				return err
			}
			if uid != 0 {
				ids = append(ids, uid)
			}
		}
		if err := im.Watchers(id, ids); err != nil {
			return err
		}
	}
	// History, so that the history tab and the reports cover the time before the migration.
	if changelog, err := pl.changelog(is); err != nil {
		return err
	} else if changelog != nil {
		for _, h := range changelog.Histories {
			actor, err := pl.us.get(ctx, tx, h.Author)
			if err != nil {
				return err
			}
			var items []issues.ChangeItem
			for _, it := range h.Items {
				if item, ok := pl.historyItem(it); ok {
					items = append(items, item)
				}
			}
			if err := im.History(id, h.ID, actor, timeOr(h.Created, time.Now()), items); err != nil {
				return err
			}
		}
	}
	return im.Spans(id)
}

// jiraChangelog returns the history of an issue: the one the search listed, or the complete one the extraction
// read where the search cut it off.
func (w Workdir) jiraChangelog(project string, is jIssue) (*jChangelog, error) {
	var full struct {
		Changelog *jChangelog `json:"changelog"`
	}
	if ok, err := w.Read(&full, "jira", "projects", project, "changelogs", is.Key+".json"); err != nil {
		return nil, err
	} else if ok && full.Changelog != nil {
		return full.Changelog, nil
	}
	return is.Changelog, nil
}

func (pl *projectLoad) changelog(is jIssue) (*jChangelog, error) {
	return pl.work.jiraChangelog(pl.key, is)
}

// historyItem maps an item of Jira's change log; ok is false for fields Kogge does not record.
func (pl *projectLoad) historyItem(it jChangeItem) (issues.ChangeItem, bool) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	item := issues.ChangeItem{FromString: str(it.FromString), ToString: str(it.ToString)}
	mapped := func(m map[string]int64) {
		get := func(p *string) any {
			if p == nil {
				return nil
			}
			if id, ok := m[*p]; ok {
				return id
			}
			return nil
		}
		item.From, item.To = get(it.From), get(it.To)
	}
	user := func(p *string) any {
		if p == nil {
			return nil
		}
		if id := pl.us.lookup(*p); id != 0 {
			return id
		}
		return nil
	}
	switch strings.ToLower(it.Field) {
	case "status":
		item.Field = issues.FieldStatus
		mapped(pl.statuses)
	case "priority":
		item.Field = issues.FieldPriority
		mapped(pl.priorities)
	case "resolution":
		item.Field = issues.FieldResolution
		mapped(pl.resolutions)
	case "issuetype":
		item.Field = issues.FieldIssueType
		mapped(pl.types)
	case "assignee":
		item.Field = issues.FieldAssignee
		item.From, item.To = user(it.From), user(it.To)
	case "reporter":
		item.Field = issues.FieldReporter
		item.From, item.To = user(it.From), user(it.To)
	case "summary":
		item.Field = issues.FieldSummary
	case "description":
		item.Field = issues.FieldDescription
	case "environment":
		item.Field = issues.FieldEnvironment
	case "labels":
		item.Field = issues.FieldLabels
	case "component":
		item.Field = issues.FieldComponents
		mapped(pl.components)
	case "fix version":
		item.Field = issues.FieldFixVersions
		mapped(pl.versions)
	case "version":
		item.Field = issues.FieldAffectsVersions
		mapped(pl.versions)
	case "duedate":
		item.Field = issues.FieldDueDate
	case "timeoriginalestimate":
		item.Field = issues.FieldOriginalEstimate
		item.From, item.To = parseIntPtr(it.From), parseIntPtr(it.To)
	case "timeestimate":
		item.Field = issues.FieldRemainingEstimate
		item.From, item.To = parseIntPtr(it.From), parseIntPtr(it.To)
	case "sprint":
		// Jira lists all sprints of the issue; the last one is the current.
		last := func(p *string) any {
			if p == nil || strings.TrimSpace(*p) == "" {
				return nil
			}
			parts := strings.Split(*p, ",")
			n, err := strconv.ParseInt(strings.TrimSpace(parts[len(parts)-1]), 10, 64)
			if id, ok := pl.sprints[n]; err == nil && ok {
				return id
			}
			return nil
		}
		item.Field = issues.FieldSprint
		item.From, item.To = last(it.From), last(it.To)
	case "parent", "parent issue", "epic link":
		item.Field = issues.FieldParent
	case "link":
		item.Field = "link"
	case "attachment":
		item.Field = "attachment"
	default:
		m, ok := pl.custom[it.FieldID]
		if !ok && it.FieldType == "custom" {
			m, ok = pl.customByName[strings.ToLower(it.Field)]
		}
		if !ok {
			return item, false
		}
		item.Field, item.Name = m.key, m.name
		if m.typ == "number" {
			item.From, item.To = parseFloatPtr(it.FromString), parseFloatPtr(it.ToString)
		}
	}
	return item, true
}

func parseIntPtr(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	n, err := strconv.ParseInt(*p, 10, 64)
	if err != nil {
		return nil
	}
	return n
}

func parseFloatPtr(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	n, err := strconv.ParseFloat(*p, 64)
	if err != nil {
		return nil
	}
	return n
}
