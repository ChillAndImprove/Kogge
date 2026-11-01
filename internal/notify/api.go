// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package notify

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/platform/api"
)

// Handler serves the inbox API.
type Handler struct {
	Pool *pgxpool.Pool
	// Mail tells whether e-mail is configured.
	Mail bool
	// Visible re-checks the subjects of notifications.
	Visible Visibilities
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	huma.Register(a, api.Op("list-notifications", http.MethodGet, "/api/v1/notifications", "Notification inbox", "Notifications"),
		h.list)
	huma.Register(a, api.Op("count-notifications", http.MethodGet, "/api/v1/notifications/unread", "Number of unread notifications",
		"Notifications"), h.count)
	huma.Register(a, api.Op("read-notifications", http.MethodPost, "/api/v1/notifications/read", "Mark notifications as read",
		"Notifications"), h.read)
	huma.Register(a, api.Op("get-notification-preferences", http.MethodGet, "/api/v1/me/notification-preferences",
		"How the caller receives notifications by e-mail", "Notifications"), h.preferences)
	huma.Register(a, api.Op("set-notification-preferences", http.MethodPut, "/api/v1/me/notification-preferences",
		"Change how the caller receives notifications by e-mail", "Notifications"), h.setPreferences)
}

// Preference is the e-mail delivery of a category of notifications.
type Preference struct {
	Category string `json:"category" enum:"issues,wiki,code,builds,system"`
	Mode     string `json:"mode" enum:"immediately,hourly,daily,off" doc:"immediately (several changes within minutes are combined), hourly or daily digest, off"`
}

// Preferences are a user's e-mail settings.
type Preferences struct {
	Mail        bool         `json:"mail" doc:"E-mail is configured on this installation"`
	Email       string       `json:"email" doc:"The address notifications go to; empty or unverified addresses get none"`
	Preferences []Preference `json:"preferences"`
}

func (h *Handler) preferences(ctx context.Context, _ *struct{}) (*struct{ Body Preferences }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	modes, err := UserModes(ctx, h.Pool, p.UserID())
	if err != nil {
		return nil, err
	}
	out := Preferences{Mail: h.Mail, Preferences: []Preference{}}
	if p.User.EmailVerified {
		out.Email = p.User.Email
	}
	for _, c := range Categories {
		out.Preferences = append(out.Preferences, Preference{Category: c, Mode: modes[c]})
	}
	return &struct{ Body Preferences }{out}, nil
}

func (h *Handler) setPreferences(ctx context.Context, in *struct {
	Body []Preference `maxItems:"10"`
}) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, SetUserModes(ctx, h.Pool, p.UserID(), in.Body)
}

func (h *Handler) list(ctx context.Context, in *struct {
	Unread bool  `query:"unread" doc:"Only unread notifications"`
	Before int64 `query:"before" doc:"Return notifications with smaller ids (paging)"`
	Limit  int   `query:"limit" minimum:"1" maximum:"200" default:"50"`
}) (*struct{ Body []Notification }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := List(ctx, h.Pool, p.UserID(), in.Unread, in.Before, in.Limit)
	if err != nil {
		return nil, err
	}
	if ns == nil {
		ns = []Notification{}
	}
	if err := h.Visible.redact(ctx, p, ns); err != nil {
		return nil, err
	}
	return &struct{ Body []Notification }{ns}, nil
}

func (h *Handler) count(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		Unread int `json:"unread"`
	}
}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, err := Unread(ctx, h.Pool, p.UserID())
	if err != nil {
		return nil, err
	}
	out := &struct {
		Body struct {
			Unread int `json:"unread"`
		}
	}{}
	out.Body.Unread = n
	return out, nil
}

func (h *Handler) read(ctx context.Context, in *struct {
	Body struct {
		IDs []int64 `json:"ids,omitempty" maxItems:"500" doc:"Empty marks all as read"`
	}
}) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, MarkRead(ctx, h.Pool, p.UserID(), in.Body.IDs)
}
