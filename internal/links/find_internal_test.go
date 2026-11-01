// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// The quick search answers with the products that answered within the budget; slow and failing ones are named
// as incomplete, and the slow ones are not waited for.
func TestFanOut(t *testing.T) {
	s := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	release := make(chan struct{})
	defer close(release)
	run := func(ctx context.Context, product string) (string, error) {
		switch product {
		case "slow":
			select {
			case <-release:
			case <-ctx.Done():
			}
		case "broken":
			return "", errors.New("boom")
		}
		return "hits of " + product, nil
	}
	start := time.Now()
	done, late := fanOut(context.Background(), s, 50*time.Millisecond, []string{"fast", "slow", "broken"}, run)
	if took := time.Since(start); took > time.Second {
		t.Errorf("waited %v for the slow product", took)
	}
	if len(done) != 1 || done["fast"] != "hits of fast" || !slices.Equal(late, []string{"slow", "broken"}) {
		t.Errorf("done %v, late %v", done, late)
	}

	// A request that ends (the client typed on) ends the waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done, late = fanOut(ctx, s, time.Minute, []string{"slow"}, run)
	if len(done) != 0 || !slices.Equal(late, []string{"slow"}) {
		t.Errorf("after the end of the request: done %v, late %v", done, late)
	}
}

// The node's search slots are a quarter of the database pool, at least two.
func TestSearchSlots(t *testing.T) {
	for size, want := range map[int]int{2: 2, 4: 2, 8: 2, 12: 3, 40: 10} {
		// The pool connects only when asked to (no connection is made here).
		cfg, err := pgxpool.ParseConfig(fmt.Sprintf("postgres://kogge@127.0.0.1:1/kogge?pool_max_conns=%d", size))
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := searchSlots(pool); got != want {
			t.Errorf("a pool of %d: %d slots, want %d", size, got, want)
		}
		pool.Close()
	}
	if got := searchSlots(nil); got != minSearchSlots {
		t.Errorf("without a pool: %d slots", got)
	}
}

// The searches of all requests together hold at most the node's slots. When many people search at once, the
// product searches beyond the slots wait for one within their budget, or are late without having run; a search
// that started keeps its slot until it ends, also after its request has stopped waiting for it; and the rest of
// the pool stays free for everything else.
func TestFanOutBoundsConnections(t *testing.T) {
	ctx := context.Background()
	url := pgtest.New(t)
	const poolSize = 12
	pool, err := db.Open(ctx, url, "kogge-test", poolSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := NewService(pool, nil, nil, nil, slog.New(slog.DiscardHandler))
	limit := cap(s.slots)
	if limit != poolSize/4 {
		t.Fatalf("%d slots for a pool of %d connections", limit, poolSize)
	}

	// Slow searches hold their connection until the test lets go of an advisory lock (on a connection of its own).
	blocker, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	if _, err := blocker.Exec(ctx, "SELECT pg_advisory_lock(4711)"); err != nil {
		t.Fatal(err)
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			if _, err := blocker.Exec(ctx, "SELECT pg_advisory_unlock(4711)"); err != nil {
				t.Error(err)
			}
		}
	}
	defer unlock()

	var started, running, most, answered, late atomic.Int32
	search := func(query string) func(context.Context, string) (string, error) {
		return func(ctx context.Context, product string) (string, error) {
			started.Add(1)
			n := running.Add(1)
			defer running.Add(-1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			_, err := pool.Exec(ctx, query)
			return product, err
		}
	}
	const people = 12
	products := int32(len(productOrder))
	searchAtOnce := func(budget time.Duration, query string) {
		started.Store(0)
		most.Store(0)
		answered.Store(0)
		late.Store(0)
		var wg sync.WaitGroup
		for range people {
			wg.Add(1)
			go func() {
				defer wg.Done()
				done, l := fanOut(ctx, s, budget, productOrder, search(query))
				answered.Add(int32(len(done)))
				late.Add(int32(len(l)))
			}()
		}
		wg.Wait()
	}

	// Twelve people use the quick search at once, and every search is slow. As many product searches start as
	// there are slots; all others give up at the end of their budget without having run, and every product of
	// every search is late.
	searchAtOnce(quickBudget, "SELECT pg_advisory_xact_lock_shared(4711)")
	if n, m := started.Load(), most.Load(); n != int32(limit) || m != int32(limit) {
		t.Errorf("%d product searches started, %d at once; want %d (the slots)", n, m, limit)
	}
	if a, l := answered.Load(), late.Load(); a != 0 || l != people*products {
		t.Errorf("answered %d products, %d late; want 0 and %d", a, l, people*products)
	}

	// The searches that started still run in their slots, now that nobody waits for them: they hold no more
	// connections than there are slots, and everything else gets the rest of the pool at once.
	waiting := func() int {
		var n int
		err := blocker.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = 4711
			AND NOT granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for deadline := time.Now().Add(5 * time.Second); waiting() < limit && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if n, c := waiting(), pool.Stat().AcquiredConns(); n != limit || c != int32(limit) {
		t.Errorf("%d searches wait in the database on %d connections; want %d (the slots)", n, c, limit)
	}
	others, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var conns []*pgxpool.Conn
	for range poolSize - limit {
		c, err := pool.Acquire(others)
		if err != nil {
			t.Fatalf("other work got no connection (%d of %d got one): %v", len(conns), poolSize-limit, err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		if err := c.QueryRow(others, "SELECT 1").Scan(new(int)); err != nil {
			t.Error(err)
		}
		c.Release()
	}

	// When the slow searches end, they give their slots back.
	unlock()
	for deadline := time.Now().Add(10 * time.Second); len(s.slots) > 0 || running.Load() > 0; {
		if time.Now().After(deadline) {
			t.Fatalf("%d slots still taken, %d searches running", len(s.slots), running.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Twelve people use the search page at once, and the searches are quick: the products wait for slots within
	// their budget and all answer, and never more of them run at once than there are slots.
	searchAtOnce(pageBudget, "SELECT pg_sleep(0.005)")
	if a, l := answered.Load(), late.Load(); a != people*products || l != 0 {
		t.Errorf("answered %d products, %d late; want %d and 0", a, l, people*products)
	}
	if n, m := started.Load(), most.Load(); n != people*products || m > int32(limit) {
		t.Errorf("%d product searches started, %d at once; want %d, at most %d at once", n, m, people*products, limit)
	}
}

// The activity stream reads its sources at once only in the slots that are free: on a node whose slots are all
// taken it reads them one after another in the request instead, completely, and leaves the slots alone.
func TestRunInSlots(t *testing.T) {
	s := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	var running, most atomic.Int32
	read := func(got []bool) func(int) {
		return func(i int) {
			n := running.Add(1)
			defer running.Add(-1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			got[i] = true
		}
	}
	got := make([]bool, 5)
	s.runInSlots(len(got), read(got))
	if slices.Contains(got, false) || most.Load() > int32(1+cap(s.slots)) || len(s.slots) != 0 {
		t.Errorf("with free slots: read %v, %d at once, %d slots still taken", got, most.Load(), len(s.slots))
	}

	for range cap(s.slots) {
		s.slots <- struct{}{}
	}
	most.Store(0)
	got = make([]bool, 5)
	s.runInSlots(len(got), read(got))
	if slices.Contains(got, false) || most.Load() != 1 || len(s.slots) != cap(s.slots) {
		t.Errorf("with every slot taken: read %v, %d at once, %d of %d slots taken", got, most.Load(), len(s.slots),
			cap(s.slots))
	}
}
