// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"kogge.de/kogge/internal/platform/apperr"
)

// Limits are the time limits of a kind of request (against denial of service): a client that sends its
// request slowly, or reads the answer slowly, or a request whose work does not end must not hold a connection, a
// goroutine and database work for ever. They are set per request with http.ResponseController, for HTTP/1.1 and
// HTTP/2 alike, and the zero value sets none.
type Limits struct {
	// Body is how long, from the start of the request, the client may take to send the request body.
	Body time.Duration
	// Respond is how long, from the start of the request, the answer may take until it is written.
	Respond time.Duration
	// Work is how long the handler may work: then the request's context ends, which stops its database queries and
	// commands, and the request is answered with request.timeout (504).
	Work time.Duration
	// Stall replaces Body and Respond for long transfers (uploads, downloads, Git, event streams): each read of the
	// body and each write of the answer must make progress within it, however long the whole takes.
	Stall time.Duration
}

// errSlowBody is the error of reading a request body that does not arrive in time. It is a timeout (net.Error),
// as the API's request handling expects, and unwraps to the problem to answer with.
type errSlowBody struct{}

var slowBodyProblem = apperr.New(http.StatusRequestTimeout, "request.body-timeout", "The request body arrived too slowly.")

func (errSlowBody) Error() string   { return "the request body arrived too slowly" }
func (errSlowBody) Timeout() bool   { return true }
func (errSlowBody) Temporary() bool { return true }
func (errSlowBody) Unwrap() error   { return slowBodyProblem }

// Serve handles the request with the limits.
func (l Limits) Serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	rc := http.NewResponseController(w)
	start := time.Now()
	body := r.Body != nil && r.Body != http.NoBody
	switch {
	case l.Stall > 0:
		sw := &stallWriter{ResponseWriter: w, rc: rc, stall: l.Stall}
		sw.extend(start)
		w = sw
		if body {
			b := &stallBody{ReadCloser: r.Body, rc: rc, stall: l.Stall}
			b.extend(start)
			r.Body = b
		}
	default:
		if l.Body > 0 && body {
			_ = rc.SetReadDeadline(start.Add(l.Body))
			r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
		}
		// Always set, also to none: with HTTP/1.1, a deadline of the previous request on the connection would
		// stay otherwise.
		var respond time.Time
		if l.Respond > 0 {
			respond = start.Add(l.Respond)
		}
		_ = rc.SetWriteDeadline(respond)
	}
	if l.Work > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), l.Work)
		defer cancel()
		r = r.WithContext(ctx)
	}
	next.ServeHTTP(w, r)
}

// deadlineBody is a request body under a read deadline. At its end the deadline is lifted: with HTTP/1.1, the
// server then reads on to notice a client that goes away, and a deadline that passes there would end the context
// of a handler still at work.
type deadlineBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	return n, readDone(b.rc, err)
}

// readDone lifts the read deadline at the end of the body and turns a read that missed its deadline into
// errSlowBody.
func readDone(rc *http.ResponseController, err error) error {
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		_ = rc.SetReadDeadline(time.Time{})
	case errors.Is(err, os.ErrDeadlineExceeded):
		return errSlowBody{}
	}
	return err
}

// The deadlines of stalling transfers move with their progress. Moving one costs a call into the server (for
// HTTP/2, a message to the connection's goroutine), so it moves only once a quarter of the time has passed: each
// read and write gets at least three quarters of it.

// stallBody is a request body whose reads must make progress within stall.
type stallBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	stall time.Duration
	moved time.Time
}

func (b *stallBody) extend(now time.Time) {
	_ = b.rc.SetReadDeadline(now.Add(b.stall))
	b.moved = now
}

func (b *stallBody) Read(p []byte) (int, error) {
	if now := time.Now(); now.Sub(b.moved) > b.stall/4 {
		b.extend(now)
	}
	n, err := b.ReadCloser.Read(p)
	return n, readDone(b.rc, err)
}

// stallWriter is the writer of an answer whose writes must make progress within stall. It flushes like the
// writer it wraps (event streams and Git need that), but does not hand out the connection: transfers that take it
// over (WebSockets) manage their own deadlines.
type stallWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
	moved time.Time
}

func (w *stallWriter) extend(now time.Time) {
	_ = w.rc.SetWriteDeadline(now.Add(w.stall))
	w.moved = now
}

func (w *stallWriter) progress() {
	if now := time.Now(); now.Sub(w.moved) > w.stall/4 {
		w.extend(now)
	}
}

func (w *stallWriter) Write(p []byte) (int, error) {
	w.progress()
	return w.ResponseWriter.Write(p)
}

// Flush implements http.Flusher.
func (w *stallWriter) Flush() {
	w.progress()
	_ = w.rc.Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
