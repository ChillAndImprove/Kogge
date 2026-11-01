// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package app is the composition root: it builds the stores, services and servers from the configuration and
// runs them according to the process's roles.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/account"
	"kogge.de/kogge/internal/admin"
	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/links"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/httpx"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/live"
	"kogge.de/kogge/internal/platform/mail"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/version"
	"kogge.de/kogge/internal/webhooks"
	"kogge.de/kogge/internal/webui"
	"kogge.de/kogge/internal/wiki"
)

// App holds the services of a Kogge process.
type App struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	gate     *db.Gate // Git writes (pushes, merges, ...) wait for backups
	agents   *builds.Downloads
	Keyring  *crypto.Keyring
	Blobs    *blob.Store
	Settings *settings.Service
	Authz    *authz.Service
	Identity *identity.Service
	Registry *jobs.Registry
	Jobs     *jobs.Client
	Bus      *events.Bus
	Live     *live.Hub
	Issues   *issues.Service
	Wiki     *wiki.Service
	Code     *code.Service
	Builds   *builds.Service
	Mail     *mail.Sender   // nil without SMTP configuration
	Mailer   *notify.Mailer // nil without SMTP configuration
	Webhooks *webhooks.Service
	Links    *links.Service
	Started  time.Time
	// Forwarder sends audit events to a SIEM; nil unless forwarding is configured and the process is a worker.
	Forwarder *audit.Forwarder

	forwarding chan struct{} // closed when the forwarder stopped
	handler    http.Handler
	router     chi.Routes
	api        huma.API
}

// Options adjust construction (tests use cheaper password hashing).
type Options struct {
	Argon2 *crypto.Argon2Params
}

// New builds the application. The database must be migrated unless the configuration allows auto-migration.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts Options) (*App, error) {
	a := &App{Cfg: cfg, Log: logger, Started: time.Now()}
	pool, err := db.Open(ctx, cfg.DatabaseURL, "kogge", 0)
	if err != nil {
		return nil, err
	}
	a.Pool = pool
	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, pool, logger); err != nil {
			pool.Close()
			return nil, err
		}
	} else if pending, err := db.PendingMigrations(ctx, pool); err != nil || pending {
		pool.Close()
		if err == nil {
			err = errors.New("the database schema is not up to date: run `kogge migrate` first")
		}
		return nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		pool.Close()
		return nil, fmt.Errorf("data directory: %w", err)
	}
	kr, created, err := crypto.LoadOrCreateKeyring(cfg.MasterKeyFile, cfg.DataDir)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if created {
		logger.Warn("created a master key in the data directory; keep a copy outside of it and set KOGGE_MASTER_KEY_FILE")
	}
	a.Keyring = kr
	if a.Blobs, err = blob.New(cfg.DataDir, pool); err != nil {
		pool.Close()
		return nil, err
	}

	params := crypto.DefaultArgon2
	if opts.Argon2 != nil {
		params = *opts.Argon2
	}
	a.Settings = settings.New(pool)
	a.Authz = authz.NewService(pool)
	a.Identity = identity.NewService(pool, cfg, crypto.NewPasswordHasher(params, 0),
		identity.NewOIDC(cfg.OIDC, cfg.PublicURL, pool), logger)
	a.Identity.SetKeyring(kr)
	a.Live = live.NewHub(pool, logger)
	a.Live.OnInvalidate("authz", a.Authz.Invalidate)
	a.Live.OnInvalidate("settings", a.Settings.Invalidate)
	a.Bus = events.NewBus()
	a.Issues = issues.NewService(pool, a.Authz, a.Settings, a.Bus, a.Blobs, logger)
	a.Live.OnInvalidate("issues-config", a.Issues.DropConfig)
	a.Live.RegisterVisibility("project", a.Issues.IssueVisibility)
	a.Live.RegisterVisibility("issue", a.Issues.IssueVisibility)
	if err := a.initLocale(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("language of the installation: %w", err)
	}
	if err := a.Issues.EnsureDefaults(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("issue configuration: %w", err)
	}
	a.Wiki = wiki.NewService(pool, a.Authz, a.Settings, a.Bus, a.Blobs, logger)
	a.Live.RegisterVisibility("space", a.Wiki.SpaceVisibility)
	a.Live.RegisterVisibility("page", a.Wiki.PageVisibility)
	if cfg.HasRole(config.RoleWeb) {
		// Shared drafts: steps and carets reach the editors connected to every node.
		a.Live.Listen(wiki.DraftChannel, a.Wiki.DraftNotified)
		a.Live.OnInvalidate("drafts", a.Wiki.DraftsResync)
	}
	if err := a.Wiki.EnsureDefaults(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("wiki templates: %w", err)
	}

	if a.gate, err = db.NewGate(pool, 16); err != nil {
		pool.Close()
		return nil, err
	}
	a.agents = &builds.Downloads{Dir: cfg.AgentDir}
	codeOpts := code.Options{DataDir: cfg.DataDir, PublicURL: cfg.PublicURL, Keyring: kr, Gate: a.gate, AllowLoopback: cfg.WebhookLoopback}
	if cfg.SSHAddr != "" {
		codeOpts.SSHPort = cfg.SSHPublicPort
	}
	if a.Code, err = code.NewService(pool, a.Authz, a.Identity, a.Settings, a.Bus, a.Blobs, codeOpts, logger); err != nil {
		pool.Close()
		return nil, fmt.Errorf("code: %w", err)
	}
	a.Live.RegisterVisibility("repo", a.Code.RepoVisibility)
	a.Builds = builds.NewService(pool, a.Authz, a.Identity, a.Settings, a.Bus, a.Blobs,
		builds.Options{DataDir: cfg.DataDir, PublicURL: cfg.PublicURL, Keyring: kr}, logger)
	if cfg.HasRole(config.RoleWeb) {
		// Agents wait for work and browsers follow logs at any web node, whichever node made the work or got the log.
		a.Live.Listen(builds.WakeChannel, a.Builds.Notified)
		a.Live.OnInvalidate("builds", a.Builds.Resync)
	}

	a.Registry = jobs.NewRegistry()
	a.registerPlatformJobs()
	notify.SetMailEnabled(cfg.SMTP.Enabled())
	if cfg.SMTP.Enabled() {
		if a.Mail, err = mail.New(mail.Config{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username,
			Password: cfg.SMTP.Password, From: cfg.SMTP.From, TLS: cfg.SMTP.TLS}); err != nil {
			pool.Close()
			return nil, err
		}
		a.Mailer = notify.NewMailer(pool, a.Mail, a.Settings, cfg.PublicURL, logger)
		a.Mailer.Visible = a.notificationSubjects()
		a.Mailer.Register(a.Registry)
		a.Issues.SetMail(a.Mail, cfg.PublicURL, a.Registry)
	}
	a.Issues.Register(a.Registry)
	a.Code.Register(a.Registry)
	a.Builds.Register(a.Registry)
	// Pages name issues by the keys they had; issues that moved keep them.
	a.Wiki.SetIssueKeys(a.Issues.VisibleIssueKeysOf)
	a.Wiki.Register(a.Registry)
	a.Wiki.SetPublicURL(cfg.PublicURL)
	a.Links = links.NewService(pool, a.Issues, a.Code, a.Identity, logger)
	a.Links.Register(a.Registry, a.Bus)
	a.Links.SetBuilds(a.Builds)
	a.Links.SetWiki(a.Wiki)
	a.Wiki.SetMacroSource(a.Links.MacroSource())
	a.Links.SetAuthz(a.Authz)
	(&links.Builds{Code: a.Code, Builds: a.Builds}).Register(a.Registry, a.Bus)
	a.Webhooks = webhooks.NewService(pool, a.Authz, a.Settings, a.Bus, a.Issues, a.Wiki, a.Code, a.Builds,
		webhooks.Options{PublicURL: cfg.PublicURL, Keyring: kr, Version: version.Version, AllowLoopback: cfg.WebhookLoopback}, logger)
	a.Webhooks.Register(a.Registry, a.Bus)
	if f := cfg.AuditForward; f.Enabled() && cfg.HasRole(config.RoleWorker) {
		sink, err := audit.NewSink(audit.SinkConfig{URL: f.URL, Authorization: f.Authorization, CAFile: f.CAFile,
			CertFile: f.CertFile, KeyFile: f.KeyFile, Hostname: cfg.PublicURL.Hostname(), UserAgent: "Kogge/" + version.Version})
		if err != nil {
			pool.Close()
			return nil, err
		}
		a.Forwarder = audit.NewForwarder(pool, audit.ForwardOptions{Sink: sink, Target: audit.TargetName(f.URL),
			Instance: cfg.PublicURL.String(), Logger: logger})
	}

	if a.Jobs, err = jobs.NewClient(pool, a.Registry, cfg.HasRole(config.RoleWorker), logger); err != nil {
		pool.Close()
		return nil, err
	}
	a.Bus.SetJobs(a.Jobs)
	a.handler = a.routes()
	return a, nil
}

// Handler returns the HTTP handler (for tests).
func (a *App) Handler() http.Handler { return a.handler }

// Routes returns the routes of the handler (for tests that check every route).
func (a *App) Routes() chi.Routes { return a.router }

// API returns the huma API (for the OpenAPI document).
func (a *App) API() huma.API { return a.api }

// Close releases resources.
func (a *App) Close() {
	if a.Code != nil {
		a.Code.Close()
	}
	if a.gate != nil {
		a.gate.Close()
	}
	a.Pool.Close()
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(metrics.HTTP)
	r.Use(a.limits(r))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/readyz", a.ready)
	r.Get("/auth/oidc/login", a.Identity.HandleOIDCLogin)
	r.Get(identity.CallbackPath, a.Identity.HandleOIDCCallback)
	r.Post(identity.BackchannelLogoutPath, a.Identity.HandleBackchannelLogout)

	a.Identity.SetTwoFactorPolicy(func(ctx context.Context) bool {
		st, err := a.Settings.Get(ctx)
		return err == nil && st.RequireTwoFactor
	})
	ui := webui.Handler(func(ctx context.Context) bool {
		st, err := a.Settings.Get(ctx)
		return err == nil && st.AllowExternalImages
	})
	r.Group(func(r chi.Router) {
		r.Use(a.Identity.Authenticate)
		a.api = api.New(r, version.Version, a.Cfg.RequestTimeout)
		a.registerAPI(a.api)
		(&issues.Handler{S: a.Issues}).Routes(r)
		(&wiki.Handler{S: a.Wiki}).Routes(r)
		(&code.Handler{S: a.Code}).Routes(r)
		(&builds.AgentHandler{S: a.Builds}).Routes(r)
		(&builds.WebHandler{S: a.Builds}).Routes(r)
		a.agents.Routes(r)
		(&importer.Redirects{Pool: a.Pool, Issues: a.Issues, Wiki: a.Wiki, Code: a.Code, Fallback: ui}).Routes(r)
		r.Get("/api/v1/live", a.serveLive)
		// The API reference is a page of the web application.
		r.Get("/api/docs", ui.ServeHTTP)
		r.HandleFunc("/api/*", func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteProblem(w, r, apperr.NotFound("not-found", "There is no API operation at %s %s.", r.Method, r.URL.Path))
		})
	})
	r.NotFound(ui.ServeHTTP)
	a.router = r
	return httpx.Base(httpx.SecurityHeaders(r, a.Cfg.SecureCookies()), a.Log, a.Cfg.TrustedProxies)
}

// registerAPI registers all JSON operations. It only uses the handlers' types at registration time, so it also
// works on an App without services (OpenAPI export).
func (a *App) registerAPI(h huma.API) {
	(&account.Handler{ID: a.Identity, Authz: a.Authz, Settings: a.Settings, Config: a.Cfg, Version: version.Version}).Register(h)
	(&admin.Handler{Pool: a.Pool, ID: a.Identity, Authz: a.Authz, Settings: a.Settings, Config: a.Cfg,
		Version: version.Version, Commit: version.Commit, BuildDate: version.BuildTime(), Started: a.Started, Mail: a.Mail,
		Figures: []admin.Figures{a.Issues.Figures, a.Wiki.Figures, a.Code.Figures, a.Builds.Figures}}).Register(h)
	(&notify.Handler{Pool: a.Pool, Mail: a.Mail != nil, Visible: a.notificationSubjects()}).Register(h)
	(&issues.Handler{S: a.Issues}).Register(h)
	(&wiki.Handler{S: a.Wiki}).Register(h)
	(&code.Handler{S: a.Code}).Register(h)
	(&builds.Handler{S: a.Builds}).Register(h)
	builds.RegisterDownloads(h, a.agents)
	(&links.Handler{S: a.Links}).Register(h)
	(&webhooks.Handler{S: a.Webhooks}).Register(h)
}

// notificationSubjects are the checks of who may still see what notifications are about.
func (a *App) notificationSubjects() notify.Visibilities {
	return notify.Visibilities{"issue": a.Issues.VisibleIssueKeys, "issue-bulk": a.Issues.VisibleBulkChanges, "page": a.Wiki.VisiblePageIDs,
		"pull-request": a.Code.VisiblePullRequestIDs, "run": a.Builds.VisibleRunIDs, "deployment": a.Builds.VisibleDeploymentIDs}
}

// OpenAPI returns the OpenAPI document without connecting to a database.
func OpenAPI() ([]byte, error) {
	h := api.New(chi.NewRouter(), version.Version, 0)
	(&App{}).registerAPI(h)
	return h.OpenAPI().MarshalJSON()
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.Pool.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := os.Stat(a.Cfg.DataDir); err != nil {
		http.Error(w, "data directory unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (a *App) serveLive(w http.ResponseWriter, r *http.Request) {
	p := identity.From(r.Context())
	if !p.Authenticated() {
		httpx.WriteProblem(w, r, apperr.Unauthorized("auth.required", "Please sign in."))
		return
	}
	a.Live.Serve(w, r, p.UserID(), p.Groups)
}

// Run starts the roles of this process and blocks until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	// Whatever ends Run (a signal or a failed listener), the goroutines it started end too, so that the pool can
	// close.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if created, err := account.EnsureBootstrapAdmin(ctx, a.Identity, a.Cfg.BootstrapAdmin); err != nil {
		return fmt.Errorf("bootstrap administrator: %w", err)
	} else if created {
		a.Log.Info("created the bootstrap administrator", "username", a.Cfg.BootstrapAdmin.Username)
	}
	// The audit log is append-only only for a role that may not change it.
	if writable, role, err := db.AuditWritable(ctx, a.Pool); err != nil {
		a.Log.Warn("could not check the database role's rights on the audit log", "error", err)
	} else if writable {
		a.Log.Warn("Kogge's database role can change and delete audit events, so the audit log is not protected against Kogge "+
			"itself: run Kogge as "+db.AppRole+" (kogge admin init-database) and the migrations with the owner's database URL",
			"role", role)
	}

	errc := make(chan error, 4)
	var servers []*http.Server
	// Every node listens for the notifications of the others: web nodes forward live messages to
	// browsers and wake waiting agents and log streams, and every node, workers too, drops its caches when another
	// changed what they hold.
	go a.Live.Run(ctx)
	if a.Cfg.HasRole(config.RoleWeb) {
		if a.Cfg.PlainHTTP() {
			a.Log.Warn("KOGGE_PUBLIC_URL is http://, so session cookies go without the Secure flag and the __Host- prefix "+
				"and browsers are not told to use HTTPS; serve Kogge over https:// (a TLS proxy, or KOGGE_TLS_CERT_FILE)",
				"public_url", a.Cfg.PublicURL.String())
		}
		// The limits of reading bodies and writing answers depend on the kind of request; the handler sets them
		// per request (limits.go), so the server sets only those of the headers and of idle connections.
		srv := &http.Server{Addr: a.Cfg.HTTPAddr, Handler: a.handler, ReadHeaderTimeout: 20 * time.Second,
			IdleTimeout: 120 * time.Second, ErrorLog: slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn)}
		servers = append(servers, srv)
		go func() {
			a.Log.Info("web server listening", "addr", a.Cfg.HTTPAddr, "public_url", a.Cfg.PublicURL.String())
			var err error
			if a.Cfg.TLSCertFile != "" {
				err = srv.ListenAndServeTLS(a.Cfg.TLSCertFile, a.Cfg.TLSKeyFile)
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("web server: %w", err)
			}
		}()
	}
	if a.Cfg.HasRole(config.RoleWeb) && a.Cfg.SSHAddr != "" {
		sshSrv, err := a.Code.NewSSHServer()
		if err != nil {
			return err
		}
		l, err := net.Listen("tcp", a.Cfg.SSHAddr)
		if err != nil {
			return fmt.Errorf("ssh server: %w", err)
		}
		go func() {
			a.Log.Info("ssh server listening", "addr", a.Cfg.SSHAddr)
			if err := sshSrv.Serve(ctx, l); err != nil {
				errc <- fmt.Errorf("ssh server: %w", err)
			}
		}()
	}
	if a.Cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler(a.Pool, version.Version, a.Cfg.AuditForward.Enabled(), func() storage.Usage {
			st, _ := a.Settings.Get(context.Background()) // the defaults when the database does not answer
			return storage.Of(st, a.Cfg.DataDir)
		}))
		srv := &http.Server{Addr: a.Cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second,
			WriteTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
		servers = append(servers, srv)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("metrics server: %w", err)
			}
		}()
	}
	if a.Cfg.HasRole(config.RoleWorker) {
		if err := a.Jobs.Start(ctx); err != nil {
			return fmt.Errorf("job worker: %w", err)
		}
		a.Log.Info("job worker started")
		// After an upgrade, the issues' field spans (KQL's WAS) are derived from their history in the background.
		go func() {
			if err := a.Issues.DeriveSpans(ctx); err != nil && ctx.Err() == nil {
				a.Log.Error("deriving issue field spans failed; WAS queries wait for them", "error", err)
			}
		}()
		a.forwarding = make(chan struct{})
		go func() {
			defer close(a.forwarding)
			if a.Forwarder != nil {
				a.Forwarder.Run(ctx)
			} else if err := audit.ForwardingOff(ctx, a.Pool); err != nil && ctx.Err() == nil {
				a.Log.Warn("could not record that audit forwarding is off", "error", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		a.Log.Error("shutting down after error", "error", err)
		cancel() // ends the live updates' streams
		a.shutdown(servers)
		return err
	}
	a.shutdown(servers)
	return nil
}

func (a *App) shutdown(servers []*http.Server) {
	a.Log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a.Builds.Stop()
	// Editors of shared drafts reconnect to another node (or to this one when it is back).
	a.Wiki.StopDrafts()
	for _, s := range servers {
		_ = s.Shutdown(ctx)
	}
	if a.Cfg.HasRole(config.RoleWorker) {
		_ = a.Jobs.Stop(ctx)
	}
	if a.forwarding != nil {
		select {
		case <-a.forwarding:
		case <-ctx.Done():
		}
	}
}

// --- platform jobs ---

// SessionCleanupArgs removes expired sessions and the rate limits that are over.
type SessionCleanupArgs struct{}

// Kind implements river.JobArgs.
func (SessionCleanupArgs) Kind() string { return "platform.session-cleanup" }

type sessionCleanupWorker struct {
	river.WorkerDefaults[SessionCleanupArgs]
	pool *pgxpool.Pool
}

func (w *sessionCleanupWorker) Work(ctx context.Context, _ *river.Job[SessionCleanupArgs]) error {
	if _, err := identity.CleanupSessions(ctx, w.pool); err != nil {
		return err
	}
	return identity.CleanupRateLimits(ctx, w.pool)
}

// DirectorySyncArgs runs the directory sync.
type DirectorySyncArgs struct{}

// Kind implements river.JobArgs.
func (DirectorySyncArgs) Kind() string { return "identity.directory-sync" }

type directorySyncWorker struct {
	river.WorkerDefaults[DirectorySyncArgs]
	id *identity.Service
}

func (w *directorySyncWorker) Timeout(*river.Job[DirectorySyncArgs]) time.Duration {
	return 30 * time.Minute
}

// Work runs the sync; a failure waits for the next run (it is recorded and shown in the administration).
func (w *directorySyncWorker) Work(ctx context.Context, _ *river.Job[DirectorySyncArgs]) error {
	_, _ = w.id.SyncDirectory(ctx, false)
	return nil
}

// AuditDigestArgs computes the audit digests of finished days.
type AuditDigestArgs struct{}

// Kind implements river.JobArgs.
func (AuditDigestArgs) Kind() string { return "platform.audit-digest" }

type auditDigestWorker struct {
	river.WorkerDefaults[AuditDigestArgs]
	app *App
}

func (w *auditDigestWorker) Work(ctx context.Context, _ *river.Job[AuditDigestArgs]) error {
	return digestAudit(ctx, w.app)
}

// BlobCollectArgs deletes unreferenced blobs.
type BlobCollectArgs struct{}

// Kind implements river.JobArgs.
func (BlobCollectArgs) Kind() string { return "platform.blob-collect" }

type blobCollectWorker struct {
	river.WorkerDefaults[BlobCollectArgs]
	blobs *blob.Store
}

func (w *blobCollectWorker) Work(ctx context.Context, _ *river.Job[BlobCollectArgs]) error {
	_, err := w.blobs.Collect(ctx, 8*24*time.Hour)
	return err
}

func (a *App) registerPlatformJobs() {
	river.AddWorker(a.Registry.Workers, &sessionCleanupWorker{pool: a.Pool})
	river.AddWorker(a.Registry.Workers, &auditDigestWorker{app: a})
	river.AddWorker(a.Registry.Workers, &blobCollectWorker{blobs: a.Blobs})
	a.Registry.Every(15*time.Minute, SessionCleanupArgs{}, nil)
	river.AddWorker(a.Registry.Workers, &directorySyncWorker{id: a.Identity})
	if a.Identity.SyncConfigured() {
		a.Registry.Every(15*time.Minute, DirectorySyncArgs{}, nil)
	}
	a.Registry.Every(time.Hour, AuditDigestArgs{}, nil)
	a.Registry.Every(6*time.Hour, BlobCollectArgs{}, nil)
	river.AddWorker(a.Registry.Workers, &retentionWorker{app: a})
	a.Registry.Every(6*time.Hour, RetentionArgs{}, nil)
}
