// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/spec"
)

// TestContainerResources: the containers of a job get the CPUs and memory it asks for (else the agent's defaults),
// no swap beyond the memory, and the agent's limit of processes; an agent refuses what is more than it allows.
func TestContainerResources(t *testing.T) {
	cfg := &Resources{CPUs: 1, Memory: "1g", MaxCPUs: 4, MaxMemory: "8g", Pids: 512}
	l, err := cfg.parse()
	if err != nil {
		t.Fatal(err)
	}
	e := newContainerExecutor("docker", "golang:1.27", []spec.Service{
		{Name: "db", Image: "postgres:18", Variables: map[string]string{"POSTGRES_PASSWORD": "x"}, Resources: spec.Resources{Memory: 2 << 30}},
		{Name: "cache", Image: "redis:8", Command: []string{"redis-server", "--save", ""}},
	}, nil, "/work/w/7", "/work/t/7", "kogge-job-7", false, []string{"kogge.job=7"}, spec.Resources{MilliCPUs: 2500}, l)
	e.network = "kogge-job-7"
	got := strings.Join(e.createArgs(map[string]string{"CI": "true"}), " ")
	want := "create --name kogge-job-7 --network kogge-job-7 --volume /work/w/7:/workspace --volume /work/t/7:/kogge:ro --workdir /workspace " +
		"--entrypoint sh --cpus 2.5 --memory 1073741824 --memory-swap 1073741824 --pids-limit 512 --label kogge.job=7 -e CI golang:1.27 -c " +
		"trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"
	if got != want {
		t.Errorf("create:\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join(e.serviceArgs(e.services[0]), " "); got != "run --detach --name kogge-job-7-db --network kogge-job-7 --network-alias db "+
		"--cpus 1 --memory 2147483648 --memory-swap 2147483648 --pids-limit 512 --label kogge.job=7 -e POSTGRES_PASSWORD postgres:18" {
		t.Errorf("service: %s", got)
	}
	if got := strings.Join(e.serviceArgs(e.services[1]), " "); !strings.Contains(got, "--cpus 1 --memory 1073741824 --memory-swap 1073741824") ||
		!strings.HasSuffix(got, "redis:8 redis-server --save ") {
		t.Errorf("service with defaults: %q", got)
	}
	// Without resources nothing is limited.
	none := newContainerExecutor("podman", "alpine", nil, nil, "/w", "/t", "kogge-job-8", false, nil, spec.Resources{}, limits{})
	if got := strings.Join(none.createArgs(nil), " "); strings.Contains(got, "--cpus") || strings.Contains(got, "--memory") ||
		strings.Contains(got, "--pids-limit") {
		t.Errorf("unlimited: %s", got)
	}

	if why := l.exceeds(spec.Resources{MilliCPUs: 6000}); !strings.Contains(why, "6 CPUs; this agent allows at most 4") {
		t.Errorf("too many CPUs: %q", why)
	}
	if why := l.exceeds(spec.Resources{Memory: 16 << 30}); !strings.Contains(why, "16g of memory; this agent allows at most 8g") {
		t.Errorf("too much memory: %q", why)
	}
	if why := l.exceeds(spec.Resources{MilliCPUs: 4000, Memory: 8 << 30}); why != "" {
		t.Errorf("at the limits: %q", why)
	}

	for c, want := range map[*Resources]string{
		{CPUs: 8, MaxCPUs: 4}:           "resources.cpus may not be more than resources.maxCpus",
		{Memory: "9g", MaxMemory: "8g"}: "resources.memory may not be more than resources.maxMemory",
		{Memory: "lots"}:                `resources.memory: "lots" is not a size of memory`,
		{MaxCPUs: -1}:                   "resources.maxCpus:",
		{Pids: 3}:                       "resources.pids must be at least 16",
		{CPUs: 0.0001}:                  "resources.cpus:",
	} {
		if _, err := c.parse(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %q", *c, err, want)
		}
	}
	if l, err := (*Resources)(nil).parse(); err != nil || l != (limits{}) {
		t.Errorf("no resources: %+v %v", l, err)
	}
}
