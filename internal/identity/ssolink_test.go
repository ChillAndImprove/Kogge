// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/oidctest"
)

// TestSingleSignOnTakesOverOnlyWhenSafe: an account without an identity — a placeholder of a migration, an account
// created ahead of the first sign-in — becomes the account of a user of the identity provider only when the
// provider asserts the account's e-mail address as verified, or when an administrator linked it to that user;
// otherwise the user gets an account of their own. The directory sync follows the same rules.
func TestSingleSignOnTakesOverOnlyWhenSafe(t *testing.T) {
	idp := oidctest.New(t, "kogge-web", "client-secret")
	env := apitest.New(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", ProviderName: "Company account",
			SyncClientID: idp.SyncClientID, SyncClientSecret: idp.SyncSecret, SyncAdminURL: idp.AdminURL}
	})
	ctx := context.Background()
	admin := env.Admin()
	// Placeholders as a migration leaves them: disabled, the source's e-mail address (if any), unverified.
	if _, err := env.App.Pool.Exec(ctx, `INSERT INTO users (username, display_name, kind, status, email) VALUES
		('olaf', 'Olaf Former', 'placeholder', 'disabled', NULL),
		('pia', 'Pia Peters', 'placeholder', 'disabled', 'pia@example.com'),
		('rolf.k', 'Rolf Kaiser', 'placeholder', 'disabled', 'rolf@example.com'),
		('olga', 'Olga Berg', 'placeholder', 'disabled', NULL),
		('sven', 'Sven Sync', 'placeholder', 'disabled', NULL)`); err != nil {
		t.Fatal(err)
	}
	signOn := func(u oidctest.User) string {
		t.Helper()
		idp.SignIn(u)
		jar, _ := cookiejar.New(nil)
		browser := &http.Client{Jar: jar}
		resp, err := browser.Get(env.URL + "/auth/oidc/login")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		resp, err = browser.Get(env.URL + "/api/v1/me")
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
	type account struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		SSOLink string `json:"ssoLink"`
	}
	lookup := func(username string) account {
		t.Helper()
		var a account
		if err := env.App.Pool.QueryRow(ctx, "SELECT id, kind, status, coalesce(sso_link, '') FROM users WHERE username = $1",
			username).Scan(&a.ID, &a.Kind, &a.Status, &a.SSOLink); err != nil {
			t.Fatalf("%s: %v", username, err)
		}
		return a
	}
	takeovers := func(by string) int {
		t.Helper()
		var n int
		if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action = 'user.sso-takeover' AND details->>'by' = $1",
			by).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The user name alone takes nothing over: the new olaf is someone else than the former colleague.
	if got := signOn(oidctest.User{Subject: "s-olaf", Username: "olaf", Name: "Olaf New", Email: "olaf@example.com"}); got != "olaf-2" {
		t.Errorf("the new olaf signed in as %q", got)
	}
	if a := lookup("olaf"); a.Kind != "placeholder" || a.Status != "disabled" {
		t.Errorf("the placeholder olaf was changed: %+v", a)
	}
	// Nor does an e-mail address the provider does not confirm.
	if got := signOn(oidctest.User{Subject: "s-pia", Username: "pia", Name: "Pia Peters", Email: "pia@example.com",
		EmailUnverified: true}); got != "pia-2" {
		t.Errorf("pia with an unverified address signed in as %q", got)
	}
	// A verified address does, whatever the user name at the provider.
	if got := signOn(oidctest.User{Subject: "s-rolf", Username: "rkaiser", Name: "Rolf Kaiser", Email: "ROLF@example.com"}); got != "rolf.k" {
		t.Errorf("rolf with a verified address signed in as %q", got)
	}
	if a := lookup("rolf.k"); a.Kind != "person" || a.Status != "active" || takeovers("verified-email") != 1 {
		t.Errorf("after rolf's takeover: %+v, %d audited", a, takeovers("verified-email"))
	}

	// An administrator links olga's placeholder to the provider's user o.berg, who has no e-mail address there.
	olga := lookup("olga")
	path := apitest.Fmt("/api/v1/admin/users/%d/sso-link", olga.ID)
	var linked account
	admin.MustDo(http.MethodPut, path, map[string]string{"username": " O.Berg "}, &linked)
	if linked.SSOLink != "O.Berg" {
		t.Errorf("link = %+v", linked)
	}
	if got := signOn(oidctest.User{Subject: "s-olga", Username: "o.berg", Name: "Olga Berg"}); got != "olga" {
		t.Errorf("o.berg signed in as %q", got)
	}
	if a := lookup("olga"); a.Kind != "person" || a.Status != "active" || a.SSOLink != "" || takeovers("administrator") != 1 {
		t.Errorf("after the linked takeover: %+v, %d audited", a, takeovers("administrator"))
	}
	var events []struct {
		Action  string         `json:"action"`
		Details map[string]any `json:"details"`
	}
	admin.Get(apitest.Fmt("/api/v1/admin/audit?target=user:%d", olga.ID), &events)
	actions := map[string]bool{}
	for _, e := range events {
		actions[e.Action] = true
	}
	if !actions["user.sso-link"] || !actions["user.sso-takeover"] {
		t.Errorf("audit of olga's account: %+v", events)
	}

	// Links that cannot take anything over are refused.
	sven := lookup("sven")
	svenPath := apitest.Fmt("/api/v1/admin/users/%d/sso-link", sven.ID)
	lena := env.CreateUser("lena")
	var lenaMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	lena.Get("/api/v1/me", &lenaMe)
	for _, c := range []struct {
		name, path, username, code string
	}{
		{"a local account", apitest.Fmt("/api/v1/admin/users/%d/sso-link", lenaMe.User.ID), "lena", "user.sso-link-password"},
		{"an account with single sign-on", path, "olga2", "user.sso-linked"},
		{"the system user", "/api/v1/admin/users/1/sso-link", "kogge", "user.sso-link-kind"},
		{"a user name of control characters", svenPath, "\t\n", "user.sso-link"},
		{"a placeholder", svenPath, "s.sync", ""},
		{"the same placeholder linked again", svenPath, "sven", ""},
	} {
		r := admin.Do(http.MethodPut, c.path, map[string]string{"username": c.username})
		if c.code == "" {
			if r.Status != http.StatusOK {
				t.Errorf("%s: %d %s", c.name, r.Status, r.Body)
			}
			continue
		}
		if r.Code() != c.code {
			t.Errorf("%s: %d %s", c.name, r.Status, r.Body)
		}
	}
	if r := admin.Do(http.MethodPut, apitest.Fmt("/api/v1/admin/users/%d/sso-link", lookup("olaf").ID),
		map[string]string{"username": "SVEN"}); r.Code() != "user.sso-link-taken" {
		t.Errorf("one provider user for two accounts: %d %s", r.Status, r.Body)
	}
	if r := lena.Do(http.MethodPut, svenPath, map[string]string{"username": "lena"}); r.Status != http.StatusForbidden {
		t.Errorf("a user links an account: %d", r.Status)
	}

	// The directory sync takes linked accounts over as well; a withdrawn link takes nothing over.
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/admin/users/%d/sso-link", lookup("olaf").ID), nil, nil)
	idp.SetDirectory([]oidctest.DirectoryUser{
		{ID: "s-sven", Username: "sven", FirstName: "Sven", Enabled: true},
		{ID: "s-pia2", Username: "pia.p", Email: "pia@example.com", EmailVerified: false, FirstName: "Pia", Enabled: true},
	}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/admin/directory-sync", nil, nil)
	if a := lookup("sven"); a.Kind != "person" || a.Status != "active" || a.SSOLink != "" {
		t.Errorf("the directory sync did not take the linked account over: %+v", a)
	}
	if a := lookup("pia"); a.Kind != "placeholder" {
		t.Errorf("the directory sync took a placeholder over by an unverified address: %+v", a)
	}
	var subject string
	if err := env.App.Pool.QueryRow(ctx, "SELECT coalesce(oidc_subject, '') FROM users WHERE username = 'sven'").Scan(&subject); err != nil ||
		subject != "s-sven" {
		t.Errorf("sven's identity: %q %v", subject, err)
	}
}
