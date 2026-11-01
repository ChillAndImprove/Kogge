// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package audit writes the append-only audit log and its daily digest chain.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
)

// Entry is one audit event.
type Entry struct {
	ActorID    int64
	Action     string // dotted, e.g. "auth.login", "group.member.add"
	TargetType string
	TargetID   string
	Details    map[string]any
}

// Record writes an entry; the client address comes from the request context.
func Record(ctx context.Context, q db.Querier, e Entry) error {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	var ip any
	if a := httpx.ClientIP(ctx); a.IsValid() {
		ip = a.String()
	}
	var actor any
	if e.ActorID != 0 {
		actor = e.ActorID
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_ip, action, target_type, target_id, details)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6)`, actor, ip, e.Action, e.TargetType, e.TargetID, details)
	if err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	return nil
}

// Report writes an event that must not undo or stop what it reports: a refused credential or a
// lockout, a revoked credential, the outcome of a backup. Changes of data record their events with Record in their
// own transaction instead, so that a change without its record does not happen. Report writes the event on its
// own, also when the request's client has gone away, and logs an event it cannot write at error level, with its
// contents, rather than losing it silently.
func Report(ctx context.Context, q db.Querier, logger *slog.Logger, e Entry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := Record(ctx, q, e); err != nil {
		logger.Error("an audit event could not be written", "action", e.Action, "actor", e.ActorID, "target_type", e.TargetType,
			"target", e.TargetID, "details", e.Details, "client", httpx.ClientIP(ctx).String(), "error", err)
	}
}

// ID formats an id for TargetID.
func ID(id int64) string { return strconv.FormatInt(id, 10) }

// Event is an audit event as listed in the administration.
type Event struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	ActorID    *int64         `json:"actorId,omitempty"`
	ActorName  *string        `json:"actorName,omitempty"`
	ActorIP    *string        `json:"actorIp,omitempty"`
	Action     string         `json:"action"`
	TargetType *string        `json:"targetType,omitempty"`
	TargetID   *string        `json:"targetId,omitempty"`
	Details    map[string]any `json:"details"`
}

// Filter selects audit events.
type Filter struct {
	Action  string
	ActorID int64
	Target  string
	From    time.Time
	To      time.Time
	Limit   int
	Before  int64 // keyset: events with a smaller id
}

// List returns events, newest first.
func List(ctx context.Context, q db.Querier, f Filter) ([]Event, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `
		SELECT e.id, e.at, e.actor_id, u.display_name, host(e.actor_ip), e.action, e.target_type, e.target_id, e.details
		FROM audit_events e LEFT JOIN users u ON u.id = e.actor_id
		WHERE ($1 = '' OR e.action LIKE $1 || '%')
		  AND ($2 = 0 OR e.actor_id = $2)
		  AND ($3 = '' OR e.target_type || ':' || e.target_id = $3)
		  AND ($4::timestamptz IS NULL OR e.at >= $4)
		  AND ($5::timestamptz IS NULL OR e.at < $5)
		  AND ($6 = 0 OR e.id < $6)
		ORDER BY e.id DESC LIMIT $7`,
		f.Action, f.ActorID, f.Target, nullTime(f.From), nullTime(f.To), f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var ev Event
		err := row.Scan(&ev.ID, &ev.At, &ev.ActorID, &ev.ActorName, &ev.ActorIP, &ev.Action, &ev.TargetType,
			&ev.TargetID, &ev.Details)
		return ev, err
	})
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// Digest computes the SHA-256 chain link for one day: previous digest followed by the canonical JSON of every
// event of the day in id order. It is idempotent and safe to run repeatedly.
func Digest(ctx context.Context, tx pgx.Tx, day time.Time) error {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM audit_digests WHERE day = $1)", day).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var previous []byte
	err := tx.QueryRow(ctx, "SELECT digest FROM audit_digests WHERE day < $1 ORDER BY day DESC LIMIT 1", day).Scan(&previous)
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	digest, count, err := dayDigest(ctx, tx, day, previous)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_digests (day, events, digest, previous) VALUES ($1, $2, $3, $4)",
		day, count, digest, previous)
	return err
}

// dayDigest hashes the previous digest and the events of a day.
func dayDigest(ctx context.Context, q db.Querier, day time.Time, previous []byte) ([]byte, int, error) {
	h := sha256.New()
	h.Write(previous)
	rows, err := q.Query(ctx, `SELECT id, at, actor_id, host(actor_ip), action, target_type, target_id, details
		FROM audit_events WHERE at >= $1 AND at < $1 + interval '1 day' ORDER BY id`, day)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			id             int64
			at             time.Time
			actor          *int64
			ip, tType, tID *string
			action         string
			details        map[string]any
		)
		if err := rows.Scan(&id, &at, &actor, &ip, &action, &tType, &tID, &details); err != nil {
			return nil, 0, err
		}
		line, _ := json.Marshal([]any{id, at.UTC().Format(time.RFC3339Nano), actor, ip, action, tType, tID, details})
		h.Write(line)
		h.Write([]byte{'\n'})
		count++
	}
	return h.Sum(nil), count, rows.Err()
}

// Finding is a day of the audit log that does not match its digest.
type Finding struct {
	Day     time.Time `json:"day"`
	Problem string    `json:"problem"`
}

// Verify recomputes the digests of the days and checks their chain. Days older than keepDays may have lost their
// events to the retention; for them only the chain is checked. It returns the number of days checked.
func Verify(ctx context.Context, q db.Querier, keepDays int) (int, []Finding, error) {
	rows, err := q.Query(ctx, "SELECT day, events, digest, previous FROM audit_digests ORDER BY day")
	if err != nil {
		return 0, nil, err
	}
	type link struct {
		day              time.Time
		events           int
		digest, previous []byte
	}
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.day, &l.events, &l.digest, &l.previous); err != nil {
			rows.Close()
			return 0, nil, err
		}
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	kept := time.Now().UTC().AddDate(0, 0, -keepDays).Truncate(24 * time.Hour)
	var findings []Finding
	var previous []byte
	for i, l := range links {
		if i > 0 && !bytes.Equal(l.previous, previous) {
			findings = append(findings, Finding{Day: l.day, Problem: "the chain is broken: the digest of the day before was changed"})
		}
		previous = l.digest
		digest, count, err := dayDigest(ctx, q, l.day, l.previous)
		if err != nil {
			return 0, nil, err
		}
		switch {
		case bytes.Equal(digest, l.digest):
		case count == 0 && l.day.Before(kept):
			// Purged by the retention.
		case count != l.events:
			findings = append(findings, Finding{Day: l.day, Problem: fmt.Sprintf("%d events were recorded, %d are there", l.events, count)})
		default:
			findings = append(findings, Finding{Day: l.day, Problem: "events of the day were changed"})
		}
	}
	return len(links), findings, nil
}

// ErrAppendOnly reports that the database role may not delete audit events: the log is append-only for it.
var ErrAppendOnly = errors.New("the audit log is append-only for this database role")

// Purge deletes the audit events of the days older than keepDays (whole days, so that their digests stay
// verifiable as purged).
func Purge(ctx context.Context, q db.Querier, keepDays int) (int64, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	tag, err := q.Exec(ctx, "DELETE FROM audit_events WHERE at < date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - make_interval(days => $1)", keepDays)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return 0, ErrAppendOnly
	}
	return tag.RowsAffected(), err
}
