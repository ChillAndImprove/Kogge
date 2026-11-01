// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The database roles of an installation: an owner role owns the schema and runs the
// migrations; Kogge runs as AppRole, which may read and write data but neither change nor delete audit events, so
// that the digest chain of the audit log shows tampering even by Kogge itself; MaintRole may delete audit events
// (retention). The Compose stack creates them when its database starts (deploy/db/roles.sh) and the migrations
// grant what the roles need; SetUpRoles does the same for an existing PostgreSQL (kogge admin init-database), also
// after the migrations ran without the roles. The names are fixed: the migrations refer to them.
const (
	AppRole   = "kogge_app"
	MaintRole = "kogge_maint"
)

// RoleSetup says how to set the roles up.
type RoleSetup struct {
	// Owner is the role that owns the schema and runs the migrations; empty: the owner of the database.
	Owner string
	// AppPassword is the password of AppRole; it is needed when the role is created and changed when given.
	AppPassword string
}

// SetUpRoles creates the roles (if missing) and grants them what the migrations grant, on the database of conn,
// which must be a superuser's connection or the owner's with the CREATEROLE attribute. It can run again at any
// time: before the migrations (they then grant the rest themselves) or after them. It returns what it did.
func SetUpRoles(ctx context.Context, conn *pgx.Conn, s RoleSetup) ([]string, error) {
	var done []string
	note := func(format string, args ...any) { done = append(done, fmt.Sprintf(format, args...)) }
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var database, current string
		var super, createRole bool
		if err := tx.QueryRow(ctx, `SELECT current_database(), current_user, rolsuper, rolcreaterole FROM pg_roles
			WHERE rolname = current_user`).Scan(&database, &current, &super, &createRole); err != nil {
			return err
		}
		owner := s.Owner
		if owner == "" {
			if err := tx.QueryRow(ctx, "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = current_database()").
				Scan(&owner); err != nil {
				return err
			}
		}
		var ownerExists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", owner).Scan(&ownerExists); err != nil {
			return err
		}
		switch {
		case !ownerExists:
			return fmt.Errorf("the owner role %s does not exist", owner)
		case owner == AppRole || owner == MaintRole:
			return fmt.Errorf("the owner of the schema must be another role than %s", owner)
		case !super && !(createRole && current == owner):
			return fmt.Errorf("connected as %s: connect as a superuser (such as postgres), or as the owner %s with the CREATEROLE attribute",
				current, owner)
		}

		// The application role, as deploy/db/roles.sh creates it.
		var exists, appSuper, bypass bool
		err := tx.QueryRow(ctx, "SELECT true, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1", AppRole).Scan(&exists, &appSuper, &bypass)
		if err != nil && !IsNoRows(err) {
			return err
		}
		var verifier string
		if s.AppPassword != "" {
			if verifier, err = scramVerifier(s.AppPassword); err != nil {
				return err
			}
		}
		if !exists && verifier == "" {
			return fmt.Errorf("the role %s does not exist yet: give its password", AppRole)
		}
		if !exists {
			// Roles belong to the whole server: someone may create it at the same moment.
			if exists, err = createRoleOnce(ctx, tx, "CREATE ROLE %I LOGIN PASSWORD %L", AppRole, verifier); err != nil {
				return err
			}
			if !exists {
				note("created the role %s", AppRole)
			} else if err := tx.QueryRow(ctx, "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1", AppRole).
				Scan(&appSuper, &bypass); err != nil {
				return err
			}
		}
		if exists {
			if appSuper || bypass {
				return fmt.Errorf("the role %s is a superuser or bypasses row security: it could change the audit log", AppRole)
			}
			if verifier != "" {
				if err := execFormat(ctx, tx, "ALTER ROLE %I LOGIN PASSWORD %L", AppRole, verifier); err != nil {
					return err
				}
				note("set the password of %s", AppRole)
			} else if err := execFormat(ctx, tx, "ALTER ROLE %I LOGIN", AppRole); err != nil {
				return err
			}
		}
		var member bool
		if err := tx.QueryRow(ctx, "SELECT pg_has_role($1, $2, 'MEMBER')", AppRole, owner).Scan(&member); err != nil {
			return err
		}
		if member {
			return fmt.Errorf("the role %s is a member of the owner %s, so it has the owner's rights: revoke the membership", AppRole, owner)
		}
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", MaintRole).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if exists, err = createRoleOnce(ctx, tx, "CREATE ROLE %I NOLOGIN", MaintRole); err != nil {
				return err
			}
			if !exists {
				note("created the role %s", MaintRole)
			}
		}

		// The grants of the migrations (00001, 00028), for the objects there are and those the owner creates.
		var auditEvents, auditDigests bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('public.audit_events') IS NOT NULL, to_regclass('public.audit_digests') IS NOT NULL`).
			Scan(&auditEvents, &auditDigests); err != nil {
			return err
		}
		steps := [][]any{
			{"GRANT CONNECT ON DATABASE %I TO %I", database, AppRole},
			// Nobody but the owner creates objects in the schema (the default since PostgreSQL 15).
			{"REVOKE CREATE ON SCHEMA public FROM PUBLIC"},
			{"GRANT USAGE ON SCHEMA public TO %I", AppRole},
			{"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I", AppRole},
			{"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I", AppRole},
			{"ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I", owner, AppRole},
			{"ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %I", owner, AppRole},
			{"GRANT USAGE ON SCHEMA public TO %I", MaintRole},
		}
		if auditEvents {
			steps = append(steps, []any{"REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM %I", AppRole},
				[]any{"GRANT SELECT, DELETE ON audit_events TO %I", MaintRole})
		}
		if auditDigests {
			steps = append(steps, []any{"REVOKE UPDATE, DELETE, TRUNCATE ON audit_digests FROM %I", AppRole})
		}
		for _, step := range steps {
			if err := execFormat(ctx, tx, step[0].(string), step[1:]...); err != nil {
				return err
			}
		}
		note("granted %s what it needs on the database %s and kept it from changing the audit log; the owner is %s", AppRole,
			database, owner)
		if !auditEvents {
			note("the database has no schema yet: run `kogge migrate` with the owner's database URL")
		}
		return nil
	})
	return done, err
}

// createRoleOnce creates a role in a savepoint and reports whether it existed already (created at the same moment by
// someone else).
func createRoleOnce(ctx context.Context, tx pgx.Tx, format string, args ...any) (bool, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	err = execFormat(ctx, sp, format, args...)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42710" { // duplicate_object
		return true, sp.Rollback(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return false, err
	}
	return false, sp.Commit(ctx)
}

// execFormat runs a statement that PostgreSQL's format() builds from the arguments (%I names, %L values): DDL
// takes no bound parameters, and so the values are quoted by the server.
func execFormat(ctx context.Context, tx pgx.Tx, format string, args ...any) error {
	var sql string
	params := make([]string, len(args))
	values := make([]any, len(args)+1)
	values[0] = format
	for i, a := range args {
		params[i] = fmt.Sprintf("$%d::text", i+2)
		values[i+1] = a
	}
	query := "SELECT format($1::text"
	if len(params) > 0 {
		query += ", " + strings.Join(params, ", ")
	}
	if err := tx.QueryRow(ctx, query+")", values...).Scan(&sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("%s: %w", strings.SplitN(sql, " PASSWORD ", 2)[0], err)
	}
	return nil
}

// scramVerifier returns PostgreSQL's SCRAM-SHA-256 verifier of a password (RFC 5802, 7677), computed here as psql's
// \password does, so that the password itself reaches neither the server nor its statement log.
func scramVerifier(password string) (string, error) {
	for _, r := range password {
		// Without the SASLprep normalisation of other characters, clients would compute other keys.
		if r < 0x21 || r > 0x7e {
			return "", errors.New("the password must consist of printable ASCII characters without spaces")
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(stored[:]), b64(mac(salted, "Server Key"))), nil
}

// AuditWritable reports whether the database role of q can change or delete audit events — it owns the table, is
// a superuser or was granted it — and names the role: the audit log is then not protected against Kogge itself.
func AuditWritable(ctx context.Context, q Querier) (bool, string, error) {
	var writable bool
	var role string
	err := q.QueryRow(ctx, `SELECT current_user, has_table_privilege(current_user, 'audit_events', 'UPDATE')
		OR has_table_privilege(current_user, 'audit_events', 'DELETE') OR has_table_privilege(current_user, 'audit_events', 'TRUNCATE')`).
		Scan(&role, &writable)
	return writable, role, err
}
