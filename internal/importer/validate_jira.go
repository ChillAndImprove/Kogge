// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
)

// The validation of a Jira load.

// Validate compares the extraction with the load in Kogge and writes jira-validation.json and
// jira-validation.html into the work directory.
func (j *Jira) Validate(ctx context.Context, l Loader, opt ValidateOptions) (*Validation, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := j.work.Read(&server, "jira", "serverInfo.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Jira extraction", j.work.Root)
	}
	v := newValidation(ctx, "jira", strings.TrimRight(cmp.Or(server.BaseURL, j.opt.URL), "/"), j.work, l, opt)
	jv := &jiraValidation{validation: v, j: j}
	if err := jv.run(); err != nil {
		return nil, err
	}
	return v.Validation, v.finish()
}

type jiraValidation struct {
	*validation
	j *Jira
	// What the extraction holds, by source id.
	projects, issues, comments, worklogs, sprints, boards, versions, components map[string]string
	files                                                                       []sourceFile
	issueKeys                                                                   []string // for samples
	sprintField                                                                 string
	mapped                                                                      map[string]bool // custom fields the load migrates, by id and lower-case name
}

func (jv *jiraValidation) run() error {
	l := jv.l
	jv.projects, jv.issues, jv.comments, jv.worklogs = map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	jv.sprints, jv.boards, jv.versions, jv.components = map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	jv.mapped = map[string]bool{}
	var fields []jField
	if _, err := jv.work.Read(&fields, "jira", "fields.json"); err != nil {
		return err
	}
	for _, f := range fields {
		if strings.HasSuffix(f.Schema.Custom, ":gh-sprint") {
			jv.sprintField = f.ID
		}
		if _, ok := jiraFieldTypes[f.Schema.Custom]; ok && f.Custom {
			jv.mapped[f.ID], jv.mapped[strings.ToLower(f.Name)] = true, true
		}
	}
	keys := jv.j.selected()
	if len(keys) == 0 {
		keys = jv.work.List("jira", "projects")
	}
	for _, key := range keys {
		if err := jv.collect(key); err != nil {
			return fmt.Errorf("project %s: %w", key, err)
		}
	}
	for _, c := range []struct {
		id, label, sourceType, entityType string
		src                               map[string]string
	}{
		{"projects", l.t("Projects"), "project", "project", jv.projects},
		{"issues", l.t("Issues"), "issue", "issue", jv.issues},
		{"comments", l.t("Comments"), "comment", "issue-comment", jv.comments},
		{"worklogs", l.t("Work logs"), "worklog", "worklog", jv.worklogs},
		{"sprints", l.t("Sprints"), "sprint", "sprint", jv.sprints},
		{"boards", l.t("Boards"), "board", "board", jv.boards},
		{"versions", l.t("Versions"), "version", "version", jv.versions},
		{"components", l.t("Components"), "component", "component", jv.components},
	} {
		if _, err := jv.countCheck(c.id, c.label, c.src, c.sourceType, c.entityType); err != nil {
			return err
		}
	}
	if err := jv.keyCheck(); err != nil {
		return err
	}
	if err := jv.fileCheck(jv.files, "issue-attachment"); err != nil {
		return err
	}
	if err := jv.linkCheck("issue", "issue-comment"); err != nil {
		return err
	}
	if err := jv.samples(keys); err != nil {
		return err
	}
	return jv.permissions(keys)
}

// collect reads what the extraction of a project holds.
func (jv *jiraValidation) collect(key string) error {
	var p jProject
	if ok, err := jv.work.Read(&p, "jira", "projects", key, "project.json"); err != nil || !ok {
		return err
	}
	jv.projects[p.ID] = p.Key
	for _, v := range p.Versions {
		jv.versions[v.ID] = key + " " + v.Name
	}
	for _, c := range p.Components {
		jv.components[c.ID] = key + " " + c.Name
	}
	var boards jBoards
	if _, err := jv.work.Read(&boards, "jira", "projects", key, "boards.json"); err != nil {
		return err
	}
	for _, b := range boards.Values {
		jv.boards[strconv.FormatInt(b.ID, 10)] = b.Name
	}
	for _, name := range jv.work.List("jira", "projects", key, "sprints") {
		var page jPage[jSprint]
		if _, err := jv.work.Read(&page, "jira", "projects", key, "sprints", name); err != nil {
			return err
		}
		for _, s := range page.Values {
			jv.sprints[strconv.FormatInt(s.ID, 10)] = s.Name
		}
	}
	for _, name := range jv.work.List("jira", "projects", key, "issues") {
		var page jSearch
		if _, err := jv.work.Read(&page, "jira", "projects", key, "issues", name); err != nil {
			return err
		}
		for _, is := range page.Issues {
			jv.issues[is.ID] = is.Key
			jv.issueKeys = append(jv.issueKeys, is.Key)
			comments, worklogs, err := jv.details(key, is)
			if err != nil {
				return err
			}
			for _, c := range comments {
				jv.comments[c.ID] = is.Key
			}
			for _, w := range worklogs {
				if w.TimeSpentSeconds > 0 { // the load leaves empty work logs out
					jv.worklogs[w.ID] = is.Key
				}
			}
			var atts []jAttachment
			_ = json.Unmarshal(is.Fields["attachment"], &atts)
			for _, a := range atts {
				f := sourceFile{srcID: a.ID, name: is.Key + ": " + a.Filename}
				if jv.work.Size("jira", "attachments", a.ID) >= 0 {
					f.path = jv.work.Path("jira", "attachments", a.ID)
				}
				jv.files = append(jv.files, f)
			}
			if jv.sprintField != "" {
				for _, s := range parseSprints(is.Fields[jv.sprintField]) {
					jv.sprints[strconv.FormatInt(s.ID, 10)] = s.Name
				}
			}
		}
	}
	return nil
}

// details returns the comments and work logs of an issue: those the extraction read separately, or the search's.
func (jv *jiraValidation) details(project string, is jIssue) ([]jComment, []jWorklog, error) {
	var comments struct {
		Comments []jComment `json:"comments"`
	}
	if ok, err := jv.work.Read(&comments, "jira", "projects", project, "comments", is.Key+".json"); err != nil {
		return nil, nil, err
	} else if !ok {
		_ = json.Unmarshal(is.Fields["comment"], &comments)
	}
	var worklogs struct {
		Worklogs []jWorklog `json:"worklogs"`
	}
	if ok, err := jv.work.Read(&worklogs, "jira", "projects", project, "worklogs", is.Key+".json"); err != nil {
		return nil, nil, err
	} else if !ok {
		_ = json.Unmarshal(is.Fields["worklog"], &worklogs)
	}
	return comments.Comments, worklogs.Worklogs, nil
}

// keyCheck checks that the issues kept their keys.
func (jv *jiraValidation) keyCheck() error {
	l := jv.l
	have, err := importedIssueKeys(jv.ctx, jv.ld.Pool, jv.source)
	if err != nil {
		return err
	}
	c := Check{ID: "counts.issue-keys", Group: "counts", Title: l.t("Issue keys")}
	differ := 0
	for _, id := range slices.Sorted(mapKeys(jv.issues)) {
		if key, ok := have[id]; ok && key != jv.issues[id] {
			differ++
			c.detail(l.t("%s is %s in Kogge", jv.issues[id], key))
		}
	}
	if differ > 0 {
		c.Status, c.Message = Fail, l.t("%d issues have another key in Kogge.", differ)
	} else {
		c.Status, c.Message = Pass, l.t("The issues in Kogge have the keys of the source.")
	}
	jv.add(c)
	return nil
}

// recordedField reports whether the load records an item of a change log in the history.
func (jv *jiraValidation) recordedField(it jChangeItem) bool {
	switch strings.ToLower(it.Field) {
	case "status", "priority", "resolution", "issuetype", "assignee", "reporter", "summary", "description", "environment", "labels",
		"component", "fix version", "version", "duedate", "timeoriginalestimate", "timeestimate", "sprint", "parent", "parent issue",
		"epic link", "link", "attachment":
		return true
	}
	return jv.mapped[it.FieldID] || (it.FieldType == "custom" && jv.mapped[strings.ToLower(it.Field)])
}

// samples checks the renderings, history and authors of a sample of issues.
func (jv *jiraValidation) samples(keys []string) error {
	l := jv.l
	picked := map[string]bool{}
	for _, k := range sample(jv.issueKeys, jv.opt.Sample) {
		picked[k] = true
	}
	have, err := importedIDs(jv.ctx, jv.ld.Pool, jv.source, "issue", "issue")
	if err != nil {
		return err
	}
	render := Check{ID: "renderings.issues", Group: "renderings", Title: l.t("Descriptions of a sample of issues")}
	history := Check{ID: "history.issues", Group: "history", Title: l.t("Authors, dates and history of a sample of issues")}
	rendered, renderBad, checked, historyBad := 0, 0, 0, 0
	for _, key := range keys {
		for _, name := range jv.work.List("jira", "projects", key, "issues") {
			var page jSearch
			if _, err := jv.work.Read(&page, "jira", "projects", key, "issues", name); err != nil {
				return err
			}
			for _, is := range page.Issues {
				id, ok := have[is.ID]
				if !picked[is.Key] || !ok {
					continue
				}
				f, err := issueFactsOf(jv.ctx, jv.ld.Pool, id)
				if err != nil {
					return err
				}
				// The rendering of the description, against the conversion of the source's.
				var expected *docmodel.Doc
				if html := rawString(is.RenderedFields["description"]); html != "" {
					expected = docmodel.FromHTML(html, docmodel.ImportResolver{})
				} else if text := rawString(is.Fields["description"]); text != "" {
					expected = docmodel.Paragraphs(text)
				}
				if expected != nil {
					rendered++
					if problem := jv.rendering(f.description, expected); problem != "" {
						renderBad++
						render.detail(is.Key + ": " + problem)
					}
				}
				checked++
				problems, err := jv.compareIssue(key, is, f)
				if err != nil {
					return err
				}
				if len(problems) > 0 {
					historyBad++
					for _, p := range problems {
						history.detail(is.Key + ": " + p)
					}
				}
			}
		}
	}
	switch {
	case rendered == 0:
		render.Status, render.Message = Warn, l.t("No issue of the sample has a description.")
	case renderBad > 0:
		render.Status, render.Message = Fail, l.t("%d of %d descriptions do not render like the source.", renderBad, rendered)
	default:
		render.Status, render.Message = Pass, l.t("%d descriptions are valid documents, render and keep the source's text.", rendered)
	}
	jv.add(render)
	switch {
	case checked == 0:
		history.Status, history.Message = Warn, l.t("No issue of the sample is in Kogge.")
	case historyBad > 0:
		history.Status, history.Message = Fail, l.t("%d of %d issues differ from the source.", historyBad, checked)
	default:
		history.Status, history.Message = Pass, l.t("%d issues have the reporter, creator, assignee, creation date, comments with their authors, work logs and history of the source.", checked)
	}
	jv.add(history)
	return nil
}

// compareIssue lists how an issue in Kogge differs from the source.
func (jv *jiraValidation) compareIssue(project string, is jIssue, f *issueFacts) ([]string, error) {
	l := jv.l
	var problems []string
	expect := func(raw json.RawMessage) (string, error) {
		var u *jUser
		if json.Unmarshal(raw, &u) != nil || u == nil || (u.Name == "" && u.Key == "") {
			return "", nil
		}
		m, _, err := jv.expectedUser(cmp.Or(u.Name, u.Key), u.EmailAddress)
		return m.Username, err
	}
	for _, x := range []struct {
		field, label, got string
	}{{"reporter", l.t("the reporter"), f.reporter}, {"assignee", l.t("the assignee"), f.assignee}} {
		want, err := expect(is.Fields[x.field])
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(want, x.got) {
			problems = append(problems, l.t("%s is %q in Kogge, %q in the source", x.label, x.got, want))
		}
	}
	if want, err := expect(is.Fields["creator"]); err != nil {
		return nil, err
	} else if want != "" && !strings.EqualFold(want, f.creator) {
		problems = append(problems, l.t("%s is %q in Kogge, %q in the source", l.t("the creator"), f.creator, want))
	}
	if created := parseTime(rawString(is.Fields["created"])); created != nil && !sameTime(f.created, created) {
		problems = append(problems, l.t("created %s in Kogge, %s in the source", f.created.UTC().Format("2006-01-02 15:04:05"),
			created.UTC().Format("2006-01-02 15:04:05")))
	}
	comments, worklogs, err := jv.details(project, is)
	if err != nil {
		return nil, err
	}
	var authors []string
	for _, c := range comments {
		name := ""
		if c.Author != nil {
			m, _, err := jv.expectedUser(cmp.Or(c.Author.Name, c.Author.Key), c.Author.EmailAddress)
			if err != nil {
				return nil, err
			}
			name = m.Username
		}
		authors = append(authors, strings.ToLower(name))
	}
	got := make([]string, len(f.commentAuthors))
	for i, a := range f.commentAuthors {
		got[i] = strings.ToLower(a)
	}
	slices.Sort(authors)
	slices.Sort(got)
	if len(comments) != f.comments {
		problems = append(problems, l.t("%d comments in Kogge, %d in the source", f.comments, len(comments)))
	} else if !slices.Equal(authors, got) {
		problems = append(problems, l.t("the comments' authors differ: %s in Kogge, %s in the source", strings.Join(got, ", "), strings.Join(authors, ", ")))
	}
	n := 0
	for _, w := range worklogs {
		if w.TimeSpentSeconds > 0 {
			n++
		}
	}
	if n != f.worklogs {
		problems = append(problems, l.t("%d work logs in Kogge, %d in the source", f.worklogs, n))
	}
	changelog, err := jv.work.jiraChangelog(project, is)
	if err != nil {
		return nil, err
	}
	recorded := 0
	if changelog != nil {
		for _, h := range changelog.Histories {
			if slices.ContainsFunc(h.Items, jv.recordedField) {
				recorded++
			}
		}
	}
	if recorded != f.changes {
		problems = append(problems, l.t("%d history entries in Kogge, %d in the source", f.changes, recorded))
	}
	return problems, nil
}

// permissions checks who may browse the projects: a sample of the users the projects' roles, groups and permission
// schemes name, in Kogge against the source's permission scheme.
func (jv *jiraValidation) permissions(keys []string) error {
	l := jv.l
	var cases []accessCase
	var people, groups []string
	emails := map[string]string{}
	type grant struct {
		users, groups []string
		all           bool
	}
	grants := map[string]*grant{}
	for _, key := range keys {
		var ps jPermissionScheme
		ok, err := jv.work.Read(&ps, "jira", "projects", key, "permissionscheme.json")
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		var roles []jRole
		if _, err := jv.work.Read(&roles, "jira", "projects", key, "roles.json"); err != nil {
			return err
		}
		var p jProject
		if _, err := jv.work.Read(&p, "jira", "projects", key, "project.json"); err != nil {
			return err
		}
		g := &grant{}
		roleMembers := func(name string) ([]string, []string) {
			var us, gs []string
			for _, r := range roles {
				if !strings.EqualFold(r.Name, name) {
					continue
				}
				for _, a := range r.Actors {
					switch a.Type {
					case "atlassian-user-role-actor":
						us = append(us, a.Name)
					case "atlassian-group-role-actor":
						gs = append(gs, a.Name)
					}
				}
			}
			return us, gs
		}
		for _, perm := range ps.Permissions {
			if perm.Permission != "BROWSE_PROJECTS" {
				continue
			}
			switch h := perm.Holder; h.Type {
			case "projectRole":
				name := h.Parameter
				if h.ProjectRole != nil {
					name = h.ProjectRole.Name
				}
				us, gs := roleMembers(name)
				g.users, g.groups = append(g.users, us...), append(g.groups, gs...)
			case "group":
				if h.Parameter == "" {
					g.all = true
				} else {
					g.groups = append(g.groups, h.Parameter)
				}
			case "user":
				if h.User != nil {
					g.users = append(g.users, h.User.Name)
				} else {
					g.users = append(g.users, h.Parameter)
				}
			case "applicationRole", "anyone":
				g.all = true
			case "projectLead":
				if p.Lead != nil {
					g.users = append(g.users, p.Lead.Name)
				}
			}
		}
		grants[key] = g
		// The users to check: those the roles and the scheme name, and the members of the groups they name (also
		// of roles without the permission: they must not get it).
		for _, r := range roles {
			for _, a := range r.Actors {
				if a.Type == "atlassian-user-role-actor" {
					people = append(people, a.Name)
				} else {
					groups = append(groups, a.Name)
				}
			}
		}
		people = append(people, g.users...)
		groups = append(groups, g.groups...)
	}
	members := map[string][]jUser{}
	for _, name := range groups {
		if members[name] != nil {
			continue
		}
		var list []jUser
		if _, err := jv.work.Read(&list, "jira", "groups", url.PathEscape(name)+".json"); err != nil {
			return err
		}
		members[name] = list
		for _, u := range list {
			people = append(people, u.Name)
			emails[strings.ToLower(u.Name)] = u.EmailAddress
		}
	}
	slices.Sort(people)
	people = slices.Compact(people)
	for _, key := range keys {
		g, ok := grants[key]
		if !ok {
			continue
		}
		for _, u := range sample(people, jv.opt.Sample) {
			want := g.all || slices.ContainsFunc(g.users, func(n string) bool { return strings.EqualFold(n, u) })
			for _, gn := range g.groups {
				for _, m := range members[gn] {
					if strings.EqualFold(m.Name, u) && !m.inactive() {
						want = true
					}
				}
			}
			cases = append(cases, accessCase{user: u, email: emails[strings.ToLower(u)], target: key, label: l.t("the project %s", key), want: want})
		}
	}
	return jv.accessCheck("projects", l.t("Who may browse the projects"), cases, func(p *identity.Principal, key string) bool {
		_, err := jv.ld.Issues.GetProject(jv.ctx, p, key)
		return err == nil
	})
}
