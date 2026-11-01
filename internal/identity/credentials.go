// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Credentials are what a user signs in with besides passwords: access tokens and SSH keys (without secrets).
type Credentials struct {
	Sessions int           `json:"sessions" doc:"Number of active browser sessions"`
	Tokens   []AccessToken `json:"tokens"`
	SSHKeys  []SSHKey      `json:"sshKeys"`
}

// UserCredentials lists the credentials of a user (administrators: when an account is compromised, they see and
// end what still works besides the password).
func (s *Service) UserCredentials(ctx context.Context, userID int64) (*Credentials, error) {
	if _, err := GetUser(ctx, s.pool, userID); err != nil {
		return nil, err
	}
	c := &Credentials{Tokens: []AccessToken{}, SSHKeys: []SSHKey{}}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1 AND expires_at > now() AND idle_expires_at > now()", userID).
		Scan(&c.Sessions); err != nil {
		return nil, err
	}
	tokens, err := listAccessTokens(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	c.Tokens = append(c.Tokens, tokens...)
	rows, err := s.pool.Query(ctx, `SELECT id, name, public_key, fingerprint, created_at, last_used_at FROM ssh_keys
		WHERE kind = 'user' AND user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, scanSSHKey)
	if err != nil {
		return nil, err
	}
	c.SSHKeys = append(c.SSHKeys, keys...)
	return c, nil
}

// RevokeUserToken revokes an access token of a user (administrators). Like all revocations, it takes effect
// whether or not its record can be written (see Logout).
func (s *Service) RevokeUserToken(ctx context.Context, actor *Principal, userID, tokenID int64) error {
	ok, err := deleteAccessToken(ctx, s.pool, userID, tokenID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("token.not-found", "The token does not exist.")
	}
	s.audit(ctx, audit.Entry{ActorID: actor.UserID(), Action: "user.token.revoke", TargetType: "user",
		TargetID: audit.ID(userID), Details: map[string]any{"token": tokenID}})
	return nil
}

// DeleteUserSSHKey deletes an SSH key of a user (administrators).
func (s *Service) DeleteUserSSHKey(ctx context.Context, actor *Principal, userID, keyID int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM ssh_keys WHERE id = $1 AND kind = 'user' AND user_id = $2", keyID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("ssh-key.not-found", "The key does not exist.")
	}
	s.audit(ctx, audit.Entry{ActorID: actor.UserID(), Action: "user.ssh-key.delete", TargetType: "user",
		TargetID: audit.ID(userID), Details: map[string]any{"key": keyID}})
	return nil
}

// EndUserSessions ends all sessions of a user (administrators).
func (s *Service) EndUserSessions(ctx context.Context, actor *Principal, userID int64) error {
	if err := DeleteUserSessions(ctx, s.pool, userID); err != nil {
		return err
	}
	s.audit(ctx, audit.Entry{ActorID: actor.UserID(), Action: "user.sessions.end", TargetType: "user", TargetID: audit.ID(userID)})
	return nil
}

// RevokeUserAccess ends every way a user is signed in besides the password: sessions, access tokens and SSH keys
// (administrators, for compromised accounts).
func (s *Service) RevokeUserAccess(ctx context.Context, actor *Principal, userID int64) error {
	u, err := GetUser(ctx, s.pool, userID)
	if err != nil {
		return err
	}
	if u.Kind == KindSystem {
		return apperr.Forbidden("user.system", "The system user cannot sign in.")
	}
	var tokens, keys int64
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := DeleteUserSessions(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM access_tokens WHERE user_id = $1", userID)
		if err != nil {
			return err
		}
		tokens = tag.RowsAffected()
		if tag, err = tx.Exec(ctx, "DELETE FROM ssh_keys WHERE kind = 'user' AND user_id = $1", userID); err != nil {
			return err
		}
		keys = tag.RowsAffected()
		return nil
	}); err != nil {
		return err
	}
	s.audit(ctx, audit.Entry{ActorID: actor.UserID(), Action: "user.access.revoke", TargetType: "user",
		TargetID: audit.ID(userID), Details: map[string]any{"tokens": tokens, "sshKeys": keys}})
	return nil
}
