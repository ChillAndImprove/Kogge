// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/platform/api"
)

const tagAgile = "Boards and sprints"

type boardPath struct {
	ID int64 `path:"id" doc:"Board id"`
}

type sprintPath struct {
	ID int64 `path:"id" doc:"Sprint id"`
}

type boardOutput struct {
	ETag string `header:"ETag"`
	Body *Board
}

func boardOut(b *Board, err error) (*boardOutput, error) {
	if err != nil {
		return nil, err
	}
	return &boardOutput{ETag: api.ETag(b.Version), Body: b}, nil
}

// registerAgile adds the operations of boards, sprints and agile reports.
func (h *Handler) registerAgile(a huma.API) {
	s := h.S

	huma.Register(a, api.Op("list-boards", http.MethodGet, "/api/v1/boards", "List a page of boards", tagAgile),
		func(ctx context.Context, in *struct {
			Project string `query:"project" doc:"Only boards of this project (key)"`
			Query   string `query:"query" maxLength:"200" doc:"Part of the name of the board or of the key or name of its project"`
			Type    string `query:"type" enum:"scrum,kanban," doc:"Only boards of this type"`
			api.Page
		}) (*body[*BoardList], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Boards(ctx, p, BoardFilter{Project: in.Project, Query: in.Query, Type: in.Type, Limit: in.Limit, Offset: in.Offset}))
		})
	huma.Register(a, api.Op("create-board", http.MethodPost, "/api/v1/boards", "Create a board", tagAgile),
		func(ctx context.Context, in *body[BoardInput]) (*boardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return boardOut(s.CreateBoard(ctx, p, in.Body))
		})
	huma.Register(a, api.Op("get-board", http.MethodGet, "/api/v1/boards/{id}", "Get a board", tagAgile),
		func(ctx context.Context, in *boardPath) (*boardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return boardOut(s.GetBoard(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("update-board", http.MethodPut, "/api/v1/boards/{id}", "Change a board", tagAgile),
		func(ctx context.Context, in *struct {
			ID      int64  `path:"id"`
			IfMatch string `header:"If-Match" doc:"ETag of the board to change"`
			Body    BoardInput
		}) (*boardOutput, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			version, _, err := api.IfMatch(in.IfMatch)
			if err != nil {
				return nil, err
			}
			return boardOut(s.UpdateBoard(ctx, p, in.ID, version, in.Body))
		})
	huma.Register(a, api.Op("delete-board", http.MethodDelete, "/api/v1/boards/{id}", "Delete a board", tagAgile),
		func(ctx context.Context, in *boardPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteBoard(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("get-board-view", http.MethodGet, "/api/v1/boards/{id}/view", "Cards of a board", tagAgile),
		func(ctx context.Context, in *struct {
			ID           int64 `path:"id"`
			QuickFilters []int `query:"quickFilter" doc:"Ids of quick filters to apply"`
			Sprint       int64 `query:"sprint" doc:"Only this active sprint (Scrum boards with parallel sprints)"`
		}) (*body[*BoardView], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.BoardData(ctx, p, in.ID, in.QuickFilters, in.Sprint))
		})
	huma.Register(a, api.Op("get-backlog", http.MethodGet, "/api/v1/boards/{id}/backlog", "Backlog and sprints of a Scrum board", tagAgile),
		func(ctx context.Context, in *struct {
			ID           int64  `path:"id"`
			QuickFilters []int  `query:"quickFilter"`
			Epic         string `query:"epic" doc:"Only issues of this epic (key), or none for issues without an epic"`
		}) (*body[*BacklogView], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Backlog(ctx, p, in.ID, in.QuickFilters, in.Epic))
		})
	huma.Register(a, api.Op("list-board-sprints", http.MethodGet, "/api/v1/boards/{id}/sprints", "Sprints of a board", tagAgile),
		func(ctx context.Context, in *struct {
			ID    int64  `path:"id"`
			State string `query:"state" enum:"future,active,closed," doc:"Only sprints in this state"`
		}) (*body[[]Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := s.GetBoard(ctx, p, in.ID); err != nil {
				return nil, err
			}
			if in.State != "" {
				return out(listSprints(ctx, s.pool, "s.board_id = $1 AND s.state = $2 ORDER BY s.completed_at DESC NULLS LAST, s.position, s.id", in.ID, in.State))
			}
			return out(listSprints(ctx, s.pool, "s.board_id = $1 ORDER BY s.state = 'closed', s.completed_at DESC NULLS LAST, s.position, s.id", in.ID))
		})
	huma.Register(a, api.Op("list-sprints", http.MethodGet, "/api/v1/sprints", "Sprints of a project's boards", tagAgile),
		func(ctx context.Context, in *struct {
			Project string `query:"project" required:"true" doc:"Project key"`
			Open    bool   `query:"open" doc:"Only future and active sprints"`
		}) (*body[[]Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.ProjectSprints(ctx, p, in.Project, in.Open))
		})
	huma.Register(a, api.Op("create-sprint", http.MethodPost, "/api/v1/boards/{id}/sprints", "Create a sprint", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body SprintInput
		}) (*body[*Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreateSprint(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("order-sprints", http.MethodPut, "/api/v1/boards/{id}/sprint-order", "Reorder the future sprints", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body []int64
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.OrderSprints(ctx, p, in.ID, in.Body)
		})
	huma.Register(a, api.Op("get-sprint", http.MethodGet, "/api/v1/sprints/{id}", "Get a sprint", tagAgile),
		func(ctx context.Context, in *sprintPath) (*body[*Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.GetSprint(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("update-sprint", http.MethodPut, "/api/v1/sprints/{id}", "Change a sprint", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body SprintInput
		}) (*body[*Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.UpdateSprint(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("delete-sprint", http.MethodDelete, "/api/v1/sprints/{id}", "Delete a sprint", tagAgile),
		func(ctx context.Context, in *sprintPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteSprint(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("start-sprint", http.MethodPost, "/api/v1/sprints/{id}/start", "Start a sprint", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body SprintInput
		}) (*body[*Sprint], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.StartSprint(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("complete-sprint", http.MethodPost, "/api/v1/sprints/{id}/complete", "Complete a sprint", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body CompleteSprintInput
		}) (*body[*CompleteSprintResult], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CompleteSprint(ctx, p, in.ID, in.Body))
		})
	huma.Register(a, api.Op("move-to-sprint", http.MethodPost, "/api/v1/sprints/{id}/issues", "Move issues into a sprint", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body MoveIssuesInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.MoveToSprint(ctx, p, in.ID, in.Body)
		})
	huma.Register(a, api.Op("move-to-backlog", http.MethodPost, "/api/v1/backlog/issues", "Move issues out of their sprint", tagAgile),
		func(ctx context.Context, in *body[MoveIssuesInput]) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.MoveToSprint(ctx, p, 0, in.Body)
		})
	huma.Register(a, api.Op("rank-issues", http.MethodPut, "/api/v1/issues/rank", "Change the rank of issues", tagAgile),
		func(ctx context.Context, in *body[RankInput]) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.RankIssues(ctx, p, in.Body)
		})

	// Reports
	huma.Register(a, api.Op("get-sprint-burndown", http.MethodGet, "/api/v1/sprints/{id}/burndown", "Burndown of a sprint", tagAgile),
		func(ctx context.Context, in *sprintPath) (*body[*Burndown], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.SprintBurndown(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("get-sprint-report", http.MethodGet, "/api/v1/sprints/{id}/report", "Sprint report", tagAgile),
		func(ctx context.Context, in *sprintPath) (*body[*SprintReport], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.GetSprintReport(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("get-board-velocity", http.MethodGet, "/api/v1/boards/{id}/velocity", "Velocity of the last sprints", tagAgile),
		func(ctx context.Context, in *struct {
			ID    int64 `path:"id"`
			Count int   `query:"count" minimum:"1" maximum:"20" default:"7"`
		}) (*body[*Velocity], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.BoardVelocity(ctx, p, in.ID, in.Count))
		})
	huma.Register(a, api.Op("get-board-cumulative-flow", http.MethodGet, "/api/v1/boards/{id}/cumulative-flow", "Cumulative flow", tagAgile),
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Days int   `query:"days" minimum:"1" maximum:"365" default:"30"`
		}) (*body[*CumulativeFlow], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.BoardCumulativeFlow(ctx, p, in.ID, in.Days))
		})
	huma.Register(a, api.Op("get-created-vs-resolved", http.MethodGet, "/api/v1/reports/created-vs-resolved", "Created vs. resolved issues", tagAgile),
		func(ctx context.Context, in *struct {
			Query  string `query:"q" maxLength:"10000" doc:"KQL"`
			Days   int    `query:"days" minimum:"1" maximum:"730" default:"30"`
			Period string `query:"period" enum:"day,week" default:"day"`
		}) (*body[*CreatedResolved], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.CreatedVsResolved(ctx, p, in.Query, in.Days, in.Period))
		})
}
