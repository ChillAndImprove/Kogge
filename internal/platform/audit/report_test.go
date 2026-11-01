// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/httpx"
)

// failingDB refuses every statement, like a database that cannot take the event.
type failingDB struct{ ctxErr error }

func (f *failingDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	f.ctxErr = ctx.Err()
	return pgconn.CommandTag{}, errors.New("disk full")
}
func (*failingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("disk full")
}
func (*failingDB) QueryRow(context.Context, string, ...any) pgx.Row       { return nil }
func (*failingDB) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }

// TestReportLogsWhatItCannotWrite: an event that must not stop what it reports is written even when the request
// was canceled, and one that cannot be written is logged at error level with its contents — never lost silently.
func TestReportLogsWhatItCannotWrite(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(httpx.WithClientIP(context.Background(), netip.MustParseAddr("192.0.2.9")))
	cancel() // the client went away
	q := &failingDB{}
	audit.Report(ctx, q, logger, audit.Entry{ActorID: 7, Action: "auth.login.failed", Details: map[string]any{"username": "lena"}})
	if q.ctxErr != nil {
		t.Errorf("the event was written with the canceled context of the request: %v", q.ctxErr)
	}
	line := logs.String()
	for _, want := range []string{"level=ERROR", "action=auth.login.failed", "actor=7", "username:lena", "client=192.0.2.9", "disk full"} {
		if !strings.Contains(line, want) {
			t.Errorf("the log does not say %q: %s", want, line)
		}
	}
}
