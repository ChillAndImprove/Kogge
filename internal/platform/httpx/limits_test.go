// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/apperr"
)

// TestSlowBodyError: a read that missed its deadline — HTTP/1.1 reports a net.OpError, HTTP/2 a wrapped
// os.ErrDeadlineExceeded — is a timeout to the API's request handling and answers request.body-timeout (408).
func TestSlowBodyError(t *testing.T) {
	for _, err := range []error{&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, fmt.Errorf("%w", os.ErrDeadlineExceeded)} {
		got := readDone(http.NewResponseController(httptest.NewRecorder()), err)
		var ne net.Error
		if !errors.As(got, &ne) || !ne.Timeout() {
			t.Errorf("%v: not a timeout: %v", err, got)
		}
		if p := apperr.From(got); p.Status != http.StatusRequestTimeout || p.Code != "request.body-timeout" {
			t.Errorf("%v: answered %d %s", err, p.Status, p.Code)
		}
	}
	if got := readDone(http.NewResponseController(httptest.NewRecorder()), io.EOF); got != io.EOF {
		t.Errorf("the end of the body: %v", got)
	}
}

// TestLimitsServe: the limits give the handler a context with the work's deadline, and a writer that flushes for
// transfers but does not hand out the connection.
func TestLimitsServe(t *testing.T) {
	var deadline time.Time
	var flusher, hijacker bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
		_, flusher = w.(http.Flusher)
		_, hijacker = w.(http.Hijacker)
		_, _ = io.ReadAll(r.Body)
	})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	Limits{Body: time.Second, Work: time.Minute, Respond: 2 * time.Minute}.Serve(httptest.NewRecorder(), r, h)
	if until := time.Until(deadline); until < 50*time.Second || until > time.Minute {
		t.Errorf("work deadline in %v", until)
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background())
	Limits{Stall: time.Minute}.Serve(httptest.NewRecorder(), r, h)
	if !deadline.IsZero() || !flusher || hijacker {
		t.Errorf("transfer: deadline %v, flusher %v, hijacker %v", deadline, flusher, hijacker)
	}
}
