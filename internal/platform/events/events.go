// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package events is the in-process event bus. Publishing an event inside a transaction inserts the jobs of all
// subscribers in the same transaction (transactional outbox on River) and notifies live listeners at commit.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/platform/jobs"
)

// Event is a domain event. Name is a dotted name such as "issue.updated".
type Event interface {
	EventName() string
}

// Live is implemented by events that browsers should learn about (they refetch the entity).
type Live interface {
	LiveMessage() LiveMessage
}

// LiveMessage is the small payload sent to browsers: never content, only what changed and where.
type LiveMessage struct {
	Type      string `json:"type"`                // e.g. "issue"
	ID        string `json:"id,omitempty"`        // entity id or key
	Container string `json:"container,omitempty"` // e.g. "project:12", used for visibility checks
	User      int64  `json:"user,omitempty"`      // if set, only this user receives the message
}

// LiveChannel is the PostgreSQL notification channel for live messages.
const LiveChannel = "kogge_live"

// Subscriber turns an event into a job (or nil to ignore it).
type Subscriber func(ev Event) (river.JobArgs, *river.InsertOpts)

// Bus maps event names to subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[string][]Subscriber
	all  []Subscriber
	jobs *jobs.Client
}

// NewBus creates a bus. The jobs client may be set later (SetJobs) because workers depend on services.
func NewBus() *Bus { return &Bus{subs: map[string][]Subscriber{}} }

// SetJobs sets the River client used to insert subscriber jobs.
func (b *Bus) SetJobs(c *jobs.Client) { b.jobs = c }

// Subscribe registers a subscriber for the named events; with no names it receives every event.
func (b *Bus) Subscribe(s Subscriber, names ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(names) == 0 {
		b.all = append(b.all, s)
		return
	}
	for _, n := range names {
		b.subs[n] = append(b.subs[n], s)
	}
}

// Publish inserts the subscriber jobs for the events and sends live messages, all within tx.
func (b *Bus) Publish(ctx context.Context, tx pgx.Tx, evs ...Event) error {
	b.mu.RLock()
	var params []river.InsertManyParams
	for _, ev := range evs {
		for _, s := range append(append([]Subscriber{}, b.subs[ev.EventName()]...), b.all...) {
			if args, opts := s(ev); args != nil {
				params = append(params, river.InsertManyParams{Args: args, InsertOpts: opts})
			}
		}
	}
	b.mu.RUnlock()
	if len(params) > 0 {
		if b.jobs == nil {
			return fmt.Errorf("events: no job client configured")
		}
		if _, err := b.jobs.InsertManyTx(ctx, tx, params); err != nil {
			return fmt.Errorf("events: enqueue: %w", err)
		}
	}
	for _, ev := range evs {
		if l, ok := ev.(Live); ok {
			if err := NotifyLive(ctx, tx, l.LiveMessage()); err != nil {
				return err
			}
		}
	}
	return nil
}

// NotifyLive sends a live message at commit of tx.
func NotifyLive(ctx context.Context, tx pgx.Tx, m LiveMessage) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "SELECT pg_notify($1, $2)", LiveChannel, string(payload))
	return err
}
