// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The analysis of a Bitbucket extraction: projects and repositories with their sizes, history and LFS objects,
// permissions, branch permissions, merge checks and strategies, default reviewers, webhooks and pull requests,
// and how the load translates them.

// Analyse reads the extraction (and the target installation, if given) and writes bitbucket-analysis.json and
// bitbucket-analysis.html into the work directory.
func (b *Bitbucket) Analyse(ctx context.Context, target *Target, language string) (*Analysis, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bitbucket", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bitbucket extraction", b.work.Root)
	}
	a, err := newAnalysis(ctx, "bitbucket", strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/"), b.work, language, target)
	if err != nil {
		return nil, err
	}
	ba := &bitbucketAnalysis{analysis: a, b: b, groups: map[string]bool{}}
	if err := ba.run(); err != nil {
		return nil, err
	}
	return a.Analysis, a.finish()
}

type bitbucketAnalysis struct {
	*analysis
	b      *Bitbucket
	groups map[string]bool
	counts map[string]int
	// Tables filled while the repositories are read.
	repos, perms, rules, checks Table
	repoBytes, lfsBytes         int64
	lfsMissing                  int
	forks, webhooks             Decision
	takenOver, reviewers        Decision
}

func (ba *bitbucketAnalysis) user(u *bbUser) {
	if u != nil {
		ba.person(cmp.Or(u.Name, u.Slug), u.DisplayName, u.EmailAddress, nil)
	}
}

func (ba *bitbucketAnalysis) run() error {
	l := ba.l
	ba.counts = map[string]int{}
	var people []bbUser
	if _, err := ba.work.Read(&people, "bitbucket", "users.json"); err != nil {
		return err
	}
	for i := range people {
		ba.user(&people[i])
	}
	ba.repos = Table{ID: "repositories", Title: l.t("Repositories"), Columns: []string{l.t("Repository"), l.t("History"), l.t("Size"),
		l.t("LFS objects"), l.t("Pull requests"), l.t("In Kogge")}}
	ba.perms = Table{ID: "permissions", Title: l.t("Permissions"), Columns: []string{l.t("Scope"), l.t("Holder"), l.t("Permission"), l.t("In Kogge")}}
	ba.rules = Table{ID: "branch-permissions", Title: l.t("Branch permissions"), Columns: []string{l.t("Scope"), l.t("Restriction"), l.t("Branches"),
		l.t("Exempt"), l.t("In Kogge")}}
	ba.checks = Table{ID: "merge-checks", Title: l.t("Merge checks, strategies and hooks"), Columns: []string{l.t("Repository"), l.t("Setting"), l.t("In Kogge")}}
	ba.forks = Decision{Code: "fork-pull-requests", Severity: "medium", Message: l.t("Kogge has no forks yet: these open pull requests from forks are declined by the load; their commits and diffs are kept. Open them again from branches after the load.")}
	ba.webhooks = Decision{Code: "webhooks", Severity: "low", Message: l.t("Webhooks are not migrated: set these up again in Kogge where they are still needed.")}
	ba.takenOver = Decision{Code: "repository-taken-over", Severity: "high", Message: l.t("Kogge has repositories with these names that were not created by this migration: the load takes them over and replaces their branches and tags with the source's. Rename or delete them in Kogge first, or leave the projects out.")}
	ba.reviewers = Decision{Code: "default-reviewer-conditions", Severity: "medium", Message: l.t("These default reviewer conditions apply to some branches only, which Kogge cannot express: they are not migrated.")}
	var keys []string
	for _, k := range ba.work.List("bitbucket", "projects") {
		if len(ba.b.opt.Projects) == 0 || slices.ContainsFunc(ba.b.opt.Projects, func(s string) bool { return strings.EqualFold(s, k) }) {
			keys = append(keys, k)
		}
	}
	projects := Table{ID: "projects", Title: l.t("Projects"), Columns: []string{l.t("Project"), l.t("Repositories"), l.t("In Kogge")}}
	for _, key := range keys {
		if err := ba.project(key, &projects); err != nil {
			return fmt.Errorf("project %s: %w", key, err)
		}
	}
	ba.Translation = append(ba.Translation, projects, ba.repos, ba.perms, ba.rules, ba.checks)
	for _, d := range []Decision{ba.takenOver, ba.forks, ba.reviewers, ba.webhooks} {
		ba.decision(d)
	}
	if len(ba.groups) > 0 {
		t := Table{ID: "groups", Title: l.t("Groups"), Intro: l.t("Groups named in permissions are created in Kogge by name, without members: Bitbucket's API does not list them. Their members come from the company directory (single sign-on) or are added by hand."),
			Columns: []string{l.t("Group"), l.t("In Kogge")}}
		renamed := Decision{Code: "group-renamed", Severity: "medium", Message: l.t("Kogge has groups of its own with these names: the migrated groups get other names. Merge them by hand if they belong together.")}
		for _, g := range slices.Sorted(mapKeys(ba.groups)) {
			how, other, err := ba.group(g)
			if err != nil {
				return err
			}
			class := ""
			if other {
				class = Warn
				renamed.item(g + ": " + how)
			}
			t.add(class, 0, g, how)
		}
		ba.Translation = append(ba.Translation, t)
		ba.decision(renamed)
		ba.decide("groups-without-members", "medium", l.t("%d groups are created without members (Bitbucket's API does not list them): permissions given to groups take effect once the groups have members.", len(ba.groups)))
	}
	nm := Table{ID: "not-migrated", Title: l.t("Not migrated"), Columns: []string{l.t("What"), l.t("Note")}}
	for _, row := range [][2]string{
		{l.t("Personal repositories"), l.t("move those still needed to a project first")},
		{l.t("Access keys of repositories and SSH keys of users"), l.t("users add their keys in Kogge")},
		{l.t("Build statuses and code insights of commits"), ""},
		{l.t("Attachments of pull request comments"), l.t("they stay links to the source")},
		{l.t("Data of apps"), ""},
	} {
		nm.add("muted", 0, row[0], row[1])
	}
	ba.Translation = append(ba.Translation, nm)
	for _, k := range []struct{ key, label string }{{"projects", l.t("Projects")}, {"repositories", l.t("Repositories")},
		{"pullRequests", l.t("Pull requests")}, {"lfsObjects", l.t("LFS objects")}, {"personalProjects", l.t("Personal projects (not migrated)")},
		{"archived", l.t("Archived repositories")}, {"open", l.t("Open pull requests")}, {"comments", l.t("Pull request comments")},
		{"tasks", l.t("Tasks")}, {"branchPermissions", l.t("Branch permissions")}, {"grants", l.t("Permissions of users and groups")}} {
		ba.count(k.key, k.label, ba.counts[k.key])
	}
	ba.size("repositoryBytes", l.t("Repositories (mirrors in the work directory)"), ba.repoBytes, true)
	ba.size("lfsBytes", l.t("LFS objects"), ba.lfsBytes, true)
	if ba.lfsMissing > 0 {
		ba.decide("lfs-missing", "medium", l.t("%d LFS objects are not in the work directory (the source lacks them, or the extraction left them out): their files stay pointer files in Kogge.", ba.lfsMissing))
	}
	if err := ba.matchUsers(); err != nil {
		return err
	}
	return ba.estimate("bitbucket-report.json", phase{name: "repositories", bytes: ba.repoBytes + ba.lfsBytes},
		phase{name: "pull-requests", items: ba.counts["pullRequests"]})
}

func (ba *bitbucketAnalysis) project(key string, projects *Table) error {
	l := ba.l
	dir := []string{"bitbucket", "projects", key}
	var p bbProject
	if ok, err := ba.work.Read(&p, append(dir, "project.json")...); err != nil || !ok {
		return err
	}
	if p.Type == "PERSONAL" || strings.HasPrefix(p.Key, "~") {
		ba.counts["personalProjects"]++
		ba.find(l.t("not migrated"), l.t("personal project %s", p.Key), p.Key, l.t("move its repositories to a project first"))
		return nil
	}
	ba.counts["projects"]++
	slugs := ba.work.List(append(dir, "repos")...)
	how := l.t("a new code project with the key %s", p.Key)
	class := ""
	var projectID int64
	if ba.target != nil {
		ours, loaded, err := ba.legacyEntity("code-project", strconv.FormatInt(p.ID, 10))
		if err != nil {
			return err
		}
		id, err := codeProjectID(ba.ctx, ba.target.tx, p.Key)
		if err != nil {
			return err
		}
		projectID = id
		switch {
		case loaded:
			how = l.t("loaded before: is updated")
		case id != 0 && id != ours:
			how, class = l.t("the existing code project %s: the repositories are added to it", p.Key), Warn
			ba.decide("code-project-exists", "medium", l.t("Kogge has a code project with the key %s that was not created by this migration: the repositories are added to it, and its members get the permissions it grants.", p.Key))
		}
	}
	projects.add(class, 0, keyed(p.Name, p.Key), l.n(int64(len(slugs))), how)
	if err := ba.permissions(p.Key, "", projectPermissions, p.Public, dir...); err != nil {
		return err
	}
	if err := ba.restrictions(p.Key, dir...); err != nil {
		return err
	}
	if err := ba.hooks(p.Key, dir...); err != nil {
		return err
	}
	for _, slug := range slugs {
		if err := ba.repository(p, slug, projectID); err != nil {
			return fmt.Errorf("repository %s: %w", slug, err)
		}
	}
	return nil
}

func (ba *bitbucketAnalysis) repository(p bbProject, slug string, projectID int64) error {
	l := ba.l
	dir := []string{"bitbucket", "projects", p.Key, "repos", slug}
	name := p.Key + "/" + slug
	var r bbRepo
	if ok, err := ba.work.Read(&r, append(dir, "repo.json")...); err != nil || !ok {
		return err
	}
	ba.counts["repositories"]++
	if r.Archived {
		ba.counts["archived"]++
	}
	mirror := ba.work.Path("bitbucket", "git", p.Key, r.Slug+".git")
	history, size := l.t("not extracted"), ""
	if _, err := os.Stat(mirror); err == nil {
		n := dirSize(mirror)
		ba.repoBytes += n
		history, size = l.t("extracted"), l.bytes(n)
	} else {
		ba.decide("history-missing", "medium", l.t("The history of %s was not extracted: the repository is created empty. Extract without --skip-git and load again.", name))
	}
	var pointers []lfsPointer
	if _, err := ba.work.Read(&pointers, append(dir, "lfs.json")...); err != nil {
		return err
	}
	have := 0
	for _, o := range pointers {
		ba.counts["lfsObjects"]++
		if ba.work.Size(lfsFile("bitbucket", o.OID)...) == o.Size {
			have++
			ba.lfsBytes += o.Size
		} else {
			ba.lfsMissing++
		}
	}
	prs, open, err := ba.pullRequests(r, name, dir...)
	if err != nil {
		return err
	}
	how, class := l.t("a new repository %s; clone addresses stay the same", name), ""
	if ba.target != nil {
		_, loaded, err := ba.legacyEntity("repository", strconv.FormatInt(r.ID, 10))
		if err != nil {
			return err
		}
		existing := int64(0)
		if projectID != 0 {
			if existing, err = repositoryID(ba.ctx, ba.target.tx, projectID, r.Slug); err != nil {
				return err
			}
		}
		switch {
		case loaded:
			how = l.t("loaded before: branches and tags become those of the extraction")
		case existing != 0:
			how, class = l.t("taken over: its branches and tags are replaced"), Fail
			ba.takenOver.item(name)
		}
	}
	ba.repos.add(class, 0, name, history, size, l.t("%d of %d extracted", have, len(pointers)), l.t("%d (%d open)", prs, open), how)
	if err := ba.permissions(p.Key, r.Slug, repoPermissions, r.Public, dir...); err != nil {
		return err
	}
	if err := ba.restrictions(name, dir...); err != nil {
		return err
	}
	if err := ba.settings(name, dir...); err != nil {
		return err
	}
	return ba.hooks(name, dir...)
}

// pullRequests counts the pull requests of a repository with their comments and tasks, and finds those from forks.
func (ba *bitbucketAnalysis) pullRequests(r bbRepo, name string, dir ...string) (int, int, error) {
	if !ba.work.Exists(append(dir, "pull-requests.json")...) {
		return 0, 0, nil
	}
	total, open := 0, 0
	err := eachItem([]string{ba.work.Path(append(dir, "pull-requests.json")...)}, func(raw json.RawMessage, _ itemAt) error {
		var pr bbPullRequest
		if err := json.Unmarshal(raw, &pr); err != nil {
			return err
		}
		total++
		ba.counts["pullRequests"]++
		if pr.State == "OPEN" {
			open++
			ba.counts["open"]++
		}
		ba.user(&pr.Author.User)
		for i := range pr.Reviewers {
			ba.user(&pr.Reviewers[i].User)
		}
		if from := pr.FromRef.Repository; from.ID != 0 && from.ID != r.ID && pr.State == "OPEN" {
			ba.forks.item(fmt.Sprintf("%s #%d (%s/%s)", name, pr.ID, from.Project.Key, from.Slug))
		}
		var activities []bbActivity
		if _, err := ba.work.Read(&activities, append(dir, "activities", strconv.Itoa(pr.ID)+".json")...); err != nil {
			return err
		}
		var walk func(c *bbComment)
		walk = func(c *bbComment) {
			ba.counts["comments"]++
			if c.Severity == "BLOCKER" {
				ba.counts["tasks"]++
			}
			ba.user(&c.Author)
			for i := range c.Comments {
				walk(&c.Comments[i])
			}
		}
		for _, a := range activities {
			ba.user(&a.User)
			if a.Action == "COMMENTED" && a.CommentAction == "ADDED" && a.Comment != nil {
				walk(a.Comment)
			}
			if _, known := map[string]bool{"OPENED": true, "REOPENED": true, "APPROVED": true, "UNAPPROVED": true, "REVIEWED": true, "MERGED": true,
				"DECLINED": true, "RESCOPED": true, "COMMENTED": true}[a.Action]; !known {
				ba.find(ba.l.t("pull request event"), a.Action, name, ba.l.t("not shown in the pull request's timeline"))
			}
		}
		return nil
	})
	return total, open, err
}

// permissions lists the user and group permissions of a project (slug "") or repository.
func (ba *bitbucketAnalysis) permissions(key, slug string, mapping map[string]string, public bool, dir ...string) error {
	l := ba.l
	scope := key
	if slug != "" {
		scope = key + "/" + slug
	}
	var users []bbUserPermission
	if _, err := ba.work.Read(&users, append(dir, "permissions-users.json")...); err != nil {
		return err
	}
	for i := range users {
		p := &users[i]
		ba.user(&p.User)
		ba.counts["grants"]++
		perm, class := mapping[p.Permission], ""
		if perm == "" {
			perm, class = l.t("not migrated"), Warn
			ba.find(l.t("permission"), p.Permission, scope, l.t("the grant is left out"))
		}
		ba.perms.add(class, 500, scope, l.t("user %s", cmp.Or(p.User.Name, p.User.Slug)), p.Permission, perm)
	}
	var groups []bbGroupPermission
	if _, err := ba.work.Read(&groups, append(dir, "permissions-groups.json")...); err != nil {
		return err
	}
	for _, p := range groups {
		ba.counts["grants"]++
		perm, class := mapping[p.Permission], ""
		if perm == "" || p.Group.Name == "" {
			perm, class = l.t("not migrated"), Warn
			ba.find(l.t("permission"), p.Permission, scope, l.t("the grant is left out"))
		} else {
			ba.groups[p.Group.Name] = true
		}
		ba.perms.add(class, 500, scope, l.t("group %s", p.Group.Name), p.Permission, perm)
	}
	if public {
		ba.perms.add(Warn, 500, scope, l.t("anonymous users"), "PUBLIC", l.t("read access for all signed-in users"))
		ba.decide("public", "low", l.t("%s is public in the source: in Kogge all signed-in users can read it; anonymous access is a setting of the installation.", scope))
	}
	return nil
}

// restrictions lists the branch permissions of a project or repository.
func (ba *bitbucketAnalysis) restrictions(scope string, dir ...string) error {
	l := ba.l
	var list []bbRestriction
	if _, err := ba.work.Read(&list, append(dir, "restrictions.json")...); err != nil {
		return err
	}
	for i := range list {
		r := &list[i]
		ba.counts["branchPermissions"]++
		var exempt []string
		for j := range r.Users {
			ba.user(&r.Users[j])
			exempt = append(exempt, cmp.Or(r.Users[j].Name, r.Users[j].Slug))
		}
		for _, g := range r.Groups {
			ba.groups[g] = true
			exempt = append(exempt, l.t("group %s", g))
		}
		if len(r.AccessKeys) > 0 {
			exempt = append(exempt, l.t("%d access keys (not exempted in Kogge)", len(r.AccessKeys)))
		}
		how, class := "", ""
		_, known := restrictionKinds[r.Type]
		switch {
		case !known:
			how, class = l.t("not migrated: no such restriction in Kogge"), Warn
			ba.find(l.t("branch permission"), r.Type, scope, l.t("left out"))
		case r.Matcher.Type.ID == "MODEL_BRANCH" || r.Matcher.Type.ID == "MODEL_CATEGORY":
			how, class = l.t("not migrated: the branching model has no counterpart; set it up again with a pattern"), Warn
			ba.find(l.t("branch permission"), l.t("on the branching model (%s)", r.Matcher.DisplayID), scope, l.t("set it up again with a pattern"))
		case r.Matcher.Type.ID == "PATTERN":
			how = l.t("a branch permission on %s", bbPattern(r.Matcher.DisplayID))
		case r.Matcher.Type.ID == "ANY_REF":
			how = l.t("a branch permission on all refs")
		default:
			how = l.t("a branch permission on %s", strings.TrimPrefix(cmp.Or(r.Matcher.ID, r.Matcher.DisplayID), "refs/heads/"))
		}
		ba.rules.add(class, 500, scope, r.Type, cmp.Or(r.Matcher.DisplayID, r.Matcher.ID), strings.Join(exempt, ", "), how)
	}
	return nil
}

// settings lists the merge checks, strategies and default reviewers of a repository.
func (ba *bitbucketAnalysis) settings(name string, dir ...string) error {
	l := ba.l
	var prs bbPullRequestSettings
	ok, err := ba.work.Read(&prs, append(dir, "pull-request-settings.json")...)
	if err != nil {
		return err
	}
	if ok {
		if prs.RequiredApprovers > 0 || (prs.ApproversCheck != nil && prs.ApproversCheck.Enabled) {
			n := prs.RequiredApprovers
			if prs.ApproversCheck != nil {
				n = prs.ApproversCheck.Count
			}
			ba.checks.add("", 500, name, l.t("%d approvals required", n), l.t("the same merge check"))
		}
		if prs.RequiredAllApprovers {
			ba.checks.add(Warn, 500, name, l.t("all reviewers must approve"), l.t("at least one approval (Kogge counts approvals)"))
		}
		if prs.RequiredSuccessfulBuilds > 0 || (prs.BuildsCheck != nil && prs.BuildsCheck.Enabled) {
			ba.checks.add("", 500, name, l.t("successful builds required"), l.t("the same merge check"))
		}
		if prs.RequiredAllTasksComplete {
			ba.checks.add("", 500, name, l.t("all tasks resolved"), l.t("the same merge check"))
		}
		for _, s := range append([]*bbStrategy{prs.MergeConfig.DefaultStrategy}, ptrs(prs.MergeConfig.Strategies)...) {
			if s == nil || !s.Enabled {
				continue
			}
			m, known := mergeStrategies[s.ID]
			switch {
			case !known:
				ba.checks.add(Warn, 500, name, l.t("merge strategy %s", s.ID), l.t("not migrated"))
				ba.find(l.t("merge strategy"), s.ID, name, l.t("left out"))
			case s.ID != "no-ff" && s.ID != "ff-only" && s.ID != "squash":
				ba.checks.add(Warn, 500, name, l.t("merge strategy %s", s.ID), l.t("approximated by %s", m))
				ba.find(l.t("merge strategy"), s.ID, name, l.t("approximated by %s", m))
			}
		}
	}
	conditions, err := (&bbLoad{Bitbucket: ba.b}).conditions(dir...)
	if err != nil {
		return err
	}
	for _, c := range conditions {
		for i := range c.Reviewers {
			ba.user(&c.Reviewers[i])
		}
		source, target := cmp.Or(c.SourceMatcher, c.SourceRefMatcher), cmp.Or(c.TargetMatcher, c.TargetRefMatcher)
		all := func(m *bbMatcher) bool { return m == nil || m.Type.ID == "ANY_REF" }
		if !all(source) || (!all(target) && !(target.Type.ID == "BRANCH")) {
			ba.reviewers.item(fmt.Sprintf("%s: %s → %s", name, matcherLabel(source), matcherLabel(target)))
		}
	}
	return nil
}

// hooks lists the enabled hooks and webhooks of a project or repository.
func (ba *bitbucketAnalysis) hooks(name string, dir ...string) error {
	l := ba.l
	var hooks []bbHook
	if _, err := ba.work.Read(&hooks, append(dir, "hooks.json")...); err != nil {
		return err
	}
	known := []string{"needs-work", "incomplete-tasks", "required-approvers", "requiredapprovers", "required-builds", "requiredbuilds",
		"force-push", "unapprove"}
	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		label := cmp.Or(h.Details.Name, h.Details.Key)
		if slices.ContainsFunc(known, func(k string) bool { return strings.Contains(strings.ToLower(h.Details.Key), k) }) {
			ba.checks.add("", 500, name, label, l.t("the corresponding setting of the repository"))
			continue
		}
		ba.checks.add(Warn, 500, name, label, l.t("not migrated (a hook of an app)"))
		ba.find(l.t("hook"), label, name, l.t("not migrated"))
	}
	var webhooks []bbWebhook
	if _, err := ba.work.Read(&webhooks, append(dir, "webhooks.json")...); err != nil {
		return err
	}
	for _, h := range webhooks {
		ba.webhooks.item(name + ": " + h.Name + " (" + webhookTarget(h.URL) + ")")
	}
	return nil
}
