// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package webhooks

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/platform/api"
)

const tag = "Webhooks"

// Handler serves the webhook API.
type Handler struct {
	S *Service
}

type body[T any] struct {
	Body T
}

// ScopeQuery selects the webhooks of a scope.
type ScopeQuery struct {
	Scope string `query:"scope" required:"true" enum:"instance,project,space,code-project,repository"`
	Key   string `query:"key" maxLength:"200" doc:"Key of the project, space or code project; KEY/slug of a repository"`
}

// IDPath addresses a webhook or a delivery.
type IDPath struct {
	ID int64 `path:"id"`
}

// Started names a delivery that was started.
type Started struct {
	DeliveryID int64 `json:"deliveryId"`
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	s := h.S
	huma.Register(a, api.Op("list-webhook-events", http.MethodGet, "/api/v1/webhooks/events", "Events webhooks can subscribe to", tag),
		func(ctx context.Context, _ *struct{}) (*body[[]EventType], error) {
			if _, err := authz.RequireUser(ctx); err != nil {
				return nil, err
			}
			return &body[[]EventType]{Body: Catalogue}, nil
		})
	huma.Register(a, api.Op("list-webhooks", http.MethodGet, "/api/v1/webhooks", "List the webhooks of a scope", tag),
		func(ctx context.Context, in *ScopeQuery) (*body[[]Webhook], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			list, err := s.List(ctx, p, in.Scope, in.Key)
			if err != nil {
				return nil, err
			}
			return &body[[]Webhook]{Body: list}, nil
		})
	huma.Register(a, api.Op("create-webhook", http.MethodPost, "/api/v1/webhooks", "Create a webhook", tag),
		func(ctx context.Context, in *struct {
			ScopeQuery
			Body WebhookInput
		}) (*body[*Webhook], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			w, err := s.Create(ctx, p, in.Scope, in.Key, in.Body)
			if err != nil {
				return nil, err
			}
			return &body[*Webhook]{Body: w}, nil
		})
	huma.Register(a, api.Op("update-webhook", http.MethodPut, "/api/v1/webhooks/{id}", "Change a webhook", tag),
		func(ctx context.Context, in *struct {
			IDPath
			Body WebhookInput
		}) (*body[*Webhook], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			w, err := s.Update(ctx, p, in.ID, in.Body)
			if err != nil {
				return nil, err
			}
			return &body[*Webhook]{Body: w}, nil
		})
	huma.Register(a, api.Op("delete-webhook", http.MethodDelete, "/api/v1/webhooks/{id}", "Delete a webhook", tag),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.Delete(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("ping-webhook", http.MethodPost, "/api/v1/webhooks/{id}/ping", "Send a test event", tag),
		func(ctx context.Context, in *IDPath) (*body[Started], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			id, err := s.Ping(ctx, p, in.ID)
			if err != nil {
				return nil, err
			}
			return &body[Started]{Body: Started{DeliveryID: id}}, nil
		})
	huma.Register(a, api.Op("list-webhook-deliveries", http.MethodGet, "/api/v1/webhooks/{id}/deliveries", "The latest deliveries of a webhook",
		tag),
		func(ctx context.Context, in *struct {
			IDPath
			Limit int `query:"limit" minimum:"1" maximum:"200" default:"50"`
		}) (*body[[]Delivery], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			list, err := s.Deliveries(ctx, p, in.ID, in.Limit)
			if err != nil {
				return nil, err
			}
			return &body[[]Delivery]{Body: list}, nil
		})
	huma.Register(a, api.Op("redeliver-webhook", http.MethodPost, "/api/v1/webhooks/deliveries/{id}/redeliver", "Deliver a payload again", tag),
		func(ctx context.Context, in *IDPath) (*body[Started], error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			id, err := s.Redeliver(ctx, p, in.ID)
			if err != nil {
				return nil, err
			}
			return &body[Started]{Body: Started{DeliveryID: id}}, nil
		})
}
