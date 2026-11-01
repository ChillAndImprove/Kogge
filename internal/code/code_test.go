// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/testutil/apitest"
)

func TestMain(m *testing.M) {
	// Pushes run the hooks, which start this test binary as `kogge hook …`.
	code.HookMain()
	os.Exit(m.Run())
}

type gitClient struct {
	t    *testing.T
	home string
	env  []string
}

func newGit(t *testing.T, env ...string) *gitClient {
	return &gitClient{t: t, home: t.TempDir(), env: env}
}

// run runs git and returns its combined output.
func (g *gitClient) run(dir string, args ...string) (string, error) {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com", "-c", "init.defaultBranch=main",
		"-c", "protocol.version=2"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + g.home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C"}, g.env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (g *gitClient) must(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.run(dir, args...)
	if err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type repository struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	DefaultBranch string `json:"defaultBranch"`
	CloneHTTP     string `json:"cloneHttp"`
	CloneSSH      string `json:"cloneSsh"`
	Permission    string `json:"permission"`
	Empty         *bool  `json:"empty"`
	Import        *struct {
		State string `json:"state"`
		Error string `json:"error"`
	} `json:"import"`
}

type pullRequest struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	SourceSHA string `json:"sourceSha"`
	MergeSHA  string `json:"mergeSha"`
	Reviewers []struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		State string `json:"state"`
	} `json:"reviewers"`
	Checks []struct {
		Key string `json:"key"`
		OK  bool   `json:"ok"`
	} `json:"checks"`
	Permissions struct {
		Merge bool `json:"merge"`
	} `json:"permissions"`
}

func TestGitHosting(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("dev")
	type me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	var devMe, adminMe me
	dev.Get("/api/v1/me", &devMe)
	admin.Get("/api/v1/me", &adminMe)
	devUser, adminUser := devMe.User, adminMe.User

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "Core Service", "readme": true}, &repo)
	if repo.Slug != "core-service" || repo.DefaultBranch != "main" || repo.Empty == nil || *repo.Empty {
		t.Fatalf("repository = %+v", repo)
	}
	// dev cannot see the repository before getting access.
	if res := dev.Do(http.MethodGet, "/api/v1/code/repos/PLAT/core-service", nil); res.Status != http.StatusNotFound {
		t.Fatalf("visible without permission: %d", res.Status)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/PLAT/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminUser.ID}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": devUser.ID}},
	}, nil)

	var token struct {
		Token string `json:"token"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	anon := u.String()
	u.User = url.UserPassword("dev", token.Token)
	authURL := u.String()

	g := newGit(t)
	work := t.TempDir()
	// Anonymous clients are asked for credentials.
	if out, err := g.run(work, "clone", anon, "anon"); err == nil || !strings.Contains(out, "Authentication") && !strings.Contains(out, "could not read Username") {
		t.Fatalf("anonymous clone: %v\n%s", err, out)
	}
	g.must(work, "clone", authURL, "core")
	dir := filepath.Join(work, "core")
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal("README.md was not cloned")
	}

	// A new branch: the push suggests a pull request.
	g.must(dir, "checkout", "-b", "feature/SHOP-1-login")
	write(t, filepath.Join(dir, "src", "login.go"), "package src\n\n// Login signs in.\nfunc Login() string {\n\treturn \"ok\"\n}\n")
	g.must(dir, "add", ".")
	g.must(dir, "commit", "-m", "SHOP-1 Add login")
	out := g.must(dir, "push", "-u", "origin", "feature/SHOP-1-login")
	if !strings.Contains(out, "Create a pull request for 'feature/SHOP-1-login'") {
		t.Errorf("no pull request hint:\n%s", out)
	}

	// Branch permissions: no rewrites of main, release branches only for the administrator.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/core-service/branch-permissions", []map[string]any{
		{"pattern": "main", "kind": "no-rewrite", "exemptUsers": []int64{}, "exemptGroups": []int64{}},
		{"pattern": "release/*", "kind": "restrict-push", "exemptUsers": []int64{adminUser.ID}, "exemptGroups": []int64{}},
	}, nil)
	g.must(dir, "checkout", "-b", "release/1.0", "main")
	if out, err := g.run(dir, "push", "origin", "release/1.0"); err == nil || !strings.Contains(out, "branch permission") {
		t.Errorf("push to a restricted branch: %v\n%s", err, out)
	}
	g.must(dir, "checkout", "main")
	write(t, filepath.Join(dir, "README.md"), "# Rewritten\n")
	g.must(dir, "commit", "-am", "Rewrite")
	g.must(dir, "push", "origin", "main")
	g.must(dir, "reset", "--hard", "HEAD~1")
	write(t, filepath.Join(dir, "other.txt"), "x\n")
	g.must(dir, "add", ".")
	g.must(dir, "commit", "-m", "Other")
	if out, err := g.run(dir, "push", "--force", "origin", "main"); err == nil || !strings.Contains(out, "history cannot be rewritten") {
		t.Errorf("force push to main: %v\n%s", err, out)
	}
	g.must(dir, "reset", "--hard", "origin/main")

	// Browsing.
	var branches struct {
		Items []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
			Ahead   int    `json:"ahead"`
		} `json:"items"`
		Total int `json:"total"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/branches", &branches)
	if branches.Total != 2 || !branches.Items[0].Default || branches.Items[1].Ahead != 1 {
		t.Errorf("branches = %+v", branches)
	}
	var tree struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
		Readme string `json:"readme"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/tree?ref="+url.QueryEscape("feature/SHOP-1-login"), &tree)
	if len(tree.Entries) != 2 || tree.Entries[0].Name != "src" || tree.Entries[0].Type != "dir" || tree.Readme != "README.md" {
		t.Errorf("tree = %+v", tree)
	}
	var file struct {
		Language string   `json:"language"`
		Lines    []string `json:"lines"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/file?ref=feature/SHOP-1-login&path=src/login.go", &file)
	if file.Language != "Go" || len(file.Lines) != 6 || !strings.Contains(file.Lines[3], `<span class="kd">func</span>`) {
		t.Errorf("file = %+v", file)
	}
	var blame struct {
		Lines  []string `json:"lines"`
		Ranges []struct {
			Commit struct {
				Subject string `json:"subject"`
			} `json:"commit"`
			From int `json:"from"`
			To   int `json:"to"`
		} `json:"ranges"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/blame?ref=feature/SHOP-1-login&path=src/login.go", &blame)
	if len(blame.Lines) != 6 || len(blame.Ranges) != 1 || blame.Ranges[0].Commit.Subject != "SHOP-1 Add login" ||
		blame.Ranges[0].From != 1 || blame.Ranges[0].To != 6 {
		t.Errorf("blame = %+v", blame)
	}
	res := dev.Do(http.MethodGet, "/api/v1/code/repos/PLAT/core-service/raw?ref=main&path=README.md", nil)
	if res.Status != http.StatusOK || string(res.Body) != "# Rewritten\n" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("raw = %d %q %v", res.Status, res.Body, res.Header)
	}
	var cmp struct {
		Commits []struct {
			Subject string `json:"subject"`
		} `json:"commits"`
		Diff struct {
			Files []struct {
				NewPath   string `json:"newPath"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
			} `json:"files"`
		} `json:"diff"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/compare?from=main&to="+url.QueryEscape("feature/SHOP-1-login"), &cmp)
	if len(cmp.Commits) != 1 || cmp.Commits[0].Subject != "SHOP-1 Add login" || len(cmp.Diff.Files) != 1 ||
		cmp.Diff.Files[0].Status != "added" || cmp.Diff.Files[0].Additions != 6 {
		t.Errorf("compare = %+v", cmp)
	}
	res = dev.Do(http.MethodGet, "/api/v1/code/repos/PLAT/core-service/archive?ref=main&format=zip", nil)
	if res.Status != http.StatusOK || len(res.Body) < 100 || string(res.Body[:2]) != "PK" {
		t.Errorf("archive = %d, %d bytes", res.Status, len(res.Body))
	}

	// A pull request, reviewed by the administrator and merged by dev.
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/PLAT/core-service", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"merge", "squash"}, "requiredApprovals": 1, "noNeedsWork": true, "tasksResolved": true,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{}}}, nil)
	var pr pullRequest
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests", map[string]any{"title": "Login",
		"descriptionMarkdown": "Adds **login**.", "source": "feature/SHOP-1-login", "reviewers": []int64{adminUser.ID}}, &pr)
	if pr.Number != 1 || pr.State != "open" || len(pr.Reviewers) != 1 {
		t.Fatalf("pull request = %+v", pr)
	}
	if res := dev.Do(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests/1/merge", map[string]any{}); res.Status != http.StatusConflict ||
		!strings.Contains(string(res.Body), "0 of 1 required approval.") {
		t.Errorf("merge without approval: %d %s", res.Status, res.Body)
	}
	// Clients word the checks themselves, from their reasons and numbers.
	var checked struct {
		Checks []struct {
			Key    string         `json:"key"`
			OK     bool           `json:"ok"`
			Reason string         `json:"reason"`
			Values map[string]int `json:"values"`
		} `json:"checks"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/pull-requests/1", &checked)
	reasons := map[string]string{}
	for _, c := range checked.Checks {
		reasons[c.Key] = c.Reason
		if c.Key == "approvals" && (c.OK || c.Values["approved"] != 0 || c.Values["required"] != 1) {
			t.Errorf("approvals check = %+v", c)
		}
	}
	if reasons["conflicts"] != "clean" || reasons["approvals"] != "count" || reasons["needs-work"] != "none" || reasons["tasks"] != "resolved" {
		t.Errorf("reasons of the checks = %v", reasons)
	}
	var comment struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests/1/comments", map[string]any{
		"markdown": "Return something better", "path": "src/login.go", "line": 5, "side": "new", "task": true}, &comment)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/core-service/pull-requests/1/review", map[string]any{"state": "approved"}, nil)
	if res := dev.Do(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests/1/merge", map[string]any{}); res.Status != http.StatusConflict ||
		!strings.Contains(string(res.Body), "1 open task.") {
		t.Errorf("merge with an open task: %d %s", res.Status, res.Body)
	}
	dev.MustDo(http.MethodPut, apitest.Fmt("/api/v1/code/pull-request-comments/%d/resolved", comment.ID), nil, nil)

	// A second commit rescopes the pull request and marks the comment outdated.
	g.must(dir, "checkout", "feature/SHOP-1-login")
	write(t, filepath.Join(dir, "src", "login.go"), "package src\n\n// Login signs in.\nfunc Login() string {\n\treturn \"signed in\"\n}\n")
	g.must(dir, "commit", "-am", "Better result")
	g.must(dir, "push")
	env.WorkJobs()
	dev.Get("/api/v1/code/repos/PLAT/core-service/pull-requests/1", &pr)
	head := strings.TrimSpace(g.must(dir, "rev-parse", "HEAD"))
	if pr.SourceSHA != head {
		t.Errorf("source not rescoped: %s, want %s", pr.SourceSHA, head)
	}
	var timeline []struct {
		Kind    string `json:"kind"`
		Comment *struct {
			Outdated bool `json:"outdated"`
		} `json:"comment"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/pull-requests/1/timeline", &timeline)
	kinds := []string{}
	outdated := false
	for _, e := range timeline {
		kinds = append(kinds, e.Kind)
		if e.Comment != nil && e.Comment.Outdated {
			outdated = true
		}
	}
	if !strings.Contains(strings.Join(kinds, ","), "opened") || !strings.Contains(strings.Join(kinds, ","), "rescoped") || !outdated {
		t.Errorf("timeline = %v (outdated %v)", kinds, outdated)
	}
	var diff struct {
		Files []struct {
			NewPath string `json:"newPath"`
		} `json:"files"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/pull-requests/1/diff", &diff)
	if len(diff.Files) != 1 {
		t.Errorf("pull request diff = %+v", diff)
	}

	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests/1/merge", map[string]any{"strategy": "merge",
		"deleteSource": true}, &pr)
	if pr.State != "merged" || pr.MergeSHA == "" {
		t.Fatalf("merged pull request = %+v", pr)
	}
	g.must(dir, "fetch", "--prune")
	if got := strings.TrimSpace(g.must(dir, "rev-parse", "origin/main")); got != pr.MergeSHA {
		t.Errorf("main = %s, want the merge %s", got, pr.MergeSHA)
	}
	if out, err := g.run(dir, "rev-parse", "--verify", "origin/feature/SHOP-1-login"); err == nil {
		t.Errorf("the source branch was not deleted: %s", out)
	}
	if parents := strings.Fields(g.must(dir, "log", "-1", "--format=%P", "origin/main")); len(parents) != 2 {
		t.Errorf("merge commit parents = %v", parents)
	}

	// A pull request merged by a push is recognised.
	g.must(dir, "checkout", "-b", "fix/typo", "origin/main")
	write(t, filepath.Join(dir, "typo.txt"), "fixed\n")
	g.must(dir, "add", ".")
	g.must(dir, "commit", "-m", "Fix typo")
	g.must(dir, "push", "-u", "origin", "fix/typo")
	dev.MustDo(http.MethodPost, "/api/v1/code/repos/PLAT/core-service/pull-requests", map[string]any{"title": "Typo", "source": "fix/typo"}, &pr)
	g.must(dir, "checkout", "main")
	g.must(dir, "pull", "--ff-only")
	g.must(dir, "merge", "--ff-only", "fix/typo")
	g.must(dir, "push", "origin", "main")
	env.WorkJobs()
	dev.Get(apitest.Fmt("/api/v1/code/repos/PLAT/core-service/pull-requests/%d", pr.Number), &pr)
	if pr.State != "merged" {
		t.Errorf("pull request merged by a push = %s", pr.State)
	}

	// Git over SSH with a user key.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "dev")
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/ssh-keys", map[string]any{"name": "laptop",
		"key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, nil)
	srv, err := env.App.Code.NewSSHServer()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, l) }()
	port := l.Addr().(*net.TCPAddr).Port
	sshGit := newGit(t, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p %d",
		keyFile, port))
	sshDir := filepath.Join(t.TempDir(), "ssh")
	sshGit.must(filepath.Dir(sshDir), "clone", "git@127.0.0.1:plat/core-service.git", sshDir)
	write(t, filepath.Join(sshDir, "ssh.txt"), "over ssh\n")
	sshGit.must(sshDir, "add", ".")
	sshGit.must(sshDir, "commit", "-m", "Over SSH")
	sshGit.must(sshDir, "push", "origin", "HEAD:refs/heads/ssh-branch")
	var sshBranches struct {
		Total int `json:"total"`
	}
	dev.Get("/api/v1/code/repos/PLAT/core-service/branches?filter=ssh", &sshBranches)
	if sshBranches.Total != 1 {
		t.Errorf("the branch pushed over SSH is missing: %+v", sshBranches)
	}
	// Unknown keys are refused.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherBlock, _ := ssh.MarshalPrivateKey(other, "other")
	otherFile := filepath.Join(t.TempDir(), "id_other")
	_ = os.WriteFile(otherFile, pem.EncodeToMemory(otherBlock), 0o600)
	stranger := newGit(t, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes -p %d",
		otherFile, port))
	if out, err := stranger.run(t.TempDir(), "clone", "git@127.0.0.1:plat/core-service.git", "x"); err == nil {
		t.Errorf("clone with an unknown key: %s", out)
	}
}
