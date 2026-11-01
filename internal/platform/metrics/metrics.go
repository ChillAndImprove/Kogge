// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package metrics defines Kogge's Prometheus metrics. The server exposes them on
// KOGGE_METRICS_ADDR together with the Go runtime and process metrics.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"kogge.de/kogge/internal/platform/storage"
)

const namespace = "kogge"

var (
	latency = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}
	slow    = []float64{.1, .5, 1, 5, 10, 30, 60, 300, 900, 1800, 3600}

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "http",
		Name: "request_duration_seconds", Help: "Duration of HTTP requests by route and status class (event streams excluded).",
		Buckets: latency}, []string{"method", "route", "code"})
	gitDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "git",
		Name: "operation_duration_seconds", Help: "Duration of Git operations (upload-pack: clones and fetches, receive-pack: pushes).",
		Buckets: slow}, []string{"service", "transport", "result"})
	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "jobs",
		Name: "duration_seconds", Help: "Duration of background jobs by kind.", Buckets: latency}, []string{"kind", "queue", "result"})
	jobWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "jobs",
		Name: "queue_wait_seconds", Help: "Time background jobs waited for a worker after they were due.", Buckets: latency},
		[]string{"queue"})
	searchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "search",
		Name: "duration_seconds", Help: "Duration of searches by kind.", Buckets: latency}, []string{"kind"})
	mails = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "mail",
		Name: "sent_total", Help: "E-mails handed to the mail relay, by result."}, []string{"result"})
	webhooks = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "webhooks",
		Name: "deliveries_total", Help: "Webhook delivery attempts by result."}, []string{"result"})
	buildWait = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "build",
		Name: "queue_wait_seconds", Help: "Time build jobs waited for an agent.", Buckets: slow})
)

func init() {
	// Results that are always reported, so that rates exist before the first failure.
	for _, r := range []string{"ok", "error"} {
		mails.WithLabelValues(r)
		webhooks.WithLabelValues(r)
	}
}

func result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// HTTP is middleware of the router that records the duration of requests by route pattern.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		// Streams and WebSockets last as long as a page is open: they are no requests to time.
		if strings.HasPrefix(ww.Header().Get("Content-Type"), "text/event-stream") || r.Header.Get("Upgrade") != "" {
			return
		}
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		httpDuration.WithLabelValues(r.Method, route, strconv.Itoa(status/100)+"xx").Observe(time.Since(start).Seconds())
	})
}

// Git records a Git operation that started at start. transport is http or ssh.
func Git(service, transport string, start time.Time, err error) {
	gitDuration.WithLabelValues(service, transport, result(err)).Observe(time.Since(start).Seconds())
}

// Search records a search (issues, wiki) that started at start.
func Search(kind string, start time.Time) {
	searchDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
}

// Mail records an e-mail handed to the relay.
func Mail(err error) { mails.WithLabelValues(result(err)).Inc() }

// Webhook records a delivery attempt.
func Webhook(ok bool) {
	r := "ok"
	if !ok {
		r = "error"
	}
	webhooks.WithLabelValues(r).Inc()
}

// BuildWait records how long a build job waited for an agent.
func BuildWait(d time.Duration) { buildWait.Observe(d.Seconds()) }

// Jobs is middleware of the job queue that records durations and queue waits of background jobs.
func Jobs() rivertype.Middleware {
	return river.WorkerMiddlewareFunc(func(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
		start := time.Now()
		if job.AttemptedAt != nil && job.AttemptedAt.After(job.ScheduledAt) {
			jobWait.WithLabelValues(job.Queue).Observe(job.AttemptedAt.Sub(job.ScheduledAt).Seconds())
		}
		err := doInner(ctx)
		jobDuration.WithLabelValues(job.Kind, job.Queue, result(err)).Observe(time.Since(start).Seconds())
		return err
	})
}

// Handler serves the metrics of this process and the state of the installation read from the database; forwarding
// tells whether audit events are forwarded to a SIEM, and disk measures the file system of the data directory.
func Handler(pool *pgxpool.Pool, version string, forwarding bool, disk func() storage.Usage) http.Handler {
	reg := prometheus.NewRegistry()
	info := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Name: "build_info", Help: "Kogge's version.",
		ConstLabels: prometheus.Labels{"version": version}})
	info.Set(1)
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), info,
		httpDuration, gitDuration, jobDuration, jobWait, searchDuration, mails, webhooks, buildWait, &state{pool: pool,
			forwarding: forwarding}, dataDir{disk})
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

var (
	dataFreeDesc    = prometheus.NewDesc(namespace+"_data_free_bytes", "Bytes available to Kogge on the file system of the data directory.", nil, nil)
	dataSizeDesc    = prometheus.NewDesc(namespace+"_data_size_bytes", "Size of the file system of the data directory.", nil, nil)
	dataReserveDesc = prometheus.NewDesc(namespace+"_data_min_free_bytes",
		"Free space the settings keep on the file system of the data directory: below it, pushes and uploads are refused.", nil, nil)
)

// dataDir reports the space of the data directory of this node, measured at every scrape (a system call).
type dataDir struct{ usage func() storage.Usage }

func (d dataDir) Describe(ch chan<- *prometheus.Desc) {
	ch <- dataFreeDesc
	ch <- dataSizeDesc
	ch <- dataReserveDesc
}

func (d dataDir) Collect(ch chan<- prometheus.Metric) {
	u := d.usage()
	if u.Total == 0 {
		return // not measurable here
	}
	ch <- prometheus.MustNewConstMetric(dataFreeDesc, prometheus.GaugeValue, float64(u.Free))
	ch <- prometheus.MustNewConstMetric(dataSizeDesc, prometheus.GaugeValue, float64(u.Total))
	ch <- prometheus.MustNewConstMetric(dataReserveDesc, prometheus.GaugeValue, float64(u.Reserve))
}

// state reports gauges read from the database: queued jobs, agents, pending e-mails, the audit forwarding. Results
// are kept for a few seconds, so that several scrapers do not multiply the queries.
type state struct {
	pool       *pgxpool.Pool
	forwarding bool
	mu         sync.Mutex
	at         time.Time
	last       []prometheus.Metric
}

var (
	dbUpDesc        = prometheus.NewDesc(namespace+"_database_up", "Whether the database answered the metrics queries.", nil, nil)
	jobsDesc        = prometheus.NewDesc(namespace+"_jobs", "Background jobs that are due, scheduled, retrying or running.", []string{"queue", "state"}, nil)
	agentsDesc      = prometheus.NewDesc(namespace+"_build_agents", "Build agents: online (seen in the last minute), offline or disabled.", []string{"state"}, nil)
	pendingDesc     = prometheus.NewDesc(namespace+"_build_jobs_pending", "Build jobs waiting for an agent.", nil, nil)
	oldestDesc      = prometheus.NewDesc(namespace+"_build_oldest_pending_seconds", "How long the oldest pending build job has waited.", nil, nil)
	mailPendingDesc = prometheus.NewDesc(namespace+"_mail_pending", "Notifications waiting to be sent by e-mail.", nil, nil)
	auditWaitDesc   = prometheus.NewDesc(namespace+"_audit_forward_waiting", "Audit events not forwarded to the SIEM yet (counted up to a million).", nil, nil)
	auditOldestDesc = prometheus.NewDesc(namespace+"_audit_forward_oldest_waiting_seconds", "Age of the oldest audit event not forwarded yet.", nil, nil)
	auditFailDesc   = prometheus.NewDesc(namespace+"_audit_forward_failing", "Whether deliveries to the SIEM fail (1) or not (0).", nil, nil)
)

func (s *state) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{dbUpDesc, jobsDesc, agentsDesc, pendingDesc, oldestDesc, mailPendingDesc,
		auditWaitDesc, auditOldestDesc, auditFailDesc} {
		ch <- d
	}
}

func (s *state) Collect(ch chan<- prometheus.Metric) {
	s.mu.Lock()
	if time.Since(s.at) > 5*time.Second {
		s.last, s.at = s.read(), time.Now()
	}
	list := s.last
	s.mu.Unlock()
	for _, m := range list {
		ch <- m
	}
}

func (s *state) read() []prometheus.Metric {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []prometheus.Metric
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...))
	}
	down := func() []prometheus.Metric {
		return []prometheus.Metric{prometheus.MustNewConstMetric(dbUpDesc, prometheus.GaugeValue, 0)}
	}

	rows, err := s.pool.Query(ctx, `SELECT queue, state::text, count(*) FROM river_job
		WHERE state IN ('available', 'scheduled', 'retryable', 'running') GROUP BY 1, 2`)
	if err != nil {
		return down()
	}
	for rows.Next() {
		var queue, st string
		var n int64
		if err := rows.Scan(&queue, &st, &n); err != nil {
			rows.Close()
			return down()
		}
		gauge(jobsDesc, float64(n), queue, st)
	}
	rows.Close()
	if rows.Err() != nil {
		return down()
	}
	var online, offline, disabled, pending, mail int64
	var oldest float64
	err = s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE enabled AND last_seen_at > now() - interval '1 minute'),
		(SELECT count(*) FROM agents WHERE enabled AND (last_seen_at IS NULL OR last_seen_at <= now() - interval '1 minute')),
		(SELECT count(*) FROM agents WHERE NOT enabled),
		(SELECT count(*) FROM run_jobs WHERE state = 'pending'),
		(SELECT coalesce(extract(epoch FROM now() - min(queued_at)), 0)::float8 FROM run_jobs WHERE state = 'pending'),
		(SELECT count(*) FROM notifications WHERE mail_state = 'pending')`).Scan(&online, &offline, &disabled, &pending, &oldest, &mail)
	if err != nil {
		return down()
	}
	gauge(agentsDesc, float64(online), "online")
	gauge(agentsDesc, float64(offline), "offline")
	gauge(agentsDesc, float64(disabled), "disabled")
	gauge(pendingDesc, float64(pending))
	gauge(oldestDesc, oldest)
	gauge(mailPendingDesc, float64(mail))
	if s.forwarding {
		var waiting int64
		var age float64
		var failing bool
		err := s.pool.QueryRow(ctx, `SELECT count(w.at), coalesce(extract(epoch FROM now() - min(w.at)), 0)::float8,
				coalesce((SELECT failing_since IS NOT NULL FROM audit_forwarding WHERE id = 1), false)
			FROM (SELECT e.at FROM audit_forwarding f, audit_events e WHERE f.id = 1 AND (e.xact, e.id) > (f.xact, f.event_id)
				ORDER BY e.xact, e.id LIMIT 1000000) w`).Scan(&waiting, &age, &failing)
		if err != nil {
			return down()
		}
		gauge(auditWaitDesc, float64(waiting))
		gauge(auditOldestDesc, age)
		gauge(auditFailDesc, map[bool]float64{true: 1}[failing])
	}
	gauge(dbUpDesc, 1)
	return out
}
