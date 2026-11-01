// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/apitest"
)

// setLimits changes limits of the settings (Administration → Settings → Limits).
func setLimits(t *testing.T, admin *apitest.Client, limits map[string]any) {
	t.Helper()
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	for k, v := range limits {
		st[k] = v
	}
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
}

// limitsRepo is a repository of the tests of limits with a clone of it.
type limitsRepo struct {
	repository
	g   *gitClient
	dir string
	url string // with credentials
}

// newLimitsRepo creates a repository with a README in the code project LIM (created if need be) and clones it with
// the client's access token.
func newLimitsRepo(t *testing.T, c *apitest.Client, slug string) *limitsRepo {
	t.Helper()
	if r := c.Do(http.MethodGet, "/api/v1/code/projects/LIM", nil); r.Status == http.StatusNotFound {
		c.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LIM", "name": "Limits"}, nil)
	}
	var r limitsRepo
	c.MustDo(http.MethodPost, "/api/v1/code/projects/LIM/repos", map[string]any{"name": slug, "readme": true}, &r.repository)
	var token struct {
		Token string `json:"token"`
	}
	c.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git " + slug, "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(r.CloneHTTP)
	u.User = url.UserPassword(c.Username, token.Token)
	r.url = u.String()
	r.g = newGit(t)
	work := t.TempDir()
	r.g.must(work, "clone", r.url, slug)
	r.dir = filepath.Join(work, slug)
	return &r
}

// commitFile commits a file of size bytes: random (incompressible) or zeros (compressing to almost nothing).
func (r *limitsRepo) commitFile(t *testing.T, path string, size int, random bool) {
	t.Helper()
	data := make([]byte, size)
	if random {
		_, _ = rand.Read(data)
	}
	full := filepath.Join(r.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r.g.must(r.dir, "add", ".")
	r.g.must(r.dir, "commit", "-m", "Add "+path)
}

// sshGit returns a Git client that reaches the repository over SSH with a new key of the client's user.
func sshGit(t *testing.T, env *apitest.Env, c *apitest.Client) *gitClient {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, c.Username)
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	c.MustDo(http.MethodPost, "/api/v1/me/ssh-keys", map[string]any{"name": "laptop", "key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, nil)
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
	return newGit(t, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p %d",
		keyFile, l.Addr().(*net.TCPAddr).Port))
}

// TestPushSizeLimit: git itself stops a push larger than the maximum push size, over HTTPS and SSH, and the client
// hears why — a refused push, not a broken connection; a push under the limit passes.
func TestPushSizeLimit(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	setLimits(t, admin, map[string]any{"maxPushMb": 1})
	r := newLimitsRepo(t, admin, "big")
	// 4 MiB of random data do not compress below the limit: more than git reads before it stops, and more than the
	// server reads of the request, so that the client still sends when git answers.
	r.commitFile(t, "big.bin", 4<<20, true)
	refused := func(how, out string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(out, "pack exceeds maximum allowed size") || !strings.Contains(out, "[remote rejected] main -> main") ||
			strings.Contains(out, "Everything up-to-date") || strings.Contains(out, "RPC failed") {
			t.Errorf("push over the limit %s: %v\n%s", how, err, out)
		}
	}
	out, err := r.g.run(r.dir, "push", "origin", "main")
	refused("over HTTPS", out, err)
	ssh := sshGit(t, env, admin)
	ssh.must(r.dir, "remote", "add", "ssh", "git@127.0.0.1:lim/big.git")
	out, err = ssh.run(r.dir, "push", "ssh", "main")
	refused("over SSH", out, err)

	// Nothing of it arrived; a push under the limit passes.
	r.g.must(r.dir, "reset", "--hard", "HEAD~1")
	r.commitFile(t, "small.bin", 100<<10, true)
	r.g.must(r.dir, "push", "origin", "main")
	if out := r.g.must(r.dir, "ls-remote", "origin", "main"); !strings.Contains(out, strings.TrimSpace(r.g.must(r.dir, "rev-parse", "HEAD"))) {
		t.Errorf("the small push did not arrive: %s", out)
	}

	// A request that announces a body over the limit is refused before git sees it.
	req, _ := http.NewRequest(http.MethodPost, strings.TrimSuffix(r.CloneHTTP, ".git")+".git/git-receive-pack", strings.NewReader(""))
	req.ContentLength = 40 << 20
	req.Body = io.NopCloser(io.LimitReader(zeros{}, 40<<20))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.Header.Set("Expect", "100-continue")
	u, _ := url.Parse(r.url)
	pass, _ := u.User.Password()
	req.SetBasicAuth("admin", pass)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "larger than the 1 MiB") {
		t.Errorf("announced body over the limit: %d %s", res.StatusCode, body)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type repoSize struct {
	ID      int64  `json:"id"`
	Size    int64  `json:"size"`
	LFSSize int64  `json:"lfsSize"`
	Quota   *int64 `json:"quota"`
}

// TestRepositoryQuota: a push that would take a repository over its quota is refused, and says by how much; pushes
// that bring nothing (deleting branches) pass over the quota, and the space of what they removed is freed. Instance
// administrators give single repositories a quota of their own, and see the repositories by size.
func TestRepositoryQuota(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t)
	admin := env.Admin()
	dev := env.CreateUser("dev")
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LIM", "name": "Limits"}, nil)
	var devMe, adminMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	dev.Get("/api/v1/me", &devMe)
	admin.Get("/api/v1/me", &adminMe)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/LIM/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": adminMe.User.ID}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": devMe.User.ID}},
	}, nil)
	r := newLimitsRepo(t, admin, "app")
	small := newLimitsRepo(t, admin, "small")
	// dev pushes to app with their own token.
	var token struct {
		Token string `json:"token"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(r.CloneHTTP)
	u.User = url.UserPassword("dev", token.Token)
	r.g.must(r.dir, "remote", "set-url", "origin", u.String())

	// Before a quota: an old branch with a large file, and main.
	r.g.must(r.dir, "checkout", "-b", "old")
	r.commitFile(t, "old/archive.bin", 2<<20, true)
	r.g.must(r.dir, "push", "origin", "old")
	r.g.must(r.dir, "checkout", "main")
	r.commitFile(t, "src/a.bin", 200<<10, true)
	r.g.must(r.dir, "push", "origin", "main")
	env.WorkJobs()
	var got repoSize
	dev.Get("/api/v1/code/repos/LIM/app", &got)
	if got.Size < 2<<20 || got.LFSSize != 0 || got.Quota == nil || *got.Quota != 0 {
		t.Fatalf("size before a quota = %+v", got)
	}

	// A quota of 2 MiB, which the repository has used up already: a push of 1 MiB more does not fit.
	setLimits(t, admin, map[string]any{"repositoryQuotaMb": 2})
	dev.Get("/api/v1/code/repos/LIM/app", &got)
	if *got.Quota != 2<<20 {
		t.Errorf("quota = %d", *got.Quota)
	}
	r.commitFile(t, "src/b.bin", 1<<20, true)
	out, err := r.g.run(r.dir, "push", "origin", "main")
	if err == nil || !strings.Contains(out, "over its quota of 2 MiB") || !strings.Contains(out, "the push brings") ||
		!strings.Contains(out, "deleting branches and tags") {
		t.Fatalf("push over the quota: %v\n%s", err, out)
	}
	// Deleting a branch brings nothing and passes; the space of what nothing refers to any more is freed (objects of
	// the last hour stay, so the test ages them).
	r.g.must(r.dir, "push", "origin", "--delete", "old")
	old := time.Now().Add(-2 * time.Hour)
	_ = filepath.WalkDir(filepath.Join(code.RepoDir(env.App.Cfg.DataDir, got.ID), "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})
	before := got.Size
	env.WorkJobs()
	dev.Get("/api/v1/code/repos/LIM/app", &got)
	if got.Size > before-(1<<20) {
		t.Errorf("deleting the branch over the quota freed nothing: %d before, %d after", before, got.Size)
	}
	// Now the push fits.
	r.g.must(r.dir, "push", "origin", "main")
	env.WorkJobs()

	// A quota of its own, which only administrators of Kogge set.
	path := apitest.Fmt("/api/v1/admin/repositories/%d/quota", got.ID)
	if res := dev.Do(http.MethodPut, path, map[string]any{"quotaMb": 100}); res.Status != http.StatusForbidden {
		t.Errorf("a developer set the quota: %d %s", res.Status, res.Body)
	}
	if res := dev.Do(http.MethodGet, "/api/v1/admin/repositories", nil); res.Status != http.StatusForbidden {
		t.Errorf("a developer listed the repositories by size: %d", res.Status)
	}
	r.commitFile(t, "src/c.bin", 2<<20, true)
	if out, err := r.g.run(r.dir, "push", "origin", "main"); err == nil {
		t.Fatalf("push over the quota: %s", out)
	}
	var st struct {
		ID      int64 `json:"id"`
		Size    int64 `json:"size"`
		Quota   int64 `json:"quota"`
		QuotaMB *int  `json:"quotaMb"`
	}
	admin.MustDo(http.MethodPut, path, map[string]any{"quotaMb": 100}, &st)
	if st.Quota != 100<<20 || st.QuotaMB == nil || *st.QuotaMB != 100 {
		t.Errorf("own quota = %+v", st)
	}
	r.g.must(r.dir, "push", "origin", "main")
	env.WorkJobs()
	var sizes struct {
		Items []struct {
			Slug    string `json:"slug"`
			Size    int64  `json:"size"`
			Quota   int64  `json:"quota"`
			QuotaMB *int   `json:"quotaMb"`
		} `json:"items"`
		Total int `json:"total"`
	}
	admin.Get("/api/v1/admin/repositories", &sizes)
	if sizes.Total != 2 || len(sizes.Items) != 2 || sizes.Items[0].Slug != "app" || sizes.Items[1].Slug != "small" ||
		sizes.Items[0].Size <= sizes.Items[1].Size || sizes.Items[0].QuotaMB == nil || sizes.Items[1].QuotaMB != nil ||
		sizes.Items[1].Quota != 2<<20 {
		t.Errorf("repositories by size = %+v", sizes)
	}
	// No quota at all, and back to the quota of the settings.
	admin.MustDo(http.MethodPut, path, map[string]any{"quotaMb": 0}, &st)
	if st.Quota != 0 || st.QuotaMB == nil || *st.QuotaMB != 0 {
		t.Errorf("no quota = %+v", st)
	}
	st.QuotaMB = nil
	admin.MustDo(http.MethodPut, path, map[string]any{}, &st)
	if st.Quota != 2<<20 || st.QuotaMB != nil {
		t.Errorf("the quota of the settings = %+v", st)
	}
	var audited int
	if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action = 'repository.quota' AND target_id = 'LIM/app'").
		Scan(&audited); err != nil || audited != 3 {
		t.Errorf("audited quota changes: %d %v", audited, err)
	}

	// Files edited in the browser count, too.
	setLimits(t, admin, map[string]any{"repositoryQuotaMb": 1})
	res := dev.Do(http.MethodPut, "/api/v1/code/repos/LIM/app/files", map[string]any{"branch": "main", "path": "note.txt", "content": "note\n"})
	if res.Status != http.StatusInsufficientStorage || res.Code() != "repository.quota-exceeded" {
		t.Errorf("an edit over the quota: %d %s", res.Status, res.Body)
	}
	// The other repository is under it.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LIM/small/files", map[string]any{"branch": "main", "path": "note.txt", "content": "note\n"}, nil)
	_ = small
}

// TestLargeFiles: with a maximum size of files without LFS, a push that brings a larger file is refused with its path
// and a hint to Git LFS — also a file that compresses to almost nothing; files the repository has already pass.
func TestLargeFiles(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	r := newLimitsRepo(t, admin, "media")
	// Before the limit: a large file in the history.
	r.commitFile(t, "legacy/video.bin", 3<<20, true)
	r.g.must(r.dir, "push", "origin", "main")

	setLimits(t, admin, map[string]any{"maxGitFileMb": 1})
	r.commitFile(t, "docs/readme.txt", 10, true)
	r.commitFile(t, "data/zeros.bin", 3<<20, false)
	r.commitFile(t, "data/random.bin", 2<<20, true)
	r.commitFile(t, "data/fine.bin", 512<<10, true)
	out, err := r.g.run(r.dir, "push", "origin", "main")
	for _, want := range []string{"data/random.bin is 2 MiB: files larger than 1 MiB must be stored with Git LFS.",
		"data/zeros.bin is 3 MiB: files larger than 1 MiB must be stored with Git LFS.", "git lfs migrate import"} {
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("push with large files: %v, no %q in\n%s", err, want, out)
		}
	}
	if strings.Contains(out, "legacy/video.bin") || strings.Contains(out, "data/fine.bin") {
		t.Errorf("files that may be pushed are named:\n%s", out)
	}
	// Without the large files, the push passes; the large file the repository has does not count.
	r.g.must(r.dir, "reset", "--hard", "HEAD~4")
	r.commitFile(t, "data/fine.bin", 512<<10, true)
	write(t, filepath.Join(r.dir, "legacy", "notes.txt"), "the video stays\n")
	r.g.must(r.dir, "add", ".")
	r.g.must(r.dir, "commit", "-m", "Notes")
	r.g.must(r.dir, "push", "origin", "main")

	// Files edited in the browser, too.
	res := admin.Do(http.MethodPut, "/api/v1/code/repos/LIM/media/files", map[string]any{"branch": "main", "path": "big.txt",
		"content": strings.Repeat("x", 1<<20+1)})
	if res.Status != http.StatusRequestEntityTooLarge || res.Code() != "file.too-large" {
		t.Errorf("a large file edited in the browser: %d %s", res.Status, res.Body)
	}
}

// TestLFSLimits: LFS objects over the maximum size are refused in the batch answer and by the upload before its
// content is read; LFS objects count towards the quota of the repository.
func TestLFSLimits(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	r := newLimitsRepo(t, admin, "lfs")
	pass, _ := func() (string, bool) { u, _ := url.Parse(r.url); return u.User.Password() }()
	base := strings.TrimSuffix(r.CloneHTTP, ".git") + ".git/info/lfs/objects/"
	type object struct {
		oid  string
		data []byte
	}
	newObject := func(size int) object {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		sum := sha256.Sum256(data)
		return object{hex.EncodeToString(sum[:]), data}
	}
	batch := func(objects ...object) []int {
		t.Helper()
		var list []map[string]any
		for _, o := range objects {
			list = append(list, map[string]any{"oid": o.oid, "size": len(o.data)})
		}
		body, _ := json.Marshal(map[string]any{"operation": "upload", "transfers": []string{"basic"}, "objects": list})
		req, _ := http.NewRequest(http.MethodPost, base+"batch", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.SetBasicAuth("admin", pass)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b struct {
			Objects []struct {
				Actions map[string]any `json:"actions"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"objects"`
		}
		if err := json.NewDecoder(res.Body).Decode(&b); err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("batch: %d %v", res.StatusCode, err)
		}
		codes := []int{}
		for _, o := range b.Objects {
			switch {
			case o.Error != nil:
				codes = append(codes, o.Error.Code)
			case o.Actions["upload"] != nil:
				codes = append(codes, http.StatusOK)
			default:
				codes = append(codes, 0)
			}
		}
		return codes
	}
	put := func(o object, body io.Reader, length int64) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, base+o.oid, body)
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/octet-stream")
		// The answer comes before the content is sent: refused uploads cost nothing.
		req.Header.Set("Expect", "100-continue")
		req.SetBasicAuth("admin", pass)
		client := &http.Client{Timeout: 10 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	setLimits(t, admin, map[string]any{"maxLfsObjectMb": 1})
	big, fits := newObject(2<<20), newObject(300<<10)
	if codes := batch(big, fits); fmt.Sprint(codes) != "[413 200]" {
		t.Errorf("batch with an object over the limit: %v", codes)
	}
	// The upload is refused by its announced length: the content, which never comes, is not waited for.
	never, _ := io.Pipe()
	if status, body := put(big, never, int64(len(big.data))); status != http.StatusRequestEntityTooLarge ||
		!strings.Contains(body, "larger than the 1 MiB") {
		t.Errorf("upload over the limit: %d %s", status, body)
	}
	if status, body := put(fits, bytes.NewReader(fits.data), int64(len(fits.data))); status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, body)
	}
	var got repoSize
	admin.Get("/api/v1/code/repos/LIM/lfs", &got)
	if got.LFSSize != int64(len(fits.data)) {
		t.Errorf("LFS size = %d, want %d", got.LFSSize, len(fits.data))
	}

	// With a quota, LFS objects count towards it, in the order of the batch.
	setLimits(t, admin, map[string]any{"maxLfsObjectMb": 100, "repositoryQuotaMb": 1})
	next, more := newObject(400<<10), newObject(600<<10)
	if codes := batch(next, more, fits); fmt.Sprint(codes) != "[200 507 0]" {
		t.Errorf("batch over the quota: %v", codes)
	}
	if status, body := put(next, bytes.NewReader(next.data), int64(len(next.data))); status != http.StatusOK {
		t.Fatalf("upload under the quota: %d %s", status, body)
	}
	if status, body := put(more, bytes.NewReader(more.data), int64(len(more.data))); status != http.StatusInsufficientStorage ||
		!strings.Contains(body, "does not fit into the quota of the repository") || !strings.Contains(body, "of 1 MiB") {
		t.Errorf("upload over the quota: %d %s", status, body)
	}

	// No uploads while the disk is low.
	restore := storage.Fake(1<<30, 100<<30)
	defer restore()
	other := newObject(10 << 10)
	if codes := batch(other); fmt.Sprint(codes) != "[507]" {
		t.Errorf("batch on a low disk: %v", codes)
	}
	if status, _ := put(other, bytes.NewReader(other.data), int64(len(other.data))); status != http.StatusInsufficientStorage {
		t.Errorf("upload on a low disk: %d", status)
	}
}

// TestLowSpace: below the free space the settings keep, pushes are refused before they start, over HTTPS and SSH,
// with a message the Git client shows; so are edits in the browser.
func TestLowSpace(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	r := newLimitsRepo(t, admin, "space")
	ssh := sshGit(t, env, admin)
	ssh.must(r.dir, "remote", "add", "ssh", "git@127.0.0.1:lim/space.git")
	r.commitFile(t, "a.txt", 100, true)

	// 1 GiB free of 100 GiB: below the 2 GiB kept free by default.
	restore := storage.Fake(1<<30, 100<<30)
	for name, g := range map[string]*gitClient{"origin": r.g, "ssh": ssh} {
		out, err := g.run(r.dir, "push", name, "main")
		if err == nil || !strings.Contains(out, "remote error: The server is running out of disk space") {
			t.Errorf("push to %s on a low disk: %v\n%s", name, err, out)
		}
	}
	res := admin.Do(http.MethodPut, "/api/v1/code/repos/LIM/space/files", map[string]any{"branch": "main", "path": "b.txt", "content": "b\n"})
	if res.Status != http.StatusInsufficientStorage || res.Code() != "storage.low" {
		t.Errorf("edit on a low disk: %d %s", res.Status, res.Body)
	}
	// The administrators keep less free: the push passes.
	setLimits(t, admin, map[string]any{"minFreeSpaceMb": 512, "minFreeSpacePercent": 0})
	r.g.must(r.dir, "push", "origin", "main")
	restore()
}

// TestImportLimits: an import stops when the repository grows over its quota, and does not start on a low disk; the
// import says why.
func TestImportLimits(t *testing.T) {
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	source := newLimitsRepo(t, admin, "source")
	source.commitFile(t, "data.bin", 3<<20, true)
	source.g.must(source.dir, "push", "origin", "main")
	pass, _ := func() (string, bool) { u, _ := url.Parse(source.url); return u.User.Password() }()
	importInto := func(slug string) repository {
		t.Helper()
		var r repository
		admin.MustDo(http.MethodPost, "/api/v1/code/projects/LIM/repos", map[string]any{"name": slug, "importUrl": source.CloneHTTP,
			"importUsername": "admin", "importPassword": pass}, &r)
		env.WorkJobs()
		admin.Get("/api/v1/code/repos/LIM/"+slug, &r)
		return r
	}

	setLimits(t, admin, map[string]any{"repositoryQuotaMb": 1})
	r := importInto("copy")
	if r.Import == nil || r.Import.State != "failed" || !strings.Contains(r.Import.Error, "larger than its quota of 1 MiB") {
		t.Errorf("import over the quota: %+v", r.Import)
	}
	setLimits(t, admin, map[string]any{"repositoryQuotaMb": 0})
	restore := storage.Fake(1<<30, 100<<30)
	r = importInto("second")
	restore()
	if r.Import == nil || r.Import.State != "failed" || !strings.Contains(r.Import.Error, "running out of disk space") {
		t.Errorf("import on a low disk: %+v", r.Import)
	}
	// With room, the import goes through.
	if r = importInto("third"); r.Import == nil || r.Import.State != "done" {
		t.Errorf("import: %+v", r.Import)
	}
}
