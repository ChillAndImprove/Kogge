// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/oidctest"
)

// TestSingleSignOn signs in through an OpenID Connect provider and checks that a login can complete only in the
// browser that started it (no login CSRF).
func TestSingleSignOn(t *testing.T) {
	idp := oidctest.New(t, "kogge-web", "client-secret")
	env := apitest.New(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", ProviderName: "Company account"}
	})
	// A local group with administrative grants; the provider has a group of the same name.
	admin := env.Admin()
	var group struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Platform Admins"}, &group)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "admin", "principalType": "group",
		"principalId": group.ID}, nil)
	idp.SignIn(oidctest.User{Subject: "s-erika", Username: "erika", Name: "Erika Mustermann", Email: "erika@example.com",
		Groups: []string{"Engineering/Platform", "Platform Admins"}})

	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}
	}
	whoami := func(c *http.Client) string {
		t.Helper()
		resp, err := c.Get(env.URL + "/api/v1/me")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var me struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&me)
		}
		return me.User.Username
	}

	// A sign-in returns to the page the user came from.
	erika := browser()
	resp, err := erika.Get(env.URL + "/auth/oidc/login?return=/browse/SHOP-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/browse/SHOP-1" {
		t.Errorf("returned to %s", resp.Request.URL)
	}
	if got := whoami(erika); got != "erika" {
		t.Fatalf("signed in as %q", got)
	}
	if resp, err := erika.Get(env.URL + "/api/v1/admin/users"); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("the provider's group made the user an administrator: %v %v", resp.StatusCode, err)
	}

	// An attacker signs in at the provider and sends the callback address to a victim: the victim's browser is not
	// signed in with the attacker's account.
	attacker := browser()
	attacker.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if req.URL.Path == identity.CallbackPath {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err = attacker.Get(env.URL + "/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	callback := resp.Header.Get("Location")
	if callback == "" {
		t.Fatalf("no callback address: %d", resp.StatusCode)
	}
	victim := browser()
	resp, err = victim.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/login" || resp.Request.URL.Query().Get("error") != "sso" {
		t.Errorf("the victim's callback ended at %s", resp.Request.URL)
	}
	if got := whoami(victim); got != "" {
		t.Errorf("the victim is signed in as %q", got)
	}

	// A placeholder from a migration is taken over only by a sign-in with its e-mail address: a reused user name
	// does not inherit a former colleague's account.
	if _, err := env.App.Pool.Exec(context.Background(), `INSERT INTO users (username, display_name, kind, status, email, email_verified)
		VALUES ('max', 'Max Former', 'placeholder', 'disabled', 'max@former.example', true),
		       ('jana', 'Jana', 'placeholder', 'disabled', 'jana@example.com', true)`); err != nil {
		t.Fatal(err)
	}
	idp.SignIn(oidctest.User{Subject: "s-max", Username: "max", Name: "Max New", Email: "max@example.com"})
	maxBrowser := browser()
	if _, err := maxBrowser.Get(env.URL + "/auth/oidc/login"); err != nil {
		t.Fatal(err)
	}
	if got := whoami(maxBrowser); got != "max-2" {
		t.Errorf("the new max signed in as %q", got)
	}
	idp.SignIn(oidctest.User{Subject: "s-jana", Username: "jana", Name: "Jana", Email: "jana@example.com"})
	jana := browser()
	if _, err := jana.Get(env.URL + "/auth/oidc/login"); err != nil {
		t.Fatal(err)
	}
	if got := whoami(jana); got != "jana" {
		t.Errorf("jana signed in as %q", got)
	}

	// Starting sign-ons stores login states: one address cannot start them without end (audit L3).
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		resp, err := noRedirect.Get(env.URL + "/auth/oidc/login")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		limited = resp.StatusCode == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("sign-ons can be started without limit")
	}
}

// TestSSOAdministration: administrators see the configuration of single sign-on with the addresses to register at
// the identity provider, and a check of the connection finds a wrong client secret.
// TestBackchannelLogout: the identity provider ends a person's sessions with a logout token, which is accepted only
// when it is new (iat), and once (jti); tokens without them are refused.
func TestBackchannelLogout(t *testing.T) {
	idp := oidctest.New(t, "kogge-web", "client-secret")
	env := apitest.New(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "profile", "email"}}
	})
	idp.SignIn(oidctest.User{Subject: "s-ida", Username: "ida", Name: "Ida", Email: "ida@example.com"})
	jar, _ := cookiejar.New(nil)
	ida := &http.Client{Jar: jar}
	signIn := func() {
		t.Helper()
		resp, err := ida.Get(env.URL + "/auth/oidc/login")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	signedIn := func() bool {
		t.Helper()
		resp, err := ida.Get(env.URL + "/api/v1/me")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	logout := func(token string) int {
		t.Helper()
		resp, err := http.PostForm(env.URL+identity.BackchannelLogoutPath, url.Values{"logout_token": {token}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	signIn()
	if !signedIn() {
		t.Fatal("not signed in")
	}
	now := time.Now()
	for name, claims := range map[string]map[string]any{
		"without iat":       {"sub": "s-ida", "iat": nil},
		"old":               {"sub": "s-ida", "iat": now.Add(-20 * time.Minute).Unix()},
		"from the future":   {"sub": "s-ida", "iat": now.Add(10 * time.Minute).Unix()},
		"without jti":       {"sub": "s-ida", "jti": nil},
		"without the event": {"sub": "s-ida", "events": map[string]any{}},
	} {
		if status := logout(idp.LogoutToken(t, claims)); status != http.StatusBadRequest || !signedIn() {
			t.Errorf("a logout token %s: %d, signed in: %v", name, status, signedIn())
		}
	}
	token := idp.LogoutToken(t, map[string]any{"sub": "s-ida", "jti": "logout-1"})
	if status := logout(token); status != http.StatusOK || signedIn() {
		t.Fatalf("a logout token: %d, signed in: %v", status, signedIn())
	}
	// The same token again does not end the next session.
	signIn()
	if status := logout(token); status != http.StatusBadRequest || !signedIn() {
		t.Errorf("a replayed logout token: %d, signed in: %v", status, signedIn())
	}
	if status := logout(idp.LogoutToken(t, map[string]any{"sub": "s-ida"})); status != http.StatusOK || signedIn() {
		t.Errorf("another logout token: %d, signed in: %v", status, signedIn())
	}
}

func TestSSOAdministration(t *testing.T) {
	idp := oidctest.New(t, "kogge-web", "client-secret")
	configure := func(secret string) *apitest.Env {
		return apitest.New(t, func(c *config.Config) {
			c.OIDC = config.OIDC{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: secret, Scopes: []string{"openid", "profile"},
				GroupsClaim: "groups", ProviderName: "Company account"}
		})
	}
	env := configure(idp.ClientSecret)
	admin := env.Admin()
	var info struct {
		Configured           bool   `json:"configured"`
		Issuer               string `json:"issuer"`
		RedirectURI          string `json:"redirectUri"`
		BackchannelLogoutURI string `json:"backchannelLogoutUri"`
		Sync                 bool   `json:"sync"`
	}
	admin.Get("/api/v1/admin/sso", &info)
	if !info.Configured || info.Issuer != idp.URL || info.RedirectURI != env.URL+identity.CallbackPath ||
		info.BackchannelLogoutURI != env.URL+identity.BackchannelLogoutPath || info.Sync {
		t.Errorf("info = %+v", info)
	}
	type check struct {
		Key    string `json:"key"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	run := func(c *apitest.Client) map[string]check {
		var list []check
		c.MustDo(http.MethodPost, "/api/v1/admin/sso/check", nil, &list)
		out := map[string]check{}
		for _, c := range list {
			out[c.Key] = c
		}
		return out
	}
	got := run(admin)
	for _, key := range []string{"discovery", "issuer", "keys", "pkce", "client", "backchannel-logout"} {
		if !got[key].OK {
			t.Errorf("%s: %+v", key, got[key])
		}
	}
	if _, ok := got["sync"]; ok {
		t.Error("the sync was checked without being configured")
	}
	// A wrong secret.
	wrong := configure("not-the-secret")
	if c := run(wrong.Admin())["client"]; c.OK {
		t.Errorf("a wrong secret passed: %+v", c)
	}
	// Only administrators.
	if r := env.CreateUser("eve").Do(http.MethodPost, "/api/v1/admin/sso/check", nil); r.Status != http.StatusForbidden {
		t.Errorf("a user checks: %d", r.Status)
	}
}
