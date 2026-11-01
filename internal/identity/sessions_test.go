// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// sessionOf returns the hash of a client's session, by which the sessions table knows it.
func sessionOf(t *testing.T, c *apitest.Client) []byte {
	t.Helper()
	for _, ck := range c.Cookies() {
		if strings.HasSuffix(ck.Name, "kogge_session") {
			return crypto.HashToken(ck.Value)
		}
	}
	t.Fatal("the client has no session")
	return nil
}

// TestSessionLifetimes: a session ends after its idle timeout and after its absolute lifetime, whatever the
// browser does; activity extends the idle timeout up to the lifetime, but what the web application does on its
// own — the event streams, requests while its user is idle — does not keep an unattended session alive.
func TestSessionLifetimes(t *testing.T) {
	env := apitest.New(t)
	ctx := context.Background()
	lena := env.CreateUser("lena")
	hash := sessionOf(t, lena)
	set := func(sql string) {
		t.Helper()
		if _, err := env.App.Pool.Exec(ctx, "UPDATE sessions SET "+sql+" WHERE id_hash = $1", hash); err != nil {
			t.Fatal(err)
		}
	}
	idleLeft := func() time.Duration {
		t.Helper()
		var at time.Time
		if err := env.App.Pool.QueryRow(ctx, "SELECT idle_expires_at FROM sessions WHERE id_hash = $1", hash).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return time.Until(at)
	}
	quiet := func() {
		set("last_seen_at = now() - interval '5 minutes', idle_expires_at = now() + interval '10 minutes'")
	}

	// Background requests and event streams leave the idle timeout as it is.
	quiet()
	lena.MustDo(http.MethodGet, "/api/v1/notifications", nil, nil, identity.BackgroundHeader, "1")
	if left := idleLeft(); left > 11*time.Minute {
		t.Errorf("a background request extended the session to %v", left)
	}
	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, env.URL+"/api/v1/live", nil)
	if res, err := lena.Send(req); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("live: %v %v", res, err)
	} else {
		res.Body.Close()
	}
	cancel()
	if left := idleLeft(); left > 11*time.Minute {
		t.Errorf("the event stream extended the session to %v", left)
	}
	// Activity extends it by the idle timeout, but never beyond the lifetime.
	lena.MustDo(http.MethodGet, "/api/v1/notifications", nil, nil)
	if left := idleLeft(); left < 119*time.Minute {
		t.Errorf("activity left the session %v", left)
	}
	quiet()
	set("expires_at = now() + interval '30 minutes'")
	lena.MustDo(http.MethodGet, "/api/v1/notifications", nil, nil)
	if left := idleLeft(); left > 31*time.Minute {
		t.Errorf("activity extended the session beyond its lifetime: %v", left)
	}

	// An expired session ends, and the browser is told to forget its cookie.
	for name, sql := range map[string]string{"idle timeout": "idle_expires_at = now() - interval '1 second'",
		"lifetime": "expires_at = now() - interval '1 second'"} {
		c := env.Login("lena", apitest.UserPassword)
		hash = sessionOf(t, c)
		set(sql)
		r := c.Do(http.MethodGet, "/api/v1/me", nil)
		if r.Status != http.StatusUnauthorized || !strings.Contains(r.Header.Get("Set-Cookie"), "Max-Age=0") {
			t.Errorf("after the %s: %d %q", name, r.Status, r.Header.Get("Set-Cookie"))
		}
		var n int
		if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE id_hash = $1", hash).Scan(&n); err != nil || n != 0 {
			t.Errorf("the session survived its %s: %d %v", name, n, err)
		}
	}
}

// TestSessionCookies: over HTTPS the session cookie is __Host- prefixed and Secure; over plain HTTP (for which
// Kogge warns at start) it can be neither. It is always HttpOnly, SameSite=Lax, for the whole host, lives no longer
// than the session, and signing out removes it.
func TestSessionCookies(t *testing.T) {
	login := func(env *apitest.Env) *http.Cookie {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": apitest.AdminPassword})
		res, err := http.Post(env.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || len(res.Cookies()) != 1 {
			t.Fatalf("login: %d %v", res.StatusCode, res.Header["Set-Cookie"])
		}
		return res.Cookies()[0]
	}
	plain := apitest.New(t)
	c := login(plain)
	if c.Name != "kogge_session" || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.Domain != "" || c.MaxAge != int((12*time.Hour).Seconds()) {
		t.Errorf("cookie over plain HTTP: %+v", c)
	}
	secure := apitest.New(t, func(c *config.Config) { c.PublicURL, _ = url.Parse("https://kogge.example.com") })
	c = login(secure)
	if c.Name != "__Host-kogge_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.Domain != "" {
		t.Errorf("cookie over HTTPS: %+v", c)
	}
	r := plain.Admin().Do(http.MethodPost, "/api/v1/auth/logout", nil)
	if gone := r.Header.Get("Set-Cookie"); !strings.HasPrefix(gone, "kogge_session=;") || !strings.Contains(gone, "Max-Age=0") {
		t.Errorf("signing out: %q", gone)
	}
}

// TestSessionsEnd: changing the password ends the other sessions, signing out everywhere else ends all but the
// current one, and disabling a user ends theirs and stops their access tokens, for the API and Git.
func TestSessionsEnd(t *testing.T) {
	env := apitest.New(t)
	lena := env.CreateUser("lena")
	laptop, phone := env.Login("lena", apitest.UserPassword), env.Login("lena", apitest.UserPassword)
	signedIn := func(c *apitest.Client) bool { return c.Do(http.MethodGet, "/api/v1/me", nil).Status == http.StatusOK }

	var ended struct {
		Ended int `json:"ended"`
	}
	lena.MustDo(http.MethodDelete, "/api/v1/me/sessions", nil, &ended)
	if ended.Ended != 2 || !signedIn(lena) || signedIn(laptop) || signedIn(phone) {
		t.Errorf("signing out everywhere else: ended %d; this one %v, laptop %v, phone %v", ended.Ended, signedIn(lena),
			signedIn(laptop), signedIn(phone))
	}
	var events []struct {
		Details map[string]any `json:"details"`
	}
	env.Admin().Get("/api/v1/admin/audit?action=auth.session.end-others", &events)
	if len(events) != 1 || events[0].Details["sessions"] != float64(2) {
		t.Errorf("audit: %+v", events)
	}

	other := env.Login("lena", apitest.UserPassword)
	lena.MustDo(http.MethodPut, "/api/v1/me/password", map[string]string{"current": apitest.UserPassword, "new": "Another-Secret-2026"}, nil)
	if !signedIn(lena) || signedIn(other) {
		t.Errorf("after the password change: this one %v, the other %v", signedIn(lena), signedIn(other))
	}

	var token struct {
		Token string `json:"token"`
	}
	lena.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "cli", "scopes": []string{"read"}}, &token)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SES", "name": "Sessions"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SES/repos", map[string]any{"name": "app", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SES/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": userID(t, lena)}, "permission": "repo.read"}}, nil)
	git := func() int {
		req, _ := http.NewRequest(http.MethodGet, env.URL+"/scm/SES/app.git/info/refs?service=git-upload-pack", nil)
		req.SetBasicAuth("lena", token.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	api := func() int { return env.WithToken(token.Token).Do(http.MethodGet, "/api/v1/me", nil).Status }
	if git() != http.StatusOK || api() != http.StatusOK {
		t.Fatalf("before disabling: git %d, api %d", git(), api())
	}
	id := userID(t, lena)
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/users/%d", id), map[string]any{"status": "disabled"}, nil)
	if signedIn(lena) || git() != http.StatusUnauthorized || api() != http.StatusUnauthorized {
		t.Errorf("after disabling: session %v, git %d, api %d", signedIn(lena), git(), api())
	}
}
