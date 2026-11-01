// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestLockoutExpires: a lock ends after its time, and the count starts again, so that one wrong password per lock
// period does not keep an account locked.
func TestLockoutExpires(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("lena")
	anon := env.Anonymous()
	login := func(password string) int {
		return anon.Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "lena", "password": password}).Status
	}
	// Ten failures locked the account (set directly: the rate limit of sign-ins would get in the way), and the lock
	// has expired.
	ctx := context.Background()
	if _, err := env.App.Pool.Exec(ctx, `UPDATE users SET failed_logins = 10, locked_until = now() - interval '1 second'
		WHERE username = 'lena'`); err != nil {
		t.Fatal(err)
	}
	login("wrong-password-123")
	var failed int
	var locked bool
	if err := env.App.Pool.QueryRow(ctx, "SELECT failed_logins, coalesce(locked_until > now(), false) FROM users WHERE username = 'lena'").
		Scan(&failed, &locked); err != nil {
		t.Fatal(err)
	}
	if failed != 1 || locked {
		t.Errorf("after the lock: %d failures, locked %v", failed, locked)
	}
	if s := login(apitest.UserPassword); s != http.StatusOK {
		t.Errorf("sign-in after the lock: %d", s)
	}
}

// TestCredentials: a password change through an access token ends all sessions, and a request with other
// credentials is not signed in by the session cookie.
func TestCredentials(t *testing.T) {
	env := apitest.New(t)
	lena := env.CreateUser("lena")
	other := env.Login("lena", apitest.UserPassword)
	var token struct {
		Token string `json:"token"`
	}
	lena.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "cli", "scopes": []string{"read", "write"}}, &token)

	// The cookie does not count next to other credentials.
	if r := other.Do(http.MethodGet, "/api/v1/me", nil, "Authorization", "Basic eDp5"); r.Status != http.StatusUnauthorized {
		t.Errorf("cookie and Basic credentials: %d", r.Status)
	}
	if r := other.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusOK {
		t.Fatalf("session: %d", r.Status)
	}

	env.WithToken(token.Token).MustDo(http.MethodPut, "/api/v1/me/password", map[string]string{"current": apitest.UserPassword,
		"new": "Another-Secret-2026"}, nil)
	for name, c := range map[string]*apitest.Client{"first": lena, "second": other} {
		if r := c.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
			t.Errorf("the %s session survived the password change: %d", name, r.Status)
		}
	}
}

// TestLastAdministrator: the last administrator grant can neither be revoked nor deleted with its group.
func TestLastAdministrator(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	var group struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Admins"}, &group)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "admin", "principalType": "group",
		"principalId": group.ID}, nil)
	admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", group.ID, me.User.ID), nil, nil)
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/admin/global-permissions?permission=admin&principalType=user&principalId=%d", me.User.ID), nil, nil)
	if r := admin.Do(http.MethodDelete, apitest.Fmt("/api/v1/admin/groups/%d", group.ID), nil); r.Code() != "permission.last-admin" {
		t.Errorf("deleting the group of the last administrator grant: %d %s", r.Status, r.Body)
	}
}

// TestLockoutNotExtended: attempts while an account is locked neither count nor extend the lock, so nobody can keep
// someone else's account locked for good; a new lock is audited (audit M2).
func TestLockoutNotExtended(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("lena")
	ctx := context.Background()
	lockedUntil := func() time.Time {
		t.Helper()
		var until *time.Time
		if err := env.App.Pool.QueryRow(ctx, "SELECT locked_until FROM users WHERE username = 'lena'").Scan(&until); err != nil {
			t.Fatal(err)
		}
		if until == nil {
			return time.Time{}
		}
		return *until
	}
	// Nine failures in a row (set directly: the rate limit of sign-ins would get in the way); the tenth locks.
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET failed_logins = 9 WHERE username = 'lena'"); err != nil {
		t.Fatal(err)
	}
	attempt := func(password string) int {
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "lena", "password": password}).Status
	}
	attempt("wrong-password-1")
	first := lockedUntil()
	if time.Until(first) < 14*time.Minute {
		t.Fatalf("the tenth failure did not lock the account: %v", first)
	}
	var audited int
	if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action = 'auth.locked'").Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("lock audited %d times (%v)", audited, err)
	}
	// Another wrong password while locked: the lock stays as it was.
	time.Sleep(1100 * time.Millisecond)
	attempt("wrong-password-2")
	if again := lockedUntil(); !again.Equal(first) {
		t.Fatalf("a failure while locked moved the lock from %v to %v", first, again)
	}
	// The right password does not get in while locked, but does once the lock is over.
	if s := attempt(apitest.UserPassword); s != http.StatusUnauthorized {
		t.Fatalf("sign-in while locked: %d", s)
	}
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET locked_until = now() - interval '1 second' WHERE username = 'lena'"); err != nil {
		t.Fatal(err)
	}
	if s := attempt(apitest.UserPassword); s != http.StatusOK {
		t.Fatalf("sign-in after the lock: %d", s)
	}
}
