// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// files is a Reader of files by repository and path; refs of other repositories resolve to "commit-<ref>".
type files struct {
	m     map[string]string // "repo:path" ("" repo: the pipeline's)
	reads []string
	fail  error
}

func (fs *files) Read(_ context.Context, repository, ref, path string) ([]byte, string, error) {
	fs.reads = append(fs.reads, repository+"@"+ref+":"+path)
	if fs.fail != nil {
		return nil, "", fs.fail
	}
	if repository == "LOCKED/secret" {
		return nil, "", &IncludeError{Message: "the repository does not exist or the person may not read it"}
	}
	data, ok := fs.m[repository+":"+path]
	if !ok {
		return nil, "", &IncludeError{Message: "the file does not exist"}
	}
	commit := "run-commit"
	if repository != "" {
		if ref == "" {
			ref = "main"
		}
		commit = strings.TrimPrefix(ref, "commit-")
		commit = "commit-" + commit
	}
	return []byte(data), commit, nil
}

func TestInclude(t *testing.T) {
	fs := &files{m: map[string]string{
		":.kogge/common.yml": `
variables:
  GOFLAGS: -mod=readonly
  REGION: eu
defaults:
  image: docker.io/library/golang:1.27
  timeout: 20m
include:
  - .kogge/more.yml
`,
		":.kogge/more.yml": `
variables:
  MORE: "yes"
  REGION: us
`,
		"SHARED/ci:templates/security.yml": `
version: 1
include:
  - templates/base.yml
pipelines:
  security:
    on: { push: { branches: [main] } }
    stages:
      - name: Scan
        jobs:
          - name: scan
            steps: [{run: make scan}]
`,
		"SHARED/ci:templates/base.yml": `
variables:
  SCANNER: trivy
`,
	}}
	main := `version: 1
include:
  - .kogge/common.yml
  - repository: SHARED/ci
    file: templates/security.yml
    ref: release
variables:
  REGION: ap
pipelines:
  ci:
    stages:
      - name: Build
        jobs:
          - name: build
            steps: [{run: make}]
`
	f, err := Load(context.Background(), []byte(main), fs)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range f.Pipelines {
		ids = append(ids, p.ID+"@"+p.File)
	}
	if got := strings.Join(ids, " "); got != "ci@ security@SHARED/ci:templates/security.yml" {
		t.Errorf("pipelines = %s", got)
	}
	// The pipeline file overrides what it includes, and common.yml what it includes.
	if f.Variables["REGION"] != "ap" || f.Variables["GOFLAGS"] != "-mod=readonly" || f.Variables["MORE"] != "yes" || f.Variables["SCANNER"] != "trivy" {
		t.Errorf("variables = %v", f.Variables)
	}
	// Defaults of an included file apply to all jobs.
	if j := f.Pipelines[0].Stages[0].Jobs[0]; j.Image != "docker.io/library/golang:1.27" || j.Timeout.Minutes() != 20 {
		t.Errorf("job = %+v", j)
	}
	var incs []string
	for _, inc := range f.Includes {
		incs = append(incs, fmt.Sprintf("%s|%s|%s|%s", inc.Repository, inc.Path, inc.Ref, inc.Commit))
	}
	want := "|.kogge/common.yml||run-commit |.kogge/more.yml||run-commit SHARED/ci|templates/security.yml|release|commit-release " +
		"SHARED/ci|templates/base.yml|release|commit-release"
	if got := strings.Join(incs, " "); got != want {
		t.Errorf("includes = %s\nwant %s", got, want)
	}
	// A file of another repository includes files of its own repository at the same commit.
	if got := strings.Join(fs.reads, " "); !strings.Contains(got, "SHARED/ci@commit-release:templates/base.yml") {
		t.Errorf("reads = %s", got)
	}

	// Without a Reader the included files are not read; the rest is checked.
	f, err = Parse([]byte(main))
	if err != nil || !f.Unresolved || len(f.Pipelines) != 1 || len(f.Includes) != 0 {
		t.Errorf("unresolved: %+v %v", f, err)
	}
	if _, err := Parse([]byte("version: 1\ninclude: [.kogge/all.yml]\n")); err != nil {
		t.Errorf("a file with only includes: %v", err)
	}

	check := func(name string, m map[string]string, main, want string) {
		t.Helper()
		_, err := Load(context.Background(), []byte(main), &files{m: m})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v\nwant %q", name, err, want)
		}
	}
	pipeline := "pipelines:\n  ci:\n    stages:\n      - name: A\n        jobs:\n          - name: a\n            steps: [{run: x}]\n"
	check("conflicting variables", map[string]string{":a.yml": "variables:\n  X: a\n", ":b.yml": "variables:\n  X: b\n"},
		"version: 1\ninclude: [a.yml, b.yml]\n"+pipeline, "b.yml: line 2, column 3: variables: X is set in a.yml (line 2) too")
	check("conflicting defaults", map[string]string{":a.yml": "defaults:\n  image: alpine\n", ":b.yml": "defaults:\n  image: debian\n"},
		"version: 1\ninclude: [a.yml, b.yml]\n"+pipeline, "b.yml: line 2, column 3: defaults: image is set in a.yml (line 2) too")
	check("pipeline twice", map[string]string{":a.yml": pipeline},
		"version: 1\ninclude: [a.yml]\n"+pipeline, "a.yml: line 2, column 3: pipeline ci is defined in .kogge/pipelines.yml (line 4) too")
	check("mistake in an included file", map[string]string{":a.yml": "pipelines:\n  ci:\n    stages: 3\n"},
		"version: 1\ninclude: [a.yml]\n", "a.yml: line 3, column 13: stages must be a non-empty list")
	check("syntax error in an included file", map[string]string{":a.yml": "pipelines: [\n"},
		"version: 1\ninclude: [a.yml]\n", "a.yml: line ")
	check("missing file", nil, "version: 1\ninclude: [nope.yml]\n"+pipeline, "line 2, column 11: include nope.yml: the file does not exist")
	check("denied repository", nil, "version: 1\ninclude:\n  - {repository: LOCKED/secret, file: x.yml}\n"+pipeline,
		"include LOCKED/secret:x.yml: the repository does not exist or the person may not read it")
	check("cycle", map[string]string{":a.yml": "include: [b.yml]\n", ":b.yml": "include: [a.yml]\n"},
		"version: 1\ninclude: [a.yml]\n"+pipeline, "b.yml: line 1, column 11: include: the files include each other: .kogge/pipelines.yml → a.yml → b.yml → a.yml")
	check("itself", nil, "version: 1\ninclude: [.kogge/pipelines.yml]\n"+pipeline, "the pipeline file cannot include itself")
	check("twice", map[string]string{":a.yml": ""}, "version: 1\ninclude: [a.yml, a.yml]\n"+pipeline, "a.yml is included twice")
	check("nested too deep", map[string]string{":1.yml": "include: [2.yml]\n", ":2.yml": "include: [3.yml]\n", ":3.yml": "include: [4.yml]\n",
		":4.yml": "variables: {A: b}\n"}, "version: 1\ninclude: [1.yml]\n"+pipeline, "3.yml: line 1, column 11: include: files may include others at most 3 deep")
	many := map[string]string{}
	var list []string
	for i := range MaxIncludes + 1 {
		many[fmt.Sprintf(":f%d.yml", i)] = fmt.Sprintf("variables: {V%d: x}\n", i)
		list = append(list, fmt.Sprintf("f%d.yml", i))
	}
	check("too many", many, "version: 1\ninclude: ["+strings.Join(list, ", ")+"]\n"+pipeline, "a pipeline file may include at most 25 files")
	check("too large", map[string]string{":big.yml": "# " + strings.Repeat("x", MaxIncludeSize)}, "version: 1\ninclude: [big.yml]\n"+pipeline,
		"include big.yml: the file has more than 256 KiB")
	for path, want := range map[string]string{
		"../x.yml": `"../x.yml" is not the path of a YAML file`, "/etc/x.yml": `"/etc/x.yml" is not the path`, "a.txt": `"a.txt" is not the path`,
		"a/./b.yml": `"a/./b.yml" is not the path`,
	} {
		check(path, nil, "version: 1\ninclude: [\""+path+"\"]\n"+pipeline, want)
	}
	check("ref without repository", nil, "version: 1\ninclude:\n  - {file: a.yml, ref: main}\n"+pipeline, "include.ref needs include.repository")
	check("bad repository", nil, "version: 1\ninclude:\n  - {file: a.yml, repository: nope}\n"+pipeline, `include.repository "nope" is not a repository`)
	check("bad ref", nil, "version: 1\ninclude:\n  - {file: a.yml, repository: A/b, ref: \"-x\"}\n"+pipeline, `include.ref "-x" is not a branch`)
	check("not a list", nil, "version: 1\ninclude: {a: b}\n"+pipeline, "include must be a list of files")
	check("unknown key in an included file", map[string]string{":a.yml": "stages: []\n"}, "version: 1\ninclude: [a.yml]\n"+pipeline,
		`a.yml: line 1, column 1: the file: unknown key "stages"`)
	check("version of an included file", map[string]string{":a.yml": "version: 2\n"}, "version: 1\ninclude: [a.yml]\n"+pipeline,
		"a.yml: line 1, column 10: version must be a number from 1 to 1")

	// A file included twice through different files is read once (no conflicts with itself).
	f, err = Load(context.Background(), []byte("version: 1\ninclude: [a.yml, b.yml]\n"+pipeline), &files{m: map[string]string{
		":a.yml": "include: [c.yml]\n", ":b.yml": "include: [c.yml]\n", ":c.yml": "variables: {C: c}\n",
	}})
	if err != nil || len(f.Includes) != 3 || f.Variables["C"] != "c" {
		t.Errorf("diamond: %v %+v", err, f)
	}
	// Errors of the Reader that are no mistakes of the file end the reading.
	broken := errors.New("git broke")
	if _, err := Load(context.Background(), []byte("version: 1\ninclude: [a.yml]\n"+pipeline), &files{fail: broken}); !errors.Is(err, broken) {
		t.Errorf("reader error: %v", err)
	}
}
