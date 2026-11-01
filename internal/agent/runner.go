// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/builds/protocol"
	"kogge.de/kogge/internal/builds/spec"
)

// Agent claims and runs jobs.
type Agent struct {
	cfg     *Config
	client  *Client
	log     *slog.Logger
	version string
	limits  limits // of containers (Config.Resources)
	// awaitingApproval is what the agent last reported about its approval.
	awaitingApproval bool
}

// New creates an agent.
func New(cfg *Config, version string, logger *slog.Logger) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Secret == "" {
		return nil, errors.New("the agent is not registered (no secret)")
	}
	tlsConfig, err := cfg.TLSConfig()
	if err != nil {
		return nil, err
	}
	if cfg.WorkDir, err = filepath.Abs(cfg.WorkDir); err != nil { // container volumes need absolute paths
		return nil, err
	}
	l, err := cfg.Resources.parse()
	if err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, client: NewClient(cfg.Server, cfg.Secret, version, tlsConfig), log: logger, version: version, limits: l}, nil
}

// Register registers a new agent with a registration token and returns its configuration with the secret.
func Register(ctx context.Context, cfg *Config, token, version string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	tlsConfig, err := cfg.TLSConfig()
	if err != nil {
		return err
	}
	c := NewClient(cfg.Server, "", version, tlsConfig)
	res, err := c.Register(ctx, protocol.RegisterRequest{Token: token, Name: cfg.Name, Labels: cfg.AllLabels(), OS: OS(), Arch: Arch(),
		Version: version})
	if err != nil {
		return err
	}
	cfg.Secret = res.Secret
	return nil
}

// Run claims and runs jobs until stop is cancelled; running jobs then finish. Cancelling kill stops them, too.
func (a *Agent) Run(stop, kill context.Context) error {
	parallel := max(a.cfg.Parallel, 1)
	slots := make(chan struct{}, parallel)
	var running sync.WaitGroup
	// The server gives the agent only jobs whose containers ask for no more than it allows.
	hello := protocol.ClaimRequest{Labels: a.cfg.AllLabels(), OS: OS(), Arch: Arch(), Version: a.version,
		Limits: &protocol.Limits{MilliCPUs: a.limits.max.MilliCPUs, Memory: a.limits.max.Memory}}
	a.log.Info("agent started", "server", a.cfg.Server, "labels", strings.Join(hello.Labels, ","), "parallel", parallel)
	backoff := time.Second
	for stop.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-stop.Done():
			continue
		}
		job, err := a.client.Claim(stop, hello)
		if err != nil {
			<-slots
			if stop.Err() != nil {
				break
			}
			if permanent(err) {
				var se *StatusError
				if errors.As(err, &se) && se.Code == 401 {
					return fmt.Errorf("the server does not accept the agent (deleted or disabled?): %w", err)
				}
			}
			a.log.Warn("cannot reach the server", "error", err, "retry", backoff)
			select {
			case <-stop.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		if waiting := a.client.AwaitingApproval(); waiting != a.awaitingApproval {
			a.awaitingApproval = waiting
			if waiting {
				a.log.Warn("waiting for an administrator to approve the agent in Kogge (Helling → Agents)")
			} else {
				a.log.Info("the agent was approved")
			}
		}
		if job == nil {
			<-slots
			if a.awaitingApproval {
				// The server answers at once until the agent is approved.
				select {
				case <-stop.Done():
				case <-time.After(protocol.PollTimeout):
				}
			}
			continue
		}
		running.Add(1)
		go func() {
			defer func() { <-slots; running.Done() }()
			a.runJob(kill, job)
		}()
	}
	a.log.Info("agent stopping; waiting for running jobs")
	running.Wait()
	return nil
}

// result is the outcome of a job.
type result struct {
	state    string
	exitCode *int
	failure  string
}

func (a *Agent) runJob(parent context.Context, p *protocol.JobPayload) {
	log := a.log.With("job", p.ID, "repository", p.Repository, "run", p.RunNumber, "name", p.Job.Name)
	log.Info("job started")
	jc := a.client.Job(p)
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	if p.Job.Timeout > 0 {
		var stopTimer context.CancelFunc
		ctx, stopTimer = context.WithTimeoutCause(ctx, p.Job.Timeout, errTimeout)
		defer stopTimer()
	}

	out := newJobLog(jc, p.LogOffset, p.Mask)
	ended := make(chan struct{})
	var endedOnServer bool
	var mu sync.Mutex
	go func() { // heartbeats; the answer says whether the job was cancelled
		t := time.NewTicker(protocol.LeaseRenewal)
		defer t.Stop()
		for {
			select {
			case <-ended:
				return
			case <-t.C:
			}
			hctx, done := context.WithTimeout(context.Background(), 20*time.Second)
			stop, err := jc.Heartbeat(hctx)
			done()
			switch {
			case err != nil && permanent(err):
				mu.Lock()
				endedOnServer = true
				mu.Unlock()
				cancel(errEnded)
				return
			case err != nil:
				log.Warn("heartbeat failed", "error", err)
			case stop:
				cancel(errCancelled)
			}
		}
	}()

	res := a.execute(ctx, p, jc, out)
	close(ended)
	switch context.Cause(ctx) {
	case errCancelled:
		res = result{state: protocol.StateCancelled, failure: "The job was cancelled."}
		out.Info("The job was cancelled.")
	case errTimeout:
		res = result{state: protocol.StateFailed, failure: "The job ran longer than its time limit of " + p.Job.Timeout.String() + "."}
		out.Error("%s", res.failure)
	default:
		if parent.Err() != nil {
			res = result{state: protocol.StateCancelled, failure: "The agent was stopped."}
			out.Error("%s", res.failure)
		}
	}
	if res.state == protocol.StateSucceeded {
		out.Info("Job succeeded.")
	} else if res.state == protocol.StateFailed && res.failure != "" && context.Cause(ctx) != errTimeout {
		out.Error("%s", res.failure)
	}
	out.Close()
	mu.Lock()
	gone := endedOnServer
	mu.Unlock()
	if gone {
		log.Warn("the job was ended on the server")
		return
	}
	cctx, done := context.WithTimeout(context.Background(), 5*time.Minute)
	defer done()
	err := retry(cctx, 10, func() error {
		return jc.Complete(cctx, protocol.CompleteRequest{State: res.state, ExitCode: res.exitCode, Failure: res.failure})
	})
	if err != nil {
		log.Error("cannot report the result", "error", err)
		return
	}
	log.Info("job finished", "state", res.state)
}

var (
	errCancelled = errors.New("cancelled")
	errTimeout   = errors.New("timeout")
	errEnded     = errors.New("ended on the server")
)

// execute prepares the workspace, runs the steps and keeps the results.
func (a *Agent) execute(ctx context.Context, p *protocol.JobPayload, jc *JobClient, out *jobLog) result {
	job := &p.Job
	id := strconv.FormatInt(p.ID, 10)
	ws := filepath.Join(a.cfg.WorkDir, "w", id)
	tmp := filepath.Join(a.cfg.WorkDir, "t", id)
	host, _ := os.Hostname()
	out.Info("Kogge agent %s %s on %s (%s/%s), attempt %d", a.cfg.Name, a.version, host, OS(), Arch(), p.Attempt)
	for _, dir := range []string{ws, tmp} {
		if err := os.RemoveAll(dir); err != nil {
			return failed("Cannot clean the workspace: %v", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return failed("Cannot create the workspace: %v", err)
		}
	}
	if !a.cfg.KeepWorkspaces {
		defer os.RemoveAll(ws)
		defer os.RemoveAll(tmp)
	}

	var ex executor
	if job.Image != "" {
		if a.cfg.Container == "" {
			return failed("The job needs the container executor, which this agent does not have.")
		}
		// The server checks images when it reads the pipeline file; the agent does not take one that its container
		// command line would read as an option either.
		for _, img := range append([]string{job.Image}, serviceImages(job.Services)...) {
			if !spec.ValidImage(img) {
				return failed("%q is not a container image reference.", img)
			}
		}
		// The server gives the agent only jobs within the limits it announces; the configuration may have changed.
		if why := a.limits.exceeds(job.Resources); why != "" {
			return failed("The job's container asks for %s.", why)
		}
		for _, svc := range job.Services {
			if why := a.limits.exceeds(svc.Resources); why != "" {
				return failed("The service %s asks for %s.", svc.Name, why)
			}
		}
		labels := []string{"kogge.job=" + id, "kogge.agent=" + a.cfg.Name}
		ex = newContainerExecutor(a.cfg.Container, job.Image, job.Services, p.Variables, ws, tmp, "kogge-job-"+id, a.cfg.MountContainerSocket, labels,
			job.Resources, a.limits)
	} else {
		if !a.cfg.Shell {
			return failed("The job needs the shell executor, which this agent does not have.")
		}
		if job.Resources != (spec.Resources{}) {
			out.Info("The job asks for resources (cpus, memory); they limit containers, and the shell executor runs the steps without limits.")
		}
		ex = newShellExecutor(ws, tmp, p.Variables)
	}

	out.Step("Checkout")
	if err := a.checkout(ctx, p, ws, out); err != nil {
		return failed("Checkout failed: %v", err)
	}
	if len(p.Artifacts) > 0 {
		out.Step("Download artifacts")
		if err := a.downloadArtifacts(ctx, p, jc, ws, tmp, out); err != nil {
			return failed("Cannot download the artifacts: %v", err)
		}
	}
	restored := map[string]bool{}
	if len(job.Cache) > 0 {
		out.Step("Restore caches")
		restored = a.restoreCaches(ctx, job.Cache, jc, ws, out)
	}

	res := result{state: protocol.StateSucceeded}
	if err := ex.Prepare(ctx, out); err != nil {
		ex.Cleanup(out)
		if ctx.Err() != nil {
			return result{state: protocol.StateFailed}
		}
		return failed("%v", err)
	}
	failedStep := false
	for i, step := range job.Steps {
		run := step.When == "always" || (step.When == "on-failure") == failedStep
		if !run || ctx.Err() != nil && step.When != "always" {
			continue
		}
		name := step.Name
		if name == "" {
			name = firstLine(step.Run)
		}
		out.Step(name)
		var sctx context.Context
		var done context.CancelFunc
		switch {
		case ctx.Err() != nil: // "always" steps after a cancellation or timeout get a moment of their own
			sctx, done = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		case step.Timeout > 0:
			sctx, done = context.WithTimeout(ctx, step.Timeout)
		default:
			sctx, done = context.WithCancel(ctx)
		}
		started := time.Now()
		code, err := ex.Run(sctx, i, step, out)
		stepTimedOut := errors.Is(sctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		done()
		elapsed := time.Since(started).Round(100 * time.Millisecond)
		switch {
		case stepTimedOut:
			out.Error("The step ran longer than its time limit of %s.", step.Timeout)
			code = -1
		case err != nil && ctx.Err() == nil:
			out.Error("%v", err)
			code = -1
		case err != nil:
			code = -1
		}
		if code == 0 {
			out.Info("Step finished in %s.", elapsed)
			continue
		}
		if code > 0 {
			out.Info("Step failed with exit code %d after %s.", code, elapsed)
		}
		if step.ContinueOnError {
			out.Info("The job continues (continue-on-error).")
			continue
		}
		if !failedStep {
			failedStep = true
			res = result{state: protocol.StateFailed, failure: "The step \"" + name + "\" failed."}
			if code > 0 {
				c := code
				res.exitCode = &c
			}
		}
	}
	ex.Cleanup(out)

	kctx := context.WithoutCancel(ctx) // results are kept even after a timeout or cancellation
	if len(job.Reports.JUnit) > 0 {
		a.uploadReports(kctx, job.Reports.JUnit, jc, ws, out)
	}
	if res.state == protocol.StateSucceeded && ctx.Err() == nil {
		if len(job.Artifacts) > 0 {
			out.Step("Upload artifacts")
			if err := a.uploadArtifacts(kctx, job.Artifacts, jc, ws, tmp, out); err != nil {
				return failed("Cannot upload the artifacts: %v", err)
			}
		}
		if len(job.Cache) > 0 {
			out.Step("Save caches")
			a.saveCaches(kctx, job.Cache, restored, jc, ws, tmp, out)
		}
	}
	return res
}

// serviceImages returns the images of a job's services.
func serviceImages(services []spec.Service) []string {
	out := make([]string, len(services))
	for i, svc := range services {
		out[i] = svc.Image
	}
	return out
}

func failed(format string, args ...any) result {
	return result{state: protocol.StateFailed, failure: fmt.Sprintf(format, args...)}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// gitEnv passes the job token to git through its environment (never the command line or .git/config), for requests
// to Kogge only: submodules and LFS servers of other hosts must not receive it.
func gitEnv(cloneURL, token string) []string {
	auth := base64.StdEncoding.EncodeToString([]byte("kogge-job:" + token))
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host == "" {
		return env
	}
	origin := u.Scheme + "://" + u.Host + "/"
	return append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+origin+".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth)
}

func (a *Agent) git(ctx context.Context, dir string, p *protocol.JobPayload, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, gitEnv(p.CloneURL, p.Token), out, out
	return runTree(cmd)
}

// checkout fetches the commit of the run into the workspace.
func (a *Agent) checkout(ctx context.Context, p *protocol.JobPayload, ws string, out *jobLog) error {
	clone := p.Job.Clone
	out.Info("Fetching %s at %s", p.Repository, short(p.Commit))
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", p.CloneURL},
	}
	fetch := []string{"-c", "protocol.version=2", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules"}
	if clone.Depth > 0 {
		fetch = append(fetch, "--depth", strconv.Itoa(clone.Depth))
	}
	fetch = append(fetch, "origin", p.Commit)
	steps = append(steps, fetch, []string{"-c", "advice.detachedHead=false", "checkout", "--quiet", "--force", p.Commit})
	if p.Branch != "" {
		steps = append(steps, []string{"checkout", "--quiet", "-B", p.Branch})
	}
	for _, args := range steps {
		if err := a.git(ctx, ws, p, out, args...); err != nil {
			return fmt.Errorf("git %s: %w", args[0], err)
		}
	}
	if clone.Submodules {
		if err := a.git(ctx, ws, p, out, "submodule", "update", "--init", "--recursive", "--quiet"); err != nil {
			return fmt.Errorf("submodules: %w", err)
		}
	}
	if clone.LFS && usesLFS(ws) {
		if _, err := exec.LookPath("git-lfs"); err != nil {
			out.Info("The repository uses Git LFS, but git-lfs is not installed on this agent; LFS files stay pointers.")
			return nil
		}
		if err := a.git(ctx, ws, p, out, "lfs", "pull"); err != nil {
			return fmt.Errorf("git lfs pull: %w", err)
		}
	}
	return nil
}

func usesLFS(ws string) bool {
	b, err := os.ReadFile(filepath.Join(ws, ".gitattributes"))
	return err == nil && strings.Contains(string(b), "filter=lfs")
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func (a *Agent) downloadArtifacts(ctx context.Context, p *protocol.JobPayload, jc *JobClient, ws, tmp string, out *jobLog) error {
	for _, art := range p.Artifacts {
		file := filepath.Join(tmp, "artifact-"+strconv.FormatInt(art.ID, 10)+".zip")
		err := retry(ctx, 4, func() error {
			f, err := os.Create(file)
			if err != nil {
				return err
			}
			defer f.Close()
			return jc.GetArtifact(ctx, art.ID, f)
		})
		if err != nil {
			return fmt.Errorf("%s of %s: %w", art.Name, art.Job, err)
		}
		problems, err := extractZip(file, ws)
		os.Remove(file)
		if err != nil {
			return fmt.Errorf("%s of %s: %w", art.Name, art.Job, err)
		}
		for _, pr := range problems {
			out.Error("%s", pr)
		}
		out.Info("Artifact %s of %s (%s)", art.Name, art.Job, size(art.Size))
	}
	return nil
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (a *Agent) cacheKeyOf(c spec.Cache, ws string) (string, error) {
	hash := ""
	if len(c.KeyFiles) > 0 {
		h, n, err := hashFiles(ws, c.KeyFiles)
		if err != nil {
			return "", err
		}
		if n > 0 {
			hash = h
		}
	}
	return cacheKey(c.Key, hash), nil
}

// restoreCaches unpacks the caches; it returns the keys that were found (they need not be saved again when their
// key depends on files).
func (a *Agent) restoreCaches(ctx context.Context, caches []spec.Cache, jc *JobClient, ws string, out *jobLog) map[string]bool {
	found := map[string]bool{}
	for _, c := range caches {
		key, err := a.cacheKeyOf(c, ws)
		if err != nil {
			out.Error("Cache %s: %v", c.Key, err)
			continue
		}
		pr, pw := io.Pipe()
		var problems []string
		var extractErr error
		done := make(chan struct{})
		go func() {
			problems, extractErr = extractTarGz(pr, ws)
			_ = pr.CloseWithError(extractErr)
			close(done)
		}()
		ok, err := jc.GetCache(ctx, key, pw)
		_ = pw.CloseWithError(err)
		<-done
		switch {
		case err != nil:
			out.Error("Cache %s: %v", key, err)
		case !ok:
			out.Info("Cache %s: not found", key)
		case extractErr != nil:
			out.Error("Cache %s: %v", key, extractErr)
		default:
			for _, p := range problems {
				out.Error("Cache %s: %s", key, p)
			}
			out.Info("Cache %s: restored", key)
			found[key] = true
		}
	}
	return found
}

func (a *Agent) saveCaches(ctx context.Context, caches []spec.Cache, restored map[string]bool, jc *JobClient, ws, tmp string, out *jobLog) {
	for i, c := range caches {
		key, err := a.cacheKeyOf(c, ws)
		if err != nil {
			out.Error("Cache %s: %v", c.Key, err)
			continue
		}
		if restored[key] && len(c.KeyFiles) > 0 {
			out.Info("Cache %s: unchanged", key)
			continue
		}
		files, err := matchFiles(ws, c.Paths)
		if err != nil {
			out.Error("Cache %s: %v", key, err)
			continue
		}
		if len(files) == 0 {
			out.Info("Cache %s: no files", key)
			continue
		}
		file := filepath.Join(tmp, "cache-"+strconv.Itoa(i)+".tar.gz")
		if err := writeArchive(file, func(w io.Writer) error { return writeTarGz(w, ws, files) }); err != nil {
			out.Error("Cache %s: %v", key, err)
			continue
		}
		err = uploadFile(ctx, file, func(r io.Reader, n int64) error { return jc.PutCache(ctx, key, r, n) })
		os.Remove(file)
		if err != nil {
			out.Error("Cache %s: %v", key, err)
			continue
		}
		out.Info("Cache %s: saved (%d files)", key, len(files))
	}
}

func (a *Agent) uploadArtifacts(ctx context.Context, arts []spec.Artifact, jc *JobClient, ws, tmp string, out *jobLog) error {
	for _, art := range arts {
		files, err := matchFiles(ws, art.Paths)
		if err != nil {
			return fmt.Errorf("%s: %w", art.Name, err)
		}
		if len(files) == 0 {
			out.Info("Artifact %s: no files match %s", art.Name, strings.Join(art.Paths, ", "))
			continue
		}
		file := filepath.Join(tmp, "artifact-"+art.Name+".zip")
		if err := writeArchive(file, func(w io.Writer) error { return writeZip(w, ws, files) }); err != nil {
			return fmt.Errorf("%s: %w", art.Name, err)
		}
		fi, _ := os.Stat(file)
		err = uploadFile(ctx, file, func(r io.Reader, n int64) error { return jc.PutArtifact(ctx, art.Name, r, n) })
		os.Remove(file)
		if err != nil {
			return fmt.Errorf("%s: %w", art.Name, err)
		}
		out.Info("Artifact %s: %d files, %s", art.Name, len(files), size(fi.Size()))
	}
	return nil
}

func (a *Agent) uploadReports(ctx context.Context, patterns []string, jc *JobClient, ws string, out *jobLog) {
	files, err := matchFiles(ws, patterns)
	if err != nil {
		out.Error("Test reports: %v", err)
		return
	}
	if len(files) == 0 {
		out.Info("Test reports: no files match %s", strings.Join(patterns, ", "))
		return
	}
	for _, rel := range files {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(p)
		if err == nil {
			err = retry(ctx, 4, func() error { return jc.PutReport(ctx, data) })
		}
		if err != nil {
			out.Error("Test report %s: %v", rel, err)
			continue
		}
		out.Info("Test report %s uploaded", rel)
	}
}

func writeArchive(file string, write func(io.Writer) error) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// uploadFile uploads a file, again from its start on transient errors.
func uploadFile(ctx context.Context, file string, put func(io.Reader, int64) error) error {
	return retry(ctx, 4, func() error {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		return put(f, fi.Size())
	})
}
