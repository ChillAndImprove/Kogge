// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/search"
)

// registerFind adds the quick search, the search page, the recently visited and the activity streams.
func (h *Handler) registerFind(a huma.API) {
	s := h.S
	const tagSearch, tagActivity = "Search", "Activity"
	huma.Register(a, api.Op("quick-search", http.MethodGet, "/api/v1/quick-search",
		"Find issues, pages, repositories, people … by key and title, as you type", tagSearch),
		func(ctx context.Context, in *struct {
			Query string `query:"q" maxLength:"500" doc:"What to look for; an issue key finds its issue first"`
		}) (*struct{ Body *QuickResult }, error) {
			res, err := s.QuickSearch(ctx, identity.From(ctx), in.Query)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *QuickResult }{res}, nil
		})
	huma.Register(a, api.Op("search-all", http.MethodGet, "/api/v1/search-all", "Search the titles and texts of all products", tagSearch),
		func(ctx context.Context, in *struct {
			Query  string `query:"q" maxLength:"500" doc:"What to look for"`
			Kind   string `query:"kind" enum:"issue,project,page,blog,space,repository,pull-request,code-project,pipelines,user," doc:"Only this kind, a page of it at a time"`
			Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"5" doc:"Hits per kind"`
			Offset int    `query:"offset" minimum:"0" maximum:"10000" default:"0" doc:"Hits to skip (with kind)"`
		}) (*struct{ Body *SearchResult }, error) {
			offset := in.Offset
			if in.Kind == "" {
				offset = 0
			}
			res, err := s.SearchAll(ctx, identity.From(ctx), in.Query, in.Kind, in.Limit, offset)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *SearchResult }{res}, nil
		})
	huma.Register(a, api.Op("list-recent-visits", http.MethodGet, "/api/v1/me/recent", "What I opened last (and may still see)", tagSearch),
		func(ctx context.Context, in *struct {
			Limit int `query:"limit" minimum:"1" maximum:"50" default:"10"`
		}) (*struct{ Body []search.Hit }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			list, err := s.Recent(ctx, p, in.Limit)
			if err != nil {
				return nil, err
			}
			return &struct{ Body []search.Hit }{list}, nil
		})
	huma.Register(a, api.Op("record-visit", http.MethodPost, "/api/v1/me/recent", "Remember that I opened something", tagSearch),
		func(ctx context.Context, in *struct {
			Body struct {
				Kind string `json:"kind" enum:"issue,project,page,space,code-project,repository,pull-request,run" doc:"Pages include blog posts"`
				ID   int64  `json:"id" minimum:"1"`
			}
		}) (*struct{}, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.RecordVisit(ctx, p, in.Body.Kind, in.Body.ID)
		})
	huma.Register(a, api.Op("get-activity-stream", http.MethodGet, "/api/v1/activity-stream",
		"What happened recently: issues, pages, pushes, pull requests, runs and deployments", tagActivity),
		func(ctx context.Context, in *struct {
			Product    string `query:"product" enum:"kontor,logbuch,hafen,helling," doc:"Only this product"`
			Project    string `query:"project" maxLength:"32" doc:"Only this issue project (key)"`
			Space      string `query:"space" maxLength:"64" doc:"Only this space (key)"`
			Repository string `query:"repository" maxLength:"300" doc:"Only this repository (KEY/slug)"`
			User       string `query:"user" maxLength:"200" doc:"Only what this person did (user name)"`
			Mine       bool   `query:"mine" doc:"Only what I watch or am involved in"`
			Before     string `query:"before" maxLength:"64" doc:"Read on after the entry with this key"`
			Limit      int    `query:"limit" minimum:"1" maximum:"100" default:"30"`
		}) (*struct{ Body *ActivityPage }, error) {
			p, err := authz.RequireUser(ctx)
			if err != nil {
				return nil, err
			}
			page, err := s.Activity(ctx, p, ActivityFilter{Product: in.Product, Project: in.Project, Space: in.Space, Repository: in.Repository,
				User: in.User, Mine: in.Mine, Before: in.Before, Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			return &struct{ Body *ActivityPage }{page}, nil
		})
}
