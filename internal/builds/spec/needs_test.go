// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"fmt"
	"strings"
	"testing"
)

func needsOf(j *Job) string {
	if j.Needs == nil {
		return "-"
	}
	var s []string
	for _, n := range j.Needs {
		x := n.Job
		if !n.Artifacts {
			x += "!a"
		}
		if n.Optional {
			x += "?"
		}
		s = append(s, x)
	}
	return strings.Join(s, ",")
}

// TestNeeds: needs name jobs of the same or earlier stages — by name, as stage/job, a matrix job (all its jobs) or
// one combination — and become the jobs a job waits for.
func TestNeeds(t *testing.T) {
	f, err := Parse([]byte(`version: 1
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: compile
            steps: [{run: make}]
          - name: lint
            needs: []
            steps: [{run: make lint}]
          - name: package
            needs: [compile]
            steps: [{run: make dist}]
      - name: Test
        jobs:
          - name: test
            matrix: { GO: ["1.26", "1.27"], DB: [pg, lite] }
            needs: [Build/compile]
            steps: [{run: go test}]
          - name: e2e ${GO}
            matrix: { GO: ["1.26", "1.27"] }
            needs: ["test (${GO}, pg)", {job: lint, artifacts: false}]
            steps: [{run: make e2e}]
          - name: report
            needs:
              - test
              - {job: package, optional: true}
              - {job: test, matrix: {DB: lite}}
            steps: [{run: make report}]
      - name: Deploy
        jobs:
          - name: deploy
            steps: [{run: make deploy}]
`))
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]*Job{}
	for _, st := range f.Pipelines[0].Stages {
		for _, j := range st.Jobs {
			jobs[j.Name] = j
		}
	}
	for name, want := range map[string]string{
		"compile":           "-",
		"lint":              "",
		"package":           "Build/compile",
		"test (1.26, pg)":   "Build/compile",
		"e2e 1.26":          "Test/test (1.26, pg),Build/lint!a",
		"e2e 1.27":          "Test/test (1.27, pg),Build/lint!a",
		"report":            "Test/test (1.26, pg),Test/test (1.26, lite),Test/test (1.27, pg),Test/test (1.27, lite),Build/package?",
		"deploy":            "-",
		"test (1.27, lite)": "Build/compile",
	} {
		if j := jobs[name]; j == nil {
			t.Errorf("no job %s", name)
		} else if got := needsOf(j); got != want {
			t.Errorf("%s needs %s, want %s", name, got, want)
		}
	}
	if len(f.Warnings) != 0 {
		t.Errorf("warnings: %v", f.Warnings)
	}

	job := func(stages string) string {
		return "version: 1\npipelines:\n  ci:\n    stages:\n" + stages
	}
	for body, want := range map[string]string{
		"      - name: A\n        jobs:\n          - name: a\n            needs: [b]\n            steps: [{run: x}]\n" +
			"      - name: B\n        jobs:\n          - name: b\n            steps: [{run: x}]\n": "line 8, column 21: job a needs b of the later stage B",
		"      - name: A\n        jobs:\n          - name: a\n            needs: [zzz]\n            steps: [{run: x}]\n": "line 8, column 21: job a needs zzz, which is not a job of the pipeline",
		"      - name: A\n        jobs:\n          - name: a\n            needs: [a]\n            steps: [{run: x}]\n":   "job a cannot need itself",
		"      - name: A\n        jobs:\n          - name: a\n            needs: [b]\n            steps: [{run: x}]\n" +
			"          - name: b\n            needs: [c]\n            steps: [{run: x}]\n" +
			"          - name: c\n            needs: [a]\n            steps: [{run: x}]\n": "needs form a cycle: a → b → c → a",
		"      - name: A\n        jobs:\n          - name: x\n            steps: [{run: x}]\n" +
			"      - name: B\n        jobs:\n          - name: x\n            steps: [{run: x}]\n" +
			"      - name: C\n        jobs:\n          - name: c\n            needs: [x]\n            steps: [{run: x}]\n": "needs x, which is a job of several stages (A, B); write stage/job",
		"      - name: A\n        jobs:\n          - name: a\n            needs: build\n            steps: [{run: x}]\n":                                                                                                               "needs must be a list of jobs",
		"      - name: A\n        jobs:\n          - name: a\n            needs: [{artifacts: false}]\n            steps: [{run: x}]\n":                                                                                                "an entry of needs names its job with job",
		"      - name: A\n        jobs:\n          - name: a\n            steps: [{run: x}]\n          - name: b\n            needs: [{job: a, matrix: {X: y}}]\n            steps: [{run: x}]\n":                                      "matrix applies to jobs with a matrix",
		"      - name: A\n        jobs:\n          - name: a\n            matrix: {X: [1, 2]}\n            steps: [{run: x}]\n          - name: b\n            needs: [{job: a, matrix: {X: \"3\"}}]\n            steps: [{run: x}]\n": "needs a with values that no job of its matrix has",
		"      - name: A\n        jobs:\n          - name: a\n            needs: [\"b ${X}\"]\n            steps: [{run: x}]\n":                                                                                                        "needs uses ${X}, but the job has no matrix",
		"      - name: A\n        jobs:\n          - name: a\n            steps: [{run: x}]\n          - name: b\n            needs: [a]\n            dependencies: [c]\n            steps: [{run: x}]\n" +
			"          - name: c\n            steps: [{run: x}]\n": "job b depends on c, which is not a job it needs",
	} {
		_, err := Parse([]byte(job(body)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s:\n%v\nwant %q", body, err, want)
		}
	}

	// A matrix job named with its variables is needed as a whole by that name.
	f, err = Parse([]byte(job("      - name: A\n        jobs:\n          - name: lint on ${OS}\n            matrix: { OS: [linux, darwin] }\n" +
		"            steps: [{run: x}]\n          - name: report\n            needs: [\"lint on ${OS}\"]\n            steps: [{run: x}]\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got := needsOf(f.Pipelines[0].Stages[0].Jobs[2]); got != "A/lint on linux,A/lint on darwin" {
		t.Errorf("report needs %s", got)
	}

	// Dependencies of a job with needs: jobs it waits for, also indirectly.
	f, err = Parse([]byte(job("      - name: A\n        jobs:\n          - name: a\n            steps: [{run: x}]\n" +
		"          - name: b\n            needs: [a]\n            steps: [{run: x}]\n" +
		"          - name: c\n            needs: [b]\n            dependencies: [a]\n            steps: [{run: x}]\n")))
	if err != nil {
		t.Fatal(err)
	}
	if c := f.Pipelines[0].Stages[0].Jobs[2]; strings.Join(c.Dependencies, ",") != "A/a" {
		t.Errorf("dependencies = %v", c.Dependencies)
	}

	// A job that needs one that does not run for every event is warned about.
	f, err = Parse([]byte(job("      - name: A\n        jobs:\n          - name: deploy\n            if: branch == 'main'\n            steps: [{run: x}]\n" +
		"          - name: notify\n            needs: [deploy]\n            steps: [{run: x}]\n" +
		"          - name: tell\n            needs: [{job: deploy, optional: true}]\n            steps: [{run: x}]\n" +
		"          - name: same\n            if: branch == 'main'\n            needs: [deploy]\n            steps: [{run: x}]\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0].Error(), "line 11, column 21: job notify needs deploy, which does not run for every event") {
		t.Errorf("warnings = %v", f.Warnings)
	}
}

// TestPlan: conditions skip stages and jobs with their reason, a job whose need does not run is skipped unless the
// need is optional, and only and except leave jobs out of the run as before.
func TestPlan(t *testing.T) {
	f, err := Parse([]byte(`version: 1
variables:
  MODE: full
pipelines:
  ci:
    variables:
      REGION: eu
    on:
      push: { branches: ["**"] }
      manual: { variables: { TARGET: { default: staging } } }
    stages:
      - name: Build
        jobs:
          - name: build
            steps: [{run: make}]
          - name: docs
            if: changed('docs/**')
            steps: [{run: make docs}]
          - name: publish-docs
            needs: [docs]
            steps: [{run: make publish}]
          - name: maybe-docs
            needs: [{job: docs, optional: true}]
            steps: [{run: make maybe}]
      - name: Matrix
        jobs:
          - name: test
            matrix: { GO: ["1.26", "1.27"] }
            if: vars.GO == '1.27' || vars.MODE == 'quick'
            steps: [{run: go test}]
      - name: Deploy
        if: branch == 'main' && vars.REGION == 'eu'
        jobs:
          - name: deploy
            steps: [{run: make deploy}]
      - name: Release
        only: { tags: ["v*"] }
        jobs:
          - name: release
            steps: [{run: make release}]
      - name: Notify
        jobs:
          - name: mail
            except: { events: [push] }
            steps: [{run: make mail}]
          - name: chat
            if: vars.TARGET == 'production'
            steps: [{run: make chat}]
`))
	if err != nil {
		t.Fatal(err)
	}
	pl := f.Pipelines[0]
	plan := func(ev Event, run map[string]string) string {
		var out []string
		for _, pj := range pl.Plan(f, ev, "Change it", run) {
			s := fmt.Sprintf("%d:%s", pj.Stage, pj.Job.Name)
			if pj.Skip != "" {
				s += "=" + pj.Skip + "(" + pj.Detail + ")"
			}
			out = append(out, s)
		}
		return strings.Join(out, " | ")
	}
	got := plan(Event{Kind: EventPush, Branch: "main", Changes: []string{"src/a.go"}, ChangesKnown: true}, nil)
	want := "0:build | 0:docs=condition(changed('docs/**')) | 0:publish-docs=need-absent(Build/docs) | 0:maybe-docs | " +
		"1:test (1.26)=condition(vars.GO == '1.27' || vars.MODE == 'quick') | 1:test (1.27) | 2:deploy | 3:chat=condition(vars.TARGET == 'production')"
	if got != want {
		t.Errorf("push to main:\n%s\nwant\n%s", got, want)
	}
	got = plan(Event{Kind: EventPush, Branch: "feature", Changes: []string{"docs/a.md"}, ChangesKnown: true}, nil)
	if !strings.Contains(got, "0:docs | 0:publish-docs | ") || !strings.Contains(got, "2:deploy=stage-condition(branch == 'main' && vars.REGION == 'eu')") {
		t.Errorf("push to feature: %s", got)
	}
	// The variables of a manual run decide, too; mail runs for manual runs.
	got = plan(Event{Kind: EventManual, Branch: "main"}, map[string]string{"TARGET": "production", "MODE": "quick"})
	if !strings.Contains(got, "1:test (1.26) | 1:test (1.27)") || !strings.Contains(got, "3:mail | 3:chat") {
		t.Errorf("manual run: %s", got)
	}
	// Nothing that runs: no run.
	only, err := Parse([]byte("version: 1\npipelines:\n  ci:\n    stages:\n      - name: A\n        if: tag != ''\n        jobs:\n" +
		"          - name: a\n            steps: [{run: x}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := only.Pipelines[0].Plan(only, Event{Kind: EventPush, Branch: "main"}, "", nil); got != nil {
		t.Errorf("nothing runs: %+v", got)
	}
}
