// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/backup"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// cluster is an installation of two web nodes and a worker on one database and one data directory, as behind a
// load balancer, with a code project NODES and its repository app.
type cluster struct {
	t              *testing.T
	a, b           *apitest.Env
	worker         *app.App
	adminA, adminB *apitest.Client // one session of the administrator, at node A and at node B
	adminID        int64
	repoID         int64
	viaA, viaB     string // the clone address of NODES/app at node A and at node B, with an access token
}

// TestSeveralNodes: two web nodes and a worker on one database and one data directory serve one installation,
// and what one node does, the others see and serve at once: permissions, live messages, pushes over HTTP and SSH
// (concurrent ones through both nodes too), pull requests, shared drafts, pipeline jobs with their logs, and a
// backup taken while both web nodes write.
func TestSeveralNodes(t *testing.T) {
	a := apitest.New(t)
	c := &cluster{t: t, a: a, b: a.Node(), worker: workerNode(t, a), adminA: a.Admin()}
	c.adminB = c.adminA.Via(c.b)
	c.adminID = userIDOf(t, c.adminA)
	var token struct {
		Token string `json:"token"`
	}
	c.adminA.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	c.adminA.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "NODES", "name": "Nodes"}, nil)
	c.adminA.MustDo(http.MethodPut, "/api/v1/code/projects/NODES/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": c.adminID}}}, nil)
	var repo struct {
		ID        int64  `json:"id"`
		CloneHTTP string `json:"cloneHttp"`
	}
	c.adminA.MustDo(http.MethodPost, "/api/v1/code/projects/NODES/repos", map[string]any{"name": "app", "readme": true}, &repo)
	c.repoID = repo.ID
	c.viaA, c.viaB = through(t, repo.CloneHTTP, c.a, token.Token), through(t, repo.CloneHTTP, c.b, token.Token)

	c.permissions()
	c.liveMessages()
	c.pushes()
	c.sshAtBothNodes()
	c.pullRequest()
	c.editingTogether()
	c.agentAndLog()
	c.backupWhileWriting()
}

// permissions: a change of permissions or settings at one node holds at the other at once, although the other has
// what it changes in its caches.
func (c *cluster) permissions() {
	t := c.t
	otto := c.a.CreateUser("otto")
	ottoB := otto.Via(c.b)
	ottoID := userIDOf(t, otto)
	// Node B has Otto's access to code in its caches: he does not see the repository.
	if r := ottoB.Do(http.MethodGet, "/api/v1/code/repos/NODES/app", nil); r.Status != http.StatusNotFound {
		t.Fatalf("otto sees the repository before he may: %d", r.Status)
	}
	c.adminA.MustDo(http.MethodPut, "/api/v1/code/repos/NODES/app/permissions", []map[string]any{
		{"permission": "repo.read", "holder": map[string]any{"type": "user", "id": ottoID}}}, nil)
	took := eventually(t, 2*time.Second, "a grant at node A holds at node B", func() bool {
		return ottoB.Do(http.MethodGet, "/api/v1/code/repos/NODES/app", nil).Status == http.StatusOK
	})
	t.Logf("a grant at node A held at node B after %v", took)
	c.adminA.MustDo(http.MethodPut, "/api/v1/code/repos/NODES/app/permissions", []map[string]any{}, nil)
	eventually(t, 2*time.Second, "a revocation at node A holds at node B", func() bool {
		return ottoB.Do(http.MethodGet, "/api/v1/code/repos/NODES/app", nil).Status == http.StatusNotFound
	})

	// Global permissions, at node B and at the worker, which checks them for notifications and webhooks.
	var m me
	ottoB.Get("/api/v1/me", &m)
	ottoAtWorker := &identity.Principal{User: &identity.User{ID: ottoID}}
	if perms, err := c.worker.Authz.Global(context.Background(), ottoAtWorker); err != nil || has(m.Permissions, "space.create") ||
		perms["space.create"] {
		t.Fatalf("permissions before the grant: %v, at the worker %v %v", m.Permissions, perms, err)
	}
	c.adminA.Post("/api/v1/admin/global-permissions", map[string]any{"permission": "space.create", "principalType": "user",
		"principalId": ottoID}, nil)
	eventually(t, 2*time.Second, "a global permission granted at node A holds at node B", func() bool {
		var m me
		ottoB.Get("/api/v1/me", &m)
		return has(m.Permissions, "space.create")
	})
	eventually(t, 2*time.Second, "a global permission granted at node A holds at the worker", func() bool {
		perms, err := c.worker.Authz.Global(context.Background(), ottoAtWorker)
		return err == nil && perms["space.create"]
	})
	// A space that Otto creates at node B is his at node A at once, although node A has his access to the wiki in
	// its caches.
	otto.Get("/api/v1/wiki/spaces", nil)
	ottoB.Post("/api/v1/wiki/spaces", map[string]any{"key": "OTTO", "name": "Otto's notes", "open": false}, nil)
	eventually(t, 2*time.Second, "a space created at node B is its creator's at node A", func() bool {
		return otto.Do(http.MethodGet, "/api/v1/wiki/spaces/OTTO", nil).Status == http.StatusOK
	})

	// Settings.
	var st map[string]any
	c.adminB.Get("/api/v1/admin/settings", &st)
	if before, err := c.worker.Settings.Get(context.Background()); err != nil || before.Name == "Kogge of two nodes" {
		t.Fatalf("the worker's settings: %+v %v", before, err)
	}
	st["name"] = "Kogge of two nodes"
	c.adminA.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	eventually(t, 2*time.Second, "settings changed at node A hold at node B", func() bool {
		var now map[string]any
		c.adminB.Get("/api/v1/admin/settings", &now)
		return now["name"] == "Kogge of two nodes"
	})
	eventually(t, 2*time.Second, "settings changed at node A hold at the worker", func() bool {
		now, err := c.worker.Settings.Get(context.Background())
		return err == nil && now.Name == "Kogge of two nodes"
	})
}

// workerNode starts a node of env's installation with the role worker alone, as `kogge serve` runs it with
// KOGGE_ROLES=worker: it serves no HTTP and runs the jobs.
func workerNode(t *testing.T, env *apitest.Env) *app.App {
	t.Helper()
	cfg := *env.App.Cfg
	cfg.Roles = map[config.Role]bool{config.RoleWorker: true}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, &cfg, slog.New(slog.DiscardHandler), app.Options{Argon2: &crypto.Argon2Params{Memory: 1024, Time: 1, Threads: 1}})
	if err != nil {
		cancel()
		t.Fatalf("worker: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("worker: %v", err)
		}
		a.Close()
	})
	select {
	case <-a.Live.Listening():
	case <-time.After(30 * time.Second):
		t.Fatal("the worker does not listen for the notifications of the other nodes")
	}
	return a
}

// liveMessages: what changes at one node reaches the browsers connected to the other.
func (c *cluster) liveMessages() {
	t := c.t
	c.adminA.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "LIVE", "name": "Live", "template": "kanban"}, nil)
	stream := liveStream(t, c.b, c.adminB)
	var issue struct {
		Key string `json:"key"`
	}
	c.adminA.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "LIVE", "typeId": c.issueType(), "summary": "Heard everywhere"},
		&issue)
	if got := waitFor(stream, "issue", issue.Key, 10*time.Second); !contains(got, issue.Key) {
		t.Fatalf("a browser at node B did not hear of %s, created at node A: %v", issue.Key, got)
	}
}

func (c *cluster) issueType() int64 {
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	c.adminA.Get("/api/v1/projects/LIVE/issue-types", &types)
	return types[0].ID
}

// pushes: node B serves reads while node A takes pushes, and shows each push when it returns; pushes through both
// nodes at once reach the hooks of their own node — a hook at another node does not know the push and refuses it —,
// every accepted push is there, and the repository is intact.
func (c *cluster) pushes() {
	t := c.t
	g := newGitter(t)
	work := t.TempDir()
	g.must(work, "clone", "-q", c.viaA, "a")
	dir := filepath.Join(work, "a")

	stop := make(chan struct{})
	readErrors := make(chan string, 100)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, path := range []string{"/api/v1/code/repos/NODES/app/branches", "/api/v1/code/repos/NODES/app/commits?ref=main",
				"/api/v1/code/repos/NODES/app/raw?ref=main&path=README.md"} {
				if status, body, err := send(c.adminB, c.b, http.MethodGet, path, nil); err != nil || status != http.StatusOK {
					select {
					case readErrors <- fmt.Sprintf("%s: %d %s %v", path, status, body, err):
					default:
					}
				}
			}
		}
	}()
	for i := range 5 {
		sha, err := g.commit(dir, "a.txt", fmt.Sprintf("version %d\n", i), fmt.Sprintf("Version %d through node A", i))
		if err != nil {
			t.Fatal(err)
		}
		g.must(dir, "push", "-q", "origin", "main")
		var branches branchList
		c.adminB.Get("/api/v1/code/repos/NODES/app/branches", &branches)
		if got := branches.sha("main"); got != sha {
			t.Fatalf("push %d: node B shows main at %s, the push brought %s", i, got, sha)
		}
	}
	close(stop)
	readers.Wait()
	close(readErrors)
	for e := range readErrors {
		t.Errorf("a read at node B during the pushes failed: %s", e)
	}

	base := g.must(dir, "rev-parse", "HEAD")
	g.must(dir, "push", "-q", "origin", "HEAD:refs/heads/shared")
	accepted := 6 // five pushes to main, one of the branch shared
	type result struct {
		branch, head string
		pushes       int
		err          error
	}
	results := make(chan result, 8)
	var pushers sync.WaitGroup
	// Six clients push three commits each to branches of their own, through node A and node B in turns.
	for i := range 6 {
		pushers.Add(1)
		go func() {
			defer pushers.Done()
			node, remote := "A", c.viaA
			if i%2 == 1 {
				node, remote = "B", c.viaB
			}
			res := result{branch: fmt.Sprintf("w%d", i)}
			defer func() { results <- res }()
			if out, err := g.run(work, "clone", "-q", remote, res.branch); err != nil {
				res.err = fmt.Errorf("clone: %v: %s", err, out)
				return
			}
			clone := filepath.Join(work, res.branch)
			for n := range 3 {
				if res.head, res.err = g.commit(clone, res.branch+".txt", fmt.Sprintf("%d\n", n),
					fmt.Sprintf("Commit %d of %s through node %s", n, res.branch, node)); res.err != nil {
					return
				}
				if out, err := g.run(clone, "push", "-q", "origin", "HEAD:refs/heads/"+res.branch); err != nil {
					res.err = fmt.Errorf("push %d through node %s: %v: %s", n, node, err, out)
					return
				}
				res.pushes++
			}
		}()
	}
	// Two clients race for one branch through different nodes: a push that lost the race is refused, and the client
	// tries again on top of the other's commits.
	for i, remote := range []string{c.viaA, c.viaB} {
		pushers.Add(1)
		go func() {
			defer pushers.Done()
			name := fmt.Sprintf("racer%d", i)
			res := result{branch: "shared"}
			defer func() { results <- res }()
			if out, err := g.run(work, "clone", "-q", "-b", "shared", remote, name); err != nil {
				res.err = fmt.Errorf("clone: %v: %s", err, out)
				return
			}
			clone := filepath.Join(work, name)
			for n := range 3 {
				if _, res.err = g.commit(clone, name+".txt", fmt.Sprintf("%d\n", n), fmt.Sprintf("Commit %d of %s", n, name)); res.err != nil {
					return
				}
				for attempt := 0; ; attempt++ {
					out, err := g.run(clone, "push", "-q", "origin", "HEAD:refs/heads/shared")
					if err == nil {
						res.pushes++
						break
					}
					if attempt == 20 {
						res.err = fmt.Errorf("%s: no push accepted in 20 attempts: %s", name, out)
						return
					}
					if out, err := g.run(clone, "pull", "-q", "--rebase", "origin", "shared"); err != nil {
						res.err = fmt.Errorf("%s: pull: %v: %s", name, err, out)
						return
					}
				}
			}
		}()
	}
	pushers.Wait()
	close(results)
	heads := map[string]string{}
	for res := range results {
		if res.err != nil {
			t.Errorf("%s: %v", res.branch, res.err)
		}
		accepted += res.pushes
		if res.branch != "shared" {
			heads[res.branch] = res.head
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	for node, admin := range map[string]*apitest.Client{"A": c.adminA, "B": c.adminB} {
		var branches branchList
		admin.Get("/api/v1/code/repos/NODES/app/branches?limit=50", &branches)
		for branch, head := range heads {
			if got := branches.sha(branch); got != head {
				t.Errorf("node %s shows %s at %s, the last push brought %s", node, branch, got, head)
			}
		}
	}
	repoDir := code.RepoDir(c.a.App.Cfg.DataDir, c.repoID)
	subjects := g.must(work, "--git-dir="+repoDir, "log", "--format=%s", base+"..refs/heads/shared")
	for i := range 2 {
		for n := range 3 {
			if want := fmt.Sprintf("Commit %d of racer%d", n, i); strings.Count(subjects, want) != 1 {
				t.Errorf("the branch shared has %q %d times:\n%s", want, strings.Count(subjects, want), subjects)
			}
		}
	}
	if out, err := g.run(work, "--git-dir="+repoDir, "fsck", "--full", "--strict", "--no-dangling"); err != nil {
		t.Errorf("git fsck: %v\n%s", err, out)
	}
	// post-receive records a push at the node that ran it: every accepted push, none twice.
	var recorded int
	if err := c.a.App.Pool.QueryRow(context.Background(), "SELECT count(*) FROM pushes WHERE repository_id = $1 AND protocol = 'http'",
		c.repoID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != accepted {
		t.Errorf("%d pushes recorded, %d accepted", recorded, accepted)
	}
	// The sockets of the hooks are the nodes' own, not on the data volume they share.
	if sockets, _ := filepath.Glob(filepath.Join(c.a.App.Cfg.DataDir, "*", "*.sock")); len(sockets) > 0 {
		t.Errorf("sockets on the data volume: %v", sockets)
	}
	c.a.WorkJobs()
}

// sshAtBothNodes: nodes that start their SSH servers at the same moment on a data directory without host keys
// agree on the keys, so that a client that knows the installation's key from one node accepts the other; pushes
// over SSH reach both nodes.
func (c *cluster) sshAtBothNodes() {
	t := c.t
	var servers [2]*code.SSHServer
	var wg sync.WaitGroup
	for i, node := range []*apitest.Env{c.a, c.b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv, err := node.App.Code.NewSSHServer()
			if err != nil {
				t.Error(err)
			}
			servers[i] = srv
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ports := make([]int, 2)
	for i, srv := range servers {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ctx, l) }()
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "admin")
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	c.adminA.MustDo(http.MethodPost, "/api/v1/me/ssh-keys", map[string]any{"name": "laptop",
		"key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, nil)
	// The client learns the installation's host key from node A, under one name, and checks it strictly at node B.
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	sshGit := func(port int, checking string) *gitter {
		return newGitter(t, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s -o HostKeyAlias=kogge "+
			"-o StrictHostKeyChecking=%s -o LogLevel=ERROR -p %d", keyFile, knownHosts, checking, port))
	}
	work := t.TempDir()
	pushed := map[string]string{}
	for i, g := range []*gitter{sshGit(ports[0], "accept-new"), sshGit(ports[1], "yes")} {
		name := []string{"ssh-a", "ssh-b"}[i]
		g.must(work, "clone", "-q", "git@127.0.0.1:nodes/app.git", name)
		sha, err := g.commit(filepath.Join(work, name), name+".txt", name+"\n", "Over SSH: "+name)
		if err != nil {
			t.Fatal(err)
		}
		g.must(filepath.Join(work, name), "push", "-q", "origin", "HEAD:refs/heads/"+name)
		pushed[name] = sha
	}
	var branches branchList
	c.adminB.Get("/api/v1/code/repos/NODES/app/branches?limit=50", &branches)
	for name, sha := range pushed {
		if got := branches.sha(name); got != sha {
			t.Errorf("%s pushed over SSH: at %q, the push brought %s", name, got, sha)
		}
	}
	if keys, _ := filepath.Glob(filepath.Join(c.a.App.Cfg.DataDir, "ssh", "*")); len(keys) != 2 {
		t.Errorf("the directory of the host keys holds %v", keys)
	}
}

// pullRequest: a pull request whose branch was pushed at node A is merged at node B.
func (c *cluster) pullRequest() {
	t := c.t
	g := newGitter(t)
	work := t.TempDir()
	g.must(work, "clone", "-q", c.viaA, "pr")
	dir := filepath.Join(work, "pr")
	g.must(dir, "checkout", "-q", "-b", "feature")
	head, err := g.commit(dir, "feature.txt", "new\n", "A feature, pushed through node A")
	if err != nil {
		t.Fatal(err)
	}
	g.must(dir, "push", "-q", "origin", "feature")
	c.a.WorkJobs()
	var pr struct {
		Number   int    `json:"number"`
		State    string `json:"state"`
		MergeSHA string `json:"mergeSha"`
	}
	c.adminB.MustDo(http.MethodPost, "/api/v1/code/repos/NODES/app/pull-requests", map[string]any{"title": "Feature", "source": "feature",
		"target": "main"}, &pr)
	c.adminB.MustDo(http.MethodPost, apitest.Fmt("/api/v1/code/repos/NODES/app/pull-requests/%d/merge", pr.Number),
		map[string]any{"strategy": "merge"}, &pr)
	if pr.State != "merged" || pr.MergeSHA == "" {
		t.Fatalf("merged at node B: %+v", pr)
	}
	c.a.WorkJobs()
	c.adminA.Get(apitest.Fmt("/api/v1/code/repos/NODES/app/pull-requests/%d", pr.Number), &pr)
	var commit struct {
		Parents []string `json:"parents"`
	}
	c.adminA.Get("/api/v1/code/repos/NODES/app/commits/"+pr.MergeSHA, &commit)
	var branches branchList
	c.adminA.Get("/api/v1/code/repos/NODES/app/branches?limit=50", &branches)
	if pr.State != "merged" || branches.sha("main") != pr.MergeSHA || len(commit.Parents) != 2 || commit.Parents[1] != head {
		t.Errorf("node A after the merge at node B: %+v, main at %s, parents %v", pr, branches.sha("main"), commit.Parents)
	}
}

// editingTogether: an editor connected to node A and one connected to node B edit a page together.
func (c *cluster) editingTogether() {
	t := c.t
	var sp struct {
		HomePageID int64 `json:"homePageId"`
	}
	c.adminA.Post("/api/v1/wiki/spaces", map[string]any{"key": "TEAM", "name": "Team"}, &sp)
	var pg struct {
		ID int64 `json:"id"`
	}
	c.adminA.Post("/api/v1/wiki/spaces/TEAM/pages", map[string]any{"title": "Plan", "parentId": sp.HomePageID}, &pg)
	pid := apitest.Fmt("%d", pg.ID)
	c.adminA.Post("/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Plan", "baseVersion": 0,
		"body": map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "Plan"}}}}}}, nil)
	var session struct {
		Version int `json:"version"`
	}
	c.adminA.Get("/api/v1/wiki/pages/"+pid+"/edit", &session)
	v := session.Version
	one, two := fmt.Sprintf("%d:node-a", c.adminID), fmt.Sprintf("%d:node-b", c.adminID)
	edA := editorSocket(t, c.a, c.adminA, pid, one, v)
	edB := editorSocket(t, c.b, c.adminB, pid, two, v)
	edA.next("welcome")
	edB.next("welcome")
	insert := func(pos int, text string) map[string]any {
		return map[string]any{"stepType": "replace", "from": pos, "to": pos,
			"slice": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}}
	}

	// A step at node A reaches the editor at node B, and one at node B the editor at node A.
	edA.send(map[string]any{"t": "steps", "id": 1, "version": v, "steps": []any{insert(5, " for Monday")}})
	if ack := edA.next("ack"); ack["version"] != float64(v+1) {
		t.Fatalf("ack at node A: %v", ack)
	}
	if m := edB.next("steps"); m["to"] != float64(v+1) {
		t.Fatalf("the step of node A at node B: %v", m)
	}
	edB.send(map[string]any{"t": "steps", "id": 1, "version": v + 1, "steps": []any{insert(1, "The ")}})
	if ack := edB.next("ack"); ack["version"] != float64(v+2) {
		t.Fatalf("ack at node B: %v", ack)
	}
	// The editor at node A hears its own step back first, then the one of node B.
	for m := edA.next("steps"); m["to"] != float64(v+2); m = edA.next("steps") {
	}
	edB.send(map[string]any{"t": "caret", "version": v + 2, "anchor": 3, "head": 3})
	if caret := edA.next("caret"); caret["client"] != two {
		t.Fatalf("the caret of the editor at node B at node A: %v", caret)
	}
	c.adminB.MustDo(http.MethodPost, "/api/v1/wiki/pages/"+pid+"/publish", map[string]any{"title": "Plan", "draftVersion": v + 2,
		"baseVersion": 1}, nil)
	for _, ed := range []*editor{edA, edB} {
		if m := ed.next("published"); m["pageVersion"] != float64(2) {
			t.Fatalf("published: %v", m)
		}
	}
	var page struct {
		Body json.RawMessage `json:"body"`
	}
	c.adminA.Get("/api/v1/wiki/pages/"+pid, &page)
	if !bytes.Contains(page.Body, []byte(`"The Plan for Monday"`)) {
		t.Errorf("the published page lacks a part: %s", page.Body)
	}
}

// agentAndLog: an agent waiting at node A takes the job of a run started at node B at once, and the job's log, which
// the agent sends to node A, is live at node B.
func (c *cluster) agentAndLog() {
	t := c.t
	const pipeline = `version: 1
pipelines:
  ci:
    on:
      manual: true
    stages:
      - name: Build
        jobs:
          - name: talk
            runs-on: [shell]
            steps:
              - run: |
                  sleep 2
                  echo "first line"
                  sleep 3
                  echo "second line"
`
	c.adminA.MustDo(http.MethodPut, "/api/v1/code/repos/NODES/app/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": pipeline, "message": "A pipeline"}, nil)
	c.a.WorkJobs()
	// Without the notifications of node B, the agent waiting at node A would look for work only when its long poll
	// starts again (after 25 seconds), and the log stream at node B would look for news every 20 seconds.
	c.a.App.Builds.ClaimRecheck = time.Minute
	c.b.App.Builds.LogRecheck = time.Minute
	var reg struct {
		Token string `json:"token"`
	}
	c.adminA.MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "node A", "autoApprove": true}, &reg)
	cfg := &agent.Config{Server: c.a.URL, Name: "at-node-a", WorkDir: t.TempDir(), Shell: true}
	if err := agent.Register(context.Background(), cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop, stopAgent := context.WithCancel(context.Background())
	kill, killAgent := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ag.Run(stop, kill) }()
	t.Cleanup(func() {
		stopAgent()
		killAgent()
		<-done
	})
	eventually(t, 10*time.Second, "the agent waits for work at node A", func() bool {
		var agents []struct {
			Online bool `json:"online"`
		}
		c.adminA.Get("/api/v1/admin/agents", &agents)
		return len(agents) == 1 && agents[0].Online
	})
	time.Sleep(2 * time.Second) // into the agent's long poll

	var started []struct {
		Number int `json:"number"`
	}
	c.adminB.MustDo(http.MethodPost, "/api/v1/code/repos/NODES/app/runs", map[string]any{"pipeline": "ci"}, &started)
	if len(started) != 1 {
		t.Fatalf("runs started: %+v", started)
	}
	var jobID int64
	took := eventually(t, 10*time.Second, "the agent at node A takes the job started at node B", func() bool {
		var run struct {
			Stages []struct {
				Jobs []struct {
					ID    int64  `json:"id"`
					State string `json:"state"`
				} `json:"jobs"`
			} `json:"stages"`
		}
		c.adminB.Get(apitest.Fmt("/api/v1/code/repos/NODES/app/runs/%d", started[0].Number), &run)
		j := run.Stages[0].Jobs[0]
		jobID = j.ID
		return j.State == "running"
	})
	t.Logf("the agent at node A took the job of node B after %v", took)

	// The log, streamed from node B: its lines come as the job writes them, three seconds apart, and its end right
	// after the job's.
	req, err := http.NewRequest(http.MethodGet, c.b.URL+apitest.Fmt("/api/v1/builds/jobs/%d/log/stream", jobID), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.adminB.Send(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the log stream at node B: %d", res.StatusCode)
	}
	connected := time.Now()
	var first, second, end time.Duration
	var text strings.Builder
	sc := bufio.NewScanner(res.Body)
	event := ""
	for end == 0 && sc.Scan() {
		line := sc.Text()
		if e, ok := strings.CutPrefix(line, "event: "); ok {
			event = e
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch event {
		case "log":
			var m struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(data), &m)
			text.WriteString(m.Text)
			if first == 0 && strings.Contains(text.String(), "first line") {
				first = time.Since(connected)
			}
			if second == 0 && strings.Contains(text.String(), "second line") {
				second = time.Since(connected)
			}
		case "end":
			end = time.Since(connected)
			if !strings.Contains(data, "succeeded") {
				t.Errorf("the job ended: %s", data)
			}
		}
	}
	t.Logf("the log at node B: first line after %v, second after %v, end after %v", first, second, end)
	if first == 0 || second == 0 || end == 0 {
		t.Fatalf("the log stream at node B: first line after %v, second after %v, end after %v:\n%s", first, second, end, text.String())
	}
	if first < 500*time.Millisecond || second-first < 2*time.Second || end > 12*time.Second {
		t.Errorf("the log at node B was not live: first line after %v, second after %v, end after %v", first, second, end)
	}
}

// backupWhileWriting: a backup taken while both nodes take pushes and create issues restores into a consistent
// installation: its repositories have the branches its database recorded the pushes of, whichever node took them.
func (c *cluster) backupWhileWriting() {
	t := c.t
	ctx := context.Background()
	g := newGitter(t)
	work := t.TempDir()
	type writer struct {
		name string
		do   func(i int) error
	}
	pusher := func(name, remote string) writer {
		g.must(work, "clone", "-q", remote, name)
		clone := filepath.Join(work, name)
		return writer{name, func(i int) error {
			if _, err := g.commit(clone, name+".txt", fmt.Sprintf("%d\n", i), fmt.Sprintf("%s %d", name, i)); err != nil {
				return err
			}
			if out, err := g.run(clone, "push", "-q", "origin", "HEAD:refs/heads/"+name); err != nil {
				return fmt.Errorf("%v: %s", err, out)
			}
			return nil
		}}
	}
	typeID := c.issueType()
	issues := func(name string, node *apitest.Env, admin *apitest.Client) writer {
		return writer{name, func(i int) error {
			status, body, err := send(admin, node, http.MethodPost, "/api/v1/issues", map[string]any{"project": "LIVE", "typeId": typeID,
				"summary": fmt.Sprintf("%s %d", name, i)})
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("%d %s", status, body)
			}
			return err
		}}
	}
	writers := []writer{pusher("backup-a", c.viaA), pusher("backup-b", c.viaB), issues("issues at node A", c.a, c.adminA),
		issues("issues at node B", c.b, c.adminB)}
	stop := make(chan struct{})
	counts := make([]atomic.Int64, len(writers))
	errs := make(chan error, len(writers))
	var running sync.WaitGroup
	for k, w := range writers {
		running.Add(1)
		go func() {
			defer running.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := w.do(i); err != nil {
					errs <- fmt.Errorf("%s %d: %w", w.name, i, err)
					return
				}
				counts[k].Add(1)
			}
		}()
	}
	// wrote waits until every writer wrote more than it had (the backup runs while they write) and returns the counts.
	wrote := func(before []int64) []int64 {
		t.Helper()
		now := make([]int64, len(counts))
		deadline := time.Now().Add(time.Minute)
		for k := range counts {
			for now[k] = counts[k].Load(); now[k] <= before[k]; now[k] = counts[k].Load() {
				if time.Now().After(deadline) {
					t.Fatalf("%s does not write", writers[k].name)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		return now
	}
	started := wrote(make([]int64, len(counts)))
	kr, err := crypto.LoadKeyring(c.a.App.Cfg.MasterKeyFile, c.a.App.Cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "kogge.tar")
	sum, err := backup.Write(ctx, backup.Options{Pool: c.a.App.Pool, DataDir: c.a.App.Cfg.DataDir, Keyring: kr}, file)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	finished := wrote(started) // everyone goes on writing after the backup
	close(stop)
	running.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a write during the backup failed: %v", err)
	}
	t.Logf("writes when the backup started: %v, when it had finished: %v", started, finished)
	if len(sum.Missing) > 0 {
		t.Errorf("the backup misses files: %v", sum.Missing)
	}

	dstURL := pgtest.NewEmpty(t)
	dst, err := db.Open(ctx, dstURL, "kogge-restore-test", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	dstDir := t.TempDir()
	keyFile := filepath.Join(dstDir, "master.key")
	key, err := os.ReadFile(c.a.App.Cfg.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := backup.Restore(ctx, backup.Options{Pool: dst, DataDir: dstDir, Keyring: kr}, file, backup.RestoreOptions{KeyFile: keyFile})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(report.Problems) > 0 {
		t.Errorf("fsck after the restore: %+v", report.Problems)
	}
	rows, err := dst.Query(ctx, "SELECT updates FROM pushes WHERE repository_id = $1 ORDER BY id", c.repoID)
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var updates []code.RefUpdate
		if err := json.Unmarshal(raw, &updates); err != nil {
			t.Fatal(err)
		}
		for _, u := range updates {
			recorded[u.Ref] = u.New
		}
	}
	rows.Close()
	restored := code.RepoDir(dstDir, c.repoID)
	for _, branch := range []string{"backup-a", "backup-b"} {
		onDisk, _ := g.run(work, "--git-dir="+restored, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
		if want := recorded["refs/heads/"+branch]; want == "" || onDisk != want {
			t.Errorf("%s in the restored repository is at %q, the last push its database recorded brought %q", branch, onDisk, want)
		}
	}
	if out, err := g.run(work, "--git-dir="+restored, "fsck", "--full", "--no-dangling"); err != nil {
		t.Errorf("git fsck of the restored repository: %v\n%s", err, out)
	}
}

// gitter runs git as a client with a home of its own; run and commit are safe in goroutines.
type gitter struct {
	t    *testing.T
	home string
	env  []string
}

func newGitter(t *testing.T, env ...string) *gitter {
	return &gitter{t: t, home: t.TempDir(), env: env}
}

// run runs git in dir and returns its combined output.
func (g *gitter) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "init.defaultBranch=main"},
		args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + g.home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"},
		g.env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (g *gitter) must(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.run(dir, args...)
	if err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// commit writes a file and commits it, and returns the commit.
func (g *gitter) commit(dir, file, content, message string) (string, error) {
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		return "", err
	}
	if out, err := g.run(dir, "add", file); err != nil {
		return "", fmt.Errorf("git add: %v: %s", err, out)
	}
	if out, err := g.run(dir, "commit", "-q", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %v: %s", err, out)
	}
	return g.run(dir, "rev-parse", "HEAD")
}

// send sends a request of a client to a node and returns the status and the body; unlike the client's Do, it is
// safe in goroutines.
func send(c *apitest.Client, node *apitest.Env, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, node.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Send(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	return res.StatusCode, data, err
}

// eventually waits up to d for cond, which a notification from the other node makes true at once (much sooner
// than any cache of the nodes expires), and returns how long it took.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > d {
			t.Fatalf("%s: not within %v", what, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start)
}

// through returns the clone address of a repository at a node, with the administrator's access token.
func through(t *testing.T, cloneHTTP string, node *apitest.Env, token string) string {
	t.Helper()
	u, err := url.Parse(cloneHTTP)
	if err != nil {
		t.Fatal(err)
	}
	n, err := url.Parse(node.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = n.Host
	u.User = url.UserPassword("admin", token)
	return u.String()
}

type branchList struct {
	Items []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	} `json:"items"`
}

func (b branchList) sha(name string) string {
	for _, it := range b.Items {
		if it.Name == name {
			return it.Commit.SHA
		}
	}
	return ""
}

// editor is the WebSocket of an editor of a shared draft.
type editor struct {
	t  *testing.T
	ws *websocket.Conn
}

// editorSocket connects an editor of a page to a node. Behind a load balancer, the editor's page comes from the
// installation's address, whichever node takes the socket.
func editorSocket(t *testing.T, node *apitest.Env, c *apitest.Client, pageID, client string, version int) *editor {
	t.Helper()
	address := strings.Replace(node.URL, "http://", "ws://", 1) + fmt.Sprintf("/api/v1/wiki/pages/%s/edit/socket?version=%d&client=%s",
		pageID, version, client)
	cfg, err := websocket.NewConfig(address, node.App.Cfg.PublicURL.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.Cookies() {
		cfg.Header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &editor{t: t, ws: ws}
}

func (e *editor) send(m map[string]any) {
	e.t.Helper()
	if err := websocket.JSON.Send(e.ws, m); err != nil {
		e.t.Fatal(err)
	}
}

// next returns the next message of a type, skipping others.
func (e *editor) next(typ string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = e.ws.SetReadDeadline(deadline)
		var m map[string]any
		if err := websocket.JSON.Receive(e.ws, &m); err != nil {
			e.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m["t"] == typ {
			return m
		}
	}
	e.t.Fatalf("no %s", typ)
	return nil
}
