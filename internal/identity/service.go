// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
)

// Lockout policy for local accounts.
const (
	lockAfterFailures = 10
	lockDuration      = 15 * time.Minute
)

// Service implements authentication and user management.
type Service struct {
	pool     *pgxpool.Pool
	cfg      *config.Config
	hasher   *crypto.PasswordHasher
	oidc     *OIDC
	log      *slog.Logger
	throttle *throttle
	// sso limits how often a client address starts single sign-ons, each of which stores a login state.
	sso   *httpx.Limiter
	dummy string // hash verified for unknown users, so timing does not reveal them
	// keyring seals the secrets of authenticator apps (SetKeyring).
	keyring *crypto.Keyring
	// twoFactorPolicy reports whether local accounts must use two-factor authentication (SetTwoFactorPolicy).
	twoFactorPolicy func(context.Context) bool
}

// SetKeyring gives the service the keyring that seals the secrets of two-factor authentication.
func (s *Service) SetKeyring(k *crypto.Keyring) { s.keyring = k }

// SetTwoFactorPolicy tells the service whether local accounts must use two-factor authentication (an instance
// setting).
func (s *Service) SetTwoFactorPolicy(f func(context.Context) bool) { s.twoFactorPolicy = f }

// TwoFactorSetupNeeded reports whether a user who signs in with a password has to set up two-factor
// authentication before anything else.
func (s *Service) TwoFactorSetupNeeded(ctx context.Context, u *User) bool {
	return !u.TwoFactor && s.twoFactorRequired(ctx)
}

// twoFactorRequired reports whether local accounts must use two-factor authentication.
func (s *Service) twoFactorRequired(ctx context.Context) bool {
	return s.twoFactorPolicy != nil && s.twoFactorPolicy(ctx)
}

// NewService creates the service. oidc may be nil.
func NewService(pool *pgxpool.Pool, cfg *config.Config, hasher *crypto.PasswordHasher, oidc *OIDC, logger *slog.Logger) *Service {
	s := &Service{pool: pool, cfg: cfg, hasher: hasher, oidc: oidc, log: logger, throttle: newThrottle(pool),
		sso: httpx.NewLimiter(2*time.Second, 30)}
	if h, err := hasher.Hash(context.Background(), crypto.RandomString(16)); err == nil {
		s.dummy = h
	}
	return s
}

// Pool returns the database pool (for handlers that need transactions).
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

// OIDC returns the identity provider client, or nil.
func (s *Service) OIDC() *OIDC { return s.oidc }

// LoginLocal checks a local password and creates a session. Failures are audited and count towards lockout;
// the error never reveals whether the user exists.
// With two-factor authentication, code is a code of the authenticator app or a recovery code: without it the
// sign-in fails with auth.second-factor-required (the password was right), with a wrong one it counts as a failure.
func (s *Service) LoginLocal(ctx context.Context, username, password, code, userAgent string) (string, *User, error) {
	invalid := apperr.Unauthorized("auth.invalid-credentials", "The user name or password is not correct.")
	if !s.cfg.LocalLogin {
		return "", nil, apperr.Forbidden("auth.local-login-disabled", "Sign-in with a local password is disabled.")
	}
	// Every attempt counts for the account; for the client and for the client with this account only failures
	// count (see throttle), so that nobody failing for one account blocks the sign-ins of others.
	if ok, err := s.throttle.admit(ctx, client(ctx), username, true); err != nil {
		return "", nil, err
	} else if !ok {
		return "", nil, apperr.TooManyRequests("auth.rate-limited", "Too many sign-in attempts. Try again later.")
	}
	creds, err := loadCredentials(ctx, s.pool, username)
	if err != nil {
		return "", nil, err
	}
	if creds == nil || creds.hash == nil {
		_, _, _ = s.hasher.Verify(ctx, password, s.dummy)
		s.refused(ctx, 0, username, "sign-in", false, true)
		s.audit(ctx, audit.Entry{Action: "auth.login.failed", Details: map[string]any{"username": username, "reason": "unknown"}})
		return "", nil, invalid
	}
	ok, rehash, err := s.hasher.Verify(ctx, password, *creds.hash)
	if err != nil {
		return "", nil, err
	}
	locked := creds.lockedUntil != nil && creds.lockedUntil.After(time.Now())
	if !ok || locked || creds.status != StatusActive {
		reason := "password"
		switch {
		case locked:
			reason = "locked"
		case creds.status != StatusActive:
			reason = creds.status
		}
		s.refused(ctx, creds.id, username, "sign-in", !ok, true)
		s.audit(ctx, audit.Entry{ActorID: creds.id, Action: "auth.login.failed", Details: map[string]any{"username": username, "reason": reason}})
		return "", nil, invalid
	}
	if err := s.secondFactor(ctx, creds.id, username, code); err != nil {
		return "", nil, err
	}
	var token string
	var user *User
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if rehash {
			if h, err := s.hasher.Hash(ctx, password); err == nil {
				if err := SetPasswordHash(ctx, tx, creds.id, h); err != nil {
					return err
				}
			}
		}
		if err := recordLoginSuccess(ctx, tx, creds.id); err != nil {
			return err
		}
		var err error
		token, err = createSession(ctx, tx, creds.id, "password", "", httpx.ClientIP(ctx).String(), userAgent,
			s.cfg.SessionIdleTimeout, s.cfg.SessionMaxLifetime)
		if err != nil {
			return err
		}
		if user, err = GetUser(ctx, tx, creds.id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: creds.id, Action: "auth.login", Details: map[string]any{"method": "password"}})
	})
	return token, user, err
}

// secondFactor checks the code of an account with two-factor authentication at sign-in. A missing code is no
// failure — the client asks for it —, a wrong one counts like a wrong password.
func (s *Service) secondFactor(ctx context.Context, userID int64, username, code string) error {
	on, err := hasTwoFactor(ctx, s.pool, userID)
	if err != nil || !on {
		return err
	}
	if strings.TrimSpace(code) == "" {
		return apperr.Unauthorized("auth.second-factor-required", "Enter the code of your authenticator app.")
	}
	var ok bool
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ok, err = s.checkSecondFactor(ctx, tx, userID, code)
		return err
	}); err != nil {
		return err
	}
	if ok {
		return nil
	}
	s.refused(ctx, userID, username, "sign-in", true, true)
	s.audit(ctx, audit.Entry{ActorID: userID, Action: "auth.login.failed", Details: map[string]any{"username": username, "reason": "second-factor"}})
	return apperr.Unauthorized("auth.second-factor-invalid", "The code is not correct.")
}

// LoginOIDC provisions the user from verified claims and creates a session.
func (s *Service) LoginOIDC(ctx context.Context, c *Claims, userAgent string) (string, *User, error) {
	var token string
	var user *User
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, created, err := s.provision(ctx, tx, c)
		if err != nil {
			return err
		}
		if u.Status != StatusActive {
			// On its own: the transaction is rolled back.
			s.audit(ctx, audit.Entry{ActorID: u.ID, Action: "auth.login.failed", Details: map[string]any{"method": "oidc", "reason": u.Status}})
			return apperr.Forbidden("auth.user-disabled", "Your account is disabled in Kogge.")
		}
		if c.GroupsPresent {
			if err := SyncProviderGroups(ctx, tx, u.ID, c.Groups); err != nil {
				return err
			}
		}
		if err := recordLoginSuccess(ctx, tx, u.ID); err != nil {
			return err
		}
		token, err = createSession(ctx, tx, u.ID, "oidc", c.SessionID, httpx.ClientIP(ctx).String(), userAgent,
			s.cfg.SessionIdleTimeout, s.cfg.SessionMaxLifetime)
		if err != nil {
			return err
		}
		user = u
		return audit.Record(ctx, tx, audit.Entry{ActorID: u.ID, Action: "auth.login",
			Details: map[string]any{"method": "oidc", "provisioned": created}})
	})
	return token, user, err
}

// provision finds or creates the user of the claims and updates its display attributes.
func (s *Service) provision(ctx context.Context, tx pgx.Tx, c *Claims) (*User, bool, error) {
	var id int64
	err := tx.QueryRow(ctx, "SELECT id FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2", c.Issuer, c.Subject).Scan(&id)
	if err != nil && !db.IsNoRows(err) {
		return nil, false, err
	}
	if err == nil {
		_, err := tx.Exec(ctx, `UPDATE users SET display_name = $2, email = NULLIF($3, ''), email_verified = $4,
			updated_at = now() WHERE id = $1`, id, c.DisplayName(), c.Email, c.EmailVerified)
		if err != nil {
			return nil, false, err
		}
		u, err := GetUser(ctx, tx, id)
		return u, false, err
	}
	username := sanitizeUsername(c.PreferredUsername)
	if username == "" {
		username = sanitizeUsername(strings.Split(c.Email, "@")[0])
	}
	if username == "" {
		username = "user-" + strings.ToLower(c.Subject[:min(8, len(c.Subject))])
	}
	// An account without an identity — a placeholder of a migration, an account created ahead of the first sign-in
	// — is taken over only when that is safe: when an administrator linked it to this user of the provider, or when
	// the provider asserts the account's e-mail address as verified. Never by the user name alone: the name of a
	// former colleague may belong to someone else at the provider now, who must not inherit the account with its
	// history, memberships and grants.
	var by string
	err = tx.QueryRow(ctx, `SELECT id, CASE WHEN lower(sso_link) = lower($1) THEN 'administrator' ELSE 'verified-email' END FROM users
		WHERE oidc_subject IS NULL AND password_hash IS NULL AND kind IN ('person', 'placeholder') AND status <> 'anonymised'
		AND ((sso_link IS NOT NULL AND $1 <> '' AND lower(sso_link) = lower($1))
			OR ($3 AND $2 <> '' AND email IS NOT NULL AND lower(email) = lower($2)))
		ORDER BY lower(sso_link) = lower($1) IS TRUE DESC, lower(username) = lower($4) DESC, kind = 'placeholder', id
		LIMIT 1 FOR UPDATE`, strings.TrimSpace(c.PreferredUsername), strings.TrimSpace(c.Email), c.EmailVerified, username).Scan(&id, &by)
	if err != nil && !db.IsNoRows(err) {
		return nil, false, err
	}
	if err == nil {
		if _, err := tx.Exec(ctx, `UPDATE users SET oidc_issuer = $2, oidc_subject = $3, kind = 'person', display_name = $4,
			email = NULLIF($5, ''), email_verified = $6, sso_link = NULL, updated_at = now(),
			status = CASE WHEN kind = 'placeholder' AND status = 'disabled' THEN 'active' ELSE status END
			WHERE id = $1`, id, c.Issuer, c.Subject, c.DisplayName(), c.Email, c.EmailVerified); err != nil {
			return nil, false, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: id, Action: "user.sso-takeover", TargetType: "user", TargetID: audit.ID(id),
			Details: map[string]any{"by": by, "username": c.PreferredUsername, "subject": c.Subject}}); err != nil {
			return nil, false, err
		}
		u, err := GetUser(ctx, tx, id)
		return u, false, err
	}
	candidate := username
	for i := 2; ; i++ {
		var taken bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower($1) AND status <> 'anonymised')",
			candidate).Scan(&taken); err != nil {
			return nil, false, err
		}
		if !taken {
			break
		}
		candidate = username + "-" + strconv.Itoa(i)
	}
	u, err := CreateUser(ctx, tx, NewUser{Username: candidate, DisplayName: c.DisplayName(), Email: c.Email,
		EmailVerified: c.EmailVerified, OIDCIssuer: c.Issuer, OIDCSubject: c.Subject, Locale: c.Locale})
	return u, true, err
}

// sanitizeUsername keeps letters, digits and . _ - @ and lower-cases the name.
func sanitizeUsername(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-@", r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// ValidUsername checks a user name for local accounts.
func ValidUsername(s string) bool {
	return s != "" && len(s) <= 64 && sanitizeUsername(s) == s
}

// Revocations — signing out, ending sessions, revoking access tokens and SSH keys — take effect whether or not
// their audit events can be written (audit.Report, which logs those it cannot write): refusing a revocation would
// keep a possibly stolen credential valid. Everything that grants or changes something is recorded in its own
// transaction and does not happen without its record.

// Logout ends the session of the principal.
func (s *Service) Logout(ctx context.Context, p *Principal) error {
	if p.SessionID == nil {
		return nil
	}
	if err := deleteSession(ctx, s.pool, p.SessionID); err != nil {
		return err
	}
	s.audit(ctx, audit.Entry{ActorID: p.UserID(), Action: "auth.logout"})
	return nil
}

// BackchannelLogout ends the sessions of an identity provider session (sid) or of a user (sub).
func (s *Service) BackchannelLogout(ctx context.Context, sid, sub string) (int64, error) {
	var tag int64
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var res interface{ RowsAffected() int64 }
		var err error
		if sid != "" {
			res, err = tx.Exec(ctx, "DELETE FROM sessions WHERE oidc_sid = $1", sid)
		} else {
			res, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id IN
				(SELECT id FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2)`, s.cfg.OIDC.Issuer, sub)
		}
		if err != nil {
			return err
		}
		tag = res.RowsAffected()
		return audit.Record(ctx, tx, audit.Entry{Action: "auth.backchannel-logout",
			Details: map[string]any{"sid": sid, "sub": sub, "sessions": tag}})
	})
	return tag, err
}

// Sessions lists the active sessions of the principal's user.
func (s *Service) Sessions(ctx context.Context, p *Principal) ([]Session, error) {
	return listSessions(ctx, s.pool, p.UserID(), p.SessionID)
}

// EndOtherSessions ends all sessions of the principal's user but the principal's own (with an access token: all of
// them) and returns how many ended.
func (s *Service) EndOtherSessions(ctx context.Context, p *Principal) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND id_hash IS DISTINCT FROM $2", p.UserID(), p.SessionID)
	if err != nil {
		return 0, err
	}
	s.audit(ctx, audit.Entry{ActorID: p.UserID(), Action: "auth.session.end-others", Details: map[string]any{"sessions": tag.RowsAffected()}})
	return tag.RowsAffected(), nil
}

// EndSession ends one of the principal's sessions.
func (s *Service) EndSession(ctx context.Context, p *Principal, id string) error {
	ok, err := deleteSessionByID(ctx, s.pool, p.UserID(), id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("session.not-found", "The session does not exist.")
	}
	s.audit(ctx, audit.Entry{ActorID: p.UserID(), Action: "auth.session.end"})
	return nil
}

// CreateAccessToken creates a personal access token; the token is returned once.
func (s *Service) CreateAccessToken(ctx context.Context, p *Principal, name string, scopes []string, expiresAt *time.Time) (string, *AccessToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", nil, apperr.Invalid("token.name", "The name must have 1 to 100 characters.")
	}
	if len(scopes) == 0 {
		return "", nil, apperr.Invalid("token.scopes", "Choose at least one scope.")
	}
	for _, sc := range scopes {
		if sc != ScopeRead && sc != ScopeWrite && sc != ScopeAdmin {
			return "", nil, apperr.Invalidf("token.scopes", "Unknown scope %s.", sc)
		}
	}
	if !p.AllowsScope(ScopeAdmin) {
		return "", nil, apperr.Forbidden("token.scope", "Access tokens cannot create access tokens without the admin scope.")
	}
	var token string
	var t *AccessToken
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		token, t, err = createAccessToken(ctx, tx, p.UserID(), name, scopes, expiresAt)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "token.create", TargetType: "token",
			TargetID: audit.ID(t.ID), Details: map[string]any{"name": name, "scopes": scopes}})
	})
	return token, t, err
}

// AccessTokens lists the principal's tokens.
func (s *Service) AccessTokens(ctx context.Context, p *Principal) ([]AccessToken, error) {
	return listAccessTokens(ctx, s.pool, p.UserID())
}

// RevokeAccessToken deletes one of the principal's tokens.
func (s *Service) RevokeAccessToken(ctx context.Context, p *Principal, id int64) error {
	ok, err := deleteAccessToken(ctx, s.pool, p.UserID(), id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("token.not-found", "The token does not exist.")
	}
	s.audit(ctx, audit.Entry{ActorID: p.UserID(), Action: "token.revoke", TargetType: "token", TargetID: audit.ID(id)})
	return nil
}

// ChangePassword changes the local password of the principal.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	// Guesses of the current password with someone else's session count like sign-ins.
	if ok, err := s.throttle.admit(ctx, client(ctx), p.User.Username, true); err != nil {
		return err
	} else if !ok {
		return apperr.TooManyRequests("auth.rate-limited", "Too many attempts. Try again later.")
	}
	creds, err := loadCredentials(ctx, s.pool, p.User.Username)
	if err != nil {
		return err
	}
	if creds == nil || creds.hash == nil {
		return apperr.BadRequest("password.not-local", "Your account has no local password.")
	}
	if creds.lockedUntil != nil && creds.lockedUntil.After(time.Now()) {
		return apperr.TooManyRequests("auth.rate-limited", "Too many attempts. Try again later.")
	}
	ok, _, err := s.hasher.Verify(ctx, current, *creds.hash)
	if err != nil {
		return err
	}
	if !ok {
		s.refused(ctx, creds.id, p.User.Username, "profile", true, true)
		return apperr.BadRequest("password.wrong", "The current password is not correct.")
	}
	if err := crypto.ValidatePassword(next, p.User.Username); err != nil {
		return apperr.Invalid("password.policy", err.Error())
	}
	h, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := SetPasswordHash(ctx, tx, p.UserID(), h); err != nil {
			return err
		}
		// All other sessions end (with a token, all sessions: there is no current one).
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND id_hash IS DISTINCT FROM $2", p.UserID(), p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "auth.password.change"})
	})
}

// SetPassword sets a local password as an administrator.
func (s *Service) SetPassword(ctx context.Context, actor *Principal, userID int64, password string) error {
	u, err := GetUser(ctx, s.pool, userID)
	if err != nil {
		return err
	}
	if u.Kind == KindSystem {
		return apperr.Forbidden("user.system", "The system user cannot sign in.")
	}
	if err := crypto.ValidatePassword(password, u.Username); err != nil {
		return apperr.Invalid("password.policy", err.Error())
	}
	h, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := SetPasswordHash(ctx, tx, userID, h); err != nil {
			return err
		}
		if err := DeleteUserSessions(ctx, tx, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.password.set",
			TargetType: "user", TargetID: audit.ID(userID)})
	})
}

// CreateLocalUser creates a user with a local password (administrators, bootstrap).
func (s *Service) CreateLocalUser(ctx context.Context, tx pgx.Tx, username, displayName, email, password string) (*User, error) {
	if !ValidUsername(username) {
		return nil, apperr.Invalid("user.username", "User names may contain lower-case letters, digits and . _ - @.")
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = username
	}
	var hash string
	if password != "" {
		if err := crypto.ValidatePassword(password, username); err != nil {
			return nil, apperr.Invalid("password.policy", err.Error())
		}
		var err error
		if hash, err = s.hasher.Hash(ctx, password); err != nil {
			return nil, err
		}
	}
	return CreateUser(ctx, tx, NewUser{Username: username, DisplayName: strings.TrimSpace(displayName), Email: email,
		EmailVerified: email != "", PasswordHash: hash})
}
