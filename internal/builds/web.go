// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/httpx"
)

// WebHandler serves what the JSON API does not: live logs (server-sent events), raw logs, artifact downloads and
// status badges.
type WebHandler struct {
	S *Service
}

// Routes registers the handlers (in the authenticated group).
func (h *WebHandler) Routes(r chi.Router) {
	r.Get("/api/v1/builds/jobs/{id}/log/raw", h.rawLog)
	r.Get("/api/v1/builds/jobs/{id}/log/stream", h.streamLog)
	r.Get("/api/v1/builds/artifacts/{id}", h.artifact)
	r.Get("/api/v1/code/repos/{key}/{slug}/badge.svg", h.badge)
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return id
}

func (h *WebHandler) rawLog(w http.ResponseWriter, r *http.Request) {
	j, err := h.S.jobForRead(r.Context(), identity.From(r.Context()), pathID(r, "id"))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	if j.LogExpired {
		httpx.WriteProblem(w, r, apperr.NotFound("job.log-expired", "The log was deleted after its retention period."))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "job-" + itoa(j.ID) + ".log"}))
	}
	f, err := os.Open(h.S.logPath(j.ID))
	if err != nil {
		w.WriteHeader(http.StatusOK) // no output yet
		return
	}
	defer f.Close()
	// The file may grow while it is sent; the recorded size is what was acknowledged to the agent.
	w.Header().Set("Content-Length", strconv.FormatInt(j.LogSize, 10))
	_, _ = io.CopyN(w, f, j.LogSize)
}

// logStreamLifetime bounds a log stream, like the live updates (platform/live): the browser connects again at the
// offset it has (Last-Event-ID) and is signed in again, so that signing out and lost permissions take effect also
// for the logs of jobs that run for hours.
const logStreamLifetime = 5 * time.Minute

// streamLog sends the log of a job as server-sent events from an offset until the job has finished: "log" events
// with the text and the offset after it (also the event id, so reconnecting browsers continue), then one "end".
func (h *WebHandler) streamLog(w http.ResponseWriter, r *http.Request) {
	s, ctx := h.S, r.Context()
	j, err := s.jobForRead(ctx, identity.From(ctx), pathID(r, "id"))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
		offset = last
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// Spread the reconnections of browsers that connected at the same time.
	end := time.NewTimer(logStreamLifetime + time.Duration(rand.Int64N(int64(time.Minute))))
	defer end.Stop()
	lastWrite := time.Now()
	send := func(event, id string, v any) {
		b, _ := json.Marshal(v)
		if id != "" {
			fmt.Fprintf(w, "id: %s\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
		lastWrite = time.Now()
	}
	for {
		wait := s.logWake.wait(j.ID)
		var size int64
		var state string
		var expired bool
		if err := s.pool.QueryRow(ctx, "SELECT log_size, state, log_expired FROM run_jobs WHERE id = $1", j.ID).Scan(&size, &state, &expired); err != nil {
			return
		}
		if expired {
			send("end", "", map[string]any{"state": state, "expired": true})
			return
		}
		finished := jobFinal(state)
		for offset < size {
			text, err := s.readLog(j.ID, offset, min(size-offset, 256<<10), finished)
			if err != nil || len(text) == 0 {
				break
			}
			offset += int64(len(text))
			send("log", strconv.FormatInt(offset, 10), map[string]any{"text": string(text), "offset": offset})
		}
		if finished {
			send("end", "", map[string]any{"state": state})
			return
		}
		if time.Since(lastWrite) > 20*time.Second {
			fmt.Fprint(w, ": ping\n\n") // keeps proxies from closing a quiet stream
			flusher.Flush()
			lastWrite = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			return
		case <-end.C:
			return
		case <-wait: // the log grew or the job ended, at whichever node (Notified)
		case <-time.After(min(s.LogRecheck, 20*time.Second)):
		}
	}
}

func (h *WebHandler) artifact(w http.ResponseWriter, r *http.Request) {
	sha, name, size, err := h.S.ArtifactFile(r.Context(), identity.From(r.Context()), pathID(r, "id"))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	f, err := h.S.blobs.Open(sha)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.NotFound("artifact.gone", "The file is not available any more."))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	_, _ = io.Copy(w, f)
}
