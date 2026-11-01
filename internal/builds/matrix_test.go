// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"archive/zip"
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/testutil/apitest"
)

const matrixPipeline = `version: 1
pipelines:
  ci:
    stages:
      - name: Test
        jobs:
          - name: test
            runs-on: [fake]
            matrix:
              GO: ["1.26", "1.27"]
              DB: [postgres, sqlite]
            matrix-exclude:
              - { GO: "1.26", DB: sqlite }
            variables: { CGO_ENABLED: "0" }
            artifacts:
              - name: report-${DB}
                paths: [report.xml]
            steps:
              - run: go test ./...
      - name: Package
        jobs:
          - name: package
            runs-on: [fake]
            dependencies: [test]
            steps:
              - run: make dist
`

// TestMatrixRun: the jobs of a matrix run as jobs of their own, with the matrix values among their variables, and
// a job that depends on the matrix receives the artifacts of all its jobs.
func TestMatrixRun(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MAT", "name": "Matrix"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MAT/repos", map[string]any{"name": "app", "readme": true}, nil)
	agent := newFakeAgent(t, env)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/MAT/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": matrixPipeline}, nil)
	env.WorkJobs()

	var run struct {
		Stages []struct {
			Name string `json:"name"`
			Jobs []struct {
				Name         string            `json:"name"`
				Matrix       string            `json:"matrix"`
				MatrixValues map[string]string `json:"matrixValues"`
			} `json:"jobs"`
		} `json:"stages"`
	}
	admin.Get("/api/v1/code/repos/MAT/app/runs/1", &run)
	var names []string
	for _, st := range run.Stages {
		for _, j := range st.Jobs {
			names = append(names, st.Name+"/"+j.Name+"/"+j.Matrix+"/"+j.MatrixValues["DB"])
		}
	}
	if got := strings.Join(names, "|"); got != "Test/test (1.26, postgres)/test/postgres|Test/test (1.27, postgres)/test/postgres|"+
		"Test/test (1.27, sqlite)/test/sqlite|Package/package//" {
		t.Fatalf("jobs = %s", got)
	}

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, _ := zw.Create("report.xml")
	_, _ = w.Write([]byte("<testsuite/>"))
	_ = zw.Close()
	got := map[string]string{}
	for range 3 {
		p := agent.claim()
		got[p.Job.Name] = p.Variables["GO"] + "/" + p.Variables["DB"] + "/" + p.Variables["CGO_ENABLED"]
		name := p.Job.Artifacts[0].Name
		if name != "report-"+p.Variables["DB"] {
			t.Errorf("%s: artifact %s", p.Job.Name, name)
		}
		if r := agent.do(p, http.MethodPut, "/artifacts/"+name, bytes.NewReader(archive.Bytes())); r.Status >= 300 {
			t.Fatalf("artifact of %s: %d %s", p.Job.Name, r.Status, r.Body)
		}
		if r := agent.do(p, http.MethodPost, "/complete", protocol.CompleteRequest{State: protocol.StateSucceeded}); r.Status >= 300 {
			t.Fatalf("complete: %d %s", r.Status, r.Body)
		}
	}
	want := map[string]string{"test (1.26, postgres)": "1.26/postgres/0", "test (1.27, postgres)": "1.27/postgres/0", "test (1.27, sqlite)": "1.27/sqlite/0"}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s ran with %q, want %q", name, got[name], v)
		}
	}
	pkg := agent.claim()
	var from []string
	for _, a := range pkg.Artifacts {
		from = append(from, a.Job+":"+a.Name)
	}
	slices.Sort(from)
	if got := strings.Join(from, "|"); got != "Test/test (1.26, postgres):report-postgres|Test/test (1.27, postgres):report-postgres|Test/test (1.27, sqlite):report-sqlite" {
		t.Errorf("artifacts of the package job = %s", got)
	}
}
