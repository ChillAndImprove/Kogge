// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kogge.de/kogge/internal/platform/db"
)

// What long migrations need besides the transfer itself: progress output, failures of single entities that do
// not stop a load, and in-memory state that follows the transactions it belongs to.

// progress reports how far a step is (done of total, rate, time left) at most every interval, and once at the end.
type progress struct {
	log      *slog.Logger
	what     string
	total    int
	done     int
	failed   int
	start    time.Time
	last     time.Time
	interval time.Duration
	mu       sync.Mutex
}

// progressInterval is how often long steps report; tests shorten it.
var progressInterval = 10 * time.Second

func newProgress(log *slog.Logger, what string, total int) *progress {
	now := time.Now()
	return &progress{log: log, what: what, total: total, start: now, last: now, interval: progressInterval}
}

// add counts finished items and reports when it is time.
func (p *progress) add(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += n
	if now := time.Now(); now.Sub(p.last) >= p.interval {
		p.last = now
		p.report(now, false)
	}
}

// fail counts an item that failed (it counts as done, too).
func (p *progress) fail() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.failed++
	p.mu.Unlock()
	p.add(1)
}

// finish reports the totals.
func (p *progress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.report(time.Now(), true)
}

// rate is the number of items per second so far.
func (p *progress) rate(now time.Time) float64 {
	if s := now.Sub(p.start).Seconds(); s > 0 {
		return float64(p.done) / s
	}
	return 0
}

func (p *progress) report(now time.Time, final bool) {
	if p.log == nil || (final && p.done == 0 && p.total == 0) {
		return
	}
	rate := p.rate(now)
	attrs := []any{"done", p.done}
	if p.total > 0 {
		attrs = append(attrs, "total", p.total)
	}
	if p.failed > 0 {
		attrs = append(attrs, "failed", p.failed)
	}
	var msg string
	switch {
	case final:
		msg = fmt.Sprintf("%s: %d in %s (%.1f per second)", p.what, p.done, now.Sub(p.start).Round(time.Second), rate)
	case p.total > 0:
		msg = fmt.Sprintf("%s: %d of %d (%d%%), %.1f per second", p.what, p.done, p.total, p.done*100/p.total, rate)
		if rate > 0 && p.done < p.total {
			left := time.Duration(float64(p.total-p.done) / rate * float64(time.Second))
			msg += ", about " + left.Round(time.Second).String() + " left"
		}
	default:
		msg = fmt.Sprintf("%s: %d so far, %.1f per second", p.what, p.done, rate)
	}
	p.log.Info(msg, attrs...)
}

// journal undoes changes of in-memory state (the caches of what a load created) when the database work they
// belong to is rolled back: a cached id of a row that no longer exists would break every later entity.
type journal struct{ undo []func() }

// add registers how to undo a change.
func (j *journal) add(f func()) {
	if j != nil {
		j.undo = append(j.undo, f)
	}
}

func (j *journal) mark() int {
	if j == nil {
		return 0
	}
	return len(j.undo)
}

// rollback undoes the changes since the mark, the last first.
func (j *journal) rollback(mark int) {
	if j == nil {
		return
	}
	for i := len(j.undo) - 1; i >= mark; i-- {
		j.undo[i]()
	}
	j.undo = j.undo[:mark]
}

// commit forgets the changes: their transaction committed.
func (j *journal) commit() {
	if j != nil {
		j.undo = j.undo[:0]
	}
}

// Failure is an entity a load could not write. The load goes on with the others and reports it.
type Failure struct {
	Entity   string `json:"entity" doc:"issue, page, pull request, …"`
	SourceID string `json:"sourceId" doc:"Id of the entity in the source system"`
	Name     string `json:"name,omitempty" doc:"Key or title"`
	Error    string `json:"error"`
}

// maxFailures bounds the failures listed in a report (all are counted).
const maxFailures = 10000

// Timing is the measured duration of a phase of a load, for estimates of later loads (see the analysis).
type Timing struct {
	Items   int     `json:"items"`
	Bytes   int64   `json:"bytes,omitempty"`
	Seconds float64 `json:"seconds"`
}

func (r *Report) time(phase string, items int, bytes int64, d time.Duration) {
	if r.Timing == nil {
		r.Timing = map[string]Timing{}
	}
	t := r.Timing[phase]
	t.Items += items
	t.Bytes += bytes
	t.Seconds += d.Seconds()
	r.Timing[phase] = t
}

// fatal reports errors that end a load: the database, the disk or the connection failed, or the load was
// stopped. They would recur with every following entity. Other errors concern the data of one entity.
func fatal(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pgx.ErrTxClosed) ||
		errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code[:2] {
		case "08", "25", "26", "28", "3D", "3F", "42", "53", "54", "57", "58", "F0", "XX":
			// Connection, transaction state, authorisation, schema, resources, operator intervention, system.
			return true
		}
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return true // the disk is full, read-only or failing
	}
	var netErr net.Error
	if errors.As(err, &netErr) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}
	var connErr *pgconn.ConnectError
	return errors.As(err, &connErr)
}

// failures collects the entities a load could not write.
type failures struct {
	rep  *Report
	stop bool // the first failure ends the load
	log  *slog.Logger
	jr   *journal
}

// record notes a failed entity; it returns the error when the load has to stop.
func (f *failures) record(kind, srcID, name string, err error) error {
	if fatal(err) || f.stop {
		if name != "" {
			return fmt.Errorf("%s %s: %w", kind, name, err)
		}
		return fmt.Errorf("%s %s: %w", kind, srcID, err)
	}
	f.rep.Counts["failures"]++
	if len(f.rep.Failures) < maxFailures {
		f.rep.Failures = append(f.rep.Failures, Failure{Entity: kind, SourceID: srcID, Name: name, Error: err.Error()})
	}
	if f.log != nil {
		f.log.Warn("not loaded", "entity", kind, "id", srcID, "name", name, "error", err)
	}
	return nil
}

// entity runs fn for one entity in a savepoint of tx: when it fails for reasons of the entity, its changes and
// the in-memory changes since are undone, the failure is recorded and the transaction goes on. ok reports
// whether the entity was written.
func (f *failures) entity(ctx context.Context, tx pgx.Tx, kind, srcID, name string, fn func(tx pgx.Tx) error) (ok bool, err error) {
	mark := f.jr.mark()
	err = db.InTx(ctx, tx, fn)
	if err == nil {
		return true, nil
	}
	f.jr.rollback(mark)
	return false, f.record(kind, srcID, name, err)
}

// inTx runs fn in a transaction of its own; in-memory changes follow its outcome.
func (f *failures) inTx(ctx context.Context, b db.Beginner, fn func(tx pgx.Tx) error) error {
	mark := f.jr.mark()
	err := db.InTx(ctx, b, fn)
	if err != nil {
		f.jr.rollback(mark)
		return err
	}
	if mark == 0 {
		f.jr.commit()
	}
	return nil
}
