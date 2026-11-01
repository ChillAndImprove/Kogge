// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// Credentials of Git clients: HTTP Basic authentication with an access token (or the local password) and SSH keys.

// AuthenticateBasic checks the credentials of a Git client: the user name with a personal access token, or with
// the local password where local sign-in is enabled.
func (s *Service) AuthenticateBasic(ctx context.Context, username, secret string) (*Principal, error) {
	invalid := apperr.Unauthorized("auth.invalid-credentials", "The user name or access token is not correct.")
	if username == "" || secret == "" {
		return nil, invalid
	}
	// Git sends the credentials with every request, so only failures count (see throttle). Valid access tokens
	// always pass: they cannot be guessed, and someone failing from the same address must not lock out build
	// servers; invalid ones are limited per address on their own.
	ip := client(ctx)
	if strings.HasPrefix(secret, crypto.PrefixAccessToken+"_") {
		t, err := lookupAccessToken(ctx, s.pool, crypto.HashToken(secret))
		if err != nil && !db.IsNoRows(err) {
			return nil, err
		}
		if t == nil || !strings.EqualFold(t.user.Username, username) || t.user.Status != StatusActive ||
			(t.expiresAt != nil && t.expiresAt.Before(time.Now())) {
			if !s.throttle.tokens.Allow(ip) {
				return nil, apperr.TooManyRequests("auth.rate-limited", "Too many failed attempts. Try again later.")
			}
			s.auditFailure(ctx, 0, "git", "token", username, tokenProblem(t, time.Now()))
			return nil, invalid
		}
		if t.lastUsed == nil || time.Since(*t.lastUsed) > time.Minute {
			_, _ = s.pool.Exec(ctx, "UPDATE access_tokens SET last_used_at = now() WHERE id = $1", t.id)
		}
		return &Principal{User: t.user, Groups: t.groups, Method: MethodToken, Scopes: t.scopes, TokenID: t.id}, nil
	}
	if !s.cfg.LocalLogin {
		return nil, invalid
	}
	// A password is checked only while the throttle admits it, also when it is right: otherwise guessing could go
	// on.
	if ok, err := s.throttle.admit(ctx, ip, username, false); err != nil {
		return nil, err
	} else if !ok {
		return nil, apperr.TooManyRequests("auth.rate-limited", "Too many failed attempts. Try again later.")
	}
	creds, err := loadCredentials(ctx, s.pool, username)
	if err != nil {
		return nil, err
	}
	if creds == nil || creds.hash == nil {
		_, _, _ = s.hasher.Verify(ctx, secret, s.dummy)
		s.refused(ctx, 0, username, "git", false, false)
		s.auditFailure(ctx, 0, "git", "password", username, "unknown")
		return nil, invalid
	}
	ok, _, err := s.hasher.Verify(ctx, secret, *creds.hash)
	if err != nil {
		return nil, err
	}
	if !ok || creds.status != StatusActive || (creds.lockedUntil != nil && creds.lockedUntil.After(time.Now())) {
		reason := "password"
		switch {
		case ok && creds.status != StatusActive:
			reason = creds.status
		case ok:
			reason = "locked"
		}
		s.refused(ctx, creds.id, username, "git", !ok, false)
		s.auditFailure(ctx, creds.id, "git", "password", username, reason)
		return nil, invalid
	}
	// With a second factor, the password alone opens nothing: Git takes access tokens.
	if on, err := hasTwoFactor(ctx, s.pool, creds.id); err != nil {
		return nil, err
	} else if on {
		s.auditFailure(ctx, creds.id, "git", "password", username, "second-factor")
		return nil, apperr.Unauthorized("auth.token-required", "Your account uses two-factor authentication: sign in to Git with a personal access token.")
	} else if s.twoFactorRequired(ctx) {
		s.auditFailure(ctx, creds.id, "git", "password", username, "two-factor-setup")
		return nil, apperr.Unauthorized("auth.two-factor-setup",
			"This installation requires two-factor authentication: sign in to Kogge, set it up, and use a personal access token for Git.")
	}
	return s.PrincipalOf(ctx, creds.id, MethodPassword)
}

// auditFailure records a failed authentication; the rate limits keep an attacker from flooding the
// log, since blocked attempts are refused before they get here.
func (s *Service) auditFailure(ctx context.Context, userID int64, via, method, username, reason string) {
	s.audit(ctx, audit.Entry{ActorID: userID, Action: "auth.login.failed",
		Details: map[string]any{"via": via, "method": method, "username": username, "reason": reason}})
}

// tokenProblem tells why an access token was refused.
func tokenProblem(t *tokenRow, now time.Time) string {
	switch {
	case t == nil:
		return "unknown"
	case t.user.Status != StatusActive:
		return t.user.Status
	case t.expiresAt != nil && t.expiresAt.Before(now):
		return "expired"
	}
	return "user"
}

// PrincipalOf returns the principal of an active user (for Git over SSH and background work).
func (s *Service) PrincipalOf(ctx context.Context, userID int64, method string) (*Principal, error) {
	return LoadPrincipal(ctx, s.pool, userID, method)
}

// LoadPrincipal returns the principal of an active user, for work done on a user's behalf or checks of what a user
// may see.
func LoadPrincipal(ctx context.Context, q db.Querier, userID int64, method string) (*Principal, error) {
	u, err := GetUser(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	if u.Status != StatusActive {
		return nil, apperr.Unauthorized("auth.user-inactive", "The user is not active.")
	}
	groups, err := UserGroups(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	p := &Principal{User: u, Method: method, Groups: []int64{}}
	for _, g := range groups {
		p.Groups = append(p.Groups, g.ID)
	}
	return p, nil
}

// SSHKey is a public key for Git over SSH.
type SSHKey struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type" doc:"Key algorithm, e.g. ssh-ed25519"`
	Fingerprint string     `json:"fingerprint" doc:"SHA256 fingerprint"`
	Comment     string     `json:"comment,omitempty"`
	Created     time.Time  `json:"created"`
	LastUsed    *time.Time `json:"lastUsed,omitempty"`
}

// ParsedKey is a validated public key.
type ParsedKey struct {
	Key         ssh.PublicKey
	Normalized  string // "type base64"
	Fingerprint string
	Comment     string
}

// minRSABits is the least size of RSA keys (BSI TR-02102-1 recommends 3000 bits and more).
const minRSABits = 3072

// ParseSSHKey validates a public key in authorized_keys format. RSA keys need at least minRSABits; DSA keys are
// not accepted. Keys added before keep working.
func ParseSSHKey(text string) (*ParsedKey, error) {
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil {
		return nil, apperr.Invalid("ssh-key.invalid", "This is not a public SSH key (as in id_ed25519.pub).")
	}
	// A certificate signs in like the key it certifies, which has to meet the same requirements.
	inner := key
	if cert, ok := key.(*ssh.Certificate); ok {
		inner = cert.Key
	}
	switch inner.Type() {
	case ssh.KeyAlgoDSA:
		return nil, apperr.Invalid("ssh-key.weak", "DSA keys are not accepted; use an Ed25519 or ECDSA key.")
	case ssh.KeyAlgoRSA:
		if ck, ok := inner.(ssh.CryptoPublicKey); ok {
			if rk, ok := ck.CryptoPublicKey().(interface{ Size() int }); ok && rk.Size()*8 < minRSABits {
				return nil, apperr.Invalidf("ssh-key.weak", "RSA keys need at least %d bits; Ed25519 keys are shorter and stronger.", minRSABits)
			}
		}
	}
	normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	return &ParsedKey{Key: key, Normalized: normalized, Fingerprint: ssh.FingerprintSHA256(key), Comment: comment}, nil
}

func scanSSHKey(row pgx.CollectableRow) (SSHKey, error) {
	var k SSHKey
	var pub string
	err := row.Scan(&k.ID, &k.Name, &pub, &k.Fingerprint, &k.Created, &k.LastUsed)
	if f := strings.Fields(pub); len(f) > 0 {
		k.Type = f[0]
	}
	return k, err
}

// SSHKeys lists the principal's SSH keys.
func (s *Service) SSHKeys(ctx context.Context, p *Principal) ([]SSHKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, public_key, fingerprint, created_at, last_used_at FROM ssh_keys
		WHERE kind = 'user' AND user_id = $1 ORDER BY created_at`, p.UserID())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSSHKey)
}

// AddSSHKey adds an SSH key of the principal.
func (s *Service) AddSSHKey(ctx context.Context, p *Principal, name, text string) (*SSHKey, error) {
	if !p.AllowsScope(ScopeAdmin) {
		return nil, apperr.Forbidden("ssh-key.scope", "Access tokens need the admin scope to add SSH keys.")
	}
	k, err := ParseSSHKey(text)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = k.Comment
	}
	if name == "" {
		name = k.Key.Type()
	}
	if len(name) > 100 {
		name = name[:100]
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO ssh_keys (kind, user_id, name, public_key, fingerprint, created_by) VALUES ('user', $1, $2, $3, $4, $1)
			RETURNING id`, p.UserID(), name, k.Normalized, k.Fingerprint).Scan(&id)
		if db.IsUniqueViolation(err, "ssh_keys_fingerprint_key") {
			return apperr.Conflict("ssh-key.taken", "This key is already in use.")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "ssh-key.add", TargetType: "ssh-key", TargetID: audit.ID(id),
			Details: map[string]any{"fingerprint": k.Fingerprint, "name": name}})
	})
	if err != nil {
		return nil, err
	}
	keys, err := s.SSHKeys(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.ID == id {
			key.Comment = k.Comment
			return &key, nil
		}
	}
	return nil, apperr.NotFound("ssh-key.not-found", "The key does not exist.")
}

// DeleteSSHKey removes one of the principal's SSH keys.
func (s *Service) DeleteSSHKey(ctx context.Context, p *Principal, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM ssh_keys WHERE id = $1 AND kind = 'user' AND user_id = $2", id, p.UserID())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("ssh-key.not-found", "The key does not exist.")
	}
	s.audit(ctx, audit.Entry{ActorID: p.UserID(), Action: "ssh-key.delete", TargetType: "ssh-key", TargetID: audit.ID(id)})
	return nil
}
