// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
)

// CookieName returns the session cookie name: __Host- prefixed over HTTPS.
func (s *Service) CookieName() string {
	if s.cfg.SecureCookies() {
		return "__Host-kogge_session"
	}
	return "kogge_session"
}

// SessionCookie creates the cookie that carries a new session token.
func (s *Service) SessionCookie(token string) *http.Cookie {
	return &http.Cookie{Name: s.CookieName(), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(s.cfg.SessionMaxLifetime.Seconds())}
}

// ClearCookie removes the session cookie.
func (s *Service) ClearCookie() *http.Cookie {
	return &http.Cookie{Name: s.CookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: -1}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// Authenticate resolves the principal of every request: a personal access token (Authorization: Bearer kgp_…)
// or the session cookie. Other credentials (Git Basic auth, agent and job tokens) are left to their handlers.
// For cookie sessions, state-changing requests must carry the session's CSRF token and a matching Origin.
func (s *Service) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := Anonymous

		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok &&
			strings.HasPrefix(bearer, crypto.PrefixAccessToken+"_") {
			t, err := lookupAccessToken(ctx, s.pool, crypto.HashToken(bearer))
			if err != nil && !db.IsNoRows(err) {
				httpx.WriteProblem(w, r, err)
				return
			}
			if t == nil || t.user.Status != StatusActive || (t.expiresAt != nil && t.expiresAt.Before(time.Now())) {
				// Failures count per address (apart from passwords) and are audited while the address is not blocked.
				ipKey := client(ctx)
				if s.throttle.tokens.Blocked(ipKey) {
					httpx.WriteProblem(w, r, apperr.TooManyRequests("auth.rate-limited", "Too many failed attempts. Try again later."))
					return
				}
				s.throttle.tokens.Fail(ipKey)
				var userID int64
				if t != nil {
					userID = t.user.ID
				}
				s.auditFailure(ctx, userID, "api", "token", "", tokenProblem(t, time.Now()))
				w.Header().Set("WWW-Authenticate", `Bearer realm="kogge"`)
				httpx.WriteProblem(w, r, apperr.Unauthorized("auth.token-invalid", "The access token is invalid or expired."))
				return
			}
			if t.lastUsed == nil || time.Since(*t.lastUsed) > time.Minute {
				_, _ = s.pool.Exec(ctx, "UPDATE access_tokens SET last_used_at = now() WHERE id = $1", t.id)
			}
			p = &Principal{User: t.user, Groups: t.groups, Method: MethodToken, Scopes: t.scopes, TokenID: t.id}
			if !p.AllowsScope(scopeFor(r.Method)) {
				httpx.WriteProblem(w, r, apperr.Forbidden("auth.token-scope", "The access token lacks the %s scope.", scopeFor(r.Method)))
				return
			}
		} else if c, err := r.Cookie(s.CookieName()); err == nil && c.Value != "" && r.Header.Get("Authorization") == "" {
			// Requests with other credentials (Git, agents) are not signed in by the cookie: one kind of credential
			// per request, and the CSRF check below then applies to every cookie session.
			hash := crypto.HashToken(c.Value)
			row, err := lookupSession(ctx, s.pool, hash)
			now := time.Now()
			switch {
			case err != nil && !db.IsNoRows(err):
				httpx.WriteProblem(w, r, err)
				return
			case err != nil || row.user.Status != StatusActive || now.After(row.expiresAt) || now.After(row.idleExpiresAt):
				if err == nil {
					_ = deleteSession(ctx, s.pool, hash)
				}
				http.SetCookie(w, s.ClearCookie())
			default:
				if now.Sub(row.lastSeenAt) > time.Minute && !background(r) {
					_ = touchSession(ctx, s.pool, hash, s.cfg.SessionIdleTimeout)
				}
				p = &Principal{User: row.user, Groups: row.groups, Method: MethodSession, CSRFToken: row.csrf, SessionID: hash,
					SignIn: row.method}
				p.TwoFactorSetup = row.method == "password" && s.TwoFactorSetupNeeded(ctx, row.user)
			}
		}

		// A WebSocket handshake is a GET that browsers make from any site, with the user's cookies: only pages of
		// our own origin may open one.
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && !s.sameOrigin(r.Header.Get("Origin")) {
			httpx.WriteProblem(w, r, apperr.Forbidden("auth.origin", "Cross-origin requests are not allowed."))
			return
		}
		if !safeMethod(r.Method) && r.Header.Get("Authorization") == "" {
			// Requests from browsers: the Origin must be ours; with a session, the CSRF token must match.
			if origin := r.Header.Get("Origin"); origin != "" && !s.sameOrigin(origin) {
				httpx.WriteProblem(w, r, apperr.Forbidden("auth.origin", "Cross-origin requests are not allowed."))
				return
			}
			if p.Method == MethodSession && !crypto.EqualStrings(r.Header.Get("X-CSRF-Token"), p.CSRFToken) {
				httpx.WriteProblem(w, r, apperr.Forbidden("auth.csrf", "The request is missing a valid CSRF token. Reload the page."))
				return
			}
		}

		if p.TwoFactorSetup && !duringSetup(r) {
			httpx.WriteProblem(w, r, apperr.Forbidden("auth.two-factor-setup",
				"This installation requires two-factor authentication: set it up to continue."))
			return
		}
		if p.Authenticated() {
			httpx.SetUser(ctx, p.UserID())
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
	})
}

// BackgroundHeader marks the requests that the web application makes while its user is idle (polling, refreshing).
const BackgroundHeader = "X-Kogge-Background"

// background reports whether a request comes from the web application without its user doing anything: the event
// streams, which reconnect every few minutes as long as a page is open, and the requests it marks as made while
// the user is idle. They do not keep a session alive, so that the idle timeout ends the sessions
// of unattended browsers, not only of closed ones.
func background(r *http.Request) bool {
	path := r.URL.Path
	return r.Header.Get(BackgroundHeader) != "" || path == "/api/v1/live" ||
		strings.HasPrefix(path, "/api/v1/builds/jobs/") && strings.HasSuffix(path, "/log/stream")
}

// SignedIn reports whether the principal of a long-lived connection (an editor's WebSocket) may still act: its
// session or access token has neither ended nor expired, and its user is active. So signing out, a new password,
// an administrator ending the sessions or disabling the user end such connections, too.
func SignedIn(ctx context.Context, q db.Querier, p *Principal) (bool, error) {
	if !p.Authenticated() {
		return false, nil
	}
	var ok bool
	var err error
	switch {
	case p.SessionID != nil:
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = $1
			AND s.expires_at > now() AND s.idle_expires_at > now() AND u.status = 'active')`, p.SessionID).Scan(&ok)
	case p.TokenID != 0:
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM access_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = $1
			AND (t.expires_at IS NULL OR t.expires_at > now()) AND u.status = 'active')`, p.TokenID).Scan(&ok)
	default:
		err = q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND status = 'active')", p.UserID()).Scan(&ok)
	}
	return ok, err
}

// duringSetup reports whether a session that has to set up two-factor authentication first may make a request:
// the web application and its files, who is signed in, the setup itself, and signing out.
func duringSetup(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case !strings.HasPrefix(path, "/api/"):
		return true
	case path == "/api/v1/me" && r.Method == http.MethodGet, path == "/api/v1/auth/logout", path == "/api/v1/auth/config":
		return true
	case path == "/api/v1/me/two-factor", strings.HasPrefix(path, "/api/v1/me/two-factor/"):
		return true
	}
	return false
}

func scopeFor(method string) string {
	if safeMethod(method) {
		return ScopeRead
	}
	return ScopeWrite
}

func (s *Service) sameOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	pub := s.cfg.PublicURL
	return strings.EqualFold(u.Scheme, pub.Scheme) && strings.EqualFold(u.Host, pub.Host)
}

// SafeReturnPath accepts only local paths as redirect targets after sign-in. Browsers drop tabs and newlines from
// addresses and treat backslashes as slashes, so these are refused rather than checked around.
func SafeReturnPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsRune(p, '\\') ||
		strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return p
}

// oidcCookie binds a login to the browser that started it: the callback is accepted only with the login's state in
// this cookie, so that nobody can sign a victim's browser in with the attacker's account.
func (s *Service) oidcCookie(state string, maxAge int) *http.Cookie {
	name := "kogge_oidc"
	if s.cfg.SecureCookies() {
		name = "__Host-kogge_oidc"
	}
	return &http.Cookie{Name: name, Value: state, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// HandleOIDCLogin redirects the browser to the identity provider.
func (s *Service) HandleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		httpx.WriteProblem(w, r, apperr.NotFound("auth.sso-disabled", "Single sign-on is not configured."))
		return
	}
	// Starting a sign-on needs no credentials but stores a login state: one client address cannot fill the table
	// (audit L3).
	if !s.sso.Allow("ip:" + httpx.ClientIP(r.Context()).String()) {
		httpx.WriteProblem(w, r, apperr.TooManyRequests("auth.rate-limited", "Too many sign-in attempts. Try again later."))
		return
	}
	target, state, err := s.oidc.AuthCodeURL(r.Context(), SafeReturnPath(r.URL.Query().Get("return")))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	http.SetCookie(w, s.oidcCookie(state, 600))
	http.Redirect(w, r, target, http.StatusFound)
}

// HandleOIDCCallback completes the sign-in and redirects to the page the user came from.
func (s *Service) HandleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		httpx.WriteProblem(w, r, apperr.NotFound("auth.sso-disabled", "Single sign-on is not configured."))
		return
	}
	q := r.URL.Query()
	binding, _ := r.Cookie(s.oidcCookie("", 0).Name)
	http.SetCookie(w, s.oidcCookie("", -1))
	if e := q.Get("error"); e != "" {
		s.log.Warn("sign-in rejected by the identity provider", "error", e, "description", q.Get("error_description"))
		http.Redirect(w, r, "/login?error=sso", http.StatusFound)
		return
	}
	if binding == nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(binding.Value), []byte(q.Get("state"))) != 1 {
		s.log.Warn("sign-in callback without the browser's login state")
		http.Redirect(w, r, "/login?error=sso", http.StatusFound)
		return
	}
	claims, returnTo, err := s.oidc.Exchange(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		s.log.Warn("sign-in failed", "error", err)
		http.Redirect(w, r, "/login?error=sso", http.StatusFound)
		return
	}
	token, _, err := s.LoginOIDC(r.Context(), claims, r.UserAgent())
	if err != nil {
		code := "sso"
		if apperr.Is(err, "auth.user-disabled") {
			code = "disabled"
		}
		s.log.Warn("sign-in failed", "error", err)
		http.Redirect(w, r, "/login?error="+code, http.StatusFound)
		return
	}
	http.SetCookie(w, s.SessionCookie(token))
	http.Redirect(w, r, SafeReturnPath(returnTo), http.StatusFound)
}

// HandleBackchannelLogout ends sessions on request of the identity provider (OpenID Connect Back-Channel Logout).
func (s *Service) HandleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sid, sub, err := s.oidc.VerifyLogoutToken(r.Context(), r.PostForm.Get("logout_token"))
	if err != nil {
		s.log.Warn("invalid logout token", "error", err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	if _, err := s.BackchannelLogout(r.Context(), sid, sub); err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
