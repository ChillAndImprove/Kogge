// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"kogge.de/kogge/internal/issues/kql"
)

// The analysis of a Jira extraction: projects, issues and what they hold, the configuration (issue types,
// statuses, custom fields, workflows, permission, notification and security schemes, roles, boards) and how the
// load translates it.

// Analyse reads the extraction (and the target installation, if given) and writes jira-analysis.json and
// jira-analysis.html into the work directory.
func (j *Jira) Analyse(ctx context.Context, target *Target, language string) (*Analysis, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
		Version string `json:"version"`
	}
	if ok, err := j.work.Read(&server, "jira", "serverInfo.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Jira extraction", j.work.Root)
	}
	a, err := newAnalysis(ctx, "jira", strings.TrimRight(cmp.Or(server.BaseURL, j.opt.URL), "/"), j.work, language, target)
	if err != nil {
		return nil, err
	}
	ja := &jiraAnalysis{analysis: a, j: j, custom: map[string]jField{}, levels: map[string]int{}, subtasks: map[string]bool{},
		groups: map[string]int{}}
	if err := ja.run(); err != nil {
		return nil, err
	}
	return a.Analysis, a.finish()
}

type jiraAnalysis struct {
	*analysis
	j      *Jira
	custom map[string]jField // custom fields that are not migrated, by id
	levels map[string]int    // issues by project and security level name
	groups map[string]int    // groups the access names
	bytes  int64             // of attachments, as the source lists them
	// subtasks are the issue types for sub-tasks, by id.
	subtasks map[string]bool
}

func (ja *jiraAnalysis) run() error {
	l := ja.l
	keys := ja.j.selected()
	if len(keys) == 0 {
		keys = ja.work.List("jira", "projects")
	}
	if err := ja.configuration(); err != nil {
		return err
	}
	counts := map[string]int{}
	var attachmentFiles, missingFiles int
	var fileBytes int64
	projects := Table{ID: "projects", Title: l.t("Projects"), Columns: []string{l.t("Project"), l.t("Type"), l.t("Issues"), l.t("Lead"), l.t("In Kogge")}}
	for _, key := range keys {
		var p jProject
		if ok, err := ja.work.Read(&p, "jira", "projects", key, "project.json"); err != nil {
			return err
		} else if !ok {
			ja.decide("project-not-extracted", "high", l.t("The project %s was selected but not extracted: extract it again.", key))
			continue
		}
		counts["projects"]++
		counts["components"] += len(p.Components)
		counts["versions"] += len(p.Versions)
		if p.Lead != nil {
			ja.person(p.Lead.Name, p.Lead.DisplayName, p.Lead.EmailAddress, p.Lead.Active)
		}
		for _, c := range p.Components {
			if c.Lead != nil {
				ja.person(c.Lead.Name, c.Lead.DisplayName, c.Lead.EmailAddress, c.Lead.Active)
			}
		}
		n, st, err := ja.project(key, counts)
		if err != nil {
			return fmt.Errorf("project %s: %w", key, err)
		}
		attachmentFiles += st.files
		missingFiles += st.missing
		fileBytes += st.fileBytes
		kogge, class, err := ja.projectTarget(p)
		if err != nil {
			return err
		}
		lead := ""
		if p.Lead != nil {
			lead = cmp.Or(p.Lead.DisplayName, p.Lead.Name)
		}
		projects.add(class, 0, keyed(p.Name, p.Key), projectType(l, p.ProjectTypeKey), l.n(int64(n)), lead, kogge)
		if p.ProjectTypeKey == "service_desk" {
			ja.decide("service-desk", "medium", l.t("%s is a Jira Service Management project: requests are migrated as issues, but queues, SLAs, request types, customers and organisations are not.", p.Key))
		}
		if err := ja.access(key); err != nil {
			return err
		}
		if err := ja.schemes(key, p); err != nil {
			return err
		}
		boards, err := ja.boards(key)
		if err != nil {
			return err
		}
		counts["boards"] += boards
	}
	ja.Translation = append([]Table{projects}, ja.Translation...)
	if err := ja.groupTable(); err != nil {
		return err
	}
	for _, k := range []struct{ key, label string }{{"projects", l.t("Projects")}, {"issues", l.t("Issues")}, {"comments", l.t("Comments")},
		{"attachments", l.t("Attachments")}, {"subtasks", l.t("Sub-tasks")}, {"restrictedComments", l.t("Comments with restricted visibility")},
		{"worklogs", l.t("Work logs")}, {"links", l.t("Issue links")}, {"history", l.t("History entries")}, {"watchers", l.t("Watchers")},
		{"sprints", l.t("Sprints")}, {"boards", l.t("Boards")}, {"versions", l.t("Versions")}, {"components", l.t("Components")}} {
		ja.count(k.key, k.label, counts[k.key])
	}
	ja.size("attachmentBytes", l.t("Attachments (as the source lists them)"), ja.bytes, true)
	ja.size("attachmentFiles", l.t("Attachment files extracted"), int64(attachmentFiles), false)
	ja.size("attachmentFileBytes", l.t("Size of the extracted attachment files"), fileBytes, true)
	if missingFiles > 0 {
		severity, msg := "medium", l.t("%d attachment files are missing from the work directory: the load leaves them out. Extract again (the extraction resumes) or check the extraction's notes.", missingFiles)
		if st := ja.Extraction; st != nil && st.Options["attachments"] == false {
			severity, msg = "low", l.t("Attachment files were not extracted (trial run): the load leaves the %d attachments out.", missingFiles)
		}
		ja.decide("attachments-missing", severity, msg)
	}
	if len(ja.levels) > 0 {
		var items []string
		for _, name := range slices.Sorted(mapKeys(ja.levels)) {
			items = append(items, fmt.Sprintf("%s (%d)", name, ja.levels[name]))
		}
		ja.decide("security-levels", "medium", l.t("Issues restricted by security levels stay restricted: each level is created with the project's Administrators role as its only member, because the source's API does not tell the members. Add them after the load."), items...)
	}
	if err := ja.matchUsers(); err != nil {
		return err
	}
	return ja.estimate("jira-report.json", phase{name: "issues", items: counts["issues"]}, phase{name: "issue-details", items: counts["issues"]},
		phase{name: "attachments", bytes: fileBytes})
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func projectType(l lang, t string) string {
	switch t {
	case "software":
		return l.t("software")
	case "business":
		return l.t("business")
	case "service_desk":
		return l.t("service management")
	}
	return t
}

// projectTarget says what becomes of a project in the target.
func (ja *jiraAnalysis) projectTarget(p jProject) (string, string, error) {
	l := ja.l
	if ja.target == nil {
		return l.t("a new project with the key %s", p.Key), "", nil
	}
	ours, loaded, err := ja.legacyEntity("project", p.ID)
	if err != nil {
		return "", "", err
	}
	id, err := issueProjectID(ja.ctx, ja.target.tx, p.Key)
	if err != nil {
		return "", "", err
	}
	switch {
	case loaded:
		return l.t("loaded before: is updated"), "", nil
	case id != 0 && id != ours:
		ja.decide("key-collision", "high", l.t("Kogge has a project with the key %s that was not created by this migration: the project cannot be loaded. Rename or delete the project in Kogge, or leave the project out.", p.Key))
		return l.t("the key is taken"), Fail, nil
	}
	return l.t("a new project with the key %s", p.Key), "", nil
}

// configuration lists the issue types, statuses, priorities, resolutions, link types and custom fields.
func (ja *jiraAnalysis) configuration() error {
	l := ja.l
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
		if _, err := ja.work.Read(f.v, "jira", f.name); err != nil {
			return err
		}
	}
	// The types under which the load records configuration in legacy_ids.
	recorded := map[string]string{"issue-type": "issuetype", "status": "status", "priority": "priority", "resolution": "resolution",
		"link-type": "linktype"}
	match := func(kind, srcID, name string) (string, error) {
		if ja.target == nil {
			return l.t("matched by name, or created"), nil
		}
		if _, ok, err := ja.legacyEntity(recorded[kind], srcID); err != nil {
			return "", err
		} else if ok {
			return l.t("loaded before"), nil
		}
		existing, err := configNames(ja.ctx, ja.target.tx, kind)
		if err != nil {
			return "", err
		}
		if existing[strings.ToLower(name)] {
			return l.t("matched to the existing one of the same name"), nil
		}
		return l.t("created"), nil
	}
	t := Table{ID: "issue-types", Title: l.t("Issue types"), Columns: []string{l.t("Issue type"), l.t("Sub-task"), l.t("In Kogge")}}
	for _, it := range types {
		ja.subtasks[it.ID] = it.Subtask
		m, err := match("issue-type", it.ID, it.Name)
		if err != nil {
			return err
		}
		t.add("", 0, it.Name, l.yesNo(it.Subtask), m)
	}
	ja.Translation = append(ja.Translation, t)
	t = Table{ID: "statuses", Title: l.t("Statuses"), Columns: []string{l.t("Status"), l.t("Category"), l.t("In Kogge")}}
	for _, s := range statuses {
		m, err := match("status", s.ID, s.Name)
		if err != nil {
			return err
		}
		t.add("", 0, s.Name, categoryLabel(l, categoryOf(s.StatusCategory.Key)), m)
	}
	ja.Translation = append(ja.Translation, t)
	for _, c := range []struct {
		kind, id, title string
		list            []jRef
	}{{"priority", "priorities", l.t("Priorities"), prios}, {"resolution", "resolutions", l.t("Resolutions"), resolutions}} {
		t := Table{ID: c.id, Title: c.title, Columns: []string{l.t("Name"), l.t("In Kogge")}}
		for _, r := range c.list {
			m, err := match(c.kind, r.ID, r.Name)
			if err != nil {
				return err
			}
			t.add("", 0, r.Name, m)
		}
		ja.Translation = append(ja.Translation, t)
	}
	t = Table{ID: "link-types", Title: l.t("Link types"), Columns: []string{l.t("Name"), l.t("Outward"), l.t("Inward"), l.t("In Kogge")}}
	for _, lt := range links.IssueLinkTypes {
		m, err := match("link-type", lt.ID, lt.Name)
		if err != nil {
			return err
		}
		t.add("", 0, lt.Name, lt.Outward, lt.Inward, m)
	}
	ja.Translation = append(ja.Translation, t)

	var existing map[string]bool
	if ja.target != nil {
		var err error
		if existing, err = customFieldNames(ja.ctx, ja.target.tx); err != nil {
			return err
		}
	}
	t = Table{ID: "custom-fields", Title: l.t("Custom fields"), Columns: []string{l.t("Field"), l.t("Type in the source"), l.t("In Kogge")}}
	for _, f := range fields {
		if !f.Custom {
			continue
		}
		kind := f.Schema.Custom
		var how, class string
		switch typ, ok := jiraFieldTypes[kind]; {
		case strings.HasSuffix(kind, ":gh-sprint"):
			how = l.t("the issues' sprints")
		case strings.HasSuffix(kind, ":gh-epic-link"):
			how = l.t("the issues' parent (epic)")
		case strings.HasPrefix(kind, "com.pyxis.greenhopper.jira:"):
			how = l.t("not needed: Kogge keeps rank and epics in the issues")
			class = "muted"
		case ok:
			how = l.t("a custom field of the type %s", typ)
			if existing != nil && existing[strings.ToLower(f.Name)+"\x00"+typ] {
				how = l.t("the existing custom field %q (%s)", f.Name, typ)
			}
		default:
			how, class = l.t("not migrated: the type is not supported"), Warn
			ja.custom[f.ID] = f
			note := l.t("the values are not migrated")
			if !strings.HasPrefix(kind, "com.atlassian.jira.") {
				note = l.t("data of an app: the values are not migrated")
			}
			// Listed also without values; the issues add their occurrences.
			ja.declare(l.t("custom field"), f.Name+" ("+kind+")", note)
		}
		t.add(class, 0, f.Name, kind, how)
	}
	ja.Translation = append(ja.Translation, t)
	return nil
}

func categoryLabel(l lang, c string) string {
	switch c {
	case "inprogress":
		return l.t("in progress")
	case "done":
		return l.t("done")
	}
	return l.t("to do")
}

// projectStats are what the issues of a project add to the sizes.
type projectStats struct {
	files, missing int
	fileBytes      int64
}

// project goes through the issues of a project, page by page.
func (ja *jiraAnalysis) project(key string, counts map[string]int) (int, projectStats, error) {
	var st projectStats
	n := 0
	visibility := map[string]int{}
	for _, name := range ja.work.List("jira", "projects", key, "issues") {
		var page jSearch
		if _, err := ja.work.Read(&page, "jira", "projects", key, "issues", name); err != nil {
			return 0, st, err
		}
		for _, is := range page.Issues {
			n++
			counts["issues"]++
			if err := ja.issue(key, is, counts, visibility, &st); err != nil {
				return 0, st, fmt.Errorf("%s: %w", is.Key, err)
			}
		}
	}
	for _, name := range ja.work.List("jira", "projects", key, "sprints") {
		var page jPage[jSprint]
		if _, err := ja.work.Read(&page, "jira", "projects", key, "sprints", name); err != nil {
			return 0, st, err
		}
		counts["sprints"] += len(page.Values)
	}
	if len(visibility) > 0 {
		var items []string
		for _, k := range slices.Sorted(mapKeys(visibility)) {
			items = append(items, fmt.Sprintf("%s (%d)", k, visibility[k]))
		}
		ja.decide("comment-visibility", "low", ja.l.t("%s: comments restricted to a role or group keep the restriction; restrictions to roles or groups Kogge does not have become restrictions to the project's Administrators role.", key), items...)
	}
	return n, st, nil
}

func (ja *jiraAnalysis) issue(project string, is jIssue, counts, visibility map[string]int, st *projectStats) error {
	f := is.Fields
	for _, k := range []string{"reporter", "creator", "assignee"} {
		var u *jUser
		if json.Unmarshal(f[k], &u) == nil && u != nil {
			ja.person(u.Name, u.DisplayName, u.EmailAddress, u.Active)
		}
	}
	var typ jIssueType
	_ = json.Unmarshal(f["issuetype"], &typ)
	if typ.Subtask || ja.subtasks[typ.ID] {
		counts["subtasks"]++
	}
	var comments struct {
		Comments []jComment `json:"comments"`
	}
	if ok, err := ja.work.Read(&comments, "jira", "projects", project, "comments", is.Key+".json"); err != nil {
		return err
	} else if !ok {
		_ = json.Unmarshal(f["comment"], &comments)
	}
	for _, c := range comments.Comments {
		counts["comments"]++
		if c.Author != nil {
			ja.person(c.Author.Name, c.Author.DisplayName, c.Author.EmailAddress, c.Author.Active)
		}
		if c.Visibility != nil {
			counts["restrictedComments"]++
			visibility[c.Visibility.Type+" "+c.Visibility.Value]++
		}
	}
	var worklogs struct {
		Worklogs []jWorklog `json:"worklogs"`
	}
	if ok, err := ja.work.Read(&worklogs, "jira", "projects", project, "worklogs", is.Key+".json"); err != nil {
		return err
	} else if !ok {
		_ = json.Unmarshal(f["worklog"], &worklogs)
	}
	counts["worklogs"] += len(worklogs.Worklogs)
	for _, w := range worklogs.Worklogs {
		if w.Author != nil {
			ja.person(w.Author.Name, w.Author.DisplayName, w.Author.EmailAddress, w.Author.Active)
		}
	}
	var watchers struct {
		Watchers []jUser `json:"watchers"`
	}
	if _, err := ja.work.Read(&watchers, "jira", "projects", project, "watchers", is.Key+".json"); err != nil {
		return err
	}
	counts["watchers"] += len(watchers.Watchers)
	for _, u := range watchers.Watchers {
		ja.person(u.Name, u.DisplayName, u.EmailAddress, u.Active)
	}
	var atts []jAttachment
	_ = json.Unmarshal(f["attachment"], &atts)
	for _, at := range atts {
		counts["attachments"]++
		ja.bytes += at.Size
		if at.Author != nil {
			ja.person(at.Author.Name, at.Author.DisplayName, at.Author.EmailAddress, at.Author.Active)
		}
		if size := ja.work.Size("jira", "attachments", at.ID); size >= 0 {
			st.files++
			st.fileBytes += size
		} else {
			st.missing++
		}
	}
	var links []struct {
		OutwardIssue *struct {
			Key string `json:"key"`
		} `json:"outwardIssue"`
	}
	_ = json.Unmarshal(f["issuelinks"], &links)
	for _, lk := range links {
		if lk.OutwardIssue != nil {
			counts["links"]++
		}
	}
	changelog, err := ja.work.jiraChangelog(project, is)
	if err != nil {
		return err
	}
	if changelog != nil {
		counts["history"] += len(changelog.Histories)
		for _, h := range changelog.Histories {
			if h.Author != nil {
				ja.person(h.Author.Name, h.Author.DisplayName, h.Author.EmailAddress, h.Author.Active)
			}
		}
	}
	var sec *struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(f["security"], &sec) == nil && sec != nil && sec.Name != "" {
		ja.levels[project+": "+sec.Name]++
	}
	for id, field := range ja.custom {
		if raw, ok := f[id]; ok && string(raw) != "null" && len(raw) > 0 && string(raw) != "[]" && string(raw) != `""` {
			ja.find(ja.l.t("custom field"), field.Name+" ("+field.Schema.Custom+")", is.Key, "")
		}
	}
	return nil
}

// access lists the project roles and the permission scheme of a project.
func (ja *jiraAnalysis) access(key string) error {
	l := ja.l
	var roles []jRole
	if _, err := ja.work.Read(&roles, "jira", "projects", key, "roles.json"); err != nil {
		return err
	}
	rt := Table{ID: "roles-" + key, Title: l.t("Project roles of %s", key), Columns: []string{l.t("Role"), l.t("Members"), l.t("In Kogge")}}
	for _, r := range roles {
		var members []string
		for _, a := range r.Actors {
			switch a.Type {
			case "atlassian-user-role-actor":
				ja.person(a.Name, a.DisplayName, "", nil)
				members = append(members, cmp.Or(a.DisplayName, a.Name))
			case "atlassian-group-role-actor":
				ja.groups[a.Name] += 0
				members = append(members, l.t("group %s", a.Name))
			}
		}
		how := l.t("a project role of the same name")
		switch strings.ToLower(r.Name) {
		case "administrators", "developers", "users":
			how = l.t("Kogge's built-in role")
		}
		rt.add("", 0, r.Name, strings.Join(members, ", "), how)
	}
	ja.Translation = append(ja.Translation, rt)
	var ps jPermissionScheme
	ok, err := ja.work.Read(&ps, "jira", "projects", key, "permissionscheme.json")
	if err != nil {
		return err
	}
	if !ok {
		ja.decide("no-permission-scheme", "medium", l.t("The permission scheme of %s was not extracted (the token needs administrator rights): the project gets Kogge's default scheme. Extract with an administrator's token, or set the permissions after the load.", key))
		return nil
	}
	byPerm := map[string][]string{}
	var order []string
	for _, p := range ps.Permissions {
		holder := ""
		switch h := p.Holder; h.Type {
		case "projectRole":
			name := h.Parameter
			if h.ProjectRole != nil {
				name = h.ProjectRole.Name
			}
			holder = l.t("role %s", name)
		case "group":
			if h.Parameter == "" {
				holder = l.t("anyone")
			} else {
				holder = l.t("group %s", h.Parameter)
				ja.groups[h.Parameter] += 0
			}
		case "user":
			name := h.Parameter
			if h.User != nil {
				name = h.User.Name
				ja.person(h.User.Name, h.User.DisplayName, h.User.EmailAddress, h.User.Active)
			}
			holder = l.t("user %s", name)
		case "reporter", "assignee", "projectLead", "applicationRole", "anyone":
			holder = map[string]string{"reporter": l.t("the reporter"), "assignee": l.t("the assignee"), "projectLead": l.t("the project lead"),
				"applicationRole": l.t("signed-in users"), "anyone": l.t("anyone")}[h.Type]
		default:
			holder = l.t("%s (not migrated)", h.Type)
			ja.find(l.t("permission holder"), h.Type, key+": "+p.Permission, l.t("the grant is left out"))
		}
		if _, ok := byPerm[p.Permission]; !ok {
			order = append(order, p.Permission)
		}
		byPerm[p.Permission] = append(byPerm[p.Permission], holder)
	}
	pt := Table{ID: "permissions-" + key, Title: l.t("Permission scheme of %s: %s", key, ps.Name),
		Intro:   l.t("The scheme is created as %q (with a suffix if the name is taken) and assigned to the project.", ps.Name),
		Columns: []string{l.t("Permission"), l.t("Holders"), l.t("In Kogge")}}
	for _, perm := range order {
		kogge, ok := jiraPermissions[perm]
		class := ""
		if !ok {
			kogge, class = l.t("not migrated"), Warn
			ja.find(l.t("permission"), perm, key, l.t("Kogge has no such permission; the grant is left out"))
		}
		pt.add(class, 0, perm, strings.Join(byPerm[perm], ", "), kogge)
	}
	ja.Translation = append(ja.Translation, pt)
	return nil
}

// jNotificationScheme is the notification scheme of a project.
type jNotificationScheme struct {
	Name   string `json:"name"`
	Events []struct {
		Event struct {
			Name string `json:"name"`
		} `json:"event"`
		Notifications []struct {
			Type      string `json:"notificationType"`
			Parameter string `json:"parameter"`
		} `json:"notifications"`
	} `json:"notificationSchemeEvents"`
}

// jSecurityScheme is the issue security scheme of a project.
type jSecurityScheme struct {
	Name   string `json:"name"`
	Levels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"levels"`
}

// schemes lists the notification scheme, security levels and workflows of a project.
func (ja *jiraAnalysis) schemes(key string, p jProject) error {
	l := ja.l
	var ns jNotificationScheme
	if ok, err := ja.work.Read(&ns, "jira", "projects", key, "notificationscheme.json"); err != nil {
		return err
	} else if ok {
		t := Table{ID: "notifications-" + key, Title: l.t("Notification scheme of %s: %s", key, ns.Name),
			Intro:   l.t("Not migrated: the project uses Kogge's default notification scheme. Set up what is needed in Kontor after the load."),
			Columns: []string{l.t("Event"), l.t("Recipients")}}
		for _, e := range ns.Events {
			var to []string
			for _, n := range e.Notifications {
				to = append(to, strings.TrimSpace(n.Type+" "+n.Parameter))
			}
			t.add("muted", 0, e.Event.Name, strings.Join(to, ", "))
		}
		ja.Translation = append(ja.Translation, t)
		ja.find(l.t("scheme"), l.t("notification scheme %s", ns.Name), key, l.t("not migrated: Kogge's default scheme applies"))
	}
	var ss jSecurityScheme
	if ok, err := ja.work.Read(&ss, "jira", "projects", key, "issuesecuritylevelscheme.json"); err != nil {
		return err
	} else if ok && len(ss.Levels) > 0 {
		t := Table{ID: "security-" + key, Title: l.t("Issue security of %s: %s", key, ss.Name),
			Columns: []string{l.t("Level"), l.t("Issues"), l.t("In Kogge")}}
		for _, lv := range ss.Levels {
			t.add("", 0, lv.Name, l.n(int64(ja.levels[key+": "+lv.Name])), l.t("a level of the scheme %q, for the Administrators role until members are added", key+" (imported)"))
		}
		ja.Translation = append(ja.Translation, t)
	}
	return ja.workflows(key, p)
}

// wfRule is a condition, validator or post-function of a workflow transition.
type wfRule struct {
	kind, class, action string
	scripted            bool
}

// readWorkflowXML lists the steps, transitions and rules of a workflow definition (OSWorkflow XML).
func readWorkflowXML(r io.Reader) (steps int, actions int, rules []wfRule, err error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var action string
	var rule *wfRule
	argName := ""
	seen := map[string]bool{}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return steps, actions, rules, nil
		}
		if err != nil {
			return 0, 0, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attr := func(name string) string {
				for _, a := range t.Attr {
					if a.Name.Local == name {
						return a.Value
					}
				}
				return ""
			}
			switch t.Name.Local {
			case "step":
				steps++
			case "action":
				action = attr("name")
				if id := attr("id"); id != "" && !seen[id] {
					seen[id] = true
					actions++
				}
			case "condition", "validator", "function":
				rule = &wfRule{kind: t.Name.Local, action: action, scripted: attr("type") == "beanshell"}
			case "arg":
				argName = attr("name")
			}
		case xml.CharData:
			if rule != nil && argName == "class.name" {
				rule.class += strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "arg":
				argName = ""
			case "condition", "validator", "function":
				if rule != nil {
					rules = append(rules, *rule)
					rule = nil
				}
			}
		}
	}
}

// Workflow rules of the source by class name, and their counterparts in Kogge's workflows ("" for rules that
// Kogge applies to every transition anyway).
var workflowCounterparts = map[string]string{
	"com.atlassian.jira.workflow.condition.PermissionCondition":                "permission",
	"com.atlassian.jira.workflow.condition.AllowOnlyAssignee":                  "assignee-only",
	"com.atlassian.jira.workflow.condition.AllowOnlyReporter":                  "reporter-only",
	"com.atlassian.jira.workflow.condition.InProjectRoleCondition":             "role",
	"com.atlassian.jira.workflow.condition.UserInGroupCondition":               "group",
	"com.atlassian.jira.workflow.condition.SubTaskBlockingCondition":           "subtasks-done",
	"com.atlassian.jira.workflow.validator.PermissionValidator":                "permission",
	"com.atlassian.jira.workflow.validator.UserPermissionValidator":            "permission",
	"com.atlassian.jira.workflow.function.issue.UpdateIssueFieldFunction":      "set-field",
	"com.atlassian.jira.workflow.function.issue.AssignToCurrentUserFunction":   "assign-to-current-user",
	"com.atlassian.jira.workflow.function.issue.AssignToReporterFunction":      "assign-to-reporter",
	"com.atlassian.jira.workflow.function.issue.AssignToLeadFunction":          "assign-to-lead",
	"com.atlassian.jira.workflow.function.issue.IssueCreateFunction":           "",
	"com.atlassian.jira.workflow.function.issue.UpdateIssueStatusFunction":     "",
	"com.atlassian.jira.workflow.function.misc.CreateCommentFunction":          "",
	"com.atlassian.jira.workflow.function.issue.GenerateChangeHistoryFunction": "",
	"com.atlassian.jira.workflow.function.issue.IssueReindexFunction":          "",
	"com.atlassian.jira.workflow.function.event.FireIssueEventFunction":        "",
	"com.atlassian.jira.workflow.function.issue.IssueStoreFunction":            "",
}

// scriptedRule reports rules that run scripts (of scripting apps, or inline).
func scriptedRule(r wfRule) bool {
	c := strings.ToLower(r.class)
	return r.scripted || strings.Contains(c, "groovy") || strings.Contains(c, "script") || strings.Contains(c, "onresolve") ||
		strings.Contains(c, "jmwe") || strings.Contains(c, "innovalog")
}

// workflows lists the workflows a project uses with their rules: the load does not take transitions over.
func (ja *jiraAnalysis) workflows(key string, p jProject) error {
	l := ja.l
	var ws jWorkflowScheme
	ok, err := ja.work.Read(&ws, "jira", "projects", key, "workflowscheme.json")
	if err != nil || !ok {
		return err
	}
	typeNames := map[string]string{}
	for _, it := range p.IssueTypes {
		typeNames[it.ID] = it.Name
	}
	usedBy := map[string][]string{}
	for typ, wf := range ws.IssueTypeMappings {
		usedBy[wf] = append(usedBy[wf], cmp.Or(typeNames[typ], typ))
	}
	if ws.DefaultWorkflow != "" {
		usedBy[ws.DefaultWorkflow] = append(usedBy[ws.DefaultWorkflow], l.t("all other issue types"))
	}
	t := Table{ID: "workflows-" + key, Title: l.t("Workflows of %s", key),
		Intro:   l.t("Kogge creates the workflow %q, in which every status can be reached from every other; the transitions and rules listed here are not taken over. Rebuild them in Kontor → Configuration → Workflows after the load: rules marked with a counterpart exist in Kogge.", key+" (imported)"),
		Columns: []string{l.t("Workflow"), l.t("Used by"), l.t("Steps"), l.t("Transitions"), l.t("Rules"), l.t("Without counterpart")}}
	var scripted []string
	for _, name := range slices.Sorted(mapKeys(usedBy)) {
		slices.Sort(usedBy[name])
		raw, err := os.ReadFile(ja.work.Path("jira", "workflows", name+".xml"))
		if errors.Is(err, os.ErrNotExist) {
			t.add(Warn, 0, name, strings.Join(usedBy[name], ", "), "?", "?", l.t("the definition could not be read"), "?")
			continue
		}
		if err != nil {
			return err
		}
		steps, actions, rules, err := readWorkflowXML(bytes.NewReader(raw))
		if err != nil {
			t.add(Warn, 0, name, strings.Join(usedBy[name], ", "), "?", "?", l.t("the definition could not be read: %s", err.Error()), "?")
			continue
		}
		without := 0
		for _, r := range rules {
			kind := map[string]string{"condition": l.t("workflow condition"), "validator": l.t("workflow validator"),
				"function": l.t("workflow post-function")}[r.kind]
			example := name + ": " + r.action
			counterpart, known := workflowCounterparts[r.class]
			switch {
			case scriptedRule(r):
				without++
				ja.find(kind, cmp.Or(r.class, "script"), example, l.t("a script: rebuild its logic by hand (or with an automation rule)"))
				scripted = append(scripted, example)
			case known && counterpart == "":
				// Kogge does this on every transition.
			case known:
				ja.find(kind, r.class, example, l.t("Kogge has the counterpart %q: add it to the transition", counterpart))
			case strings.HasPrefix(r.class, "com.atlassian."):
				without++
				ja.find(kind, r.class, example, l.t("no counterpart in Kogge"))
			default:
				without++
				ja.find(kind, r.class, example, l.t("of an app: no counterpart in Kogge"))
			}
		}
		class := ""
		if without > 0 {
			class = Warn
		}
		t.add(class, 0, name, strings.Join(usedBy[name], ", "), l.n(int64(steps)), l.n(int64(actions)), l.n(int64(len(rules))), l.n(int64(without)))
	}
	ja.Translation = append(ja.Translation, t)
	if len(scripted) > 0 {
		slices.Sort(scripted)
		ja.decide("scripted-workflows", "medium", l.t("Workflows of %s run scripts in transitions; their logic is not migrated and must be rebuilt.", key), slices.Compact(scripted)...)
	}
	return nil
}

// boards lists the boards with their filters: queries that do not parse as KQL make a board show the project.
func (ja *jiraAnalysis) boards(key string) (int, error) {
	l := ja.l
	var boards jBoards
	if _, err := ja.work.Read(&boards, "jira", "projects", key, "boards.json"); err != nil {
		return 0, err
	}
	if len(boards.Values) == 0 {
		return 0, nil
	}
	t := Table{ID: "boards-" + key, Title: l.t("Boards of %s", key), Columns: []string{l.t("Board"), l.t("Type"), l.t("Filter"), l.t("Quick filters"), l.t("In Kogge")}}
	translatable := func(jql string) bool {
		for _, m := range cfPattern.FindAllStringSubmatch(jql, -1) {
			if _, unsupported := ja.custom["customfield_"+m[1]]; unsupported {
				return false
			}
		}
		_, err := kql.Parse(jql)
		return err == nil
	}
	for _, b := range boards.Values {
		var d jBoardDetails
		if _, err := ja.work.Read(&d, "jira", "projects", key, "boards", fmt.Sprintf("board-%d.json", b.ID)); err != nil {
			return 0, err
		}
		filter, how, class := "", l.t("a board with the same columns, filter and quick filters"), ""
		if d.Filter != nil {
			filter = d.Filter.JQL
			if !translatable(d.Filter.JQL) {
				how, class = l.t("the filter is not translatable: the board shows the whole project"), Warn
				ja.find(l.t("board filter"), b.Name, d.Filter.JQL, l.t("the board shows the whole project; edit its query"))
			}
		}
		if d.Configuration == nil {
			how, class = l.t("the configuration was not extracted: represented by the project's board"), Warn
		}
		quick, ok := 0, 0
		if d.QuickFilters != nil {
			for _, q := range d.QuickFilters.Values {
				quick++
				if translatable(q.JQL) {
					ok++
				} else {
					ja.find(l.t("quick filter"), b.Name+": "+q.Name, q.JQL, l.t("left out; add it again"))
				}
			}
		}
		t.add(class, 0, b.Name, b.Type, filter, l.t("%d of %d translatable", ok, quick), how)
	}
	ja.Translation = append(ja.Translation, t)
	ja.find(l.t("not migrated"), l.t("swimlanes and card colours of boards"), key, l.t("set them up again"))
	return len(boards.Values), nil
}

// groupTable lists the groups the access of the projects names, and what they become in Kogge.
func (ja *jiraAnalysis) groupTable() error {
	l := ja.l
	if len(ja.groups) == 0 {
		return nil
	}
	t := Table{ID: "groups", Title: l.t("Groups"), Intro: l.t("Groups are created in Kogge by name with their members; users who are inactive in the source do not join."),
		Columns: []string{l.t("Group"), l.t("Members"), l.t("In Kogge")}}
	renamed := Decision{Code: "group-renamed", Severity: "medium", Message: l.t("Kogge has groups of its own with these names: the migrated groups get other names. Merge them by hand if they belong together.")}
	for _, g := range slices.Sorted(mapKeys(ja.groups)) {
		var members []jUser
		if _, err := ja.work.Read(&members, "jira", "groups", url.PathEscape(g)+".json"); err != nil {
			return err
		}
		active := 0
		for _, m := range members {
			ja.person(m.Name, m.DisplayName, m.EmailAddress, m.Active)
			if !m.inactive() {
				active++
			}
		}
		how, other, err := ja.group(g)
		if err != nil {
			return err
		}
		class := ""
		if other {
			class = Warn
			renamed.item(g + ": " + how)
		}
		t.add(class, 0, g, l.t("%d (%d active)", len(members), active), how)
	}
	ja.Translation = append(ja.Translation, t)
	ja.decision(renamed)
	return nil
}
