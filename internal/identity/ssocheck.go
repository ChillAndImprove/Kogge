// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The administration's view of single sign-on: what Kogge is configured with, what to register at the identity
// provider, and a check of the connection — what an administrator would otherwise find out by trying.

// SSOInfo is the configuration of single sign-on (without secrets).
type SSOInfo struct {
	Configured   bool     `json:"configured"`
	ProviderName string   `json:"providerName,omitempty"`
	Issuer       string   `json:"issuer,omitempty"`
	InternalURL  string   `json:"internalUrl,omitempty" doc:"Where Kogge reaches the provider's token and key endpoints"`
	ClientID     string   `json:"clientId,omitempty"`
	Scopes       []string `json:"scopes"`
	GroupsClaim  string   `json:"groupsClaim,omitempty"`
	// The addresses to register at the identity provider.
	RedirectURI          string `json:"redirectUri"`
	BackchannelLogoutURI string `json:"backchannelLogoutUri"`
	PostLogoutRedirect   string `json:"postLogoutRedirect"`
	Sync                 bool   `json:"sync" doc:"The directory sync is configured"`
	SyncClientID         string `json:"syncClientId,omitempty"`
	SyncAdminURL         string `json:"syncAdminUrl,omitempty"`
	LocalLogin           bool   `json:"localLogin" doc:"Local accounts may sign in with passwords"`
}

// SSOCheck is the result of one check of the connection to the identity provider.
type SSOCheck struct {
	Key    string `json:"key" enum:"discovery,issuer,keys,pkce,client,backchannel-logout,sync"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// SSO returns the configuration of single sign-on.
func (s *Service) SSO() SSOInfo {
	c := s.cfg.OIDC
	base := strings.TrimRight(s.cfg.PublicURL.String(), "/")
	info := SSOInfo{Configured: c.Enabled(), Scopes: []string{}, RedirectURI: base + CallbackPath,
		BackchannelLogoutURI: base + BackchannelLogoutPath, PostLogoutRedirect: base + "/*", LocalLogin: s.cfg.LocalLogin}
	if !info.Configured {
		return info
	}
	info.ProviderName, info.Issuer, info.InternalURL, info.ClientID = c.ProviderName, c.Issuer, c.InternalURL, c.ClientID
	info.Scopes = append(info.Scopes, c.Scopes...)
	info.GroupsClaim = c.GroupsClaim
	info.Sync = c.SyncEnabled()
	if info.Sync {
		info.SyncClientID, info.SyncAdminURL = c.SyncClientID, c.AdminURL()
	}
	return info
}

// CheckSSO checks the connection to the identity provider: its discovery document and keys, the issuer, PKCE,
// the client and its secret, back-channel logout, and the service account of the directory sync.
func (s *Service) CheckSSO(ctx context.Context) []SSOCheck {
	if s.oidc == nil {
		return []SSOCheck{}
	}
	return s.oidc.check(ctx, s.cfg.OIDC.SyncEnabled())
}

func (o *OIDC) check(ctx context.Context, sync bool) []SSOCheck {
	out := []SSOCheck{}
	add := func(key string, ok bool, format string, args ...any) {
		out = append(out, SSOCheck{Key: key, OK: ok, Detail: fmt.Sprintf(format, args...)})
	}
	discoveryURL := o.internalize(o.cfg.Issuer + "/.well-known/openid-configuration")
	var d struct {
		discovery
		CodeChallengeMethods []string `json:"code_challenge_methods_supported"`
		BackchannelLogout    bool     `json:"backchannel_logout_supported"`
	}
	if err := o.getJSON(ctx, discoveryURL, &d); err != nil {
		add("discovery", false, "%s: %v", discoveryURL, err)
		return out
	}
	add("discovery", d.AuthorizationEndpoint != "" && d.TokenEndpoint != "" && d.JWKSURI != "", "%s", discoveryURL)
	add("issuer", strings.TrimRight(d.Issuer, "/") == o.cfg.Issuer, "The provider reports %s; Kogge is configured with %s.", d.Issuer,
		o.cfg.Issuer)
	var keys struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := o.getJSON(ctx, o.internalize(d.JWKSURI), &keys); err != nil {
		add("keys", false, "%v", err)
	} else {
		add("keys", len(keys.Keys) > 0, "%d signing keys", len(keys.Keys))
	}
	add("pkce", slices.Contains(d.CodeChallengeMethods, "S256"), "Supported methods: %s", strings.Join(d.CodeChallengeMethods, ", "))
	add("backchannel-logout", d.BackchannelLogout, "%s", map[bool]string{true: "Supported", false: "Not announced by the provider"}[d.BackchannelLogout])

	// The client and its secret: a token request that the provider refuses for the grant, not for the client,
	// proves the secret (a confidential client without a service account gets unauthorized_client).
	tokenURL := o.internalize(d.TokenEndpoint)
	status, code, err := o.tokenRequest(ctx, tokenURL, o.cfg.ClientID, o.cfg.ClientSecret)
	switch {
	case err != nil:
		add("client", false, "%v", err)
	case status == http.StatusOK || code == "unauthorized_client" || code == "unsupported_grant_type":
		add("client", true, "The provider accepts the client %s and its secret.", o.cfg.ClientID)
	default:
		add("client", false, "The provider refuses the client %s (%d %s): check the client secret.", o.cfg.ClientID, status, code)
	}
	if sync {
		o.checkSync(ctx, tokenURL, add)
	}
	return out
}

// checkSync signs in with the service account of the directory sync and reads one user.
func (o *OIDC) checkSync(ctx context.Context, tokenURL string, add func(key string, ok bool, format string, args ...any)) {
	clientID, secret := o.cfg.SyncClientID, o.cfg.SyncClientSecret
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		add("sync", false, "%v", err)
		return
	}
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		add("sync", false, "%v", err)
		return
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok) != nil || tok.AccessToken == "" {
		add("sync", false, "The service account %s cannot sign in (%d).", clientID, resp.StatusCode)
		return
	}
	users := o.internalize(o.cfg.AdminURL()) + "/users?max=1&briefRepresentation=true"
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, users, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	r2, err := o.client.Do(req)
	if err != nil {
		add("sync", false, "%v", err)
		return
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		add("sync", false, "The service account %s may not read users (%d): it needs the roles view-users, query-users and query-groups.",
			clientID, r2.StatusCode)
		return
	}
	add("sync", true, "The service account %s reads the directory.", clientID)
}

// tokenRequest asks for a token with the client credentials and returns the status and the error code.
func (o *OIDC) tokenRequest(ctx context.Context, tokenURL, clientID, secret string) (int, string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
	return resp.StatusCode, e.Error, nil
}

func (o *OIDC) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}
