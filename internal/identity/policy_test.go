// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"encoding/base32"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// enableTwoFactor sets up an authenticator app for the signed-in user and returns its secret.
func enableTwoFactor(t *testing.T, c *apitest.Client) []byte {
	t.Helper()
	var setup struct {
		Secret string `json:"secret"`
	}
	c.MustDo(http.MethodPost, "/api/v1/me/two-factor", nil, &setup)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(setup.Secret, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	c.MustDo(http.MethodPost, "/api/v1/me/two-factor/enable", map[string]string{"password": passwordOf(c),
		"code": crypto.TOTPCode(secret, crypto.TOTPStep(time.Now()))}, nil)
	return secret
}

func basic(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}

func passwordOf(c *apitest.Client) string {
	if c.Username == "admin" {
		return apitest.AdminPassword
	}
	return apitest.UserPassword
}

// TestRequireTwoFactor: with the setting, local accounts sign in with a second factor: who has none can, after
// the password, only set one up; Git takes no passwords. Single sign-on and access tokens are not affected, and
// whoever requires it has a second factor.
func TestRequireTwoFactor(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	dora, emil := env.CreateUser("dora"), env.CreateUser("emil")
	var token struct {
		Token string `json:"token"`
	}
	emil.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &token)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TFA", "name": "Two factors"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TFA/repos", map[string]any{"name": "app", "readme": true}, nil)

	set := func(on bool) *apitest.Response {
		t.Helper()
		var st map[string]any
		admin.Get("/api/v1/admin/settings", &st)
		st["requireTwoFactor"] = on
		return admin.Do(http.MethodPut, "/api/v1/admin/settings", st)
	}
	// Whoever requires a second factor has one.
	if r := set(true); r.Status != http.StatusConflict || r.Code() != "settings.two-factor-own" {
		t.Fatalf("required without a second factor of one's own: %d %s", r.Status, r.Body)
	}
	enableTwoFactor(t, admin)
	if r := set(true); r.Status != http.StatusOK {
		t.Fatalf("require: %d %s", r.Status, r.Body)
	}
	var events []struct {
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=settings.update", &events)
	if len(events) == 0 || events[0].Details["requireTwoFactor"] != true {
		t.Errorf("audit: %+v", events)
	}

	// dora's session, which began before, reaches nothing but the setup now; so does a new one.
	type me struct {
		TwoFactorSetup bool `json:"twoFactorSetup"`
	}
	var m me
	dora.Get("/api/v1/me", &m)
	if !m.TwoFactorSetup {
		t.Errorf("dora's /me does not ask for the setup")
	}
	if r := dora.Do(http.MethodGet, "/api/v1/code/repos", nil); r.Status != http.StatusForbidden || r.Code() != "auth.two-factor-setup" {
		t.Errorf("dora reached the API: %d %s", r.Status, r.Body)
	}
	fresh := env.Login("dora", apitest.UserPassword)
	for _, path := range []string{"/api/v1/projects", "/api/v1/notifications", "/api/v1/me/tokens"} {
		if r := fresh.Do(http.MethodGet, path, nil); r.Status != http.StatusForbidden || r.Code() != "auth.two-factor-setup" {
			t.Errorf("%s during the setup: %d %s", path, r.Status, r.Body)
		}
	}
	if r := fresh.Do(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "sneak", "scopes": []string{"read"}}); r.Status != http.StatusForbidden {
		t.Errorf("a token was created during the setup: %d %s", r.Status, r.Body)
	}
	enableTwoFactor(t, fresh)
	m = me{}
	fresh.Get("/api/v1/me", &m)
	if m.TwoFactorSetup {
		t.Errorf("still asked for the setup")
	}
	if r := fresh.Do(http.MethodGet, "/api/v1/code/repos", nil); r.Status != http.StatusOK {
		t.Errorf("after the setup: %d %s", r.Status, r.Body)
	}

	// Access tokens keep working; Git takes no passwords.
	if r := env.Anonymous().Do(http.MethodGet, "/api/v1/me", nil, "Authorization", "Bearer "+token.Token); r.Status != http.StatusOK {
		t.Errorf("token: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/TFA/app/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": userID(t, emil)}, "permission": "repo.read"}}, nil)
	gitWithPassword := func() *apitest.Response {
		return env.Anonymous().Do(http.MethodGet, "/scm/TFA/app.git/info/refs?service=git-upload-pack", nil,
			"Authorization", "Basic "+basic("emil", apitest.UserPassword))
	}
	if r := gitWithPassword(); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "personal access token") {
		t.Errorf("Git with a password: %d %s", r.Status, r.Body)
	}

	// Without the setting, emil's password session reaches everything again.
	if r := set(false); r.Status != http.StatusOK {
		t.Fatalf("no longer require: %d %s", r.Status, r.Body)
	}
	if r := emil.Do(http.MethodGet, "/api/v1/code/repos", nil); r.Status != http.StatusOK {
		t.Errorf("without the setting: %d %s", r.Status, r.Body)
	}
	if r := gitWithPassword(); r.Status != http.StatusOK {
		t.Errorf("Git with a password, without the setting: %d %s", r.Status, r.Body)
	}
}

func userID(t *testing.T, c *apitest.Client) int64 {
	t.Helper()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	c.Get("/api/v1/me", &me)
	return me.User.ID
}
