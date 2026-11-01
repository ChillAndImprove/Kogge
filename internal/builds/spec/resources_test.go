// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"strings"
	"testing"
)

// TestResources: jobs and services ask for CPUs and memory, over the defaults; only container jobs ask agents for
// them.
func TestResources(t *testing.T) {
	for in, want := range map[string]int64{"2": 2000, "0.5": 500, "1.25": 1250, ".5": 500, "1024": 1024000, "3.": 3000} {
		if got, err := ParseCPUs(in); err != nil || got != want {
			t.Errorf("cpus %q = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "-1", "1.2345", "two", "1e3", "1025", "99999999", "."} {
		if _, err := ParseCPUs(in); err == nil {
			t.Errorf("cpus %q accepted", in)
		}
	}
	for in, want := range map[string]int64{"512m": 512 << 20, "4g": 4 << 30, "4G": 4 << 30, "4gb": 4 << 30, "4GiB": 4 << 30, "1.5g": 3 << 29,
		"2048Mi": 2 << 30, "1t": 1 << 40} {
		if got, err := ParseMemory(in); err != nil || got != want {
			t.Errorf("memory %q = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "4096", "4x", "g", "-1g", "1k", "5t", "1.2.3g"} {
		if _, err := ParseMemory(in); err == nil {
			t.Errorf("memory %q accepted", in)
		}
	}
	if FormatCPUs(1500) != "1.5" || FormatCPUs(2000) != "2" || FormatCPUs(250) != "0.25" || FormatMemory(4<<30) != "4g" || FormatMemory(3<<29) != "1536m" {
		t.Error("formatting")
	}

	f, err := Parse([]byte(`version: 1
defaults:
  image: docker.io/library/golang:1.27
  resources: { cpus: 2, memory: 4g }
pipelines:
  ci:
    stages:
      - name: Test
        jobs:
          - name: unit
            resources: { memory: 8g }
            services:
              - name: db
                image: docker.io/library/postgres:18
                resources: { cpus: 4 }
            steps: [{run: go test}]
          - name: plain
            steps: [{run: go vet}]
          - name: shell
            image: ""
            steps: [{run: make}]
`))
	if err != nil {
		t.Fatal(err)
	}
	jobs := f.Pipelines[0].Stages[0].Jobs
	unit, plain, shell := jobs[0], jobs[1], jobs[2]
	if unit.Resources != (Resources{MilliCPUs: 2000, Memory: 8 << 30}) || unit.Services[0].Resources != (Resources{MilliCPUs: 4000, Memory: 4 << 30}) {
		t.Errorf("unit = %+v, service %+v", unit.Resources, unit.Services[0].Resources)
	}
	if unit.Requests() != (Resources{MilliCPUs: 4000, Memory: 8 << 30}) || plain.Requests() != (Resources{MilliCPUs: 2000, Memory: 4 << 30}) {
		t.Errorf("requests = %+v, %+v", unit.Requests(), plain.Requests())
	}
	// The shell executor limits nothing: shell jobs ask for nothing (the defaults apply to container jobs).
	if shell.Executor() != "shell" || shell.Requests() != (Resources{}) || len(f.Warnings) != 0 {
		t.Errorf("shell = %+v, warnings %v", shell.Requests(), f.Warnings)
	}
	f, err = Parse([]byte("version: 1\npipelines:\n  ci:\n    stages:\n      - name: A\n        jobs:\n          - name: a\n" +
		"            resources: { cpus: 1 }\n            steps: [{run: x}]\n"))
	if err != nil || len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0].Error(), "line 8, column 24: resources apply to container jobs") {
		t.Errorf("shell job with resources: %v %v", err, f.Warnings)
	}
	_, err = Parse([]byte("version: 1\npipelines:\n  ci:\n    stages:\n      - name: A\n        jobs:\n          - name: a\n" +
		"            image: alpine\n            resources: { cpus: lots, memory: 1, disk: 9g }\n            steps: [{run: x}]\n"))
	for _, want := range []string{`resources.cpus: "lots" is not a number of CPUs`, `resources.memory: "1" is not a size of memory`, `resources: unknown key "disk"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
