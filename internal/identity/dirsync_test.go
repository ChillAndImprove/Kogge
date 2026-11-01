// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/oidctest"
)

// TestDirectorySync: users and groups of the directory reach Kogge before the first sign-in; users removed or
// disabled in the directory are disabled (their sessions end) and enabled again when they come back, unless an
// administrator disabled them; a sync that would disable most users is refused.
func TestDirectorySync(t *testing.T) {
	ctx := context.Background()
	idp := oidctest.New(t, "kogge-web", "client-secret")
	env := apitest.New(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", ProviderName: "Company account",
			SyncClientID: idp.SyncClientID, SyncClientSecret: idp.SyncSecret, SyncAdminURL: idp.AdminURL}
	})
	admin := env.Admin()
	erika := oidctest.DirectoryUser{ID: "u-erika", Username: "erika", Email: "erika@example.com", FirstName: "Erika", LastName: "Muster",
		Enabled: true}
	max := oidctest.DirectoryUser{ID: "u-max", Username: "max", Email: "max@example.com", FirstName: "Max", Enabled: true}
	ben := oidctest.DirectoryUser{ID: "u-ben", Username: "ben", Email: "ben@example.com", FirstName: "Ben", Enabled: false}
	platform := oidctest.DirectoryGroup{ID: "g-platform", Name: "Platform", Members: []string{"u-erika"}}
	groups := []oidctest.DirectoryGroup{{ID: "g-eng", Name: "Engineering", Children: []oidctest.DirectoryGroup{platform}}}
	idp.SetDirectory([]oidctest.DirectoryUser{erika, max, ben}, groups)

	type result struct {
		Users, Created, Disabled, Enabled, Groups int
		Error                                     string
	}
	sync := func(force bool) (result, *apitest.Response) {
		t.Helper()
		var r result
		path := "/api/v1/admin/directory-sync"
		if force {
			path += "?force=true"
		}
		res := admin.Do(http.MethodPost, path, nil)
		if res.Status == http.StatusOK {
			res.Decode(&r)
		}
		return r, res
	}
	status := func(username string) string {
		t.Helper()
		var s string
		if err := env.App.Pool.QueryRow(ctx, "SELECT status FROM users WHERE username = $1", username).Scan(&s); err != nil {
			return "none"
		}
		return s
	}

	r, res := sync(false)
	if res.Status != http.StatusOK || r.Users != 3 || r.Created != 2 || r.Disabled != 0 || r.Groups != 2 {
		t.Fatalf("first sync: %d %s %+v", res.Status, res.Body, r)
	}
	if status("erika") != "active" || status("max") != "active" || status("ben") != "none" {
		t.Errorf("users: erika %s, max %s, ben %s", status("erika"), status("max"), status("ben"))
	}
	var member bool
	if err := env.App.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM group_members m JOIN groups g ON g.id = m.group_id
		JOIN users u ON u.id = m.user_id WHERE g.name = 'Engineering/Platform' AND g.source = 'oidc' AND u.username = 'erika')`).Scan(&member); err != nil || !member {
		t.Errorf("erika is not in Engineering/Platform: %v", err)
	}

	// Erika signs in; then she is disabled in the directory and max is removed from it.
	idp.SignIn(oidctest.User{Subject: "u-erika", Username: "erika", Name: "Erika Muster", Email: "erika@example.com",
		Groups: []string{"Engineering/Platform"}})
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	if _, err := browser.Get(env.URL + "/auth/oidc/login"); err != nil {
		t.Fatal(err)
	}
	signedIn := func() bool {
		resp, err := browser.Get(env.URL + "/api/v1/me")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	if !signedIn() {
		t.Fatal("erika could not sign in")
	}
	erika.Enabled = false
	idp.SetDirectory([]oidctest.DirectoryUser{erika, ben}, groups)
	if r, res = sync(false); res.Status != http.StatusOK || r.Disabled != 2 {
		t.Fatalf("offboarding: %d %s %+v", res.Status, res.Body, r)
	}
	if status("erika") != "disabled" || status("max") != "disabled" || signedIn() {
		t.Errorf("after offboarding: erika %s, max %s, signed in %v", status("erika"), status("max"), signedIn())
	}

	// Both come back, but an administrator disabled max in the meantime.
	var maxID int64
	if err := env.App.Pool.QueryRow(ctx, "SELECT id FROM users WHERE username = 'max'").Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	admin.MustDo(http.MethodPatch, apitest.Fmt("/api/v1/admin/users/%d", maxID), map[string]any{"status": "disabled"}, nil)
	erika.Enabled = true
	idp.SetDirectory([]oidctest.DirectoryUser{erika, max, ben}, groups)
	if r, res = sync(false); res.Status != http.StatusOK || r.Enabled != 1 {
		t.Fatalf("return: %d %s %+v", res.Status, res.Body, r)
	}
	if status("erika") != "active" || status("max") != "disabled" {
		t.Errorf("after the return: erika %s, max %s", status("erika"), status("max"))
	}

	// An empty answer of the directory changes nothing.
	idp.SetDirectory(nil, nil)
	if _, res = sync(false); res.Status == http.StatusOK || status("erika") != "active" {
		t.Errorf("empty directory: %d, erika %s", res.Status, status("erika"))
	}
	var state struct {
		Configured bool `json:"configured"`
		Last       *struct {
			Error string `json:"error"`
		} `json:"last"`
	}
	admin.Get("/api/v1/admin/directory-sync", &state)
	if !state.Configured || state.Last == nil || state.Last.Error == "" {
		t.Errorf("state = %+v", state)
	}
}
