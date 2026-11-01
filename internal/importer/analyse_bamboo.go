// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
)

// The analysis of a Bamboo extraction converts every plan as the load would, and reports the pipelines, the
// repositories they are proposed for, the tasks without a counterpart, the environments, variables and secrets.

// Analyse reads the extraction (and the target installation, if given) and writes bamboo-analysis.json and
// bamboo-analysis.html into the work directory.
func (b *Bamboo) Analyse(ctx context.Context, target *Target, language string) (*Analysis, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bamboo", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bamboo extraction", b.work.Root)
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/")
	a, err := newAnalysis(ctx, "bamboo", base, b.work, language, target)
	if err != nil {
		return nil, err
	}
	l := a.l
	ld := &bambooLoad{Bamboo: b, source: "bamboo:" + base, links: map[string]string{}, known: map[string]bool{},
		project: map[string][]bambooVariable{}, codeProjects: map[string]map[string]bool{}}
	ld.rep = newReport(ld.source)
	plans, err := ld.plans()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	projects := map[string]bool{}
	tasks, placeholders := map[string]int{}, map[string]int{}
	labels := map[string][]string{}
	pt := Table{ID: "plans", Title: l.t("Plans"), Columns: []string{l.t("Plan"), l.t("Builds in Bamboo"), l.t("Repository"), l.t("Pipeline"),
		l.t("Steps to replace"), l.t("In Kogge")}}
	et := Table{ID: "environments", Title: l.t("Deployment environments"), Columns: []string{l.t("Plan"), l.t("Environment"), l.t("Variables"),
		l.t("In Kogge")}}
	notExported := Decision{Code: "plans-not-exported", Severity: "medium", Message: l.t("Bamboo could not export these plans as specs: convert them by hand (see specs-error.json in the work directory).")}
	noRepo := Decision{Code: "plans-without-repository", Severity: "medium", Message: l.t("No repository in Kogge was found for these plans: their pipelines are written to bamboo/proposals in the work directory instead of being proposed. Migrate the repositories from Bitbucket first, or map the plans with --repo PLAN=KEY/slug.")}
	secrets := Decision{Code: "secrets", Severity: "medium", Message: l.t("Bamboo does not reveal secrets: enter these variables in Kogge after the load.")}
	replace := Decision{Code: "placeholders", Severity: "medium", Message: l.t("Tasks without a counterpart become steps marked TODO that fail until they are replaced: the proposals cannot be merged as they are.")}
	for _, in := range plans {
		counts["plans"]++
		projects[in.project] = true
		builds := l.t("none")
		if in.sum.Builds > 0 {
			builds = l.t("%d, %d%% of the last ones successful", in.sum.Builds, int(in.sum.SuccessRate*100+0.5))
		}
		if in.plan.Specs == "" {
			notExported.item(in.plan.Key)
			pt.add(Warn, 0, keyed(in.plan.Name, in.plan.Key), builds, "", "", "", l.t("not exported by Bamboo: convert it by hand"))
			continue
		}
		counts["exported"]++
		repo, err := ld.repository(in.plan)
		if err != nil {
			return nil, err
		}
		cp, err := convertBambooPlan(in.plan)
		if err != nil {
			pt.add(Fail, 0, keyed(in.plan.Name, in.plan.Key), builds, repo, "", "", l.t("cannot be converted: %s", err.Error()))
			a.decide("plan-not-convertible", "medium", l.t("The plan %s cannot be converted: %s", in.plan.Key, err.Error()))
			continue
		}
		for kind, n := range cp.Tasks {
			tasks[kind] += n
		}
		for kind, n := range cp.PlaceholderTasks {
			placeholders[kind] += n
			replace.item(in.plan.Key + ": " + kind)
		}
		counts["placeholders"] += cp.Placeholders
		for _, lb := range cp.Labels {
			labels[lb] = append(labels[lb], in.plan.Key)
		}
		for _, s := range cp.Secrets {
			counts["secrets"]++
			secrets.item(in.plan.Key + ": " + s)
		}
		for _, n := range cp.Notes {
			a.find(l.t("plan setting"), n, in.plan.Key, l.t("noted in the proposal"))
		}
		how, class := "", ""
		switch {
		case repo == "":
			noRepo.item(in.plan.Key)
			how, class = l.t("no repository: the pipeline is written to the work directory"), Warn
		case target != nil:
			key, slug, _ := strings.Cut(repo, "/")
			id, err := codeProjectID(a.ctx, target.tx, key)
			if err != nil {
				return nil, err
			}
			repoID := int64(0)
			if id != 0 {
				if repoID, err = repositoryID(a.ctx, target.tx, id, slug); err != nil {
					return nil, err
				}
			}
			if repoID == 0 {
				noRepo.item(in.plan.Key + " → " + repo)
				how, class = l.t("the repository %s is not in Kogge: the pipeline is written to the work directory", repo), Warn
			} else {
				how = l.t("proposed as a pull request in %s", repo)
			}
		default:
			how = l.t("proposed as a pull request in %s, once it is in Kogge", repo)
		}
		if cp.Placeholders > 0 && class == "" {
			class = Warn
		}
		pt.add(class, 0, keyed(in.plan.Name, in.plan.Key), builds, repo, cp.ID, l.n(int64(cp.Placeholders)), how)
		for _, env := range cp.Environments {
			counts["environments"]++
			et.add("", 0, in.plan.Key, env.Name, l.n(int64(len(env.Variables))), l.t("the environment %s of the repository", env.ID))
		}
		counts["deployments"] += len(in.plan.Deployments)
	}
	tt := Table{ID: "tasks", Title: l.t("Tasks"), Columns: []string{l.t("Task"), l.t("Occurrences"), l.t("In Kogge")}}
	for _, kind := range slices.SortedFunc(mapKeys(tasks), func(x, y string) int { return cmp.Or(cmp.Compare(tasks[y], tasks[x]), cmp.Compare(x, y)) }) {
		how, class := l.t("converted"), ""
		if placeholders[kind] > 0 {
			how, class = l.t("a step marked TODO (%d of %d)", placeholders[kind], tasks[kind]), Warn
			a.declare(l.t("task"), kind, l.t("a step marked TODO that fails until it is replaced"))
			a.findings[l.t("task")+"\x00"+kind].Count += placeholders[kind]
		}
		tt.add(class, 0, kind, l.n(int64(tasks[kind])), how)
	}
	lt := Table{ID: "labels", Title: l.t("Agent labels"), Intro: l.t("Requirements of the jobs become labels that agents must have (runs-on)."),
		Columns: []string{l.t("Label"), l.t("Plans")}}
	for _, lb := range slices.Sorted(mapKeys(labels)) {
		lt.add("", 0, lb, strings.Join(slices.Compact(labels[lb]), ", "))
	}
	vt := Table{ID: "variables", Title: l.t("Variables"), Columns: []string{l.t("Scope"), l.t("Variables"), l.t("Secrets"), l.t("In Kogge")}}
	addVars := func(scope, kogge string, vars []bambooVariable) {
		plain, secret := 0, 0
		for _, v := range vars {
			if secretVariable(v.Name, v.Value) {
				secret++
				secrets.item(scope + ": " + koggeVar(v.Name))
				counts["secrets"]++
			} else {
				plain++
			}
		}
		counts["variables"] += plain
		vt.add("", 0, scope, l.n(int64(plain)), l.n(int64(secret)), kogge)
	}
	addVars(l.t("global"), l.t("variables of the installation"), ld.global)
	for _, p := range slices.Sorted(mapKeys(projects)) {
		addVars(l.t("project %s", p), l.t("variables of the repositories its plans build"), ld.project[p])
	}
	a.Translation = append(a.Translation, pt, tt, et, vt, lt)
	nm := Table{ID: "not-migrated", Title: l.t("Not migrated"), Columns: []string{l.t("What"), l.t("Note")}}
	for _, row := range [][2]string{
		{l.t("Plan permissions"), l.t("access follows the repository's permissions")},
		{l.t("Notifications of plans"), l.t("Kogge notifies the watchers of the repository")},
		{l.t("Plan dependencies, branch overrides and labels"), ""},
		{l.t("Release naming of deployment projects"), ""},
		{l.t("Build logs and artifacts of old builds"), l.t("the build history is summarised in the load's report")},
	} {
		nm.add("muted", 0, row[0], row[1])
	}
	a.Translation = append(a.Translation, nm)
	for _, d := range []Decision{notExported, noRepo, secrets, replace} {
		a.decision(d)
	}
	for _, k := range []struct{ key, label string }{{"plans", l.t("Plans")}, {"exported", l.t("Plans exported as specs")},
		{"deployments", l.t("Deployment projects")}, {"environments", l.t("Environments")}, {"variables", l.t("Variables")},
		{"secrets", l.t("Secrets to enter")}, {"placeholders", l.t("Steps to replace")}} {
		a.count(k.key, k.label, counts[k.key])
	}
	return a.Analysis, a.finish()
}
