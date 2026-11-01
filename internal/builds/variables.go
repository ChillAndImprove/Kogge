// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Variables and secrets: instance, code project, repository and environment scopes; the narrower
// scope wins. Secret values are sealed with the master key, never returned, and masked in logs. Protected ones
// reach only runs of protected refs and deployments.

// Scopes of variables.
const (
	ScopeInstance    = "instance"
	ScopeProject     = "project"
	ScopeRepository  = "repository"
	ScopeEnvironment = "environment"
)

// Variable is a variable or secret (whose value is not shown).
type Variable struct {
	Name      string    `json:"name"`
	Value     string    `json:"value,omitempty" doc:"Empty for secrets"`
	Secret    bool      `json:"secret"`
	Protected bool      `json:"protected" doc:"Only for jobs of protected branches and tags and for deployments to environments with deployers or approvals"`
	Updated   time.Time `json:"updated"`
}

// VariableInput sets a variable; for secrets, an empty value keeps the stored one.
type VariableInput struct {
	Name      string `json:"name,omitempty" maxLength:"128" doc:"Taken from the path"`
	Value     string `json:"value" maxLength:"65536"`
	Secret    bool   `json:"secret,omitempty"`
	Protected bool   `json:"protected,omitempty"`
}

var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// scopeID resolves a scope reference and checks the right to manage it.
func (s *Service) scopeID(ctx context.Context, p *identity.Principal, scope, key, slug, env string) (int64, error) {
	switch scope {
	case ScopeInstance:
		if _, err := s.authz.RequireGlobal(ctx, authz.AgentManage); err != nil {
			return 0, err
		}
		return 0, nil
	case ScopeProject:
		return s.repos.ProjectForVariables(ctx, p, key)
	case ScopeRepository, ScopeEnvironment:
		repo, _, err := s.repoFor(ctx, p, key, slug, LevelAdmin)
		if err != nil {
			return 0, err
		}
		if scope == ScopeRepository {
			return repo.ID, nil
		}
		var id int64
		err = s.pool.QueryRow(ctx, "SELECT id FROM environments WHERE repository_id = $1 AND name = $2", repo.ID, env).Scan(&id)
		if db.IsNoRows(err) {
			return 0, apperr.NotFound("environment.not-found", "The environment %s does not exist.", env)
		}
		return id, err
	}
	return 0, apperr.Invalidf("variables.scope", "Unknown scope %s.", scope)
}

// Variables lists the variables of a scope.
func (s *Service) Variables(ctx context.Context, scope string, scopeID int64) ([]Variable, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, CASE WHEN secret THEN '' ELSE value END, secret, protected, updated_at FROM build_variables
		WHERE scope = $1 AND scope_id = $2 ORDER BY name`, scope, scopeID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Variable, error) {
		var v Variable
		err := r.Scan(&v.Name, &v.Value, &v.Secret, &v.Protected, &v.Updated)
		return v, err
	})
	return nonNil(list), err
}

func sealAAD(scope string, scopeID int64, name string) string {
	return "build-secret:" + scope + ":" + itoa(scopeID) + ":" + name
}

// SetVariable creates or changes a variable of a scope.
func (s *Service) SetVariable(ctx context.Context, p *identity.Principal, scope string, scopeID int64, in VariableInput) error {
	if !varNamePattern.MatchString(in.Name) || strings.HasPrefix(in.Name, "KOGGE_") || in.Name == "CI" {
		return apperr.Invalid("variable.name", "The name may have letters, digits and _, and must not start with KOGGE_.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.Secret {
			if in.Value == "" {
				tag, err := tx.Exec(ctx, `UPDATE build_variables SET protected = $4, updated_by = $5, updated_at = now()
					WHERE scope = $1 AND scope_id = $2 AND name = $3 AND secret`, scope, scopeID, in.Name, in.Protected, nullID(p.UserID()))
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return apperr.Invalid("variable.value", "A new secret needs a value.")
				}
			} else {
				sealed, err := s.opt.Keyring.Seal([]byte(in.Value), sealAAD(scope, scopeID, in.Name))
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO build_variables (scope, scope_id, name, value, sealed, secret, protected, updated_by)
					VALUES ($1, $2, $3, '', $4, true, $5, $6) ON CONFLICT (scope, scope_id, name) DO UPDATE SET value = '', sealed = EXCLUDED.sealed,
					secret = true, protected = EXCLUDED.protected, updated_by = EXCLUDED.updated_by, updated_at = now()`, scope, scopeID, in.Name,
					sealed, in.Protected, nullID(p.UserID())); err != nil {
					return err
				}
			}
		} else if _, err := tx.Exec(ctx, `INSERT INTO build_variables (scope, scope_id, name, value, sealed, secret, protected, updated_by)
			VALUES ($1, $2, $3, $4, NULL, false, $5, $6) ON CONFLICT (scope, scope_id, name) DO UPDATE SET value = EXCLUDED.value, sealed = NULL,
			secret = false, protected = EXCLUDED.protected, updated_by = EXCLUDED.updated_by, updated_at = now()`, scope, scopeID, in.Name, in.Value,
			in.Protected, nullID(p.UserID())); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "build.variable.set", TargetType: scope, TargetID: itoa(scopeID),
			Details: map[string]any{"name": in.Name, "secret": in.Secret}})
	})
}

// DeleteVariable removes a variable of a scope.
func (s *Service) DeleteVariable(ctx context.Context, p *identity.Principal, scope string, scopeID int64, name string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM build_variables WHERE scope = $1 AND scope_id = $2 AND name = $3", scope, scopeID, name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("variable.not-found", "The variable %s does not exist.", name)
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "build.variable.delete", TargetType: scope,
			TargetID: itoa(scopeID), Details: map[string]any{"name": name}})
	})
}

// Protection is how a job's ref and environment are protected, for protected values.
type Protection int

const (
	// ProtectionNone: everyone who can write may run code with the job.
	ProtectionNone Protection = iota
	// ProtectionRepository: a branch permission of the repository protects the ref, or people let the deployment
	// through (deployers or approvals of the repository's environment); the repository's administrators control both.
	ProtectionRepository
	// ProtectionProject: a branch permission of the code project protects the ref.
	ProtectionProject
)

// requiredProtection is what a protected value of a scope needs: those of the code project and the installation
// are not given to what the repository's administrators alone control.
func requiredProtection(scope string) Protection {
	if scope == ScopeInstance || scope == ScopeProject {
		return ProtectionProject
	}
	return ProtectionRepository
}

// resolveVariables returns the variables of the instance, the code project and the repository (narrower scopes
// win), those of the environment separately (they override the pipeline file), and the secret values for masking.
func (s *Service) resolveVariables(ctx context.Context, q db.Querier, repo *Repo, envID int64, protection Protection) (vars,
	envVars map[string]string, secrets []string, err error) {
	rows, err := q.Query(ctx, `SELECT scope, name, value, coalesce(sealed, ''), secret, protected, scope_id FROM build_variables
		WHERE (scope = 'instance') OR (scope = 'project' AND scope_id = $1) OR (scope = 'repository' AND scope_id = $2)
		   OR (scope = 'environment' AND scope_id = $3)
		ORDER BY CASE scope WHEN 'instance' THEN 0 WHEN 'project' THEN 1 WHEN 'repository' THEN 2 ELSE 3 END`, repo.ProjectID, repo.ID, envID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	vars, envVars = map[string]string{}, map[string]string{}
	for rows.Next() {
		var scope, name, value, sealed string
		var secret, prot bool
		var scopeID int64
		if err := rows.Scan(&scope, &name, &value, &sealed, &secret, &prot, &scopeID); err != nil {
			return nil, nil, nil, err
		}
		// Protected values reach protected refs and gated deployments only — those of the code project and the
		// installation only refs the code project protects; a broader value of the same name stays.
		if prot && protection < requiredProtection(scope) {
			continue
		}
		if secret {
			plain, err := s.opt.Keyring.Open(sealed, sealAAD(scope, scopeID, name))
			if err != nil {
				return nil, nil, nil, err
			}
			value = string(plain)
			if value != "" {
				secrets = append(secrets, value)
			}
		}
		if scope == ScopeEnvironment {
			envVars[name] = value
		} else {
			vars[name] = value
		}
	}
	return vars, envVars, secrets, rows.Err()
}
