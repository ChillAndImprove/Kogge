// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// exampleFiles reads the files that testdata/example.yml includes from testdata.
type exampleFiles struct{}

func (exampleFiles) Read(_ context.Context, repository, ref, path string) ([]byte, string, error) {
	name, ok := map[string]string{":.kogge/common.yml": "testdata/common.yml",
		"PLATFORM/ci-templates:templates/security.yml": "testdata/security.yml"}[repository+":"+path]
	if !ok {
		return nil, "", &IncludeError{Message: "the file does not exist"}
	}
	data, err := os.ReadFile(name)
	return data, "c0ffee", err
}

// A pipeline file with what the format offers.
func TestExample(t *testing.T) {
	example, err := os.ReadFile("testdata/example.yml")
	if err != nil {
		t.Fatal(err)
	}
	// On its own, without the files it includes.
	f, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Unresolved || len(f.Pipelines) != 2 {
		t.Fatalf("without includes: %+v", f.Pipelines)
	}
	f, err = Load(context.Background(), example, exampleFiles{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Pipelines) != 3 || f.Pipelines[0].ID != "ci" || f.Pipelines[1].ID != "nightly" || f.Pipelines[2].ID != "security" ||
		f.Pipelines[2].File != "PLATFORM/ci-templates:templates/security.yml" || len(f.Includes) != 2 || len(f.Warnings) != 0 {
		t.Fatalf("pipelines = %+v, includes %+v, warnings %v", f.Pipelines, f.Includes, f.Warnings)
	}
	// The file overrides the variables of the files it includes; their defaults apply to all jobs.
	if f.Variables["GOFLAGS"] != "-mod=readonly" || f.Variables["GOPROXY"] != "https://proxy.golang.org" || f.Defaults.Retry != 1 {
		t.Errorf("variables = %v, defaults %+v", f.Variables, f.Defaults)
	}
	ci := f.Pipelines[0]
	if len(ci.Stages) != 3 || ci.Stages[0].Jobs[0].Name != "unit" || ci.Stages[0].Jobs[0].Image != "docker.io/library/golang:1.27" ||
		ci.Stages[0].Jobs[1].Image != "docker.io/golangci/golangci-lint:v2" || ci.Stages[0].Jobs[0].Timeout != 30*time.Minute {
		t.Errorf("ci = %+v", ci.Stages[0].Jobs[0])
	}
	unit := ci.Stages[0].Jobs[0]
	if len(unit.Services) != 1 || unit.Services[0].Name != "postgres" || unit.Reports.JUnit[0] != "report.xml" || unit.Cache[0].Key != "go-modules" ||
		unit.Retry != 1 {
		t.Errorf("unit = %+v", unit)
	}
	if unit.Resources != (Resources{MilliCPUs: 4000, Memory: 8 << 30}) || unit.Services[0].Resources != (Resources{MilliCPUs: 2000, Memory: 1 << 30}) ||
		ci.Stages[0].Jobs[1].Resources != (Resources{MilliCPUs: 2000, Memory: 4 << 30}) {
		t.Errorf("resources: %+v %+v", unit.Resources, unit.Services[0].Resources)
	}
	if got := unit.Labels(); strings.Join(got, ",") != "docker,linux" {
		t.Errorf("labels = %v", got)
	}
	docs := ci.Stages[0].Jobs[2]
	build, sbom := ci.Stages[1].Jobs[0], ci.Stages[1].Jobs[1]
	if docs.If == nil || docs.Needs != nil || needsOf(build) != "Test/unit,Test/lint" || needsOf(sbom) != "Package/build,Test/docs!a?" {
		t.Errorf("docs %v %v, build %s, sbom %s", docs.If, docs.Needs, needsOf(build), needsOf(sbom))
	}
	prod := ci.Stages[2].Jobs[1]
	if prod.Environment != "production" || !prod.Manual || ci.Stages[2].Only == nil || ci.Stages[2].If == nil || needsOf(prod) != "Deploy/staging" {
		t.Errorf("production = %+v", prod)
	}
	if build.Artifacts[0].ExpireIn != 30*24*time.Hour {
		t.Errorf("artifact = %+v", build.Artifacts)
	}
	nightly := f.Pipelines[1]
	if len(nightly.On.Schedule) != 1 || nightly.On.Schedule[0].Branch != "main" || nightly.Stages[0].Jobs[0].Timeout != 2*time.Hour {
		t.Errorf("nightly = %+v", nightly)
	}
	if report := nightly.Stages[0].Jobs[2]; needsOf(report) != "Integration/long-tests (postgres),Integration/long-tests (mariadb)" {
		t.Errorf("report needs %s", needsOf(report))
	}
	// A push of a feature branch that changes code: docs is skipped by its condition, sbom does not need it; the
	// stage Deploy is only for main.
	var plan []string
	for _, pj := range ci.Plan(f, Event{Kind: EventPush, Branch: "feature/x", Changes: []string{"main.go"}, ChangesKnown: true}, "Fix it", nil) {
		plan = append(plan, pj.Job.Name+":"+pj.Skip)
	}
	if got := strings.Join(plan, " "); got != "unit: lint: docs:condition build: sbom:" {
		t.Errorf("plan = %s", got)
	}

	// Triggers and filters.
	names := func(ps []*Pipeline) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.ID)
		}
		return strings.Join(s, ",")
	}
	if got := names(f.Triggered(Event{Kind: EventPush, Branch: "feature/x"})); got != "ci" {
		t.Errorf("push = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventPush, Branch: "main"})); got != "ci,security" {
		t.Errorf("push to main = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventPush, Tag: "v1.0"})); got != "ci" {
		t.Errorf("tag push = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventPush, Tag: "release-1"})); got != "" {
		t.Errorf("other tag = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventPullRequest, Branch: "feature/x", TargetBranch: "main"})); got != "ci" {
		t.Errorf("pull request = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventPullRequest, Branch: "feature/x", TargetBranch: "develop"})); got != "" {
		t.Errorf("pull request to develop = %s", got)
	}
	if got := names(f.Triggered(Event{Kind: EventManual})); got != "ci" {
		t.Errorf("manual = %s", got)
	}
	deploy := ci.Stages[2]
	if Runs(deploy.Only, deploy.Except, Event{Kind: EventPush, Branch: "feature/x"}) || !Runs(deploy.Only, deploy.Except, Event{Kind: EventPush, Branch: "main"}) {
		t.Error("stage filter")
	}
}

func TestErrors(t *testing.T) {
	_, err := Parse([]byte(`version: 1
pipelines:
  CI:
    stages:
      - name: Test
        jobs:
          - name: unit
            steps:
              - run: ""
            retry: 5
            colour: red
          - name: unit
            steps: [{run: make}]
            dependencies: [later]
            timeout: 3d
            artifacts:
              - name: dist
                paths: [dist]
                expire-in: 400d
`))
	errs, ok := err.(Errors)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	text := errs.Error()
	for _, want := range []string{
		`line 3, column 3: the pipeline id "CI"`,
		`a step needs a script`,
		`retry must be a number from 0 to 2`,
		`line 11, column 13: job: unknown key "colour"`,
		`the job name "unit" is used twice`,
		`depends on later`,
		`at most 24h`,
		`expire-in must be at most 365d`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if _, err := Parse([]byte("pipelines: [")); err == nil {
		t.Error("syntax error not reported")
	}
	if _, err := Parse([]byte("version: 2\npipelines: {}")); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("version: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	f, err := Parse([]byte(`version: 1
pipelines:
  build:
    stages:
      - name: Build
        jobs:
          - name: make
            steps:
              - run: |
                  make all
                  make test
`))
	if err != nil {
		t.Fatal(err)
	}
	j := f.Pipelines[0].Stages[0].Jobs[0]
	if j.Executor() != "shell" || j.Timeout != DefaultTimeout || j.Steps[0].Name != "make all" || j.Clone.Depth != 50 || !j.Clone.LFS {
		t.Errorf("job = %+v", j)
	}
	// Without "on", every push runs the pipeline.
	if len(f.Triggered(Event{Kind: EventPush, Branch: "any/branch"})) != 1 || len(f.Triggered(Event{Kind: EventManual})) != 0 {
		t.Error("default trigger")
	}
}

func TestCron(t *testing.T) {
	c, err := ParseCron("0 2 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC) // a Monday
	if !c.Matches(mon) || c.Matches(mon.Add(time.Minute)) || c.Matches(mon.AddDate(0, 0, 6)) {
		t.Error("weekday schedule")
	}
	c, _ = ParseCron("*/15 * 1 jan *")
	if !c.Matches(time.Date(2027, 1, 1, 10, 45, 0, 0, time.UTC)) || c.Matches(time.Date(2027, 1, 1, 10, 50, 0, 0, time.UTC)) {
		t.Error("steps and names")
	}
	for _, bad := range []string{"* * *", "60 * * * *", "1-0 * * * *", "*/0 * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if d, err := ParseDuration("1h30m"); err != nil || d != 90*time.Minute {
		t.Errorf("duration = %v %v", d, err)
	}
	if !MatchGlob("release/*", "release/1.0") || MatchGlob("release/*", "release/1.0/x") || !MatchGlob("feature/**", "feature/a/b") {
		t.Error("globs")
	}
}

// TestPatternLimits: overlong patterns are refused in pipeline files and match nothing, so they cannot grow the
// cache of compiled patterns.
func TestPatternLimits(t *testing.T) {
	long := strings.Repeat("a", MaxPattern+1)
	if MatchGlob(long, long) {
		t.Error("an overlong pattern matched")
	}
	if !MatchGlob("release/**", "release/1/2") || MatchGlob("release/*", "release/1/2") {
		t.Error("patterns do not match as before")
	}
	for pattern, names := range map[string][2][]string{
		"**/*.md":      {{"README.md", "docs/a.md", "a/b/c.md"}, {"README.txt", "a.md/x"}},
		"docs/**/*.md": {{"docs/a.md", "docs/x/y/a.md"}, {"docsa.md", "web/docs/a.md"}},
		"**/testdata":  {{"testdata", "a/b/testdata"}, {"testdata/x", "atestdata"}},
		"a**/b":        {{"a/b", "ax/y/b"}, {"b"}},
		"**":           {{"a", "a/b/c"}, nil},
	} {
		for _, n := range names[0] {
			if !MatchGlob(pattern, n) {
				t.Errorf("%s does not match %s", pattern, n)
			}
		}
		for _, n := range names[1] {
			if MatchGlob(pattern, n) {
				t.Errorf("%s matches %s", pattern, n)
			}
		}
	}
	file := "version: 1\npipelines:\n  ci:\n    on: { push: { branches: [\"" + long + "\"] } }\n" +
		"    stages: [{ name: B, jobs: [{ name: j, runs-on: [x], steps: [{ run: \"true\" }] }] }]\n"
	if _, err := Parse([]byte(file)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("overlong branch pattern: %v", err)
	}
}

// TestImages: only container image references are accepted — no options for the container command line (audit
// L35) and no service names that are not host names.
func TestImages(t *testing.T) {
	for ref, ok := range map[string]bool{
		"golang":                           true,
		"golang:1.25":                      true,
		"golang:1.25-alpine":               true,
		"library/postgres:18":              true,
		"registry.example.com/team/app:v1": true,
		"registry.example.com:5000/app":    true,
		"localhost:5000/app@sha256:" + strings.Repeat("ab", 32): true,
		"[2001:db8::1]:5000/app":                                true,
		"ghcr.io/org/tool_name.v2__x:latest":                    true,
		"--privileged":                                          false,
		"-v":                                                    false,
		"alpine --volume=/:/host":                               false,
		"Alpine":                                                false,
		"alpine:":                                               false,
		"alpine:" + strings.Repeat("x", 129):                    false,
		"alpine@sha256:abc":                                     false,
		"alpine\n--privileged":                                  false,
		"registry.example.com/team/../app":                      false,
	} {
		if got := ValidImage(ref); got != ok {
			t.Errorf("%q: %v", ref, got)
		}
	}
	_, err := Parse([]byte(`version: 1
defaults:
  image: --privileged
pipelines:
  ci:
    stages:
      - name: Test
        jobs:
          - name: unit
            image: "alpine --volume=/:/host"
            services:
              - name: --network=host
                image: postgres:18
            steps: [{run: make}]
`))
	if err == nil {
		t.Fatal("bad images were accepted")
	}
	for _, want := range []string{`defaults.image "--privileged" is not a container image`, `image "alpine --volume=/:/host"`,
		`service name "--network=host" is not a host name`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %s", want, err)
		}
	}
}

// TestPaths: pushes to branches and pull requests can be selected by the files they change; tags and unknown
// changes are not filtered.
func TestPaths(t *testing.T) {
	f, err := Parse([]byte(`version: 1
pipelines:
  web:
    on:
      push:
        branches: ["**"]
        tags: ["web-*"]
        paths: ["web/**", "package.json"]
      pull-request:
        paths: ["web/**"]
    stages:
      - name: Build
        jobs:
          - name: build
            steps:
              - run: make web
  server:
    on:
      push:
        paths-ignore: ["docs/**", "**/*.md"]
      pull-request:
        paths: ["internal/**"]
        paths-ignore: ["internal/**/testdata/**"]
    stages:
      - name: Build
        jobs:
          - name: build
            steps:
              - run: make server
`))
	if err != nil {
		t.Fatal(err)
	}
	if !f.UsesPaths() {
		t.Error("the file uses paths")
	}
	run := func(ev Event) string {
		var ids []string
		for _, p := range f.Triggered(ev) {
			ids = append(ids, p.ID)
		}
		return strings.Join(ids, ",")
	}
	push := func(files ...string) Event {
		return Event{Kind: EventPush, Branch: "main", Changes: files, ChangesKnown: true}
	}
	for files, want := range map[string]string{
		"web/src/app.ts":                     "web,server",
		"README.md":                          "",
		"docs/guide.md,docs/img/a.png":       "",
		"internal/app/app.go":                "server",
		"package.json":                       "web,server",
		"web/README.md":                      "web",
		"docs/guide.md,internal/x/x_test.go": "server",
	} {
		if got := run(push(strings.Split(files, ",")...)); got != want {
			t.Errorf("push of %s = %q, want %q", files, got, want)
		}
	}
	// A push that changes nothing (a branch moved to an existing commit) runs nothing that filters by paths.
	if got := run(Event{Kind: EventPush, Branch: "main", ChangesKnown: true}); got != "" {
		t.Errorf("no changes = %q", got)
	}
	if got := run(Event{Kind: EventPush, Branch: "main"}); got != "web,server" {
		t.Errorf("unknown changes = %q", got)
	}
	if got := run(Event{Kind: EventPush, Tag: "web-1.0", ChangesKnown: true}); got != "web" {
		t.Errorf("tag = %q", got)
	}
	skipped := func(ev Event) string {
		var ids []string
		for _, p := range f.SkippedByPaths(ev) {
			ids = append(ids, p.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := skipped(push("docs/guide.md")); got != "web,server" {
		t.Errorf("skipped by docs = %q", got)
	}
	if got := skipped(push("internal/app/app.go")); got != "web" {
		t.Errorf("skipped by server code = %q", got)
	}
	if got := skipped(Event{Kind: EventPush, Branch: "main"}); got != "" {
		t.Errorf("skipped with unknown changes = %q", got)
	}
	if got := skipped(Event{Kind: EventPush, Tag: "v1", ChangesKnown: true}); got != "" {
		t.Errorf("skipped tag = %q", got)
	}
	pr := func(files ...string) Event {
		return Event{Kind: EventPullRequest, Branch: "feature", TargetBranch: "main", Changes: files, ChangesKnown: true}
	}
	if got := run(pr("internal/app/testdata/fixture.json")); got != "" {
		t.Errorf("ignored test data = %q", got)
	}
	if got := run(pr("internal/app/testdata/fixture.json", "internal/app/app.go", "web/x.ts")); got != "web,server" {
		t.Errorf("pull request = %q", got)
	}

	plain, err := Parse([]byte("version: 1\npipelines:\n  ci:\n    stages:\n      - name: A\n        jobs:\n          - name: a\n            steps:\n              - run: \"true\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.UsesPaths() {
		t.Error("a file without paths uses paths")
	}
	_, err = Parse([]byte("version: 1\npipelines:\n  ci:\n    on:\n      pull-request:\n        paths-ignore: { a: b }\n" +
		"    stages: [{ name: B, jobs: [{ name: j, steps: [{ run: \"true\" }] }] }]\n"))
	if err == nil || !strings.Contains(err.Error(), "on.pull-request.paths-ignore must be a list") {
		t.Errorf("paths that are no list: %v", err)
	}
}

// TestMatrix: a job with a matrix makes a job for every combination of values that matrix-exclude leaves, named
// after the values, with them as variables and in its image, runs-on, cache keys and artifact names; depending on
// the job means depending on all jobs of its matrix.
func TestMatrix(t *testing.T) {
	f, err := Parse([]byte(`version: 1
pipelines:
  ci:
    stages:
      - name: Test
        jobs:
          - name: test
            matrix:
              GO: ["1.26", "1.27"]
              OS: [linux, windows]
            matrix-exclude:
              - { GO: "1.26", OS: windows }
            image: docker.io/library/golang:${GO}
            runs-on: ["${OS}", docker]
            variables: { CGO_ENABLED: "0" }
            services:
              - { name: db, image: "docker.io/library/postgres:${GO}" }
            cache:
              - key: go-${GO}-${OS}
                paths: [.cache]
            artifacts:
              - { name: "report-${OS}", paths: [report.xml] }
            steps:
              - run: go test ./...
          - name: lint on ${OS}
            matrix: { OS: [linux, darwin] }
            steps:
              - run: make lint
      - name: Package
        jobs:
          - name: package
            dependencies: [test, Test/lint on linux]
            steps:
              - run: make dist
`))
	if err != nil {
		t.Fatal(err)
	}
	jobs := f.Pipelines[0].Stages[0].Jobs
	var names []string
	for _, j := range jobs {
		names = append(names, j.Name)
	}
	if got := strings.Join(names, "|"); got != "test (1.26, linux)|test (1.27, linux)|test (1.27, windows)|lint on linux|lint on darwin" {
		t.Fatalf("jobs = %s", got)
	}
	j := jobs[2]
	if j.Image != "docker.io/library/golang:1.27" || strings.Join(j.RunsOn, ",") != "windows,docker" || j.Matrix != "test" ||
		j.MatrixValues["GO"] != "1.27" || j.MatrixValues["OS"] != "windows" || j.Services[0].Image != "docker.io/library/postgres:1.27" ||
		j.Cache[0].Key != "go-1.27-windows" || j.Artifacts[0].Name != "report-windows" {
		t.Errorf("job = %+v", j)
	}
	if j.Variables["GO"] != "1.27" || j.Variables["OS"] != "windows" || j.Variables["CGO_ENABLED"] != "0" || len(j.Variables) != 3 {
		t.Errorf("variables = %v", j.Variables)
	}
	// The jobs share nothing they could change in each other.
	if jobs[0].RunsOn[0] != "linux" || jobs[0].Cache[0].Key != "go-1.26-linux" || jobs[0].Variables["GO"] != "1.26" {
		t.Errorf("first job = %+v", jobs[0])
	}
	deps := f.Pipelines[0].Stages[1].Jobs[0].Dependencies
	if got := strings.Join(deps, "|"); got != "Test/test (1.26, linux)|Test/test (1.27, linux)|Test/test (1.27, windows)|Test/lint on linux" {
		t.Errorf("dependencies = %s", got)
	}

	job := func(body string) string {
		return "version: 1\npipelines:\n  ci:\n    stages:\n      - name: S\n        jobs:\n          - name: j\n" + body +
			"            steps:\n              - run: \"true\"\n"
	}
	for body, want := range map[string]string{
		"            matrix: { OS: [linux] }\n            image: docker.io/library/alpine:${ARCH}\n":                                                       "image uses ${ARCH}, which is not a variable of the job's matrix",
		"            image: docker.io/library/alpine:${ARCH}\n":                                                                                            "image uses ${ARCH}, but the job has no matrix",
		"            runs-on: [\"${OS}\"]\n":                                                                                                               "runs-on uses ${OS}, but the job has no matrix",
		"            matrix: { A: [1, 2, 3, 4, 5, 6, 7, 8, 9], B: [1, 2, 3, 4, 5, 6, 7, 8, 9] }\n":                                                         "makes 81 jobs; at most 64 are allowed",
		"            matrix: { A: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], B: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], C: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11] }\n": "makes more than 64 jobs",
		"            matrix: { OS: [linux] }\n            matrix-exclude: [{ ARCH: arm64 }]\n":                                                             "matrix-exclude names ARCH, which is not a variable of the matrix",
		"            matrix: { OS: [linux] }\n            matrix-exclude: [{ OS: linux }]\n":                                                               "matrix-exclude leaves no job of j",
		"            matrix-exclude: [{ OS: linux }]\n":                                                                                                    "matrix-exclude needs a matrix",
		"            matrix: { OS: [linux] }\n            variables: { OS: x }\n":                                                                          "OS is a variable of the matrix and of the job",
		"            matrix: { OS: [linux, linux] }\n":                                                                                                     "the value \"linux\" is given twice",
		"            matrix: { OS: [] }\n":                                                                                                                 "matrix OS has no values",
		"            matrix: [linux]\n":                                                                                                                    "matrix must be a mapping",
		"            matrix: { os-name: [linux] }\n":                                                                                                       "\"os-name\" is not a valid variable name",
		"            matrix: { ENV: [Prod] }\n            environment: ${ENV}\n":                                                                           "the environment name \"Prod\" may only have",
	} {
		_, err := Parse([]byte(job(body)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", strings.TrimSpace(body), err, want)
		}
	}
	// A mistake shared by the jobs of a matrix is reported once.
	_, err = Parse([]byte(job("            matrix: { OS: [linux, darwin] }\n            image: docker.io/library/alpine:${ARCH}\n")))
	if err == nil || strings.Count(err.Error(), "${ARCH}") != 1 {
		t.Errorf("repeated mistake: %v", err)
	}
	// Names of matrix jobs must not collide with other jobs of the stage.
	_, err = Parse([]byte(job("            matrix: { OS: [linux] }\n") + "          - name: j (linux)\n            steps:\n              - run: \"true\"\n"))
	if err == nil || !strings.Contains(err.Error(), `the job name "j (linux)" is used twice`) {
		t.Errorf("collision: %v", err)
	}
}

func TestPipelineTrigger(t *testing.T) {
	f, err := Parse([]byte(`version: 1
pipelines:
  build:
    stages:
      - name: Build
        jobs:
          - name: build
            steps: [{run: make}]
  deploy:
    on:
      pipeline:
        name: build
        branches: [main, "release/*"]
    stages:
      - name: Deploy
        jobs:
          - name: deploy
            steps: [{run: make deploy}]
  report:
    on:
      pipeline: [build, deploy]
    stages:
      - name: Report
        jobs:
          - name: report
            steps: [{run: make report}]
  alert:
    on:
      pipeline:
        - name: build
          result: failure
        - {name: build, repository: LIB/core, result: any, tags: ["v*"]}
    stages:
      - name: Alert
        jobs:
          - name: alert
            steps: [{run: make alert}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if d := f.Pipelines[1].On; d.Push != nil || len(d.Pipeline) != 1 || d.Pipeline[0].Result != ResultSuccess {
		t.Errorf("deploy runs after build only, when it succeeds: %+v", d)
	}
	after := func(u Upstream) string {
		var ids []string
		for _, p := range f.Triggered(Event{Kind: EventPipeline, Upstream: &u}) {
			ids = append(ids, p.ID)
		}
		return strings.Join(ids, " ")
	}
	for _, c := range []struct {
		u    Upstream
		want string
	}{
		{Upstream{Same: true, Pipeline: "build", Result: ResultSuccess, Branch: "main"}, "deploy report"},
		{Upstream{Same: true, Pipeline: "build", Result: ResultSuccess, Branch: "release/2.1"}, "deploy report"},
		{Upstream{Same: true, Pipeline: "build", Result: ResultSuccess, Branch: "feature/x"}, "report"},
		{Upstream{Same: true, Pipeline: "build", Result: ResultSuccess, Tag: "v1.0"}, "report"},
		{Upstream{Same: true, Pipeline: "build", Result: ResultFailure, Branch: "main"}, "alert"},
		{Upstream{Same: true, Pipeline: "deploy", Result: ResultSuccess, Branch: "main"}, "report"},
		{Upstream{Same: true, Pipeline: "report", Result: ResultSuccess, Branch: "main"}, ""},
		// Another repository: only pipelines that name it, and its tags as they are named.
		{Upstream{Repository: "LIB/core", Pipeline: "build", Result: ResultSuccess, Branch: "main"}, ""},
		{Upstream{Repository: "lib/CORE", Pipeline: "build", Result: ResultFailure, Tag: "v2"}, "alert"},
		{Upstream{Repository: "LIB/other", Pipeline: "build", Result: ResultFailure, Tag: "v2"}, ""},
	} {
		if got := after(c.u); got != c.want {
			t.Errorf("after %+v: %q, want %q", c.u, got, c.want)
		}
	}
	if got := f.Triggered(Event{Kind: EventPipeline}); len(got) != 0 {
		t.Errorf("an event without its run: %v", got)
	}
	if got := f.Upstreams(); len(got) != 1 || got[0] != "LIB/core" {
		t.Errorf("upstreams = %v", got)
	}

	_, err = Parse([]byte(`version: 1
pipelines:
  build:
    on:
      pipeline:
        - build
        - {name: Deploy}
        - {repository: "no slash", result: sometimes}
        - [nested]
    stages:
      - name: Build
        jobs:
          - name: build
            steps: [{run: make}]
            only: {events: [pipeline, merge]}
`))
	for _, want := range []string{
		"pipeline build cannot run after itself",
		`"Deploy" is not the id of a pipeline`,
		"needs the name of the pipeline to follow",
		`"no slash" is not a repository`,
		"result must be success, failure or any",
		"must be the id of a pipeline or a mapping",
		`unknown event "merge"`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
