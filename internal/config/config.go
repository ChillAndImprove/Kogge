// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package config loads Kogge's start-up configuration from environment variables. Secrets can be given in files
// through the *_FILE variant of a variable (Docker secrets). Everything else is configured at run time and stored
// in the database.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Role is a runtime role of a Kogge process.
type Role string

const (
	RoleWeb    Role = "web"
	RoleWorker Role = "worker"
)

// Config is the start-up configuration.
type Config struct {
	PublicURL     *url.URL
	HTTPAddr      string
	SSHAddr       string // empty disables the SSH server
	SSHPublicPort int
	MetricsAddr   string // empty disables the metrics listener
	AgentDir      string // kogge-agent binaries offered for download
	DatabaseURL   string
	DataDir       string
	MasterKeyFile string
	Roles         map[Role]bool
	AutoMigrate   bool
	LocalLogin    bool
	LogLevel      slog.Level
	LogFormat     string // json or text
	GitBinary     string

	TLSCertFile    string
	TLSKeyFile     string
	TrustedProxies []netip.Prefix

	OIDC OIDC
	SMTP SMTP

	// WebhookLoopback allows webhooks to loopback addresses (development and tests only).
	WebhookLoopback bool

	SessionIdleTimeout time.Duration
	SessionMaxLifetime time.Duration

	// RequestTimeout bounds ordinary requests of the API and the web application: they must send their bodies and
	// do their work within it (their database queries are canceled then).
	RequestTimeout time.Duration
	// StallTimeout ends uploads, downloads, Git transfers and event streams that make no progress for this long.
	StallTimeout time.Duration

	// DefaultLocale is the language of a new installation (de or en, empty: German): the default for its users and
	// the language of its default configuration, set at the very first start only.
	DefaultLocale string

	BootstrapAdmin BootstrapAdmin
	AuditForward   AuditForward
}

// OIDC configures the identity provider. Issuer is the public issuer URL as it appears in tokens; InternalURL
// optionally replaces the issuer's origin for server-to-server calls (token, keys) on the internal network.
type OIDC struct {
	Issuer       string
	InternalURL  string
	ClientID     string
	ClientSecret string
	Scopes       []string
	GroupsClaim  string
	ProviderName string
	// The directory sync reads users and groups through the Keycloak admin API with a service account.
	SyncClientID     string
	SyncClientSecret string
	SyncAdminURL     string // default: the issuer with /realms/ replaced by /admin/realms/
}

// Enabled reports whether single sign-on is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" && o.ClientID != "" }

// SyncEnabled reports whether the directory sync is configured.
func (o OIDC) SyncEnabled() bool {
	return o.Enabled() && o.SyncClientSecret != "" && o.AdminURL() != ""
}

// AdminURL returns the base of the Keycloak admin API of the realm.
func (o OIDC) AdminURL() string {
	if o.SyncAdminURL != "" {
		return strings.TrimRight(o.SyncAdminURL, "/")
	}
	if i := strings.Index(o.Issuer, "/realms/"); i >= 0 {
		return o.Issuer[:i] + "/admin" + o.Issuer[i:]
	}
	return ""
}

// SMTP configures the mail relay.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      string // none, starttls, tls
}

// Enabled reports whether e-mail can be sent.
func (s SMTP) Enabled() bool { return s.Host != "" }

// AuditForward configures the forwarding of audit events to a SIEM.
type AuditForward struct {
	// URL is the target (see audit.SinkConfig); nil disables forwarding.
	URL *url.URL
	// Authorization is the value of the Authorization header of HTTP targets.
	Authorization string
	// CAFile replaces the system's certificate authorities for TLS targets; CertFile and KeyFile are a client
	// certificate.
	CAFile, CertFile, KeyFile string
}

// Enabled reports whether audit events are forwarded.
func (a AuditForward) Enabled() bool { return a.URL != nil }

// BootstrapAdmin creates a local administrator at start-up if no administrator exists yet.
type BootstrapAdmin struct {
	Username string
	Password string
	Email    string
}

// Lookup returns the value of an environment variable and whether it is set.
type Lookup func(key string) (string, bool)

// Load reads the configuration with the given lookup function (os.LookupEnv in production).
func Load(lookup Lookup) (*Config, error) {
	l := loader{lookup: lookup}
	c := &Config{
		HTTPAddr:           l.str("KOGGE_HTTP_ADDR", ":8080"),
		SSHAddr:            l.str("KOGGE_SSH_ADDR", ":2222"),
		MetricsAddr:        l.str("KOGGE_METRICS_ADDR", ""),
		AgentDir:           l.str("KOGGE_AGENT_DIR", "/usr/share/kogge/agents"),
		DatabaseURL:        l.secret("KOGGE_DATABASE_URL"),
		DataDir:            l.str("KOGGE_DATA_DIR", "./data"),
		MasterKeyFile:      l.str("KOGGE_MASTER_KEY_FILE", ""),
		AutoMigrate:        l.boolean("KOGGE_AUTO_MIGRATE", false),
		LocalLogin:         l.boolean("KOGGE_LOCAL_LOGIN", true),
		LogFormat:          l.str("KOGGE_LOG_FORMAT", "json"),
		GitBinary:          l.str("KOGGE_GIT_BINARY", "git"),
		WebhookLoopback:    l.boolean("KOGGE_WEBHOOK_ALLOW_LOOPBACK", false),
		TLSCertFile:        l.str("KOGGE_TLS_CERT_FILE", ""),
		TLSKeyFile:         l.str("KOGGE_TLS_KEY_FILE", ""),
		SessionIdleTimeout: l.duration("KOGGE_SESSION_IDLE_TIMEOUT", 2*time.Hour),
		SessionMaxLifetime: l.duration("KOGGE_SESSION_MAX_LIFETIME", 12*time.Hour),
		RequestTimeout:     l.duration("KOGGE_REQUEST_TIMEOUT", time.Minute),
		StallTimeout:       l.duration("KOGGE_STALL_TIMEOUT", time.Minute),
		DefaultLocale:      strings.ToLower(l.str("KOGGE_DEFAULT_LOCALE", "")),
		OIDC: OIDC{
			Issuer:           strings.TrimRight(l.str("KOGGE_OIDC_ISSUER", ""), "/"),
			InternalURL:      strings.TrimRight(l.str("KOGGE_OIDC_INTERNAL_URL", ""), "/"),
			ClientID:         l.str("KOGGE_OIDC_CLIENT_ID", ""),
			ClientSecret:     l.secret("KOGGE_OIDC_CLIENT_SECRET"),
			Scopes:           l.list("KOGGE_OIDC_SCOPES", []string{"openid", "profile", "email"}),
			GroupsClaim:      l.str("KOGGE_OIDC_GROUPS_CLAIM", "groups"),
			ProviderName:     l.str("KOGGE_OIDC_PROVIDER_NAME", "Single sign-on"),
			SyncClientID:     l.str("KOGGE_SYNC_CLIENT_ID", "kogge-sync"),
			SyncClientSecret: l.secret("KOGGE_SYNC_CLIENT_SECRET"),
			SyncAdminURL:     l.str("KOGGE_SYNC_ADMIN_URL", ""),
		},
		SMTP: SMTP{
			Host:     l.str("KOGGE_SMTP_HOST", ""),
			Port:     l.integer("KOGGE_SMTP_PORT", 587),
			Username: l.str("KOGGE_SMTP_USERNAME", ""),
			Password: l.secret("KOGGE_SMTP_PASSWORD"),
			From:     l.str("KOGGE_SMTP_FROM", ""),
			TLS:      l.str("KOGGE_SMTP_TLS", "starttls"),
		},
		BootstrapAdmin: BootstrapAdmin{
			Username: l.str("KOGGE_BOOTSTRAP_ADMIN_USERNAME", ""),
			Password: l.secret("KOGGE_BOOTSTRAP_ADMIN_PASSWORD"),
			Email:    l.str("KOGGE_BOOTSTRAP_ADMIN_EMAIL", ""),
		},
		AuditForward: AuditForward{
			Authorization: l.secret("KOGGE_AUDIT_FORWARD_AUTHORIZATION"),
			CAFile:        l.str("KOGGE_AUDIT_FORWARD_CA_FILE", ""),
			CertFile:      l.str("KOGGE_AUDIT_FORWARD_CERT_FILE", ""),
			KeyFile:       l.str("KOGGE_AUDIT_FORWARD_KEY_FILE", ""),
		},
	}

	public := l.str("KOGGE_PUBLIC_URL", "http://localhost:8080")
	u, err := url.Parse(strings.TrimRight(public, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail("KOGGE_PUBLIC_URL must be an absolute http(s) URL, got %q", public)
	} else {
		c.PublicURL = u
	}

	c.SSHPublicPort = l.integer("KOGGE_SSH_PUBLIC_PORT", portOf(c.SSHAddr, 2222))

	c.Roles = map[Role]bool{}
	for _, r := range l.list("KOGGE_ROLES", []string{"web", "worker"}) {
		switch Role(r) {
		case RoleWeb, RoleWorker:
			c.Roles[Role(r)] = true
		default:
			l.fail("KOGGE_ROLES: unknown role %q (known: web, worker)", r)
		}
	}

	switch strings.ToLower(l.str("KOGGE_LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		l.fail("KOGGE_LOG_LEVEL must be debug, info, warn or error")
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		l.fail("KOGGE_LOG_FORMAT must be json or text")
	}

	for _, p := range l.list("KOGGE_TRUSTED_PROXIES", nil) {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				l.fail("KOGGE_TRUSTED_PROXIES: %q is neither an address nor a network", p)
				continue
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix)
	}

	if c.DatabaseURL == "" {
		l.fail("KOGGE_DATABASE_URL is required")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		l.fail("KOGGE_TLS_CERT_FILE and KOGGE_TLS_KEY_FILE must be set together")
	}
	if c.OIDC.Issuer != "" && c.OIDC.ClientID == "" {
		l.fail("KOGGE_OIDC_CLIENT_ID is required when KOGGE_OIDC_ISSUER is set")
	}
	if c.DefaultLocale != "" && c.DefaultLocale != "de" && c.DefaultLocale != "en" {
		l.fail("KOGGE_DEFAULT_LOCALE must be de or en, got %q", c.DefaultLocale)
	}
	if c.SMTP.TLS != "none" && c.SMTP.TLS != "starttls" && c.SMTP.TLS != "tls" {
		l.fail("KOGGE_SMTP_TLS must be none, starttls or tls")
	}
	if c.SMTP.Host != "" && c.SMTP.From == "" {
		l.fail("KOGGE_SMTP_FROM is required when KOGGE_SMTP_HOST is set")
	}
	if c.BootstrapAdmin.Username != "" && len(c.BootstrapAdmin.Password) < 12 {
		l.fail("KOGGE_BOOTSTRAP_ADMIN_PASSWORD must have at least 12 characters")
	}
	if dir, err := filepath.Abs(c.DataDir); err == nil {
		c.DataDir = dir
	}
	l.auditForward(&c.AuditForward)

	if len(l.errs) > 0 {
		return nil, errors.Join(l.errs...)
	}
	return c, nil
}

// SecureCookies reports whether cookies must be marked Secure and __Host- prefixed.
func (c *Config) SecureCookies() bool { return c.PublicURL.Scheme == "https" }

// PlainHTTP reports whether the public URL is http:// for a host other than this machine: browsers then get the
// session cookie without the Secure flag and the __Host- prefix, and no HSTS.
func (c *Config) PlainHTTP() bool {
	if c.PublicURL == nil || c.PublicURL.Scheme != "http" {
		return false
	}
	host := strings.ToLower(c.PublicURL.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err != nil || !ip.IsLoopback()
}

// HasRole reports whether the process runs the role.
func (c *Config) HasRole(r Role) bool { return c.Roles[r] }

// auditForward reads KOGGE_AUDIT_FORWARD_URL and checks that the other settings fit the target.
func (l *loader) auditForward(a *AuditForward) {
	raw := strings.TrimSpace(l.secret("KOGGE_AUDIT_FORWARD_URL"))
	if raw != "" {
		u, err := forwardTarget(raw)
		if err != nil {
			l.fail("KOGGE_AUDIT_FORWARD_URL: %v", err)
		}
		a.URL = u
	}
	scheme := ""
	if a.URL != nil {
		scheme = a.URL.Scheme
	}
	tls := scheme == "tls" || scheme == "syslog+tls" || scheme == "https"
	if (a.CertFile == "") != (a.KeyFile == "") {
		l.fail("KOGGE_AUDIT_FORWARD_CERT_FILE and KOGGE_AUDIT_FORWARD_KEY_FILE must be set together")
	}
	if !tls && (a.CAFile != "" || a.CertFile != "") {
		l.fail("KOGGE_AUDIT_FORWARD_CA_FILE, _CERT_FILE and _KEY_FILE apply to tls, syslog+tls and https targets only")
	}
	if a.Authorization != "" && scheme != "http" && scheme != "https" {
		l.fail("KOGGE_AUDIT_FORWARD_AUTHORIZATION applies to http and https targets only")
	}
}

// forwardTarget parses the target of the audit forwarding. Errors never repeat the value: it may hold credentials.
func forwardTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a URL")
	}
	network := false
	switch u.Scheme {
	case "file":
		if (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
			return nil, errors.New("a file needs an absolute path, e.g. file:///var/log/kogge/audit.jsonl")
		}
		return &url.URL{Scheme: "file", Path: filepath.Clean(u.Path)}, nil
	case "tcp", "tls":
		if u.Port() == "" {
			return nil, fmt.Errorf("%s targets need a port, e.g. %s://siem.example.com:5170", u.Scheme, u.Scheme)
		}
		network = true
	case "syslog+udp", "syslog+tcp", "syslog+tls":
		network = true
	case "http", "https":
	default:
		return nil, errors.New("the target must be a file://, tcp://, tls://, syslog+udp://, syslog+tcp://, syslog+tls://, " +
			"http:// or https:// URL")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%s targets need a host", u.Scheme)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errors.New("the port must be a number from 1 to 65535")
		}
	}
	if network && (u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "") {
		return nil, fmt.Errorf("%s targets are a host and a port only", u.Scheme)
	}
	return u, nil
}

func portOf(addr string, fallback int) int {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil {
			return p
		}
	}
	return fallback
}

type loader struct {
	lookup Lookup
	errs   []error
}

func (l *loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

func (l *loader) str(key, fallback string) string {
	if v, ok := l.lookup(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

// secret reads KEY, or the file named by KEY_FILE.
func (l *loader) secret(key string) string {
	if path, ok := l.lookup(key + "_FILE"); ok && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			l.fail("%s_FILE: %v", key, err)
			return ""
		}
		return strings.TrimRight(string(b), "\r\n")
	}
	v, _ := l.lookup(key)
	return v
}

func (l *loader) boolean(key string, fallback bool) bool {
	v, ok := l.lookup(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail("%s must be true or false, got %q", key, v)
		return fallback
	}
	return b
}

func (l *loader) integer(key string, fallback int) int {
	v, ok := l.lookup(key)
	if !ok || v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		l.fail("%s must be a number, got %q", key, v)
		return fallback
	}
	return i
}

func (l *loader) duration(key string, fallback time.Duration) time.Duration {
	v, ok := l.lookup(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail("%s must be a positive duration such as 2h, got %q", key, v)
		return fallback
	}
	return d
}

func (l *loader) list(key string, fallback []string) []string {
	v, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
