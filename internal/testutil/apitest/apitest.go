// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package apitest starts a complete Kogge server on a fresh database for API tests and provides HTTP clients
// that sign in like the web application (session cookie and CSRF token) or use access tokens.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// AdminPassword is the password of the bootstrap administrator "admin".
const AdminPassword = "Test-Root-Secret-1"

// UserPassword is the password of users created with CreateUser.
const UserPassword = "Test-User-Secret-1"

// Env is a running test server.
type Env struct {
	T      testing.TB
	App    *app.App
	Server *httptest.Server
	URL    string
	// origin is the installation's public address (KOGGE_PUBLIC_URL), which clients send as their Origin: the URL
	// of the first node, which the other nodes of the installation share (Node).
	origin string
}

// Option adjusts the configuration.
type Option func(*config.Config)

// New starts a server on a new database. It is skipped when no database is available.
func New(t testing.TB, opts ...Option) *Env {
	t.Helper()
	dbURL := pgtest.New(t)
	// The limits of the free space see a large, empty disk, whatever the machine's disk holds (tests of a full disk
	// make one with storage.Fake).
	t.Cleanup(storage.Fake(1<<40, 2<<40))
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	t.Cleanup(srv.Close)
	dataDir := t.TempDir()
	keyFile := dataDir + "/master.key"
	if err := os.WriteFile(keyFile, []byte(crypto.GenerateKey()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"KOGGE_DATABASE_URL":             dbURL,
		"KOGGE_DATA_DIR":                 dataDir,
		"KOGGE_MASTER_KEY_FILE":          keyFile,
		"KOGGE_PUBLIC_URL":               srv.URL,
		"KOGGE_ROLES":                    "web",
		"KOGGE_BOOTSTRAP_ADMIN_USERNAME": "admin",
		"KOGGE_BOOTSTRAP_ADMIN_PASSWORD": AdminPassword,
		"KOGGE_SSH_ADDR":                 "",
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	for _, o := range opts {
		o(cfg)
	}
	a, ctx := start(t, srv, cfg)
	e := &Env{T: t, App: a, Server: srv, URL: srv.URL, origin: srv.URL}
	if _, err := ensureAdmin(ctx, a, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return e
}

// start runs an application with its server, as `kogge serve` does with the role web.
func start(t testing.TB, srv *httptest.Server, cfg *config.Config) (*app.App, context.Context) {
	t.Helper()
	level := slog.LevelWarn
	if os.Getenv("KOGGE_TEST_LOG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, cfg, logger, app.Options{Argon2: &crypto.Argon2Params{Memory: 1024, Time: 1, Threads: 1}})
	if err != nil {
		cancel()
		t.Fatalf("app: %v", err)
	}
	// Cleanups run in reverse order: stop the background goroutines before the pool closes.
	t.Cleanup(a.Close)
	t.Cleanup(cancel)
	srv.Config.Handler = a.Handler()
	go a.Live.Run(ctx)
	// Tests that hear live messages (shared drafts, event streams) start once this node listens for them: what is
	// committed before reaches its subscribers only through the catching up that follows.
	select {
	case <-a.Live.Listening():
	case <-time.After(30 * time.Second):
		t.Fatal("the live hub does not listen for notifications")
	}
	return a, ctx
}

// Node starts another node of e's installation, as behind a load balancer: a server of its own on
// the same database, data directory and master key, with the configuration and the public address of e, listening
// for the notifications of the other nodes. Its clients (Login, Admin, Via) send their requests to it.
func (e *Env) Node(opts ...Option) *Env {
	e.T.Helper()
	cfg := *e.App.Cfg
	for _, o := range opts {
		o(&cfg)
	}
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	e.T.Cleanup(srv.Close)
	a, _ := start(e.T, srv, &cfg)
	return &Env{T: e.T, App: a, Server: srv, URL: srv.URL, origin: e.origin}
}

func ensureAdmin(ctx context.Context, a *app.App, cfg *config.Config) (bool, error) {
	return accountBootstrap(ctx, a, cfg)
}

// WorkJobs executes all jobs that are due now (and the jobs they enqueue) and returns when the queue is idle.
func (e *Env) WorkJobs() {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Jobs that jobs enqueue are fetched at once, not after River's poll interval of a second.
	client, err := jobs.NewClient(e.App.Pool, e.App.Registry, true, slog.New(slog.DiscardHandler), func(c *river.Config) {
		c.FetchCooldown, c.FetchPollInterval = 5*time.Millisecond, 20*time.Millisecond
	})
	if err != nil {
		e.T.Fatalf("job client: %v", err)
	}
	if err := client.Start(ctx); err != nil {
		e.T.Fatalf("start jobs: %v", err)
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = client.Stop(stopCtx)
	}()
	idleSince := time.Time{}
	for ctx.Err() == nil {
		var pending int
		if err := e.App.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state IN ('available', 'running')
			OR (state IN ('scheduled', 'retryable') AND scheduled_at <= now() + interval '1 second')`).Scan(&pending); err != nil {
			e.T.Fatalf("job queue: %v", err)
		}
		if pending == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) > 150*time.Millisecond {
				return
			}
		} else {
			idleSince = time.Time{}
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.T.Fatalf("jobs did not finish in time")
}

// Client is an HTTP client of the test server.
type Client struct {
	env   *Env
	http  *http.Client
	csrf  string
	token string
	// Username is set for signed-in clients.
	Username string
}

// Anonymous returns a client without credentials.
func (e *Env) Anonymous() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{env: e, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

// Login signs in with a local password like the web application.
func (e *Env) Login(username, password string) *Client {
	e.T.Helper()
	return e.LoginWithCode(username, password, "")
}

// LoginWithCode signs in with a local password and, for accounts with two-factor authentication, a code.
func (e *Env) LoginWithCode(username, password, code string) *Client {
	e.T.Helper()
	c := e.Anonymous()
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	body := map[string]string{"username": username, "password": password}
	if code != "" {
		body["code"] = code
	}
	r := c.Do(http.MethodPost, "/api/v1/auth/login", body)
	if r.Status != http.StatusOK {
		e.T.Fatalf("login %s: %d %s", username, r.Status, r.Body)
	}
	r.Decode(&me)
	c.csrf = me.CSRFToken
	c.Username = username
	return c
}

// Admin returns a client signed in as the bootstrap administrator.
func (e *Env) Admin() *Client { return e.Login("admin", AdminPassword) }

// CreateUser creates a local user (as the administrator) and returns a signed-in client.
func (e *Env) CreateUser(username string) *Client {
	e.T.Helper()
	e.Admin().MustDo(http.MethodPost, "/api/v1/admin/users", map[string]string{"username": username,
		"displayName": strings.ToUpper(username[:1]) + username[1:], "password": UserPassword, "email": username + "@example.com"}, nil)
	return e.Login(username, UserPassword)
}

// WithToken returns a client that authenticates with an access token.
func (e *Env) WithToken(token string) *Client {
	c := e.Anonymous()
	c.token = token
	return c
}

// Response is an HTTP response with its body read.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	t      testing.TB
}

// Decode unmarshals the JSON body.
func (r *Response) Decode(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// Code returns the problem code of an error response.
func (r *Response) Code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

// Do sends a request; body is encoded as JSON unless it is an io.Reader.
func (c *Client) Do(method, path string, body any, headers ...string) *Response {
	c.env.T.Helper()
	var rd io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rd = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			c.env.T.Fatalf("encode: %v", err)
		}
		rd, contentType = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequest(method, c.env.URL+path, rd)
	if err != nil {
		c.env.T.Fatalf("request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Origin", c.env.origin)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.env.T.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data, t: c.env.T}
}

// Via returns a client with the same credentials (session, CSRF token or access token) that sends its requests to
// another node of the installation, as a load balancer may.
func (c *Client) Via(node *Env) *Client {
	return &Client{env: node, http: c.http, csrf: c.csrf, token: c.token, Username: c.Username}
}

// Cookies returns the client's cookies for the server (its session), e.g. for WebSocket handshakes.
func (c *Client) Cookies() []*http.Cookie {
	u, err := url.Parse(c.env.URL)
	if err != nil || c.http.Jar == nil {
		return nil
	}
	return c.http.Jar.Cookies(u)
}

// Send sends a prepared request with the client's credentials and returns the response with its body unread, for
// streams and requests with a deadline of their own.
func (c *Client) Send(req *http.Request) (*http.Response, error) {
	req.Header.Set("Origin", c.env.origin)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// MustDo sends a request (with optional header name/value pairs), fails the test unless the status is 2xx, and
// decodes the body into out (if not nil).
func (c *Client) MustDo(method, path string, body, out any, headers ...string) *Response {
	c.env.T.Helper()
	r := c.Do(method, path, body, headers...)
	if r.Status < 200 || r.Status > 299 {
		c.env.T.Fatalf("%s %s: status %d: %s", method, path, r.Status, r.Body)
	}
	if out != nil {
		r.Decode(out)
	}
	return r
}

// Get is MustDo with GET.
func (c *Client) Get(path string, out any) {
	c.env.T.Helper()
	c.MustDo(http.MethodGet, path, nil, out)
}

// Post is MustDo with POST.
func (c *Client) Post(path string, body, out any) {
	c.env.T.Helper()
	c.MustDo(http.MethodPost, path, body, out)
}

// Query escapes a query value.
func Query(v string) string { return url.QueryEscape(v) }

// Fmt formats a path.
func Fmt(format string, args ...any) string { return fmt.Sprintf(format, args...) }
