// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/netpolicy"
)

// Repositories can be imported from another Git server (GitLab, GitHub, Gitea, Bitbucket, …): a background job
// fetches its branches and tags into a temporary repository and swaps it in. Git connects only through a proxy that
// applies the outbound policy (no loopback, link-local or metadata addresses), the credentials are kept sealed
// until the import ends, and pipelines do not start for the imported history.

const importTimeout = 6 * time.Hour

func validImportURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return apperr.Invalid("repository.import-url", "Import from an http(s) address of a Git repository.")
	case u.User != nil:
		return apperr.Invalid("repository.import-url", "Give the user name and password separately, not in the address.")
	}
	return nil
}

func importAAD(repoID int64) string { return "repository-import:" + itoa(repoID) }

// importRequested starts the import job in the transaction that created or reset the import.
type importRequested struct{ repoID int64 }

func (importRequested) EventName() string { return "repository.import-requested" }

// startImport records an import and enqueues its job (in tx).
func (s *Service) startImport(ctx context.Context, tx pgx.Tx, repoID int64, rawURL, username, password string) error {
	var sealed any
	if password != "" {
		v, err := s.opt.Keyring.Seal([]byte(password), importAAD(repoID))
		if err != nil {
			return err
		}
		sealed = v
	}
	if _, err := tx.Exec(ctx, `UPDATE repositories SET import_url = $2, import_username = NULLIF($3, ''), import_secret = $4,
		import_state = 'running', import_error = '', import_finished_at = NULL WHERE id = $1`, repoID, rawURL, strings.TrimSpace(username),
		sealed); err != nil {
		return err
	}
	return s.publish(ctx, tx, importRequested{repoID: repoID})
}

// RetryImportInput restarts a failed import, optionally with other credentials.
type RetryImportInput struct {
	Username string `json:"username,omitempty" maxLength:"200"`
	Password string `json:"password,omitempty" maxLength:"2000"`
}

// RetryImport restarts the failed import of an empty repository (repository administrators).
func (s *Service) RetryImport(ctx context.Context, p *identity.Principal, key, slug string, in RetryImportInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return apperr.Forbidden("repository.admin-required", "Only administrators of the repository can import into it.")
	}
	if r.Import == nil || r.Import.State != "failed" {
		return apperr.Conflict("repository.import-state", "There is no failed import to start again.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.startImport(ctx, tx, r.ID, r.Import.URL, in.Username, in.Password); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.import", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug, Details: map[string]any{"url": r.Import.URL}})
	})
}

// ImportArgs imports a repository from another Git server.
type ImportArgs struct {
	RepoID int64 `json:"repoId"`
}

// Kind implements river.JobArgs.
func (ImportArgs) Kind() string { return "code.import" }

// InsertOpts puts imports into the import queue.
func (ImportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueImport, MaxAttempts: 1}
}

type importWorker struct {
	river.WorkerDefaults[ImportArgs]
	s *Service
}

func (w *importWorker) Timeout(*river.Job[ImportArgs]) time.Duration {
	return importTimeout + time.Minute
}

func (w *importWorker) Work(ctx context.Context, job *river.Job[ImportArgs]) error {
	return w.s.runImport(ctx, job.Args.RepoID)
}

// runImport runs an import; failures are recorded on the repository.
func (s *Service) runImport(ctx context.Context, repoID int64) error {
	var rawURL, username, sealed, state, branch string
	err := s.pool.QueryRow(ctx, `SELECT coalesce(import_url, ''), coalesce(import_username, ''), coalesce(import_secret, ''),
		coalesce(import_state, ''), default_branch FROM repositories WHERE id = $1`, repoID).Scan(&rawURL, &username, &sealed, &state, &branch)
	if db.IsNoRows(err) || (err == nil && state != "running") {
		return nil
	}
	if err != nil {
		return err
	}
	password := ""
	if sealed != "" {
		b, err := s.opt.Keyring.Open(sealed, importAAD(repoID))
		if err != nil {
			return s.importFailed(ctx, repoID, "The credentials of the import cannot be read (master key).")
		}
		password = string(b)
	}
	updates, size, err := s.fetchImport(ctx, repoID, rawURL, username, password, branch)
	if err != nil {
		return s.importFailed(ctx, repoID, err.Error())
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE repositories SET import_state = 'done', import_secret = NULL, import_error = '',
			import_finished_at = now(), default_branch = $2, size_bytes = $3, updated_at = now() WHERE id = $1`,
			repoID, updates.defaultBranch, size); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "repository.updated", RepoID: repoID})
	})
	if err != nil {
		return err
	}
	// Recorded like a push (protocol import): issue keys of the history are indexed; pipelines and commit commands
	// do not run for it.
	if len(updates.refs) > 0 {
		if _, err := s.recordPush(ctx, &pushContext{repoID: repoID, protocol: "import"}, updates.refs); err != nil {
			s.log.Error("recording an import", "repository", repoID, "error", err)
		}
	}
	return nil
}

func (s *Service) importFailed(ctx context.Context, repoID int64, message string) error {
	if len(message) > 1000 {
		message = message[:1000] + "…"
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE repositories SET import_state = 'failed', import_secret = NULL, import_error = $2,
			import_finished_at = now() WHERE id = $1`, repoID, message); err != nil {
			return err
		}
		return s.publish(ctx, tx, RepoEvent{Name: "repository.updated", RepoID: repoID})
	})
}

type importResult struct {
	refs          []RefUpdate
	defaultBranch string
}

// fetchImport fetches the branches and tags of a remote source through the outbound policy's proxy.
func (s *Service) fetchImport(ctx context.Context, repoID int64, rawURL, username, password, branch string) (*importResult, int64, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, err
	}
	// The same outbound policy as for webhooks: internal addresses only if an administrator allows them.
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, 0, err
	}
	proxyURL, stop, err := netpolicy.New(st.WebhookTargets, s.opt.AllowLoopback).Proxy()
	if err != nil {
		return nil, 0, err
	}
	defer stop()
	env := []string{"GIT_ALLOW_PROTOCOL=http:https", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.proxy", "GIT_CONFIG_VALUE_0=" + proxyURL,
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=initial"}
	if username != "" || password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		env[1] = "GIT_CONFIG_COUNT=3"
		env = append(env, "GIT_CONFIG_KEY_2=http."+u.Scheme+"://"+u.Host+"/.extraHeader", "GIT_CONFIG_VALUE_2=Authorization: Basic "+auth)
	}
	return s.fetchInto(ctx, repoID, fetchSpec{source: rawURL, display: u.Redacted(), env: env, defaultBranch: branch, askHead: true,
		refspecs: []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}})
}

// importedRefs keeps refs of migrated systems (e.g. the commits of their pull requests) alive without showing them.
const importedRefs = "refs/imported/"

// fetchSpec describes a fetch of a whole repository.
type fetchSpec struct {
	source        string // URL or local path
	display       string // the source in messages
	env           []string
	refspecs      []string
	defaultBranch string // preferred; the source's HEAD wins if askHead and it names a branch
	askHead       bool
}

// fetchInto fetches a source into a new repository and swaps it in for the repository's (empty) one.
func (s *Service) fetchInto(ctx context.Context, repoID int64, f fetchSpec) (*importResult, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, importTimeout)
	defer cancel()
	tmp := filepath.Join(s.opt.DataDir, "run", "import-"+itoa(repoID)+"-"+crypto.RandomString(8)+".git")
	defer os.RemoveAll(tmp)
	repo, err := s.git.Init(ctx, tmp, f.defaultBranch)
	if err != nil {
		return nil, 0, err
	}
	limits, err := s.importLimits(ctx, repoID, tmp)
	if err != nil {
		return nil, 0, err
	}
	var stderr bytes.Buffer
	args := append([]string{"fetch", "--prune", "--no-tags", "--quiet", "--end-of-options", f.source}, f.refspecs...)
	fetchCtx, stopFetch := context.WithCancelCause(ctx)
	defer stopFetch(nil)
	go limits.watch(fetchCtx, stopFetch)
	cmd := s.git.Command(fetchCtx, tmp, f.env, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var stopped importLimitError
		if errors.As(context.Cause(fetchCtx), &stopped) {
			return nil, 0, stopped
		}
		if ctx.Err() != nil {
			return nil, 0, errors.New("The import took longer than " + importTimeout.String() + ".")
		}
		return nil, 0, fmt.Errorf("git could not fetch from %s: %s", f.display, gitMessage(stderr.String()))
	}
	if err := limits.check(); err != nil {
		return nil, 0, err
	}
	refs, err := repo.Refs(ctx, "refs/heads/", "refs/tags/")
	if err != nil {
		return nil, 0, err
	}
	if len(refs) == 0 {
		return nil, 0, errors.New("The repository at " + f.display + " has no branches.")
	}
	res := &importResult{defaultBranch: f.defaultBranch}
	zero := strings.Repeat("0", 40)
	branches := map[string]bool{}
	for _, r := range refs {
		res.refs = append(res.refs, RefUpdate{Ref: r.Name, Old: zero, New: r.SHA})
		if b, ok := strings.CutPrefix(r.Name, "refs/heads/"); ok {
			branches[b] = true
		}
	}
	if f.askHead {
		// The source's default branch, if it says which.
		var out bytes.Buffer
		ls := s.git.Command(ctx, tmp, f.env, "ls-remote", "--symref", "--end-of-options", f.source, "HEAD")
		ls.Stdout = &out
		if ls.Run() == nil {
			for _, line := range strings.Split(out.String(), "\n") {
				if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
					if b, _, ok := strings.Cut(rest, "\t"); ok && branches[b] {
						res.defaultBranch = b
					}
				}
			}
		}
	}
	if !branches[res.defaultBranch] {
		for _, r := range refs {
			if b, ok := strings.CutPrefix(r.Name, "refs/heads/"); ok {
				res.defaultBranch = b
				break
			}
		}
	}
	if err := repo.SetHead(ctx, res.defaultBranch); err != nil {
		return nil, 0, err
	}
	// Refs kept for history are not offered to clients.
	for _, key := range []string{"uploadpack.hideRefs", "receive.hideRefs"} {
		if _, err := s.git.Run(ctx, tmp, nil, "config", "--add", key, strings.TrimSuffix(importedRefs, "/")); err != nil {
			return nil, 0, err
		}
	}
	// The swap is a change of the repositories on disk.
	release, err := s.opt.Gate.Enter(ctx, maintenanceWait)
	if err != nil {
		return nil, 0, err
	}
	defer release()
	target := s.repoPath(repoID)
	if _, err := os.Stat(target); err != nil {
		return nil, 0, errors.New("The repository was deleted during the import.")
	}
	old := target + ".replaced-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.Rename(target, old); err != nil {
		return nil, 0, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Rename(old, target)
		return nil, 0, err
	}
	_ = os.RemoveAll(old)
	return res, s.open(repoID).Size(), nil
}

// gitMessage keeps the meaningful lines of git's error output.
func gitMessage(stderr string) string {
	if strings.Contains(stderr, "could not read Username") || strings.Contains(stderr, "Authentication failed") {
		return "the source refused the user name or the password (token)."
	}
	// Whether an address does not answer, refuses or is not allowed stays undistinguished: otherwise imports would
	// map which internal hosts and ports exist.
	for _, network := range []string{"Failed to connect", "Could not resolve", "Connection refused", "Connection timed out",
		"Operation timed out", "proxy", "Proxy", "CONNECT", "unable to access", "SSL", "TLS", "certificate", "Empty reply", "Recv failure",
		"Send failure", "Couldn't connect"} {
		if strings.Contains(stderr, network) {
			return "the source could not be reached, or Kogge may not connect to it (Administration → Settings → Allowed outbound targets)."
		}
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "fatal: "))
		if l != "" && !strings.HasPrefix(l, "hint:") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "unknown error"
	}
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, " ")
}
