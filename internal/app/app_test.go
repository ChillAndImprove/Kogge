// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/testutil/apitest"
)

type me struct {
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Permissions []string `json:"permissions"`
	CSRFToken   string   `json:"csrfToken"`
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestLoginAndSession(t *testing.T) {
	env := apitest.New(t)
	anon := env.Anonymous()
	if r := anon.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized || r.Code() != "auth.required" {
		t.Fatalf("anonymous me: %d %s", r.Status, r.Body)
	}
	if r := anon.Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong"}); r.Status != http.StatusUnauthorized ||
		r.Code() != "auth.invalid-credentials" {
		t.Fatalf("wrong password: %d %s", r.Status, r.Body)
	}
	if r := anon.Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "whatever"}); r.Code() != "auth.invalid-credentials" {
		t.Fatalf("unknown user must look like a wrong password: %s", r.Body)
	}

	admin := env.Admin()
	var m me
	r := admin.MustDo(http.MethodGet, "/api/v1/me", nil, &m)
	if m.User.Username != "admin" || !has(m.Permissions, "admin") || m.CSRFToken == "" {
		t.Fatalf("me: %+v", m)
	}
	// Answers of the API are kept by no browser or proxy (they carry the CSRF token, for one).
	if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control of the API: %q", cc)
	}

	// Logout ends the session.
	admin.MustDo(http.MethodPost, "/api/v1/auth/logout", nil, nil)
	if r := admin.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", r.Status)
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	body := map[string]any{"name": "Team", "description": ""}
	if r := admin.Do(http.MethodPost, "/api/v1/admin/groups", body, "X-CSRF-Token", "forged"); r.Code() != "auth.csrf" {
		t.Fatalf("forged CSRF token accepted: %d %s", r.Status, r.Body)
	}
	if r := admin.Do(http.MethodPost, "/api/v1/admin/groups", body, "Origin", "https://evil.example"); r.Code() != "auth.origin" {
		t.Fatalf("foreign origin accepted: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", body, nil)
}

func TestAccessTokens(t *testing.T) {
	env := apitest.New(t)
	anna := env.CreateUser("anna")
	var created struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID   int64  `json:"id"`
			Hint string `json:"hint"`
		} `json:"accessToken"`
	}
	anna.Post("/api/v1/me/tokens", map[string]any{"name": "read only", "scopes": []string{"read"}}, &created)
	if !strings.HasPrefix(created.Token, "kgp_") || strings.Contains(created.AccessToken.Hint, created.Token[10:30]) {
		t.Fatalf("token: %+v", created)
	}
	tok := env.WithToken(created.Token)
	var m me
	tok.Get("/api/v1/me", &m)
	if m.User.Username != "anna" || m.CSRFToken != "" {
		t.Fatalf("token principal: %+v", m)
	}
	// Read scope cannot write; tokens need no CSRF token.
	if r := tok.Do(http.MethodPatch, "/api/v1/me", map[string]any{"theme": "dark"}); r.Code() != "auth.token-scope" {
		t.Fatalf("write with read token: %d %s", r.Status, r.Body)
	}
	var tokens []struct{ ID int64 }
	anna.Get("/api/v1/me/tokens", &tokens)
	if len(tokens) != 1 {
		t.Fatalf("tokens: %v", tokens)
	}
	anna.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/me/tokens/%d", created.AccessToken.ID), nil, nil)
	if r := tok.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", r.Status)
	}
}

func TestAdministrationNeedsPermission(t *testing.T) {
	env := apitest.New(t)
	ben := env.CreateUser("ben")
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/groups", "/api/v1/admin/audit",
		"/api/v1/admin/settings", "/api/v1/admin/system", "/api/v1/admin/global-permissions"} {
		if r := ben.Do(http.MethodGet, path, nil); r.Status != http.StatusForbidden {
			t.Errorf("%s: %d", path, r.Status)
		}
		if r := env.Anonymous().Do(http.MethodGet, path, nil); r.Status != http.StatusUnauthorized {
			t.Errorf("anonymous %s: %d", path, r.Status)
		}
	}
}

func TestGroupGrantsReachMembers(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	carla := env.CreateUser("carla")
	var group struct{ ID int64 }
	admin.Post("/api/v1/admin/groups", map[string]any{"name": "Project leads"}, &group)
	var carlaMe me
	carla.Get("/api/v1/me", &carlaMe)
	admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", group.ID, carlaMe.User.ID), nil, nil)
	admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": "project.create",
		"principalType": "group", "principalId": group.ID}, nil)

	carla = env.Login("carla", apitest.UserPassword)
	carla.Get("/api/v1/me", &carlaMe)
	if !has(carlaMe.Permissions, "project.create") || has(carlaMe.Permissions, "admin") {
		t.Fatalf("permissions through the group: %v", carlaMe.Permissions)
	}

	// The last administrator grant cannot be removed.
	var adminMe me
	admin.Get("/api/v1/me", &adminMe)
	r := admin.Do(http.MethodDelete, apitest.Fmt("/api/v1/admin/global-permissions?permission=admin&principalType=user&principalId=%d",
		adminMe.User.ID), nil)
	if r.Code() != "permission.last-admin" {
		t.Fatalf("removed the last administrator: %d %s", r.Status, r.Body)
	}
}

func TestDisabledUserLosesAccess(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dora := env.CreateUser("dora")
	var m me
	dora.Get("/api/v1/me", &m)
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/users/%d", m.User.ID), map[string]any{"status": "disabled"}, nil)
	if r := dora.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("disabled user still signed in: %d", r.Status)
	}
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "dora",
		"password": apitest.UserPassword}); r.Status != http.StatusUnauthorized {
		t.Fatalf("disabled user can sign in: %d", r.Status)
	}
	var adminMe me
	admin.Get("/api/v1/me", &adminMe)
	if r := admin.Do(http.MethodPatch, apitest.Fmt("/api/v1/admin/users/%d", adminMe.User.ID), map[string]any{"status": "disabled"}); r.Code() != "user.self-disable" {
		t.Fatalf("administrators must not disable themselves: %s", r.Body)
	}
}

func TestAuditLog(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "nope"})
	var events []struct {
		Action  string         `json:"action"`
		ActorIP *string        `json:"actorIp"`
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=auth.", &events)
	var logins, failures int
	for _, e := range events {
		switch e.Action {
		case "auth.login":
			logins++
			if e.ActorIP == nil || *e.ActorIP != "127.0.0.1" {
				t.Errorf("login without client address: %+v", e)
			}
		case "auth.login.failed":
			failures++
		}
	}
	if logins == 0 || failures == 0 {
		t.Fatalf("audit events: %+v", events)
	}
}

func TestPasswordChange(t *testing.T) {
	env := apitest.New(t)
	eva := env.CreateUser("eva")
	if r := eva.Do(http.MethodPut, "/api/v1/me/password", map[string]string{"current": "wrong-password", "new": "New-Secret-Value-9"}); r.Code() != "password.wrong" {
		t.Fatalf("wrong current password: %s", r.Body)
	}
	if r := eva.Do(http.MethodPut, "/api/v1/me/password", map[string]string{"current": apitest.UserPassword, "new": "eva-is-my-name-ok"}); r.Code() != "password.policy" {
		t.Fatalf("password containing the user name: %s", r.Body)
	}
	eva.MustDo(http.MethodPut, "/api/v1/me/password", map[string]string{"current": apitest.UserPassword, "new": "New-Secret-Value-9"}, nil)
	env.Login("eva", "New-Secret-Value-9")
}

func TestUserDirectoryAndOpenAPI(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("frank")
	gina := env.CreateUser("gina")
	var users struct {
		Total int `json:"total"`
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
	}
	gina.Get("/api/v1/users?query=fra", &users)
	if users.Total != 1 || users.Items[0].Username != "frank" {
		t.Fatalf("user search: %+v", users)
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	env.Anonymous().Get("/api/v1/openapi.json", &doc)
	if doc.OpenAPI != "3.1.0" || doc.Paths["/api/v1/me"] == nil {
		t.Fatalf("openapi: %s %d", doc.OpenAPI, len(doc.Paths))
	}
}

// TestSignInLimitsPerAddress: people behind one address (a terminal server, a proxy) can all sign in, Git clients
// that send their password with every request are not slowed down, but failures from one address are limited, for
// the web sign-in and for Git, also when the password finally is right. Valid access tokens still pass.
func TestSignInLimitsPerAddress(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	login := func(user, password string) int {
		t.Helper()
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": user, "password": password}).Status
	}
	for i := range 12 {
		name := fmt.Sprintf("desk%02d", i)
		admin.MustDo(http.MethodPost, "/api/v1/admin/users", map[string]string{"username": name, "displayName": name,
			"password": apitest.UserPassword, "email": name + "@example.com"}, nil)
		if s := login(name, apitest.UserPassword); s != http.StatusOK {
			t.Fatalf("colleague %d behind the same address: %d", i, s)
		}
	}

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TS", "name": "Terminal server"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TS/repos", map[string]any{"name": "tools", "readme": true}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &token)
	git := func(user, secret string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, repo.CloneHTTP+"/info/refs?service=git-upload-pack", nil)
		req.SetBasicAuth(user, secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for i := range 15 {
		if s := git("admin", apitest.AdminPassword); s != http.StatusOK {
			t.Fatalf("git request %d with the right password: %d", i, s)
		}
	}

	// Password spraying: wrong passwords for many accounts block the address (internal/identity/throttle.go).
	for i := range 60 {
		if s := login(fmt.Sprintf("desk%02d", i), "Wrong-Password-2026"); s != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d: %d", i, s)
		}
	}
	if s := login("desk11", apitest.UserPassword); s != http.StatusTooManyRequests {
		t.Fatalf("sign-in from a blocked address with the right password: %d", s)
	}
	if s := git("admin", apitest.AdminPassword); s != http.StatusTooManyRequests {
		t.Fatalf("git password from a blocked address: %d", s)
	}
	if s := git("admin", token.Token); s != http.StatusOK {
		t.Fatalf("access token from a blocked address: %d", s)
	}
}

// TestSignInLimitsPerAccount: every attempt counts for the account, so one account cannot be signed in to (or
// guessed at) without limit; other accounts are not affected.
func TestSignInLimitsPerAccount(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("eva")
	for range 9 {
		env.Login("eva", apitest.UserPassword)
	}
	r := env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "eva", "password": apitest.UserPassword})
	if r.Status != http.StatusTooManyRequests || r.Code() != "auth.rate-limited" {
		t.Fatalf("eleventh sign-in within seconds: %d %s", r.Status, r.Body)
	}
	env.CreateUser("finn")
}

// TestAdminScopeForImpliedPermissions: administrators have every global permission, but through an access token only
// with the admin scope — also for permissions they hold only as administrators, such as managing build agents and
// the instance's secrets (audit M1).
func TestAdminScopeForImpliedPermissions(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	token := func(scopes ...string) *apitest.Client {
		var created struct {
			Token string `json:"token"`
		}
		admin.Post("/api/v1/me/tokens", map[string]any{"name": strings.Join(scopes, "+"), "scopes": scopes}, &created)
		return env.WithToken(created.Token)
	}
	plain, full := token("read", "write"), token("read", "write", "admin")
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/agents", "/api/v1/admin/agent-tokens",
		"/api/v1/builds/variables?scope=instance"} {
		if r := plain.Do(http.MethodGet, path, nil); r.Status != http.StatusForbidden || r.Code() != "auth.token-scope" {
			t.Errorf("%s without the admin scope: %d %s", path, r.Status, r.Body)
		}
		if r := full.Do(http.MethodGet, path, nil); r.Status != http.StatusOK {
			t.Errorf("%s with the admin scope: %d %s", path, r.Status, r.Body)
		}
	}
	if r := plain.Do(http.MethodPost, "/api/v1/admin/agent-tokens", map[string]any{}); r.Status != http.StatusForbidden {
		t.Errorf("agent token created without the admin scope: %d", r.Status)
	}
	// A permission granted explicitly works through any token.
	env.CreateUser("lena")
	var users struct {
		Items []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	full.Get("/api/v1/admin/users?query=lena", &users)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "agent.manage", "principalType": "user",
		"principalId": users.Items[0].ID}, nil)
	lena := env.Login("lena", apitest.UserPassword)
	var created struct {
		Token string `json:"token"`
	}
	lena.Post("/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &created)
	if r := env.WithToken(created.Token).Do(http.MethodGet, "/api/v1/admin/agents", nil); r.Status != http.StatusOK {
		t.Errorf("an explicitly granted permission through a token: %d %s", r.Status, r.Body)
	}
}

// TestAdministrationThroughTokensNeedsTheAdminScope: access tokens without the admin scope work with the content
// of projects, spaces and repositories, but do not administer them — no permissions, deploy keys or settings
// (audit L4), like global administration and SSH keys.
func TestAdministrationThroughTokensNeedsTheAdminScope(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	lena := env.CreateUser("lena")
	var m me
	lena.Get("/api/v1/me", &m)
	for _, perm := range []string{"project.create", "space.create", "codeproject.create"} {
		admin.Post("/api/v1/admin/global-permissions", map[string]any{"permission": perm, "principalType": "user",
			"principalId": m.User.ID}, nil)
	}
	lena.Post("/api/v1/projects", map[string]any{"key": "TOK", "name": "Tokens"}, nil)
	lena.Post("/api/v1/wiki/spaces", map[string]any{"key": "TOK", "name": "Tokens"}, nil)
	lena.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TOK", "name": "Tokens"}, nil)
	lena.MustDo(http.MethodPost, "/api/v1/code/projects/TOK/repos", map[string]any{"name": "app", "readme": true}, nil)
	token := func(scopes ...string) *apitest.Client {
		var created struct {
			Token string `json:"token"`
		}
		lena.Post("/api/v1/me/tokens", map[string]any{"name": strings.Join(scopes, "+"), "scopes": scopes}, &created)
		return env.WithToken(created.Token)
	}
	writer, administrator := token("read", "write"), token("read", "write", "admin")
	grants := []map[string]any{{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": m.User.ID}}}
	var spaceGrants []map[string]any
	lena.Get("/api/v1/wiki/spaces/TOK/permissions", &spaceGrants)
	administration := []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/v1/code/projects/TOK/permissions", grants},
		{http.MethodPut, "/api/v1/code/repos/TOK/app/permissions", []any{}},
		{http.MethodPut, "/api/v1/code/repos/TOK/app/build-settings", map[string]any{"autoCancel": true, "maxParallelJobs": 2}},
		{http.MethodPost, "/api/v1/projects/TOK/versions", map[string]any{"name": "1.0"}},
		{http.MethodPut, "/api/v1/wiki/spaces/TOK/permissions", spaceGrants},
	}
	for _, a := range administration {
		if r := writer.Do(a.method, a.path, a.body); r.Status != http.StatusForbidden {
			t.Errorf("%s %s with a write token: %d %s", a.method, a.path, r.Status, r.Body)
		}
		administrator.MustDo(a.method, a.path, a.body, nil)
	}
	// The write token still works with the content.
	writer.MustDo(http.MethodPut, "/api/v1/code/repos/TOK/app/files", map[string]any{"branch": "main", "path": "a.txt", "content": "a"}, nil)
	writer.Get("/api/v1/wiki/spaces/TOK", nil)
	writer.Get("/api/v1/projects/TOK", nil)
}

// TestAdministratorsRevokeAccess: for a compromised account, administrators see and end everything that works
// besides the password — sessions, access tokens and SSH keys (audit L1).
func TestAdministratorsRevokeAccess(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	anna := env.CreateUser("anna")
	var m me
	anna.Get("/api/v1/me", &m)
	newToken := func() (*apitest.Client, int64) {
		var created struct {
			Token       string `json:"token"`
			AccessToken struct {
				ID int64 `json:"id"`
			} `json:"accessToken"`
		}
		anna.Post("/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &created)
		return env.WithToken(created.Token), created.AccessToken.ID
	}
	tok, tokID := newToken()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	var key struct {
		ID int64 `json:"id"`
	}
	anna.Post("/api/v1/me/ssh-keys", map[string]any{"name": "laptop", "key": string(ssh.MarshalAuthorizedKey(sshPub))}, &key)

	base := apitest.Fmt("/api/v1/admin/users/%d", m.User.ID)
	var creds struct {
		Sessions int `json:"sessions"`
		Tokens   []struct {
			ID   int64  `json:"id"`
			Hint string `json:"hint"`
		} `json:"tokens"`
		SSHKeys []struct {
			ID int64 `json:"id"`
		} `json:"sshKeys"`
	}
	admin.Get(base+"/credentials", &creds)
	if creds.Sessions != 1 || len(creds.Tokens) != 1 || creds.Tokens[0].Hint == "" || len(creds.SSHKeys) != 1 {
		t.Fatalf("credentials: %+v", creds)
	}
	admin.MustDo(http.MethodDelete, apitest.Fmt("%s/tokens/%d", base, tokID), nil, nil)
	if r := tok.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("a revoked token still works: %d", r.Status)
	}
	admin.MustDo(http.MethodDelete, apitest.Fmt("%s/ssh-keys/%d", base, key.ID), nil, nil)
	if r := admin.Do(http.MethodDelete, apitest.Fmt("%s/ssh-keys/%d", base, key.ID), nil); r.Status != http.StatusNotFound {
		t.Errorf("deleted twice: %d", r.Status)
	}

	// Revoking all access ends the sessions and the tokens.
	tok, _ = newToken()
	admin.MustDo(http.MethodDelete, base+"/credentials", nil, nil)
	if r := anna.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("the session survived: %d", r.Status)
	}
	if r := tok.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("the token survived: %d", r.Status)
	}
	admin.Get(base+"/credentials", &creds)
	if creds.Sessions != 0 || len(creds.Tokens) != 0 || len(creds.SSHKeys) != 0 {
		t.Errorf("after revoking: %+v", creds)
	}
	var log []struct {
		Action string `json:"action"`
	}
	admin.Get("/api/v1/admin/audit?action=user.access.revoke", &log)
	if len(log) != 1 {
		t.Errorf("audit: %+v", log)
	}
}

// TestFailedAuthenticationIsAudited: failed sign-ins with access tokens — through the API and through Git — are
// audited and limited per address, so that they can neither be tried without end nor flood the log.
func TestFailedAuthenticationIsAudited(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	anna := env.CreateUser("anna")
	var created struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID int64 `json:"id"`
		} `json:"accessToken"`
	}
	anna.Post("/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &created)
	if _, err := env.App.Pool.Exec(context.Background(), "UPDATE access_tokens SET expires_at = now() - interval '1 day' WHERE id = $1",
		created.AccessToken.ID); err != nil {
		t.Fatal(err)
	}
	if r := env.WithToken(created.Token).Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", r.Status)
	}
	if r := env.WithToken("kgp_"+strings.Repeat("x", 43)).Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", r.Status)
	}
	req, _ := http.NewRequest(http.MethodGet, env.URL+"/scm/NONE/repo.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("anna", "kgp_"+strings.Repeat("y", 43))
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("git with a wrong token: %v %v", err, res)
	}
	var log []struct {
		ActorID *int64         `json:"actorId"`
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=auth.login.failed", &log)
	reasons := map[string]bool{}
	for _, e := range log {
		reasons[apitest.Fmt("%v/%v", e.Details["via"], e.Details["reason"])] = true
	}
	for _, want := range []string{"api/expired", "api/unknown", "git/unknown"} {
		if !reasons[want] {
			t.Errorf("no audit event %s: %+v", want, log)
		}
	}
	// Guessing from one address ends.
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		limited = env.WithToken("kgp_"+strings.Repeat("z", 43)).Do(http.MethodGet, "/api/v1/me", nil).Status == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("unknown tokens can be tried without end")
	}
}

// TestTokenLifetime: the installation bounds the lifetime of new access tokens (default 365 days); a token asked
// for without an expiry gets the longest one allowed.
func TestTokenLifetime(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	anna := env.CreateUser("anna")
	var m struct {
		TokenMaxDays int `json:"tokenMaxDays"`
	}
	anna.Get("/api/v1/me", &m)
	if m.TokenMaxDays != 365 {
		t.Fatalf("tokenMaxDays: %d", m.TokenMaxDays)
	}
	create := func(body map[string]any) (*apitest.Response, *time.Time) {
		var out struct {
			AccessToken struct {
				ExpiresAt *time.Time `json:"expiresAt"`
			} `json:"accessToken"`
		}
		r := anna.Do(http.MethodPost, "/api/v1/me/tokens", body)
		if r.Status < 300 {
			r.Decode(&out)
		}
		return r, out.AccessToken.ExpiresAt
	}
	if _, exp := create(map[string]any{"name": "ci", "scopes": []string{"read"}}); exp == nil ||
		exp.Sub(time.Now()) < 364*24*time.Hour || exp.Sub(time.Now()) > 366*24*time.Hour {
		t.Errorf("a token without expiry: %v", exp)
	}
	if r, _ := create(map[string]any{"name": "ci", "scopes": []string{"read"}, "expiresInDays": 400}); r.Code() != "token.lifetime" {
		t.Errorf("a token beyond the limit: %d %s", r.Status, r.Body)
	}
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	st["maxTokenDays"] = 0
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	if _, exp := create(map[string]any{"name": "ci", "scopes": []string{"read"}}); exp != nil {
		t.Errorf("without a limit, a token without expiry expires: %v", exp)
	}
}

// TestPreferences: the web application keeps settings for the user (the columns of the issue navigator), which
// follow them to other browsers, and the time zone their schedules use.
func TestPreferences(t *testing.T) {
	env := apitest.New(t)
	lena := env.CreateUser("lena")
	type prefs struct {
		TimeZone    string `json:"timeZone"`
		Preferences struct {
			NavigatorColumns []string `json:"navigatorColumns"`
		} `json:"preferences"`
	}
	var got prefs
	lena.Get("/api/v1/me", &got)
	if got.TimeZone != "Europe/Berlin" || len(got.Preferences.NavigatorColumns) != 0 {
		t.Errorf("before: %+v", got)
	}
	lena.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"timeZone": "America/New_York",
		"preferences": map[string]any{"navigatorColumns": []string{"key", "summary", "cf_12"}}}, &got)
	if got.TimeZone != "America/New_York" || strings.Join(got.Preferences.NavigatorColumns, ",") != "key,summary,cf_12" {
		t.Errorf("after: %+v", got)
	}
	// Another session sees them; other changes of the profile keep them.
	other := env.Login("lena", apitest.UserPassword)
	other.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"theme": "dark"}, &got)
	if strings.Join(got.Preferences.NavigatorColumns, ",") != "key,summary,cf_12" {
		t.Errorf("in another session: %+v", got)
	}
	if r := lena.Do(http.MethodPatch, "/api/v1/me", map[string]any{"preferences": map[string]any{
		"navigatorColumns": []string{"summary", "<script>"}}}); r.Code() != "profile.column" {
		t.Errorf("bad column: %d %s", r.Status, r.Body)
	}
}
