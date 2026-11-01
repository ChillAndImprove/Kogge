// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// Pipeline triggers: tokens with which other systems — another build system, the release of a
// library, a scheduler — start the pipelines of one repository, without a personal access token. A trigger can do
// nothing else. It acts for the user who created it, and only while that user is active and may write to the
// repository; its runs are API runs, which accept the variables of the run form.

// PipelineTrigger is a trigger of a repository (its token is shown once, when it is created).
type PipelineTrigger struct {
	ID          int64             `json:"id"`
	Description string            `json:"description"`
	TokenHint   string            `json:"tokenHint"`
	CreatedBy   *identity.UserRef `json:"createdBy,omitempty"`
	Created     time.Time         `json:"created"`
	Expires     *time.Time        `json:"expires,omitempty"`
	LastUsed    *time.Time        `json:"lastUsed,omitempty"`
	Token       string            `json:"token,omitempty" doc:"Only in the answer to its creation"`
}

// NewPipelineTrigger creates a trigger.
type NewPipelineTrigger struct {
	Description   string `json:"description" minLength:"1" maxLength:"200" doc:"What the trigger is for, e.g. the system that uses it"`
	ExpiresInDays int    `json:"expiresInDays,omitempty" minimum:"0" maximum:"3650" doc:"0: does not expire"`
}

// TriggerRequest starts pipelines with a trigger.
type TriggerRequest struct {
	Ref       string            `json:"ref,omitempty" maxLength:"255" doc:"Branch, tag or commit; default: the default branch"`
	Pipeline  string            `json:"pipeline,omitempty" maxLength:"100" doc:"Only this pipeline; default: every pipeline that API runs start"`
	Variables map[string]string `json:"variables,omitempty" doc:"Values of the run form (on.manual.variables)"`
}

// TriggeredRun is a run a trigger started.
type TriggeredRun struct {
	Number   int    `json:"number"`
	Pipeline string `json:"pipeline"`
	URL      string `json:"url"`
}

// Triggers lists the triggers of a repository (its administrators).
func (s *Service) Triggers(ctx context.Context, p *identity.Principal, key, slug string) ([]PipelineTrigger, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, description, token_hint, created_by, created_at, expires_at, last_used_at
		FROM pipeline_triggers WHERE repository_id = $1 ORDER BY lower(description), id`, repo.ID)
	if err != nil {
		return nil, err
	}
	var creators []int64
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PipelineTrigger, error) {
		var t PipelineTrigger
		var by int64
		err := r.Scan(&t.ID, &t.Description, &t.TokenHint, &by, &t.Created, &t.Expires, &t.LastUsed)
		t.CreatedBy = &identity.UserRef{ID: by}
		creators = append(creators, by)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	users, err := identity.GetUserRefs(ctx, s.pool, creators)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if u, ok := users[list[i].CreatedBy.ID]; ok {
			list[i].CreatedBy = &u
		}
	}
	return nonNil(list), nil
}

// CreateTrigger creates a trigger of a repository (its administrators); the answer carries the token, once.
func (s *Service) CreateTrigger(ctx context.Context, p *identity.Principal, key, slug string, in NewPipelineTrigger) (*PipelineTrigger, error) {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return nil, err
	}
	in.Description = strings.TrimSpace(in.Description)
	if in.Description == "" {
		return nil, apperr.Invalid("trigger.description", "The trigger needs a description.")
	}
	token, hash := crypto.NewToken(crypto.PrefixTrigger)
	t := &PipelineTrigger{Description: in.Description, TokenHint: crypto.TokenHint(token), Token: token}
	var expires any
	if in.ExpiresInDays > 0 {
		e := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		t.Expires, expires = &e, e
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO pipeline_triggers (repository_id, description, token_hash, token_hint, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`, repo.ID, in.Description, hash, t.TokenHint, p.UserID(), expires).
			Scan(&t.ID, &t.Created); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "pipeline-trigger.create", TargetType: "repository",
			TargetID: itoa(repo.ID), Details: map[string]any{"trigger": t.ID, "description": in.Description}})
	})
	if err != nil {
		return nil, err
	}
	if u, err := identity.GetUserRefs(ctx, s.pool, []int64{p.UserID()}); err == nil {
		if ref, ok := u[p.UserID()]; ok {
			t.CreatedBy = &ref
		}
	}
	return t, nil
}

// DeleteTrigger revokes a trigger of a repository (its administrators).
func (s *Service) DeleteTrigger(ctx context.Context, p *identity.Principal, key, slug string, id int64) error {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var description string
		err := tx.QueryRow(ctx, "DELETE FROM pipeline_triggers WHERE id = $1 AND repository_id = $2 RETURNING description", id, repo.ID).
			Scan(&description)
		if db.IsNoRows(err) {
			return apperr.NotFound("trigger.not-found", "The trigger does not exist.")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "pipeline-trigger.delete", TargetType: "repository",
			TargetID: itoa(repo.ID), Details: map[string]any{"trigger": id, "description": description}})
	})
}

// RunTrigger starts pipelines with a trigger's token (Authorization: Bearer kgt_…).
func (s *Service) RunTrigger(ctx context.Context, authorization string, in TriggerRequest) ([]TriggeredRun, error) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || !strings.HasPrefix(token, crypto.PrefixTrigger+"_") {
		return nil, apperr.Unauthorized("trigger.token", "A pipeline trigger's token is needed (Authorization: Bearer kgt_…).")
	}
	var id, repoID, creator int64
	var description string
	var expires *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, repository_id, created_by, description, expires_at FROM pipeline_triggers WHERE token_hash = $1`,
		crypto.HashToken(token)).Scan(&id, &repoID, &creator, &description, &expires)
	if db.IsNoRows(err) || err == nil && expires != nil && expires.Before(time.Now()) {
		return nil, apperr.Unauthorized("trigger.token", "The trigger's token is invalid or expired.")
	}
	if err != nil {
		return nil, err
	}
	// The trigger acts for its creator, as long as the creator may run the repository's pipelines.
	p, err := s.identity.PrincipalOf(ctx, creator, identity.MethodInternal)
	if err != nil {
		return nil, apperr.Forbidden("trigger.creator", "The user who created the trigger is no longer active.")
	}
	repo, err := s.repos.Repo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	if l, err := s.repos.Level(ctx, p, repoID); err != nil {
		return nil, err
	} else if l < LevelWrite {
		return nil, apperr.Forbidden("trigger.creator", "The user who created the trigger may no longer run the repository's pipelines.")
	}
	for name, v := range in.Variables {
		if !varNamePattern.MatchString(name) || strings.HasPrefix(name, "KOGGE_") || len(v) > 65536 {
			return nil, apperr.Invalidf("run.variable", "Invalid variable %s.", name)
		}
	}
	ref := in.Ref
	if ref == "" {
		ref = repo.DefaultBranch
	}
	ev := spec.Event{Kind: spec.EventAPI, Pipeline: in.Pipeline}
	commit, err := s.resolveRunRef(ctx, repo.ID, ref, &ev)
	if err != nil {
		return nil, apperr.Invalidf("run.ref", "The ref %s does not exist.", ref)
	}
	ids, err := s.Trigger(ctx, TriggerInput{RepoID: repo.ID, Event: ev, Commit: commit, ActorID: creator, Variables: in.Variables,
		TriggerName: description})
	if err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, "UPDATE pipeline_triggers SET last_used_at = now() WHERE id = $1", id)
	out := []TriggeredRun{}
	base := strings.TrimRight(s.opt.PublicURL.String(), "/")
	for _, runID := range ids {
		var t TriggeredRun
		if err := s.pool.QueryRow(ctx, "SELECT number, pipeline FROM runs WHERE id = $1", runID).Scan(&t.Number, &t.Pipeline); err != nil {
			return nil, err
		}
		t.URL = base + RunPath(repo.ProjectKey, repo.Slug, t.Number)
		out = append(out, t)
	}
	return out, nil
}
