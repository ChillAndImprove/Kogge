// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package account

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
)

// RecoveryCodes are shown once, when they are created.
type RecoveryCodes struct {
	Codes []string `json:"codes" doc:"Each signs in once instead of a code of the authenticator app"`
}

type passwordAndCode struct {
	Body struct {
		Password string `json:"password" minLength:"1" maxLength:"1024" doc:"The current password"`
		Code     string `json:"code" minLength:"1" maxLength:"32" doc:"A code of the authenticator app (or a recovery code)"`
	}
}

func (h *Handler) registerTwoFactor(a huma.API) {
	const tag = "Profile"
	huma.Register(a, api.Op("get-two-factor", http.MethodGet, "/api/v1/me/two-factor", "The state of two-factor authentication", tag),
		func(ctx context.Context, _ *struct{}) (*struct{ Body *identity.TwoFactorStatus }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			st, err := h.ID.TwoFactor(ctx, p)
			return &struct{ Body *identity.TwoFactorStatus }{st}, err
		})
	huma.Register(a, api.Op("begin-two-factor", http.MethodPost, "/api/v1/me/two-factor",
		"Start setting up an authenticator app (its secret, as a QR code)", tag),
		func(ctx context.Context, _ *struct{}) (*struct{ Body *identity.TwoFactorSetup }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			setup, err := h.ID.BeginTwoFactor(ctx, p)
			return &struct{ Body *identity.TwoFactorSetup }{setup}, err
		})
	huma.Register(a, api.Op("enable-two-factor", http.MethodPost, "/api/v1/me/two-factor/enable",
		"Turn two-factor authentication on with a code of the app; returns the recovery codes", tag),
		func(ctx context.Context, in *passwordAndCode) (*struct{ Body RecoveryCodes }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			codes, err := h.ID.EnableTwoFactor(ctx, p, in.Body.Password, in.Body.Code)
			return &struct{ Body RecoveryCodes }{RecoveryCodes{Codes: codes}}, err
		})
	huma.Register(a, api.Op("disable-two-factor", http.MethodPost, "/api/v1/me/two-factor/disable",
		"Turn two-factor authentication off", tag),
		func(ctx context.Context, in *passwordAndCode) (*struct{}, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			return nil, h.ID.DisableTwoFactor(ctx, p, in.Body.Password, in.Body.Code)
		})
	huma.Register(a, api.Op("renew-recovery-codes", http.MethodPost, "/api/v1/me/two-factor/recovery-codes",
		"Replace the recovery codes", tag),
		func(ctx context.Context, in *struct {
			Body struct {
				Password string `json:"password" minLength:"1" maxLength:"1024" doc:"The current password"`
			}
		}) (*struct{ Body RecoveryCodes }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			codes, err := h.ID.NewRecoveryCodes(ctx, p, in.Body.Password)
			return &struct{ Body RecoveryCodes }{RecoveryCodes{Codes: codes}}, err
		})
}
