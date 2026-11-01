// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Events that start runs.
const (
	EventPush        = "push"
	EventPullRequest = "pull-request"
	EventSchedule    = "schedule"
	EventManual      = "manual"
	EventAPI         = "api"
	EventPipeline    = "pipeline"
)

// Event is what a run is started for.
type Event struct {
	Kind         string
	Branch       string // push, schedule, manual, api: the branch; pull requests: the source branch
	Tag          string
	TargetBranch string // pull requests
	Pipeline     string // manual, api and a run of a pipeline event again: the chosen pipeline
	// Changes are the files a push to a branch or a pull request changes, if ChangesKnown: paths filters need them.
	// Unknown changes (too many files, or a base commit that is gone) let every paths filter pass.
	Changes      []string
	ChangesKnown bool
	Upstream     *Upstream // pipeline: the run that finished
}

// Upstream is a finished run that pipelines may follow.
type Upstream struct {
	Repository string // "KEY/slug"
	Same       bool   // the run is of the repository whose pipelines follow it
	Pipeline   string
	Result     string // ResultSuccess or ResultFailure
	Branch     string
	Tag        string
}

// follows reports whether a pipeline trigger accepts a finished run.
func (t PipelineTrigger) follows(u *Upstream) bool {
	if u == nil || t.Pipeline != u.Pipeline || (t.Result != ResultAny && t.Result != u.Result) {
		return false
	}
	if t.Repository == "" {
		if !u.Same {
			return false
		}
	} else if !strings.EqualFold(t.Repository, u.Repository) {
		return false
	}
	if t.Branches == nil && t.Tags == nil {
		return true
	}
	if u.Tag != "" {
		return matchAny(t.Tags, u.Tag)
	}
	return matchAny(t.Branches, u.Branch)
}

// UsesPaths reports whether a pipeline of the file selects pushes or pull requests by the files they change, or a
// condition asks which files changed (changed()), so that the caller only works out the changes when it must.
func (f *File) UsesPaths() bool {
	for _, p := range f.Pipelines {
		if t := p.On.Push; t != nil && (len(t.Paths) > 0 || len(t.PathsIgnore) > 0) {
			return true
		}
		if t := p.On.PullRequest; t != nil && (len(t.Paths) > 0 || len(t.PathsIgnore) > 0) {
			return true
		}
		for _, st := range p.Stages {
			if st.If.UsesChanges() {
				return true
			}
			for _, j := range st.Jobs {
				if j.If.UsesChanges() {
					return true
				}
			}
		}
	}
	return false
}

// pathsMatch reports whether the changes of an event pass paths filters: some changed file that paths-ignore does
// not exclude matches paths (or there is no paths). Changes that are not known pass.
func pathsMatch(paths, ignore []string, ev Event) bool {
	if (len(paths) == 0 && len(ignore) == 0) || !ev.ChangesKnown {
		return true
	}
	for _, f := range ev.Changes {
		if matchAny(ignore, f) {
			continue
		}
		if len(paths) == 0 || matchAny(paths, f) {
			return true
		}
	}
	return false
}

// MaxPattern is the longest branch, tag or path pattern; longer ones match nothing.
const MaxPattern = 1024

// globs caches compiled patterns; it is emptied when full, as patterns come from pipeline files that anyone who
// can push writes.
var globs = struct {
	sync.Mutex
	m map[string]*regexp.Regexp
}{m: map[string]*regexp.Regexp{}}

const maxGlobs = 4096

// MatchGlob matches a name against a pattern: "*" within a path segment, "**" across segments ("**/" also matches
// no segment), "?" one character.
func MatchGlob(pattern, name string) bool {
	if len(pattern) > MaxPattern {
		return false
	}
	globs.Lock()
	re, ok := globs.m[pattern]
	globs.Unlock()
	if ok {
		return re.MatchString(name)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			// "**/" at the start or after a slash also matches no directory: "**/*.md" matches "README.md".
			if i+2 < len(pattern) && pattern[i+2] == '/' && (i == 0 || pattern[i-1] == '/') {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re = regexp.MustCompile(b.String())
	globs.Lock()
	if len(globs.m) >= maxGlobs {
		clear(globs.m)
	}
	globs.m[pattern] = re
	globs.Unlock()
	return re.MatchString(name)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if MatchGlob(p, name) {
			return true
		}
	}
	return false
}

// Triggered returns the pipelines that run for an event.
func (f *File) Triggered(ev Event) []*Pipeline {
	var out []*Pipeline
	for _, p := range f.Pipelines {
		if p.triggeredBy(ev) {
			out = append(out, p)
		}
	}
	return out
}

// SkippedByPaths returns the pipelines the event would start but for their paths filters.
func (f *File) SkippedByPaths(ev Event) []*Pipeline {
	if !ev.ChangesKnown {
		return nil
	}
	all := ev
	all.Changes, all.ChangesKnown = nil, false
	var out []*Pipeline
	for _, p := range f.Pipelines {
		if p.triggeredBy(all) && !p.triggeredBy(ev) {
			out = append(out, p)
		}
	}
	return out
}

func (p *Pipeline) triggeredBy(ev Event) bool {
	t := p.On
	switch ev.Kind {
	case EventPush:
		if t.Push == nil {
			return false
		}
		// Tags release what was built: paths filters apply to branches only.
		if ev.Tag != "" {
			return matchAny(t.Push.Tags, ev.Tag) && !matchAny(t.Push.TagsIgnore, ev.Tag)
		}
		return matchAny(t.Push.Branches, ev.Branch) && !matchAny(t.Push.BranchesIgnore, ev.Branch) &&
			pathsMatch(t.Push.Paths, t.Push.PathsIgnore, ev)
	case EventPullRequest:
		if t.PullRequest == nil {
			return false
		}
		if len(t.PullRequest.TargetBranches) > 0 && !matchAny(t.PullRequest.TargetBranches, ev.TargetBranch) {
			return false
		}
		return (len(t.PullRequest.SourceBranches) == 0 || matchAny(t.PullRequest.SourceBranches, ev.Branch)) &&
			pathsMatch(t.PullRequest.Paths, t.PullRequest.PathsIgnore, ev)
	case EventManual:
		return t.Manual != nil && (ev.Pipeline == "" || ev.Pipeline == p.ID)
	case EventAPI:
		return t.API && (ev.Pipeline == "" || ev.Pipeline == p.ID)
	case EventSchedule:
		return ev.Pipeline == p.ID
	case EventPipeline:
		if ev.Upstream == nil { // a run again: the pipeline still follows a pipeline
			return ev.Pipeline == p.ID && len(t.Pipeline) > 0
		}
		for _, pt := range t.Pipeline {
			if pt.follows(ev.Upstream) {
				return true
			}
		}
	}
	return false
}

// Upstreams returns the repositories other than its own whose runs the pipelines follow (KEY/slug).
func (f *File) Upstreams() []string {
	var out []string
	for _, p := range f.Pipelines {
		for _, t := range p.On.Pipeline {
			if t.Repository != "" && !contains(out, t.Repository) {
				out = append(out, t.Repository)
			}
		}
	}
	return out
}

// Applies reports whether a filter (only or except) matches the event.
func (f *Filter) matches(ev Event) bool {
	if len(f.Events) > 0 && !contains(f.Events, ev.Kind) {
		return false
	}
	if len(f.Branches) == 0 && len(f.Tags) == 0 {
		return true
	}
	if ev.Tag != "" {
		return matchAny(f.Tags, ev.Tag)
	}
	return matchAny(f.Branches, ev.Branch)
}

// Runs reports whether a stage or job with these filters runs for the event.
func Runs(only, except *Filter, ev Event) bool {
	if only != nil && !only.matches(ev) {
		return false
	}
	if except != nil && except.matches(ev) {
		return false
	}
	return true
}

// Cron is a five-field cron expression (minute, hour, day of month, month, day of week).
type Cron struct {
	fields [5]uint64 // bit sets
	domAny bool
	dowAny bool
}

var cronNames = map[string]string{"jan": "1", "feb": "2", "mar": "3", "apr": "4", "may": "5", "jun": "6", "jul": "7", "aug": "8",
	"sep": "9", "oct": "10", "nov": "11", "dec": "12", "sun": "0", "mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6"}

// ParseCron reads a cron expression such as "0 2 * * 1-5" (also @daily, @hourly, @weekly, @monthly).
func ParseCron(expr string) (*Cron, error) {
	switch strings.TrimSpace(expr) {
	case "@hourly":
		expr = "0 * * * *"
	case "@daily", "@midnight":
		expr = "0 0 * * *"
	case "@weekly":
		expr = "0 0 * * 0"
	case "@monthly":
		expr = "0 0 1 * *"
	}
	parts := strings.Fields(strings.ToLower(expr))
	if len(parts) != 5 {
		return nil, fmt.Errorf("%q needs five fields: minute hour day-of-month month day-of-week", expr)
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	c := &Cron{domAny: parts[2] == "*", dowAny: parts[4] == "*"}
	for i, part := range parts {
		for name, v := range cronNames {
			part = strings.ReplaceAll(part, name, v)
		}
		for _, item := range strings.Split(part, ",") {
			step := 1
			if r, s, ok := strings.Cut(item, "/"); ok {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("invalid step in %q", item)
				}
				step, item = n, r
			}
			lo, hi := bounds[i][0], bounds[i][1]
			if item != "*" {
				a, b, isRange := strings.Cut(item, "-")
				var err error
				if lo, err = strconv.Atoi(a); err != nil {
					return nil, fmt.Errorf("invalid value %q", item)
				}
				hi = lo
				if isRange {
					if hi, err = strconv.Atoi(b); err != nil {
						return nil, fmt.Errorf("invalid range %q", item)
					}
				} else if step > 1 {
					hi = bounds[i][1]
				}
			}
			if lo < bounds[i][0] || hi > bounds[i][1] || lo > hi {
				return nil, fmt.Errorf("%q is out of range", item)
			}
			for v := lo; v <= hi; v += step {
				c.fields[i] |= 1 << uint(v)
			}
		}
	}
	if c.fields[4]&(1<<7) != 0 { // 7 is Sunday too
		c.fields[4] |= 1
	}
	return c, nil
}

// Matches reports whether the minute of t is scheduled.
func (c *Cron) Matches(t time.Time) bool {
	if c.fields[0]&(1<<uint(t.Minute())) == 0 || c.fields[1]&(1<<uint(t.Hour())) == 0 || c.fields[3]&(1<<uint(t.Month())) == 0 {
		return false
	}
	dom := c.fields[2]&(1<<uint(t.Day())) != 0
	dow := c.fields[4]&(1<<uint(t.Weekday())) != 0
	// Like cron: if both day fields are restricted, either may match.
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}
