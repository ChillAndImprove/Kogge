// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"strings"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
)

// Pipelines and code: the builds module reads repositories through the code module, pushes
// and pull requests start runs, and the results of runs feed the merge checks of pull requests.

// CodeRepos implements builds.Repos with the code module.
type CodeRepos struct {
	Code *code.Service
}

func basics(b *code.RepoBasics) *builds.Repo {
	return &builds.Repo{ID: b.ID, ProjectID: b.ProjectID, ProjectKey: b.ProjectKey, Slug: b.Slug, Name: b.Name, DefaultBranch: b.DefaultBranch,
		CloneURL: b.CloneURL}
}

// Repo implements builds.Repos.
func (r CodeRepos) Repo(ctx context.Context, id int64) (*builds.Repo, error) {
	b, err := r.Code.RepoBasicsByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return basics(b), nil
}

// RepoByPath implements builds.Repos.
func (r CodeRepos) RepoByPath(ctx context.Context, key, slug string) (*builds.Repo, error) {
	b, err := r.Code.RepoBasicsByPath(ctx, key, slug)
	if err != nil {
		return nil, err
	}
	return basics(b), nil
}

// Level implements builds.Repos; the access levels of both modules are the same numbers.
func (r CodeRepos) Level(ctx context.Context, p *identity.Principal, repoID int64) (int, error) {
	return r.Code.AccessLevel(ctx, p, repoID)
}

// ProjectForVariables implements builds.Repos.
func (r CodeRepos) ProjectForVariables(ctx context.Context, p *identity.Principal, key string) (int64, error) {
	return r.Code.ProjectForVariables(ctx, p, key)
}

// ProjectID implements builds.Repos.
func (r CodeRepos) ProjectID(ctx context.Context, key string) (int64, error) {
	return r.Code.ProjectIDByKey(ctx, key)
}

// ReadFile implements builds.Repos.
func (r CodeRepos) ReadFile(ctx context.Context, repoID int64, commit, path string) ([]byte, bool, error) {
	return r.Code.ReadFileAt(ctx, repoID, commit, path)
}

// RootNames implements builds.Repos.
func (r CodeRepos) RootNames(ctx context.Context, repoID int64, commit string) ([]string, error) {
	return r.Code.RootNames(ctx, repoID, commit)
}

// Resolve implements builds.Repos.
func (r CodeRepos) Resolve(ctx context.Context, repoID int64, ref string) (string, error) {
	return r.Code.ResolveRef(ctx, repoID, ref)
}

// ChangedFiles implements builds.Repos.
func (r CodeRepos) ChangedFiles(ctx context.Context, repoID int64, base, head string) ([]string, bool, error) {
	return r.Code.ChangedFiles(ctx, repoID, base, head)
}

// CommitMessage implements builds.Repos.
func (r CodeRepos) CommitMessage(ctx context.Context, repoID int64, sha string) string {
	return r.Code.CommitMessage(ctx, repoID, sha)
}

// Pusher implements builds.Repos.
func (r CodeRepos) Pusher(ctx context.Context, repoID int64, ref, sha string) (int64, error) {
	return r.Code.Pusher(ctx, repoID, ref, sha)
}

// Readable implements builds.Repos.
func (r CodeRepos) Readable(ctx context.Context, p *identity.Principal) (bool, []int64, []int64, error) {
	return r.Code.ReadableScope(ctx, p)
}

// Protection implements builds.Repos.
func (r CodeRepos) Protection(ctx context.Context, repoID int64, ref string) builds.Protection {
	switch r.Code.RefProtection(ctx, repoID, ref) {
	case code.ProtectionProject:
		return builds.ProtectionProject
	case code.ProtectionRepository:
		return builds.ProtectionRepository
	}
	return builds.ProtectionNone
}

// BuildPushArgs starts the pipelines of a push.
type BuildPushArgs struct {
	RepoID int64 `json:"repoId"`
	PushID int64 `json:"pushId"`
}

// Kind implements river.JobArgs.
func (BuildPushArgs) Kind() string { return "links.build-push" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (BuildPushArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueBuilds} }

// BuildPullRequestArgs starts the pipelines of a pull request at its source commit.
type BuildPullRequestArgs struct {
	PRID int64 `json:"prId"`
	// ActorID is who caused the run: who opened the pull request or pushed its new source commit.
	ActorID int64 `json:"actorId,omitempty"`
}

// Kind implements river.JobArgs.
func (BuildPullRequestArgs) Kind() string { return "links.build-pull-request" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (BuildPullRequestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueBuilds}
}

type buildPushWorker struct {
	river.WorkerDefaults[BuildPushArgs]
	l *Builds
}

func (w *buildPushWorker) Work(ctx context.Context, job *river.Job[BuildPushArgs]) error {
	return w.l.push(ctx, job.Args.RepoID, job.Args.PushID)
}

type buildPRWorker struct {
	river.WorkerDefaults[BuildPullRequestArgs]
	l *Builds
}

func (w *buildPRWorker) Work(ctx context.Context, job *river.Job[BuildPullRequestArgs]) error {
	return w.l.pullRequest(ctx, job.Args.PRID, job.Args.ActorID)
}

// Builds connects the builds module with the code module.
type Builds struct {
	Code   *code.Service
	Builds *builds.Service
}

// Register wires the modules and subscribes to the events of the code module.
func (l *Builds) Register(reg *jobs.Registry, bus *events.Bus) {
	l.Builds.SetRepos(CodeRepos{Code: l.Code})
	l.Code.SetBuildStatus(func(ctx context.Context, repoID int64, sha string, external bool) (code.BuildCounts, error) {
		c, err := l.Builds.StatusCounts(ctx, repoID, sha, external)
		return code.BuildCounts{Successful: c.Successful, Skipped: c.Skipped, Failed: c.Failed, Running: c.Running}, err
	})
	l.Code.SetJobAuth(l.Builds.JobRepo)
	l.Code.SetRepoMoved(l.Builds.RepositoryMoved)
	river.AddWorker(reg.Workers, &buildPushWorker{l: l})
	river.AddWorker(reg.Workers, &buildPRWorker{l: l})
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(code.RepoEvent); ok && e.PushID != 0 {
			return BuildPushArgs{RepoID: e.RepoID, PushID: e.PushID}, nil
		}
		return nil, nil
	}, "repository.pushed")
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(code.RepoEvent); ok && e.PRID != 0 {
			return BuildPullRequestArgs{PRID: e.PRID, ActorID: e.ActorID}, nil
		}
		return nil, nil
	}, "pull-request.opened", "pull-request.updated", "pull-request.reopened")
	// Finished builds and the statuses of other build systems change merge checks: pull requests that merge when
	// ready look again.
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		switch e := ev.(type) {
		case builds.RunEvent:
			return code.AutoMergeRepoArgs{RepoID: e.RepoID}, nil
		case builds.StatusEvent:
			return code.AutoMergeRepoArgs{RepoID: e.RepoID}, nil
		}
		return nil, nil
	}, "run."+builds.RunSucceeded, "run."+builds.RunFailed, "run."+builds.RunCancelled, "status.reported")
}

// push starts the pipelines for the branches and tags a push created or moved.
func (l *Builds) push(ctx context.Context, repoID, pushID int64) error {
	push, err := l.Code.PushOf(ctx, pushID)
	if err != nil {
		if db.IsNoRows(err) {
			return nil
		}
		return err
	}
	if push.Imported() {
		return nil // an imported history starts no pipelines
	}
	for _, u := range push.Updates {
		if u.Deleted() {
			continue
		}
		ev := spec.Event{Kind: spec.EventPush}
		base := ""
		if b, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
			ev.Branch = b
			base = l.Code.ChangeBase(ctx, repoID, u) // tags release what was built: paths filters do not apply
		} else if t, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok {
			ev.Tag = t
		} else {
			continue
		}
		commit, err := l.Code.ResolveRef(ctx, repoID, u.New) // annotated tags: their commit
		if err != nil {
			continue // gone again
		}
		if _, err := l.Builds.Trigger(ctx, builds.TriggerInput{RepoID: repoID, Event: ev, Commit: commit, ActorID: push.UserID,
			ChangesBase: base}); err != nil {
			return err
		}
	}
	return nil
}

// pullRequest starts the pipelines of an open pull request at its source commit (once per commit). The run is
// started by whoever caused it — who opened the pull request or pushed its source commit — not by its author: anyone
// who can write to the source branch puts code into the run.
func (l *Builds) pullRequest(ctx context.Context, prID, actorID int64) error {
	pr, err := l.Code.PullRequestByID(ctx, prID)
	if err != nil || pr.State != "open" {
		return nil
	}
	ev := spec.Event{Kind: spec.EventPullRequest, Branch: pr.Source, TargetBranch: pr.Target}
	// The changes of a pull request are those since its source branch left the target branch.
	if _, err := l.Builds.Trigger(ctx, builds.TriggerInput{RepoID: pr.RepoID, Event: ev, Commit: pr.SourceSHA, ActorID: actorID,
		PullRequestID: pr.ID, PullRequestNo: pr.Number, ChangesBase: pr.MergeBase}); err != nil {
		return err
	}
	// Pipelines that their paths filters skip count as done at once, without a run that would announce it.
	return l.Code.RecheckAutoMerge(ctx, pr.ID)
}
