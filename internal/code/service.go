// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package code implements code projects and Git repositories: storage, access over HTTPS and SSH, push policy,
// browsing, diffs and pull requests.
package code

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/files"
	"kogge.de/kogge/internal/platform/settings"
)

// Options configure the service.
type Options struct {
	DataDir   string
	PublicURL *url.URL
	SSHHost   string // host name in SSH clone URLs (default: the public host)
	SSHPort   int
	Keyring   *crypto.Keyring // signs short-lived tokens of LFS transfers
	// Gate pauses changes of the repositories on disk while a backup copies them; NewService
	// creates one when nil.
	Gate *db.Gate
	// WriteWait is how long changes made in the browser wait for a running backup (default 20 seconds).
	WriteWait time.Duration
	// AllowLoopback lets imports connect to loopback addresses (tests).
	AllowLoopback bool
}

const (
	// pushWait is how long pushes wait for a running backup; git sends keep-alives to the client meanwhile.
	pushWait = 10 * time.Minute
	// maintenanceWait is how long repository maintenance waits for a running backup.
	maintenanceWait = 30 * time.Minute
)

// Service implements the code module.
type Service struct {
	pool     *pgxpool.Pool
	authz    *authz.Service
	identity *identity.Service
	settings *settings.Service
	bus      *events.Bus
	blobs    *blob.Store
	log      *slog.Logger
	git      *git.Git
	opt      Options

	access      accessCache
	hooks       *hookServer
	locks       sync.Map // repository id → *sync.Mutex (merges and ref updates on this node)
	rebases     rebaseCache
	buildStatus BuildStatus
	jobAuth     JobAuth
	repoMoved   RepoMoved
}

// NewService creates the service; it needs the git program.
func NewService(pool *pgxpool.Pool, az *authz.Service, id *identity.Service, st *settings.Service, bus *events.Bus,
	blobs *blob.Store, opt Options, logger *slog.Logger) (*Service, error) {
	hooksDir := filepath.Join(opt.DataDir, "git-hooks")
	g, err := git.New(hooksDir)
	if err != nil {
		return nil, err
	}
	if opt.Gate == nil {
		if opt.Gate, err = db.NewGate(pool, 8); err != nil {
			return nil, err
		}
	}
	if opt.WriteWait <= 0 {
		opt.WriteWait = 20 * time.Second
	}
	s := &Service{pool: pool, authz: az, identity: id, settings: st, bus: bus, blobs: blobs, log: logger, git: g, opt: opt}
	program, err := s.writeHooks(hooksDir)
	if err != nil {
		return nil, err
	}
	s.hooks = newHookServer(s, program)
	return s, nil
}

// Close stops the server of this node's Git hooks and removes its socket (after the pushes in progress ended).
func (s *Service) Close() { s.hooks.close() }

// Git returns the git runner (for importers and tests).
func (s *Service) Git() *git.Git { return s.git }

// now is the clock (tests may replace it).
var now = time.Now

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// repoPath is where a repository lives: by id, so renames and moves only change the database.
func (s *Service) repoPath(id int64) string { return RepoDir(s.opt.DataDir, id) }

// RepoDir returns the directory of a repository in a data directory.
func RepoDir(dataDir string, id int64) string {
	return filepath.Join(dataDir, "repositories", fmt.Sprintf("%02d", id%100), itoa(id)+".git")
}

func (s *Service) open(id int64) *git.Repo { return s.git.Open(s.repoPath(id)) }

// lock serialises ref updates by Kogge itself (merges) per repository on this node; a PostgreSQL advisory lock
// covers other nodes.
func (s *Service) lock(ctx context.Context, tx pgx.Tx, repoID int64) (func(), error) {
	m, _ := s.locks.LoadOrStore(repoID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	if tx != nil {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7001, $1::int)", repoID); err != nil {
			mu.Unlock()
			return nil, err
		}
	}
	return mu.Unlock, nil
}

// writing holds the Git write lock for a change of repositories on disk that a user waits for: a backup that
// copies the repositories keeps it for a while.
func (s *Service) writing(ctx context.Context) (release func(), err error) {
	release, err = s.opt.Gate.Enter(ctx, s.opt.WriteWait)
	if errors.Is(err, db.ErrPaused) {
		return nil, apperr.New(http.StatusServiceUnavailable, "code.paused",
			"A backup is running and changes of repositories are paused; try again in a few minutes.")
	}
	return release, err
}

// RepoEvent announces changes of repositories and pull requests to live clients and other modules.
type RepoEvent struct {
	Name     string
	RepoID   int64
	PRID     int64
	ActorID  int64
	PushID   int64
	Updates  []RefUpdate
	Projects []int64
}

// EventName implements events.Event.
func (e RepoEvent) EventName() string { return e.Name }

// LiveMessage implements events.Live.
func (e RepoEvent) LiveMessage() events.LiveMessage {
	if e.PRID != 0 {
		return events.LiveMessage{Type: "pull-request", ID: itoa(e.PRID), Container: "repo:" + itoa(e.RepoID)}
	}
	return events.LiveMessage{Type: "repository", ID: itoa(e.RepoID), Container: "repo:" + itoa(e.RepoID)}
}

// publish publishes an event of the module; events that may change whether a pull request can merge look at its
// auto-merge again, if it has one.
func (s *Service) publish(ctx context.Context, tx pgx.Tx, e events.Event) error {
	evs := []events.Event{e}
	if re, ok := e.(RepoEvent); ok && re.PRID != 0 && autoMergeEvents[re.Name] {
		var on bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pull_request_auto_merges WHERE pull_request_id = $1)", re.PRID).Scan(&on); err != nil {
			return err
		}
		if on {
			evs = append(evs, autoMergeDue{prID: re.PRID})
		}
	}
	return s.bus.Publish(ctx, tx, evs...)
}

// writeHooks writes the hook scripts that call back into Kogge and returns the program they
// run. The scripts are on the data volume, which all nodes share and write at their start: they run the program
// that the node running the push names in their environment (KOGGE_HOOK_PROGRAM), so that each node's pushes run
// its own program wherever it is installed; this node's program only where a node names none (an older version).
func (s *Service) writeHooks(dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	for _, name := range []string{"pre-receive", "post-receive"} {
		script := "#!/bin/sh\n# Written by Kogge at start-up; changes are overwritten.\nprogram=${KOGGE_HOOK_PROGRAM:-}\n" +
			"[ -n \"$program\" ] || program=" + shellQuote(exe) + "\nexec \"$program\" hook " + name + "\n"
		if err := files.Replace(filepath.Join(dir, name), []byte(script), 0o750); err != nil {
			return "", err
		}
	}
	return exe, nil
}

func shellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}
