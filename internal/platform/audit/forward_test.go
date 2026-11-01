// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// memorySink keeps what it receives; while fail is set, it fails.
type memorySink struct {
	mu    sync.Mutex
	raw   []string
	lines []map[string]any
	fail  error
}

func (s *memorySink) Send(_ context.Context, lines []audit.Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal(l.JSON, &m); err != nil {
			return err
		}
		if m["action"] != l.Action {
			return fmt.Errorf("line of %v has the action %q", m["action"], l.Action)
		}
		s.raw, s.lines = append(s.raw, string(l.JSON)), append(s.lines, m)
	}
	return nil
}

func (s *memorySink) Close() error { return nil }

func (s *memorySink) setFail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

// actions are the actions received, in order.
func (s *memorySink) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.lines {
		out = append(out, l["action"].(string))
	}
	return out
}

func (s *memorySink) line(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lines[i]
}

// forward runs a forwarder until the returned function (or the end of the test) stops it.
func forward(t *testing.T, pool *pgxpool.Pool, sink audit.Sink, target string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := audit.NewForwarder(pool, audit.ForwardOptions{Sink: sink, Target: target, Instance: "https://kogge.example.com",
		Logger: slog.New(slog.DiscardHandler), Poll: 20 * time.Millisecond, Standby: 50 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}

func record(t *testing.T, q db.Querier, e audit.Entry) {
	t.Helper()
	if err := audit.Record(context.Background(), q, e); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// receive waits until the sink has n events and returns their actions.
func receive(t *testing.T, s *memorySink, n int) []string {
	t.Helper()
	eventually(t, fmt.Sprintf("%d forwarded events", n), func() bool { return len(s.actions()) >= n })
	got := s.actions()
	if len(got) != n {
		t.Fatalf("forwarded %d events, want %d: %v", len(got), n, got)
	}
	return got
}

func status(t *testing.T, pool *pgxpool.Pool) audit.ForwardStatus {
	t.Helper()
	st, err := audit.Forwarding(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestForwarding: events go to the SIEM as JSON lines with the actor's names, in batches and in order, beginning with
// the events after the forwarding started; the status tells what was forwarded.
func TestForwarding(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	var lena int64
	if err := pool.QueryRow(ctx, "INSERT INTO users (username, display_name) VALUES ('lena', 'Lena Huber') RETURNING id").Scan(&lena); err != nil {
		t.Fatal(err)
	}
	record(t, pool, audit.Entry{Action: "before.start"})
	if st := status(t, pool); st.Active || st.Target != "" || st.Waiting != 0 {
		t.Errorf("status before forwarding = %+v", st)
	}

	sink := &memorySink{}
	forward(t, pool, sink, "syslog+tls://siem.example.com:6514")
	if got := receive(t, sink, 1); got[0] != "audit.forward.start" {
		t.Fatalf("first event = %v", got)
	}
	if d := sink.line(0)["details"].(map[string]any); d["target"] != "syslog+tls://siem.example.com:6514" {
		t.Errorf("start details = %v", d)
	}

	record(t, pool, audit.Entry{ActorID: lena, Action: "group.create", TargetType: "group", TargetID: "7",
		Details: map[string]any{"name": "<Ops & Co>"}})
	receive(t, sink, 2)
	ev := sink.line(1)
	at, err := time.Parse(time.RFC3339Nano, ev["at"].(string))
	if err != nil || time.Since(at) > time.Minute || !strings.HasSuffix(ev["at"].(string), "Z") {
		t.Errorf("at = %v (%v)", ev["at"], err)
	}
	for k, want := range map[string]any{"action": "group.create", "actorId": float64(lena), "actorUsername": "lena",
		"actorName": "Lena Huber", "targetType": "group", "targetId": "7", "instance": "https://kogge.example.com"} {
		if ev[k] != want {
			t.Errorf("%s = %v, want %v", k, ev[k], want)
		}
	}
	if _, ok := ev["id"].(float64); !ok || !strings.Contains(sink.raw[1], `"name":"<Ops & Co>"`) {
		t.Errorf("line = %s", sink.raw[1])
	}
	if _, ok := ev["actorIp"]; ok {
		t.Errorf("an event without an address has one: %s", sink.raw[1])
	}

	// More than a batch: all of them, in order, once.
	for i := range 1234 {
		record(t, pool, audit.Entry{Action: "bulk", TargetID: fmt.Sprint(i)})
	}
	receive(t, sink, 1236)
	var last float64
	for i := 2; i < 1236; i++ {
		ev := sink.line(i)
		if ev["targetId"] != fmt.Sprint(i-2) || ev["id"].(float64) <= last {
			t.Fatalf("event %d = %v after id %v", i, ev, last)
		}
		last = ev["id"].(float64)
	}
	eventually(t, "the position", func() bool { st := status(t, pool); return st.Waiting == 0 && st.LastEventID != nil })
	st := status(t, pool)
	if !st.Active || st.Target != "syslog+tls://siem.example.com:6514" || st.Forwarded != 1236 || *st.LastEventID != int64(last) ||
		st.ForwardedAt == nil || st.StartedAt == nil || st.Error != "" || st.FailingSince != nil || st.OldestWaiting != nil {
		t.Errorf("status = %+v", st)
	}
	if slices.Contains(sink.actions(), "before.start") {
		t.Error("an event from before the start was forwarded")
	}
	// Another installation in the same database cluster has its own lock.
	other := pgtest.Pool(t)
	if err := audit.RewindForwarding(ctx, other, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, other); st.Active {
		t.Errorf("another database's forwarder counts: %+v", st)
	}
}

// TestForwardingWaitsForRunningTransactions: an event of a transaction that commits after a later one is not
// skipped — later events wait until it ended; events of transactions that began earlier do not wait for it.
func TestForwardingWaitsForRunningTransactions(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	sink := &memorySink{}
	forward(t, pool, sink, "tcp://siem.example.com:5170")
	receive(t, sink, 1)

	slow, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Rollback(ctx) }()
	record(t, slow, audit.Entry{Action: "slow"})
	record(t, pool, audit.Entry{Action: "fast"})
	time.Sleep(300 * time.Millisecond) // many polls
	if got := sink.actions(); len(got) != 1 {
		t.Fatalf("forwarded while an earlier transaction runs: %v", got)
	}
	if err := slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, sink, 3); !slices.Equal(got[1:], []string{"slow", "fast"}) {
		t.Errorf("order = %v", got)
	}

	// A transaction that writes later than one that commits first does not hold that one back.
	late, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = late.Rollback(ctx) }()
	if _, err := late.Exec(ctx, "SELECT 1"); err != nil { // begun, without a transaction id yet
		t.Fatal(err)
	}
	record(t, pool, audit.Entry{Action: "early"})
	record(t, late, audit.Entry{Action: "late"})
	if got := receive(t, sink, 4); got[3] != "early" {
		t.Errorf("got %v", got)
	}
	if err := late.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, sink, 5); got[4] != "late" {
		t.Errorf("got %v", got)
	}
	// A transaction that is rolled back leaves nothing behind and holds nothing back.
	gone, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record(t, gone, audit.Entry{Action: "rolled-back"})
	if err := gone.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	record(t, pool, audit.Entry{Action: "after"})
	if got := receive(t, sink, 6); got[5] != "after" {
		t.Errorf("got %v", got)
	}
	// A transaction of another database in the same cluster holds nothing back.
	elsewhere, err := pgtest.Pool(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = elsewhere.Rollback(ctx) }()
	record(t, elsewhere, audit.Entry{Action: "elsewhere"})
	record(t, pool, audit.Entry{Action: "here"})
	if got := receive(t, sink, 7); got[6] != "here" {
		t.Errorf("got %v", got)
	}
}

// TestForwardingRetries: while the SIEM fails, nothing is lost: the status shows the error and the events waiting,
// and once it accepts again, everything is delivered and the error cleared.
func TestForwardingRetries(t *testing.T) {
	pool := pgtest.Pool(t)
	sink := &memorySink{}
	sink.setFail(errors.New("dial tcp 192.0.2.1:6514: connect: connection refused"))
	forward(t, pool, sink, "syslog+tcp://siem.example.com")
	eventually(t, "the start", func() bool { return status(t, pool).Target != "" })
	record(t, pool, audit.Entry{Action: "one"})
	eventually(t, "the failure in the status", func() bool { return status(t, pool).Error != "" })
	st := status(t, pool)
	if !strings.Contains(st.Error, "connection refused") || st.FailingSince == nil || st.Waiting != 2 ||
		st.OldestWaiting == nil || st.Forwarded != 0 || !st.Active {
		t.Errorf("failing status = %+v", st)
	}
	sink.setFail(nil)
	if got := receive(t, sink, 2); !slices.Equal(got, []string{"audit.forward.start", "one"}) {
		t.Errorf("got %v", got)
	}
	eventually(t, "the recovered status", func() bool { st := status(t, pool); return st.Error == "" && st.Waiting == 0 })
	if st := status(t, pool); st.FailingSince != nil || st.Forwarded != 2 {
		t.Errorf("recovered status = %+v", st)
	}
}

// TestOneForwarderAtATime: of several nodes, one forwards; when it stops, another takes over where it stopped and
// records its target.
func TestOneForwarderAtATime(t *testing.T) {
	pool := pgtest.Pool(t)
	a, b := &memorySink{}, &memorySink{}
	stopA := forward(t, pool, a, "tcp://a.example.com:5170")
	receive(t, a, 1)
	forward(t, pool, b, "tcp://b.example.com:5170")
	record(t, pool, audit.Entry{Action: "x"})
	receive(t, a, 2)
	eventually(t, "the position", func() bool { return status(t, pool).Waiting == 0 })
	time.Sleep(200 * time.Millisecond)
	if got := b.actions(); len(got) != 0 {
		t.Fatalf("the second node forwarded %v", got)
	}

	stopA()
	record(t, pool, audit.Entry{Action: "y"})
	got := receive(t, b, 2)
	slices.Sort(got)
	if !slices.Equal(got, []string{"audit.forward.target", "y"}) {
		t.Fatalf("the second node forwarded %v", got)
	}
	for i := range 2 {
		if ev := b.line(i); ev["action"] == "audit.forward.target" {
			d := ev["details"].(map[string]any)
			if d["from"] != "tcp://a.example.com:5170" || d["target"] != "tcp://b.example.com:5170" {
				t.Errorf("target change = %v", d)
			}
		}
	}
	if len(a.actions()) != 2 {
		t.Errorf("the stopped node forwarded %v", a.actions())
	}
	// The status counts what went to the new target.
	eventually(t, "the position", func() bool { return status(t, pool).Waiting == 0 })
	if st := status(t, pool); st.Target != "tcp://b.example.com:5170" || st.Forwarded != 2 || st.ForwardedAt == nil {
		t.Errorf("status after the takeover = %+v", st)
	}
}

// TestRewindForwarding: kogge admin forward-audit sends the events from a day on again, or skips those waiting.
func TestRewindForwarding(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	sink := &memorySink{}
	stop := forward(t, pool, sink, "tcp://siem.example.com:5170")
	receive(t, sink, 1)
	record(t, pool, audit.Entry{Action: "old"})
	receive(t, sink, 2)

	if err := audit.RewindForwarding(ctx, pool, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, sink, 5); !slices.Equal(got[2:], []string{"audit.forward.start", "old", "audit.forward.rewind"}) {
		t.Errorf("after the rewind: %v", got)
	}
	if d := sink.line(4)["details"].(map[string]any); !strings.HasSuffix(d["from"].(string), "Z") {
		t.Errorf("rewind details = %v", d)
	}

	// Skipping: what waits while the SIEM is away is not sent.
	stop()
	record(t, pool, audit.Entry{Action: "skipped"})
	if err := audit.RewindForwarding(ctx, pool, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	forward(t, pool, sink, "tcp://siem.example.com:5170")
	record(t, pool, audit.Entry{Action: "new"})
	if got := receive(t, sink, 7); !slices.Equal(got[5:], []string{"audit.forward.rewind", "new"}) {
		t.Errorf("after skipping: %v", got)
	}
	if d := sink.line(5)["details"].(map[string]any); d["from"] != "now" {
		t.Errorf("rewind details = %v", d)
	}
}

// TestForwardingSwitchedOff: switching forwarding off and on is recorded, and the events in between are forwarded
// when it is on again.
func TestForwardingSwitchedOff(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	sink := &memorySink{}
	stop := forward(t, pool, sink, "tcp://siem.example.com:5170")
	receive(t, sink, 1)
	stop()
	for range 2 {
		if err := audit.ForwardingOff(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if st := status(t, pool); st.Target != "" || st.Active || st.Waiting != 1 {
		t.Errorf("status after switching off = %+v", st)
	}
	record(t, pool, audit.Entry{Action: "meanwhile"})
	forward(t, pool, sink, "tcp://siem.example.com:5170")
	got := receive(t, sink, 4)
	if !slices.Equal(got[1:3], []string{"audit.forward.stop", "meanwhile"}) || got[3] != "audit.forward.start" {
		t.Errorf("got %v", got)
	}
	if d := sink.line(1)["details"].(map[string]any); d["target"] != "tcp://siem.example.com:5170" {
		t.Errorf("stop details = %v", d)
	}
}

// TestPositionFromAnotherCluster: a position beyond the transactions of this database (restored with other tools
// than Kogge's) would hold back every event; forwarding continues with the events to come instead.
func TestPositionFromAnotherCluster(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	sink := &memorySink{}
	stop := forward(t, pool, sink, "tcp://siem.example.com:5170")
	receive(t, sink, 1)
	stop()
	if _, err := pool.Exec(ctx, "UPDATE audit_forwarding SET xact = '9000000000000'::xid8 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	forward(t, pool, sink, "tcp://siem.example.com:5170")
	if got := receive(t, sink, 2); got[1] != "audit.forward.reset" {
		t.Fatalf("got %v", got)
	}
	record(t, pool, audit.Entry{Action: "after"})
	if got := receive(t, sink, 3); got[2] != "after" {
		t.Errorf("got %v", got)
	}
}

// TestLongLines: an event whose details would make its line too long for a datagram is forwarded without them.
func TestLongLines(t *testing.T) {
	pool := pgtest.Pool(t)
	sink := &memorySink{}
	forward(t, pool, sink, "syslog+udp://siem.example.com")
	receive(t, sink, 1)
	record(t, pool, audit.Entry{Action: "settings.update", Details: map[string]any{"banner": strings.Repeat("x", 70<<10)}})
	receive(t, sink, 2)
	if d := sink.line(1)["details"].(map[string]any); d["truncated"] != true || len(sink.raw[1]) > 60<<10 {
		t.Errorf("long line: %d bytes, details %v", len(sink.raw[1]), d)
	}
}
