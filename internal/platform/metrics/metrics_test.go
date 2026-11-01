// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/pgtest"
)

func scrape(pool *pgxpool.Pool, forwarding bool) string {
	rec := httptest.NewRecorder()
	disk := func() storage.Usage { return storage.Usage{Free: 5 << 30, Total: 100 << 30, Reserve: 2 << 30} }
	metrics.Handler(pool, "1.2.3", forwarding, disk).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestMetrics(t *testing.T) {
	pool := pgtest.Pool(t)
	r := chi.NewRouter()
	r.Use(metrics.HTTP)
	r.Get("/api/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r.Get("/live", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "text/event-stream") })
	for _, path := range []string{"/api/v1/things/1", "/api/v1/things/2", "/live", "/nowhere"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	metrics.Git("upload-pack", "ssh", time.Now().Add(-time.Second), nil)
	metrics.Search("issues", time.Now())

	text := scrape(pool, false)
	for _, want := range []string{
		`kogge_build_info{version="1.2.3"} 1`,
		`kogge_http_request_duration_seconds_count{code="4xx",method="GET",route="/api/v1/things/{id}"} 2`,
		`kogge_http_request_duration_seconds_count{code="4xx",method="GET",route="unmatched"} 1`,
		`kogge_git_operation_duration_seconds_count{result="ok",service="upload-pack",transport="ssh"} 1`,
		`kogge_search_duration_seconds_count{kind="issues"} 1`,
		`kogge_mail_sent_total{result="error"} 0`,
		`kogge_database_up 1`,
		`kogge_build_agents{state="online"} 0`,
		`kogge_build_jobs_pending 0`,
		`kogge_mail_pending 0`,
		`kogge_data_free_bytes 5.36870912e+09`,
		`kogge_data_size_bytes 1.073741824e+11`,
		`kogge_data_min_free_bytes 2.147483648e+09`,
		`go_goroutines`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(text, `route="/live"`) {
		t.Error("event streams are measured")
	}
	if strings.Contains(text, "kogge_audit_forward") {
		t.Error("the audit forwarding is reported without being configured")
	}
}

// TestAuditForwardMetrics: with forwarding configured, the metrics tell how many events wait and whether deliveries
// fail.
func TestAuditForwardMetrics(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()
	text := scrape(pool, true)
	for _, want := range []string{"kogge_audit_forward_waiting 0", "kogge_audit_forward_oldest_waiting_seconds 0",
		"kogge_audit_forward_failing 0", "kogge_database_up 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("before forwarding started: missing %s", want)
		}
	}
	// The rewind is itself an event, and it comes after the position.
	if err := audit.RewindForwarding(ctx, pool, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE audit_forwarding SET failing_since = now() WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	text = scrape(pool, true)
	for _, want := range []string{"kogge_audit_forward_waiting 1", "kogge_audit_forward_failing 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
}
