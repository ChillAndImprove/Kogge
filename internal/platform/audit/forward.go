// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/db"
)

// Forwarding to a SIEM. Events go out in the order of the transactions that wrote them (the column
// xact), and only once no transaction is running that might still add an earlier one — ordered by id, the event of
// a transaction that commits after a later one would be skipped. Only transactions in Kogge's database count, so
// that long transactions of other databases in the same cluster do not hold forwarding back. Every event is
// delivered at least once: after a failure the same batch is sent again, and receivers recognise repeated events
// by their id.

const (
	forwardBatch = 500
	forwardBytes = 1 << 20
	// maxLine is the longest line: an event whose details would make it longer loses them (it still fits a UDP
	// datagram).
	maxLine = 60 << 10
)

// ForwardOptions configure a Forwarder.
type ForwardOptions struct {
	Sink Sink
	// Target names the sink for administrators and logs, without credentials.
	Target string
	// Instance is the public URL of the installation; every event names it, so that a SIEM tells installations
	// apart.
	Instance string
	Logger   *slog.Logger
	// Poll is how often a forwarder that caught up looks for new events (2 seconds). Standby is how often a node
	// that does not forward tries to take over (30 seconds).
	Poll, Standby time.Duration
}

// Forwarder forwards the audit log. One node forwards at a time: the one that holds db.LockAuditForward.
type Forwarder struct {
	pool    *pgxpool.Pool
	o       ForwardOptions
	log     *slog.Logger
	failing time.Time // since when deliveries fail
	logged  time.Time // when that was logged last
}

// NewForwarder prepares forwarding to o.Sink.
func NewForwarder(pool *pgxpool.Pool, o ForwardOptions) *Forwarder {
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	if o.Standby <= 0 {
		o.Standby = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Forwarder{pool: pool, o: o, log: o.Logger.With("target", o.Target)}
}

// forwarded is the JSON object of a forwarded event.
// Fields may be added, never changed.
type forwarded struct {
	ID            int64          `json:"id"`
	At            time.Time      `json:"at"`
	Action        string         `json:"action"`
	ActorID       *int64         `json:"actorId,omitempty"`
	ActorUsername *string        `json:"actorUsername,omitempty"`
	ActorName     *string        `json:"actorName,omitempty"`
	ActorIP       *string        `json:"actorIp,omitempty"`
	TargetType    *string        `json:"targetType,omitempty"`
	TargetID      *string        `json:"targetId,omitempty"`
	Details       map[string]any `json:"details"`
	Instance      string         `json:"instance"`
}

// position is a place in the order of forwarding: after the event id of transaction xact.
type position struct {
	xact uint64
	id   int64
}

// Run forwards until ctx ends, whenever this node holds the lock.
func (f *Forwarder) Run(ctx context.Context) {
	defer func() { _ = f.o.Sink.Close() }()
	for ctx.Err() == nil {
		if err := f.lead(ctx); err != nil && ctx.Err() == nil {
			f.log.Warn("audit forwarding interrupted", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(f.o.Standby):
		}
	}
}

// lead takes the lock and forwards until ctx ends or the database fails. It returns at once if another node
// forwards.
func (f *Forwarder) lead(ctx context.Context) error {
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	var locked bool
	defer func() {
		if locked {
			// A connection that might still hold the lock is closed rather than returned to the pool.
			bg := context.WithoutCancel(ctx)
			if _, err := conn.Exec(bg, "SELECT pg_advisory_unlock($1)", db.LockAuditForward); err != nil {
				_ = conn.Conn().Close(bg)
			}
		}
		conn.Release()
	}()
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", db.LockAuditForward).Scan(&locked); err != nil || !locked {
		return err
	}
	if err := f.begin(ctx, conn); err != nil {
		return err
	}
	f.log.Info("forwarding audit events")
	var backoff time.Duration
	for {
		n, err := f.cycle(ctx, conn)
		var de *deliveryError
		wait := f.o.Poll
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.As(err, &de):
			backoff = min(max(2*backoff, time.Second), time.Minute)
			wait = backoff
		case err != nil:
			return err
		case n > 0:
			backoff, wait = 0, 0
		default:
			backoff = 0
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
	}
}

// deliveryError is a failure of the sink (rather than of the database).
type deliveryError struct{ err error }

func (e *deliveryError) Error() string { return e.err.Error() }

// begin prepares the position: on the first start, forwarding begins with the events still to come; a position from
// another database cluster (a restore with other tools than Kogge's) is replaced likewise. A new target is
// recorded in the audit log, which then tells the SIEM where forwarding came from.
func (f *Forwarder) begin(ctx context.Context, conn *pgxpool.Conn) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var target string
		var at, next uint64
		err := tx.QueryRow(ctx, `SELECT target, xact, pg_snapshot_xmax(pg_current_snapshot()) FROM audit_forwarding
			WHERE id = 1 FOR UPDATE`).Scan(&target, &at, &next)
		switch {
		case db.IsNoRows(err):
			p, err := upcoming(ctx, tx)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO audit_forwarding (id, target, xact, event_id) VALUES (1, $1, $2, $3)",
				f.o.Target, p.xact, p.id); err != nil {
				return err
			}
			return Record(ctx, tx, Entry{Action: "audit.forward.start", TargetType: "audit", TargetID: "forwarding",
				Details: map[string]any{"target": f.o.Target}})
		case err != nil:
			return err
		case at >= next:
			p, err := upcoming(ctx, tx)
			if err != nil {
				return err
			}
			f.log.Warn("the forwarding position comes from another database; forwarding continues with the events to come")
			if _, err := tx.Exec(ctx, `UPDATE audit_forwarding SET target = $1, xact = $2, event_id = $3, started_at = now(),
				forwarded = 0, forwarded_at = NULL WHERE id = 1`, f.o.Target, p.xact, p.id); err != nil {
				return err
			}
			return Record(ctx, tx, Entry{Action: "audit.forward.reset", TargetType: "audit", TargetID: "forwarding",
				Details: map[string]any{"target": f.o.Target}})
		case target != f.o.Target:
			// The counts are the new target's.
			if _, err := tx.Exec(ctx, `UPDATE audit_forwarding SET target = $1, started_at = now(), forwarded = 0,
				forwarded_at = NULL WHERE id = 1`, f.o.Target); err != nil {
				return err
			}
			if target == "" {
				return Record(ctx, tx, Entry{Action: "audit.forward.start", TargetType: "audit", TargetID: "forwarding",
					Details: map[string]any{"target": f.o.Target}})
			}
			return Record(ctx, tx, Entry{Action: "audit.forward.target", TargetType: "audit", TargetID: "forwarding",
				Details: map[string]any{"from": target, "target": f.o.Target}})
		}
		return nil
	})
}

// upcoming is the position before the events still to come: those of the transactions that are running or start
// later.
func upcoming(ctx context.Context, q db.Querier) (position, error) {
	bound, err := settled(ctx, q)
	if err != nil {
		return position{}, err
	}
	return position{xact: bound - 1, id: math.MaxInt64}, nil
}

// settled returns the transaction id below which every transaction that could write to Kogge's database has ended:
// that of the oldest transaction running in it, or the next one. A transaction has its id from its first write on,
// before it records an event, and holds locks in its database until it ends; pg_locks shows them to every role. The
// events of transactions below the bound must be read with a later statement, whose snapshot sees all of them.
func settled(ctx context.Context, q db.Querier) (uint64, error) {
	var (
		next    uint64
		running []int64
	)
	err := q.QueryRow(ctx, `SELECT pg_snapshot_xmax(pg_current_snapshot()),
			coalesce(array_agg(DISTINCT t.transactionid::text::int8), '{}')
		FROM pg_locks t
		WHERE t.locktype = 'transactionid' AND t.mode = 'ExclusiveLock' AND t.granted
			AND t.virtualtransaction IN (SELECT virtualtransaction FROM pg_locks
				WHERE database = (SELECT oid FROM pg_database WHERE datname = current_database()))`).Scan(&next, &running)
	if err != nil {
		return 0, err
	}
	bound := next
	for _, x := range running {
		// pg_locks has 32-bit ids: they are of the epoch of the next id, or of the one before it.
		full := next&^0xffffffff | uint64(x)
		if full >= next {
			full -= 1 << 32
		}
		bound = min(bound, full)
	}
	return bound, nil
}

// cycle forwards the next batch and returns the number of events delivered.
func (f *Forwarder) cycle(ctx context.Context, conn *pgxpool.Conn) (int, error) {
	var from position
	if err := conn.QueryRow(ctx, "SELECT xact, event_id FROM audit_forwarding WHERE id = 1").Scan(&from.xact, &from.id); err != nil {
		return 0, err
	}
	bound, err := settled(ctx, conn)
	if err != nil {
		return 0, err
	}
	lines, last, err := f.next(ctx, conn, from, bound)
	if err != nil || len(lines) == 0 {
		return 0, err
	}
	if err := f.o.Sink.Send(ctx, lines); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		f.failed(err)
		msg := []rune(err.Error())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if _, err := conn.Exec(ctx, `UPDATE audit_forwarding SET error = $1, error_at = now(),
			failing_since = coalesce(failing_since, now()) WHERE id = 1`, string(msg)); err != nil {
			return 0, err
		}
		return 0, &deliveryError{err}
	}
	f.recovered()
	// The position moves only from where this batch began: a rewind meanwhile (kogge admin forward-audit) wins. A
	// delivered batch is recorded also while the node stops, so that a restart does not send it again.
	if _, err := conn.Exec(context.WithoutCancel(ctx), `UPDATE audit_forwarding SET xact = $1, event_id = $2, forwarded = forwarded + $3,
		forwarded_at = now(), error = NULL, error_at = NULL, failing_since = NULL
		WHERE id = 1 AND xact = $4 AND event_id = $5`, last.xact, last.id, len(lines), from.xact, from.id); err != nil {
		return 0, err
	}
	return len(lines), nil
}

// next reads the events after from of the transactions below bound, as lines of at most forwardBytes together. It
// reads none once its snapshot sees the position moved: a rewind and its own event are written together, so the
// event comes only after the events the rewind sends again, not also before them.
func (f *Forwarder) next(ctx context.Context, conn *pgxpool.Conn, from position, bound uint64) ([]Line, position, error) {
	rows, err := conn.Query(ctx, `
		SELECT e.xact, e.id, e.at, e.actor_id, u.username, u.display_name, host(e.actor_ip), e.action, e.target_type,
			e.target_id, e.details
		FROM audit_events e LEFT JOIN users u ON u.id = e.actor_id
		WHERE (e.xact, e.id) > ($1, $2) AND e.xact < $3
			AND EXISTS (SELECT FROM audit_forwarding WHERE id = 1 AND xact = $1 AND event_id = $2)
		ORDER BY e.xact, e.id LIMIT $4`, from.xact, from.id, bound, forwardBatch)
	if err != nil {
		return nil, position{}, err
	}
	defer rows.Close()
	var (
		lines []Line
		last  position
		size  int
	)
	for rows.Next() {
		var p position
		ev := forwarded{Instance: f.o.Instance}
		if err := rows.Scan(&p.xact, &p.id, &ev.At, &ev.ActorID, &ev.ActorUsername, &ev.ActorName, &ev.ActorIP,
			&ev.Action, &ev.TargetType, &ev.TargetID, &ev.Details); err != nil {
			return nil, position{}, err
		}
		ev.ID, ev.At = p.id, ev.At.UTC()
		line, err := marshalLine(ev)
		if err != nil {
			return nil, position{}, err
		}
		if size+len(line) > forwardBytes && len(lines) > 0 {
			break
		}
		lines = append(lines, Line{JSON: line, At: ev.At, Action: ev.Action})
		last, size = p, size+len(line)
	}
	return lines, last, rows.Err()
}

// marshalLine encodes an event as one line, without its details when they would make it longer than maxLine.
func marshalLine(ev forwarded) ([]byte, error) {
	encode := func(ev forwarded) ([]byte, error) {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(ev); err != nil {
			return nil, err
		}
		return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
	}
	if ev.Details == nil {
		ev.Details = map[string]any{}
	}
	line, err := encode(ev)
	if err == nil && len(line) > maxLine {
		ev.Details = map[string]any{"truncated": true}
		line, err = encode(ev)
	}
	return line, err
}

func (f *Forwarder) failed(err error) {
	switch t := time.Now(); {
	case f.failing.IsZero():
		f.failing, f.logged = t, t
		f.log.Warn("audit events cannot be forwarded", "error", err)
	case t.Sub(f.logged) >= 10*time.Minute:
		f.logged = t
		f.log.Warn("audit events still cannot be forwarded", "since", f.failing, "error", err)
	}
}

func (f *Forwarder) recovered() {
	if !f.failing.IsZero() {
		f.log.Info("audit events are forwarded again", "after", time.Since(f.failing).Round(time.Second))
		f.failing = time.Time{}
	}
}

// ForwardingOff records that forwarding was switched off, when a node that does not forward finds a target in the
// position.
func ForwardingOff(ctx context.Context, pool *pgxpool.Pool) error {
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		var target string
		err := tx.QueryRow(ctx, "SELECT target FROM audit_forwarding WHERE id = 1 FOR UPDATE").Scan(&target)
		if db.IsNoRows(err) || (err == nil && target == "") {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE audit_forwarding SET target = '', error = NULL, failing_since = NULL WHERE id = 1"); err != nil {
			return err
		}
		return Record(ctx, tx, Entry{Action: "audit.forward.stop", TargetType: "audit", TargetID: "forwarding",
			Details: map[string]any{"target": target}})
	})
}

// RewindForwarding makes forwarding continue with the events from the given time on (some written shortly before
// may come again), or, for the zero time, with the events still to come, skipping those that wait.
func RewindForwarding(ctx context.Context, pool *pgxpool.Pool, actorID int64, from time.Time) error {
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		p, err := upcoming(ctx, tx)
		if err != nil {
			return err
		}
		if !from.IsZero() {
			var first *uint64
			if err := tx.QueryRow(ctx, "SELECT min(xact) FROM audit_events WHERE at >= $1", from).Scan(&first); err != nil {
				return err
			}
			if first != nil {
				p = position{xact: *first - 1, id: math.MaxInt64}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_forwarding (id, target, xact, event_id) VALUES (1, '', $1, $2)
			ON CONFLICT (id) DO UPDATE SET xact = excluded.xact, event_id = excluded.event_id`, p.xact, p.id); err != nil {
			return err
		}
		details := map[string]any{"from": "now"}
		if !from.IsZero() {
			details["from"] = from.UTC().Format(time.RFC3339)
		}
		return Record(ctx, tx, Entry{ActorID: actorID, Action: "audit.forward.rewind", TargetType: "audit",
			TargetID: "forwarding", Details: details})
	})
}

// ForwardStatus is the state of the forwarding, for administrators.
type ForwardStatus struct {
	Configured    bool       `json:"configured" doc:"Forwarding is configured (KOGGE_AUDIT_FORWARD_URL)"`
	Active        bool       `json:"active" doc:"A node forwards now"`
	Target        string     `json:"target,omitempty" doc:"Where the events go, without credentials"`
	StartedAt     *time.Time `json:"startedAt,omitempty" doc:"When forwarding to the target began"`
	Forwarded     int64      `json:"forwarded" doc:"Events forwarded to the target"`
	LastEventID   *int64     `json:"lastEventId,omitempty" doc:"The event forwarded last"`
	ForwardedAt   *time.Time `json:"forwardedAt,omitempty" doc:"Time of the last delivery to the target"`
	Waiting       int64      `json:"waiting" doc:"Events not forwarded yet, counted up to a million"`
	OldestWaiting *time.Time `json:"oldestWaiting,omitempty" doc:"Time of the oldest event not forwarded yet"`
	Error         string     `json:"error,omitempty" doc:"Why deliveries fail, while they do"`
	FailingSince  *time.Time `json:"failingSince,omitempty"`
}

// Forwarding reads the state of the forwarding; Configured is the caller's to set.
func Forwarding(ctx context.Context, q db.Querier) (ForwardStatus, error) {
	var (
		s       ForwardStatus
		p       position
		failure *string
	)
	err := q.QueryRow(ctx, `SELECT target, started_at, forwarded, xact, event_id, forwarded_at, error, failing_since,
			EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND classid::int8 = $1 AND objid::int8 = $2
				AND objsubid = 1 AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))
		FROM audit_forwarding WHERE id = 1`, db.LockAuditForward>>32, db.LockAuditForward&0xffffffff).Scan(&s.Target,
		&s.StartedAt, &s.Forwarded, &p.xact, &p.id, &s.ForwardedAt, &failure, &s.FailingSince, &s.Active)
	if db.IsNoRows(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if failure != nil {
		s.Error = *failure
	}
	if p.id != math.MaxInt64 {
		s.LastEventID = &p.id
	}
	err = q.QueryRow(ctx, `SELECT count(*), min(at) FROM (SELECT at FROM audit_events WHERE (xact, id) > ($1, $2)
		ORDER BY xact, id LIMIT 1000000) w`, p.xact, p.id).Scan(&s.Waiting, &s.OldestWaiting)
	if err != nil {
		return s, fmt.Errorf("waiting audit events: %w", err)
	}
	return s, nil
}
