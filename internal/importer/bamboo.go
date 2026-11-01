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
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/legacy"
)

// The Bamboo Data Center migration reads the projects, the plans as YAML specs with their build
// history, the deployment projects as YAML specs with their environments' variables, and the project, global and
// linked repository settings. The load converts the plans into pipelines and offers them as pull requests on the
// repositories the plans built (those migrated from Bitbucket, or as mapped by the administrator), creates the
// environments and sets the plain variables; secrets are listed for manual entry.

// BambooOptions configure a Bamboo migration.
type BambooOptions struct {
	URL      string
	Token    string
	Projects []string // Bamboo project keys; all when empty
	Workdir  string
	// Repositories maps plan keys to Kogge repositories ("KEY/slug"), for plans whose repository is not found by its
	// address.
	Repositories map[string]string
	// Restart begins the extraction afresh instead of resuming an interrupted one.
	Restart bool
}

// Bamboo migrates from Bamboo Data Center.
type Bamboo struct {
	opt  BambooOptions
	c    *Client
	work Workdir
	log  *slog.Logger
	cp   *checkpoint // of the running extraction
}

// NewBamboo creates a migration.
func NewBamboo(opt BambooOptions, log *slog.Logger) *Bamboo {
	return &Bamboo{opt: opt, c: NewClient(opt.URL, opt.Token, log), work: Workdir{Root: opt.Workdir}, log: log}
}

// PlanSummary describes a migrated build plan and its history in Bamboo.
type PlanSummary struct {
	Key            string     `json:"key"`
	Name           string     `json:"name"`
	Repository     string     `json:"repository,omitempty" doc:"KEY/slug of the repository the pipeline is proposed for"`
	Pipeline       string     `json:"pipeline,omitempty"`
	PullRequest    int        `json:"pullRequest,omitempty"`
	Placeholders   int        `json:"placeholders"`
	Builds         int        `json:"builds" doc:"Builds of the plan in Bamboo"`
	SuccessRate    float64    `json:"successRate" doc:"Share of successful builds among the last ones (0 to 1)"`
	LastSuccess    *time.Time `json:"lastSuccess,omitempty"`
	AverageSeconds int64      `json:"averageSeconds" doc:"Average duration of the last builds"`
}

type bbmPlan struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	ShortName string `json:"shortName"`
	Enabled   bool   `json:"enabled"`
}

type bbmProject struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type bbmDeployment struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	PlanKey struct {
		Key string `json:"key"`
	} `json:"planKey"`
	Environments []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"environments"`
}

type bbmSpecs struct {
	Spec struct {
		Code string `json:"code"`
	} `json:"spec"`
}

// list reads all pages of a list resource of Bamboo: {outer: {size, start-index, inner: [...]}}.
func (b *Bamboo) list(ctx context.Context, path, outer, inner string) ([]json.RawMessage, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var out []json.RawMessage
	for {
		raw, err := b.c.GetJSON(ctx, path+sep+"max-result=500&start-index="+strconv.Itoa(len(out)))
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var list map[string]json.RawMessage
		if err := json.Unmarshal(page[outer], &list); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var size int
		var items []json.RawMessage
		_ = json.Unmarshal(list["size"], &size)
		if err := json.Unmarshal(list[inner], &items); err != nil && list[inner] != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, items...)
		if len(items) == 0 || len(out) >= size {
			return out, nil
		}
	}
}

// optional stores a resource that may be missing (older versions) or forbidden (settings of administrators).
func (b *Bamboo) optional(ctx context.Context, path string, parts ...string) (json.RawMessage, error) {
	raw, err := b.c.GetJSON(ctx, path)
	var se *StatusError
	if errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusForbidden) {
		b.log.Debug("not available in the source", "resource", path, "status", se.Status)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, b.work.Write(raw, parts...)
}

// specs stores the YAML specs of a plan or deployment project; specs Bamboo cannot export are noted (and the
// specs of an earlier extraction removed, and the other way round).
func (b *Bamboo) specs(ctx context.Context, path string, parts ...string) error {
	raw, err := b.c.GetJSON(ctx, path)
	errFile := b.work.Path(append(parts[:len(parts)-1:len(parts)-1], "specs-error.json")...)
	var se *StatusError
	if errors.As(err, &se) && se.Status < 500 && se.Status != http.StatusUnauthorized {
		if err := os.Remove(b.work.Path(parts...)); err != nil && !os.IsNotExist(err) {
			return err
		}
		note, _ := json.Marshal(map[string]string{"error": err.Error()})
		return writeFile(errFile, note)
	}
	if err != nil {
		return err
	}
	if err := os.Remove(errFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return b.work.Write(raw, parts...)
}

// Extract reads the source into the work directory. An interrupted extraction resumes where it stopped (unless
// Restart is set).
func (b *Bamboo) Extract(ctx context.Context) error {
	cp, err := openCheckpoint(b.work, "bamboo", b.opt.Restart, nil, b.log)
	if err != nil {
		return err
	}
	defer cp.close()
	b.cp = cp
	err = cp.part("meta", func() error {
		info, err := b.c.GetJSON(ctx, "/rest/api/latest/info")
		if err != nil {
			return fmt.Errorf("the source does not answer like Bamboo Data Center: %w", err)
		}
		if err := b.work.Write(info, "bamboo", "info.json"); err != nil {
			return err
		}
		server, _ := json.Marshal(map[string]string{"baseUrl": b.c.BaseURL})
		if err := b.work.Write(server, "bamboo", "server.json"); err != nil {
			return err
		}
		if _, err := b.optional(ctx, "/rest/api/latest/repository", "bamboo", "repositories.json"); err != nil {
			return fmt.Errorf("linked repositories: %w", err)
		}
		if _, err := b.optional(ctx, "/rest/admin/latest/globalVariables", "bamboo", "global-variables.json"); err != nil {
			return fmt.Errorf("global variables: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	projects, err := b.list(ctx, "/rest/api/latest/project", "projects", "project")
	if err != nil {
		return fmt.Errorf("projects: %w", err)
	}
	found := map[string]bool{}
	for _, raw := range projects {
		var p bbmProject
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if len(b.opt.Projects) > 0 && !slices.ContainsFunc(b.opt.Projects, func(k string) bool { return strings.EqualFold(k, p.Key) }) {
			continue
		}
		if !bbName.MatchString(p.Key) {
			return fmt.Errorf("unexpected project key %q", p.Key)
		}
		found[strings.ToUpper(p.Key)] = true
		if err := cp.part("project/"+p.Key, func() error { return b.extractProject(ctx, p, raw) }); err != nil {
			return fmt.Errorf("project %s: %w", p.Key, err)
		}
	}
	for _, k := range b.opt.Projects {
		if !found[strings.ToUpper(k)] {
			return fmt.Errorf("the project %s does not exist or the token cannot read it", k)
		}
	}
	err = cp.part("deployments", func() error {
		deployments, err := b.c.GetJSON(ctx, "/rest/api/latest/deploy/project/all")
		if err != nil {
			return fmt.Errorf("deployment projects: %w", err)
		}
		var list []bbmDeployment
		if err := json.Unmarshal(deployments, &list); err != nil {
			return fmt.Errorf("deployment projects: %w", err)
		}
		kept := []bbmDeployment{}
		for _, d := range list {
			project, _, _ := strings.Cut(d.PlanKey.Key, "-")
			if !found[strings.ToUpper(project)] {
				continue
			}
			kept = append(kept, d)
			id := strconv.FormatInt(d.ID, 10)
			b.log.Info("extracting deployment project", "deployment", d.Name)
			if err := b.specs(ctx, "/rest/api/latest/deploy/project/"+id+"/specs?format=YAML", "bamboo", "deployments", id, "specs.json"); err != nil {
				return fmt.Errorf("deployment project %s: %w", d.Name, err)
			}
			for _, env := range d.Environments {
				envID := strconv.FormatInt(env.ID, 10)
				if _, err := b.optional(ctx, "/rest/api/latest/deploy/environment/"+envID+"/variables", "bamboo", "deployments", id,
					"variables-"+envID+".json"); err != nil {
					return fmt.Errorf("deployment project %s: %w", d.Name, err)
				}
			}
		}
		raw, _ := json.Marshal(kept)
		return b.work.Write(raw, "bamboo", "deployments.json")
	})
	if err != nil {
		return err
	}
	return cp.finish()
}

var planKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]+-[A-Za-z0-9]+$`)

func (b *Bamboo) extractProject(ctx context.Context, p bbmProject, raw json.RawMessage) error {
	b.log.Info("extracting project", "project", p.Key)
	dir := []string{"bamboo", "projects", p.Key}
	if err := b.work.Write(raw, append(dir, "project.json")...); err != nil {
		return err
	}
	if _, err := b.optional(ctx, "/rest/api/latest/project/"+url.PathEscape(p.Key)+"/variables", append(dir, "variables.json")...); err != nil {
		return fmt.Errorf("variables: %w", err)
	}
	plans, err := b.list(ctx, "/rest/api/latest/project/"+url.PathEscape(p.Key)+"?expand=plans", "plans", "plan")
	if err != nil {
		return fmt.Errorf("plans: %w", err)
	}
	for _, raw := range plans {
		var plan bbmPlan
		if err := json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		if !planKeyPattern.MatchString(plan.Key) {
			return fmt.Errorf("unexpected plan key %q", plan.Key)
		}
		err := b.cp.part("plan/"+plan.Key, func() error {
			pdir := append(slices.Clone(dir), "plans", plan.Key)
			if err := b.work.Write(raw, append(pdir, "plan.json")...); err != nil {
				return err
			}
			if err := b.specs(ctx, "/rest/api/latest/plan/"+plan.Key+"/specs?format=YAML", append(pdir, "specs.json")...); err != nil {
				return fmt.Errorf("plan %s: %w", plan.Key, err)
			}
			if _, err := b.optional(ctx, "/rest/api/latest/result/"+plan.Key+"?expand=results.result&max-results=100",
				append(pdir, "results.json")...); err != nil {
				return fmt.Errorf("plan %s: builds: %w", plan.Key, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// --- loading ---

type bambooLoad struct {
	*Bamboo
	l       Loader
	source  string
	rep     *Report
	f       *failures
	links   map[string]string // linked repository name → address
	known   map[string]bool   // variables defined in Bamboo, with "_" for "."
	global  []bambooVariable
	project map[string][]bambooVariable // by Bamboo project key
	// codeProjects are the code projects whose repositories the plans of a Bamboo project went to.
	codeProjects map[string]map[string]bool
}

// mapSource records where an entity of Bamboo went, for the redirects of its old addresses.
func (ld *bambooLoad) mapSource(ctx context.Context, typ, srcID, entity string, id int64) error {
	return legacy.Record(ctx, ld.l.Pool, ld.source, typ, srcID, entity, id)
}

// readVariables reads a list of variables: [{name, value}] or {variables: [...]}.
func (ld *bambooLoad) readVariables(parts ...string) ([]bambooVariable, error) {
	var raw json.RawMessage
	if ok, err := ld.work.Read(&raw, parts...); err != nil || !ok {
		return nil, err
	}
	var list []bambooVariable
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var wrapped struct {
		Variables []bambooVariable `json:"variables"`
	}
	err := json.Unmarshal(raw, &wrapped)
	return wrapped.Variables, err
}

// plans reads the extracted plans with their deployment projects, history and the variables they know. Plans
// Bamboo could not export have no specs.
func (ld *bambooLoad) plans() ([]*planInput, error) {
	if err := ld.readLinks(); err != nil {
		return nil, err
	}
	var err error
	if ld.global, err = ld.readVariables("bamboo", "global-variables.json"); err != nil {
		return nil, err
	}
	for _, v := range ld.global {
		ld.known[koggeVar(v.Name)] = true
	}
	var deployments []bbmDeployment
	if _, err := ld.work.Read(&deployments, "bamboo", "deployments.json"); err != nil {
		return nil, err
	}
	var plans []*planInput
	for _, key := range ld.work.List("bamboo", "projects") {
		if len(ld.opt.Projects) > 0 && !slices.ContainsFunc(ld.opt.Projects, func(k string) bool { return strings.EqualFold(k, key) }) {
			continue
		}
		vars, err := ld.readVariables("bamboo", "projects", key, "variables.json")
		if err != nil {
			return nil, err
		}
		ld.project[key] = vars
		for _, v := range vars {
			ld.known[koggeVar(v.Name)] = true
		}
		for _, planKey := range ld.work.List("bamboo", "projects", key, "plans") {
			dir := []string{"bamboo", "projects", key, "plans", planKey}
			var meta bbmPlan
			if _, err := ld.work.Read(&meta, append(dir, "plan.json")...); err != nil {
				return nil, err
			}
			in := &planInput{project: key, plan: bambooPlan{Key: planKey, Name: cmp.Or(meta.Name, planKey), Enabled: meta.Enabled, Known: ld.known},
				sum: PlanSummary{Key: planKey, Name: cmp.Or(meta.Name, planKey)}}
			ld.history(&in.sum, dir...)
			plans = append(plans, in)
			var specs bbmSpecs
			if ok, err := ld.work.Read(&specs, append(dir, "specs.json")...); err != nil {
				return nil, err
			} else if !ok || specs.Spec.Code == "" {
				continue
			}
			in.plan.Specs = specs.Spec.Code
			for _, d := range deployments {
				if d.PlanKey.Key != planKey {
					continue
				}
				dep, err := ld.deployment(d)
				if err != nil {
					return nil, err
				}
				if dep != nil {
					in.plan.Deployments = append(in.plan.Deployments, *dep)
				}
			}
		}
	}
	return plans, nil
}

// Load converts the plans and offers them as pull requests. Loads can be repeated: open proposals are updated.
func (b *Bamboo) Load(ctx context.Context, l Loader) (*Report, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bamboo", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bamboo extraction", b.work.Root)
	}
	if l.Code == nil || l.Builds == nil {
		return nil, errors.New("the load needs the code and builds modules")
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/")
	ld := &bambooLoad{Bamboo: b, l: l, source: "bamboo:" + base, links: map[string]string{}, known: map[string]bool{},
		project: map[string][]bambooVariable{}, codeProjects: map[string]map[string]bool{}}
	ld.rep = newReport(ld.source)
	ld.f = l.failures(ld.rep, nil)
	all, err := ld.plans()
	if err != nil {
		return nil, err
	}
	var plans []*planInput
	for _, in := range all {
		if in.plan.Specs == "" {
			ld.rep.warn("%s: Bamboo could not export the plan as specs (see specs-error.json); convert it by hand", in.plan.Key)
			ld.rep.Plans = append(ld.rep.Plans, in.sum)
			continue
		}
		plans = append(plans, in)
	}

	// Plans grouped by repository; each repository gets one proposal.
	byRepo := map[int64][]*planInput{}
	var repoOrder []int64
	repoNames := map[int64]string{}
	for _, in := range plans {
		target, err := ld.repository(in.plan)
		if err != nil {
			return nil, err
		}
		if target == "" {
			ld.unmapped(in.plan, &in.sum)
			continue
		}
		key, slug, _ := strings.Cut(target, "/")
		target = strings.ToUpper(key) + "/" + slug
		id, branch, err := l.Code.RepositoryID(ctx, key, slug)
		if err != nil {
			ld.rep.warn("%s: the repository %s is not in Kogge (migrate it from Bitbucket first, or map the plan with -repo)", in.plan.Key, target)
			ld.unmapped(in.plan, &in.sum)
			continue
		}
		in.repoID, in.plan.DefaultBranch, in.sum.Repository = id, branch, target
		if _, ok := byRepo[id]; !ok {
			repoOrder = append(repoOrder, id)
			repoNames[id] = in.sum.Repository
		}
		byRepo[id] = append(byRepo[id], in)
	}
	for _, repoID := range repoOrder {
		group := byRepo[repoID]
		var converted []*convertedPlan
		var proposal []*planInput
		for _, in := range group {
			cp, err := convertBambooPlan(in.plan)
			if err != nil {
				ld.rep.warn("%s: %v", in.plan.Key, err)
				ld.rep.Plans = append(ld.rep.Plans, in.sum)
				continue
			}
			converted = append(converted, cp)
			proposal = append(proposal, in)
			in.sum.Pipeline, in.sum.Placeholders = cp.ID, cp.Placeholders
		}
		if len(converted) == 0 {
			continue
		}
		number, err := ld.propose(ctx, repoID, repoNames[repoID], converted)
		if err != nil {
			// The proposal of one repository failing does not keep the others from being made.
			if err := ld.f.record("proposal", repoNames[repoID], repoNames[repoID], err); err != nil {
				return ld.rep, err
			}
			continue
		}
		for _, in := range proposal {
			in.sum.PullRequest = number
			ld.rep.Plans = append(ld.rep.Plans, in.sum)
			if err := ld.mapSource(ctx, "plan", in.plan.Key, "repository", repoID); err != nil {
				return ld.rep, err
			}
			for _, d := range in.plan.Deployments {
				if err := ld.mapSource(ctx, "deployment-project", strconv.FormatInt(d.ID, 10), "repository", repoID); err != nil {
					return ld.rep, err
				}
			}
			if ld.codeProjects[in.project] == nil {
				ld.codeProjects[in.project] = map[string]bool{}
			}
			key, _, _ := strings.Cut(in.sum.Repository, "/")
			ld.codeProjects[in.project][key] = true
		}
		// Variables of the Bamboo projects become variables of the repositories they built.
		projects := map[string]bool{}
		for _, in := range proposal {
			projects[in.project] = true
		}
		for _, project := range sortedKeys(projects) {
			ld.setVariables(ctx, builds.ScopeRepository, repoID, ld.project[project], "project "+project)
		}
	}
	ld.setVariables(ctx, builds.ScopeInstance, 0, ld.global, "global variables")
	// A Bamboo project whose plans went to repositories of one code project leads there.
	for _, project := range slices.Sorted(maps.Keys(ld.codeProjects)) {
		if keys := ld.codeProjects[project]; len(keys) == 1 {
			for key := range keys {
				id, err := codeProjectID(ctx, l.Pool, key)
				if err != nil {
					return ld.rep, err
				}
				if id != 0 {
					if err := ld.mapSource(ctx, "plan-project", strings.ToUpper(project), "code-project", id); err != nil {
						return ld.rep, err
					}
				}
			}
		}
	}
	ld.rep.Counts["plans"] = len(ld.rep.Plans)
	slices.SortFunc(ld.rep.Plans, func(a, b PlanSummary) int { return strings.Compare(a.Key, b.Key) })
	err = ld.rep.Write(b.work, "bamboo-report.json")
	return ld.rep, err
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// readLinks reads the linked repositories: their names are what plans refer to.
func (ld *bambooLoad) readLinks() error {
	var raw json.RawMessage
	if ok, err := ld.work.Read(&raw, "bamboo", "repositories.json"); err != nil || !ok {
		return err
	}
	type link struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	var shapes struct {
		SearchResults []link `json:"searchResults"`
		AllElements   []link `json:"allElements"`
		Repositories  struct {
			Repository []link `json:"repository"`
		} `json:"repositories"`
	}
	var list []link
	if json.Unmarshal(raw, &list) != nil {
		if err := json.Unmarshal(raw, &shapes); err != nil {
			return fmt.Errorf("linked repositories: %w", err)
		}
		list = slices.Concat(shapes.SearchResults, shapes.AllElements, shapes.Repositories.Repository)
	}
	for _, l := range list {
		if l.Name != "" && l.URL != "" {
			ld.links[l.Name] = l.URL
		}
	}
	return nil
}

var (
	bitbucketBrowse = regexp.MustCompile(`/projects/([^/]+)/repos/([^/]+)`)
	gitAddress      = regexp.MustCompile(`/(?:scm/)?([^/]+)/([^/]+?)(?:\.git)?/?$`)
)

// repoOf reads "KEY/slug" from the address of a repository (Bitbucket or Kogge, HTTP or SSH).
func repoOf(address string) string {
	if strings.HasPrefix(address, "git@") { // git@host:key/slug.git
		if _, p, ok := strings.Cut(address, ":"); ok {
			address = "ssh://x/" + p
		}
	}
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	if m := bitbucketBrowse.FindStringSubmatch(u.Path); m != nil {
		return m[1] + "/" + m[2]
	}
	if m := gitAddress.FindStringSubmatch(u.Path); m != nil && m[1] != "" {
		return m[1] + "/" + m[2]
	}
	return ""
}

// repository finds the repository a plan built: mapped by the administrator, or the plan's first repository.
func (ld *bambooLoad) repository(p bambooPlan) (string, error) {
	for k, v := range ld.opt.Repositories {
		if strings.EqualFold(k, p.Key) {
			return v, nil
		}
	}
	docs, err := yamlDocs(p.Specs)
	if err != nil {
		return "", nil // reported by the conversion
	}
	for _, doc := range docs {
		for _, r := range yentries(yget(doc, "repositories")) {
			address := ""
			if r.value != nil && r.value.Kind == yaml.MappingNode {
				address = ystr(yget(r.value, "url"))
				if project, slug := ystr(yget(r.value, "project")), cmp.Or(ystr(yget(r.value, "slug")), ystr(yget(r.value, "repository-slug"))); project != "" &&
					slug != "" {
					return project + "/" + slug, nil
				}
			}
			if address == "" {
				address = ld.links[r.name]
			}
			if target := repoOf(address); target != "" {
				return target, nil
			}
			ld.rep.warn("%s: the address of its repository %q is unknown; map the plan to a repository with -repo", p.Key, r.name)
			return "", nil
		}
	}
	ld.rep.warn("%s: the plan has no repository; map it to one with -repo", p.Key)
	return "", nil
}

// unmapped keeps the converted pipeline of a plan without a repository in the work directory.
func (ld *bambooLoad) unmapped(p bambooPlan, sum *PlanSummary) {
	if cp, err := convertBambooPlan(p); err == nil {
		sum.Pipeline, sum.Placeholders = cp.ID, cp.Placeholders
		if data, err := pipelineFile("Converted from Bamboo: "+p.Key+". No repository was found for it.", []*convertedPlan{cp}); err == nil {
			path := ld.work.Path("bamboo", "proposals", p.Key+".yml")
			if err := writeFile(path, data); err == nil {
				ld.rep.warn("%s: the converted pipeline is in %s", p.Key, path)
			}
		}
	}
	ld.rep.Plans = append(ld.rep.Plans, *sum)
}

// deployment reads a deployment project with the variables of its environments.
func (ld *bambooLoad) deployment(d bbmDeployment) (*bambooDeployment, error) {
	id := strconv.FormatInt(d.ID, 10)
	var specs bbmSpecs
	if ok, err := ld.work.Read(&specs, "bamboo", "deployments", id, "specs.json"); err != nil {
		return nil, err
	} else if !ok || specs.Spec.Code == "" {
		ld.rep.warn("deployment project %q: Bamboo could not export it as specs; set up its deployments by hand", d.Name)
		return nil, nil
	}
	out := &bambooDeployment{ID: d.ID, Name: d.Name, Specs: specs.Spec.Code, Variables: map[string][]bambooVariable{},
		Environments: map[string]int64{}}
	for _, env := range d.Environments {
		out.Environments[env.Name] = env.ID
		vars, err := ld.readVariables("bamboo", "deployments", id, "variables-"+strconv.FormatInt(env.ID, 10)+".json")
		if err != nil {
			return nil, err
		}
		out.Variables[env.Name] = vars
		for _, v := range vars {
			ld.known[koggeVar(v.Name)] = true
		}
	}
	return out, nil
}

// history summarises the last builds of a plan.
func (ld *bambooLoad) history(sum *PlanSummary, dir ...string) {
	var results struct {
		Results struct {
			Size   int `json:"size"`
			Result []struct {
				State                  string `json:"state"`
				BuildState             string `json:"buildState"`
				BuildCompletedTime     string `json:"buildCompletedTime"`
				BuildDurationInSeconds int64  `json:"buildDurationInSeconds"`
			} `json:"result"`
		} `json:"results"`
	}
	if ok, err := ld.work.Read(&results, append(dir, "results.json")...); err != nil || !ok {
		return
	}
	list := results.Results.Result
	sum.Builds = max(results.Results.Size, len(list))
	if len(list) == 0 {
		return
	}
	ok := 0
	var total int64
	for _, r := range list {
		total += r.BuildDurationInSeconds
		if cmp.Or(r.BuildState, r.State) != "Successful" {
			continue
		}
		ok++
		if t := parseTime(r.BuildCompletedTime); t != nil && (sum.LastSuccess == nil || t.After(*sum.LastSuccess)) {
			sum.LastSuccess = t
		}
	}
	sum.SuccessRate = float64(ok) / float64(len(list))
	sum.AverageSeconds = total / int64(len(list))
}

// setVariables sets plain variables; secrets are listed for manual entry.
func (ld *bambooLoad) setVariables(ctx context.Context, scope string, scopeID int64, vars []bambooVariable, owner string) {
	for _, v := range vars {
		name := koggeVar(v.Name)
		if secretVariable(v.Name, v.Value) {
			ld.rep.warn("%s: the secret %s cannot be read from Bamboo; enter it in Kogge", owner, name)
			ld.rep.Counts["secrets"]++
			continue
		}
		set, err := ld.l.Builds.ImportVariable(ctx, scope, scopeID, name, v.Value)
		if err != nil {
			ld.rep.warn("%s: the variable %s: %v", owner, name, err)
			continue
		}
		if set {
			ld.rep.Counts["variables"]++
		}
	}
}

// propose offers the converted pipelines of a repository as a pull request and sets up their environments.
func (ld *bambooLoad) propose(ctx context.Context, repoID int64, repo string, converted []*convertedPlan) (int, error) {
	header := "Pipelines converted from Bamboo (" + strings.TrimPrefix(ld.source, "bamboo:") + ") by the migration.\n" +
		"Replace the steps marked TODO and read the notes of the pull request before merging."
	data, err := pipelineFile(header, converted)
	if err != nil {
		// Proposed all the same: the pull request shows where the file needs work.
		ld.rep.warn("%s: %v", repo, err)
	}
	for _, cp := range converted {
		for _, env := range cp.Environments {
			envID, err := ld.l.Builds.ImportEnvironment(ctx, repoID, env.ID, "Migrated from Bamboo: "+env.Name)
			if err != nil {
				ld.rep.warn("%s: the environment %s: %v", repo, env.ID, err)
				continue
			}
			if env.SourceID != 0 {
				if err := ld.mapSource(ctx, "deployment-environment", strconv.FormatInt(env.SourceID, 10), "environment", envID); err != nil {
					return 0, err
				}
			}
			ld.rep.Counts["environments"]++
			ld.setVariables(ctx, builds.ScopeEnvironment, envID, env.Variables, repo+" environment "+env.ID)
		}
		for _, secret := range cp.Secrets {
			ld.rep.warn("%s: the secret %s cannot be read from Bamboo; enter it in Kogge", repo, secret)
			ld.rep.Counts["secrets"]++
		}
		for _, n := range cp.Notes {
			ld.rep.Unsupported[n]++
		}
		ld.rep.Counts["placeholders"] += cp.Placeholders
	}
	number, err := ld.l.Code.Propose(ctx, ld.source, repoID, code.Proposal{SrcID: "pipelines:" + repo, Branch: "migration/bamboo-pipelines",
		Path: spec.Path, Content: data, Message: "Convert the Bamboo plans into pipelines", Title: "Pipelines converted from Bamboo",
		Description: docmodel.FromMarkdownWith(proposalText(converted), docmodel.MarkdownOptions{NoMath: true})})
	if err != nil {
		return 0, fmt.Errorf("%s: %w", repo, err)
	}
	ld.rep.Counts["pipelines"] += len(converted)
	if number != 0 {
		ld.rep.Counts["pull-requests"]++
	} else {
		ld.rep.warn("%s: the proposal was merged or declined before, or the pipelines are there already; it is not made again", repo)
	}
	return number, nil
}

// proposalText is the description of a proposal: what to do before merging. It quotes names and notes of Bamboo,
// whose dollars ($HOME, ${bamboo.x}) are text: it is read without formulas.
func proposalText(converted []*convertedPlan) string {
	var b strings.Builder
	b.WriteString("The migration converted the Bamboo plans of this repository into pipelines:\n\n")
	var labels, secrets, envs, notes []string
	placeholders := 0
	for _, cp := range converted {
		fmt.Fprintf(&b, "- `%s`: %s\n", cp.ID, strings.TrimSuffix(strings.TrimPrefix(cp.Comment, "Converted from the "), "."))
		for _, l := range cp.Labels {
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
		secrets = append(secrets, cp.Secrets...)
		for _, e := range cp.Environments {
			envs = append(envs, "`"+e.ID+"`")
		}
		notes = append(notes, cp.Notes...)
		placeholders += cp.Placeholders
	}
	b.WriteString("\n**Before merging**\n\n")
	if placeholders > 0 {
		fmt.Fprintf(&b, "- Replace the %d steps marked *TODO*: they stand for Bamboo tasks without a counterpart and fail until then.\n", placeholders)
	}
	if len(labels) > 0 {
		slices.Sort(labels)
		fmt.Fprintf(&b, "- Jobs ask for agents with the labels %s (from the requirements of the Bamboo jobs).\n", "`"+strings.Join(labels, "`, `")+"`")
	}
	if len(secrets) > 0 {
		fmt.Fprintf(&b, "- Enter the secrets Bamboo does not reveal: %s.\n", strings.Join(secrets, ", "))
	}
	if len(envs) > 0 {
		fmt.Fprintf(&b, "- Decide who may deploy to the environments %s (repository settings → Environments).\n", strings.Join(envs, ", "))
	}
	b.WriteString("- Run the pipelines on this branch to try them.\n")
	if len(notes) > 0 {
		b.WriteString("\n**Notes**\n\n")
		for _, n := range notes {
			b.WriteString("- " + n + "\n")
		}
	}
	return b.String()
}

// planInput is a plan on its way into a proposal.
type planInput struct {
	plan    bambooPlan
	project string
	sum     PlanSummary
	repoID  int64
}
