// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
)

const (
	tagRuns         = "Pipelines"
	tagEnvironments = "Environments"
	tagVariables    = "Pipeline variables"
	tagAgents       = "Agents"
)

// Handler serves the JSON API of the module.
type Handler struct {
	S *Service
}

type body[T any] struct {
	Body T
}

func out[T any](v T, err error) (*body[T], error) {
	if err != nil {
		return nil, err
	}
	return &body[T]{Body: v}, nil
}

func signedIn(ctx context.Context) (*identity.Principal, error) { return authz.RequireUser(ctx) }

func principal(ctx context.Context) *identity.Principal { return identity.From(ctx) }

// RepoPath addresses a repository.
type RepoPath struct {
	Key  string `path:"key" doc:"Code project key"`
	Slug string `path:"slug" doc:"Repository slug"`
}

// EnvPath addresses an environment of a repository.
type EnvPath struct {
	RepoPath
	Name string `path:"name" doc:"Environment name"`
}

// IDPath addresses a run, job, deployment, agent or token.
type IDPath struct {
	ID int64 `path:"id"`
}

// Paged is a page of a list with the total count.
type Paged[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// RunRequest starts pipelines by hand.
type RunRequest struct {
	Ref       string            `json:"ref,omitempty" maxLength:"255" doc:"Branch, tag or commit; default: the default branch"`
	Pipeline  string            `json:"pipeline,omitempty" maxLength:"100" doc:"Only this pipeline; default: all that allow manual runs"`
	Variables map[string]string `json:"variables,omitempty" doc:"Values for the variables of the run form"`
}

// ApprovalRequest approves or rejects a deployment.
type ApprovalRequest struct {
	Approved bool   `json:"approved"`
	Comment  string `json:"comment,omitempty" maxLength:"2000"`
}

// AgentChange enables or disables an agent.
type AgentChange struct {
	Enabled   *bool     `json:"enabled,omitempty"`
	Approve   bool      `json:"approve,omitempty" doc:"Let a new agent take jobs"`
	Labels    *[]string `json:"labels,omitempty" maxItems:"50" doc:"The agent's labels, which decide which jobs it gets"`
	Protected *bool     `json:"protected,omitempty" doc:"Take only jobs of refs that a branch permission of the code project protects"`
}

// NewAgentToken creates a registration token.
type NewAgentToken struct {
	Description   string `json:"description,omitempty" maxLength:"200"`
	Project       string `json:"project,omitempty" maxLength:"64" doc:"Key of a code project; agents then serve only its repositories"`
	ExpiresInDays *int   `json:"expiresInDays,omitempty" minimum:"0" maximum:"3650" doc:"Days until it expires (default 7); 0: never"`
	MaxUses       *int   `json:"maxUses,omitempty" minimum:"0" maximum:"10000" doc:"Agents the token may register (default 1); 0: any number"`
	AutoApprove   bool   `json:"autoApprove,omitempty" doc:"Agents it registers take jobs without an administrator's approval"`
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	s := h.S
	repo := "/api/v1/code/repos/{key}/{slug}"

	// Runs
	huma.Register(a, api.Op("list-runs", http.MethodGet, repo+"/runs", "List the pipeline runs of a repository", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			Branch   string `query:"branch" maxLength:"255"`
			Pipeline string `query:"pipeline" maxLength:"100"`
			State    string `query:"state" enum:"queued,running,succeeded,failed,cancelled,error,"`
			Commit   string `query:"commit" maxLength:"64"`
			api.Page
		}) (*body[Paged[RunSummary]], error) {
			list, total, err := s.Runs(ctx, principal(ctx), in.Key, in.Slug, RunFilter{Branch: in.Branch, Pipeline: in.Pipeline, State: in.State,
				Commit: in.Commit, Limit: in.Limit, Offset: in.Offset})
			return out(Paged[RunSummary]{Items: list, Total: total}, err)
		})
	// Pipeline templates and the check of pipeline files.
	tagTemplates := "Pipeline templates"
	huma.Register(a, api.Op("list-pipeline-templates", http.MethodGet, "/api/v1/builds/templates",
		"Built-in templates and those of the organisation; with a repository, those for its kind of project are recommended",
		tagTemplates), func(ctx context.Context, in *struct {
		Project    string `query:"project" maxLength:"64" doc:"Code project key of the repository"`
		Repository string `query:"repository" maxLength:"100" doc:"Repository slug"`
	}) (*body[[]PipelineTemplate], error) {
		return out(s.Templates(ctx, principal(ctx), in.Project, in.Repository))
	})
	huma.Register(a, api.Op("create-pipeline-template", http.MethodPost, "/api/v1/builds/templates",
		"Create a template of the organisation", tagTemplates), func(ctx context.Context, in *body[PipelineTemplateInput]) (*body[*PipelineTemplate], error) {
		return out(s.SaveTemplate(ctx, "", in.Body))
	})
	huma.Register(a, api.Op("update-pipeline-template", http.MethodPut, "/api/v1/builds/templates/{id}",
		"Change a template of the organisation", tagTemplates), func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"30"`
		Body PipelineTemplateInput
	}) (*body[*PipelineTemplate], error) {
		return out(s.SaveTemplate(ctx, in.ID, in.Body))
	})
	huma.Register(a, api.Op("delete-pipeline-template", http.MethodDelete, "/api/v1/builds/templates/{id}",
		"Delete a template of the organisation", tagTemplates), func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"30"`
	}) (*struct{}, error) {
		return nil, s.DeleteTemplate(ctx, in.ID)
	})
	huma.Register(a, api.Op("check-pipeline-file", http.MethodPost, "/api/v1/builds/check",
		"Check a pipeline file: its mistakes with file, line and column, or the pipelines it defines; with a repository, "+
			"the files it includes are read with the caller's permissions", tagTemplates),
		func(ctx context.Context, in *body[CheckInput]) (*body[*Check], error) {
			return out(s.CheckFile(ctx, principal(ctx), in.Body))
		})
	huma.Register(a, api.Op("get-builds-dashboard", http.MethodGet, "/api/v1/builds/dashboard",
		"What runs and waits, the last days and the pipelines of the readable repositories", tagRuns),
		func(ctx context.Context, _ *struct{}) (*body[*BuildOverview], error) {
			return out(s.Dashboard(ctx, principal(ctx)))
		})
	huma.Register(a, api.Op("list-recent-runs", http.MethodGet, "/api/v1/builds/runs", "The newest runs of all readable repositories", tagRuns),
		func(ctx context.Context, in *struct {
			State string `query:"state" enum:"queued,running,succeeded,failed,cancelled,error,"`
			Mine  bool   `query:"mine" doc:"Only runs the caller started or pushed"`
			api.Page
		}) (*body[Paged[RunSummary]], error) {
			list, total, err := s.RecentRuns(ctx, principal(ctx), RecentFilter{State: in.State, Mine: in.Mine, Limit: in.Limit, Offset: in.Offset})
			return out(Paged[RunSummary]{Items: list, Total: total}, err)
		})
	huma.Register(a, api.Op("start-run", http.MethodPost, repo+"/runs", "Start pipelines by hand", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			Body RunRequest
		}) (*body[[]StartedRun], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			ids, err := s.RunPipeline(ctx, p, in.Key, in.Slug, in.Body.Ref, in.Body.Pipeline, in.Body.Variables)
			if err != nil {
				return nil, err
			}
			return out(s.startedRuns(ctx, ids))
		})
	huma.Register(a, api.Op("get-run", http.MethodGet, repo+"/runs/{number}", "Get a pipeline run", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			Number int `path:"number"`
		}) (*body[*RunDetail], error) {
			return out(s.Run(ctx, principal(ctx), in.Key, in.Slug, in.Number))
		})
	huma.Register(a, api.Op("list-manual-pipelines", http.MethodGet, repo+"/pipelines", "Pipelines of a ref with their run forms", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			Ref string `query:"ref" maxLength:"255"`
		}) (*body[[]PipelineInfo], error) {
			return out(s.ManualPipelines(ctx, principal(ctx), in.Key, in.Slug, in.Ref))
		})
	huma.Register(a, api.Op("cancel-run", http.MethodPost, "/api/v1/builds/runs/{id}/cancel", "Cancel a run", tagRuns),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.Cancel(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("rerun-run", http.MethodPost, "/api/v1/builds/runs/{id}/rerun", "Run the pipeline of a run again", tagRuns),
		func(ctx context.Context, in *IDPath) (*body[StartedRun], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			id, err := s.Rerun(ctx, p, in.ID)
			if err != nil {
				return nil, err
			}
			list, err := s.startedRuns(ctx, []int64{id})
			if err != nil || len(list) == 0 {
				return nil, err
			}
			return out(list[0], nil)
		})
	huma.Register(a, api.Op("retry-job", http.MethodPost, "/api/v1/builds/jobs/{id}/retry", "Run a finished job again", tagRuns),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.RetryJob(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("play-job", http.MethodPost, "/api/v1/builds/jobs/{id}/play", "Start a job that waits for a manual start", tagRuns),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.Play(ctx, p, in.ID)
		})
	huma.Register(a, api.Op("get-job-log", http.MethodGet, "/api/v1/builds/jobs/{id}/log", "Read the log of a job from an offset", tagRuns),
		func(ctx context.Context, in *struct {
			IDPath
			Offset int64 `query:"offset" minimum:"0"`
		}) (*body[*LogChunk], error) {
			return out(s.JobLog(ctx, principal(ctx), in.ID, in.Offset))
		})

	// Commit statuses
	huma.Register(a, api.Op("list-commit-statuses", http.MethodGet, repo+"/statuses", "Statuses of commits", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			SHA []string `query:"sha,explode" maxItems:"100" doc:"Commit ids (repeat the parameter)"`
		}) (*body[map[string][]CommitStatus], error) {
			return out(s.Statuses(ctx, principal(ctx), in.Key, in.Slug, in.SHA))
		})
	huma.Register(a, api.Op("report-commit-status", http.MethodPost, repo+"/statuses/{sha}", "Report the status of an external check", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			SHA  string `path:"sha" maxLength:"64"`
			Body StatusInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.ReportStatus(ctx, p, in.Key, in.Slug, in.SHA, in.Body)
		})

	// Settings of the repository
	huma.Register(a, api.Op("get-build-settings", http.MethodGet, repo+"/build-settings", "Pipeline settings of a repository", tagRuns),
		func(ctx context.Context, in *RepoPath) (*body[*BuildSettings], error) {
			return out(s.RepoSettings(ctx, principal(ctx), in.Key, in.Slug))
		})
	huma.Register(a, api.Op("set-build-settings", http.MethodPut, repo+"/build-settings", "Change the pipeline settings of a repository", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			Body BuildSettings
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.SaveRepoSettings(ctx, p, in.Key, in.Slug, in.Body)
		})

	// Pipeline triggers
	huma.Register(a, api.Op("list-pipeline-triggers", http.MethodGet, repo+"/triggers", "The pipeline triggers of a repository", tagRuns),
		func(ctx context.Context, in *RepoPath) (*body[[]PipelineTrigger], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Triggers(ctx, p, in.Key, in.Slug))
		})
	huma.Register(a, api.Op("create-pipeline-trigger", http.MethodPost, repo+"/triggers", "Create a pipeline trigger (its token is shown once)",
		tagRuns), func(ctx context.Context, in *struct {
		RepoPath
		Body NewPipelineTrigger
	}) (*body[*PipelineTrigger], error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return out(s.CreateTrigger(ctx, p, in.Key, in.Slug, in.Body))
	})
	huma.Register(a, api.Op("delete-pipeline-trigger", http.MethodDelete, repo+"/triggers/{id}", "Revoke a pipeline trigger", tagRuns),
		func(ctx context.Context, in *struct {
			RepoPath
			IDPath
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteTrigger(ctx, p, in.Key, in.Slug, in.ID)
		})
	huma.Register(a, api.Op("run-pipeline-trigger", http.MethodPost, "/api/v1/builds/trigger",
		"Start pipelines with a pipeline trigger's token (Authorization: Bearer kgt_…)", tagRuns),
		func(ctx context.Context, in *struct {
			Authorization string `header:"Authorization" doc:"Bearer and the trigger's token"`
			Body          TriggerRequest
		}) (*body[[]TriggeredRun], error) {
			return out(s.RunTrigger(ctx, in.Authorization, in.Body))
		})

	// Environments and deployments
	huma.Register(a, api.Op("list-environments", http.MethodGet, repo+"/environments", "List the environments of a repository", tagEnvironments),
		func(ctx context.Context, in *RepoPath) (*body[[]Environment], error) {
			return out(s.Environments(ctx, principal(ctx), in.Key, in.Slug))
		})
	huma.Register(a, api.Op("save-environment", http.MethodPut, repo+"/environments/{name}", "Create or change an environment", tagEnvironments),
		func(ctx context.Context, in *struct {
			EnvPath
			Body EnvironmentInput
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			in.Body.Name = in.Name
			return nil, s.SaveEnvironment(ctx, p, in.Key, in.Slug, in.Body)
		})
	huma.Register(a, api.Op("delete-environment", http.MethodDelete, repo+"/environments/{name}", "Delete an environment", tagEnvironments),
		func(ctx context.Context, in *EnvPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteEnvironment(ctx, p, in.Key, in.Slug, in.Name)
		})
	huma.Register(a, api.Op("list-deployments", http.MethodGet, repo+"/environments/{name}/deployments", "A page of the deployments to an environment, newest first",
		tagEnvironments),
		func(ctx context.Context, in *struct {
			EnvPath
			api.Page
		}) (*body[Paged[Deployment]], error) {
			list, total, err := s.Deployments(ctx, principal(ctx), in.Key, in.Slug, in.Name, in.Limit, in.Offset)
			return out(Paged[Deployment]{Items: list, Total: total}, err)
		})
	huma.Register(a, api.Op("approve-deployment", http.MethodPost, "/api/v1/builds/deployments/{id}/approval", "Approve or reject a deployment",
		tagEnvironments),
		func(ctx context.Context, in *struct {
			IDPath
			Body ApprovalRequest
		}) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.Approve(ctx, p, in.ID, in.Body.Approved, in.Body.Comment)
		})
	huma.Register(a, api.Op("redeploy", http.MethodPost, "/api/v1/builds/deployments/{id}/redeploy", "Deploy the version of a deployment again",
		tagEnvironments),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.Redeploy(ctx, p, in.ID)
		})

	// Variables and secrets of all scopes
	huma.Register(a, api.Op("list-build-variables", http.MethodGet, "/api/v1/builds/variables", "List the variables of a scope", tagVariables),
		func(ctx context.Context, in *VarScope) (*body[[]Variable], error) {
			_, id, err := h.scope(ctx, in)
			if err != nil {
				return nil, err
			}
			return out(s.Variables(ctx, in.Scope, id))
		})
	huma.Register(a, api.Op("set-build-variable", http.MethodPut, "/api/v1/builds/variables/{name}", "Create or change a variable", tagVariables),
		func(ctx context.Context, in *struct {
			VarScope
			Name string `path:"name"`
			Body VariableInput
		}) (*struct{}, error) {
			p, id, err := h.scope(ctx, &in.VarScope)
			if err != nil {
				return nil, err
			}
			in.Body.Name = in.Name
			return nil, s.SetVariable(ctx, p, in.Scope, id, in.Body)
		})
	huma.Register(a, api.Op("delete-build-variable", http.MethodDelete, "/api/v1/builds/variables/{name}", "Delete a variable", tagVariables),
		func(ctx context.Context, in *struct {
			VarScope
			Name string `path:"name"`
		}) (*struct{}, error) {
			p, id, err := h.scope(ctx, &in.VarScope)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteVariable(ctx, p, in.Scope, id, in.Name)
		})

	// Agents
	huma.Register(a, api.Op("list-agents", http.MethodGet, "/api/v1/admin/agents", "List the build agents", tagAgents),
		func(ctx context.Context, _ *struct{}) (*body[[]AgentInfo], error) {
			return out(s.Agents(ctx))
		})
	huma.Register(a, api.Op("change-agent", http.MethodPatch, "/api/v1/admin/agents/{id}", "Approve, label, protect, enable or disable an agent", tagAgents),
		func(ctx context.Context, in *struct {
			IDPath
			Body AgentChange
		}) (*struct{}, error) {
			if in.Body.Approve {
				if err := s.ApproveAgent(ctx, in.ID); err != nil {
					return nil, err
				}
			}
			if in.Body.Labels != nil {
				if err := s.SetAgentLabels(ctx, in.ID, *in.Body.Labels); err != nil {
					return nil, err
				}
			}
			if in.Body.Protected != nil {
				if err := s.SetAgentProtected(ctx, in.ID, *in.Body.Protected); err != nil {
					return nil, err
				}
			}
			if in.Body.Enabled != nil {
				return nil, s.SetAgentEnabled(ctx, in.ID, *in.Body.Enabled)
			}
			return nil, nil
		})
	huma.Register(a, api.Op("delete-agent", http.MethodDelete, "/api/v1/admin/agents/{id}", "Remove an agent", tagAgents),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			return nil, s.DeleteAgent(ctx, in.ID)
		})
	huma.Register(a, api.Op("list-agent-tokens", http.MethodGet, "/api/v1/admin/agent-tokens", "List the agent registration tokens", tagAgents),
		func(ctx context.Context, _ *struct{}) (*body[[]AgentToken], error) {
			return out(s.AgentTokens(ctx))
		})
	huma.Register(a, api.Op("create-agent-token", http.MethodPost, "/api/v1/admin/agent-tokens", "Create an agent registration token", tagAgents),
		func(ctx context.Context, in *body[NewAgentToken]) (*body[*AgentToken], error) {
			return out(s.CreateAgentToken(ctx, in.Body))
		})
	huma.Register(a, api.Op("delete-agent-token", http.MethodDelete, "/api/v1/admin/agent-tokens/{id}", "Revoke an agent registration token",
		tagAgents),
		func(ctx context.Context, in *IDPath) (*struct{}, error) {
			return nil, s.DeleteAgentToken(ctx, in.ID)
		})
}

// VarScope selects the variables of the instance, a code project, a repository or an environment.
type VarScope struct {
	Scope       string `query:"scope" required:"true" enum:"instance,project,repository,environment"`
	Project     string `query:"project" maxLength:"64" doc:"Code project key (scopes project, repository and environment)"`
	Repository  string `query:"repository" maxLength:"100" doc:"Repository slug (scopes repository and environment)"`
	Environment string `query:"environment" maxLength:"63" doc:"Environment name (scope environment)"`
}

// scope resolves the scope and checks that the caller may manage its variables.
func (h *Handler) scope(ctx context.Context, in *VarScope) (*identity.Principal, int64, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, 0, err
	}
	id, err := h.S.scopeID(ctx, p, in.Scope, in.Project, in.Repository, in.Environment)
	return p, id, err
}
