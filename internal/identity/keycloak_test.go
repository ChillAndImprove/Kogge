// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// keycloakImage is the Keycloak of deploy/compose.yaml.
const keycloakImage = "quay.io/keycloak/keycloak:26.4"

// keycloak is a Keycloak in a container with the realm of deploy/keycloak, and its admin API.
type keycloak struct {
	t     *testing.T
	URL   string
	token string
}

// startKeycloak starts Keycloak for a Kogge at kogge (its public and internal address) with the realm Kogge's
// deployment imports; it is removed when the test ends.
func startKeycloak(t *testing.T, port int, kogge, oidcSecret, syncSecret, adminPassword string) *keycloak {
	t.Helper()
	realm, err := os.ReadFile("../../deploy/keycloak/kogge-realm.json")
	if err != nil {
		t.Fatal(err)
	}
	// The realm goes into the container as a tar archive on standard input: the Docker CLI may not see this
	// process's temporary directory (snap packages have their own).
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	_ = tw.WriteHeader(&tar.Header{Name: "import/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "import/kogge-realm.json", Mode: 0o644, Size: int64(len(realm))})
	_, _ = tw.Write(realm)
	_ = tw.Close()

	name := fmt.Sprintf("kogge-test-keycloak-%d", time.Now().UnixNano())
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	docker := func(stdin io.Reader, args ...string) {
		t.Helper()
		cmd := exec.Command("docker", args...)
		cmd.Stdin = stdin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	docker(nil, "create", "--name", name, "--network", "host",
		"-e", "KC_BOOTSTRAP_ADMIN_USERNAME=admin", "-e", "KC_BOOTSTRAP_ADMIN_PASSWORD="+adminPassword,
		"-e", "KOGGE_PUBLIC_URL="+kogge, "-e", "KOGGE_INTERNAL_URL="+kogge,
		"-e", "KOGGE_OIDC_CLIENT_SECRET="+oidcSecret, "-e", "KOGGE_SYNC_CLIENT_SECRET="+syncSecret,
		keycloakImage, "start-dev", "--import-realm", fmt.Sprintf("--http-port=%d", port), "--hostname="+base)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	docker(&archive, "cp", "-", name+":/opt/keycloak/data/")
	docker(nil, "start", name)

	kc := &keycloak{t: t, URL: base}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := http.Get(base + "/realms/kogge/.well-known/openid-configuration")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", "--tail", "40", name).CombinedOutput()
			t.Fatalf("Keycloak did not start:\n%s", out)
		}
		time.Sleep(time.Second)
	}
	resp, err := http.PostForm(base+"/realms/master/protocol/openid-connect/token", url.Values{"grant_type": {"password"},
		"client_id": {"admin-cli"}, "username": {"admin"}, "password": {adminPassword}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("admin token: %d %v", resp.StatusCode, err)
	}
	kc.token = tok.AccessToken
	return kc
}

// admin calls the admin API of the realm; it returns the response's Location (ids of new objects).
func (k *keycloak) admin(method, path string, body, out any) string {
	k.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, k.URL+"/admin/realms/kogge"+path, rd)
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		k.t.Fatalf("keycloak %s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp.Header.Get("Location")
}

func lastSegment(location string) string { return location[strings.LastIndex(location, "/")+1:] }

// user creates a user of the directory with a password, in groups (paths).
func (k *keycloak) user(username, first, last, password string, groups ...string) string {
	return lastSegment(k.admin(http.MethodPost, "/users", map[string]any{"username": username, "firstName": first, "lastName": last,
		"email": username + "@example.com", "emailVerified": true, "enabled": true, "groups": groups,
		"credentials": []map[string]any{{"type": "password", "value": password, "temporary": false}}}, nil))
}

var loginForm = regexp.MustCompile(`<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// TestKeycloak signs in through a real Keycloak with the realm of Kogge's deployment (deploy/keycloak): the
// authorization code flow with PKCE, groups by path, back-channel logout and the directory sync. It runs where
// Docker and the image of the deployment's Keycloak are present.
func TestKeycloak(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Keycloak")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI")
	}
	if err := exec.Command("docker", "image", "inspect", keycloakImage).Run(); err != nil {
		t.Skipf("the image %s is not present", keycloakImage)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	oidcSecret, syncSecret := crypto.RandomString(24), crypto.RandomString(24)

	// Kogge first: it asks the provider only when someone signs in, and Keycloak's realm needs Kogge's address.
	env := apitest.New(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Issuer: fmt.Sprintf("http://127.0.0.1:%d/realms/kogge", port), ClientID: "kogge-web",
			ClientSecret: oidcSecret, Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups",
			ProviderName: "Company account", SyncClientID: "kogge-sync", SyncClientSecret: syncSecret}
	})
	kc := startKeycloak(t, port, env.URL, oidcSecret, syncSecret, "Keycloak-Admin-Secret-1")
	// The administration's check of the connection finds nothing wrong.
	var checks []struct {
		Key    string `json:"key"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/sso/check", nil, &checks)
	if len(checks) != 7 {
		t.Errorf("checks = %+v", checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s: %s", c.Key, c.Detail)
		}
	}
	engineering := lastSegment(kc.admin(http.MethodPost, "/groups", map[string]any{"name": "Engineering"}, nil))
	kc.admin(http.MethodPost, "/groups/"+engineering+"/children", map[string]any{"name": "Platform"}, nil)
	const password = "Directory-Secret-2026"
	erika := kc.user("erika", "Erika", "Mustermann", password, "/Engineering/Platform")

	// The sign-in: Kogge sends the browser to Keycloak with PKCE, Keycloak's form signs in, Kogge's callback
	// creates the session.
	jar, _ := cookiejar.New(nil)
	var hops []string
	browser := &http.Client{Jar: jar, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		hops = append(hops, r.URL.String())
		return nil
	}}
	resp, err := browser.Get(env.URL + "/auth/oidc/login?return=/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(hops) == 0 || !strings.Contains(hops[0], "code_challenge_method=S256") || !strings.Contains(hops[0], "code_challenge=") {
		t.Fatalf("the authorization request has no PKCE: %v", hops)
	}
	m := loginForm.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no login form: %d %.500s", resp.StatusCode, page)
	}
	resp, err = browser.PostForm(html.UnescapeString(string(m[1])), url.Values{"username": {"erika"}, "password": {password},
		"credentialId": {""}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var me struct {
		User struct {
			Username    string `json:"username"`
			DisplayName string `json:"displayName"`
			External    bool   `json:"external"`
		} `json:"user"`
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	resp, err = browser.Get(env.URL + "/api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || me.User.Username != "erika" || me.User.DisplayName != "Erika Mustermann" || !me.User.External {
		t.Fatalf("after the sign-in: %d %+v (hops %v)", resp.StatusCode, me, hops)
	}
	groups := []string{}
	for _, g := range me.Groups {
		groups = append(groups, g.Name)
	}
	if strings.Join(groups, ",") != "Engineering/Platform" {
		t.Errorf("groups = %v", groups)
	}

	// Back-channel logout: ending the user's Keycloak sessions ends Kogge's.
	kc.admin(http.MethodPost, "/users/"+erika+"/logout", nil, nil)
	ended := false
	for i := 0; i < 40 && !ended; i++ {
		resp, err := browser.Get(env.URL + "/api/v1/me")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		ended = resp.StatusCode == http.StatusUnauthorized
		if !ended {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !ended {
		t.Error("the Kogge session survived the logout in Keycloak")
	}

	// The directory sync creates users before their first sign-in and disables those disabled in the directory.
	ids := map[string]string{}
	for _, u := range []string{"anna", "ben", "carla", "dirk"} {
		ids[u] = kc.user(u, strings.ToUpper(u[:1])+u[1:], "Tester", password)
	}
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/admin/directory-sync", nil, nil)
	status := func(username string) string {
		var page struct {
			Items []struct {
				Username string `json:"username"`
				Status   string `json:"status"`
			} `json:"items"`
		}
		admin.Get("/api/v1/admin/users?query="+username, &page)
		for _, u := range page.Items {
			if u.Username == username {
				return u.Status
			}
		}
		return ""
	}
	for _, u := range []string{"anna", "ben", "carla", "dirk"} {
		if got := status(u); got != "active" {
			t.Errorf("%s after the sync: %q", u, got)
		}
	}
	kc.admin(http.MethodPut, "/users/"+ids["dirk"], map[string]any{"enabled": false}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/admin/directory-sync", nil, nil)
	if got := status("dirk"); got != "disabled" {
		t.Errorf("dirk disabled in the directory: %q", got)
	}
	if got := status("anna"); got != "active" {
		t.Errorf("anna: %q", got)
	}
}
