// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package builds runs pipelines defined in repositories: runs and jobs, agents that pull work,
// logs, artifacts, caches, test reports, variables and secrets, environments and deployments, commit statuses.
package builds

import (
	"context"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/settings"
)

// Access levels to repositories, as the code module defines them.
const (
	LevelNone = iota
	LevelRead
	LevelWrite
	LevelAdmin
)

// Repo describes a repository.
type Repo struct {
	ID            int64
	ProjectID     int64
	ProjectKey    string
	Slug          string
	Name          string
	DefaultBranch string
	CloneURL      string
}

// Repos is what the builds module needs from the code module; the application wires an implementation (package
// links), so the modules stay independent.
type Repos interface {
	Repo(ctx context.Context, id int64) (*Repo, error)
	RepoByPath(ctx context.Context, key, slug string) (*Repo, error)
	Level(ctx context.Context, p *identity.Principal, repoID int64) (int, error)
	// ProjectForVariables resolves a code project whose variables the principal manages.
	ProjectForVariables(ctx context.Context, p *identity.Principal, key string) (int64, error)
	ProjectID(ctx context.Context, key string) (int64, error)
	ReadFile(ctx context.Context, repoID int64, commit, path string) ([]byte, bool, error)
	// RootNames lists the names at the top of a commit's tree.
	RootNames(ctx context.Context, repoID int64, commit string) ([]string, error)
	Resolve(ctx context.Context, repoID int64, ref string) (string, error)
	// ChangedFiles lists the files that differ between two commits; known is false when they are too many.
	ChangedFiles(ctx context.Context, repoID int64, base, head string) (files []string, known bool, err error)
	CommitMessage(ctx context.Context, repoID int64, sha string) string
	// Pusher returns the user whose push last moved a branch (refs/heads/…) to a commit; 0 for none or a deploy key.
	Pusher(ctx context.Context, repoID int64, ref, sha string) (int64, error)
	Protection(ctx context.Context, repoID int64, ref string) Protection
	// Readable returns what the principal may read: everything, or the repositories of code projects and single
	// repositories.
	Readable(ctx context.Context, p *identity.Principal) (all bool, projects, repos []int64, err error)
}

// Options configure the service.
type Options struct {
	DataDir   string
	PublicURL *url.URL
	Keyring   *crypto.Keyring
}

// Service implements the builds module.
type Service struct {
	pool     *pgxpool.Pool
	authz    *authz.Service
	identity *identity.Service
	settings *settings.Service
	bus      *events.Bus
	blobs    *blob.Store
	repos    Repos
	log      *slog.Logger
	opt      Options

	wake    broadcaster // wakes the agents waiting on this node for work (Notified)
	logWake jobWaiters  // wakes the log streams of this node (Notified)
	masks   jobMasks    // the secrets of running jobs, for masking their logs

	// ClaimRecheck and LogRecheck are how often waiting agents and log streams look again without being woken: the
	// notifications of every node wake them at once (Notified), so this only matters when the connection to the
	// database that hears them was lost (tests lengthen them to show that notifications reach them).
	ClaimRecheck time.Duration
	LogRecheck   time.Duration

	stopping chan struct{} // closed by Stop: long polls and log streams end
	stopOnce sync.Once
}

// NewService creates the service.
func NewService(pool *pgxpool.Pool, az *authz.Service, id *identity.Service, st *settings.Service, bus *events.Bus, blobs *blob.Store,
	opt Options, logger *slog.Logger) *Service {
	return &Service{pool: pool, authz: az, identity: id, settings: st, bus: bus, blobs: blobs, opt: opt, log: logger,
		ClaimRecheck: 3 * time.Second, LogRecheck: time.Second, stopping: make(chan struct{})}
}

// Stop ends the waiting requests of agents and the streams of build logs when the server shuts down; agents poll
// again and browsers reconnect.
func (s *Service) Stop() { s.stopOnce.Do(func() { close(s.stopping) }) }

// SetRepos connects the code module.
func (s *Service) SetRepos(r Repos) { s.repos = r }

// now is the clock (tests may replace it).
var now = time.Now

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// pageLimit is the size of a page of a list: 50 unless asked otherwise, at most the 500 that api.Page documents.
func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return min(limit, 500)
}

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

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// logPath is where the log of a job is stored.
func (s *Service) logPath(jobID int64) string {
	return LogFile(s.opt.DataDir, jobID)
}

// LogFile returns the log file of a job in a data directory.
func LogFile(dataDir string, jobID int64) string {
	return filepath.Join(dataDir, "logs", strconv.FormatInt(jobID%100, 10), itoa(jobID)+".log")
}

// RunEvent announces changes of runs to live clients and other modules.
type RunEvent struct {
	Name   string
	RunID  int64
	RepoID int64
	State  string
}

// EventName implements events.Event.
func (e RunEvent) EventName() string { return e.Name }

// DeploymentEvent announces finished deployments (deployment.succeeded, .failed, .cancelled).
type DeploymentEvent struct {
	Name         string
	DeploymentID int64
	RunID        int64
	RepoID       int64
	Environment  string
	Commit       string
}

// EventName implements events.Event.
func (e DeploymentEvent) EventName() string { return e.Name }

// StatusEvent announces a status of a commit that another build system reported (status.reported).
type StatusEvent struct {
	RepoID int64
	Commit string
}

// EventName implements events.Event.
func (StatusEvent) EventName() string { return "status.reported" }

// LiveMessage implements events.Live.
func (e RunEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "run", ID: itoa(e.RunID), Container: "repo:" + itoa(e.RepoID)}
}

// LiveMessage implements events.Live: a deployment ends while its run may go on, and pages showing the run (or
// activity streams) refresh.
func (e DeploymentEvent) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "run", ID: itoa(e.RunID), Container: "repo:" + itoa(e.RepoID)}
}

func (s *Service) publish(ctx context.Context, tx pgx.Tx, e events.Event) error {
	return s.bus.Publish(ctx, tx, e)
}

// broadcaster wakes all goroutines waiting at the time of the notification.
type broadcaster struct {
	mu sync.Mutex
	ch chan struct{}
}

func (b *broadcaster) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

func (b *broadcaster) notify() {
	b.mu.Lock()
	if b.ch != nil {
		close(b.ch)
		b.ch = nil
	}
	b.mu.Unlock()
}

// jobWaiters wakes the goroutines waiting for news of a job (the streams of its log) at the time of the
// notification.
type jobWaiters struct {
	mu  sync.Mutex
	chs map[int64]chan struct{}
}

func (w *jobWaiters) wait(jobID int64) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.chs == nil {
		w.chs = map[int64]chan struct{}{}
	}
	ch := w.chs[jobID]
	if ch == nil {
		ch = make(chan struct{})
		w.chs[jobID] = ch
	}
	return ch
}

func (w *jobWaiters) notify(jobID int64) {
	w.mu.Lock()
	if ch := w.chs[jobID]; ch != nil {
		close(ch)
		delete(w.chs, jobID)
	}
	w.mu.Unlock()
}

func (w *jobWaiters) notifyAll() {
	w.mu.Lock()
	for _, ch := range w.chs {
		close(ch)
	}
	w.chs = nil
	w.mu.Unlock()
}

// WakeChannel is the notification channel of pipelines. Agents wait for work at any node and the logs of their
// jobs are streamed from any node, while what they wait for happens at another: a job that becomes pending on the
// node that ran a trigger, a log that grows on the node an agent sends it to. The notifications reach every node at
// the commit of what they announce:
//
//	jobs        a job became pending, or one ended (jobs it held back may start): waiting agents look again;
//	log:ID      the log or the state of a job changed: the streams of its log look again;
//	ended:ID    a job ended or starts again: also its secrets are forgotten.
const WakeChannel = "kogge_builds"

// jobsChanged tells the agents waiting at every node to look for work, at the commit of tx. PostgreSQL delivers
// the same notification once per transaction, however often a transaction sends it.
func jobsChanged(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_notify($1, 'jobs')", WakeChannel)
	return err
}

// jobChanged tells the streams of a job's log at every node to look again, at the commit of tx; ended also drops
// the job's secrets that nodes keep for masking its log.
func jobChanged(ctx context.Context, tx pgx.Tx, jobID int64, ended bool) error {
	what := "log:"
	if ended {
		what = "ended:"
	}
	_, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", WakeChannel, what+itoa(jobID))
	return err
}

// Notified receives the notifications on WakeChannel (registered with the live hub's listener).
func (s *Service) Notified(payload string) {
	if payload == "jobs" {
		s.wake.notify()
		return
	}
	what, id, _ := strings.Cut(payload, ":")
	jobID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return
	}
	switch what {
	case "ended":
		s.masks.drop(jobID)
		s.logWake.notify(jobID)
	case "log":
		s.logWake.notify(jobID)
	}
}

// Resync wakes every waiting agent and log stream of this node, when notifications may have been missed (the
// listener reconnected).
func (s *Service) Resync() {
	s.wake.notify()
	s.logWake.notifyAll()
}
