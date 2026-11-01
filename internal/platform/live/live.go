// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package live forwards live messages (PostgreSQL LISTEN/NOTIFY) to browsers over server-sent events. Messages
// carry no content; each subscriber only receives messages for containers it may see.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/events"
)

// Visibility decides whether a user may see a container ("project:12"). Modules register one per container
// type; containers of unknown types are not delivered.
type Visibility func(ctx context.Context, userID int64, groups []int64, containerID string) bool

// CacheChannel carries the names of server-side caches to drop on every node ("authz", "settings").
const CacheChannel = "kogge_cache"

// Hub fans out live messages to subscribers on this node and cache invalidations to registered handlers.
type Hub struct {
	pool        *pgxpool.Pool
	log         *slog.Logger
	mu          sync.RWMutex
	subs        map[*subscriber]struct{}
	checks      map[string]Visibility
	invalidates map[string][]func()
	listeners   map[string][]func(payload string)
	done        chan struct{} // closed when Run ends: streams end, so that shutdowns need not wait for them
	stopOnce    sync.Once
	listening   chan struct{} // closed when the hub listens for the first time
	listenOnce  sync.Once
}

type subscriber struct {
	userID int64
	groups []int64
	ch     chan events.LiveMessage
}

// NewHub creates a hub; Run must be started to receive messages.
func NewHub(pool *pgxpool.Pool, logger *slog.Logger) *Hub {
	return &Hub{pool: pool, log: logger, subs: map[*subscriber]struct{}{}, checks: map[string]Visibility{},
		invalidates: map[string][]func(){}, listeners: map[string][]func(string){}, done: make(chan struct{}),
		listening: make(chan struct{})}
}

// Listening is closed once the hub listens for notifications for the first time. Messages committed before reach no
// subscriber of this node; the invalidation that starts every listening tells them to catch up.
func (h *Hub) Listening() <-chan struct{} { return h.listening }

// OnInvalidate registers a function that drops a cache when any node calls InvalidateCache(name).
func (h *Hub) OnInvalidate(name string, fn func()) {
	h.mu.Lock()
	h.invalidates[name] = append(h.invalidates[name], fn)
	h.mu.Unlock()
}

// InvalidateCache asks all nodes (including this one, at commit) to drop the named cache.
func InvalidateCache(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, name string) error {
	_, err := q.Exec(ctx, "SELECT pg_notify($1, $2)", CacheChannel, name)
	return err
}

// Listen calls fn with the payload of every notification on a channel, from all nodes, in the order of their
// commits. Modules register their channels (names of the code, never of input) before Run; after a lost connection
// to the database, OnInvalidate functions tell that notifications may have been missed.
func (h *Hub) Listen(channel string, fn func(payload string)) {
	h.mu.Lock()
	h.listeners[channel] = append(h.listeners[channel], fn)
	h.mu.Unlock()
}

// RegisterVisibility registers the check for a container type ("project", "space", "repo").
func (h *Hub) RegisterVisibility(containerType string, v Visibility) {
	h.mu.Lock()
	h.checks[containerType] = v
	h.mu.Unlock()
}

// Run listens for notifications until ctx ends, reconnecting after errors.
func (h *Hub) Run(ctx context.Context) {
	defer h.stopOnce.Do(func() { close(h.done) })
	for ctx.Err() == nil {
		if err := h.listen(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn("live updates: listener failed, retrying", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	statement := "LISTEN " + events.LiveChannel + "; LISTEN " + CacheChannel
	h.mu.RLock()
	for channel := range h.listeners {
		statement += "; LISTEN " + pgx.Identifier{channel}.Sanitize()
	}
	h.mu.RUnlock()
	if _, err := conn.Exec(ctx, statement); err != nil {
		return err
	}
	// Anything may have changed while the listener was down.
	h.invalidateAll()
	h.listenOnce.Do(func() { close(h.listening) })
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "UNLISTEN *") }()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		h.mu.RLock()
		fns := h.listeners[n.Channel]
		h.mu.RUnlock()
		if len(fns) > 0 {
			for _, fn := range fns {
				fn(n.Payload)
			}
			continue
		}
		if n.Channel == CacheChannel {
			h.mu.RLock()
			fns := h.invalidates[n.Payload]
			h.mu.RUnlock()
			for _, fn := range fns {
				fn()
			}
			continue
		}
		var m events.LiveMessage
		if err := json.Unmarshal([]byte(n.Payload), &m); err != nil {
			continue
		}
		h.dispatch(ctx, m)
	}
}

func (h *Hub) invalidateAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, fns := range h.invalidates {
		for _, fn := range fns {
			fn()
		}
	}
}

func (h *Hub) dispatch(ctx context.Context, m events.LiveMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if m.User != 0 && m.User != s.userID {
			continue
		}
		if m.User == 0 && !h.visible(ctx, s, m.Container) {
			continue
		}
		select {
		case s.ch <- m:
		default: // slow subscriber: drop; the browser refetches on reconnect anyway
		}
	}
}

func (h *Hub) visible(ctx context.Context, s *subscriber, container string) bool {
	if container == "" {
		return false
	}
	typ := container
	for i := 0; i < len(container); i++ {
		if container[i] == ':' {
			typ = container[:i]
			break
		}
	}
	check, ok := h.checks[typ]
	if !ok {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return check(cctx, s.userID, s.groups, container)
}

// streamLifetime bounds a stream: the browser reconnects (EventSource does so by itself) and signs in again, so
// that logouts, deactivations and changed groups take effect.
const streamLifetime = 5 * time.Minute

// Serve streams messages to one authenticated browser until the request ends, the stream's lifetime is over or
// the hub stops.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID int64, groups []int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s := &subscriber{userID: userID, groups: groups, ch: make(chan events.LiveMessage, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	// Spread reconnections of browsers that connected at the same time.
	end := time.NewTimer(streamLifetime + time.Duration(rand.Int64N(int64(time.Minute))))
	defer end.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case <-end.C:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case m := <-s.ch:
			b, _ := json.Marshal(m)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, b)
			flusher.Flush()
		}
	}
}

// Subscribers returns the number of connected browsers on this node.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
