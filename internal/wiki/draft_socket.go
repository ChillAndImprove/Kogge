// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/websocket"
	"golang.org/x/time/rate"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/httpx"
)

// The connection of an editor to a shared draft: a WebSocket that carries the editor's steps, carets and title to
// the server and the others' to the editor. Messages are JSON objects with a type "t":
//
//	editor → server: {"t":"steps","id":1,"version":12,"steps":[…]}, answered by {"t":"ack","id":1,"version":14} or
//	                 {"t":"nack","id":1,"code":"draft.behind","message":"…"};
//	                 {"t":"caret","version":14,"anchor":3,"head":9}; {"t":"title","id":2,"title":"…"};
//	                 {"t":"ping"}, answered by {"t":"pong"}
//	server → editor: {"t":"welcome","version":14}, then the steps after the editor's version and the events of
//	                 draft_hub.go; {"t":"reset"} (load the draft again); {"t":"bye","code":"…"} before closing.
//
// The editor's own steps come back with the others' (as ProseMirror's collaborative editing expects); steps that
// build on an older version are refused (nack draft.behind): the editor waits for the steps it misses, rebases its
// own and sends them again.

type socketIn struct {
	Type    string           `json:"t"`
	ID      int64            `json:"id"`
	Version int              `json:"version"`
	Steps   []map[string]any `json:"steps"`
	Anchor  int              `json:"anchor"`
	Head    int              `json:"head"`
	Title   string           `json:"title"`
}

type socketOut struct {
	Type    string `json:"t"`
	ID      int64  `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

const (
	socketPing      = 25 * time.Second
	socketReadLimit = 90 * time.Second // editors send a ping every 30 seconds
	socketRecheck   = time.Minute      // the sign-in and the permissions are checked again this often (Service.SocketRecheck)
)

func (h *Handler) draftSocket(w http.ResponseWriter, r *http.Request) {
	p := identity.From(r.Context())
	if !p.Authenticated() {
		httpx.WriteProblem(w, r, apperr.Unauthorized("auth.required", "Please sign in."))
		return
	}
	// The socket writes the draft, but its handshake is a GET, for which an access token needs only the read scope.
	if !p.AllowsScope(identity.ScopeWrite) {
		httpx.WriteProblem(w, r, apperr.Forbidden("auth.token-scope", "The access token lacks the %s scope.", identity.ScopeWrite))
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, notFoundPage())
		return
	}
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || version < 0 {
		httpx.WriteProblem(w, r, apperr.BadRequest("draft.version", "Give the draft version the editor has."))
		return
	}
	client := r.URL.Query().Get("client")
	lang := apperr.Language(r.Header.Get("Accept-Language"))
	if err := h.S.checkDraftEditor(r.Context(), p, pageID, client); err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	websocket.Server{
		// The identity middleware lets only pages of Kogge's own origin open a WebSocket.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			h.S.serveDraftSocket(r.Context(), ws, p, pageID, client, version, lang)
		},
	}.ServeHTTP(w, r)
}

// checkDraftEditor checks that a principal may edit a page as the editor client.
func (s *Service) checkDraftEditor(ctx context.Context, p *identity.Principal, pageID int64, client string) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.editablePage(ctx, s.pool, a, pageID, false); err != nil {
		return err
	}
	return checkClient(client, a.userID)
}

func (s *Service) draftVersion(ctx context.Context, pageID int64) (int, error) {
	var v int
	err := s.pool.QueryRow(ctx, "SELECT version FROM page_shared_drafts WHERE page_id = $1", pageID).Scan(&v)
	return v, err
}

func (s *Service) serveDraftSocket(ctx context.Context, ws *websocket.Conn, p *identity.Principal, pageID int64, client string,
	version int, lang string) {
	ws.MaxPayloadBytes = 2 << 20
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub := s.hub.subscribe(pageID, client, p.UserID())
	defer func() {
		s.hub.unsubscribe(sub)
		if err := s.notifyDraft(context.WithoutCancel(ctx), s.pool, pageID, draftEvent{Type: "leave", Client: client}); err != nil {
			s.log.Debug("shared drafts: announcing a leaving editor failed", "error", err)
		}
	}()
	out := make(chan any, 64)
	done := make(chan struct{})
	// Reader and writer both end the connection; it is closed once, or both would send a close frame, which
	// browsers report as an error ("Close received after close").
	var closing sync.Once
	closeSocket := func() { closing.Do(func() { _ = ws.Close() }) }
	go func() {
		defer close(done)
		s.socketWriter(ctx, ws, p, sub, version, out)
		cancel()
		closeSocket()
	}()
	s.socketReader(ctx, ws, p, pageID, client, lang, out)
	cancel()
	closeSocket()
	<-done
}

// socketReader handles the editor's messages until the connection ends.
func (s *Service) socketReader(ctx context.Context, ws *websocket.Conn, p *identity.Principal, pageID int64, client, lang string,
	out chan<- any) {
	carets := rate.NewLimiter(20, 40)
	reply := func(id int64, version int, err error) {
		m := socketOut{Type: "ack", ID: id, Version: version}
		if err != nil {
			e := apperr.From(err).Localize(lang)
			if e.Status >= 500 {
				slog.Default().Error("shared drafts: request failed", "error", err, "page", pageID)
			}
			m = socketOut{Type: "nack", ID: id, Code: e.Code, Message: e.Detail}
		}
		select {
		case out <- m:
		case <-ctx.Done():
		}
	}
	for {
		if err := ws.SetReadDeadline(time.Now().Add(socketReadLimit)); err != nil {
			return
		}
		var m socketIn
		if err := websocket.JSON.Receive(ws, &m); err != nil {
			return
		}
		switch m.Type {
		case "steps":
			if len(m.Steps) == 0 || len(m.Steps) > 500 {
				reply(m.ID, 0, apperr.Invalid("draft.steps", "Send between 1 and 500 steps at a time."))
				continue
			}
			res, err := s.SubmitSteps(ctx, p, pageID, StepsInput{Version: m.Version, ClientID: client, Steps: m.Steps})
			v := 0
			if res != nil {
				v = res.Version
			}
			reply(m.ID, v, err)
		case "caret":
			if carets.Allow() && m.Version >= 0 && m.Anchor >= 0 && m.Head >= 0 {
				if err := s.sendCaret(ctx, p, pageID, CaretInput{ClientID: client, Version: m.Version, Anchor: m.Anchor, Head: m.Head}); err != nil {
					s.log.Warn("shared drafts: sending a caret failed", "error", err)
				}
			}
		case "title":
			reply(m.ID, 0, s.SetDraftTitle(ctx, p, pageID, DraftTitleInput{Title: m.Title, ClientID: client}))
		case "ping":
			select {
			case out <- socketOut{Type: "pong"}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// socketWriter sends the editor what happens to the draft, from its version on.
func (s *Service) socketWriter(ctx context.Context, ws *websocket.Conn, p *identity.Principal, sub *draftSub, sent int, out <-chan any) {
	send := func(v any) bool {
		if err := ws.SetWriteDeadline(time.Now().Add(20 * time.Second)); err != nil {
			return false
		}
		return websocket.JSON.Send(ws, v) == nil
	}
	bye := func(code string) { send(draftEvent{Type: "bye", Code: code}) }
	// catchUp sends the steps after the editor's version up to a version.
	catchUp := func(upTo int) bool {
		if upTo <= sent {
			return true
		}
		steps, err := s.stepsBetween(ctx, s.pool, sub.pageID, sent, upTo)
		if errors.Is(err, errStepsGone) {
			send(draftEvent{Type: "reset"})
			return false
		}
		if err != nil {
			bye("error")
			return false
		}
		e := draftEvent{Type: "steps", From: sent, To: upTo, Steps: make([]draftStepOut, len(steps))}
		for i, st := range steps {
			e.Steps[i] = draftStepOut{Step: st.Step, Client: st.ClientID}
		}
		sent = upTo
		return send(e)
	}
	current, err := s.draftVersion(ctx, sub.pageID)
	if err != nil {
		bye("error")
		return
	}
	if sent > current {
		send(draftEvent{Type: "reset"})
		return
	}
	if !send(socketOut{Type: "welcome", Version: current}) || !catchUp(current) {
		return
	}
	for _, e := range s.hub.caretEvents(sub.pageID) {
		if e.Client != sub.client && !send(e) {
			return
		}
	}
	ping := time.NewTicker(socketPing)
	defer ping.Stop()
	recheck := time.NewTicker(s.SocketRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.hub.closed:
			bye("restart")
			return
		case <-sub.overflow:
			bye("reconnect")
			return
		case m := <-out:
			if !send(m) {
				return
			}
		case <-ping.C:
			if !send(socketOut{Type: "ping"}) {
				return
			}
		case <-recheck.C:
			// The editor's session may have ended in the meantime (signing out, a new password, an administrator) or
			// the user been disabled: the socket must not outlive them.
			if ok, err := identity.SignedIn(ctx, s.pool, p); err != nil || !ok {
				bye("denied")
				return
			}
			if err := s.checkDraftEditor(ctx, p, sub.pageID, sub.client); err != nil {
				bye("denied")
				return
			}
		case e := <-sub.events:
			switch e.Type {
			case "steps":
				if e.To <= sent {
					continue
				}
				if e.From > sent && !catchUp(e.From) {
					return
				}
				e.Steps = e.Steps[sent-e.From:]
				e.From = sent
				sent = e.To
				if !send(e) {
					return
				}
			case "check":
				v, err := s.draftVersion(ctx, sub.pageID)
				if err != nil || !catchUp(v) {
					return
				}
			default:
				if !send(e) {
					return
				}
			}
		}
	}
}

func validTitle(title string) error {
	if utf8.RuneCountInString(title) > 255 {
		return apperr.Invalid("page.title-length", "The title may have at most 255 characters.")
	}
	return nil
}
