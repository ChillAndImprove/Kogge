// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// behindProxy is a test server that trusts the test client as its reverse proxy, so that requests name their
// client address in X-Forwarded-For.
func behindProxy(t *testing.T) *apitest.Env {
	return apitest.New(t, func(c *config.Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	})
}

// node starts another server on the database of env — a second node of the installation, with limits of its
// own — and returns its address.
func node(t *testing.T, env *apitest.Env) string {
	t.Helper()
	srv := httptest.NewServer(nil)
	t.Cleanup(srv.Close)
	a, err := app.New(context.Background(), env.App.Cfg, slog.New(slog.DiscardHandler),
		app.Options{Argon2: &crypto.Argon2Params{Memory: 1024, Time: 1, Threads: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	srv.Config.Handler = a.Handler()
	return srv.URL
}

// signIn tries a local sign-in at a server from a client address and returns the status.
func signIn(t *testing.T, server, from, username, password string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequest(http.MethodPost, server+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", from)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// gitAuth authenticates a Git request at a server with a password from a client address and returns the status.
func gitAuth(t *testing.T, server, from, username, password string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server+"/scm/THR/tools.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("X-Forwarded-For", from)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// gitRepository creates the repository THR/tools that the users may read.
func gitRepository(t *testing.T, env *apitest.Env, readers ...*apitest.Client) {
	t.Helper()
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "THR", "name": "Throttle"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/THR/repos", map[string]any{"name": "tools", "readme": true}, nil)
	var grants []map[string]any
	for _, c := range readers {
		grants = append(grants, map[string]any{"holder": map[string]any{"type": "user", "id": userID(t, c)}, "permission": "repo.read"})
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/THR/tools/permissions", grants, nil)
}

// TestOneClientFailingBlocksNobodyElse: a client that keeps failing for one account is stopped after a few tries,
// for the web sign-in and Git, but the other people behind the same address sign in, and the account's owner signs
// in from elsewhere.
func TestOneClientFailingBlocksNobodyElse(t *testing.T) {
	env := behindProxy(t)
	lena, otto := env.CreateUser("lena"), env.CreateUser("otto")
	gitRepository(t, env, lena, otto)
	const office = "203.0.113.10" // behind a proxy that is not trusted, all clients of the office have its address

	refused := 0
	for i := range 12 {
		switch s := signIn(t, env.URL, office, "lena", fmt.Sprintf("Guess-Number-%d", i)); s {
		case http.StatusUnauthorized:
			if refused > 0 {
				t.Fatalf("attempt %d was checked after a refusal", i)
			}
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("attempt %d: %d", i, s)
		}
	}
	if refused < 5 {
		t.Fatalf("one client guessed one account's password %d times in a row", 12-refused)
	}
	// Git with the password is refused for lena from there too, also with the right password.
	if s := gitAuth(t, env.URL, office, "lena", apitest.UserPassword); s != http.StatusTooManyRequests {
		t.Errorf("Git for the blocked account from the same client: %d", s)
	}

	// Everyone else in the office signs in, on the web and with Git.
	if s := signIn(t, env.URL, office, "otto", apitest.UserPassword); s != http.StatusOK {
		t.Errorf("a colleague behind the same address: %d", s)
	}
	for i := range 3 {
		if s := gitAuth(t, env.URL, office, "otto", apitest.UserPassword); s != http.StatusOK {
			t.Errorf("Git request %d of a colleague behind the same address: %d", i, s)
		}
	}
	if s := signIn(t, env.URL, office, "admin", apitest.AdminPassword); s != http.StatusOK {
		t.Errorf("the administrator behind the same address: %d", s)
	}
	// lena herself signs in from home: five failures do not lock her account.
	if s := signIn(t, env.URL, "198.51.100.20", "lena", apitest.UserPassword); s != http.StatusOK {
		t.Errorf("the account's owner from another address: %d", s)
	}
}

// TestPasswordSprayingIsLimited: one wrong password for many accounts from one address is stopped — loosely enough
// that the typos of a whole office behind one address never get there, strictly enough to stop spraying. Other
// addresses are not affected, and valid access tokens pass.
func TestPasswordSprayingIsLimited(t *testing.T) {
	env := behindProxy(t)
	env.CreateUser("otto")
	limited := -1
	for i := range 120 {
		// From ever new addresses of one /64 network: an IPv6 client usually has a whole network to choose from.
		from := fmt.Sprintf("2001:db8:5:5::%x", i+1)
		s := signIn(t, env.URL, from, fmt.Sprintf("person%03d", i), "Summer-2026-Password")
		if s == http.StatusTooManyRequests {
			limited = i
			break
		}
		if s != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, s)
		}
	}
	if limited < 0 {
		t.Fatal("120 user names were tried from one network without a limit")
	}
	if limited < 40 {
		t.Fatalf("the address was blocked after %d failures: the typos of an office behind one address would block it", limited)
	}
	if s := signIn(t, env.URL, "2001:db8:5:5::ffff", "person999", "Summer-2026-Password"); s != http.StatusTooManyRequests {
		t.Errorf("another user name from the spraying network: %d", s)
	}
	if s := signIn(t, env.URL, "192.0.2.44", "otto", apitest.UserPassword); s != http.StatusOK {
		t.Errorf("sign-in from another address: %d", s)
	}
	var token struct {
		Token string `json:"token"`
	}
	env.Login("otto", apitest.UserPassword).MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "cli",
		"scopes": []string{"read"}}, &token)
	if r := env.Anonymous().Do(http.MethodGet, "/api/v1/me", nil, "Authorization", "Bearer "+token.Token,
		"X-Forwarded-For", "2001:db8:5:5::1"); r.Status != http.StatusOK {
		t.Errorf("a valid access token from the spraying network: %d", r.Status)
	}
}

// TestSprayingThroughSeveralNodes: the limit of the failures of a client address holds for all nodes together, so
// that spraying through two nodes behind a load balancer is stopped as soon as through one, at both.
func TestSprayingThroughSeveralNodes(t *testing.T) {
	env := behindProxy(t)
	nodes := []string{env.URL, node(t, env)}
	limited := -1
	for i := range 120 {
		s := signIn(t, nodes[i%2], fmt.Sprintf("2001:db8:7:7::%x", i+1), fmt.Sprintf("person%03d", i), "Summer-2026-Password")
		if s == http.StatusTooManyRequests {
			limited = i
			break
		}
		if s != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, s)
		}
	}
	// A node alone stops the address after 60 failures (TestPasswordSprayingIsLimited).
	if limited < 55 || limited > 62 {
		t.Fatalf("spraying through two nodes was stopped after %d failures", limited)
	}
	for _, n := range nodes {
		if s := signIn(t, n, "2001:db8:7:7::ffff", "person999", "Summer-2026-Password"); s != http.StatusTooManyRequests {
			t.Errorf("the spraying network at %s: %d", n, s)
		}
	}
}

// TestBruteForceAcrossAddressesLocksTheAccount: guesses for one account from ever new addresses lock the account;
// the lockout is kept in the database, so it holds on every node of the installation (each of which has limits of
// its own in memory), for the web sign-in and Git, and it is audited. Other accounts are not affected.
func TestBruteForceAcrossAddressesLocksTheAccount(t *testing.T) {
	env := behindProxy(t)
	lena, otto := env.CreateUser("lena"), env.CreateUser("otto")
	gitRepository(t, env, lena, otto)
	ctx := context.Background()
	locked := func() bool {
		t.Helper()
		var on bool
		if err := env.App.Pool.QueryRow(ctx, "SELECT coalesce(locked_until > now(), false) FROM users WHERE username = 'lena'").
			Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}

	// The guesses go to a node of their own: on the first one, the test's own sign-ins count for lena's account.
	guessing := node(t, env)
	for i := range 10 {
		from := fmt.Sprintf("198.51.100.%d", i+1)
		if s := signIn(t, guessing, from, "lena", fmt.Sprintf("Guess-Number-%d", i)); s != http.StatusUnauthorized {
			t.Fatalf("guess %d from %s: %d", i, from, s)
		}
	}
	// Each node also limits the attempts per account: the eleventh within a minute is refused unchecked.
	if s := signIn(t, guessing, "198.51.100.11", "lena", "Guess-Number-10"); s != http.StatusTooManyRequests {
		t.Errorf("an eleventh guess at the same node: %d", s)
	}
	if !locked() {
		t.Fatal("ten guesses from ten addresses did not lock the account")
	}
	// Another node refuses the right password from yet another address, on the web and with Git.
	other := node(t, env)
	if s := signIn(t, other, "198.51.100.200", "lena", apitest.UserPassword); s != http.StatusUnauthorized {
		t.Errorf("the right password while locked: %d", s)
	}
	if s := gitAuth(t, other, "198.51.100.201", "lena", apitest.UserPassword); s != http.StatusUnauthorized {
		t.Errorf("Git with the right password while locked: %d", s)
	}
	var locks, failures int
	if err := env.App.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'auth.locked'),
		count(*) FILTER (WHERE action = 'auth.login.failed') FROM audit_events WHERE details->>'username' = 'lena'`).
		Scan(&locks, &failures); err != nil {
		t.Fatal(err)
	}
	if locks != 1 || failures != 12 {
		t.Errorf("audited %d lockouts and %d failed sign-ins, want 1 and 12", locks, failures)
	}
	if s := signIn(t, env.URL, "198.51.100.202", "otto", apitest.UserPassword); s != http.StatusOK {
		t.Errorf("another account: %d", s)
	}

	// Guesses through Git lock the account as well.
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET locked_until = NULL, failed_logins = 0 WHERE username = 'lena'"); err != nil {
		t.Fatal(err)
	}
	gitNode := node(t, env)
	for i := range 10 {
		if s := gitAuth(t, gitNode, fmt.Sprintf("192.0.2.%d", i+1), "lena", fmt.Sprintf("Guess-Number-%d", i)); s != http.StatusUnauthorized {
			t.Fatalf("Git guess %d: %d", i, s)
		}
	}
	if !locked() {
		t.Fatal("ten Git guesses did not lock the account")
	}
	if s := signIn(t, node(t, env), "192.0.2.100", "lena", apitest.UserPassword); s != http.StatusUnauthorized {
		t.Errorf("sign-in after ten Git guesses: %d", s)
	}
}
