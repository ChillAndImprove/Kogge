// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/builds/spec"
)

// executor runs the steps of a job.
type executor interface {
	// Prepare starts what the job needs (containers and services).
	Prepare(ctx context.Context, out io.Writer) error
	// Run runs a step and returns its exit code.
	Run(ctx context.Context, index int, step spec.Step, out io.Writer) (int, error)
	// Cleanup removes what Prepare created.
	Cleanup(out io.Writer)
	// Workspace is the path of the workspace as the steps see it.
	Workspace() string
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// validEnvName reports whether a variable can be passed to a process.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// exitCode returns the exit code of a finished process, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}

// script writes the step's script to a file in dir and returns the command line that runs it.
func script(dir string, index int, step spec.Step, shell string) (string, []string, error) {
	base := filepath.Join(dir, "step-"+strconv.Itoa(index))
	var file, body string
	var args []string
	switch shell {
	case "bash":
		file, body = base+".sh", step.Run
		args = []string{"bash", "--noprofile", "--norc", "-eo", "pipefail", file}
	case "pwsh":
		file = base + ".ps1"
		body = "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n" + step.Run +
			"\nif ((Test-Path -LiteralPath variable:\\LASTEXITCODE) -and $LASTEXITCODE) { exit $LASTEXITCODE }\n"
		args = []string{"pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file}
	case "cmd":
		file = base + ".cmd"
		body = "@echo off\r\n" + strings.ReplaceAll(step.Run, "\n", "\r\n")
		args = []string{"cmd", "/D", "/E:ON", "/V:OFF", "/S", "/C", "CALL " + file}
	default:
		file, body = base+".sh", step.Run
		args = []string{"sh", "-e", file}
	}
	// Readable for the user of a container image, too; scripts come from the pipeline file and hold no secrets.
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		return "", nil, err
	}
	return file, args, nil
}

// defaultShell is the shell of steps that name none (shell executor).
func defaultShell() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh"
		}
		return "cmd"
	}
	return "sh"
}

// workDir resolves a step's working directory below the workspace.
func workDir(ws, dir string) (string, error) {
	if dir == "" {
		return ws, nil
	}
	clean, err := cleanPattern(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(ws, filepath.FromSlash(clean)), nil
}

// shellExecutor runs steps directly on the build host, in the job's workspace.
type shellExecutor struct {
	ws      string // workspace
	scripts string // directory for step scripts, outside the workspace
	env     []string
	// trees are the steps' processes: what a step leaves running in the background (a server for the next steps)
	// ends with the job, where the platform can tell (Windows job objects).
	trees []*processTree
}

func newShellExecutor(ws, scripts string, vars map[string]string) *shellExecutor {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KOGGE_AGENT_") {
			env = append(env, kv)
		}
	}
	for _, k := range sortedKeys(vars) {
		if validEnvName(k) {
			env = append(env, k+"="+vars[k])
		}
	}
	env = append(env, "KOGGE_WORKSPACE="+ws)
	return &shellExecutor{ws: ws, scripts: scripts, env: env}
}

func (e *shellExecutor) Prepare(context.Context, io.Writer) error { return nil }

func (e *shellExecutor) Cleanup(io.Writer) {
	for _, t := range e.trees {
		t.Release()
	}
	e.trees = nil
}

func (e *shellExecutor) Workspace() string { return e.ws }

func (e *shellExecutor) Run(ctx context.Context, index int, step spec.Step, out io.Writer) (int, error) {
	shell := step.Shell
	if shell == "" {
		shell = defaultShell()
	}
	dir, err := workDir(e.ws, step.WorkingDirectory)
	if err != nil {
		return -1, err
	}
	file, args, err := script(e.scripts, index, step, shell)
	if err != nil {
		return -1, err
	}
	defer os.Remove(file)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, e.env, out, out
	tree, err := startTree(cmd)
	if err != nil {
		return -1, fmt.Errorf("cannot start %s: %w", args[0], err)
	}
	e.trees = append(e.trees, tree)
	if w := tree.Warning(); w != "" {
		fmt.Fprintln(out, protocol.MarkInfo+w)
	}
	err = tree.Wait()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if code := exitCode(err); code >= 0 {
		return code, nil
	}
	return -1, err
}

// containerExecutor runs the steps in a container of the job's image (Docker or Podman CLI); service containers
// run beside it on a network of the job and are reachable by their names.
type containerExecutor struct {
	cli         string // docker or podman
	image       string
	services    []spec.Service
	vars        map[string]string
	ws          string // workspace on the host, mounted at /workspace
	scripts     string // step scripts on the host, mounted at /kogge (read-only)
	name        string // name prefix of the job's containers and network
	mountSocket bool
	labels      []string
	resources   spec.Resources // of the job's container
	limits      limits         // the agent's defaults and process limit

	network    string
	containers []string
	created    bool // the job container exists
}

const containerWorkspace = "/workspace"

func newContainerExecutor(cli, image string, services []spec.Service, vars map[string]string, ws, scripts, name string,
	mountSocket bool, labels []string, resources spec.Resources, l limits) *containerExecutor {
	return &containerExecutor{cli: cli, image: image, services: services, vars: vars, ws: ws, scripts: scripts, name: name,
		mountSocket: mountSocket, labels: labels, resources: resources, limits: l}
}

// effective returns what a container gets: what it asks for, else the agent's defaults.
func (l limits) effective(r spec.Resources) spec.Resources {
	if r.MilliCPUs == 0 {
		r.MilliCPUs = l.defaults.MilliCPUs
	}
	if r.Memory == 0 {
		r.Memory = l.defaults.Memory
	}
	return r
}

// exceeds tells what of a container's request the agent does not allow ("" when it allows all).
func (l limits) exceeds(r spec.Resources) string {
	r = l.effective(r)
	switch {
	case l.max.MilliCPUs > 0 && r.MilliCPUs > l.max.MilliCPUs:
		return fmt.Sprintf("%s CPUs; this agent allows at most %s (resources.maxCpus of its configuration)", r.CPUs(),
			spec.FormatCPUs(l.max.MilliCPUs))
	case l.max.Memory > 0 && r.Memory > l.max.Memory:
		return fmt.Sprintf("%s of memory; this agent allows at most %s (resources.maxMemory of its configuration)",
			spec.FormatMemory(r.Memory), spec.FormatMemory(l.max.Memory))
	}
	return ""
}

// resourceArgs are the options of the container command line that limit a container: CPUs, memory without swap
// beyond it, and processes.
func (l limits) resourceArgs(r spec.Resources) []string {
	r = l.effective(r)
	var args []string
	if r.MilliCPUs > 0 {
		args = append(args, "--cpus", r.CPUs())
	}
	if r.Memory > 0 {
		m := strconv.FormatInt(r.Memory, 10)
		args = append(args, "--memory", m, "--memory-swap", m)
	}
	if l.pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(l.pids))
	}
	return args
}

func (e *containerExecutor) Workspace() string { return containerWorkspace }

// command runs the container CLI. Variable values reach it through its environment ("-e NAME" without value),
// never through the command line, where other users of the host could read them.
func (e *containerExecutor) command(ctx context.Context, env map[string]string, out io.Writer, args ...string) error {
	return e.commandTo(ctx, env, out, out, args...)
}

// commandTo is command with separate outputs (create and run print the container id on stdout).
func (e *containerExecutor) commandTo(ctx context.Context, env map[string]string, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, e.cli, args...)
	cmd.Env = os.Environ()
	for _, k := range sortedKeys(env) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return runTree(cmd)
}

func envArgs(env map[string]string) []string {
	var args []string
	for _, k := range sortedKeys(env) {
		if validEnvName(k) {
			args = append(args, "-e", k)
		}
	}
	return args
}

func (e *containerExecutor) labelArgs() []string {
	var args []string
	for _, l := range e.labels {
		args = append(args, "--label", l)
	}
	return args
}

// serviceArgs is the command line that starts a service container.
func (e *containerExecutor) serviceArgs(svc spec.Service) []string {
	args := []string{"run", "--detach", "--name", e.name + "-" + svc.Name, "--network", e.network, "--network-alias", svc.Name}
	args = append(args, e.limits.resourceArgs(svc.Resources)...)
	args = append(args, e.labelArgs()...)
	args = append(args, envArgs(svc.Variables)...)
	args = append(args, svc.Image)
	return append(args, svc.Command...)
}

// createArgs is the command line that creates the job container. Containers run without extra privileges (no
// --privileged, no added capabilities).
func (e *containerExecutor) createArgs(env map[string]string) []string {
	args := []string{"create", "--name", e.name, "--network", e.network,
		"--volume", e.ws + ":" + containerWorkspace, "--volume", e.scripts + ":/kogge:ro", "--workdir", containerWorkspace,
		"--entrypoint", "sh"}
	args = append(args, e.limits.resourceArgs(e.resources)...)
	if e.mountSocket {
		args = append(args, "--volume", "/var/run/docker.sock:/var/run/docker.sock")
	}
	args = append(args, e.labelArgs()...)
	args = append(args, envArgs(env)...)
	// The container idles; the steps run in it with exec.
	return append(args, e.image, "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done")
}

func (e *containerExecutor) Prepare(ctx context.Context, out io.Writer) error {
	e.network = e.name
	if err := e.commandTo(ctx, nil, io.Discard, out, append([]string{"network", "create"}, append(e.labelArgs(), e.network)...)...); err != nil {
		e.network = ""
		return fmt.Errorf("cannot create the network of the job: %w", err)
	}
	for _, svc := range e.services {
		name := e.name + "-" + svc.Name
		fmt.Fprintf(out, "Starting service %s (%s)\n", svc.Name, svc.Image)
		if err := e.commandTo(ctx, svc.Variables, io.Discard, out, e.serviceArgs(svc)...); err != nil {
			return fmt.Errorf("cannot start the service %s: %w", svc.Name, err)
		}
		e.containers = append(e.containers, name)
	}
	env := map[string]string{}
	for k, v := range e.vars {
		env[k] = v
	}
	env["KOGGE_WORKSPACE"] = containerWorkspace
	probe := filepath.Join(e.scripts, ".probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Starting the job container (%s)\n", e.image)
	if err := e.commandTo(ctx, env, io.Discard, out, e.createArgs(env)...); err != nil {
		return fmt.Errorf("cannot create the job container from %s: %w", e.image, err)
	}
	e.containers = append(e.containers, e.name)
	e.created = true
	if err := e.command(ctx, nil, io.Discard, "start", e.name); err != nil {
		return fmt.Errorf("cannot start the job container: %w", err)
	}
	// A container engine in its own mount namespace (such as Docker installed as a snap) mounts empty directories.
	if err := e.command(ctx, nil, io.Discard, "exec", e.name, "test", "-e", "/kogge/.probe"); err != nil {
		return fmt.Errorf("the container does not see the agent's directory %s; with Docker installed as a snap, choose a "+
			"workDir below the home directory", filepath.Dir(filepath.Dir(e.ws)))
	}
	return nil
}

func (e *containerExecutor) Run(ctx context.Context, index int, step spec.Step, out io.Writer) (int, error) {
	if step.Shell != "" && step.Shell != "sh" && step.Shell != "bash" {
		return -1, fmt.Errorf("the container executor runs sh or bash steps, not %s", step.Shell)
	}
	dir := containerWorkspace
	if step.WorkingDirectory != "" {
		clean, err := cleanPattern(step.WorkingDirectory)
		if err != nil {
			return -1, err
		}
		dir += "/" + clean
	}
	file, args, err := script(e.scripts, index, step, step.Shell)
	if err != nil {
		return -1, err
	}
	defer os.Remove(file)
	args[len(args)-1] = "/kogge/" + filepath.Base(file)
	cmdArgs := append([]string{"exec", "--workdir", dir, e.name}, args...)
	err = e.command(ctx, nil, out, cmdArgs...)
	if ctx.Err() != nil {
		// exec does not stop the step's processes in the container; stopping the container does.
		_ = e.command(context.Background(), nil, io.Discard, "kill", e.name)
		return -1, ctx.Err()
	}
	if code := exitCode(err); code >= 0 {
		return code, nil
	}
	return -1, err
}

func (e *containerExecutor) Cleanup(out io.Writer) {
	ctx := context.Background()
	// Files created in the container belong to its user; the agent must be able to read and delete them. A
	// stopped job container (timeout, cancellation) is replaced by a short-lived one.
	if owner := ownerArgs(); owner != "" && e.created {
		if err := e.command(ctx, nil, io.Discard, "exec", "--user", "0", e.name, "chown", "-R", owner, containerWorkspace); err != nil {
			_ = e.command(ctx, nil, io.Discard, "run", "--rm", "--user", "0", "--volume", e.ws+":"+containerWorkspace, "--entrypoint", "chown",
				e.image, "-R", owner, containerWorkspace)
		}
	}
	for i := len(e.containers) - 1; i >= 0; i-- {
		_ = e.command(ctx, nil, io.Discard, "rm", "--force", "--volumes", e.containers[i])
	}
	if e.network != "" {
		_ = e.command(ctx, nil, io.Discard, "network", "rm", e.network)
	}
}
