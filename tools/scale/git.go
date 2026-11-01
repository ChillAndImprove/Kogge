// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// gitClient runs the Git command line against the installation over HTTP.
type gitClient struct {
	binary string
	base   string
}

// url is the HTTP address of a repository named KEY/slug.
func (g *gitClient) url(repo string) string {
	key, slug, _ := strings.Cut(repo, "/")
	return g.base + "/scm/" + strings.ToLower(key) + "/" + slug + ".git"
}

// run runs git in dir, without prompts; with a user, requests carry the user's credentials (Basic: user name and
// access token).
func (g *gitClient) run(dir, user, token string, args ...string) (string, error) {
	full := []string{"-c", "credential.helper=", "-c", "protocol.version=2"}
	if user != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
		full = append(full, "-c", "http.extraHeader=Authorization: Basic "+auth)
	}
	cmd := exec.Command(g.binary, append(full, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// pusher pushes batches of 100 commits that name issues to the push repository, as the writer.
type pusher struct {
	b        *bench
	dir      string // a clone of the push repository
	follower string // a second clone that fetches what was pushed
	batch    int
	ready    bool
}

func (p *pusher) prepare() error {
	if p.ready {
		return nil
	}
	m := p.b.m
	if m.PushRepository == "" || m.WriterToken == "" {
		return errors.New("the manifest has no push repository; generate the data with this version of the tool")
	}
	if _, err := exec.LookPath(p.b.git.binary); err != nil {
		return err
	}
	p.dir = filepath.Join(p.b.work, "push")
	if _, err := p.b.git.run(p.b.work, m.Writer, m.WriterToken, "clone", "-q", p.b.git.url(m.PushRepository), p.dir); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func (p *pusher) prepareFollower() error {
	if err := p.prepare(); err != nil {
		return err
	}
	if p.follower != "" {
		return nil
	}
	if _, _, err := p.push(rand.New(rand.NewPCG(7, 7))); err != nil { // the follower clones at least one commit
		return err
	}
	p.follower = filepath.Join(p.b.work, "follower")
	_, err := p.b.git.run(p.b.work, p.b.m.Reader, p.b.m.Token, "clone", "-q", "--bare", p.b.git.url(p.b.m.PushRepository), p.follower)
	return err
}

// commits adds 100 commits to the local main branch, each naming a random issue, and returns the issue key and SHA
// of the last one.
func (p *pusher) commits(r *rand.Rand) (key, sha string, err error) {
	m := p.b.m
	p.batch++
	parent, _ := p.b.git.run(p.dir, "", "", "rev-parse", "-q", "--verify", "refs/heads/main")
	parent = strings.TrimSpace(parent)
	var stream bytes.Buffer
	var content strings.Builder
	when := time.Now().Unix()
	file := fmt.Sprintf("bench/push-%d-%d.txt", time.Now().UnixNano(), p.batch)
	for i := range 100 {
		key = m.Projects[1+r.IntN(len(m.Projects)-1)] + "-" + strconv.Itoa(1+r.IntN(m.Issues))
		author := "scale-writer@example.com"
		if len(m.Authors) > 0 {
			author = m.Authors[r.IntN(len(m.Authors))]
		}
		msg := fmt.Sprintf("%s %s (change %d of push %d)\n", key, words[r.IntN(len(words))], i+1, p.batch)
		fmt.Fprintf(&content, "%d %s\n", i, msg)
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\nauthor Bench <%s> %d +0000\ncommitter Bench <%s> %d +0000\ndata %d\n%s",
			i+1, author, when, author, when, len(msg), msg)
		if i == 0 && parent != "" {
			fmt.Fprintf(&stream, "from %s\n", parent)
		}
		fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", file, content.Len(), content.String())
	}
	stream.WriteString("done\n")
	cmd := exec.Command(p.b.git.binary, "fast-import", "--quiet", "--done")
	cmd.Dir = p.dir
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git fast-import: %v: %s", err, out)
	}
	out, err := p.b.git.run(p.dir, "", "", "rev-parse", "refs/heads/main")
	return key, strings.TrimSpace(out), err
}

// push creates and pushes 100 commits; it returns the issue key and SHA of the last commit.
func (p *pusher) push(r *rand.Rand) (string, string, error) {
	key, sha, err := p.commits(r)
	if err != nil {
		return "", "", err
	}
	_, err = p.b.git.run(p.dir, p.b.m.Writer, p.b.m.WriterToken, "push", "-q", "origin", "main")
	return key, sha, err
}

// visible measures from the start of a push of 100 commits until the issue of the last commit lists it. All
// commits of a push are linked in one transaction, so the last is as good as any.
func (p *pusher) visible(r *rand.Rand) (time.Duration, error) {
	key, sha, err := p.commits(r)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	if _, err := p.b.git.run(p.dir, p.b.m.Writer, p.b.m.WriterToken, "push", "-q", "origin", "main"); err != nil {
		return 0, err
	}
	for time.Since(start) < time.Minute {
		var dev struct {
			Commits []struct {
				SHA string `json:"sha"`
			} `json:"commits"`
		}
		if err := p.b.call("GET", "/api/v1/issues/"+key+"/development", p.b.m.Token, nil, &dev); err != nil {
			return 0, err
		}
		for _, c := range dev.Commits {
			if c.SHA == sha {
				return time.Since(start), nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, fmt.Errorf("the commit %s is not in %s after a minute", sha[:10], key)
}

// fetchNew pushes 100 commits (not measured) and measures how long the follower takes to fetch them.
func (p *pusher) fetchNew(r *rand.Rand) (time.Duration, error) {
	if _, _, err := p.push(r); err != nil {
		return 0, err
	}
	start := time.Now()
	_, err := p.b.git.run(p.follower, p.b.m.Reader, p.b.m.Token, "fetch", "-q", "origin", "+refs/heads/*:refs/heads/*")
	return time.Since(start), err
}

// fetcher clones the repository with files and fetches it again, as the reader.
type fetcher struct {
	b      *bench
	clones chan string // clones for fetches, one per worker
	n      atomic.Int64
	once   sync.Once
	err    error
}

func (f *fetcher) prepare() error {
	f.once.Do(func() {
		if _, f.err = exec.LookPath(f.b.git.binary); f.err != nil {
			return
		}
		f.clones = make(chan string, 16)
		for i := range 16 {
			dir := filepath.Join(f.b.work, "fetch-"+strconv.Itoa(i))
			if _, f.err = f.b.git.run(f.b.work, f.b.m.Reader, f.b.m.Token, "clone", "-q", "--bare", f.b.git.url(f.b.m.Repository), dir); f.err != nil {
				return
			}
			f.clones <- dir
		}
	})
	return f.err
}

// clone measures a full clone with a checkout of the 200 files.
func (f *fetcher) clone(*rand.Rand) (time.Duration, error) {
	dir := filepath.Join(f.b.work, "clone-"+strconv.FormatInt(f.n.Add(1), 10))
	defer os.RemoveAll(dir)
	start := time.Now()
	_, err := f.b.git.run(f.b.work, f.b.m.Reader, f.b.m.Token, "clone", "-q", f.b.git.url(f.b.m.Repository), dir)
	return time.Since(start), err
}

// fetch measures a fetch of a clone that is up to date: the refs are compared, nothing is sent.
func (f *fetcher) fetch(*rand.Rand) (time.Duration, error) {
	dir := <-f.clones
	defer func() { f.clones <- dir }()
	start := time.Now()
	_, err := f.b.git.run(dir, f.b.m.Reader, f.b.m.Token, "fetch", "-q", "origin", "+refs/heads/*:refs/heads/*")
	return time.Since(start), err
}
