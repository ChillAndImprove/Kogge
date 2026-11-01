// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package spec parses and validates pipeline files (.kogge/pipelines.yml; testdata/example.yml shows the whole
// format). It is pure: no database, no network, no file system — the files a pipeline file includes are read
// through a Reader of the caller. Errors carry the file, line and column of the offending value.
package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Path is where pipeline files live in repositories.
const Path = ".kogge/pipelines.yml"

// File is a parsed pipeline file with the files it includes.
type File struct {
	Version   int
	Defaults  Defaults
	Variables map[string]string
	Pipelines []*Pipeline
	// Includes are the files that were included, in the order they were read.
	Includes []Included
	// Unresolved: the file includes others, but they were not read (Parse); their pipelines, variables and defaults
	// are missing.
	Unresolved bool
	// Warnings are doubtful parts that do not keep the file from running.
	Warnings Errors
}

// Defaults apply to all jobs.
type Defaults struct {
	Image     string
	RunsOn    []string
	Timeout   time.Duration
	Retry     int
	Resources Resources
}

// Pipeline is a named set of stages with its triggers.
type Pipeline struct {
	ID        string
	Name      string
	On        Triggers
	Variables map[string]string
	Stages    []*Stage
	Line      int
	// File is the included file that defines the pipeline (Included.Name); empty: the pipeline file itself.
	File string
}

// Triggers say when a pipeline runs.
type Triggers struct {
	Push        *PushTrigger
	PullRequest *PullRequestTrigger
	Schedule    []Schedule
	Manual      *Manual
	API         bool
	Pipeline    []PipelineTrigger
}

// PipelineTrigger runs a pipeline after runs of another pipeline: of the same repository, or of another one of
// the same code project.
type PipelineTrigger struct {
	Pipeline   string
	Repository string   // "KEY/slug"; empty: the same repository
	Branches   []string // runs of these branches (with Tags: of these refs; neither: of all)
	Tags       []string
	Result     string // ResultSuccess, ResultFailure or ResultAny
}

// Results of the runs a pipeline trigger follows.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultAny     = "any"
)

// PushTrigger selects pushes by branch and tag patterns, and pushes to branches by the files they change.
type PushTrigger struct {
	Branches       []string
	Tags           []string
	BranchesIgnore []string
	TagsIgnore     []string
	Paths          []string
	PathsIgnore    []string
}

// PullRequestTrigger selects pull requests by their branches and the files they change.
type PullRequestTrigger struct {
	TargetBranches []string
	SourceBranches []string
	Paths          []string
	PathsIgnore    []string
}

// Schedule runs a pipeline on a branch at times given by a cron expression.
type Schedule struct {
	Cron   *Cron
	Expr   string
	Branch string
}

// Manual allows starting the pipeline from the user interface, with variables.
type Manual struct {
	Variables map[string]ManualVariable
}

// ManualVariable is a field of the run form.
type ManualVariable struct {
	Default     string   `json:"default"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// Filter restricts stages and jobs to branches, tags and events.
type Filter struct {
	Branches []string
	Tags     []string
	Events   []string
}

// Stage is a group of jobs that run in parallel.
type Stage struct {
	Name   string
	Only   *Filter
	Except *Filter
	// If is the stage's condition (nil: none); its jobs are skipped when it is false.
	If   *Condition
	Jobs []*Job
	Line int
}

// Job runs steps in one container or shell.
type Job struct {
	Name         string
	Image        string
	RunsOn       []string
	Services     []Service
	Variables    map[string]string
	Steps        []Step
	Artifacts    []Artifact
	Dependencies []string // nil: all jobs of earlier stages (or the jobs of Needs)
	// Needs are the jobs it waits for (stage/job), when the file names them: the job then starts as soon as they
	// succeeded, also before earlier stages are done. nil: it waits for all jobs of earlier stages; empty: for none.
	Needs        []Need
	If           *Condition // nil: none
	Cache        []Cache
	Reports      Reports
	Environment  string
	Manual       bool
	Timeout      time.Duration
	Retry        int
	AllowFailure bool
	Only         *Filter
	Except       *Filter
	Clone        Clone
	// Resources limit the job's container (container executor only).
	Resources Resources
	// Matrix is the name of the job whose matrix made this one ("" for other jobs); MatrixValues are the values of
	// the matrix variables for this job, which it also has as variables.
	Matrix       string
	MatrixValues map[string]string
	Line         int

	// needRefs are the entries of needs as written, resolved to Needs when the pipeline is complete.
	needRefs []needRef
}

// Need is a job that a job waits for.
type Need struct {
	Job       string // stage/job
	Artifacts bool   // its artifacts are downloaded (default)
	// Optional: the job also runs when the needed one does not run for the event (only, except, if).
	Optional bool
}

// Executor returns the executor a job needs: container (with an image) or shell.
func (j *Job) Executor() string {
	if j.Image != "" {
		return "container"
	}
	return "shell"
}

// Labels returns the labels an agent must have to run the job.
func (j *Job) Labels() []string {
	labels := append([]string{}, j.RunsOn...)
	if j.Executor() == "container" && !contains(labels, "docker") && !contains(labels, "podman") {
		labels = append(labels, "docker")
	}
	sort.Strings(labels)
	return labels
}

// Requests returns the most that one container of the job asks for (the job's own or a service's): an agent must
// allow that much. Shell jobs ask for nothing — the shell executor does not limit resources.
func (j *Job) Requests() Resources {
	if j.Executor() != "container" {
		return Resources{}
	}
	r := j.Resources
	for _, s := range j.Services {
		r.MilliCPUs = max(r.MilliCPUs, s.Resources.MilliCPUs)
		r.Memory = max(r.Memory, s.Resources.Memory)
	}
	return r
}

// Service is a container that runs beside a job.
type Service struct {
	Name      string
	Image     string
	Variables map[string]string
	Command   []string
	Resources Resources
}

// Step is a script.
type Step struct {
	Name             string
	Run              string
	Shell            string
	WorkingDirectory string
	Timeout          time.Duration
	When             string // on-success, on-failure, always
	ContinueOnError  bool
}

// Artifact is a set of files kept after a job.
type Artifact struct {
	Name     string
	Paths    []string
	ExpireIn time.Duration
}

// Cache is a set of files restored before and saved after a job.
type Cache struct {
	Key      string
	KeyFiles []string
	Paths    []string
}

// Reports are test reports a job produces.
type Reports struct {
	JUnit []string
}

// Clone configures the checkout.
type Clone struct {
	Depth      int
	LFS        bool
	Submodules bool
}

// Error is a problem in a pipeline file or a file it includes.
type Error struct {
	// File is the included file with the problem (Included.Name); empty: the pipeline file itself.
	File    string
	Line    int
	Column  int
	Message string
}

func (e Error) Error() string {
	at := ""
	if e.File != "" {
		at = e.File + ": "
	}
	if e.Line == 0 {
		return at + e.Message
	}
	return fmt.Sprintf("%sline %d, column %d: %s", at, e.Line, e.Column, e.Message)
}

// Errors are all problems found.
type Errors []Error

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, x := range e {
		parts[i] = x.Error()
	}
	return strings.Join(parts, "; ")
}

// Limits of the format.
const (
	DefaultTimeout = 60 * time.Minute
	MaxTimeout     = 24 * time.Hour
	DefaultExpiry  = 30 * 24 * time.Hour
	// MaxExpiry bounds how long artifacts are kept (audit L36): storage is the instance's, not the pipeline's.
	MaxExpiry = 365 * 24 * time.Hour
)

var (
	// repoPattern is the path of a repository: the key of its code project and its slug.
	repoPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}/[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	namePattern = regexp.MustCompile(`^[^/\x00-\x1f]{1,100}$`)
	varPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	envPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	// cachePattern allows dots, too: cache keys often name versions (go-1.27).
	cachePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

type parser struct {
	errs     Errors
	warnings Errors
	file     string // the file being read, for errors ("" for the pipeline file itself)
}

func (p *parser) at(n *yaml.Node, format string, args ...any) Error {
	e := Error{File: p.file, Message: fmt.Sprintf(format, args...)}
	if n != nil {
		e.Line, e.Column = n.Line, n.Column
	}
	return e
}

func (p *parser) errorf(n *yaml.Node, format string, args ...any) {
	p.add(&p.errs, p.at(n, format, args...))
}

func (p *parser) warnf(n *yaml.Node, format string, args ...any) {
	p.add(&p.warnings, p.at(n, format, args...))
}

func (p *parser) add(list *Errors, e Error) {
	// The jobs of a matrix share their places in the file: a mistake there is reported once.
	for _, x := range *list {
		if x == e {
			return
		}
	}
	*list = append(*list, e)
}

// fields walks a mapping and reports unknown keys.
func (p *parser) fields(n *yaml.Node, what string, known map[string]func(v *yaml.Node)) {
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping", what)
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			p.errorf(k, "%s: %q is given twice", what, k.Value)
			continue
		}
		seen[k.Value] = true
		fn, ok := known[k.Value]
		if !ok {
			p.errorf(k, "%s: unknown key %q", what, k.Value)
			continue
		}
		fn(v)
	}
}

func (p *parser) str(n *yaml.Node, what string) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		p.errorf(n, "%s must be a text", what)
		return ""
	}
	return n.Value
}

// imageRef matches container image references: [registry[:port]/]path[:tag][@digest], as the container tools
// define them. Anything else — above all a value starting with "-", which the container command line would take
// as an option (audit L35) — is refused.
var imageRef = regexp.MustCompile(`^(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*` +
	`(?::[0-9]+)?|\[[0-9a-fA-F:]+\](?::[0-9]+)?)/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@[A-Za-z][A-Za-z0-9]*(?:[-_+.][A-Za-z][A-Za-z0-9]*)*:[0-9A-Fa-f]{32,})?$`)

// serviceName is a host name label: services are reached by their names.
var serviceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidImage reports whether ref is a container image reference.
func ValidImage(ref string) bool { return len(ref) <= 512 && imageRef.MatchString(ref) }

func (p *parser) image(n *yaml.Node, what string) string {
	v := p.str(n, what)
	if v != "" && !ValidImage(v) {
		p.errorf(n, "%s %q is not a container image reference (such as golang:1.25 or registry.example.com/team/app@sha256:…)", what, v)
	}
	return v
}

func (p *parser) strList(n *yaml.Node, what string) []string {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
		return []string{n.Value}
	}
	if n.Kind != yaml.SequenceNode {
		p.errorf(n, "%s must be a list", what)
		return nil
	}
	out := []string{}
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode {
			p.errorf(c, "%s must be a list of texts", what)
			continue
		}
		if len(c.Value) > MaxPattern {
			p.errorf(c, "%s: an entry has more than %d characters", what, MaxPattern)
			continue
		}
		out = append(out, c.Value)
	}
	return out
}

func (p *parser) boolean(n *yaml.Node, what string) bool {
	var b bool
	if n.Kind != yaml.ScalarNode || n.Decode(&b) != nil {
		p.errorf(n, "%s must be true or false", what)
	}
	return b
}

func (p *parser) integer(n *yaml.Node, what string, min, max int) int {
	var i int
	if n.Kind != yaml.ScalarNode || n.Decode(&i) != nil || i < min || i > max {
		p.errorf(n, "%s must be a number from %d to %d", what, min, max)
	}
	return i
}

func (p *parser) duration(n *yaml.Node, what string) time.Duration {
	if n.Kind != yaml.ScalarNode {
		p.errorf(n, "%s must be a duration such as 30m or 2h", what)
		return 0
	}
	d, err := ParseDuration(n.Value)
	if err != nil {
		p.errorf(n, "%s: %v", what, err)
	}
	return d
}

func (p *parser) variables(n *yaml.Node, what string) map[string]string {
	out := map[string]string{}
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping of names to values", what)
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !varPattern.MatchString(k.Value) {
			p.errorf(k, "%s: %q is not a valid variable name", what, k.Value)
			continue
		}
		if v.Kind != yaml.ScalarNode {
			p.errorf(v, "%s: the value of %s must be a text", what, k.Value)
			continue
		}
		out[k.Value] = v.Value
	}
	return out
}

// condition reads an if: a condition, with mistakes reported where in the file they are.
func (p *parser) condition(n *yaml.Node) *Condition {
	src := p.str(n, "if")
	if src == "" {
		if n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
			p.errorf(n, "if: the condition is empty")
		}
		return nil
	}
	c, err := ParseCondition(src)
	if err != nil {
		ce, _ := err.(*ConditionError)
		at := *n
		if ce != nil {
			at.Line, at.Column = scalarPosition(n, ce.Offset)
		}
		p.errorf(&at, "if: %v", err)
		return nil
	}
	return c
}

// scalarPosition returns the line and column of the character at a byte offset of a scalar's value, where it can be
// told: in plain scalars and quoted ones without escapes on one line. Elsewhere it is the scalar's own position.
func scalarPosition(n *yaml.Node, offset int) (int, int) {
	v := n.Value
	if offset < 0 || offset > len(v) || strings.ContainsAny(v, "\n\r") {
		return n.Line, n.Column
	}
	runes := len([]rune(v[:offset]))
	switch n.Style {
	case 0:
		return n.Line, n.Column + runes
	case yaml.SingleQuotedStyle:
		if !strings.Contains(v, "'") {
			return n.Line, n.Column + 1 + runes
		}
	case yaml.DoubleQuotedStyle:
		if !strings.ContainsAny(v, "\\\"") {
			return n.Line, n.Column + 1 + runes
		}
	}
	return n.Line, n.Column
}

// ParseDuration reads durations such as 90s, 30m, 2h, 1h30m, 7d and 4w.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	units := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}
	for s != "" {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i == len(s) {
			return 0, fmt.Errorf("%q is not a duration such as 30m or 2h", s)
		}
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, err
		}
		u, ok := units[s[i]]
		if !ok {
			return 0, fmt.Errorf("unknown unit %q (use s, m, h, d or w)", s[i])
		}
		total += time.Duration(n) * u
		s = s[i+1:]
	}
	return total, nil
}

var lineInError = regexp.MustCompile(`line (\d+)`)

// yamlError turns a syntax error of the YAML library into an error of the file.
func yamlError(file string, err error) Error {
	e := Error{File: file, Message: err.Error()}
	if te, ok := err.(*yaml.TypeError); ok {
		e.Message = strings.Join(te.Errors, "; ")
	}
	if m := lineInError.FindStringSubmatch(err.Error()); m != nil {
		e.Line, _ = strconv.Atoi(m[1])
	}
	return e
}

func (p *parser) pipeline(f *File, id string, n *yaml.Node) *Pipeline {
	pl := &Pipeline{ID: id, Name: id, Variables: map[string]string{}, Line: n.Line, On: Triggers{API: true}, File: p.file}
	explicit := false
	p.fields(n, "pipeline "+id, map[string]func(*yaml.Node){
		"name":      func(v *yaml.Node) { pl.Name = p.str(v, "name") },
		"variables": func(v *yaml.Node) { pl.Variables = p.variables(v, "variables") },
		"on":        func(v *yaml.Node) { explicit = true; p.triggers(&pl.On, v) },
		"stages": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
				p.errorf(v, "stages must be a non-empty list")
				return
			}
			names := map[string]bool{}
			for _, sn := range v.Content {
				st := p.stage(f, sn)
				if names[st.Name] {
					p.errorf(sn, "the stage name %q is used twice", st.Name)
				}
				names[st.Name] = true
				pl.Stages = append(pl.Stages, st)
			}
		},
	})
	if !explicit {
		pl.On.Push = &PushTrigger{Branches: []string{"**"}}
	}
	for _, t := range pl.On.Pipeline {
		if t.Pipeline == id && t.Repository == "" {
			p.errorf(n, "pipeline %s cannot run after itself", id)
		}
	}
	if len(pl.Stages) == 0 && n.Kind == yaml.MappingNode {
		p.errorf(n, "pipeline %s has no stages", id)
	}
	p.resolveNeeds(pl)
	p.resolveDependencies(pl)
	return pl
}

func (p *parser) triggers(t *Triggers, n *yaml.Node) {
	p.fields(n, "on", map[string]func(*yaml.Node){
		"push": func(v *yaml.Node) {
			t.Push = &PushTrigger{}
			if v.Tag == "!!null" {
				t.Push.Branches = []string{"**"}
				return
			}
			p.fields(v, "on.push", map[string]func(*yaml.Node){
				"branches":        func(v *yaml.Node) { t.Push.Branches = p.strList(v, "on.push.branches") },
				"tags":            func(v *yaml.Node) { t.Push.Tags = p.strList(v, "on.push.tags") },
				"branches-ignore": func(v *yaml.Node) { t.Push.BranchesIgnore = p.strList(v, "on.push.branches-ignore") },
				"tags-ignore":     func(v *yaml.Node) { t.Push.TagsIgnore = p.strList(v, "on.push.tags-ignore") },
				"paths":           func(v *yaml.Node) { t.Push.Paths = p.strList(v, "on.push.paths") },
				"paths-ignore":    func(v *yaml.Node) { t.Push.PathsIgnore = p.strList(v, "on.push.paths-ignore") },
			})
			if t.Push.Branches == nil && t.Push.Tags == nil {
				t.Push.Branches = []string{"**"}
			}
		},
		"pull-request": func(v *yaml.Node) {
			t.PullRequest = &PullRequestTrigger{}
			if v.Tag == "!!null" {
				return
			}
			p.fields(v, "on.pull-request", map[string]func(*yaml.Node){
				"target-branches": func(v *yaml.Node) { t.PullRequest.TargetBranches = p.strList(v, "on.pull-request.target-branches") },
				"source-branches": func(v *yaml.Node) { t.PullRequest.SourceBranches = p.strList(v, "on.pull-request.source-branches") },
				"paths":           func(v *yaml.Node) { t.PullRequest.Paths = p.strList(v, "on.pull-request.paths") },
				"paths-ignore":    func(v *yaml.Node) { t.PullRequest.PathsIgnore = p.strList(v, "on.pull-request.paths-ignore") },
			})
		},
		"schedule": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode {
				p.errorf(v, "on.schedule must be a list of {cron, branch}")
				return
			}
			for _, sn := range v.Content {
				var s Schedule
				p.fields(sn, "on.schedule", map[string]func(*yaml.Node){
					"cron": func(v *yaml.Node) {
						s.Expr = p.str(v, "cron")
						c, err := ParseCron(s.Expr)
						if err != nil {
							p.errorf(v, "cron: %v", err)
						}
						s.Cron = c
					},
					"branch": func(v *yaml.Node) { s.Branch = p.str(v, "branch") },
				})
				if s.Expr == "" {
					p.errorf(sn, "a schedule needs a cron expression")
				}
				t.Schedule = append(t.Schedule, s)
			}
		},
		"manual": func(v *yaml.Node) {
			if v.Kind == yaml.ScalarNode {
				if p.boolean(v, "on.manual") {
					t.Manual = &Manual{Variables: map[string]ManualVariable{}}
				}
				return
			}
			t.Manual = &Manual{Variables: map[string]ManualVariable{}}
			p.fields(v, "on.manual", map[string]func(*yaml.Node){
				"variables": func(v *yaml.Node) {
					if v.Kind != yaml.MappingNode {
						p.errorf(v, "on.manual.variables must be a mapping")
						return
					}
					for i := 0; i+1 < len(v.Content); i += 2 {
						k, vn := v.Content[i], v.Content[i+1]
						if !varPattern.MatchString(k.Value) {
							p.errorf(k, "%q is not a valid variable name", k.Value)
						}
						var mv ManualVariable
						if vn.Kind == yaml.ScalarNode {
							mv.Default = vn.Value
						} else {
							p.fields(vn, "on.manual.variables."+k.Value, map[string]func(*yaml.Node){
								"default":     func(v *yaml.Node) { mv.Default = p.str(v, "default") },
								"description": func(v *yaml.Node) { mv.Description = p.str(v, "description") },
								"options":     func(v *yaml.Node) { mv.Options = p.strList(v, "options") },
							})
						}
						t.Manual.Variables[k.Value] = mv
					}
				},
			})
		},
		"api":      func(v *yaml.Node) { t.API = p.boolean(v, "on.api") },
		"pipeline": func(v *yaml.Node) { t.Pipeline = p.pipelineTriggers(v) },
	})
}

// pipelineTriggers reads on.pipeline: a pipeline's id, a mapping, or a list of them.
func (p *parser) pipelineTriggers(n *yaml.Node) []PipelineTrigger {
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
		if len(items) == 0 {
			p.errorf(n, "on.pipeline must name at least one pipeline")
		}
	}
	var out []PipelineTrigger
	for _, it := range items {
		t := PipelineTrigger{Result: ResultSuccess}
		switch it.Kind {
		case yaml.ScalarNode:
			t.Pipeline = p.str(it, "on.pipeline")
		case yaml.MappingNode:
			p.fields(it, "on.pipeline", map[string]func(*yaml.Node){
				"name":       func(v *yaml.Node) { t.Pipeline = p.str(v, "on.pipeline.name") },
				"repository": func(v *yaml.Node) { t.Repository = p.str(v, "on.pipeline.repository") },
				"branches":   func(v *yaml.Node) { t.Branches = p.strList(v, "on.pipeline.branches") },
				"tags":       func(v *yaml.Node) { t.Tags = p.strList(v, "on.pipeline.tags") },
				"result": func(v *yaml.Node) {
					t.Result = p.str(v, "on.pipeline.result")
					if !contains([]string{ResultSuccess, ResultFailure, ResultAny}, t.Result) {
						p.errorf(v, "on.pipeline.result must be success, failure or any")
					}
				},
			})
		default:
			p.errorf(it, "on.pipeline must be the id of a pipeline or a mapping with its name")
			continue
		}
		switch {
		case t.Pipeline == "" && it.Kind == yaml.MappingNode:
			p.errorf(it, "on.pipeline needs the name of the pipeline to follow")
		case t.Pipeline != "" && !idPattern.MatchString(t.Pipeline):
			p.errorf(it, "on.pipeline: %q is not the id of a pipeline", t.Pipeline)
		}
		if t.Repository != "" && !repoPattern.MatchString(t.Repository) {
			p.errorf(it, "on.pipeline.repository %q is not a repository (KEY/slug)", t.Repository)
		}
		out = append(out, t)
	}
	return out
}

func (p *parser) filter(n *yaml.Node, what string) *Filter {
	f := &Filter{}
	p.fields(n, what, map[string]func(*yaml.Node){
		"branches": func(v *yaml.Node) { f.Branches = p.strList(v, what+".branches") },
		"tags":     func(v *yaml.Node) { f.Tags = p.strList(v, what+".tags") },
		"events": func(v *yaml.Node) {
			f.Events = p.strList(v, what+".events")
			for _, e := range f.Events {
				if !contains([]string{EventPush, EventPullRequest, EventSchedule, EventManual, EventAPI, EventPipeline}, e) {
					p.errorf(v, "%s.events: unknown event %q", what, e)
				}
			}
		},
	})
	return f
}

func (p *parser) stage(f *File, n *yaml.Node) *Stage {
	st := &Stage{Line: n.Line}
	p.fields(n, "stage", map[string]func(*yaml.Node){
		"name":   func(v *yaml.Node) { st.Name = p.str(v, "stage name") },
		"only":   func(v *yaml.Node) { st.Only = p.filter(v, "only") },
		"except": func(v *yaml.Node) { st.Except = p.filter(v, "except") },
		"if":     func(v *yaml.Node) { st.If = p.condition(v) },
		"jobs": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
				p.errorf(v, "jobs must be a non-empty list")
				return
			}
			names := map[string]bool{}
			for _, jn := range v.Content {
				for _, j := range p.job(f, jn) {
					if names[j.Name] {
						p.errorf(jn, "the job name %q is used twice in the stage", j.Name)
					}
					names[j.Name] = true
					st.Jobs = append(st.Jobs, j)
				}
			}
		},
	})
	if st.Name == "" {
		p.errorf(n, "a stage needs a name")
	} else if !namePattern.MatchString(st.Name) {
		p.errorf(n, "the stage name %q is not valid", st.Name)
	}
	if len(st.Jobs) == 0 && n.Kind == yaml.MappingNode {
		p.errorf(n, "stage %s has no jobs", st.Name)
	}
	return st
}

// job reads a job — or, with a matrix, the jobs it makes.
func (p *parser) job(f *File, n *yaml.Node) []*Job {
	j := &Job{Line: n.Line, Image: f.Defaults.Image, RunsOn: f.Defaults.RunsOn, Timeout: f.Defaults.Timeout, Retry: f.Defaults.Retry,
		Resources: f.Defaults.Resources, Variables: map[string]string{}, Clone: Clone{Depth: 50, LFS: true}}
	var at jobNodes
	var matrix []MatrixVariable
	var exclude []map[string]string
	var matrixNode, excludeNode *yaml.Node
	p.fields(n, "job", map[string]func(*yaml.Node){
		"name":      func(v *yaml.Node) { j.Name = p.str(v, "job name") },
		"image":     func(v *yaml.Node) { j.Image, at.image = p.str(v, "image"), v },
		"runs-on":   func(v *yaml.Node) { j.RunsOn, at.runsOn = p.strList(v, "runs-on"), v },
		"variables": func(v *yaml.Node) { j.Variables = p.variables(v, "variables") },
		"matrix":    func(v *yaml.Node) { matrix, matrixNode = p.matrix(v), v },
		"matrix-exclude": func(v *yaml.Node) {
			excludeNode = v
			if v.Kind != yaml.SequenceNode {
				p.errorf(v, "matrix-exclude must be a list of combinations")
				return
			}
			for _, c := range v.Content {
				exclude = append(exclude, p.variables(c, "matrix-exclude"))
			}
		},
		"services": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode {
				p.errorf(v, "services must be a list")
				return
			}
			for _, sn := range v.Content {
				s := Service{Variables: map[string]string{}, Resources: f.Defaults.Resources}
				p.fields(sn, "service", map[string]func(*yaml.Node){
					"name": func(v *yaml.Node) {
						if s.Name = p.str(v, "service name"); s.Name != "" && !serviceName.MatchString(s.Name) {
							p.errorf(v, "the service name %q is not a host name (lower-case letters, digits and -)", s.Name)
						}
					},
					"image":     func(v *yaml.Node) { s.Image = p.str(v, "service image"); at.services = append(at.services, v) },
					"variables": func(v *yaml.Node) { s.Variables = p.variables(v, "service variables") },
					"command":   func(v *yaml.Node) { s.Command = p.strList(v, "service command") },
					"resources": func(v *yaml.Node) { s.Resources = p.resources(v, "service resources", f.Defaults.Resources) },
				})
				if s.Name == "" || s.Image == "" {
					p.errorf(sn, "a service needs a name and an image")
					return
				}
				j.Services = append(j.Services, s)
			}
		},
		"steps": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
				p.errorf(v, "steps must be a non-empty list")
				return
			}
			for _, sn := range v.Content {
				s := Step{When: "on-success"}
				p.fields(sn, "step", map[string]func(*yaml.Node){
					"name":              func(v *yaml.Node) { s.Name = p.str(v, "step name") },
					"run":               func(v *yaml.Node) { s.Run = p.str(v, "run") },
					"shell":             func(v *yaml.Node) { s.Shell = p.str(v, "shell") },
					"working-directory": func(v *yaml.Node) { s.WorkingDirectory = p.str(v, "working-directory") },
					"timeout":           func(v *yaml.Node) { s.Timeout = p.duration(v, "step timeout") },
					"when":              func(v *yaml.Node) { s.When = p.str(v, "when") },
					"continue-on-error": func(v *yaml.Node) { s.ContinueOnError = p.boolean(v, "continue-on-error") },
				})
				if strings.TrimSpace(s.Run) == "" {
					p.errorf(sn, "a step needs a script in run")
				}
				if !contains([]string{"on-success", "on-failure", "always"}, s.When) {
					p.errorf(sn, "when must be on-success, on-failure or always")
				}
				if s.Shell != "" && !contains([]string{"sh", "bash", "pwsh", "cmd"}, s.Shell) {
					p.errorf(sn, "shell must be sh, bash, pwsh or cmd")
				}
				if strings.Contains(s.WorkingDirectory, "..") || strings.HasPrefix(s.WorkingDirectory, "/") {
					p.errorf(sn, "working-directory must be a path inside the workspace")
				}
				if s.Name == "" {
					s.Name, _, _ = strings.Cut(strings.TrimSpace(s.Run), "\n")
					if len(s.Name) > 80 {
						s.Name = s.Name[:80] + "…"
					}
				}
				j.Steps = append(j.Steps, s)
			}
		},
		"artifacts": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode {
				p.errorf(v, "artifacts must be a list")
				return
			}
			for _, an := range v.Content {
				a := Artifact{ExpireIn: DefaultExpiry}
				p.fields(an, "artifact", map[string]func(*yaml.Node){
					"name":  func(v *yaml.Node) { a.Name = p.str(v, "artifact name") },
					"paths": func(v *yaml.Node) { a.Paths = p.strList(v, "artifact paths") },
					"expire-in": func(v *yaml.Node) {
						if a.ExpireIn = p.duration(v, "expire-in"); a.ExpireIn > MaxExpiry || a.ExpireIn < 0 {
							p.errorf(v, "expire-in must be at most 365d")
						}
					},
				})
				if a.Name == "" || len(a.Paths) == 0 {
					p.errorf(an, "an artifact needs a name (lower-case letters, digits, - and _) and paths")
					return
				}
				j.Artifacts = append(j.Artifacts, a)
				at.artifacts = append(at.artifacts, an)
			}
		},
		"dependencies": func(v *yaml.Node) { j.Dependencies, at.dependencies = p.strList(v, "dependencies"), v },
		"needs":        func(v *yaml.Node) { j.Needs, j.needRefs = []Need{}, p.needs(v) },
		"if":           func(v *yaml.Node) { j.If = p.condition(v) },
		"resources":    func(v *yaml.Node) { j.Resources, at.resources = p.resources(v, "resources", f.Defaults.Resources), v },
		"cache": func(v *yaml.Node) {
			if v.Kind != yaml.SequenceNode {
				p.errorf(v, "cache must be a list")
				return
			}
			for _, cn := range v.Content {
				var c Cache
				p.fields(cn, "cache", map[string]func(*yaml.Node){
					"key":       func(v *yaml.Node) { c.Key = p.str(v, "cache key") },
					"key-files": func(v *yaml.Node) { c.KeyFiles = p.strList(v, "key-files") },
					"paths":     func(v *yaml.Node) { c.Paths = p.strList(v, "cache paths") },
				})
				if c.Key == "" || len(c.Paths) == 0 {
					p.errorf(cn, "a cache needs a key and paths")
					return
				}
				j.Cache = append(j.Cache, c)
				at.caches = append(at.caches, cn)
			}
		},
		"reports": func(v *yaml.Node) {
			p.fields(v, "reports", map[string]func(*yaml.Node){
				"junit": func(v *yaml.Node) { j.Reports.JUnit = p.strList(v, "reports.junit") },
			})
		},
		"environment": func(v *yaml.Node) { j.Environment, at.environment = p.str(v, "environment"), v },
		"trigger": func(v *yaml.Node) {
			if t := p.str(v, "trigger"); t != "manual" {
				p.errorf(v, "trigger can only be manual")
			}
			j.Manual = true
		},
		"timeout":       func(v *yaml.Node) { j.Timeout = p.duration(v, "timeout") },
		"retry":         func(v *yaml.Node) { j.Retry = p.integer(v, "retry", 0, 2) },
		"allow-failure": func(v *yaml.Node) { j.AllowFailure = p.boolean(v, "allow-failure") },
		"only":          func(v *yaml.Node) { j.Only = p.filter(v, "only") },
		"except":        func(v *yaml.Node) { j.Except = p.filter(v, "except") },
		"clone": func(v *yaml.Node) {
			p.fields(v, "clone", map[string]func(*yaml.Node){
				"depth":      func(v *yaml.Node) { j.Clone.Depth = p.integer(v, "clone.depth", 0, 100000) },
				"lfs":        func(v *yaml.Node) { j.Clone.LFS = p.boolean(v, "clone.lfs") },
				"submodules": func(v *yaml.Node) { j.Clone.Submodules = p.boolean(v, "clone.submodules") },
			})
		},
	})
	if j.Name == "" {
		p.errorf(n, "a job needs a name")
	}
	if len(j.Steps) == 0 && n.Kind == yaml.MappingNode {
		p.errorf(n, "job %s has no steps", j.Name)
	}
	if j.Timeout <= 0 {
		j.Timeout = DefaultTimeout
	}
	if j.Timeout > MaxTimeout {
		p.errorf(n, "job %s: the timeout may be at most 24h", j.Name)
	}
	if excludeNode != nil && matrixNode == nil {
		p.errorf(excludeNode, "matrix-exclude needs a matrix")
	}
	// Resources limit containers; the shell executor runs steps as they are.
	if at.resources != nil && j.Image == "" && (j.Resources.MilliCPUs > 0 || j.Resources.Memory > 0) {
		p.warnf(at.resources, "resources apply to container jobs (with an image); the shell executor does not limit job %s", j.Name)
	}
	if matrixNode == nil {
		p.checkJob(j, n, at, nil)
		return []*Job{j}
	}
	return p.expand(j, n, at, matrix, matrixNode, exclude)
}

// jobNodes are where the values of a job that can hold matrix variables are, for errors.
type jobNodes struct {
	image, runsOn, environment  *yaml.Node
	dependencies, resources     *yaml.Node
	services, caches, artifacts []*yaml.Node
}

// checkJob checks the values of a job that can hold matrix variables, once they are known.
func (p *parser) checkJob(j *Job, n *yaml.Node, at jobNodes, matrix map[string]string) {
	if j.Name != "" && !namePattern.MatchString(j.Name) {
		p.errorf(n, "the job name %q is not valid", j.Name)
	}
	unknown := func(node *yaml.Node, what, v string) bool {
		m := placeholder.FindStringSubmatch(v)
		if m == nil {
			return false
		}
		if matrix == nil {
			p.errorf(node, "%s uses ${%s}, but the job has no matrix", what, m[1])
		} else {
			p.errorf(node, "%s uses ${%s}, which is not a variable of the job's matrix", what, m[1])
		}
		return true
	}
	if at.image != nil && j.Image != "" && !unknown(at.image, "image", j.Image) && !ValidImage(j.Image) {
		p.errorf(at.image, "image %q is not a container image reference (such as golang:1.25 or registry.example.com/team/app@sha256:…)", j.Image)
	}
	if at.runsOn != nil {
		for _, l := range j.RunsOn {
			if unknown(at.runsOn, "runs-on", l) {
				break
			}
		}
	}
	for i, s := range j.Services {
		if !unknown(at.services[i], "service image", s.Image) && !ValidImage(s.Image) {
			p.errorf(at.services[i], "service image %q is not a container image reference (such as golang:1.25 or registry.example.com/team/app@sha256:…)", s.Image)
		}
	}
	if at.environment != nil && !unknown(at.environment, "environment", j.Environment) && !envPattern.MatchString(j.Environment) {
		p.errorf(at.environment, "the environment name %q may only have lower-case letters, digits, - and _", j.Environment)
	}
	for i, c := range j.Cache {
		if !unknown(at.caches[i], "cache key", c.Key) && !cachePattern.MatchString(c.Key) {
			p.errorf(at.caches[i], "the cache key %q may only have lower-case letters, digits, ., - and _", c.Key)
		}
	}
	for i, a := range j.Artifacts {
		if !unknown(at.artifacts[i], "artifact name", a.Name) && !envPattern.MatchString(a.Name) {
			p.errorf(at.artifacts[i], "an artifact needs a name (lower-case letters, digits, - and _) and paths")
		}
	}
	// Placeholders in needs are checked with the pipeline (resolveNeeds): ${NAME} may also be part of the name of a
	// matrix job that is needed as a whole.
}

// A job matrix makes a job for every combination of the values of its variables.

// MatrixVariable is a variable of a job matrix with its values, in the order of the file.
type MatrixVariable struct {
	Name   string
	Values []string
}

// MaxMatrixJobs bounds the jobs one matrix makes.
const MaxMatrixJobs = 64

// placeholder is a reference to a matrix variable in a value of a job: ${NAME}.
var placeholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]{0,127})\}`)

func (p *parser) matrix(n *yaml.Node) []MatrixVariable {
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		p.errorf(n, "matrix must be a mapping of variable names to lists of values")
		return nil
	}
	var out []MatrixVariable
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !varPattern.MatchString(k.Value) {
			p.errorf(k, "matrix: %q is not a valid variable name", k.Value)
			continue
		}
		values := p.strList(v, "matrix "+k.Value)
		if len(values) == 0 && v.Kind == yaml.SequenceNode {
			p.errorf(v, "matrix %s has no values", k.Value)
		}
		for i, x := range values {
			if x == "" || len(x) > 100 || strings.ContainsAny(x, "\x00\n\r") {
				p.errorf(v, "matrix %s: the values must be one line of at most 100 characters", k.Value)
				break
			}
			if contains(values[:i], x) {
				p.errorf(v, "matrix %s: the value %q is given twice", k.Value, x)
				break
			}
		}
		out = append(out, MatrixVariable{Name: k.Value, Values: values})
	}
	return out
}

// expand makes the jobs of a matrix: one for every combination of values that matrix-exclude does not leave out,
// named after the job with its values, with the values as variables and in place of ${NAME} in its image,
// runs-on, service images, environment, cache keys, artifact names and needs.
func (p *parser) expand(j *Job, n *yaml.Node, at jobNodes, matrix []MatrixVariable, matrixNode *yaml.Node, exclude []map[string]string) []*Job {
	// Combinations are counted before they are made: matrix-exclude can remove some, but not many.
	names := map[string]bool{}
	total := 1
	for _, v := range matrix {
		names[v.Name] = true
		if _, ok := j.Variables[v.Name]; ok {
			p.errorf(matrixNode, "%s is a variable of the matrix and of the job", v.Name)
		}
		if total = min(total*len(v.Values), 16*MaxMatrixJobs+1); total == 0 {
			return nil // reported: a variable without values
		}
	}
	if len(matrix) == 0 {
		return nil // reported: no valid matrix
	}
	for _, c := range exclude {
		for k := range c {
			if !names[k] {
				p.errorf(matrixNode, "matrix-exclude names %s, which is not a variable of the matrix", k)
			}
		}
	}
	if total > 16*MaxMatrixJobs {
		p.errorf(matrixNode, "the matrix of job %s makes more than %d jobs", j.Name, MaxMatrixJobs)
		return nil
	}
	combos := [][]string{{}}
	for _, v := range matrix {
		var next [][]string
		for _, c := range combos {
			for _, x := range v.Values {
				next = append(next, append(append([]string{}, c...), x))
			}
		}
		combos = next
	}
	var out []*Job
	for _, combo := range combos {
		values := map[string]string{}
		for i, v := range matrix {
			values[v.Name] = combo[i]
		}
		if excluded(values, exclude) {
			continue
		}
		fill := func(s string) string {
			return placeholder.ReplaceAllStringFunc(s, func(ref string) string {
				if v, ok := values[ref[2:len(ref)-1]]; ok {
					return v
				}
				return ref
			})
		}
		x := *j
		x.Matrix, x.MatrixValues = j.Name, values
		if placeholder.MatchString(j.Name) {
			x.Name = fill(j.Name)
		} else {
			x.Name = j.Name + " (" + strings.Join(combo, ", ") + ")"
		}
		x.Variables = map[string]string{}
		for k, v := range j.Variables {
			x.Variables[k] = v
		}
		for k, v := range values {
			x.Variables[k] = v
		}
		x.Image, x.Environment = fill(j.Image), fill(j.Environment)
		x.RunsOn = make([]string, len(j.RunsOn))
		for i, l := range j.RunsOn {
			x.RunsOn[i] = fill(l)
		}
		x.Services = append([]Service{}, j.Services...)
		for i := range x.Services {
			x.Services[i].Image = fill(x.Services[i].Image)
		}
		x.Cache = append([]Cache{}, j.Cache...)
		for i := range x.Cache {
			x.Cache[i].Key = fill(x.Cache[i].Key)
		}
		x.Artifacts = append([]Artifact{}, j.Artifacts...)
		for i := range x.Artifacts {
			x.Artifacts[i].Name = fill(x.Artifacts[i].Name)
		}
		if j.needRefs != nil {
			x.Needs = []Need{}
			x.needRefs = make([]needRef, len(j.needRefs))
			for i, r := range j.needRefs {
				r.name = fill(r.name)
				x.needRefs[i] = r
			}
		}
		p.checkJob(&x, n, at, values)
		out = append(out, &x)
	}
	switch {
	case len(out) == 0 && len(combos) > 0:
		p.errorf(matrixNode, "matrix-exclude leaves no job of %s", j.Name)
	case len(out) > MaxMatrixJobs:
		p.errorf(matrixNode, "the matrix of job %s makes %d jobs; at most %d are allowed", j.Name, len(out), MaxMatrixJobs)
		return nil
	}
	return out
}

// excluded reports whether a combination of matrix values matches one of matrix-exclude: all values it names.
func excluded(values map[string]string, exclude []map[string]string) bool {
	for _, c := range exclude {
		match := len(c) > 0
		for k, v := range c {
			if values[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
