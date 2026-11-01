// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
)

// The Bitbucket Data Center migration reads projects, repositories, permissions, branch
// permissions, merge checks, default reviewers and pull requests with their reviews, comments and events through
// the REST API, and the repositories themselves as mirrors (all refs, including the commits of pull requests) with
// their Git LFS objects into the work directory. The load creates code projects and repositories with the same keys
// and slugs, so clone addresses (/scm/KEY/slug.git) and pull request numbers stay the same.

type bbUser struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

func (u *bbUser) jira() *jUser {
	if u == nil || (u.Name == "" && u.Slug == "") {
		return nil
	}
	return &jUser{Name: cmp.Or(u.Name, u.Slug), Key: u.Slug, DisplayName: u.DisplayName, EmailAddress: u.EmailAddress}
}

type bbProject struct {
	ID          int64  `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Public      bool   `json:"public"`
	Type        string `json:"type"` // NORMAL or PERSONAL
}

type bbRepo struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ScmID       string `json:"scmId"`
	Public      bool   `json:"public"`
	Archived    bool   `json:"archived"`
}

type bbUserPermission struct {
	User       bbUser `json:"user"`
	Permission string `json:"permission"`
}

type bbGroupPermission struct {
	Group struct {
		Name string `json:"name"`
	} `json:"group"`
	Permission string `json:"permission"`
}

type bbMatcher struct {
	ID        string `json:"id"`
	DisplayID string `json:"displayId"`
	Type      struct {
		ID   string `json:"id"` // BRANCH, PATTERN, ANY_REF, MODEL_BRANCH, MODEL_CATEGORY
		Name string `json:"name"`
	} `json:"type"`
}

type bbRestriction struct {
	ID         int64             `json:"id"`
	Type       string            `json:"type"`
	Matcher    bbMatcher         `json:"matcher"`
	Users      []bbUser          `json:"users"`
	Groups     []string          `json:"groups"`
	AccessKeys []json.RawMessage `json:"accessKeys"`
}

type bbRef struct {
	ID           string `json:"id"`
	DisplayID    string `json:"displayId"`
	LatestCommit string `json:"latestCommit"`
	Repository   struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
	} `json:"repository"`
}

type bbParticipant struct {
	User     bbUser `json:"user"`
	Approved bool   `json:"approved"`
	Status   string `json:"status"` // APPROVED, NEEDS_WORK, UNAPPROVED
}

type bbPullRequest struct {
	ID          int             `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	State       string          `json:"state"` // OPEN, MERGED, DECLINED
	Draft       bool            `json:"draft"`
	CreatedDate int64           `json:"createdDate"`
	UpdatedDate int64           `json:"updatedDate"`
	ClosedDate  int64           `json:"closedDate"`
	FromRef     bbRef           `json:"fromRef"`
	ToRef       bbRef           `json:"toRef"`
	Author      bbParticipant   `json:"author"`
	Reviewers   []bbParticipant `json:"reviewers"`
}

type bbComment struct {
	ID                 int64       `json:"id"`
	Text               string      `json:"text"`
	Author             bbUser      `json:"author"`
	CreatedDate        int64       `json:"createdDate"`
	UpdatedDate        int64       `json:"updatedDate"`
	Comments           []bbComment `json:"comments"`
	Severity           string      `json:"severity"` // BLOCKER: a task
	State              string      `json:"state"`    // OPEN, RESOLVED
	ResolvedDate       int64       `json:"resolvedDate"`
	ThreadResolved     bool        `json:"threadResolved"`
	ThreadResolvedDate int64       `json:"threadResolvedDate"`
}

type bbCommits struct {
	Commits []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"commits"`
}

type bbActivity struct {
	ID            int64      `json:"id"`
	CreatedDate   int64      `json:"createdDate"`
	User          bbUser     `json:"user"`
	Action        string     `json:"action"`
	CommentAction string     `json:"commentAction"`
	Comment       *bbComment `json:"comment"`
	CommentAnchor *struct {
		Path     string `json:"path"`
		Line     int    `json:"line"`
		FileType string `json:"fileType"` // FROM (old side) or TO (new side)
		FromHash string `json:"fromHash"`
		ToHash   string `json:"toHash"`
		Orphaned bool   `json:"orphaned"`
	} `json:"commentAnchor"`
	Commit *struct {
		ID string `json:"id"`
	} `json:"commit"`
	FromHash         string     `json:"fromHash"`
	PreviousFromHash string     `json:"previousFromHash"`
	Added            *bbCommits `json:"added"`
	Removed          *bbCommits `json:"removed"`
}

// bbPullRequestSettings are the merge checks and strategies of a repository; Bitbucket 8 reports the checks also as
// settings of their hooks.
type bbPullRequestSettings struct {
	MergeConfig struct {
		DefaultStrategy *bbStrategy  `json:"defaultStrategy"`
		Strategies      []bbStrategy `json:"strategies"`
	} `json:"mergeConfig"`
	RequiredAllApprovers     bool     `json:"requiredAllApprovers"`
	RequiredAllTasksComplete bool     `json:"requiredAllTasksComplete"`
	RequiredApprovers        int      `json:"requiredApprovers"`
	RequiredSuccessfulBuilds int      `json:"requiredSuccessfulBuilds"`
	ApproversCheck           *bbCheck `json:"com.atlassian.bitbucket.server.bundled-hooks.requiredApproversMergeHook"`
	BuildsCheck              *bbCheck `json:"com.atlassian.bitbucket.server.bitbucket-build.requiredBuildsMergeCheck"`
}

type bbStrategy struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type bbCheck struct {
	Enabled bool `json:"enabled"`
	Count   int  `json:"count"`
}

type bbHook struct {
	Details struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"details"`
	Enabled bool `json:"enabled"`
}

type bbCondition struct {
	ID                int64      `json:"id"`
	SourceMatcher     *bbMatcher `json:"sourceMatcher"`
	TargetMatcher     *bbMatcher `json:"targetMatcher"`
	SourceRefMatcher  *bbMatcher `json:"sourceRefMatcher"`
	TargetRefMatcher  *bbMatcher `json:"targetRefMatcher"`
	Reviewers         []bbUser   `json:"reviewers"`
	RequiredApprovals int        `json:"requiredApprovals"`
}

type bbWebhook struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

type bbPage struct {
	Values        []json.RawMessage `json:"values"`
	IsLastPage    bool              `json:"isLastPage"`
	NextPageStart int               `json:"nextPageStart"`
}

// BitbucketOptions configure a Bitbucket migration.
type BitbucketOptions struct {
	URL      string
	Token    string
	Projects []string // keys; all when empty
	Workdir  string
	// GitUser authenticates git with the token as password; without it, git sends the token as a bearer token.
	GitUser string
	// SkipGit leaves the repositories' history out of the extraction, SkipLFS their LFS objects (trial runs).
	SkipGit bool
	SkipLFS bool
	// Concurrency is how many requests for pull request events and LFS objects run at a time (default 4).
	Concurrency int
	// Restart begins the extraction afresh instead of resuming an interrupted one.
	Restart bool
}

// Bitbucket migrates from Bitbucket Data Center.
type Bitbucket struct {
	opt  BitbucketOptions
	c    *Client
	work Workdir
	log  *slog.Logger
	cp   *checkpoint // of the running extraction
}

// NewBitbucket creates a migration.
func NewBitbucket(opt BitbucketOptions, log *slog.Logger) *Bitbucket {
	return &Bitbucket{opt: opt, c: NewClient(opt.URL, opt.Token, log), work: Workdir{Root: opt.Workdir}, log: log}
}

// bbName are keys and slugs that are safe as names in the work directory.
var bbName = regexp.MustCompile(`^~?[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// all reads every page of a paged resource.
func (b *Bitbucket) all(ctx context.Context, path string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for start := 0; ; {
		raw, err := b.c.GetJSON(ctx, path+sep+"limit=500&start="+strconv.Itoa(start))
		if err != nil {
			return nil, err
		}
		var page bbPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 || page.NextPageStart <= start {
			return out, nil
		}
		start = page.NextPageStart
	}
}

// saveAll stores every page of a resource as one JSON array. The pages are written as they arrive, so that a
// listing of tens of thousands of pull requests is not held in memory.
func (b *Bitbucket) saveAll(ctx context.Context, path string, parts ...string) error {
	file := b.work.Path(parts...)
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	err = func() error {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		_ = w.WriteByte('[')
		n := 0
		for start := 0; ; {
			raw, err := b.c.GetJSON(ctx, path+sep+"limit=500&start="+strconv.Itoa(start))
			if err != nil {
				return err
			}
			var page bbPage
			if err := json.Unmarshal(raw, &page); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			for _, v := range page.Values {
				if n > 0 {
					_ = w.WriteByte(',')
				}
				if _, err := w.Write(v); err != nil {
					return err
				}
				n++
			}
			if page.IsLastPage || len(page.Values) == 0 || page.NextPageStart <= start {
				break
			}
			start = page.NextPageStart
		}
		_ = w.WriteByte(']')
		return w.Flush()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

// notFound reports a resource the source does not have (an older version, a disabled app).
func notFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// saveOptional stores a resource that older versions of the source do not have; paged ones as one JSON array.
func (b *Bitbucket) saveOptional(ctx context.Context, path string, paged bool, parts ...string) error {
	var err error
	if paged {
		err = b.saveAll(ctx, path, parts...)
	} else {
		var raw json.RawMessage
		if raw, err = b.c.GetJSON(ctx, path); err == nil {
			err = b.work.Write(raw, parts...)
		}
	}
	if notFound(err) {
		b.log.Debug("not available in the source", "resource", path)
		if err := os.Remove(b.work.Path(parts...)); err != nil && !os.IsNotExist(err) {
			return err // of an earlier extraction
		}
		return nil
	}
	return err
}

func (b *Bitbucket) concurrency() int { return max(b.opt.Concurrency, 1) }

// Extract reads the source into the work directory (repeatable: mirrors and LFS objects are updated). An
// interrupted extraction resumes where it stopped (unless Restart is set).
func (b *Bitbucket) Extract(ctx context.Context) error {
	if b.opt.Concurrency <= 0 {
		b.opt.Concurrency = 4
	}
	cp, err := openCheckpoint(b.work, "bitbucket", b.opt.Restart, map[string]any{"git": !b.opt.SkipGit, "lfs": !b.opt.SkipLFS}, b.log)
	if err != nil {
		return err
	}
	defer cp.close()
	b.cp = cp
	err = cp.part("meta", func() error {
		props, err := b.c.GetJSON(ctx, "/rest/api/1.0/application-properties")
		if err != nil {
			return fmt.Errorf("the source does not answer like Bitbucket Data Center: %w", err)
		}
		if err := b.work.Write(props, "bitbucket", "application-properties.json"); err != nil {
			return err
		}
		server, _ := json.Marshal(map[string]string{"baseUrl": b.c.BaseURL})
		if err := b.work.Write(server, "bitbucket", "server.json"); err != nil {
			return err
		}
		if err := b.saveAll(ctx, "/rest/api/1.0/users", "bitbucket", "users.json"); err != nil {
			return fmt.Errorf("users: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	projects, err := b.all(ctx, "/rest/api/1.0/projects")
	if err != nil {
		return fmt.Errorf("projects: %w", err)
	}
	found := map[string]bool{}
	for _, raw := range projects {
		var p bbProject
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if len(b.opt.Projects) > 0 && !slices.ContainsFunc(b.opt.Projects, func(k string) bool { return strings.EqualFold(k, p.Key) }) {
			continue
		}
		if !bbName.MatchString(p.Key) {
			return fmt.Errorf("unexpected project key %q", p.Key)
		}
		found[strings.ToUpper(p.Key)] = true
		if err := cp.part("project/"+p.Key, func() error { return b.extractProject(ctx, p, raw) }); err != nil {
			return fmt.Errorf("project %s: %w", p.Key, err)
		}
	}
	for _, k := range b.opt.Projects {
		if !found[strings.ToUpper(k)] {
			return fmt.Errorf("the project %s does not exist or the token cannot read it", k)
		}
	}
	return cp.finish()
}

func (b *Bitbucket) extractProject(ctx context.Context, p bbProject, raw json.RawMessage) error {
	key := p.Key
	b.log.Info("extracting project", "project", key)
	base := "/rest/api/1.0/projects/" + url.PathEscape(key)
	dir := []string{"bitbucket", "projects", key}
	if err := b.work.Write(raw, append(dir, "project.json")...); err != nil {
		return err
	}
	if p.Type == "PERSONAL" {
		return nil // reported by the load
	}
	err := b.cp.part("project/"+key+"/meta", func() error {
		for name, path := range map[string]string{"permissions-users.json": base + "/permissions/users",
			"permissions-groups.json": base + "/permissions/groups",
			"restrictions.json":       "/rest/branch-permissions/2.0/projects/" + url.PathEscape(key) + "/restrictions"} {
			if err := b.saveAll(ctx, path, append(dir, name)...); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if err := b.saveOptional(ctx, "/rest/default-reviewers/1.0/projects/"+url.PathEscape(key)+"/conditions", false,
			append(dir, "default-reviewers.json")...); err != nil {
			return fmt.Errorf("default reviewers: %w", err)
		}
		if err := b.saveOptional(ctx, base+"/webhooks", true, append(dir, "webhooks.json")...); err != nil {
			return fmt.Errorf("webhooks: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	repos, err := b.all(ctx, base+"/repos")
	if err != nil {
		return err
	}
	for _, rraw := range repos {
		var r bbRepo
		if err := json.Unmarshal(rraw, &r); err != nil {
			return err
		}
		if r.ScmID != "" && r.ScmID != "git" {
			continue
		}
		if !bbName.MatchString(r.Slug) || strings.HasPrefix(r.Slug, "~") {
			return fmt.Errorf("unexpected repository slug %q", r.Slug)
		}
		if err := b.cp.part("repo/"+key+"/"+r.Slug, func() error { return b.extractRepo(ctx, key, r, rraw) }); err != nil {
			return fmt.Errorf("repository %s: %w", r.Slug, err)
		}
	}
	return nil
}

func (b *Bitbucket) extractRepo(ctx context.Context, key string, r bbRepo, raw json.RawMessage) error {
	b.log.Info("extracting repository", "project", key, "repository", r.Slug)
	project := url.PathEscape(key)
	base := "/rest/api/1.0/projects/" + project + "/repos/" + url.PathEscape(r.Slug)
	dir := []string{"bitbucket", "projects", key, "repos", r.Slug}
	unit := "repo/" + key + "/" + r.Slug
	err := b.cp.part(unit+"/meta", func() error {
		if err := b.work.Write(raw, append(dir, "repo.json")...); err != nil {
			return err
		}
		// The default branch: Bitbucket 8 has its own resource, older versions one below the branches.
		for _, path := range []string{base + "/default-branch", base + "/branches/default"} {
			if def, err := b.c.GetJSON(ctx, path); err == nil {
				if err := b.work.Write(def, append(dir, "default-branch.json")...); err != nil {
					return err
				}
				break
			} else if !notFound(err) {
				return fmt.Errorf("default branch: %w", err)
			}
		}
		for name, path := range map[string]string{"permissions-users.json": base + "/permissions/users",
			"permissions-groups.json": base + "/permissions/groups",
			"restrictions.json":       "/rest/branch-permissions/2.0/projects/" + project + "/repos/" + url.PathEscape(r.Slug) + "/restrictions"} {
			if err := b.saveAll(ctx, path, append(dir, name)...); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		for _, o := range []struct {
			name, path string
			paged      bool
		}{
			{"pull-request-settings.json", base + "/settings/pull-requests", false},
			{"hooks.json", base + "/settings/hooks", true},
			{"default-reviewers.json", "/rest/default-reviewers/1.0/projects/" + project + "/repos/" + url.PathEscape(r.Slug) + "/conditions", false},
			{"webhooks.json", base + "/webhooks", true},
		} {
			if err := b.saveOptional(ctx, o.path, o.paged, append(dir, o.name)...); err != nil {
				return fmt.Errorf("%s: %w", o.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := b.cp.part(unit+"/pull-requests", func() error {
		return b.saveAll(ctx, base+"/pull-requests?state=ALL&order=OLDEST", append(dir, "pull-requests.json")...)
	}); err != nil {
		return fmt.Errorf("pull requests: %w", err)
	}
	// The events of the pull requests, several at a time.
	var ids []int
	err = eachItem([]string{b.work.Path(append(dir, "pull-requests.json")...)}, func(raw json.RawMessage, _ itemAt) error {
		var pr struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(raw, &pr); err != nil {
			return err
		}
		ids = append(ids, pr.ID)
		return nil
	})
	if err != nil {
		return err
	}
	p := newProgress(b.log, "pull requests of "+key+"/"+r.Slug, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(b.concurrency())
	for _, id := range ids {
		g.Go(func() error {
			n := strconv.Itoa(id)
			err := b.cp.part(unit+"/activities/"+n, func() error {
				return b.saveAll(gctx, base+"/pull-requests/"+n+"/activities", append(dir, "activities", n+".json")...)
			})
			if err != nil {
				return fmt.Errorf("pull request %d: %w", id, err)
			}
			p.add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	p.finish()
	// The history last: it then holds every commit the pull requests name.
	if b.opt.SkipGit {
		return nil
	}
	return b.mirror(ctx, key, r, dir)
}

// gitAuth is the Authorization header for git and LFS requests to the source.
func (b *Bitbucket) gitAuth() string {
	if b.opt.GitUser != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(b.opt.GitUser+":"+b.opt.Token))
	}
	return "Bearer " + b.opt.Token
}

// redirect matches git's report of a redirect that it did not follow.
var redirect = regexp.MustCompile(`returned error: 30[1278]`)

// mirror clones (or updates) a bare mirror of the repository with all refs, and fetches its LFS objects. Git
// itself streams the history to disk; an update fetches only what is new.
func (b *Bitbucket) mirror(ctx context.Context, key string, r bbRepo, dir []string) error {
	// The clone address of the base URL in use (the source's own links may name another host name).
	clone := b.c.BaseURL + "/scm/" + url.PathEscape(strings.ToLower(key)) + "/" + url.PathEscape(r.Slug) + ".git"
	u, err := url.Parse(clone)
	if err != nil {
		return err
	}
	path := b.work.Path("bitbucket", "git", key, r.Slug+".git")
	// Git reads only the source: a redirect to another system (which would bring its repository into the import)
	// is refused, as the importer's other requests refuse them.
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http."+u.Scheme+"://"+u.Host+"/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: "+b.gitAuth(),
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false")
	var args []string
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		args = []string{"--git-dir", path, "fetch", "--prune", "--quiet", "--end-of-options", clone, "+refs/*:refs/*"}
	} else {
		// A clone that broke off is started again.
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		args = []string{"clone", "--mirror", "--quiet", "--", clone, path}
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env, cmd.Stderr = env, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if redirect.MatchString(msg) {
			msg += " (the source redirected the clone; the importer reads repositories only from " + b.c.BaseURL +
				": use the address it redirects to as the source's URL if that is the same system)"
		}
		return fmt.Errorf("git: %v: %s", err, msg)
	}
	pointers, err := lfsPointers(ctx, path)
	if err != nil {
		return err
	}
	if pointers == nil {
		pointers = []lfsPointer{}
	}
	list, _ := json.Marshal(pointers)
	if err := b.work.Write(list, append(dir, "lfs.json")...); err != nil {
		return err
	}
	if len(pointers) == 0 || b.opt.SkipLFS {
		return nil
	}
	missing, err := fetchLFS(ctx, b.work, "bitbucket", clone, b.gitAuth(), pointers, b.concurrency(), b.log)
	if err != nil {
		return fmt.Errorf("LFS: %w", err)
	}
	if len(missing) > 0 {
		b.log.Warn("the source lacks LFS objects", "project", key, "repository", r.Slug, "count", len(missing))
	}
	return nil
}

// --- loading ---

// Load writes the work directory into Kogge. Loads can be repeated: entities loaded before are updated, and the
// repositories' branches and tags become those of the latest extraction.
func (b *Bitbucket) Load(ctx context.Context, l Loader) (*Report, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bitbucket", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bitbucket extraction", b.work.Root)
	}
	if l.Code == nil {
		return nil, errors.New("the load needs the code module")
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/")
	ld := &bbLoad{Bitbucket: b, l: l, source: "bitbucket:" + base, groups: map[string]int64{}, people: map[string]bbUser{}, jr: &journal{}}
	ld.rep = newReport(ld.source)
	ld.us = newUsers(ld.rep, ld.jr)
	ld.f = l.failures(ld.rep, ld.jr)
	var people []bbUser
	if _, err := b.work.Read(&people, "bitbucket", "users.json"); err != nil {
		return nil, err
	}
	for _, u := range people {
		for _, ref := range []string{u.Name, u.Slug} {
			if ref != "" {
				ld.people[strings.ToLower(ref)] = u
			}
		}
	}
	var keys []string
	for _, k := range b.work.List("bitbucket", "projects") {
		if len(b.opt.Projects) == 0 || slices.ContainsFunc(b.opt.Projects, func(s string) bool { return strings.EqualFold(s, k) }) {
			keys = append(keys, k)
		}
	}
	var err error
	for _, key := range keys {
		b.log.Info("loading project", "project", key)
		if perr := ld.project(ctx, key); perr != nil {
			if err = ld.f.record("code project", key, key, perr); err != nil {
				break
			}
		}
	}
	ld.rep.Counts["users"] = len(ld.rep.UsersMatched) + len(ld.rep.Placeholders)
	if werr := ld.rep.Write(b.work, "bitbucket-report.json"); err == nil {
		err = werr
	}
	return ld.rep, err
}

type bbLoad struct {
	*Bitbucket
	l      Loader
	source string
	rep    *Report
	us     *users
	jr     *journal
	f      *failures
	groups map[string]int64
	people map[string]bbUser // the source's users by name and slug, for details missing elsewhere
}

// count adds one to a count of the report, undone with the transaction it belongs to.
func (ld *bbLoad) count(name string) {
	ld.rep.Counts[name]++
	ld.jr.add(func() { ld.rep.Counts[name]-- })
}

// inTx runs fn in a transaction; in-memory state follows its outcome.
func (ld *bbLoad) inTx(ctx context.Context, fn func(tx pgx.Tx, im *code.Importer) error) error {
	return ld.f.inTx(ctx, ld.l.Pool, func(tx pgx.Tx) error { return fn(tx, ld.l.Code.NewImporter(tx, ld.source)) })
}

func (ld *bbLoad) group(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	if id, ok := ld.groups[strings.ToLower(name)]; ok {
		return id, nil
	}
	id, kname, err := identity.ImportGroup(ctx, tx, name)
	if err != nil {
		return 0, err
	}
	ld.rep.groupRenamed(name, kname)
	ld.groups[strings.ToLower(name)] = id
	ld.jr.add(func() { delete(ld.groups, strings.ToLower(name)) })
	return id, nil
}

func (ld *bbLoad) user(ctx context.Context, tx pgx.Tx, u *bbUser) (int64, error) {
	if u == nil {
		return 0, nil
	}
	full := *u
	if p, ok := ld.people[strings.ToLower(cmp.Or(u.Name, u.Slug))]; ok {
		full.Name = cmp.Or(full.Name, p.Name)
		full.EmailAddress = cmp.Or(full.EmailAddress, p.EmailAddress)
		full.DisplayName = cmp.Or(full.DisplayName, p.DisplayName)
	}
	return ld.us.get(ctx, tx, full.jira())
}

func ms(v int64) time.Time {
	if v <= 0 {
		return time.Now()
	}
	return time.UnixMilli(v)
}

// bbMarkdown converts the Markdown of descriptions and comments. Bitbucket's has no formulas: its dollars stay text
// (prices, variables of shells). The validation converts the same way.
func bbMarkdown(s string) *docmodel.Doc {
	return docmodel.FromMarkdownWith(s, docmodel.MarkdownOptions{NoMath: true})
}

var projectPermissions = map[string]string{"PROJECT_READ": code.PermRead, "PROJECT_WRITE": code.PermWrite, "PROJECT_ADMIN": code.PermProjectAdmin}
var repoPermissions = map[string]string{"REPO_READ": code.PermRead, "REPO_WRITE": code.PermWrite, "REPO_ADMIN": code.PermAdmin}

// grants loads the user and group permissions of a project (repoID 0) or a repository.
func (ld *bbLoad) grants(ctx context.Context, im *code.Importer, tx pgx.Tx, projectID, repoID int64, mapping map[string]string,
	public bool, name string, dir ...string) error {
	var users []bbUserPermission
	if _, err := ld.work.Read(&users, append(dir, "permissions-users.json")...); err != nil {
		return err
	}
	for _, p := range users {
		perm, ok := mapping[p.Permission]
		if !ok {
			ld.rep.Unsupported["permission "+p.Permission]++
			continue
		}
		uid, err := ld.user(ctx, tx, &p.User)
		if err != nil {
			return err
		}
		if uid == 0 {
			continue
		}
		if err := im.Grant(projectID, repoID, perm, "user", uid); err != nil {
			return err
		}
		ld.count("grants")
	}
	var groups []bbGroupPermission
	if _, err := ld.work.Read(&groups, append(dir, "permissions-groups.json")...); err != nil {
		return err
	}
	for _, p := range groups {
		perm, ok := mapping[p.Permission]
		if !ok || p.Group.Name == "" {
			ld.rep.Unsupported["permission "+p.Permission]++
			continue
		}
		gid, err := ld.group(ctx, tx, p.Group.Name)
		if err != nil {
			return err
		}
		if err := im.Grant(projectID, repoID, perm, "group", gid); err != nil {
			return err
		}
		ld.count("grants")
	}
	if public {
		// Anonymous access of the source becomes access for signed-in users; Kogge's anonymous access is a setting.
		if err := im.Grant(projectID, repoID, code.PermRead, "logged-in", 0); err != nil {
			return err
		}
		ld.rep.warn("%s was public; it is readable for all signed-in users", name)
	}
	return nil
}

var restrictionKinds = map[string]string{"read-only": code.RestrictPush, "no-deletes": code.NoDelete, "fast-forward-only": code.NoRewrite,
	"pull-request-only": code.PullRequestOnly}

// bbPattern translates a branch permission pattern: Bitbucket's match a suffix of the full ref name.
func bbPattern(p string) string {
	if strings.HasSuffix(p, "/") {
		p += "**"
	}
	if strings.HasPrefix(p, "refs/") {
		return p
	}
	return "refs/**/" + p
}

// rules loads the branch permissions of a project (repoID 0) or a repository.
func (ld *bbLoad) rules(ctx context.Context, im *code.Importer, tx pgx.Tx, projectID, repoID int64, name string, dir ...string) error {
	var list []bbRestriction
	if _, err := ld.work.Read(&list, append(dir, "restrictions.json")...); err != nil {
		return err
	}
	for _, r := range list {
		kind, ok := restrictionKinds[r.Type]
		if !ok {
			ld.rep.Unsupported["branch permission "+r.Type]++
			continue
		}
		pattern := ""
		switch r.Matcher.Type.ID {
		case "BRANCH":
			pattern = strings.TrimPrefix(cmp.Or(r.Matcher.ID, "refs/heads/"+r.Matcher.DisplayID), "refs/heads/")
		case "PATTERN":
			pattern = bbPattern(r.Matcher.DisplayID)
		case "ANY_REF":
			pattern = "refs/**"
		default:
			// Branching-model matchers (development, production, feature branches, …) have no counterpart.
			ld.rep.Unsupported["branch permission matcher "+r.Matcher.Type.ID]++
			ld.rep.warn("%s: the branch permission %s on %s %q was not migrated; set it up again with a pattern",
				name, r.Type, strings.ToLower(cmp.Or(r.Matcher.Type.Name, r.Matcher.Type.ID)), r.Matcher.DisplayID)
			continue
		}
		if len(r.AccessKeys) > 0 {
			ld.rep.warn("%s: access keys exempted from the branch permission %s on %q are not exempted in Kogge", name, r.Type,
				r.Matcher.DisplayID)
		}
		var users, groups []int64
		for i := range r.Users {
			uid, err := ld.user(ctx, tx, &r.Users[i])
			if err != nil {
				return err
			}
			if uid != 0 {
				users = append(users, uid)
			}
		}
		for _, g := range r.Groups {
			gid, err := ld.group(ctx, tx, g)
			if err != nil {
				return err
			}
			groups = append(groups, gid)
		}
		if err := im.BranchRule(projectID, repoID, "restriction:"+strconv.FormatInt(r.ID, 10), pattern, kind, users, groups); err != nil {
			return err
		}
		ld.count("branch-permissions")
	}
	return nil
}

// conditions reads default reviewer conditions (a plain list, or a page in some versions).
func (ld *bbLoad) conditions(dir ...string) ([]bbCondition, error) {
	var raw json.RawMessage
	if ok, err := ld.work.Read(&raw, append(dir, "default-reviewers.json")...); err != nil || !ok {
		return nil, err
	}
	var list []bbCondition
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var page struct {
		Values []bbCondition `json:"values"`
	}
	err := json.Unmarshal(raw, &page)
	return page.Values, err
}

// webhooks lists the webhooks of a project or repository in the report: they are not migrated.
func (ld *bbLoad) webhooks(name string, dir ...string) error {
	var list []bbWebhook
	if _, err := ld.work.Read(&list, append(dir, "webhooks.json")...); err != nil {
		return err
	}
	for _, h := range list {
		ld.rep.warn("%s: the webhook %q (%s) was not migrated; set it up again in Kogge if it is still needed", name, h.Name, webhookTarget(h.URL))
		ld.rep.Unsupported["webhook"]++
	}
	return nil
}

// webhookTarget is where a webhook pointed, without user information and parameters, which may hold secrets.
func webhookTarget(address string) string {
	if u, err := url.Parse(address); err == nil {
		return u.Scheme + "://" + u.Host + u.Path
	}
	return ""
}

func (ld *bbLoad) project(ctx context.Context, key string) error {
	var p bbProject
	dir := []string{"bitbucket", "projects", key}
	if ok, err := ld.work.Read(&p, append(dir, "project.json")...); err != nil {
		return err
	} else if !ok {
		return errors.New("not extracted")
	}
	if p.Type == "PERSONAL" || strings.HasPrefix(p.Key, "~") {
		ld.rep.warn("the personal project %s was not migrated; move its repositories to a project first", p.Key)
		return nil
	}
	var projectID int64
	err := ld.inTx(ctx, func(tx pgx.Tx, im *code.Importer) error {
		var err error
		if projectID, err = im.Project(code.ImportProject{SrcID: strconv.FormatInt(p.ID, 10), Key: p.Key, Name: p.Name,
			Description: p.Description}); err != nil {
			return err
		}
		if err := ld.grants(ctx, im, tx, projectID, 0, projectPermissions, p.Public, p.Key, dir...); err != nil {
			return err
		}
		return ld.rules(ctx, im, tx, projectID, 0, p.Key, dir...)
	})
	if err != nil {
		return err
	}
	ld.rep.Counts["code-projects"]++
	if err := ld.webhooks(p.Key, dir...); err != nil {
		return err
	}
	conditions, err := ld.conditions(dir...)
	if err != nil {
		return err
	}
	for _, slug := range ld.work.List(append(dir, "repos")...) {
		// A repository that cannot be loaded is reported; the others are loaded.
		if err := ld.repo(ctx, p.Key, projectID, slug, conditions); err != nil {
			if err := ld.f.record("repository", key+"/"+slug, key+"/"+slug, err); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ld *bbLoad) repo(ctx context.Context, key string, projectID int64, slug string, projectConditions []bbCondition) error {
	dir := []string{"bitbucket", "projects", key, "repos", slug}
	name := key + "/" + slug
	var r bbRepo
	if ok, err := ld.work.Read(&r, append(dir, "repo.json")...); err != nil || !ok {
		return err
	}
	var def bbRef
	if _, err := ld.work.Read(&def, append(dir, "default-branch.json")...); err != nil {
		return err
	}
	sum := RepositorySummary{Project: key, Slug: r.Slug}
	var repoID int64
	err := ld.inTx(ctx, func(_ pgx.Tx, im *code.Importer) error {
		var err error
		repoID, _, err = im.Repository(projectID, code.ImportRepository{SrcID: strconv.FormatInt(r.ID, 10),
			Slug: r.Slug, Name: r.Name, Description: r.Description, DefaultBranch: cmp.Or(def.DisplayID, "main"), Archived: r.Archived})
		return err
	})
	if err != nil {
		return err
	}
	ld.rep.Counts["repositories"]++

	mirror := ld.work.Path("bitbucket", "git", key, r.Slug+".git")
	if _, err := os.Stat(mirror); err == nil {
		started := time.Now()
		if err := ld.l.Code.LoadMirror(ctx, repoID, mirror, def.DisplayID); err != nil {
			return err
		}
		var pointers []lfsPointer
		if _, err := ld.work.Read(&pointers, append(dir, "lfs.json")...); err != nil {
			return err
		}
		missing := 0
		var bytes int64
		for _, p := range pointers {
			file := ld.work.Path(lfsFile("bitbucket", p.OID)...)
			if _, err := os.Stat(file); err != nil {
				missing++
				continue
			}
			if err := ld.l.Code.ImportLFSObject(ctx, repoID, p.OID, file); err != nil {
				return fmt.Errorf("LFS object %s: %w", p.OID, err)
			}
			sum.LFSObjects++
			bytes += p.Size
		}
		ld.rep.Counts["lfs-objects"] += sum.LFSObjects
		ld.rep.time("repositories", 1, dirSize(mirror)+bytes, time.Since(started))
		if missing > 0 {
			ld.rep.warn("%s: %d LFS objects were not extracted (the source lacks them, or the extraction skipped them)", name, missing)
		}
	} else {
		ld.rep.warn("%s: no history was extracted; the repository keeps what it has (extract without -skip-git and load again)", name)
	}
	sum.History = ld.l.Code.RepoHasHistory(ctx, repoID)

	err = ld.inTx(ctx, func(tx pgx.Tx, im *code.Importer) error {
		if err := ld.grants(ctx, im, tx, projectID, repoID, repoPermissions, r.Public, name, dir...); err != nil {
			return err
		}
		if err := ld.rules(ctx, im, tx, projectID, repoID, name, dir...); err != nil {
			return err
		}
		return ld.settings(ctx, im, tx, repoID, name, cmp.Or(def.DisplayID, "main"), projectConditions, dir...)
	})
	if err != nil {
		return err
	}
	if err := ld.webhooks(name, dir...); err != nil {
		return err
	}
	if err := ld.pullRequests(ctx, r, repoID, name, &sum, dir...); err != nil {
		return err
	}
	ld.rep.Repositories = append(ld.rep.Repositories, sum)
	return nil
}

// Merge strategies: Kogge has merge commits, squash, fast-forward, and rebases without and with a merge commit;
// the rest (fast-forward where possible, squash only when it fast-forwards) is approximated.
var mergeStrategies = map[string]string{"no-ff": code.MergeCommit, "ff": code.MergeCommit, "ff-only": code.MergeFastForward,
	"squash": code.MergeSquash, "squash-ff-only": code.MergeSquash, "rebase-no-ff": code.MergeRebaseCommit, "rebase-ff-only": code.MergeRebase}

// exactStrategies are the strategies Kogge merges with as the source did.
var exactStrategies = []string{"no-ff", "ff-only", "squash", "rebase-no-ff", "rebase-ff-only"}

// settings carries the merge checks, merge strategies and default reviewers of a repository over. What the source
// did not answer keeps Kogge's defaults.
func (ld *bbLoad) settings(ctx context.Context, im *code.Importer, tx pgx.Tx, repoID int64, name, defaultBranch string,
	projectConditions []bbCondition, dir ...string) error {
	var prs bbPullRequestSettings
	hasSettings, err := ld.work.Read(&prs, append(dir, "pull-request-settings.json")...)
	if err != nil {
		return err
	}
	var hooks []bbHook
	hasHooks, err := ld.work.Read(&hooks, append(dir, "hooks.json")...)
	if err != nil {
		return err
	}
	// Merge checks are hooks whose keys are not documented: they are recognised by name.
	hook := func(part string) bool {
		return slices.ContainsFunc(hooks, func(h bbHook) bool { return h.Enabled && strings.Contains(strings.ToLower(h.Details.Key), part) })
	}
	known := []string{"needs-work", "incomplete-tasks", "required-approvers", "requiredapprovers", "required-builds", "requiredbuilds",
		"force-push", "unapprove"}
	for _, h := range hooks {
		if h.Enabled && !slices.ContainsFunc(known, func(k string) bool { return strings.Contains(strings.ToLower(h.Details.Key), k) }) {
			ld.rep.Unsupported["hook "+cmp.Or(h.Details.Name, h.Details.Key)]++
		}
	}
	conditions, err := ld.conditions(dir...)
	if err != nil {
		return err
	}
	conditions = append(slices.Clone(projectConditions), conditions...)
	var reviewers []int64
	extraApprovals := 0
	for _, c := range conditions {
		source, target := cmp.Or(c.SourceMatcher, c.SourceRefMatcher), cmp.Or(c.TargetMatcher, c.TargetRefMatcher)
		all := func(m *bbMatcher) bool { return m == nil || m.Type.ID == "ANY_REF" }
		toDefault := target != nil && target.Type.ID == "BRANCH" && strings.TrimPrefix(cmp.Or(target.ID, target.DisplayID), "refs/heads/") == defaultBranch
		if !all(source) || (!all(target) && !toDefault) {
			ld.rep.warn("%s: the default reviewer condition %d (%s → %s) was not migrated: Kogge's default reviewers apply to all pull requests",
				name, c.ID, matcherLabel(source), matcherLabel(target))
			continue
		}
		for i := range c.Reviewers {
			uid, err := ld.user(ctx, tx, &c.Reviewers[i])
			if err != nil {
				return err
			}
			if uid != 0 && !slices.Contains(reviewers, uid) {
				reviewers = append(reviewers, uid)
			}
		}
		extraApprovals = max(extraApprovals, c.RequiredApprovals)
	}
	if hook("force-push") {
		// The hook that rejects force pushes is a rule for all refs in Kogge.
		if err := im.BranchRule(0, repoID, "force-push:"+name, "refs/**", code.NoRewrite, nil, nil); err != nil {
			return err
		}
		ld.count("branch-permissions")
	}
	return im.UpdateSettings(repoID, func(st *code.RepoSettings) {
		if hasSettings {
			st.RequiredApprovals = prs.RequiredApprovers
			if prs.ApproversCheck != nil {
				st.RequiredApprovals = map[bool]int{true: prs.ApproversCheck.Count}[prs.ApproversCheck.Enabled]
			}
			st.RequiredBuilds = prs.RequiredSuccessfulBuilds
			if prs.BuildsCheck != nil {
				st.RequiredBuilds = map[bool]int{true: prs.BuildsCheck.Count}[prs.BuildsCheck.Enabled]
			}
			if prs.RequiredAllApprovers {
				st.RequiredApprovals = max(st.RequiredApprovals, 1)
				ld.rep.warn("%s: merging needed the approval of all reviewers; Kogge counts approvals (%d required)", name, st.RequiredApprovals)
			}
			st.TasksResolved = prs.RequiredAllTasksComplete || hook("incomplete-tasks")
			var strategies []string
			for _, s := range append([]*bbStrategy{prs.MergeConfig.DefaultStrategy}, ptrs(prs.MergeConfig.Strategies)...) {
				if s == nil || !s.Enabled {
					continue
				}
				m, ok := mergeStrategies[s.ID]
				if !ok {
					ld.rep.Unsupported["merge strategy "+s.ID]++
					continue
				}
				if !slices.Contains(exactStrategies, s.ID) {
					ld.rep.Unsupported["merge strategy "+s.ID+" (became "+m+")"]++
				}
				if !slices.Contains(strategies, m) {
					strategies = append(strategies, m)
				}
			}
			if len(strategies) > 0 {
				st.MergeStrategies = strategies
			}
		}
		if hasHooks {
			st.NoNeedsWork = hook("needs-work")
			st.ResetApprovals = hook("unapprove")
			st.TasksResolved = st.TasksResolved || hook("incomplete-tasks")
		}
		if extraApprovals > st.RequiredApprovals {
			st.RequiredApprovals = extraApprovals
		}
		st.DefaultReviewers = reviewers
	})
}

func ptrs[T any](list []T) []*T {
	out := make([]*T, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out
}

func matcherLabel(m *bbMatcher) string {
	if m == nil || m.Type.ID == "ANY_REF" {
		return "any branch"
	}
	return cmp.Or(m.DisplayID, m.ID)
}

var prStates = map[string]string{"OPEN": code.StateOpen, "MERGED": code.StateMerged, "DECLINED": code.StateDeclined}
var reviewStates = map[string]string{"APPROVED": "approved", "NEEDS_WORK": "needs-work", "UNAPPROVED": "unapproved"}

// pullRequests loads the pull requests of a repository, each in a transaction of its own; one that fails is
// reported and the others are loaded. The listing is read element by element.
func (ld *bbLoad) pullRequests(ctx context.Context, r bbRepo, repoID int64, name string, sum *RepositorySummary, dir ...string) error {
	files := []string{ld.work.Path(append(dir, "pull-requests.json")...)}
	if !ld.work.Exists(append(dir, "pull-requests.json")...) {
		return nil
	}
	total, err := countItems(files)
	if err != nil {
		return err
	}
	p := newProgress(ld.log, "pull requests of "+name, total)
	defer p.finish()
	started := time.Now()
	return eachItem(files, func(raw json.RawMessage, _ itemAt) error {
		var pr bbPullRequest
		if err := json.Unmarshal(raw, &pr); err != nil {
			return err
		}
		var activities []bbActivity
		if _, err := ld.work.Read(&activities, append(dir, "activities", strconv.Itoa(pr.ID)+".json")...); err != nil {
			return err
		}
		slices.SortStableFunc(activities, func(a, b bbActivity) int { return cmp.Compare(a.CreatedDate, b.CreatedDate) })
		comments := ld.rep.Counts["comments"]
		err := ld.inTx(ctx, func(tx pgx.Tx, im *code.Importer) error {
			return ld.pullRequest(ctx, im, tx, r, repoID, name, pr, activities)
		})
		if err != nil {
			p.fail()
			return ld.f.record("pull request", strconv.FormatInt(r.ID, 10)+":"+strconv.Itoa(pr.ID), name+"#"+strconv.Itoa(pr.ID), err)
		}
		p.add(1)
		sum.PullRequests++
		sum.Comments += ld.rep.Counts["comments"] - comments
		ld.rep.Counts["pull-requests"]++
		ld.rep.time("pull-requests", 1, 0, time.Since(started))
		started = time.Now()
		return nil
	})
}

// dirSize is the size of the files below a directory.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func (ld *bbLoad) pullRequest(ctx context.Context, im *code.Importer, tx pgx.Tx, r bbRepo, repoID int64, name string, pr bbPullRequest,
	activities []bbActivity) error {
	author, err := ld.user(ctx, tx, &pr.Author.User)
	if err != nil {
		return err
	}
	state, ok := prStates[pr.State]
	if !ok {
		state = code.StateDeclined
	}
	var closed *time.Time
	if pr.ClosedDate > 0 {
		t := ms(pr.ClosedDate)
		closed = &t
	}
	source := pr.FromRef.DisplayID
	if from := pr.FromRef.Repository; from.ID != 0 && from.ID != r.ID {
		// From a fork: Kogge's pull requests come from branches of the same repository. The commits are there (the
		// source keeps them for the pull request), the branch is not; ":" keeps the name from matching a branch.
		source = from.Project.Key + "/" + from.Slug + ":" + pr.FromRef.DisplayID
		if state == code.StateOpen {
			state = code.StateDeclined
			t := ms(pr.UpdatedDate)
			closed = &t
			ld.rep.warn("%s: the open pull request #%d came from the fork %s/%s and was closed; open it again from a branch",
				name, pr.ID, from.Project.Key, from.Slug)
		}
	}
	var closedBy int64
	for _, a := range activities {
		if (a.Action == "MERGED" && state == code.StateMerged) || (a.Action == "DECLINED" && state == code.StateDeclined) {
			if closedBy, err = ld.user(ctx, tx, &a.User); err != nil {
				return err
			}
		}
	}
	srcID := strconv.FormatInt(r.ID, 10) + ":" + strconv.Itoa(pr.ID)
	prID, err := im.PullRequest(repoID, code.ImportPullRequest{SrcID: srcID, Number: pr.ID, Title: pr.Title,
		Description: bbMarkdown(pr.Description), AuthorID: author, Source: source, Target: pr.ToRef.DisplayID,
		SourceSHA: pr.FromRef.LatestCommit, TargetSHA: pr.ToRef.LatestCommit, State: state, Draft: pr.Draft, ClosedBy: closedBy,
		Created: ms(pr.CreatedDate), Updated: ms(pr.UpdatedDate), Closed: closed})
	if err != nil {
		return err
	}
	for _, rv := range pr.Reviewers {
		uid, err := ld.user(ctx, tx, &rv.User)
		if err != nil {
			return err
		}
		if err := im.Reviewer(prID, uid, cmp.Or(reviewStates[rv.Status], "unapproved"), ms(pr.UpdatedDate)); err != nil {
			return err
		}
	}
	for _, a := range activities {
		if err := ld.activity(ctx, im, tx, prID, srcID, source, pr.ToRef.DisplayID, a); err != nil {
			return err
		}
	}
	return nil
}

func commitSummary(c *bbCommits) []map[string]string {
	out := []map[string]string{}
	if c != nil {
		for _, x := range c.Commits {
			subject, _, _ := strings.Cut(x.Message, "\n")
			out = append(out, map[string]string{"sha": x.ID, "subject": subject})
		}
	}
	return out
}

func (ld *bbLoad) activity(ctx context.Context, im *code.Importer, tx pgx.Tx, prID int64, prSrc, source, target string, a bbActivity) error {
	actor, err := ld.user(ctx, tx, &a.User)
	if err != nil {
		return err
	}
	srcID := prSrc + ":" + strconv.FormatInt(a.ID, 10)
	at := ms(a.CreatedDate)
	switch a.Action {
	case "OPENED":
		return im.Activity(prID, srcID, actor, "opened", map[string]any{"source": source, "target": target}, at)
	case "REOPENED":
		return im.Activity(prID, srcID, actor, "reopened", nil, at)
	case "APPROVED":
		return im.Activity(prID, srcID, actor, "reviewed", map[string]any{"state": "approved"}, at)
	case "UNAPPROVED":
		return im.Activity(prID, srcID, actor, "reviewed", map[string]any{"state": "unapproved"}, at)
	case "REVIEWED":
		return im.Activity(prID, srcID, actor, "reviewed", map[string]any{"state": "needs-work"}, at)
	case "MERGED":
		data := map[string]any{}
		if a.Commit != nil {
			data["commit"] = a.Commit.ID
		}
		return im.Activity(prID, srcID, actor, "merged", data, at)
	case "DECLINED":
		return im.Activity(prID, srcID, actor, "declined", nil, at)
	case "RESCOPED":
		return im.Activity(prID, srcID, actor, "rescoped", map[string]any{"added": commitSummary(a.Added), "removed": commitSummary(a.Removed),
			"from": a.PreviousFromHash, "to": a.FromHash}, at)
	case "COMMENTED":
		if a.CommentAction != "ADDED" || a.Comment == nil {
			return nil
		}
		ic := code.ImportComment{}
		if an := a.CommentAnchor; an != nil {
			ic.Path, ic.Line, ic.CommitSHA, ic.Outdated = an.Path, an.Line, an.ToHash, an.Orphaned
			ic.Side = map[bool]string{true: "old", false: "new"}[an.FileType == "FROM"]
		}
		_, err := ld.comment(ctx, im, tx, prID, *a.Comment, ic, 0)
		return err
	default:
		ld.rep.Unsupported["pull request event "+a.Action]++
	}
	return nil
}

// comment loads a comment with its replies; anchor holds the comment's place in the diff (top-level comments only).
func (ld *bbLoad) comment(ctx context.Context, im *code.Importer, tx pgx.Tx, prID int64, c bbComment, anchor code.ImportComment,
	parent int64) (int64, error) {
	author, err := ld.user(ctx, tx, &c.Author)
	if err != nil {
		return 0, err
	}
	ic := anchor
	ic.SrcID, ic.ParentID, ic.AuthorID = "comment:"+strconv.FormatInt(c.ID, 10), parent, author
	ic.Body = bbMarkdown(c.Text)
	ic.Task = c.Severity == "BLOCKER"
	ic.Created, ic.Updated = ms(c.CreatedDate), ms(cmp.Or(c.UpdatedDate, c.CreatedDate))
	if c.State == "RESOLVED" || c.ThreadResolved {
		t := ms(cmp.Or(c.ResolvedDate, c.ThreadResolvedDate, c.UpdatedDate))
		ic.Resolved = &t
	}
	id, err := im.Comment(prID, ic)
	if err != nil {
		return 0, err
	}
	ld.count("comments")
	if ic.Task {
		ld.count("tasks")
	}
	for _, reply := range c.Comments {
		if _, err := ld.comment(ctx, im, tx, prID, reply, code.ImportComment{}, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}
