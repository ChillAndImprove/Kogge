// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Single sign-on takes over an account without an identity only when that is safe (see provision): when the
// identity provider asserts the account's e-mail address as verified, or when an administrator linked the account
// to a user of the provider. The link is for placeholders of migrations and accounts created ahead of the first
// sign-in whose people the provider does not confirm by e-mail: the provider's user with that user name takes the
// account over at the first sign-in, or when the directory sync finds them, and the link is gone then. An account
// that signed in already keeps its own identity; links do not merge accounts.

// SetSSOLink links an account to the user of the identity provider with the given user name (administrators).
func (s *Service) SetSSOLink(ctx context.Context, actor *Principal, userID int64, providerUsername string) (*User, error) {
	name := strings.TrimSpace(providerUsername)
	if name == "" || len(name) > 200 || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, apperr.Invalid("user.sso-link", "Enter the user name of the person at the identity provider.")
	}
	var u *User
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := linkable(cur); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE users SET sso_link = $2, updated_at = now() WHERE id = $1", userID, name)
		if db.IsUniqueViolation(err, "users_sso_link_key") {
			return apperr.Conflict("user.sso-link-taken", "Another account waits for the sign-in of %s already.", name)
		}
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.sso-link", TargetType: "user",
			TargetID: audit.ID(userID), Details: map[string]any{"username": name, "previous": cur.SSOLink}}); err != nil {
			return err
		}
		u, err = GetUser(ctx, tx, userID)
		return err
	})
	return u, err
}

// RemoveSSOLink withdraws the link of an account (administrators); without a link it changes nothing.
func (s *Service) RemoveSSOLink(ctx context.Context, actor *Principal, userID int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockUser(ctx, tx, userID)
		if err != nil || cur.SSOLink == "" {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET sso_link = NULL, updated_at = now() WHERE id = $1", userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.sso-link.remove", TargetType: "user",
			TargetID: audit.ID(userID), Details: map[string]any{"username": cur.SSOLink}})
	})
}

// lockUser returns a user and locks its row until the end of the transaction.
func lockUser(ctx context.Context, tx pgx.Tx, id int64) (*User, error) {
	if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = $1 FOR UPDATE", id); err != nil {
		return nil, err
	}
	return GetUser(ctx, tx, id)
}

// linkable checks that single sign-on may take an account over: a person or placeholder without an identity of
// the provider and without a local password (an account with a password signs in with it).
func linkable(u *User) error {
	switch {
	case u.Kind != KindPerson && u.Kind != KindPlaceholder || u.Status == StatusAnonymised:
		return apperr.Conflict("user.sso-link-kind", "Only people and placeholders can be linked to single sign-on.")
	case u.External:
		return apperr.Conflict("user.sso-linked", "%s signs in through single sign-on already.", u.DisplayName)
	case u.HasPassword:
		return apperr.Conflict("user.sso-link-password", "%s signs in with a local password.", u.DisplayName)
	}
	return nil
}
