// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/platform/api"
)

// registerSubscriptions adds the operations of filter subscriptions.
func (h *Handler) registerSubscriptions(a huma.API) {
	const tag = "Search"
	s := h.S
	huma.Register(a, api.Op("list-filter-subscriptions", http.MethodGet, "/api/v1/filters/{id}/subscriptions",
		"Subscriptions of a filter", tag), func(ctx context.Context, in *idPath) (*body[*FilterSubscriptions], error) {
		return out(s.FilterSubscriptions(ctx, principal(ctx), in.ID))
	})
	huma.Register(a, api.Op("subscribe-filter", http.MethodPost, "/api/v1/filters/{id}/subscriptions",
		"Subscribe to a filter by e-mail", tag), func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body SubscriptionInput
	}) (*body[*Subscription], error) {
		return out(s.Subscribe(ctx, principal(ctx), in.ID, in.Body))
	})
	huma.Register(a, api.Op("list-my-filter-subscriptions", http.MethodGet, "/api/v1/filter-subscriptions",
		"The caller's filter subscriptions", tag), func(ctx context.Context, _ *struct{}) (*body[*FilterSubscriptions], error) {
		return out(s.MySubscriptions(ctx, principal(ctx)))
	})
	huma.Register(a, api.Op("update-filter-subscription", http.MethodPut, "/api/v1/filter-subscriptions/{id}",
		"Change when a subscription sends", tag), func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body SubscriptionChange
	}) (*body[*Subscription], error) {
		return out(s.UpdateSubscription(ctx, principal(ctx), in.ID, in.Body))
	})
	huma.Register(a, api.Op("delete-filter-subscription", http.MethodDelete, "/api/v1/filter-subscriptions/{id}",
		"Delete a subscription", tag), func(ctx context.Context, in *idPath) (*struct{}, error) {
		return nil, s.Unsubscribe(ctx, principal(ctx), in.ID)
	})
	huma.Register(a, api.Op("send-filter-subscription", http.MethodPost, "/api/v1/filter-subscriptions/{id}/send",
		"Send a subscription now", tag), func(ctx context.Context, in *idPath) (*struct{}, error) {
		return nil, s.SendSubscription(ctx, principal(ctx), in.ID)
	})
	huma.Register(a, api.Op("mute-filter-subscription", http.MethodPut, "/api/v1/filter-subscriptions/{id}/mute",
		"Stop the e-mails of a group subscription for oneself", tag), func(ctx context.Context, in *idPath) (*struct{}, error) {
		return nil, s.MuteSubscription(ctx, principal(ctx), in.ID, true)
	})
	huma.Register(a, api.Op("unmute-filter-subscription", http.MethodDelete, "/api/v1/filter-subscriptions/{id}/mute",
		"Receive the e-mails of a group subscription again", tag), func(ctx context.Context, in *idPath) (*struct{}, error) {
		return nil, s.MuteSubscription(ctx, principal(ctx), in.ID, false)
	})
}
