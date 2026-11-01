// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package apitest

import (
	"context"

	"kogge.de/kogge/internal/account"
	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/config"
)

func accountBootstrap(ctx context.Context, a *app.App, cfg *config.Config) (bool, error) {
	return account.EnsureBootstrapAdmin(ctx, a.Identity, cfg.BootstrapAdmin)
}
