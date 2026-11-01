// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package git runs the git program for repository operations: with argument lists (never a shell), a
// cleaned environment, Kogge's hook directory forced, timeouts and limits on output.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// EmptyTree is the id of the empty tree (for diffs of root commits).
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// MaxOutput is the default limit of the output a command may produce.
const MaxOutput = 64 << 20

// ErrTooLarge is returned when the output of a command exceeds its limit.
var ErrTooLarge = errors.New("git: output too large")

// Git runs the git executable.
type Git struct {
	Bin      string
	HooksDir string
	Timeout  time.Duration
	Version  string

	noPCRE atomic.Bool // git was built without Perl-compatible expressions
}

// New finds git (at least 2.39) and uses hooksDir as the forced hook directory.
func New(hooksDir string) (*Git, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git is not installed: %w", err)
	}
	g := &Git{Bin: bin, HooksDir: hooksDir, Timeout: 2 * time.Minute}
	out, err := g.Run(context.Background(), "", nil, "version")
	if err != nil {
		return nil, err
	}
	g.Version = strings.TrimSpace(strings.TrimPrefix(string(out), "git version "))
	if !atLeast(g.Version, 2, 39, 1) {
		return nil, fmt.Errorf("git %s is too old; Kogge needs 2.39.1 or later", g.Version)
	}
	return g, nil
}

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// atLeast compares a version of git with major.minor.patch.
func atLeast(v string, major, minor, patch int) bool {
	m := versionPattern.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	if a != major {
		return a > major
	}
	if b != minor {
		return b > minor
	}
	return c >= patch
}

// Env returns the environment of git processes: nothing of the server's environment except PATH, no system or
// user configuration, no prompts.
func (g *Git) Env(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
		"TZ=UTC",
	}
	return append(env, extra...)
}

// legacyDefects are fsck findings of harmless defects in old histories (converted from other systems or written
// by old tools), which would otherwise refuse their push or import.
var legacyDefects = []string{"zeroPaddedFilemode", "badTimezone", "missingSpaceBeforeDate", "missingEmail", "badDate",
	"badDateOverflow"}

// Args puts Kogge's configuration in front of a git command line.
func (g *Git) Args(args ...string) []string {
	base := []string{"-c", "core.hooksPath=" + g.HooksDir, "-c", "core.quotePath=false", "-c", "safe.directory=*",
		"-c", "protocol.allow=never", "-c", "core.fsmonitor=false", "-c", "uploadpack.allowFilter=true",
		"-c", "uploadpack.allowAnySHA1InWant=false", "-c", "uploadpack.allowReachableSHA1InWant=true", // build agents fetch commits
		"-c", "receive.advertisePushOptions=false",
		// Objects that come in (pushes, imports) are checked (audit L29); defects old histories commonly have are
		// tolerated, those that attack clients (.gitmodules, .git paths) are not.
		"-c", "transfer.fsckObjects=true"}
	for _, id := range legacyDefects {
		base = append(base, "-c", "receive.fsck."+id+"=ignore", "-c", "fetch.fsck."+id+"=ignore")
	}
	return append(base, args...)
}

// Command prepares a git process in dir (a repository) for streaming; the caller sets stdin and stdout.
func (g *Git) Command(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, g.Bin, g.Args(args...)...)
	cmd.Dir = dir
	cmd.Env = g.Env(env...)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Error is a failed git command.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// ExitCode returns the exit code of a failed command, or -1.
func ExitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode
	}
	return -1
}

// Opts adjust a command.
type Opts struct {
	Stdin   io.Reader
	Env     []string
	Limit   int64 // maximum output; 0 = MaxOutput
	Timeout time.Duration
}

type limitWriter struct {
	buf   bytes.Buffer
	limit int64
	over  bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len())+int64(len(p)) > w.limit {
		w.over = true
		return 0, ErrTooLarge
	}
	return w.buf.Write(p)
}

// Run runs git in dir and returns its standard output.
func (g *Git) Run(ctx context.Context, dir string, o *Opts, args ...string) ([]byte, error) {
	if o == nil {
		o = &Opts{}
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = g.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := g.Command(ctx, dir, o.Env, args...)
	cmd.Stdin = o.Stdin
	limit := o.Limit
	if limit <= 0 {
		limit = MaxOutput
	}
	out := &limitWriter{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &limitedBuffer{b: &stderr, n: 64 << 10}
	err := cmd.Run()
	if out.over {
		return out.buf.Bytes(), ErrTooLarge
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.buf.Bytes(), &Error{Args: args, ExitCode: ee.ExitCode(), Stderr: stderr.String()}
		}
		return nil, err
	}
	return out.buf.Bytes(), nil
}

type limitedBuffer struct {
	b *bytes.Buffer
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
