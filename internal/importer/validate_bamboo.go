// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/builds/spec"
)

// The validation of a Bamboo load: the plans were converted and proposed in their repositories (the proposals'
// pipeline files are valid and hold the pipelines), the environments exist, the plain variables are set, and the
// old addresses of plans, deployment projects and environments lead somewhere.

// Validate compares the extraction with the load in Kogge and writes bamboo-validation.json and
// bamboo-validation.html into the work directory.
func (b *Bamboo) Validate(ctx context.Context, l Loader, opt ValidateOptions) (*Validation, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bamboo", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bamboo extraction", b.work.Root)
	}
	if l.Code == nil {
		return nil, fmt.Errorf("the validation needs the code module")
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/")
	v := newValidation(ctx, "bamboo", base, b.work, l, opt)
	lang := v.l
	ld := &bambooLoad{Bamboo: b, l: l, source: v.source, links: map[string]string{}, known: map[string]bool{},
		project: map[string][]bambooVariable{}, codeProjects: map[string]map[string]bool{}}
	ld.rep = newReport(ld.source)
	plans, err := ld.plans()
	if err != nil {
		return nil, err
	}
	mapped, err := importedIDs(ctx, l.Pool, v.source, "plan", "repository")
	if err != nil {
		return nil, err
	}
	proposals, err := importedIDs(ctx, l.Pool, v.source, "pull-request", "pull-request")
	if err != nil {
		return nil, err
	}
	environments, err := importedIDs(ctx, l.Pool, v.source, "deployment-environment", "environment")
	if err != nil {
		return nil, err
	}
	pc := Check{ID: "proposals.pipelines", Group: "proposals", Title: lang.t("Plans proposed as pipelines")}
	ec := Check{ID: "proposals.environments", Group: "proposals", Title: lang.t("Environments of the deployment projects")}
	vc := Check{ID: "proposals.variables", Group: "proposals", Title: lang.t("Variables")}
	expected, bad, envs, envBad, vars, varBad := 0, 0, 0, 0, 0, 0
	byRepo := map[int64][]*convertedPlan{}
	repoNames := map[int64]string{}
	repoProjects := map[int64][]string{}
	for _, in := range plans {
		if in.plan.Specs == "" {
			continue // not exported by Bamboo: reported by the analysis and the load
		}
		target, err := ld.repository(in.plan)
		if err != nil {
			return nil, err
		}
		if target == "" {
			continue // the pipeline was written to the work directory
		}
		key, slug, _ := strings.Cut(target, "/")
		repoID, branch, err := l.Code.RepositoryID(ctx, key, slug)
		if err != nil {
			continue // the repository is not in Kogge: reported by the load
		}
		in.plan.DefaultBranch = branch
		cp, err := convertBambooPlan(in.plan)
		if err != nil {
			continue // reported by the load
		}
		expected++
		name := strings.ToUpper(key) + "/" + slug
		repoNames[repoID] = name
		byRepo[repoID] = append(byRepo[repoID], cp)
		repoProjects[repoID] = append(repoProjects[repoID], in.project)
		if got, ok := mapped[in.plan.Key]; !ok || got != repoID {
			bad++
			pc.detail(lang.t("%s: not recorded for %s (its old addresses do not lead there)", in.plan.Key, name))
		}
		for _, env := range cp.Environments {
			if env.SourceID == 0 {
				continue
			}
			envs++
			if _, ok := environments[strconv.FormatInt(env.SourceID, 10)]; !ok {
				envBad++
				ec.detail(lang.t("%s: the environment %s is missing", name, env.ID))
				continue
			}
			names, err := variableNames(ctx, l.Pool, builds.ScopeEnvironment, environments[strconv.FormatInt(env.SourceID, 10)])
			if err != nil {
				return nil, err
			}
			for _, variable := range env.Variables {
				vars++
				if !names[variable.Name] {
					varBad++
					vc.detail(lang.t("%s, environment %s: %s is not set", name, env.ID, variable.Name))
				}
			}
		}
	}
	for _, repoID := range slices.Sorted(mapKeysInt(byRepo)) {
		name := repoNames[repoID]
		prID, ok := proposals["pipelines:"+name]
		if !ok {
			bad++
			pc.detail(lang.t("%s: no pull request with the pipelines", name))
			continue
		}
		number, state, err := pullRequestState(ctx, l.Pool, prID)
		if err != nil {
			return nil, err
		}
		if state == "declined" {
			pc.detail(lang.t("%s: the pull request #%d was declined", name, number))
			continue
		}
		branch := "migration/bamboo-pipelines"
		if state == "merged" {
			_, def, err := l.Code.RepositoryID(ctx, strings.Split(name, "/")[0], strings.Split(name, "/")[1])
			if err != nil {
				return nil, err
			}
			branch = def
		}
		data, err := l.Code.ImportedFile(ctx, repoID, branch, spec.Path, 4<<20)
		if err != nil {
			bad++
			pc.detail(lang.t("%s: %s cannot be read on %s: %s", name, spec.Path, branch, err.Error()))
			continue
		}
		f, err := spec.Parse(data)
		if err != nil {
			bad++
			pc.detail(lang.t("%s: %s on %s is not valid: %s", name, spec.Path, branch, err.Error()))
			continue
		}
		for _, cp := range byRepo[repoID] {
			if !slices.ContainsFunc(f.Pipelines, func(p *spec.Pipeline) bool { return p.ID == cp.ID }) {
				bad++
				pc.detail(lang.t("%s: the pipeline %s is not in %s on %s", name, cp.ID, spec.Path, branch))
			}
		}
		// The plain variables of the Bamboo projects are variables of the repository.
		names, err := variableNames(ctx, l.Pool, builds.ScopeRepository, repoID)
		if err != nil {
			return nil, err
		}
		slices.Sort(repoProjects[repoID])
		for _, project := range slices.Compact(repoProjects[repoID]) {
			for _, variable := range ld.project[project] {
				if secretVariable(variable.Name, variable.Value) {
					continue
				}
				vars++
				if !names[koggeVar(variable.Name)] {
					varBad++
					vc.detail(lang.t("%s: %s is not set", name, koggeVar(variable.Name)))
				}
			}
		}
	}
	switch {
	case expected == 0:
		pc.Status, pc.Message = Warn, lang.t("No plan has a repository in Kogge: nothing was proposed.")
	case bad > 0:
		pc.Status, pc.Message = Fail, lang.t("%d problems with the proposals of %d plans.", bad, expected)
	default:
		pc.Status, pc.Message = Pass, lang.t("The %d plans are proposed as valid pipelines in their repositories, and their old addresses lead there.", expected)
	}
	v.add(pc)
	switch {
	case envBad > 0:
		ec.Status, ec.Message = Fail, lang.t("%d of %d environments are missing.", envBad, envs)
	default:
		ec.Status, ec.Message = Pass, lang.t("The %d environments exist.", envs)
	}
	v.add(ec)
	switch {
	case varBad > 0:
		vc.Status, vc.Message = Fail, lang.t("%d of %d plain variables are not set.", varBad, vars)
	default:
		vc.Status, vc.Message = Pass, lang.t("The %d plain variables are set (secrets are entered by hand).", vars)
	}
	v.add(vc)
	return v.Validation, v.finish()
}
