// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// Limits of pushes (Administration → Settings → Limits): the size of one push, the quota of a repository, the size
// of files pushed without Git LFS, and the free space of the data volume. git stops a push larger than the maximum
// itself; the other limits are checked in pre-receive, where the objects of the push wait in quarantine: the hook
// measures them and looks for large files, and the server decides.

// pushOverhead is what a push sends besides its pack (the ref updates); the body of a push over HTTP may exceed the
// maximum push size by it.
const pushOverhead = 16 << 20

// receivePackArgs are the arguments of git receive-pack under the limits: git stops reading a pack larger than the
// maximum push size and tells the client so ("pack exceeds maximum allowed size").
func receivePackArgs(st settings.Instance, args ...string) []string {
	return append([]string{"-c", "receive.maxInputSize=" + strconv.FormatInt(settings.MiB(st.MaxPushMB), 10), "receive-pack"}, args...)
}

// pushRefusal returns why a push to the repository cannot start (for the Git client), or "".
func (s *Service) pushRefusal(ctx context.Context, r *repoRow) (string, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return "", err
	}
	if storage.Check(st, s.repoPath(r.ID), 0) != nil {
		return lowSpaceMessage, nil
	}
	return "", nil
}

// lowSpaceMessage tells Git clients why a push is refused for want of space.
const lowSpaceMessage = "The server is running out of disk space, so it accepts no pushes for now. Please tell an administrator."

// quotaOf returns the quota of a repository in bytes (0: none): its own, or that of the settings.
func quotaOf(st settings.Instance, r *repoRow) int64 {
	if r.Quota != nil {
		return *r.Quota
	}
	return settings.MiB(st.RepositoryQuotaMB)
}

// quotaExceeded is the error of changes that would take a repository over its quota.
func quotaExceeded(used, quota int64) error {
	return apperr.New(http.StatusInsufficientStorage, "repository.quota-exceeded",
		"The change does not fit into the quota of the repository, which takes %s of %s. Delete branches and tags that are no longer needed, or ask an administrator for a larger quota.",
		storage.Format(used), storage.Format(quota))
}

// LargeFile is a file of a push that is larger than files without Git LFS may be.
type LargeFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// maxListed is how many large files the answer to a push names.
const maxListed = 10

// checkSizes applies the limits to a push whose objects the hook measured: the free space, the quota and the size of
// files without LFS. Pushes that bring no objects (deleting branches and tags, pointing them at existing commits)
// pass whatever the size, so that a repository over its quota can be cleaned up.
func (s *Service) checkSizes(ctx context.Context, pc *pushContext, req hookRequest) ([]string, error) {
	if req.Problem != "" {
		s.log.Error("the hook could not measure a push", "repository", pc.repoID, "problem", req.Problem)
		return []string{"Kogge could not check the sizes of the files in the push; try again later."}, nil
	}
	if req.Incoming <= 0 && len(req.Large) == 0 {
		return nil, nil
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	r, err := repoByID(ctx, s.pool, pc.repoID)
	if err != nil {
		return nil, err
	}
	// The objects of the push are on the disk already, in quarantine; a refused push leaves nothing behind.
	if storage.Check(st, s.repoPath(r.ID), 0) != nil {
		return []string{lowSpaceMessage}, nil
	}
	var problems []string
	if quota, used := quotaOf(st, r), r.Size+r.LFSSize; quota > 0 && req.Incoming > 0 && used+req.Incoming > quota {
		problems = append(problems,
			fmt.Sprintf("The repository would grow to %s, over its quota of %s (it has %s, the push brings %s).",
				storage.Format(used+req.Incoming), storage.Format(quota), storage.Format(used), storage.Format(req.Incoming)),
			"Make room by deleting branches and tags that are no longer needed (that works over the quota, and Kogge "+
				"frees their space soon after), or ask an administrator for a larger quota.")
	}
	if limit := settings.MiB(st.MaxGitFileMB); limit > 0 {
		var large []LargeFile
		for _, f := range req.Large {
			if f.Size > limit {
				large = append(large, f)
			}
		}
		for i, f := range large {
			if i == maxListed {
				problems = append(problems, fmt.Sprintf("… and %d more.", len(large)-maxListed))
				break
			}
			problems = append(problems, fmt.Sprintf("%s is %s: files larger than %s must be stored with Git LFS.", f.Path,
				storage.Format(f.Size), storage.Format(limit)))
		}
		if len(large) > 0 {
			problems = append(problems, `Track such files with "git lfs track", move them out of the commits you push with `+
				`"git lfs migrate import --include=<path>", and push again.`)
		}
	}
	if len(problems) == 0 {
		pc.incoming = req.Incoming
	}
	return problems, nil
}

// dirSize returns the size of the files in a directory: of the objects a push brings, which git keeps in a directory
// of their own until pre-receive allows the push (GIT_QUARANTINE_PATH in the hook's environment), or of a repository
// being imported.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// largeFiles returns the files larger than limit that a push brings (run by the hook, which sees the objects in
// quarantine). Listing the objects of the quarantine is quick even for large pushes; only if a blob there is too
// large does it look for the paths, among the objects that the pushed refs reach and the repository's refs do not.
// Blobs that none of the pushed commits contain are no files of the push.
func largeFiles(ctx context.Context, updates []RefUpdate, limit int64) ([]LargeFile, error) {
	// Without the repository's objects as alternates, git lists the objects of the quarantine alone.
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES=") })
	large := map[string]int64{}
	err := scanGit(ctx, env, nil, func(line string) bool {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "blob" {
			if size, err := strconv.ParseInt(f[1], 10, 64); err == nil && size > limit {
				large[f[2]] = size
			}
		}
		return true
	}, "cat-file", "--batch-all-objects", "--unordered", "--batch-check=%(objecttype) %(objectsize) %(objectname)")
	if err != nil || len(large) == 0 {
		return nil, err
	}
	var tips strings.Builder
	for _, u := range updates {
		if !u.Deleted() {
			tips.WriteString(u.New + "\n")
		}
	}
	var out []LargeFile
	err = scanGit(ctx, os.Environ(), strings.NewReader(tips.String()), func(line string) bool {
		sha, path, ok := strings.Cut(line, " ")
		if size, found := large[sha]; ok && found {
			out = append(out, LargeFile{Path: path, Size: size})
			delete(large, sha)
		}
		return len(large) > 0
	}, "rev-list", "--objects", "--stdin", "--not", "--all")
	slices.SortFunc(out, func(a, b LargeFile) int { return strings.Compare(a.Path, b.Path) })
	return out, err
}

// scanGit runs git with its output read line by line until line returns false.
func scanGit(ctx context.Context, env []string, stdin io.Reader, line func(string) bool, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	cmd.Stdin = stdin
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	stopped := false
	for sc.Scan() {
		if !line(sc.Text()) {
			stopped = true
			cancel() // the rest is not needed
			break
		}
	}
	scanErr := sc.Err()
	waitErr := cmd.Wait()
	switch {
	case stopped:
		return nil
	case scanErr != nil:
		return scanErr
	case waitErr != nil:
		return fmt.Errorf("git %s: %w: %s", args[0], waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// cleanupEvery is how often the cleanup of one repository runs at most.
const cleanupEvery = 10 * time.Minute

// CleanupArgs frees the space of a repository over its quota (cleanUp). Pushes over the quota ask for it, at
// whichever node their processing runs; River runs at most one per repository and period of cleanupEvery for all
// nodes together (a unique job), so that nodes neither clean up a repository at the same time nor each on its own.
type CleanupArgs struct {
	RepoID int64 `json:"repoId"`
}

// Kind implements river.JobArgs.
func (CleanupArgs) Kind() string { return "code.cleanup" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (CleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueGit, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: cleanupEvery}}
}

// cleanupDue asks for the cleanup of a repository, in the transaction that publishes it.
type cleanupDue struct{ repoID int64 }

func (cleanupDue) EventName() string { return "repository.cleanup-due" }

type cleanupWorker struct {
	river.WorkerDefaults[CleanupArgs]
	s *Service
}

// Timeout gives the cleanup the time of a repository's maintenance and of waiting for a backup.
func (w *cleanupWorker) Timeout(*river.Job[CleanupArgs]) time.Duration {
	return maintenanceWait + maintenanceTimeout
}

func (w *cleanupWorker) Work(ctx context.Context, job *river.Job[CleanupArgs]) error {
	if _, err := repoByID(ctx, w.s.pool, job.Args.RepoID); err != nil {
		return nil // deleted in the meantime
	}
	size, err := w.s.cleanUp(ctx, job.Args.RepoID)
	if err != nil {
		// The next push over the quota asks again.
		w.s.log.Warn("cleaning up a repository over its quota failed", "repository", job.Args.RepoID, "error", err)
		return nil
	}
	_, err = w.s.pool.Exec(ctx, "UPDATE repositories SET size_bytes = $2 WHERE id = $1", job.Args.RepoID, size)
	return err
}

// cleanUp frees the space of what nothing refers to any more in a repository over its quota: pushes that delete
// branches or rewrite them may pass over the quota so that people can make room, and the daily maintenance would
// keep what they removed for two weeks. Objects younger than an hour stay, for writers that have not referred to
// them yet. It returns the size after. Git keeps two cleanups (or a cleanup and the maintenance) of one repository
// from running at once, also on different hosts (gc.pid names the host).
func (s *Service) cleanUp(ctx context.Context, repoID int64) (int64, error) {
	release, err := s.opt.Gate.Enter(ctx, maintenanceWait)
	if err != nil {
		return -1, err
	}
	defer release()
	if _, err := s.git.Run(ctx, s.repoPath(repoID), &git.Opts{Timeout: maintenanceTimeout}, "gc", "--quiet", "--prune=1.hour.ago"); err != nil {
		return -1, err
	}
	return s.open(repoID).Size(), nil
}

// importLimitError is the end of an import that a limit stopped.
type importLimitError struct{ message string }

func (e importLimitError) Error() string { return e.message }

// importWatch stops an import that would take the repository over its quota or the disk below the free space the
// settings keep: imports fetch whole histories, which no limit of pushes bounds.
type importWatch struct {
	st    settings.Instance
	dir   string // the repository fetched into
	quota int64
	lfs   int64 // the repository's LFS objects, which count towards the quota, too
}

func (s *Service) importLimits(ctx context.Context, repoID int64, dir string) (*importWatch, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return nil, err
	}
	w := &importWatch{st: st, dir: dir, quota: quotaOf(st, r), lfs: r.LFSSize}
	return w, w.check()
}

// check returns why the import must stop, or nil.
func (w *importWatch) check() error {
	if storage.Check(w.st, w.dir, 0) != nil {
		return importLimitError{"The server is running out of disk space, so the import stopped. Please tell an administrator."}
	}
	if w.quota > 0 && dirSize(w.dir)+w.lfs > w.quota {
		return importLimitError{fmt.Sprintf("The repository would be larger than its quota of %s, so the import stopped. "+
			"Ask an administrator for a larger quota.", storage.Format(w.quota))}
	}
	return nil
}

// watch checks the limits while git fetches, and stops it when one is reached.
func (w *importWatch) watch(ctx context.Context, stop context.CancelCauseFunc) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.check(); err != nil {
				stop(err)
				return
			}
		}
	}
}
