// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// CallbackPath is where the identity provider redirects after sign-in.
const CallbackPath = "/auth/oidc/callback"

// BackchannelLogoutPath receives logout tokens from the identity provider.
const BackchannelLogoutPath = "/auth/oidc/backchannel-logout"

const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// OIDC is the OpenID Connect client (authorization code flow with PKCE). It initialises lazily, so Kogge starts
// even when the identity provider is not reachable yet.
type OIDC struct {
	cfg       config.OIDC
	publicURL *url.URL
	pool      *pgxpool.Pool
	client    *http.Client

	mu             sync.Mutex
	ready          bool
	authURL        string
	tokenURL       string
	endSessionURL  string
	verifier       *oidc.IDTokenVerifier
	logoutVerifier *oidc.IDTokenVerifier

	logouts usedLogoutTokens
}

// Logout tokens are accepted while they are new: issued (iat) at most logoutTokenAge ago, and at most
// logoutTokenSkew ahead of this node's clock (clocks differ a little).
const (
	logoutTokenAge  = 5 * time.Minute
	logoutTokenSkew = 2 * time.Minute
)

// usedLogoutTokens remembers the ids (jti) of the logout tokens this node accepted while they are new, so that each
// is accepted once: a token that was sent along on another way cannot end the sessions again. It is kept per node, in
// memory; another node may accept a replayed token once more, which ends sessions that ended already (or new ones of
// the same identity provider session, which the provider ended).
type usedLogoutTokens struct {
	mu   sync.Mutex
	seen map[string]time.Time // issuer and jti → when the token is too old anyway
}

// first records a token's id and reports whether it was not recorded before.
func (u *usedLogoutTokens) first(id string, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for k, until := range u.seen {
		if now.After(until) {
			delete(u.seen, k)
		}
	}
	if _, ok := u.seen[id]; ok {
		return false
	}
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	u.seen[id] = now.Add(logoutTokenAge + logoutTokenSkew)
	return true
}

// NewOIDC creates the client; it returns nil if single sign-on is not configured.
func NewOIDC(cfg config.OIDC, publicURL *url.URL, pool *pgxpool.Pool) *OIDC {
	if !cfg.Enabled() {
		return nil
	}
	return &OIDC{cfg: cfg, publicURL: publicURL, pool: pool, client: &http.Client{Timeout: 10 * time.Second}}
}

// ProviderName is the label of the sign-in button.
func (o *OIDC) ProviderName() string { return o.cfg.ProviderName }

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

// internalize replaces the issuer's origin with the internal URL for server-to-server calls.
func (o *OIDC) internalize(endpoint string) string {
	if o.cfg.InternalURL == "" {
		return endpoint
	}
	iss, err := url.Parse(o.cfg.Issuer)
	if err != nil {
		return endpoint
	}
	origin := iss.Scheme + "://" + iss.Host
	if strings.HasPrefix(endpoint, origin) {
		return o.cfg.InternalURL + strings.TrimPrefix(endpoint, origin)
	}
	return endpoint
}

func (o *OIDC) init(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ready {
		return nil
	}
	discoveryURL := o.internalize(o.cfg.Issuer + "/.well-known/openid-configuration")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("identity provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("identity provider discovery: HTTP %d", resp.StatusCode)
	}
	var d discovery
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&d); err != nil {
		return fmt.Errorf("identity provider discovery: %w", err)
	}
	if strings.TrimRight(d.Issuer, "/") != o.cfg.Issuer {
		return fmt.Errorf("identity provider reports issuer %q, configured is %q", d.Issuer, o.cfg.Issuer)
	}
	keyCtx := oidc.ClientContext(context.Background(), o.client)
	keys := oidc.NewRemoteKeySet(keyCtx, o.internalize(d.JWKSURI))
	o.authURL = d.AuthorizationEndpoint
	o.tokenURL = o.internalize(d.TokenEndpoint)
	o.endSessionURL = d.EndSessionEndpoint
	o.verifier = oidc.NewVerifier(o.cfg.Issuer, keys, &oidc.Config{ClientID: o.cfg.ClientID})
	o.logoutVerifier = oidc.NewVerifier(o.cfg.Issuer, keys, &oidc.Config{ClientID: o.cfg.ClientID, SkipExpiryCheck: true})
	o.ready = true
	return nil
}

func (o *OIDC) oauth() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: o.authURL, TokenURL: o.tokenURL, AuthStyle: oauth2.AuthStyleInHeader},
		RedirectURL:  o.publicURL.String() + CallbackPath,
		Scopes:       o.cfg.Scopes,
	}
}

// AuthCodeURL starts a login and returns the identity provider URL to redirect the browser to, and the login's
// state, which the browser must present again at the callback.
func (o *OIDC) AuthCodeURL(ctx context.Context, returnTo string) (target, state string, err error) {
	if err := o.init(ctx); err != nil {
		return "", "", apperr.New(http.StatusServiceUnavailable, "auth.provider-unavailable",
			"The sign-in service is not available. Try again later.").WithCause(err)
	}
	state, nonce, verifier := crypto.RandomString(24), crypto.RandomString(24), oauth2.GenerateVerifier()
	if _, err := o.pool.Exec(ctx, "INSERT INTO oidc_logins (state, nonce, verifier, return_to) VALUES ($1, $2, $3, $4)",
		state, nonce, verifier, returnTo); err != nil {
		return "", "", err
	}
	return o.oauth().AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

// Claims are the verified claims of a login.
type Claims struct {
	Issuer            string
	Subject           string
	PreferredUsername string
	Name              string
	GivenName         string
	FamilyName        string
	Email             string
	EmailVerified     bool
	Locale            string
	SessionID         string
	Groups            []string
	GroupsPresent     bool
}

// DisplayName returns the best available name.
func (c *Claims) DisplayName() string {
	switch {
	case strings.TrimSpace(c.Name) != "":
		return strings.TrimSpace(c.Name)
	case strings.TrimSpace(c.GivenName+" "+c.FamilyName) != "":
		return strings.TrimSpace(c.GivenName + " " + c.FamilyName)
	case c.PreferredUsername != "":
		return c.PreferredUsername
	default:
		return c.Email
	}
}

// Exchange completes a login: it checks the state, redeems the code and verifies the ID token.
func (o *OIDC) Exchange(ctx context.Context, state, code string) (*Claims, string, error) {
	if err := o.init(ctx); err != nil {
		return nil, "", apperr.New(http.StatusServiceUnavailable, "auth.provider-unavailable",
			"The sign-in service is not available. Try again later.").WithCause(err)
	}
	var nonce, verifier, returnTo string
	err := o.pool.QueryRow(ctx, `DELETE FROM oidc_logins WHERE state = $1 AND created_at > now() - interval '10 minutes'
		RETURNING nonce, verifier, return_to`, state).Scan(&nonce, &verifier, &returnTo)
	if db.IsNoRows(err) {
		return nil, "", apperr.BadRequest("auth.state", "The sign-in attempt expired. Please sign in again.")
	}
	if err != nil {
		return nil, "", err
	}
	tctx := context.WithValue(ctx, oauth2.HTTPClient, o.client)
	tok, err := o.oauth().Exchange(tctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", apperr.Unauthorized("auth.code", "The sign-in could not be completed.").WithCause(err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, "", apperr.Unauthorized("auth.id-token", "The identity provider sent no ID token.")
	}
	idt, err := o.verifier.Verify(oidc.ClientContext(ctx, o.client), raw)
	if err != nil {
		return nil, "", apperr.Unauthorized("auth.id-token", "The ID token is not valid.").WithCause(err)
	}
	if !crypto.EqualStrings(idt.Nonce, nonce) {
		return nil, "", apperr.Unauthorized("auth.nonce", "The ID token does not belong to this sign-in.")
	}
	var std struct {
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		GivenName         string `json:"given_name"`
		FamilyName        string `json:"family_name"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Locale            string `json:"locale"`
		SID               string `json:"sid"`
	}
	var all map[string]any
	if err := idt.Claims(&std); err != nil {
		return nil, "", err
	}
	if err := idt.Claims(&all); err != nil {
		return nil, "", err
	}
	c := &Claims{Issuer: o.cfg.Issuer, Subject: idt.Subject, PreferredUsername: std.PreferredUsername, Name: std.Name,
		GivenName: std.GivenName, FamilyName: std.FamilyName, Email: std.Email, EmailVerified: std.EmailVerified,
		Locale: normaliseLocale(std.Locale), SessionID: std.SID}
	if v, ok := all[o.cfg.GroupsClaim]; ok {
		c.GroupsPresent = true
		if list, ok := v.([]any); ok {
			for _, g := range list {
				if s, ok := g.(string); ok {
					s = strings.Trim(s, "/ ")
					if s != "" && !slices.Contains(c.Groups, s) {
						c.Groups = append(c.Groups, s)
					}
				}
			}
		}
	}
	return c, returnTo, nil
}

func normaliseLocale(l string) string {
	l = strings.ToLower(l)
	switch {
	case strings.HasPrefix(l, "de"):
		return "de"
	case strings.HasPrefix(l, "en"):
		return "en"
	}
	return ""
}

// VerifyLogoutToken validates a back-channel logout token and returns its session id and subject.
func (o *OIDC) VerifyLogoutToken(ctx context.Context, raw string) (sid, sub string, err error) {
	if err := o.init(ctx); err != nil {
		return "", "", err
	}
	t, err := o.logoutVerifier.Verify(oidc.ClientContext(ctx, o.client), raw)
	if err != nil {
		return "", "", err
	}
	var c struct {
		SID    string         `json:"sid"`
		JTI    string         `json:"jti"`
		Events map[string]any `json:"events"`
		Nonce  *string        `json:"nonce"`
	}
	if err := t.Claims(&c); err != nil {
		return "", "", err
	}
	now := time.Now()
	switch _, event := c.Events[backchannelLogoutEvent]; {
	case !event:
		return "", "", errors.New("not a logout token")
	case c.Nonce != nil:
		return "", "", errors.New("logout token must not contain a nonce")
	case t.IssuedAt.IsZero():
		return "", "", errors.New("logout token without iat")
	case now.Sub(t.IssuedAt) > logoutTokenAge:
		return "", "", errors.New("logout token too old")
	case t.IssuedAt.Sub(now) > logoutTokenSkew:
		return "", "", errors.New("logout token issued in the future")
	case c.JTI == "":
		return "", "", errors.New("logout token without jti")
	case c.SID == "" && t.Subject == "":
		return "", "", errors.New("logout token names neither session nor subject")
	case !o.logouts.first(t.Issuer+"\x00"+c.JTI, now):
		return "", "", errors.New("logout token used before")
	}
	return c.SID, t.Subject, nil
}

// EndSessionURL returns where to send the browser to end the identity provider session, or "".
func (o *OIDC) EndSessionURL(ctx context.Context) string {
	if err := o.init(ctx); err != nil || o.endSessionURL == "" {
		return ""
	}
	q := url.Values{}
	q.Set("client_id", o.cfg.ClientID)
	q.Set("post_logout_redirect_uri", o.publicURL.String()+"/")
	sep := "?"
	if strings.Contains(o.endSessionURL, "?") {
		sep = "&"
	}
	return o.endSessionURL + sep + q.Encode()
}
