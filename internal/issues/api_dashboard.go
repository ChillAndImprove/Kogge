// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/platform/api"
)

const tagDashboards = "Dashboards"

type dashboardOutput struct {
	ETag string `header:"ETag"`
	Body *Dashboard
}

func dashboardOut(d *Dashboard, err error) (*dashboardOutput, error) {
	if err != nil {
		return nil, err
	}
	return &dashboardOutput{ETag: api.ETag(d.Version), Body: d}, nil
}

// registerDashboards adds the dashboard operations and the statistics their widgets use.
func (h *Handler) registerDashboards(a huma.API) {
	s := h.S
	huma.Register(a, api.Op("list-dashboards", http.MethodGet, "/api/v1/dashboards", "List a page of the dashboards visible to the caller", tagDashboards),
		func(ctx context.Context, in *sharedListInput) (*body[*DashboardList], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Dashboards(ctx, p, in.selection()))
		})
	huma.Register(a, api.Op("create-dashboard", http.MethodPost, "/api/v1/dashboards", "Create a dashboard", tagDashboards),
		func(ctx context.Context, in *body[DashboardInput]) (*dashboardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return dashboardOut(s.SaveDashboard(ctx, p, 0, 0, in.Body))
		})
	huma.Register(a, api.Op("get-dashboard", http.MethodGet, "/api/v1/dashboards/{id}", "Get a dashboard", tagDashboards),
		func(ctx context.Context, in *idPath) (*dashboardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return dashboardOut(s.GetDashboard(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("update-dashboard", http.MethodPut, "/api/v1/dashboards/{id}", "Change a dashboard", tagDashboards),
		func(ctx context.Context, in *struct {
			ID      int64  `path:"id"`
			IfMatch string `header:"If-Match"`
			Body    DashboardInput
		}) (*dashboardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			version, _, err := api.IfMatch(in.IfMatch)
			if err != nil {
				return nil, err
			}
			return dashboardOut(s.SaveDashboard(ctx, p, in.ID, version, in.Body))
		})
	huma.Register(a, api.Op("delete-dashboard", http.MethodDelete, "/api/v1/dashboards/{id}", "Delete a dashboard", tagDashboards),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteDashboard(ctx, p, in.ID)
		})
	fav := func(on bool) func(ctx context.Context, in *idPath) (*struct{}, error) {
		return func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SetDashboardFavourite(ctx, p, in.ID, on)
		}
	}
	huma.Register(a, api.Op("favourite-dashboard", http.MethodPut, "/api/v1/dashboards/{id}/favourite", "Add to my dashboards", tagDashboards), fav(true))
	huma.Register(a, api.Op("unfavourite-dashboard", http.MethodDelete, "/api/v1/dashboards/{id}/favourite", "Remove from my dashboards", tagDashboards), fav(false))

	huma.Register(a, api.Op("get-distribution", http.MethodGet, "/api/v1/reports/distribution", "Count issues by a field", tagDashboards),
		func(ctx context.Context, in *struct {
			Query string `query:"q" maxLength:"10000" doc:"KQL"`
			Field string `query:"field" required:"true" doc:"status, statusCategory, priority, assignee, reporter, issuetype, resolution, project, labels, components, fixVersions or a select field (cf_<id>)"`
		}) (*body[*Distribution], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.IssueDistribution(ctx, p, in.Query, in.Field))
		})
	huma.Register(a, api.Op("get-matrix", http.MethodGet, "/api/v1/reports/matrix", "Count issues by two fields", tagDashboards),
		func(ctx context.Context, in *struct {
			Query string `query:"q" maxLength:"10000"`
			X     string `query:"x" required:"true"`
			Y     string `query:"y" required:"true"`
		}) (*body[*Matrix], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.IssueMatrix(ctx, p, in.Query, in.X, in.Y))
		})
	huma.Register(a, api.Op("get-sprint-health", http.MethodGet, "/api/v1/boards/{id}/sprint-health", "Progress of the active sprints", tagDashboards),
		func(ctx context.Context, in *idPath) (*body[*SprintHealth], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.BoardSprintHealth(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("get-issue-activity", http.MethodGet, "/api/v1/activity", "Recent changes of issues", tagDashboards),
		func(ctx context.Context, in *struct {
			Project string `query:"project" doc:"Only this project (key)"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"30"`
		}) (*body[[]ActivityItem], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Activity(ctx, p, in.Project, in.Limit))
		})
}
