// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestCanceledRequests: a request canceled because its client went away is no failure of the server (no 500 in
// the logs and metrics); other errors are.
func TestCanceledRequests(t *testing.T) {
	for _, err := range []error{context.Canceled, fmt.Errorf("load the board: %w", context.Canceled)} {
		if e := From(err); e.Status != StatusClientClosedRequest || e.Code != "request.canceled" {
			t.Errorf("From(%v) = %d %s", err, e.Status, e.Code)
		}
	}
	if e := From(errors.New("disk full")); e.Status != http.StatusInternalServerError {
		t.Errorf("another error: %d", e.Status)
	}
	if e := From(Conflict("draft.behind", "Behind.")); e.Status != http.StatusConflict {
		t.Errorf("an Error: %d", e.Status)
	}
}

// TestTimedOutRequests: a request that its time limit stopped answers 504 request.timeout — also when the code
// in between wrapped the error or made it an internal error — in German too; it is no internal error (500) and
// no client going away (499).
func TestTimedOutRequests(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("search: %w", context.DeadlineExceeded),
		Internal(fmt.Errorf("query: %w", context.DeadlineExceeded))} {
		e := From(err)
		if e.Status != http.StatusGatewayTimeout || e.Code != "request.timeout" {
			t.Errorf("From(%v) = %d %s", err, e.Status, e.Code)
		}
		if de := e.Localize("de"); !strings.HasPrefix(de.Detail, "Die Anfrage") {
			t.Errorf("German: %q", de.Detail)
		}
	}
	if e := Internal(nil); e.Status != http.StatusInternalServerError {
		t.Errorf("an internal error without cause: %d", e.Status)
	}
}
