// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// Agents are managed by users with the agent.manage permission.

// AgentInfo describes a registered agent.
type AgentInfo struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Project  string   `json:"project,omitempty" doc:"Key of the code project the agent serves; empty: all"`
	Labels   []string `json:"labels"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Version  string   `json:"version"`
	Enabled  bool     `json:"enabled"`
	Approved bool     `json:"approved" doc:"An administrator let the agent take jobs (or its registration token did)"`
	// Protected agents take only jobs of refs that a branch permission of the code project protects.
	Protected bool `json:"protected" doc:"The agent takes only jobs of refs that a branch permission of the code project protects"`
	// AnnouncedLabels are labels the agent announces that differ from its labels; they apply once accepted.
	AnnouncedLabels []string `json:"announcedLabels,omitempty" doc:"Labels the agent announces that differ from its labels; they apply once an administrator accepts them"`
	Online          bool     `json:"online" doc:"Seen in the last minute"`
	// The most the agent allows one container of a job, as it announced (its configuration); 0: no limit.
	MaxMilliCPUs int64      `json:"maxMilliCpus,omitempty" doc:"The most thousandths of a CPU one container may have; jobs asking for more go to other agents"`
	MaxMemory    int64      `json:"maxMemory,omitempty" doc:"The most memory (bytes) one container may have"`
	Busy         int        `json:"busy" doc:"Jobs it runs now"`
	LastSeen     *time.Time `json:"lastSeen,omitempty"`
	LastIP       string     `json:"lastIp,omitempty"`
	Created      time.Time  `json:"created"`
}

// AgentToken is a registration token (shown once, when created).
type AgentToken struct {
	ID          int64      `json:"id"`
	Hint        string     `json:"hint"`
	Project     string     `json:"project,omitempty"`
	Description string     `json:"description"`
	Uses        int        `json:"uses"`
	MaxUses     int        `json:"maxUses" doc:"Agents it may register; 0: any number"`
	AutoApprove bool       `json:"autoApprove" doc:"Agents it registers take jobs without an administrator's approval"`
	Expires     *time.Time `json:"expires,omitempty"`
	Created     time.Time  `json:"created"`
	Token       string     `json:"token,omitempty" doc:"Only in the answer to the creation"`
}

func (s *Service) requireAgents(ctx context.Context) (*identity.Principal, error) {
	return s.authz.RequireGlobal(ctx, authz.AgentManage)
}

// Agents lists the agents.
func (s *Service) Agents(ctx context.Context) ([]AgentInfo, error) {
	if _, err := s.requireAgents(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.name, coalesce(p.key, ''), a.labels, a.os, a.arch, a.version, a.enabled,
		a.approved_at IS NOT NULL, a.protected, coalesce(a.announced_labels, '{}'),
		a.last_seen_at > now() - interval '1 minute', (SELECT count(*) FROM run_jobs j WHERE j.agent_id = a.id AND j.state = 'running'),
		a.last_seen_at, a.last_ip, a.created_at, a.max_millicpus, a.max_memory FROM agents a LEFT JOIN code_projects p ON p.id = a.project_id
		ORDER BY lower(a.name), a.id`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AgentInfo, error) {
		var a AgentInfo
		var online *bool
		err := r.Scan(&a.ID, &a.Name, &a.Project, &a.Labels, &a.OS, &a.Arch, &a.Version, &a.Enabled, &a.Approved, &a.Protected, &a.AnnouncedLabels,
			&online, &a.Busy, &a.LastSeen, &a.LastIP, &a.Created, &a.MaxMilliCPUs, &a.MaxMemory)
		a.Online = online != nil && *online
		a.Labels = nonNil(a.Labels)
		if len(a.AnnouncedLabels) == 0 {
			a.AnnouncedLabels = nil
		}
		return a, err
	})
	return nonNil(list), err
}

// changeAgent changes an agent and records the change in the same transaction: a change without its record does
// not happen.
func (s *Service) changeAgent(ctx context.Context, id int64, e audit.Entry, sql string, args ...any) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("agent.not-found", "The agent does not exist.")
		}
		return audit.Record(ctx, tx, e)
	})
}

// SetAgentEnabled enables or disables an agent; disabled agents get no work.
func (s *Service) SetAgentEnabled(ctx context.Context, id int64, enabled bool) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	return s.changeAgent(ctx, id, audit.Entry{ActorID: p.UserID(), Action: "agent.enable", TargetType: "agent", TargetID: itoa(id),
		Details: map[string]any{"enabled": enabled}}, "UPDATE agents SET enabled = $2 WHERE id = $1", id, enabled)
}

// SetAgentProtected limits an agent to jobs of refs that a branch permission of the code project protects, or
// lifts the limit.
func (s *Service) SetAgentProtected(ctx context.Context, id int64, protected bool) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	return s.changeAgent(ctx, id, audit.Entry{ActorID: p.UserID(), Action: "agent.protect", TargetType: "agent", TargetID: itoa(id),
		Details: map[string]any{"protected": protected}}, "UPDATE agents SET protected = $2 WHERE id = $1", id, protected)
}

// ApproveAgent lets a new agent take jobs.
func (s *Service) ApproveAgent(ctx context.Context, id int64) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	return s.changeAgent(ctx, id, audit.Entry{ActorID: p.UserID(), Action: "agent.approve", TargetType: "agent", TargetID: itoa(id)},
		"UPDATE agents SET approved_at = coalesce(approved_at, now()), approved_by = coalesce(approved_by, $2) WHERE id = $1", id, p.UserID())
}

// SetAgentLabels sets the labels of an agent, which decide which jobs it gets (for example the labels it announces).
func (s *Service) SetAgentLabels(ctx context.Context, id int64, labels []string) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	labels = normalizeLabels(labels)
	return s.changeAgent(ctx, id, audit.Entry{ActorID: p.UserID(), Action: "agent.labels", TargetType: "agent", TargetID: itoa(id),
		Details: map[string]any{"labels": labels}}, `UPDATE agents SET labels = $2,
		announced_labels = CASE WHEN announced_labels @> $2 AND announced_labels <@ $2 THEN NULL ELSE announced_labels END WHERE id = $1`,
		id, labels)
}

// DeleteAgent removes an agent; it cannot reconnect with its secret. Like the revocation of a credential, it takes
// effect whether or not its record can be written (audit.Report).
func (s *Service) DeleteAgent(ctx context.Context, id int64) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM agents WHERE id = $1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.NotFound("agent.not-found", "The agent does not exist.")
	}
	if err != nil {
		return err
	}
	audit.Report(ctx, s.pool, s.log, audit.Entry{ActorID: p.UserID(), Action: "agent.delete", TargetType: "agent", TargetID: itoa(id)})
	return nil
}

// AgentTokens lists the registration tokens.
func (s *Service) AgentTokens(ctx context.Context) ([]AgentToken, error) {
	if _, err := s.requireAgents(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT t.id, t.hint, coalesce(p.key, ''), t.description, t.uses, t.max_uses, t.auto_approve, t.expires_at,
		t.created_at FROM agent_tokens t LEFT JOIN code_projects p ON p.id = t.project_id ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AgentToken, error) {
		var t AgentToken
		err := r.Scan(&t.ID, &t.Hint, &t.Project, &t.Description, &t.Uses, &t.MaxUses, &t.AutoApprove, &t.Expires, &t.Created)
		return t, err
	})
	return nonNil(list), err
}

// CreateAgentToken creates a registration token for all repositories or those of one code project.
func (s *Service) CreateAgentToken(ctx context.Context, in NewAgentToken) (*AgentToken, error) {
	// Unless said otherwise, a token registers one agent within a week.
	description, project, expiresInDays, maxUses := in.Description, in.Project, 7, 1
	if in.ExpiresInDays != nil {
		expiresInDays = *in.ExpiresInDays
	}
	if in.MaxUses != nil {
		maxUses = *in.MaxUses
	}
	p, err := s.requireAgents(ctx)
	if err != nil {
		return nil, err
	}
	var projectID int64
	if project != "" {
		if projectID, err = s.repos.ProjectID(ctx, project); err != nil {
			return nil, err
		}
	}
	token, hash := crypto.NewToken(crypto.PrefixAgentReg)
	var expires *time.Time
	if expiresInDays > 0 {
		e := now().Add(time.Duration(expiresInDays) * 24 * time.Hour)
		expires = &e
	}
	t := &AgentToken{Hint: crypto.TokenHint(token), Project: project, Description: description, MaxUses: maxUses,
		AutoApprove: in.AutoApprove, Expires: expires, Created: now(), Token: token}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO agent_tokens (token_hash, hint, project_id, description, created_by, expires_at, max_uses, auto_approve)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, hash, t.Hint, nullID(projectID), description, p.UserID(), expires, maxUses,
			in.AutoApprove).Scan(&t.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "agent-token.create", TargetType: "agent-token", TargetID: itoa(t.ID),
			Details: map[string]any{"project": project, "expiresInDays": expiresInDays, "maxUses": maxUses, "autoApprove": in.AutoApprove}})
	})
	return t, err
}

// DeleteAgentToken revokes a registration token (registered agents keep working), whether or not its record can be
// written (audit.Report).
func (s *Service) DeleteAgentToken(ctx context.Context, id int64) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM agent_tokens WHERE id = $1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.NotFound("agent-token.not-found", "The token does not exist.")
	}
	if err != nil {
		return err
	}
	audit.Report(ctx, s.pool, s.log, audit.Entry{ActorID: p.UserID(), Action: "agent-token.delete", TargetType: "agent-token", TargetID: itoa(id)})
	return nil
}
