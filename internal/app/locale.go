// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app

import (
	"context"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
)

// initLocale gives a new installation the language of KOGGE_DEFAULT_LOCALE before its default
// configuration — roles, issue types, statuses, workflows, wiki templates — is created in the installation's
// language. Only at the very first start: afterwards the language is a setting of the administrators
// (Administration → Settings), and a KOGGE_DEFAULT_LOCALE that differs from it is reported and ignored.
func (a *App) initLocale(ctx context.Context) error {
	locale := a.Cfg.DefaultLocale
	var first bool
	err := db.InTx(ctx, a.Pool, func(tx pgx.Tx) error {
		// Nodes that start at the same time agree on one of them.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7366000)"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM settings WHERE key IN ('instance', 'issues.defaults'))
			AND NOT EXISTS (SELECT 1 FROM issue_types)`).Scan(&first); err != nil {
			return err
		}
		if !first || locale == "" {
			return nil
		}
		st := settings.Defaults()
		st.DefaultLocale = locale
		if locale == "en" {
			st.ContentLanguage = "english" // the language of the content, for the full-text search
		}
		return a.Settings.Save(ctx, tx, st, identity.SystemUserID)
	})
	if err != nil || locale == "" {
		return err
	}
	st, err := a.Settings.Get(ctx)
	switch {
	case err != nil:
		return err
	case first:
		a.Log.Info("a new installation: its language is the one of KOGGE_DEFAULT_LOCALE", "locale", st.DefaultLocale)
	case st.DefaultLocale != locale:
		a.Log.Info("KOGGE_DEFAULT_LOCALE applies to new installations only; this one's language is a setting of the administration",
			"configured", locale, "locale", st.DefaultLocale)
	}
	return nil
}
