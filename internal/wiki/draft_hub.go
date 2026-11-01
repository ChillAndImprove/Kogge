// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"kogge.de/kogge/internal/platform/db"
)

// draftEvent is what the editors of a shared draft hear: steps, carets, the title, publishing and discarding. It
// is the message editors receive (see draft_socket.go) and, wrapped with the page, the payload of notifications.
type draftEvent struct {
	Type        string         `json:"t"`
	From        int            `json:"from,omitempty"`
	To          int            `json:"to,omitempty"`
	Steps       []draftStepOut `json:"steps,omitempty"`
	Client      string         `json:"client,omitempty"`
	User        *draftUser     `json:"user,omitempty"`
	Title       string         `json:"title,omitempty"`
	Version     int            `json:"version,omitempty"`
	Anchor      int            `json:"anchor,omitempty"`
	Head        int            `json:"head,omitempty"`
	PageVersion int            `json:"pageVersion,omitempty"`
	Code        string         `json:"code,omitempty"`
}

type draftStepOut struct {
	Step   json.RawMessage `json:"step"`
	Client string          `json:"client"`
}

type draftUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// draftNotification is the payload of a notification on DraftChannel.
type draftNotification struct {
	Page  int64      `json:"p"`
	Event draftEvent `json:"e"`
	// Fetch: the steps were too large for the notification; receivers read them from the database.
	Fetch bool `json:"f,omitempty"`
}

// notifyMax is the largest payload sent with a notification (PostgreSQL allows somewhat less than 8000 bytes).
const notifyMax = 7000

// notifyDraft tells the editors of a draft on all nodes, at the commit of q (or at once outside transactions).
func (s *Service) notifyDraft(ctx context.Context, q db.Querier, pageID int64, e draftEvent) error {
	n := draftNotification{Page: pageID, Event: e}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if len(payload) > notifyMax && e.Type == "steps" {
		n.Event.Steps, n.Fetch = nil, true
		if payload, err = json.Marshal(n); err != nil {
			return err
		}
	}
	_, err = q.Exec(ctx, "SELECT pg_notify($1, $2)", DraftChannel, string(payload))
	return err
}

// draftHub delivers the events of shared drafts to the editors connected to this node.
type draftHub struct {
	log    *slog.Logger
	mu     sync.Mutex
	pages  map[int64]map[*draftSub]struct{}
	seen   map[int64]map[string]caretSeen // where editors are, from all nodes
	fetch  func(pageID int64, after, upTo int) ([]DraftStep, error)
	closed chan struct{}
	once   sync.Once
}

type caretSeen struct {
	event draftEvent
	at    time.Time
}

// caretTTL is how long an editor's caret is shown without news; editors repeat it every half minute.
const caretTTL = 75 * time.Second

// draftSub is an editor connected to this node.
type draftSub struct {
	pageID int64
	client string
	userID int64
	events chan draftEvent
	// overflow is closed when the editor falls behind; it reconnects and catches up.
	overflow chan struct{}
	once     sync.Once
}

func newDraftHub(logger *slog.Logger) *draftHub {
	return &draftHub{log: logger, pages: map[int64]map[*draftSub]struct{}{}, seen: map[int64]map[string]caretSeen{},
		closed: make(chan struct{})}
}

func (h *draftHub) subscribe(pageID int64, client string, userID int64) *draftSub {
	sub := &draftSub{pageID: pageID, client: client, userID: userID, events: make(chan draftEvent, 256),
		overflow: make(chan struct{})}
	h.mu.Lock()
	if h.pages[pageID] == nil {
		h.pages[pageID] = map[*draftSub]struct{}{}
	}
	h.pages[pageID][sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

func (h *draftHub) unsubscribe(sub *draftSub) {
	h.mu.Lock()
	delete(h.pages[sub.pageID], sub)
	if len(h.pages[sub.pageID]) == 0 {
		delete(h.pages, sub.pageID)
	}
	h.mu.Unlock()
}

// carets returns the carets of a page's editors that were heard of recently.
func (h *draftHub) carets(pageID int64) []Caret {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Caret{}
	for client, c := range h.seen[pageID] {
		if time.Since(c.at) > caretTTL {
			delete(h.seen[pageID], client)
			continue
		}
		e := c.event
		out = append(out, Caret{ClientID: client, UserID: e.User.ID, Name: e.User.Name, Version: e.Version, Anchor: e.Anchor, Head: e.Head})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientID < out[j].ClientID })
	return out
}

func (h *draftHub) caretEvents(pageID int64) []draftEvent {
	var out []draftEvent
	for _, c := range h.carets(pageID) {
		out = append(out, draftEvent{Type: "caret", Client: c.ClientID, User: &draftUser{ID: c.UserID, Name: c.Name},
			Version: c.Version, Anchor: c.Anchor, Head: c.Head})
	}
	return out
}

// receive handles a notification (from the listener of this node, in the order of the commits).
func (h *draftHub) receive(payload string) {
	var n draftNotification
	if err := json.Unmarshal([]byte(payload), &n); err != nil {
		return
	}
	e := n.Event
	h.mu.Lock()
	switch e.Type {
	case "caret":
		if e.User != nil {
			if h.seen[n.Page] == nil {
				h.seen[n.Page] = map[string]caretSeen{}
			}
			h.seen[n.Page][e.Client] = caretSeen{event: e, at: time.Now()}
		}
	case "leave":
		delete(h.seen[n.Page], e.Client)
		if len(h.seen[n.Page]) == 0 {
			delete(h.seen, n.Page)
		}
	}
	subs := make([]*draftSub, 0, len(h.pages[n.Page]))
	for sub := range h.pages[n.Page] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	if n.Fetch {
		steps, err := h.fetch(n.Page, e.From, e.To)
		if err != nil {
			// Editors notice the gap with the next steps (or the reconnection) and catch up themselves.
			h.log.Warn("shared drafts: reading steps failed", "page", n.Page, "error", err)
			return
		}
		e.Steps = make([]draftStepOut, len(steps))
		for i, st := range steps {
			e.Steps[i] = draftStepOut{Step: st.Step, Client: st.ClientID}
		}
	}
	for _, sub := range subs {
		if e.Client != "" && e.Client == sub.client && (e.Type == "caret" || e.Type == "leave") {
			continue
		}
		select {
		case sub.events <- e:
		default:
			sub.once.Do(func() { close(sub.overflow) })
		}
	}
}

// resync tells every editor connected to this node to check for missed steps, after the listener was down.
func (h *draftHub) resync() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, subs := range h.pages {
		for sub := range subs {
			select {
			case sub.events <- draftEvent{Type: "check"}:
			default:
				sub.once.Do(func() { close(sub.overflow) })
			}
		}
	}
}

func (h *draftHub) close() { h.once.Do(func() { close(h.closed) }) }

// DraftNotified receives the notifications of shared drafts (registered with the live hub's listener).
func (s *Service) DraftNotified(payload string) { s.hub.receive(payload) }

// DraftsResync is called when notifications may have been missed (the listener reconnected).
func (s *Service) DraftsResync() { s.hub.resync() }

// StopDrafts ends the editors' connections of this node (at shutdown); they reconnect to another node.
func (s *Service) StopDrafts() { s.hub.close() }

// ForgetDraftDocs drops the documents of shared drafts this node holds, which are then read from the database
// again (as on a node that just started).
func (s *Service) ForgetDraftDocs() {
	s.drafts.mu.Lock()
	s.drafts.docs = nil
	s.drafts.mu.Unlock()
}
