// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package oidctest is an OpenID Connect provider for tests: discovery, keys, the authorization endpoint (which
// signs in the configured user at once), the token endpoint with PKCE, and the parts of Keycloak's admin API that
// the directory sync reads (users, groups, members) for a service account.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// User is who signs in at the provider. Its e-mail address is verified unless EmailUnverified says otherwise.
type User struct {
	Subject         string
	Username        string
	Name            string
	Email           string
	EmailUnverified bool
	Groups          []string
}

// DirectoryUser is a user of the directory; its id is the subject of its sign-ins.
type DirectoryUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Enabled       bool   `json:"enabled"`
}

// DirectoryGroup is a group of the directory with the ids of its members and its subgroups.
type DirectoryGroup struct {
	ID       string
	Name     string
	Members  []string
	Children []DirectoryGroup
}

// Provider is a running test provider. AdminURL is the base of its admin API; the service account is SyncClientID
// with SyncSecret.
type Provider struct {
	*httptest.Server
	ClientID, ClientSecret   string
	SyncClientID, SyncSecret string
	AdminURL                 string

	key    *rsa.PrivateKey
	signer jose.Signer

	mu         sync.Mutex
	user       User
	codes      map[string]grant
	users      []DirectoryUser
	groups     []DirectoryGroup
	adminToken string
}

type grant struct {
	user                 User
	nonce, challenge     string
	clientID, redirectTo string
}

// New starts a provider for the client.
func New(t testing.TB, clientID, clientSecret string) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{ClientID: clientID, ClientSecret: clientSecret, SyncClientID: "kogge-sync", SyncSecret: "sync-secret", key: key,
		signer: signer, codes: map[string]grant{}, adminToken: rand.Text()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /auth", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /admin/realms/test/users", p.admin(p.listUsers))
	mux.HandleFunc("GET /admin/realms/test/groups", p.admin(p.listGroups))
	mux.HandleFunc("GET /admin/realms/test/groups/{id}/children", p.admin(p.listChildren))
	mux.HandleFunc("GET /admin/realms/test/groups/{id}/members", p.admin(p.listMembers))
	p.Server = httptest.NewServer(mux)
	p.AdminURL = p.URL + "/admin/realms/test"
	t.Cleanup(p.Close)
	return p
}

// SetDirectory sets the users and groups the admin API returns.
func (p *Provider) SetDirectory(users []DirectoryUser, groups []DirectoryGroup) {
	p.mu.Lock()
	p.users, p.groups = users, groups
	p.mu.Unlock()
}

func (p *Provider) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+p.adminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		next(w, r)
	}
}

// paged returns the slice of list that first and max select.
func paged[T any](r *http.Request, list []T) []T {
	first, _ := strconv.Atoi(r.URL.Query().Get("first"))
	n, _ := strconv.Atoi(r.URL.Query().Get("max"))
	if n <= 0 {
		n = 100
	}
	if first >= len(list) {
		return []T{}
	}
	return list[first:min(first+n, len(list))]
}

func (p *Provider) listUsers(w http.ResponseWriter, r *http.Request) { writeJSON(w, paged(r, p.users)) }

type groupJSON struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	SubGroupCount int    `json:"subGroupCount"`
	SubGroups     []any  `json:"subGroups"`
}

// Groups are listed like Keycloak 23 and later: subgroups only through /children.
func groupsJSON(parent string, list []DirectoryGroup) []groupJSON {
	out := []groupJSON{}
	for _, g := range list {
		out = append(out, groupJSON{ID: g.ID, Name: g.Name, Path: parent + "/" + g.Name, SubGroupCount: len(g.Children), SubGroups: []any{}})
	}
	return out
}

func findGroup(list []DirectoryGroup, id, parent string) (*DirectoryGroup, string) {
	for i := range list {
		if list[i].ID == id {
			return &list[i], parent + "/" + list[i].Name
		}
		if g, path := findGroup(list[i].Children, id, parent+"/"+list[i].Name); g != nil {
			return g, path
		}
	}
	return nil, ""
}

func (p *Provider) listGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, paged(r, groupsJSON("", p.groups)))
}

func (p *Provider) listChildren(w http.ResponseWriter, r *http.Request) {
	g, path := findGroup(p.groups, r.PathValue("id"), "")
	if g == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, paged(r, groupsJSON(path, g.Children)))
}

func (p *Provider) listMembers(w http.ResponseWriter, r *http.Request) {
	g, _ := findGroup(p.groups, r.PathValue("id"), "")
	if g == nil {
		http.NotFound(w, r)
		return
	}
	var members []DirectoryUser
	for _, id := range g.Members {
		members = append(members, DirectoryUser{ID: id})
	}
	writeJSON(w, paged(r, members))
}

// SignIn sets the user that the next sign-ins authenticate as.
func (p *Provider) SignIn(u User) {
	p.mu.Lock()
	p.user = u
	p.mu.Unlock()
}

// LogoutToken signs a back-channel logout token for the client: the issuer, audience and event, the time of
// issue (now) and a random id, with claims added or replaced; a claim set to nil is left out.
func (p *Provider) LogoutToken(t testing.TB, claims map[string]any) string {
	t.Helper()
	all := map[string]any{"iss": p.URL, "aud": p.ClientID, "iat": time.Now().Unix(), "jti": rand.Text(),
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}}
	for k, v := range claims {
		if v == nil {
			delete(all, k)
		} else {
			all[k] = v
		}
	}
	raw, err := jwt.Signed(p.signer).Claims(all).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.URL, "authorization_endpoint": p.URL + "/auth", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported": []string{"S256"}, "backchannel_logout_supported": true,
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

// authorize signs the current user in and sends the browser back with a code.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	p.mu.Lock()
	p.codes[code] = grant{user: p.user, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), clientID: q.Get("client_id"),
		redirectTo: q.Get("redirect_uri")}
	p.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.FormValue("client_id"), r.FormValue("client_secret")
	}
	if r.FormValue("grant_type") == "client_credentials" {
		// Like Keycloak: the sign-in client has no service account (unauthorized_client) — unless its secret is
		// wrong (invalid_client).
		if id == p.ClientID && secret == p.ClientSecret {
			http.Error(w, `{"error":"unauthorized_client"}`, http.StatusBadRequest)
			return
		}
		if id != p.SyncClientID || secret != p.SyncSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"access_token": p.adminToken, "token_type": "Bearer", "expires_in": 300})
		return
	}
	if id != p.ClientID || secret != p.ClientSecret || r.FormValue("grant_type") != "authorization_code" {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	g, ok := p.codes[r.FormValue("code")]
	delete(p.codes, r.FormValue("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	if !ok || g.redirectTo != r.FormValue("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.URL, "sub": g.user.Subject, "aud": g.clientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce, "preferred_username": g.user.Username, "name": g.user.Name, "email": g.user.Email,
		"email_verified": !g.user.EmailUnverified, "groups": g.user.Groups, "sid": rand.Text(),
	}
	raw, err := jwt.Signed(p.signer).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 300, "id_token": raw})
}
