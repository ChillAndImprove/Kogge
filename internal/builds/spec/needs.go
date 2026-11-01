// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Pipelines as graphs: a job with needs waits for the jobs it names and starts as soon as they
// succeeded, also before the earlier stages are done; a job without needs waits for all jobs of the earlier stages.
// A job can need jobs of its own stage and of earlier ones: stages stay the order in which a pipeline reads, and
// a job of an earlier stage that needed one of a later stage would wait for a stage that waits for it.

// needRef is an entry of needs as written.
type needRef struct {
	name      string            // job, matrix job, one of its combinations, or stage/job
	artifacts bool              // download its artifacts
	optional  bool              // run also when it does not run
	matrix    map[string]string // only the combinations of a matrix job with these values
	node      *yaml.Node
}

// needs reads the entries of needs: names of jobs, or mappings {job, artifacts, optional, matrix}.
func (p *parser) needs(n *yaml.Node) []needRef {
	refs := []needRef{}
	if n.Kind != yaml.SequenceNode {
		p.errorf(n, "needs must be a list of jobs, such as [build, test]")
		return refs
	}
	for _, c := range n.Content {
		r := needRef{artifacts: true, node: c}
		switch c.Kind {
		case yaml.ScalarNode:
			r.name = p.str(c, "needs")
		case yaml.MappingNode:
			p.fields(c, "needs", map[string]func(*yaml.Node){
				"job":       func(v *yaml.Node) { r.name = p.str(v, "needs.job") },
				"artifacts": func(v *yaml.Node) { r.artifacts = p.boolean(v, "needs.artifacts") },
				"optional":  func(v *yaml.Node) { r.optional = p.boolean(v, "needs.optional") },
				"matrix":    func(v *yaml.Node) { r.matrix = p.variables(v, "needs.matrix") },
			})
			if r.name == "" {
				p.errorf(c, "an entry of needs names its job with job")
			}
		default:
			p.errorf(c, "needs must be a list of jobs, such as [build, test]")
		}
		if r.name != "" {
			refs = append(refs, r)
		}
	}
	return refs
}

// jobAt is a job of a pipeline with the index of its stage.
type jobAt struct {
	stage int
	job   *Job
}

func (pl *Pipeline) jobs() []jobAt {
	var out []jobAt
	for si, st := range pl.Stages {
		for _, j := range st.Jobs {
			out = append(out, jobAt{si, j})
		}
	}
	return out
}

// Key returns the key of a job of a stage, as needs and dependencies name it: stage/job.
func Key(stage, job string) string { return stage + "/" + job }

// matrixMatches reports whether a job of a matrix has all the values given.
func matrixMatches(j *Job, values map[string]string) bool {
	for k, v := range values {
		if j.MatrixValues[k] != v {
			return false
		}
	}
	return true
}

// resolveNeeds turns the entries of needs into the jobs they name and checks them: jobs of the pipeline, of the
// same or an earlier stage, not the job itself, without cycles.
func (p *parser) resolveNeeds(pl *Pipeline) {
	all := pl.jobs()
	key := func(a jobAt) string { return Key(pl.Stages[a.stage].Name, a.job.Name) }
	byKey := map[string]jobAt{}
	templates := map[string]bool{} // names of matrix jobs as written (lint on ${OS})
	for _, a := range all {
		byKey[key(a)] = a
		if a.job.Matrix != "" {
			templates[a.job.Matrix] = true
		}
	}
	nodes := map[*Job]map[string]*yaml.Node{} // where each need is written, for errors about cycles
	for _, a := range all {
		j := a.job
		if j.needRefs == nil {
			continue
		}
		nodes[j] = map[string]*yaml.Node{}
		for _, r := range j.needRefs {
			stageName, name := "", r.name
			if s, n, ok := strings.Cut(r.name, "/"); ok {
				stageName, name = s, n
			}
			if m := placeholder.FindStringSubmatch(name); m != nil && !templates[name] {
				if j.Matrix == "" {
					p.errorf(r.node, "needs uses ${%s}, but the job has no matrix", m[1])
				} else {
					p.errorf(r.node, "needs uses ${%s}, which is not a variable of the job's matrix", m[1])
				}
				continue
			}
			var found []jobAt
			stages := map[int]bool{}
			isMatrix := false
			for _, c := range all {
				if stageName != "" && pl.Stages[c.stage].Name != stageName {
					continue
				}
				if c.job.Name != name && c.job.Matrix != name {
					continue
				}
				if c.job.Matrix != "" && c.job.Matrix == name {
					isMatrix = true
				}
				if r.matrix != nil && !matrixMatches(c.job, r.matrix) {
					continue
				}
				found = append(found, c)
				stages[c.stage] = true
			}
			switch {
			case r.matrix != nil && !isMatrix && len(found) == 0:
				p.errorf(r.node, "needs %s: matrix applies to jobs with a matrix", r.name)
				continue
			case len(found) == 0 && isMatrix:
				p.errorf(r.node, "needs %s with values that no job of its matrix has", r.name)
				continue
			case len(found) == 0:
				p.errorf(r.node, "job %s needs %s, which is not a job of the pipeline", j.Name, r.name)
				continue
			case len(stages) > 1:
				var names []string
				for si := range stages {
					names = append(names, pl.Stages[si].Name)
				}
				slices.Sort(names)
				p.errorf(r.node, "needs %s, which is a job of several stages (%s); write stage/job", r.name, strings.Join(names, ", "))
				continue
			}
			for _, c := range found {
				switch {
				case c.job == j:
					p.errorf(r.node, "job %s cannot need itself", j.Name)
				case c.stage > a.stage:
					p.errorf(r.node, "job %s needs %s of the later stage %s; a job can need jobs of its own and of earlier stages",
						j.Name, c.job.Name, pl.Stages[c.stage].Name)
				default:
					k := key(c)
					if i := slices.IndexFunc(j.Needs, func(n Need) bool { return n.Job == k }); i >= 0 {
						// Named twice: its artifacts if one entry wants them, optional only if both are.
						j.Needs[i].Artifacts = j.Needs[i].Artifacts || r.artifacts
						j.Needs[i].Optional = j.Needs[i].Optional && r.optional
						continue
					}
					j.Needs = append(j.Needs, Need{Job: k, Artifacts: r.artifacts, Optional: r.optional})
					nodes[j][k] = r.node
				}
			}
		}
	}
	// Cycles: jobs of a stage that need each other (needs of earlier stages cannot lead back).
	state := map[*Job]int{} // 1: on the path being followed, 2: done
	var path []string
	var visit func(a jobAt)
	visit = func(a jobAt) {
		state[a.job] = 1
		path = append(path, a.job.Name)
		for _, n := range a.job.Needs {
			d, ok := byKey[n.Job]
			if !ok {
				continue
			}
			switch state[d.job] {
			case 0:
				visit(d)
			case 1:
				i := slices.Index(path, d.job.Name)
				cycle := append(append([]string{}, path[i:]...), d.job.Name)
				p.errorf(nodes[a.job][n.Job], "needs form a cycle: %s", strings.Join(cycle, " → "))
			}
		}
		path = path[:len(path)-1]
		state[a.job] = 2
	}
	for _, a := range all {
		if state[a.job] == 0 {
			visit(a)
		}
	}
	// Needs of jobs that do not run for every event: the job is skipped when they do not run.
	for _, a := range all {
		for _, n := range a.job.Needs {
			d, ok := byKey[n.Job]
			if !ok || n.Optional {
				continue
			}
			need, mine := conditions(pl.Stages[d.stage], d.job), conditions(pl.Stages[a.stage], a.job)
			if need != "" && need != mine {
				p.warnf(nodes[a.job][n.Job], "job %s needs %s, which does not run for every event (only, except or if): %s is skipped "+
					"when it does not run, unless the need is optional", a.job.Name, d.job.Name, a.job.Name)
			}
		}
	}
}

// conditions describes what decides whether a job runs (only, except and if of the job and its stage), for
// comparing jobs; "" when it always runs.
func conditions(st *Stage, j *Job) string {
	var b strings.Builder
	filter := func(what string, f *Filter) {
		if f != nil {
			b.WriteString(what + strings.Join(f.Branches, ",") + "|" + strings.Join(f.Tags, ",") + "|" + strings.Join(f.Events, ",") + "\n")
		}
	}
	filter("stage only ", st.Only)
	filter("stage except ", st.Except)
	if st.If != nil {
		b.WriteString("stage if " + st.If.String() + "\n")
	}
	filter("only ", j.Only)
	filter("except ", j.Except)
	if j.If != nil {
		b.WriteString("if " + j.If.String() + "\n")
	}
	return b.String()
}

// resolveDependencies checks the jobs that dependencies name — jobs of earlier stages, or for a job with needs the
// jobs it waits for — and replaces a job with a matrix by all jobs it made.
func (p *parser) resolveDependencies(pl *Pipeline) {
	all := pl.jobs()
	key := func(a jobAt) string { return Key(pl.Stages[a.stage].Name, a.job.Name) }
	byKey := map[string]jobAt{}
	for _, a := range all {
		byKey[key(a)] = a
	}
	// ancestors are the jobs a job waits for, also indirectly.
	memo := map[*Job][]jobAt{}
	var ancestors func(a jobAt, seen map[*Job]bool) []jobAt
	ancestors = func(a jobAt, seen map[*Job]bool) []jobAt {
		if got, ok := memo[a.job]; ok {
			return got
		}
		var out []jobAt
		if a.job.Needs == nil {
			for _, c := range all {
				if c.stage < a.stage {
					out = append(out, c)
				}
			}
		} else if !seen[a.job] {
			seen[a.job] = true // cycles are reported elsewhere
			for _, n := range a.job.Needs {
				if d, ok := byKey[n.Job]; ok {
					out = append(out, d)
					out = append(out, ancestors(d, seen)...)
				}
			}
		}
		memo[a.job] = out
		return out
	}
	for _, a := range all {
		j := a.job
		if j.Dependencies == nil {
			continue
		}
		allowed := ancestors(a, map[*Job]bool{})
		deps := []string{}
		for _, d := range j.Dependencies {
			var matrix, plain []string
			for _, c := range allowed {
				k := key(c)
				switch {
				case c.job.Matrix != "" && (c.job.Matrix == d || Key(pl.Stages[c.stage].Name, c.job.Matrix) == d):
					if !contains(matrix, k) {
						matrix = append(matrix, k)
					}
				case k == d || c.job.Name == d:
					plain = append(plain, k)
				}
			}
			switch {
			case len(matrix) > 0:
				deps = append(deps, matrix...)
			case len(plain) > 0 && j.Needs != nil:
				// A job with needs may start before earlier stages are done: its dependencies name exactly the jobs
				// it waits for, not others of the same name.
				deps = append(deps, plain...)
			case len(plain) > 0:
				deps = append(deps, d)
			case j.Needs != nil:
				p.errorf(&yaml.Node{Line: j.Line}, "job %s depends on %s, which is not a job it needs", j.Name, d)
			default:
				p.errorf(&yaml.Node{Line: j.Line}, "job %s depends on %s, which is not a job of an earlier stage", j.Name, d)
			}
		}
		j.Dependencies = deps
	}
}

// Skip reasons of jobs that their conditions leave out of a run (see Plan).
const (
	SkipCondition      = "condition"       // the job's if is false; Detail: the condition
	SkipStageCondition = "stage-condition" // the if of its stage is false; Detail: the condition
	SkipNeedAbsent     = "need-absent"     // a job it needs does not run; Detail: that job (stage/job)
)

// PlannedJob is a job of a run: one that runs, or one that is skipped (Skip) when the run is created.
type PlannedJob struct {
	Stage     int // the index of its stage among the stages of the run
	StageName string
	Job       *Job
	Skip      string // why it does not run; "" when it runs
	Detail    string
}

// Plan returns the jobs of a run of the pipeline for an event, with the commit message and the variables of the
// run: those that only and except leave, in the order of the file, with those that their conditions (if) skip —
// and jobs that need one that does not run, unless the need is optional. It returns nothing when no job would run.
func (pl *Pipeline) Plan(f *File, ev Event, message string, run map[string]string) []PlannedJob {
	base := map[string]string{}
	for _, m := range []map[string]string{f.Variables, pl.Variables} {
		for k, v := range m {
			base[k] = v
		}
	}
	env := &Env{Event: ev, Message: message}
	with := func(vars ...map[string]string) *Env {
		e := *env
		e.Variables = map[string]string{}
		for _, m := range append(append([]map[string]string{base}, vars...), run) {
			for k, v := range m {
				e.Variables[k] = v
			}
		}
		e.changed = env.changed
		return &e
	}
	env.changed = map[string]bool{}
	var out []PlannedJob
	index := 0
	for _, st := range pl.Stages {
		if !Runs(st.Only, st.Except, ev) {
			continue
		}
		stageSkip := ""
		if st.If != nil && !st.If.Eval(with()) {
			stageSkip = SkipStageCondition
		}
		added := false
		for _, j := range st.Jobs {
			if !Runs(j.Only, j.Except, ev) {
				continue
			}
			pj := PlannedJob{Stage: index, StageName: st.Name, Job: j}
			switch {
			case stageSkip != "":
				pj.Skip, pj.Detail = stageSkip, st.If.String()
			case j.If != nil && !j.If.Eval(with(j.Variables)):
				pj.Skip, pj.Detail = SkipCondition, j.If.String()
			}
			out = append(out, pj)
			added = true
		}
		if added {
			index++
		}
	}
	// A job whose need does not run does not run either, unless the need is optional; the skips spread until
	// nothing changes (every round skips a job, so there are at most as many rounds as jobs).
	runs := map[string]bool{}
	for _, pj := range out {
		runs[Key(pj.StageName, pj.Job.Name)] = pj.Skip == ""
	}
	for changed := true; changed; {
		changed = false
		for i := range out {
			pj := &out[i]
			if pj.Skip != "" {
				continue
			}
			for _, n := range pj.Job.Needs {
				if !runs[n.Job] && !n.Optional {
					pj.Skip, pj.Detail = SkipNeedAbsent, n.Job
					runs[Key(pj.StageName, pj.Job.Name)] = false
					changed = true
					break
				}
			}
		}
	}
	for _, pj := range out {
		if pj.Skip == "" {
			return out
		}
	}
	return nil
}
