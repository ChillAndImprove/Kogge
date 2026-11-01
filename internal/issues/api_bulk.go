// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

// registerBulk adds the operations of moves, bulk changes and the query box.
func (h *Handler) registerBulk(a huma.API) {
	const ti, ts = "Issues", "Search"
	s := h.S
	huma.Register(a, api.Op("move-issue", http.MethodPost, "/api/v1/issues/{key}/move",
		"Move an issue (with its sub-tasks) to another project", ti),
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body MoveInput
		}) (*issueOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return issueOut(s.MoveIssue(ctx, p, in.Key, in.Body))
		})
	huma.Register(a, api.Op("plan-issue-move", http.MethodPost, "/api/v1/issues/move-plan",
		"What moving issues to a project does, and what has to be chosen for it", ti),
		func(ctx context.Context, in *struct {
			Lang string `header:"Accept-Language" doc:"de or en: the language of the reasons"`
			Body struct {
				Issues []string `json:"issues" minItems:"1" maxItems:"1000" doc:"Issue keys"`
				MoveInput
			}
		}) (*body[*MovePlan], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.PlanMove(ctx, p, in.Body.Issues, in.Body.MoveInput, apperr.Language(in.Lang)))
		})
	huma.Register(a, api.Op("check-bulk-change", http.MethodPost, "/api/v1/issues/bulk/check",
		"What a bulk change would do with the issues", ti),
		func(ctx context.Context, in *struct {
			Lang string `header:"Accept-Language" doc:"de or en: the language of the reasons"`
			Body BulkCheckInput
		}) (*body[*BulkCheck], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CheckBulkChange(ctx, p, in.Body, apperr.Language(in.Lang)))
		})
	start := api.Op("create-bulk-change", http.MethodPost, "/api/v1/issues/bulk",
		"Start a bulk change: edit, transition, move, delete or watch many issues", ti)
	start.DefaultStatus = http.StatusAccepted
	huma.Register(a, start, func(ctx context.Context, in *struct {
		Lang string `header:"Accept-Language" doc:"de or en: the language of the results"`
		Body BulkInput
	}) (*body[*BulkChange], error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return out(s.StartBulkChange(ctx, p, in.Body, apperr.Language(in.Lang)))
	})
	huma.Register(a, api.Op("list-bulk-changes", http.MethodGet, "/api/v1/issues/bulk", "The caller's recent bulk changes", ti),
		func(ctx context.Context, _ *struct{}) (*body[[]BulkChange], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.BulkChanges(ctx, p))
		})
	huma.Register(a, api.Op("get-bulk-change", http.MethodGet, "/api/v1/issues/bulk/{id}", "A bulk change with its results", ti),
		func(ctx context.Context, in *idPath) (*body[*BulkChange], error) {
			return out(s.GetBulkChange(ctx, principal(ctx), in.ID))
		})

	huma.Register(a, api.Op("kql-autocomplete", http.MethodGet, "/api/v1/search/autocomplete",
		"Fields, operators, functions and keywords of KQL, for the query box", ts),
		func(ctx context.Context, _ *struct{}) (*body[*KQLCatalogue], error) {
			return out(s.KQLCatalogue(ctx, principal(ctx)))
		})
	huma.Register(a, api.Op("kql-suggestions", http.MethodGet, "/api/v1/search/suggestions",
		"Values of a field that start with a prefix, of what the caller may see", ts),
		func(ctx context.Context, in *struct {
			Field  string `query:"field" required:"true" maxLength:"100" doc:"The field as written in the query, e.g. assignee or \"Story Points\""`
			Prefix string `query:"prefix" maxLength:"100" doc:"What was typed of the value"`
		}) (*body[[]KQLSuggestion], error) {
			return out(s.KQLSuggestions(ctx, principal(ctx), in.Field, in.Prefix))
		})
}
