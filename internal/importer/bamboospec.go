// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"kogge.de/kogge/internal/builds/spec"
)

// The conversion of Bamboo plans into Kogge pipelines reads the YAML specs Bamboo exports for a
// plan (version 2) and for its deployment projects. A plan becomes a pipeline: stages, jobs, script and build tool
// tasks, artifacts, test reports, Docker images, requirements (as agent labels), triggers and plan variables.
// Deployment projects become deployment stages at the end of the pipeline, one per environment. Tasks without a
// counterpart become steps that fail with a note, so nothing is skipped silently.

// bambooPlan is what the conversion of a plan needs.
type bambooPlan struct {
	Key, Name     string
	Enabled       bool
	Specs         string // the plan's YAML specs
	Deployments   []bambooDeployment
	DefaultBranch string          // of the repository the pipeline is proposed for
	Known         map[string]bool // variables defined in Bamboo (plan, project, global, environments), as names with "_"
}

// bambooDeployment is a deployment project of the plan.
type bambooDeployment struct {
	ID           int64 // in Bamboo
	Name         string
	Specs        string
	Variables    map[string][]bambooVariable // by environment name
	Environments map[string]int64            // Bamboo's ids of the environments, by name
}

type bambooVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// convertedPlan is the pipeline proposed for a plan, with what the migration sets up around it.
type convertedPlan struct {
	ID           string
	Comment      string
	Pipeline     *yaml.Node
	Environments []convertedEnvironment
	Secrets      []string // variables to enter in Kogge by hand
	Notes        []string // for the report and the pull request
	Placeholders int      // steps that stand for tasks without a counterpart
	Labels       []string // agent labels the pipeline asks for

	// Tasks counts the plan's tasks by type, PlaceholderTasks those of them without a counterpart (for the
	// analysis).
	Tasks, PlaceholderTasks map[string]int
}

type convertedEnvironment struct {
	ID, Name  string
	SourceID  int64 // the environment in Bamboo, if known
	Variables []bambooVariable
}

// --- reading YAML ---

// yamlDocs reads the documents of a specs export (a plan and its permissions are separate documents).
func yamlDocs(data string) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(data))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(n.Content) > 0 && n.Content[0].Kind == yaml.MappingNode {
			docs = append(docs, n.Content[0])
		}
	}
}

func yget(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func ystr(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

func ybool(n *yaml.Node) bool {
	v := strings.ToLower(ystr(n))
	return v == "true" || v == "yes"
}

// ystrs reads a scalar or a list of scalars.
func ystrs(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		if v := ystr(n); v != "" {
			return []string{v}
		}
		return nil
	}
	var out []string
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			if v := ystr(c); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// yentry is an item of a list that is either a name ("- checkout") or a name with a value ("- script: …").
type yentry struct {
	name  string
	value *yaml.Node
}

func yentries(n *yaml.Node) []yentry {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []yentry
	for _, item := range n.Content {
		switch {
		case item.Kind == yaml.ScalarNode:
			out = append(out, yentry{name: item.Value})
		case item.Kind == yaml.MappingNode && len(item.Content) == 2:
			out = append(out, yentry{name: item.Content[0].Value, value: item.Content[1]})
		case item.Kind == yaml.MappingNode:
			out = append(out, yentry{value: item})
		}
	}
	return out
}

// ypairs lists the keys and values of a mapping in order.
func ypairs(n *yaml.Node) []yentry {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]yentry, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, yentry{name: n.Content[i].Value, value: n.Content[i+1]})
	}
	return out
}

// --- writing YAML ---

func ynode(v any) *yaml.Node {
	switch v := v.(type) {
	case *yaml.Node:
		return v
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		if strings.Contains(strings.TrimRight(v, "\n"), "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
	case []string:
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range v {
			n.Content = append(n.Content, ynode(s))
		}
		return n
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}
	}
	panic(fmt.Sprintf("ynode: %T", v))
}

// ymap builds a mapping of keys and values; nil values are left out.
func ymap(kv ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			continue
		}
		if n, ok := kv[i+1].(*yaml.Node); ok && n == nil {
			continue
		}
		yset(m, kv[i].(string), ynode(kv[i+1]))
	}
	return m
}

func yset(m *yaml.Node, key string, v *yaml.Node) {
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func yseq(items []*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

// --- names ---

// koggeVar is the name a Bamboo variable gets in Kogge: dots and other characters become underscores.
func koggeVar(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	if strings.HasPrefix(s, "KOGGE_") || s == "CI" {
		s = "BAMBOO_" + s
	}
	return s[:min(len(s), 128)]
}

// secretVariable reports variables whose values Bamboo does not reveal: encrypted in specs, masked in its API,
// or named like secrets (which Bamboo masks).
func secretVariable(name, value string) bool {
	return strings.HasPrefix(value, "BAMSCRT@") || (value != "" && strings.Trim(value, "*") == "") ||
		regexp.MustCompile(`(?i)password|secret|passphrase|token|ssh_?key|private`).MatchString(name)
}

// koggeID makes an identifier such as a pipeline id or an environment name: lower case, digits, "-" (and "_").
func koggeID(s string, underscore bool) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || underscore && r == '_'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(b.String(), "-_")
	if id == "" {
		id = "x"
	}
	return strings.TrimRight(id[:min(len(id), 63)], "-_")
}

// koggeName makes a stage or job name: no "/" and no control characters, at most 100 characters.
func koggeName(s string, taken map[string]bool) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' {
			return '-'
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if s == "" {
		s = "job"
	}
	s = string([]rune(s)[:min(len([]rune(s)), 90)])
	name := s
	for i := 2; taken[name]; i++ {
		name = s + " " + strconv.Itoa(i)
	}
	taken[name] = true
	return name
}

// label turns a requirement into an agent label: "system.builder.mvn3.Maven 3" → "maven-3", "os: Linux" → "os-linux".
func label(key, value string) string {
	k := key
	switch {
	case strings.HasPrefix(k, "system.builder."):
		if parts := strings.SplitN(k, ".", 4); len(parts) == 4 {
			k = parts[3]
		}
	case strings.HasPrefix(k, "system.jdk."):
		k = strings.TrimPrefix(k, "system.jdk.")
	case strings.HasPrefix(k, "system.") && strings.HasSuffix(k, ".executable"):
		k = strings.TrimSuffix(strings.TrimPrefix(k, "system."), ".executable")
	case strings.HasPrefix(k, "system."):
		k = k[strings.LastIndex(k, ".")+1:]
	}
	if value != "" {
		// Requirements match values with regular expressions; their literal parts make the label.
		value = regexp.MustCompile(`\\[a-zA-Z][+*?]?|\.\*|[\[\](){}^$|+*?]`).ReplaceAllString(value, " ")
		k += " " + value
	}
	return koggeID(k, false)
}

// --- variables in scripts ---

// Built-in variables of Bamboo (with "_" for ".") and their counterparts.
var bambooBuiltins = map[string]string{
	"buildNumber": "KOGGE_RUN_NUMBER", "buildResultsUrl": "KOGGE_RUN_URL", "resultsUrl": "KOGGE_RUN_URL",
	"planRepository_branch": "KOGGE_BRANCH", "planRepository_branchName": "KOGGE_BRANCH", "planRepository_1_branch": "KOGGE_BRANCH",
	"planRepository_1_branchName": "KOGGE_BRANCH", "repository_branch_name": "KOGGE_BRANCH",
	"planRepository_revision": "KOGGE_COMMIT", "planRepository_1_revision": "KOGGE_COMMIT", "repository_revision_number": "KOGGE_COMMIT",
	"build_working_directory": "KOGGE_WORKSPACE", "shortJobName": "KOGGE_JOB", "shortPlanName": "KOGGE_PIPELINE",
	"repository_pr_key": "KOGGE_PULL_REQUEST", "repository_pr_targetBranch": "KOGGE_PULL_REQUEST_TARGET",
	"deploy_environment": "KOGGE_ENVIRONMENT",
}

// Other built-in variables, recognised by their first part, have no counterpart.
var bambooBuiltinPrefixes = []string{"agent", "agentId", "agentWorkingDirectory", "build", "buildKey", "buildPlanName", "buildResultKey",
	"buildTimeStamp", "capability", "dependency", "deploy", "ManualBuildTriggerReason", "planKey", "planName", "planRepository",
	"repository", "shortJobKey", "shortPlanKey", "system", "tmp"}

var (
	bambooRef      = regexp.MustCompile(`\$\{bamboo\.([A-Za-z0-9_.-]+)\}`)
	bambooShellRef = regexp.MustCompile(`\$\{bamboo_([A-Za-z0-9_]+)\}|\$bamboo_([A-Za-z0-9_]+)`)
	bambooPwshRef  = regexp.MustCompile(`\$env:bamboo_([A-Za-z0-9_]+)`)
	bambooCmdRef   = regexp.MustCompile(`%bamboo_([A-Za-z0-9_]+)%`)
)

type converter struct {
	plan    bambooPlan
	out     *convertedPlan
	noted   map[string]bool
	labels  map[string]bool
	sources map[string]string // artifact name → "stage/job" that shares it
}

func (c *converter) note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !c.noted[msg] {
		c.noted[msg] = true
		c.out.Notes = append(c.out.Notes, msg)
	}
}

// variable is the Kogge name of a Bamboo variable used in a script.
func (c *converter) variable(name string) string {
	under := strings.NewReplacer(".", "_", "-", "_").Replace(name)
	if k, ok := bambooBuiltins[under]; ok {
		return k
	}
	if !c.plan.Known[under] {
		for _, p := range bambooBuiltinPrefixes {
			if under == p || strings.HasPrefix(under, p+"_") {
				c.note("The Bamboo variable %s has no counterpart; scripts that use it need a change.", name)
				break
			}
		}
	}
	return koggeVar(under)
}

// rewrite replaces Bamboo's variable references in a script by references to Kogge's variables.
func (c *converter) rewrite(script, shell string) string {
	ref := func(name string) string {
		switch shell {
		case "pwsh":
			return "$env:" + name
		case "cmd":
			return "%" + name + "%"
		}
		return "${" + name + "}"
	}
	script = bambooRef.ReplaceAllStringFunc(script, func(m string) string { return ref(c.variable(bambooRef.FindStringSubmatch(m)[1])) })
	script = bambooPwshRef.ReplaceAllStringFunc(script, func(m string) string {
		return "$env:" + c.variable(bambooPwshRef.FindStringSubmatch(m)[1])
	})
	script = bambooCmdRef.ReplaceAllStringFunc(script, func(m string) string { return "%" + c.variable(bambooCmdRef.FindStringSubmatch(m)[1]) + "%" })
	return bambooShellRef.ReplaceAllStringFunc(script, func(m string) string {
		sm := bambooShellRef.FindStringSubmatch(m)
		return "${" + c.variable(sm[1]+sm[2]) + "}"
	})
}

// --- conversion ---

// convertBambooPlan converts the specs of a plan and its deployment projects into a pipeline.
func convertBambooPlan(p bambooPlan) (*convertedPlan, error) {
	docs, err := yamlDocs(p.Specs)
	if err != nil {
		return nil, fmt.Errorf("the specs of %s are not YAML: %w", p.Key, err)
	}
	var doc *yaml.Node
	for _, d := range docs {
		if yget(d, "stages") != nil || yget(d, "plan") != nil && yget(d, "plan-permissions") == nil {
			doc = d
			break
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("the specs of %s hold no plan", p.Key)
	}
	if v := ystr(yget(doc, "version")); v != "" && v != "2" {
		return nil, fmt.Errorf("the specs of %s have version %s; only version 2 is read", p.Key, v)
	}
	known := map[string]bool{}
	for k := range p.Known {
		known[k] = true
	}
	for _, kv := range ypairs(yget(doc, "variables")) {
		known[koggeVar(kv.name)] = true
	}
	p.Known = known
	c := &converter{plan: p, out: &convertedPlan{ID: koggeID(p.Key, false), Tasks: map[string]int{},
		PlaceholderTasks: map[string]int{}}, noted: map[string]bool{}, labels: map[string]bool{},
		sources: map[string]string{}}
	c.out.Comment = fmt.Sprintf("Converted from the Bamboo plan %s (%s).", p.Key, p.Name)
	pipeline := ymap("name", strings.TrimSpace(cmp.Or(p.Name, p.Key)))
	yset(pipeline, "on", c.triggers(doc))
	if vars := c.variables(yget(doc, "variables"), "plan "+p.Key); len(vars.Content) > 0 {
		yset(pipeline, "variables", vars)
	}
	stageNames := map[string]bool{}
	var stages []*yaml.Node
	for _, st := range yentries(yget(doc, "stages")) {
		stage := c.stage(doc, st, stageNames)
		if stage != nil {
			stages = append(stages, stage)
		}
	}
	for _, d := range p.Deployments {
		deployStages, err := c.deployment(d, len(p.Deployments) > 1, stageNames)
		if err != nil {
			return nil, err
		}
		stages = append(stages, deployStages...)
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("the plan %s has no stages", p.Key)
	}
	yset(pipeline, "stages", yseq(stages))
	for _, key := range []string{"notifications", "labels", "branch-overrides", "dependencies"} {
		if n := yget(doc, key); n != nil && len(n.Content) > 0 {
			c.note("%s", map[string]string{"notifications": "Notifications of the plan are not converted; Kogge notifies watchers of the repository.",
				"labels": "Labels of the plan are not converted.", "branch-overrides": "Branch overrides of the plan are not converted.",
				"dependencies": "The plan started other plans after a successful build; Kogge pipelines do not start each other yet."}[key])
		}
	}
	c.out.Pipeline = pipeline
	for l := range c.labels {
		c.out.Labels = append(c.out.Labels, l)
	}
	slices.Sort(c.out.Labels)
	return c.out, nil
}

// variables converts plain plan or environment variables; secrets are listed for manual entry.
func (c *converter) variables(n *yaml.Node, owner string) *yaml.Node {
	m := ymap()
	for _, kv := range ypairs(n) {
		name, value := koggeVar(kv.name), ystr(kv.value)
		if secretVariable(kv.name, value) {
			c.out.Secrets = append(c.out.Secrets, fmt.Sprintf("%s (%s)", name, owner))
			continue
		}
		yset(m, name, ynode(value))
	}
	return m
}

// triggers converts the plan's triggers and branch settings.
func (c *converter) triggers(doc *yaml.Node) *yaml.Node {
	on := ymap()
	if !c.plan.Enabled {
		c.note("The plan was disabled in Bamboo; the pipeline only runs when started by hand.")
		yset(on, "manual", ynode(true))
		return on
	}
	branch := cmp.Or(c.plan.DefaultBranch, "main")
	allBranches, pullRequests := false, false
	if create := yget(yget(doc, "branches"), "create"); create != nil {
		kind := ystr(create)
		if create.Kind == yaml.MappingNode && len(create.Content) >= 2 {
			kind = create.Content[0].Value
			if kind == "for-new-branch" && ystr(create.Content[1]) != "" {
				c.note("Plan branches were created for branches matching %q; the pipeline runs for all branches.", ystr(create.Content[1]))
			}
		}
		switch kind {
		case "for-new-branch":
			allBranches = true
		case "for-pull-request":
			pullRequests = true
		case "for-new-branch-and-pull-request":
			allBranches, pullRequests = true, true
		}
	}
	push := false
	var schedules []*yaml.Node
	for _, t := range yentries(yget(doc, "triggers")) {
		switch t.name {
		case "polling", "remote", "bitbucket-server-trigger", "stash-trigger", "repository-change", "repository":
			push = true
		case "cron", "scheduled":
			expr := ystr(t.value)
			if t.value != nil && t.value.Kind == yaml.MappingNode {
				expr = cmp.Or(ystr(yget(t.value, "expression")), ystr(yget(t.value, "cron")))
			}
			if cron, ok := quartzToCron(expr); ok {
				schedules = append(schedules, ymap("cron", cron, "branch", branch))
			} else {
				c.note("The schedule %q could not be converted; add it to the pipeline's triggers.", expr)
			}
		case "single-daily-build":
			if h, m, ok := strings.Cut(ystr(t.value), ":"); ok {
				schedules = append(schedules, ymap("cron", strings.TrimLeft(m, "0")+" "+strings.TrimLeft(h, "0")+" * * *", "branch", branch))
			}
		default:
			c.note("The trigger %q of the plan is not converted.", t.name)
		}
	}
	for i := range schedules {
		// "0" and "00" lose their digits above; an empty field is minute or hour 0.
		cron := schedules[i].Content[1]
		fields := strings.Fields(" " + cron.Value)
		if len(fields) == 3 {
			cron.Value = "0 0 * * *"
		} else if len(fields) == 4 {
			cron.Value = "0 " + cron.Value
		}
	}
	if push {
		branches := []string{branch}
		if allBranches {
			branches = []string{"**"}
		}
		yset(on, "push", ymap("branches", branches))
	}
	if pullRequests {
		yset(on, "pull-request", ynode(nil))
	}
	if len(schedules) > 0 {
		yset(on, "schedule", yseq(schedules))
	}
	yset(on, "manual", ynode(true))
	return on
}

// quartzToCron converts a Quartz expression (seconds first, optional year) of Bamboo into a five-field one.
func quartzToCron(q string) (string, bool) {
	f := strings.Fields(q)
	if len(f) < 6 || len(f) > 7 {
		return "", false
	}
	if _, err := strconv.Atoi(f[0]); err != nil {
		return "", false // runs more than once a minute
	}
	if len(f) == 7 && f[6] != "*" {
		return "", false
	}
	dom, dow := strings.ReplaceAll(f[3], "?", "*"), strings.ReplaceAll(f[5], "?", "*")
	if strings.ContainsAny(dom+dow, "LW#") {
		return "", false
	}
	// Quartz numbers the days of the week from 1 (Sunday) to 7, cron from 0.
	var days []string
	for _, item := range strings.Split(dow, ",") {
		r, step, hasStep := strings.Cut(item, "/")
		r = regexp.MustCompile(`\d+`).ReplaceAllStringFunc(r, func(d string) string {
			n, _ := strconv.Atoi(d)
			return strconv.Itoa(n - 1)
		})
		if hasStep {
			r += "/" + step
		}
		days = append(days, r)
	}
	expr := strings.Join([]string{f[1], f[2], dom, f[4], strings.Join(days, ",")}, " ")
	if _, err := spec.ParseCron(expr); err != nil {
		return "", false
	}
	return expr, true
}

// docker reads a Docker configuration: an image name or {image, volumes, docker-run-arguments}.
func (c *converter) docker(n *yaml.Node, where string) (image string, set bool) {
	switch {
	case n == nil:
		return "", false
	case n.Kind == yaml.ScalarNode:
		v := ystr(n)
		return map[bool]string{true: "", false: v}[v == "none"], true
	case n.Kind == yaml.MappingNode:
		if ystr(yget(n, "enabled")) == "false" {
			return "", true
		}
		if yget(n, "volumes") != nil || ystr(yget(n, "docker-run-arguments")) != "" {
			c.note("%s: Docker volumes and run arguments are not converted.", where)
		}
		return ystr(yget(n, "image")), true
	}
	return "", false
}

func (c *converter) stage(doc *yaml.Node, st yentry, names map[string]bool) *yaml.Node {
	name := koggeName(st.name, names)
	if st.value != nil && ybool(yget(st.value, "final")) {
		c.note("The stage %q always ran, also after failures; in Kogge it runs only when the stages before it succeed.", st.name)
	}
	manual := st.value != nil && ybool(yget(st.value, "manual"))
	jobNames := map[string]bool{}
	var jobs []*yaml.Node
	for _, jobName := range ystrs(yget(st.value, "jobs")) {
		def := yget(doc, jobName)
		if def == nil {
			c.note("The job %q of the stage %q is not defined in the specs.", jobName, st.name)
			continue
		}
		jobs = append(jobs, c.job(doc, jobName, def, name, manual, jobNames))
	}
	if len(jobs) == 0 {
		return nil
	}
	return ymap("name", name, "jobs", yseq(jobs))
}

func (c *converter) job(doc *yaml.Node, bambooName string, def *yaml.Node, stage string, manual bool, names map[string]bool) *yaml.Node {
	name := koggeName(bambooName, names)
	where := fmt.Sprintf("Job %q", bambooName)
	image, set := c.docker(yget(def, "docker"), where)
	if !set {
		image, _ = c.docker(yget(doc, "docker"), "The plan")
	}
	j := &jobBuilder{c: c, where: where, image: image}
	j.requirements(yget(def, "requirements"))
	j.tasks(yget(def, "tasks"), false)
	j.tasks(yget(def, "final-tasks"), true)
	job := ymap("name", name)
	if image != "" {
		yset(job, "image", ynode(image))
	}
	if len(j.labels) > 0 {
		yset(job, "runs-on", ynode(j.labels))
	}
	if manual {
		yset(job, "trigger", ynode("manual"))
	}
	var deps []string
	for _, sub := range yentries(yget(def, "artifact-subscriptions")) {
		artifact := ystr(yget(sub.value, "artifact"))
		if src, ok := c.sources[artifact]; ok && !slices.Contains(deps, src) {
			deps = append(deps, src)
		}
	}
	if len(deps) > 0 {
		yset(job, "dependencies", ynode(deps))
	}
	if len(j.steps) == 0 {
		j.steps = append(j.steps, ymap("name", "Nothing to do", "run", "true"))
	}
	yset(job, "steps", yseq(j.steps))
	var artifacts []*yaml.Node
	for _, a := range yentries(yget(def, "artifacts")) {
		n := a.value
		if a.name != "" && n == nil {
			continue
		}
		if a.name != "" && n != nil && n.Kind == yaml.MappingNode && yget(n, "pattern") == nil && len(n.Content) > 0 {
			// "- name: {location, pattern}" instead of "- {name, location, pattern}"
			n = ymap("name", a.name, "location", ystr(yget(n, "location")), "pattern", ystr(yget(n, "pattern")))
		}
		artifactName := cmp.Or(ystr(yget(n, "name")), a.name)
		pattern := cmp.Or(ystr(yget(n, "pattern")), "**")
		p := pattern
		if loc := strings.Trim(ystr(yget(n, "location")), "/"); loc != "" && loc != "." {
			p = path.Join(loc, pattern)
		}
		artifacts = append(artifacts, ymap("name", koggeName(artifactName, map[string]bool{}), "paths", []string{p}))
		c.sources[artifactName] = stage + "/" + name
	}
	if len(artifacts) > 0 {
		yset(job, "artifacts", yseq(artifacts))
	}
	if len(j.junit) > 0 {
		yset(job, "reports", ymap("junit", j.junit))
	}
	if len(j.comments) > 0 {
		job.HeadComment = strings.Join(j.comments, "\n")
	}
	return job
}

// jobBuilder collects the steps and settings of a job (or of a deployment environment).
type jobBuilder struct {
	c        *converter
	where    string
	image    string
	labels   []string
	steps    []*yaml.Node
	junit    []string
	comments []string
}

func (j *jobBuilder) addLabel(l string) {
	if l != "" && !slices.Contains(j.labels, l) {
		j.labels = append(j.labels, l)
		j.c.labels[l] = true
	}
}

func (j *jobBuilder) requirements(n *yaml.Node) {
	var raw []string
	for _, r := range yentries(n) {
		key, value := r.name, ystr(r.value)
		if r.name == "" && r.value != nil && len(r.value.Content) >= 2 {
			key, value = r.value.Content[0].Value, ystr(r.value.Content[1])
		}
		raw = append(raw, strings.TrimSpace(key+" "+value))
		if j.image != "" {
			continue // agents of container jobs need no tools
		}
		j.addLabel(label(key, value))
	}
	if len(raw) > 0 {
		j.comments = append(j.comments, "Bamboo requirements: "+strings.Join(raw, ", "))
	}
}

// shellOf maps a script interpreter of Bamboo to a shell of Kogge's shell executor ("" for the default).
func shellOf(interpreter string) string {
	i := strings.ToLower(interpreter)
	switch {
	case strings.Contains(i, "power") || strings.Contains(i, "pwsh"):
		return "pwsh"
	case i == "cmd" || i == "cmdexe" || strings.HasSuffix(i, "cmd.exe"):
		return "cmd"
	case strings.Contains(i, "bash"):
		return "bash"
	}
	return ""
}

func (j *jobBuilder) step(name, run, shell, dir string, final bool) {
	s := ymap()
	if name = strings.TrimSpace(name); name != "" {
		yset(s, "name", ynode(name))
	}
	yset(s, "run", ynode(run))
	if shell != "" && j.image == "" {
		yset(s, "shell", ynode(shell))
	}
	if dir = strings.Trim(dir, "/"); dir != "" && dir != "." {
		yset(s, "working-directory", ynode(dir))
	}
	if final {
		yset(s, "when", ynode("always"))
	}
	j.steps = append(j.steps, s)
}

// placeholder stands for a task without a counterpart: it fails until it is replaced.
func (j *jobBuilder) placeholder(kind, description string, config *yaml.Node, final bool) {
	j.c.out.Placeholders++
	j.c.out.PlaceholderTasks[kind]++
	j.c.note("%s: the Bamboo task %q has no counterpart; its step fails until it is replaced.", j.where, cmp.Or(description, kind))
	var lines []string
	for _, kv := range ypairs(config) {
		v := ystr(kv.value)
		switch {
		case kv.value != nil && kv.value.Kind != yaml.ScalarNode:
			v = "…"
		case secretVariable(kv.name, v):
			v = "(secret)"
		}
		lines = append(lines, kv.name+": "+v)
		if len(lines) == 20 {
			break
		}
	}
	title := "Bamboo task " + kind
	if description != "" {
		title += ": " + description
	}
	run := fmt.Sprintf("echo %s >&2\nexit 1\n", shellQuote("Replace this step: it stands for the "+title+"."))
	j.step("TODO "+title, run, "", "", final)
	if len(lines) > 0 {
		j.steps[len(j.steps)-1].HeadComment = "Configuration of the Bamboo task:\n" + strings.Join(lines, "\n")
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tasks converts the tasks (or final tasks, which run also after failures) of a job.
func (j *jobBuilder) tasks(n *yaml.Node, final bool) {
	for _, t := range yentries(n) {
		v := t.value
		description := ystr(yget(v, "description"))
		if v != nil && v.Kind == yaml.MappingNode && ystr(yget(v, "enabled")) == "false" {
			continue
		}
		kind := t.name
		if kind == "any-task" {
			kind = cmp.Or(ystr(yget(v, "plugin-key")), kind)
		}
		j.c.out.Tasks[kind]++
		switch t.name {
		case "checkout":
			if repo, dir := ystr(yget(v, "repository")), ystr(yget(v, "path")); repo != "" || dir != "" {
				j.comments = append(j.comments, fmt.Sprintf("Bamboo checked out %s into %q; Kogge clones the repository into the workspace.",
					cmp.Or(repo, "the repository"), dir))
			}
		case "clean":
			// Workspaces are fresh in Kogge.
		case "script":
			j.script(v, description, final)
		case "test-parser":
			kind, results := ystr(v), []string(nil)
			if v != nil && v.Kind == yaml.MappingNode {
				kind, results = ystr(yget(v, "type")), ystrs(yget(v, "test-results"))
			}
			if kind != "junit" && kind != "" {
				j.c.note("%s: test results of type %s are not read by Kogge (only JUnit XML).", j.where, kind)
				continue
			}
			if len(results) == 0 {
				results = []string{"**/test-reports/*.xml"}
			}
			j.junit = append(j.junit, results...)
		case "maven":
			goal := cmp.Or(ystr(yget(v, "goal")), "package")
			j.tool(v, "mvn "+goal, description, final)
			if ybool(yget(v, "tests")) {
				j.junit = append(j.junit, "**/target/surefire-reports/*.xml")
			}
			j.requireTools(yget(v, "executable"), yget(v, "jdk"))
		case "npm":
			j.tool(v, "npm "+cmp.Or(ystr(yget(v, "command")), "install"), description, final)
			j.requireTools(yget(v, "executable"))
		case "command":
			executable := ystr(yget(v, "executable"))
			j.tool(v, strings.TrimSpace(strings.ToLower(strings.Fields(executable + " x")[0])+" "+ystr(yget(v, "argument"))), description, final)
			j.comments = append(j.comments, fmt.Sprintf("The command task ran the executable %q of the agent.", executable))
			j.requireTools(yget(v, "executable"))
		case "artifact-download":
			if src := ystr(yget(v, "source-plan")); src != "" && src != j.c.plan.Key {
				j.placeholder(t.name, cmp.Or(description, "artifacts of "+src), v, final)
			}
			// Artifacts of earlier stages are in the workspace.
		case "inject-variables":
			file := ystr(v)
			if v != nil && v.Kind == yaml.MappingNode {
				file = ystr(yget(v, "file"))
			}
			j.comments = append(j.comments, fmt.Sprintf("Bamboo injected the variables of %s; read them in the steps that need them (set -a; . %s).",
				file, file))
		default:
			kind := t.name
			if kind == "any-task" {
				kind = ystr(yget(v, "plugin-key"))
			}
			j.placeholder(cmp.Or(kind, "task"), description, v, final)
		}
	}
}

// requireTools adds the agent capabilities a build tool task needed as labels (shell jobs only).
func (j *jobBuilder) requireTools(nodes ...*yaml.Node) {
	for _, n := range nodes {
		if v := ystr(n); v != "" && j.image == "" {
			j.addLabel(label(v, ""))
		}
	}
}

// tool adds a step that runs a build tool with the task's environment and working directory.
func (j *jobBuilder) tool(v *yaml.Node, command, description string, final bool) {
	run := command
	if env := ystr(yget(v, "environment")); env != "" {
		run = "export " + env + "\n" + run
	}
	j.step(cmp.Or(description, command), j.c.rewrite(run, "")+"\n", "", ystr(yget(v, "working-dir")), final)
}

func (j *jobBuilder) script(v *yaml.Node, description string, final bool) {
	var body, interpreter, file, args, env, dir string
	if v != nil && v.Kind == yaml.ScalarNode {
		body = ystr(v)
	} else {
		interpreter, file, args = ystr(yget(v, "interpreter")), ystr(yget(v, "file")), ystr(yget(v, "argument"))
		env, dir = ystr(yget(v, "environment")), ystr(yget(v, "working-dir"))
		body = strings.Join(ystrs(yget(v, "scripts")), "\n")
		if yget(v, "conditions") != nil {
			j.c.note("%s: conditions of script tasks are not converted; the step always runs.", j.where)
		}
	}
	shell := shellOf(interpreter)
	if file != "" {
		switch shell {
		case "pwsh":
			body = "& ./" + strings.TrimPrefix(file, "./") + " " + args
		case "cmd":
			body = "call " + file + " " + args
		case "bash":
			body = "bash " + file + " " + args
		default:
			body = "sh " + file + " " + args
		}
		body = strings.TrimSpace(body)
	}
	if env != "" {
		switch shell {
		case "", "bash":
			body = "export " + env + "\n" + body
		default:
			j.c.note("%s: the environment %q of a script task is not converted.", j.where, env)
		}
	}
	if strings.TrimSpace(body) == "" {
		return
	}
	run := j.c.rewrite(strings.TrimRight(body, "\n"), shell) + "\n"
	name := description
	if name == "" {
		name, _, _ = strings.Cut(strings.TrimSpace(run), "\n")
		name = string([]rune(name)[:min(len([]rune(name)), 80)])
	}
	j.step(name, run, shell, dir, final)
}

// deployment converts a deployment project into stages, one per environment, at the end of the pipeline.
func (c *converter) deployment(d bambooDeployment, several bool, names map[string]bool) ([]*yaml.Node, error) {
	docs, err := yamlDocs(d.Specs)
	if err != nil {
		return nil, fmt.Errorf("the specs of the deployment project %q are not YAML: %w", d.Name, err)
	}
	var doc *yaml.Node
	for _, x := range docs {
		if yget(x, "environments") != nil {
			doc = x
			break
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("the specs of the deployment project %q hold no environments", d.Name)
	}
	if yget(doc, "release-naming") != nil {
		c.note("The deployment project %q named releases; Kogge identifies deployments by run and commit.", d.Name)
	}
	branch := cmp.Or(c.plan.DefaultBranch, "main")
	var stages []*yaml.Node
	for _, envName := range ystrs(yget(doc, "environments")) {
		def := yget(doc, envName)
		id := koggeID(envName, true)
		if several {
			id = koggeID(d.Name+"-"+envName, true)
		}
		stageName := "Deploy " + envName
		if several {
			stageName = "Deploy " + d.Name + " " + envName
		}
		where := fmt.Sprintf("Environment %q", envName)
		image, _ := c.docker(yget(def, "docker"), where)
		j := &jobBuilder{c: c, where: where, image: image}
		j.requirements(yget(def, "requirements"))
		j.tasks(yget(def, "tasks"), false)
		j.tasks(yget(def, "final-tasks"), true)
		automatic := false
		for _, t := range yentries(yget(def, "triggers")) {
			switch t.name {
			case "build-success", "stage-success", "environment-success", "after-successful-build-plan", "after-successful-stage",
				"after-successful-deployment":
				automatic = true
			default:
				c.note("%s: the trigger %q is not converted; deployments start by hand.", where, t.name)
			}
		}
		job := ymap("name", koggeName(envName, map[string]bool{}), "environment", id)
		if image != "" {
			yset(job, "image", ynode(image))
		}
		if len(j.labels) > 0 {
			yset(job, "runs-on", ynode(j.labels))
		}
		if !automatic {
			yset(job, "trigger", ynode("manual"))
		}
		if len(j.steps) == 0 {
			j.steps = append(j.steps, ymap("name", "Nothing to do", "run", "true"))
		}
		yset(job, "steps", yseq(j.steps))
		if len(j.junit) > 0 {
			yset(job, "reports", ymap("junit", j.junit))
		}
		if len(j.comments) > 0 {
			job.HeadComment = strings.Join(j.comments, "\n")
		}
		stage := ymap("name", koggeName(stageName, names))
		if automatic {
			// Automatic deployments of Bamboo deployed the builds of the plan's main branch.
			yset(stage, "only", ymap("branches", []string{branch}))
		}
		yset(stage, "jobs", yseq([]*yaml.Node{job}))
		stages = append(stages, stage)

		env := convertedEnvironment{ID: id, Name: envName, SourceID: d.Environments[envName]}
		owner := fmt.Sprintf("environment %s of %s", envName, d.Name)
		values := map[string]string{}
		for _, kv := range ypairs(yget(def, "variables")) {
			values[kv.name] = ystr(kv.value)
		}
		for _, v := range d.Variables[envName] {
			values[v.Name] = v.Value
		}
		names := make([]string, 0, len(values))
		for n := range values {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if secretVariable(n, values[n]) {
				c.out.Secrets = append(c.out.Secrets, fmt.Sprintf("%s (%s)", koggeVar(n), owner))
				continue
			}
			env.Variables = append(env.Variables, bambooVariable{Name: koggeVar(n), Value: values[n]})
		}
		c.out.Environments = append(c.out.Environments, env)
	}
	return stages, nil
}

// pipelineFile assembles the proposed pipeline file of a repository and checks it like Kogge does.
func pipelineFile(header string, plans []*convertedPlan) ([]byte, error) {
	pipelines := ymap()
	for _, p := range plans {
		yset(pipelines, p.ID, p.Pipeline)
		pipelines.Content[len(pipelines.Content)-2].HeadComment = p.Comment
	}
	root := ymap("version", 1, "pipelines", pipelines)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, HeadComment: header, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	if _, err := spec.Parse(data); err != nil {
		return data, fmt.Errorf("the converted pipeline file is not valid: %w", err)
	}
	return data, nil
}
