// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
)

func TestMain(m *testing.M) {
	// Pushes run the hooks, which start this test binary as `kogge hook …`.
	code.HookMain()
	os.Exit(m.Run())
}

// aSecond makes ordinary requests time out after a second.
func aSecond(c *config.Config) { c.RequestTimeout = time.Second }

// overHTTP2 serves env's handler over TLS with HTTP/2, as Kogge does with KOGGE_TLS_CERT_FILE.
func overHTTP2(t *testing.T, env *apitest.Env) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(env.App.Handler())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// signInAt signs in at a server with a client of its own and returns it with the session's CSRF token.
func signInAt(t *testing.T, base *http.Client, server, username, password string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: base.Transport, Jar: jar}
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	res, err := c.Post(server+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(res.Body).Decode(&me); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("sign-in at %s: %d %v", server, res.StatusCode, err)
	}
	return c, me.CSRFToken
}

// TestRouteLimits: every route outside the operations of the API names the limits of its kind of requests, so that
// none is forgotten; operations of the API are ordinary unless they name theirs.
func TestRouteLimits(t *testing.T) {
	env := apitest.New(t)
	listed := app.RouteLimits()
	operations := map[string]bool{}
	for path, item := range env.App.API().OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPost: item.Post,
			http.MethodPut: item.Put, http.MethodPatch: item.Patch, http.MethodDelete: item.Delete} {
			if op != nil {
				operations[method+" "+path] = true
			}
		}
	}
	routes := map[string]bool{}
	err := chi.Walk(env.App.Routes(), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodHead || method == http.MethodOptions || method == http.MethodConnect || method == http.MethodTrace {
			return nil
		}
		route := method + " " + pattern
		routes[route] = true
		if !operations[route] && listed[route] == "" {
			t.Errorf("%s names no limits: decide what kind of request it is (internal/app/limits.go)", route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for route := range listed {
		if !routes[route] {
			t.Errorf("limits for %s, which is not a route", route)
		}
	}
}

// trickle is a request body that sends a space every interval until it is closed.
type trickle struct {
	every  time.Duration
	closed chan struct{}
}

func newTrickle(every time.Duration) *trickle {
	return &trickle{every: every, closed: make(chan struct{})}
}

func (b *trickle) Read(p []byte) (int, error) {
	select {
	case <-b.closed:
		return 0, io.EOF
	case <-time.After(b.every):
	}
	p[0] = ' '
	return 1, nil
}

func (b *trickle) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

// slowPost posts a body that never ends and returns the answer's status, its protocol and how long it took.
func slowPost(t *testing.T, c *http.Client, url string) (int, int, time.Duration) {
	t.Helper()
	body := newTrickle(200 * time.Millisecond)
	defer body.Close()
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.Do(req.WithContext(ctx))
	took := time.Since(start)
	if err != nil {
		return 0, 0, took // the server closed the connection
	}
	res.Body.Close()
	return res.StatusCode, res.ProtoMajor, took
}

// TestSlowBodiesAreCutOff: a client that trickles its request body is cut off — over HTTP/1.1 and HTTP/2 — at
// KOGGE_REQUEST_TIMEOUT, on routes of their own and on the operations of the API, while uploads that make progress
// go on for as long as they take and stalled ones end at KOGGE_STALL_TIMEOUT.
func TestSlowBodiesAreCutOff(t *testing.T) {
	env := apitest.New(t, aSecond)
	h2 := overHTTP2(t, env)
	for _, s := range []struct {
		name   string
		client *http.Client
		url    string
		proto  int
	}{{"HTTP/1.1", http.DefaultClient, env.URL, 1}, {"HTTP/2", h2.Client(), h2.URL, 2}} {
		status, proto, took := slowPost(t, s.client, s.url+"/api/agent/v1/register")
		if status == http.StatusOK || took > 4*time.Second || status != 0 && proto != s.proto {
			t.Errorf("%s, a route of its own: %d (HTTP/%d) after %v", s.name, status, proto, took)
		}
		status, proto, took = slowPost(t, s.client, s.url+"/api/v1/auth/login")
		if status != http.StatusRequestTimeout || took > 8*time.Second || proto != s.proto {
			t.Errorf("%s, an operation of the API: %d (HTTP/%d) after %v", s.name, status, proto, took)
		}
	}

	// An upload that makes progress takes longer than an ordinary request may.
	admin := env.Admin()
	project := createProject(t, env, admin)
	upload := func(server string, client *http.Client, csrf string, pause time.Duration) (int, time.Duration) {
		t.Helper()
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			part, _ := mw.CreateFormFile("file", "notes.txt")
			for i := 0; i < 10; i++ {
				if _, err := part.Write(bytes.Repeat([]byte("progress "), 100)); err != nil {
					return
				}
				time.Sleep(pause)
			}
			_ = mw.Close()
			_ = pw.Close()
		}()
		req, _ := http.NewRequest(http.MethodPost, server+"/api/v1/issues/"+project+"-1/attachments", pr)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", server)
		start := time.Now()
		res, err := client.Do(req)
		took := time.Since(start)
		if err != nil {
			_ = pr.CloseWithError(err)
			return 0, took
		}
		res.Body.Close()
		return res.StatusCode, took
	}
	jar, csrf := signInAt(t, http.DefaultClient, env.URL, "admin", apitest.AdminPassword)
	if status, took := upload(env.URL, jar, csrf, 300*time.Millisecond); status != http.StatusCreated || took < 2*time.Second {
		t.Errorf("an upload that makes progress: %d after %v", status, took)
	}

	// An upload that stalls ends at the stall limit.
	stalling := apitest.New(t, func(c *config.Config) { c.StallTimeout = time.Second })
	project = createProject(t, stalling, stalling.Admin())
	jar, csrf = signInAt(t, http.DefaultClient, stalling.URL, "admin", apitest.AdminPassword)
	if status, took := upload(stalling.URL, jar, csrf, 3*time.Second); status == http.StatusCreated || took > 5*time.Second {
		t.Errorf("an upload that stalls: %d after %v", status, took)
	}
}

// createProject creates a project with one issue and returns its key.
func createProject(t *testing.T, env *apitest.Env, admin *apitest.Client) string {
	t.Helper()
	var cfg struct {
		IssueTypes []struct {
			ID   int64  `json:"id"`
			Icon string `json:"icon"`
		} `json:"issueTypes"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "LIM", "name": "Limits"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "LIM", "typeId": cfg.IssueTypes[2].ID,
		"summary": "Attachments"}, nil)
	return "LIM"
}

// TestRequestsStopAtTheirTimeLimit: an ordinary request that does not end within KOGGE_REQUEST_TIMEOUT is
// stopped — its database query canceled — and answers request.timeout (504), in the client's language.
func TestRequestsStopAtTheirTimeLimit(t *testing.T) {
	env := apitest.New(t, aSecond)
	lena := env.CreateUser("lena")
	ctx := context.Background()
	tx, err := env.App.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Hold lena's row: changing her profile waits for it.
	if _, err := tx.Exec(ctx, "SELECT id FROM users WHERE username = 'lena' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := lena.Do(http.MethodPatch, "/api/v1/me", map[string]any{"theme": "dark"}, "Accept-Language", "de")
	took := time.Since(start)
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(r.Body, &p)
	if r.Status != http.StatusGatewayTimeout || p.Code != "request.timeout" || !strings.HasPrefix(p.Detail, "Die Anfrage hat zu lange gedauert") ||
		took < time.Second || took > 5*time.Second {
		t.Errorf("a request held up: %d %s after %v", r.Status, r.Body, took)
	}
	_ = tx.Rollback(ctx)
	lena.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"theme": "dark"}, nil)
}

// TestLongPollOutlastsTheRequestTimeout: an agent's request for work waits the 25 seconds of its long poll, longer
// than ordinary requests may take.
func TestLongPollOutlastsTheRequestTimeout(t *testing.T) {
	env := apitest.New(t, aSecond)
	var reg struct {
		Token string `json:"token"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{"description": "poll", "autoApprove": true}, &reg)
	cfg := &agent.Config{Server: env.URL, Name: "poller", WorkDir: t.TempDir(), Shell: true}
	if err := agent.Register(context.Background(), cfg, reg.Token, "test"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, env.URL+"/api/agent/v1/jobs/claim", strings.NewReader(`{"labels":["linux"]}`))
	req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if took := time.Since(start); res.StatusCode != http.StatusNoContent || took < 20*time.Second {
		t.Errorf("the long poll: %d after %v", res.StatusCode, took)
	}
}

// TestEventStreamsOutlastTheRequestTimeout: the live updates stay open longer than ordinary requests may take and
// carry the messages that come later — over HTTP/1.1 and HTTP/2.
func TestEventStreamsOutlastTheRequestTimeout(t *testing.T) {
	env := apitest.New(t, aSecond)
	lena := env.CreateUser("lena")
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "space.create",
		"principalType": "user", "principalId": userIDOf(t, lena)}, nil)
	var sp struct {
		HomePageID int64 `json:"homePageId"`
	}
	lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "LIMW", "name": "Limits"}, &sp)
	page := apitest.Fmt("%d", sp.HomePageID)

	h1 := liveStream(t, env, lena)
	h2 := overHTTP2(t, env)
	c, _ := signInAt(t, h2.Client(), h2.URL, "lena", apitest.UserPassword)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h2.URL+"/api/v1/live", nil)
	res, err := c.Do(req)
	if err != nil || res.StatusCode != http.StatusOK || res.ProtoMajor != 2 {
		t.Fatalf("live over HTTP/2: %v %v", res, err)
	}
	h2stream := make(chan map[string]any, 100)
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				if json.Unmarshal([]byte(data), &m) == nil {
					h2stream <- m
				}
			}
		}
	}()

	time.Sleep(2500 * time.Millisecond) // longer than an ordinary request may take
	lena.MustDo(http.MethodPut, "/api/v1/wiki/pages/"+page+"/labels", []string{"later"}, nil)
	env.WorkJobs()
	for name, stream := range map[string]<-chan map[string]any{"HTTP/1.1": h1, "HTTP/2": h2stream} {
		if got := waitFor(stream, "page", page, 10*time.Second); !contains(got, page) {
			t.Errorf("%s: the stream carried %v", name, got)
		}
	}
}

func userIDOf(t *testing.T, c *apitest.Client) int64 {
	t.Helper()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	c.Get("/api/v1/me", &me)
	return me.User.ID
}

// slowLink forwards connections to target at about bytesPerSecond each way, like a slow network, and returns its
// address.
func slowLink(t *testing.T, target string, bytesPerSecond int) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	pipe := func(dst, src net.Conn, done chan<- struct{}) {
		defer func() { done <- struct{}{} }()
		lim := rate.NewLimiter(rate.Limit(bytesPerSecond), 32<<10)
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				_ = lim.WaitN(context.Background(), n)
				if _, err := dst.Write(buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				if tc, ok := dst.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
				return
			}
		}
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer s.Close()
				done := make(chan struct{}, 2)
				go pipe(s, c, done)
				go pipe(c, s, done)
				<-done
				<-done
			}()
		}
	}()
	return l.Addr().String()
}

// TestLargeGitPushOutlastsTheRequestTimeout: Git over HTTP pushes and clones a large repository over a slow link,
// for much longer than ordinary requests may take.
func TestLargeGitPushOutlastsTheRequestTimeout(t *testing.T) {
	env := apitest.New(t, aSecond)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "BIG", "name": "Big"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/BIG/repos", map[string]any{"name": "assets"}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	server, _ := url.Parse(env.URL)
	u, _ := url.Parse(repo.CloneHTTP)
	u.Host = slowLink(t, server.Host, 8<<20) // 8 MB/s: the push of 24 MB takes three seconds at least
	u.User = url.UserPassword("admin", token.Token)

	home, work := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Dev", "-c", "user.email=dev@example.com",
			"-c", "init.defaultBranch=main", "-c", "core.compression=0"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	data := make([]byte, 24<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "assets.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "init")
	git(work, "add", "assets.bin")
	git(work, "commit", "-m", "Assets")
	start := time.Now()
	git(work, "push", u.String(), "main")
	if took := time.Since(start); took < 2*time.Second {
		t.Fatalf("the push took %v: too fast to show anything", took)
	}
	clone := t.TempDir()
	git(clone, "clone", u.String(), "copy")
	got, err := os.ReadFile(filepath.Join(clone, "copy", "assets.bin"))
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("the clone differs: %v", err)
	}
}
